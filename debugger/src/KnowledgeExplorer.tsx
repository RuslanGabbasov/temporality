import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Button, TextInput, InlineNotification, Tag } from '@carbon/react'
import {
  observationApi,
  type ExploreArea,
  type ExploreKnowledgeCard,
  type KnowledgeFacets,
  type KnowledgeItem,
} from './observationApi'
import AreaMap, { areaKey } from './explorer/AreaMap'
import ConflictPanel from './explorer/ConflictPanel'
import LifecycleBar from './explorer/LifecycleBar'
import AgentResearch from './explorer/AgentResearch'
import HintsPreview from './explorer/HintsPreview'
import SessionsPanel from './explorer/SessionsPanel'
import { applySessionPayload, type SessionSnapshot } from './explorer/sessions'
import Dossier from './Dossier'
import { stateLabel, kindLabel, scopeLabel } from './explorer/shared'
import ProjectRequired from './ProjectRequired'
import { workspaceApi } from './workspaceApi'
import { useT } from './i18n'

function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : 'Request failed'
}

/** Filters auto-apply, so the search input needs a debounce to avoid firing a
 *  request triple (orient + search + listing) per keystroke. */
function useDebounced<T>(value: T, delayMs = 350): T {
  const [debounced, setDebounced] = useState(value)
  useEffect(() => {
    const timer = setTimeout(() => setDebounced(value), delayMs)
    return () => clearTimeout(timer)
  }, [value, delayMs])
  return debounced
}

const PAGE_SIZE = 50
const STATE_ORDER = ['alive', 'at_risk', 'proposed', 'confirmed', 'challenged', 'corrected', 'superseded', 'invalidated']
const STATE_TAG: Record<string, 'blue' | 'green' | 'warm-gray' | 'gray' | 'red'> = {
  proposed: 'blue', confirmed: 'green', challenged: 'warm-gray',
  corrected: 'warm-gray', superseded: 'gray', invalidated: 'red',
}

const EMPTY_FILTERS = { state: '', scope: '', topic: '', entity: '', origin: '', kind: '' }
type Filters = typeof EMPTY_FILTERS

/** One navigation record of the research session (ТЗ §3.3): what moved, and
 *  the full explorer snapshot to restore on click. */
interface NavRecord {
  id: number
  label: string
  question: string
  filters: Filters
  areaKey: string
  view: string
  dossierID: string
}

type View = 'search' | 'conflicts' | 'agent'

export default function KnowledgeExplorer({ project }: { project: string }) {
  const t = useT()
  const initParams = new URLSearchParams(window.location.search)

  const [question, setQuestion] = useState(initParams.get('q') ?? '')
  const [searchInput, setSearchInput] = useState(initParams.get('q') ?? '')
  const [filters, setFilters] = useState<Filters>({
    state: initParams.get('state') ?? '',
    scope: initParams.get('scope') ?? '',
    topic: initParams.get('topic') ?? '',
    entity: initParams.get('entity') ?? '',
    origin: initParams.get('origin') ?? '',
    kind: initParams.get('kind') ?? '',
  })
  const [selectedAreaKey, setSelectedAreaKey] = useState(initParams.get('area') ?? '')
  const [revision, setRevision] = useState(0)
  const [view, setView] = useState<View>(initParams.get('view') === 'conflicts' ? 'conflicts' : initParams.get('view') === 'agent' ? 'agent' : 'search')
  const [dossierID, setDossierID] = useState(initParams.get('dossier') ?? '')

  const [areas, setAreas] = useState<ExploreArea[]>([])
  const [annotations, setAnnotations] = useState<Map<string, ExploreKnowledgeCard>>(new Map())
  const [exploreError, setExploreError] = useState('')

  const [list, setList] = useState<KnowledgeItem[]>([])
  const [facets, setFacets] = useState<KnowledgeFacets | null>(null)
  const [total, setTotal] = useState(0)
  const [cursor, setCursor] = useState('')
  const [listLoading, setListLoading] = useState(false)
  const [listError, setListError] = useState('')

  const [expandedID, setExpandedID] = useState('')

  const [areaFilter, setAreaFilter] = useState('')
  const [areaSort, setAreaSort] = useState<'size' | 'name'>('size')
  const [normalizeBusy, setNormalizeBusy] = useState(false)

  const [navStack, setNavStack] = useState<NavRecord[]>([])
  const navSeq = useRef(0)
  const listSeq = useRef(0)
  const exploreSeq = useRef(0)

  // Research sessions (ТЗ §11): panel visibility and the session currently
  // bound to the editor (0 = the position is not saved yet).
  const [sessionsOpen, setSessionsOpen] = useState(false)
  const [currentSessionID, setCurrentSessionID] = useState(0)

  const debouncedQuestion = useDebounced(question)

  const pushNav = useCallback((label: string, next: { question?: string; filters?: Filters; areaKey?: string; view?: string; dossierID?: string }) => {
    setNavStack((stack) => {
      const last = stack[stack.length - 1]
      if (last && last.label === label && last.question === (next.question ?? last.question)) return stack
      navSeq.current += 1
      const record: NavRecord = {
        id: navSeq.current,
        label,
        question: next.question ?? last?.question ?? '',
        filters: next.filters ?? last?.filters ?? { ...EMPTY_FILTERS },
        areaKey: next.areaKey ?? last?.areaKey ?? '',
        view: next.view ?? last?.view ?? 'search',
        dossierID: next.dossierID ?? (next.view && next.view !== 'search' ? '' : last?.dossierID ?? ''),
      }
      return [...stack.slice(-19), record]
    })
  }, [])

  /** URL carries the full research context so a link reproduces the view and
   *  the browser back/forward buttons move through it (ТЗ §3.3). */
  useEffect(() => {
    const url = new URLSearchParams()
    url.set('project', project)
    if (question.trim()) url.set('q', question.trim()); else url.delete('q')
    if (selectedAreaKey) url.set('area', selectedAreaKey); else url.delete('area')
    if (view !== 'search') url.set('view', view); else url.delete('view')
    if (dossierID) url.set('dossier', dossierID); else url.delete('dossier')
    for (const [key, value] of Object.entries(filters)) {
      if (value) url.set(key, value)
    }
    window.history.replaceState(null, '', `${window.location.pathname}?${url.toString()}`)
  }, [project, question, filters, selectedAreaKey, view, dossierID])

  const applyParams = useCallback(() => {
    const params = new URLSearchParams(window.location.search)
    setSearchInput(params.get('q') ?? '')
    setQuestion(params.get('q') ?? '')
    setSelectedAreaKey(params.get('area') ?? '')
    setView(params.get('view') === 'conflicts' ? 'conflicts' : params.get('view') === 'agent' ? 'agent' : 'search')
    setDossierID(params.get('dossier') ?? '')
    setFilters({
      state: params.get('state') ?? '',
      scope: params.get('scope') ?? '',
      topic: params.get('topic') ?? '',
      entity: params.get('entity') ?? '',
      origin: params.get('origin') ?? '',
      kind: params.get('kind') ?? '',
    })
  }, [])

  useEffect(() => {
    const onPop = () => applyParams()
    window.addEventListener('popstate', onPop)
    return () => window.removeEventListener('popstate', onPop)
  }, [applyParams])

  // Orient maps the areas; search explains why each card matched. Both run off
  // the same question so the map and the result list never disagree (§4–5).
  useEffect(() => {
    if (!project.trim()) return
    const seq = ++exploreSeq.current
    setExploreError('')
    ;(async () => {
      try {
        const [orientRes, searchRes] = await Promise.all([
          observationApi.explore({ project: project.trim(), operation: 'orient', question: debouncedQuestion.trim() || undefined, max_items: 24 }),
          debouncedQuestion.trim()
            ? observationApi.explore({ project: project.trim(), operation: 'search', question: debouncedQuestion.trim(), max_items: 100 })
            : Promise.resolve(null),
        ])
        if (seq !== exploreSeq.current) return
        setAreas(orientRes.areas ?? [])
        const map = new Map<string, ExploreKnowledgeCard>()
        for (const card of searchRes?.results ?? []) map.set(card.knowledge_id, card)
        setAnnotations(map)
      } catch (e) {
        if (seq === exploreSeq.current) setExploreError(e instanceof Error ? e.message : 'Request failed')
      }
    })()
    return () => { exploreSeq.current++ }
  }, [project, debouncedQuestion, revision])

  async function loadList(append: boolean) {
    if (!project.trim()) return
    const seq = ++listSeq.current
    setListLoading(true); setListError('')
    try {
      const page = await observationApi.knowledge(project.trim(), undefined, undefined, {
        q: debouncedQuestion.trim() || undefined,
        state: filters.state || undefined,
        scope: filters.scope || undefined,
        topic: filters.topic || undefined,
        entity: filters.entity || undefined,
        origin: filters.origin || undefined,
        kind: filters.kind || undefined,
        limit: PAGE_SIZE,
        cursor: append && cursor ? cursor : undefined,
      })
      if (seq !== listSeq.current) return
      const items = page.knowledge ?? []
      setList((current) => append ? [...current, ...items] : items)
      setTotal(page.total ?? items.length)
      setCursor(page.next_cursor ?? '')
      if (page.facets) setFacets(page.facets)
    } catch (e) {
      if (seq === listSeq.current) setListError(e instanceof Error ? e.message : 'Request failed')
    } finally {
      if (seq === listSeq.current) setListLoading(false)
    }
  }

  useEffect(() => { void loadList(false) }, [project, debouncedQuestion, filters, revision])

  const selectArea = useCallback((area: ExploreArea) => {
    const key = areaKey(area)
    setSelectedAreaKey(key)
    setQuestion(area.name)
    setSearchInput(area.name)
    pushNav(area.name, { question: area.name, areaKey: key })
  }, [pushNav])

  const setFilter = useCallback((dimension: keyof Filters, value: string) => {
    setFilters((current) => {
      const next = { ...current, [dimension]: value || '' }
      pushNav(`${t(`explorer.facet_${dimension}`) ?? dimension}: ${value}`, { filters: next })
      return next
    })
  }, [pushNav, t])

  const submitQuestion = useCallback(() => {
    setQuestion(searchInput)
    setSelectedAreaKey('')
    setView('search')
    if (searchInput.trim()) pushNav(searchInput.trim(), { question: searchInput.trim(), areaKey: '', view: 'search' })
  }, [pushNav, searchInput])

  const restoreNav = useCallback((record: NavRecord) => {
    setQuestion(record.question)
    setSearchInput(record.question)
    setFilters(record.filters)
    setSelectedAreaKey(record.areaKey)
    setView(record.view === 'conflicts' || record.view === 'agent' ? record.view : 'search')
    setDossierID(record.dossierID)
  }, [])

  const switchView = useCallback((next: View) => {
    setView(next)
    setDossierID('')
    const label = next === 'conflicts' ? (t('explorer.tab_conflicts') ?? 'Contradictions')
      : next === 'agent' ? (t('explorer.tab_agent') ?? 'Agent research')
        : (t('explorer.tab_search') ?? 'Search')
    pushNav(label, { view: next, dossierID: '' })
  }, [pushNav, t])

  const openDossier = useCallback((id: string) => {
    setDossierID(id)
    pushNav(`${t('explorer.dossier') ?? 'Dossier'}: …${id.slice(-8)}`, { dossierID: id })
  }, [pushNav, t])

  const closeDossier = useCallback(() => {
    setDossierID('')
  }, [])

  /** Restore a saved session (ТЗ §11): apply the research position and put
   *  the navigation path back — knowledge itself is re-resolved from the
   *  CURRENT projection, so the session honors revoked rights and changed
   *  statements (§11.3). */
  const applySession = useCallback((state: SessionSnapshot) => {
    setQuestion(state.question)
    setSearchInput(state.searchInput)
    setFilters(state.filters)
    setSelectedAreaKey(state.areaKey)
    setView(state.view === 'conflicts' || state.view === 'agent' ? state.view : 'search')
    setDossierID(state.dossierID)
    setNavStack(state.nav.map((record) => ({ ...record })))
    navSeq.current = Math.max(navSeq.current, ...state.nav.map((record) => record.id), 0)
  }, [])

  /** Live research position for session saves; notes and conclusion are
   *  merged in by the panel — they are the researcher's words, not knowledge. */
  const sessionPosition = useCallback((): SessionSnapshot => ({
    question,
    searchInput,
    filters,
    areaKey: selectedAreaKey,
    view,
    dossierID,
    nav: navStack,
    notes: '',
    conclusion: '',
  }), [question, searchInput, filters, selectedAreaKey, view, dossierID, navStack])

  /** Start a fresh research question from anywhere (dossier «research from
   *  this statement» etc.) — returns to the search view. */
  const startQuestion = useCallback((nextQuestion: string) => {
    setQuestion(nextQuestion)
    setSearchInput(nextQuestion)
    setView('search')
    setDossierID('')
    if (nextQuestion.trim()) pushNav(nextQuestion.trim().slice(0, 28), { question: nextQuestion.trim(), view: 'search', dossierID: '' })
  }, [pushNav])

  const visibleAreas = useMemo(() => {
    const needle = areaFilter.trim().toLowerCase()
    const filtered = needle ? areas.filter((a) => a.name.includes(needle)) : areas
    return [...filtered].sort((a, b) =>
      areaSort === 'size'
        ? (b.knowledge_count || 0) - (a.knowledge_count || 0) || a.name.localeCompare(b.name)
        : a.name.localeCompare(b.name))
  }, [areas, areaFilter, areaSort])

  // Judge-судья сводит варианты областей к каноничным и прячет мусорные
  // (имена файлов и т.п.). After success the map re-fetches via revision; the
  // summary lands in a neutral notice line, not the error notification.
  const [normalizeNotice, setNormalizeNotice] = useState('')
  const normalizeAreas = useCallback(async () => {
    setNormalizeBusy(true)
    setExploreError('')
    setNormalizeNotice('')
    try {
      const result = await workspaceApi.normalizeKnowledgeAreas(project)
      setNormalizeNotice(t('explorer.area_normalize_done', { merged: String(result.merged ?? 0), hidden: String(result.hidden ?? 0) }))
      setRevision((r) => r + 1)
    } catch (failure) {
      setExploreError(errorMessage(failure))
    } finally {
      setNormalizeBusy(false)
    }
  }, [project, t])

  // "No data" vs "no results" (ТЗ §6.2, §13.4): the orient map ignores the
  // listing filters, so it reliably separates an empty knowledge base from an
  // over-filtered one.
  const hasAnyData = areas.length > 0 || list.length > 0
  const activeFilterCount = Object.values(filters).filter(Boolean).length

  if (!project.trim()) return <ProjectRequired what={t('explorer.title') ?? 'Knowledge explorer'} />

  const chip = (active: boolean) => ({
    padding: '0.15rem 0.55rem', borderRadius: '999px', border: `1px solid ${active ? 'var(--tm-teal)' : 'var(--tm-border)'}`,
    background: active ? 'var(--tm-elevated)' : 'transparent',
    color: active ? 'var(--tm-teal)' : 'var(--tm-text-2)', cursor: 'pointer', fontSize: '0.7rem', whiteSpace: 'nowrap',
  })

  // Compact facet filter (same toolbar pattern as the timeline): a small
  // labeled select per dimension instead of a wall of chip rows.
  const facetSelect = (label: string, dimension: keyof Filters, values: [string, number][], labeler: (value: string) => string = (value) => value) => {
    if (values.length === 0) return null
    const active = filters[dimension]
    // The active value may have dropped out of the current facet counts
    // (other filters narrowed the set) — keep it selectable so the select
    // never renders a blank value.
    const missing = active && !values.some(([value]) => value === active)
    return (
      <label key={dimension} className="toolbar-select">
        {label}
        <select
          value={active}
          onChange={(e) => setFilter(dimension, e.target.value)}
          style={active ? { borderColor: 'var(--tm-teal)', color: 'var(--tm-teal)' } : undefined}
        >
          <option value="">{t('explorer.all') ?? 'All'}</option>
          {missing && <option value={active}>{labeler(active)}</option>}
          {values.map(([value, count]) => (
            <option key={value} value={value}>{labeler(value)} · {count}</option>
          ))}
        </select>
      </label>
    )
  }

  const facetValues = (map?: Record<string, number>): [string, number][] =>
    Object.entries(map ?? {}).filter(([, count]) => count > 0).sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))

  const stateFacetValues: [string, number][] = STATE_ORDER
    .map((state) => [state, facets?.states?.[state] ?? 0] as [string, number])
    .filter(([, count]) => count > 0)

  return (
    <div style={{ padding: '1rem' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', marginBottom: '0.75rem', flexWrap: 'wrap' }}>
        <h2 style={{ margin: 0, fontSize: '1.1rem' }}>{t('explorer.title') ?? 'Knowledge explorer'}</h2>
        <div style={{ flex: 1, minWidth: '320px', display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
          <TextInput
            id="explorer-search"
            size="sm"
            value={searchInput}
            placeholder={t('explorer.search_placeholder') ?? 'Question or keywords…'}
            onChange={(e) => setSearchInput(e.target.value)}
            onKeyDown={(e) => { if (e.key === 'Enter') submitQuestion() }}
            labelText=""
            hideLabel
            style={{ flex: 1 }}
          />
          <Button size="sm" onClick={submitQuestion}>{t('knowledge.search') ?? 'Search'}</Button>
          <button onClick={() => setRevision((r) => r + 1)} disabled={listLoading} title={t('explorer.refresh_hint') ?? 'Refresh'} style={{ background: 'none', border: 'none', color: 'var(--tm-text-2)', cursor: 'pointer', fontSize: '1rem', padding: '0.25rem', lineHeight: 1, borderRadius: '4px', flexShrink: 0 }}>↻</button>
          <button
            onClick={() => setSessionsOpen((value) => !value)}
            title={t('explorer.sessions_hint_short') ?? 'Save the current investigation (question, filters, path) and come back to it later'}
            style={{
              background: sessionsOpen ? 'var(--tm-elevated)' : 'none', border: `1px solid ${sessionsOpen ? 'var(--tm-teal)' : 'var(--tm-border)'}`,
              color: sessionsOpen ? 'var(--tm-teal)' : 'var(--tm-text-2)', cursor: 'pointer', fontSize: '0.7rem',
              padding: '0.3rem 0.6rem', lineHeight: 1.4, borderRadius: '999px', whiteSpace: 'nowrap', flexShrink: 0,
            }}
          >{t('explorer.sessions_button') ?? 'Sessions'}{currentSessionID > 0 ? ' ●' : ''}</button>
        </div>
      </div>

      <SessionsPanel
        project={project}
        open={sessionsOpen}
        snapshot={sessionPosition}
        currentID={currentSessionID}
        onLoaded={(id, _title, state) => { setCurrentSessionID(id); applySession(state) }}
        onClose={() => setSessionsOpen(false)}
      />

      {exploreError && <InlineNotification kind="warning" title="Explore" subtitle={exploreError} onClose={() => setExploreError('')} lowContrast style={{ marginBottom: '0.75rem' }} />}
      {listError && <InlineNotification kind="error" title="Error" subtitle={listError} onClose={() => setListError('')} lowContrast style={{ marginBottom: '0.75rem'}} />}

      {/* Research view tabs: search, contradiction discovery and the agent's
          own research calls (ТЗ §10, §12). */}
      <div style={{ display: 'flex', alignItems: 'center', gap: '0.25rem', borderBottom: '1px solid var(--tm-border)', marginBottom: '0.75rem' }}>
        {(['search', 'conflicts', 'agent'] as View[]).map((key) => (
          <button
            key={key}
            onClick={() => switchView(key)}
            style={{
              padding: '0.45rem 0.9rem', background: view === key && !dossierID ? 'var(--tm-elevated)' : 'transparent',
              border: 'none', borderBottom: view === key && !dossierID ? '2px solid var(--tm-teal)' : '2px solid transparent',
              color: view === key && !dossierID ? 'var(--tm-teal)' : 'var(--tm-text-3)', cursor: 'pointer', fontSize: '0.78rem', fontWeight: 600,
            }}
          >{key === 'search' ? (t('explorer.tab_search') ?? 'Search') : key === 'conflicts' ? (t('explorer.tab_conflicts') ?? 'Contradictions') : (t('explorer.tab_agent') ?? 'Agent research')}</button>
        ))}
      </div>

      {navStack.length > 1 && (
        <div style={{ display: 'flex', alignItems: 'center', gap: '0.35rem', flexWrap: 'wrap', marginBottom: '0.75rem' }}>
          <span title={t('explorer.nav_history_hint') ?? 'Screens you opened during this investigation. Click to return; × clears the history.'} style={{ fontSize: '0.7rem', color: 'var(--tm-muted)' }}>{t('explorer.nav_history') ?? 'Path'}:</span>
          {navStack.slice(-6).map((record) => (
            <button key={record.id} style={chip(false)} onClick={() => restoreNav(record)} title={record.question}>
              {record.label.length > 28 ? `${record.label.slice(0, 27)}…` : record.label}
            </button>
          ))}
          <button
            onClick={() => setNavStack((stack) => stack.slice(-1))}
            title={t('explorer.nav_history_clear') ?? 'Clear history'}
            style={{ background: 'none', border: 'none', color: 'var(--tm-text-3)', cursor: 'pointer', fontSize: '0.8rem', padding: '0.15rem 0.3rem', lineHeight: 1, borderRadius: '4px', flexShrink: 0 }}
          >×</button>
        </div>
      )}

      <div style={{ display: 'grid', gridTemplateColumns: 'minmax(280px, 360px) 1fr', gap: '1rem', alignItems: 'start' }}>
        {/* Left: the area map — one widget: header with the filter and sort
            that used to live in the list below, then the treemap. The list was
            the same data in a second shape, so it is gone (ТЗ §4). */}
        <div>
          <div style={{ border: '1px solid var(--tm-border)', borderRadius: 'var(--tm-radius-md)', padding: '0.75rem' }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', marginBottom: '0.25rem' }}>
              <div style={{ fontSize: '0.8rem', fontWeight: 600 }}>{t('explorer.map_title') ?? 'Area map'}</div>
              <button style={{ ...chip(false), marginLeft: 'auto', flexShrink: 0 }} disabled={normalizeBusy} onClick={() => void normalizeAreas()} title={t('explorer.area_normalize_hint') ?? undefined}>
                {normalizeBusy ? '…' : (t('explorer.area_normalize') ?? 'Tidy up')}
              </button>
            </div>
            <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', marginBottom: '0.5rem' }}>
              <TextInput
                id="explorer-areas-filter"
                size="sm" value={areaFilter} labelText="" hideLabel
                placeholder={t('explorer.areas_filter') ?? 'Filter areas…'}
                onChange={(e) => setAreaFilter(e.target.value)}
                style={{ flex: 1 }}
              />
              <button style={{ ...chip(areaSort === 'size'), flexShrink: 0 }} onClick={() => setAreaSort('size')}>{t('explorer.sort_size') ?? 'Size'}</button>
              <button style={{ ...chip(areaSort === 'name'), flexShrink: 0 }} onClick={() => setAreaSort('name')}>{t('explorer.sort_name') ?? 'A–Z'}</button>
            </div>
            {normalizeNotice && <div style={{ fontSize: '0.72rem', color: 'var(--tm-text-3)', marginBottom: '0.4rem' }}>{normalizeNotice}</div>}
            {areas.length > 0 && visibleAreas.length === 0 ? (
              <div style={{ fontSize: '0.75rem', color: 'var(--tm-muted)', padding: '0.3rem 0' }}>{t('explorer.areas_no_match') ?? 'No areas match the filter.'}</div>
            ) : (
              <AreaMap areas={visibleAreas} selectedKey={selectedAreaKey} onSelect={selectArea} />
            )}
          </div>
        </div>

        {/* Right: dossier takes over the column; otherwise the view decides
            between contradiction discovery and the search results (ТЗ §3.2). */}
        <div>
          {dossierID ? (
            <Dossier
              project={project}
              knowledgeID={dossierID}
              revision={revision}
              onBack={closeDossier}
              onOpenDossier={openDossier}
              onQuestion={startQuestion}
              onRefresh={() => setRevision((r) => r + 1)}
            />
          ) : view === 'conflicts' ? (
            <ConflictPanel project={project} revision={revision} onChanged={() => setRevision((r) => r + 1)} onOpenDossier={openDossier} />
          ) : view === 'agent' ? (
            <AgentResearch project={project} onOpenDossier={openDossier} />
          ) : (
          <>
          <HintsPreview project={project} />
          <div className="experience-toolbar">
            {facetSelect(t('explorer.facet_state') ?? 'State', 'state', stateFacetValues, (value) => stateLabel(value, t))}
            {facetSelect(t('explorer.facet_scope') ?? 'Scope', 'scope', facetValues(facets?.scopes), (value) => scopeLabel(value, t))}
            {facetSelect(t('explorer.facet_kind') ?? 'Kind', 'kind', facetValues(facets?.kinds), (value) => kindLabel(value, t))}
            {facetSelect(t('explorer.facet_origin') ?? 'Origin', 'origin', facetValues(facets?.projects))}
            {facetSelect(t('explorer.facet_topic') ?? 'Topic', 'topic', facetValues(facets?.topics))}
            {facetSelect(t('explorer.facet_entity') ?? 'Entity', 'entity', facetValues(facets?.entities))}
            {activeFilterCount > 0 && (
              <button
                style={{ ...chip(true), marginLeft: 'auto' }}
                title={t('explorer.relax_filters') ?? 'Clear filters'}
                onClick={() => {
                  setFilters({ ...EMPTY_FILTERS })
                  pushNav(t('explorer.relax_filters') ?? 'Clear filters', { filters: { ...EMPTY_FILTERS } })
                }}
              >✕ {activeFilterCount}</button>
            )}
          </div>

          <div style={{ display: 'flex', alignItems: 'baseline', gap: '0.5rem', marginBottom: '0.5rem' }}>
            <div style={{ fontSize: '0.8rem', fontWeight: 600 }}>{t('explorer.results') ?? 'Results'}</div>
            <div style={{ fontSize: '0.7rem', color: 'var(--tm-muted)' }}>{list.length} / {total}</div>
          </div>

          {!hasAnyData && (
            <div style={{ border: '1px dashed var(--tm-border)', borderRadius: 'var(--tm-radius-md)', padding: '1rem', color: 'var(--tm-muted)', fontSize: '0.8rem' }}>
              {t('explorer.no_data') ?? 'No knowledge recorded in this project yet.'}
            </div>
          )}
          {hasAnyData && list.length === 0 && (
            <div style={{ border: '1px dashed var(--tm-border)', borderRadius: 'var(--tm-radius-md)', padding: '1rem', fontSize: '0.8rem' }}>
              <div style={{ marginBottom: '0.35rem' }}>{t('explorer.no_results') ?? 'Nothing matched.'}</div>
              <div style={{ color: 'var(--tm-muted)', marginBottom: '0.6rem', fontSize: '0.75rem' }}>{t('explorer.no_results_hint') ?? ''}</div>
              {activeFilterCount > 0 && (
                <Button size="sm" kind="ghost" onClick={() => { setFilters({ ...EMPTY_FILTERS }); pushNav(t('explorer.relax_filters') ?? 'Clear filters', { filters: { ...EMPTY_FILTERS } }) }}>
                  {t('explorer.relax_filters') ?? 'Clear filters'}
                </Button>
              )}
            </div>
          )}

          {list.map((item) => {
            const annotation = annotations.get(item.id)
            const expanded = expandedID === item.id
            return (
              <div key={item.id} style={{ border: '1px solid var(--tm-border)', borderRadius: 'var(--tm-radius-md)', padding: '0.6rem 0.75rem', marginBottom: '0.5rem' }}>
                <div
                  role="button" tabIndex={0}
                  onClick={() => { setExpandedID(expanded ? '' : item.id) }}
                  onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); setExpandedID(expanded ? '' : item.id) } }}
                  style={{ cursor: 'pointer' }}
                >
                  <div style={{ display: 'flex', alignItems: 'flex-start', gap: '0.5rem' }}>
                    <Tag type={STATE_TAG[item.state] ?? 'gray'} size="sm">{stateLabel(item.state, t)}</Tag>
                    <div style={{ flex: 1, fontSize: '0.82rem', lineHeight: 1.45 }}>{item.proposition}</div>
                    {item.at_risk && <Tag type="red" size="sm">{t('knowledge.at_risk') ?? 'at risk'}</Tag>}
                  </div>
                  <div style={{ display: 'flex', gap: '0.4rem', marginTop: '0.3rem', alignItems: 'center', flexWrap: 'wrap' }}>
                    {item.kind === 'observation' && <Tag size="sm" type="cyan">{kindLabel(item.kind, t)}</Tag>}
                    {(item.topics ?? []).slice(0, 3).map((topic) => <Tag key={topic} size="sm" type="cool-gray">{topic}</Tag>)}
                    {(item.entities ?? []).slice(0, 3).map((entity) => <Tag key={entity} size="sm" type="purple">{entity}</Tag>)}
                    {annotation && (
                      <span style={{ fontSize: '0.68rem', color: 'var(--tm-muted)' }}>
                        {(annotation.matched_by ?? []).slice(0, 4).join(' · ')}
                        {annotation.score !== undefined && annotation.score > 0 ? ` · ${annotation.score.toFixed(2)}` : ''}
                      </span>
                    )}
                  </div>
                </div>

                {expanded && (
                  <div style={{ borderTop: '1px solid var(--tm-border)', marginTop: '0.5rem', paddingTop: '0.5rem' }}>
                    <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(140px, 1fr))', gap: '0.4rem', fontSize: '0.72rem', color: 'var(--tm-text-2)', marginBottom: '0.6rem' }}>
                      <div>{t('knowledge.evidence') ?? 'Evidence'}: {(item.evidence ?? []).length}</div>
                      <div>{t('explorer.offers') ?? 'offers'}: {item.hint_offers} · {t('explorer.uses') ?? 'uses'}: {item.hint_uses} · 👍 {item.helpful_outcomes} / 👎 {item.harmful_outcomes}</div>
                      <div>{t('knowledge.formed_in') ?? 'Created'}: {new Date(item.created_at).toLocaleString()}</div>
                      {item.project && item.project !== project && <div>{t('knowledge.scope_origin') ?? 'from'}: {item.project}</div>}
                    </div>
                    {(item.evidence ?? []).length > 0 && (
                      <div style={{ fontFamily: 'var(--tm-font-mono)', fontSize: '0.68rem', color: 'var(--tm-muted)', marginBottom: '0.6rem', wordBreak: 'break-all' }}>
                        {(item.evidence ?? []).slice(0, 6).map((e) => e.ref).join('  ')}
                      </div>
                    )}
                    <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'center', flexWrap: 'wrap', marginBottom: '0.5rem' }}>
                      <Button size="sm" kind="secondary" onClick={() => openDossier(item.id)}>{t('explorer.dossier_open') ?? 'Open dossier'}</Button>
                    </div>
                    <LifecycleBar project={project} knowledgeID={item.id} onDone={() => setRevision((r) => r + 1)} onOpenDossier={openDossier} />
                  </div>
                )}
              </div>
            )
          })}

          {cursor && (
            <div style={{ display: 'flex', justifyContent: 'center', marginTop: '0.5rem' }}>
              <Button size="sm" kind="ghost" disabled={listLoading} onClick={() => void loadList(true)}>
                {listLoading ? '…' : (t('knowledge.load_more') ?? 'Load more')}
              </Button>
            </div>
          )}
          </>
          )}
        </div>
      </div>
    </div>
  )
}
