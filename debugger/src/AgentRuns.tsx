import { useCallback, useEffect, useMemo, useRef, useState, Fragment } from 'react'
import { API_BASE, authHeaders } from './api'
import { observationApi, type KnowledgeItem, type ObservationEvent } from './observationApi'
import { workspaceApi, type Agent } from './workspaceApi'
import {
  buildRunRows,
  fetchRunList,
  fetchRunResult,
  fetchRunTrajectory,
  isAbortError,
  mergeRunEntries,
  openTabLoads,
  selectRunEvents,
  type RunListEntry,
  type RunRow,
} from './runsLoad'
import DelegationTree from './DelegationTree'
import PlanGraph from './PlanGraph'
import RunDisputes from './RunDisputes'
import Markdown from './Markdown'
import { useT, useI18n, pluralT } from './i18n'
import { eventSummary, shortTime, explainKernelError, restoreMarkdownLines, unwrapJsonString, formatDuration, approvalHint } from './eventSummary'
import { humanFactorsOf, type RunHumanFactors } from './humanFactors'
import { useRunTokenMetrics } from './tokenStream'
import { subscribeRunJournal } from './journalStream'
import {
  Button,
  TextInput,
  InlineNotification,
  Tag,
  Tile,
  Grid,
  Column,
  Stack,
  Section,
  Heading,
} from '@carbon/react'
import PageLoading from './PageLoading'
import { Time, Play, ChevronDown, ChevronRight } from '@carbon/icons-react'
import ListFilter, { matchesFilter } from './ListFilter'
import {
  runCost, taskCost, type RunCost, type TaskCost,
  operations as fetchOperations, reconcile, canReconcile, whoami,
  type UncertainOperation, type ReconcileEffect, type Whoami,
} from './kernelApi'

const KERNEL_API = '/kernel-api'
function message(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }
function json(value: unknown) { return JSON.stringify(value, null, 2) }
function runID(event: ObservationEvent) { return event.context?.run ?? '' }
function pendingApprovals(events: ObservationEvent[]) {
  const ended = new Set(events.filter((event) => ['approval.granted', 'approval.rejected', 'approval.timed_out', 'human.answered', 'human.cancelled', 'human.timed_out', 'human.rejected'].includes(event.type)).map((event) => event.data?.operation_id))
  return events.filter((event) => (event.type === 'approval.requested' || event.type === 'human.requested') && !ended.has(event.data?.operation_id))
}

type RunView = 'overview' | 'trace' | 'trajectory' | 'budget'
const RUN_VIEWS: RunView[] = ['overview', 'trace', 'trajectory', 'budget']

/** Trace window (NFR-5): the timeline renders a bounded window of the newest
 *  rows — 50 at first, extended 50 per explicit request — so a 4000-event run
 *  never materializes thousands of DOM nodes; heavy texts (model responses)
 *  render only when a row is expanded. */
export const TRACE_WINDOW = 50
export const TRACE_WINDOW_STEP = 50

/** Newest `limit` rows of `rows`, newest first (the trace renders in this
 *  order; index 0 stays the live-model row). Exported so the NFR-5 window
 *  bounds are pinned by a unit test instead of by review. */
export function windowedTrace<T>(rows: readonly T[], limit: number): T[] {
  const bounded = Math.max(0, limit)
  return rows.slice(Math.max(0, rows.length - bounded)).reverse()
}

/** Inline doubt actions for knowledge referenced by a run: challenge keeps the
 *  item visible but flagged (hints carry a caution), invalidation retires it.
 *  Lives next to where the reader noticed the problem — the run itself. */
function KnowledgeActions({ knowledgeID, project, actorID, onDone }: { knowledgeID: string; project: string; actorID: string; onDone: () => void }) {
  const t = useT()
  const [open, setOpen] = useState(false)
  const [reason, setReason] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  async function act(kind: 'challenge' | 'invalidate') {
    if (!reason.trim()) return
    setBusy(true); setError('')
    try {
      const actor = { id: actorID, type: 'human' }
      const body = { knowledge_id: knowledgeID, project, actor, reason: reason.trim() }
      if (kind === 'challenge') await observationApi.challenge(body)
      else await observationApi.invalidate(body)
      setReason(''); setOpen(false); onDone()
    } catch (failure) { setError(message(failure)) }
    finally { setBusy(false) }
  }
  if (!open) {
    return (
      <button onClick={() => setOpen(true)} style={{ background: 'none', border: '1px dashed var(--tm-border)', color: 'var(--tm-text-3)', cursor: 'pointer', fontSize: '0.7rem', padding: '0.1rem 0.45rem', lineHeight: 1.5, borderRadius: '4px', marginTop: '0.3rem' }}>
        ⚑ {t('runs.knowledge_doubt') ?? 'Doubt this knowledge'}
      </button>
    )
  }
  return (
    <div style={{ marginTop: '0.4rem', display: 'flex', gap: '0.5rem', alignItems: 'flex-end', flexWrap: 'wrap' }}>
      <div style={{ minWidth: '220px', flex: 1 }}>
        <TextInput
          id={`doubt-${knowledgeID}`}
          size="sm"
          labelText={t('knowledge.reason') ?? 'Reason'}
          placeholder={t('runs.knowledge_reason_placeholder') ?? 'Why does this knowledge look wrong?'}
          value={reason}
          onChange={(e: React.ChangeEvent<HTMLInputElement>) => setReason(e.target.value)}
        />
      </div>
      <Button size="sm" kind="secondary" disabled={busy || !reason.trim()} onClick={() => void act('challenge')}>{t('runs.knowledge_challenge') ?? 'Challenge'}</Button>
      <Button size="sm" kind="danger" disabled={busy || !reason.trim()} onClick={() => void act('invalidate')}>{t('runs.knowledge_invalidate') ?? 'Invalidate'}</Button>
      <Button size="sm" kind="ghost" onClick={() => setOpen(false)}>✕</Button>
      {error && <div style={{ width: '100%', fontSize: '0.75rem', color: '#f7768e' }}>{error}</div>}
    </div>
  )
}

/** Tool metadata for reconcile cards (from the retired Operations page). */
function toolInfo(tool: string, server?: string): { icon: string; label: string; category: string } {
  if (tool.startsWith('mcp__')) {
    const name = tool.replace('mcp__', '')
    return { icon: '🔌', label: name, category: server ? `MCP · ${server}` : 'MCP tool' }
  }
  if (tool === 'run_command') return { icon: '⚡', label: 'Shell command', category: 'Sandbox' }
  if (tool === 'write_file') return { icon: '📝', label: 'Write file', category: 'Sandbox' }
  if (tool === 'read_file') return { icon: '📄', label: 'Read file', category: 'Sandbox' }
  if (tool === 'search') return { icon: '🔍', label: 'Search', category: 'Sandbox' }
  return { icon: '🔧', label: tool, category: 'Tool' }
}

/** Extract turn number from operation_id like ".../turn/05/call_..." */
function turnOf(opId: string): string | null {
  const match = opId.match(/turn\/(\d+)/)
  return match ? match[1] : null
}

/** Tool-specific hint: where to verify whether the effect landed. */
function whereToLook(op: UncertainOperation, t: (key: string) => string): { icon: string; text: string } {
  if (op.tool === 'run_command')
    return { icon: '⚡', text: t('operations.look.sandbox') }
  if (op.tool === 'write_file' || op.tool === 'read_file' || op.tool === 'search')
    return { icon: '📝', text: t('operations.look.files') }
  if (op.tool.startsWith('mcp__'))
    return { icon: '🔌', text: t('operations.look.mcp') }
  return { icon: '🔎', text: t('operations.look.default') }
}

const REASON_KEYS: Record<string, string> = {
  crash_window: 'operations.reason.crash_window',
  stale_in_flight: 'operations.reason.stale_in_flight',
  failed_uncertain: 'operations.reason.failed_uncertain',
}

/** Expandable event detail. For model.completed rows the response text (from
 * the run's model.text_delta/model.reasoning events) is shown as the primary
 * content; the observability payload is reduced to what the summary line
 * doesn't already say. */
function EventDetail({ event, t, project, response, reasoning, knowledgeDoubt }: { event: ObservationEvent; t: (key: string, vars?: Record<string, string>) => string; project: string; response?: string; reasoning?: string; knowledgeDoubt?: React.ReactNode }) {
  const [expanded, setExpanded] = useState(false)
  const d = event.data ?? {}
  // Knowledge-bearing events (knowledge.*, hint.*) can be doubted in place:
  // the reader is looking at the exact moment the knowledge was used/formed.
  const knowledgeID = typeof d.knowledge_id === 'string' && d.knowledge_id
    && (event.type.startsWith('knowledge.') || event.type.startsWith('hint.')) ? d.knowledge_id : ''

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
      {/* Model response/reasoning are the heaviest nodes on the trace (a turn
          can run to thousands of tokens): NFR-5 renders them only once the row
          is expanded — the summary line above carries the headline either way. */}
      {(fields.length > 0 || Boolean(response) || Boolean(reasoning)) && (
        <>
          <button
            onClick={() => setExpanded(!expanded)}
            style={{ background: 'none', border: 'none', color: 'var(--tm-text-3)', cursor: 'pointer', fontSize: '0.7rem', padding: '0.15rem 0', display: 'flex', alignItems: 'center', gap: '0.25rem' }}
          >
            {expanded ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
            {expanded ? t('runs.hide_details') : t('runs.show_details')}
          </button>
          {expanded && (
            <>
              {response && (
                <div className="run-model-response" style={{ marginTop: '0.35rem' }}>
                  <Markdown content={response} />
                </div>
              )}
              {reasoning && (
                <details className="run-model-reasoning" style={{ marginTop: '0.35rem' }}>
                  <summary>💭 {t('runs.reasoning')}</summary>
                  <div className="run-model-reasoning-content">{reasoning}</div>
                </details>
              )}
              {fields.length > 0 && (
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
        </>
      )}
      {knowledgeID && knowledgeDoubt}
    </div>
  )
}

export default function AgentRuns({ project }: { project: string }) {
  const t = useT()
  const { locale } = useI18n()
  // The batch list (GET /v1/agent/runs) accumulates here: a fresh page
  // updates known runs, rows the reader explicitly loaded («show older»)
  // survive every silent refresh. Rows are derived — no journal scans.
  const [entries, setEntries] = useState<Map<string, RunListEntry>>(() => new Map())
  const rows = useMemo(() => buildRunRows([...entries.values()]), [entries])
  /** Cursor of the next older batch page (empty = everything loaded). */
  const [listNext, setListNext] = useState('')
  /** K-3: terminal statuses failed to resolve — the list still renders, this
   *  flag raises the «statuses unavailable» indicator; the reason is logged
   *  server-side and in the console instead of being swallowed. */
  const [listPartial, setListPartial] = useState(false)
  const [listPartialError, setListPartialError] = useState('')
  const [loadingMore, setLoadingMore] = useState(false)
  /** Per-request abort scopes (NFR-4): every load owns a controller; a new
   *  load of the same kind cancels the in-flight one, unmount cancels all. */
  const listAbortRef = useRef<AbortController | null>(null)
  const runAbortRef = useRef<AbortController | null>(null)
  const resultAbortRef = useRef<AbortController | null>(null)
  const trajectoryAbortRef = useRef<AbortController | null>(null)
  const budgetAbortRef = useRef<AbortController | null>(null)
  const unmountRef = useRef(new AbortController())
  /** Per-run guards of the view-lazy loaders: one fetch per run per view —
   *  timeline updates (poll/SSE) never re-trigger them (NFR-2 laziness). */
  const resultRunRef = useRef('')
  const trajectoryRunRef = useRef('')
  const budgetRunRef = useRef('')
  /** Identity resolves once, and only when the reconcile card needs it. */
  const identityTriedRef = useRef(false)
  // NFR-6: polling and SSE pause while the document is hidden.
  const [pageVisible, setPageVisible] = useState(() => !document.hidden)
  useEffect(() => {
    const onChange = () => setPageVisible(!document.hidden)
    document.addEventListener('visibilitychange', onChange)
    return () => document.removeEventListener('visibilitychange', onChange)
  }, [])
  useEffect(() => () => {
    unmountRef.current.abort()
    listAbortRef.current?.abort()
    runAbortRef.current?.abort()
    resultAbortRef.current?.abort()
    trajectoryAbortRef.current?.abort()
    budgetAbortRef.current?.abort()
  }, [])
  // Read the run from the URL up front (links from other panels point at
  // /agents?project=…&run=…) — an initializer, not an effect, so the first
  // loadRuns call sees it through its closure and skips auto-select.
  const [selected, setSelected] = useState(() => new URLSearchParams(window.location.search).get('run') ?? '')
  // Detail tab (docs aside: the run page used to stack answer + approvals +
  // disputes + trees + the whole trace + trajectory + budget in one column
  // that could not be scrolled to the end). URL-initialized so a link can
  // open the trace directly.
  const [view, setView] = useState<RunView>(() => {
    const initial = new URLSearchParams(window.location.search).get('view') as RunView | null
    return initial && RUN_VIEWS.includes(initial) ? initial : 'overview'
  })
  const viewRef = useRef(view)
  useEffect(() => { viewRef.current = view }, [view])
  // The run whose extras (trajectory, budgets) are on screen; loadRun drops
  // them only when this changes (run switch), not on silent refreshes.
  const loadedRunRef = useRef('')
  // loadRuns reads the current selection through a ref so its identity doesn't
  // change (and re-trigger the list effect) every time the user picks a run.
  const selectedRef = useRef(selected)
  useEffect(() => { selectedRef.current = selected }, [selected])
  /** Agent records for the name lookups of the list and the trees: the batch
   *  list carries agent_id + agent_name per row, so no workspace request runs
   *  for display names (the old listAllAgents mount fetch is gone). Fields
   *  beyond id/name are empty — nothing else on those consumers reads them. */
  const agents = useMemo<Agent[]>(() => {
    const catalog: Agent[] = []
    const seen = new Set<string>()
    for (const entry of entries.values()) {
      if (!entry.agent_id || seen.has(entry.agent_id)) continue
      seen.add(entry.agent_id)
      catalog.push({
        id: entry.agent_id,
        name: entry.agent_name || entry.agent_id,
        description: '',
        model: '',
        provider: '',
        system_prompt: '',
        skills: [],
        mcp_servers: [],
        approval_mode: '',
        labels: {},
        created_at: '',
        updated_at: '',
        // Display-only catalog rows: the sandbox profile of an agent is a
        // server-side field nothing in the name/tree lookups reads.
        sandbox_profile: '',
      })
    }
    return catalog
  }, [entries])
  const [timeline, setTimeline] = useState<ObservationEvent[]>([])
  const [trajectory, setTrajectory] = useState<any>(null)
  // Human factors (§7): what this session cost its humans — derived from the
  // events on screen (the merged timeline, including lazily expanded
  // delegated children), never a second journal pass.
  const humanFactors = useMemo<RunHumanFactors>(() => humanFactorsOf(timeline), [timeline])
  const [runCostData, setRunCostData] = useState<RunCost | null>(null)
  // Task budget spans every run of the task (follow-ups + delegation tree).
  const [taskCostData, setTaskCostData] = useState<TaskCost | null>(null)
  const [result, setResult] = useState<Record<string, unknown> | null>(null)
  const [reason, setReason] = useState('Reviewed in Agent Runs UI')
  const [busy, setBusy] = useState(false)
  // Force-stop guard: the first click asks, the second terminates.
  const [confirmStop, setConfirmStop] = useState('')
  const [error, setError] = useState('')
  const [filter, setFilter] = useState('')
  // Runs with an open human question (human_request entity, §28) — marks the
  // asking run in the list so the inbox is navigable. Older kernels without
  // the endpoint simply show no markers.
  const [questionRuns, setQuestionRuns] = useState<Set<string>>(new Set())
  // Unresolved reconcile operations grouped by run (docs/failure-
  // reconciliation.md). The verdict UI lives next to the trace that produced
  // the uncertainty instead of a standalone page.
  const [opsByRun, setOpsByRun] = useState<Map<string, UncertainOperation[]>>(new Map())
  const [opsOnly, setOpsOnly] = useState(false)
  const [identity, setIdentity] = useState<Whoami | null>(null)
  const [reconcileNote, setReconcileNote] = useState('')
  // Historical knowledge events (recorded before propositions were embedded in
  // events) carry bare knowledge ids — resolve them against the project list.
  // Full items (not just propositions): the run's knowledge rows also show the
  // current lifecycle state, which changes when the reader doubts one.
  const [knowledgeItems, setKnowledgeItems] = useState<Map<string, KnowledgeItem>>(new Map())
  const [knowledgeRevision, setKnowledgeRevision] = useState(0)
  const pending = useMemo(() => pendingApprovals(timeline), [timeline])
  // Ancestors of runs with unresolved ops light up too, so the delegation
  // chain points straight at the failure; subtree counts feed the row tags.
  const opRunIds = useMemo(() => {
    const ids = new Set(opsByRun.keys())
    const direct = new Map<string, number>()
    for (const [id, list] of opsByRun) direct.set(id, list.length)
    const parentOf = new Map<string, string>()
    const childrenOf = new Map<string, string[]>()
    for (const row of rows) {
      if (!row.parent) continue
      parentOf.set(row.id, row.parent)
      const list = childrenOf.get(row.parent) ?? []
      list.push(row.id)
      childrenOf.set(row.parent, list)
    }
    const subtree = (id: string): number => {
      let total = direct.get(id) ?? 0
      for (const child of childrenOf.get(id) ?? []) total += subtree(child)
      return total
    }
    for (const id of [...ids]) {
      let parent = parentOf.get(id)
      while (parent) { ids.add(parent); parent = parentOf.get(parent) }
    }
    const subtreeCount = new Map<string, number>()
    for (const id of ids) subtreeCount.set(id, subtree(id))
    return { ids, subtreeCount }
  }, [opsByRun, rows])
  // Ops anchored to the selected run or any of its delegated children —
  // team-run traces merge child events, so their ops belong here too.
  const selectedOps = useMemo(() => {
    if (!selected) return [] as UncertainOperation[]
    const keys = new Set<string>([selected])
    const childrenOf = new Map<string, string[]>()
    for (const row of rows) {
      if (!row.parent) continue
      const list = childrenOf.get(row.parent) ?? []
      list.push(row.id)
      childrenOf.set(row.parent, list)
    }
    const walk = (id: string) => {
      for (const child of childrenOf.get(id) ?? []) { keys.add(child); walk(child) }
    }
    walk(selected)
    return [...keys].flatMap((key) => opsByRun.get(key) ?? [])
  }, [selected, rows, opsByRun])
  const opById = useMemo(() => {
    const map = new Map<string, UncertainOperation>()
    for (const list of opsByRun.values()) for (const op of list) map.set(op.operation_id, op)
    return map
  }, [opsByRun])
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
    for (const row of rows) selfMatch.set(row.id, matchesFilter(filter, row.id, row.title) && (!opsOnly || opRunIds.ids.has(row.id)))
    const subtreeMatch = (id: string): boolean => {
      const match = selfMatch.get(id) ?? false
      if (match) return true
      return (childrenOf.get(id) ?? []).some((child) => subtreeMatch(child))
    }
    return rows.filter((row) => subtreeMatch(row.id))
  }, [rows, filter, opsOnly, opRunIds])
  const selectedLive = useMemo(() =>
    timeline.some((event) => event.type === 'run.started' && runID(event) === selected)
      && !timeline.some((event) => (event.type === 'run.completed' || event.type === 'run.failed' || event.type === 'run.cancelled') && runID(event) === selected),
    [timeline, selected])
  // Live model-call progress: the journal only records the call when it
  // completes, so an in-flight "Вызов модели" row would sit silent for minutes.
  // The ephemeral token stream (same SSE the chat preview uses) fills the gap:
  // ~tokens, tok/s and a liveness marker refreshed at 1Hz.
  const [nowTick, setNowTick] = useState(() => Date.now())
  useEffect(() => {
    if (!selectedLive) return
    const timer = window.setInterval(() => setNowTick(Date.now()), 1000)
    return () => window.clearInterval(timer)
  }, [selectedLive])
  // NFR-6: the ephemeral token stream is network traffic too — it pauses
  // with the document and comes back (fresh meter) on the visible page.
  const tokenMetrics = useRunTokenMetrics(selected, selectedLive && pageVisible)
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

  /** NFR-5: a bounded render window over the trace — the newest TRACE_WINDOW
   *  rows first, extended 50 per explicit request; the DOM never carries the
   *  whole run at once. Reset on every run switch / view open. */
  const [traceLimit, setTraceLimit] = useState(TRACE_WINDOW)
  useEffect(() => { setTraceLimit(TRACE_WINDOW) }, [selected, view])
  const traceWindow = useMemo(() => windowedTrace(trace.visible, traceLimit), [trace, traceLimit])

  // NFR-3: delegated children never preload their events (the old
  // Promise.all over child runs is gone). A meta row lists each child with
  // the status from the batch list; its events load on request through the
  // same per-run cache as the selection timeline.
  const childRuns = useMemo(() => {
    const seen = new Map<string, string>()
    for (const event of timeline) {
      if (event.type !== 'delegation.started' && event.type !== 'plan.task.started') continue
      const child = String(event.data?.child_run_id ?? '')
      if (!child || child === selected || seen.has(child)) continue
      seen.set(child, event.type === 'plan.task.started' ? 'plan' : 'delegation')
    }
    return [...seen.entries()].map(([childRunId, kind]) => ({ childRunId, kind }))
  }, [timeline, selected])
  const [childLoading, setChildLoading] = useState('')
  const childEventsLoaded = (childRunId: string) => timeline.some((event) => runID(event) === childRunId)
  const loadChildEvents = async (childRunId: string) => {
    setChildLoading(childRunId)
    try {
      const childEvents = await selectRunEvents(childRunId, { signal: unmountRef.current.signal })
      setTimeline((prev) => {
        const seen = new Set(prev.map((event) => `${event.source.id}:${event.event_id}`))
        const fresh = childEvents.filter((event) => !seen.has(`${event.source.id}:${event.event_id}`))
        if (!fresh.length) return prev
        return [...prev, ...fresh].sort((left, right) => left.occurred_at.localeCompare(right.occurred_at))
      })
    } catch (failure) {
      if (!isAbortError(failure)) setError(message(failure))
    } finally {
      setChildLoading('')
    }
  }

  // Re-fetch unresolved operations and group them by run — the post-verdict
  // refresh (the open path fetches its own copy inside openTabLoads). The
  // project filter is optional: the global listing spans every project the
  // caller may see (docs/activity-scoping.md §5). Failures log the reason
  // instead of silently showing an empty map; older kernels without the
  // endpoint simply show no markers.
  const refreshOps = useCallback(async () => {
    try {
      const page = await fetchOperations(project.trim() || undefined, unmountRef.current.signal)
      const map = new Map<string, UncertainOperation[]>()
      for (const op of page.operations ?? []) {
        const list = map.get(op.run_id) ?? []
        list.push(op)
        map.set(op.run_id, list)
      }
      setOpsByRun(map)
    } catch (failure) {
      if (isAbortError(failure)) return
      console.warn(`[agent-runs] reconcile operations refresh failed: ${message(failure)}`)
      setOpsByRun(new Map())
    }
  }, [project])

  // Force stop (docs/agent-delegation.md §runs ops): a two-click guard — the
  // first click asks, the second terminates the workflow and journals the
  // terminal event. Terminate lands even when the workflow is stuck failing
  // its determinism check, which a graceful cancel cannot reach.
  const stopRun = async (row: RunRow) => {
    setBusy(true)
    try {
      await workspaceApi.terminateRun(row.id, row.project ?? project ?? '')
    } catch { /* the silent refresh picks the terminal state up either way */ }
    setConfirmStop('')
    await loadRuns(true, true)
    setBusy(false)
  }

  const loadRuns = useCallback(async (autoSelect = false, silent = false) => {
    if (!autoSelect && !silent) setBusy(true)
    if (!silent) setError('')
    // One abort scope per list load: a newer load (or unmount) cancels the
    // in-flight one instead of racing its setState (NFR-4).
    listAbortRef.current?.abort()
    const controller = new AbortController()
    listAbortRef.current = controller
    try {
      // The whole list path in one round: batch page + open human questions +
      // reconcile ops — OPEN_REQUEST_BUDGET requests. No journal scans, no
      // per-row detail probes (K-2), agent names ride the rows (NFR-1).
      const loaded = await openTabLoads({ project, signal: controller.signal })
      // K-3: unresolved statuses are a visible state, never a swallowed
      // rejection — the indicator explains it, the console carries the reason.
      setListPartial(Boolean(loaded.page.partial))
      setListPartialError(loaded.page.partial_error ?? '')
      if (loaded.page.partial) {
        console.warn(`[agent-runs] run statuses unavailable (partial list): ${loaded.page.partial_error || 'no reason given'}`)
      }
      if (loaded.errors.questions) console.warn(`[agent-runs] open human requests unavailable: ${loaded.errors.questions}`)
      if (loaded.errors.ops) console.warn(`[agent-runs] reconcile operations unavailable: ${loaded.errors.ops}`)
      setQuestionRuns(new Set(loaded.questionRunIDs))
      setOpsByRun(loaded.opsByRun)
      setEntries((prev) => mergeRunEntries(prev, loaded.page.runs))
      // A refresh keeps the reader's pagination position; a fresh load takes
      // the first page's cursor.
      setListNext((prev) => (silent && prev ? prev : loaded.page.next_cursor ?? ''))
      // Auto-select the newest root of the first page: its status rides the
      // row — the per-run detail probes (K-2) are gone.
      if (autoSelect && !selectedRef.current) {
        const firstRows = buildRunRows(loaded.page.runs)
        const root = firstRows.find((row) => row.depth === 0)
        setSelected(root?.id ?? '')
      }
    } catch (failure) {
      if (isAbortError(failure)) return
      if (!silent) setError(message(failure))
      else console.warn(`[agent-runs] background list refresh failed: ${message(failure)}`)
    } finally {
      if (listAbortRef.current === controller) listAbortRef.current = null
      if (!autoSelect && !silent) setBusy(false)
    }
  }, [project])

  // «Show older runs»: the next batch page appends to the accumulated
  // entries; dedup by run_id keeps a refresh-advanced boundary harmless.
  const loadOlderRuns = useCallback(async () => {
    if (!listNext || loadingMore) return
    setLoadingMore(true)
    try {
      const page = await fetchRunList({ project, cursor: listNext, signal: unmountRef.current.signal })
      setEntries((prev) => mergeRunEntries(prev, page.runs))
      setListNext(page.next_cursor ?? '')
      if (page.partial) {
        setListPartial(true)
        setListPartialError(page.partial_error ?? '')
        console.warn(`[agent-runs] run statuses unavailable (partial list): ${page.partial_error || 'no reason given'}`)
      }
    } catch (failure) {
      if (!isAbortError(failure)) setError(message(failure))
    } finally {
      setLoadingMore(false)
    }
  }, [project, listNext, loadingMore])

  const loadRun = useCallback(async (run: string, silent = false) => {
    if (!run) return
    // Extras (result, trajectory, budgets) belong to a specific run; reset
    // them only when switching runs — the silent poll refreshes the same run
    // and must not flash the tabs' empty placeholders while refetching. The
    // view-lazy loaders below repopulate them when their view needs them
    // (NFR-2: selection itself only fetches the timeline).
    const runChanged = run !== loadedRunRef.current
    loadedRunRef.current = run
    if (!silent) setBusy(true)
    setError('')
    if (runChanged) {
      setResult(null)
      setTrajectory(null)
      setRunCostData(null)
      setTaskCostData(null)
      resultRunRef.current = ''
      trajectoryRunRef.current = ''
      budgetRunRef.current = ''
      resultAbortRef.current?.abort()
      trajectoryAbortRef.current?.abort()
      budgetAbortRef.current?.abort()
    }
    if (!silent) {
      // A fresh selection cancels the previous run's in-flight load; silent
      // polls join the cache's in-flight request instead of stacking (NFR-4).
      runAbortRef.current?.abort()
    }
    const controller = new AbortController()
    runAbortRef.current = controller
    try {
      // Exactly one journal page for a fresh run (NFR-2), cursor catch-up to
      // the run's end preserved (the old runEvents loop, NFR-4), served from
      // the per-run cache with in-flight deduplication. Events are fetched by
      // run id alone: the journal scopes the query server-side
      // (docs/activity-scoping.md §5), the run's own project comes from its
      // root event — the header filter may be the global "All projects".
      const events = await selectRunEvents(run, {
        refresh: silent,
        signal: silent ? unmountRef.current.signal : controller.signal,
      })
      const root = events.find((event) => event.type === 'run.started')
      const runProject = root?.context?.project ?? project.trim()
      setTimeline((prev) => {
        if (!silent) return events
        // The live SSE tail (and lazily expanded children) may hold events
        // the poll snapshot lacks — merge, never drop.
        const seen = new Set(events.map((e) => `${e.source.id}:${e.event_id}`))
        const extra = prev.filter((e) => !seen.has(`${e.source.id}:${e.event_id}`))
        return [...events, ...extra].sort((left, right) => left.occurred_at.localeCompare(right.occurred_at))
      })
      const query = new URLSearchParams({ project: runProject, run, view: viewRef.current })
      window.history.replaceState(null, '', `/agents?${query}`)
    } catch (failure) {
      if (isAbortError(failure)) return
      setError(message(failure))
    } finally {
      if (runAbortRef.current === controller) runAbortRef.current = null
      if (!silent) setBusy(false)
    }
  }, [project])

  useEffect(() => { void loadRuns(true) }, [loadRuns])
  useEffect(() => { if (selected) void loadRun(selected) }, [selected, loadRun])

  // ── View-lazy extras (NFR-2): selection fetches ONLY the timeline. ──────
  // Result feeds the overview (answer/stages/expired status). It needs the
  // timeline root for the run's own project + workflow, waits for it, then
  // loads once per run — poll/SSE timeline updates never re-trigger it.
  useEffect(() => {
    if (!selected || view !== 'overview') {
      resultAbortRef.current?.abort()
      resultAbortRef.current = null
      resultRunRef.current = ''
      return
    }
    if (resultRunRef.current === selected) return
    const root = timeline.find((event) => event.type === 'run.started' && runID(event) === selected)
    if (!root) return
    resultAbortRef.current?.abort()
    const controller = new AbortController()
    resultAbortRef.current = controller
    resultRunRef.current = selected
    const runProject = root.context?.project ?? project.trim()
    const teamRun = root.data?.workflow === 'LeadCoderReviewerQA'
    void fetchRunResult(selected, { project: runProject, sourceID: root.source.id, teamRun, signal: controller.signal })
      .then((payload) => { if (!controller.signal.aborted) setResult(payload) })
      .catch((failure) => {
        if (isAbortError(failure)) return
        resultRunRef.current = '' // failed once — the next view pass retries
        setError(message(failure))
      })
  }, [selected, view, timeline, project])

  // Trajectory extraction + run cost totals — trajectory view only.
  useEffect(() => {
    if (!selected || view !== 'trajectory') {
      trajectoryAbortRef.current?.abort()
      trajectoryAbortRef.current = null
      trajectoryRunRef.current = ''
      return
    }
    if (trajectoryRunRef.current === selected) return
    const root = timeline.find((event) => event.type === 'run.started' && runID(event) === selected)
    if (!root) return
    trajectoryAbortRef.current?.abort()
    const controller = new AbortController()
    trajectoryAbortRef.current = controller
    trajectoryRunRef.current = selected
    const runProject = root.context?.project ?? project.trim()
    void (async () => {
      try {
        const extracted = await fetchRunTrajectory(selected, runProject, controller.signal)
        if (controller.signal.aborted) return
        setTrajectory(extracted)
      } catch (failure) {
        if (isAbortError(failure)) return
        trajectoryRunRef.current = ''
        console.warn(`[agent-runs] trajectory load failed: ${message(failure)}`)
      }
    })()
    // Token/cost totals for the run (cached prompt tokens billed at half
    // price); stays hidden until model prices are configured.
    void runCost(runProject, selected, controller.signal)
      .then((costs) => { if (!controller.signal.aborted) setRunCostData(costs) })
      .catch((failure) => {
        if (isAbortError(failure)) return
        console.warn(`[agent-runs] run cost unavailable: ${message(failure)}`)
      })
  }, [selected, view, timeline, project])

  // Task-wide budget — budget view only; the task id lives on the root event.
  useEffect(() => {
    if (!selected || view !== 'budget') {
      budgetAbortRef.current?.abort()
      budgetAbortRef.current = null
      budgetRunRef.current = ''
      return
    }
    if (budgetRunRef.current === selected) return
    const root = timeline.find((event) => event.type === 'run.started' && runID(event) === selected)
    if (!root) return
    const taskID = root.context?.task ?? ''
    if (!taskID) { budgetRunRef.current = selected; return }
    budgetAbortRef.current?.abort()
    const controller = new AbortController()
    budgetAbortRef.current = controller
    budgetRunRef.current = selected
    const runProject = root.context?.project ?? project.trim()
    void taskCost(runProject, taskID, controller.signal)
      .then((data) => { if (!controller.signal.aborted) setTaskCostData(data) })
      .catch((failure) => {
        if (isAbortError(failure)) return
        budgetRunRef.current = ''
        console.warn(`[agent-runs] task budget unavailable: ${message(failure)}`)
      })
  }, [selected, view, timeline, project])
  // Tab switches update the URL so a trace/trajectory/budget view is
  // shareable; loadRun preserves the current tab via viewRef.
  useEffect(() => {
    if (!selected) return
    const params = new URLSearchParams(window.location.search)
    if (params.get('view') === view) return
    params.set('view', view)
    window.history.replaceState(null, '', `${window.location.pathname}?${params.toString()}`)
  }, [view, selected])
  useEffect(() => {
    if (!selected || !pageVisible) return
    // Poll only the selected run's events, don't re-fetch the full run list.
    // NFR-6: hidden document → the polling pauses (the interval re-arms on
    // visibility, the next tick refreshes within 10s).
    const timer = window.setInterval(() => { void loadRun(selected, true) }, 10000)
    return () => window.clearInterval(timer)
  }, [selected, loadRun, pageVisible])
  // Live journal tail: while the selected run is in flight, events arrive over
  // SSE at the server's ~1.5s cadence instead of the 10s poll above — without
  // it a finishing model call, its tool events and the next turn land in one
  // delayed burst and the run looks frozen while it works. Frames carry only
  // {type, occurred_at, event_id, data}; the shape the rows expect (source,
  // context) is stamped from the run's own root event. The poll stays as the
  // safety net (and covers team-run children the per-run stream skips).
  useEffect(() => {
    // NFR-6: the SSE tail pauses while the document is hidden — aborting the
    // subscription (the poll above pauses too); a visible page re-subscribes.
    if (!selected || !selectedLive || !pageVisible) return
    const controller = new AbortController()
    subscribeRunJournal(selected, (frame) => {
      if (frame.event === 'done') return
      let parsed: { type?: string; event_id?: string; occurred_at?: string; data?: Record<string, unknown> }
      try { parsed = JSON.parse(frame.data) } catch { return }
      if (!parsed.type || !parsed.event_id || !parsed.occurred_at) return
      setTimeline((prev) => {
        if (prev.some((existing) => existing.event_id === parsed.event_id)) return prev
        const root = prev.find((e) => e.type === 'run.started')
        const normalized: ObservationEvent = {
          schema: root?.schema ?? 'temporality/observation/event/v1',
          event_id: parsed.event_id!,
          occurred_at: parsed.occurred_at!,
          received_at: parsed.occurred_at!,
          source: root?.source ?? { id: 'temporality-agent-kernel', integration: 'temporality-agent-kernel' },
          context: { ...(root?.context ?? {}), run: selected },
          type: parsed.type!,
          data: parsed.data ?? {},
        }
        return [...prev, normalized].sort((left, right) => left.occurred_at.localeCompare(right.occurred_at))
      })
    }, controller.signal, { tail: true })
    return () => controller.abort()
  }, [selected, selectedLive, pageVisible])
  // Silent list refresh: keeps status tags and newly started runs (including
  // delegated children) fresh without touching the current selection.
  useEffect(() => {
    // NFR-6: the refresh interval pauses while the document is hidden and
    // re-arms on the next visible tick.
    if (!pageVisible) return
    const timer = window.setInterval(() => { void loadRuns(true, true) }, 15000)
    return () => window.clearInterval(timer)
  }, [loadRuns, pageVisible])
  // Agent display names ride the batch list rows (agent_name) — the old
  // listAllAgents mount fetch is gone; the catalog memo above feeds the trees.
  // The selected run's own project: detail endpoints (result, approval,
  // nudge, knowledge doubt actions) need it even when the header filter is the
  // global "All projects". Falls back to the filter for run ids the list
  // hasn't loaded yet. Declared before the effects that depend on it.
  const selectedProject = useMemo(
    () => rows.find((row) => row.id === selected)?.project ?? project.trim(),
    [rows, selected, project],
  )

  // Knowledge propositions resolve historical knowledge.used/hint.* events.
  // Scoped to the selected run's project (not the header filter): the global
  // listing spans projects, and the doubt actions target the run's own store.
  useEffect(() => {
    const projectID = selectedProject.trim()
    if (!projectID) { setKnowledgeItems(new Map()); return }
    // Lazy per view (NFR-2): the trace rows and the trajectory knowledge list
    // are the only consumers — the overview falls back to bare ids, so
    // selecting a run costs no knowledge request.
    const wantKnowledge = view === 'trace' || view === 'trajectory'
    if (!wantKnowledge || !selected) return
    const controller = new AbortController()
    observationApi.knowledge(projectID, undefined, undefined, { signal: controller.signal })
      .then((page) => {
        const map = new Map<string, KnowledgeItem>()
        for (const item of page.knowledge ?? []) map.set(item.id, item)
        setKnowledgeItems(map)
      })
      .catch((failure) => { if (!isAbortError(failure)) console.warn(`[agent-runs] knowledge list unavailable: ${message(failure)}`) })
    return () => controller.abort()
  }, [selectedProject, knowledgeRevision, view, selected])
  // Verdict buttons need the caller's role — only the reconcile card does:
  // identity resolves when one appears instead of on mount (the open path
  // stays at its three budgeted requests). Kernels without /whoami are
  // legacy no-auth deployments — treated as operator (the kernel still
  // enforces the ladder server-side on the reconcile call itself).
  const needsIdentity = selectedOps.length > 0
  useEffect(() => {
    if (!needsIdentity || identityTriedRef.current) return
    identityTriedRef.current = true
    void whoami(unmountRef.current.signal)
      .then(setIdentity)
      .catch((failure) => { if (!isAbortError(failure)) setIdentity(null) })
  }, [needsIdentity])
  const operator = identity === null || canReconcile(identity.role)
  // Manual knowledge verdicts need an actor id; fall back to a stable label
  // for anonymous/no-auth kernels (the server records it as-is in the event).
  const humanActorID = identity?.subject && identity.subject !== 'anonymous' ? identity.subject : 'human-ui'

  async function submitReconcile(op: UncertainOperation, effect: ReconcileEffect) {
    setBusy(true); setError('')
    try {
      await reconcile({
        project: op.project || project.trim(),
        run_id: op.run_id,
        operation_id: op.operation_id,
        effect,
        note: reconcileNote.trim() || undefined,
        actor_id: identity?.subject && identity.subject !== 'anonymous' ? identity.subject : 'human-ui',
        started_event_id: op.started_event_id,
      })
      setReconcileNote('')
      await Promise.all([refreshOps(), loadRun(selected, true)])
    } catch (failure) { setError(message(failure)) }
    finally { setBusy(false) }
  }

  async function decide(event: ObservationEvent, approved: boolean, response = '') {
    const operationID = String(event.data?.operation_id ?? '')
    setBusy(true); setError('')
    try {
      const root = timeline.find((item) => item.type === 'run.started' && runID(item) === selected)
      const operation = event.data?.operation as Record<string, unknown> | undefined
      const approvalQuery = new URLSearchParams({ project: selectedProject, source_id: root?.source.id ?? '' })
      const response2 = await fetch(`${KERNEL_API}/v1/agent/runs/${encodeURIComponent(selected)}/approval?${approvalQuery}`, {
        method: 'POST', headers: { 'Content-Type': 'application/json', ...authHeaders() },
        body: JSON.stringify({ operation_id: operationID, arguments_hash: operation?.arguments_hash ?? String(event.data?.arguments_hash ?? ''), approved, actor_id: 'human-ui', reason: reason.trim(), response: response.trim() }),
      })
      if (!response2.ok) throw new Error(`${response2.status} ${await response2.text()}`)
      await loadRun(selected, true)
      // The answered question no longer marks the run — refresh the open set.
      try {
        const open = await workspaceApi.listHumanRequests({ project: selectedProject || undefined, onlyOpen: true })
        setQuestionRuns(new Set((open.requests ?? []).map((request) => request.run_id)))
      } catch { /* best-effort */ }
    } catch (failure) { setError(message(failure)) }
    finally { setBusy(false) }
  }

  // Nudge is the escape hatch for a stuck provider call: it cancels the
  // in-flight request and retries the turn immediately (kernel.nudge signal).
  // The applied nudge lands in the journal as model.nudged, which is the
  // user-visible confirmation.
  async function nudge(turn: number) {
    setBusy(true); setError('')
    try {
      const root = timeline.find((item) => item.type === 'run.started' && runID(item) === selected)
      const query = new URLSearchParams({ project: selectedProject })
      // source_id is only a legacy hint (identity comes from the kernel's
      // journal), and an absent run.started must not ship an empty parameter.
      const hintedSource = root?.source.id
      if (hintedSource) query.set('source_id', hintedSource)
      const response = await fetch(`${KERNEL_API}/v1/agent/runs/${encodeURIComponent(selected)}/nudge?${query}`, {
        method: 'POST', headers: { 'Content-Type': 'application/json', ...authHeaders() },
        body: JSON.stringify({ turn, actor_id: identity?.subject && identity.subject !== 'anonymous' ? identity.subject : 'human-ui' }),
      })
      const raw = await response.text()
      let payload: Record<string, unknown> = {}
      try { payload = JSON.parse(raw) as Record<string, unknown> } catch { /* non-JSON body: raw text stays as the fallback below */ }
      if (response.ok && payload.status === 'already_gone') {
        // The workflow is gone and the kernel healed the run: say so in the
        // user's language instead of raw JSON, then show the settled run.
        setError(t('runs.nudge_already_gone'))
        await loadRun(selected, true)
        return
      }
      if (!response.ok) {
        // Errors carry a stable human-readable message in the JSON error
        // field; the raw body is only the fallback for a non-JSON failure.
        throw new Error(String(payload.error ?? `${response.status} ${raw}`))
      }
      // Pull the trace right away instead of waiting for the 10s poll: the
      // workflow journals model.nudged within ~1–2s of the signal, and that
      // row is the user-visible confirmation the nudge worked.
      await loadRun(selected, true)
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
  // Team runs surface their rollup in the header (docs/agent-teams.md §9):
  // tokens/cost/rework come from team.completed; a team without it either
  // failed (team.failed names the broken step) or is still running.
  const teamStarted = timeline.find((event) => event.type === 'team.started')
  const teamDone = timeline.find((event) => event.type === 'team.completed')
  const teamFailedEvent = timeline.find((event) => event.type === 'team.failed')
  // Overview empty state: the tab opens by default, and until the run answers
  // (no answer/stages/result yet, no delegations or plan) it would render
  // nothing at all. Instead it onboards: current phase, progress counters and
  // where to watch the run live.
  const ownEvents = useMemo(
    () => timeline.filter((event) => runID(event) === selected),
    [timeline, selected])
  const overviewEmpty = typeof answer !== 'string' && !Array.isArray(stages) && result?.result === undefined
    && !ownEvents.some((event) => event.type === 'delegation.started' || event.type === 'plan.started' || event.type === 'plan.task.started')
  const overviewRunning = selectedLive || (!result && ownEvents.some((event) => event.type === 'run.started') && !ownEvents.some((event) => event.type === 'run.completed' || event.type === 'run.failed' || event.type === 'run.cancelled'))
  const overviewLast = ownEvents[ownEvents.length - 1]
  const overviewTurns = ownEvents.filter((event) => event.type === 'model.completed').length
  const overviewStartedAt = ownEvents.find((event) => event.type === 'run.started')?.occurred_at
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
                <div style={{ display: 'flex', gap: '0.25rem', alignItems: 'center' }}>
                  <button
                    onClick={() => setOpsOnly((value) => !value)}
                    disabled={opRunIds.ids.size === 0 && !opsOnly}
                    title={t('runs.ops_filter')}
                    aria-pressed={opsOnly}
                    style={{ background: opsOnly ? 'rgba(247,118,142,0.15)' : 'none', border: `1px solid ${opsOnly ? '#f7768e' : 'var(--tm-border)'}`, color: opsOnly ? '#f7768e' : 'var(--tm-text-2)', cursor: 'pointer', fontSize: '0.85rem', padding: '0.2rem 0.4rem', lineHeight: 1, borderRadius: '4px' }}
                  >⚠</button>
                  <button onClick={() => { setSelected(''); setTimeline([]); void loadRuns(true) }} disabled={busy} style={{ background: 'none', border: 'none', color: 'var(--tm-text-2)', cursor: 'pointer', fontSize: '1rem', padding: '0.25rem', lineHeight: 1, borderRadius: '4px' }} title="Refresh">↻</button>
                </div>
              </div>
              <ListFilter value={filter} onChange={setFilter} placeholder={t('common.filter_runs') ?? 'Filter runs…'} />
            </div>
            {/* K-3: the batch page's partial flag is a visible state — rows
                lose their status tags, so the list says why instead of
                silently rendering no statuses (the reason is also logged). */}
            {listPartial && (
              <InlineNotification
                kind="warning"
                lowContrast
                hideCloseButton
                title={t('runs.statuses_unavailable') ?? 'Run statuses unavailable'}
                subtitle={[t('runs.statuses_unavailable_hint') ?? '', listPartialError].filter(Boolean).join(' · ')}
                style={{ marginTop: '0.5rem' }}
              />
            )}
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
                    <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{(row.agentId && agents.find((a) => a.id === row.agentId)?.name) || row.role || 'agent'} · {shortTime(row.at)}</span>
                    {/* Project attribution in the global listing
                        (docs/activity-scoping.md §7): shows which project the
                        run worked in without narrowing the list. */}
                    {!project.trim() && row.project && <Tag type="gray" size="sm">{row.project}</Tag>}
                    {row.status === 'completed' && <Tag type="green" size="sm">{t('chat.delegation.completed')}</Tag>}
                    {row.status === 'failed' && <Tag type="red" size="sm">{t('chat.delegation.failed')}</Tag>}
                    {row.status === 'running' && <Tag type="blue" size="sm">{t('chat.delegation.running')}</Tag>}
                    {row.status === 'running' && (
                      confirmStop === row.id ? (
                        <button
                          onClick={(e) => { e.stopPropagation(); void stopRun(row) }}
                          disabled={busy}
                          title={t('runs.stop_confirm') ?? 'Click again to force-stop'}
                          style={{ marginLeft: 'auto', flexShrink: 0, background: 'var(--tm-danger)', border: 'none', color: '#fff', cursor: busy ? 'wait' : 'pointer', fontSize: '0.62rem', padding: '0.05rem 0.3rem', borderRadius: '3px', lineHeight: 1.4 }}
                        >✓?</button>
                      ) : (
                        <button
                          onClick={(e) => { e.stopPropagation(); setConfirmStop(row.id) }}
                          title={`${t('runs.stop') ?? 'Stop'} — ${t('runs.stop_hint') ?? 'Force-stop the workflow; use when a run is stuck'}`}
                          style={{ marginLeft: 'auto', flexShrink: 0, background: 'none', border: '1px solid var(--tm-border)', color: 'var(--tm-text-3)', cursor: 'pointer', fontSize: '0.62rem', padding: '0.05rem 0.3rem', borderRadius: '3px', lineHeight: 1.4 }}
                        >■</button>
                      )
                    )}
                    {row.status === 'cancelled' && <Tag type="warm-gray" size="sm">{t('status.cancelled')}</Tag>}
                    {questionRuns.has(row.id) && <Tag type="warm-gray" size="sm" title={t('runs.asks_human') ?? 'This run is waiting for a human answer'}>❓ {t('runs.asks_human') ?? 'asks you'}</Tag>}
                    {opRunIds.ids.has(row.id) && (
                      <Tag type="red" size="sm" title={t('runs.ops_tag_title')}>⚠ {opRunIds.subtreeCount.get(row.id) ?? 0}</Tag>
                    )}
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
                  {teamStarted && (
                    <div style={{ display: 'flex', gap: '0.4rem', alignItems: 'center', flexWrap: 'wrap', marginTop: '0.4rem' }}>
                      <Tag type="cyan" size="sm">👥 {String(teamStarted.data?.team_name ?? teamStarted.data?.team_id ?? '')}</Tag>
                      {typeof teamStarted.data?.version === 'string' && teamStarted.data.version && <Tag type="gray" size="sm">v{teamStarted.data.version}</Tag>}
                      {teamDone ? (
                        <>
                          <Tag type="green" size="sm">{t('chat.delegation.completed')}</Tag>
                          {typeof teamDone.data?.total_tokens === 'number' && <Tag type="gray" size="sm">{teamDone.data.total_tokens} tok</Tag>}
                          {typeof teamDone.data?.cost_usd === 'number' && <Tag type="gray" size="sm">${teamDone.data.cost_usd.toFixed(4)}</Tag>}
                          {typeof teamDone.data?.rework_rounds === 'number' && teamDone.data.rework_rounds > 0 && (
                            <Tag type="purple" size="sm">{t('runs.event.team_rework_rounds', { count: String(teamDone.data.rework_rounds) })}</Tag>
                          )}
                        </>
                      ) : teamFailedEvent ? (
                        <Tag type="red" size="sm">{t('runs.event.team_failed')}{typeof teamFailedEvent.data?.step === 'string' && teamFailedEvent.data.step ? ` · ${teamFailedEvent.data.step}` : ''}</Tag>
                      ) : (
                        <Tag type="blue" size="sm">{t('chat.delegation.running')}</Tag>
                      )}
                    </div>
                  )}
                </div>

                {/* Detail tabs — trace, trajectory and budget are peer views of
                    the same run; the bar sits right under the title so switching
                    views never depends on content length. Actionable cards
                    (approvals, questions, reconcile) stay visible on every
                    view, directly under the bar. */}
                <div style={{ display: 'flex', gap: 0, borderBottom: '1px solid var(--tm-border)', marginBottom: '0.5rem', flexWrap: 'wrap' }}>
                  {([
                    { key: 'overview' as RunView, label: t('runs.view_overview') ?? 'Overview', badge: selectedOps.length > 0 ? `⚠ ${selectedOps.length}` : '' },
                    { key: 'trace' as RunView, label: t('runs.trace') ?? 'Trace', badge: String(trace.visible.length) },
                    { key: 'trajectory' as RunView, label: t('runs.trajectory') ?? 'Trajectory', badge: '' },
                    { key: 'budget' as RunView, label: t('runs.task_budget') ?? 'Budget', badge: '' },
                  ]).map((v) => (
                    <button
                      key={v.key}
                      onClick={() => setView(v.key)}
                      style={{
                        padding: '0.5rem 1rem', background: view === v.key ? 'var(--tm-elevated)' : 'transparent',
                        border: 'none', borderBottom: view === v.key ? '2px solid var(--tm-teal)' : '2px solid transparent',
                        color: view === v.key ? 'var(--tm-teal)' : 'var(--tm-text-3)', cursor: 'pointer', fontSize: '0.8rem', fontWeight: 600,
                        display: 'flex', alignItems: 'center', gap: '0.35rem',
                      }}
                    >
                      {v.label}
                      {v.badge && <span style={{ fontSize: '0.7rem', fontWeight: 400 }}>{v.badge}</span>}
                    </button>
                  ))}
                </div>

                {/* Pending approvals and human questions */}
                {pending.length > 0 && error && (
                  <InlineNotification kind="error" title={t('action.error') ?? 'Error'} subtitle={error} onClose={() => setError('')} lowContrast style={{ marginBottom: '0.75rem' }} />
                )}
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

                {/* Failure reconciliation — uncertain operations of this run and
                    its delegated children (docs/failure-reconciliation.md). The
                    verdict is recorded next to the trace that produced the
                    uncertainty; unlike approvals it never resumes the run. */}
                {selectedOps.length > 0 && (
                  <Tile style={{ borderLeft: '3px solid #f7768e' }}>
                    <Heading style={{ fontSize: '0.875rem', display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
                      ⚠ {t('runs.reconcile_title')}
                      <Tag type="red" size="sm">{selectedOps.length}</Tag>
                    </Heading>
                    <p style={{ fontSize: '0.8rem', color: 'var(--tm-text-2)', margin: '0.25rem 0 0.75rem' }}>{t('operations.check_external')}</p>
                    <Stack gap={3}>
                      {selectedOps.map((op) => {
                        const info = toolInfo(op.tool, op.server)
                        const turn = turnOf(op.operation_id)
                        const look = whereToLook(op, t)
                        const delegated = op.error_type === 'delegated_run_failed'
                        return (
                          <div key={op.operation_id} style={{ padding: '0.75rem', background: 'var(--tm-raised)', borderRadius: '6px', minWidth: 0 }}>
                            <div style={{ display: 'flex', alignItems: 'center', gap: '0.4rem', flexWrap: 'wrap', fontSize: '0.85rem' }}>
                              <span>{info.icon}</span>
                              <strong style={{ minWidth: 0, overflowWrap: 'anywhere' }}>{info.label}</strong>
                              <Tag type="gray" size="sm">{info.category}</Tag>
                              {turn && <Tag type="cool-gray" size="sm">{t('operations.turn_n', { turn })}</Tag>}
                              <Tag type="red" size="sm">{REASON_KEYS[op.reason] ? t(REASON_KEYS[op.reason]) : op.reason}</Tag>
                              {op.state === 'in_flight' && <Tag type="blue" size="sm">{t('runs.ops_in_flight')}</Tag>}
                              <span style={{ color: 'var(--tm-text-3)', fontSize: '0.7rem', marginLeft: 'auto', flexShrink: 0 }}>{shortTime(op.started_at)}</span>
                            </div>
                            <p style={{ fontSize: '0.8rem', color: 'var(--tm-text)', margin: '0.5rem 0' }}>
                              {delegated ? t('operations.delegated_notice', { run: op.child_run_id ?? '' }) : t('operations.agent_notice')}
                            </p>
                            {delegated && (op.child_ops_total ?? 0) > 0 && (
                              <p style={{ fontSize: '0.75rem', color: 'var(--tm-text-3)', margin: '0 0 0.25rem' }}>
                                {t('operations.delegated_stats', { total: String(op.child_ops_total ?? 0), unresolved: String(op.child_ops_unresolved ?? 0) })}
                              </p>
                            )}
                            {op.error_detail && (
                              <pre style={{ background: 'var(--tm-surface)', color: '#f7768e', padding: '0.5rem', fontSize: '0.7rem', marginTop: '0.25rem', borderRadius: '4px', maxWidth: '100%', overflowX: 'auto', whiteSpace: 'pre-wrap', overflowWrap: 'anywhere', margin: '0.25rem 0' }}>{op.error_detail}</pre>
                            )}
                            {op.arguments && (
                              <pre style={{ background: 'var(--tm-surface)', color: 'var(--tm-text)', padding: '0.5rem', fontSize: '0.7rem', borderRadius: '4px', maxWidth: '100%', overflowX: 'auto', whiteSpace: 'pre-wrap', overflowWrap: 'anywhere', margin: '0.25rem 0' }}>{op.arguments}</pre>
                            )}
                            <p style={{ fontSize: '0.75rem', color: 'var(--tm-text-3)', margin: '0.25rem 0 0.5rem' }}>{look.icon} {look.text}</p>
                            <div style={{ fontSize: '0.65rem', color: 'var(--tm-muted)', fontFamily: 'monospace', overflowWrap: 'anywhere', margin: '0 0 0.5rem' }}>{op.operation_id}</div>
                            <div style={{ display: 'flex', gap: '0.5rem', flexWrap: 'wrap', alignItems: 'center' }}>
                              {op.child_run_id && <Button size="sm" kind="tertiary" onClick={() => setSelected(op.child_run_id ?? '')}>{t('operations.open_child_run')}</Button>}
                              {(['occurred', 'none', 'unknown'] as ReconcileEffect[]).map((effect) => (
                                <Button
                                  key={effect}
                                  size="sm"
                                  kind={effect === 'none' ? 'danger' : effect === 'occurred' ? 'primary' : 'secondary'}
                                  disabled={busy || !operator}
                                  onClick={() => void submitReconcile(op, effect)}
                                >
                                  {effect === 'occurred' ? t('operations.happened') : effect === 'none' ? t('operations.did_not_happen') : t('operations.unknown_verdict')}
                                </Button>
                              ))}
                            </div>
                            {!operator && <p style={{ fontSize: '0.75rem', color: 'var(--tm-text-3)', marginTop: '0.5rem', marginBottom: 0 }}>{t('operations.operator_required')}</p>}
                          </div>
                        )
                      })}
                    </Stack>
                    <div style={{ marginTop: '0.75rem' }}>
                      <TextInput id="reconcile-note" labelText={t('operations.what_did_you_find')} value={reconcileNote} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setReconcileNote(e.target.value)} placeholder={t('operations.find_placeholder')} />
                    </div>
                  </Tile>
                )}

                {view === 'overview' && (<>
                {/* Empty-state onboarding: the overview tab opens by default,
                    and until the run answers it would be blank. Show the current
                    phase, progress counters and where to watch the run live. */}
                {overviewEmpty && !busy && (
                  <Tile>
                    <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', marginBottom: '0.5rem', flexWrap: 'wrap' }}>
                      <Heading style={{ fontSize: '0.875rem' }}>{overviewRunning ? t('runs.overview.in_progress') : t('runs.overview.no_result_yet')}</Heading>
                      {overviewRunning && <Tag type="blue" size="sm">{t('chat.delegation.running')}</Tag>}
                    </div>
                    {overviewRunning && overviewStartedAt && (
                      <p style={{ fontSize: '0.8rem', color: 'var(--tm-text-2)', margin: '0 0 0.5rem' }}>
                        ⏱ {t('runs.overview.elapsed', { time: formatDuration(Math.max(0, nowTick - Date.parse(overviewStartedAt))) })}
                      </p>
                    )}
                    {overviewLast && (() => {
                      const info = eventSummary(overviewLast, t, (id) => knowledgeItems.get(id)?.proposition)
                      const liveModel = overviewRunning && overviewLast.type === 'model.started' && tokenMetrics
                      return (
                        <div style={{ display: 'flex', alignItems: 'center', gap: '0.4rem', flexWrap: 'wrap', padding: '0.5rem 0.75rem', background: 'var(--tm-raised)', borderRadius: '6px', fontSize: '0.8rem', marginBottom: '0.75rem' }}>
                          <span style={{ color: info.color }}>{info.icon}</span>
                          <strong>{info.label}</strong>
                          {info.detail && <span style={{ color: 'var(--tm-text-3)', minWidth: 0, overflowWrap: 'anywhere' }}>{info.detail}</span>}
                          {liveModel && tokenMetrics.tool && <span style={{ color: 'var(--tm-text-3)' }}>⚙ {tokenMetrics.tool}</span>}
                          {liveModel && tokenMetrics.approxTokens > 0 && <span style={{ color: 'var(--tm-text-3)' }}>~{tokenMetrics.approxTokens} tok</span>}
                          {liveModel && tokenMetrics.tps > 0 && <span style={{ color: 'var(--tm-text-3)' }}>{tokenMetrics.tps} tok/s</span>}
                          {liveModel && tokenMetrics.streaming && <Tag type="green" size="sm">{t('runs.live.alive')}</Tag>}
                          <Tag type="gray" size="sm">{t('runs.events_count', { count: String(ownEvents.length) })}</Tag>
                          {overviewTurns > 0 && <Tag type="gray" size="sm">{overviewTurns} {t('runs.turns')}</Tag>}
                        </div>
                      )
                    })()}
                    <p style={{ fontSize: '0.8rem', color: 'var(--tm-text-2)', margin: '0 0 0.75rem' }}>{t('runs.overview.hint')}</p>
                    <Button size="sm" kind="secondary" onClick={() => setView('trace')}>{t('runs.overview.open_trace')}</Button>
                  </Tile>
                )}

                {/* Answer — the overview tab opens with what the run concluded */}
                {typeof answer === 'string' && (
                  <Tile>
                    <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', marginBottom: '0.5rem' }}>
                      <Heading style={{ fontSize: '0.875rem' }}>{t('runs.answer') ?? 'Agent Answer'}</Heading>
                      <Tag type={result?.status === 'completed' ? 'green' : result?.status === 'cancelled' ? 'warm-gray' : 'red'} size="sm">{String(result?.status ?? '')}</Tag>
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

                {/* Cross-agent knowledge disputes across the selected run's chain */}
                <RunDisputes project={selectedProject} selected={selected} rows={rows} onOpenRun={setSelected} />

                {/* Delegation call tree — children runs with live status */}
                <DelegationTree project={selectedProject} runId={selected} agents={agents} live={selectedLive} onOpenRun={setSelected} />

                {/* Plan DAG view — task graph of plan calls with live statuses */}
                <PlanGraph project={selectedProject} runId={selected} agents={agents} live={selectedLive} onOpenRun={setSelected} />
                </>)}

                {view === 'trace' && (
                <Tile>
                  <Heading style={{ fontSize: '0.875rem', marginBottom: '0.5rem' }}>{t('runs.trace') ?? 'Trace'} ({t('runs.events_count', { count: String(trace.visible.length) })})</Heading>
                  <Stack gap={0}>
                    {traceWindow.map((event, index) => {
                      const info = eventSummary(event, t, (id) => knowledgeItems.get(id)?.proposition)
                      const turnKey = event.type === 'model.completed' ? `${runID(event)}:${event.data?.turn}` : undefined
                      // The newest trace row being model.started of the selected
                      // run means the call is in flight right now — render it as a
                      // live progress row (elapsed time always; tokens/rate when
                      // the provider streams). Event timestamps come from the
                      // journal clock, same as every other row on the page.
                      const isLiveModelCall = index === 0 && selectedLive && event.type === 'model.started' && runID(event) === selected
                      const liveParts: string[] = []
                      if (isLiveModelCall) {
                        const elapsedMs = Math.max(0, nowTick - Date.parse(event.occurred_at))
                        liveParts.push(formatDuration(elapsedMs))
                        if (tokenMetrics) {
                          // A tool name here means the model is streaming a tool
                          // call right now (no answer text this turn) — the most
                          // informative thing the row can show.
                          if (tokenMetrics.tool) liveParts.push(`⚙ ${tokenMetrics.tool}`)
                          if (tokenMetrics.approxTokens > 0) liveParts.push(`~${tokenMetrics.approxTokens} tok`)
                          if (tokenMetrics.tps > 0) liveParts.push(`${tokenMetrics.tps} tok/s`)
                          const stalled = tokenMetrics.lastChunkAgoMs > 15_000
                          if (tokenMetrics.streaming) {
                            liveParts.push(stalled
                              ? t('runs.live.stalled', { seconds: String(Math.floor(tokenMetrics.lastChunkAgoMs / 1000)) })
                              : t('runs.live.alive'))
                          } else if (tokenMetrics.approxTokens === 0) {
                            // No chunks yet: either the thinking phase before
                            // the first token or a non-streaming transport.
                            liveParts.push(t('runs.live.waiting_first_token'))
                          }
                        }
                      }
                      const detail = isLiveModelCall ? liveParts.join(' · ') || info.detail : info.detail
                      // An event whose operation effect is still unresolved ties
                      // this row to the reconcile card above — mark it in place.
                      const unresolvedOp = opById.get(String(event.data?.operation_id ?? ''))
                      return (
                        <div
                          key={`${event.source.id}:${event.event_id}`}
                          style={{
                            padding: '0.4rem 0.5rem',
                            borderLeft: `3px solid ${unresolvedOp ? '#f7768e' : info.color}`,
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
                            <span style={{ color: info.color, width: '1.2rem', textAlign: 'center', flexShrink: 0 }} className={isLiveModelCall ? 'live-dot' : undefined}>{info.icon}</span>
                            <strong style={{ color: info.color, minWidth: '100px' }}>{info.label}</strong>
                            <span style={{ color: isLiveModelCall ? 'var(--tm-teal)' : 'var(--tm-text-3)', flex: 1, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{detail}</span>
                            {isLiveModelCall && (
                              <button
                                onClick={() => void nudge(Number(event.data?.turn ?? 0) || 0)}
                                disabled={busy}
                                title={t('runs.nudge_hint') ?? 'Cancel the stuck model call and retry immediately'}
                                aria-label={t('runs.nudge') ?? 'Nudge model call'}
                                style={{ background: 'none', border: '1px solid var(--tm-border)', color: 'var(--tm-text-2)', cursor: 'pointer', fontSize: '0.75rem', padding: '0.1rem 0.35rem', lineHeight: 1, borderRadius: '4px', flexShrink: 0 }}
                              >↻</button>
                            )}
                            {unresolvedOp && (
                              <span style={{ color: '#f7768e', fontSize: '0.7rem', flexShrink: 0 }} title={t('runs.ops_trace_mark')}>⚠</span>
                            )}
                            <span style={{ color: 'var(--tm-muted)', fontSize: '0.7rem', flexShrink: 0 }}>{shortTime(event.occurred_at)}</span>
                          </div>
                          <div style={{ marginLeft: '1.7rem', marginTop: '0.15rem' }}>
                            <EventDetail event={event} t={t} project={selectedProject} response={turnKey ? trace.responseByKey.get(turnKey) : undefined} reasoning={turnKey ? trace.reasoningByKey.get(turnKey) : undefined}
                              knowledgeDoubt={typeof event.data?.knowledge_id === 'string' && event.data.knowledge_id
                                ? <KnowledgeActions knowledgeID={String(event.data.knowledge_id)} project={selectedProject} actorID={humanActorID} onDone={() => setKnowledgeRevision((r) => r + 1)} />
                                : undefined} />
                          </div>
                        </div>
                      )
                    })}
                  </Stack>
                  {/* NFR-5: the window never grows by itself — the reader
                      asks for TRACE_WINDOW_STEP more rows each time. */}
                  {trace.visible.length > traceLimit && (
                    <button
                      onClick={() => setTraceLimit((limit) => limit + TRACE_WINDOW_STEP)}
                      style={{ marginTop: '0.5rem', width: '100%', background: 'var(--tm-raised)', border: '1px solid var(--tm-border)', color: 'var(--tm-text-2)', cursor: 'pointer', fontSize: '0.75rem', padding: '0.35rem', borderRadius: '4px' }}
                    >
                      {t('runs.trace_show_more', { count: String(Math.min(TRACE_WINDOW_STEP, trace.visible.length - traceLimit)) })}
                    </button>
                  )}
                </Tile>
                )}

                {view === 'trajectory' && (!(trajectory || runCostData) ? (
                  <Tile style={{ textAlign: 'center', color: 'var(--tm-text-3)', padding: '2rem' }}>
                    <p>{t('runs.no_trajectory') ?? 'Trajectory extraction is not available for this run.'}</p>
                  </Tile>
                ) : (
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
                      {humanFactors && humanFactors.hwtSeconds > 0 && (
                        <span title={t('runs.hwt_hint') ?? 'Wall-clock time the run spent waiting for a human (HWT)'}>⏳ <strong>{formatDuration(humanFactors.hwtSeconds * 1000)}</strong> {t('runs.hwt') ?? 'human wait'}</span>
                      )}
                      {humanFactors && humanFactors.habSeconds > 0 && (
                        <span title={t('runs.hab_hint') ?? 'Estimated human attention: decisions, answers and reading (HAB, estimate)'}>👁 <strong>{formatDuration(humanFactors.habSeconds * 1000)}</strong> {t('runs.hab') ?? 'human attention'}</span>
                      )}
                      {humanFactors && humanFactors.approvalDecisions + humanFactors.askHumanAnswered > 0 && (
                        <span>✋ <strong>{humanFactors.approvalDecisions + humanFactors.askHumanAnswered}</strong> {t('runs.decisions') ?? 'human decisions'}</span>
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
                    {/* Knowledge events — doubt/invalidate in place: the
                        reader looking at a wrong proposition is the right
                        moment to record the verdict */}
                    {trajectory.knowledge?.length > 0 && (
                      <div>
                        <h5 style={{ fontSize: '0.75rem', fontWeight: 600, color: 'var(--tm-text-3)', textTransform: 'uppercase', letterSpacing: '0.05em', marginBottom: '0.5rem' }}>{t('runs.knowledge_heading') ?? 'Knowledge'}</h5>
                        {trajectory.knowledge.map((k: any, i: number) => {
                          const live = knowledgeItems.get(String(k.knowledge_id ?? ''))
                          return (
                            <div key={i} style={{ fontSize: '0.8rem', marginBottom: '0.25rem' }}>
                              <Tag type={k.kind === 'proposed' ? 'blue' : k.kind === 'recalled' ? 'purple' : 'red'} size="sm">{k.kind}</Tag>
                              {live && live.state !== 'proposed' && live.state !== 'confirmed' && (
                                <Tag type={live.state === 'invalidated' || live.state === 'superseded' || live.state === 'corrected' ? 'red' : 'warm-gray'} size="sm">{live.state}</Tag>
                              )}
                              <span style={{ marginLeft: '0.5rem' }}>{k.proposition || k.knowledge_id}</span>
                              {k.knowledge_id && (
                                <KnowledgeActions
                                  knowledgeID={String(k.knowledge_id)}
                                  project={selectedProject}
                                  actorID={humanActorID}
                                  onDone={() => setKnowledgeRevision((r) => r + 1)}
                                />
                              )}
                              {k.knowledge_id && (
                                <a
                                  href={`/knowledge?project=${encodeURIComponent(selectedProject)}&dossier=${encodeURIComponent(String(k.knowledge_id))}`}
                                  onClick={(e) => { e.preventDefault(); window.history.pushState(null, '', `/knowledge?project=${encodeURIComponent(selectedProject)}&dossier=${encodeURIComponent(String(k.knowledge_id))}`); window.dispatchEvent(new PopStateEvent('popstate')) }}
                                  title={t('runs.knowledge_dossier') ?? 'Open the knowledge dossier'}
                                  style={{ fontSize: '0.72rem', color: 'var(--tm-teal)', marginLeft: '0.4rem' }}
                                >{t('runs.knowledge_dossier') ?? 'dossier'} →</a>
                              )}
                            </div>
                          )
                        })}
                      </div>
                    )}
                    </>)}
                  </Tile>
                ))}

                {view === 'budget' && (!(taskCostData && taskCostData.run_count > 0) ? (
                  <Tile style={{ textAlign: 'center', color: 'var(--tm-text-3)', padding: '2rem' }}>
                    <p>{t('runs.no_budget') ?? 'No task budget data for this run yet.'}</p>
                  </Tile>
                ) : (() => {
                  const running = taskCostData.runs.filter((r) => r.status === 'running').length
                  const anyCost = taskCostData.agents.some((a) => a.cost_usd > 0)
                  const agentLabel = (id: string) => {
                    const name = id !== '-' ? agents.find((a) => a.id === id)?.name : undefined
                    return name || (id === '-' ? (t('runs.task_unknown_agent') ?? 'unknown agent') : id)
                  }
                  return (
                  <Tile>
                    <Heading style={{ fontSize: '0.875rem', marginBottom: '0.75rem', display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
                      {t('runs.task_budget') ?? 'Task budget'}
                      {running > 0 && <Tag type="cool-gray" size="sm">{pluralT(t, locale, 'runs.task_running_n', running)}</Tag>}
                    </Heading>
                    <div style={{ display: 'flex', gap: '1rem', flexWrap: 'wrap', marginBottom: '0.75rem', fontSize: '0.8rem' }}
                      title={taskCostData.first_started_at ? `${taskCostData.first_started_at} → ${taskCostData.last_ended_at ?? '…'}` : undefined}>
                      <span><strong>{taskCostData.run_count}</strong> {pluralT(t, locale, 'runs.task_runs_n', taskCostData.run_count)}</span>
                      <span><strong>{taskCostData.agents.length}</strong> {pluralT(t, locale, 'runs.task_agents_n', taskCostData.agents.length)}</span>
                      <span>⏱ <strong>{formatDuration(taskCostData.elapsed_ms)}</strong> {t('runs.task_elapsed') ?? 'elapsed'}</span>
                      <span>⚒ <strong>{formatDuration(taskCostData.active_ms)}</strong> {t('runs.task_active') ?? 'active'}</span>
                      <span><strong>{taskCostData.total_tokens.toLocaleString()}</strong> {t('runs.tokens') ?? 'tokens'}</span>
                      {(taskCostData.total_cached_tokens ?? 0) > 0 && (
                        <span>↻ <strong>{(taskCostData.total_cached_tokens ?? 0).toLocaleString()}</strong> {t('runs.cached') ?? 'cached'}</span>
                      )}
                      {taskCostData.total_cost_usd > 0 && (
                        <span><strong>${taskCostData.total_cost_usd.toFixed(4)}</strong> {t('runs.cost') ?? 'cost'}</span>
                      )}
                      {taskCostData.approvals && taskCostData.approvals.requested > 0 && (
                        <span title={approvalHint(t, taskCostData.approvals)} style={{ cursor: 'help' }}>
                          👤 <strong>{taskCostData.approvals.granted + taskCostData.approvals.rejected}/{taskCostData.approvals.requested}</strong> {t('runs.approvals_col') ?? 'approvals'} · {formatDuration(taskCostData.approvals.wait_ms)}
                        </span>
                      )}
                      {taskCostData.human && taskCostData.human.hab_seconds > 0 && (
                        <span title={t('runs.human_hint') ?? undefined} style={{ cursor: 'help' }}>
                          🧠 <strong>{formatDuration(taskCostData.human.hab_seconds * 1000)}</strong> {t('runs.hab') ?? 'attention'} · {formatDuration(taskCostData.human.hwt_seconds * 1000)} {t('runs.hwt') ?? 'wait'}
                        </span>
                      )}
                    </div>
                    {taskCostData.agents.length > 0 && (
                      <div>
                        <h5 style={{ fontSize: '0.75rem', fontWeight: 600, color: 'var(--tm-text-3)', textTransform: 'uppercase', letterSpacing: '0.05em', marginBottom: '0.5rem' }}>{t('runs.task_by_agent') ?? 'By agent'}</h5>
                        <div style={{ display: 'grid', gridTemplateColumns: `minmax(8rem, 1.5fr) repeat(${anyCost ? 4 : 3}, minmax(4rem, 1fr))`, gap: '0.25rem 0.75rem', fontSize: '0.8rem', alignItems: 'baseline' }}>
                          <span style={{ color: 'var(--tm-text-3)' }}>{t('runs.task_col_agent') ?? 'Agent'}</span>
                          <span style={{ color: 'var(--tm-text-3)' }}>{t('runs.task_col_runs') ?? 'Runs'}</span>
                          <span style={{ color: 'var(--tm-text-3)' }}>{t('runs.task_active') ?? 'Active'}</span>
                          <span style={{ color: 'var(--tm-text-3)' }}>{t('runs.tokens') ?? 'Tokens'}</span>
                          {anyCost && <span style={{ color: 'var(--tm-text-3)' }}>{t('runs.cost') ?? 'Cost'}</span>}
                          {taskCostData.agents.map((a) => (
                            <Fragment key={a.agent_id}>
                              <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={a.agent_id}>{agentLabel(a.agent_id)}</span>
                              <span>{a.runs}</span>
                              <span>{formatDuration(a.active_ms)}</span>
                              <span>{a.total_tokens.toLocaleString()}{(a.cached_tokens ?? 0) > 0 ? <span style={{ color: 'var(--tm-text-3)' }}> ↻{((a.cached_tokens ?? 0) / a.total_tokens * 100).toFixed(0)}%</span> : null}</span>
                              {anyCost && <span>{a.cost_usd > 0 ? `$${a.cost_usd.toFixed(4)}` : '—'}</span>}
                            </Fragment>
                          ))}
                        </div>
                      </div>
                    )}
                  </Tile>
                  )
                })())}
              </Stack>
            )}
          </Section>
        </Column>
      </Grid>

      {busy && <PageLoading />}
    </div>
  )
}
