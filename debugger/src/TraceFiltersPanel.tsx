import { useMemo } from 'react'
import { Search } from '@carbon/react'
import type { EventCategory, TranslateFn } from './eventPresentation'
import { EVENT_GROUPS } from './eventCatalog'
import type { ObservationEvent } from './observationApi'
import type { Locale } from './locales'
import {
  searchItems,
  type FilterChip,
  type TraceFilter,
  type TraceParticipant,
  type TraceUrlState,
} from './traceFilters'
import type { TraceItem } from './traceModel'

type TFn = TranslateFn

/**
 * The §8 filter panel above the folded timeline (DoD-4): category, event
 * type, status, participant, duration threshold, the quick chips and the
 * text search. Fully controlled — the state lives in AgentRuns (URL-backed
 * through `trace*` params), so a link restores the exact view. The panel
 * itself performs zero fetches (NFR-9): filtering and search run over the
 * already-loaded window; the console buffers it searches are the ones the
 * client actually holds (§6/§11.2 tail) and the hint under the search box
 * states that limit.
 */

/** The minimal console shape the search index reads (ConsoleOutput state). */
export interface ConsoleStateLike {
  stdout?: { text: string } | null
  stderr?: { text: string } | null
}

function itemEventsOf(item: TraceItem): ObservationEvent[] {
  if (item.kind === 'group') return item.events
  if (item.kind === 'saga') return item.events
  if (item.kind === 'approvalPair') return item.outcome ? [item.requested, item.outcome] : [item.requested]
  return [item.event]
}

function operationIdOf(event: ObservationEvent): string {
  const id = event.data?.operation_id
  return typeof id === 'string' ? id : ''
}

/** Build the console search index: event_id → the client-held stdout/stderr
 *  tail. Only events of the window with a console state participate —
 *  the search never fetches (§8, NFR-9). */
export function consoleSearchIndex(
  states: ReadonlyMap<string, ConsoleStateLike> | undefined,
  items: readonly TraceItem[],
): Map<string, string> {
  const index = new Map<string, string>()
  if (!states || states.size === 0) return index
  const eventByOperation = new Map<string, ObservationEvent>()
  for (const item of items) {
    for (const event of itemEventsOf(item)) {
      const operationId = operationIdOf(event)
      if (operationId && !eventByOperation.has(operationId)) eventByOperation.set(operationId, event)
    }
  }
  for (const [operationId, state] of states) {
    const event = eventByOperation.get(operationId)
    if (!event) continue
    const text = [state.stdout?.text ?? '', state.stderr?.text ?? ''].filter(Boolean).join('\n')
    if (text.trim()) index.set(event.event_id, text)
  }
  return index
}

const STATUS_OPTIONS = [
  { value: 'ok', key: 'runs.filters.status.ok' },
  { value: 'error', key: 'runs.filters.status.error' },
  { value: 'cancelled', key: 'runs.filters.status.cancelled' },
]

const PARTICIPANT_OPTIONS: Array<{ value: TraceParticipant; key: string }> = [
  { value: 'model', key: 'runs.filters.who.model' },
  { value: 'tool', key: 'runs.filters.who.tool' },
  { value: 'human', key: 'runs.filters.who.human' },
  { value: 'system', key: 'runs.filters.who.system' },
]

const CHIP_OPTIONS: Array<{ value: Exclude<FilterChip, 'all'>; key: string; icon: string }> = [
  { value: 'errors', key: 'runs.chip.errors', icon: '⚠' },
  { value: 'tools', key: 'runs.chip.tools', icon: '🔧' },
  { value: 'human', key: 'runs.chip.human', icon: '✋' },
  { value: 'myInput', key: 'runs.chip.my_input', icon: '💬' },
]

const DURATION_OPTIONS: Array<{ value: number; key: string }> = [
  { value: 0, key: 'runs.filters.duration.any' },
  { value: 1000, key: 'runs.filters.duration.1s' },
  { value: 5000, key: 'runs.filters.duration.5s' },
  { value: 30000, key: 'runs.filters.duration.30s' },
]

const SELECT_STYLE: React.CSSProperties = {
  background: 'var(--tm-raised)',
  color: 'var(--tm-text)',
  border: '1px solid var(--tm-border)',
  borderRadius: '4px',
  fontSize: '0.72rem',
  padding: '0.15rem 0.25rem',
  minWidth: 0,
}

export default function TraceFiltersPanel({ state, onState, items, consoleStates, locale, t }: {
  state: TraceUrlState
  onState: (state: TraceUrlState) => void
  /** The folded window BEFORE filtering — the type dropdown options come
   *  from the full window, not from the filtered one. */
  items: TraceItem[]
  /** Loaded console buffers by operation id (T4 ConsoleOutput state). */
  consoleStates?: ReadonlyMap<string, ConsoleStateLike>
  locale: Locale
  t: TFn
}) {
  const { filter, query, chips } = state
  // Category labels come from the catalog's own ru/en pair (symmetric by
  // construction, no extra i18n keys).
  const categoryOptions = EVENT_GROUPS.map((group) => ({ value: group.id, label: locale === 'ru' ? group.ru : group.en }))
  const consoleIndex = useMemo(
    () => consoleSearchIndex(consoleStates, items),
    [consoleStates, items],
  )
  const typeOptions = useMemo(() => {
    const types = new Set<string>()
    for (const item of items) {
      for (const event of itemEventsOf(item)) types.add(event.type)
    }
    return [...types].sort()
  }, [items])
  const setFilter = (patch: Partial<TraceFilter>) => onState({ ...state, filter: { ...filter, ...patch } })
  const toggleChip = (chip: Exclude<FilterChip, 'all'>) => {
    const next = new Set(chips)
    if (next.has(chip)) next.delete(chip)
    else next.add(chip)
    onState({ ...state, chips: next })
  }
  const anyActive = Boolean(
    filter.category || filter.type || filter.status || filter.participant
    || filter.minMs > 0 || query.trim() || chips.size > 0,
  )
  const reset = () => onState({ filter: { category: '', type: '', status: '', participant: '', minMs: 0 }, query: '', chips: new Set() })

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: '0.3rem', marginBottom: '0.5rem' }}>
      <div style={{ display: 'flex', gap: '0.3rem', flexWrap: 'wrap', alignItems: 'center' }} role="toolbar" aria-label={t('runs.layers.chips')}>
        {CHIP_OPTIONS.map((chip) => (
          <button
            key={chip.value}
            type="button"
            title={t(chip.key)}
            aria-pressed={chips.has(chip.value)}
            onClick={() => toggleChip(chip.value)}
            style={{ background: chips.has(chip.value) ? 'rgba(122,162,247,0.15)' : 'none', border: `1px solid ${chips.has(chip.value) ? 'var(--tm-teal)' : 'var(--tm-border)'}`, color: 'var(--tm-text-2)', cursor: 'pointer', fontSize: '0.72rem', padding: '0.1rem 0.45rem', borderRadius: '4px', lineHeight: 1.4 }}
          >
            {chip.icon}
          </button>
        ))}
        {anyActive && (
          <button type="button" onClick={reset} title={t('runs.filters.reset')} aria-label={t('runs.filters.reset')} style={{ background: 'none', border: 'none', color: 'var(--tm-text-3)', cursor: 'pointer', fontSize: '0.78rem', padding: '0.1rem 0.35rem' }}>✕</button>
        )}
      </div>
      <div style={{ display: 'flex', gap: '0.3rem', flexWrap: 'wrap', alignItems: 'center' }}>
        <select
          aria-label={t('runs.filters.category')}
          title={t('runs.filters.category')}
          value={filter.category}
          onChange={(e) => setFilter({ category: e.target.value as EventCategory | '' })}
          style={SELECT_STYLE}
        >
          <option value="">{t('runs.filters.category')}</option>
          {categoryOptions.map((entry) => (
            <option key={entry.value} value={entry.value}>{entry.label}</option>
          ))}
        </select>
        <select
          aria-label={t('runs.filters.type')}
          title={t('runs.filters.type')}
          value={filter.type}
          onChange={(e) => setFilter({ type: e.target.value })}
          style={SELECT_STYLE}
        >
          <option value="">{t('runs.filters.type')}</option>
          {typeOptions.map((type) => (
            <option key={type} value={type}>{type}</option>
          ))}
        </select>
        <select
          aria-label={t('runs.filters.status')}
          title={t('runs.filters.status')}
          value={filter.status}
          onChange={(e) => setFilter({ status: e.target.value as TraceFilter['status'] })}
          style={SELECT_STYLE}
        >
          <option value="">{t('runs.filters.status')}</option>
          {STATUS_OPTIONS.map((entry) => (
            <option key={entry.value} value={entry.value}>{t(entry.key)}</option>
          ))}
        </select>
        <select
          aria-label={t('runs.filters.participant')}
          title={t('runs.filters.participant')}
          value={filter.participant}
          onChange={(e) => setFilter({ participant: e.target.value as TraceParticipant | '' })}
          style={SELECT_STYLE}
        >
          <option value="">{t('runs.filters.participant')}</option>
          {PARTICIPANT_OPTIONS.map((entry) => (
            <option key={entry.value} value={entry.value}>{t(entry.key)}</option>
          ))}
        </select>
        <select
          aria-label={t('runs.filters.duration')}
          title={t('runs.filters.duration')}
          value={String(filter.minMs)}
          onChange={(e) => setFilter({ minMs: Number(e.target.value) || 0 })}
          style={SELECT_STYLE}
        >
          {DURATION_OPTIONS.map((entry) => (
            <option key={String(entry.value)} value={String(entry.value)}>{t(entry.key)}</option>
          ))}
        </select>
      </div>
      <div style={{ maxWidth: '26rem' }}>
        <Search
          size="sm"
          id="trace-search"
          labelText=""
          placeholder={t('runs.filters.search_placeholder')}
          value={query}
          onChange={(e: React.ChangeEvent<HTMLInputElement>) => onState({ ...state, query: e.target.value })}
        />
        <div style={{ fontSize: '0.68rem', color: 'var(--tm-text-3)', marginTop: '0.1rem' }}>{t('runs.filters.search_hint')}</div>
      </div>
    </div>
  )
}

/** Search hits of the current query over the given (pre-filter) window —
 *  shared by the panel's owner to render the result list. Exported so the
 *  wiring stays in one place. */
export function traceSearchEntries(
  items: readonly TraceItem[],
  query: string,
  locale: Locale,
  t: TFn,
  consoleStates?: ReadonlyMap<string, ConsoleStateLike>,
) {
  return searchItems(items, query, locale, t, consoleSearchIndex(consoleStates, items))
}
