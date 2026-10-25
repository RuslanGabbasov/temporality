import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Button, InlineNotification, Tag, TextInput } from '@carbon/react'
import { observationApi } from './observationApi'
import { useT } from './i18n'
import { CLUSTER_COLORS, STATE_COLORS, projectSemanticMap, type LayoutNode, type MapItem } from './semanticMap'

/** Semantic knowledge map (docs/human-knowledge-explorer.md §12): the whole
 *  visible knowledge base laid out by embedding similarity — proximity reads
 *  as meaning. Rendered on canvas (dots + kNN edges) with d3-zoom/pan;
 *  labels appear as the zoom level makes room. Click opens the dossier.
 *
 *  The layout math runs once per dataset in projectSemanticMap; this
 *  component is rendering + interaction only. */

interface MapPoint extends LayoutNode {
  r: number
}

const STATE_TAG: Record<string, 'green' | 'blue' | 'red' | 'warm-gray' | 'cool-gray' | 'purple'> = {
  proposed: 'blue', confirmed: 'green', challenged: 'purple', invalidated: 'red', superseded: 'warm-gray', rejected: 'red', corrected: 'cool-gray',
}

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
  const [hover, setHover] = useState<MapPoint | null>(null)
  const transformRef = useRef({ x: 0, y: 0, k: 1 })
  const frameRef = useRef(0)

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

  // Re-layout when the dataset changes.
  useEffect(() => {
    if (!items) return
    if (items.length === 0) { setPoints([]); return }
    // Bounded work: the largest supported corpus settles in well under a
    // second on commodity hardware.
    const layout = projectSemanticMap(items, { k: 6, width: 1400, height: 900 })
    const radius = Math.max(2.6, Math.min(6, 900 / Math.sqrt(layout.nodes.length + 1)))
    setPoints(layout.nodes.map((node) => ({ ...node, r: radius })))
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
    const { x: tx, y: ty, k } = transformRef.current
    ctx.save()
    ctx.translate(tx, ty)
    ctx.scale(k, k)
    const base = points.length ? Math.min(w / 1400, h / 900) : 1
    const matchesArr = matches
    // kNN skeleton: faint links make neighborhoods readable even before dots.
    ctx.lineWidth = 1 / k
    for (let i = 0; i < points.length; i++) {
      if (!matchesArr[i]) continue
      for (let j = i + 1; j < points.length; j++) {
        if (!matchesArr[j]) continue
        const dx = points[i].x - points[j].x, dy = points[i].y - points[j].y
        if (dx * dx + dy * dy > 130 * 130) continue
        ctx.strokeStyle = 'rgba(122, 162, 247, 0.10)'
        ctx.beginPath()
        ctx.moveTo(points[i].x, points[i].y)
        ctx.lineTo(points[j].x, points[j].y)
        ctx.stroke()
      }
    }
    // Labels only when the zoom makes room for them.
    const showLabels = k * base > 1.15
    ctx.textAlign = 'center'
    for (let i = 0; i < points.length; i++) {
      const point = points[i]
      const active = matchesArr[i]
      const color = STATE_COLORS[point.item.state] ?? '#7aa2f7'
      ctx.globalAlpha = active ? 1 : 0.14
      ctx.beginPath()
      ctx.arc(point.x, point.y, point.r, 0, Math.PI * 2)
      ctx.fillStyle = color
      ctx.fill()
      // Cluster ring: the topic palette around the state fill.
      if (point.cluster > 0) {
        ctx.lineWidth = 1.6
        ctx.strokeStyle = CLUSTER_COLORS[point.cluster % CLUSTER_COLORS.length]
        ctx.stroke()
      }
      if (showLabels && active) {
        ctx.globalAlpha = 1
        ctx.fillStyle = 'var(--tm-text, #cfcfcf)'
        ctx.font = `${11 / 1}px Inter, sans-serif`
        const label = point.item.proposition.length > 46 ? `${point.item.proposition.slice(0, 45)}…` : point.item.proposition
        ctx.fillText(label, point.x, point.y - point.r - 5)
      }
    }
    ctx.globalAlpha = 1
    ctx.restore()
  }, [points, matches])

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
      // Hover pick: nearest point within radius in world coordinates.
      const rect = wrap.getBoundingClientRect()
      const transform = transformRef.current
      const mx = (event.clientX - rect.left - transform.x) / transform.k
      const my = (event.clientY - rect.top - transform.y) / transform.k
      let best: MapPoint | null = null
      let bestDist = 14 / transform.k
      for (const point of points) {
        const dx = point.x - mx, dy = point.y - my
        const dist = Math.sqrt(dx * dx + dy * dy)
        if (dist < Math.max(point.r + 3 / transform.k, bestDist)) { best = point; bestDist = dist }
      }
      setHover(best)
    }
    const onUp = () => { dragging = false }
    const onClick = (event: MouseEvent) => {
      const rect = wrap.getBoundingClientRect()
      const transform = transformRef.current
      const mx = (event.clientX - rect.left - transform.x) / transform.k
      const my = (event.clientY - rect.top - transform.y) / transform.k
      let best: MapPoint | null = null
      let bestDist = 14 / transform.k
      for (const point of points) {
        const dx = point.x - mx, dy = point.y - my
        const dist = Math.sqrt(dx * dx + dy * dy)
        if (dist < Math.max(point.r + 3 / transform.k, bestDist)) { best = point; bestDist = dist }
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
  }, [points, draw, onOpenDossier])

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

      <div ref={wrapRef} style={{ position: 'relative', flex: 1, minHeight: '480px', border: '1px solid var(--tm-border)', borderRadius: 'var(--tm-radius-md)', background: 'var(--tm-bg)', overflow: 'hidden', cursor: hover ? 'pointer' : 'grab' }}>
        <canvas ref={canvasRef} style={{ display: 'block' }} />
        {hover && (
          <div style={{
            position: 'absolute', pointerEvents: 'none', maxWidth: '360px',
            background: 'var(--tm-elevated, #1a1a1a)', border: '1px solid var(--tm-border)', borderRadius: '8px',
            padding: '0.5rem 0.65rem', fontSize: '0.76rem', lineHeight: 1.45,
            left: `min(calc(50% ), 50%)`, top: '0.6rem', transform: 'translateX(-50%)',
          }}>
            <div style={{ display: 'flex', gap: '0.35rem', alignItems: 'center', marginBottom: '0.25rem', flexWrap: 'wrap' }}>
              <Tag size="sm" type={STATE_TAG[hover.item.state] ?? 'cool-gray'}>{hover.item.state}</Tag>
              <span style={{ color: 'var(--tm-muted)', fontFamily: 'var(--tm-font-mono)', fontSize: '0.66rem' }}>{hover.item.project}</span>
              {(hover.item.topics ?? []).slice(0, 3).map((topic) => <Tag key={topic} size="sm" type="cool-gray">{topic}</Tag>)}
            </div>
            {hover.item.proposition}
          </div>
        )}
        {loading && <div style={{ position: 'absolute', inset: 0, display: 'grid', placeItems: 'center', color: 'var(--tm-muted)', fontSize: '0.8rem' }}>…</div>}
        {!loading && items !== null && items.length === 0 && (
          <div style={{ position: 'absolute', inset: 0, display: 'grid', placeItems: 'center', color: 'var(--tm-muted)', fontSize: '0.8rem', textAlign: 'center', padding: '1rem' }}>
            {t('knowledge.map_empty') ?? 'Нет векторов знаний для карты: включите эмбеддинги (semantic-слой) и дайте бэкфиллу наполнить базу.'}
          </div>
        )}
      </div>
      <div style={{ display: 'flex', gap: '0.75rem', flexWrap: 'wrap', fontSize: '0.7rem', color: 'var(--tm-muted)' }}>
        <span>{t('knowledge.map_hint_zoom') ?? 'Колесо — масштаб, перетаскивание — панорама, клик по точке — досье.'}</span>
        <span style={{ marginLeft: 'auto' }}>{t('knowledge.map_hint_labels') ?? 'Подписи появляются при увеличении.'}</span>
      </div>
    </div>
  )
}
