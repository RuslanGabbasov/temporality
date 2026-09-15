import { type FormEvent, type ReactNode, useCallback, useEffect, useReducer, useRef, useState } from 'react'
import { api, API_BASE } from './api'
import { extractClaims } from './answer'
import { assistantMessage, conversationReducer, initialConversationState, userMessage } from './conversation'
import { eventKey, eventLabel, eventTime, executionIdOf, frameIdOf, newestFirst, unwrapEvents } from './timeline'
import { ATTENTION_HINT_TYPES, describeEvent, isAttentionHint } from './eventSummary'
import { formatDuration, isAbortError, modelProgress, modelTimeoutMs } from './modelProgress'
import type { Execution, ForkGroup, Frame, FrameSection, FrpEvent, ModelConfig, ModelStepResponse, RenderPacket, TokenUsage } from './types'
import { renderSection, tokenUsageView } from './tokenUsage'
import { childFrameId, continueWithModel, EpisodeWorkflowError, FollowUpWorkflowError, renderRequest, runEpisodeWorkflow, runFollowUpWorkflow, twoBranchForkRequest, type EpisodeDraft, type EpisodeWorkflowResult, type FollowUpResult, type FollowUpStage, type WorkflowStage } from './workflow'

const SECTIONS: FrameSection[] = ['focus', 'map', 'periphery', 'working_set', 'procedures', 'recent']
const DEFAULT_DRAFT: EpisodeDraft = { prompt: '', successConditions: [], tokenBudget: 4000, mode: 'explore', trustMin: 0.5, rebuildRegions: false }

function JsonView({ value, empty = 'No data' }: { value: unknown; empty?: string }) {
  if (value === undefined || value === null) return <p className="muted empty">{empty}</p>
  return <pre>{typeof value === 'string' ? value : JSON.stringify(value, null, 2)}</pre>
}
function TokenUsageCard({ title, usage, renderId }: { title: string; usage?: TokenUsage; renderId?: string }) {
  const view = tokenUsageView(usage)
  const hasUsage = view.estimated !== undefined || view.budget !== undefined
  return <article className={`data-card token-card ${view.warning ? 'warning' : ''}`}><h3><span>◴</span>{title}</h3>{renderId && <code className="render-id">render {renderId}</code>}{!hasUsage ? <p className="muted empty">No token usage reported</p> : <><strong className="token-total">{view.estimated ?? '—'} / {view.budget ?? '—'}</strong><span className="token-percent">{view.percent === undefined ? 'Percentage unavailable' : `${Math.round(view.percent)}%`}</span><div className="token-track" role="progressbar" aria-label={title} aria-valuemin={0} aria-valuemax={100} aria-valuenow={view.percent === undefined ? undefined : Math.min(100, Math.round(view.percent))}><span style={{ width: `${Math.min(100, view.percent ?? 0)}%` }} /></div><small>{view.remaining === undefined ? 'Remaining unavailable' : `${view.remaining} remaining`}{view.warning && ' · Warning: over 85%'}</small></>}</article>
}
function ModelResultDetails({ response }: { response: ModelStepResponse }) {
  const claims = extractClaims(response)
  const protocol = { render_packet: response.render_packet, emission: response.emission, step: response.step }
  return <section className="model-result" aria-label="Latest model result details">
    <article className="claims-panel"><h3>Claims <span>{claims.length}</span></h3>{claims.length ? <ul>{claims.map((claim, index) => <li key={`${claim.proposition}-${index}`}><span>{claim.proposition}</span><small>{claim.confidence !== undefined && <data value={claim.confidence}>confidence {Math.round(claim.confidence * 100)}%</data>}{claim.status && <span className="claim-status">{claim.status}</span>}</small></li>)}</ul> : <p className="muted">No claims emitted.</p>}</article>
    {response.render_packet && <div className="model-input-usage"><TokenUsageCard title="Model input usage" usage={response.render_packet.token_usage} renderId={response.render_packet.render_id} /></div>}
    <details className="protocol-details"><summary>Protocol details: RenderPacket / Emission / Step</summary><JsonView value={protocol} /></details>
    {(response.model_provenance !== undefined || response.debugger_summary !== undefined) && <div className="model-meta">{response.model_provenance !== undefined && <article><h3>Model provenance</h3><JsonView value={response.model_provenance} /></article>}{response.debugger_summary !== undefined && <article><h3>Debugger summary</h3><JsonView value={response.debugger_summary} /></article>}</div>}
  </section>
}
function Status({ loading, error, children, keep }: { loading: boolean; error?: string; children: ReactNode; keep?: boolean }) {
  // `keep` preserves mounted children (e.g. the scrolling event list) while a
  // background refresh runs or a transient error occurs — unmounting the list
  // would collapse the scroll container and throw the reader back to the top.
  if (keep && (loading || error)) {
    return <><div className={error ? 'status error' : 'status'} role={error ? 'alert' : 'status'}>{error ?? <><span className="spinner" /> Refreshing…</>}</div>{children}</>
  }
  if (loading) return <div className="status" role="status"><span className="spinner" /> Loading…</div>
  if (error) return <div className="status error" role="alert">{error}</div>
  return <>{children}</>
}
function errorText(error: unknown, fallback: string) { return error instanceof Error ? error.message : fallback }
function frameBudget(value: Frame | null) {
  const budget = value?.budget
  return typeof budget === 'object' && budget && 'tokens' in budget && typeof budget.tokens === 'number' ? budget.tokens : 4000
}

function App() {
  const [episodeInput, setEpisodeInput] = useState(''); const [episodeId, setEpisodeId] = useState(''); const [limit, setLimit] = useState(100)
  const [events, setEvents] = useState<FrpEvent[]>([]); const [eventsBusy, setEventsBusy] = useState(false); const [eventsError, setEventsError] = useState(''); const [hideHints, setHideHints] = useState(true)
  const [frame, setFrame] = useState<Frame | null>(null); const [frameBusy, setFrameBusy] = useState(false); const [frameError, setFrameError] = useState(''); const [selectedFrameId, setSelectedFrameId] = useState('')
  const [execution, setExecution] = useState<Execution | null>(null); const [executionBusy, setExecutionBusy] = useState(false); const [executionError, setExecutionError] = useState('')
  const [selectedRender, setSelectedRender] = useState<RenderPacket | null>(null); const [renderBusy, setRenderBusy] = useState(false); const [renderError, setRenderError] = useState(''); const [actionBusy, setActionBusy] = useState(''); const [actionError, setActionError] = useState(''); const [replay, setReplay] = useState<unknown>(null)
  const [blameRootId, setBlameRootId] = useState(''); const [blameMaxDepth, setBlameMaxDepth] = useState(8); const [blame, setBlame] = useState<unknown>(null); const [forkGroup, setForkGroup] = useState<ForkGroup | null>(null)
  const [modelConfig, setModelConfig] = useState<ModelConfig | null>(null); const [configBusy, setConfigBusy] = useState(false); const [configError, setConfigError] = useState('')
  const [dialogOpen, setDialogOpen] = useState(false); const [draft, setDraft] = useState(DEFAULT_DRAFT); const [conditionsText, setConditionsText] = useState('')
  const [workflowBusy, setWorkflowBusy] = useState(false); const [workflowStage, setWorkflowStage] = useState<WorkflowStage | ''>(''); const [workflowError, setWorkflowError] = useState(''); const [pending, setPending] = useState<EpisodeWorkflowResult | null>(null); const [summary, setSummary] = useState<ModelStepResponse | null>(null)
  const [conversation, dispatchConversation] = useReducer(conversationReducer, initialConversationState)
  const [modelRun, setModelRun] = useState<{ controller: AbortController; startedAt: number; now: number; operation: string } | null>(null)
  const [followUpOpen, setFollowUpOpen] = useState(false); const [followUpText, setFollowUpText] = useState(''); const [followUpStage, setFollowUpStage] = useState<FollowUpStage | ''>(''); const [savedFollowUp, setSavedFollowUp] = useState<FollowUpResult | null>(null)
  const dialogRef = useRef<HTMLDialogElement>(null)
  const followUpDialogRef = useRef<HTMLDialogElement>(null)
  const latestAssistantRef = useRef<HTMLElement>(null)
  const followUpTurnIdRef = useRef('')
  const renderControllerRef = useRef<AbortController | null>(null)
  const renderGenerationRef = useRef(0)

  const loadEvents = useCallback(async (id: string, options?: { silent?: boolean }) => {
    if (!id.trim()) return []
    // Silent mode is for background polling: it must not toggle the busy flag,
    // because Status would otherwise unmount the list and reset its scroll.
    const silent = options?.silent === true
    if (!silent) setEventsBusy(true)
    setEventsError('')
    try { const loaded = unwrapEvents(await api.events(id.trim(), limit)); setEvents(loaded); return loaded }
    catch (error) { setEventsError(errorText(error, 'Unable to load timeline')); return [] }
    finally { if (!silent) setEventsBusy(false) }
  }, [limit])
  const refreshConfig = useCallback(async () => {
    setConfigBusy(true); setConfigError('')
    try { setModelConfig(await api.modelConfig()) } catch (error) { setConfigError(errorText(error, 'Unable to load model config')) } finally { setConfigBusy(false) }
  }, [])
  useEffect(() => { void refreshConfig() }, [refreshConfig])
  useEffect(() => { if (!episodeId || modelRun) return; const timer = window.setInterval(() => void loadEvents(episodeId, { silent: true }), 5000); return () => window.clearInterval(timer) }, [episodeId, loadEvents, modelRun])
  useEffect(() => { if (!modelRun) return; const timer = window.setInterval(() => setModelRun((run) => run ? { ...run, now: Date.now() } : null), 1000); return () => window.clearInterval(timer) }, [modelRun?.startedAt])
  useEffect(() => { if (dialogOpen) dialogRef.current?.showModal(); else dialogRef.current?.close() }, [dialogOpen])
  useEffect(() => { if (followUpOpen) followUpDialogRef.current?.showModal(); else followUpDialogRef.current?.close() }, [followUpOpen])
  useEffect(() => () => { renderGenerationRef.current += 1; renderControllerRef.current?.abort() }, [])
  useEffect(() => {
    if (!conversation.messages.length || conversation.messages.at(-1)?.role !== 'assistant') return
    const frame = window.requestAnimationFrame(() => {
      latestAssistantRef.current?.focus({ preventScroll: true })
      latestAssistantRef.current?.scrollIntoView({ behavior: 'smooth', block: 'nearest' })
    })
    return () => window.cancelAnimationFrame(frame)
  }, [conversation.messages])

  async function loadSelectedRender(value: Frame, fallbackId: string) {
    const request = renderRequest(value, fallbackId)
    if (!request) { setSelectedRender(null); setRenderError('Frame does not include an objective ID.'); return }
    renderControllerRef.current?.abort()
    const controller = new AbortController(); const generation = ++renderGenerationRef.current
    renderControllerRef.current = controller; setRenderBusy(true); setRenderError('')
    try {
      const packet = await api.render(request, controller.signal)
      if (generation === renderGenerationRef.current && !controller.signal.aborted) setSelectedRender(packet)
    } catch (error) {
      if (generation === renderGenerationRef.current && !isAbortError(error)) setRenderError(errorText(error, 'Unable to render context'))
    } finally {
      if (generation === renderGenerationRef.current) { setRenderBusy(false); renderControllerRef.current = null }
    }
  }
  async function openFrame(id: string) {
    renderControllerRef.current?.abort(); const generation = ++renderGenerationRef.current
    setSelectedFrameId(id); setFrameBusy(true); setFrameError(''); setFrame(null); setSelectedRender(null); setRenderBusy(false); setRenderError(''); setReplay(null); setBlame(null); setForkGroup(null); setActionError('')
    try {
      const loaded = await api.frame(id)
      if (generation !== renderGenerationRef.current) return
      setFrame(loaded); setFrameBusy(false)
      await loadSelectedRender(loaded, id)
    } catch (error) {
      if (generation === renderGenerationRef.current) setFrameError(errorText(error, 'Unable to load frame'))
    } finally { if (generation === renderGenerationRef.current) setFrameBusy(false) }
  }
  async function openExecution(id: string) { setExecutionBusy(true); setExecutionError(''); setExecution(null); try { setExecution(await api.execution(id)) } catch (error) { setExecutionError(errorText(error, 'Unable to load execution')) } finally { setExecutionBusy(false) } }
  async function runAction(name: string, operation: () => Promise<void>) { setActionBusy(name); setActionError(''); try { await operation() } catch (error) { setActionError(errorText(error, `Unable to ${name}`)) } finally { setActionBusy('') } }

  const objectiveId = frame?.objective_id; const frameId = frame?.frame_id ?? frame?.id ?? selectedFrameId
  function submitEpisode(event: FormEvent) { event.preventDefault(); const id = episodeInput.trim(); setEpisodeId(id); setSummary(null); dispatchConversation({ type: 'switchEpisode' }); void loadEvents(id) }
  function submitBlame(event: FormEvent) { event.preventDefault(); const rootId = blameRootId.trim(); if (rootId) void runAction('blame', async () => setBlame(await api.blame({ root_id: rootId, max_depth: blameMaxDepth }))) }
  function fork() { if (!frameId) return; void runAction('fork', async () => setForkGroup(await api.fork(twoBranchForkRequest(frameId)))) }
  function renderSelected() { if (frame) void loadSelectedRender(frame, selectedFrameId) }

  function withDuration(result: ModelStepResponse, startedAt: number): ModelStepResponse {
    const durationMs = Date.now() - startedAt
    return { ...result, debugger_summary: { duration_ms: durationMs, duration: formatDuration(durationMs) } }
  }
  async function finishModel(result: EpisodeWorkflowResult, modelResult?: ModelStepResponse) {
    const response = modelResult ?? result.modelResult
    setPending(null); setEpisodeId(result.ids.episodeId); setEpisodeInput(result.ids.episodeId)
    if (response) { setSummary(response); dispatchConversation({ type: 'appendAssistant', message: assistantMessage('initial:assistant', response, childFrameId(response)) }) }
    await loadEvents(result.ids.episodeId)
    const child = modelResult ? childFrameId(modelResult) : result.childFrameId
    await openFrame(child ?? result.parentFrameId)
  }
  async function createEpisode(event: FormEvent) {
    event.preventDefault(); if (workflowBusy || !draft.prompt.trim()) return
    setWorkflowBusy(true); setWorkflowError(''); setSummary(null); dispatchConversation({ type: 'start', user: userMessage('initial:user', draft.prompt) })
    const controller = new AbortController(); let modelStartedAt = 0
    try {
      const result = await runEpisodeWorkflow(api, { ...draft, successConditions: conditionsText.split('\n').map((value) => value.trim()).filter(Boolean) }, modelConfig ?? { configured: false }, (stage) => { setWorkflowStage(stage); if (stage === 'Calling model') { modelStartedAt = Date.now(); setModelRun({ controller, startedAt: modelStartedAt, now: modelStartedAt, operation: 'New Episode' }) } }, undefined, controller.signal)
      if (result.modelResult && modelStartedAt) result.modelResult = withDuration(result.modelResult, modelStartedAt)
      setDialogOpen(false); setEpisodeId(result.ids.episodeId); setEpisodeInput(result.ids.episodeId); await loadEvents(result.ids.episodeId); await openFrame(result.parentFrameId)
      if (result.needsModel) { setPending(result); setWorkflowError('Objective and Frame were created. Configure TEMPORALITY_MODEL_BASE_URL and TEMPORALITY_MODEL_ID, refresh model status, then retry.') } else await finishModel(result)
    } catch (error) {
      if (error instanceof EpisodeWorkflowError) { setPending(error.result); setEpisodeId(error.result.ids.episodeId); setEpisodeInput(error.result.ids.episodeId); setDialogOpen(false); await loadEvents(error.result.ids.episodeId); await openFrame(error.result.parentFrameId) }
      setWorkflowError(isAbortError(error instanceof EpisodeWorkflowError ? error.cause : error) ? 'Model step cancelled. Episode and frame IDs are retained and ready to retry.' : errorText(error, 'Unable to create episode'))
    } finally { setWorkflowBusy(false); setWorkflowStage(''); setModelRun(null) }
  }
  async function retryPending() {
    if (!pending || workflowBusy) return
    if (!modelConfig?.configured) { setWorkflowError('Model is still unconfigured. Set the runtime model environment, then refresh model status.'); return }
    setWorkflowBusy(true); setWorkflowStage('Calling model'); setWorkflowError('')
    const controller = new AbortController(); const startedAt = Date.now(); setModelRun({ controller, startedAt, now: startedAt, operation: 'Retry' })
    try { const result = await continueWithModel(api, pending.parentFrameId, pending.ids.objectiveId, draft.tokenBudget, controller.signal); await finishModel(pending, withDuration(result, startedAt)) } catch (error) { setWorkflowError(isAbortError(error) ? 'Model step cancelled. Episode and frame IDs are retained and ready to retry.' : errorText(error, 'Model step failed; IDs are retained for retry.')) } finally { setWorkflowBusy(false); setWorkflowStage(''); setModelRun(null) }
  }
  function openNewEpisode() {
    setSummary(null); setPending(null); setWorkflowError(''); dispatchConversation({ type: 'switchEpisode' }); setDialogOpen(true)
  }
  function continueSelected() {
    if (!frameId || !objectiveId || actionBusy) return
    setFollowUpText(''); setSavedFollowUp(null); setActionError(''); followUpTurnIdRef.current = ''; setFollowUpOpen(true)
  }
  async function submitFollowUp(event?: FormEvent) {
    event?.preventDefault()
    if (!frameId || !objectiveId || actionBusy || !followUpText.trim()) return
    const input = { sourceFrameId: frameId, text: followUpText, objectiveId, budgetTokens: frameBudget(frame) }
    const turnId = followUpTurnIdRef.current || `follow-up:${frameId}:${Date.now()}`
    followUpTurnIdRef.current = turnId
    dispatchConversation({ type: 'ensureUser', message: userMessage(`${turnId}:user`, followUpText, frameId) })
    const controller = new AbortController(); let startedAt = 0
    setActionBusy(savedFollowUp ? 'Calling model' : 'Saving follow-up'); setActionError('')
    try {
      const result = await runFollowUpWorkflow(api, input, (stage) => {
        setFollowUpStage(stage); setActionBusy(stage)
        if (stage === 'Calling model') { startedAt = Date.now(); setModelRun({ controller, startedAt, now: startedAt, operation: savedFollowUp ? 'Retry follow-up' : 'Continue' }) }
      }, savedFollowUp ?? undefined, controller.signal)
      setSavedFollowUp(null); setFollowUpOpen(false)
      if (result.modelResult) {
        const response = withDuration(result.modelResult, startedAt)
        setSummary(response)
        dispatchConversation({ type: 'appendAssistant', message: assistantMessage(`${turnId}:assistant`, response, childFrameId(response)) })
        followUpTurnIdRef.current = ''
      }
      if (episodeId) await loadEvents(episodeId)
      const child = result.modelResult && childFrameId(result.modelResult)
      await openFrame(child ?? result.instructionFrameId)
    } catch (error) {
      const workflowError = error instanceof FollowUpWorkflowError ? error : undefined
      if (workflowError?.result) {
        setSavedFollowUp(workflowError.result); setFollowUpOpen(false)
        if (episodeId) await loadEvents(episodeId)
        await openFrame(workflowError.result.instructionFrameId)
        setActionError(isAbortError(workflowError.cause) ? 'Follow-up saved. Model step cancelled; retry runs the model only.' : `Follow-up saved. ${errorText(workflowError, 'Model step failed')}`)
      } else setActionError(errorText(error, 'Unable to save follow-up'))
    } finally { setActionBusy(''); setFollowUpStage(''); setModelRun(null) }
  }
  function retryFollowUp() { if (savedFollowUp) { setFollowUpText(savedFollowUp.text); setFollowUpOpen(true) } }

  const provenance = modelConfig?.provenance; const modelName = provenance?.model ?? modelConfig?.model; const baseUrl = provenance?.base_url ?? modelConfig?.base_url; const maxOutputTokens = provenance?.max_output_tokens ?? modelConfig?.max_output_tokens
  const provider = provenance?.provider ?? modelConfig?.provider ?? (baseUrl ? (() => { try { return new URL(baseUrl).host } catch { return baseUrl } })() : 'provider')
  const progress = modelRun ? modelProgress(modelRun.startedAt, modelRun.now, modelTimeoutMs(modelConfig)) : null
  return <div className="app-shell">
    <header className="topbar"><div><span className="eyebrow">TEMPORALITY / FRP</span><h1>Cognitive Debugger</h1></div><div className="header-actions"><div className={`model-badge ${modelConfig?.configured ? 'ready' : 'offline'}`} title={configError || undefined}><span className="pulse" />{configBusy ? 'Checking model…' : modelConfig?.configured ? `Configured · ${modelName ?? 'model'} · max ${maxOutputTokens ?? '—'} output tokens · ${baseUrl ?? 'base URL set'}` : 'Model not configured'}</div><button className="icon-button" onClick={() => void refreshConfig()} disabled={configBusy} aria-label="Refresh model configuration">↻</button><span className="connection">API <code>{API_BASE}</code></span><button className="primary" onClick={openNewEpisode}>＋ New Episode</button></div></header>

    {modelRun && progress && <section className="model-progress" role="status" aria-live="polite"><div className="model-activity" aria-hidden="true"><span /><span /><span /></div><div className="model-progress-copy"><span className="eyebrow">{modelRun.operation.toUpperCase()} · CALLING MODEL</span><h2>{modelName ?? 'Configured model'} <small>via {provider}</small></h2><p>The provider is processing this frame with a maximum of {maxOutputTokens ?? '—'} output tokens. The result commits atomically only when the model step completes.</p></div><div className="model-timing"><strong>{progress.elapsed}</strong><span>elapsed</span>{progress.timeout !== undefined && <small>{progress.timedOut ? 'Configured timeout reached' : `timeout in ${progress.timeout}`}</small>}</div><button className="cancel-model" onClick={() => modelRun.controller.abort()}>Cancel model step</button></section>}
    <main className="workspace">
      <aside className="timeline panel" aria-label="Episode timeline"><div className="panel-heading"><div><span className="eyebrow">EPISODE</span><h2>Timeline</h2></div><div className="timeline-actions">{(() => { const hidden = events.filter((item) => isAttentionHint(item)).length; return <label className="hint-toggle" title={`Attention bookkeeping: ${ATTENTION_HINT_TYPES.join(', ')}`}><input type="checkbox" checked={hideHints} onChange={(e) => setHideHints(e.target.checked)} />attention hints{hideHints && hidden > 0 ? ` · ${hidden} hidden` : ''}</label> })()}{episodeId && <button className="icon-button" onClick={() => void loadEvents(episodeId)} aria-label="Refresh timeline">↻</button>}</div></div>
        <form className="episode-form" onSubmit={submitEpisode}><label htmlFor="episode">Episode ID</label><div className="input-row"><input id="episode" value={episodeInput} onChange={(e) => setEpisodeInput(e.target.value)} placeholder="UUID" required /><button type="submit">Open</button></div><label htmlFor="limit">Event limit <output>{limit}</output></label><input id="limit" type="range" min="10" max="500" step="10" value={limit} onChange={(e) => setLimit(Number(e.target.value))} /></form>
        {(workflowError || pending) && <div className="status warning" role="status"><strong>{workflowError || 'Model step pending.'}</strong>{pending && <><small>Episode <code>{pending.ids.episodeId}</code> and frame <code>{pending.parentFrameId}</code> are safe.</small><button onClick={() => void retryPending()} disabled={workflowBusy || !modelConfig?.configured}>{workflowBusy ? workflowStage : 'Retry model step'}</button></>}</div>}
        :        <Status keep loading={eventsBusy} error={eventsError}><ol className="event-list">{newestFirst(events).filter((item) => !hideHints || !isAttentionHint(item)).map((item, index) => { const fid = frameIdOf(item); const xid = executionIdOf(item); const summary = describeEvent(item); return <li key={eventKey(item, index)} className={fid === selectedFrameId ? 'selected' : ''}><div className="event-rail"><span className="event-dot" /><span /></div><div className="event-card"><span className="event-time">{eventTime(item) ? new Date(eventTime(item)).toLocaleString() : `#${index + 1}`}</span><strong>{eventLabel(item)}</strong><span className="event-summary">{summary.title}</span>{summary.detail && <span className="event-detail">{summary.detail}</span>}{summary.meta && <span className="event-meta">{summary.meta}</span>}<details className="event-raw"><summary>payload</summary><JsonView value={item.payload} empty="No payload" /></details><div className="event-links">{fid && <button onClick={() => void openFrame(fid)}>Frame {fid}</button>}{xid && <button onClick={() => void openExecution(xid)}>Execution {xid}</button>}</div></div></li> })}{!events.length && !eventsBusy && <li className="empty-state"><strong>Start an operational trace</strong><span>Create a new task with New Episode, or paste an existing <code>episode_id</code> above.</span></li>}</ol></Status>
      </aside>

      <section className="inspector panel" aria-label="Frame inspector"><div className="panel-heading inspector-heading"><div><span className="eyebrow">SELECTED FRAME</span><h2>{selectedFrameId || 'No frame selected'}</h2></div>{objectiveId && <div className="objective"><span>Objective</span><code>{objectiveId}</code></div>}</div>
        <section className="conversation" aria-labelledby="conversation-title"><div className="conversation-heading"><div><span className="eyebrow">CONVERSATION</span><h2 id="conversation-title">Model conversation</h2></div>{conversation.messages.length > 0 && <button onClick={() => { setSummary(null); dispatchConversation({ type: 'switchEpisode' }) }}>Clear</button>}</div>{conversation.messages.map((message, index) => { const latestAssistant = message.role === 'assistant' && index === conversation.messages.length - 1; return <article key={message.id} className={`conversation-message ${message.role}`} ref={latestAssistant ? latestAssistantRef : undefined} tabIndex={latestAssistant ? -1 : undefined}><header><strong>{message.role === 'user' ? 'You' : message.label ?? 'Assistant'}</strong>{message.duration && <span>{message.duration}</span>}</header><div className="conversation-text">{message.text}</div>{message.frameId && <small>Frame <code>{message.frameId}</code></small>}{latestAssistant && summary && <ModelResultDetails response={summary} />}</article> })}{!conversation.messages.length && <p className="conversation-empty">{conversation.unavailableHistory && episodeId ? 'Conversation history is unavailable for existing episodes.' : 'No conversation yet. Start a New Episode to talk to the model.'}</p>}</section>
        <Status loading={frameBusy} error={frameError}>{!frame ? <div className="hero-empty"><div className="frame-glyph">⌗</div><h3>Create a task or select a frame</h3><p>Run cognition with a configured model, or inspect an existing episode’s boundaries, evidence, token use, and provenance.</p><button className="primary" onClick={openNewEpisode}>New Episode</button></div> : <><div className="actionbar" aria-label="Frame actions"><button onClick={continueSelected} disabled={!!actionBusy || !modelConfig?.configured}>Continue with model</button><button onClick={renderSelected} disabled={renderBusy || !objectiveId} title="Render the selected Frame using its objective and token budget (default 4000)">{renderBusy ? 'Rendering context…' : 'Render selected frame'}</button><button onClick={() => void runAction('replay', async () => setReplay(await api.replay(frameId)))} disabled={!!actionBusy}>↶ Replay from here</button><button onClick={fork} disabled={!!actionBusy}>⑂ Fork ×2</button>{actionBusy && <span className="muted" role="status"><span className="spinner" /> {actionBusy}…</span>}</div>{actionError && <div className={savedFollowUp ? 'status warning' : 'status error'} role="alert"><strong>{actionError}</strong>{savedFollowUp && <><small>Instruction Frame <code>{savedFollowUp.instructionFrameId}</code> is the retry source. Your follow-up text is preserved.</small><button onClick={retryFollowUp} disabled={!!actionBusy || !modelConfig?.configured}>Retry model only</button></>}</div>}

          <div className="frame-meta"><article className="data-card"><h3>Frame identity / mode / revision</h3><JsonView value={{ identity: frame.identity ?? { frame_id: frameId, objective_id: frame.objective_id, episode_id: frame.episode_id }, mode: frame.mode, revision: frame.revision }} /></article></div>
          {renderBusy && <div className="render-status" role="status"><span className="spinner" /> Rendering context…</div>}{renderError && <div className="render-status error" role="alert"><span>{renderError}</span><button onClick={renderSelected}>Retry Render</button></div>}
          <div className="section-grid">{SECTIONS.map((section) => <article className={`data-card ${section === 'focus' ? 'featured' : ''}`} key={section}><h3><span>{section === 'focus' ? '◎' : '◇'}</span>{section}</h3><JsonView value={renderSection(selectedRender, section)?.items} empty={renderBusy ? 'Rendering context…' : 'No rendered data'} /></article>)}</div><div className="evidence-grid"><article className="data-card warning"><h3><span>↗</span>outside_frame</h3><JsonView value={selectedRender?.outside_frame} empty={renderBusy ? 'Rendering context…' : 'No rendered data'} /></article><article className="data-card"><h3><span>⌁</span>provenance</h3><JsonView value={selectedRender?.provenance} empty={renderBusy ? 'Rendering context…' : 'No rendered data'} /></article><TokenUsageCard title="Selected frame usage" usage={selectedRender?.token_usage} renderId={selectedRender?.render_id} /></div>
          {replay !== null && <div className="result-grid"><article className="result-card"><h3>Replay result</h3><JsonView value={replay} /></article></div>}
          <article className="tool-card"><div><span className="eyebrow">CAUSAL REFERENCE</span><h3>Blame analysis</h3><p>Enter the exact Claim, Event, or Frame reference ID to trace—not a natural-language question.</p></div><form onSubmit={submitBlame}><label>Claim / Event / Frame reference ID<input value={blameRootId} onChange={(e) => setBlameRootId(e.target.value)} placeholder="Reference ID" aria-label="Blame root reference ID" required /></label><label>Maximum traversal depth<input type="number" min="1" step="1" value={blameMaxDepth} onChange={(e) => setBlameMaxDepth(Number(e.target.value))} aria-label="Blame maximum depth" required /></label><button disabled={!!actionBusy}>Trace reference</button></form>{blame !== null && <JsonView value={blame} />}</article>{forkGroup && <article className="tool-card"><span className="eyebrow">COUNTERFACTUAL GROUP</span><h3>Forked branches</h3><p>Fork group <code>{forkGroup.fork_group_id}</code></p><div className="branch-grid">{forkGroup.branches.map((branch) => <div className="branch" key={branch.branch_id}><strong>{branch.label ?? 'Branch'}</strong><code>{branch.branch_id}</code><JsonView value={branch} /></div>)}</div></article>}</>}</Status>
      </section>
      <aside className={`execution-drawer panel ${execution || executionBusy || executionError ? 'open' : ''}`} aria-label="Execution details"><div className="panel-heading"><div><span className="eyebrow">EXECUTION</span><h2>Details</h2></div><button className="icon-button" onClick={() => { setExecution(null); setExecutionError('') }} aria-label="Close execution details">×</button></div><Status loading={executionBusy} error={executionError}><JsonView value={execution} /></Status></aside>
    </main>

    <dialog ref={followUpDialogRef} onCancel={(event) => { if (actionBusy) event.preventDefault(); else setFollowUpOpen(false) }} aria-labelledby="follow-up-title"><form className="episode-dialog" onSubmit={(event) => void submitFollowUp(event)}><div className="dialog-heading"><div><span className="eyebrow">MODEL FOLLOW-UP</span><h2 id="follow-up-title">Continue with model</h2></div><button type="button" className="icon-button" onClick={() => setFollowUpOpen(false)} disabled={!!actionBusy} aria-label="Close follow-up">×</button></div><div className="follow-up-context"><div><span>Current Objective</span><code>{savedFollowUp?.objectiveId ?? objectiveId}</code></div><div><span>{savedFollowUp ? 'Instruction Frame' : 'Selected Frame'}</span><code>{savedFollowUp?.instructionFrameId ?? frameId}</code></div></div>{conversation.messages.length > 0 && <details className="recent-conversation"><summary>Recent conversation</summary>{conversation.messages.slice(-4).map((message) => <p key={message.id}><strong>{message.role === 'user' ? 'You' : 'Assistant'}:</strong> {message.text}</p>)}</details>}<label className="follow-up-field">Follow-up <span aria-hidden="true">*</span><textarea autoFocus required value={followUpText} onChange={(event) => setFollowUpText(event.target.value)} onKeyDown={(event) => { if (event.key === 'Enter' && (event.metaKey || event.ctrlKey)) { event.preventDefault(); event.currentTarget.form?.requestSubmit() } }} placeholder="What should the model attend to next?" /></label>{actionBusy && <div className="status" role="status"><span className="spinner" /> {followUpStage}…</div>}<div className="dialog-actions"><button type="button" onClick={() => setFollowUpOpen(false)} disabled={!!actionBusy}>Cancel</button><button className="primary" type="submit" disabled={!!actionBusy || !followUpText.trim()}>{savedFollowUp ? 'Retry model' : 'Send & continue'}</button></div></form></dialog>

    <dialog ref={dialogRef} onCancel={() => !workflowBusy && setDialogOpen(false)} aria-labelledby="new-episode-title"><form className="episode-dialog" onSubmit={createEpisode}><div className="dialog-heading"><div><span className="eyebrow">OPERATIONAL WORKFLOW</span><h2 id="new-episode-title">New Episode</h2></div><button type="button" className="icon-button" onClick={() => setDialogOpen(false)} disabled={workflowBusy} aria-label="Close">×</button></div><div className="form-grid"><label className="wide">Prompt <span aria-hidden="true">*</span><textarea autoFocus value={draft.prompt} onChange={(e) => setDraft({ ...draft, prompt: e.target.value })} required placeholder="What should the cognitive agent accomplish?" /></label><label className="wide">Success conditions <small>optional, one per line</small><textarea value={conditionsText} onChange={(e) => setConditionsText(e.target.value)} placeholder={'Evidence gathered\nDecision justified'} /></label><label>Token budget<input type="number" min="1" step="1" value={draft.tokenBudget} onChange={(e) => setDraft({ ...draft, tokenBudget: Number(e.target.value) })} required /></label><label>Max cost <small>optional</small><input type="number" min="0" step="0.01" value={draft.maxCost ?? ''} onChange={(e) => setDraft({ ...draft, maxCost: e.target.value === '' ? undefined : Number(e.target.value) })} /></label><label>Mode<select value={draft.mode} onChange={(e) => setDraft({ ...draft, mode: e.target.value })}><option value="explore">explore</option><option value="exploit">exploit</option><option value="verify">verify</option><option value="recover">recover</option></select></label><label>Trust minimum<input type="number" min="0" max="1" step="0.05" value={draft.trustMin} onChange={(e) => setDraft({ ...draft, trustMin: Number(e.target.value) })} required /></label><label className="checkbox wide"><input type="checkbox" checked={draft.rebuildRegions} onChange={(e) => setDraft({ ...draft, rebuildRegions: e.target.checked })} /> Rebuild region projections before cognition</label></div>{workflowBusy && <div className="status" role="status"><span className="spinner" /> {workflowStage}…</div>}<div className="dialog-actions"><button type="button" onClick={() => setDialogOpen(false)} disabled={workflowBusy}>Cancel</button><button className="primary" type="submit" disabled={workflowBusy || !draft.prompt.trim()}>{workflowBusy ? workflowStage : 'Create and run'}</button></div></form></dialog>
  </div>
}
export default App
