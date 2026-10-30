import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Button, InlineNotification, Tag, TextInput } from '@carbon/react'
import { observationApi } from './observationApi'
import { useT } from './i18n'
import { useTheme } from './theme'
import { stateLabel } from './explorer/shared'
import { useSemanticLayoutState } from './useSemanticLayout'
import {
  STATE_COLORS,
  type ClusterTreeNode,
  type LayoutNode,
  type MapItem,
} from './semanticMap'
import { itemTopic, topicColor } from './knowledgeTaxonomy'
import { queryMatchesItem } from './knowledgeQuery'

/** Semantic knowledge map (docs/human-knowledge-explorer.md §12): the whole
 *  visible knowledge base laid out by embedding similarity — proximity reads
 *  as meaning. Rendered on canvas with level-of-detail, the way map UIs do
 *  it (Nomic Atlas, Embedding Projector):
 *
 *   - far:  topic-cluster bubbles with captions («React · 42»), tiny dots;
 *   - mid:  dots in cluster colors, no labels yet;
 *   - near: state colors + a bounded set of item labels (greedy spacing —
 *           never more than fits without overlapping).
 *
 *  At every zoom level the screen shows O(tens) of labels, not hundreds —
 *  the earlier single-threshold version dumped every proposition on screen
 *  at once and turned into unreadable sludge. */

interface MapPoint extends LayoutNode {
  r: number
}

const STATE_TAG: Record<string, 'green' | 'blue' | 'red' | 'warm-gray' | 'cool-gray' | 'purple'> = {
  proposed: 'blue', confirmed: 'green', challenged: 'purple', invalidated: 'red', superseded: 'warm-gray', rejected: 'red', corrected: 'cool-gray',
}

/** Layout world size; the canvas scales it to fit (see viewScale). */
const WORLD_W = 1400
const WORLD_H = 900
/** Zoom tiers (effective on-screen scale): below FAR — broad-region heat
 *  map; FAR..NEAR — sub-cluster bubbles over cluster-colored dots; above
 *  NEAR — state cores and the bounded item labels. NEAR sits deep: points
 *  are the last thing you get, after two tiers of themes. */
const FAR = 0.55
const NEAR = 2.6
/** A label is drawn only if no already-drawn label sits within this box. */
const LABEL_BOX_W = 92
const LABEL_BOX_H = 18
const LABEL_MAX = 70

/** Shape codes the lifecycle, the way the experience timeline does: live
 *  statements are circles, challenged — diamonds, invalidated/superseded —
 *  crosses. Color carries the topic cluster, shape carries the verdict. */
export function stateShape(state: string): 'circle' | 'diamond' | 'cross' | 'square' {
  if (state === 'invalidated' || state === 'disproved' || state === 'superseded') return 'cross'
  if (state === 'challenged') return 'diamond'
  if (state === 'rejected') return 'square'
  return 'circle' // proposed / confirmed / corrected — live
}

export function drawShape(ctx: CanvasRenderingContext2D, shape: 'circle' | 'diamond' | 'cross' | 'square', x: number, y: number, r: number) {
  switch (shape) {
    case 'cross': {
      const arm = r * 0.9
      ctx.beginPath()
      ctx.moveTo(x - arm, y - arm); ctx.lineTo(x + arm, y + arm)
      ctx.moveTo(x + arm, y - arm); ctx.lineTo(x - arm, y + arm)
      ctx.stroke()
      break
    }
    case 'diamond': {
      ctx.beginPath()
      ctx.moveTo(x, y - r * 1.2); ctx.lineTo(x + r * 1.2, y)
      ctx.lineTo(x, y + r * 1.2); ctx.lineTo(x - r * 1.2, y)
      ctx.closePath()
      ctx.fill()
      break
    }
    case 'square': {
      const half = r * 0.85
      ctx.beginPath()
      ctx.rect(x - half, y - half, half * 2, half * 2)
      ctx.fill()
      break
    }
    default:
      ctx.beginPath()
      ctx.arc(x, y, r, 0, Math.PI * 2)
      ctx.fill()
  }
}

/** Corpus cache keyed by project: the map unmounts on every dossier
 *  open/close and a fresh mount would replay the whole batched fetch — a
 *  cache hit paints the full map instantly and spends zero requests. TTL
 *  bounds staleness: past it the next mount streams again. The layout worker
 *  keeps its own cache (useSemanticLayout), so a corpus hit skips the heavy
 *  pipeline too. */
type CorpusEntry = { at: number; items: Array<import('./semanticMap').MapItem>; total: number | null; knowledgeTotal: number | null; states: Record<string, number> | null }
const corpusCache = new Map<string, CorpusEntry>()
const CORPUS_TTL_MS = 5 * 60 * 1000
function cacheKey(project: string): string { return project || '*' }

const easeOutCubic = (t: number) => 1 - Math.pow(1 - t, 3)

export default function KnowledgeMap({ project, onOpenDossier }: {
  project: string
  onOpenDossier: (id: string) => void
}) {
  const t = useT()
  const { resolved: theme } = useTheme()
  const canvasRef = useRef<HTMLCanvasElement>(null)
  const wrapRef = useRef<HTMLDivElement>(null)
  const [items, setItems] = useState<MapItem[] | null>(null)
  const [points, setPoints] = useState<MapPoint[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [query, setQuery] = useState('')
  const [stateFilter, setStateFilter] = useState('')
  const [hover, setHover] = useState<{ point: MapPoint; clientX: number; clientY: number } | null>(null)
  const transformRef = useRef({ x: 0, y: 0, k: 1 })
  const frameRef = useRef(0)
  const edgesRef = useRef<Array<[number, number, number]>>([])
  // Spatial grid for O(1) hover/click picking: bucket centers by 40px world
  // cells, rebuilt with the layout instead of a linear scan per mousemove.
  const gridRef = useRef<Map<string, MapPoint[]>>(new Map())
  const pointsRef = useRef<MapPoint[]>([])
  const level1Ref = useRef<ClusterTreeNode[]>([])
  const level2Ref = useRef<ClusterTreeNode[]>([])
  /** Aggregate opacity per zoom: which pyramid tier is on stage. Kept in a
   *  ref + state mirror so the tier badges re-render without redrawing hooks. */
  const [tier, setTier] = useState<1 | 2 | 3>(1)
  /** Topic legend: the largest regions with their palette colors — the map
   *  paints dots by cluster color, so «what does blue mean» needs an answer
   *  that follows the data. */
  const [legendTopics, setLegendTopics] = useState<Array<{ label: string; color: string; count: number }>>([])
  /** Hover focus: the hovered point and its kNN neighbors stay lit while the
   *  rest of the map dims — reading a point's context beats hunting it in a
   *  crowd. Kept in a ref so the draw pass stays out of React state. */
  const highlightRef = useRef<{ center: number; set: Set<number> } | null>(null)
  // Batch arrivals fade in: id → the frame time the point first appeared at.
  // Kept in a ref — the draw pass reads it every frame while a fade is live.
  const appearedAtRef = useRef<Map<string, number>>(new Map())
  const [batchesLanded, setBatchesLanded] = useState(0)
  // Streaming smoothness: between relayouts the dots MORPH to their new
  // positions (per-batch force recomputes would otherwise jerk the whole
  // plane), the camera TWEENS to each new fit instead of snapping, and the
  // cluster bubbles cross-fade. Manual pan/zoom cancels the camera tween.
  const morphRef = useRef<{ start: number; duration: number; from: Array<{ x: number; y: number } | null> }>({ start: 0, duration: 0, from: [] })
  const camRef = useRef<{ start: number; duration: number; from: { x: number; y: number; k: number }; to: { x: number; y: number; k: number } } | null>(null)
  const treeFadeRef = useRef<{ start: number; fromL1: ClusterTreeNode[]; fromL2: ClusterTreeNode[] } | null>(null)
  // The moment the last pending birth reaches full ink — bounds the trail:
  // past it the draw pass skips warmth computation entirely.
  const maxBirthRef = useRef(0)
  const pointIndexById = useMemo(() => new Map(points.map((p, i) => [p.id, i] as const)), [points])

  // Streaming load: batches of 400 arrive freshest-first and land on the map
  // as they come — the first paint needs one batch, not the whole corpus.
  // genRef invalidates the loop when the project flips or the tab unmounts.
  const [progress, setProgress] = useState<{ loaded: number; total: number | null } | null>(null)
  // Knowledge items seen vs vectors embedded: when knowledge exists but no
  // vectors do, the semantic layer never produced embeddings (model not
  // loaded) and the empty state must say that instead of a bare «empty».
  const [knowledgeTotal, setKnowledgeTotal] = useState<number | null>(null)
  // Stable per-state totals from the server (the full corpus, not the pages
  // landed so far): the filter chips must not «grow» with each batch.
  const [states, setStates] = useState<Record<string, number> | null>(null)
  const [projectErrors, setProjectErrors] = useState<string[]>([])
  const projectErrorsRef = useRef<string[]>([])
  const loadGen = useRef(0)
  const load = useCallback(async (opts?: { ignoreCache?: boolean }) => {
    const gen = ++loadGen.current
    const stale = () => gen !== loadGen.current
    // Stream diagnostics go to the console on purpose: «карта не грузится»
    // reports need the client-side trace to be diagnosable remotely.
    console.info('[knowledge-map] load start, project:', JSON.stringify(project), 'gen:', gen)
    // Fresh in-memory cache → instant paint, no network. Retry passes
    // ignoreCache: a retry that answers from the cache would be no retry.
    if (!opts?.ignoreCache) {
      const cached = corpusCache.get(cacheKey(project))
      if (cached && Date.now() - cached.at < CORPUS_TTL_MS) {
        setItems([...cached.items])
        setKnowledgeTotal(cached.knowledgeTotal)
        setStates(cached.states)
        setLoading(false)
        setProgress(null)
        console.info('[knowledge-map] cache hit:', cached.items.length, 'items, age', Math.round((Date.now() - cached.at) / 1000), 's')
        return
      }
    } else {
      corpusCache.delete(cacheKey(project))
    }
    // The full streaming path — also the fallback whenever the IndexedDB
    // delta path distrusts its cache (count mismatch, load error).
    const streamFull = async () => {
      setLoading(true); setError('')
      setProgress({ loaded: 0, total: null })
      projectErrorsRef.current = []
      try {
        const accumulated: Array<import('./semanticMap').MapItem> = []
        const seen = new Set<string>()
        const BATCH = 100
        let total: number | null = null
        let knowledgeTotal: number | null = null
        let states: Record<string, number> | null = null
        // Guards against a journal older than offset-paging (it would replay
        // the same first page forever): a hard page cap and a stall breaker —
        // two consecutive pages with zero fresh items stop the stream. The cap
        // is sized for 100-item pages (4k items) — the corpus outgrowing that
        // renders progressively but stops streaming rather than spin forever.
        const maxPages = 40
        let stalled = 0
        for (let offset = 0, pageIdx = 0; !stale() && pageIdx < maxPages && (total === null || offset < total); offset += BATCH, pageIdx++) {
          const page = await observationApi.knowledgeEmbeddingMap({ project: project || undefined, limit: BATCH, offset })
          if (stale()) return
          if (typeof page.total === 'number') total = page.total
          if (typeof page.knowledge_total === 'number') knowledgeTotal = page.knowledge_total
          if (page.states) states = page.states
          for (const pe of page.project_errors ?? []) if (!projectErrorsRef.current.includes(pe)) projectErrorsRef.current.push(pe)
          const before = accumulated.length
          const fresh = (page.items ?? []).filter((item) => !seen.has(item.id))
          for (const item of page.items ?? []) seen.add(item.id)
          accumulated.push(...fresh)
          setItems([...accumulated])
          setProgress({ loaded: accumulated.length, total: total ?? accumulated.length })
          setKnowledgeTotal(knowledgeTotal)
          if (states) setStates(states)
          setProjectErrors([])
          console.info('[knowledge-map] batch ok: offset', offset, 'page', (page.items ?? []).length, 'accumulated', accumulated.length, 'total', total)
          if ((page.items ?? []).length === 0) break
          if (accumulated.length === before && ++stalled >= 2) break
        }
        // Cache the corpus: the map unmounts on every dossier open/close and
        // the fetch would otherwise replay on each return — a fresh cache
        // paints the full map instantly and spends zero requests.
        corpusCache.set(cacheKey(project), { at: Date.now(), items: accumulated, total, knowledgeTotal, states })
        // IndexedDB: the cross-session cache. The next cold mount paints from
        // here and only fetches the delta.
        if (accumulated.length) {
          void import('./mapCorpusCache').then((cache) =>
            cache.saveProject(project, accumulated, { states: states ?? {}, knowledgeTotal: knowledgeTotal ?? accumulated.length }))
        }
      } catch (e) {
        // A superseded gen must stay quiet — the newer load owns the UI state.
        if (!stale()) {
          setError(e instanceof Error ? e.message : 'Request failed')
          setItems([])
        }
      } finally {
        if (!stale()) {
          setLoading(false)
          setProgress(null)
          setProjectErrors([...projectErrorsRef.current])
        }
      }
    }
    // ── IndexedDB path: paint the cached corpus instantly, then fetch only
    // what changed (since = cached max updated_at) under the same progress
    // pill. full_total guards correctness: cache + delta must add up, else
    // the full stream takes over (a visibility change removed cached points
    // without any updated_at bump).
    if (!opts?.ignoreCache) {
      try {
        const cache = await import('./mapCorpusCache').then((m) => m.loadProject(project))
        if (stale()) return
        if (cache && cache.items.length > 0 && cache.meta?.maxUpdatedAt) {
          console.info('[knowledge-map] idb hydrate:', cache.items.length, 'items, cached at', new Date(cache.meta.at).toISOString())
          // Cached dots paint instantly and skip the birth trail — the trail
          // belongs to the delta arrivals.
          const nowMs = performance.now()
          for (const item of cache.items) appearedAtRef.current.set(item.id, nowMs)
          setItems(cache.items)
          setKnowledgeTotal(cache.meta.knowledgeTotal)
          if (cache.meta.states && Object.keys(cache.meta.states).length) setStates(cache.meta.states)
          setLoading(false)
          const byId = new Map(cache.items.map((item) => [item.id, item]))
          const deltaBatch = 200
          let deltaTotal: number | null = null
          let fullTotal: number | null = null
          let deltaStates: Record<string, number> | null = null
          let deltaKnowledgeTotal: number | null = null
          let changed = 0
          let vanishedChecked = false
          let vanished = false
          let pageErrors: string[] = []
          for (let offset = 0, pageIdx = 0; !stale() && pageIdx < 100 && (deltaTotal === null || offset < deltaTotal); offset += deltaBatch, pageIdx++) {
            const page = await observationApi.knowledgeEmbeddingMap({ project: project || undefined, limit: deltaBatch, offset, since: cache.meta.maxUpdatedAt })
            if (stale()) return
            if (typeof page.total === 'number') deltaTotal = page.total
            if (typeof page.full_total === 'number') { fullTotal = page.full_total; vanishedChecked = true }
            if (page.states) deltaStates = page.states
            if (typeof page.knowledge_total === 'number') deltaKnowledgeTotal = page.knowledge_total
            for (const pe of page.project_errors ?? []) pageErrors.push(pe)
            const pageItems = page.items ?? []
            changed += pageItems.length
            for (const item of pageItems) byId.set(item.id, item)
            const merged = [...byId.values()]
            setItems(merged)
            setProgress({ loaded: merged.length, total: cache.items.length + (deltaTotal ?? 0) })
            if (deltaStates) setStates(deltaStates)
            if (deltaKnowledgeTotal !== null) setKnowledgeTotal(deltaKnowledgeTotal)
            if (pageItems.length === 0) break
          }
          if (stale()) return
          const merged = [...byId.values()]
          // The delta path owns its own progress cleanup: streamFull's finally
          // never runs here, and a pill left at «X из X» hangs forever.
          if (!stale()) setProgress(null)
          if (vanishedChecked && fullTotal !== null && merged.length !== fullTotal) {
            // Cached points vanished server-side (scope/visibility change):
            // the delta cannot express removals — re-stream the full corpus.
            console.info('[knowledge-map] idb mismatch: merged', merged.length, 'vs full', fullTotal, '— full reload')
            await streamFull()
            return
          }
          // Fresh states/knowledge totals came with the delta; keep the last.
          const finalStates = deltaStates ?? cache.meta.states ?? {}
          const finalKnowledge = deltaKnowledgeTotal ?? cache.meta.knowledgeTotal
          corpusCache.set(cacheKey(project), { at: Date.now(), items: merged, total: merged.length, knowledgeTotal: finalKnowledge, states: finalStates })
          void import('./mapCorpusCache').then((m) => m.saveProject(project, merged, { states: finalStates, knowledgeTotal: finalKnowledge }))
          setProjectErrors([...new Set(pageErrors)])
          console.info('[knowledge-map] idb delta ok:', changed, 'changed, merged', merged.length)
          return
        }
      } catch (e) {
        console.info('[knowledge-map] idb path failed, full stream:', e instanceof Error ? e.message : String(e))
        if (stale()) { /* fall through to streamFull */ } else return
      }
    }
    await streamFull()
  }, [project])

  useEffect(() => { void load(); return () => { loadGen.current++ } }, [load])

  // The heavy pipeline (normalize → kNN → force → cluster pyramid) runs in a
  // worker; this effect just installs the results into the render state.
  const { result: layout, pending: layoutPending } = useSemanticLayoutState(items)
  useEffect(() => {
    if (!layout) return
    // Fresh arrivals light up ONE PER FRAME as a spatial chain: each next dot
    // is the nearest to the previous one, so the batch reads as a trail
    // unrolling through the cluster («шлейф»), not a big blob snapping in at
    // once. The trail's total span is budgeted (5s first mount, 2.5s per
    // batch) — corpora larger than the frame budget just accelerate the
    // per-dot spacing.
    const now = performance.now()
    const prevById = new Map(pointsRef.current.map((p) => [p.id, { x: p.x, y: p.y }]))
    const newborns = layout.nodes.filter((node) => !appearedAtRef.current.has(node.id))
    if (newborns.length > 2) {
      const remaining = [...newborns]
      const chain: typeof newborns = [remaining.shift()!]
      let current = chain[0]
      while (remaining.length) {
        let bestIdx = 0
        let bestDist = Infinity
        for (let i = 0; i < remaining.length; i++) {
          const dx = remaining[i].x - current.x
          const dy = remaining[i].y - current.y
          const d = dx * dx + dy * dy
          if (d < bestDist) { bestDist = d; bestIdx = i }
        }
        current = remaining.splice(bestIdx, 1)[0]
        chain.push(current)
      }
      newborns.length = 0
      newborns.push(...chain)
    }
    const budget = prevById.size ? 2500 : 5000
    const step = newborns.length > 1 ? Math.min(16.7, budget / newborns.length) : 0
    newborns.forEach((node, i) => appearedAtRef.current.set(node.id, now + i * step))
    if (newborns.length) maxBirthRef.current = now + (newborns.length - 1) * step + 750
    // Position morph: existing dots tween from where they were drawn to their
    // new layout positions; brand-new dots have no from — they only fade in.
    morphRef.current = {
      start: now,
      duration: prevById.size ? 700 : 0,
      from: layout.nodes.map((node) => prevById.get(node.id) ?? null),
    }
    // Bubbles have no stable ids across layouts — cross-fade the whole tree.
    if (level1Ref.current.length) {
      treeFadeRef.current = { start: now, fromL1: level1Ref.current, fromL2: level2Ref.current }
    }
    setBatchesLanded((n) => n + 1)
    const radius = Math.max(2.2, Math.min(4, 900 / Math.sqrt(layout.nodes.length + 1)))
    const mapPoints = layout.nodes.map((node) => ({ ...node, r: radius }))
    const grid = new Map<string, MapPoint[]>()
    for (const point of mapPoints) {
      const key = `${Math.floor(point.x / 40)}:${Math.floor(point.y / 40)}`
      const bucket = grid.get(key)
      if (bucket) bucket.push(point)
      else grid.set(key, [point])
    }
    gridRef.current = grid
    pointsRef.current = mapPoints
    setPoints(mapPoints)
    level1Ref.current = layout.level1
    level2Ref.current = layout.level2
    edgesRef.current = layout.edges
    setLegendTopics(
      [...layout.level1]
        .sort((a, b) => b.count - a.count)
        .slice(0, 6)
        .filter((node) => node.label || node.labelLines?.length)
        .map((node) => {
          // The canvas captions a bubble with its TWO most distinctive topics
          // (labelLines); the legend used to show only the dominant one —
          // every «git»-dominated region then read as «git · N» and the
          // legend repeated the same word for genuinely different regions.
          // Join the same lines the canvas draws so entries stay distinct.
          const lines = [...new Set([node.label, ...(node.labelLines ?? [])])].filter(Boolean)
          return { label: lines.join(' · '), color: node.color, count: node.count }
        }),
    )
    // Fit-to-screen: the world layout is drawn at WORLD_W×H; scale it into
    // the actual canvas box with a margin, centered. The camera TWEENS to the
    // new fit — a first mount starts zoomed in and flies out, later batches
    // glide from the previous view — instead of snapping between batches.
    requestAnimationFrame(() => {
      const wrap = wrapRef.current
      if (!wrap) return
      const w = wrap.clientWidth, h = wrap.clientHeight
      const margin = 40
      const fit = Math.min((w - margin) / WORLD_W, (h - margin) / WORLD_H)
      const base = Math.min(w / WORLD_W, h / WORLD_H)
      const target = {
        k: Math.max(0.2, Math.min(8, fit / base)),
        x: (w - WORLD_W * fit) / 2,
        y: (h - WORLD_H * fit) / 2,
      }
      const first = prevById.size === 0
      const from = first
        ? // Zoomed in on the center: the flight out reads as the camera
          // pulling back from the first lit stars to the whole sky.
          { k: target.k * 1.9, x: w / 2 - (w / 2 - target.x) * 1.9, y: h / 2 - (h / 2 - target.y) * 1.9 }
        : { ...transformRef.current }
      camRef.current = { start: performance.now(), duration: first ? 1200 : 700, from, to: target }
      if (first) transformRef.current = from
    })
  }, [layout])

  const matches = useMemo(() => {
    const needle = query.trim()
    return points.map((point) => {
      if (stateFilter && point.item.state !== stateFilter) return false
      if (!needle) return true
      // Word-form tolerant: «прогон» finds «прогона», «run» finds «runs» —
      // plain includes() used to demand the exact substring form.
      return queryMatchesItem(needle, point.item)
    })
  }, [points, query, stateFilter])

  // Recompute the lit neighborhood only when the hovered point changes (the
  // tooltip state also carries cursor coordinates — those must not re-run
  // this at pointer frequency).
  const hoverId = hover?.point.id ?? ''

  /** Nearest point to a world-space query within maxDist, via the spatial
   *  grid: O(bucket) instead of a full scan per mousemove. */
  const pickPoint = useCallback((mx: number, my: number, maxDist: number): MapPoint | null => {
    let best: MapPoint | null = null
    let bestDist = maxDist
    const cell = 40
    const gx = Math.floor(mx / cell), gy = Math.floor(my / cell)
    for (let ox = -1; ox <= 1; ox++) {
      for (let oy = -1; oy <= 1; oy++) {
        const bucket = gridRef.current.get(`${gx + ox}:${gy + oy}`)
        if (!bucket) continue
        for (const point of bucket) {
          const dx = point.x - mx, dy = point.y - my
          const dist = Math.sqrt(dx * dx + dy * dy)
          if (dist < Math.max(point.r + 3, bestDist)) { best = point; bestDist = dist }
        }
      }
    }
    return best
  }, [])

  const viewScale = useCallback(() => {
    const wrap = wrapRef.current
    const w = wrap?.clientWidth ?? WORLD_W
    const h = wrap?.clientHeight ?? WORLD_H
    const base = Math.min(w / WORLD_W, h / WORLD_H)
    const { x, y, k } = transformRef.current
    return { scale: k * base, tx: x, ty: y, w, h }
  }, [])

  /** The point's region shade: its level1 region's color — the same hue its
   *  bubble and legend entry carry. Falls back to the dominant-topic hash for
   *  points the cluster tree has not reached. */
  const regionColorOf = useCallback((p: MapPoint): string =>
    level1Ref.current[p.cluster]?.color
    ?? topicColor(itemTopic(p.item.topics) || p.item.proposition), [])

  const draw = useCallback(() => {
    const canvas = canvasRef.current
    const wrap = wrapRef.current
    if (!canvas || !wrap) return
    const dpr = window.devicePixelRatio || 1
    const w = wrap.clientWidth, h = wrap.clientHeight
    if (canvas.width !== w * dpr || canvas.height !== h * dpr) {
      canvas.width = w * dpr; canvas.height = h * dpr
      canvas.style.width = `${w}px`; canvas.style.height = `${h}px`
    }
    const ctx = canvas.getContext('2d')
    if (!ctx) return
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
    ctx.clearRect(0, 0, w, h)
    const now = performance.now()
    let animating = false
    // Camera tween: write the interpolated transform back so viewScale picks
    // it up; wheel/drag handlers cancel camRef for manual control.
    const cam = camRef.current
    if (cam) {
      const t = Math.min(1, (now - cam.start) / cam.duration)
      const e = easeOutCubic(t)
      transformRef.current = {
        k: cam.from.k + (cam.to.k - cam.from.k) * e,
        x: cam.from.x + (cam.to.x - cam.from.x) * e,
        y: cam.from.y + (cam.to.y - cam.from.y) * e,
      }
      if (t < 1) animating = true
      else camRef.current = null
    }
    const { scale, tx, ty } = viewScale()
    // Dark theme: glows and captions need less fill and dimmer text to read
    // as a heat map; light theme packs stronger fills for the same contrast.
    const dark = theme === 'dark'

    // Position morph: dots glide from their previous layout positions to the
    // new ones instead of teleporting on every batch recompute.
    const morph = morphRef.current
    const morphT = morph.duration ? Math.min(1, (now - morph.start) / morph.duration) : 1
    if (morphT < 1) animating = true
    const morphE = easeOutCubic(morphT)
    const animPos = points.map((p, i) => {
      const from = morph.from[i]
      if (!from) return { x: p.x, y: p.y }
      return { x: from.x + (p.x - from.x) * morphE, y: from.y + (p.y - from.y) * morphE }
    })

    // Project + cull once; everything below works in screen pixels, so fonts
    // stay constant-size and dots grow only mildly with zoom.
    const visible: Array<{ p: MapPoint; sx: number; sy: number; active: boolean; i: number }> = []
    for (let i = 0; i < points.length; i++) {
      const sx = animPos[i].x * scale + tx
      const sy = animPos[i].y * scale + ty
      if (sx < -30 || sx > w + 30 || sy < -30 || sy > h + 30) continue
      visible.push({ p: points[i], sx, sy, active: matches[i], i })
    }
    let stillFading = false
    // Hover focus belongs to the statements layer only: on the topic and
    // sub-cluster tiers the bubbles already aggregate the view, dimming the
    // plane around one dot there reads as flicker, not focus.
    const highlight = scale >= NEAR ? highlightRef.current : null
    const dotR = Math.max(1.6, Math.min(3.2, points[0]?.r ?? 3)) * Math.min(1.5, Math.max(0.8, scale))

    // Tier tracking for the legend badges (state mirror, cheap guard).
    const nextTier: 1 | 2 | 3 = scale < FAR ? 1 : scale < NEAR ? 2 : 3
    setTier((current) => (current === nextTier ? current : nextTier))

    /** Layer model (bottom → top): bubble fills → edges → dots → bubble
     *  captions → item labels. Captions must sit on TOP — they used to be
     *  painted before the dots and vanished under them. */
    const drawBubbleFill = (node: ClusterTreeNode, alpha: number) => {
      const cx = node.cx * scale + tx
      const cy = node.cy * scale + ty
      const r = node.radius * scale
      if (cx + r < 0 || cx - r > w || cy + r < 0 || cy - r > h) return
      const gradient = ctx.createRadialGradient(cx, cy, r * 0.15, cx, cy, r)
      gradient.addColorStop(0, node.color)
      gradient.addColorStop(1, node.color + '00')
      ctx.globalAlpha = alpha * (dark ? 0.24 : 0.36)
      ctx.fillStyle = gradient
      ctx.beginPath()
      ctx.arc(cx, cy, r, 0, Math.PI * 2)
      ctx.fill()
      ctx.globalAlpha = 1
    }
    const drawBubbleCaption = (node: ClusterTreeNode, alpha: number, font: number) => {
      if (!node.label) return
      const cx = node.cx * scale + tx
      const cy = node.cy * scale + ty
      const r = node.radius * scale
      if (cx + r < 0 || cx - r > w || cy + r < 0 || cy - r > h || r <= 34) return
      ctx.font = `600 ${font}px Inter, sans-serif`
      ctx.textAlign = 'center'
      // Contrast via a two-pass offset draw: shadowBlur is prohibitively
      // slow on Firefox's software canvas paths.
      const halo = dark ? 'rgba(0, 0, 0, 0.85)' : 'rgba(255, 255, 255, 0.95)'
      const ink = dark ? '#eef1f6' : '#181f2a'
      const lines = node.labelLines ?? [node.label]
      const startY = cy - ((lines.length - 1) * (font + 2)) / 2 + font / 3
      for (let pass = 0; pass < 2; pass++) {
        ctx.globalAlpha = alpha * (pass === 0 ? 0.85 : (dark ? 0.98 : 1))
        ctx.fillStyle = pass === 0 ? halo : ink
        // Topic lines only — no per-cluster counts: they used to ride on
        // every caption line (twice per two-line caption) and read as noise.
        lines.forEach((line, index) => {
          if (ctx.measureText(line).width < r * 2.1) {
            ctx.fillText(line, cx + (pass === 0 ? 1 : 0), startY + index * (font + 2) + (pass === 0 ? 1 : 0))
          }
        })
      }
    }

    // ── layer 1: heat fills (tier 1: broad regions; tier 2: regions fading
    // out as sub-clusters fade in) — during a relayout the previous tree
    // cross-fades into the new one so bubbles breathe instead of snapping. ──
    const treeFade = treeFadeRef.current
    const treeT = treeFade ? Math.min(1, (now - treeFade.start) / 500) : 1
    if (treeT < 1) animating = true
    else treeFadeRef.current = null
    const treeE = easeOutCubic(treeT)
    // Birth maturity of one dot (0 → не родилась, ramp → 1). Shared by edges,
    // dots and the heat map below, so everything weaves in with one trail.
    const maturityOf = (id: string): number => {
      const born = appearedAtRef.current.get(id)
      if (born === undefined) return 1
      const age = now - born
      if (age < 0) return 0
      if (age < 750) return 0.1 + 0.9 * (1 - Math.pow(1 - age / 750, 2))
      return 1
    }
    // Region warmth: the average maturity of its members. While the trail is
    // running, a bubble's fill and caption warm up together with the dots it
    // owns instead of sitting there fully painted from frame one. Computed
    // only while births are pending (maxBirthRef bounds the whole trail).
    let warmthMap: Map<ClusterTreeNode, number> | null = null
    if (now < maxBirthRef.current) {
      warmthMap = new Map()
      const sums = new Map<ClusterTreeNode, number>()
      const counts = new Map<ClusterTreeNode, number>()
      for (let i = 0; i < points.length; i++) {
        const region = level1Ref.current[points[i].cluster]
        if (!region) continue
        sums.set(region, (sums.get(region) ?? 0) + maturityOf(points[i].id))
        counts.set(region, (counts.get(region) ?? 0) + 1)
      }
      for (const [region, sum] of sums) {
        const warmth = sum / (counts.get(region) ?? 1)
        warmthMap.set(region, warmth)
        for (const child of region.children ?? []) warmthMap.set(child, warmth)
      }
    }
    const warmthOf = (node: ClusterTreeNode): number => (warmthMap ? warmthMap.get(node) ?? 1 : 1)
    const drawTreePair = (drawTree: (nodes: ClusterTreeNode[], alpha: number) => void, next: ClusterTreeNode[], prev: ClusterTreeNode[] | undefined, baseAlpha: number) => {
      if (treeFade && treeT < 1 && prev) {
        if ((1 - treeE) * baseAlpha > 0.02) drawTree(prev, (1 - treeE) * baseAlpha)
        if (treeE * baseAlpha > 0.02) drawTree(next, treeE * baseAlpha)
      } else if (baseAlpha > 0.02) {
        drawTree(next, baseAlpha)
      }
    }
    if (scale < FAR) {
      drawTreePair((nodes, a) => { for (const region of nodes) drawBubbleFill(region, a * warmthOf(region)) }, level1Ref.current, treeFade?.fromL1, 1)
    } else if (scale < NEAR) {
      const regionAlpha = Math.max(0, Math.min(1, (FAR + 0.65 - scale) / 0.5))
      if (regionAlpha > 0.02) {
        drawTreePair((nodes, a) => { for (const region of nodes) drawBubbleFill(region, a * 0.7 * warmthOf(region)) }, level1Ref.current, treeFade?.fromL1, regionAlpha)
      }
      const subAlpha = Math.max(0, Math.min(1, (scale - FAR * 0.8) / 0.3))
      drawTreePair((nodes, a) => { for (const child of nodes) drawBubbleFill(child, a * warmthOf(child)) }, level2Ref.current, treeFade?.fromL2, subAlpha)
    }

    // ── dots: the color IS the lifecycle state (same palette as the
    // timeline), the shape is the verdict category; cluster membership is
    // already spoken by position and the region bubbles underneath. ──
    // Edges respect the birth trail too: an edge lights up with the alpha of
    // its least-mature endpoint, so threads appear as the trail reaches them
    // instead of hanging in the air before their dots exist.
    const showEdges = scale >= 0.5
    if (highlight) {
      // Focus mode: only the hovered point's own links, clearly brighter —
      // the context of one point beats two thousand faint lines. Filtered-out
      // neighbors still draw here: exploring a point's company is the point.
      ctx.lineWidth = 1.4
      ctx.strokeStyle = 'rgba(122, 162, 247, 0.45)'
      for (const [a, b] of edgesRef.current) {
        if (a !== highlight.center && b !== highlight.center) continue
        const edgeAlpha = Math.min(maturityOf(points[a].id), maturityOf(points[b].id))
        if (edgeAlpha <= 0.01) { stillFading = true; continue }
        const pa = animPos[a], pb = animPos[b]
        ctx.globalAlpha = edgeAlpha
        ctx.beginPath()
        ctx.moveTo(pa.x * scale + tx, pa.y * scale + ty)
        ctx.lineTo(pb.x * scale + tx, pb.y * scale + ty)
        ctx.stroke()
      }
      ctx.globalAlpha = 1
    } else if (showEdges) {
      ctx.lineWidth = 1
      ctx.strokeStyle = 'rgb(122, 162, 247)'
      const visibleIds = new Set(visible.map((entry) => entry.p.id))
      const indexById = new Map(points.map((p, i) => [p.id, i]))
      for (const [a, b] of edgesRef.current) {
        const edgeAlpha = Math.min(maturityOf(points[a].id), maturityOf(points[b].id)) * 0.08
        if (edgeAlpha <= 0.008) {
          if (edgeAlpha > 0) stillFading = true
          continue
        }
        const pa = animPos[a], pb = animPos[b]
        if (!visibleIds.has(points[a].id) || !visibleIds.has(points[b].id)) continue
        if (!matches[indexById.get(points[a].id)!] || !matches[indexById.get(points[b].id)!]) continue
        ctx.globalAlpha = edgeAlpha
        ctx.beginPath()
        ctx.moveTo(pa.x * scale + tx, pa.y * scale + ty)
        ctx.lineTo(pb.x * scale + tx, pb.y * scale + ty)
        ctx.stroke()
      }
      ctx.globalAlpha = 1
    }
    const near = scale >= NEAR
    const stateAlpha = Math.max(0, Math.min(1, (scale - (NEAR - 0.2)) / 0.6))
    // Dot opacity tracks the zoom tier: topics ~50%, sub-clusters ~75%,
    // statements 100% — the dots thicken as detail takes over, smoothly
    // interpolated between the tier boundaries instead of stepping.
    const dotAlpha = Math.max(0.5, Math.min(1, 0.5 + ((scale - FAR) / (NEAR - FAR)) * 0.5))
    for (const { p, sx, sy, active, i } of visible) {
      // Color = the point's REGION (the same shade as its bubble, its
      // sub-bubbles and the legend entry): a region reads as one hue on every
      // tier. Shape = the lifecycle verdict (circle/diamond/cross).
      const regionColor = regionColorOf(p)
      const shape = stateShape(p.item.state)
      const dead = shape === 'cross'
      let alpha = active ? dotAlpha : dotAlpha * 0.35
      // Star birth: a dot is invisible until its staggered birth moment, then
      // ramps up over ~750ms. The bloom ring is a quiet one-frame accent —
      // thin and short-lived, not a strobe around every arrival.
      const born = appearedAtRef.current.get(p.id)
      if (born !== undefined) {
        const age = now - born
        if (age < 0) { stillFading = true; continue }
        if (age < 750) {
          const k = age / 750
          alpha *= 0.1 + 0.9 * (1 - Math.pow(1 - k, 2))
          stillFading = true
          if (age < 260) {
            const rk = age / 260
            ctx.globalAlpha = (1 - rk) * 0.28 * (active ? 1 : 0.6)
            ctx.strokeStyle = regionColor
            ctx.lineWidth = 0.9
            ctx.beginPath()
            ctx.arc(sx, sy, dotR + 1.5 + rk * 5, 0, Math.PI * 2)
            ctx.stroke()
          }
        }
      }
      let radius = dotR
      if (highlight) {
        // The lit neighborhood keeps (almost) full ink and the hovered point
        // grows a little; everything else sinks to a whisper.
        if (highlight.set.has(i)) {
          alpha = Math.max(alpha, i === highlight.center ? 1 : 0.9)
          if (i === highlight.center) radius = dotR * 1.5
        } else {
          alpha = Math.min(alpha, dotAlpha * 0.12)
        }
      }
      ctx.globalAlpha = dead ? alpha * 0.85 : alpha
      if (dead) {
        ctx.lineWidth = Math.max(1.1, radius * 0.55)
        ctx.strokeStyle = regionColor
        ctx.lineCap = 'round'
        drawShape(ctx, 'cross', sx, sy, radius)
      } else {
        ctx.fillStyle = regionColor
        drawShape(ctx, shape, sx, sy, radius)
      }
    }
    ctx.globalAlpha = 1
    // Keep the animation frames coming while births, morphs or the camera
    // tween are still in flight.
    if (stillFading || animating) frameRef.current = requestAnimationFrame(draw)

    // ── layer 4: cluster captions, on top of the dots (cross-faded with the
    // bubbles they belong to). While a search/state filter dims most dots,
    // caption ink is normalized across regions by ABSOLUTE hit counts: the
    // region holding the most matches stays at full brightness, the rest
    // fade proportionally — 10 hits → 100%, 3 hits → 30%, none → 0. ──
    const filtering = matches.some((m) => !m)
    const captionFactor = new Map<ClusterTreeNode, number>()
    if (filtering) {
      const hits = new Map<ClusterTreeNode, number>()
      for (let i = 0; i < points.length; i++) {
        if (!matches[i]) continue
        const region = level1Ref.current[points[i].cluster]
        if (!region) continue
        hits.set(region, (hits.get(region) ?? 0) + 1)
      }
      const maxHits = Math.max(0, ...hits.values())
      for (const region of level1Ref.current) {
        const factor = maxHits ? (hits.get(region) ?? 0) / maxHits : 0
        captionFactor.set(region, factor)
        for (const child of region.children ?? []) captionFactor.set(child, factor)
      }
    }
    const fadeCaption = (node: ClusterTreeNode, alpha: number, font: number) =>
      drawBubbleCaption(node, alpha * (captionFactor.get(node) ?? 1) * warmthOf(node), font)
    if (scale < FAR) {
      drawTreePair((nodes, a) => { for (const region of nodes) fadeCaption(region, a, 13) }, level1Ref.current, treeFade?.fromL1, 1)
    } else if (scale < NEAR) {
      const regionAlpha = Math.max(0, Math.min(1, (FAR + 0.65 - scale) / 0.5))
      if (regionAlpha > 0.02) {
        drawTreePair((nodes, a) => { for (const region of nodes) fadeCaption(region, a * 0.7, 12) }, level1Ref.current, treeFade?.fromL1, regionAlpha)
      }
      const subAlpha = Math.max(0, Math.min(1, (scale - FAR * 0.8) / 0.3))
      drawTreePair((nodes, a) => { for (const child of nodes) fadeCaption(child, a, 11) }, level2Ref.current, treeFade?.fromL2, subAlpha)
    }

    // ── layer 5: bounded, non-overlapping item labels ──
    if (near) {
      ctx.font = '10.5px Inter, sans-serif'
      ctx.textAlign = 'center'
      const accepted: Array<{ x: number; y: number }> = []
      let drawn = 0
      // With a hover focus the labels belong to the lit neighborhood only —
      // labeling the dimmed crowd would bury the answer again.
      const labelPool = highlight ? visible.filter((entry) => highlight.set.has(entry.i)) : visible
      for (const { p, sx, sy, active } of labelPool) {
        if (!highlight && (!active || drawn >= LABEL_MAX)) continue
        if (drawn >= LABEL_MAX) continue
        let crowded = false
        for (const spot of accepted) {
          if (Math.abs(spot.x - sx) < LABEL_BOX_W && Math.abs(spot.y - sy) < LABEL_BOX_H) { crowded = true; break }
        }
        if (crowded) continue
        accepted.push({ x: sx, y: sy })
        drawn++
        // The label rides its point's region color — one hue per region, the
        // same the dot below shows — over a two-pass halo for contrast. A
        // CSS var() string is not a canvas color: assigning one silently
        // keeps the previous fillStyle, so every label used to inherit
        // whatever the last drawn dot had — all labels flipped hue together
        // as the zoom moved the visible set.
        const halo = dark ? 'rgba(0, 0, 0, 0.85)' : 'rgba(255, 255, 255, 0.95)'
        const label = p.item.proposition.length > 44 ? `${p.item.proposition.slice(0, 43)}…` : p.item.proposition
        ctx.globalAlpha = highlight ? 1 : active ? 1 : 0.55
        for (let pass = 0; pass < 2; pass++) {
          ctx.fillStyle = pass === 0 ? halo : regionColorOf(p)
          ctx.fillText(label, sx + (pass === 0 ? 1 : 0), sy - dotR - 4 + (pass === 0 ? 1 : 0))
        }
        ctx.globalAlpha = 1
      }
    }
  }, [points, matches, viewScale, regionColorOf])

  useEffect(() => {
    frameRef.current = requestAnimationFrame(draw)
    return () => cancelAnimationFrame(frameRef.current)
  }, [draw])

  // Recompute the lit neighborhood when the hovered point changes and redraw.
  useEffect(() => {
    const redraw = () => { frameRef.current = requestAnimationFrame(draw) }
    if (!hoverId) {
      if (highlightRef.current) { highlightRef.current = null; redraw() }
      return
    }
    const center = pointIndexById.get(hoverId)
    if (center === undefined) {
      if (highlightRef.current) { highlightRef.current = null; redraw() }
      return
    }
    const set = new Set<number>([center])
    for (const [a, b] of edgesRef.current) {
      if (a === center) set.add(b)
      else if (b === center) set.add(a)
    }
    highlightRef.current = { center, set }
    redraw()
  }, [hoverId, draw, pointIndexById])

  // Zoom & pan (d3-zoom semantics, minimal hand-rolled version on canvas).
  // A drag that ends over a point used to open the dossier — the click event
  // fires after mouseup wherever the gesture started. The pointer travel is
  // now measured: only a press-release without real movement counts as a
  // click (CLICK_SLOP absorbs the tremor of an honest standing click).
  useEffect(() => {
    const wrap = wrapRef.current
    if (!wrap) return
    let dragging = false
    let lastX = 0, lastY = 0
    let downX = 0, downY = 0
    let movedFar = false
    const CLICK_SLOP = 4
    const onWheel = (event: WheelEvent) => {
      event.preventDefault()
      camRef.current = null // manual control cancels the camera tween
      const transform = transformRef.current
      const factor = Math.exp(-event.deltaY * 0.0016)
      const next = Math.min(8, Math.max(0.2, transform.k * factor))
      const rect = wrap.getBoundingClientRect()
      const mx = event.clientX - rect.left, my = event.clientY - rect.top
      transform.x = mx - ((mx - transform.x) * next) / transform.k
      transform.y = my - ((my - transform.y) * next) / transform.k
      transform.k = next
      frameRef.current = requestAnimationFrame(draw)
    }
    const onDown = (event: MouseEvent) => {
      dragging = true; movedFar = false
      downX = lastX = event.clientX; downY = lastY = event.clientY
      if (event.button === 0) camRef.current = null // a drag cancels the camera tween
    }
    const onMove = (event: MouseEvent) => {
      if (dragging) {
        if (Math.hypot(event.clientX - downX, event.clientY - downY) > CLICK_SLOP) movedFar = true
        const transform = transformRef.current
        transform.x += event.clientX - lastX
        transform.y += event.clientY - lastY
        lastX = event.clientX; lastY = event.clientY
        frameRef.current = requestAnimationFrame(draw)
        return
      }
      // Hover pick: nearest point within radius in world coordinates. The
      // tooltip rides the cursor (clamped to the container) — a fixed panel
      // across the top kept disconnecting the hint from the dot.
      const rect = wrap.getBoundingClientRect()
      const { scale, tx, ty } = viewScale()
      const mx = (event.clientX - rect.left - tx) / scale
      const my = (event.clientY - rect.top - ty) / scale
      const best = pickPoint(mx, my, 14 / scale)
      // Guard by id: a fresh object per mousemove re-rendered the whole
      // component at pointer frequency for no visual change.
      setHover((current) => {
        const same = current?.point.id === best?.id && (best !== null || current !== null)
        if (same && current) {
          return { point: best ?? current.point, clientX: event.clientX - rect.left, clientY: event.clientY - rect.top }
        }
        return best ? { point: best, clientX: event.clientX - rect.left, clientY: event.clientY - rect.top } : null
      })
    }
    const onUp = () => { dragging = false }
    const onClick = (event: MouseEvent) => {
      if (movedFar) return // a pan gesture ended where it happened to land
      const rect = wrap.getBoundingClientRect()
      const { scale, tx, ty } = viewScale()
      const mx = (event.clientX - rect.left - tx) / scale
      const my = (event.clientY - rect.top - ty) / scale
      const best = pickPoint(mx, my, 14 / scale)
      if (best) onOpenDossier(best.item.id)
    }
    wrap.addEventListener('wheel', onWheel, { passive: false })
    wrap.addEventListener('mousedown', onDown)
    window.addEventListener('mousemove', onMove)
    window.addEventListener('mouseup', onUp)
    wrap.addEventListener('click', onClick)
    return () => {
      wrap.removeEventListener('wheel', onWheel)
      wrap.removeEventListener('mousedown', onDown)
      window.removeEventListener('mousemove', onMove)
      window.removeEventListener('mouseup', onUp)
      wrap.removeEventListener('click', onClick)
    }
  }, [points, draw, onOpenDossier, viewScale, pickPoint])

  // Filter chips read the server's whole-corpus state counts — stable from
  // the first batch instead of growing as later pages land. An older journal
  // without `states` falls back to counting the pages landed so far.
  const statesList = useMemo(() => {
    if (states) return Object.keys(states).filter((s) => (states as Record<string, number>)[s] > 0).sort()
    return [...new Set((items ?? []).map((item) => item.state))].sort()
  }, [states, items])
  const countOf = useCallback((state: string) => {
    if (states) return (states as Record<string, number>)[state] ?? 0
    return (items ?? []).filter((item) => item.state === state).length
  }, [states, items])
  const allCount = states ? statesList.reduce((sum, s) => sum + (states as Record<string, number>)[s], 0) : (items ?? []).length

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: '0.5rem', height: '100%' }}>
      <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'center', flexWrap: 'wrap' }}>
        <TextInput
          id="knowledge-map-search" size="sm" labelText="" hideLabel style={{ width: '260px' }}
          placeholder={t('knowledge.map_search') ?? 'Поиск по формулировкам и топикам…'}
          value={query} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setQuery(e.target.value)}
        />
        <Button size="sm" kind={stateFilter === '' ? 'primary' : 'ghost'} onClick={() => setStateFilter('')}>
          {t('knowledge.map_all') ?? 'Все'} ({allCount})
        </Button>
        {statesList.map((state) => (
          <Button key={state} size="sm" kind={stateFilter === state ? 'primary' : 'ghost'} onClick={() => setStateFilter(stateFilter === state ? '' : state)}>
            {stateLabel(state, t)} ({countOf(state)})
          </Button>
        ))}
        <span style={{ flex: 1 }} />
        <Button size="sm" kind="ghost" onClick={() => void load({ ignoreCache: true })} disabled={loading}>↻</Button>
      </div>

      {error && <InlineNotification kind="error" title={t('action.error') ?? 'Error'} subtitle={error} onClose={() => setError('')} lowContrast />}

      <div ref={wrapRef} style={{ position: 'relative', flex: 1, minHeight: '320px', border: '1px solid var(--tm-border)', borderRadius: 'var(--tm-radius-md)', background: 'var(--tm-bg)', overflow: 'hidden', cursor: hover ? 'pointer' : 'grab' }}>
        <canvas ref={canvasRef} style={{ display: 'block' }} />
        {/* A failed load is unmissable: dead center, with a retry — the old
            placement (above the toolbar) scrolled out of sight and the map
            looked «broken» instead of saying what happened. */}
        {error && !loading && (
          <div style={{ position: 'absolute', inset: 0, display: 'grid', placeItems: 'center', background: 'rgba(0,0,0,.25)', zIndex: 4 }}>
            <div style={{ maxWidth: '28rem', textAlign: 'center', background: 'var(--tm-elevated, #1a1a1a)', border: '1px solid var(--tm-border)', borderRadius: '10px', padding: '1rem 1.25rem' }}>
              <p style={{ color: 'var(--tm-danger, #da1e28)', fontWeight: 600, margin: 0 }}>{t('action.error') ?? 'Error'}</p>
              <p style={{ color: 'var(--tm-text-2)', fontSize: '0.8rem', margin: '0.4rem 0 0.75rem', wordBreak: 'break-word' }}>{error}</p>
              <Button size="sm" onClick={() => { setError(''); void load({ ignoreCache: true }) }}>{t('action.retry') ?? 'Retry'}</Button>
            </div>
          </div>
        )}
        {/* Streaming progress lives on the map itself: batches land as dots
            blooming in, this pill counts them up. */}
        {progress && (
          <div style={{
            position: 'absolute', top: '0.6rem', left: '50%', transform: 'translateX(-50%)',
            display: 'flex', alignItems: 'center', gap: '0.5rem', padding: '0.3rem 0.7rem',
            background: 'var(--tm-elevated, #1a1a1a)', border: '1px solid var(--tm-border)',
            borderRadius: '999px', fontSize: '0.72rem', color: 'var(--tm-text-2)',
            boxShadow: '0 4px 14px rgba(0,0,0,.25)', pointerEvents: 'none', zIndex: 3,
          }}>
            <span className="pulse" style={{ width: 6, height: 6, borderRadius: '999px', background: 'var(--tm-teal, #4fb3a9)', display: 'inline-block' }} />
            <span>{t('knowledge.map_progress', { loaded: String(progress.loaded), total: String(progress.total ?? progress.loaded) }) ?? `Знания: ${progress.loaded} из ${progress.total ?? '…'}`}</span>
            <span style={{ width: '5.5rem', height: 3, borderRadius: 999, background: 'var(--tm-border)', overflow: 'hidden', display: 'inline-block' }}>
              <span style={{
                display: 'block', height: '100%', borderRadius: 999,
                width: `${progress.total ? Math.min(100, Math.round((progress.loaded / progress.total) * 100)) : 12}%`,
                background: 'var(--tm-teal, #4fb3a9)', transition: 'width 240ms ease',
              }} />
            </span>
          </div>
        )}
        {hover && (() => {
          const card = 340
          const left = Math.min(Math.max(hover.clientX + 14, card / 2 + 8), (wrapRef.current?.clientWidth ?? card) - card / 2 - 8)
          const above = hover.clientY > 150
          return (
            <div style={{
              position: 'absolute', pointerEvents: 'none', width: `${card}px`, maxWidth: '90%',
              background: 'var(--tm-elevated, #1a1a1a)', border: '1px solid var(--tm-border)', borderRadius: '8px',
              padding: '0.5rem 0.65rem', fontSize: '0.76rem', lineHeight: 1.45,
              left, top: above ? hover.clientY - 12 : hover.clientY + 14,
              transform: above ? 'translate(-50%, -100%)' : 'translate(-50%, 0)',
            }}>
              <div style={{ display: 'flex', gap: '0.35rem', alignItems: 'center', marginBottom: '0.25rem', flexWrap: 'wrap' }}>
                <Tag size="sm" type={STATE_TAG[hover.point.item.state] ?? 'cool-gray'}>{stateLabel(hover.point.item.state, t)}</Tag>
                <span style={{ color: 'var(--tm-muted)', fontFamily: 'var(--tm-font-mono)', fontSize: '0.66rem' }}>{hover.point.item.project}</span>
                {(hover.point.item.topics ?? []).slice(0, 3).map((topic) => <Tag key={topic} size="sm" type="cool-gray">{topic}</Tag>)}
              </div>
              {hover.point.item.proposition}
            </div>
          )
        })()}
        {loading && <div style={{ position: 'absolute', inset: 0, display: 'grid', placeItems: 'center', color: 'var(--tm-muted)', fontSize: '0.8rem' }}>…</div>}
        {!loading && items !== null && items.length === 0 && (
          <div style={{ position: 'absolute', inset: 0, display: 'grid', placeItems: 'center', color: 'var(--tm-muted)', fontSize: '0.8rem', textAlign: 'center', padding: '1rem' }}>
            {(knowledgeTotal ?? 0) > 0
              ? t('knowledge.map_no_vectors', { count: String(knowledgeTotal) }) ?? 'Знания есть, но векторы не считаны — проверьте логи ядра и доступ к huggingface.co.'
              : t('knowledge.map_empty') ?? 'Нет векторов знаний для карты: включите эмбеддинги (semantic-слой) и дайте бэкфиллу наполнить базу.'}
            {projectErrors.length > 0 && (
              <p style={{ color: 'var(--tm-danger, #da1e28)', marginTop: '0.4rem' }}>{t('knowledge.map_project_errors') ?? 'Проекции с ошибками:'} {projectErrors.join('; ')}</p>
            )}
          </div>
        )}
      </div>
      {/* Topic legend: which cluster color is which theme. Built from the
          same pyramid the canvas renders — the largest regions first. */}
      {/* Shape legend: color = region, shape = lifecycle verdict. */}
      {legendTopics.length > 0 && (
        <div style={{ display: 'flex', gap: '0.6rem', flexWrap: 'wrap', fontSize: '0.7rem', color: 'var(--tm-text-2)', alignItems: 'center' }}>
          <span style={{ color: 'var(--tm-muted)' }}>{t('knowledge.map_legend_topics') ?? 'Регионы:'}</span>
          {legendTopics.map((topic, index) => (
            <span key={index} style={{ display: 'inline-flex', gap: '0.3rem', alignItems: 'center' }}>
              <span style={{ display: 'inline-block', width: '0.6rem', height: '0.6rem', borderRadius: '50%', background: topic.color, opacity: 0.55, flexShrink: 0 }} />
              {topic.label} · {topic.count}
            </span>
          ))}
        </div>
      )}
      <div style={{ display: 'flex', gap: '0.75rem', flexWrap: 'wrap', fontSize: '0.7rem', color: 'var(--tm-muted)', alignItems: 'center' }}>
        <span>{t('knowledge.map_hint_zoom') ?? 'Колесо — масштаб: сначала кластеры с подписями, при приближении — отдельные знания. Клик по точке — досье.'}</span>
        <span style={{ display: 'inline-flex', gap: '0.3rem', alignItems: 'center' }}>
          <span style={{ display: 'inline-block', width: '0.5rem', height: '0.5rem', borderRadius: '50%', background: 'var(--tm-text-2)', flexShrink: 0 }} />
          {t('knowledge.map_shape_live') ?? 'живые'}
        </span>
        <span style={{ display: 'inline-flex', gap: '0.3rem', alignItems: 'center' }}>
          <span style={{ display: 'inline-block', width: '0.5rem', height: '0.5rem', background: 'var(--tm-text-2)', transform: 'rotate(45deg)', flexShrink: 0 }} />
          {t('knowledge.map_shape_challenged') ?? 'оспоренные'}
        </span>
        <span style={{ display: 'inline-flex', gap: '0.3rem', alignItems: 'center' }}>
          <span style={{ color: 'var(--tm-text-2)', fontFamily: 'var(--tm-font-mono)', fontWeight: 700, flexShrink: 0 }}>✕</span>
          {t('knowledge.map_shape_dead') ?? 'выбыли'}
        </span>
        <span style={{ marginLeft: 'auto' }}>
          {tier === 1 ? (t('knowledge.map_tier_regions') ?? 'уровень: темы') : tier === 2 ? (t('knowledge.map_tier_sub') ?? 'уровень: подкластеры') : (t('knowledge.map_tier_points') ?? 'уровень: знания')}
        </span>
      </div>
    </div>
  )
}
