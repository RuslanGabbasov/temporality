import { apiUrl, authHeaders } from './api'

export interface ObservationActor { id: string; type?: string }
export interface ObservationEvidence { ref: string; type?: string }
export interface ObservationContext { project?: string; run?: string; task?: string; actor?: ObservationActor; parent_event_id?: string }
export interface ObservationEvent {
  schema: string
  event_id: string
  occurred_at: string
  received_at: string
  source: { id: string; integration: string; version?: string }
  context?: ObservationContext
  type: string
  data?: Record<string, unknown>
  evidence?: ObservationEvidence[]
}
export interface KnowledgeTransition {
  event_id: string
  source_id: string
  type: string
  state: string
  rule?: string
  at: string
  actor?: ObservationActor
  evidence?: ObservationEvidence[]
  reason?: string
}
export interface KnowledgeItem {
  id: string
  proposition: string
  topics?: string[]
  entities?: string[]
  state: string
  project?: string
  /** Effective visibility: absent means "project" (the birth scope);
   * org_unit/organization arrive only through explicit promotion. */
  scope_kind?: 'project' | 'org_unit' | 'organization'
  scope_id?: string
  promoted_by?: ObservationActor
  promoted_at?: string
  created_at: string
  updated_at: string
  created_by?: ObservationActor
  reuse_count: number
  hint_offers: number
  hint_uses: number
  hint_ignores: number
  helpful_outcomes: number
  harmful_outcomes: number
  relationships?: { type: string; target_id: string; event_id: string }[]
  at_risk?: boolean
  risk_sources?: string[]
  last_used_at?: string
  replacement_id?: string
  evidence?: ObservationEvidence[]
  history: KnowledgeTransition[]
}
export interface ObservationHint {
  knowledge_id: string
  hint_id: string
  offer_event_id: string
  proposition: string
  state: string
  matched_by: string[]
  caution?: string
  evidence?: ObservationEvidence[]
  history: KnowledgeTransition[]
}

async function request<T>(path: string, init?: RequestInit, query?: Record<string, string | number | undefined>): Promise<T> {
  const response = await fetch(apiUrl(path, query), {
    ...init,
    headers: { Accept: 'application/json', ...authHeaders(), ...(init?.body ? { 'Content-Type': 'application/json' } : {}), ...init?.headers },
  })
  if (!response.ok) {
    const detail = await response.text().catch(() => '')
    throw new Error(`${response.status} ${response.statusText}${detail ? ` — ${detail}` : ''}`)
  }
  return response.json() as Promise<T>
}

const post = <T>(path: string, body: unknown) => request<T>(path, { method: 'POST', body: JSON.stringify(body) })

export const observationApi = {
  events: (project?: string, cursor?: string, sourceID?: string, eventID?: string, filters?: { run?: string; type?: string; limit?: number }) => request<{ events: ObservationEvent[]; count: number; next_cursor?: string }>(
    '/v1/observations/events', undefined, { project, limit: filters?.limit ?? 100, cursor, source_id: sourceID, event_id: eventID, run: filters?.run, type: filters?.type }),
  event: async (sourceID: string, eventID: string) => {
    const page = await request<{ events: ObservationEvent[]; count: number }>('/v1/observations/events', undefined, { source_id: sourceID, event_id: eventID, limit: 1 })
    const event = page.events[0]
    if (!event) throw new Error('Source event was not found')
    return event
  },
  knowledge: (project: string, asOf?: string, knownAt?: string) => request<{ knowledge: KnowledgeItem[]; count: number }>(
    '/v1/observations/knowledge', undefined, { project, as_of: asOf, known_at: knownAt }),
  invalidate: (body: { knowledge_id: string; project: string; actor: ObservationActor; reason: string; evidence?: ObservationEvidence[] }) =>
    post<ObservationEvent>('/v1/observations/knowledge/invalidate', body),
  promote: (body: { knowledge_id: string; project: string; actor: ObservationActor; scope_kind: 'org_unit' | 'organization'; scope_id?: string; reason: string; evidence?: ObservationEvidence[] }) =>
    post<ObservationEvent>('/v1/observations/knowledge/promote', body),
  hints: (body: { project: string; query?: string; tool?: string; tool_result?: string; entities?: string[]; topics?: string[]; limit?: number }) =>
    post<{ activation_id: string; matcher: string; context_block: unknown; hints: ObservationHint[]; count: number }>('/v1/observations/hints', body),
}
