import { type FormEvent, type ReactNode, useCallback, useEffect, useRef, useState } from 'react'
import { api, API_BASE } from './api'
import { eventKey, eventLabel, eventTime, executionIdOf, frameIdOf, unwrapEvents } from './timeline'
import type { Execution, ForkGroup, Frame, FrameSection, FrpEvent, ModelConfig, ModelStepResponse, RenderResponse } from './types'
import { childFrameId, continueWithModel, EpisodeWorkflowError, renderRequest, runEpisodeWorkflow, twoBranchForkRequest, type EpisodeDraft, type EpisodeWorkflowResult, type WorkflowStage } from './workflow'

const SECTIONS: FrameSection[] = ['focus', 'map', 'periphery', 'procedures', 'recent']
const DEFAULT_DRAFT: EpisodeDraft = { prompt: '', successConditions: [], tokenBudget: 4000, mode: 'explore', trustMin: 0.5, rebuildRegions: false }

function JsonView({ value, empty = 'No data' }: { value: unknown; empty?: string }) {
  if (value === undefined || value === null) return <p className="muted empty">{empty}</p>
  return <pre>{typeof value === 'string' ? value : JSON.stringify(value, null, 2)}</pre>
}
function Status({ loading, error, children }: { loading: boolean; error?: string; children: ReactNode }) {
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
  const [events, setEvents] = useState<FrpEvent[]>([]); const [eventsBusy, setEventsBusy] = useState(false); const [eventsError, setEventsError] = useState('')
  const [frame, setFrame] = useState<Frame | null>(null); const [frameBusy, setFrameBusy] = useState(false); const [frameError, setFrameError] = useState(''); const [selectedFrameId, setSelectedFrameId] = useState('')
  const [execution, setExecution] = useState<Execution | null>(null); const [executionBusy, setExecutionBusy] = useState(false); const [executionError, setExecutionError] = useState('')
  const [rendered, setRendered] = useState<RenderResponse | null>(null); const [actionBusy, setActionBusy] = useState(''); const [actionError, setActionError] = useState(''); const [replay, setReplay] = useState<unknown>(null)
  const [blameRootId, setBlameRootId] = useState(''); const [blameMaxDepth, setBlameMaxDepth] = useState(8); const [blame, setBlame] = useState<unknown>(null); const [forkGroup, setForkGroup] = useState<ForkGroup | null>(null)
  const [modelConfig, setModelConfig] = useState<ModelConfig | null>(null); const [configBusy, setConfigBusy] = useState(false); const [configError, setConfigError] = useState('')
  const [dialogOpen, setDialogOpen] = useState(false); const [draft, setDraft] = useState(DEFAULT_DRAFT); const [conditionsText, setConditionsText] = useState('')
  const [workflowBusy, setWorkflowBusy] = useState(false); const [workflowStage, setWorkflowStage] = useState<WorkflowStage | ''>(''); const [workflowError, setWorkflowError] = useState(''); const [pending, setPending] = useState<EpisodeWorkflowResult | null>(null); const [summary, setSummary] = useState<ModelStepResponse | null>(null)
  const dialogRef = useRef<HTMLDialogElement>(null)

  const loadEvents = useCallback(async (id: string) => {
    if (!id.trim()) return []
    setEventsBusy(true); setEventsError('')
    try { const loaded = unwrapEvents(await api.events(id.trim(), limit)); setEvents(loaded); return loaded }
    catch (error) { setEventsError(errorText(error, 'Unable to load timeline')); return [] }
    finally { setEventsBusy(false) }
  }, [limit])
  const refreshConfig = useCallback(async () => {
    setConfigBusy(true); setConfigError('')
    try { setModelConfig(await api.modelConfig()) } catch (error) { setConfigError(errorText(error, 'Unable to load model config')) } finally { setConfigBusy(false) }
  }, [])
  useEffect(() => { void refreshConfig() }, [refreshConfig])
  useEffect(() => { if (!episodeId) return; const timer = window.setInterval(() => void loadEvents(episodeId), 5000); return () => window.clearInterval(timer) }, [episodeId, loadEvents])
  useEffect(() => { if (dialogOpen) dialogRef.current?.showModal(); else dialogRef.current?.close() }, [dialogOpen])

  async function openFrame(id: string) {
    setSelectedFrameId(id); setFrameBusy(true); setFrameError(''); setFrame(null); setRendered(null); setReplay(null); setBlame(null); setForkGroup(null); setActionError('')
    try { setFrame(await api.frame(id)) } catch (error) { setFrameError(errorText(error, 'Unable to load frame')) } finally { setFrameBusy(false) }
  }
  async function openExecution(id: string) { setExecutionBusy(true); setExecutionError(''); setExecution(null); try { setExecution(await api.execution(id)) } catch (error) { setExecutionError(errorText(error, 'Unable to load execution')) } finally { setExecutionBusy(false) } }
  async function runAction(name: string, operation: () => Promise<void>) { setActionBusy(name); setActionError(''); try { await operation() } catch (error) { setActionError(errorText(error, `Unable to ${name}`)) } finally { setActionBusy('') } }

  const objectiveId = frame?.objective_id; const frameId = frame?.frame_id ?? frame?.id ?? selectedFrameId
  function submitEpisode(event: FormEvent) { event.preventDefault(); const id = episodeInput.trim(); setEpisodeId(id); void loadEvents(id) }
  function submitBlame(event: FormEvent) { event.preventDefault(); const rootId = blameRootId.trim(); if (rootId) void runAction('blame', async () => setBlame(await api.blame({ root_id: rootId, max_depth: blameMaxDepth }))) }
  function fork() { if (!frameId) return; void runAction('fork', async () => setForkGroup(await api.fork(twoBranchForkRequest(frameId)))) }
  function renderSelected() { if (!frame) return; const request = renderRequest(frame, selectedFrameId); if (request) void runAction('render', async () => setRendered(await api.render(request))) }

  async function finishModel(result: EpisodeWorkflowResult, modelResult?: ModelStepResponse) {
    setPending(null); setSummary(modelResult ?? result.modelResult ?? null); setEpisodeId(result.ids.episodeId); setEpisodeInput(result.ids.episodeId)
    await loadEvents(result.ids.episodeId)
    const child = modelResult ? childFrameId(modelResult) : result.childFrameId
    await openFrame(child ?? result.parentFrameId)
  }
  async function createEpisode(event: FormEvent) {
    event.preventDefault(); if (workflowBusy || !draft.prompt.trim()) return
    setWorkflowBusy(true); setWorkflowError(''); setSummary(null)
    try {
      const result = await runEpisodeWorkflow(api, { ...draft, successConditions: conditionsText.split('\n').map((value) => value.trim()).filter(Boolean) }, modelConfig ?? { configured: false }, setWorkflowStage)
      setDialogOpen(false); setEpisodeId(result.ids.episodeId); setEpisodeInput(result.ids.episodeId); await loadEvents(result.ids.episodeId); await openFrame(result.parentFrameId)
      if (result.needsModel) { setPending(result); setWorkflowError('Objective and Frame were created. Configure TEMPORALITY_MODEL_BASE_URL and TEMPORALITY_MODEL_ID, refresh model status, then retry.') } else await finishModel(result)
    } catch (error) {
      if (error instanceof EpisodeWorkflowError) { setPending(error.result); setEpisodeId(error.result.ids.episodeId); setEpisodeInput(error.result.ids.episodeId); setDialogOpen(false); await loadEvents(error.result.ids.episodeId); await openFrame(error.result.parentFrameId) }
      setWorkflowError(errorText(error, 'Unable to create episode'))
    } finally { setWorkflowBusy(false); setWorkflowStage('') }
  }
  async function retryPending() {
    if (!pending || workflowBusy) return
    if (!modelConfig?.configured) { setWorkflowError('Model is still unconfigured. Set the runtime model environment, then refresh model status.'); return }
    setWorkflowBusy(true); setWorkflowStage('Calling model'); setWorkflowError('')
    try { const result = await continueWithModel(api, pending.parentFrameId, pending.ids.objectiveId, draft.tokenBudget); await finishModel(pending, result) } catch (error) { setWorkflowError(errorText(error, 'Model step failed; IDs are retained for retry.')) } finally { setWorkflowBusy(false); setWorkflowStage('') }
  }
  function continueSelected() { if (!frameId || !objectiveId) return; void runAction('Calling model', async () => { const result = await continueWithModel(api, frameId, objectiveId, frameBudget(frame)); setSummary(result); if (episodeId) await loadEvents(episodeId); const child = childFrameId(result); if (child) await openFrame(child) }) }

  const provenance = modelConfig?.provenance; const modelName = provenance?.model ?? modelConfig?.model; const baseUrl = provenance?.base_url ?? modelConfig?.base_url
  return <div className="app-shell">
    <header className="topbar"><div><span className="eyebrow">TEMPORALITY / FRP</span><h1>Cognitive Debugger</h1></div><div className="header-actions"><div className={`model-badge ${modelConfig?.configured ? 'ready' : 'offline'}`} title={configError || undefined}><span className="pulse" />{configBusy ? 'Checking model…' : modelConfig?.configured ? `Configured · ${modelName ?? 'model'} · ${baseUrl ?? 'base URL set'}` : 'Model not configured'}</div><button className="icon-button" onClick={() => void refreshConfig()} disabled={configBusy} aria-label="Refresh model configuration">↻</button><span className="connection">API <code>{API_BASE}</code></span><button className="primary" onClick={() => setDialogOpen(true)}>＋ New Episode</button></div></header>

    <main className="workspace">
      <aside className="timeline panel" aria-label="Episode timeline"><div className="panel-heading"><div><span className="eyebrow">EPISODE</span><h2>Timeline</h2></div>{episodeId && <button className="icon-button" onClick={() => void loadEvents(episodeId)} aria-label="Refresh timeline">↻</button>}</div>
        <form className="episode-form" onSubmit={submitEpisode}><label htmlFor="episode">Episode ID</label><div className="input-row"><input id="episode" value={episodeInput} onChange={(e) => setEpisodeInput(e.target.value)} placeholder="UUID" required /><button type="submit">Open</button></div><label htmlFor="limit">Event limit <output>{limit}</output></label><input id="limit" type="range" min="10" max="500" step="10" value={limit} onChange={(e) => setLimit(Number(e.target.value))} /></form>
        {(workflowError || pending) && <div className="status warning" role="status"><strong>{workflowError || 'Model step pending.'}</strong>{pending && <><small>Episode <code>{pending.ids.episodeId}</code> and frame <code>{pending.parentFrameId}</code> are safe.</small><button onClick={() => void retryPending()} disabled={workflowBusy || !modelConfig?.configured}>{workflowBusy ? workflowStage : 'Retry model step'}</button></>}</div>}
        <Status loading={eventsBusy} error={eventsError}><ol className="event-list">{events.map((item, index) => { const fid = frameIdOf(item); const xid = executionIdOf(item); return <li key={eventKey(item, index)} className={fid === selectedFrameId ? 'selected' : ''}><div className="event-rail"><span className="event-dot" /><span /></div><div className="event-card"><span className="event-time">{eventTime(item) ? new Date(eventTime(item)).toLocaleString() : `#${index + 1}`}</span><strong>{eventLabel(item)}</strong><div className="event-links">{fid && <button onClick={() => void openFrame(fid)}>Frame {fid}</button>}{xid && <button onClick={() => void openExecution(xid)}>Execution {xid}</button>}</div></div></li> })}{!events.length && !eventsBusy && <li className="empty-state"><strong>Start an operational trace</strong><span>Create a new task with New Episode, or paste an existing <code>episode_id</code> above.</span></li>}</ol></Status>
      </aside>

      <section className="inspector panel" aria-label="Frame inspector"><div className="panel-heading inspector-heading"><div><span className="eyebrow">SELECTED FRAME</span><h2>{selectedFrameId || 'No frame selected'}</h2></div>{objectiveId && <div className="objective"><span>Objective</span><code>{objectiveId}</code></div>}</div>
        <Status loading={frameBusy} error={frameError}>{!frame ? <div className="hero-empty"><div className="frame-glyph">⌗</div><h3>Create a task or select a frame</h3><p>Run cognition with a configured model, or inspect an existing episode’s boundaries, evidence, token use, and provenance.</p><button className="primary" onClick={() => setDialogOpen(true)}>New Episode</button></div> : <><div className="actionbar" aria-label="Frame actions"><button onClick={continueSelected} disabled={!!actionBusy || !modelConfig?.configured}>Continue with model</button><button onClick={renderSelected} disabled={!!actionBusy || !objectiveId} title="Render the selected Frame using its objective and token budget (default 4000)">Render selected frame</button><button onClick={() => void runAction('replay', async () => setReplay(await api.replay(frameId)))} disabled={!!actionBusy}>↶ Replay from here</button><button onClick={fork} disabled={!!actionBusy}>⑂ Fork ×2</button>{actionBusy && <span className="muted" role="status"><span className="spinner" /> {actionBusy}…</span>}</div>{actionError && <div className="status error" role="alert">{actionError}</div>}
          <div className="section-grid">{SECTIONS.map((section) => <article className={`data-card ${section === 'focus' ? 'featured' : ''}`} key={section}><h3><span>{section === 'focus' ? '◎' : '◇'}</span>{section}</h3><JsonView value={frame.sections?.[section] ?? frame[section]} /></article>)}</div><div className="evidence-grid"><article className="data-card warning"><h3><span>↗</span>outside_frame</h3><JsonView value={frame.outside_frame} /></article><article className="data-card"><h3><span>⌁</span>provenance</h3><JsonView value={frame.provenance} /></article><article className="data-card"><h3><span>◴</span>token usage</h3><JsonView value={frame.token_usage ?? frame.usage} /></article></div>
          {(summary || rendered || replay) && <div className="result-grid">{summary && <article className="result-card"><h3>Model result summary</h3><JsonView value={summary} /></article>}{rendered && <article className="result-card"><h3>Rendered cognition</h3><JsonView value={rendered.rendered ?? rendered.output ?? rendered.content ?? rendered} /></article>}{replay !== null && <article className="result-card"><h3>Replay result</h3><JsonView value={replay} /></article>}</div>}
          <article className="tool-card"><div><span className="eyebrow">CAUSAL REFERENCE</span><h3>Blame analysis</h3><p>Enter the exact Claim, Event, or Frame reference ID to trace—not a natural-language question.</p></div><form onSubmit={submitBlame}><label>Claim / Event / Frame reference ID<input value={blameRootId} onChange={(e) => setBlameRootId(e.target.value)} placeholder="Reference ID" aria-label="Blame root reference ID" required /></label><label>Maximum traversal depth<input type="number" min="1" step="1" value={blameMaxDepth} onChange={(e) => setBlameMaxDepth(Number(e.target.value))} aria-label="Blame maximum depth" required /></label><button disabled={!!actionBusy}>Trace reference</button></form>{blame !== null && <JsonView value={blame} />}</article>{forkGroup && <article className="tool-card"><span className="eyebrow">COUNTERFACTUAL GROUP</span><h3>Forked branches</h3><p>Fork group <code>{forkGroup.fork_group_id}</code></p><div className="branch-grid">{forkGroup.branches.map((branch) => <div className="branch" key={branch.branch_id}><strong>{branch.label ?? 'Branch'}</strong><code>{branch.branch_id}</code><JsonView value={branch} /></div>)}</div></article>}</>}</Status>
      </section>
      <aside className={`execution-drawer panel ${execution || executionBusy || executionError ? 'open' : ''}`} aria-label="Execution details"><div className="panel-heading"><div><span className="eyebrow">EXECUTION</span><h2>Details</h2></div><button className="icon-button" onClick={() => { setExecution(null); setExecutionError('') }} aria-label="Close execution details">×</button></div><Status loading={executionBusy} error={executionError}><JsonView value={execution} /></Status></aside>
    </main>

    <dialog ref={dialogRef} onCancel={() => !workflowBusy && setDialogOpen(false)} aria-labelledby="new-episode-title"><form className="episode-dialog" onSubmit={createEpisode}><div className="dialog-heading"><div><span className="eyebrow">OPERATIONAL WORKFLOW</span><h2 id="new-episode-title">New Episode</h2></div><button type="button" className="icon-button" onClick={() => setDialogOpen(false)} disabled={workflowBusy} aria-label="Close">×</button></div><div className="form-grid"><label className="wide">Prompt <span aria-hidden="true">*</span><textarea autoFocus value={draft.prompt} onChange={(e) => setDraft({ ...draft, prompt: e.target.value })} required placeholder="What should the cognitive agent accomplish?" /></label><label className="wide">Success conditions <small>optional, one per line</small><textarea value={conditionsText} onChange={(e) => setConditionsText(e.target.value)} placeholder={'Evidence gathered\nDecision justified'} /></label><label>Token budget<input type="number" min="1" step="1" value={draft.tokenBudget} onChange={(e) => setDraft({ ...draft, tokenBudget: Number(e.target.value) })} required /></label><label>Max cost <small>optional</small><input type="number" min="0" step="0.01" value={draft.maxCost ?? ''} onChange={(e) => setDraft({ ...draft, maxCost: e.target.value === '' ? undefined : Number(e.target.value) })} /></label><label>Mode<select value={draft.mode} onChange={(e) => setDraft({ ...draft, mode: e.target.value })}><option value="explore">explore</option><option value="exploit">exploit</option><option value="verify">verify</option><option value="recover">recover</option></select></label><label>Trust minimum<input type="number" min="0" max="1" step="0.05" value={draft.trustMin} onChange={(e) => setDraft({ ...draft, trustMin: Number(e.target.value) })} required /></label><label className="checkbox wide"><input type="checkbox" checked={draft.rebuildRegions} onChange={(e) => setDraft({ ...draft, rebuildRegions: e.target.checked })} /> Rebuild region projections before cognition</label></div>{workflowBusy && <div className="status" role="status"><span className="spinner" /> {workflowStage}…</div>}<div className="dialog-actions"><button type="button" onClick={() => setDialogOpen(false)} disabled={workflowBusy}>Cancel</button><button className="primary" type="submit" disabled={workflowBusy || !draft.prompt.trim()}>{workflowBusy ? workflowStage : 'Create and run'}</button></div></form></dialog>
  </div>
}
export default App
