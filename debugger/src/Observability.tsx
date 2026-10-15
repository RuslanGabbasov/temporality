import { useEffect, useMemo, useRef, useState } from 'react'
import { authHeaders } from './api'
import { observationApi, type KnowledgeFacets, type KnowledgeItem, type ObservationEvent, type ObservationHint } from './observationApi'
import {
  Button,
  TextInput,
  TextArea,
  InlineNotification,
  Tag,
  Tile,
  Stack,
  Heading,
} from '@carbon/react'
import PageLoading from './PageLoading'
import ProjectRequired from './ProjectRequired'
import { useT } from './i18n'
import { useOrgUnits, OrgUnitSelect, OrgBadge } from './orgUnits'

function errorMessage(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }
/** Partial datetime-local values must not become Invalid Date .toISOString()
 *  crashes — an unparseable value simply means "filter not applied yet". */
function utcValue(local: string): string | undefined {
  if (!local) return undefined
  const date = new Date(local)
  return Number.isNaN(date.getTime()) ? undefined : date.toISOString()
}

/** datetime-local inputs take "YYYY-MM-DDTHH:mm" in LOCAL time — a plain
 *  toISOString() slice would shift the preset by the timezone offset. */
function localDateTimeValue(date: Date): string {
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`
}

const TIME_PRESETS: { key: string; shiftMs: () => number }[] = [
  { key: 'knowledge.preset_hour', shiftMs: () => 3600e3 },
  { key: 'knowledge.preset_day', shiftMs: () => 24 * 3600e3 },
  { key: 'knowledge.preset_week', shiftMs: () => 7 * 24 * 3600e3 },
]

/** Filters auto-apply, so inputs (search, datetime pickers) need a debounce
 *  to avoid firing a request per keystroke. */
function useDebounced<T>(value: T, delayMs = 350): T {
  const [debounced, setDebounced] = useState(value)
  useEffect(() => {
    const timer = setTimeout(() => setDebounced(value), delayMs)
    return () => clearTimeout(timer)
  }, [value, delayMs])
  return debounced
}

const KNOWLEDGE_PAGE_SIZE = 50

const STATE_FILTERS = ['all', 'alive', 'at_risk', 'proposed', 'confirmed', 'challenged', 'corrected', 'superseded', 'invalidated'] as const
const SCOPE_FILTERS = ['all', 'project', 'org_unit', 'organization'] as const

const STATE_COLORS: Record<string, 'blue' | 'green' | 'warm-gray' | 'gray' | 'red'> = {
  proposed: 'blue', confirmed: 'green', challenged: 'warm-gray',
  corrected: 'warm-gray', superseded: 'gray', invalidated: 'red',
}

/** Localized knowledge-state label; t() returns the key itself when a
 * translation is missing — treat that as "no translation" and keep the raw
 * state id so a new state never renders as its own key name. */
function stateLabel(state: string, t: (key: string, vars?: Record<string, string>) => string): string {
  const key = `knowledge.state_${state}`
  const text = t(key)
  return text !== key ? text : state
}

type Tab = 'overview' | 'knowledge' | 'patterns'

export default function Observability({ project }: { project: string }) {
  const t = useT()
  const org = useOrgUnits()
  const initParams = new URLSearchParams(window.location.search)
  const [tab, setTab] = useState<Tab>((['overview', 'knowledge', 'patterns'].includes(initParams.get('tab') as Tab) ? initParams.get('tab') : 'overview') as Tab)
  const [asOf, setAsOf] = useState(initParams.get('as_of') ?? '')
  const [compareAsOf, setCompareAsOf] = useState(initParams.get('compare') ?? '')
  const [knownAt, setKnownAt] = useState(initParams.get('known_at') ?? '')
  const [knowledge, setKnowledge] = useState<KnowledgeItem[]>([])
  const [comparisonKnowledge, setComparisonKnowledge] = useState<KnowledgeItem[] | null>(null)
  const [selectedID, setSelectedID] = useState(initParams.get('selected') ?? '')
  const [stateFilter, setStateFilter] = useState<string>(initParams.get('state') ?? 'all')
  const [scopeFilter, setScopeFilter] = useState<string>(initParams.get('scope') ?? 'all')
  const [searchInput, setSearchInput] = useState(initParams.get('q') ?? '')
  const [listKnowledge, setListKnowledge] = useState<KnowledgeItem[]>([])
  const [listTotal, setListTotal] = useState(0)
  const [listCursor, setListCursor] = useState('')
  const [listLoading, setListLoading] = useState(false)
  const [listError, setListError] = useState('')
  const [facets, setFacets] = useState<KnowledgeFacets | null>(null)
  const [revision, setRevision] = useState(0)
  const listSeq = useRef(0)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [chain, setChain] = useState<any>(null)
  const [actor, setActor] = useState('human')
  const [reason, setReason] = useState('')
  const [promoteScope, setPromoteScope] = useState('')
  const [promoteReason, setPromoteReason] = useState('')
  const [promoteBusy, setPromoteBusy] = useState(false)
  const [promoteError, setPromoteError] = useState('')
  const [query, setQuery] = useState('')
  const [tool, setTool] = useState('')
  const [toolResult, setToolResult] = useState('')
  const [hints, setHints] = useState<ObservationHint[]>([])
  const [hintBusy, setHintBusy] = useState(false)
  const [hintError, setHintError] = useState('')
  const [projection, setProjection] = useState<any>(null)
  const [projectionLoading, setProjectionLoading] = useState(false)
  const [timeOpen, setTimeOpen] = useState(false)

  const debouncedSearch = useDebounced(searchInput)
  const asOfUTC = utcValue(useDebounced(asOf, 500))
  const knownAtUTC = utcValue(useDebounced(knownAt, 500))
  const compareUTC = utcValue(useDebounced(compareAsOf, 500))

  const selected = useMemo(() => listKnowledge.find((item) => item.id === selectedID), [listKnowledge, selectedID])
  const clusters = useMemo(() => {
    const groups = new Map<string, KnowledgeItem[]>()
    for (const item of knowledge) {
      const name = item.topics?.[0] || item.entities?.[0] || (t('knowledge.no_topic') ?? 'No topic')
      groups.set(name, [...(groups.get(name) ?? []), item])
    }
    return [...groups.entries()].sort((a, b) => b[1].length - a[1].length)
  }, [knowledge, t])

  // Health summary
  const health = useMemo(() => {
    const counts = { alive: 0, stale: 0, invalidated: 0, total: knowledge.length }
    for (const k of knowledge) {
      if (k.state === 'invalidated' || k.state === 'superseded' || k.state === 'corrected') counts.invalidated++
      else if (k.at_risk) counts.stale++
      else counts.alive++
    }
    return counts
  }, [knowledge])

  // Knowledge changes vs comparison
  const knowledgeChanges = useMemo(() => {
    if (!comparisonKnowledge) return null
    const oldByID = new Map(comparisonKnowledge.map((k) => [k.id, k]))
    const added = knowledge.filter((k) => !oldByID.has(k.id))
    const changed = knowledge.filter((k) => {
      const old = oldByID.get(k.id)
      return old && old.state !== k.state
    })
    return { added, changed, previousCount: comparisonKnowledge.length }
  }, [knowledge, comparisonKnowledge])

  // URL sync
  useEffect(() => {
    const urlP = new URLSearchParams(window.location.search)
    urlP.set('project', project)
    urlP.set('tab', tab)
    if (asOf) urlP.set('as_of', asOf); else urlP.delete('as_of')
    if (compareAsOf) urlP.set('compare', compareAsOf); else urlP.delete('compare')
    if (knownAt) urlP.set('known_at', knownAt); else urlP.delete('known_at')
    if (selectedID) urlP.set('selected', selectedID); else urlP.delete('selected')
    if (stateFilter !== 'all') urlP.set('state', stateFilter); else urlP.delete('state')
    if (scopeFilter !== 'all') urlP.set('scope', scopeFilter); else urlP.delete('scope')
    if (searchInput.trim()) urlP.set('q', searchInput.trim()); else urlP.delete('q')
    window.history.replaceState(null, '', `${window.location.pathname}?${urlP.toString()}`)
  }, [project, tab, asOf, compareAsOf, knownAt, selectedID, stateFilter, scopeFilter, searchInput])

  // Overview data: the full projection feeds health, clusters and the
  // bitemporal comparison — it is not paginated.
  async function loadOverview() {
    if (!project.trim()) { setError(t('knowledge.enter_project') ?? 'Enter a project ID.'); return }
    setLoading(true); setError('')
    try {
      const [history, comparison] = await Promise.all([
        observationApi.knowledge(project.trim(), asOfUTC, knownAtUTC),
        compareUTC ? observationApi.knowledge(project.trim(), compareUTC, knownAtUTC) : Promise.resolve(null),
      ])
      setKnowledge(history.knowledge ?? [])
      setComparisonKnowledge(comparison?.knowledge ?? null)
    } catch (reason) { setError(errorMessage(reason)) }
    finally { setLoading(false) }
  }

  // Knowledge tab: server-side search + filters + cursor pagination, so the
  // page stays usable when the projection grows to thousands of items.
  async function loadList(reset: boolean) {
    if (!project.trim()) return
    const seq = ++listSeq.current
    setListLoading(true); setListError('')
    try {
      const page = await observationApi.knowledge(project.trim(), asOfUTC, knownAtUTC, {
        q: debouncedSearch.trim() || undefined,
        state: stateFilter !== 'all' ? stateFilter : undefined,
        scope: scopeFilter !== 'all' ? scopeFilter : undefined,
        limit: KNOWLEDGE_PAGE_SIZE,
        cursor: reset ? undefined : listCursor || undefined,
      })
      if (seq !== listSeq.current) return
      // The server contract says [], but a historical slice that matches
      // nothing once arrived as JSON null — never trust an array field from
      // the wire enough to iterate it raw.
      const items = page.knowledge ?? []
      setListKnowledge((current) => reset ? items : [...current, ...items])
      setListTotal(page.total ?? items.length)
      setListCursor(page.next_cursor ?? '')
      if (page.facets) setFacets(page.facets)
      if (reset) setSelectedID((current) => items.some((item) => item.id === current) ? current : items[0]?.id ?? '')
    } catch (reason) { if (seq === listSeq.current) setListError(errorMessage(reason)) }
    finally { if (seq === listSeq.current) setListLoading(false) }
  }

  // Load projection
  async function loadProjection() {
    if (!project.trim() || projection) return
    setProjectionLoading(true)
    try {
      const resp = await fetch(`/kernel-api/v1/workspace/projects/${encodeURIComponent(project.trim())}/projection`, { headers: { ...authHeaders() } })
      if (resp.ok) setProjection(await resp.json())
    } catch { /* ignore */ }
    finally { setProjectionLoading(false) }
  }

  // Bitemporal filters and the search box auto-apply (debounced); mutations
  // bump the revision so both views refetch.
  useEffect(() => { void loadOverview() }, [project, asOfUTC, knownAtUTC, compareUTC, revision])
  useEffect(() => { void loadList(true) }, [project, debouncedSearch, stateFilter, scopeFilter, asOfUTC, knownAtUTC, revision])
  useEffect(() => { if (tab === 'patterns') void loadProjection() }, [tab])

  // Fetch activation chain
  useEffect(() => {
    if (!selectedID || !project.trim()) { setChain(null); return }
    const fetchChain = async () => {
      try {
        const resp = await fetch(`/kernel-api/v1/workspace/knowledge/${encodeURIComponent(selectedID)}/chain?project=${encodeURIComponent(project.trim())}`, { headers: { ...authHeaders() } })
        if (resp.ok) setChain(await resp.json())
        else setChain(null)
      } catch { setChain(null) }
    }
    void fetchChain()
  }, [selectedID, project])

  // Invalidation
  async function invalidate() {
    if (!selected || !reason.trim()) return
    try {
      await observationApi.invalidate({
        knowledge_id: selected.id, project,
        actor: { id: actor || 'human', type: 'human' },
        reason: reason.trim(),
      })
      setReason('')
      setRevision((r) => r + 1)
    } catch (e) { setError(errorMessage(e)) }
  }

  // Scope promotion: widen visibility beyond this project.
  async function promote() {
    if (!selected || !promoteReason.trim()) return
    setPromoteBusy(true); setPromoteError('')
    try {
      await observationApi.promote({
        knowledge_id: selected.id, project,
        actor: { id: actor || 'human', type: 'human' },
        scope_kind: promoteScope ? 'org_unit' : 'organization',
        scope_id: promoteScope || undefined,
        reason: promoteReason.trim(),
      })
      setPromoteReason('')
      setRevision((r) => r + 1)
    } catch (e) { setPromoteError(errorMessage(e)) }
    finally { setPromoteBusy(false) }
  }

  // Hints
  async function retrieveHints() {
    if (!query.trim() && !toolResult.trim()) return
    setHintBusy(true); setHintError('')
    try {
      const response = await observationApi.hints({ project, query, tool_result: toolResult, limit: 7 })
      setHints(response.hints ?? [])
    } catch (e) { setHintError(errorMessage(e)) }
    finally { setHintBusy(false) }
  }

  const TABS: { key: Tab; label: string }[] = [
    { key: 'overview', label: t('knowledge.overview') ?? 'Overview' },
    { key: 'knowledge', label: t('knowledge.title') ?? 'Knowledge' },
    { key: 'patterns', label: t('knowledge.patterns') ?? 'Patterns' },
  ]

  // The knowledge projection is a per-project view (docs/activity-scoping.md
  // §7): with the global filter active there is nothing to project.
  if (!project.trim()) return <ProjectRequired what={t('knowledge.title') ?? 'Knowledge'} />

  return (
    <div style={{ padding: '1rem' }}>
      {error && <InlineNotification kind="error" title="Error" subtitle={error} onClose={() => setError('')} lowContrast style={{ marginBottom: '1rem' }} />}

      {/* Tab bar with inline controls: refresh + bitemporal popover share the
          tab row (right side) to keep the header a single line. The three
          datetime fields live in a popover — the default view is "now", and
          the toggle stays quiet (badge count) until a historical cut is
          active. */}
      <div style={{ display: 'flex', alignItems: 'center', borderBottom: '1px solid var(--tm-border)', marginBottom: (asOfUTC || knownAtUTC || compareUTC) ? '0.4rem' : '1rem' }}>
        {TABS.map((t) => (
          <button
            key={t.key}
            onClick={() => setTab(t.key)}
            style={{
              padding: '0.5rem 1rem', background: tab === t.key ? 'var(--tm-elevated)' : 'transparent',
              border: 'none', borderBottom: tab === t.key ? '2px solid var(--tm-teal)' : '2px solid transparent',
              color: tab === t.key ? 'var(--tm-teal)' : 'var(--tm-text-3)', cursor: 'pointer', fontSize: '0.8rem', fontWeight: 600,
            }}
          >{t.label}</button>
        ))}
        <div style={{ marginLeft: 'auto', display: 'flex', gap: '0.5rem', alignItems: 'center' }}>
          <button onClick={() => setRevision((r) => r + 1)} disabled={loading || listLoading} style={{ background: 'none', border: 'none', color: 'var(--tm-text-2)', cursor: 'pointer', fontSize: '1rem', padding: '0.25rem', lineHeight: 1, borderRadius: '4px' }} title="Refresh">↻</button>
          <div className="time-picker">
            {timeOpen && <div className="run-picker-backdrop" onClick={() => setTimeOpen(false)} />}
            <button
              className={`time-toggle ${timeOpen ? 'open' : ''} ${(asOfUTC || knownAtUTC || compareUTC) ? 'filtered' : ''}`}
              onClick={() => setTimeOpen((open) => !open)}
            >
              ⏱ {t('knowledge.historical_view') ?? 'Historical view'}
              {(asOfUTC || knownAtUTC || compareUTC) && (
                <span className="tune-badge">{([asOfUTC, knownAtUTC, compareUTC].filter(Boolean)).length}</span>
              )}
            </button>
            {timeOpen && (
              <div className="time-panel">
                <div className="time-field">
                  <label htmlFor="as-of">{t('knowledge.as_of') ?? 'As of'}</label>
                  <input id="as-of" type="datetime-local" value={asOf} onChange={(e) => setAsOf(e.target.value)} />
                </div>
                <div className="time-field">
                  <label htmlFor="known-at">{t('knowledge.known_by') ?? 'Known by'}</label>
                  <input id="known-at" type="datetime-local" value={knownAt} onChange={(e) => setKnownAt(e.target.value)} />
                </div>
                <div className="time-field">
                  <label htmlFor="compare">{t('knowledge.compare') ?? 'Compare'}</label>
                  <input id="compare" type="datetime-local" value={compareAsOf} onChange={(e) => setCompareAsOf(e.target.value)} />
                </div>
                <div className="time-presets">
                  {TIME_PRESETS.map((preset) => (
                    <button key={preset.key} className="preset-chip" onClick={() => { setAsOf(localDateTimeValue(new Date(Date.now() - preset.shiftMs()))); setTimeOpen(false) }}>
                      {t(preset.key) ?? preset.key}
                    </button>
                  ))}
                  {(asOf || knownAt || compareAsOf) && (
                    <button className="preset-chip tune-reset" onClick={() => { setAsOf(''); setKnownAt(''); setCompareAsOf(''); setTimeOpen(false) }}>
                      {t('knowledge.preset_reset') ?? 'Now'}
                    </button>
                  )}
                </div>
                <p className="time-hint">{t('knowledge.time_hint') ?? 'as_of cuts by when events happened; known_by by when the journal ingested them; compare adds a diff point on the overview.'}</p>
              </div>
            )}
          </div>
        </div>
      </div>

      {/* Active bitemporal view: visible reminder that the projection is cut
          at a historical point, removable in one click */}
      {(asOfUTC || knownAtUTC || compareUTC) && (
        <div style={{ display: 'flex', gap: '0.35rem', flexWrap: 'wrap', marginBottom: '0.75rem', alignItems: 'center' }}>
          <span style={{ fontSize: '0.65rem', color: 'var(--tm-text-3)', textTransform: 'uppercase', letterSpacing: '0.05em' }}>⏱ {t('knowledge.historical_view') ?? 'Historical view'}</span>
          {asOfUTC && <Tag type="purple" size="sm" filter onClose={() => setAsOf('')}>{t('knowledge.as_of')}: {new Date(asOfUTC).toLocaleString()}</Tag>}
          {knownAtUTC && <Tag type="purple" size="sm" filter onClose={() => setKnownAt('')}>{t('knowledge.known_by')}: {new Date(knownAtUTC).toLocaleString()}</Tag>}
          {compareUTC && <Tag type="purple" size="sm" filter onClose={() => setCompareAsOf('')}>{t('knowledge.compare')}: {new Date(compareUTC).toLocaleString()}</Tag>}
        </div>
      )}

      {loading && <PageLoading />}

      {/* ═══════════ OVERVIEW TAB ═══════════ */}
      {tab === 'overview' && !loading && (
        <div>
          {/* Health summary — clickable to Knowledge tab */}
          <div style={{ display: 'flex', gap: '1rem', marginBottom: '1.5rem', flexWrap: 'wrap' }}>
            {([
              { label: t('knowledge.total') ?? 'Total', count: health.total, color: 'var(--tm-text)', state: 'all' },
              { label: t('knowledge.alive') ?? 'Alive', count: health.alive, color: 'var(--tm-success)', state: 'alive' },
              { label: t('knowledge.stale') ?? 'Stale', count: health.stale, color: 'var(--tm-amber)', state: 'at_risk' },
              { label: t('knowledge.invalidated') ?? 'Invalidated', count: health.invalidated, color: 'var(--tm-danger)', state: 'invalidated' },
            ]).map((s) => (
              <Tile
                key={s.label}
                className="workspace-tile"
                onClick={() => { setStateFilter(s.state); setTab('knowledge') }}
                style={{ padding: '0.75rem 1.25rem', minWidth: '100px', textAlign: 'center', cursor: 'pointer' }}
              >
                <div style={{ fontSize: '1.5rem', fontWeight: 700, color: s.color }}>{s.count}</div>
                <div style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)', textTransform: 'uppercase', letterSpacing: '0.05em' }}>{s.label}</div>
              </Tile>
            ))}
          </div>

          {/* Changes vs comparison */}
          {knowledgeChanges && compareUTC && (
            <InlineNotification
              kind="info"
              title={`Change since ${new Date(compareUTC).toLocaleString()}`}
              subtitle={`+${knowledgeChanges.added.length} new · ${knowledgeChanges.changed.length} changed · ${knowledgeChanges.previousCount} at comparison point`}
              lowContrast style={{ marginBottom: '1rem' }}
            />
          )}

          {/* Cluster summary */}
          {clusters.length > 0 && (
            <Tile style={{ marginBottom: '1rem' }}>
              <Heading style={{ fontSize: '0.875rem', marginBottom: '0.5rem' }}>{t('knowledge.by_scope') ?? 'Knowledge by scope'}</Heading>
              <div style={{ display: 'flex', gap: '0.5rem', flexWrap: 'wrap' }}>
                {clusters.map(([name, items]) => {
                  const alive = items.filter((k) => k.state !== 'invalidated' && k.state !== 'superseded' && !k.at_risk).length
                  return (
                    <div key={name} style={{ padding: '0.5rem 0.75rem', background: 'var(--tm-ink)', borderRadius: '6px', border: '1px solid var(--tm-border)', minWidth: '120px' }}>
                      <div style={{ fontSize: '0.8rem', fontWeight: 600, marginBottom: '0.25rem' }}>{name}</div>
                      <div style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)' }}>{items.length} items · {alive} alive</div>
                    </div>
                  )
                })}
              </div>
            </Tile>
          )}

          {knowledge.length === 0 && !loading && (
            <div style={{ textAlign: 'center', color: 'var(--tm-text-3)', padding: '3rem 1rem' }}>
              <p>{t('knowledge.no_knowledge') ?? 'No knowledge found for this project. Load a project to see agent knowledge.'}</p>
            </div>
          )}
        </div>
      )}

      {/* ═══════════ KNOWLEDGE TAB ═══════════ */}
      {tab === 'knowledge' && (
        <div style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 1fr) 380px', gap: '1rem' }}>
          {/* Left: compact toolbar (search + facet selects, server-side) + knowledge list */}
          <div style={{ minWidth: 0 }}>
            <div className="knowledge-toolbar">
              <input
                className="toolbar-search"
                type="search"
                value={searchInput}
                onChange={(e) => setSearchInput(e.target.value)}
                placeholder={t('knowledge.search_placeholder') ?? 'Search propositions, topics, entities…'}
                title={t('knowledge.search') ?? 'Search'}
              />
              {/* State facet select: counts come from the server, each dimension
                  counted without its own filter — the label always shows what
                  switching would reveal. */}
              <label className="toolbar-select">{t('knowledge.state_label') ?? 'State'}
                <select value={stateFilter} onChange={(e) => setStateFilter(e.target.value)} title={t('knowledge.state_label') ?? 'State'}>
                  {STATE_FILTERS.map((state) => {
                    const count = state === 'all'
                      ? (facets ? ['proposed', 'confirmed', 'challenged', 'corrected', 'superseded', 'invalidated'].reduce((sum, key) => sum + (facets.states[key] ?? 0), 0) : 0)
                      : facets?.states[state] ?? 0
                    const label = state === 'all' ? (t('knowledge.all') ?? 'All')
                      : state === 'alive' ? (t('knowledge.alive') ?? 'Alive')
                      : state === 'at_risk' ? (t('knowledge.stale') ?? 'Stale')
                      : stateLabel(state, t)
                    return <option key={state} value={state} disabled={state !== 'all' && count === 0 && stateFilter !== state}>{label} ({count})</option>
                  })}
                </select>
              </label>
              <label className="toolbar-select">{t('knowledge.scope_label') ?? 'Scope'}
                <select value={scopeFilter} onChange={(e) => setScopeFilter(e.target.value)} title={t('knowledge.scope_label') ?? 'Scope'}>
                  {SCOPE_FILTERS.map((scope) => {
                    const count = scope === 'all'
                      ? (facets ? (facets.scopes.project ?? 0) + (facets.scopes.org_unit ?? 0) + (facets.scopes.organization ?? 0) : 0)
                      : facets?.scopes[scope] ?? 0
                    const label = scope === 'all' ? (t('knowledge.all') ?? 'All')
                      : scope === 'project' ? (t('knowledge.scope_project') ?? 'Project')
                      : scope === 'org_unit' ? (t('knowledge.scope_org_unit') ?? 'Org unit')
                      : (t('knowledge.scope_org') ?? 'Organization')
                    return <option key={scope} value={scope} disabled={scope !== 'all' && count === 0 && scopeFilter !== scope}>{label} ({count})</option>
                  })}
                </select>
              </label>
            </div>
            {listError && <InlineNotification kind="error" title="Error" subtitle={listError} onClose={() => setListError('')} lowContrast style={{ marginBottom: '0.75rem' }} />}

            {/* Knowledge items */}
            {listLoading && listKnowledge.length === 0 ? <PageLoading /> : (
              <Stack gap={1}>
                {listKnowledge.map((item) => (
                  <Tile
                    key={item.id}
                    onClick={() => setSelectedID(item.id)}
                    className={`workspace-tile ${selectedID === item.id ? 'selected' : ''}`}
                    style={{ padding: '0.5rem', cursor: 'pointer' }}
                  >
                    <Tag type={STATE_COLORS[item.state] || 'gray'} size="sm">{item.at_risk ? (t('knowledge.at_risk') ?? 'at risk') : stateLabel(item.state, t)}</Tag>
                    {(item.scope_kind === 'organization' || item.scope_kind === 'org_unit') && (
                      <Tag type="purple" size="sm" title={item.scope_id}>
                        {item.scope_kind === 'organization' ? (t('knowledge.scope_organization') ?? 'organization') : (org.nameOf(item.scope_id))}
                      </Tag>
                    )}
                    <strong style={{ fontSize: '0.875rem', marginLeft: '0.35rem' }}>{item.proposition}</strong>
                    <br />
                    <small style={{ color: 'var(--tm-text-3)' }}>
                      {item.history.length} events · reused {item.reuse_count} · hints {item.hint_uses}/{item.hint_offers}
                    </small>
                  </Tile>
                ))}
              </Stack>
            )}
            {listKnowledge.length === 0 && !listLoading && (
              <div style={{ textAlign: 'center', color: 'var(--tm-text-3)', padding: '2rem 1rem' }}>
                {(asOfUTC || knownAtUTC) ? (
                  <p>{t('knowledge.empty_historical') ?? 'Nothing had happened (or was known) in this project by the selected point in time. Clear the historical view or pick a later cut.'}</p>
                ) : (
                  <p>{t('knowledge.no_results') ?? 'No results. Try a different query or filters.'}</p>
                )}
              </div>
            )}
            {(listCursor || listTotal > 0) && (
              <div style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', marginTop: '0.75rem' }}>
                {listCursor && (
                  <Button size="sm" kind="ghost" onClick={() => void loadList(false)} disabled={listLoading}>
                    {t('knowledge.load_more') ?? 'Load more'}
                  </Button>
                )}
                {listTotal > 0 && (
                  <small style={{ color: 'var(--tm-text-3)' }}>
                    {t('knowledge.showing') ?? 'Showing'}: {listKnowledge.length} / {listTotal}
                  </small>
                )}
              </div>
            )}
          </div>

          {/* Right: detail panel — sticky so it stays visible while left list scrolls */}
          <div style={{ minWidth: 0, position: 'sticky', top: '3rem', alignSelf: 'start', maxHeight: 'calc(100vh - 5rem)', overflowY: 'auto' }}>
            {!selected ? (
              <Tile style={{ textAlign: 'center', color: 'var(--tm-text-3)', padding: '2rem' }}>
                <p>{t('knowledge.select_item') ?? 'Select a knowledge item to inspect'}</p>
              </Tile>
            ) : (
              <Stack gap={2}>
                <Tile>
                  <Tag type={STATE_COLORS[selected.state] || 'gray'}>{stateLabel(selected.state, t)}</Tag>
                  {(selected.scope_kind === 'organization' || selected.scope_kind === 'org_unit') && (
                    <Tag type="purple" size="sm" title={selected.scope_id}>
                      {t('knowledge.scope_shared') ?? 'Shared'}: {selected.scope_kind === 'organization' ? (t('knowledge.scope_organization') ?? 'whole organization') : org.nameOf(selected.scope_id)}
                    </Tag>
                  )}
                  {selected.project && selected.project !== project && (
                    <Tag type="gray" size="sm" title={selected.project}>{t('knowledge.scope_origin') ?? 'from'} {selected.project}</Tag>
                  )}
                  <p style={{ fontSize: '0.875rem', fontWeight: 600, marginTop: '0.5rem', lineHeight: 1.4 }}>{selected.proposition}</p>
                  <small style={{ color: 'var(--tm-text-3)' }}>ID: {selected.id}</small>
                  <p style={{ fontSize: '0.75rem', color: 'var(--tm-text-3)', marginTop: '0.25rem' }}>
                    Created: {new Date(selected.created_at).toLocaleString()} · Updated: {new Date(selected.updated_at).toLocaleString()}
                  </p>
                  <p style={{ fontSize: '0.75rem', color: 'var(--tm-text-3)' }}>
                    Reuse: {selected.reuse_count} · Hints: {selected.hint_uses}/{selected.hint_offers} · Helpful: {selected.helpful_outcomes} · Harmful: {selected.harmful_outcomes}
                  </p>
                  {selected.at_risk && <InlineNotification kind="warning" title="At risk" subtitle={`Depends on: ${selected.risk_sources?.join(', ')}`} lowContrast />}
                  {selected.relationships?.length ? (
                    <>
                      <h5 style={{ fontSize: '0.75rem', fontWeight: 600, color: 'var(--tm-text-3)', textTransform: 'uppercase', letterSpacing: '0.05em', marginBottom: '0.5rem', marginTop: '0.75rem' }}>{t('knowledge.relations') ?? 'Relations'}</h5>
                      {selected.relationships.map((r) => <div key={r.event_id} style={{ fontSize: '0.8rem' }}>{r.type} → <code>{r.target_id}</code></div>)}
                    </>
                  ) : null}
                  {selected.evidence?.length ? (
                    <>
                      <h5 style={{ fontSize: '0.75rem', fontWeight: 600, color: 'var(--tm-text-3)', textTransform: 'uppercase', letterSpacing: '0.05em', marginBottom: '0.5rem', marginTop: '0.75rem' }}>{t('knowledge.evidence') ?? 'Evidence'}</h5>
                      {selected.evidence.map((e) => <div key={e.ref} style={{ fontSize: '0.8rem' }}><code>{e.ref}</code> ({e.type})</div>)}
                    </>
                  ) : <p style={{ color: 'var(--tm-text-3)', fontSize: '0.8rem' }}>{t('knowledge.no_evidence') ?? 'No evidence attached'}</p>}
                </Tile>

                {/* Lifecycle */}
                <Tile>
                  <h5 style={{ fontSize: '0.75rem', fontWeight: 600, color: 'var(--tm-text-3)', textTransform: 'uppercase', letterSpacing: '0.05em', marginBottom: '0.5rem' }}>{t('knowledge.lifecycle') ?? 'Lifecycle'}</h5>
                  <Stack gap={1}>
                    {selected.history.map((entry) => (
                      <div key={entry.event_id} style={{ display: 'flex', gap: '0.5rem', alignItems: 'flex-start', fontSize: '0.75rem' }}>
                        <small style={{ color: 'var(--tm-text-3)', minWidth: '7rem' }}>{new Date(entry.at).toLocaleString()}</small>
                        <Tag type="gray" size="sm">{entry.type}</Tag>
                        <span>{stateLabel(entry.state, t)}{entry.rule ? ` · ${entry.rule}` : ''}</span>
                        {entry.reason && <small style={{ color: 'var(--tm-text-3)' }}>{entry.reason}</small>}
                      </div>
                    ))}
                  </Stack>
                </Tile>

                {/* Activation chain */}
                {chain && chain.activations?.length > 0 && (
                  <Tile>
                    <h5 style={{ fontSize: '0.75rem', fontWeight: 600, color: 'var(--tm-text-3)', textTransform: 'uppercase', letterSpacing: '0.05em', marginBottom: '0.5rem' }}>
                      {t('knowledge.activation_chain') ?? 'Activation Chain'} — {chain.total_runs} {t('knowledge.runs')?.toLowerCase() ?? 'runs'} · {chain.activations.length} {t('knowledge.activations') ?? 'activations'} · {chain.is_alive ? (t('knowledge.alive')?.toLowerCase() ?? 'alive') : (t('knowledge.dead')?.toLowerCase() ?? 'dead')}
                    </h5>
                    {chain.formation_evidence?.length > 0 && (
                      <div style={{ marginBottom: '0.5rem', padding: '0.4rem 0.5rem', borderLeft: '3px solid var(--tm-teal)', background: 'rgba(87,215,232,0.05)', borderRadius: '0 4px 4px 0' }}>
                        <div style={{ fontSize: '0.75rem', color: 'var(--tm-teal)', fontWeight: 600, marginBottom: '0.25rem' }}>{t('knowledge.formed_in') ?? 'Formed in'} {chain.formed_in} {t('knowledge.turn') ?? 'turn'} {chain.formed_turn}</div>
                        <div style={{ display: 'flex', gap: '0.25rem', flexWrap: 'wrap' }}>
                          {chain.formation_evidence.map((t: any, i: number) => (
                            <Tag key={i} type={t.success ? 'green' : 'red'} size="sm">{t.tool}{t.exit_code !== undefined && t.exit_code !== 0 ? ` (${t.exit_code})` : ''}</Tag>
                          ))}
                        </div>
                      </div>
                    )}
                    {chain.activations.map((a: any, i: number) => (
                      <div key={i} style={{ padding: '0.4rem 0.5rem', borderLeft: `3px solid ${a.outcome === 'success' ? 'var(--tm-success)' : a.outcome === 'failure' ? 'var(--tm-danger)' : 'var(--tm-text-3)'}`, marginBottom: '0.25rem', background: 'rgba(255,255,255,0.02)', borderRadius: '0 4px 4px 0', fontSize: '0.8rem' }}>
                        <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
                          <strong>Turn {a.turn}</strong>
                          <span style={{ color: 'var(--tm-text-3)' }}>{a.run_id}</span>
                          {a.tool_name && <Tag type={a.tool_success ? 'green' : 'red'} size="sm">{a.tool_name}</Tag>}
                          {a.outcome && <Tag type={a.outcome === 'success' ? 'green' : 'red'} size="sm">{a.outcome}</Tag>}
                        </div>
                        <div style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)', marginTop: '0.15rem' }}>
                          recalled {new Date(a.recalled_at).toLocaleString()}
                          {a.injected_at && ` → injected ${new Date(a.injected_at).toLocaleString()}`}
                        </div>
                      </div>
                    ))}
                    {chain.invalidation && (
                      <div style={{ padding: '0.4rem 0.5rem', borderLeft: '3px solid var(--tm-danger)', marginTop: '0.25rem', background: 'rgba(247,118,142,0.05)', borderRadius: '0 4px 4px 0', fontSize: '0.8rem' }}>
                        <strong style={{ color: 'var(--tm-danger)' }}>{chain.invalidation.kind}</strong>
                        <span style={{ color: 'var(--tm-text-3)', marginLeft: '0.5rem' }}>{chain.invalidation.run_id} · {new Date(chain.invalidation.at).toLocaleString()}</span>
                      </div>
                    )}
                  </Tile>
                )}

                {/* Manual invalidation */}
                {selected.state !== 'invalidated' && selected.state !== 'corrected' && selected.state !== 'superseded' && (
                  <Tile>
                    <h5 style={{ fontSize: '0.75rem', fontWeight: 600, color: 'var(--tm-text-3)', textTransform: 'uppercase', letterSpacing: '0.05em', marginBottom: '0.5rem' }}>{t('knowledge.manual_invalidation') ?? 'Manual Invalidation'}</h5>
                    <Stack gap={2}>
                      <TextInput id="actor" labelText={t('knowledge.actor') ?? 'Actor'} value={actor} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setActor(e.target.value)} size="sm" />
                      <TextArea id="reason" labelText={t('knowledge.reason') ?? 'Reason'} value={reason} onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => setReason(e.target.value)} rows={2} />
                      <Button kind="danger" size="sm" onClick={invalidate} disabled={!reason.trim()}>{t('knowledge.invalidate') ?? 'Invalidate'}</Button>
                    </Stack>
                  </Tile>
                )}

                {/* Scope promotion: widen visibility beyond this project */}
                {selected.scope_kind !== 'organization' && selected.state !== 'invalidated' && selected.state !== 'corrected' && selected.state !== 'superseded' && (
                  <Tile>
                    <h5 style={{ fontSize: '0.75rem', fontWeight: 600, color: 'var(--tm-text-3)', textTransform: 'uppercase', letterSpacing: '0.05em', marginBottom: '0.5rem' }}>{t('knowledge.promote_title') ?? 'Share knowledge'}</h5>
                    <p style={{ fontSize: '0.75rem', color: 'var(--tm-text-3)', marginBottom: '0.5rem' }}>{t('knowledge.promote_hint') ?? 'Knowledge is project-local by default. Promote it to share with other projects.'}</p>
                    <Stack gap={2}>
                      <OrgUnitSelect
                        id="promote-scope"
                        value={promoteScope}
                        onChange={setPromoteScope}
                        org={org}
                        label={t('knowledge.promote_scope') ?? 'Share with'}
                      />
                      <TextArea id="promote-reason" labelText={t('knowledge.reason') ?? 'Reason'} value={promoteReason} onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => setPromoteReason(e.target.value)} rows={2} placeholder={t('knowledge.promote_reason_placeholder') ?? 'Why does this knowledge hold beyond this project?'} />
                      {promoteError && <InlineNotification kind="error" title="Error" subtitle={promoteError} onClose={() => setPromoteError('')} lowContrast />}
                      <Button size="sm" onClick={promote} disabled={promoteBusy || !promoteReason.trim()}>
                        {promoteBusy ? (t('knowledge.promoting') ?? 'Sharing…') : (t('knowledge.promote') ?? 'Share')}
                      </Button>
                    </Stack>
                  </Tile>
                )}

                {/* Memory hints */}
                <Tile>
                  <h5 style={{ fontSize: '0.75rem', fontWeight: 600, color: 'var(--tm-text-3)', textTransform: 'uppercase', letterSpacing: '0.05em', marginBottom: '0.5rem' }}>{t('knowledge.memory_lookup') ?? 'Memory Lookup'}</h5>
                  <Stack gap={2}>
                    <TextArea id="query" labelText={t('knowledge.query') ?? 'Query'} value={query} onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => setQuery(e.target.value)} rows={2} placeholder="What is the agent trying to do?" />
                    <TextInput id="tool" labelText={t('knowledge.tool') ?? 'Tool'} value={tool} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setTool(e.target.value)} placeholder="test-runner" size="sm" />
                    <TextArea id="tool-result" labelText={t('knowledge.tool_result') ?? 'Tool result'} value={toolResult} onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => setToolResult(e.target.value)} rows={2} placeholder="Relevant excerpt" />
                    <Button size="sm" onClick={retrieveHints} disabled={hintBusy || (!query.trim() && !toolResult.trim())}>
                      {hintBusy ? (t('knowledge.searching') ?? 'Searching…') : (t('knowledge.retrieve_hints') ?? 'Retrieve hints')}
                    </Button>
                    {hintError && <InlineNotification kind="error" title="Error" subtitle={hintError} lowContrast />}
                    {hints.map((hint) => (
                      <Tile key={hint.hint_id} style={{ padding: '0.5rem' }}>
                        <Tag type="gray" size="sm">{stateLabel(hint.state, t)}</Tag>
                        <strong style={{ fontSize: '0.85rem' }}>{hint.proposition}</strong>
                        {hint.caution && <small style={{ color: '#e6b85c', display: 'block', marginTop: '0.25rem' }}>{hint.caution}</small>}
                        <small style={{ color: 'var(--tm-text-3)', display: 'block' }}>{t('knowledge.matched_by') ?? 'Matched by'}: {hint.matched_by.join(', ')}</small>
                      </Tile>
                    ))}
                  </Stack>
                </Tile>
              </Stack>
            )}
          </div>
        </div>
      )}

      {/* ═══════════ PATTERNS TAB ═══════════ */}
      {tab === 'patterns' && (
        <div>
          {projectionLoading && <PageLoading />}
          {!projection && !projectionLoading && (
            <div style={{ textAlign: 'center', color: 'var(--tm-text-3)', padding: '3rem 1rem' }}>
              <p>{t('knowledge.no_projection') ?? 'No projection data yet. Click Load to fetch experience patterns.'}</p>
              <Button onClick={loadProjection} style={{ marginTop: '1rem' }}>{t('knowledge.load_patterns') ?? 'Load patterns'}</Button>
            </div>
          )}
          {projection && (
            <Stack gap={3}>
              {/* Summary */}
              <div style={{ display: 'flex', gap: '1rem', flexWrap: 'wrap' }}>
                {[
                  { label: t('knowledge.runs') ?? 'Runs', count: projection.run_count, color: 'var(--tm-teal)' },
                  { label: t('knowledge.tool_patterns') ?? 'Tool patterns', count: projection.summary?.total_tool_patterns ?? 0, color: 'var(--tm-success)' },
                  { label: t('knowledge.title') ?? 'Knowledge', count: projection.summary?.total_knowledge ?? 0, color: 'var(--tm-teal)' },
                  { label: t('knowledge.alive') ?? 'Alive', count: projection.summary?.alive_knowledge ?? 0, color: 'var(--tm-success)' },
                  { label: t('knowledge.dead') ?? 'Dead', count: projection.summary?.dead_knowledge ?? 0, color: 'var(--tm-danger)' },
                ].map((s) => (
                  <Tile key={s.label} style={{ padding: '0.5rem 1rem', minWidth: '80px', textAlign: 'center' }}>
                    <div style={{ fontSize: '1.25rem', fontWeight: 700, color: s.color }}>{s.count}</div>
                    <div style={{ fontSize: '0.65rem', color: 'var(--tm-text-3)', textTransform: 'uppercase', letterSpacing: '0.05em' }}>{s.label}</div>
                  </Tile>
                ))}
                {projection.summary?.avg_success_rate > 0 && (
                  <Tile style={{ padding: '0.5rem 1rem', textAlign: 'center' }}>
                    <div style={{ fontSize: '1.25rem', fontWeight: 700, color: 'var(--tm-success)' }}>{(projection.summary.avg_success_rate * 100).toFixed(0)}%</div>
                    <div style={{ fontSize: '0.65rem', color: 'var(--tm-text-3)', textTransform: 'uppercase', letterSpacing: '0.05em' }}>{t('knowledge.success_rate') ?? 'Success rate'}</div>
                  </Tile>
                )}
              </div>

              {/* Top tools */}
              {projection.summary?.most_used_tools?.length > 0 && (
                <Tile>
                  <h5 style={{ fontSize: '0.75rem', fontWeight: 600, color: 'var(--tm-text-3)', textTransform: 'uppercase', letterSpacing: '0.05em', marginBottom: '0.5rem' }}>{t('knowledge.most_used_tools') ?? 'Most used tools'}</h5>
                  <div style={{ display: 'flex', gap: '0.35rem', flexWrap: 'wrap' }}>
                    {projection.summary.most_used_tools.map((t: string) => <Tag key={t} type="blue" size="sm">{t}</Tag>)}
                  </div>
                </Tile>
              )}

              {/* Tool patterns */}
              {projection.tool_patterns?.length > 0 && (
                <Tile>
                  <h5 style={{ fontSize: '0.75rem', fontWeight: 600, color: 'var(--tm-text-3)', textTransform: 'uppercase', letterSpacing: '0.05em', marginBottom: '0.5rem' }}>{t('knowledge.tool_patterns') ?? 'Tool Patterns'}</h5>
                  <Stack gap={1}>
                    {projection.tool_patterns.map((p: any, i: number) => (
                      <div key={i} style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', fontSize: '0.8rem', padding: '0.3rem 0', borderBottom: '1px solid #1a2332' }}>
                        <Tag type="cyan" size="sm">{p.count}x</Tag>
                        <code style={{ color: '#9e8cff', fontSize: '0.78rem' }}>{p.signature}</code>
                        <span style={{ color: 'var(--tm-text-3)', marginLeft: 'auto' }}>{p.run_count} runs · {(p.success_rate * 100).toFixed(0)}% success</span>
                      </div>
                    ))}
                  </Stack>
                </Tile>
              )}

              {/* Knowledge lifecycle */}
              {projection.knowledge_lifecycle?.length > 0 && (
                <Tile>
                  <h5 style={{ fontSize: '0.75rem', fontWeight: 600, color: 'var(--tm-text-3)', textTransform: 'uppercase', letterSpacing: '0.05em', marginBottom: '0.5rem' }}>{t('knowledge.knowledge_lifecycle') ?? 'Knowledge Lifecycle'}</h5>
                  <Stack gap={1}>
                    {projection.knowledge_lifecycle.map((k: any, i: number) => (
                      <div key={i} style={{ display: 'flex', alignItems: 'flex-start', gap: '0.5rem', fontSize: '0.8rem', padding: '0.4rem 0', borderBottom: '1px solid #1a2332' }}>
                        <Tag type={k.is_alive ? 'green' : 'red'} size="sm" style={{ flexShrink: 0 }}>{stateLabel(k.current_state, t)}</Tag>
                        <span style={{ flex: 1, wordBreak: 'break-word' }}>{k.proposition || k.knowledge_id}</span>
                        <span style={{ color: 'var(--tm-text-3)', flexShrink: 0 }}>{k.run_count} runs</span>
                      </div>
                    ))}
                  </Stack>
                </Tile>
              )}

              {/* Scopes */}
              {projection.scopes?.length > 0 && (
                <Tile>
                  <h5 style={{ fontSize: '0.75rem', fontWeight: 600, color: 'var(--tm-text-3)', textTransform: 'uppercase', letterSpacing: '0.05em', marginBottom: '0.5rem' }}>{t('knowledge.scopes') ?? 'Scopes'}</h5>
                  <div style={{ display: 'flex', gap: '0.5rem', flexWrap: 'wrap' }}>
                    {projection.scopes.map((s: any, i: number) => (
                      <div key={i} style={{ padding: '0.4rem 0.6rem', background: 'var(--tm-ink)', borderRadius: '4px', border: '1px solid var(--tm-border)', fontSize: '0.8rem' }}>
                        <strong>{s.scope}</strong>
                        <span style={{ color: 'var(--tm-text-3)', marginLeft: '0.5rem' }}>{s.tool_count} tools · {s.run_ids?.length ?? 0} runs</span>
                      </div>
                    ))}
                  </div>
                </Tile>
              )}
            </Stack>
          )}
        </div>
      )}
    </div>
  )
}