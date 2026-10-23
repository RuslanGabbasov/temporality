import { Fragment, useMemo, useState } from 'react'
import type { ObservationEvent } from './observationApi'
import { eventDetails, timelineRow, rawEnvelope } from './eventPresentation'
import { shortTime } from './eventSummary'
import { foldTrace, foldTraceTree, groupSummary, matchesChip, type TraceChip, type TraceItem, type TurnNode } from './traceModel'
import { filterTraceItems, isTraceStateEmpty, searchItems, type TraceUrlState } from './traceFilters'
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

/** Turn node duration: completed.occurred_at − started.occurred_at, human
 *  short form (the same style the rows use). */
function formatTurnDuration(startedAt: string, completedAt: string): string {
  const ms = Date.parse(completedAt) - Date.parse(startedAt)
  if (!Number.isFinite(ms) || ms < 0) return ''
  const seconds = ms / 1000
  if (seconds < 60) return `${seconds.toFixed(seconds < 10 ? 1 : 0)}s`
  const minutes = Math.floor(seconds / 60)
  const rest = Math.round(seconds % 60)
  return `${minutes}m ${rest}s`
}

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

/** Layers 2 + 3 of one event, rendered inline in the expanded row: the human
 *  details, the run_command console as a small always-visible block, and —
 *  toggled by the {} icon in the row header — the raw envelope. */
function EventLayeredDetails({ event, t, project, response, reasoning, knowledgeDoubt, live, onConsoleState, rawOpen, onRawToggle }: Omit<EventRowVisualProps, 'locale'> & { rawOpen: boolean; onRawToggle: () => void }) {
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
      {/* run_command console slot (§6) as a small always-visible block: the
          row expansion is the reader's intent signal, so the console mounts
          right here (one subscription per expanded event, NFR-9) instead of
          hiding behind a second discloser. */}
      {details.consoleOperationId && (
        <ConsoleOutput
          operationId={details.consoleOperationId}
          project={project}
          runId={event.context?.run}
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
      {rawOpen && (
        <pre style={{ padding: '0.5rem', background: 'var(--tm-raised)', color: 'var(--tm-text-2)', fontSize: '0.68rem', fontFamily: '"SFMono-Regular", Consolas, monospace', maxHeight: '260px', overflow: 'auto', borderRadius: '4px', whiteSpace: 'pre-wrap', overflowWrap: 'anywhere', marginTop: '0.35rem' }}>{rawEnvelope(event)}</pre>
      )}
    </div>
  )
}

/** One standalone timeline row (layer 1) + its expandable layers. The raw
 *  JSON envelope is the {} icon in the row's right corner — one click, not a
 *  full-width discloser button. */
export function TraceEventRow({ event, locale, t, project, response, reasoning, knowledgeDoubt, live, onConsoleState }: EventRowVisualProps) {
  const [open, setOpen] = useState(false)
  const [rawOpen, setRawOpen] = useState(false)
  const row = timelineRow(event, locale, t)
  const statusDot = row.status === 'error' ? '#f7768e'
    : row.status === 'ok' ? '#9ece6a'
      : row.status === 'running' ? 'var(--tm-teal)'
        : row.status === 'cancelled' ? 'var(--tm-text-3)'
          : ''
  return (
    <div style={{ padding: '0.4rem 0.5rem', borderLeft: `3px solid ${row.color}`, marginBottom: '0.15rem', background: 'var(--tm-surface)', borderRadius: '0 4px 4px 0', minWidth: 0, overflow: 'hidden' }}>
      {/* The whole header line toggles the details — the reader clicks the
          row, not a 12px chevron. The {} and chevron buttons stop the
          propagation and keep their own a11y contracts. */}
      <div
        onClick={row.hasDetails ? () => setOpen((value) => !value) : undefined}
        style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', fontSize: '0.8rem', cursor: row.hasDetails ? 'pointer' : 'default', minWidth: 0 }}
      >
        <span style={{ color: row.color, width: '1.2rem', textAlign: 'center', flexShrink: 0 }}>{row.icon}</span>
        <strong style={{ color: row.color, flexShrink: 0 }}>{row.title}</strong>
        {row.fact && <span style={{ color: 'var(--tm-text-3)', flex: 1, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', minWidth: 0 }}>{row.fact}</span>}
        {row.duration && <span style={{ color: 'var(--tm-muted)', fontSize: '0.7rem', flexShrink: 0 }}>{row.duration}</span>}
        {statusDot && <span aria-hidden title={row.status} style={{ width: '0.5rem', height: '0.5rem', borderRadius: '50%', background: statusDot, flexShrink: 0 }} />}
        {(
          <span style={{ color: 'var(--tm-muted)', fontSize: '0.65rem', flexShrink: 0, marginLeft: 'auto' }}>{shortTime(event.occurred_at)}</span>
        )}
        {(
          <button
            type="button"
            onClick={(e) => { e.stopPropagation(); setRawOpen((value) => !value) }}
            aria-expanded={rawOpen}
            aria-label={`${t('runs.layers.raw_summary')}: ${row.title}`}
            title={t('runs.layers.raw_summary')}
            style={{ background: rawOpen ? 'var(--tm-raised)' : 'none', border: '1px solid var(--tm-border)', color: 'var(--tm-text-3)', cursor: 'pointer', fontSize: '0.62rem', fontFamily: 'monospace', padding: '0.05rem 0.28rem', lineHeight: 1.4, borderRadius: '4px', flexShrink: 0, marginLeft: 'auto' }}
          >
            {'{}'}
          </button>
        )}
        {row.hasDetails && (
          <button
            type="button"
            onClick={(e) => { e.stopPropagation(); setOpen((value) => !value) }}
            aria-expanded={open}
            aria-label={`${t('runs.layers.toggle_details')}: ${row.title}`}
            style={{ background: 'none', border: 'none', color: 'var(--tm-text-3)', cursor: 'pointer', fontSize: '0.75rem', padding: '0.1rem 0.35rem', lineHeight: 1, borderRadius: '4px', flexShrink: 0 }}
          >
            {open ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
          </button>
        )}
      </div>
      {(open || rawOpen) && (
        <div style={{ marginLeft: '1.7rem', marginTop: '0.15rem' }}>
          <EventLayeredDetails event={event} t={t} project={project} response={response} reasoning={reasoning} knowledgeDoubt={knowledgeDoubt} live={live} onConsoleState={onConsoleState} rawOpen={rawOpen} onRawToggle={() => setRawOpen((value) => !value)} />
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
export default function TraceLayers({ events, locale, t, project, responsesByTurn, reasoningsByTurn, knowledgeDoubtOf, live, emptyHint, filterState, onFilterState, consoleStates, onConsoleState, liveModelParts, newestFirst }: {
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
  /** Display order of the turn tree: newest segment first (the running turn
   *  opens the view). The segment EVENTS stay chronological — a turn reads
   *  top-down like a log regardless of the spine's direction. */
  newestFirst?: boolean
}) {
  // Turn tree expansion (§5.1 reworked): a turn node is expanded while it is
  // the running one and collapses when it completes; the reader's manual
  // toggle pins a turn open/closed until they toggle it back.
  const [chip, setChip] = useState<TraceChip>('all')
  const [openGroups, setOpenGroups] = useState<Set<string>>(new Set())
  const [pinnedTurns, setPinnedTurns] = useState<Map<number, boolean>>(new Map())
  const allItems = foldTrace(events, locale, t)
  const tree = useMemo(() => foldTraceTree(events), [events])
  // Alias for the legacy chips row and the render loop below.
  const items = allItems
  // §8: with no filter state the trace renders as the turn tree (the spine of
  // the run); with one, it renders flat — filtering works over the folded
  // items with turn interiors flattened into them.
  // A filter/search state object is ALWAYS passed by the owner (URL-backed),
  // so "activeFilter" means the state actually constrains something — an
  // empty state renders the turn tree, not the flat folded list.
  const activeFilter = Boolean(filterState && onFilterState) && filterState ? !isTraceStateEmpty(filterState) : false
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
      // Groups are keyed by their first member: the same group kind can
      // appear twice (hints before the turns, extraction after) and each
      // instance must toggle independently.
      case 'group': return `group:${item.group}:${item.events[0]?.event_id ?? ''}`
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
  const turnNodes = tree.filter((segment) => segment.turn !== null).map((segment) => segment.turn!)
  // The running turn is the highest number, wherever it sits in the display
  // order (the tree is newest-first).
  const lastTurnNumber = turnNodes.length > 0 ? Math.max(...turnNodes.map((turn) => turn.number)) : -1
  const turnExpanded = (turn: TurnNode): boolean => {
    const pin = pinnedTurns.get(turn.number)
    if (pin !== undefined) return pin
    return Boolean(live) && !turn.completed && turn.number === lastTurnNumber
  }
  const pinTurn = (number: number, open: boolean) => {
    setPinnedTurns((prev) => {
      const next = new Map(prev)
      next.set(number, open)
      return next
    })
  }
  const renderSegment = (segment: { turn: TurnNode | null; events: ObservationEvent[] }, index: number): React.ReactNode => {
    if (segment.turn) {
      const turn = segment.turn
      const innerItems = foldTrace(turn.events, locale, t)
      const expanded = turnExpanded(turn)
      const duration = turn.completed
        ? formatTurnDuration(turn.started.occurred_at, turn.completed.occurred_at)
        : ''
      const tools = turn.events.filter((e) => e.type === 'tool.completed').length
      const tokens = turn.events
        .filter((e) => e.type === 'model.completed')
        .reduce((sum, e) => sum + (Number(e.data?.total_tokens) || 0), 0)
      const response = responsesByTurn?.get(`${turn.runId}:${turn.number}`) ?? ''
      // The collapsed header carries the turn's own summary: tool count,
      // token spend and up to two lines of the model's intermediate answer —
      // the reader skims turns by these without expanding anything.
      const preview = response.replace(/\s+/g, ' ').trim().slice(0, 320)
      const running = Boolean(live) && !turn.completed
      const statusDot = turn.hasError ? '#f7768e' : running ? 'var(--tm-teal)' : turn.completed ? '#9ece6a' : ''
      return (
        <div key={`turn:${turn.runId}:${turn.number}`} style={{ margin: '0.2rem 0', border: '1px solid var(--tm-border)', borderRadius: '6px', minWidth: 0, overflow: 'hidden' }}>
          <button
            type="button"
            aria-expanded={expanded}
            aria-label={t('runs.layers.group.turns') + ' ' + turn.number}
            onClick={() => pinTurn(turn.number, !expanded)}
            style={{ background: 'var(--tm-raised)', border: 'none', cursor: 'pointer', width: '100%', textAlign: 'left', padding: '0.35rem 0.5rem', display: 'flex', alignItems: 'flex-start', gap: '0.5rem', fontSize: '0.78rem', minWidth: 0 }}
          >
            {expanded ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
            <strong style={{ flexShrink: 0 }}>{t('runs.layers.turn_n', { n: String(turn.number) })}</strong>
            {tools > 0 && <span style={{ color: 'var(--tm-text-3)', fontSize: '0.7rem', flexShrink: 0 }}>⚙ {tools}</span>}
            {tokens > 0 && <span style={{ color: 'var(--tm-text-3)', fontSize: '0.7rem', flexShrink: 0 }}>{tokens.toLocaleString()} tok</span>}
            {preview
              ? <span style={{ color: 'var(--tm-text-3)', flex: 1, minWidth: 0, fontSize: '0.72rem', lineHeight: 1.35, display: '-webkit-box', WebkitLineClamp: 2, WebkitBoxOrient: 'vertical', overflow: 'hidden' }}>{preview}</span>
              : <span style={{ flex: 1 }} />}
            {duration && <span style={{ color: 'var(--tm-muted)', fontSize: '0.7rem', flexShrink: 0 }}>{duration}</span>}
            <span style={{ color: 'var(--tm-muted)', fontSize: '0.65rem', flexShrink: 0 }}>{shortTime(turn.started.occurred_at)}</span>
            {statusDot && <span aria-hidden title={running ? 'running' : turn.hasError ? 'error' : 'completed'} style={{ width: '0.5rem', height: '0.5rem', borderRadius: '50%', background: statusDot, flexShrink: 0 }} />}
          </button>
          {expanded && (
            <div style={{ padding: '0.25rem 0.4rem 0.35rem 0.6rem' }}>
              {innerItems.length === 0 && <div style={{ color: 'var(--tm-text-3)', fontSize: '0.72rem', padding: '0.15rem 0' }}>{t('runs.layers.turn_empty')}</div>}
              {innerItems.map((item) => renderFolded(item, `turn${turn.number}:`))}
              {response && (
                <div style={{ marginTop: '0.25rem', padding: '0.35rem 0.5rem', background: 'var(--tm-raised)', borderRadius: '4px', fontSize: '0.78rem', overflowWrap: 'anywhere' }}>
                  <Markdown content={response} />
                </div>
              )}
            </div>
          )}
        </div>
      )
    }
    return <Fragment key={`plain:${index}`}>{foldTrace(segment.events, locale, t).map((item, itemIndex) => renderFolded(item, `plain${index}:${itemIndex}:`))}</Fragment>
  }
  const renderFolded = (item: TraceItem, keyPrefix: string): React.ReactNode => {
    const key = keyPrefix + keyOf(item)
    if (item.kind === 'group') {
      const open = openGroups.has(key)
      const summary = groupSummary(item, t)
      // The whole header block toggles: the button keeps the keyboard/a11y
      // contract, the container onClick makes every pixel of the header —
      // padding included — clickable the way a reader expects.
      const toggle = () => setOpenGroups((prev) => {
        const next = new Set(prev)
        if (next.has(key)) next.delete(key)
        else next.add(key)
        return next
      })
      const lastSeen = item.events.length > 0 ? shortTime(item.events[item.events.length - 1].occurred_at) : ''
      return (
        <div
          key={key}
          onClick={toggle}
          style={{ padding: '0.35rem 0.5rem', marginBottom: '0.15rem', background: 'var(--tm-raised)', borderRadius: '4px', borderLeft: `3px solid ${item.hasError ? '#f7768e' : 'var(--tm-border)'}`, cursor: 'pointer' }}
        >
          <button
            type="button"
            onClick={(e) => { e.stopPropagation(); toggle() }}
            aria-expanded={open}
            aria-label={summary.label}
            style={{ background: 'none', border: 'none', color: 'var(--tm-text-2)', cursor: 'pointer', fontSize: '0.78rem', padding: 0, display: 'flex', alignItems: 'center', gap: '0.35rem', width: '100%', textAlign: 'left' }}
          >
            {open ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
            <strong>{summary.label}</strong>
            <span style={{ color: 'var(--tm-text-3)', fontWeight: 400 }}>{summary.detail}</span>
            {lastSeen && <span style={{ color: 'var(--tm-muted)', fontSize: '0.65rem', marginLeft: 'auto', flexShrink: 0 }}>{lastSeen}</span>}
          </button>
          {open && (
            <div style={{ marginTop: '0.35rem', display: 'flex', flexDirection: 'column', gap: '0.15rem' }} onClick={(e) => e.stopPropagation()}>
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
        <div key={key} style={{ padding: '0.4rem 0.5rem', borderLeft: `3px solid ${outcomeRow?.color ?? row.color}`, marginBottom: '0.15rem', background: 'var(--tm-surface)', borderRadius: '0 4px 4px 0', minWidth: 0 }}>
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
        <div key={key} style={{ padding: '0.4rem 0.5rem', borderLeft: `3px solid ${row.color}`, marginBottom: '0.15rem', background: 'var(--tm-surface)', borderRadius: '0 4px 4px 0', minWidth: 0 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', fontSize: '0.8rem' }}>
            <span style={{ color: row.color, width: '1.2rem', textAlign: 'center', flexShrink: 0 }}>{row.icon}</span>
            <strong style={{ color: row.color, minWidth: '100px' }}>{row.title}</strong>
            {row.fact && <span style={{ color: 'var(--tm-text-3)', flex: 1, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{row.fact}</span>}
            <span style={{ color: 'var(--tm-muted)', fontSize: '0.7rem', flexShrink: 0 }}>{item.events.length}</span>
          </div>
        </div>
      )
    }
    const { response, reasoning } = turnProps(item.event)
    return (
      <TraceEventRow
        key={key}
        event={item.event}
        locale={locale}
        t={t}
        project={project}
        response={response}
        reasoning={reasoning}
        knowledgeDoubt={knowledgeDoubtOf?.(item.event)}
        live={live}
        onConsoleState={onConsoleState}
      />
    )
  }
  return (
    <div>
      {/* The compact control bar renders always; without a URL-backed state
          (the only test/path that renders TraceLayers bare) the quick chips
          fall back to the local chip state, everything else hides. */}
      {filterState && onFilterState ? (
        <TraceFiltersPanel
          state={filterState}
          onState={onFilterState}
          items={allItems}
          consoleStates={consoleStates}
          locale={locale}
          t={t}
        />
      ) : (
        <div style={{ display: 'flex', gap: '0.3rem', marginBottom: '0.5rem' }} role="toolbar" aria-label={t('runs.layers.chips')}>
          {[
            { key: 'errors' as TraceChip, label: '⚠', title: t('runs.chip.errors') },
            { key: 'tools' as TraceChip, label: '🔧', title: t('runs.chip.tools') },
            { key: 'human' as TraceChip, label: '✋', title: t('runs.chip.human') },
          ].map((entry) => (
            <button
              key={entry.key}
              type="button"
              title={entry.title}
              aria-pressed={chip === entry.key}
              onClick={() => setChip(chip === entry.key ? 'all' : entry.key)}
              style={{ background: chip === entry.key ? 'rgba(122,162,247,0.15)' : 'none', border: `1px solid ${chip === entry.key ? 'var(--tm-teal)' : 'var(--tm-border)'}`, color: 'var(--tm-text-2)', cursor: 'pointer', fontSize: '0.72rem', padding: '0.1rem 0.45rem', borderRadius: '4px', lineHeight: 1.4 }}
            >
              {entry.label}
            </button>
          ))}
        </div>
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
      {/* Newest-first: the in-flight call is the newest event, so its live
          row opens the timeline. */}
      {liveModelParts && liveModelParts.length > 0 && (
        <div style={{ padding: '0.4rem 0.5rem', marginBottom: '0.15rem', background: 'var(--tm-surface)', borderRadius: '0 4px 4px 0', borderLeft: '3px solid var(--tm-teal)', minWidth: 0, display: 'flex', alignItems: 'center', gap: '0.5rem', fontSize: '0.8rem' }}>
          <span style={{ color: 'var(--tm-teal)', width: '1.2rem', textAlign: 'center', flexShrink: 0 }}>⏳</span>
          <strong style={{ minWidth: '100px' }}>{t('runs.event.model_call')}</strong>
          <span style={{ color: 'var(--tm-text-3)', flex: 1, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{liveModelParts.join(' · ')}</span>
        </div>
      )}
      {visibleItems.length === 0 && !activeFilter && <div style={{ color: 'var(--tm-text-3)', fontSize: '0.8rem', padding: '0.5rem 0' }}>{emptyHint ?? t('runs.layers.timeline.empty')}</div>}
      {activeFilter ? (
        visibleItems.map((item) => renderFolded(item, 'flat:'))
      ) : (
        (newestFirst ? [...tree].reverse() : tree).map((segment, index) => renderSegment(segment, index))
      )}
    </div>
  )
}
