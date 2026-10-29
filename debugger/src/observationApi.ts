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
  /** "claim" (model-authored) vs "observation" (kernel heuristic). */
  kind?: string
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

export interface KnowledgeFacets {
  states: Record<string, number>
  scopes: Record<string, number>
  topics?: Record<string, number>
  entities?: Record<string, number>
  projects?: Record<string, number>
  kinds?: Record<string, number>
}
export interface KnowledgeListFilters {
  q?: string
  state?: string
  scope?: string
  topic?: string
  entity?: string
  /** Sticky area filter from the explorer's map: matches the value against
   *  topic OR entity (the map merges both dimensions by name). */
  area?: string
  /** Birth project of shared knowledge items (server calls it "origin" —
   * "project" is already the visibility parameter). */
  origin?: string
  kind?: string
  /** Exact-item lookup: the server filters the projection to this id (404
   * when it is not visible in the project). Used by the conflict panel to
   * resolve propositions behind compare results. */
  knowledge_id?: string
  limit?: number
  cursor?: string
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
  events: (project?: string, cursor?: string, sourceID?: string, eventID?: string, filters?: { run?: string; type?: string; limit?: number; since?: string; until?: string; order?: 'asc' | 'desc'; types?: string[] }) => request<{ events: ObservationEvent[]; count: number; next_cursor?: string }>(
    '/v1/observations/events', undefined, { project, limit: filters?.limit ?? 100, cursor, source_id: sourceID, event_id: eventID, run: filters?.run, type: filters?.type, since: filters?.since, until: filters?.until, order: filters?.order, types: filters?.types?.join(',') }),
  event: async (sourceID: string, eventID: string) => {
    const page = await request<{ events: ObservationEvent[]; count: number }>('/v1/observations/events', undefined, { source_id: sourceID, event_id: eventID, limit: 1 })
    const event = page.events[0]
    if (!event) throw new Error('Source event was not found')
    return event
  },
  knowledge: (project: string, asOf?: string, knownAt?: string, filters?: KnowledgeListFilters) => request<{ knowledge: KnowledgeItem[]; count: number; total?: number; next_cursor?: string; facets?: KnowledgeFacets }>(
    '/v1/observations/knowledge', undefined, {
      project, as_of: asOf, known_at: knownAt,
      q: filters?.q, state: filters?.state, scope: filters?.scope,
      topic: filters?.topic, entity: filters?.entity, origin: filters?.origin, kind: filters?.kind,
      area: filters?.area,
      knowledge_id: filters?.knowledge_id,
      limit: filters?.limit, cursor: filters?.cursor,
    }),
  invalidate: (body: { knowledge_id: string; project: string; actor: ObservationActor; reason: string; /** Successor knowledge: turns the invalidation into a correction (knowledge.superseded). */ replacement_id?: string; evidence?: ObservationEvidence[] }) =>
    post<ObservationEvent>('/v1/observations/knowledge/invalidate', body),
  /** Manual knowledge creation (the supervisor's generalize action): the
   *  projection builds knowledge from knowledge.proposed events, so appending
   *  one through the regular ingest endpoint creates the item — the same
   *  path run extraction uses. Idempotent per knowledge_id: an identical
   *  re-proposal strengthens the existing node (observation/knowledge.go). */
  proposeKnowledge: (body: { knowledge_id: string; proposition: string; topics?: string[]; entities?: string[]; project: string; actor: ObservationActor }) =>
    post<{ accepted: number; repeated: number }>('/v1/observations/events', {
      events: [{
        schema: 'temporality.event/1',
        event_id: `manual/${body.knowledge_id}/${Date.now()}`,
        occurred_at: new Date().toISOString(),
        source: { id: 'supervisor-queue', integration: 'temporality-debugger', version: '1' },
        context: { project: body.project, actor: body.actor },
        type: 'knowledge.proposed',
        data: { knowledge_id: body.knowledge_id, proposition: body.proposition, topics: body.topics ?? [], entities: body.entities ?? [] },
      }],
    }),
  challenge: (body: { knowledge_id: string; project: string; actor: ObservationActor; reason: string; evidence?: ObservationEvidence[] }) =>
    post<ObservationEvent>('/v1/observations/knowledge/challenge', body),
  promote: (body: { knowledge_id: string; project: string; actor: ObservationActor; scope_kind: 'org_unit' | 'organization'; scope_id?: string; reason: string; evidence?: ObservationEvidence[] }) =>
    post<ObservationEvent>('/v1/observations/knowledge/promote', body),
  hints: (body: { project: string; query?: string; tool?: string; tool_result?: string; entities?: string[]; topics?: string[]; limit?: number }) =>
    post<{ activation_id: string; matcher: string; context_block: unknown; hints: ObservationHint[]; count: number }>('/v1/observations/hints', body),
  explore: (body: {
    project: string
    operation: 'orient' | 'search' | 'expand' | 'trace' | 'compare' | 'synthesize'
    question?: string
    targets?: string[]
    relations?: string[]
    max_depth?: number
    max_items?: number
    include_candidates?: boolean
    as_of?: string
    known_at?: string
  }) => post<ExploreResult>('/v1/observations/knowledge/explore', body),
  eventById: (eventID: string, opts?: { source_id?: string; project?: string }) =>
    request<{ event: ObservationEvent }>(`/v1/observations/events/${encodeURIComponent(eventID)}`, undefined, { source_id: opts?.source_id, project: opts?.project }),
  // Semantic map: every embedded knowledge item visible to the caller — raw
  // vectors + render meta; the client projects them to 2D. offset/total page
  // through the stable freshness order, so the map streams in batches.
  // The map request can legitimately run for a while (the journal computes
  // the project's knowledge projection on cache misses). 120s with a clear
  // failure beats a request that hangs forever with no feedback.
  knowledgeEmbeddingMap: async (opts?: { model?: string; project?: string; limit?: number; offset?: number }) => {
    try {
      return await request<{ items: Array<import('./semanticMap').MapItem>; model: string; total?: number; knowledge_total?: number; project_errors?: string[] }>('/v1/observations/knowledge/embeddings/map', { signal: AbortSignal.timeout(120_000) }, { model: opts?.model, project: opts?.project, limit: opts?.limit, offset: opts?.offset })
    } catch (e) {
      if (e instanceof DOMException && (e.name === 'AbortError' || e.name === 'TimeoutError')) {
        throw new Error('journal did not answer the map request in 120 s — the knowledge projection is likely recomputing, see the journal log')
      }
      throw e
    }
  },
  healthStats: (project?: string, since?: string) =>
    request<HealthStats>('/v1/observations/stats/health', undefined, { project, since }),
  // Hint tier effectiveness (docs/supervisor-model.md §6.6): helpful/harmful
  // rates per matcher tier. Lives on the JOURNAL (the matcher's own
  // telemetry), not on the kernel — the workspaceApi twin of this call 404'd
  // forever, leaving the supervisor's tier panel silently empty.
  hintEffectiveness: (project?: string) =>
    request<{ tiers: { tier: string; helpful: number; harmful: number; helpful_rate_pct: number }[]; since: string }>(
      '/v1/observations/hints/effectiveness', undefined, { project }),
  // Resolve one knowledge id to its owning project in a single request: the
  // supervisor fallback used to probe every visible project serially, and
  // each miss surfaced as a 404 in the network log.
  resolveKnowledge: (knowledgeID: string) =>
    request<{ knowledge: KnowledgeItem[]; count: number; project?: string }>('/v1/observations/knowledge/resolve', undefined, { knowledge_id: knowledgeID }),
}

/** All events of one run, oldest-first, following journal cursors to the end.
 *  The journal pages forward from the OLDEST event, so a single limited
 *  request silently truncates long runs — terminal events (human.answered,
 *  run.completed) would never reach the UI and pending question cards would
 *  stay forever. The cap keeps the poll bounded for pathological runs. */
export async function runEvents(run: string, project?: string, cap = 4000): Promise<ObservationEvent[]> {
  const pageSize = 500
  const collected: ObservationEvent[] = []
  let cursor: string | undefined
  do {
    const page = await observationApi.events(project, cursor, undefined, undefined, { run, limit: pageSize })
    collected.push(...page.events)
    cursor = page.next_cursor
  } while (cursor && collected.length < cap)
  return collected
}

// ── Health stats (observation/health.go) ──────────────────────────────────────
// Server-side aggregation over a time window (docs/system-dashboard.md §3.1):
// the dashboard renders these counters, it never folds raw events client-side.

export interface HealthModelStats {
  model: string
  provider?: string
  calls: number
  completed: number
  failed: number
  in_flight: number
  retries: number
  nudged: number
  p50_latency_ms: number
  p95_latency_ms: number
  total_tokens: number
  cached_tokens: number
}

export interface HealthStats {
  since: string
  generated_at: string
  models: HealthModelStats[]
  totals: { model_calls: number; model_failures: number; in_flight: number; nudged: number }
  runs: { started: number; completed: number; failed: number; cancelled: number; running_now: number }
  approvals: { requested: number; granted: number; rejected: number; timed_out: number; auto_granted: number; pending: number; wait_ms_avg: number }
  /** Human factors (docs/supervisor-model.md §5): HAB — estimated attention,
   *  HWT — wall-clock wait for a human, flow efficiency = HAB/(HAB+HWT). */
  human?: { approval_decisions: number; ask_human_answered: number; hab_seconds: number; hwt_seconds: number; flow_efficiency: number }
}

// ── Explore engine contracts (observation/explore.go) ─────────────────────────
// The read-only research surface: orient maps the areas, search explains why
// each card matched (docs/human-knowledge-explorer.md §4–5).

export interface ExploreKnowledgeCard {
  knowledge_id: string
  proposition: string
  state: string
  kind?: string
  topics?: string[]
  entities?: string[]
  project?: string
  scope_kind?: string
  scope_id?: string
  matched_by?: string[]
  score?: number
  depth?: number
  via?: string
  basis?: string
  at_risk?: boolean
  replacement_id?: string
  evidence?: number
}

export interface ExploreArea {
  name: string
  kind: 'topic' | 'entity'
  knowledge_count: number
  states: Record<string, number>
  score?: number
  matched_by?: string[]
  representative?: ExploreKnowledgeCard[]
  contradictions?: number
}

export interface ExploreLimits {
  max_items: number
  max_depth: number
  items_returned: number
  truncated: boolean
}

export interface ExploreRelation {
  from_id: string
  to_id: string
  type: string
  /** explicit (recorded knowledge.linked event) | candidate (heuristic). */
  basis: string
  event_id?: string
  depth?: number
  note?: string
}

export interface ExploreHistoryStep {
  event_id: string
  type: string
  state?: string
  rule?: string
  at: string
  actor_id?: string
  reason?: string
}

/** Hint telemetry recorded for the item (offer/use/ignore/outcome counters). */
export interface ExploreTraceUsage {
  hint_offers: number
  hint_uses: number
  hint_ignores: number
  helpful_outcomes: number
  harmful_outcomes: number
  reuse_count: number
}

/** Provenance dossier of one knowledge item: full lifecycle steps plus usage
 *  statistics, straight from the projection (ТЗ §4.4). */
export interface ExploreHistory {
  knowledge_id: string
  proposition: string
  state: string
  created_at: string
  created_by?: string
  steps?: ExploreHistoryStep[]
  usage: ExploreTraceUsage
  replacement_id?: string
  at_risk?: boolean
  evidence?: ObservationEvidence[]
}

export interface ExploreConflict {
  knowledge_ids: string[]
  /** explicit:contradicts | candidate:same-topic-different-state | resolved:replacement. */
  basis: string
  detail: string
  context_differences?: string[]
  resolved: boolean
  resolution?: string
}

/** Synthesis dossier of the explore engine (observation/explore.go
 *  ExploreSynthesis): established/disputed/retired are grounded in the
 *  working set; gaps/next_actions are heuristics — the interpretation flag
 *  keeps them visibly distinct from content (ТЗ §4.6, §12.3). */
export interface ExploreSynthesis {
  established?: ExploreKnowledgeCard[]
  disputed?: ExploreKnowledgeCard[]
  retired?: ExploreKnowledgeCard[]
  conflicts?: ExploreConflict[]
  gaps?: string[]
  next_actions?: string[]
  references?: string[]
  interpretation: boolean
}

export interface ExploreResult {
  operation: string
  algorithm_version: string
  areas?: ExploreArea[]
  results?: ExploreKnowledgeCard[]
  relations?: ExploreRelation[]
  traces?: ExploreHistory[]
  conflicts?: ExploreConflict[]
  synthesis?: ExploreSynthesis
  gaps?: string[]
  next_actions?: string[]
  limits: ExploreLimits
  completeness: string
  notes?: string[]
}
