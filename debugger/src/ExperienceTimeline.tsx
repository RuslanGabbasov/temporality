import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { API_BASE } from './api'
import { observationApi, type ObservationEvent } from './observationApi'
import { foldExperience, LIFECYCLE_KINDS, type ExperienceCluster, type LifecycleKind, type RunInfo } from './experience'

const GUTTER = 210
const AXIS_HEIGHT = 34
const RUN_LANE = 24
const CLUSTER_LANE = 30

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
  const [hiddenRoots, setHiddenRoots] = useState<Set<string>>(new Set())
  const [selected, setSelected] = useState('')
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
    const scopes = [...new Set(model.clusters.map((cluster) => cluster.scope))].sort()
    const visibleRun = (run: RunInfo) => !hiddenRoots.has(run.parentRun ?? '') && !hiddenRoots.has(run.id.split('/')[0]) && (roleFilter === 'all' || run.role === roleFilter || !run.role)
    const visibleRuns = model.runs.filter(visibleRun)
    const visibleClusters = model.clusters.filter((cluster) =>
      (scopeFilter === 'all' || cluster.scope === scopeFilter) &&
      (roleFilter === 'all' || cluster.roles.includes(roleFilter)) &&
      cluster.points.some((point) => kinds.has(point.kind)) &&
      cluster.runs.some((run) => !hiddenRoots.has(run.split('/')[0])))
    const conflictsPresent = model.clusters.some((cluster) => cluster.points.some((point) => point.kind === 'contradicted' || point.kind === 'weakened' || point.kind === 'archived'))
    const runLaneY = new Map<string, number>()
    let y = AXIS_HEIGHT
    for (const run of visibleRuns) { runLaneY.set(run.id, y + RUN_LANE / 2); y += RUN_LANE }
    const clusterLaneY = new Map<string, number>()
    const clusterStart = y + 6
    y = clusterStart
    for (const cluster of visibleClusters) { clusterLaneY.set(cluster.id, y + CLUSTER_LANE / 2); y += CLUSTER_LANE }
    return { full, active, roots, roles, scopes, visibleRuns, visibleClusters, conflictsPresent, runLaneY, clusterLaneY, height: y + 8 }
  }, [model, window_, hiddenRoots, roleFilter, scopeFilter, kinds])

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
  const selectedCluster = model.clusters.find((cluster) => cluster.id === selected)

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

  const step = tickStep(view.active.t1 - view.active.t0, track)
  const ticks: number[] = []
  for (let t = Math.ceil(view.active.t0 / step) * step; t <= view.active.t1; t += step) ticks.push(t)

  return <div className="observability-shell" ref={shellRef}>
    <Header project={project} setProject={setProject} load={load} loading={loading} />
    {error && <div className="obs-error" role="alert">{error}</div>}
    <div className="experience-meta">
      <span>{model.runs.filter((run) => !run.parentRun).length} team runs · {model.runs.length} lanes · {model.clusters.length} experience clusters · {model.totals.knowledge} knowledge items · {model.totals.events} events</span>
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
      <button onClick={() => setWindow(null)}>fit</button>
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
      <div className="run-chips">{view.roots.map((root) => (
        <button key={root.id} className={`run-chip ${hiddenRoots.has(root.id) ? '' : 'on'}`} onClick={() => toggleRoot(root.id)}>{shortRun(root.id)} · {clock(root.startedAt)}</button>
      ))}</div>
    </div>
    <main className="experience-grid">
      <section className="obs-panel experience-canvas">
        <svg ref={svgRef} width={width} height={view.height} className="experience-svg"
          onPointerDown={(down) => {
            if (down.button !== 0) return
            const rect = svgRef.current!.getBoundingClientRect()
            dragRef.current = { x: down.clientX - rect.left, t0: view.active.t0, t1: view.active.t1 }
            svgRef.current!.setPointerCapture(down.pointerId)
          }}
          onPointerMove={(move) => {
            const drag = dragRef.current
            if (!drag) return
            const rect = svgRef.current!.getBoundingClientRect()
            const span = drag.t1 - drag.t0
            const dt = ((move.clientX - rect.left - drag.x) / track) * span
            setWindow({ t0: drag.t0 - dt, t1: drag.t1 - dt })
          }}
          onPointerUp={() => { dragRef.current = null }}
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
          {lens.experience && view.visibleClusters.map((cluster) => {
            const yLane = view.clusterLaneY.get(cluster.id)!
            const x0 = x(cluster.firstAt)
            const x1 = x(cluster.lastAt)
            const stateColor = STATE_COLORS[cluster.state] ?? '#7aa2f7'
            const points = lens.lifecycle ? cluster.points.filter((point) => kinds.has(point.kind)) : []
            return <g key={cluster.id} className={`cluster-lane ${selected === cluster.id ? 'selected' : ''}`} onClick={() => setSelected(cluster.id)}>
              <rect x={0} y={yLane - CLUSTER_LANE / 2} width={width} height={CLUSTER_LANE} fill="transparent" />
              <text x={8} y={yLane - 2} className="lane-label cluster-label">{cluster.title}</text>
              <text x={8} y={yLane + 9} className="lane-sublabel">{cluster.members.length} items · {cluster.runs.length} runs · {cluster.state}</text>
              <rect x={Math.max(GUTTER, x0)} width={Math.max(3, Math.min(x1, GUTTER + track) - Math.max(GUTTER, x0))} y={yLane - 2.5} height={5} rx={2.5} fill={stateColor} opacity={0.25 + cluster.strength * 0.6}>
                <title>{`${cluster.title} · ${cluster.state} · strength ${cluster.strength.toFixed(2)} (derived)`}</title>
              </rect>
              {points.map((point) => {
                const px = x(point.at)
                if (px < GUTTER || px > GUTTER + track) return null
                return <circle key={point.eventId} cx={px} cy={yLane} r={point.kind === 'appeared' ? 5 : 3.6} fill={LIFECYCLE_COLORS[point.kind]} fillOpacity={point.kind === 'appeared' ? 0.25 : 0.95} stroke={LIFECYCLE_COLORS[point.kind]} strokeWidth={1.4}>
                  <title>{`${point.kind} · ${point.at} · ${point.run ?? ''}${point.role ? ` (${point.role})` : ''}${point.rule ? ` · ${point.rule}` : ''}`}</title>
                </circle>
              })}
            </g>
          })}
          {lens.activation && model.links.map((link) => {
            if (hiddenRoots.has(link.offeredRun?.split('/')[0] ?? '')) return null
            const clusterY = view.clusterLaneY.get(link.clusterId)
            const runY = view.runLaneY.get(link.usedRun ?? '')
            if (clusterY === undefined || runY === undefined) return null
            const x1 = x(link.offeredAt)
            const x2 = x(link.usedAt ?? link.offeredAt)
            if (x1 < GUTTER || x1 > GUTTER + track) return null
            return <g key={link.hintId} className="activation-link">
              <line x1={x1} y1={clusterY} x2={x2} y2={runY} stroke="#bb9af7" strokeWidth={1} strokeDasharray="3 3" opacity={0.8} markerEnd="url(#activation-arrow)" />
              <title>{`recall ${link.knowledgeId}\n${link.offeredRun} → ${link.usedRun}`}</title>
            </g>
          })}
          <defs>
            <marker id="activation-arrow" viewBox="0 0 8 8" refX={7} refY={4} markerWidth={6} markerHeight={6} orient="auto">
              <path d="M0,0 L8,4 L0,8 z" fill="#bb9af7" />
            </marker>
          </defs>
        </svg>
        <p className="experience-hint">scroll — zoom · drag — pan · cluster click — details · bars are derived state, points are recorded events</p>
      </section>
      <aside className="obs-panel experience-detail">
        {!selectedCluster ? <p className="obs-empty">Выберите кластер опыта на таймлайне — здесь появятся его жизненный цикл, эпизоды и цепочки активации.</p> : <ClusterDetails cluster={selectedCluster} />}
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

function ClusterDetails({ cluster }: { cluster: ExperienceCluster }) {
  return <div className="cluster-details">
    <header><span className="eyebrow">{cluster.kind === 'execution' ? `EXECUTION · ${cluster.scope}` : `CLAIM · ${cluster.scope}`}</span><strong>{cluster.title}</strong></header>
    <div className="cluster-stats">
      <span>state <strong style={{ color: STATE_COLORS[cluster.state] }}>{cluster.state}</strong></span>
      <span>items {cluster.members.length}</span>
      <span>runs {cluster.runs.length}</span>
      <span>episodes {cluster.episodes.length}</span>
      <span>roles {cluster.roles.join(', ') || '—'}</span>
    </div>
    <div className="strength-meter" title="derived from lifecycle state + reuse telemetry, not a model output">
      <span>strength</span>
      <div className="strength-track"><div style={{ width: `${Math.round(cluster.strength * 100)}%`, background: STATE_COLORS[cluster.state] }} /></div>
      <data>{cluster.strength.toFixed(2)}</data>
    </div>
    {cluster.commands.length > 1 && <p className="cluster-variants">command variants: {cluster.commands.join(' · ')}</p>}
    <section>
      <h4>Lifecycle</h4>
      <ol className="cluster-points">
        {cluster.points.map((point) => <li key={point.eventId} data-kind={point.kind}>
          <span className="point-dot" style={{ background: LIFECYCLE_COLORS[point.kind] }} />
          <time>{point.at}</time>
          <strong>{point.kind}</strong>
          <small>{point.run ?? '—'}{point.role ? ` · ${point.role}` : ''}{point.rule ? ` · ${point.rule}` : ''}</small>
        </li>)}
      </ol>
    </section>
    {cluster.episodes.length > 0 && <section>
      <h4>Episodes</h4>
      <ul className="cluster-episodes">
        {cluster.episodes.map((episode, index) => <li key={`${episode.ref}-${index}`}><span className={`episode-kind ${episode.kind}`} />{episode.ref}<small>{episode.at}</small></li>)}
      </ul>
    </section>}
    <section>
      <h4>Knowledge items</h4>
      <ul className="cluster-members">
        {cluster.members.map((member) => <li key={member.knowledgeId}><code>{member.knowledgeId}</code><span className={`member-state ${member.state}`}>{member.state}</span><p>{member.proposition}</p></li>)}
      </ul>
    </section>
  </div>
}
