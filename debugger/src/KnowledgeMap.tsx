import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Button, InlineNotification, Tag, TextInput } from '@carbon/react'
import { observationApi } from './observationApi'
import { useT } from './i18n'
import { useTheme } from './theme'
import { stateLabel } from './explorer/shared'
import {
  CLUSTER_COLORS,
  STATE_COLORS,
  buildClusterTree,
  projectSemanticMap,
  semanticGraph,
  type ClusterTreeNode,
  type LayoutNode,
  type MapItem,
} from './semanticMap'

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
 *  NEAR — state-colored cores and the bounded item labels. */
const FAR = 0.55
const NEAR = 1.6
/** A label is drawn only if no already-drawn label sits within this box. */
const LABEL_BOX_W = 92
const LABEL_BOX_H = 18
const LABEL_MAX = 70

/** Shape codes the lifecycle, the way the experience timeline does: live
 *  statements are circles, challenged — diamonds, invalidated/superseded —
 *  crosses. Color carries the topic cluster, shape carries the verdict. */
function stateShape(state: string): 'circle' | 'diamond' | 'cross' | 'square' {
  if (state === 'invalidated' || state === 'disproved' || state === 'superseded') return 'cross'
  if (state === 'challenged') return 'diamond'
  if (state === 'rejected') return 'square'
  return 'circle' // proposed / confirmed / corrected — live
}

function drawShape(ctx: CanvasRenderingContext2D, shape: 'circle' | 'diamond' | 'cross' | 'square', x: number, y: number, r: number) {
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
  const level1Ref = useRef<ClusterTreeNode[]>([])
  const level2Ref = useRef<ClusterTreeNode[]>([])
  /** Aggregate opacity per zoom: which pyramid tier is on stage. Kept in a
   *  ref + state mirror so the tier badges re-render without redrawing hooks. */
  const [tier, setTier] = useState<1 | 2 | 3>(1)
  /** Topic legend: the largest regions with their palette colors — the map
   *  paints dots by cluster color, so «what does blue mean» needs an answer
   *  that follows the data. */
  const [legendTopics, setLegendTopics] = useState<Array<{ label: string; color: string; count: number }>>([])

  const load = useCallback(async () => {
    setLoading(true); setError('')
    try {
      const page = await observationApi.knowledgeEmbeddingMap({ project: project || undefined })
      setItems(page.items ?? [])
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Request failed')
      setItems([])
    } finally {
      setLoading(false)
    }
  }, [project])

  useEffect(() => { void load() }, [load])

  // Re-layout when the dataset changes; the cluster pyramid (regions →
  // sub-clusters) is computed once here, not per frame. The initial view
  // fits every point on screen (with a margin) instead of opening at a
  // zoom that crops half the map.
  useEffect(() => {
    if (!items) return
    if (items.length === 0) { setPoints([]); level1Ref.current = []; level2Ref.current = []; return }
    const layout = projectSemanticMap(items, { k: 6, width: WORLD_W, height: WORLD_H })
    const radius = Math.max(2.2, Math.min(4, 900 / Math.sqrt(layout.nodes.length + 1)))
    setPoints(layout.nodes.map((node) => ({ ...node, r: radius })))
    const tree = buildClusterTree(items, layout.nodes)
    level1Ref.current = tree.level1
    level2Ref.current = tree.level2
    edgesRef.current = semanticGraph(items, 8, 0.6).edges
    setLegendTopics(
      [...tree.level1]
        .sort((a, b) => b.count - a.count)
        .slice(0, 6)
        .filter((node) => node.label)
        .map((node) => ({ label: node.label, color: node.color, count: node.count })),
    )
    // Fit-to-screen: the world layout is drawn at WORLD_W×H; scale it into
    // the actual canvas box with a margin, centered.
    requestAnimationFrame(() => {
      const wrap = wrapRef.current
      if (!wrap) return
      const w = wrap.clientWidth, h = wrap.clientHeight
      const margin = 40
      const fit = Math.min((w - margin) / WORLD_W, (h - margin) / WORLD_H)
      const base = Math.min(w / WORLD_W, h / WORLD_H)
      // transform.k is relative to base (screen scale = k * base): the fit
      // factor expressed in that space.
      transformRef.current = {
        k: Math.max(0.2, Math.min(8, fit / base)),
        x: (w - WORLD_W * fit) / 2,
        y: (h - WORLD_H * fit) / 2,
      }
    })
  }, [items])

  const matches = useMemo(() => {
    const needle = query.trim().toLowerCase()
    return points.map((point) => {
      if (stateFilter && point.item.state !== stateFilter) return false
      if (!needle) return true
      return point.item.proposition.toLowerCase().includes(needle)
        || point.item.id.toLowerCase().includes(needle)
        || (point.item.topics ?? []).some((topic) => topic.toLowerCase().includes(needle))
    })
  }, [points, query, stateFilter])

  const viewScale = useCallback(() => {
    const wrap = wrapRef.current
    const w = wrap?.clientWidth ?? WORLD_W
    const h = wrap?.clientHeight ?? WORLD_H
    const base = Math.min(w / WORLD_W, h / WORLD_H)
    const { x, y, k } = transformRef.current
    return { scale: k * base, tx: x, ty: y, w, h }
  }, [])

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
    const { scale, tx, ty } = viewScale()
    // Dark theme: glows and captions need less fill and dimmer text to read
    // as a heat map; light theme packs stronger fills for the same contrast.
    const dark = theme === 'dark'

    // Project + cull once; everything below works in screen pixels, so fonts
    // stay constant-size and dots grow only mildly with zoom.
    const visible: Array<{ p: MapPoint; sx: number; sy: number; active: boolean }> = []
    for (let i = 0; i < points.length; i++) {
      const sx = points[i].x * scale + tx
      const sy = points[i].y * scale + ty
      if (sx < -30 || sx > w + 30 || sy < -30 || sy > h + 30) continue
      visible.push({ p: points[i], sx, sy, active: matches[i] })
    }
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
      ctx.globalAlpha = alpha * (dark ? 0.95 : 1)
      ctx.fillStyle = dark ? '#eef1f6' : '#181f2a'
      ctx.font = `600 ${font}px Inter, sans-serif`
      ctx.textAlign = 'center'
      ctx.shadowColor = dark ? 'rgba(0, 0, 0, 0.9)' : 'rgba(255, 255, 255, 0.95)'
      ctx.shadowBlur = 5
      ctx.shadowOffsetY = 1
      const lines = node.labelLines ?? [node.label]
      const startY = cy - ((lines.length - 1) * (font + 2)) / 2 + font / 3
      lines.forEach((line, index) => {
        if (ctx.measureText(line).width < r * 2.1) {
          ctx.fillText(line, cx, startY + index * (font + 2))
        }
      })
    }

    // ── layer 1: heat fills (tier 1: broad regions; tier 2: regions fading
    // out as sub-clusters fade in) ──
    if (scale < FAR) {
      for (const region of level1Ref.current) drawBubbleFill(region, 1)
    } else if (scale < NEAR) {
      const regionAlpha = Math.max(0, Math.min(1, (FAR + 0.65 - scale) / 0.5))
      if (regionAlpha > 0.02) {
        for (const region of level1Ref.current) drawBubbleFill(region, regionAlpha * 0.7)
      }
      const subAlpha = Math.max(0, Math.min(1, (scale - FAR * 0.8) / 0.3))
      for (const child of level2Ref.current) drawBubbleFill(child, subAlpha)
    }

    // ── dots: cluster color from the mid plane on; state joins near ──
    const showEdges = scale >= 0.5
    if (showEdges) {
      ctx.lineWidth = 1
      ctx.strokeStyle = 'rgba(122, 162, 247, 0.08)'
      const visibleIds = new Set(visible.map((entry) => entry.p.id))
      const indexById = new Map(points.map((p, i) => [p.id, i]))
      for (const [a, b] of edgesRef.current) {
        const pa = points[a], pb = points[b]
        if (!visibleIds.has(pa.id) || !visibleIds.has(pb.id)) continue
        if (!matches[indexById.get(pa.id)!] || !matches[indexById.get(pb.id)!]) continue
        ctx.beginPath()
        ctx.moveTo(pa.x * scale + tx, pa.y * scale + ty)
        ctx.lineTo(pb.x * scale + tx, pb.y * scale + ty)
        ctx.stroke()
      }
    }
    const near = scale >= NEAR
    const stateAlpha = Math.max(0, Math.min(1, (scale - (NEAR - 0.2)) / 0.6))
    for (const { p, sx, sy, active } of visible) {
      const clusterColor = p.cluster > 0 ? CLUSTER_COLORS[p.cluster % CLUSTER_COLORS.length] : '#7aa2f7'
      const shape = stateShape(p.item.state)
      const dead = shape === 'cross'
      ctx.globalAlpha = active ? (dead ? 0.85 : 1) : 0.18
      if (dead) {
        // Retired statements: a cross in the cluster color — reads at any
        // zoom without filling space.
        ctx.lineWidth = Math.max(1.1, dotR * 0.55)
        ctx.strokeStyle = clusterColor
        ctx.lineCap = 'round'
        drawShape(ctx, 'cross', sx, sy, dotR)
      } else {
        ctx.fillStyle = clusterColor
        drawShape(ctx, shape, sx, sy, dotR)
        if (near && stateAlpha > 0) {
          // The state core joins only at the near zoom (confirmed = a light
          // center, challenged keeps the diamond) — it never replaces the
          // cluster identity.
          ctx.globalAlpha = (active ? 1 : 0.18) * stateAlpha
          ctx.beginPath()
          ctx.arc(sx, sy, dotR * 0.5, 0, Math.PI * 2)
          ctx.fillStyle = STATE_COLORS[p.item.state] ?? '#7aa2f7'
          ctx.fill()
        }
      }
    }
    ctx.globalAlpha = 1

    // ── layer 4: cluster captions, on top of the dots ──
    if (scale < FAR) {
      for (const region of level1Ref.current) drawBubbleCaption(region, 1, 13)
    } else if (scale < NEAR) {
      const regionAlpha = Math.max(0, Math.min(1, (FAR + 0.65 - scale) / 0.5))
      if (regionAlpha > 0.02) {
        for (const region of level1Ref.current) drawBubbleCaption(region, regionAlpha * 0.7, 12)
      }
      const subAlpha = Math.max(0, Math.min(1, (scale - FAR * 0.8) / 0.3))
      for (const child of level2Ref.current) drawBubbleCaption(child, subAlpha, 11)
    }

    // ── layer 5: bounded, non-overlapping item labels ──
    if (near) {
      ctx.font = '10.5px Inter, sans-serif'
      ctx.textAlign = 'center'
      const accepted: Array<{ x: number; y: number }> = []
      let drawn = 0
      for (const { p, sx, sy, active } of visible) {
        if (!active || drawn >= LABEL_MAX) continue
        let crowded = false
        for (const spot of accepted) {
          if (Math.abs(spot.x - sx) < LABEL_BOX_W && Math.abs(spot.y - sy) < LABEL_BOX_H) { crowded = true; break }
        }
        if (crowded) continue
        accepted.push({ x: sx, y: sy })
        drawn++
        ctx.fillStyle = 'var(--tm-text, #cfcfcf)'
        const label = p.item.proposition.length > 44 ? `${p.item.proposition.slice(0, 43)}…` : p.item.proposition
        ctx.fillText(label, sx, sy - dotR - 4)
      }
    }
  }, [points, matches, viewScale])

  useEffect(() => {
    frameRef.current = requestAnimationFrame(draw)
    return () => cancelAnimationFrame(frameRef.current)
  }, [draw])

  // Zoom & pan (d3-zoom semantics, minimal hand-rolled version on canvas).
  useEffect(() => {
    const wrap = wrapRef.current
    if (!wrap) return
    let dragging = false
    let lastX = 0, lastY = 0
    const onWheel = (event: WheelEvent) => {
      event.preventDefault()
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
      dragging = true; lastX = event.clientX; lastY = event.clientY
    }
    const onMove = (event: MouseEvent) => {
      if (dragging) {
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
      let best: MapPoint | null = null
      let bestDist = 14 / scale
      for (const point of points) {
        const dx = point.x - mx, dy = point.y - my
        const dist = Math.sqrt(dx * dx + dy * dy)
        if (dist < Math.max(point.r + 3 / scale, bestDist)) { best = point; bestDist = dist }
      }
      if (best) setHover({ point: best, clientX: event.clientX - rect.left, clientY: event.clientY - rect.top })
      else setHover(null)
    }
    const onUp = () => { dragging = false }
    const onClick = (event: MouseEvent) => {
      const rect = wrap.getBoundingClientRect()
      const { scale, tx, ty } = viewScale()
      const mx = (event.clientX - rect.left - tx) / scale
      const my = (event.clientY - rect.top - ty) / scale
      let best: MapPoint | null = null
      let bestDist = 14 / scale
      for (const point of points) {
        const dx = point.x - mx, dy = point.y - my
        const dist = Math.sqrt(dx * dx + dy * dy)
        if (dist < Math.max(point.r + 3 / scale, bestDist)) { best = point; bestDist = dist }
      }
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
  }, [points, draw, onOpenDossier, viewScale])

  const states = useMemo(() => [...new Set((items ?? []).map((item) => item.state))].sort(), [items])

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: '0.5rem', height: '100%' }}>
      <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'center', flexWrap: 'wrap' }}>
        <TextInput
          id="knowledge-map-search" size="sm" labelText="" hideLabel style={{ width: '260px' }}
          placeholder={t('knowledge.map_search') ?? 'Поиск по формулировкам и топикам…'}
          value={query} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setQuery(e.target.value)}
        />
        <Button size="sm" kind={stateFilter === '' ? 'primary' : 'ghost'} onClick={() => setStateFilter('')}>
          {t('knowledge.map_all') ?? 'Все'} ({(items ?? []).length})
        </Button>
        {states.map((state) => (
          <Button key={state} size="sm" kind={stateFilter === state ? 'primary' : 'ghost'} onClick={() => setStateFilter(stateFilter === state ? '' : state)}>
            {stateLabel(state, t)} ({(items ?? []).filter((item) => item.state === state).length})
          </Button>
        ))}
        <span style={{ flex: 1 }} />
        <Button size="sm" kind="ghost" onClick={() => void load()} disabled={loading}>↻</Button>
      </div>

      {error && <InlineNotification kind="error" title={t('action.error') ?? 'Error'} subtitle={error} onClose={() => setError('')} lowContrast />}

      <div ref={wrapRef} style={{ position: 'relative', flex: 1, minHeight: '320px', border: '1px solid var(--tm-border)', borderRadius: 'var(--tm-radius-md)', background: 'var(--tm-bg)', overflow: 'hidden', cursor: hover ? 'pointer' : 'grab' }}>
        <canvas ref={canvasRef} style={{ display: 'block' }} />
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
            {t('knowledge.map_empty') ?? 'Нет векторов знаний для карты: включите эмбеддинги (semantic-слой) и дайте бэкфиллу наполнить базу.'}
          </div>
        )}
      </div>
      {/* Topic legend: which cluster color is which theme. Built from the
          same pyramid the canvas renders — the largest regions first. */}
      {legendTopics.length > 0 && (
        <div style={{ display: 'flex', gap: '0.6rem', flexWrap: 'wrap', fontSize: '0.7rem', color: 'var(--tm-text-2)', alignItems: 'center' }}>
          <span style={{ color: 'var(--tm-muted)' }}>{t('knowledge.map_legend_topics') ?? 'Темы:'}</span>
          {legendTopics.map((topic) => (
            <span key={topic.label} style={{ display: 'inline-flex', gap: '0.3rem', alignItems: 'center' }}>
              <span style={{ display: 'inline-block', width: '0.6rem', height: '0.6rem', borderRadius: '50%', background: topic.color, flexShrink: 0 }} />
              {topic.label} · {topic.count}
            </span>
          ))}
        </div>
      )}
      <div style={{ display: 'flex', gap: '0.75rem', flexWrap: 'wrap', fontSize: '0.7rem', color: 'var(--tm-muted)', alignItems: 'center' }}>
        <span>{t('knowledge.map_hint_zoom') ?? 'Колесо — масштаб: сначала кластеры с подписями, при приближении — отдельные знания. Клик по точке — досье.'}</span>
        <span style={{ display: 'inline-flex', gap: '0.35rem', alignItems: 'center' }}>
          <span style={{ display: 'inline-block', width: '0.55rem', height: '0.55rem', borderRadius: '50%', background: '#73daca', flexShrink: 0 }} />
          {t('knowledge.map_shape_live') ?? 'живые'}
        </span>
        <span style={{ display: 'inline-flex', gap: '0.35rem', alignItems: 'center' }}>
          <span style={{ display: 'inline-block', width: '0.55rem', height: '0.55rem', background: '#7aa2f7', transform: 'rotate(45deg)', flexShrink: 0 }} />
          {t('knowledge.map_shape_challenged') ?? 'оспоренные'}
        </span>
        <span style={{ display: 'inline-flex', gap: '0.35rem', alignItems: 'center' }}>
          <span style={{ color: '#f7768e', fontFamily: 'var(--tm-font-mono)', fontWeight: 700, flexShrink: 0 }}>✕</span>
          {t('knowledge.map_shape_dead') ?? 'выбыли'}
        </span>
        <span style={{ marginLeft: 'auto' }}>
          {tier === 1 ? (t('knowledge.map_tier_regions') ?? 'уровень: темы') : tier === 2 ? (t('knowledge.map_tier_sub') ?? 'уровень: подкластеры') : (t('knowledge.map_tier_points') ?? 'уровень: знания')}
        </span>
      </div>
    </div>
  )
}
