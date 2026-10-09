import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { API_BASE, authHeaders } from './api'
import { observationApi, type ObservationEvent } from './observationApi'
import { workspaceApi, type Agent } from './workspaceApi'
import DelegationTree from './DelegationTree'
import PlanGraph from './PlanGraph'
import RunDisputes from './RunDisputes'
import Markdown from './Markdown'
import { useT } from './i18n'
import { eventSummary, shortTime, explainKernelError, restoreMarkdownLines, unwrapJsonString } from './eventSummary'
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
import { runCost, type RunCost } from './kernelApi'

const KERNEL_API = '/kernel-api'
function message(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }
function json(value: unknown) { return JSON.stringify(value, null, 2) }
function runID(event: ObservationEvent) { return event.context?.run ?? '' }
function pendingApprovals(events: ObservationEvent[]) {
  const ended = new Set(events.filter((event) => ['approval.granted', 'approval.rejected', 'approval.timed_out', 'human.answered', 'human.cancelled', 'human.timed_out'].includes(event.type)).map((event) => event.data?.operation_id))
  return events.filter((event) => (event.type === 'approval.requested' || event.type === 'human.requested') && !ended.has(event.data?.operation_id))
}

type RunStatus = 'completed' | 'failed' | 'running'

interface RunRow {
  id: string
  title: string
  role: string
  at: string
  parent: string | null
  depth: number
  status: RunStatus
  sourceId: string
  workflow?: string
  agentId?: string
}

/** Build the run call tree: roots are runs without a known parent, children
 * (delegated runs carry parent_run_id) are nested under their parent. */
function buildRunRows(started: ObservationEvent[], statuses: Map<string, RunStatus>): RunRow[] {
  const byId = new Map<string, ObservationEvent>()
  for (const event of started) {
    const id = runID(event)
    if (id) byId.set(id, event)
  }
  const childrenOf = new Map<string, ObservationEvent[]>()
  const roots: ObservationEvent[] = []
  for (const event of started) {
    const id = runID(event)
    if (!id) continue
    const parent = event.data?.parent_run_id ? String(event.data.parent_run_id) : ''
    if (parent && parent !== id && byId.has(parent)) {
      const list = childrenOf.get(parent) ?? []
      list.push(event)
      childrenOf.set(parent, list)
    } else {
      roots.push(event)
    }
  }
  const rows: RunRow[] = []
  const walk = (event: ObservationEvent, depth: number) => {
    const id = runID(event)
    const rawTitle = event.data?.title
    rows.push({
      id,
      title: typeof rawTitle === 'string' && rawTitle.trim() ? rawTitle.trim() : '',
      role: String(event.data?.role ?? 'agent'),
      at: event.occurred_at,
      parent: depth === 0 ? null : String(event.data?.parent_run_id ?? ''),
      depth,
      status: statuses.get(id) ?? 'running',
      sourceId: event.source.id,
      workflow: typeof event.data?.workflow === 'string' ? event.data.workflow : undefined,
      agentId: typeof event.data?.agent_id === 'string' && event.data.agent_id ? event.data.agent_id : undefined,
    })
    for (const child of childrenOf.get(id) ?? []) walk(child, depth + 1)
  }
  // Journal returns oldest-first; show newest runs first.
  for (const root of roots.reverse()) walk(root, 0)
  return rows
}

/** Expandable event detail. For model.completed rows the response text (from
 * the run's model.text_delta/model.reasoning events) is shown as the primary
 * content; the observability payload is reduced to what the summary line
 * doesn't already say. */
function EventDetail({ event, t, project, response, reasoning }: { event: ObservationEvent; t: (key: string, vars?: Record<string, string>) => string; project: string; response?: string; reasoning?: string }) {
  const [expanded, setExpanded] = useState(false)
  const d = event.data ?? {}

  // Human-readable failure explanation: Temporal wraps the real cause into
  // "child workflow execution error (…): activity error (…): cause" — peel it
  // and show the cause, the activity chain and a child-run link when present.
  const rawError = typeof d.error === 'string' && d.error.trim() ? d.error : null
  const explained = rawError ? explainKernelError(rawError) : null
  const childRunId = typeof d.child_run_id === 'string' && d.child_run_id ? d.child_run_id : null
  const isFailure = event.type === 'run.failed' || event.type === 'model.failed' || event.type === 'tool.failed' || event.type === 'delegation.failed' || event.type === 'mcp.call.failed'

  // Show useful fields first, skip noise
  const skipFields = new Set(['caused_by', 'frame_id', 'parent_frame_id', 'sequence'])
  if (event.type === 'model.completed') {
    // tokens/latency/tools are already in the row summary; refs, retries and
    // token splits are plumbing nobody reads.
    for (const k of ['turn', 'total_tokens', 'latency_ms', 'tool_call_count', 'finish_reason', 'model', 'provider', 'prompt_tokens', 'completion_tokens', 'input_ref', 'output_ref', 'truncated', 'attempts']) skipFields.add(k)
  }
  if (rawError && explained) skipFields.add('error') // shown in the explanation block
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
      {/* Failure explanation — what a human actually needs to read */}
      {isFailure && explained && (explained.chain.length > 0 || explained.timeout) && (
        <div className="run-failure-cause">
          <div className="run-failure-cause-title">{t('errors.cause')}</div>
          <div className="run-failure-cause-text">{explained.cause}</div>
          {explained.timeout && (
            <div className="run-failure-cause-hint">{t('errors.model_timeout_hint')}</div>
          )}
          {explained.chain.length > 0 && (
            <div className="run-failure-cause-chain">{explained.chain.join(' → ')}</div>
          )}
          {rawError && rawError !== explained.cause && (
            <details className="run-failure-raw">
              <summary>{t('errors.raw_error')}</summary>
              <div>{rawError}</div>
            </details>
          )}
        </div>
      )}
      {/* Child run link — delegate failures point at the crashed subagent run */}
      {childRunId && (
        <a className="run-failure-child-link" href={`/agents?project=${encodeURIComponent(project)}&run=${encodeURIComponent(childRunId)}`}>
          ↗ {t('runs.event.open_child_run')} <span className="run-failure-child-id">{childRunId}</span>
        </a>
      )}
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
                  {/* Older agent.summary events carry the answer newline-collapsed — restore its markdown structure */}
                  <div style={{ color: 'var(--tm-text)', background: 'var(--tm-elevated)', padding: '0.5rem', borderRadius: '3px', maxHeight: '150px', overflowY: 'auto' }}><Markdown content={restoreMarkdownLines(d.answer.slice(0, 2000))} /></div>
                </div>
              )}
              {/* Show other data */}
              {fields.map(([k, v]) => (
                <div key={k} style={{ marginBottom: '0.2rem' }}>
                  <span style={{ color: 'var(--tm-text-3)' }}>{k}: </span>
                  <span style={{ color: 'var(--tm-text)', overflowWrap: 'anywhere' }}>
                    {/* Tool outputs arrive JSON-encoded (MCP sometimes doubly
                     * so) — unwrap the string layers for readability. */}
                    {typeof v === 'object' ? JSON.stringify(v).slice(0, 200) : (k === 'output' ? unwrapJsonString(String(v)) : String(v)).slice(0, 200)}
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
  const [rows, setRows] = useState<RunRow[]>([])
  // Read the run from the URL up front (links from other panels point at
  // /agents?project=…&run=…) — an initializer, not an effect, so the first
  // loadRuns call sees it through its closure and skips auto-select.
  const [selected, setSelected] = useState(() => new URLSearchParams(window.location.search).get('run') ?? '')
  // loadRuns reads the current selection through a ref so its identity doesn't
  // change (and re-trigger the list effect) every time the user picks a run.
  const selectedRef = useRef(selected)
  useEffect(() => { selectedRef.current = selected }, [selected])
  const [agents, setAgents] = useState<Agent[]>([])
  const [timeline, setTimeline] = useState<ObservationEvent[]>([])
  const [trajectory, setTrajectory] = useState<any>(null)
  const [runCostData, setRunCostData] = useState<RunCost | null>(null)
  const [result, setResult] = useState<Record<string, unknown> | null>(null)
  const [reason, setReason] = useState('Reviewed in Agent Runs UI')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [filter, setFilter] = useState('')
  // Runs with an open human question (human_request entity, §28) — marks the
  // asking run in the list so the inbox is navigable. Older kernels without
  // the endpoint simply show no markers.
  const [questionRuns, setQuestionRuns] = useState<Set<string>>(new Set())
  // Historical knowledge events (recorded before propositions were embedded in
  // events) carry bare knowledge ids — resolve them against the project list.
  const [knowledgeById, setKnowledgeById] = useState<Map<string, string>>(new Map())
  const pending = useMemo(() => pendingApprovals(timeline), [timeline])
  // A run row stays visible when it matches the filter or one of its delegated
  // children does, so searching for a child keeps its parent chain.
  const visibleRuns = useMemo(() => {
    const childrenOf = new Map<string, string[]>()
    for (const row of rows) {
      if (!row.parent) continue
      const list = childrenOf.get(row.parent) ?? []
      list.push(row.id)
      childrenOf.set(row.parent, list)
    }
    const selfMatch = new Map<string, boolean>()
    for (const row of rows) selfMatch.set(row.id, matchesFilter(filter, row.id, row.title))
    const subtreeMatch = (id: string): boolean => {
      const match = selfMatch.get(id) ?? false
      if (match) return true
      return (childrenOf.get(id) ?? []).some((child) => subtreeMatch(child))
    }
    return rows.filter((row) => subtreeMatch(row.id))
  }, [rows, filter])
  const selectedLive = useMemo(() =>
    timeline.some((event) => event.type === 'run.started' && runID(event) === selected)
      && !timeline.some((event) => (event.type === 'run.completed' || event.type === 'run.failed') && runID(event) === selected),
    [timeline, selected])
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

  const loadRuns = useCallback(async (autoSelect = false, silent = false) => {
    if (!project.trim()) return
    if (!autoSelect && !silent) setBusy(true)
    if (!silent) setError('')
    try {
      const startedPage = await observationApi.events(project.trim(), undefined, undefined, undefined, { type: 'run.started', limit: 500 })
      // Terminal events give every list row (roots and delegated children
      // alike) its status tag without opening each run.
      const [completedPage, failedPage] = await Promise.all([
        observationApi.events(project.trim(), undefined, undefined, undefined, { type: 'run.completed', limit: 500 }).catch(() => ({ events: [] as ObservationEvent[], count: 0 })),
        observationApi.events(project.trim(), undefined, undefined, undefined, { type: 'run.failed', limit: 500 }).catch(() => ({ events: [] as ObservationEvent[], count: 0 })),
      ])
      const statuses = new Map<string, RunStatus>()
      for (const event of failedPage.events) statuses.set(runID(event), 'failed')
      for (const event of completedPage.events) statuses.set(runID(event), 'completed')
      const allRows = buildRunRows(startedPage.events, statuses)
      setRows(allRows)
      // Open human questions mark the asking runs (best-effort — the badge
      // in the header falls back to events when the endpoint is missing).
      try {
        const open = await workspaceApi.listHumanRequests({ project: project.trim(), onlyOpen: true })
        setQuestionRuns(new Set((open.requests ?? []).map((request) => request.run_id)))
      } catch { setQuestionRuns(new Set()) }
      const rootRuns = allRows.filter((row) => row.depth === 0)
      // Only auto-select on initial load when nothing is selected
      const currentSelected = selectedRef.current
      if (autoSelect && !currentSelected && rootRuns.length) {
        let found = ''
        for (const row of rootRuns) {
          const query = new URLSearchParams({ project: project.trim(), source_id: row.sourceId })
          let response = await fetch(`${KERNEL_API}/v1/agent/runs/${encodeURIComponent(row.id)}?${query}`, { headers: authHeaders() })
          if (!response.ok && row.workflow === 'LeadCoderReviewerQA') {
            response = await fetch(`${KERNEL_API}/v1/agent/examples/lead-coder-reviewer-qa/runs/${encodeURIComponent(row.id)}?${query}`, { headers: authHeaders() })
          }
          if (response.ok) { found = row.id; break }
        }
        setSelected(found || rootRuns[0]?.id || '')
      }
    } catch (failure) { if (!silent) setError(message(failure)) }
    finally { if (!autoSelect && !silent) setBusy(false) }
  }, [project])

  const loadRun = useCallback(async (run: string, silent = false) => {
    if (!project.trim() || !run) return
    if (!silent) setBusy(true)
    setError('')
    // Drop extras from the previously selected run while loading.
    setTrajectory(null)
    setRunCostData(null)
    try {
      const page = await observationApi.events(project.trim(), undefined, undefined, undefined, { run, limit: 500 })
      const root = page.events.find((event) => event.type === 'run.started')
      const teamRun = root?.data?.workflow === 'LeadCoderReviewerQA'
      let allEvents = page.events
      if (teamRun) {
        const childIDs = page.events
          .filter((event) => event.type === 'delegation.started' || event.type === 'plan.task.started')
          .map((event) => String(event.data?.child_run_id ?? ''))
          .filter(Boolean)
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
      // Token/cost totals for the run (cached prompt tokens billed at half
      // price); stays zero until model prices are configured.
      try {
        setRunCostData(await runCost(project.trim(), run))
      } catch { /* totals stay hidden */ }
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
  // Silent list refresh: keeps status tags and newly started runs (including
  // delegated children) fresh without touching the current selection.
  useEffect(() => {
    const timer = window.setInterval(() => { void loadRuns(true, true) }, 15000)
    return () => window.clearInterval(timer)
  }, [loadRuns])
  // Agents power the delegation tree node names; they rarely change.
  useEffect(() => {
    workspaceApi.listAllAgents().then((page) => setAgents(page.agents)).catch(() => { /* names fall back to ids */ })
  }, [])
  // Knowledge propositions resolve historical knowledge.used/hint.* events.
  useEffect(() => {
    const projectID = project.trim()
    if (!projectID) { setKnowledgeById(new Map()); return }
    observationApi.knowledge(projectID)
      .then((page) => {
        const map = new Map<string, string>()
        for (const item of page.knowledge ?? []) map.set(item.id, item.proposition)
        setKnowledgeById(map)
      })
      .catch(() => { /* bare ids stay as the fallback */ })
  }, [project])

  async function decide(event: ObservationEvent, approved: boolean, response = '') {
    const operationID = String(event.data?.operation_id ?? '')
    setBusy(true); setError('')
    try {
      const root = timeline.find((item) => item.type === 'run.started' && runID(item) === selected)
      const operation = event.data?.operation as Record<string, unknown> | undefined
      const approvalQuery = new URLSearchParams({ project: project.trim(), source_id: root?.source.id ?? '' })
      const response2 = await fetch(`${KERNEL_API}/v1/agent/runs/${encodeURIComponent(selected)}/approval?${approvalQuery}`, {
        method: 'POST', headers: { 'Content-Type': 'application/json', ...authHeaders() },
        body: JSON.stringify({ operation_id: operationID, arguments_hash: operation?.arguments_hash ?? String(event.data?.arguments_hash ?? ''), approved, actor_id: 'human-ui', reason: reason.trim(), response: response.trim() }),
      })
      if (!response2.ok) throw new Error(`${response2.status} ${await response2.text()}`)
      await loadRun(selected, true)
      // The answered question no longer marks the run — refresh the open set.
      try {
        const open = await workspaceApi.listHumanRequests({ project: project.trim(), onlyOpen: true })
        setQuestionRuns(new Set((open.requests ?? []).map((request) => request.run_id)))
      } catch { /* best-effort */ }
    } catch (failure) { setError(message(failure)) }
    finally { setBusy(false) }
  }

  // Prefer the run result (REST); evicted child runs have none — fall back
  // to the agent.summary event, which survives in the journal. Older summary
  // events are newline-collapsed, so restore the markdown structure.
  const summaryAnswer = (() => {
    const raw = timeline.find((e) => e.type === 'agent.summary')?.data
    const value = (raw as Record<string, unknown> | undefined)?.answer
    return typeof value === 'string' && value.trim() ? restoreMarkdownLines(value) : undefined
  })()
  const answer = ((result?.result as Record<string, unknown> | undefined)?.answer as string | undefined) ?? summaryAnswer
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
              {rows.length === 0 && <Tile style={{ color: 'var(--tm-text-3)', textAlign: 'center' }}>{t('runs.no_runs') ?? 'No runs for this project'}</Tile>}
              {visibleRuns.map((row) => {
                const agentName = row.agentId ? agents.find((a) => a.id === row.agentId)?.name : undefined
                const label = row.title || agentName || (row.depth > 0 ? `#${row.id.split('/').pop() ?? ''}` : row.id)
                return (
                <Tile
                  key={row.id}
                  onClick={() => setSelected(row.id)}
                  className={`workspace-tile ${selected === row.id ? 'selected' : ''}`}
                  style={{ padding: '0.5rem 0.75rem', cursor: 'pointer', minWidth: 0, overflow: 'hidden', marginLeft: row.depth > 0 ? `${row.depth * 0.75}rem` : undefined }}
                >
                  <div style={{ fontWeight: 500, fontSize: '0.8rem', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                    {row.depth > 0 && <span style={{ color: 'var(--tm-text-3)', marginRight: '0.25rem' }}>↳</span>}
                    {label}
                  </div>
                  <div style={{ display: 'flex', alignItems: 'center', gap: '0.35rem', fontSize: '0.7rem', color: 'var(--tm-text-3)' }}>
                    <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{row.role || 'agent'} · {shortTime(row.at)}</span>
                    {row.status === 'completed' && <Tag type="green" size="sm">{t('chat.delegation.completed')}</Tag>}
                    {row.status === 'failed' && <Tag type="red" size="sm">{t('chat.delegation.failed')}</Tag>}
                    {row.status === 'running' && <Tag type="blue" size="sm">{t('chat.delegation.running')}</Tag>}
                    {questionRuns.has(row.id) && <Tag type="warm-gray" size="sm" title={t('runs.asks_human') ?? 'This run is waiting for a human answer'}>❓ {t('runs.asks_human') ?? 'asks you'}</Tag>}
                  </div>
                  {(row.title || agentName) && (
                    <div style={{ fontSize: '0.65rem', color: 'var(--tm-muted)', fontFamily: 'monospace', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{row.id}</div>
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

                {/* Pending approvals and human questions */}
                {pending.map((event) => {
                  const details = event.data?.details as Record<string, unknown> | undefined
                  const operation = event.data?.operation as Record<string, unknown> | undefined
                  const operationArgs = operation?.arguments as Record<string, unknown> | undefined
                  const command = operationArgs?.command
                  const risk = event.data?.risk as Record<string, unknown> | undefined
                  const isHuman = event.type === 'human.requested'
                  const options = Array.isArray(event.data?.options) ? (event.data?.options as unknown[]).map((o) => String(o)) : []
                  return (
                    <Tile key={event.event_id} style={{ borderLeft: `3px solid ${isHuman ? 'var(--tm-amber)' : '#e6b85c'}` }}>
                      <Heading style={{ fontSize: '0.875rem' }}>{isHuman ? `❓ ${t('runs.human_question') ?? 'Agent asks'}` : `⚠ ${t('runs.approval_required') ?? 'Approval Required'}`}</Heading>
                      {isHuman ? (
                        <>
                          <p style={{ fontSize: '0.9rem', margin: '0.5rem 0' }}>{String(event.data?.question ?? '')}</p>
                          {Boolean(event.data?.context) && (
                            <p style={{ fontSize: '0.8rem', color: 'var(--tm-text-2)', margin: '0 0 0.5rem', whiteSpace: 'pre-wrap' }}>{String(event.data?.context)}</p>
                          )}
                          								{options.length > 0 && (
                          									<Stack orientation="horizontal" gap={2} style={{ marginTop: '0.5rem', flexWrap: 'nowrap', overflowX: 'auto', maxWidth: '100%', paddingBottom: '0.25rem' }}>
                          										{options.map((option) => (
                          											<Button key={option} size="sm" style={{ flex: '0 0 auto', whiteSpace: 'nowrap' }} title={option} onClick={() => void decide(event, true, option)} disabled={busy}>{option}</Button>
                          										))}
                          									</Stack>
                          								)}
                          <div style={{ marginTop: '0.75rem' }}>
                            <TextInput id="human-answer" labelText={t('runs.answer_label') ?? 'Your answer'} value={reason} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setReason(e.target.value)} placeholder={t('runs.answer_placeholder') ?? 'Type a free-form answer…'} />
                          </div>
                          <Stack orientation="horizontal" gap={2} style={{ marginTop: '0.5rem' }}>
                            <Button onClick={() => void decide(event, true)} disabled={busy}>{t('runs.send_answer') ?? 'Send'}</Button>
                            <Button kind="secondary" onClick={() => void decide(event, false)} disabled={busy}>{t('runs.cancel_question') ?? 'Cancel question'}</Button>
                          </Stack>
                        </>
                      ) : (
                        <>
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
                        </>
                      )}
                    </Tile>
                  )
                })}

                {/* Cross-agent knowledge disputes across the selected run's chain */}
                <RunDisputes project={project.trim()} selected={selected} rows={rows} onOpenRun={setSelected} />

                {/* Delegation call tree — children runs with live status */}
                <DelegationTree project={project.trim()} runId={selected} agents={agents} live={selectedLive} onOpenRun={setSelected} />

                {/* Plan DAG view — task graph of plan calls with live statuses */}
                <PlanGraph project={project.trim()} runId={selected} agents={agents} live={selectedLive} onOpenRun={setSelected} />

                {/* Timeline — the main trace view */}
                <Tile>
                  <Heading style={{ fontSize: '0.875rem', marginBottom: '0.5rem' }}>{t('runs.trace') ?? 'Trace'} ({t('runs.events_count', { count: String(trace.visible.length) })})</Heading>
                  <Stack gap={0}>
                    {[...trace.visible].reverse().map((event) => {
                      const info = eventSummary(event, t, (id) => knowledgeById.get(id))
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
                            <EventDetail event={event} t={t} project={project.trim()} response={turnKey ? trace.responseByKey.get(turnKey) : undefined} reasoning={turnKey ? trace.reasoningByKey.get(turnKey) : undefined} />
                          </div>
                        </div>
                      )
                    })}
                  </Stack>
                </Tile>

                {/* Trajectory extraction & run cost totals */}
                {(trajectory || runCostData) && (
                  <Tile>
                    <Heading style={{ fontSize: '0.875rem', marginBottom: '0.75rem' }}>{t('runs.trajectory') ?? 'Trajectory'}</Heading>
                    {/* Summary */}
                    <div style={{ display: 'flex', gap: '1rem', flexWrap: 'wrap', marginBottom: '0.75rem', fontSize: '0.8rem' }}>
                      {trajectory && (<>
                      <span><strong>{trajectory.summary?.total_turns ?? 0}</strong> {t('runs.turns') ?? 'turns'}</span>
                      <span><strong>{trajectory.summary?.total_tools ?? 0}</strong> {t('runs.tool_calls') ?? 'tool calls'}</span>
                      <span><strong>{trajectory.summary?.total_tokens ?? 0}</strong> {t('runs.tokens') ?? 'tokens'}</span>
                      <span><strong>{trajectory.summary?.knowledge_formed ?? 0}</strong> {t('runs.learned') ?? 'learned'}</span>
                      <span><strong>{trajectory.summary?.knowledge_recalled ?? 0}</strong> {t('runs.recalled') ?? 'recalled'}</span>
                      {trajectory.summary?.failed_tools > 0 && <span style={{ color: '#f7768e' }}><strong>{trajectory.summary.failed_tools}</strong> {t('runs.failed') ?? 'failed'}</span>}
                      </>)}
                      {runCostData && runCostData.total_cost_usd > 0 && (
                        <span><strong>${runCostData.total_cost_usd.toFixed(4)}</strong> {t('runs.cost') ?? 'cost'}</span>
                      )}
                      {runCostData && (runCostData.total_cached_tokens ?? 0) > 0 && (
                        <span>↻ <strong>{(runCostData.total_cached_tokens ?? 0).toLocaleString()}</strong> {t('runs.cached') ?? 'cached'}</span>
                      )}
                    </div>
                    {trajectory && (<>
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
                    </>)}
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
