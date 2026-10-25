import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Button, InlineNotification, Tag, TextInput } from '@carbon/react'
import { observationApi } from './observationApi'
import { useT } from './i18n'
import {
  CLUSTER_COLORS,
  STATE_COLORS,
  projectSemanticMap,
  semanticGraph,
  type LayoutNode,
  type MapItem,
  type SemanticCluster,
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
/** Zoom tiers (in effective on-screen scale). */
const FAR = 0.62 // below: cluster bubbles
const NEAR = 1.5 // above: item labels
/** A label is drawn only if no already-drawn label sits within this box. */
const LABEL_BOX_W = 92
const LABEL_BOX_H = 18
const LABEL_MAX = 70

export default function KnowledgeMap({ project, onOpenDossier }: {
  project: string
  onOpenDossier: (id: string) => void
}) {
  const t = useT()
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
  const clustersRef = useRef<SemanticCluster[]>([])

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

  // Re-layout when the dataset changes; the graph skeleton (edges +
  // super-clusters) is computed once here, not per frame.
  useEffect(() => {
    if (!items) return
    if (items.length === 0) { setPoints([]); edgesRef.current = []; clustersRef.current = []; return }
    const layout = projectSemanticMap(items, { k: 6, width: WORLD_W, height: WORLD_H })
    const radius = Math.max(2.2, Math.min(4, 900 / Math.sqrt(layout.nodes.length + 1)))
    setPoints(layout.nodes.map((node) => ({ ...node, r: radius })))
    const graph = semanticGraph(items, 8, 0.6)
    edgesRef.current = graph.edges
    clustersRef.current = graph.clusters
    transformRef.current = { x: 0, y: 0, k: 1 }
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

    // Project + cull once; everything below works in screen pixels, so fonts
    // stay constant-size and dots grow only mildly with zoom.
    const visible: Array<{ p: MapPoint; sx: number; sy: number; active: boolean }> = []
    for (let i = 0; i < points.length; i++) {
      const sx = points[i].x * scale + tx
      const sy = points[i].y * scale + ty
      if (sx < -30 || sx > w + 30 || sy < -30 || sy > h + 30) continue
      visible.push({ p: points[i], sx, sy, active: matches[i] })
    }

    const dotR = Math.max(1.4, Math.min(3.4, points[0]?.r ?? 3)) * Math.min(1.6, Math.max(0.75, scale))

    // ── far plane: topic bubbles instead of a swarm of dots ──
    if (scale < FAR) {
      for (const cluster of clustersRef.current) {
        if (cluster.count < 4) continue
        let cx = 0, cy = 0, matched = 0
        for (const index of cluster.members) {
          cx += points[index].x * scale + tx
          cy += points[index].y * scale + ty
          if (matches[index]) matched++
        }
        cx /= cluster.count; cy /= cluster.count
        if (cx < -120 || cx > w + 120 || cy < -80 || cy > h + 80) continue
        const radius = 12 + Math.sqrt(matched || cluster.count) * 6
        const palette = CLUSTER_COLORS[Math.abs(cluster.label.length * 31 + cluster.count) % CLUSTER_COLORS.length]
        ctx.beginPath()
        ctx.arc(cx, cy, radius, 0, Math.PI * 2)
        ctx.fillStyle = palette
        ctx.globalAlpha = 0.16
        ctx.fill()
        ctx.globalAlpha = 0.55
        ctx.lineWidth = 1.2
        ctx.strokeStyle = palette
        ctx.stroke()
        ctx.globalAlpha = 1
        ctx.fillStyle = palette
        ctx.font = '600 12px Inter, sans-serif'
        ctx.textAlign = 'center'
        const caption = cluster.label || '•'
        ctx.fillText(`${caption} · ${matched || cluster.count}`, cx, cy + 4)
        if (cluster.label) {
          ctx.font = '10px Inter, sans-serif'
          ctx.fillStyle = 'var(--tm-muted, #8a8a8a)'
        }
      }
      // Dots shrink to a quiet texture under the bubbles.
      for (const { sx, sy, active } of visible) {
        ctx.globalAlpha = active ? 0.7 : 0.12
        ctx.beginPath()
        ctx.arc(sx, sy, 1.7, 0, Math.PI * 2)
        ctx.fillStyle = '#7aa2f7'
        ctx.fill()
      }
      ctx.globalAlpha = 1
      return
    }

    // ── kNN skeleton: only from the mid plane up, only visible endpoints ──
    if (scale >= FAR * 0.8) {
      ctx.lineWidth = 1
      ctx.strokeStyle = 'rgba(122, 162, 247, 0.10)'
      const onScreen = new Set(visible.map((entry) => entry.p.id))
      const indexById = new Map(points.map((p, i) => [p.id, i]))
      for (const [a, b] of edgesRef.current) {
        const pa = points[a], pb = points[b]
        if (!onScreen.has(pa.id) || !onScreen.has(pb.id)) continue
        const ai = indexById.get(pa.id)!, bi = indexById.get(pb.id)!
        if (!matches[ai] || !matches[bi]) continue
        ctx.beginPath()
        ctx.moveTo(pa.x * scale + tx, pa.y * scale + ty)
        ctx.lineTo(pb.x * scale + tx, pb.y * scale + ty)
        ctx.stroke()
      }
    }

    // ── dots: cluster color mid, state color near ──
    const near = scale >= NEAR
    for (const { p, sx, sy, active } of visible) {
      const color = near
        ? (STATE_COLORS[p.item.state] ?? '#7aa2f7')
        : (p.cluster > 0 ? CLUSTER_COLORS[p.cluster % CLUSTER_COLORS.length] : '#7aa2f7')
      ctx.globalAlpha = active ? 1 : 0.15
      ctx.beginPath()
      ctx.arc(sx, sy, dotR, 0, Math.PI * 2)
      ctx.fillStyle = color
      ctx.fill()
    }
    ctx.globalAlpha = 1

    // ── near plane: bounded, non-overlapping item labels ──
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
            {state} ({(items ?? []).filter((item) => item.state === state).length})
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
                <Tag size="sm" type={STATE_TAG[hover.point.item.state] ?? 'cool-gray'}>{hover.point.item.state}</Tag>
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
      <div style={{ display: 'flex', gap: '0.75rem', flexWrap: 'wrap', fontSize: '0.7rem', color: 'var(--tm-muted)' }}>
        <span>{t('knowledge.map_hint_zoom') ?? 'Колесо — масштаб: сначала кластеры с подписями, при приближении — отдельные знания. Клик по точке — досье.'}</span>
        <span style={{ marginLeft: 'auto' }}>{t('knowledge.map_hint_labels') ?? 'Подписи появляются, когда на экране есть место.'}</span>
      </div>
    </div>
  )
}
