import { describe, expect, it } from 'vitest'
import type { ObservationEvent } from './observationApi'
import { foldTrace, groupSummary, hintAggregate, matchesChip, turnAlignedWindow } from './traceModel'
import { timelineRow } from './eventPresentation'
import { ru } from './locales/ru'
import { en } from './locales/en'

/** The test harness t(): resolves real locale messages with {{var}} interpolation. */
const t = (key: string, vars?: Record<string, string>): string => {
  const template = ru[key] ?? en[key] ?? key
  if (!vars) return template
  return template.replace(/\{\{(\w+)\}\}/g, (match, name: string) => (name in vars ? vars[name] : match))
}
const RU = 'ru' as const

let seq = 0
const ev = (type: string, data: Record<string, unknown> = {}, second = 0, run = 'r1'): ObservationEvent => {
  seq += 1
  return {
    schema: 'temporality/observation/event/v1',
    event_id: `e${seq}`,
    occurred_at: `2026-10-22T10:00:${String(second).padStart(2, '0')}Z`,
    received_at: `2026-10-22T10:00:${String(second).padStart(2, '0')}Z`,
    source: { id: 'kernel' },
    context: { run, project: 'p' },
    type,
    data,
  } as ObservationEvent
}

describe('foldTrace (§5)', () => {
  it('folds turn.started/completed into the «Ход N» group, not per-event rows (§5.1)', () => {
    const items = foldTrace([
      ev('run.started', { model: 'gpt' }),
      ev('turn.started', { turn: 1 }),
      ev('turn.completed', { turn: 1 }),
      ev('turn.started', { turn: 2 }),
      ev('turn.completed', { turn: 2 }),
    ], RU, t)
    const turns = items.filter((item) => item.kind === 'group' && item.group === 'turns')
    expect(turns).toHaveLength(1)
    expect((turns[0] as Extract<typeof turns[0], { kind: 'group' }>).events).toHaveLength(4)
    // run.started stays a standalone row before the group.
    expect(items[0].kind).toBe('event')
  })

  it('aggregates hints into «N подсказок, M использовано» (§5.6)', () => {
    const items = foldTrace([
      ev('hint.offered', { hint_id: 'h1', proposition: 'a' }),
      ev('hint.offered', { hint_id: 'h2', proposition: 'b' }),
      ev('hint.offered', { hint_id: 'h3', proposition: 'c' }),
      ev('hint.used', { hint_id: 'h1', proposition: 'a' }),
      ev('hint.used', { hint_id: 'h2', proposition: 'b' }),
      ev('hint.outcome', { hint_id: 'h1', outcome: 'helpful' }),
    ], RU, t)
    const group = items.find((item) => item.kind === 'group' && item.group === 'knowledge')
    expect(group).toBeTruthy()
    expect((group as { events: ObservationEvent[] }).events).toHaveLength(6)
    const summary = groupSummary(group as Parameters<typeof groupSummary>[0], t)
    expect(summary.detail).toContain('подсказок 6 (использовано 2)')
    const agg = hintAggregate((group as { events: ObservationEvent[] }).events)
    expect(agg).toEqual({ total: 6, used: 2, offered: 3 })
  })

  it('folds minor plan transitions into «изменения плана», keeps rejected visible (§5.4)', () => {
    const items = foldTrace([
      ev('plan.task.skipped', { task_id: 'a' }),
      ev('plan.task.invalidated', { task_id: 'b' }),
      ev('plan.task.reopened', { task_id: 'c' }),
      ev('plan.task.incomplete', { task_id: 'd' }),
      ev('plan.task.rejected', { task_id: 'e', feedback: 'fix' }),
    ], RU, t)
    const minor = items.find((item) => item.kind === 'group' && item.group === 'planMinor')
    expect((minor as { events: ObservationEvent[] }).events).toHaveLength(4)
    // rejected is a UX-important transition — stays a standalone row (§5.4).
    const rejected = items.find((item) => item.kind === 'event')
    expect(rejected && 'event' in rejected ? rejected.event.type : '').toBe('plan.task.rejected')
  })

  it('collapses an approval pair into one row with the outcome status (§5.5)', () => {
    const items = foldTrace([
      ev('approval.requested', { operation_id: 'op-1', action: 'run_command' }, 1),
      ev('tool.completed', { tool: 'run_command', exit_code: 0 }, 2),
      ev('approval.granted', { operation_id: 'op-1', approver: 'ruslan' }, 3),
    ], RU, t)
    const pairs = items.filter((item) => item.kind === 'approvalPair')
    expect(pairs).toHaveLength(1)
    const pair = pairs[0] as Extract<(typeof items)[number], { kind: 'approvalPair' }>
    expect(pair.outcome?.type).toBe('approval.granted')
    expect(pair.status).toBe('ok')
    // The outcome event does not render its own row.
    expect(items.filter((item) => item.kind === 'event')).toHaveLength(1)
  })

  it('keeps an unanswered approval visible as awaiting (§5.5)', () => {
    const items = foldTrace([ev('human.requested', { question: 'Как постим?' }, 1)], RU, t)
    const pair = items.find((item) => item.kind === 'approvalPair') as Extract<(typeof items)[number], { kind: 'approvalPair' }>
    expect(pair.outcome).toBeNull()
    expect(pair.status).toBe('running')
  })

  it('does not pair events across runs without an operation id', () => {
    const items = foldTrace([
      ev('approval.requested', {}, 1, 'run-a'),
      ev('approval.granted', {}, 2, 'run-b'),
    ], RU, t)
    // Only the request folds into a pair; the outcome without a matching
    // operation id stays a standalone row.
    expect(items.filter((item) => item.kind === 'approvalPair')).toHaveLength(1)
    expect(items.filter((item) => item.kind === 'event')).toHaveLength(1)
  })

  it('folds ops noise into «Эксплуатация» with an error flag (§5.7)', () => {
    const items = foldTrace([
      ev('storage.snapshot', {}),
      ev('notification.sent', {}),
      ev('memory.read.failed', { error: 'boom' }),
    ], RU, t)
    const noise = items.find((item) => item.kind === 'group' && item.group === 'opsNoise') as Extract<(typeof items)[number], { kind: 'group' }>
    expect(noise.events).toHaveLength(3)
    expect(noise.hasError).toBe(true)
    const summary = groupSummary(noise, t)
    expect(summary.label).toBe('Эксплуатационные события')
    expect(summary.detail).toContain('3')
  })

  it('folds saga.* into one row per saga (§5.7)', () => {
    const items = foldTrace([
      ev('saga.registered', { saga_id: 'saga-1', title: 'Миграция' }, 1),
      ev('saga.attached', { saga_id: 'saga-1', task_id: 't9' }, 2),
      ev('saga.registered', { saga_id: 'saga-2', title: 'Другая' }, 3),
    ], RU, t)
    const sagas = items.filter((item) => item.kind === 'saga')
    expect(sagas).toHaveLength(2)
    const first = sagas[0] as Extract<(typeof items)[number], { kind: 'saga' }>
    expect(first.sagaId).toBe('saga-1')
    expect(first.events).toHaveLength(2)
  })

  it('streaming types never enter the folded timeline (§5.2)', () => {
    const items = foldTrace([
      ev('model.text_delta', { text: 'chunk' }, 1),
      ev('model.reasoning', { text: 'thought' }, 2),
    ], RU, t)
    // The §5.2 rule is enforced upstream of the fold (AgentRuns trace memo);
    // the fold must not resurrect them as rows either.
    expect(items).toHaveLength(0)
  })

  it('preserves the order of first members', () => {
    const items = foldTrace([
      ev('run.started', {}, 1),
      ev('turn.started', {}, 2),
      ev('tool.completed', { tool: 'run_command', exit_code: 0 }, 3),
      ev('run.completed', { turns: 2 }, 4),
    ], RU, t)
    const kinds = items.map((item) => (item.kind === 'group' ? `group:${item.group}` : item.kind))
    expect(kinds).toEqual(['event', 'group:turns', 'event', 'event'])
  })
})

describe('matchesChip (§8 quick chips)', () => {
  const requested = ev('approval.requested', { operation_id: 'op-9', action: 'x' }, 1)
  const failedTool = foldTrace([ev('tool.failed', { tool: 'run_command', error: 'boom' }, 1)], RU, t)[0]
  const okTool = foldTrace([ev('tool.completed', { tool: 'read_file', exit_code: 0 }, 1)], RU, t)[0]
  const humanPair = foldTrace([requested, ev('human.answered', { operation_id: 'op-9' }, 2)], RU, t)[0]
  const noiseGroup = foldTrace([ev('storage.snapshot', {})], RU, t)[0]

  it('errors chip keeps failed tools and drops ok rows and noise', () => {
    expect(matchesChip(failedTool, 'errors')).toBe(true)
    expect(matchesChip(okTool, 'errors')).toBe(false)
    expect(matchesChip(noiseGroup, 'errors')).toBe(false)
  })

  it('tools chip keeps tool/mcp rows', () => {
    expect(matchesChip(failedTool, 'tools')).toBe(true)
    expect(matchesChip(okTool, 'tools')).toBe(true)
  })

  it('human chip keeps approval pairs', () => {
    expect(matchesChip(humanPair, 'human')).toBe(true)
    expect(matchesChip(humanPair, 'tools')).toBe(false)
  })

  it('a rejected approval pair shows on the errors chip', () => {
    const rejected = foldTrace([requested, ev('approval.rejected', { operation_id: 'op-9', reason: 'no' }, 2)], RU, t)[0]
    expect(matchesChip(rejected, 'errors')).toBe(true)
  })
})

describe('timelineRow × foldTrace consistency', () => {
  it('every standalone row keeps its catalog category', () => {
    const event = ev('mcp.call.completed', { tool: 'fetch', result_bytes: 2048 })
    const items = foldTrace([event], RU, t)
    expect(items).toHaveLength(1)
    const row = timelineRow(event, RU, t)
    expect(row.category).toBe('mcp')
    expect(row.status).toBe('ok')
    expect(row.fact).toContain('2.0 KB')
  })
})

describe('turnAlignedWindow — the window never cuts a turn in half', () => {
  const ev = (type: string, turn?: number): ObservationEvent =>
    ({ type, data: turn !== undefined ? { turn } : {}, context: { run: 'r1' } } as unknown as ObservationEvent)

  const events = [
    ev('run.started'),
    ev('turn.started', 1),
    ev('tool.completed', 1),
    ev('turn.completed', 1),
    ev('turn.started', 2),
    ev('tool.completed', 2),
    ev('turn.completed', 2),
  ]

  it('keeps the whole run when the window covers it', () => {
    expect(turnAlignedWindow(events, 10)).toHaveLength(events.length)
  })

  it('extends the cut back to the turn boundary', () => {
    // limit=2 lands inside turn 2 (its tool.completed): the window must pull
    // back to include that turn's turn.started.
    const windowed = turnAlignedWindow(events, 2)
    expect(windowed[0]?.type).toBe('turn.started')
    expect(windowed[0]?.data?.turn).toBe(2)
  })

  it('does not pull in the previous turn', () => {
    // limit=3 from the end = [turn.started(2), tool, turn.completed] — the
    // window stops at turn 2's own boundary, turn 1 stays out.
    const windowed = turnAlignedWindow(events, 3)
    expect(windowed.some((e) => e.data?.turn === 1)).toBe(false)
  })
})
