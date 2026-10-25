import type { ObservationEvent } from '../observationApi'

/** One knowledge_explore call extracted from a run's journal: the arguments
 *  exactly as the agent sent them, so the replay is faithful (ТЗ §12.2). */
export interface AgentExploreCall {
  eventID: string
  at: string
  turn: number
  operation: string
  question: string
  targets: string[]
  relations: string[]
  maxDepth: number
  maxItems: number
  includeCandidates: boolean
  /** Links the call to its tool.completed event (what the agent got back). */
  operationID: string
}

function stringList(value: unknown): string[] {
  if (!Array.isArray(value)) return []
  return value.filter((item): item is string => typeof item === 'string' && item.trim() !== '')
}

function intOrZero(value: unknown): number {
  return typeof value === 'number' && Number.isFinite(value) ? value : 0
}

/** Parse the `arguments` payload of a tool.started event. The journal stores
 *  it as a JSON string; an already-decoded object (older fixtures) is fine
 *  too. Returns null for anything without an operation — malformed calls are
 *  shown as-is by the run trace, not replayed here. */
export function parseExploreArguments(data: unknown): AgentExploreCall | null {
  if (!data || typeof data !== 'object') return null
  const raw = data as Record<string, unknown>
  let args: unknown = raw.arguments
  if (typeof args === 'string') {
    try { args = JSON.parse(args) } catch { return null }
  }
  if (!args || typeof args !== 'object' || Array.isArray(args)) return null
  const parsed = args as Record<string, unknown>
  const operation = typeof parsed.operation === 'string' ? parsed.operation : ''
  	if (!operation) return null
  	return {
  		eventID: '',
  		at: '',
  		turn: intOrZero(raw.turn),
  		operation,
  		question: typeof parsed.question === 'string' ? parsed.question : '',
  		targets: stringList(parsed.targets),
  		relations: stringList(parsed.relations),
  		maxDepth: intOrZero(parsed.max_depth),
  		maxItems: intOrZero(parsed.max_items),
  		includeCandidates: parsed.include_candidates === true,
  		operationID: typeof raw.operation_id === 'string' ? raw.operation_id : '',
  	}
  }

/** Extract every knowledge_explore call of a run, oldest first: only
 *  tool.started events of that tool carry the agent's own arguments;
 *  completions and other tools are noise here (ТЗ §12.1). */
export function extractExploreCalls(events: ObservationEvent[]): AgentExploreCall[] {
  const calls: AgentExploreCall[] = []
  for (const event of events) {
    if (event.type !== 'tool.started') continue
    const data = event.data
    if (!data || typeof data !== 'object' || (data as Record<string, unknown>).tool !== 'knowledge_explore') continue
    const call = parseExploreArguments(data)
    if (!call) continue
    call.eventID = event.event_id
    call.at = event.occurred_at
    calls.push(call)
  }
  return calls.sort((a, b) => a.at.localeCompare(b.at))
}

/** What the agent got back from one call: the tool.completed output is the
 *  explore JSON, triple-encoded by the tool pipeline («\ »{...}« » strings
 *  inside strings) — peel until it parses. */
export interface ExploreCallResult {
  areas: Array<{ name: string; knowledge_count?: number }>
  results: Array<{ knowledge_id: string; proposition: string; state?: string }>
  relations: number
  empty: boolean
}

function peelJSON(raw: unknown, depth = 0): unknown {
  let value = raw
  for (let i = 0; i < 4 && typeof value === 'string'; i++) {
    try { value = JSON.parse(value) } catch { return value }
  }
  if (depth > 0) return value
  return value
}

export function extractCallResult(event: ObservationEvent): ExploreCallResult | null {
  if (event.type !== 'tool.completed') return null
  const data = event.data as Record<string, unknown> | undefined
  if (!data || data.tool !== 'knowledge_explore') return null
  const parsed = peelJSON(data.output) as Record<string, unknown> | undefined
  if (!parsed || typeof parsed !== 'object') {
    return { areas: [], results: [], relations: 0, empty: true }
  }
  const results = Array.isArray(parsed.results)
    ? (parsed.results as Array<Record<string, unknown>>).map((row) => ({
        knowledge_id: String(row.knowledge_id ?? ''),
        proposition: String(row.proposition ?? ''),
        state: String(row.state ?? ''),
      })).filter((row) => row.proposition)
    : []
  const areas = Array.isArray(parsed.areas)
    ? (parsed.areas as Array<Record<string, unknown>>).map((row) => ({
        name: String(row.name ?? ''),
        knowledge_count: intOrZero(row.knowledge_count),
      }))
    : []
  const relations = Array.isArray(parsed.relations) ? parsed.relations.length : 0
  return { areas, results, relations, empty: results.length === 0 && areas.length === 0 && relations === 0 }
}

/** The run's hint telemetry (what the research turned into downstream):
 *  offered hints, how many were used, and the helpful/harmful grades. */
export interface RunHintOutcome {
  offered: number
  used: number
  helpful: number
  harmful: number
  /** The propositions that were offered to this run's agents. */
  propositions: Array<{ id: string; proposition: string; state: string; used: boolean }>
}

export function extractRunHintOutcome(events: ObservationEvent[]): RunHintOutcome {
  const offered = new Map<string, { id: string; proposition: string; state: string }>()
  const usedHints = new Set<string>()
  let helpful = 0, harmful = 0
  for (const event of events) {
    const data = event.data as Record<string, unknown> | undefined
    if (!data) continue
    if (event.type === 'hint.offered') {
      const hintID = typeof data.hint_id === 'string' ? data.hint_id : ''
      const id = typeof data.knowledge_id === 'string' ? data.knowledge_id : ''
      const proposition = typeof data.proposition === 'string' ? data.proposition : ''
      if (hintID && id && !offered.has(hintID)) offered.set(hintID, { id, proposition, state: String(data.state ?? '') })
    } else if (event.type === 'hint.used') {
      const hintID = typeof data.hint_id === 'string' ? data.hint_id : ''
      if (hintID) usedHints.add(hintID)
    } else if (event.type === 'hint.outcome') {
      const outcome = String(data.outcome ?? '')
      if (outcome === 'helpful') helpful++
      else if (outcome === 'harmful') harmful++
    }
  }
  return {
    offered: offered.size,
    used: usedHints.size,
    helpful, harmful,
    // Dedupe by knowledge id: the same statement may ride several hints;
    // «used» = at least one of its hints was actually opened by the agent.
    propositions: [...new Map(
      [...offered.entries()].map(([hintID, entry]) => [entry.id, { ...entry, used: usedHints.has(hintID) }]),
    ).values()].map((entry) => {
      const anyUsed = [...offered.entries()].some(([hintID, offeredEntry]) => offeredEntry.id === entry.id && usedHints.has(hintID))
      return { ...entry, used: anyUsed }
    }),
  }
}
