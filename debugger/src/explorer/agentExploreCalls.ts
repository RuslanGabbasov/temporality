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
