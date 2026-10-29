import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { forceCollide, forceLink, forceManyBody, forceSimulation } from 'd3-force'
import { useTheme } from '../theme'
import { drawShape, stateShape } from '../KnowledgeMap'
import type { GraphNode, PositionedNode } from './RelationGraph'
import { RELATION_COLORS } from './RelationGraph'
import { STATE_FILL } from './AreaMap'

/** The relation neighborhood drawn in the semantic map's visual language
 *  (glowing dots on a dark plane, shape = lifecycle verdict, halo captions):
 *  the old concentric SVG rings read as a debug artifact next to the big map.
 *  Positions start from the deterministic ring layout and relax through force
 *  ticks with a soft spring back to their rings — organic, but stable. */

const FALLBACKS: Record<string, string> = {
  '--tm-success': '#3fbf7f',
  '--tm-danger': '#f7768e',
  '--tm-text-3': '#8b93a7',
  '--tm-info': '#4f9cf9',
  '--tm-amber': '#e0af68',
  '--tm-text-2': '#a9b1c6',
  '--tm-text': '#eef1f6',
  '--tm-teal': '#4fb3a9',
}

/** CSS vars are not canvas colors (a var() assignment silently keeps the
 *  previous fill): resolve each against the computed style with a fallback.
 *  Accepts both '--tm-x' and the 'var(--tm-x)' strings the palettes carry. */
function makeColor(): (cssValue: string) => string {
  const style = getComputedStyle(document.documentElement)
  return (cssValue: string) => {
    const name = cssValue.startsWith('var(') ? cssValue.slice(4, -1) : cssValue
    return style.getPropertyValue(name).trim() || FALLBACKS[name] || FALLBACKS['--tm-text-2']
  }
}

/** Force-relax the deterministic ring layout: copies (the simulation mutates
 *  its nodes), link attraction along real edges, gentle springs back to the
 *  ring positions so local tangles untangle without the graph drifting. */
function relax(positioned: PositionedNode[], edges: Array<{ from_id: string; to_id: string }>, width: number, height: number): PositionedNode[] {
  const nodes: Array<PositionedNode & { vx?: number; vy?: number; x0: number; y0: number }> = positioned.map((p) => ({ ...p, x0: p.x, y0: p.y }))
  const byID = new Map(nodes.map((n) => [n.node.id, n]))
  const links = edges
    .map((e) => ({ source: byID.get(e.from_id), target: byID.get(e.to_id) }))
    .filter((l) => l.source && l.target) as Array<{ source: PositionedNode & { vx?: number }; target: PositionedNode & { vx?: number } }>
  if (nodes.length > 1 && links.length > 0) {
    const simulation = forceSimulation(nodes as never)
      .force('link', forceLink(links).distance(84).strength(0.3))
      .force('charge', forceManyBody().strength(-120))
      .force('collide', forceCollide((n: PositionedNode) => n.r + 9))
      .stop()
    for (let tick = 0; tick < 160; tick++) {
      simulation.tick(1)
      for (const n of nodes) {
        const k = 0.06
        n.vx = (n.vx ?? 0) + (n.x0 - n.x) * k
        n.vy = (n.vy ?? 0) + (n.y0 - n.y) * k
        n.x += n.vx
        n.y += n.vy
      }
    }
  }
  void width; void height
  return nodes
}

export default function RelationMapCanvas({ positioned, edges, selected, onSelect }: {
  positioned: PositionedNode[]
  edges: Array<{ from_id: string; to_id: string; type: string; basis?: string }>
  selected: string
  onSelect: (id: string) => void
}) {
  const { resolved: theme } = useTheme()
  const canvasRef = useRef<HTMLCanvasElement>(null)
  const wrapRef = useRef<HTMLDivElement>(null)
  const [hover, setHover] = useState<{ id: string; x: number; y: number } | null>(null)
  const bornAtRef = useRef<Map<string, number>>(new Map())

  const relaxed = useMemo(() => relax(positioned, edges, 0, 0), [positioned, edges])
  const byID = useMemo(() => new Map(relaxed.map((n) => [n.node.id, n])), [relaxed])
  const geoRef = useRef<{ scale: number; ox: number; oy: number }>({ scale: 1, ox: 0, oy: 0 })

  // Birth stagger: each node lights up in sequence on the first draws.
  useEffect(() => {
    const now = performance.now()
    let scheduled = 0
    for (const n of relaxed) {
      if (!bornAtRef.current.has(n.node.id)) {
        bornAtRef.current.set(n.node.id, now + scheduled * 24)
        scheduled++
      }
    }
  }, [relaxed])

  const draw = useCallback(() => {
    const canvas = canvasRef.current
    const wrap = wrapRef.current
    if (!canvas || !wrap) return
    const dpr = window.devicePixelRatio || 1
    const w = wrap.clientWidth
    const h = wrap.clientHeight
    if (w === 0 || h === 0) return
    if (canvas.width !== Math.round(w * dpr) || canvas.height !== Math.round(h * dpr)) {
      canvas.width = Math.round(w * dpr); canvas.height = Math.round(h * dpr)
      canvas.style.width = `${w}px`; canvas.style.height = `${h}px`
    }
    const ctx = canvas.getContext('2d')
    if (!ctx) return
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
    ctx.clearRect(0, 0, w, h)
    const color = makeColor()
    const dark = theme === 'dark'
    const ink = dark ? '#eef1f6' : '#181f2a'
    const halo = dark ? 'rgba(0, 0, 0, 0.85)' : 'rgba(255, 255, 255, 0.95)'
    ctx.fillStyle = dark ? '#12151c' : '#f4f6fa'
    ctx.fillRect(0, 0, w, h)
    if (!relaxed.length) return

    // Fit the relaxed bounding box into the canvas with a margin.
    const pad = 46
    let minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity
    for (const n of relaxed) {
      minX = Math.min(minX, n.x - n.r); minY = Math.min(minY, n.y - n.r)
      maxX = Math.max(maxX, n.x + n.r); maxY = Math.max(maxY, n.y + n.r)
    }
    const bw = Math.max(1, maxX - minX), bh = Math.max(1, maxY - minY)
    const scale = Math.min((w - pad * 2) / bw, (h - pad * 2) / bh, 1.6)
    const ox = (w - bw * scale) / 2 - minX * scale
    const oy = (h - bh * scale) / 2 - minY * scale
    geoRef.current = { scale, ox, oy }

    const now = performance.now()
    let stillFading = false
    const alphaOf = (n: PositionedNode): number => {
      const born = bornAtRef.current.get(n.node.id)
      if (born === undefined) return 1
      const age = now - born
      if (age < 0) { stillFading = true; return 0 }
      if (age < 650) { stillFading = true; return 0.1 + 0.9 * (age / 650) }
      return 1
    }

    // ── edges: relation color, dashed candidates, brighter around selection ──
    for (const edge of edges) {
      const from = byID.get(edge.from_id)
      const to = byID.get(edge.to_id)
      if (!from || !to) continue
      const a = alphaOf(from) * alphaOf(to)
      if (a <= 0.02) continue
      const p1 = { x: from.x * scale + ox, y: from.y * scale + oy }
      const p2 = { x: to.x * scale + ox, y: to.y * scale + oy }
      const highlight = selected === edge.from_id || selected === edge.to_id
      ctx.globalAlpha = a * (highlight ? 0.9 : 0.45)
      ctx.strokeStyle = color(RELATION_COLORS[edge.type])
      ctx.lineWidth = highlight ? 2 : 1.2
      ctx.setLineDash(edge.basis === 'candidate' ? [5, 4] : [])
      ctx.beginPath()
      ctx.moveTo(p1.x, p1.y)
      ctx.lineTo(p2.x, p2.y)
      ctx.stroke()
    }
    ctx.setLineDash([])

    // ── nodes: halo + verdict shape, root ringed — the map's dot language ──
    for (const n of relaxed) {
      const alpha = alphaOf(n)
      if (alpha <= 0.02) continue
      const p = { x: n.x * scale + ox, y: n.y * scale + oy }
      const isRoot = n.node.depth === 0
      const active = n.node.id === selected
      const r = (isRoot ? n.r * 1.3 : n.r) * Math.max(0.7, Math.min(1.5, scale * 1.2))
      const fill = color(STATE_FILL[n.node.state] ?? FALLBACKS['--tm-info'])
      ctx.globalAlpha = alpha * 0.2
      ctx.fillStyle = fill
      ctx.beginPath()
      ctx.arc(p.x, p.y, r * 2.1, 0, Math.PI * 2)
      ctx.fill()
      ctx.globalAlpha = alpha * (active ? 0.95 : isRoot ? 0.85 : 0.72)
      drawShape(ctx, stateShape(n.node.state), p.x, p.y, r)
      if (isRoot || active) {
        ctx.globalAlpha = alpha * 0.9
        ctx.strokeStyle = active ? color('--tm-teal') : ink
        ctx.lineWidth = active ? 2 : 1.2
        ctx.beginPath()
        ctx.arc(p.x, p.y, r + 4, 0, Math.PI * 2)
        ctx.stroke()
      }
      const label = n.node.label.length > 30 ? `${n.node.label.slice(0, 29)}…` : n.node.label
      ctx.font = '10.5px Inter, sans-serif'
      ctx.textAlign = 'center'
      const hot = active || hover?.id === n.node.id
      ctx.globalAlpha = alpha * (hot ? 1 : 0.6)
      for (let pass = 0; pass < 2; pass++) {
        ctx.fillStyle = pass === 0 ? halo : ink
        ctx.fillText(label, p.x + (pass === 0 ? 1 : 0), p.y + r + 12 + (pass === 0 ? 1 : 0))
      }
    }
    ctx.globalAlpha = 1
    if (stillFading) requestAnimationFrame(draw)
  }, [relaxed, byID, edges, selected, hover, theme])

  useEffect(() => {
    const id = requestAnimationFrame(draw)
    return () => cancelAnimationFrame(id)
  }, [draw])

  // Hover pick + click select (screen-space nearest node within a slop).
  const pick = useCallback((event: { clientX: number; clientY: number }): GraphNode | null => {
    const wrap = wrapRef.current
    if (!wrap) return null
    const rect = wrap.getBoundingClientRect()
    const mx = event.clientX - rect.left, my = event.clientY - rect.top
    const { scale, ox, oy } = geoRef.current
    let best: GraphNode | null = null
    let bestDist = 14
    for (const n of relaxed) {
      const dx = n.x * scale + ox - mx, dy = n.y * scale + oy - my
      const dist = Math.hypot(dx, dy)
      if (dist < bestDist) { best = n.node; bestDist = dist }
    }
    return best
  }, [relaxed])

  return (
    <div ref={wrapRef} style={{ position: 'relative', border: '1px solid var(--tm-border)', borderRadius: 'var(--tm-radius-md)', overflow: 'hidden' }}>
      <canvas
        ref={canvasRef}
        style={{ display: 'block', width: '100%', height: 360, cursor: hover ? 'pointer' : 'default' }}
        onMouseMove={(e) => {
          const rect = wrapRef.current?.getBoundingClientRect()
          const hit = pick(e)
          if ((hit?.id ?? '') !== (hover?.id ?? '')) {
            setHover(hit && rect ? { id: hit.id, x: e.clientX - rect.left, y: e.clientY - rect.top } : null)
          } else if (hit && rect) {
            setHover({ id: hit.id, x: e.clientX - rect.left, y: e.clientY - rect.top })
          }
        }}
        onMouseLeave={() => setHover(null)}
        onClick={(e) => {
          const hit = pick(e)
          if (hit) onSelect(hit.id === selected ? '' : hit.id)
        }}
      />
      {hover && (
        <div style={{
          position: 'absolute', left: Math.min(hover.x + 12, (wrapRef.current?.clientWidth ?? 300) - 230), top: hover.y + 12,
          maxWidth: 220, padding: '0.4rem 0.55rem', background: 'var(--tm-elevated)', border: '1px solid var(--tm-border)',
          borderRadius: 8, fontSize: '0.72rem', color: 'var(--tm-text)', pointerEvents: 'none', zIndex: 3,
        }}>
          {byID.get(hover.id)?.node.label}
        </div>
      )}
    </div>
  )
}
