import { authHeaders } from './api'
import { KERNEL_API, operations as fetchOperations, type UncertainOperation } from './kernelApi'
import { observationApi, type ObservationEvent } from './observationApi'
import { workspaceApi } from './workspaceApi'

// ── Load policy for the Runs tab (docs/trace-rework/01-spec §3E) ──────────────
//
// Everything the tab fetches lives here so the request budgets are enforced
// by structure (and pinned by perf-guard tests), not by convention:
//
//   open   (NFR-1): batch run list + open human requests + reconcile ops.
//   select (NFR-2): exactly ONE journal page for the run's timeline (first
//                   page 50–100 events, then cursor catch-up to the run's
//                   end — the old runEvents loop's semantics, preserved per
//                   NFR-4), served from a per-run cache with in-flight
//                   deduplication.
//   views  (lazy)  : result → overview, trajectory + run cost → trajectory
//                    view, task cost → budget view, knowledge → trace/
//                    trajectory views. Selection never triggers them.
//
// The old shape this replaces: four journal scans of 500 events to build the
// list (run.started + three terminal types) and a per-row GET
// /v1/agent/runs/{id} N+1 — both are gone (K-2), and the terminal-status
// .catch(() => ({events: []})) silence became the server's partial flag (K-3).

/** Opening the list: batch page + open human requests + reconcile ops. */
export const OPEN_REQUEST_BUDGET = 3
/** Selecting a run: one journal page request for its timeline. */
export const SELECTION_EVENTS_REQUESTS = 1
/** Batch list page size (spec: 50–100 rows, default 100). */
export const RUN_LIST_PAGE = 100
/** First timeline page on selection (spec: 50–100 events). */
export const TIMELINE_FIRST_PAGE = 100
/** Cursor catch-up pages — the existing runEvents loop's page size. */
export const TIMELINE_CATCHUP_PAGE = 500
/** Hard cap of the per-run catch-up loop (the old runEvents cap = 4000). */
export const TIMELINE_EVENT_CAP = 4000
/** Runs kept in the timeline cache; older evicted (NFR-4 dedup by run_id). */
const TIMELINE_CACHE_MAX = 6

export function isAbortError(failure: unknown): boolean {
  return failure instanceof Error && failure.name === 'AbortError'
}

function message(error: unknown): string {
  return error instanceof Error ? error.message : 'Request failed'
}

// ── Batch list (GET /v1/agent/runs) ──────────────────────────────────────────

export type RunStatus = 'completed' | 'failed' | 'running' | 'cancelled' | 'unknown'

export interface RunListEntry {
  run_id: string
  project?: string
  title: string
  role: string
  parent_run_id: string
  started_at: string
  /** "unknown" when the server could not resolve terminal statuses (K-3
   *  partial page) — the row renders no status tag then. */
  status: RunStatus
  source_id: string
  workflow?: string
  agent_id?: string
  /** Display name resolved server-side — the list needs no agents request. */
  agent_name?: string
}

export interface RunListPage {
  runs: RunListEntry[]
  count: number
  next_cursor?: string
  /** True when statuses are unavailable: rows say "unknown", the UI shows
   *  the «statuses unavailable» indicator (K-3). */
  partial?: boolean
  partial_error?: string
}

export interface RunListOptions {
  project?: string
  cursor?: string
  limit?: number
  signal?: AbortSignal
}

/** One cursor page of the batch run list — the tab's first request. */
export async function fetchRunList(opts: RunListOptions = {}): Promise<RunListPage> {
  const params = new URLSearchParams()
  const project = opts.project?.trim()
  if (project) params.set('project', project)
  params.set('limit', String(opts.limit ?? RUN_LIST_PAGE))
  if (opts.cursor) params.set('cursor', opts.cursor)
  const response = await fetch(`${KERNEL_API}/v1/agent/runs?${params.toString()}`, {
    headers: authHeaders(),
    signal: opts.signal,
  })
  if (!response.ok) throw new Error(`${response.status} ${await response.text()}`)
  return response.json() as Promise<RunListPage>
}

export interface RunRow {
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
  agentName?: string
  /** Project attribution of the run (docs/activity-scoping.md §4): global
   * listings span projects, so every row carries its own project — detail
   * calls (result, cost, nudge) must use it, not the header filter. */
  project?: string
}

/** Build the run call tree from batch entries: roots are runs whose parent is
 *  not part of the loaded set, children nest under it, newest runs first.
 *  Pure — the list no longer scans the journal (K-2). */
export function buildRunRows(entries: RunListEntry[]): RunRow[] {
  const ordered = [...entries].sort((a, b) => b.started_at.localeCompare(a.started_at) || a.run_id.localeCompare(b.run_id))
  const byId = new Map<string, RunListEntry>()
  for (const entry of ordered) byId.set(entry.run_id, entry)
  const childrenOf = new Map<string, RunListEntry[]>()
  const roots: RunListEntry[] = []
  for (const entry of ordered) {
    const parent = entry.parent_run_id
    if (parent && parent !== entry.run_id && byId.has(parent)) {
      const list = childrenOf.get(parent) ?? []
      list.push(entry)
      childrenOf.set(parent, list)
    } else {
      roots.push(entry)
    }
  }
  const rows: RunRow[] = []
  const walk = (entry: RunListEntry, depth: number) => {
    rows.push({
      id: entry.run_id,
      title: entry.title?.trim() ?? '',
      role: entry.role || 'agent',
      at: entry.started_at,
      parent: depth === 0 ? null : entry.parent_run_id,
      depth,
      status: entry.status,
      sourceId: entry.source_id,
      workflow: entry.workflow || undefined,
      agentId: entry.agent_id || undefined,
      agentName: entry.agent_name || undefined,
      project: entry.project || undefined,
    })
    for (const child of childrenOf.get(entry.run_id) ?? []) walk(child, depth + 1)
  }
  for (const root of roots) walk(root, 0)
  return rows
}

/** Merge a fresh page into the accumulated entries: known runs update in
 *  place (status/title may have changed), new runs join — the user's
 *  explicitly loaded older pages survive every silent refresh. */
export function mergeRunEntries(prev: Map<string, RunListEntry>, fresh: RunListEntry[]): Map<string, RunListEntry> {
  const next = new Map(prev)
  for (const entry of fresh) next.set(entry.run_id, entry)
  return next
}

// ── Open path (NFR-1: ≤3 requests) ───────────────────────────────────────────

export interface OpenTabResult {
  page: RunListPage
  questionRunIDs: string[]
  opsByRun: Map<string, UncertainOperation[]>
  /** Best-effort companions degrade loudly: the caller logs the reason
   *  instead of pretending the answers were empty (K-3 spirit). */
  errors: { questions?: string; ops?: string }
}

/** The whole open path in one round: batch list + open human requests +
 *  reconcile ops — exactly OPEN_REQUEST_BUDGET fetches. Agent names ride the
 *  list rows, identity resolves when the reconcile card appears, knowledge
 *  waits for the trace views: nothing else may run on tab open. */
export async function openTabLoads(opts: { project?: string; signal?: AbortSignal } = {}): Promise<OpenTabResult> {
  const project = opts.project?.trim() || undefined
  const [list, questions, ops] = await Promise.allSettled([
    fetchRunList({ project: opts.project, signal: opts.signal }),
    workspaceApi.listHumanRequests({ project, onlyOpen: true, signal: opts.signal }),
    fetchOperations(project, opts.signal),
  ])
  // The list IS the load: its failure (or abort) fails the open.
  if (list.status === 'rejected') throw list.reason
  const result: OpenTabResult = {
    page: list.value,
    questionRunIDs: [],
    opsByRun: new Map(),
    errors: {},
  }
  if (questions.status === 'fulfilled') {
    result.questionRunIDs = (questions.value.requests ?? []).map((request) => request.run_id)
  } else if (!isAbortError(questions.reason)) {
    result.errors.questions = message(questions.reason)
  }
  if (ops.status === 'fulfilled') {
    const grouped = new Map<string, UncertainOperation[]>()
    for (const op of ops.value.operations ?? []) {
      const bucket = grouped.get(op.run_id) ?? []
      bucket.push(op)
      grouped.set(op.run_id, bucket)
    }
    result.opsByRun = grouped
  } else if (!isAbortError(ops.reason)) {
    result.errors.ops = message(ops.reason)
  }
  return result
}

// ── Selection timeline (NFR-2: one page + cursor catch-up, NFR-4 cache) ──────

interface TimelineCacheEntry {
  events: ObservationEvent[]
  complete: boolean
  pending?: Promise<ObservationEvent[]>
}

const timelineCache = new Map<string, TimelineCacheEntry>()

/** Drop all cached timelines — tests isolate on it; the app never needs it. */
export function clearRunTimelineCache(): void {
  timelineCache.clear()
}

function rememberTimeline(run: string, entry: TimelineCacheEntry): void {
  timelineCache.delete(run)
  timelineCache.set(run, entry)
  while (timelineCache.size > TIMELINE_CACHE_MAX) {
    const oldest = timelineCache.keys().next().value
    if (oldest === undefined || oldest === run) break
    timelineCache.delete(oldest)
  }
}

/** Fetch the run's timeline following journal cursors to its end: the first
 *  page is the selection request (TIMELINE_FIRST_PAGE), every further page is
 *  the preserved runEvents catch-up (500/page, cap). Duplicate events across
 *  page boundaries never happen (cursor paging), but dedupe by
 *  source:event_id anyway — the cache merge relies on it. */
async function fetchRunTimeline(run: string, signal?: AbortSignal): Promise<ObservationEvent[]> {
  const collected: ObservationEvent[] = []
  const seen = new Set<string>()
  let cursor: string | undefined
  let pages = 0
  do {
    const page = await observationApi.events(undefined, cursor, undefined, undefined, {
      run,
      limit: pages === 0 ? TIMELINE_FIRST_PAGE : TIMELINE_CATCHUP_PAGE,
      signal,
    })
    for (const event of page.events) {
      const key = `${event.source.id}:${event.event_id}`
      if (seen.has(key)) continue
      seen.add(key)
      collected.push(event)
    }
    cursor = page.next_cursor || undefined
    pages += 1
  } while (cursor && collected.length < TIMELINE_EVENT_CAP)
  return collected
}

export interface SelectRunOptions {
  /** Polls pass refresh=true: bypass the cache, replace it with the fresh
   *  snapshot (still deduped against an in-flight load of the same run). */
  refresh?: boolean
  signal?: AbortSignal
}

/** Selecting a run (or expanding a delegated child — same code path):
 *  exactly SELECTION_EVENTS_REQUESTS journal request for a fresh run, ZERO
 *  for a cached or in-flight one, cursor catch-up preserved. */
export async function selectRunEvents(run: string, opts: SelectRunOptions = {}): Promise<ObservationEvent[]> {
  const cached = timelineCache.get(run)
  if (cached?.pending) return cached.pending
  if (cached?.complete && !opts.refresh) return cached.events

  const previous = cached?.events ?? []
  const pending = fetchRunTimeline(run, opts.signal)
  rememberTimeline(run, { events: previous, complete: cached?.complete ?? false, pending })
  try {
    const events = await pending
    rememberTimeline(run, { events, complete: true })
    return events
  } catch (failure) {
    if (isAbortError(failure)) {
      // An aborted load must not poison the cache: the previous snapshot (if
      // any) stays valid, otherwise the run is simply not cached.
      if (previous.length > 0) rememberTimeline(run, { events: previous, complete: true })
      else timelineCache.delete(run)
    } else {
      // A failed refresh keeps serving the last good snapshot.
      rememberTimeline(run, { events: previous, complete: cached?.complete ?? false })
    }
    throw failure
  }
}

// ── View-lazy extras (result / trajectory / costs) ───────────────────────────

/** Run result (REST): evicted child runs have none — the caller keeps the
 *  journal summary as its fallback. 404 means Temporal evicted the workflow:
 *  surface it as a status, not as an error. */
export async function fetchRunResult(
  run: string,
  opts: { project: string; sourceID: string; teamRun?: boolean; signal?: AbortSignal },
): Promise<Record<string, unknown>> {
  const query = new URLSearchParams({ project: opts.project, source_id: opts.sourceID })
  const primary = opts.teamRun
    ? `/v1/agent/examples/lead-coder-reviewer-qa/runs/${encodeURIComponent(run)}`
    : `/v1/agent/runs/${encodeURIComponent(run)}`
  let response = await fetch(`${KERNEL_API}${primary}?${query.toString()}`, { headers: authHeaders(), signal: opts.signal })
  if (!response.ok && opts.teamRun) {
    response = await fetch(`${KERNEL_API}/v1/agent/runs/${encodeURIComponent(run)}?${query.toString()}`, { headers: authHeaders(), signal: opts.signal })
  }
  if (response.ok) return response.json() as Promise<Record<string, unknown>>
  if (response.status === 404) {
    return { status: 'workflow_expired', error: 'Workflow no longer available in Temporal (events preserved in journal)' }
  }
  const detail = await response.text()
  throw new Error(`${response.status} ${detail}`)
}

/** Trajectory extraction (deterministic patterns from the event stream);
 *  absent extraction renders the view's empty state, so null, not an error. */
export async function fetchRunTrajectory(run: string, project: string, signal?: AbortSignal): Promise<any> {
  const response = await fetch(
    `${KERNEL_API}/v1/workspace/runs/${encodeURIComponent(run)}/trajectory?project=${encodeURIComponent(project)}`,
    { headers: authHeaders(), signal },
  )
  if (!response.ok) return null
  return response.json()
}
