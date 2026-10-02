import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { ChevronDown } from '@carbon/icons-react'
import { API_BASE } from './api'
import { useT } from './i18n'
import { observationApi, type ObservationEvent } from './observationApi'
import { aliveRowAt, foldExperience, forensicOf, LIFECYCLE_KINDS, shortKnowledge, stateBucket, windowAround, type ForensicRecord, type KnowledgeLineage, type KnowledgeRow, type LifecycleKind, type MemoryBucket, type RunInfo } from './experience'

const GUTTER = 210
const AXIS_HEIGHT = 34
const RUN_LANE = 24
const SCOPE_HEADER = 34
const ROW_LANE = 22
const AGGREGATE_LANE = 26
const POPULATION_LANE = 64
// Semantic zoom: zoomed out beyond this share of the full span, and only once
// the corpus is big enough to need it, multi-row lanes collapse into summary
// bands (manual click always overrides; `fit` returns to the automatic mode).
const AGGREGATE_RATIO = 0.45
const AUTO_AGGREGATE_ROWS = 12
// Episode zoom: when the visible span is below this share of the full span,
// individual tool-call episodes become visible within each knowledge row.
const EPISODE_RATIO = 0.15

const MEMORY_BUCKETS: MemoryBucket[] = ['active', 'stale', 'invalidated', 'archived']

type RecencyFilter = 'all' | 'last-run' | '24h' | '7d' | '30d'
type TerminalFilter = 'all' | 'alive' | 'dead'
const RECENCY_OPTIONS: { value: RecencyFilter; label: string }[] = [
  { value: 'all', label: 'timeline.recency.all' },
  { value: 'last-run', label: 'timeline.recency.last_run' },
  { value: '24h', label: 'timeline.recency.24h' },
  { value: '7d', label: 'timeline.recency.7d' },
  { value: '30d', label: 'timeline.recency.30d' },
]

const LIFECYCLE_COLORS: Record<LifecycleKind, string> = {
  appeared: '#7aa2f7',
  recalled: '#9d7cd8',
  injected: '#bb9af7',
  reused: '#9ece6a',
  validated: '#73daca',
  contradicted: '#f7768e',
  weakened: '#e0af68',
  archived: '#565f89',
}

const STATE_COLORS: Record<string, string> = {
  proposed: '#7aa2f7',
  confirmed: '#73daca',
  challenged: '#f7768e',
  corrected: '#e0af68',
  superseded: '#565f89',
  invalidated: '#565f89',
}

const ROLE_COLORS = ['#73daca', '#7aa2f7', '#e0af68', '#f7768e', '#bb9af7', '#9ece6a', '#ff9e64', '#4fd6be']

interface Lens {
  trajectory: boolean
  experience: boolean
  lifecycle: boolean
  conflicts: boolean
  activation: boolean
}

const DEFAULT_LENS: Lens = { trajectory: true, experience: true, lifecycle: true, conflicts: false, activation: false }

// §12 memory-lens presets: quick ways to see only the activation chains or
// only the conflicts, on top of the same folded event space.
const ACTIVATION_KINDS: LifecycleKind[] = ['recalled', 'injected', 'reused']
const CONFLICT_KINDS: LifecycleKind[] = ['contradicted', 'weakened', 'archived']
const PRESETS = {
  all: { lens: DEFAULT_LENS, kinds: LIFECYCLE_KINDS },
  activation: { lens: { trajectory: true, experience: true, lifecycle: true, conflicts: false, activation: true }, kinds: ACTIVATION_KINDS },
  conflicts: { lens: { trajectory: false, experience: true, lifecycle: true, conflicts: true, activation: false }, kinds: CONFLICT_KINDS },
} as const
type PresetName = keyof typeof PRESETS

type MemoryLensPreset = { label: string; bucket: 'all' | MemoryBucket; terminal: TerminalFilter; recency: RecencyFilter; activated: boolean; xscope: boolean; strength: number }
const MEMORY_LENS_PRESETS: MemoryLensPreset[] = [
  { label: 'timeline.all', bucket: 'all', terminal: 'all', recency: 'all', activated: false, xscope: false, strength: 0 },
  { label: 'timeline.active_only', bucket: 'active', terminal: 'alive', recency: 'all', activated: false, xscope: false, strength: 0 },
  { label: 'timeline.activated', bucket: 'all', terminal: 'all', recency: 'all', activated: true, xscope: false, strength: 0 },
  { label: 'timeline.cross_scope', bucket: 'all', terminal: 'all', recency: 'all', activated: false, xscope: true, strength: 0 },
  { label: 'timeline.strong_recent', bucket: 'active', terminal: 'alive', recency: '7d', activated: false, xscope: false, strength: 0.5 },
  { label: 'timeline.dead', bucket: 'all', terminal: 'dead', recency: 'all', activated: false, xscope: false, strength: 0 },
]

function message(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }
function shortRun(id: string) {
  // ids look like `forge-20260925-13` (optionally `/role/agent`): the project
  // prefix and the date are constant within a corpus, so `forge 13` scans
  // better than the date tail.
  const parts = id.split('/')
  const root = parts[0].replace(/^([a-z][a-z0-9]*)-\d{8}-/i, '$1 ')
  return parts.length > 1 ? `${root}/${parts[parts.length - 1]}` : root
}
function clock(iso: string) {
  const date = new Date(iso)
  return Number.isNaN(date.getTime()) ? '' : date.toTimeString().slice(0, 8)
}

/** Format tick label adaptively based on visible time span. */
function formatTick(ms: number, span: number): string {
  const d = new Date(ms)
  if (Number.isNaN(d.getTime())) return ''
  if (span <= 3 * 3600e3) return d.toTimeString().slice(0, 8)           // HH:MM:SS
  if (span <= 86400e3) return d.toTimeString().slice(0, 5)               // HH:MM
  const day = d.toLocaleDateString('en', { month: 'short', day: 'numeric' })
  const hm = d.toTimeString().slice(0, 5)
  if (span <= 7 * 86400e3) return `${day} ${hm}`                          // Mon DD HH:MM
  return day                                                                 // Mon DD
}
function ms(iso: string) { return new Date(iso).getTime() }

/** Gutter label budget is ~180px of .66rem mono ≈ 30 chars: propositions,
 * not opaque ids, are what the reader scans lanes by. */
function claimLabel(proposition: string): string {
  const flat = proposition.replace(/\s+/g, ' ').trim()
  return flat.length > 30 ? `${flat.slice(0, 29)}…` : flat
}


function roleColor(role: string | undefined, index: number) {
  if (!role) return '#565f89'
  const palette: Record<string, string> = { lead: '#7aa2f7', coder: '#73daca', reviewer: '#e0af68', qa: '#bb9af7' }
  return palette[role] ?? ROLE_COLORS[index % ROLE_COLORS.length]
}

/** Adaptive tick step so labels never collide. */
function tickStep(span: number, width: number): number {
  const steps = [10e3, 30e3, 60e3, 120e3, 300e3, 600e3, 900e3, 1800e3, 3600e3, 3 * 3600e3, 6 * 3600e3, 12 * 3600e3, 86400e3]
  for (const step of steps) if ((step / span) * width >= 90) return step
  return 86400e3
}

export default function ExperienceTimeline({ project }: { project: string }) {
  const t = useT()
  const params = new URLSearchParams(window.location.search)
  const [events, setEvents] = useState<ObservationEvent[]>([])
  const [loading, setLoading] = useState(false)
  const [progress, setProgress] = useState('')
  const [error, setError] = useState('')
  const parseLens = (s: string | null): Lens => { if (!s) return DEFAULT_LENS; try { return { ...DEFAULT_LENS, ...JSON.parse(s) } } catch { return DEFAULT_LENS } }
  const [lens, setLens] = useState<Lens>(() => parseLens(params.get('lens')))
  const parseKinds = (s: string | null): Set<LifecycleKind> => { if (!s) return new Set(LIFECYCLE_KINDS); const arr = s.split(',').filter((k): k is LifecycleKind => LIFECYCLE_KINDS.includes(k as LifecycleKind)); return arr.length ? new Set(arr) : new Set(LIFECYCLE_KINDS) }
  const [kinds, setKinds] = useState<Set<LifecycleKind>>(() => parseKinds(params.get('kinds')))
  const [roleFilter, setRoleFilter] = useState(params.get('role') ?? 'all')
  const [scopeFilter, setScopeFilter] = useState(params.get('scope') ?? 'all')
  const [bucketFilter, setBucketFilter] = useState<'all' | MemoryBucket>(() => { const v = params.get('bucket'); return (['active','stale','invalidated','archived'] as const).includes(v as MemoryBucket) ? v as MemoryBucket : 'all' })
  const [strengthMin, setStrengthMin] = useState(() => { const v = parseFloat(params.get('str') ?? ''); return Number.isFinite(v) && v >= 0 && v <= 1 ? v : 0 })
  const [recencyFilter, setRecencyFilter] = useState<RecencyFilter>(() => { const v = params.get('recency'); return (['all','last-run','24h','7d','30d'] as const).includes(v as RecencyFilter) ? v as RecencyFilter : 'all' })
  const [hasActivations, setHasActivations] = useState(params.get('activated') === '1')
  const [crossScopeOnly, setCrossScopeOnly] = useState(params.get('xscope') === '1')
  const [terminalFilter, setTerminalFilter] = useState<TerminalFilter>(() => { const v = params.get('life'); return (['all','alive','dead'] as const).includes(v as TerminalFilter) ? v as TerminalFilter : 'all' })
  const [hiddenRoots, setHiddenRoots] = useState<Set<string>>(() => { const v = params.get('hidden'); return v ? new Set(v.split(',')) : new Set() })
  // Lane aggregation state: id → aggregated?. Auto execution observations are
  // noise lanes (one per command); their merged lane starts aggregated.
  const [laneOverrides, setLaneOverrides] = useState<Record<string, boolean>>({ execution: true })
  const [query, setQuery] = useState(params.get('q') ?? '')
  // The detail panel is selection-driven: it exists only while a knowledge
  // row is selected (opened by clicking a row, closed by its × button).
  const [selected, setSelected] = useState(params.get('selected') ?? '')
  const [runsOpen, setRunsOpen] = useState(false)
  const [focus, setFocus] = useState<{ at: string; eventId: string; label: string } | null>(null)
  const parseWindow = (s: string | null): { t0: number; t1: number } | null => { if (!s) return null; const [a, b] = s.split(',').map(Number); return a && b ? { t0: a, t1: b } : null }
  const [window_, setWindow] = useState<{ t0: number; t1: number } | null>(() => parseWindow(params.get('w')))
  const [width, setWidth] = useState(1100)

  const shellRef = useRef<HTMLDivElement>(null)
  const canvasRef = useRef<HTMLElement>(null)
  const svgRef = useRef<SVGSVGElement>(null)
  const dragRef = useRef<{ x: number; t0: number; t1: number } | null>(null)

  const load = useCallback(async (projectID: string) => {
    if (!projectID.trim()) return
    setLoading(true); setError(''); setEvents([]); setWindow(null); setSelected('')
    try {
      const collected: ObservationEvent[] = []
      let cursor: string | undefined
      let pages = 0
      do {
        const page = await observationApi.events(projectID.trim(), cursor, undefined, undefined, { limit: 500 })
        collected.push(...page.events)
        cursor = page.next_cursor
        pages += 1
        setProgress(`loaded ${collected.length} events${cursor ? '…' : ''}`)
        if (pages > 60) break
      } while (cursor)
      setEvents(collected)
      const query = new URLSearchParams({ project: projectID.trim() })
      window.history.replaceState(null, '', `/experience?${query}`)
    } catch (failure) { setError(message(failure)) }
    finally { setLoading(false); setProgress('') }
  }, [])

  const [initialised, setInitialised] = useState(false)
  useEffect(() => { setInitialised(true); void load(project) }, [project, load])

  // Sync investigation state to URL for shareable links.
  useEffect(() => {
    const urlParams = new URLSearchParams()
    urlParams.set('project', project)
    const lensObj: Record<string, boolean> = {}
    for (const [k, v] of Object.entries(lens)) { if (v !== DEFAULT_LENS[k as keyof Lens]) lensObj[k] = v }
    if (Object.keys(lensObj).length) urlParams.set('lens', JSON.stringify(lensObj))
    const kindArr = [...kinds]
    if (kindArr.length !== LIFECYCLE_KINDS.length || !LIFECYCLE_KINDS.every((k) => kinds.has(k))) urlParams.set('kinds', kindArr.join(','))
    if (roleFilter !== 'all') urlParams.set('role', roleFilter)
    if (scopeFilter !== 'all') urlParams.set('scope', scopeFilter)
    if (bucketFilter !== 'all') urlParams.set('bucket', bucketFilter)
    if (strengthMin > 0) urlParams.set('str', strengthMin.toFixed(2))
    if (recencyFilter !== 'all') urlParams.set('recency', recencyFilter)
    if (hasActivations) urlParams.set('activated', '1')
    if (crossScopeOnly) urlParams.set('xscope', '1')
    if (terminalFilter !== 'all') urlParams.set('life', terminalFilter)
    if (hiddenRoots.size) urlParams.set('hidden', [...hiddenRoots].join(','))
    if (query) urlParams.set('q', query)
    if (selected) urlParams.set('selected', selected)
    if (window_) urlParams.set('w', `${window_.t0},${window_.t1}`)
    window.history.replaceState(null, '', `/experience?${urlParams.toString()}`)
  }, [project, lens, kinds, roleFilter, scopeFilter, bucketFilter, strengthMin, recencyFilter, hasActivations, crossScopeOnly, terminalFilter, hiddenRoots, query, selected, window_])

  const model = useMemo(() => (events.length ? foldExperience(events) : null), [events])

  // Canvas width, not shell width: the svg must fit the column it renders in
  // (and re-measure when the detail panel is hidden).
  useEffect(() => {
    const element = canvasRef.current
    if (!element) return
    const observer = new ResizeObserver((entries) => setWidth(Math.max(640, entries[0].contentRect.width)))
    observer.observe(element)
    return () => observer.disconnect()
  }, [model])

  const view = useMemo(() => {
    if (!model) return null
    const from = ms(model.bounds.from)
    const to = ms(model.bounds.to)
    const pad = Math.max(1, (to - from) * 0.03)
    const full = { t0: from - pad, t1: to + pad }
    const active = window_ ?? full
    const roots = model.runs.filter((run) => !run.parentRun)
    const roleRuns = model.runs.filter((run) => run.role)
    const roles = [...new Set(roleRuns.map((run) => run.role))].sort()
    const scopes = model.scopes.map((scope) => scope.id).sort()
    const needle = query.trim().toLowerCase()
    const lastRunId = model.runs.length ? model.runs[model.runs.length - 1].id : ''
    const now = Date.now()
    const recencyCutoff = recencyFilter === 'all' ? 0 : recencyFilter === 'last-run' ? (model.runs.length ? ms(model.runs[model.runs.length - 1].startedAt) : 0) : recencyFilter === '24h' ? now - 86400e3 : recencyFilter === '7d' ? now - 7 * 86400e3 : now - 30 * 86400e3
    const visibleRun = (run: RunInfo) => !hiddenRoots.has(run.parentRun ?? '') && !hiddenRoots.has(run.id.split('/')[0]) && (roleFilter === 'all' || run.role === roleFilter || !run.role) && (recencyFilter === 'all' || ms(run.startedAt) >= recencyCutoff)
    const visibleRuns = model.runs.filter(visibleRun)
    const visibleRunIds = new Set(visibleRuns.map((r) => r.id))
    const visibleRow = (row: KnowledgeRow) =>
      (bucketFilter === 'all' || stateBucket(row.state) === bucketFilter) &&
      (scopeFilter === 'all' || row.scopes.primary === scopeFilter) &&
      (roleFilter === 'all' || row.roles.includes(roleFilter)) &&
      (!needle || row.proposition.toLowerCase().includes(needle) || row.knowledgeId.toLowerCase().includes(needle)) &&
      row.points.some((point) => kinds.has(point.kind)) &&
      row.runs.some((run) => !hiddenRoots.has(run.split('/')[0])) &&
      row.strength >= strengthMin &&
      (recencyFilter === 'all' || ms(row.lastAt) >= recencyCutoff) &&
      (!hasActivations || row.points.some((pt) => ['recalled', 'injected', 'reused'].includes(pt.kind))) &&
      (!crossScopeOnly || row.scopes.secondary.length > 0) &&
      (terminalFilter === 'all' || (terminalFilter === 'alive' ? !row.terminal : !!row.terminal))
    const visibleRows = model.rows.filter(visibleRow)
    const visibleRowIds = new Set(visibleRows.map((row) => row.knowledgeId))
    // Claim lanes render as before; every execution lane merges into one
    // collapsed-by-default lane so per-command auto rows stop flooding lanes.
    const claimLanes = model.scopes
      .filter((scope) => scope.kind !== 'execution')
      .map((scope) => ({ scope, rows: scope.rows.filter((row) => visibleRowIds.has(row.knowledgeId)) }))
      .filter((entry) => entry.rows.length > 0)
    const executionRows = model.scopes.filter((scope) => scope.kind === 'execution').flatMap((scope) => scope.rows.filter((row) => visibleRowIds.has(row.knowledgeId)))
    const executionLane = executionRows.length ? [{
      scope: {
        id: 'execution', title: 'execution observations', kind: 'execution' as const, rows: executionRows,
        runs: [...new Set(executionRows.flatMap((row) => row.runs))], roles: [...new Set(executionRows.flatMap((row) => row.roles))],
        firstAt: executionRows.reduce((min, row) => row.firstAt < min ? row.firstAt : min, executionRows[0].firstAt),
        lastAt: executionRows.reduce((max, row) => row.lastAt > max ? row.lastAt : max, executionRows[0].lastAt),
      },
      rows: executionRows,
    }] : []
    const visibleScopes = [...claimLanes, ...executionLane]
    // Semantic zoom: at wide spans and scale, multi-row lanes collapse into
    // one summary band each; explicit clicks override, `fit` resets.
    const autoAggregate = (active.t1 - active.t0) / Math.max(1, full.t1 - full.t0) > AGGREGATE_RATIO && visibleRows.length > AUTO_AGGREGATE_ROWS
    const aggregatedIds = new Set<string>()
    for (const { scope, rows } of visibleScopes) {
      if (rows.length < 2 && scope.id !== 'execution') continue
      const override = laneOverrides[scope.id]
      const aggregated = override !== undefined ? override : (scope.id === 'execution' ? true : autoAggregate)
      if (aggregated) aggregatedIds.add(scope.id)
    }
    const conflictsPresent = model.lineage.length > 0 || model.rows.some((row) => row.points.some((point) => point.kind === 'contradicted' || point.kind === 'weakened' || point.kind === 'archived'))
    const showEpisodes = (active.t1 - active.t0) / Math.max(1, full.t1 - full.t0) < EPISODE_RATIO
    const runLaneY = new Map<string, number>()
    let y = AXIS_HEIGHT
    for (const run of visibleRuns) { runLaneY.set(run.id, y + RUN_LANE / 2); y += RUN_LANE }
    const rowY = new Map<string, number>()
    const scopeHeaderY = new Map<string, number>()
    const bandY = new Map<string, number>()
    const populationY = y + 10
    if (lens.experience) {
      y = populationY + POPULATION_LANE
      for (const { scope, rows } of visibleScopes) {
        scopeHeaderY.set(scope.id, y + SCOPE_HEADER / 2)
        y += SCOPE_HEADER
        if (aggregatedIds.has(scope.id)) { bandY.set(scope.id, y + AGGREGATE_LANE / 2); y += AGGREGATE_LANE; continue }
        for (const row of rows) { rowY.set(row.knowledgeId, y + ROW_LANE / 2); y += ROW_LANE }
        y += 4
      }
    }
    const visibleLinks = model.links.filter((link) => {
      const offeredRun = link.offeredRun?.split('/')[0] ?? ''
      const usedRun = link.usedRun?.split('/')[0] ?? ''
      if (hiddenRoots.has(offeredRun) || hiddenRoots.has(usedRun)) return false
      if (recencyFilter !== 'all') {
        const offeredVisible = link.offeredRun ? visibleRunIds.has(link.offeredRun) : false
        const usedVisible = link.usedRun ? visibleRunIds.has(link.usedRun) : false
        if (!offeredVisible && !usedVisible) return false
      }
      return visibleRowIds.has(link.knowledgeId)
    })
    return { full, active, roots, roles, scopes, visibleRuns, visibleRows, visibleRowIds, visibleRunIds, visibleLinks, visibleScopes, aggregatedIds, conflictsPresent, showEpisodes, runLaneY, rowY, scopeHeaderY, bandY, populationY, height: y + 8 }
  }, [model, window_, hiddenRoots, roleFilter, scopeFilter, bucketFilter, strengthMin, recencyFilter, hasActivations, crossScopeOnly, terminalFilter, kinds, lens.experience, laneOverrides, query])

  const activity = useMemo(() => {
    const map = new Map<string, { tools: number[]; models: number[] }>()
    for (const event of events) {
      const run = event.context?.run
      if (!run) continue
      if (event.type !== 'tool.started' && event.type !== 'model.started') continue
      const entry = map.get(run) ?? { tools: [], models: [] }
      const at = ms(event.occurred_at)
      if (event.type === 'tool.started') entry.tools.push(at)
      else entry.models.push(at)
      map.set(run, entry)
    }
    return map
  }, [events])

  // Native wheel listener: React attaches wheel as passive, so zoom needs preventDefault.
  useEffect(() => {
    const element = svgRef.current
    if (!element || !view) return
    const onWheel = (wheel: WheelEvent) => {
      // The left label column is a panel, not timeline canvas: wheel there must
      // keep native page scrolling instead of zooming.
      const rect = element.getBoundingClientRect()
      if (wheel.clientX - rect.left < GUTTER) return

      // Detect pinch-to-zoom: ctrl+wheel (desktop trackpad) or very large deltaY
      // with no deltaX (touchscreen pinch).
      const isPinch = wheel.ctrlKey || (Math.abs(wheel.deltaY) > 30 && wheel.deltaX === 0 && !Number.isInteger(wheel.deltaY))

      // Horizontal pan: only horizontal movement, not a pinch
      if (!isPinch && Math.abs(wheel.deltaX) > Math.abs(wheel.deltaY) && Math.abs(wheel.deltaX) > 2) {
        wheel.preventDefault()
        setWindow((current) => {
          const state = current ?? view.full
          const msPerPx = (state.t1 - state.t0) / Math.max(1, rect.width - GUTTER)
          const bounded = Math.min(view.full.t1 - state.t1, Math.max(view.full.t0 - state.t0, wheel.deltaX * msPerPx))
          return { t0: state.t0 + bounded, t1: state.t1 + bounded }
        })
        return
      }

      // Vertical scroll (not pinch, not horizontal): let the page scroll
      if (!isPinch && Math.abs(wheel.deltaY) > Math.abs(wheel.deltaX)) {
        return // don't preventDefault — let the page scroll normally
      }

      // Pinch zoom
      if (!isPinch) return
      wheel.preventDefault()
      const x = wheel.clientX - rect.left - GUTTER
      setWindow((current) => {
        const state = current ?? view.full
        const span = state.t1 - state.t0
        const ratio = Math.min(1, Math.max(0, x / Math.max(1, rect.width - GUTTER)))
        const pivot = state.t0 + span * ratio
        // Smooth exponential scale for pinch
        const factor = Math.min(2, Math.max(0.5, Math.exp(wheel.deltaY * 0.005)))
        let next0 = pivot - (pivot - state.t0) * factor
        let next1 = pivot + (state.t1 - pivot) * factor
        if (next1 - next0 < 5000) return state
        const min = view.full.t0, max = view.full.t1
        if (next0 < min) { next1 += min - next0; next0 = min }
        if (next1 > max) { next0 -= next1 - max; next1 = max }
        return { t0: next0, t1: next1 }
      })
    }
    element.addEventListener('wheel', onWheel, { passive: false })
    return () => element.removeEventListener('wheel', onWheel)
  }, [view])

  if (!model || !view) {
    return <div className="observability-shell" ref={shellRef}>
      <Header project={project} load={load} loading={loading} />
      {error && <div className="obs-error" role="alert">{error}<small>Проверьте, что журнал доступен на {API_BASE}.</small></div>}
      {loading && <div className="status" role="status"><span className="spinner" /> Loading experience… {progress}</div>}
      {!loading && !error && <div className="obs-controls"><p className="obs-empty">Введите project ID и нажмите Open timeline.</p></div>}
    </div>
  }

  const track = Math.max(200, width - GUTTER)
  const x = (at: string | number) => {
    const value = typeof at === 'number' ? at : ms(at)
    const span = view.active.t1 - view.active.t0
    return GUTTER + ((value - view.active.t0) / span) * track
  }
  const selectedRow = model.rows.find((row) => row.knowledgeId === selected)
  const rowOf = new Map(model.rows.map((row) => [row.knowledgeId, row]))

  const toggleKind = (kind: LifecycleKind) => setKinds((current) => {
    const next = new Set(current)
    if (next.has(kind)) next.delete(kind)
    else next.add(kind)
    return next.size ? next : new Set(LIFECYCLE_KINDS)
  })
  const toggleRoot = (root: string) => setHiddenRoots((current) => {
    const next = new Set(current)
    if (next.has(root)) next.delete(root)
    else next.add(root)
    return next
  })
  const focusOn = (at: string, eventId: string, label: string) => {
    setFocus({ at, eventId, label })
    setWindow(windowAround(at, model.bounds))
  }
  const resetMemoryLens = () => {
    setBucketFilter('all'); setTerminalFilter('all'); setRecencyFilter('all')
    setHasActivations(false); setCrossScopeOnly(false); setStrengthMin(0)
  }
  const activePreset: PresetName | 'custom' = (Object.keys(PRESETS) as PresetName[]).find((name) => {
    const preset = PRESETS[name]
    return (Object.keys(DEFAULT_LENS) as (keyof Lens)[]).every((key) => lens[key] === preset.lens[key]) &&
      kinds.size === preset.kinds.length && [...kinds].every((kind) => preset.kinds.includes(kind))
  }) ?? 'custom'

  const step = tickStep(view.active.t1 - view.active.t0, track)
  const ticks: number[] = []
  for (let t = Math.ceil(view.active.t0 / step) * step; t <= view.active.t1; t += step) ticks.push(t)

  return <div className="observability-shell" ref={shellRef}>
    <Header project={project} load={load} loading={loading} />
    {error && <div className="obs-error" role="alert">{error}</div>}
    <div className="experience-meta">
      <span>{model.runs.filter((run) => !run.parentRun).length} {t('timeline.team_runs')} · {view.visibleRows.length}{view.visibleRows.length < model.rows.length ? `/${model.rows.length}` : ''} {t('timeline.experiences')} · {model.scopes.length} {t('timeline.scopes')} · {model.totals.events} {t('timeline.events')}</span>
      <span className="experience-note">
        {t('timeline.active')} {model.rows.filter((row) => stateBucket(row.state) === 'active').length} · {t('timeline.stale')} {model.rows.filter((row) => stateBucket(row.state) === 'stale').length} · {t('timeline.invalidated')} {model.rows.filter((row) => stateBucket(row.state) === 'invalidated').length} · {t('timeline.archived')} {model.rows.filter((row) => stateBucket(row.state) === 'archived').length} · {t('timeline.activations')} {view.visibleLinks.length}
      </span>
      {lens.conflicts && !view.conflictsPresent && <span className="experience-note">{t('timeline.no_conflicts')}</span>}
    </div>
    <div className="experience-lens">
      {(Object.keys(DEFAULT_LENS) as (keyof Lens)[]).map((key) => (
        <label key={key} className={`lens-toggle ${lens[key] ? 'on' : ''}`}>
          <input type="checkbox" checked={lens[key]} onChange={(change) => setLens((current) => ({ ...current, [key]: change.target.checked }))} />
          {t(`timeline.${key}`)}
        </label>
      ))}
      <span className="lens-sep" />
      {(Object.keys(PRESETS) as PresetName[]).map((name) => (
        <button key={name} className={`preset-chip ${activePreset === name ? 'on' : ''}`} onClick={() => { setLens(PRESETS[name].lens); setKinds(new Set(PRESETS[name].kinds)) }}>{t(`timeline.preset.${name}`)}</button>
      ))}
      <span className="lens-sep" />
      <button onClick={() => { setWindow(null); setFocus(null); setLaneOverrides({ execution: true }) }}>{t('timeline.fit')}</button>
      <button onClick={() => setWindow((current) => zoom(current ?? view.full, view.full, 0.6))}>{t('timeline.zoom_in')}</button>
      <button onClick={() => setWindow((current) => zoom(current ?? view.full, view.full, 1.6))}>{t('timeline.zoom_out')}</button>
      <span className="lens-sep" />
      {LIFECYCLE_KINDS.map((kind) => (
        <button key={kind} className={`kind-chip ${kinds.has(kind) ? 'on' : ''}`} style={{ ['--chip' as string]: LIFECYCLE_COLORS[kind] }} onClick={() => toggleKind(kind)}>{t(`timeline.kind.${kind}`)}</button>
      ))}
      <span className="lens-sep" />
      {MEMORY_LENS_PRESETS.map((preset) => (
        <button key={preset.label} className="preset-chip" onClick={() => { setBucketFilter(preset.bucket); setTerminalFilter(preset.terminal); setRecencyFilter(preset.recency); setHasActivations(preset.activated); setCrossScopeOnly(preset.xscope); setStrengthMin(preset.strength) }}>{t(preset.label)}</button>
      ))}
      {(bucketFilter !== 'all' || terminalFilter !== 'all' || recencyFilter !== 'all' || hasActivations || crossScopeOnly || strengthMin > 0) && (
        <button className="preset-chip" onClick={resetMemoryLens} style={{ color: '#f7768e', borderColor: '#f7768e' }}>{t('timeline.reset')}</button>
      )}
    </div>
    <div className="experience-filters">
      <label>{t('timeline.filter.role')}
        <select value={roleFilter} onChange={(change) => setRoleFilter(change.target.value)}>
          <option value="all">{t('timeline.terminal.all')}</option>
          {view.roles.map((role) => <option key={role} value={role}>{role}</option>)}
        </select>
      </label>
      <label>{t('timeline.filter.scope')}
        <select value={scopeFilter} onChange={(change) => setScopeFilter(change.target.value)}>
          <option value="all">{t('timeline.terminal.all')}</option>
          {view.scopes.map((scope) => <option key={scope} value={scope}>{scope}</option>)}
        </select>
      </label>
      <label>{t('timeline.filter.memory')}
        <select value={bucketFilter} onChange={(change) => setBucketFilter(change.target.value as 'all' | MemoryBucket)}>
          <option value="all">{t('timeline.terminal.all')}</option>
          {MEMORY_BUCKETS.map((bucket) => <option key={bucket} value={bucket}>{bucket}</option>)}
        </select>
      </label>
      <label>{t('timeline.filter.terminal')}
        <select value={terminalFilter} onChange={(change) => setTerminalFilter(change.target.value as TerminalFilter)}>
          {([['all','timeline.terminal.all'],['alive','timeline.terminal.alive'],['dead','timeline.terminal.dead']] as const).map(([v, l]) => <option key={v} value={v}>{t(l)}</option>)}
        </select>
      </label>
      <label>{t('timeline.filter.recency')}
        <select value={recencyFilter} onChange={(change) => setRecencyFilter(change.target.value as RecencyFilter)}>
          {RECENCY_OPTIONS.map((opt) => <option key={opt.value} value={opt.value}>{t(opt.label)}</option>)}
        </select>
      </label>
      <label>{t('timeline.filter.strength')} ≥ {strengthMin.toFixed(2)}
        <input type="range" min={0} max={1} step={0.05} value={strengthMin} onChange={(change) => setStrengthMin(parseFloat(change.target.value))} style={{ width: '80px', verticalAlign: 'middle' }} />
      </label>
      <label className={`lens-toggle ${hasActivations ? 'on' : ''}`}>
        <input type="checkbox" checked={hasActivations} onChange={(change) => setHasActivations(change.target.checked)} />
        {t('timeline.filter.activated')}
      </label>
      <label className={`lens-toggle ${crossScopeOnly ? 'on' : ''}`}>
        <input type="checkbox" checked={crossScopeOnly} onChange={(change) => setCrossScopeOnly(change.target.checked)} />
        {t('timeline.filter.cross_scope')}
      </label>
      <label>{t('timeline.filter.search')}
        <input type="search" value={query} onChange={(change) => setQuery(change.target.value)} placeholder={t('timeline.claim_text_id')} title={t('timeline.filter_experiences')} />
      </label>
      <div className="run-picker">
        {runsOpen && <div className="run-picker-backdrop" onClick={() => setRunsOpen(false)} />}
        <button className={`run-picker-toggle ${hiddenRoots.size ? 'filtered' : ''}`} onClick={() => setRunsOpen((open) => !open)}>
          {t('timeline.runs_abbr')} {view.roots.filter((root) => !hiddenRoots.has(root.id)).length}/{view.roots.length}
          <ChevronDown size={12} className="chevron-flip" data-open={runsOpen ? 'true' : 'false'} style={{ color: 'currentColor', flexShrink: 0 }} />
        </button>
        {runsOpen && <div className="run-picker-panel">
          <div className="run-picker-actions">
            <button onClick={() => setHiddenRoots(new Set())}>{t('timeline.runs_all')}</button>
            <button onClick={() => setHiddenRoots(new Set(view.roots.map((root) => root.id)))}>{t('timeline.runs_none')}</button>
          </div>
          <ul>
            {view.roots.map((root) => (
              <li key={root.id}>
                <label>
                  <input type="checkbox" checked={!hiddenRoots.has(root.id)} onChange={() => toggleRoot(root.id)} />
                  <span>{shortRun(root.id)}</span>
                  <small>{clock(root.startedAt)}{root.status ? ` · ${root.status}` : ''}</small>
                </label>
              </li>
            ))}
          </ul>
        </div>}
      </div>
    </div>
    <main className={`experience-grid ${selected ? '' : 'no-details'}`}>
      <section className="obs-panel experience-canvas" ref={canvasRef}>
        <svg ref={svgRef} width={width} height={view.height} className="experience-svg"
          onPointerDown={(down) => {
            if (down.button !== 0) return
            const svg = svgRef.current!
            const rect = svg.getBoundingClientRect()
            const drag = { x: down.clientX - rect.left, t0: view.active.t0, t1: view.active.t1 }
            dragRef.current = drag
            // Pan via window listeners instead of setPointerCapture: capture
            // retargets pointerup to the <svg> and the browser then synthesizes
            // the click on the svg, swallowing row selection.
            const onMove = (move: PointerEvent) => {
              const bounds = svg.getBoundingClientRect()
              const span = drag.t1 - drag.t0
              const dt = ((move.clientX - bounds.left - drag.x) / track) * span
              setWindow({ t0: drag.t0 - dt, t1: drag.t1 - dt })
            }
            const onUp = () => {
              dragRef.current = null
              window.removeEventListener('pointermove', onMove)
              window.removeEventListener('pointerup', onUp)
            }
            window.addEventListener('pointermove', onMove)
            window.addEventListener('pointerup', onUp)
          }}
        >
          <g>
            {ticks.map((tick) => <g key={tick}>
              <line x1={x(tick)} x2={x(tick)} y1={20} y2={view.height} stroke="#1c2530" strokeWidth={1} />
              <text x={x(tick)} y={14} textAnchor="middle" className="tick-label">{formatTick(tick, view.active.t1 - view.active.t0)}</text>
            </g>)}
          </g>
          {/* Activation links target run lanes: keep the lanes rendered
             (labels + bars) whenever the arrows need somewhere to land. */}
          {(lens.trajectory || lens.activation) && view.visibleRuns.map((run, index) => {
            const yLane = view.runLaneY.get(run.id)!
            const x0 = x(run.startedAt)
            const x1 = x(run.endedAt ?? run.startedAt)
            const acts = activity.get(run.id)
            return <g key={run.id} className={`run-lane ${selected === run.id ? 'selected' : ''}`}>
              <text x={8} y={yLane + 3} className="lane-label run-label">{shortRun(run.id)}{run.status ? ` · ${run.status}` : ''}</text>
              <rect x={GUTTER} y={yLane - RUN_LANE / 2 + 3} width={track} height={RUN_LANE - 6} fill="var(--tm-ink)" rx={3} />
              <rect x={Math.max(GUTTER, x0)} width={Math.max(2, Math.min(x1, GUTTER + track) - Math.max(GUTTER, x0))} y={yLane - RUN_LANE / 2 + 3} height={RUN_LANE - 6} fill={roleColor(run.role, index)} opacity={0.28} rx={3} />
              {acts?.tools.map((at, index) => { const px = x(at); return px >= GUTTER && px <= GUTTER + track ? <line key={`t${index}`} x1={px} x2={px} y1={yLane - 6} y2={yLane + 6} stroke={roleColor(run.role, index)} strokeWidth={1} opacity={0.55} /> : null })}
              {acts?.models.map((at, index) => { const px = x(at); return px >= GUTTER && px <= GUTTER + track ? <circle key={`m${index}`} cx={px} cy={yLane} r={1.8} fill="#e8e8e8" opacity={0.7} /> : null })}
            </g>
          })}
          {lens.experience && <g className="population-lane">
            <text x={8} y={view.populationY + 12} className="lane-label population-label">{t('timeline.memory_population')}</text>
            <text x={8} y={view.populationY + 24} className="lane-sublabel">{t('timeline.alive_at_run_start')}</text>
            {view.roots.filter((run) => !hiddenRoots.has(run.id)).map((run) => {
              const alive = aliveRowAt(model.rows, run.startedAt)
              const px = x(run.startedAt)
              if (px < GUTTER || px > GUTTER + track || alive === 0) return null
              const base = view.populationY + POPULATION_LANE - 8
              return <g key={run.id}>
                <rect x={px - 4} y={base - alive * 5} width={8} height={alive * 5} fill="#4fd6be" opacity={0.65} rx={1.5}>
                  <title>{`${shortRun(run.id)} · ${alive} ${t('timeline.tooltip_alive_at_start')}`}</title>
                </rect>
                <text x={px} y={base - alive * 5 - 3} textAnchor="middle" className="population-count">{alive}</text>
              </g>
            })}
          </g>}
          {lens.experience && view.visibleScopes.map(({ scope, rows }) => {
            const headerY = view.scopeHeaderY.get(scope.id)!
            const aggregated = view.aggregatedIds.has(scope.id)
            const toggleLane = () => setLaneOverrides((current) => ({ ...current, [scope.id]: !aggregated }))
            const activations = view.visibleLinks.filter((link) => scope.rows.some((row) => row.knowledgeId === link.knowledgeId)).length
            const retired = scope.rows.filter((row) => row.terminal).length
            const alive = rows.length - retired
            return <g key={scope.id} className="scope-section">
              <text x={8} y={headerY - 2} className="lane-label scope-label" onClick={toggleLane} style={{ cursor: 'pointer' }}>
                <title>{aggregated ? t('timeline.expand') : t('timeline.collapse')}</title>
                {aggregated ? '▸' : '▾'} {scope.title.toUpperCase()}
              </text>
              <text x={8} y={headerY + 10} className="lane-sublabel">{rows.length} {t('timeline.exp_abbr')} · {scope.runs.length} {t('timeline.runs_abbr')} · {activations} {t('timeline.act_abbr')}{retired ? ` · ${retired} ${t('timeline.retired')}` : ''}</text>
              <line x1={GUTTER - 6} x2={width} y1={headerY + SCOPE_HEADER / 2 - 2} y2={headerY + SCOPE_HEADER / 2 - 2} stroke="#1c2530" strokeWidth={1} />
              {aggregated ? (() => {
                const bandCenter = view.bandY.get(scope.id)!
                const bx0 = Math.max(GUTTER, x(scope.firstAt))
                const bx1 = Math.min(GUTTER + track, x(scope.lastAt))
                const expand = () => setLaneOverrides((current) => ({ ...current, [scope.id]: false }))
                return <g className="scope-band" onClick={expand} style={{ cursor: 'pointer' }}>
                  <rect x={0} y={bandCenter - AGGREGATE_LANE / 2} width={width} height={AGGREGATE_LANE} fill="transparent" onClick={expand} />
                  <rect x={bx0} y={bandCenter - 4} width={Math.max(3, bx1 - bx0)} height={8} rx={4} fill="#7aa2f7" opacity={0.16}>
                    <title>{`${scope.title}: ${rows.length} ${t('timeline.experiences')} · ${alive} ${t('timeline.alive')} · ${retired} ${t('timeline.retired')} · ${clock(scope.firstAt)} → ${clock(scope.lastAt)}`}</title>
                  </rect>
                  {rows.map((row) => {
                    const px = x(row.firstAt)
                    if (px < GUTTER || px > GUTTER + track) return null
                    return <line key={`born-${row.knowledgeId}`} x1={px} x2={px} y1={bandCenter - 9} y2={bandCenter - 4} stroke={STATE_COLORS[row.state] ?? '#7aa2f7'} strokeWidth={1.8} >
                      <title>{`${row.knowledgeId} ${t('timeline.appeared')} · ${clock(row.firstAt)}`}</title>
                    </line>
                  })}
                  {rows.map((row) => row.terminal && (() => {
                    const px = x(row.terminal.at)
                    if (px < GUTTER || px > GUTTER + track) return null
                    return <g key={`died-${row.knowledgeId}`} stroke={row.state === 'invalidated' ? '#f7768e' : '#565f89'} strokeWidth={1.4}>
                      <line x1={px - 3} y1={bandCenter + 4} x2={px + 3} y2={bandCenter + 9} />
                      <line x1={px - 3} y1={bandCenter + 9} x2={px + 3} y2={bandCenter + 4} />
                      <title>{`${row.knowledgeId} ${row.terminal.kind} · ${clock(row.terminal.at)}`}</title>
                    </g>
                  })())}
                  <text x={Math.min(bx1 + 8, width - 78)} y={bandCenter + 3} className="lane-sublabel">{rows.length} {t('timeline.exp_abbr')} · {alive} {t('timeline.alive')}</text>
                </g>
              })() : rows.map((row) => {
                const yLane = view.rowY.get(row.knowledgeId)!
                const x0 = x(row.firstAt)
                const x1 = x(row.terminal?.at ?? row.lastAt)
                const stateColor = STATE_COLORS[row.state] ?? '#7aa2f7'
                const points = lens.lifecycle ? row.points.filter((point) => kinds.has(point.kind)) : []
                return <g key={row.knowledgeId} className={`row-lane ${selected === row.knowledgeId ? 'selected' : ''}`} onClick={() => setSelected(row.knowledgeId)}>
                  <rect x={0} y={yLane - ROW_LANE / 2} width={width} height={ROW_LANE} fill="transparent" />
                  <circle cx={12} cy={yLane} r={3.4} fill={stateColor}>
                    <title>{`${row.state}${row.scopes.secondary.length ? ` · +${row.scopes.secondary.join(', ')}` : ''}`}</title>
                  </circle>
                  <text x={20} y={yLane + 3} className="row-label"><title>{`${row.knowledgeId} · ${row.state}`}</title>{claimLabel(row.proposition)}</text>
                  <rect x={Math.max(GUTTER, x0)} width={Math.max(3, Math.min(x1, GUTTER + track) - Math.max(GUTTER, x0))} y={yLane - 2} height={4} rx={2} fill={stateColor} opacity={0.25 + row.strength * 0.55}>
                    <title>{`${row.knowledgeId} · ${t('timeline.state.' + row.state) || row.state} · ${t('timeline.strength_abbr')} ${row.strength.toFixed(2)} (${t('timeline.derived')})${row.terminal ? ` · ${t('timeline.died')} ${row.terminal.at}` : ` · ${t('timeline.alive')}`}`}</title>
                  </rect>
                  {points.map((point) => {
                    const px = x(point.at)
                    if (px < GUTTER || px > GUTTER + track) return null
                    const hollow = point.kind === 'contradicted' || point.kind === 'weakened'
                    const isFocus = focus?.eventId === point.eventId
                    return <g key={point.eventId}>
                      {isFocus && <circle cx={px} cy={yLane} r={8} className="point-focus-ring" />}
                      <circle cx={px} cy={yLane} r={point.kind === 'appeared' ? 4.5 : 3.4} fill={hollow ? 'var(--tm-ink)' : LIFECYCLE_COLORS[point.kind]} fillOpacity={point.kind === 'appeared' ? 0.25 : 0.95} stroke={LIFECYCLE_COLORS[point.kind]} strokeWidth={isFocus ? 2.4 : 1.4}>
                        <title>{`${point.kind} · ${point.at} · ${point.run ?? ''}${point.role ? ` (${point.role})` : ''}${point.rule ? ` · ${point.rule}` : ''}`}</title>
                      </circle>
                    </g>
                  })}
                  {/* Episode bars: visible when zoomed in past EPISODE_RATIO */}
                  {view.showEpisodes && row.episodes.map((ep, ei) => {
                    const px = x(ep.at)
                    if (px < GUTTER || px > GUTTER + track) return null
                    const epColor = ep.kind === 'artifact' ? '#9ece6a' : ep.kind === 'validation' ? '#73daca' : ep.kind === 'invalidation' ? '#f7768e' : '#7e8a9c'
                    return <g key={`ep-${ei}`}>
                      <rect x={px - 1.5} y={yLane - ROW_LANE / 2 + 2} width={3} height={ROW_LANE - 4} rx={1} fill={epColor} opacity={0.6}>
                        <title>{`${ep.kind} · ${ep.at} · ${ep.run}${ep.role ? ` (${ep.role})` : ''}`}</title>
                      </rect>
                    </g>
                  })}
                  {row.terminal && (() => {
                    const px = x(row.terminal.at)
                    if (px < GUTTER - 4 || px > GUTTER + track + 4) return null
                    const dead = row.state === 'invalidated'
                    const stroke = dead ? '#f7768e' : '#565f89'
                    return <g className="death-marker" stroke={stroke} strokeWidth={1.6}>
                      <line x1={px - 3.5} y1={yLane - 3.5} x2={px + 3.5} y2={yLane + 3.5} />
                      <line x1={px - 3.5} y1={yLane + 3.5} x2={px + 3.5} y2={yLane - 3.5} />
                      <title>{`${row.terminal.kind} · ${row.terminal.at}`}</title>
                    </g>
                  })()}
                </g>
              })}
            </g>
          })}
          {lens.conflicts && model.lineage.map((edge) => {
            const y1 = view.rowY.get(edge.fromId)
            const y2 = view.rowY.get(edge.toId)
            if (y1 === undefined || y2 === undefined) return null
            const fromRow = rowOf.get(edge.fromId)
            const toRow = rowOf.get(edge.toId)
            const fromAt = fromRow?.terminal?.at ?? fromRow?.points[fromRow.points.length - 1]?.at
            const toAt = toRow?.points.find((point) => point.kind === 'appeared')?.at ?? toRow?.firstAt
            if (!fromAt || !toAt) return null
            const x1 = x(fromAt)
            const x2 = x(toAt)
            if (Math.max(x1, x2) < GUTTER || Math.min(x1, x2) > GUTTER + track) return null
            const bend = Math.max(20, Math.abs(x2 - x1) * 0.35)
            return <g key={`${edge.eventId}-${edge.fromId}`} className="lineage-link">
              <path d={`M ${x1} ${y1} C ${x1 + bend} ${y1}, ${x2 - bend} ${y2}, ${x2} ${y2}`} fill="none" stroke="#ff9e64" strokeWidth={1.2} opacity={0.8} markerEnd="url(#lineage-arrow)" />
              <title>{`${edge.fromId} → ${edge.toId} · ${edge.inferred ? t('timeline.inferred') : t('timeline.explicit')}${edge.reason ? `\n${edge.reason}` : ''}`}</title>
            </g>
          })}
          {lens.activation && view.visibleLinks.map((link) => {
            if (hiddenRoots.has(link.offeredRun?.split('/')[0] ?? '')) return null
            const rowLaneY = view.rowY.get(link.knowledgeId)
            const runY = view.runLaneY.get(link.usedRun ?? '')
            if (rowLaneY === undefined || runY === undefined) return null
            const x1 = x(link.offeredAt)
            const x2 = x(link.usedAt ?? link.offeredAt)
            if (x1 < GUTTER || x1 > GUTTER + track) return null
            const failed = link.usedRunStatus === 'failed'
            return <g key={link.hintId} className={`activation-link ${failed ? 'failed' : ''}`}>
              <line x1={x1} y1={rowLaneY} x2={x2} y2={runY} stroke={failed ? '#f7768e' : '#bb9af7'} strokeWidth={1} strokeDasharray={failed ? '2 4' : '3 3'} opacity={0.8} markerEnd="url(#activation-arrow)" />
              {failed && <g stroke="#f7768e" strokeWidth={1.4}>
                <line x1={x2 - 3} y1={runY - 3} x2={x2 + 3} y2={runY + 3} />
                <line x1={x2 - 3} y1={runY + 3} x2={x2 + 3} y2={runY - 3} />
              </g>}
              <title>{`recall ${link.knowledgeId}\n${link.offeredRun} → ${link.usedRun}${failed ? ` · run ${link.usedRunStatus}` : ''}`}</title>
            </g>
          })}
          {focus && (() => {
            const px = x(focus.at)
            if (px < GUTTER - 12 || px > GUTTER + track + 12) return null
            return <g className="focus-marker">
              <line x1={px} x2={px} y1={18} y2={view.height} />
              <circle cx={px} cy={13} r={3.5} />
              <title>{`${focus.label} · ${focus.at}`}</title>
            </g>
          })()}
          <defs>
            <marker id="activation-arrow" viewBox="0 0 8 8" refX={7} refY={4} markerWidth={6} markerHeight={6} orient="auto">
              <path d="M0,0 L8,4 L0,8 z" fill="#bb9af7" />
            </marker>
            <marker id="lineage-arrow" viewBox="0 0 8 8" refX={7} refY={4} markerWidth={6} markerHeight={6} orient="auto">
              <path d="M0,0 L8,4 L0,8 z" fill="#ff9e64" />
            </marker>
          </defs>
        </svg>
        <p className="experience-hint">{t('timeline.hint')}</p>
      </section>
      {selectedRow && <aside className="obs-panel experience-detail">
        <button className="detail-close" title={t('timeline.close_details')} onClick={() => setSelected('')} aria-label="Close details">×</button>
        <RowDetails row={selectedRow} lineage={model.lineage} forensic={forensicOf(selectedRow, model)} onFocus={focusOn} related={selectedRow.relatedIds.map((id) => rowOf.get(id)).filter((row): row is KnowledgeRow => Boolean(row))} project={project} />
      </aside>}
    </main>
  </div>
}

function zoom(current: { t0: number; t1: number }, full: { t0: number; t1: number }, factor: number) {
  const center = (current.t0 + current.t1) / 2
  let t0 = center - (center - current.t0) * factor
  let t1 = center + (current.t1 - center) * factor
  if (t1 - t0 < 5000) return current
  if (t0 < full.t0) { t1 += full.t0 - t0; t0 = full.t0 }
  if (t1 > full.t1) { t0 -= t1 - full.t1; t1 = full.t1 }
  return { t0: Math.max(full.t0, t0), t1: Math.min(full.t1, t1) }
}

function Header({ project, load, loading }: { project: string; load: (project: string) => Promise<void> | void; loading: boolean }) {
  return null
}

function RowDetails({ row, lineage, forensic, onFocus, related, project }: { row: KnowledgeRow; lineage: KnowledgeLineage[]; forensic: ForensicRecord; onFocus: (at: string, eventId: string, label: string) => void; related: KnowledgeRow[]; project: string }) {
  const t = useT()
  const counts = LIFECYCLE_KINDS.map((kind) => {
    const count = row.points.filter((point) => point.kind === kind).length
    return count ? `${t(`timeline.kind.${kind}`)} ×${count}` : ''
  }).filter(Boolean).join(' · ') || t('timeline.no_lifecycle_points')
  const supersedes = lineage.filter((edge) => edge.fromId === row.knowledgeId)
  const supersededBy = lineage.filter((edge) => edge.toId === row.knowledgeId)
  const stateColor = STATE_COLORS[row.state] ?? '#7aa2f7'
  return <div className="cluster-details row-details">
    <header>
      <span className="eyebrow">{row.policy ? `EXECUTION · ${row.scopes.primary}` : `CLAIM · ${row.scopes.primary}${row.scopes.secondary.length ? ` +${row.scopes.secondary.join(', ')}` : ''}`}</span>
      <strong title={row.knowledgeId}>{shortKnowledge(row.knowledgeId)}</strong>
    </header>
    <div className="cluster-stats">
      <span>{t('timeline.state_label')} <strong style={{ color: stateColor }}>{row.state}</strong></span>
      <span>{t('timeline.runs_label')} {row.runs.length}</span>
      <span>{t('timeline.roles')} {row.roles.join(', ') || '—'}</span>
      <span>{t('timeline.episodes')} {row.episodes.length}</span>
      <span>{row.terminal ? `${t('timeline.died')} ${row.terminal.at}` : t('timeline.alive')}</span>
    </div>
    <div className="strength-meter" title={t('timeline.strength_title')}>
      <span>{t('timeline.strength_label')}</span>
      <div className="strength-track"><div style={{ width: `${Math.round(row.strength * 100)}%`, background: stateColor }} /></div>
      <data>{row.strength.toFixed(2)}</data>
    </div>
    <p className="row-proposition">{row.proposition}</p>
    {row.command && <p className="cluster-variants">{t('timeline.command')} {row.command}</p>}
    <section>
      <h4>{t('timeline.forensics')}</h4>
      {forensic.formed && <div className="forensic-block">
        <p className="forensic-head clickable" title={t('timeline.locate')} onClick={() => onFocus(forensic.formed!.at, row.points.find((point) => point.kind === 'appeared')?.eventId ?? '', `${t('timeline.formed')} · ${shortKnowledge(row.knowledgeId)}`)}>{t('timeline.formed')} <time>{clock(forensic.formed.at)}</time>{forensic.formed.run ? ` · ${shortRun(forensic.formed.run)}` : ''}{forensic.formed.role ? ` · ${forensic.formed.role}` : ''}{forensic.formed.actor ? ` · by ${forensic.formed.actor}` : ''}</p>
        {forensic.formed.evidence.length > 0 && <ul className="forensic-evidence">
          {forensic.formed.evidence.map((item, index) => <li key={index} className="clickable" title={t('timeline.locate')} onClick={() => onFocus(item.at, '', `evidence · ${item.ref}`)}>{item.ref}{item.run ? <small> · {shortRun(item.run)}</small> : null}</li>)}
        </ul>}
        {forensic.formed.commands.length > 0 && <details className="forensic-commands">
          <summary>{t('timeline.prior_commands')} · {forensic.formed.commands.length}</summary>
          <ol>{forensic.formed.commands.map((command) => <li key={command.eventId}><code>{command.command}</code></li>)}</ol>
        </details>}
      </div>}
      {forensic.activations.length > 0 && <div className="forensic-block">
        <p className="forensic-head">{t('timeline.activations_label')} · {forensic.activations.length}</p>
        <ul className="forensic-activations">
          {forensic.activations.map((activation) => <li key={`${activation.offeredAt}-${activation.usedRun ?? 'unused'}`} data-status={activation.usedRunStatus} title={t('timeline.locate')} onClick={() => onFocus(activation.usedAt ?? activation.offeredAt, '', `activation → ${activation.usedRun ?? t('timeline.not_used')}`)}>
            <span>{activation.usedRun ? shortRun(activation.usedRun) : t('timeline.not_used')}</span>
            <small>{activation.usedAt ? clock(activation.usedAt) : clock(activation.offeredAt)}{activation.usedRunStatus ? ` · run ${activation.usedRunStatus}` : ''}</small>
            {activation.commands.length > 0 && <details>
              <summary>+{activation.commands.length} {t('timeline.commands')}</summary>
              <ol>{activation.commands.map((command) => <li key={command.eventId}><code>{command.command}</code></li>)}</ol>
            </details>}
          </li>)}
        </ul>
      </div>}
      {forensic.deaths.map((death) => <div className="forensic-block forensic-death" key={death.eventId}>
        <p className="forensic-head clickable" title={t('timeline.locate')} onClick={() => onFocus(death.at, death.eventId, `${death.kind} · ${shortKnowledge(row.knowledgeId)}`)}>{death.kind} <time>{clock(death.at)}</time>{death.actor ? ` · by ${death.actor}` : ''}{death.run ? ` · ${shortRun(death.run)}` : ` · ${t('timeline.manual')}`}</p>
        {death.reason && <p className="forensic-reason" title={death.reason}>{death.reason}</p>}
        {death.supersededBy.length > 0 && <p className="forensic-successor">{t('timeline.superseded_by')} → {death.supersededBy.map(shortKnowledge).join(', ')}</p>}
      </div>)}
      {!forensic.formed && forensic.activations.length === 0 && forensic.deaths.length === 0 && <p className="obs-empty">{t('timeline.insufficient_events')}</p>}
    </section>
    <section>
      <h4>{t('timeline.lifecycle_heading')}</h4>
      <p className="member-counts">{counts}</p>
      <ol className="cluster-points">
        {row.points.map((point) => <li key={point.eventId} data-kind={point.kind} title={t('timeline.locate')} onClick={() => onFocus(point.at, point.eventId, `${point.kind} · ${shortKnowledge(row.knowledgeId)}`)}>
          <span className="point-dot" style={{ background: LIFECYCLE_COLORS[point.kind] }} />
          <time>{point.at}</time>
          <strong>{point.kind}</strong>
          <small>{point.run ?? '—'}{point.role ? ` · ${point.role}` : ''}{point.rule ? ` · ${point.rule}` : ''}</small>
        </li>)}
      </ol>
    </section>
    {row.episodes.length > 0 && <section>
      <h4>{t('timeline.episodes')}</h4>
      <ul className="cluster-episodes">
        {row.episodes.map((episode, index) => <li key={`${episode.ref}-${index}`} className="clickable" title={t('timeline.locate')} onClick={() => onFocus(episode.at, '', `episode · ${episode.ref}`)}><span className={`episode-kind ${episode.kind}`} />{episode.ref}<small>{episode.run ? `${shortRun(episode.run)} · ` : ''}{episode.at}</small></li>)}
      </ul>
    </section>}
    {(supersedes.length > 0 || supersededBy.length > 0) && <section>
      <h4>{t('timeline.lineage')}</h4>
      <ul className="lineage-list">
        {supersedes.map((edge) => <li key={`out-${edge.eventId}`}>{t('timeline.supersedes')} <code>{edge.toId}</code>{edge.inferred ? ` · ${t('timeline.inferred')}` : ''}{edge.reason ? <small>{edge.reason}</small> : null}</li>)}
        {supersededBy.map((edge) => <li key={`in-${edge.eventId}`}>{t('timeline.superseded_by')} <code>{edge.fromId}</code>{edge.inferred ? ` · ${t('timeline.inferred')}` : ''}{edge.reason ? <small>{edge.reason}</small> : null}</li>)}
      </ul>
    </section>}
    {related.length > 0 && <section>
      <h4>{t('timeline.related_experiences')}</h4>
      <ul className="related-list">
        {related.map((item) => <li key={item.knowledgeId}><code>{shortKnowledge(item.knowledgeId)}</code><span className={`member-state ${item.state}`}>{item.state}</span><small>{item.scopes.primary}</small></li>)}
      </ul>
    </section>}
    <section style={{ marginTop: '0.75rem', paddingTop: '0.5rem', borderTop: '1px solid #202a38' }}>
      <h4>{t('timeline.investigate')}</h4>
      <div style={{ display: 'flex', gap: '0.35rem', flexWrap: 'wrap', marginTop: '0.35rem' }}>
        <a href={`/observability?project=${encodeURIComponent(project)}&selected=${encodeURIComponent(row.knowledgeId)}`} style={{ color: '#57d7e8', fontSize: '0.7rem', textDecoration: 'none', padding: '0.2rem 0.5rem', border: '1px solid #344258', borderRadius: '4px' }}>{t('timeline.knowledge_page')}</a>
        {row.runs[0] && <a href={`/agents?project=${encodeURIComponent(project)}&run=${encodeURIComponent(row.runs[0])}`} style={{ color: '#57d7e8', fontSize: '0.7rem', textDecoration: 'none', padding: '0.2rem 0.5rem', border: '1px solid #344258', borderRadius: '4px' }}>{t('timeline.first_run_trace')}</a>}
        {row.runs.length > 1 && <a href={`/agents?project=${encodeURIComponent(project)}&run=${encodeURIComponent(row.runs[row.runs.length - 1])}`} style={{ color: '#57d7e8', fontSize: '0.7rem', textDecoration: 'none', padding: '0.2rem 0.5rem', border: '1px solid #344258', borderRadius: '4px' }}>{t('timeline.last_run_trace')}</a>}
      </div>
    </section>
  </div>
}
