import type { ObservationEvent } from './observationApi'
import { timelineRow, type TimelineRow, type TranslateFn } from './eventPresentation'
import type { Locale } from './locales'

/**
 * §5 folding of the trace into groups (v2-consolidated.md §5.1, §5.4–§5.7).
 *
 * The timeline renders groups, not every raw event:
 *  - turn.started/turn.completed  → the «Ход N» group (§5.1);
 *  - hint.*                       → one aggregate «3 подсказки, 2 использованы» (§5.6);
 *  - plan.task.{skipped,invalidated,reopened,incomplete} → «изменения плана» (§5.4);
 *  - an approval pair (requested … granted/rejected/timed_out/auto) → one row (§5.5);
 *  - saga.*                       → one row per saga (§5.7);
 *  - ops noise (storage.snapshot, operation.reconciled, notification.sent,
 *    memory.read.failed)          → a folded, filterable «Эксплуатация» group (§5.7).
 *
 * Everything else stays a standalone timeline row. Pure functions over the
 * already-loaded window — no network, no journal scans (NFR-9).
 */

/** Foldable groups (§5). */
export type TraceGroupKey = 'turns' | 'hints' | 'planMinor' | 'opsNoise'

/** One render item of the folded trace. */
export type TraceItem =
  | { kind: 'event'; event: ObservationEvent; row: TimelineRow }
  | { kind: 'approvalPair'; requested: ObservationEvent; row: TimelineRow; outcome: ObservationEvent | null; status: TimelineRow['status'] }
  | { kind: 'saga'; sagaId: string; events: ObservationEvent[]; row: TimelineRow }
  | { kind: 'group'; group: TraceGroupKey; events: ObservationEvent[]; hasError: boolean }

/** §5.7: ops noise — folded/filterable by default, never standalone rows. */
const NOISE_TYPES = new Set(['storage.snapshot', 'operation.reconciled', 'notification.sent', 'memory.read.failed'])

/** §5.4: minor plan-task transitions fold into «изменения плана». */
const MINOR_PLAN_TYPES = new Set(['plan.task.skipped', 'plan.task.invalidated', 'plan.task.reopened', 'plan.task.incomplete'])

/** §5.5: the outcomes that close a request/decision pair. */
const APPROVAL_OUTCOMES = new Set(['approval.granted', 'approval.rejected', 'approval.timed_out', 'approval.auto_granted', 'human.answered', 'human.rejected', 'human.cancelled', 'human.timed_out'])
const APPROVAL_REQUESTS = new Set(['approval.requested', 'human.requested'])

/** §5.7 noise is the exact curated set — an uncatalogued type (e.g.
 *  approval.* without an operation id) stays a standalone row, it never
 *  disappears into «Эксплуатация». */
const isNoise = (type: string): boolean => NOISE_TYPES.has(type)
const isHint = (type: string): boolean => type.startsWith('hint.')

/** The pair key of an approval/question event: the operation id when the
 *  payload carries one, otherwise the event's own id (never pairs across). */
const operationKey = (event: ObservationEvent): string => {
  const id = event.data?.operation_id
  return typeof id === 'string' && id ? id : event.event_id
}

/** Fold the trace window into render items (§5). Order preserved: items come
 *  out in the order of their first member event. */
export function foldTrace(events: ObservationEvent[], locale: Locale, t: TranslateFn): TraceItem[] {
  const items: TraceItem[] = []
  const groups = new Map<TraceGroupKey, Extract<TraceItem, { kind: 'group' }>>()
  const sagas = new Map<string, Extract<TraceItem, { kind: 'saga' }>>()
  // requested operation_id → the pair item awaiting its outcome.
  const openPairs = new Map<string, Extract<TraceItem, { kind: 'approvalPair' }>>()
  // event_id of an outcome already folded into a pair (its row disappears).
  const paired = new Set<string>()

  const groupOf = (key: TraceGroupKey): Extract<TraceItem, { kind: 'group' }> => {
    let group = groups.get(key)
    if (!group) {
      group = { kind: 'group', group: key, events: [], hasError: false }
      groups.set(key, group)
      items.push(group)
    }
    return group
  }

  for (const event of events) {
    if (paired.has(event.event_id)) continue
    const row = timelineRow(event, locale, t)
    const type = event.type
    // §5.2: streaming types never become timeline items — they feed the live
    // model row and the parent call's details (AgentRuns trace memo).
    if (row.kind === 'stream') continue

    // §5.5: the decision closes its request pair.
    if (APPROVAL_OUTCOMES.has(type)) {
      const pair = openPairs.get(operationKey(event))
      if (pair) {
        pair.outcome = event
        pair.status = row.status
        openPairs.delete(operationKey(event))
        continue
      }
    }

    if (APPROVAL_REQUESTS.has(type)) {
      const pair: Extract<TraceItem, { kind: 'approvalPair' }> = { kind: 'approvalPair', requested: event, row, outcome: null, status: 'running' }
      items.push(pair)
      openPairs.set(operationKey(event), pair)
      continue
    }

    // §5.7: one row per saga — every saga.* lands on its saga's row.
    if (type.startsWith('saga.')) {
      const sagaId = typeof event.data?.saga_id === 'string' && event.data.saga_id ? event.data.saga_id : 'saga'
      let saga = sagas.get(sagaId)
      if (!saga) {
        saga = { kind: 'saga', sagaId, events: [event], row }
        sagas.set(sagaId, saga)
        items.push(saga)
      } else {
        saga.events.push(event)
        if (row.status === 'error') saga.row = { ...saga.row, status: 'error' }
      }
      continue
    }

    if (isNoise(type)) {
      const group = groupOf('opsNoise')
      group.events.push(event)
      group.hasError = group.hasError || row.status === 'error'
      continue
    }
    if (type === 'turn.started' || type === 'turn.completed') {
      const group = groupOf('turns')
      group.events.push(event)
      group.hasError = group.hasError || row.status === 'error'
      continue
    }
    if (MINOR_PLAN_TYPES.has(type)) {
      const group = groupOf('planMinor')
      group.events.push(event)
      group.hasError = group.hasError || row.status === 'error'
      continue
    }
    if (isHint(type)) {
      const group = groupOf('hints')
      group.events.push(event)
      group.hasError = group.hasError || row.status === 'error'
      continue
    }

    items.push({ kind: 'event', event, row })
  }
  return items
}

/** Human summary of a folded group (§5.1 «Ход N», §5.6 «3 подсказки, 2
 *  использованы», §5.4 «изменения плана», §5.7 «Эксплуатация»). */
export function groupSummary(group: Extract<TraceItem, { kind: 'group' }>, t: TranslateFn): { label: string; detail: string } {
  switch (group.group) {
    case 'turns':
      return { label: t('runs.layers.group.turns'), detail: t('runs.layers.group.turns_hint') }
    case 'planMinor':
      return { label: t('runs.layers.group.plan_minor'), detail: t('runs.layers.group.plan_minor_hint', { count: String(group.events.length) }) }
    case 'opsNoise':
      return { label: t('runs.layers.group.ops_noise'), detail: t('runs.layers.group.ops_noise_hint', { count: String(group.events.length) }) }
    case 'hints': {
      const offered = group.events.filter((e) => e.type === 'hint.offered').length
      const used = group.events.filter((e) => e.type === 'hint.used').length
      return offered === 0
        ? { label: t('runs.layers.group.hints'), detail: t('runs.layers.group.hints_zero') }
        : { label: t('runs.layers.group.hints'), detail: t('runs.layers.group.hints_aggregate', { total: String(group.events.length), used: String(used), offered: String(offered) }) }
    }
  }
}

/** Hint-aggregate counter «N подсказок, M использовано» (§5.6). */
export function hintAggregate(events: ObservationEvent[]): { total: number; used: number; offered: number } {
  return {
    total: events.filter((e) => isHint(e.type)).length,
    used: events.filter((e) => e.type === 'hint.used').length,
    offered: events.filter((e) => e.type === 'hint.offered').length,
  }
}

/** Quick chips over the folded items (§8): «только ошибки», «только
 *  инструменты», «только решения человека», «только мой ввод» — pure, works
 *  on the loaded window, no network (NFR-9). */
export type TraceChip = 'all' | 'errors' | 'tools' | 'human' | 'myInput'

/** One turn node of the trace tree: every event between turn.started and
 *  turn.completed lives inside its «Ход N» node. While the turn runs the
 *  node is expanded; once it completes it collapses (unless pinned open by
 *  the reader). */
export interface TurnNode {
  kind: 'turn'
  number: number
  runId: string
  started: ObservationEvent
  completed: ObservationEvent | null
  events: ObservationEvent[]
  hasError: boolean
}

export type TraceTreeItem =
  | { kind: 'turn'; turn: TurnNode }
  | TraceItem

/** Split the window into tree segments: turn nodes (one per
 *  turn.started…turn.completed span) and runs of between-turn events. Pure
 *  over the loaded window (NFR-9). Turn interiors stay raw here; the caller
 *  folds them with foldTrace at render time (the same folding rules, one
 *  level deeper). */
export function foldTraceTree(events: ObservationEvent[]): Array<{ turn: TurnNode | null; events: ObservationEvent[] }> {
  const segments: Array<{ turn: TurnNode | null; events: ObservationEvent[] }> = []
  let current: TurnNode | null = null
  for (const event of events) {
    if (event.type === 'turn.started') {
      const number = Number(event.data?.turn ?? 0)
      current = { kind: 'turn', number, runId: event.context?.run ?? '', started: event, completed: null, events: [], hasError: false }
      segments.push({ turn: current, events: [] })
      continue
    }
    if (event.type === 'turn.completed') {
      const last = segments[segments.length - 1]
      if (current && last?.turn === current) {
        current.completed = event
        current = null
      }
      // A completed without a started span: dropped — the tree spine is the
      // turn node, an orphan marker has nothing to attach to.
      continue
    }
    if (current) {
      current.events.push(event)
      continue
    }
    const last = segments[segments.length - 1]
    if (last && last.turn === null) last.events.push(event)
    else segments.push({ turn: null, events: [event] })
  }
  return segments
}

/** §8 «только мой ввод»: the event types carrying the user's own words. */
function isMyInputType(type: string): boolean {
  return type === 'run.user_note' || type === 'hitl.verdict.recorded'
    || type === 'human.answered' || type === 'human.rejected'
}

export function matchesChip(item: TraceItem, chip: TraceChip): boolean {
  if (chip === 'all') return true
  switch (item.kind) {
    case 'group':
      if (chip === 'errors') return item.hasError
      if (chip === 'tools') return item.events.some((e) => e.type.startsWith('tool.') || e.type.startsWith('mcp.'))
      if (chip === 'myInput') return item.events.some((e) => isMyInputType(e.type))
      return false
    case 'approvalPair':
      // §8 «только мой ввод»: an unanswered question the human must answer
      // is their pending input; a decided pair is their decision.
      return chip === 'human' || chip === 'myInput'
        || (chip === 'errors' && item.status === 'error')
    case 'saga':
      return chip === 'errors' && item.row.status === 'error'
    case 'event': {
      const { row, event } = item
      if (chip === 'errors') return row.status === 'error'
      if (chip === 'tools') return row.category === 'tools' || row.category === 'mcp' || event.type.startsWith('tool.') || event.type.startsWith('mcp.')
      if (chip === 'myInput') return isMyInputType(event.type)
      return row.category === 'approvals' || event.type.startsWith('human.')
    }
  }
}
