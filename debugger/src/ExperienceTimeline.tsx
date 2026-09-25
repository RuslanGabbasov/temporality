import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { API_BASE } from './api'
import { observationApi, type ObservationEvent } from './observationApi'
import { aliveRowAt, foldExperience, forensicOf, LIFECYCLE_KINDS, shortKnowledge, stateBucket, windowAround, type ForensicRecord, type KnowledgeLineage, type KnowledgeRow, type LifecycleKind, type MemoryBucket, type RunInfo } from './experience'

const GUTTER = 210
const AXIS_HEIGHT = 34
const RUN_LANE = 24
const SCOPE_HEADER = 26
const ROW_LANE = 22
const POPULATION_LANE = 64

const MEMORY_BUCKETS: MemoryBucket[] = ['active', 'stale', 'invalidated', 'archived']

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

function message(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }
function shortRun(id: string) {
  const parts = id.split('/')
  const root = parts[0].replace(/^calculator-e2e-\d+-/, '').slice(-8)
  return parts.length > 1 ? `${root}/${parts[parts.length - 1]}` : root
}
function clock(iso: string) {
  const date = new Date(iso)
  return Number.isNaN(date.getTime()) ? '' : date.toTimeString().slice(0, 8)
}
function ms(iso: string) { return new Date(iso).getTime() }

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

export default function ExperienceTimeline() {
  const [initialProject] = useState(() => new URLSearchParams(window.location.search).get('project') ?? 'calculator-e2e')
  const [project, setProject] = useState(initialProject)
  const [events, setEvents] = useState<ObservationEvent[]>([])
  const [loading, setLoading] = useState(false)
  const [progress, setProgress] = useState('')
  const [error, setError] = useState('')
  const [lens, setLens] = useState<Lens>(DEFAULT_LENS)
  const [kinds, setKinds] = useState<Set<LifecycleKind>>(new Set(LIFECYCLE_KINDS))
  const [roleFilter, setRoleFilter] = useState('all')
  const [scopeFilter, setScopeFilter] = useState('all')
  const [bucketFilter, setBucketFilter] = useState<'all' | MemoryBucket>('all')
  const [hiddenRoots, setHiddenRoots] = useState<Set<string>>(new Set())
  const [selected, setSelected] = useState('')
  const [focus, setFocus] = useState<{ at: string; eventId: string; label: string } | null>(null)
  const [window_, setWindow] = useState<{ t0: number; t1: number } | null>(null)
  const [width, setWidth] = useState(1100)

  const shellRef = useRef<HTMLDivElement>(null)
  const svgRef = useRef<SVGSVGElement>(null)
  const dragRef = useRef<{ x: number; t0: number; t1: number } | null>(null)

  useEffect(() => {
    const element = shellRef.current
    if (!element) return
    const observer = new ResizeObserver((entries) => setWidth(Math.max(640, entries[0].contentRect.width)))
    observer.observe(element)
    return () => observer.disconnect()
  }, [])

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
  useEffect(() => { if (!initialised) { setInitialised(true); void load(initialProject) } }, [initialised, initialProject, load])

  const model = useMemo(() => (events.length ? foldExperience(events) : null), [events])

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
    const visibleRun = (run: RunInfo) => !hiddenRoots.has(run.parentRun ?? '') && !hiddenRoots.has(run.id.split('/')[0]) && (roleFilter === 'all' || run.role === roleFilter || !run.role)
    const visibleRuns = model.runs.filter(visibleRun)
    const visibleRow = (row: KnowledgeRow) =>
      (bucketFilter === 'all' || stateBucket(row.state) === bucketFilter) &&
      (scopeFilter === 'all' || row.scopes.primary === scopeFilter) &&
      (roleFilter === 'all' || row.roles.includes(roleFilter)) &&
      row.points.some((point) => kinds.has(point.kind)) &&
      row.runs.some((run) => !hiddenRoots.has(run.split('/')[0]))
    const visibleRows = model.rows.filter(visibleRow)
    const visibleRowIds = new Set(visibleRows.map((row) => row.knowledgeId))
    const visibleScopes = model.scopes
      .map((scope) => ({ scope, rows: scope.rows.filter((row) => visibleRowIds.has(row.knowledgeId)) }))
      .filter((entry) => entry.rows.length > 0)
    const conflictsPresent = model.lineage.length > 0 || model.rows.some((row) => row.points.some((point) => point.kind === 'contradicted' || point.kind === 'weakened' || point.kind === 'archived'))
    const runLaneY = new Map<string, number>()
    let y = AXIS_HEIGHT
    for (const run of visibleRuns) { runLaneY.set(run.id, y + RUN_LANE / 2); y += RUN_LANE }
    const rowY = new Map<string, number>()
    const scopeHeaderY = new Map<string, number>()
    const populationY = y + 10
    if (lens.experience) {
      y = populationY + POPULATION_LANE
      for (const { scope, rows } of visibleScopes) {
        scopeHeaderY.set(scope.id, y + SCOPE_HEADER / 2)
        y += SCOPE_HEADER
        for (const row of rows) { rowY.set(row.knowledgeId, y + ROW_LANE / 2); y += ROW_LANE }
        y += 4
      }
    }
    return { full, active, roots, roles, scopes, visibleRuns, visibleRows, visibleRowIds, visibleScopes, conflictsPresent, runLaneY, rowY, scopeHeaderY, populationY, height: y + 8 }
  }, [model, window_, hiddenRoots, roleFilter, scopeFilter, bucketFilter, kinds, lens.experience])

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
      wheel.preventDefault()
      const rect = element.getBoundingClientRect()
      const x = wheel.clientX - rect.left - GUTTER
      setWindow((current) => {
        const state = current ?? view.full
        const span = state.t1 - state.t0
        const ratio = Math.min(1, Math.max(0, x / Math.max(1, rect.width - GUTTER)))
        const pivot = state.t0 + span * ratio
        const factor = wheel.deltaY > 0 ? 1.25 : 0.8
        let next0 = pivot - (pivot - state.t0) * factor
        let next1 = pivot + (state.t1 - pivot) * factor
        if (next1 - next0 < 5000) return state
        const min = view.full.t0, max = view.full.t1
        if (next0 < min) { next1 += min - next0; next0 = min }
        if (next1 > max) { next0 -= next1 - max; next1 = max }
        return { t0: Math.max(min, next0), t1: Math.min(max, next1) }
      })
    }
    element.addEventListener('wheel', onWheel, { passive: false })
    return () => element.removeEventListener('wheel', onWheel)
  }, [view])

  if (!model || !view) {
    return <div className="observability-shell" ref={shellRef}>
      <Header project={project} setProject={setProject} load={load} loading={loading} />
      {error && <div className="obs-error" role="alert">{error}<small>Проверьте, что Runtime доступен на {API_BASE}.</small></div>}
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
  const activePreset: PresetName | 'custom' = (Object.keys(PRESETS) as PresetName[]).find((name) => {
    const preset = PRESETS[name]
    return (Object.keys(DEFAULT_LENS) as (keyof Lens)[]).every((key) => lens[key] === preset.lens[key]) &&
      kinds.size === preset.kinds.length && [...kinds].every((kind) => preset.kinds.includes(kind))
  }) ?? 'custom'

  const step = tickStep(view.active.t1 - view.active.t0, track)
  const ticks: number[] = []
  for (let t = Math.ceil(view.active.t0 / step) * step; t <= view.active.t1; t += step) ticks.push(t)

  return <div className="observability-shell" ref={shellRef}>
    <Header project={project} setProject={setProject} load={load} loading={loading} />
    {error && <div className="obs-error" role="alert">{error}</div>}
    <div className="experience-meta">
      <span>{model.runs.filter((run) => !run.parentRun).length} team runs · {model.rows.length} experiences · {model.scopes.length} scopes · {model.totals.events} events</span>
      <span className="experience-note">
        active {model.rows.filter((row) => stateBucket(row.state) === 'active').length} · stale {model.rows.filter((row) => stateBucket(row.state) === 'stale').length} · invalidated {model.rows.filter((row) => stateBucket(row.state) === 'invalidated').length} · archived {model.rows.filter((row) => stateBucket(row.state) === 'archived').length} · activations {model.links.length}
      </span>
      {lens.conflicts && !view.conflictsPresent && <span className="experience-note">No contradicted / weakened / archived points in this project yet</span>}
    </div>
    <div className="experience-lens">
      {(Object.keys(DEFAULT_LENS) as (keyof Lens)[]).map((key) => (
        <label key={key} className={`lens-toggle ${lens[key] ? 'on' : ''}`}>
          <input type="checkbox" checked={lens[key]} onChange={(change) => setLens((current) => ({ ...current, [key]: change.target.checked }))} />
          {key}
        </label>
      ))}
      <span className="lens-sep" />
      {(Object.keys(PRESETS) as PresetName[]).map((name) => (
        <button key={name} className={`preset-chip ${activePreset === name ? 'on' : ''}`} onClick={() => { setLens(PRESETS[name].lens); setKinds(new Set(PRESETS[name].kinds)) }}>{name === 'all' ? 'all' : `${name} only`}</button>
      ))}
      <span className="lens-sep" />
      <button onClick={() => { setWindow(null); setFocus(null) }}>fit</button>
      <button onClick={() => setWindow((current) => zoom(current ?? view.full, view.full, 0.6))}>zoom +</button>
      <button onClick={() => setWindow((current) => zoom(current ?? view.full, view.full, 1.6))}>zoom −</button>
      <span className="lens-sep" />
      {LIFECYCLE_KINDS.map((kind) => (
        <button key={kind} className={`kind-chip ${kinds.has(kind) ? 'on' : ''}`} style={{ ['--chip' as string]: LIFECYCLE_COLORS[kind] }} onClick={() => toggleKind(kind)}>{kind}</button>
      ))}
    </div>
    <div className="experience-filters">
      <label>role
        <select value={roleFilter} onChange={(change) => setRoleFilter(change.target.value)}>
          <option value="all">all</option>
          {view.roles.map((role) => <option key={role} value={role}>{role}</option>)}
        </select>
      </label>
      <label>scope
        <select value={scopeFilter} onChange={(change) => setScopeFilter(change.target.value)}>
          <option value="all">all</option>
          {view.scopes.map((scope) => <option key={scope} value={scope}>{scope}</option>)}
        </select>
      </label>
      <label>memory
        <select value={bucketFilter} onChange={(change) => setBucketFilter(change.target.value as 'all' | MemoryBucket)}>
          <option value="all">all</option>
          {MEMORY_BUCKETS.map((bucket) => <option key={bucket} value={bucket}>{bucket}</option>)}
        </select>
      </label>
      <div className="run-chips">{view.roots.map((root) => (
        <button key={root.id} className={`run-chip ${hiddenRoots.has(root.id) ? '' : 'on'}`} onClick={() => toggleRoot(root.id)}>{shortRun(root.id)} · {clock(root.startedAt)}</button>
      ))}</div>
    </div>
    <main className="experience-grid">
      <section className="obs-panel experience-canvas">
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
              <text x={x(tick)} y={14} textAnchor="middle" className="tick-label">{clock(new Date(tick).toISOString())}</text>
            </g>)}
          </g>
          {lens.trajectory && view.visibleRuns.map((run, index) => {
            const yLane = view.runLaneY.get(run.id)!
            const x0 = x(run.startedAt)
            const x1 = x(run.endedAt ?? run.startedAt)
            const acts = activity.get(run.id)
            return <g key={run.id} className={`run-lane ${selected === run.id ? 'selected' : ''}`}>
              <text x={8} y={yLane + 3} className="lane-label run-label">{shortRun(run.id)}{run.status ? ` · ${run.status}` : ''}</text>
              <rect x={GUTTER} y={yLane - RUN_LANE / 2 + 3} width={track} height={RUN_LANE - 6} fill="#0b1016" rx={3} />
              <rect x={Math.max(GUTTER, x0)} width={Math.max(2, Math.min(x1, GUTTER + track) - Math.max(GUTTER, x0))} y={yLane - RUN_LANE / 2 + 3} height={RUN_LANE - 6} fill={roleColor(run.role, index)} opacity={0.28} rx={3} />
              {acts?.tools.map((at, index) => { const px = x(at); return px >= GUTTER && px <= GUTTER + track ? <line key={`t${index}`} x1={px} x2={px} y1={yLane - 6} y2={yLane + 6} stroke={roleColor(run.role, index)} strokeWidth={1} opacity={0.55} /> : null })}
              {acts?.models.map((at, index) => { const px = x(at); return px >= GUTTER && px <= GUTTER + track ? <circle key={`m${index}`} cx={px} cy={yLane} r={1.8} fill="#e8e8e8" opacity={0.7} /> : null })}
            </g>
          })}
          {lens.experience && <g className="population-lane">
            <text x={8} y={view.populationY + 12} className="lane-label population-label">memory population</text>
            <text x={8} y={view.populationY + 24} className="lane-sublabel">alive experiences at each run start</text>
            {view.roots.filter((run) => !hiddenRoots.has(run.id)).map((run) => {
              const alive = aliveRowAt(model.rows, run.startedAt)
              const px = x(run.startedAt)
              if (px < GUTTER || px > GUTTER + track || alive === 0) return null
              const base = view.populationY + POPULATION_LANE - 8
              return <g key={run.id}>
                <rect x={px - 4} y={base - alive * 5} width={8} height={alive * 5} fill="#4fd6be" opacity={0.65} rx={1.5}>
                  <title>{`${shortRun(run.id)} · ${alive} experiences alive at run start`}</title>
                </rect>
                <text x={px} y={base - alive * 5 - 3} textAnchor="middle" className="population-count">{alive}</text>
              </g>
            })}
          </g>}
          {lens.experience && view.visibleScopes.map(({ scope, rows }) => {
            const headerY = view.scopeHeaderY.get(scope.id)!
            const activations = model.links.filter((link) => scope.rows.some((row) => row.knowledgeId === link.knowledgeId)).length
            const retired = scope.rows.filter((row) => row.terminal).length
            return <g key={scope.id} className="scope-section">
              <text x={8} y={headerY + 4} className="lane-label scope-label">{scope.title.toUpperCase()}</text>
              <text x={GUTTER - 8} y={headerY + 4} textAnchor="end" className="lane-sublabel">{rows.length} experiences · {scope.runs.length} runs · {activations} activations{retired ? ` · ${retired} retired` : ''}</text>
              <line x1={GUTTER - 6} x2={width} y1={headerY + SCOPE_HEADER / 2 - 2} y2={headerY + SCOPE_HEADER / 2 - 2} stroke="#1c2530" strokeWidth={1} />
              {rows.map((row) => {
                const yLane = view.rowY.get(row.knowledgeId)!
                const x0 = x(row.firstAt)
                const x1 = x(row.terminal?.at ?? row.lastAt)
                const stateColor = STATE_COLORS[row.state] ?? '#7aa2f7'
                const points = lens.lifecycle ? row.points.filter((point) => kinds.has(point.kind)) : []
                return <g key={row.knowledgeId} className={`row-lane ${selected === row.knowledgeId ? 'selected' : ''}`} onClick={() => setSelected(row.knowledgeId)}>
                  <rect x={0} y={yLane - ROW_LANE / 2} width={width} height={ROW_LANE} fill="transparent" />
                  <text x={16} y={yLane + 3} className="row-label">{shortKnowledge(row.knowledgeId)}</text>
                  <text x={58} y={yLane + 3} className="row-state" style={{ fill: stateColor }}>{row.state}{row.scopes.secondary.length ? ` ·+${row.scopes.secondary.join(',')}` : ''}</text>
                  <rect x={Math.max(GUTTER, x0)} width={Math.max(3, Math.min(x1, GUTTER + track) - Math.max(GUTTER, x0))} y={yLane - 2} height={4} rx={2} fill={stateColor} opacity={0.25 + row.strength * 0.55}>
                    <title>{`${row.knowledgeId} · ${row.state} · strength ${row.strength.toFixed(2)} (derived)${row.terminal ? ` · died ${row.terminal.at}` : ' · alive'}`}</title>
                  </rect>
                  {points.map((point) => {
                    const px = x(point.at)
                    if (px < GUTTER || px > GUTTER + track) return null
                    const hollow = point.kind === 'contradicted' || point.kind === 'weakened'
                    const isFocus = focus?.eventId === point.eventId
                    return <g key={point.eventId}>
                      {isFocus && <circle cx={px} cy={yLane} r={8} className="point-focus-ring" />}
                      <circle cx={px} cy={yLane} r={point.kind === 'appeared' ? 4.5 : 3.4} fill={hollow ? '#0b1016' : LIFECYCLE_COLORS[point.kind]} fillOpacity={point.kind === 'appeared' ? 0.25 : 0.95} stroke={LIFECYCLE_COLORS[point.kind]} strokeWidth={isFocus ? 2.4 : 1.4}>
                        <title>{`${point.kind} · ${point.at} · ${point.run ?? ''}${point.role ? ` (${point.role})` : ''}${point.rule ? ` · ${point.rule}` : ''}`}</title>
                      </circle>
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
              <title>{`${edge.fromId} → ${edge.toId} · ${edge.inferred ? 'inferred' : 'explicit'}${edge.reason ? `\n${edge.reason}` : ''}`}</title>
            </g>
          })}
          {lens.activation && model.links.map((link) => {
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
        <p className="experience-hint">scroll — zoom · drag — pan · row click — details · bars are lifespans, points are events, ✕ marks death</p>
      </section>
      <aside className="obs-panel experience-detail">
        {!selectedRow ? <p className="obs-empty">Выберите опыт на таймлайне — здесь появятся его жизненный цикл, эпизоды, происхождение и связи.</p> : <RowDetails row={selectedRow} lineage={model.lineage} forensic={forensicOf(selectedRow, model)} onFocus={focusOn} related={selectedRow.relatedIds.map((id) => rowOf.get(id)).filter((row): row is KnowledgeRow => Boolean(row))} />}
      </aside>
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

function Header({ project, setProject, load, loading }: { project: string; setProject: (value: string) => void; load: (project: string) => Promise<void> | void; loading: boolean }) {
  return <header className="topbar obs-topbar">
    <div><span className="eyebrow">TEMPORALITY / EXPERIENCE</span><h1>Experience timeline</h1></div>
    <div className="header-actions">
      <span className="connection">Events <code>{API_BASE}</code></span>
      <a className="obs-link" href="/agents">Agent runs</a>
      <a className="obs-link" href="/observability">Knowledge</a>
      <a className="obs-link" href="/">FRP debugger</a>
    </div>
    <form className="experience-project" onSubmit={(event) => { event.preventDefault(); void load(project) }}>
      <input value={project} onChange={(change) => setProject(change.target.value)} placeholder="calculator-e2e" />
      <button className="primary" disabled={loading}>{loading ? 'Loading…' : 'Open timeline'}</button>
    </form>
  </header>
}

function RowDetails({ row, lineage, forensic, onFocus, related }: { row: KnowledgeRow; lineage: KnowledgeLineage[]; forensic: ForensicRecord; onFocus: (at: string, eventId: string, label: string) => void; related: KnowledgeRow[] }) {
  const counts = LIFECYCLE_KINDS.map((kind) => {
    const count = row.points.filter((point) => point.kind === kind).length
    return count ? `${kind} ×${count}` : ''
  }).filter(Boolean).join(' · ') || 'no lifecycle points'
  const supersedes = lineage.filter((edge) => edge.fromId === row.knowledgeId)
  const supersededBy = lineage.filter((edge) => edge.toId === row.knowledgeId)
  const stateColor = STATE_COLORS[row.state] ?? '#7aa2f7'
  return <div className="cluster-details row-details">
    <header>
      <span className="eyebrow">{row.policy ? `EXECUTION · ${row.scopes.primary}` : `CLAIM · ${row.scopes.primary}${row.scopes.secondary.length ? ` +${row.scopes.secondary.join(', ')}` : ''}`}</span>
      <strong>{shortKnowledge(row.knowledgeId)}</strong>
    </header>
    <div className="cluster-stats">
      <span>state <strong style={{ color: stateColor }}>{row.state}</strong></span>
      <span>runs {row.runs.length}</span>
      <span>roles {row.roles.join(', ') || '—'}</span>
      <span>episodes {row.episodes.length}</span>
      <span>{row.terminal ? `died ${row.terminal.at}` : 'alive'}</span>
    </div>
    <div className="strength-meter" title="derived from lifecycle state + reuse telemetry, not a model output">
      <span>strength</span>
      <div className="strength-track"><div style={{ width: `${Math.round(row.strength * 100)}%`, background: stateColor }} /></div>
      <data>{row.strength.toFixed(2)}</data>
    </div>
    <p className="row-proposition">{row.proposition}</p>
    {row.command && <p className="cluster-variants">command: {row.command}</p>}
    <section>
      <h4>Forensics</h4>
      {forensic.formed && <div className="forensic-block">
        <p className="forensic-head clickable" title="locate on timeline" onClick={() => onFocus(forensic.formed!.at, row.points.find((point) => point.kind === 'appeared')?.eventId ?? '', `formed · ${shortKnowledge(row.knowledgeId)}`)}>formed <time>{clock(forensic.formed.at)}</time>{forensic.formed.run ? ` · ${shortRun(forensic.formed.run)}` : ''}{forensic.formed.role ? ` · ${forensic.formed.role}` : ''}{forensic.formed.actor ? ` · by ${forensic.formed.actor}` : ''}</p>
        {forensic.formed.evidence.length > 0 && <ul className="forensic-evidence">
          {forensic.formed.evidence.map((item, index) => <li key={index} title={item.ref}>{item.ref}</li>)}
        </ul>}
        {forensic.formed.commands.length > 0 && <details className="forensic-commands">
          <summary>prior commands · {forensic.formed.commands.length}</summary>
          <ol>{forensic.formed.commands.map((command) => <li key={command.eventId}><code>{command.command}</code></li>)}</ol>
        </details>}
      </div>}
      {forensic.activations.length > 0 && <div className="forensic-block">
        <p className="forensic-head">activations · {forensic.activations.length}</p>
        <ul className="forensic-activations">
          {forensic.activations.map((activation) => <li key={`${activation.offeredAt}-${activation.usedRun ?? 'unused'}`} data-status={activation.usedRunStatus} title="locate on timeline" onClick={() => onFocus(activation.usedAt ?? activation.offeredAt, '', `activation → ${activation.usedRun ?? 'not used'}`)}>
            <span>{activation.usedRun ? shortRun(activation.usedRun) : 'not used'}</span>
            <small>{activation.usedAt ? clock(activation.usedAt) : clock(activation.offeredAt)}{activation.usedRunStatus ? ` · run ${activation.usedRunStatus}` : ''}</small>
            {activation.commands.length > 0 && <details>
              <summary>+{activation.commands.length} commands</summary>
              <ol>{activation.commands.map((command) => <li key={command.eventId}><code>{command.command}</code></li>)}</ol>
            </details>}
          </li>)}
        </ul>
      </div>}
      {forensic.deaths.map((death) => <div className="forensic-block forensic-death" key={death.eventId}>
        <p className="forensic-head clickable" title="locate on timeline" onClick={() => onFocus(death.at, death.eventId, `${death.kind} · ${shortKnowledge(row.knowledgeId)}`)}>{death.kind} <time>{clock(death.at)}</time>{death.actor ? ` · by ${death.actor}` : ''}{death.run ? ` · ${shortRun(death.run)}` : ' · manual'}</p>
        {death.reason && <p className="forensic-reason" title={death.reason}>{death.reason}</p>}
        {death.supersededBy.length > 0 && <p className="forensic-successor">superseded by → {death.supersededBy.map(shortKnowledge).join(', ')}</p>}
      </div>)}
      {!forensic.formed && forensic.activations.length === 0 && forensic.deaths.length === 0 && <p className="obs-empty">Недостаточно событий для реконструкции цепочки.</p>}
    </section>
    <section>
      <h4>Lifecycle</h4>
      <p className="member-counts">{counts}</p>
      <ol className="cluster-points">
        {row.points.map((point) => <li key={point.eventId} data-kind={point.kind} title="locate on timeline" onClick={() => onFocus(point.at, point.eventId, `${point.kind} · ${shortKnowledge(row.knowledgeId)}`)}>
          <span className="point-dot" style={{ background: LIFECYCLE_COLORS[point.kind] }} />
          <time>{point.at}</time>
          <strong>{point.kind}</strong>
          <small>{point.run ?? '—'}{point.role ? ` · ${point.role}` : ''}{point.rule ? ` · ${point.rule}` : ''}</small>
        </li>)}
      </ol>
    </section>
    {row.episodes.length > 0 && <section>
      <h4>Episodes</h4>
      <ul className="cluster-episodes">
        {row.episodes.map((episode, index) => <li key={`${episode.ref}-${index}`}><span className={`episode-kind ${episode.kind}`} />{episode.ref}<small>{episode.at}</small></li>)}
      </ul>
    </section>}
    {(supersedes.length > 0 || supersededBy.length > 0) && <section>
      <h4>Lineage</h4>
      <ul className="lineage-list">
        {supersedes.map((edge) => <li key={`out-${edge.eventId}`}>supersedes <code>{edge.toId}</code>{edge.inferred ? ' · inferred' : ''}{edge.reason ? <small>{edge.reason}</small> : null}</li>)}
        {supersededBy.map((edge) => <li key={`in-${edge.eventId}`}>superseded by <code>{edge.fromId}</code>{edge.inferred ? ' · inferred' : ''}{edge.reason ? <small>{edge.reason}</small> : null}</li>)}
      </ul>
    </section>}
    {related.length > 0 && <section>
      <h4>Related experiences</h4>
      <ul className="related-list">
        {related.map((item) => <li key={item.knowledgeId}><code>{shortKnowledge(item.knowledgeId)}</code><span className={`member-state ${item.state}`}>{item.state}</span><small>{item.scopes.primary}</small></li>)}
      </ul>
    </section>}
  </div>
}
