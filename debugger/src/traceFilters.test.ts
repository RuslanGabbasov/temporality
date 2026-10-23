import { describe, expect, it } from 'vitest'
import type { ObservationEvent } from './observationApi'
import { ru } from './locales/ru'
import { en } from './locales/en'
import { foldTrace, type TraceItem } from './traceModel'
import { timelineRow } from './eventPresentation'
import {
  EMPTY_FILTER,
  buildFilterIndex,
  countMatchingMembers,
  durationMsOf,
  filterTraceItems,
  haystackOf,
  matchesChipSet,
  matchesFilter,
  parseTraceState,
  participantOf,
  payloadTextOf,
  searchItems,
  snippetAround,
  stripTraceParams,
  traceStateQuery,
  TRACE_QUERY_KEYS,
  type FilterChip,
  type TraceFilter,
} from './traceFilters'

/** Test harness t(): real locale messages with {{var}} interpolation. */
const makeT = (dict: Record<string, string>) => (key: string, vars?: Record<string, string>): string => {
  const template = dict[key] ?? key
  if (!vars) return template
  return template.replace(/\{\{(\w+)\}\}/g, (match, name: string) => (name in vars ? vars[name] : match))
}
const t = makeT(ru)
const tEn = makeT(en)
const RU = 'ru' as const
const EN = 'en' as const

let seq = 0
const ev = (type: string, data: Record<string, unknown> = {}, second = 0): ObservationEvent => {
  seq += 1
  return {
    schema: 'temporality/observation/event/v1',
    event_id: `e${seq}`,
    occurred_at: `2026-10-22T10:00:${String(second).padStart(2, '0')}Z`,
    received_at: `2026-10-22T10:00:${String(second).padStart(2, '0')}Z`,
    source: { id: 'kernel' },
    context: { run: 'r1', project: 'p' },
    type,
    data,
  } as ObservationEvent
}

const itemsOf = (events: ObservationEvent[]): TraceItem[] => foldTrace(events, RU, t)
const chips = (...values: Array<Exclude<FilterChip, 'all'>>): ReadonlySet<FilterChip> => new Set(values)

/** Stable one-line descriptor of a surviving item, for window assertions. */
const describeItem = (item: TraceItem): string => {
  switch (item.kind) {
    case 'event': return item.event.type
    case 'group': return `group:${item.group}`
    case 'saga': return `saga:${item.sagaId}`
    case 'approvalPair': return `pair:${String(item.requested.data?.operation_id ?? item.requested.event_id)}`
  }
}
const labels = (items: readonly TraceItem[]): string[] => items.map(describeItem)

describe('participantOf (§8: who acts)', () => {
  it('maps every participant family', () => {
    expect(participantOf(ev('model.completed'))).toBe('model')
    expect(participantOf(ev('tool.completed'))).toBe('tool')
    expect(participantOf(ev('mcp.call.completed'))).toBe('tool')
    expect(participantOf(ev('human.answered'))).toBe('human')
    expect(participantOf(ev('run.user_note'))).toBe('human')
    expect(participantOf(ev('hitl.verdict.recorded'))).toBe('human')
    expect(participantOf(ev('approval.granted'))).toBe('human')
    expect(participantOf(ev('hint.used'))).toBe('human')
    expect(participantOf(ev('run.started'))).toBe('system')
    expect(participantOf(ev('knowledge.used'))).toBe('system')
    expect(participantOf(ev('totally.unknown.type'))).toBe('system')
  })
})

describe('matchesFilter — the structured §8 dimensions', () => {
  it('category keeps only rows of the catalog group', () => {
    const items = itemsOf([
      ev('model.completed', { total_tokens: 10 }, 1),
      ev('tool.completed', { tool: 'read_file', exit_code: 0 }, 2),
      ev('run.started', {}, 3),
    ])
    expect(labels(items)).toEqual(['model.completed', 'tool.completed', 'run.started'])
    expect(matchesFilter(EMPTY_FILTER, items[0])).toBe(true)
    expect(labels(filterTraceItems(items, { ...EMPTY_FILTER, category: 'tools' }, chips(), RU, t))).toEqual(['tool.completed'])
  })

  it('type matches a row or either half of a pair', () => {
    const items = itemsOf([
      ev('approval.requested', { operation_id: 'op-1', action: 'deploy' }, 1),
      ev('approval.granted', { operation_id: 'op-1', approver: 'ruslan' }, 2),
      ev('model.completed', {}, 3),
    ])
    expect(labels(filterTraceItems(items, { ...EMPTY_FILTER, type: 'approval.granted' }, chips(), RU, t))).toEqual(['pair:op-1'])
    expect(labels(filterTraceItems(items, { ...EMPTY_FILTER, type: 'model.completed' }, chips(), RU, t))).toEqual(['model.completed'])
    expect(filterTraceItems(items, { ...EMPTY_FILTER, type: 'tool.failed' }, chips(), RU, t)).toHaveLength(0)
  })

  // The checkbox dropdown selects several types: filter.type carries them
  // comma-separated, a row matches when ANY listed type is among its events.
  it('type matches any of the comma-separated list', () => {
    const items = itemsOf([
      ev('approval.requested', { operation_id: 'op-1', action: 'deploy' }, 1),
      ev('approval.granted', { operation_id: 'op-1', approver: 'ruslan' }, 2),
      ev('model.completed', {}, 3),
      ev('run.started', {}, 4),
    ])
    expect(labels(filterTraceItems(items, { ...EMPTY_FILTER, type: 'approval.granted,model.completed' }, chips(), RU, t)))
      .toEqual(['pair:op-1', 'model.completed'])
    expect(filterTraceItems(items, { ...EMPTY_FILTER, type: 'run.completed, tool.failed' }, chips(), RU, t)).toHaveLength(0)
  })

  it('status keeps matching rows; a folded group has no own status', () => {
    const items = itemsOf([
      ev('run.failed', { error: 'boom' }, 1),
      ev('run.completed', { turns: 2 }, 2),
      ev('turn.started', { turn: 1 }, 3),
    ])
    expect(labels(filterTraceItems(items, { ...EMPTY_FILTER, status: 'error' }, chips(), RU, t))).toEqual(['run.failed'])
    expect(labels(filterTraceItems(items, { ...EMPTY_FILTER, status: 'ok' }, chips(), RU, t))).toEqual(['run.completed'])
    const group = items.find((item) => item.kind === 'group')
    expect(group).toBeTruthy()
    expect(matchesFilter({ ...EMPTY_FILTER, status: 'error' }, group as TraceItem)).toBe(true)
  })

  it('a pair filters by its decision status; a running pair matches none', () => {
    const items = itemsOf([
      ev('approval.requested', { operation_id: 'op-1', action: 'deploy' }, 1),
      ev('approval.granted', { operation_id: 'op-1' }, 2),
      ev('approval.requested', { operation_id: 'op-2', action: 'drop-db' }, 3),
      ev('approval.rejected', { operation_id: 'op-2', reason: 'no' }, 4),
      ev('approval.requested', { operation_id: 'op-3', action: 'ask' }, 5),
    ])
    expect(labels(filterTraceItems(items, { ...EMPTY_FILTER, status: 'ok' }, chips(), RU, t))).toEqual(['pair:op-1'])
    expect(labels(filterTraceItems(items, { ...EMPTY_FILTER, status: 'error' }, chips(), RU, t))).toEqual(['pair:op-2'])
    expect(filterTraceItems(items, { ...EMPTY_FILTER, status: 'cancelled' }, chips(), RU, t)).toHaveLength(0)
  })

  it('participant keeps model/tool/human/system rows', () => {
    const items = itemsOf([
      ev('model.completed', {}, 1),
      ev('tool.completed', { tool: 'read_file' }, 2),
      ev('human.requested', { operation_id: 'op-1', question: 'лимит?' }, 3),
      ev('human.answered', { operation_id: 'op-1', response: '500' }, 4),
      ev('run.started', {}, 5),
    ])
    expect(labels(filterTraceItems(items, { ...EMPTY_FILTER, participant: 'model' }, chips(), RU, t))).toEqual(['model.completed'])
    expect(labels(filterTraceItems(items, { ...EMPTY_FILTER, participant: 'tool' }, chips(), RU, t))).toEqual(['tool.completed'])
    expect(labels(filterTraceItems(items, { ...EMPTY_FILTER, participant: 'human' }, chips(), RU, t))).toEqual(['pair:op-1'])
    expect(labels(filterTraceItems(items, { ...EMPTY_FILTER, participant: 'system' }, chips(), RU, t))).toEqual(['run.started'])
  })

  it('minMs keeps only rows with a known duration at/above the threshold', () => {
    const events = [
      ev('model.completed', { latency_ms: 5000 }, 1),
      ev('tool.completed', { tool: 'x', duration_ms: 1200 }, 2),
      ev('run.started', { wait_ms: 2000 }, 3),
      ev('run.completed', { turns: 3 }, 4),
    ]
    const items = itemsOf(events)
    expect(durationMsOf(events[0])).toBe(5000)
    expect(durationMsOf(events[1])).toBe(1200)
    expect(durationMsOf(events[2])).toBe(2000)
    expect(durationMsOf(events[3])).toBe(0)
    expect(labels(filterTraceItems(items, { ...EMPTY_FILTER, minMs: 1000 }, chips(), RU, t)))
      .toEqual(['model.completed', 'tool.completed', 'run.started'])
    expect(labels(filterTraceItems(items, { ...EMPTY_FILTER, minMs: 4000 }, chips(), RU, t)))
      .toEqual(['model.completed'])
  })

  it('dimensions AND together', () => {
    const items = itemsOf([
      ev('tool.failed', { tool: 'run_command', error: 'panic: x' }, 1),
      ev('tool.completed', { tool: 'read_file', exit_code: 0 }, 2),
      ev('model.failed', { error: 'rate limit' }, 3),
    ])
    expect(labels(filterTraceItems(items, { ...EMPTY_FILTER, category: 'tools', status: 'error' }, chips(), RU, t)))
      .toEqual(['tool.failed'])
  })
})

describe('matchesChipSet — quick chips (§8)', () => {
  it('no active chips → everything passes', () => {
    const items = itemsOf([ev('run.completed', { turns: 1 }, 1)])
    expect(matchesChipSet(items[0], chips())).toBe(true)
    expect(matchesChipSet(items[0], new Set<FilterChip>(['all']))).toBe(true)
  })

  it('active chips OR together inside the set', () => {
    const items = itemsOf([
      ev('model.failed', { error: 'x' }, 1),
      ev('tool.completed', { tool: 'read_file', exit_code: 0 }, 2),
      ev('run.completed', { turns: 1 }, 3),
      ev('run.user_note', { note: 'поднять лимит', turn: 1 }, 4),
    ])
    expect(labels(filterTraceItems(items, EMPTY_FILTER, chips('errors', 'tools'), RU, t)))
      .toEqual(['model.failed', 'tool.completed'])
    expect(labels(filterTraceItems(items, EMPTY_FILTER, chips('myInput'), RU, t))).toEqual(['run.user_note'])
    expect(filterTraceItems(items, EMPTY_FILTER, chips('human'), RU, t)).toHaveLength(0)
  })

  it('the chip set ANDs with the structured filter', () => {
    const items = itemsOf([
      ev('model.failed', { error: 'x' }, 1),
      ev('tool.failed', { tool: 'run_command', error: 'y' }, 2),
    ])
    expect(labels(filterTraceItems(items, { ...EMPTY_FILTER, category: 'model' }, chips('errors'), RU, t)))
      .toEqual(['model.failed'])
    expect(filterTraceItems(items, { ...EMPTY_FILTER, participant: 'model' }, chips('tools'), RU, t)).toHaveLength(0)
  })

  it('a chip looks at folded group members (errors)', () => {
    const items = itemsOf([
      ev('memory.read.failed', { error: 'boom' }, 1),
      ev('storage.snapshot', {}, 2),
      ev('turn.started', { turn: 1 }, 3),
      ev('turn.completed', { turn: 1 }, 4),
    ])
    expect(labels(items)).toEqual(['group:opsNoise', 'group:turns'])
    expect(labels(filterTraceItems(items, EMPTY_FILTER, chips('errors'), RU, t))).toEqual(['group:opsNoise'])
  })
})

describe('filterTraceItems — folded groups (§8)', () => {
  it('an empty filter and no chips return the window unchanged', () => {
    const items = itemsOf([
      ev('run.started', {}, 1),
      ev('saga.registered', { saga_id: 's1', title: 'x' }, 2),
      ev('storage.snapshot', {}, 3),
      ev('approval.requested', { operation_id: 'op-1' }, 4),
      ev('approval.granted', { operation_id: 'op-1' }, 5),
    ])
    expect(labels(items)).toEqual(['run.started', 'saga:s1', 'group:opsNoise', 'pair:op-1'])
    expect(filterTraceItems(items, EMPTY_FILTER, chips(), RU, t)).toEqual(items)
  })

  it('a folded group stays while any member matches the filter', () => {
    const items = itemsOf([
      ev('hint.offered', { hint_id: 'h1', proposition: 'alpha' }, 1),
      ev('hint.offered', { hint_id: 'h2', proposition: 'beta' }, 2),
      ev('hint.used', { hint_id: 'h1', proposition: 'alpha' }, 3),
      ev('run.started', {}, 4),
    ])
    expect(labels(filterTraceItems(items, { ...EMPTY_FILTER, type: 'hint.used' }, chips(), RU, t)))
      .toEqual(['group:knowledge'])
    expect(labels(filterTraceItems(items, { ...EMPTY_FILTER, participant: 'human' }, chips(), RU, t)))
      .toEqual(['group:knowledge'])
  })

  it('a group with no matching member is dropped, not orphaned', () => {
    const items = itemsOf([
      ev('turn.started', { turn: 1 }, 1),
      ev('turn.completed', { turn: 1 }, 2),
      ev('model.completed', {}, 3),
    ])
    expect(labels(filterTraceItems(items, { ...EMPTY_FILTER, category: 'model' }, chips(), RU, t)))
      .toEqual(['model.completed'])
  })

  it('counts matching members of a group (the N/M marker)', () => {
    const items = itemsOf([
      ev('hint.offered', { hint_id: 'h1' }, 1),
      ev('hint.offered', { hint_id: 'h2' }, 2),
      ev('hint.used', { hint_id: 'h1' }, 3),
    ])
    const group = items.find((item) => item.kind === 'group' && item.group === 'knowledge') as Extract<TraceItem, { kind: 'group' }>
    expect(countMatchingMembers(group, { ...EMPTY_FILTER, type: 'hint.used' }, chips(), RU, t)).toBe(1)
    expect(countMatchingMembers(group, EMPTY_FILTER, chips(), RU, t)).toBe(3)
  })
})

describe('searchItems / haystackOf (§8 text search)', () => {
  it('is case-insensitive with partial matching over titles and facts', () => {
    const items = itemsOf([ev('model.completed', { total_tokens: 120, latency_ms: 300 }, 1)])
    expect(searchItems(items, 'ОТВЕТ', RU, t)).toHaveLength(1)
    expect(searchItems(items, 'ответ мод', RU, t)).toHaveLength(1)
    expect(searchItems(items, '120 tok', RU, t)).toHaveLength(1)
    expect(searchItems(items, 'квота', RU, t)).toHaveLength(0)
    expect(searchItems(items, '   ', RU, t)).toHaveLength(0)
  })

  it('haystack carries both locales: the current title and the other locale catalog title', () => {
    const event = ev('model.completed', { total_tokens: 5 }, 1)
    // ru: the override title «Ответ модели» + the en CATALOG title as fallback.
    const hayRu = haystackOf(event, timelineRow(event, 'ru', t), 'ru', t)
    expect(hayRu).toContain('Ответ модели')
    expect(hayRu).toContain('Model call completed')
    expect(searchItems(itemsOf([event]), 'model call completed', RU, t)).toHaveLength(1)
    // en: the override title «Model response» + the ru catalog title.
    const hayEn = haystackOf(event, timelineRow(event, EN, tEn), EN, tEn)
    expect(hayEn).toContain('Model response')
    expect(hayEn).toContain('Вызов модели завершён')
    expect(searchItems(itemsOf([event]), 'вызов модели', EN, tEn)).toHaveLength(1)
  })

  it('finds human payload values: the note, the question, the answer', () => {
    const items = itemsOf([
      ev('run.user_note', { note: 'подними лимит до 500', turn: 2 }, 1),
      ev('human.requested', { operation_id: 'op-1', question: 'Какой бюджет?' }, 2),
      ev('human.answered', { operation_id: 'op-1', response: '500 ядер' }, 3),
    ])
    expect(labels(items)).toEqual(['run.user_note', 'pair:op-1'])
    expect(searchItems(items, 'подними лимит', RU, t)).toHaveLength(1)
    expect(searchItems(items, 'бюджет', RU, t)).toHaveLength(1)
    expect(searchItems(items, '500 ядер', RU, t)).toHaveLength(1)
    expect(payloadTextOf(ev('agent.summary', { answer: 'итог' }))).toBe('итог')
  })

  it('hits carry the item, the event and a context snippet', () => {
    const items = itemsOf([ev('tool.started', { tool: 'run_command', arguments: '{"command":["go","test"]}' }, 1)])
    const [hit] = searchItems(items, 'go test', RU, t)
    expect(hit.item.kind).toBe('event')
    expect(hit.event.type).toBe('tool.started')
    expect(hit.snippet).toContain('go test')
  })

  it('snippets center on the match with context markers', () => {
    const long = `${'a'.repeat(100)}NEEDLE${'b'.repeat(100)}`
    const snip = snippetAround(long, 'needle')
    expect(snip.startsWith('…')).toBe(true)
    expect(snip.endsWith('…')).toBe(true)
    // The snippet preserves the haystack's original casing…
    expect(snip).toContain('NEEDLE')
    expect(snip.toLowerCase()).toContain('needle')
    expect(snippetAround('короткий стог', 'стог')).toBe('короткий стог')
    expect(snippetAround('ничего тут нет', 'стог')).toBe('')
  })

  it('searches the loaded console buffers and binds hits to their event', () => {
    const cmd = ev('tool.started', { tool: 'run_command', operation_id: 'op-9', arguments: '{"command":["make"]}' }, 1)
    const items = itemsOf([cmd, ev('run.completed', { turns: 1 }, 2)])
    const buffers = new Map([[cmd.event_id, 'stdout ok\npanic: line 42']])
    const hits = searchItems(items, 'panic', RU, t, buffers)
    expect(hits).toHaveLength(1)
    expect(hits[0].event.event_id).toBe(cmd.event_id)
    expect(hits[0].snippet).toContain('panic: line 42')
    // A buffer of an event outside the window never produces a hit.
    expect(searchItems(items, 'panic', RU, t, new Map([['ghost', 'panic']]))).toHaveLength(0)
  })

  it('finds folded groups through their label', () => {
    const items = itemsOf([ev('turn.started', { turn: 1 }, 1), ev('turn.completed', { turn: 1 }, 2)])
    const hits = searchItems(items, 'ходы агента', RU, t)
    expect(hits.length).toBeGreaterThan(0)
    expect(hits.every((hit) => hit.item.kind === 'group')).toBe(true)
  })

  it('P-2: secret rows are searchable by name/status only — never by value', () => {
    const items = itemsOf([
      ev('secret.value_set', { secret: 'API_TOKEN', value: 'hunter2' }, 1),
      ev('secret.denied', { secret: 'DB_PASS', reason: 'отказано политикой' }, 2),
    ])
    expect(searchItems(items, 'hunter2', RU, t)).toHaveLength(0)
    const secretRow = items[0] as Extract<TraceItem, { kind: 'event' }>
    const hay = haystackOf(secretRow.event, secretRow.row, RU, t)
    expect(hay).not.toContain('hunter2')
    expect(hay).toContain('API_TOKEN')
    expect(payloadTextOf(secretRow.event)).toBe('')
    expect(searchItems(items, 'api_token', RU, t)).toHaveLength(1)
    // The sanctioned denial reason stays searchable.
    expect(searchItems(items, 'отказано политикой', RU, t)).toHaveLength(1)
  })
})

describe('buildFilterIndex', () => {
  it('indexes unique sorted types of the window, including pair and group members', () => {
    const items = itemsOf([
      ev('approval.requested', { operation_id: 'op-1' }, 1),
      ev('approval.granted', { operation_id: 'op-1' }, 2),
      ev('hint.offered', {}, 3),
      ev('model.completed', {}, 4),
      ev('model.completed', {}, 5),
    ])
    expect(buildFilterIndex(items)).toEqual(['approval.granted', 'approval.requested', 'hint.offered', 'model.completed'])
  })
})

describe('URL state (§8: trace* params)', () => {
  const state: { filter: TraceFilter; query: string; chips: Set<FilterChip> } = {
    filter: { category: 'tools', type: 'run_command.x', status: 'error', participant: 'human', minMs: 1500 },
    query: 'паника run',
    chips: new Set<FilterChip>(['errors', 'myInput']),
  }

  it('round-trips through the query string', () => {
    const qs = traceStateQuery(state.filter, state.query, state.chips)
    const parsed = parseTraceState(new URLSearchParams(qs))
    expect(parsed.filter).toEqual(state.filter)
    expect(parsed.query).toBe(state.query)
    expect([...parsed.chips].sort()).toEqual(['errors', 'myInput'])
  })

  it('serializes nothing when the state is empty and parses the defaults', () => {
    expect(traceStateQuery(EMPTY_FILTER, '', new Set())).toBe('')
    expect(traceStateQuery(EMPTY_FILTER, '   ', new Set())).toBe('')
    const parsed = parseTraceState(new URLSearchParams(''))
    expect(parsed.filter).toEqual(EMPTY_FILTER)
    expect(parsed.query).toBe('')
    expect(parsed.chips.size).toBe(0)
  })

  it('resets unknown values per dimension — a stale link degrades to «no filter»', () => {
    const params = new URLSearchParams(
      'traceCat=no_such_group&traceType=tool.x&traceStatus=fatal&traceWho=alien&traceMinMs=-5'
      + '&traceChips=errors, bogus, all, tools&traceQ=x',
    )
    const parsed = parseTraceState(params)
    expect(parsed.filter).toEqual({ category: '', type: 'tool.x', status: '', participant: '', minMs: 0 })
    expect(parsed.chips).toEqual(new Set(['errors', 'tools']))
    expect(parsed.query).toBe('x')
  })

  it('trace* keys stay disjoint from the existing run/view params', () => {
    for (const key of Object.values(TRACE_QUERY_KEYS)) {
      expect(key.startsWith('trace')).toBe(true)
      expect(['run', 'view', 'project', 'q', 'source']).not.toContain(key)
    }
  })

  it('stripTraceParams keeps run/view/project and drops only trace* params', () => {
    expect(stripTraceParams('?project=p&run=r1&view=timeline&traceQ=abc&traceCat=tools&traceMinMs=30'))
      .toBe('project=p&run=r1&view=timeline')
    expect(stripTraceParams('run=r2&view=detail')).toBe('run=r2&view=detail')
    expect(stripTraceParams('traceQ=x')).toBe('')
  })
})
