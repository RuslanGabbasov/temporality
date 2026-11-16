import { authHeaders } from './api'
import { KERNEL_API } from './kernelApi'
import { reportKernelReachable, reportKernelTransportFailure } from './kernelAvailability'

// Organization operations API (docs/org-activity.md surface): activity board,
// work board, awareness flow, per-unit activity settings and plan graph. All
// endpoints live on the KERNEL API (the same base the supervisor and dashboard
// read from); scoping is org-node based — an empty org_node means the whole
// installation as visible to the caller.

// ── Activity board ──────────────────────────────────────────────────────────

export type OrgActivityCategory = 'run' | 'progress' | 'plan' | 'task' | 'artifact' | 'quality' | 'human'

export interface OrgActivityEvent {
  source_id: string
  event_id: string
  event_type: string
  category: OrgActivityCategory | string
  occurred_at: string
  project_id?: string
  run_id?: string
  task_id?: string
  plan_id?: string
  actor_id?: string
  org_node_id?: string
  summary: string
  visibility_policy?: string
  refs?: unknown
  schema_version?: string | number
}

export interface OrgActivityResponse {
  events: OrgActivityEvent[]
  count: number
}

// ── Active runs ─────────────────────────────────────────────────────────────

export interface OrgActiveRun {
  project_id: string
  run_id: string
  org_node_id?: string
  actor_id?: string
  agent_id?: string
  task_id?: string
  title: string
  status: 'running' | 'completed' | 'failed' | 'cancelled' | string
  urgent?: boolean
  started_at: string
  last_event_at: string
}

export interface OrgActiveRunsResponse {
  runs: OrgActiveRun[]
  count: number
}

// ── Work board (management view) ────────────────────────────────────────────

export interface OrgCardRun {
  run_id: string
  status: string
  title?: string
  urgent?: boolean
  actor_id?: string
  last_event_at?: string
}

/** What the target runs know about this task's context: the awareness
 *  pipeline funnel (selected → delivered → consumed → details requested). */
export interface OrgAwarenessSummary {
  selected?: number
  delivered?: number
  consumed?: number
  details?: number
}

export interface OrgWorkCard {
  task_id: string
  title: string
  project_id?: string
  column: string
  actors: string[]
  org_node_ids?: string[]
  run_count: number
  waiting_reason?: string
  last_event_at?: string
  flags?: string[]
  awareness?: OrgAwarenessSummary
  runs: OrgCardRun[]
}

export interface OrgWorkMetrics {
  active?: number
  waiting?: number
  review?: number
  completed?: number
  planned?: number
  failed_runs?: number
  completed_runs_24h?: number
}

export interface OrgWorkProblem {
  kind: 'waiting_on_human' | 'failed_run' | 'no_movement' | string
  task_id?: string
  run_id?: string
  title?: string
  reason?: string
  since?: string
}

export interface OrgWorkView {
  columns: string[]
  cards: OrgWorkCard[]
  ungrouped_runs?: OrgCardRun[]
  metrics?: OrgWorkMetrics
  problems?: OrgWorkProblem[]
}

// ── Awareness flow ──────────────────────────────────────────────────────────

export type OrgAwarenessStatus =
  | 'selected' | 'queued' | 'delivered' | 'consumed' | 'follow_up_requested'
  | 'details_delivered' | 'acted_upon' | 'dismissed' | 'expired' | 'failed'

export type OrgAwarenessDecision = 'notify' | 'recommend_lookup' | 'urgent_steer' | 'update_awareness' | 'ignore'

export interface OrgDelivery {
  id: string
  decision_id?: string
  batch_id?: string
  org_node_id?: string
  project_id?: string
  source_event_id?: string
  source_run_id?: string
  source_summary?: string
  target_run_id: string
  decision: OrgAwarenessDecision | string
  relevance?: number
  urgency?: number
  reason?: string
  message?: string
  detail_level?: string
  status: OrgAwarenessStatus | string
  message_ref?: string
  attempts?: number
  deduplication_key?: string
  created_at?: string
  queued_at?: string
  delivered_at?: string
  consumed_at?: string
}

export interface OrgFlowBatch {
  id: string
  org_node_id?: string
  status: string
  trigger?: string
  window_from?: string
  window_to?: string
  event_count?: number
  decision_count?: number
  delivery_count?: number
  empty?: boolean
}

export interface OrgFlowSourceEvent {
  event_type?: string
  summary?: string
  occurred_at?: string
  run_id?: string
}

/** One pipeline row: the full chain Event → Batch → Decision → Delivery.
 *  Batch and source event are best-effort context — either may be absent
 *  (retention, cross-window deliveries). */
export interface OrgFlowRow {
  delivery: OrgDelivery
  batch?: OrgFlowBatch | null
  source_event?: OrgFlowSourceEvent | null
}

export interface OrgFlowResponse {
  flow: OrgFlowRow[]
  count: number
}

// ── Activity batches ────────────────────────────────────────────────────────

export interface OrgActivityBatch {
  id: string
  org_node_id?: string
  status?: string
  trigger?: string
  window_from?: string
  window_to?: string
  event_count?: number
  decision_count?: number
  delivery_count?: number
  empty?: boolean
  created_at?: string
}

export interface OrgActivityBatchesResponse {
  batches: OrgActivityBatch[]
  count: number
}

export interface OrgBatchDecision {
  id?: string
  batch_id?: string
  delivery_id?: string
  decision?: string
  target_run_id?: string
  relevance?: number
  urgency?: number
  reason?: string
  created_at?: string
}

export interface OrgBatchDecisionsResponse {
  decisions: OrgBatchDecision[]
  count: number
}

// ── Per-unit activity settings ──────────────────────────────────────────────

export interface OrgSettingSource {
  key: string
  org_unit_id?: string
  origin: 'local' | 'inherited' | 'system' | 'default' | string
  value?: unknown
}

export interface OrgActivitySettings {
  org_unit_id: string
  /** Typed effective values keyed by the canonical (Go-style) key. */
  settings: Record<string, unknown>
  /** Raw effective values as strings — the PUT contract speaks strings. */
  values: Record<string, string>
  sources: Record<string, OrgSettingSource>
  /** Local overrides only — a key here can go back to «inherit». */
  local: Record<string, string>
  keys: string[]
}

// ── Plan graph ──────────────────────────────────────────────────────────────

export interface OrgPlanGraphNode {
  id: string
  kind: 'task' | 'run' | 'artifact' | string
  label: string
  status?: string
  org_node_id?: string
}

export interface OrgPlanGraphEdge {
  source: string
  target: string
  kind: 'executes' | 'delegates' | 'produces' | 'related' | string
  evidence?: string
}

export interface OrgPlanGraphResponse {
  nodes: OrgPlanGraphNode[]
  edges: OrgPlanGraphEdge[]
  count?: number
}

// ── Details-on-demand ───────────────────────────────────────────────────────

export type OrgDetailLevel = 'summary' | 'findings' | 'decisions' | 'artifacts' | 'trace'

export interface OrgDetailsResponse {
  run_id: string
  level: string
  details: unknown
}

// ── Filters ─────────────────────────────────────────────────────────────────

export interface OrgScopeFilters {
  org_node?: string
  /** unit | subtree */
  scope?: string
}

export interface OrgActivityFilters extends OrgScopeFilters {
  from?: string
  to?: string
  /** Comma-separated event types. */
  type?: string
  category?: string
  run?: string
  task?: string
  actor?: string
  project?: string
  limit?: number
  offset?: number
}

export interface OrgAwarenessFlowFilters extends OrgScopeFilters {
  batch_id?: string
  source_run?: string
  target_run?: string
  decision?: string
  status?: string
  actor?: string
  from?: string
  to?: string
  limit?: number
  offset?: number
}

// ── Transport ───────────────────────────────────────────────────────────────

function query(params?: Record<string, string | number | undefined>): string {
  const search = new URLSearchParams()
  Object.entries(params ?? {}).forEach(([key, value]) => {
    if (value !== undefined && value !== '') search.set(key, String(value))
  })
  // NB: no URLSearchParams.size — the HTTP stand keeps working on older
  // engines (cf. apiUrl in api.ts).
  const suffix = search.toString()
  return suffix ? `?${suffix}` : ''
}

async function request<T>(path: string, init?: RequestInit, params?: Record<string, string | number | undefined>): Promise<T> {
  let response: Response
  try {
    response = await fetch(`${KERNEL_API}${path}${query(params)}`, {
      ...init,
      headers: {
        Accept: 'application/json',
        ...(init?.body ? { 'Content-Type': 'application/json' } : {}),
        ...authHeaders(),
      },
    })
  } catch {
    // No HTTP answer at all — the server is unreachable; the calm overlay
    // owns this story (cf. kernelApi.request).
    reportKernelTransportFailure('unreachable')
    throw new Error('server unreachable — no connection')
  }
  if (!response.ok) {
    if (response.status === 502 || response.status === 503 || response.status === 504) {
      reportKernelTransportFailure('gateway')
    }
    const detail = await response.text().catch(() => '')
    throw new Error(`${response.status} ${response.statusText}${detail ? ` — ${detail}` : ''}`)
  }
  reportKernelReachable()
  return response.json() as Promise<T>
}

/** Mutating calls may answer 202/204 with an empty body — parse only when
 *  there is something to parse. */
async function requestSend<T>(path: string, init?: RequestInit): Promise<T> {
  let response: Response
  try {
    response = await fetch(`${KERNEL_API}${path}`, {
      ...init,
      headers: {
        Accept: 'application/json',
        ...(init?.body ? { 'Content-Type': 'application/json' } : {}),
        ...authHeaders(),
      },
    })
  } catch {
    reportKernelTransportFailure('unreachable')
    throw new Error('server unreachable — no connection')
  }
  if (!response.ok) {
    if (response.status === 502 || response.status === 503 || response.status === 504) {
      reportKernelTransportFailure('gateway')
    }
    const detail = await response.text().catch(() => '')
    throw new Error(`${response.status} ${response.statusText}${detail ? ` — ${detail}` : ''}`)
  }
  reportKernelReachable()
  const text = await response.text()
  if (!text) return {} as T
  try { return JSON.parse(text) as T } catch { return {} as T }
}

export const orgApi = {
  // Boards
  activity: (filters: OrgActivityFilters) => request<OrgActivityResponse>('/v1/org/activity', undefined, { ...filters }),
  activeRuns: (filters: OrgScopeFilters & { status?: string; limit?: number }) =>
    request<OrgActiveRunsResponse>('/v1/org/active-runs', undefined, { ...filters }),
  workView: (filters: OrgScopeFilters) => request<OrgWorkView>('/v1/org/work-view', undefined, { ...filters }),
  awarenessFlow: (filters: OrgAwarenessFlowFilters) => request<OrgFlowResponse>('/v1/org/awareness-flow', undefined, { ...filters }),
  planGraph: (filters: OrgScopeFilters) => request<OrgPlanGraphResponse>('/v1/org/plan-graph', undefined, { ...filters }),

  // Per-unit activity settings (values travel as strings, per the PUT contract)
  activitySettings: (unitID: string) =>
    request<OrgActivitySettings>(`/v1/org/units/${encodeURIComponent(unitID)}/activity-settings`),
  putActivitySetting: (unitID: string, key: string, value: string) =>
    requestSend<Record<string, unknown>>(`/v1/org/units/${encodeURIComponent(unitID)}/activity-settings`, {
      method: 'PUT',
      body: JSON.stringify({ [key]: String(value) }),
    }),
  deleteActivitySetting: (unitID: string, key: string) =>
    requestSend<Record<string, unknown>>(`/v1/org/units/${encodeURIComponent(unitID)}/activity-settings/${encodeURIComponent(key)}`, {
      method: 'DELETE',
    }),

  // Awareness actions
  dismissDelivery: (deliveryID: string) =>
    requestSend<Record<string, unknown>>('/v1/org/awareness/dismiss', {
      method: 'POST',
      body: JSON.stringify({ delivery_id: deliveryID }),
    }),
  awarenessFollowUp: (deliveryID: string, actionRef: string) =>
    requestSend<Record<string, unknown>>('/v1/org/awareness/follow-up', {
      method: 'POST',
      body: JSON.stringify({ delivery_id: deliveryID, action_ref: actionRef, status: 'acted_upon' }),
    }),
  awarenessDetails: (sourceRunID: string, detailLevel: OrgDetailLevel, deliveryID: string) =>
    request<OrgDetailsResponse>('/v1/org/awareness/details', {
      method: 'POST',
      body: JSON.stringify({ source_run_id: sourceRunID, detail_level: detailLevel, delivery_id: deliveryID }),
    }),

  // Batches
  createActivityBatch: (orgNodeID: string) =>
    requestSend<{ batch_id?: string }>('/v1/org/activity-batches', {
      method: 'POST',
      body: JSON.stringify({ org_node_id: orgNodeID }),
    }),
  activityBatches: (filters: { org_node?: string; limit?: number }) =>
    request<OrgActivityBatchesResponse>('/v1/org/activity-batches', undefined, { ...filters }),
  activityBatch: (batchID: string) =>
    request<OrgActivityBatch>(`/v1/org/activity-batches/${encodeURIComponent(batchID)}`),
  activityBatchDecisions: (batchID: string) =>
    request<OrgBatchDecisionsResponse>(`/v1/org/activity-batches/${encodeURIComponent(batchID)}/decisions`),
}
