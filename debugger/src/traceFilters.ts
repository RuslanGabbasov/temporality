import type { ObservationEvent } from './observationApi'
import { eventDetails, timelineRow, type EventCategory, type TimelineRow, type TranslateFn } from './eventPresentation'
import { EVENT_GROUPS, eventInfo } from './eventCatalog'
import { matchesChip, type TraceChip, type TraceItem } from './traceModel'
import type { Locale } from './locales'

/**
 * Timeline filters, quick chips and text search for the selected run's trace
 * (v2-consolidated.md §8, DoD-4). Everything here is pure and client-side:
 * §8 filters and search work over the ALREADY-LOADED window — zero network
 * requests per keystroke (NFR-9, pinned by traceFilters.perf.test.ts).
 *
 * The module owns three composable §8 dimensions (they AND together):
 *  - the structured filter: category (catalog group), status
 *    (ok/error/cancelled), participant (model/tool/human/system), minMs;
 *  - the quick chips («только ошибки», «только инструменты», «только
 *    решения человека», «только мой ввод») — semantic presets over T2's
 *    matchesChip, combinable as a set;
 *  - the text query over titles, human-form details, facts and the loaded
 *    console buffers.
 *
 * Command-output search covers only the console buffers the client actually
 * holds (the 2000-line/256-KiB tail, §11.2): the kernel does not persist
 * output in this iteration (§11.1), so a server-side search over it is
 * impossible — the UI states this limit (runs.filters.search_hint).
 *
 * The filter/search state round-trips through URL query params (§8) with a
 * `trace*` prefix disjoint from the runs-list filter and the existing
 * run/view params.
 */

/** §8 structured dimensions. Every field optional — unset = no constraint. */
export interface TraceFilter {
  /** Catalog group of the row («Инструменты», «Модель» …). */
  category: EventCategory | ''
  /** Exact event type(s), comma-separated when the checkbox dropdown
   *  selected several (`run_command,tool.started`). A row matches when any
   *  listed type is among its events. */
  type: string
  /** Row status: success, failure, cancelled. */
  status: 'ok' | 'error' | 'cancelled' | ''
  /** §8 participant: who produced the event. */
  participant: TraceParticipant | ''
  /** Minimum duration in ms — rows without a duration drop out. */
  minMs: number
}

export type TraceParticipant = 'model' | 'tool' | 'human' | 'system'

export const EMPTY_FILTER: TraceFilter = { category: '', type: '', status: '', participant: '', minMs: 0 }

export function isFilterEmpty(filter: TraceFilter): boolean {
  return !filter.category && !filter.type && !filter.status && !filter.participant && filter.minMs === 0
}

/** §8 participant of an event: WHO acts, not what it means. The model
 *  thinks, tools (and MCP servers) execute, humans decide and answer,
 *  everything else is system plumbing. */
export function participantOf(event: ObservationEvent): TraceParticipant {
  const type = event.type
  if (type.startsWith('model.')) return 'model'
  if (type.startsWith('tool.') || type.startsWith('mcp.')) return 'tool'
  if (type.startsWith('human.') || type === 'run.user_note' || type === 'hitl.verdict.recorded'
    || type.startsWith('approval.') || type.startsWith('hint.')) return 'human'
  return 'system'
}

type RowStatus = TimelineRow['status']

/** §8: folded groups have no status of their own — they surface through
 *  while any member matches, their own row is never status-filtered away. */
function statusOfItem(item: TraceItem): RowStatus {
  if (item.kind === 'approvalPair') return item.status
  // Groups never reach status filtering (matchesFilter passes them through
  // while a member matches); the guard only narrows the type.
  return item.kind === 'group' ? 'none' : item.row.status
}

function categoryOfItem(item: TraceItem): EventCategory | '' {
  if (item.kind === 'group') return ''
  return item.row.category
}

function participantOfItem(item: TraceItem): TraceParticipant | '' {
  if (item.kind === 'group') return ''
  if (item.kind === 'approvalPair') return 'human'
  if (item.kind === 'saga') return 'system'
  return participantOf(item.event)
}

/** Every event an item renders (the pair counts both its halves). */
function itemEventsOf(item: TraceItem): ObservationEvent[] {
  if (item.kind === 'group') return item.events
  if (item.kind === 'saga') return item.events
  if (item.kind === 'approvalPair') return item.outcome ? [item.requested, item.outcome] : [item.requested]
  return [item.event]
}

/** Duration of an event in ms from the latency fields the kernel journals
 *  (same keys the timeline duration marker reads). 0 = unknown. */
export function durationMsOf(event: ObservationEvent): number {
  const d = event.data ?? {}
  for (const key of ['latency_ms', 'duration_ms', 'wait_ms']) {
    const ms = Number(d[key])
    if (Number.isFinite(ms) && ms > 0) return ms
  }
  return 0
}

/** The structured §8 filter against one item's aspects. Folded groups pass
 *  (filterTraceItems keeps them while any member matches) — pure. */
export function matchesFilter(filter: TraceFilter, item: TraceItem): boolean {
  if (item.kind === 'group') return true
  if (filter.category && categoryOfItem(item) !== filter.category) return false
  if (filter.type) {
    const wanted = filter.type.split(',').map((value) => value.trim()).filter(Boolean)
    if (!itemEventsOf(item).some((event) => wanted.includes(event.type))) return false
  }
  if (filter.status && statusOfItem(item) !== filter.status) return false
  if (filter.participant && participantOfItem(item) !== filter.participant) return false
  if (filter.minMs > 0) {
    const ms = item.kind === 'event' ? durationMsOf(item.event) : 0
    if (ms < filter.minMs) return false
  }
  return true
}

export type FilterChip = TraceChip

/** True when the chip set has any active (non-«all») chip. */
export function hasActiveChips(chips: ReadonlySet<FilterChip>): boolean {
  for (const chip of chips) if (chip !== 'all') return true
  return false
}

/** The §8 chip set against one item: active chips OR together inside the
 *  set (a row an «errors» OR a «my input» chip matches stays visible) and
 *  AND with the other dimensions. No active chip → everything passes. */
export function matchesChipSet(item: TraceItem, chips: ReadonlySet<FilterChip>): boolean {
  if (!hasActiveChips(chips)) return true
  for (const chip of chips) {
    if (chip !== 'all' && matchesChip(item, chip)) return true
  }
  return false
}

/** A group member as a standalone item, so the per-event dimensions
 *  (status/chips/filter) apply to folded events too. */
function memberItem(group: Extract<TraceItem, { kind: 'group' }>, event: ObservationEvent, locale: Locale, t: TranslateFn): Extract<TraceItem, { kind: 'event' }> {
  return { kind: 'event', event, row: timelineRow(event, locale, t) }
}

/** Apply the structured filter and the chip set to the folded window.
 *  A folded group stays visible while at least one member matches —
 *  a filter must not orphan a group its matching events belong to. Pure. */
export function filterTraceItems(
  items: readonly TraceItem[],
  filter: TraceFilter,
  chips: ReadonlySet<FilterChip>,
  locale: Locale,
  t: TranslateFn,
): TraceItem[] {
  if (isFilterEmpty(filter) && !hasActiveChips(chips)) return [...items]
  return items.filter((item) => {
    if (item.kind === 'group') {
      return item.events.some((event) => {
        const member = memberItem(item, event, locale, t)
        return matchesFilter(filter, member) && matchesChipSet(member, chips)
      })
    }
    return matchesFilter(filter, item) && matchesChipSet(item, chips)
  })
}

/** Events the «только мой ввод» chip covers (§8): the user's own words —
 *  the mid-run chat note and the answers to human questions. */
const PAYLOAD_PICKS: Array<{ types: RegExp; keys: string[] }> = [
  { types: /^(run\.user_note$)/, keys: ['note'] },
  { types: /^(human\.requested$|human\.reasked$)/, keys: ['question'] },
  { types: /^(human\.answered$)/, keys: ['response'] },
  { types: /^(agent\.summary$)/, keys: ['answer'] },
  { types: /^(knowledge\.|hint\.)/, keys: ['proposition'] },
  { types: /^(plan\.task\.)/, keys: ['feedback'] },
]

/** Human-form values beyond the timeline preview, in full (§8: «значения
 *  human-форм»): the note text, the question, the answer, the proposition.
 *  P-2: secret.* events never expose payload values — the search haystack
 *  of a secret row is its name/status line only. */
export function payloadTextOf(event: ObservationEvent): string {
  if (event.type.startsWith('secret.')) return ''
  const d = event.data ?? {}
  const parts: string[] = []
  for (const pick of PAYLOAD_PICKS) {
    if (!pick.types.test(event.type)) continue
    for (const key of pick.keys) {
      const value = d[key]
      if (typeof value === 'string' && value.trim()) parts.push(value.trim())
    }
  }
  return parts.join(' · ')
}

/** Everything searchable about one event: the timeline title + key fact +
 *  duration, the human-form details layer and the full human-form payload
 *  values. Both locale titles participate (find a row by either wording). */
export function haystackOf(event: ObservationEvent, row: TimelineRow, locale: Locale, t: TranslateFn): string {
  const head = [row.title, row.fact, row.duration]
  // The alt-locale catalog title (search in either language's wording);
  // memoized — the haystacks rebuild on every committed keystroke.
  const altTitle = altTitleOf(row.type, locale)
  const details = event.type.startsWith('secret.')
    ? []
    : (() => {
      const d = eventDetails(event, t)
      return [
        ...d.argumentLines.map((line) => `${line.key} ${line.value}`),
        d.result,
        d.failureCause,
      ]
    })()
  return [...head, altTitle, payloadTextOf(event), ...details].filter(Boolean).join(' · ')
}

const altTitleMemo = new Map<string, string>()

/** The event's title in the OTHER locale, so «Ответ модели» is findable by
 *  "model response" too. Only the catalog title participates — the human
 *  override wording of the current locale is already in `row.title`. */
function altTitleOf(type: string, current: Locale): string {
  const alt: Locale = current === 'ru' ? 'en' : 'ru'
  const key = `${alt}:${type}`
  let title = altTitleMemo.get(key)
  if (title === undefined) {
    const meta = eventInfo(type)
    title = meta ? meta[alt].title : ''
    altTitleMemo.set(key, title)
  }
  return title
}

/** Snippet around the first match with ~60 chars of context on each side. */
export function snippetAround(haystack: string, needle: string): string {
  const at = haystack.toLowerCase().indexOf(needle.toLowerCase())
  if (at === -1) return ''
  const from = Math.max(0, at - 60)
  const to = Math.min(haystack.length, at + needle.length + 60)
  const lead = from > 0 ? '…' : ''
  const tail = to < haystack.length ? '…' : ''
  return `${lead}${haystack.slice(from, to).trim()}${tail}`
}

/** One search hit: the visible item it belongs to, the event that matched
 *  and a context snippet. */
export interface SearchEntry {
  item: TraceItem
  event: ObservationEvent
  snippet: string
}

/** §8 text search over the (already filtered) folded window: case-
 *  insensitive substring over every row's haystack; folded groups match
 *  through their members (and their label), saga rows through the saga
 *  events. `consoleBuffers` maps event_id → the loaded console text
 *  (stdout+stderr tail, §6) — §8's «в пределах буфера вывода». Pure. */
export function searchItems(
  items: readonly TraceItem[],
  query: string,
  locale: Locale,
  t: TranslateFn,
  consoleBuffers?: ReadonlyMap<string, string>,
): SearchEntry[] {
  const needle = query.trim().toLowerCase()
  if (!needle) return []
  const entries: SearchEntry[] = []
  const push = (item: TraceItem, event: ObservationEvent, haystack: string) => {
    if (!haystack.toLowerCase().includes(needle)) return
    entries.push({ item, event, snippet: snippetAround(haystack, needle) })
  }
  for (const item of items) {
    if (item.kind === 'event') {
      push(item, item.event, haystackOf(item.event, item.row, locale, t))
    } else if (item.kind === 'approvalPair') {
      const outcome = item.outcome ? haystackOf(item.outcome, timelineRow(item.outcome, locale, t), locale, t) : ''
      push(item, item.requested, `${item.row.title} · ${item.row.fact}${outcome ? ` · ${outcome}` : ''}`)
    } else if (item.kind === 'saga') {
      // A saga item always carries at least its first event (foldTrace);
      // an empty one contributes nothing to search.
      if (item.events.length === 0) continue
      const members = item.events.map((event) => haystackOf(event, timelineRow(event, locale, t), locale, t))
      push(item, item.events[0], `${item.row.title} · ${item.row.fact} · ${members.join(' · ')}`)
    } else {
      const label = t(groupLabelKey(item.group))
      for (const event of item.events) {
        push(item, event, `${label} · ${haystackOf(event, timelineRow(event, locale, t), locale, t)}`)
      }
    }
  }
  if (consoleBuffers) {
    for (const [eventId, buffer] of consoleBuffers) {
      if (!buffer.toLowerCase().includes(needle)) continue
      const item = items.find((candidate) => (candidate.kind === 'event' || candidate.kind === 'approvalPair' || candidate.kind === 'saga')
        && itemAnchorEvent(candidate)?.event_id === eventId)
      const event = items.flatMap((candidate) => itemEventsOf(candidate)).find((candidate) => candidate.event_id === eventId)
      if (item && event) entries.push({ item, event, snippet: snippetAround(buffer, needle) })
    }
  }
  return entries
}

function itemAnchorEvent(item: TraceItem): ObservationEvent | undefined {
  return item.kind === 'event' ? item.event : item.kind === 'approvalPair' ? item.requested : item.kind === 'saga' ? item.events[0] : undefined
}

/** The i18n key of a folded group's label (mirrors traceModel.groupSummary). */
export function groupLabelKey(group: Extract<TraceItem, { kind: 'group' }>['group']): string {
  switch (group) {
    case 'turns': return 'runs.layers.group.turns'
    case 'hints': return 'runs.layers.group.hints'
    case 'planMinor': return 'runs.layers.group.plan_minor'
    case 'opsNoise': return 'runs.layers.group.ops_noise'
  }
}

/** Count of a group's members passing every dimension — the «N/M» marker
 *  on a partially filtered folded group. */
export function countMatchingMembers(
  group: Extract<TraceItem, { kind: 'group' }>,
  filter: TraceFilter,
  chips: ReadonlySet<FilterChip>,
  locale: Locale,
  t: TranslateFn,
): number {
  return group.events.filter((event) => {
    const member = memberItem(group, event, locale, t)
    return matchesFilter(filter, member) && matchesChipSet(member, chips)
  }).length
}

/** Unique event types of the window (the «тип события» dropdown options). */
export function buildFilterIndex(items: readonly TraceItem[]): string[] {
  const types = new Set<string>()
  for (const item of items) for (const event of itemEventsOf(item)) types.add(event.type)
  return [...types].sort()
}

/** Catalog groups in catalog order — the category dropdown's options. */
export function filterCategories(): EventCategory[] {
  return EVENT_GROUPS.map((group) => group.id as EventCategory)
}

// ── URL state (§8: «состояние фильтров/поиска — в URL») ─────────────────────

/** URL parameter names. The `trace*` prefix keeps them disjoint from the
 *  runs-list text filter and the existing run/view params. */
export const TRACE_QUERY_KEYS = {
  category: 'traceCat',
  type: 'traceType',
  status: 'traceStatus',
  participant: 'traceWho',
  minMs: 'traceMinMs',
  query: 'traceQ',
  chips: 'traceChips',
} as const

const TRACE_CATEGORIES: readonly string[] = EVENT_GROUPS.map((group) => group.id)
const TRACE_STATUSES: readonly TraceFilter['status'][] = ['ok', 'error', 'cancelled']
const TRACE_PARTICIPANTS: readonly TraceParticipant[] = ['model', 'tool', 'human', 'system']
const TRACE_CHIP_VALUES: readonly FilterChip[] = ['all', 'errors', 'tools', 'human', 'myInput']

/** The parsed URL state of the filter panel. */
export interface TraceUrlState {
  filter: TraceFilter
  query: string
  chips: Set<FilterChip>
}

/** Parse the trace state from URL params. Unknown values reset their
 *  dimension — a stale link degrades to «no filter», never to an empty
 *  view. Absent params yield the all-clear default. Pure. */
export function parseTraceState(params: URLSearchParams): TraceUrlState {
  const category = params.get(TRACE_QUERY_KEYS.category) ?? ''
  const status = params.get(TRACE_QUERY_KEYS.status) ?? ''
  const participant = params.get(TRACE_QUERY_KEYS.participant) ?? ''
  const minMsRaw = params.get(TRACE_QUERY_KEYS.minMs) ?? ''
  const chipsRaw = params.get(TRACE_QUERY_KEYS.chips) ?? ''
  const filter: TraceFilter = {
    category: TRACE_CATEGORIES.includes(category) ? (category as EventCategory) : '',
    type: params.get(TRACE_QUERY_KEYS.type) ?? '',
    status: TRACE_STATUSES.includes(status as TraceFilter['status']) ? (status as TraceFilter['status']) : '',
    participant: TRACE_PARTICIPANTS.includes(participant as TraceParticipant) ? (participant as TraceParticipant) : '',
    minMs: /^\d+$/.test(minMsRaw) ? Math.max(0, Number(minMsRaw)) : 0,
  }
  const chips = new Set<FilterChip>()
  for (const token of chipsRaw.split(',')) {
    const chip = token.trim()
    if (TRACE_CHIP_VALUES.includes(chip as FilterChip) && chip !== 'all') chips.add(chip as FilterChip)
  }
  return { filter, query: params.get(TRACE_QUERY_KEYS.query) ?? '', chips }
}

/** Serialize the state to a query string ('' when nothing is set — the URL
 *  stays clean). Pure. */
export function traceStateQuery(filter: TraceFilter, query: string, chips: ReadonlySet<FilterChip>): string {
  const params = new URLSearchParams()
  if (filter.category) params.set(TRACE_QUERY_KEYS.category, filter.category)
  if (filter.type) params.set(TRACE_QUERY_KEYS.type, filter.type)
  if (filter.status) params.set(TRACE_QUERY_KEYS.status, filter.status)
  if (filter.participant) params.set(TRACE_QUERY_KEYS.participant, filter.participant)
  if (filter.minMs > 0) params.set(TRACE_QUERY_KEYS.minMs, String(filter.minMs))
  if (query.trim()) params.set(TRACE_QUERY_KEYS.query, query.trim())
  const active = [...chips].filter((chip) => chip !== 'all')
  if (active.length > 0) params.set(TRACE_QUERY_KEYS.chips, active.sort().join(','))
  return params.toString()
}

/** True when nothing constrains the view — the panel then writes nothing
 *  into the URL. */
export function isTraceStateEmpty(state: TraceUrlState): boolean {
  return isFilterEmpty(state.filter) && !state.query.trim() && !hasActiveChips(state.chips)
}

/** Drop every `trace*` param from a query string, keeping run/view/etc. */
export function stripTraceParams(search: string): string {
  const params = new URLSearchParams(search)
  for (const key of [...params.keys()]) {
    if (key.startsWith('trace')) params.delete(key)
  }
  return params.toString()
}
