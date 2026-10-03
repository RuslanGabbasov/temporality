import { useCallback, useEffect, useMemo, useState } from 'react'
import { API_BASE, authHeaders } from './api'
import { observationApi, type ObservationEvent } from './observationApi'
import Markdown from './Markdown'
import { useT } from './i18n'
import {
  Button,
  TextInput,
  InlineNotification,
  Loading,
  Tag,
  Tile,
  Grid,
  Column,
  Stack,
  Section,
  Heading,
} from '@carbon/react'
import { Time, Play, ChevronDown, ChevronRight } from '@carbon/icons-react'
import ListFilter, { matchesFilter } from './ListFilter'

const KERNEL_API = '/kernel-api'
function message(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }
function json(value: unknown) { return JSON.stringify(value, null, 2) }
function runID(event: ObservationEvent) { return event.context?.run ?? '' }
function pendingApprovals(events: ObservationEvent[]) {
  const ended = new Set(events.filter((event) => ['approval.granted', 'approval.rejected', 'approval.timed_out'].includes(event.type)).map((event) => event.data?.operation_id))
  return events.filter((event) => event.type === 'approval.requested' && !ended.has(event.data?.operation_id))
}

function shortTime(iso: string) {
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? '' : d.toLocaleTimeString()
}

/** Format an event into a human-readable summary line. */
function eventSummary(event: ObservationEvent, t: (key: string, vars?: Record<string, string>) => string): { icon: string; label: string; detail: string; color: string } {
  const d = event.data ?? {}
  switch (event.type) {
    case 'run.started':
      return { icon: '▶', label: t('runs.event.run_started'), detail: d.model ? t('runs.model', { name: String(d.model) }) : '', color: 'var(--tm-teal)' }
    case 'run.completed':
      return { icon: '✓', label: t('runs.event.run_completed'), detail: `${d.turns ?? '?'} ${t('runs.turns')}`, color: '#9ece6a' }
    case 'run.failed':
      return { icon: '✗', label: t('runs.event.run_failed'), detail: d.error ? String(d.error).slice(0, 80) : '', color: '#f7768e' }
    case 'turn.started':
      return { icon: '→', label: t('runs.turn', { turn: String(d.turn ?? '?') }), detail: t('runs.event.started'), color: 'var(--tm-text-3)' }
    case 'turn.completed':
      return { icon: '←', label: t('runs.turn', { turn: String(d.turn ?? '?') }), detail: d.tool_calls ? t('runs.tool_calls_n', { count: String(d.tool_calls) }) : t('runs.event.completed'), color: 'var(--tm-text-3)' }
    case 'model.started':
      return { icon: '⏳', label: t('runs.event.model_call'), detail: String(d.model ?? t('runs.event.started')), color: 'var(--tm-text-3)' }
    case 'model.completed': {
      const tokens = d.total_tokens ? `${d.total_tokens} tok` : ''
      const latency = d.latency_ms ? `${(Number(d.latency_ms) / 1000).toFixed(1)}s` : ''
      const calls = d.tool_call_count ? `${d.tool_call_count} tools` : ''
      const parts = [tokens, latency, calls].filter(Boolean).join(' · ')
      return { icon: '🧠', label: t('runs.event.model_response'), detail: parts, color: '#bb9af7' }
    }
    case 'tool.started': {
      const args = d.arguments
      let preview = ''
      if (typeof args === 'string') {
        try {
          const parsed = JSON.parse(args)
          if (Array.isArray(parsed.command)) preview = parsed.command.join(' ')
          else if (parsed.path) preview = parsed.path
          else if (parsed.query) preview = String(parsed.query).slice(0, 60)
          else if (parsed.proposition) preview = String(parsed.proposition).slice(0, 60)
          else preview = args.slice(0, 60)
        } catch { preview = String(args).slice(0, 60) }
      }
      return { icon: '🔧', label: String(d.tool ?? 'tool'), detail: preview || t('runs.event.started'), color: '#e0af68' }
    }
    case 'tool.completed': {
      const exit = d.exit_code !== undefined ? `exit ${d.exit_code}` : ''
      const ms = d.latency_ms ? `${(Number(d.latency_ms) / 1000).toFixed(1)}s` : ''
      const output = typeof d.output === 'string' ? d.output.slice(0, 80).replace(/\n/g, ' ') : ''
      return { icon: d.exit_code === 0 ? '✓' : '✗', label: String(d.tool ?? 'tool'), detail: [exit, ms, output].filter(Boolean).join(' · '), color: d.exit_code === 0 ? '#9ece6a' : '#f7768e' }
    }
    case 'tool.failed': {
      const errDetail = d.error ? String(d.error).slice(0, 60) : 'failed'
      const args = d.arguments
      let preview = ''
      if (typeof args === 'string') {
        try {
          const parsed = JSON.parse(args)
          if (Array.isArray(parsed.command)) preview = parsed.command.join(' ')
          else if (parsed.path) preview = parsed.path
        } catch { /* ignore */ }
      }
      return { icon: '✗', label: String(d.tool ?? 'tool'), detail: preview ? `${preview} → ${errDetail}` : errDetail, color: '#f7768e' }
    }
    case 'knowledge.proposed':
      return { icon: '💡', label: t('runs.event.learned'), detail: String(d.proposition ?? '').slice(0, 60), color: '#73daca' }
    case 'knowledge.recalled':
      return { icon: '📚', label: t('runs.event.recalled'), detail: String(d.proposition ?? '').slice(0, 60), color: '#9d7cd8' }
    case 'hint.query':
      return { icon: '🔍', label: t('runs.event.memory_lookup'), detail: t('runs.candidates', { count: String(d.candidate_count ?? 0) }), color: 'var(--tm-text-3)' }
    case 'memory.read':
      return { icon: '📖', label: t('runs.event.memory_read'), detail: t('runs.hints_loaded', { count: String(d.hint_count ?? 0) }), color: 'var(--tm-text-3)' }
    case 'mcp.call.started':
      return { icon: '🔌', label: String(d.tool ?? 'MCP'), detail: `→ ${d.server ?? ''}`, color: '#bb9af7' }
    case 'mcp.call.completed':
      return { icon: '🔌', label: String(d.tool ?? 'MCP'), detail: t('runs.event.completed'), color: '#9ece6a' }
    case 'mcp.call.failed':
      return { icon: '🔌', label: String(d.tool ?? 'MCP'), detail: String(d.error_type ?? 'failed'), color: '#f7768e' }
    case 'approval.requested': {
      const op = d.operation as Record<string, unknown> | undefined
      return { icon: '⚠', label: t('runs.event.approval_needed'), detail: String(d.action ?? op?.tool ?? ''), color: '#e6b85c' }
    }
    case 'approval.granted':
      return { icon: '✓', label: t('runs.event.approved'), detail: d.approver ? `by ${String(d.approver)}` : '', color: '#9ece6a' }
    case 'approval.auto_granted': {
      const op2 = d.operation as Record<string, unknown> | undefined
      return { icon: '✓', label: t('runs.event.auto_approved'), detail: String(op2?.tool ?? d.policy_id ?? ''), color: '#9ece6a' }
    }
    case 'tool.blocked':
      return { icon: '🚫', label: t('runs.event.blocked'), detail: String(d.reason ?? ''), color: '#f7768e' }
    case 'agent.summary':
      return { icon: '📋', label: t('runs.event.summary'), detail: String(d.kind ?? ''), color: 'var(--tm-teal)' }
    case 'approval.rejected':
      return { icon: '✗', label: t('runs.event.rejected'), detail: String(d.reason ?? ''), color: '#f7768e' }
    case 'delegation.started':
      return { icon: '↗', label: t('runs.event.delegation'), detail: `→ ${d.child_run_id ?? '?'}`, color: '#7aa2f7' }
    default:
      return { icon: '•', label: event.type, detail: '', color: 'var(--tm-text-3)' }
  }
}

/** Expandable event detail. For model.completed rows the response text (from
 * the run's model.text_delta/model.reasoning events) is shown as the primary
 * content; the observability payload is reduced to what the summary line
 * doesn't already say. */
function EventDetail({ event, t, response, reasoning }: { event: ObservationEvent; t: (key: string, vars?: Record<string, string>) => string; response?: string; reasoning?: string }) {
  const [expanded, setExpanded] = useState(false)
  const d = event.data ?? {}

  // Show useful fields first, skip noise
  const skipFields = new Set(['caused_by', 'frame_id', 'parent_frame_id', 'sequence'])
  if (event.type === 'model.completed') {
    // tokens/latency/tools are already in the row summary; refs, retries and
    // token splits are plumbing nobody reads.
    for (const k of ['turn', 'total_tokens', 'latency_ms', 'tool_call_count', 'finish_reason', 'model', 'provider', 'prompt_tokens', 'completion_tokens', 'input_ref', 'output_ref', 'truncated', 'attempts']) skipFields.add(k)
  }
  const importantFields: [string, unknown][] = []
  const otherFields: [string, unknown][] = []
  for (const [k, v] of Object.entries(d)) {
    if (skipFields.has(k)) continue
    if (v === '' || v === null || v === undefined) continue
    if (['tool', 'turn', 'total_tokens', 'latency_ms', 'exit_code', 'tool_call_count', 'finish_reason', 'model', 'provider', 'error', 'proposition', 'answer', 'arguments', 'output'].includes(k)) {
      importantFields.push([k, v])
    } else {
      otherFields.push([k, v])
    }
  }
  const fields = [...importantFields, ...otherFields]

  return (
    <div>
      {/* Model response for this turn — the thing the reader actually wants */}
      {response && (
        <div className="run-model-response">
          <Markdown content={response} />
        </div>
      )}
      {reasoning && (
        <details className="run-model-reasoning">
          <summary>💭 {t('runs.reasoning')}</summary>
          <div className="run-model-reasoning-content">{reasoning}</div>
        </details>
      )}
      {fields.length > 0 && (
        <>
          <button
            onClick={() => setExpanded(!expanded)}
            style={{ background: 'none', border: 'none', color: 'var(--tm-text-3)', cursor: 'pointer', fontSize: '0.7rem', padding: '0.15rem 0', display: 'flex', alignItems: 'center', gap: '0.25rem' }}
          >
            {expanded ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
            {expanded ? t('runs.hide_details') : t('runs.show_details')}
          </button>
          {expanded && (
            <div style={{ marginTop: '0.35rem', padding: '0.5rem', background: 'var(--tm-raised)', borderRadius: '4px', fontSize: '0.7rem', fontFamily: '"SFMono-Regular", Consolas, monospace', maxHeight: '300px', overflowY: 'auto', minWidth: 0, overflowWrap: 'anywhere' }}>
              {/* Show answer/content if present */}
              {typeof d.answer === 'string' && (
                <div style={{ marginBottom: '0.5rem' }}>
                  <div style={{ color: 'var(--tm-text-3)', marginBottom: '0.25rem' }}>answer:</div>
                  <div style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere', color: 'var(--tm-text)', background: 'var(--tm-elevated)', padding: '0.5rem', borderRadius: '3px', maxHeight: '150px', overflowY: 'auto' }}>{d.answer.slice(0, 2000)}</div>
                </div>
              )}
              {/* Show other data */}
              {fields.map(([k, v]) => (
                <div key={k} style={{ marginBottom: '0.2rem' }}>
                  <span style={{ color: 'var(--tm-text-3)' }}>{k}: </span>
                  <span style={{ color: 'var(--tm-text)' }}>
                    {typeof v === 'object' ? JSON.stringify(v).slice(0, 200) : String(v).slice(0, 200)}
                  </span>
                </div>
              ))}
              {/* Event metadata */}
              <div style={{ marginTop: '0.35rem', borderTop: '1px solid var(--tm-border)', paddingTop: '0.35rem', color: 'var(--tm-muted)' }}>
                event_id: {event.event_id}
              </div>
            </div>
          )}
        </>
      )}
    </div>
  )
}

export default function AgentRuns({ project }: { project: string }) {
  const t = useT()
  const [runs, setRuns] = useState<ObservationEvent[]>([])
  const [selected, setSelected] = useState('')
  const [timeline, setTimeline] = useState<ObservationEvent[]>([])
  const [trajectory, setTrajectory] = useState<any>(null)
  const [result, setResult] = useState<Record<string, unknown> | null>(null)
  const [reason, setReason] = useState('Reviewed in Agent Runs UI')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [filter, setFilter] = useState('')
  const visibleRuns = runs.filter((event) => matchesFilter(filter, runID(event), String(event.data?.title ?? '')))
  const pending = useMemo(() => pendingApprovals(timeline), [timeline])
  // model.text_delta/model.reasoning carry each turn's response text. They are
  // joined into their model.completed row instead of polluting the trace with
  // duplicate per-turn entries.
  const trace = useMemo(() => {
    const responseByKey = new Map<string, string>()
    const reasoningByKey = new Map<string, string>()
    for (const event of timeline) {
      const turn = event.data?.turn
      const text = event.data?.text
      if (turn === undefined || turn === null || typeof text !== 'string' || !text) continue
      const key = `${runID(event)}:${turn}`
      if (event.type === 'model.text_delta') responseByKey.set(key, text)
      if (event.type === 'model.reasoning') reasoningByKey.set(key, text)
    }
    const visible = timeline.filter((event) => event.type !== 'model.text_delta' && event.type !== 'model.reasoning')
    return { visible, responseByKey, reasoningByKey }
  }, [timeline])

  const loadRuns = useCallback(async (autoSelect = false) => {
    if (!project.trim()) return
    if (!autoSelect) setBusy(true)
    setError('')
    try {
      const page = await observationApi.events(project.trim(), undefined, undefined, undefined, { type: 'run.started', limit: 500 })
      const rootRuns = page.events.filter((event) => !event.data?.parent_run_id)
      setRuns(rootRuns.reverse())
      // Only auto-select on initial load when nothing is selected
      if (autoSelect && !selected && rootRuns.length) {
        for (const event of rootRuns) {
          const id = runID(event)
          if (!id) continue
          const query = new URLSearchParams({ project: project.trim(), source_id: event.source.id })
          let response = await fetch(`${KERNEL_API}/v1/agent/runs/${encodeURIComponent(id)}?${query}`, { headers: authHeaders() })
          if (!response.ok && event.data?.workflow === 'LeadCoderReviewerQA') {
            response = await fetch(`${KERNEL_API}/v1/agent/examples/lead-coder-reviewer-qa/runs/${encodeURIComponent(id)}?${query}`, { headers: authHeaders() })
          }
          if (response.ok) { setSelected(id); break }
        }
        if (!selected) setSelected(runID(rootRuns[0]) || '')
      }
    } catch (failure) { setError(message(failure)) }
    finally { if (!autoSelect) setBusy(false) }
  }, [project])

  const loadRun = useCallback(async (run: string, silent = false) => {
    if (!project.trim() || !run) return
    if (!silent) setBusy(true)
    setError('')
    try {
      const page = await observationApi.events(project.trim(), undefined, undefined, undefined, { run, limit: 500 })
      const root = page.events.find((event) => event.type === 'run.started')
      const teamRun = root?.data?.workflow === 'LeadCoderReviewerQA'
      let allEvents = page.events
      if (teamRun) {
        const childIDs = page.events.filter((event) => event.type === 'delegation.started').map((event) => String(event.data?.child_run_id ?? '')).filter(Boolean)
        const childPages = await Promise.all(childIDs.map((child) => observationApi.events(project.trim(), undefined, undefined, undefined, { run: child, limit: 500 })))
        allEvents = [...page.events, ...childPages.flatMap((childPage) => childPage.events)].sort((left, right) => left.occurred_at.localeCompare(right.occurred_at))
      }
      setTimeline(allEvents)
      const resultPath = teamRun ? `/v1/agent/examples/lead-coder-reviewer-qa/runs/${encodeURIComponent(run)}` : `/v1/agent/runs/${encodeURIComponent(run)}`
      const sourceQuery = new URLSearchParams({ project: project.trim(), source_id: root?.source.id ?? '' })
      let response = await fetch(`${KERNEL_API}${resultPath}?${sourceQuery}`, { headers: authHeaders() })
      if (!response.ok && teamRun) response = await fetch(`${KERNEL_API}/v1/agent/runs/${encodeURIComponent(run)}?${sourceQuery}`, { headers: authHeaders() })
      if (response.ok) {
        setResult(await response.json() as Record<string, unknown>)
      } else if (response.status === 404) {
        setResult({ status: 'workflow_expired', error: 'Workflow no longer available in Temporal (events preserved in journal)' })
      } else {
        const detail = await response.text()
        throw new Error(`${response.status} ${detail}`)
      }
      const query = new URLSearchParams({ project: project.trim(), run })
      window.history.replaceState(null, '', `/agents?${query}`)
      // Fetch trajectory extraction (deterministic patterns from event stream)
      try {
        const tResp = await fetch(`${KERNEL_API}/v1/workspace/runs/${encodeURIComponent(run)}/trajectory`, { headers: authHeaders() })
        if (tResp.ok) setTrajectory(await tResp.json())
      } catch { /* ignore */ }
    } catch (failure) { setError(message(failure)) }
    finally { if (!silent) setBusy(false) }
  }, [project])

  useEffect(() => { void loadRuns(true) }, [loadRuns])
  useEffect(() => { if (selected) void loadRun(selected) }, [selected, loadRun])
  useEffect(() => {
    if (!selected) return
    // Poll only the selected run's events, don't re-fetch the full run list
    const timer = window.setInterval(() => { void loadRun(selected, true) }, 10000)
    return () => window.clearInterval(timer)
  }, [selected, loadRun])

  async function decide(event: ObservationEvent, approved: boolean) {
    const operationID = String(event.data?.operation_id ?? '')
    setBusy(true); setError('')
    try {
      const root = timeline.find((item) => item.type === 'run.started' && runID(item) === selected)
      const operation = event.data?.operation as Record<string, unknown> | undefined
      const approvalQuery = new URLSearchParams({ project: project.trim(), source_id: root?.source.id ?? '' })
      const response = await fetch(`${KERNEL_API}/v1/agent/runs/${encodeURIComponent(selected)}/approval?${approvalQuery}`, {
        method: 'POST', headers: { 'Content-Type': 'application/json', ...authHeaders() },
        body: JSON.stringify({ operation_id: operationID, arguments_hash: operation?.arguments_hash ?? '', approved, actor_id: 'human-ui', reason: reason.trim() }),
      })
      if (!response.ok) throw new Error(`${response.status} ${await response.text()}`)
      await loadRun(selected, true)
    } catch (failure) { setError(message(failure)) }
    finally { setBusy(false) }
  }

  const answer = (result?.result as Record<string, unknown> | undefined)?.answer
  const stages = (result?.result as Record<string, unknown> | undefined)?.stages
  const rootEvent = timeline.find((event) => event.type === 'run.started')
  const rawTitle = rootEvent?.data?.title
  const runTitle = typeof rawTitle === 'string' && rawTitle.trim() ? rawTitle.trim() : ''
  return (
    <div style={{ padding: '1rem' }}>
      {error && (
        <InlineNotification kind="error" title={t('action.error') ?? 'Error'} subtitle={error} onClose={() => setError('')} lowContrast style={{ marginBottom: '1rem' }} />
      )}

      <Grid>
        {/* Run list */}
        <Column sm={4} md={3} lg={4} style={{ minWidth: 0 }}>
          <div style={{ position: 'sticky', top: '3rem', maxHeight: 'calc(100vh - 4rem)', overflowY: 'auto' }}>
            <div style={{ padding: '0.5rem 0 0', borderBottom: '1px solid var(--tm-border)', position: 'sticky', top: 0, background: 'var(--tm-bg)', zIndex: 1 }}>
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '0.25rem' }}>
                <Heading style={{ fontSize: '1rem' }}>{t('runs.count', { count: String(visibleRuns.length) })}</Heading>
                <button onClick={() => { setSelected(''); setTimeline([]); void loadRuns(true) }} disabled={busy} style={{ background: 'none', border: 'none', color: 'var(--tm-text-2)', cursor: 'pointer', fontSize: '1rem', padding: '0.25rem', lineHeight: 1, borderRadius: '4px' }} title="Refresh">↻</button>
              </div>
              <ListFilter value={filter} onChange={setFilter} placeholder={t('common.filter_runs') ?? 'Filter runs…'} />
            </div>
            <Stack gap={1}>
              {runs.length === 0 && <Tile style={{ color: 'var(--tm-text-3)', textAlign: 'center' }}>{t('runs.no_runs') ?? 'No runs for this project'}</Tile>}
              {visibleRuns.map((event) => {
                const id = runID(event)
                const rawTitle = event.data?.title
                const title = typeof rawTitle === 'string' && rawTitle.trim() ? rawTitle.trim() : ''
                return (
                  <Tile
                    key={event.event_id}
                    onClick={() => setSelected(id)}
                    className={`workspace-tile ${selected === id ? 'selected' : ''}`}
                    style={{ padding: '0.5rem 0.75rem', cursor: 'pointer', minWidth: 0, overflow: 'hidden' }}
                  >
                    <div style={{ fontWeight: 500, fontSize: '0.8rem', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{title || id}</div>
                    <div style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)' }}>
                      {String(event.data?.role ?? 'agent')} · {shortTime(event.occurred_at)}
                    </div>
                    {title && (
                      <div style={{ fontSize: '0.65rem', color: 'var(--tm-muted)', fontFamily: 'monospace', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{id}</div>
                    )}
                  </Tile>
                )
              })}
            </Stack>
          </div>
        </Column>

        {/* Run detail — min-width: 0 lets the column shrink below the
         * min-content of long unbreakable strings in trace rows */}
        <Column sm={4} md={5} lg={12} style={{ minWidth: 0 }}>
          <Section level={3}>
            {!selected ? (
              <div style={{ textAlign: 'center', color: 'var(--tm-text-3)', padding: '3rem 1rem' }}>
                <Heading>{t('runs.select') ?? 'Select a run'}</Heading>
                <p style={{ marginTop: '0.5rem' }}>{t('runs.select_hint') ?? 'Click a run on the left to see its trace.'}</p>
              </div>
            ) : (
              <Stack gap={3}>
                <div>
                  <Heading style={{ fontSize: '1.1rem' }}>{runTitle || selected}</Heading>
                  {runTitle && (
                    <div style={{ fontSize: '0.7rem', color: 'var(--tm-muted)', fontFamily: 'monospace', marginTop: '0.25rem', overflowWrap: 'anywhere' }}>{selected}</div>
                  )}
                </div>

                {/* Answer */}
                {typeof answer === 'string' && (
                  <Tile>
                    <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', marginBottom: '0.5rem' }}>
                      <Heading style={{ fontSize: '0.875rem' }}>{t('runs.answer') ?? 'Agent Answer'}</Heading>
                      <Tag type={result?.status === 'completed' ? 'green' : 'red'} size="sm">{String(result?.status ?? '')}</Tag>
                    </div>
                    <Markdown content={answer} />
                  </Tile>
                )}

                {/* Multi-stage answer */}
                {Array.isArray(stages) && stages.map((stage, index) => {
                  const item = stage as Record<string, unknown>
                  return (
                    <Tile key={`${String(item.run_id ?? item.role ?? index)}`}>
                      <Heading style={{ fontSize: '0.875rem' }}>{String(item.role ?? `Stage ${index + 1}`)}</Heading>
                      <Tag type="gray" size="sm">{String(item.status ?? '')}</Tag>
                      {typeof item.answer === 'string' && <Markdown content={item.answer} />}
                    </Tile>
                  )
                })}

                {/* Raw result if no answer */}
                {result?.result !== undefined && !answer && (
                  <Tile>
                    <Heading style={{ fontSize: '0.875rem' }}>{t('runs.result') ?? 'Run Result'}</Heading>
                    <pre style={{ background: 'var(--tm-raised)', color: 'var(--tm-text)', padding: '0.75rem', fontSize: '0.75rem', overflow: 'auto', borderRadius: '4px', maxHeight: '300px' }}>
                      {json(result.result)}
                    </pre>
                  </Tile>
                )}

                {/* Pending approvals */}
                {pending.map((event) => {
                  const details = event.data?.details as Record<string, unknown> | undefined
                  const operation = event.data?.operation as Record<string, unknown> | undefined
                  const operationArgs = operation?.arguments as Record<string, unknown> | undefined
                  const command = operationArgs?.command
                  const risk = event.data?.risk as Record<string, unknown> | undefined
                  return (
                    <Tile key={event.event_id} style={{ borderLeft: '3px solid #e6b85c' }}>
                      <Heading style={{ fontSize: '0.875rem' }}>⚠ {t('runs.approval_required') ?? 'Approval Required'}</Heading>
                      <p style={{ fontSize: '0.875rem', margin: '0.5rem 0' }}>{String(operation?.summary ?? event.data?.reason ?? event.data?.action ?? t('runs.approval_requested') ?? 'Agent requested approval')}</p>
                      {details && (
                        <>
                          <Tag type="warm-gray" size="sm">{String(details.tool)}</Tag>
                          <Tag type="gray" size="sm">{t('runs.risk', { level: String(risk?.level ?? 'unknown') })}</Tag>
                          <pre style={{ background: 'var(--tm-raised)', color: 'var(--tm-text)', padding: '0.5rem', fontSize: '0.75rem', marginTop: '0.5rem', borderRadius: '4px', maxWidth: '100%', overflowX: 'auto' }}>
                            {Array.isArray(command) ? command.join(' ') : json(operation?.arguments ?? details)}
                          </pre>
                        </>
                      )}
                      <div style={{ marginTop: '0.75rem' }}>
                        <TextInput id="decision-note" labelText={t('runs.decision_note') ?? 'Decision note'} value={reason} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setReason(e.target.value)} />
                      </div>
                      <Stack orientation="horizontal" gap={2} style={{ marginTop: '0.5rem' }}>
                        <Button onClick={() => void decide(event, true)} disabled={busy}>{t('runs.approve') ?? 'Approve'}</Button>
                        <Button kind="secondary" onClick={() => void decide(event, false)} disabled={busy}>{t('runs.reject') ?? 'Reject'}</Button>
                      </Stack>
                    </Tile>
                  )
                })}

                {/* Timeline — the main trace view */}
                <Tile>
                  <Heading style={{ fontSize: '0.875rem', marginBottom: '0.5rem' }}>{t('runs.trace') ?? 'Trace'} ({t('runs.events_count', { count: String(trace.visible.length) })})</Heading>
                  <Stack gap={0}>
                    {[...trace.visible].reverse().map((event) => {
                      const info = eventSummary(event, t)
                      const turnKey = event.type === 'model.completed' ? `${runID(event)}:${event.data?.turn}` : undefined
                      return (
                        <div
                          key={`${event.source.id}:${event.event_id}`}
                          style={{
                            padding: '0.4rem 0.5rem',
                            borderLeft: `3px solid ${info.color}`,
                            marginBottom: '0.15rem',
                            background: 'var(--tm-surface)',
                            borderRadius: '0 4px 4px 0',
                            // The rows live in a CSS-grid Stack; without min-width:0
                            // a long nowrap detail line inflates the row's intrinsic
                            // width and stretches every row past the column edge.
                            minWidth: 0,
                            overflow: 'hidden',
                          }}
                        >
                          <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', fontSize: '0.8rem' }}>
                            <span style={{ color: info.color, width: '1.2rem', textAlign: 'center', flexShrink: 0 }}>{info.icon}</span>
                            <strong style={{ color: info.color, minWidth: '100px' }}>{info.label}</strong>
                            <span style={{ color: 'var(--tm-text-3)', flex: 1, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{info.detail}</span>
                            <span style={{ color: 'var(--tm-muted)', fontSize: '0.7rem', flexShrink: 0 }}>{shortTime(event.occurred_at)}</span>
                          </div>
                          <div style={{ marginLeft: '1.7rem', marginTop: '0.15rem' }}>
                            <EventDetail event={event} t={t} response={turnKey ? trace.responseByKey.get(turnKey) : undefined} reasoning={turnKey ? trace.reasoningByKey.get(turnKey) : undefined} />
                          </div>
                        </div>
                      )
                    })}
                  </Stack>
                </Tile>

                {/* Trajectory extraction */}
                {trajectory && (
                  <Tile>
                    <Heading style={{ fontSize: '0.875rem', marginBottom: '0.75rem' }}>{t('runs.trajectory') ?? 'Trajectory'}</Heading>
                    {/* Summary */}
                    <div style={{ display: 'flex', gap: '1rem', flexWrap: 'wrap', marginBottom: '0.75rem', fontSize: '0.8rem' }}>
                      <span><strong>{trajectory.summary?.total_turns ?? 0}</strong> {t('runs.turns') ?? 'turns'}</span>
                      <span><strong>{trajectory.summary?.total_tools ?? 0}</strong> {t('runs.tool_calls') ?? 'tool calls'}</span>
                      <span><strong>{trajectory.summary?.total_tokens ?? 0}</strong> {t('runs.tokens') ?? 'tokens'}</span>
                      <span><strong>{trajectory.summary?.knowledge_formed ?? 0}</strong> {t('runs.learned') ?? 'learned'}</span>
                      <span><strong>{trajectory.summary?.knowledge_recalled ?? 0}</strong> {t('runs.recalled') ?? 'recalled'}</span>
                      {trajectory.summary?.failed_tools > 0 && <span style={{ color: '#f7768e' }}><strong>{trajectory.summary.failed_tools}</strong> {t('runs.failed') ?? 'failed'}</span>}
                    </div>
                    {trajectory.summary?.tools_used?.length > 0 && (
                      <div style={{ display: 'flex', gap: '0.25rem', flexWrap: 'wrap', marginBottom: '0.75rem' }}>
                        {trajectory.summary.tools_used.map((t: string) => <Tag key={t} type="blue" size="sm">{t}</Tag>)}
                      </div>
                    )}
                    {/* Turns */}
                    {trajectory.turns?.length > 0 && (
                      <div style={{ marginBottom: '0.75rem' }}>
                        <h5 style={{ fontSize: '0.75rem', fontWeight: 600, color: 'var(--tm-text-3)', textTransform: 'uppercase', letterSpacing: '0.05em', marginBottom: '0.5rem' }}>{t('runs.turns_heading') ?? 'Turns'}</h5>
                        {trajectory.turns.map((turn: any) => (
                          <div key={turn.number} style={{ padding: '0.4rem 0.5rem', borderLeft: '3px solid #bb9af7', marginBottom: '0.25rem', background: 'var(--tm-surface)', borderRadius: '0 4px 4px 0', fontSize: '0.8rem' }}>
                            <strong>Turn {turn.number}</strong>
                            {turn.tokens > 0 && <span style={{ color: 'var(--tm-text-3)', marginLeft: '0.5rem' }}>{turn.tokens} tok</span>}
                            {turn.latency_ms > 0 && <span style={{ color: 'var(--tm-text-3)', marginLeft: '0.5rem' }}>{(turn.latency_ms / 1000).toFixed(1)}s</span>}
                            {turn.tools?.length > 0 && (
                              <div style={{ marginTop: '0.25rem', display: 'flex', gap: '0.25rem', flexWrap: 'wrap' }}>
                                {turn.tools.map((tool: any, ti: number) => (
                                  <Tag key={ti} type={tool.success ? 'green' : 'red'} size="sm">{tool.tool}{tool.exit_code !== undefined && tool.exit_code !== 0 ? ` (${tool.exit_code})` : ''}</Tag>
                                ))}
                              </div>
                            )}
                          </div>
                        ))}
                      </div>
                    )}
                    {/* Patterns */}
                    {trajectory.patterns?.length > 0 && (
                      <div style={{ marginBottom: '0.75rem' }}>
                        <h5 style={{ fontSize: '0.75rem', fontWeight: 600, color: 'var(--tm-text-3)', textTransform: 'uppercase', letterSpacing: '0.05em', marginBottom: '0.5rem' }}>{t('runs.patterns_heading') ?? 'Repeating patterns'}</h5>
                        {trajectory.patterns.map((p: any, i: number) => (
                          <div key={i} style={{ fontSize: '0.8rem', marginBottom: '0.25rem', color: 'var(--tm-text)' }}>
                            <Tag type="cyan" size="sm">×{p.count}</Tag>
                            <code style={{ marginLeft: '0.5rem', color: '#9e8cff', overflowWrap: 'anywhere' }}>{p.signature}</code>
                            <span style={{ color: 'var(--tm-text-3)', marginLeft: '0.5rem' }}>turns {p.turns.join(', ')}</span>
                          </div>
                        ))}
                      </div>
                    )}
                    {/* Knowledge events */}
                    {trajectory.knowledge?.length > 0 && (
                      <div>
                        <h5 style={{ fontSize: '0.75rem', fontWeight: 600, color: 'var(--tm-text-3)', textTransform: 'uppercase', letterSpacing: '0.05em', marginBottom: '0.5rem' }}>{t('runs.knowledge_heading') ?? 'Knowledge'}</h5>
                        {trajectory.knowledge.map((k: any, i: number) => (
                          <div key={i} style={{ fontSize: '0.8rem', marginBottom: '0.25rem' }}>
                            <Tag type={k.kind === 'proposed' ? 'blue' : k.kind === 'recalled' ? 'purple' : 'red'} size="sm">{k.kind}</Tag>
                            <span style={{ marginLeft: '0.5rem' }}>{k.proposition || k.knowledge_id}</span>
                          </div>
                        ))}
                      </div>
                    )}
                  </Tile>
                )}
              </Stack>
            )}
          </Section>
        </Column>
      </Grid>

      {busy && <Loading withOverlay={false} />}
    </div>
  )
}
