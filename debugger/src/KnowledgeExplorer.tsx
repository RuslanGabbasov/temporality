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
import ProjectRequired from './ProjectRequired'
import { useT } from './i18n'
import { useOrgUnits, OrgUnitSelect } from './orgUnits'

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
}

function stateLabel(state: string, t: (key: string) => string): string {
  const key = `knowledge.state_${state}`
  const text = t(key)
  return text !== key ? text : state
}

function kindLabel(kind: string | undefined, t: (key: string) => string): string {
  if (kind === 'observation') return t('explorer.kind_observation') ?? 'Observation'
  return t('explorer.kind_claim') ?? 'Claim'
}

export default function KnowledgeExplorer({ project }: { project: string }) {
  const t = useT()
  const org = useOrgUnits()
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
  const [reason, setReason] = useState('')
  const [promoteScope, setPromoteScope] = useState('')
  const [lifecycleBusy, setLifecycleBusy] = useState(false)
  const [lifecycleError, setLifecycleError] = useState('')

  const [areaFilter, setAreaFilter] = useState('')
  const [areaSort, setAreaSort] = useState<'size' | 'name'>('size')

  const [navStack, setNavStack] = useState<NavRecord[]>([])
  const navSeq = useRef(0)
  const listSeq = useRef(0)
  const exploreSeq = useRef(0)

  const debouncedQuestion = useDebounced(question)

  const pushNav = useCallback((label: string, next: { question?: string; filters?: Filters; areaKey?: string }) => {
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
    for (const [key, value] of Object.entries(filters)) {
      if (value) url.set(key, value)
    }
    window.history.replaceState(null, '', `${window.location.pathname}?${url.toString()}`)
  }, [project, question, filters, selectedAreaKey])

  const applyParams = useCallback(() => {
    const params = new URLSearchParams(window.location.search)
    setSearchInput(params.get('q') ?? '')
    setQuestion(params.get('q') ?? '')
    setSelectedAreaKey(params.get('area') ?? '')
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
    if (searchInput.trim()) pushNav(searchInput.trim(), { question: searchInput.trim(), areaKey: '' })
  }, [pushNav, searchInput])

  const restoreNav = useCallback((record: NavRecord) => {
    setQuestion(record.question)
    setSearchInput(record.question)
    setFilters(record.filters)
    setSelectedAreaKey(record.areaKey)
  }, [])

  async function runLifecycle(action: 'challenge' | 'invalidate' | 'promote', item: KnowledgeItem) {
    if (!reason.trim()) return
    setLifecycleBusy(true); setLifecycleError('')
    try {
      const actor = { id: 'human', type: 'human' as const }
      if (action === 'challenge') {
        await observationApi.challenge({ knowledge_id: item.id, project, actor, reason: reason.trim() })
      } else if (action === 'invalidate') {
        await observationApi.invalidate({ knowledge_id: item.id, project, actor, reason: reason.trim() })
      } else {
        await observationApi.promote({
          knowledge_id: item.id, project, actor,
          scope_kind: promoteScope ? 'org_unit' : 'organization',
          scope_id: promoteScope || undefined,
          reason: reason.trim(),
        })
      }
      setReason('')
      setRevision((r) => r + 1)
    } catch (e) {
      setLifecycleError(e instanceof Error ? e.message : 'Request failed')
    } finally {
      setLifecycleBusy(false)
    }
  }

  const visibleAreas = useMemo(() => {
    const needle = areaFilter.trim().toLowerCase()
    const filtered = needle ? areas.filter((a) => a.name.includes(needle)) : areas
    return [...filtered].sort((a, b) =>
      areaSort === 'size'
        ? (b.knowledge_count || 0) - (a.knowledge_count || 0) || a.name.localeCompare(b.name)
        : a.name.localeCompare(b.name))
  }, [areas, areaFilter, areaSort])

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

  const facetRow = (label: string, dimension: keyof Filters, values: [string, number][]) => (
    <div key={dimension} style={{ marginBottom: '0.5rem' }}>
      <div style={{ fontSize: '0.65rem', textTransform: 'uppercase', letterSpacing: '0.05em', color: 'var(--tm-muted)', marginBottom: '0.25rem' }}>{label}</div>
      <div style={{ display: 'flex', flexWrap: 'wrap', gap: '0.3rem' }}>
        {activeFilterCount > 0 && filters[dimension] && (
          <button style={chip(false)} onClick={() => setFilter(dimension, '')}>{t('explorer.all') ?? 'All'}</button>
        )}
        {values.map(([value, count]) => (
          <button key={value} style={chip(filters[dimension] === value)} onClick={() => setFilter(dimension, filters[dimension] === value ? '' : value)}>
            {STATE_ORDER.includes(value) ? stateLabel(value, t) : value}
            <span style={{ opacity: 0.6, marginLeft: '0.3rem' }}>{count}</span>
          </button>
        ))}
      </div>
    </div>
  )

  const facetValues = (map?: Record<string, number>): [string, number][] =>
    Object.entries(map ?? {}).filter(([, count]) => count > 0).sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))

  const stateFacetValues: [string, number][] = STATE_ORDER
    .map((state) => [state, facets?.states?.[state] ?? 0] as [string, number])
    .filter(([, count]) => count > 0)

  return (
    <div style={{ padding: '1rem' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', marginBottom: '0.75rem', flexWrap: 'wrap' }}>
        <h2 style={{ margin: 0, fontSize: '1.1rem' }}>{t('explorer.title') ?? 'Knowledge explorer'}</h2>
        <div style={{ flex: 1, minWidth: '320px', display: 'flex', gap: '0.5rem' }}>
          <TextInput
            id="explorer-search"
            value={searchInput}
            placeholder={t('explorer.search_placeholder') ?? 'Question or keywords…'}
            onChange={(e) => setSearchInput(e.target.value)}
            onKeyDown={(e) => { if (e.key === 'Enter') submitQuestion() }}
            labelText=""
            hideLabel
          />
          <Button size="sm" onClick={submitQuestion}>{t('knowledge.search') ?? 'Search'}</Button>
          <button onClick={() => setRevision((r) => r + 1)} disabled={listLoading} style={{ background: 'none', border: 'none', color: 'var(--tm-text-2)', cursor: 'pointer', fontSize: '1rem', padding: '0.25rem', lineHeight: 1, borderRadius: '4px' }} title="Refresh">↻</button>
        </div>
      </div>

      {exploreError && <InlineNotification kind="warning" title="Explore" subtitle={exploreError} onClose={() => setExploreError('')} lowContrast style={{ marginBottom: '0.75rem' }} />}
      {listError && <InlineNotification kind="error" title="Error" subtitle={listError} onClose={() => setListError('')} lowContrast style={{ marginBottom: '0.75rem' }} />}
      {lifecycleError && <InlineNotification kind="error" title="Error" subtitle={lifecycleError} onClose={() => setLifecycleError('')} lowContrast style={{ marginBottom: '0.75rem' }} />}

      {navStack.length > 1 && (
        <div style={{ display: 'flex', alignItems: 'center', gap: '0.35rem', flexWrap: 'wrap', marginBottom: '0.75rem' }}>
          <span style={{ fontSize: '0.7rem', color: 'var(--tm-muted)' }}>{t('explorer.nav_history') ?? 'Path'}:</span>
          {navStack.slice(-6).map((record) => (
            <button key={record.id} style={chip(false)} onClick={() => restoreNav(record)} title={record.question}>
              {record.label.length > 28 ? `${record.label.slice(0, 27)}…` : record.label}
            </button>
          ))}
        </div>
      )}

      <div style={{ display: 'grid', gridTemplateColumns: 'minmax(280px, 360px) 1fr', gap: '1rem', alignItems: 'start' }}>
        {/* Left: area map + alternative list (ТЗ §4) */}
        <div>
          <div style={{ border: '1px solid var(--tm-border)', borderRadius: 'var(--tm-radius-md)', padding: '0.75rem' }}>
            <div style={{ fontSize: '0.8rem', fontWeight: 600, marginBottom: '0.25rem' }}>{t('explorer.map_title') ?? 'Area map'}</div>
            <div style={{ fontSize: '0.7rem', color: 'var(--tm-muted)', marginBottom: '0.5rem' }}>{t('explorer.map_hint') ?? ''}</div>
            <AreaMap areas={areas} selectedKey={selectedAreaKey} onSelect={selectArea} />
          </div>
          <div style={{ border: '1px solid var(--tm-border)', borderRadius: 'var(--tm-radius-md)', padding: '0.75rem', marginTop: '0.75rem' }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', marginBottom: '0.5rem' }}>
              <div style={{ fontSize: '0.8rem', fontWeight: 600 }}>{t('explorer.areas') ?? 'Areas'}</div>
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
            <div style={{ maxHeight: '320px', overflowY: 'auto' }}>
              {visibleAreas.map((area) => (
                <div
                  key={areaKey(area)}
                  role="button" tabIndex={0}
                  onClick={() => selectArea(area)}
                  onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); selectArea(area) } }}
                  style={{
                    display: 'flex', alignItems: 'center', gap: '0.5rem', padding: '0.3rem 0.4rem', cursor: 'pointer', borderRadius: '4px',
                    background: areaKey(area) === selectedAreaKey ? 'var(--tm-elevated)' : 'transparent',
                  }}
                >
                  <span style={{ fontSize: '0.75rem', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', flex: 1 }}>{area.name}</span>
                  {(area.contradictions ?? 0) > 0 && <span title={t('explorer.contradictions') ?? 'contradictions'} style={{ color: 'var(--tm-danger)', fontSize: '0.7rem' }}>⚠ {area.contradictions}</span>}
                  <span style={{ fontSize: '0.7rem', color: 'var(--tm-muted)' }}>{area.knowledge_count}</span>
                </div>
              ))}
              {visibleAreas.length === 0 && (
                <div style={{ fontSize: '0.75rem', color: 'var(--tm-muted)', padding: '0.3rem 0' }}>{t('explorer.map_empty') ?? 'No areas recorded yet.'}</div>
              )}
            </div>
          </div>
        </div>

        {/* Right: facets + results */}
        <div>
          <div style={{ border: '1px solid var(--tm-border)', borderRadius: 'var(--tm-radius-md)', padding: '0.75rem', marginBottom: '0.75rem' }}>
            {facetRow(t('knowledge.state_label') ?? 'State', 'state', stateFacetValues)}
            {facetRow(t('knowledge.scope_label') ?? 'Scope', 'scope', facetValues(facets?.scopes))}
            {facetRow(t('explorer.facet_topic') ?? 'Topic', 'topic', facetValues(facets?.topics).slice(0, 12))}
            {facetRow(t('explorer.facet_entity') ?? 'Entity', 'entity', facetValues(facets?.entities).slice(0, 12))}
            {facetRow(t('explorer.facet_origin') ?? 'Origin', 'origin', facetValues(facets?.projects))}
            {facetRow(t('explorer.facet_kind') ?? 'Kind', 'kind', facetValues(facets?.kinds))}
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
                  onClick={() => { setExpandedID(expanded ? '' : item.id); if (!expanded) setReason('') }}
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
                    <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'flex-start', flexWrap: 'wrap' }}>
                      <TextInput
                        id="explorer-reason"
                        size="sm" labelText="" hideLabel style={{ flex: 1, minWidth: '220px' }}
                        placeholder={t('runs.knowledge_reason_placeholder') ?? 'Reason'}
                        value={reason} onChange={(e) => setReason(e.target.value)}
                      />
                      <OrgUnitSelect id="explorer-promote-unit" org={org} value={promoteScope} onChange={setPromoteScope} allowUnassigned label={t('knowledge.promote_scope') ?? 'Visible to'} />
                      <Button size="sm" kind="secondary" disabled={!reason.trim() || lifecycleBusy} onClick={() => void runLifecycle('challenge', item)}>{t('runs.knowledge_challenge') ?? 'Challenge'}</Button>
                      <Button size="sm" kind="secondary" disabled={!reason.trim() || lifecycleBusy} onClick={() => void runLifecycle('invalidate', item)}>{t('runs.knowledge_invalidate') ?? 'Invalidate'}</Button>
                      <Button size="sm" kind="primary" disabled={!reason.trim() || lifecycleBusy} onClick={() => void runLifecycle('promote', item)}>{t('knowledge.promote') ?? 'Promote'}</Button>
                    </div>
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
        </div>
      </div>
    </div>
  )
}
