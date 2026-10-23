import { useState } from 'react'
import type { ObservationEvent } from './observationApi'
import { eventDetails, timelineRow, rawEnvelope } from './eventPresentation'
import { foldTrace, groupSummary, matchesChip, type TraceChip, type TraceItem } from './traceModel'
import { filterTraceItems, searchItems, type TraceUrlState } from './traceFilters'
import TraceFiltersPanel, { consoleSearchIndex, type ConsoleStateLike } from './TraceFiltersPanel'
import type { ConsoleState } from './consoleStream'
import type { Locale } from './locales'
import Markdown from './Markdown'
import ConsoleOutput from './ConsoleOutput'
import { ChevronDown, ChevronRight } from '@carbon/icons-react'

/**
 * The three-layer trace renderer (T2, v2-consolidated.md §4–§5, DoD-1/2/3/9).
 *
 * Layer 1 — the timeline row: semantic category icon/color, a human title
 * (the event type is never shown), one key fact, duration, status.
 * Layer 2 — inline details: human-form arguments, result, the failure cause,
 * links, the run_command console slot (console://, T4's ConsoleOutput).
 * Layer 3 — the collapsed raw envelope «Сырое событие (JSON)», the only
 * place technical payloads render (K-1); secret events expose the name only
 * (P-2).
 *
 * The old «Детализация» button is gone (DoD-3): disclosure is inline and
 * keyboard-operable with aria-expanded/aria-label (DoD-9).
 */

type TFn = (key: string, vars?: Record<string, string>) => string

export interface EventRowVisualProps {
  event: ObservationEvent
  locale: Locale
  t: TFn
  project: string
  response?: string
  reasoning?: string
  knowledgeDoubt?: React.ReactNode
  live?: boolean
  /** §8/NFR-9 (T4): receive each loaded console buffer of this row so the
   *  owner can index it for the text search (no fetches in the search). */
  onConsoleState?: (state: ConsoleState) => void
}

/** Discloser shared by the details toggle and the raw envelope. Keyboard
 *  disclosure is native to <button> (Enter/Space); aria-expanded carries the
 *  state to assistive tech (DoD-9). */
function Discloser({ open, onToggle, label, children }: { open: boolean; onToggle: () => void; label: string; children: React.ReactNode }) {
  return (
    <>
      <button
        type="button"
        onClick={onToggle}
        aria-expanded={open}
        aria-label={label}
        style={{ background: 'none', border: 'none', color: 'var(--tm-text-3)', cursor: 'pointer', fontSize: '0.7rem', padding: '0.15rem 0', display: 'flex', alignItems: 'center', gap: '0.25rem' }}
      >
        {open ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
        {label}
      </button>
      {open && children}
    </>
  )
}

/** Layers 2 + 3 of one event: inline details and the collapsed raw envelope. */
function EventLayeredDetails({ event, t, project, response, reasoning, knowledgeDoubt, live, onConsoleState }: Omit<EventRowVisualProps, 'locale'>) {
  const [detailsOpen, setDetailsOpen] = useState(false)
  const [rawOpen, setRawOpen] = useState(false)
  const d = event.data ?? {}
  const details = eventDetails(event, t)
  const knowledgeID = typeof d.knowledge_id === 'string' && d.knowledge_id
    && (event.type.startsWith('knowledge.') || event.type.startsWith('hint.')) ? d.knowledge_id : ''
  const hasExplanation = Boolean(details.failureCause)
    || details.argumentLines.length > 0
    || details.links.length > 0
    || Boolean(response)
    || Boolean(reasoning)
    || (Boolean(details.result) && event.type !== 'agent.summary')

  return (
    <div>
      <Discloser
        open={detailsOpen}
        onToggle={() => setDetailsOpen((value) => !value)}
        label={detailsOpen ? t('runs.layers.hide_details') : t('runs.layers.toggle_details')}
      >
        <div style={{ marginTop: '0.15rem', display: 'flex', flexDirection: 'column', gap: '0.35rem' }}>
          {details.failureCause && (
            <div className="run-failure-cause">
              <div className="run-failure-cause-title">{t('errors.cause')}</div>
              <div className="run-failure-cause-text">{details.failureCause}</div>
              {details.timeout && <div className="run-failure-cause-hint">{t('errors.model_timeout_hint')}</div>}
            </div>
          )}
          {details.links.map((link) => (
            <a
              key={`${link.kind}:${link.label}`}
              href={link.url}
              onClick={(e) => {
                e.preventDefault()
                window.history.pushState(null, '', link.url)
                window.dispatchEvent(new PopStateEvent('popstate'))
              }}
              style={{ fontSize: '0.72rem', color: 'var(--tm-teal)', overflowWrap: 'anywhere' }}
            >
              {link.kind === 'child_run' ? `↗ ${t('runs.layers.child.open')}` : link.kind === 'artifact' ? `📄 ${t('runs.layers.artifact.open')}` : `📚 ${t('runs.layers.knowledge.open')}`}
              {' — '}
              {link.label}
            </a>
          ))}
          {/* run_command console slot (§6): lazy — mounts only while the
              details are open, one subscription per open event (NFR-9);
              addresses console://<operation_id>, never artifact://.
              onStateLoaded feeds the §8 search index (loaded buffers only). */}
          {details.consoleOperationId && (
            <ConsoleOutput
              operationId={details.consoleOperationId}
              project={project}
              live={Boolean(live)}
              onStateLoaded={onConsoleState}
            />          )}
          {response && (
            <div className="run-model-response">
              <Markdown content={response} />
            </div>
          )}
          {reasoning && (
            <details className="run-model-reasoning">
              <summary>💭 {t('runs.reasoning')}</summary>
              <div className="run-model-reasoning-content">{reasoning}</div>
            </details>
          )}
          {details.argumentLines.length > 0 && (
            <div style={{ padding: '0.5rem', background: 'var(--tm-raised)', borderRadius: '4px', fontSize: '0.72rem' }}>
              {details.argumentLines.map((line) => (
                <div key={line.key} style={{ marginBottom: '0.2rem', overflowWrap: 'anywhere' }}>
                  <span style={{ color: 'var(--tm-text-3)' }}>{line.key}: </span>
                  <span style={{ color: 'var(--tm-text)' }}>{line.value}</span>
                </div>
              ))}
            </div>
          )}
          {details.result && !details.failureCause && !response && (
            <div style={{ fontSize: '0.72rem', color: 'var(--tm-text-2)', whiteSpace: 'pre-wrap', overflowWrap: 'anywhere', maxHeight: '200px', overflowY: 'auto' }}>{details.result}</div>
          )}
          {knowledgeID && knowledgeDoubt}
        </div>
      </Discloser>
      {/* Layer 3 — the raw envelope, collapsed by default (§4.3, K-1); the
          last block of every expanded row (§4.3 «в конце детализации»). */}
      <Discloser
        open={rawOpen}
        onToggle={() => setRawOpen((value) => !value)}
        label={t('runs.layers.raw_summary')}
      >
        <pre style={{ padding: '0.5rem', background: 'var(--tm-raised)', color: 'var(--tm-text-2)', fontSize: '0.68rem', fontFamily: '"SFMono-Regular", Consolas, monospace', maxHeight: '260px', overflow: 'auto', borderRadius: '4px', whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{rawEnvelope(event)}</pre>
      </Discloser>
    </div>
  )
}

/** One standalone timeline row (layer 1) + its expandable layers. */
export function TraceEventRow({ event, locale, t, project, response, reasoning, knowledgeDoubt, live, onConsoleState }: EventRowVisualProps) {
  const [open, setOpen] = useState(false)
  const row = timelineRow(event, locale, t)
  const statusDot = row.status === 'error' ? '#f7768e'
    : row.status === 'ok' ? '#9ece6a'
      : row.status === 'running' ? 'var(--tm-teal)'
        : row.status === 'cancelled' ? 'var(--tm-text-3)'
          : ''
  return (
    <div style={{ padding: '0.4rem 0.5rem', borderLeft: `3px solid ${row.color}`, marginBottom: '0.15rem', background: 'var(--tm-surface)', borderRadius: '0 4px 4px 0', minWidth: 0, overflow: 'hidden' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', fontSize: '0.8rem' }}>
        <span style={{ color: row.color, width: '1.2rem', textAlign: 'center', flexShrink: 0 }}>{row.icon}</span>
        <strong style={{ color: row.color, minWidth: '100px' }}>{row.title}</strong>
        {row.fact && <span style={{ color: 'var(--tm-text-3)', flex: 1, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{row.fact}</span>}
        {row.duration && <span style={{ color: 'var(--tm-muted)', fontSize: '0.7rem', flexShrink: 0 }}>{row.duration}</span>}
        {statusDot && <span aria-hidden title={row.status} style={{ width: '0.5rem', height: '0.5rem', borderRadius: '50%', background: statusDot, flexShrink: 0 }} />}
        {row.hasDetails && (
          <button
            type="button"
            onClick={() => setOpen((value) => !value)}
            aria-expanded={open}
            aria-label={`${t('runs.layers.toggle_details')}: ${row.title}`}
            style={{ background: 'none', border: 'none', color: 'var(--tm-text-3)', cursor: 'pointer', fontSize: '0.75rem', padding: '0.1rem 0.35rem', lineHeight: 1, borderRadius: '4px', flexShrink: 0 }}
          >
            {open ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
          </button>
        )}
      </div>
      {open && (
        <div style={{ marginLeft: '1.7rem', marginTop: '0.15rem' }}>
          <EventLayeredDetails event={event} t={t} project={project} response={response} reasoning={reasoning} knowledgeDoubt={knowledgeDoubt} live={live} onConsoleState={onConsoleState} />
        </div>
      )}
    </div>
  )
}

/** §8 quick chips over the folded items. */
function TraceChips({ chip, onChip, items, t }: { chip: TraceChip; onChip: (chip: TraceChip) => void; items: TraceItem[]; t: TFn }) {
  const chips: Array<{ key: TraceChip; label: string; title: string }> = [
    { key: 'all', label: t('runs.layers.timeline.length', { count: String(items.length) }), title: t('runs.layers.timeline.length', { count: String(items.length) }) },
    { key: 'errors', label: '⚠', title: t('runs.chip.errors') },
    { key: 'tools', label: '🔧', title: t('runs.chip.tools') },
    { key: 'human', label: '✋', title: t('runs.chip.human') },
  ]
  return (
    <div style={{ display: 'flex', gap: '0.3rem', flexWrap: 'wrap', marginBottom: '0.4rem' }} role="toolbar" aria-label={t('runs.layers.chips')}>
      {chips.map((entry) => (
        <button
          key={entry.key}
          type="button"
          title={entry.title}
          aria-pressed={chip === entry.key}
          onClick={() => onChip(entry.key)}
          style={{ background: chip === entry.key ? 'rgba(122,162,247,0.15)' : 'none', border: `1px solid ${chip === entry.key ? 'var(--tm-teal)' : 'var(--tm-border)'}`, color: 'var(--tm-text-2)', cursor: 'pointer', fontSize: '0.72rem', padding: '0.1rem 0.45rem', borderRadius: '4px', lineHeight: 1.4 }}
        >
          {entry.label}
        </button>
      ))}
    </div>
  )
}

/** Grouped trace: the §5 folded timeline with the three-layer rows, the §8
 *  filter panel (category/type/status/participant/duration, quick chips,
 *  text search — all URL-backed) above it. */
export default function TraceLayers({ events, locale, t, project, responsesByTurn, reasoningsByTurn, knowledgeDoubtOf, live, emptyHint, filterState, onFilterState, consoleStates, onConsoleState, liveModelParts }: {
  events: ObservationEvent[]
  locale: Locale
  t: TFn
  project: string
  /** model.text_delta/model.reasoning joined by `${runId}:${turn}` — the
   *  streaming types never render as rows (§5.2). */
  responsesByTurn?: Map<string, string>
  reasoningsByTurn?: Map<string, string>
  knowledgeDoubtOf?: (event: ObservationEvent) => React.ReactNode
  live?: boolean
  emptyHint?: string
  /** §8 controlled filter/search state (URL-backed by the owner). When
   *  absent the panel renders nothing and the trace shows unfiltered. */
  filterState?: TraceUrlState
  onFilterState?: (state: TraceUrlState) => void
  /** Loaded console buffers by operation id (T4 ConsoleOutput) — the text
   *  search covers exactly these, never the network (§8, NFR-9). */
  consoleStates?: ReadonlyMap<string, ConsoleStateLike>
  /** §8/NFR-9 (T4): receive each console buffer loaded by an expanded row
   *  so the owner can index it for the text search (by operation id). */
  onConsoleState?: (state: ConsoleState) => void
  /** Parts of the in-flight model-call row (elapsed · tokens · rate ·
   *  channel state), assembled by the owner from the ephemeral token stream.
   *  Non-empty renders as the first timeline row — the live call is the most
   *  recent thing in the trace. */
  liveModelParts?: string[]
}) {
  const [chip, setChip] = useState<TraceChip>('all')
  const [openGroups, setOpenGroups] = useState<Set<string>>(new Set())
  const allItems = foldTrace(events, locale, t)
  // Alias for the legacy chips row and the render loop below.
  const items = allItems
  // §8: with no filter state the trace renders unfiltered (back-compat and
  // the other views); with one, the three dimensions AND together and the
  // chips OR inside their set. Search highlights hits on top.
  const activeFilter = Boolean(filterState && onFilterState)
  const visibleItems = activeFilter
    ? filterTraceItems(allItems, filterState!.filter, filterState!.chips, locale, t)
    : items.filter((item) => matchesChip(item, chip))
  const searchEntries = activeFilter && filterState!.query.trim()
    ? searchItems(visibleItems, filterState!.query, locale, t, consoleSearchIndex(consoleStates, allItems))
    : []
  const keyOf = (item: TraceItem): string => {
    switch (item.kind) {
      case 'event': return `${item.event.source.id}:${item.event.event_id}`
      case 'approvalPair': return `pair:${item.requested.event_id}`
      case 'saga': return `saga:${item.sagaId}`
      case 'group': return `group:${item.group}`
    }
  }
  const turnProps = (event: ObservationEvent) => {
    const turn = event.data?.turn
    const turnKey = event.type === 'model.completed' ? `${event.context?.run ?? ''}:${turn}` : undefined
    return {
      response: turnKey ? responsesByTurn?.get(turnKey) : undefined,
      reasoning: turnKey ? reasoningsByTurn?.get(turnKey) : undefined,
    }
  }
  return (
    <div>
      {activeFilter && filterState && onFilterState ? (
        <TraceFiltersPanel
          state={filterState}
          onState={onFilterState}
          items={allItems}
          consoleStates={consoleStates}
          locale={locale}
          t={t}
        />
      ) : (
        <TraceChips chip={chip} onChip={setChip} items={items} t={t} />
      )}
      {searchEntries.length > 0 && (
        <div style={{ marginBottom: '0.4rem', border: '1px solid var(--tm-border)', borderRadius: '4px', padding: '0.35rem 0.5rem' }}>
          <div style={{ fontSize: '0.72rem', color: 'var(--tm-text-3)', marginBottom: '0.2rem' }}>
            {t('runs.filters.results', { count: String(searchEntries.length) })}
          </div>
          {searchEntries.slice(0, 20).map((entry, index) => (
            <div key={`${entry.event.event_id}:${index}`} style={{ fontSize: '0.72rem', color: 'var(--tm-text-2)', overflowWrap: 'anywhere' }}>
              <span aria-hidden>🔍 </span>{entry.snippet}
            </div>
          ))}
          {searchEntries.length > 20 && (
            <div style={{ fontSize: '0.68rem', color: 'var(--tm-text-3)' }}>… +{searchEntries.length - 20}</div>
          )}
        </div>
      )}
      {liveModelParts && liveModelParts.length > 0 && (
        <div style={{ padding: '0.4rem 0.5rem', marginBottom: '0.15rem', background: 'var(--tm-surface)', borderRadius: '0 4px 4px 0', borderLeft: '3px solid var(--tm-teal)', minWidth: 0, display: 'flex', alignItems: 'center', gap: '0.5rem', fontSize: '0.8rem' }}>
          <span style={{ color: 'var(--tm-teal)', width: '1.2rem', textAlign: 'center', flexShrink: 0 }}>⏳</span>
          <strong style={{ minWidth: '100px' }}>{t('runs.event.model_call')}</strong>
          <span style={{ color: 'var(--tm-text-3)', flex: 1, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{liveModelParts.join(' · ')}</span>
        </div>
      )}
      {visibleItems.length === 0 && <div style={{ color: 'var(--tm-text-3)', fontSize: '0.8rem', padding: '0.5rem 0' }}>{emptyHint ?? t('runs.layers.timeline.empty')}</div>}
      {visibleItems.map((item) => {
        if (item.kind === 'group') {
          const open = openGroups.has(item.group)
          const summary = groupSummary(item, t)
          return (
            <div key={keyOf(item)} style={{ padding: '0.35rem 0.5rem', marginBottom: '0.15rem', background: 'var(--tm-raised)', borderRadius: '4px', borderLeft: `3px solid ${item.hasError ? '#f7768e' : 'var(--tm-border)'}` }}>
              <button
                type="button"
                onClick={() => setOpenGroups((prev) => {
                  const next = new Set(prev)
                  if (next.has(item.group)) next.delete(item.group)
                  else next.add(item.group)
                  return next
                })}
                aria-expanded={open}
                aria-label={summary.label}
                style={{ background: 'none', border: 'none', color: 'var(--tm-text-2)', cursor: 'pointer', fontSize: '0.78rem', padding: 0, display: 'flex', alignItems: 'center', gap: '0.35rem', width: '100%', textAlign: 'left' }}
              >
                {open ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
                <strong>{summary.label}</strong>
                <span style={{ color: 'var(--tm-text-3)', fontWeight: 400 }}>{summary.detail}</span>
              </button>
              {open && (
                <div style={{ marginTop: '0.35rem', display: 'flex', flexDirection: 'column', gap: '0.15rem' }}>
                  {item.events.map((event) => (
                    <TraceEventRow key={`${event.source.id}:${event.event_id}`} event={event} locale={locale} t={t} project={project} knowledgeDoubt={knowledgeDoubtOf?.(event)} live={live} onConsoleState={onConsoleState} />
                  ))}
                </div>
              )}
            </div>
          )
        }
        if (item.kind === 'approvalPair') {
          const row = item.row
          const outcomeRow = item.outcome ? timelineRow(item.outcome, locale, t) : null
          const title = t('runs.layers.approval_pair', { action: `${row.title} — ${outcomeRow ? outcomeRow.title : t('runs.layers.approval_requested')}` }).slice(0, 120)
          return (
            <div key={keyOf(item)} style={{ padding: '0.4rem 0.5rem', borderLeft: `3px solid ${outcomeRow?.color ?? row.color}`, marginBottom: '0.15rem', background: 'var(--tm-surface)', borderRadius: '0 4px 4px 0', minWidth: 0 }}>
              <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', fontSize: '0.8rem' }}>
                <span style={{ color: row.color, width: '1.2rem', textAlign: 'center', flexShrink: 0 }}>{row.icon}</span>
                <strong style={{ color: row.color, minWidth: '100px' }}>{title}</strong>
                {outcomeRow?.fact && <span style={{ color: 'var(--tm-text-3)', flex: 1, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{outcomeRow.fact}</span>}
              </div>
            </div>
          )
        }
        if (item.kind === 'saga') {
          const row = item.row
          return (
            <div key={keyOf(item)} style={{ padding: '0.4rem 0.5rem', borderLeft: `3px solid ${row.color}`, marginBottom: '0.15rem', background: 'var(--tm-surface)', borderRadius: '0 4px 4px 0', minWidth: 0 }}>
              <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', fontSize: '0.8rem' }}>
                <span style={{ color: row.color, width: '1.2rem', textAlign: 'center', flexShrink: 0 }}>{row.icon}</span>
                <strong style={{ color: row.color, minWidth: '100px' }}>{row.title}</strong>
                {row.fact && <span style={{ color: 'var(--tm-text-3)', flex: 1, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{row.fact}</span>}
                <span style={{ color: 'var(--tm-muted)', fontSize: '0.7rem', flexShrink: 0 }}>{item.events.length}</span>
              </div>
            </div>
          )
        }
        return (
          <TraceEventRow
            key={keyOf(item)}
            event={item.event}
            locale={locale}
            t={t}
            project={project}
            response={turnProps(item.event).response}
            reasoning={turnProps(item.event).reasoning}
            knowledgeDoubt={knowledgeDoubtOf?.(item.event)}
            live={live}
            onConsoleState={onConsoleState}
          />
        )
      })}
    </div>
  )
}
