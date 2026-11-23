import { useMemo } from 'react'

// Native UI renderers for the extended visualization catalog
// (docs/presentation-engine-ext.md §9 Native UI): React SVG implementations
// mirroring the kernel draw-model semantics — same data, adaptive size, no
// JS-library dependency. The kernel SVG/PNG renderers stay the source of
// truth for static exports; this file renders the same IR interactively.

type Point = { x: number; y: number }

function Frame({ children, viewBox }: { children: React.ReactNode; viewBox: string }) {
  return (
    <figure className="presentation-chart">
      <svg viewBox={viewBox} className="presentation-chart-svg" role="img">{children}</svg>
    </figure>
  )
}

// ── shared scales ───────────────────────────────────────────────────────────

function niceDomain(points: Array<{ x: number; y: number }>, pick: (p: { x: number; y: number }) => number, min?: number, max?: number): [number, number] {
  if (min !== undefined && max !== undefined) return [min, max]
  let lo = Infinity
  let hi = -Infinity
  for (const p of points) {
    const v = pick(p)
    if (v < lo) lo = v
    if (v > hi) hi = v
  }
  if (!Number.isFinite(lo) || !Number.isFinite(hi)) return [0, 1]
  if (lo === hi) {
    const pad = Math.abs(lo) * 0.1 + 1
    lo -= pad
    hi += pad
  }
  const pad = (hi - lo) * 0.08
  if (min === undefined) lo -= pad
  if (max === undefined) hi += pad
  return [lo, hi]
}

const SERIES_COLORS = ['var(--tm-chart-1)', 'var(--tm-chart-2)', 'var(--tm-chart-3)', 'var(--tm-chart-4)', 'var(--tm-chart-5)', 'var(--tm-chart-6)']

const fmt = (n: number): string => {
  if (Math.abs(n) >= 1e6) return `${(n / 1e6).toFixed(1)}M`
  if (Math.abs(n) >= 1e3) return `${(n / 1e3).toFixed(1)}k`
  if (Number.isInteger(n)) return String(n)
  return n.toFixed(2)
}

const clip = (s: string, max: number): string => (s.length > max ? `${s.slice(0, max - 1)}…` : s)

// Heatmap/treemap cells use FIXED rgb ramps that do not follow the theme,
// so their label color is chosen by contrast, not by theme: on these ramps
// black beats white almost everywhere (teal: 8:1 vs 2.6:1), and the old
// `--tm-text-1` branch painted black-on-dark — that variable was never
// defined anywhere (invalid var in a presentation attribute → black).
function textOnCell(r: number, g: number, b: number): string {
  const lin = (c: number) => {
    const s = c / 255
    return s <= 0.03928 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4)
  }
  const luminance = 0.2126 * lin(r) + 0.7152 * lin(g) + 0.0722 * lin(b)
  // L > 0.179 is where black's contrast (L+0.05)/0.05 overtakes white's 1.05/(L+0.05).
  return luminance > 0.179 ? '#0B1412' : '#FFFFFF'
}

// ── Scatter / quadrant -------------------------------------------------------

type ScatterPointIR = { x: number; y: number; label?: string; group?: string; size?: number; highlight?: boolean; note?: string; ref?: { type: string; id: string } }
type RefLineIR = { axis: 'x' | 'y'; at: number; label?: string }

export function ScatterView({ component }: { component: { data?: unknown; props?: unknown; type?: string } }) {
  const data = (component.data ?? {}) as { points?: ScatterPointIR[]; x_title?: string; y_title?: string; x_min?: number; x_max?: number; y_min?: number; y_max?: number }
  const props = (component.props ?? {}) as { title?: string; showValues?: boolean; showLegend?: boolean; quadrant?: boolean; quadrantLabels?: string[]; refLines?: RefLineIR[] }
  const points = data.points ?? []
  if (points.length === 0) return <PlaceholderLike what="scatter without points" />
  const W = 560
  const H = 320
  const ml = 46
  const mb = 38
  const plotW = W - ml - 16
  const plotH = H - mb - 14
  const [x0, x1] = niceDomain(points, (p) => p.x, data.x_min, data.x_max)
  const [y0v, y1v] = niceDomain(points, (p) => p.y, data.y_min, data.y_max)
  const px = (x: number) => ml + plotW * ((x - x0) / (x1 - x0))
  const py = (y: number) => 14 + plotH * ((y1v - y) / (y1v - y0v))
  const groups: string[] = []
  for (const p of points) if (p.group && !groups.includes(p.group)) groups.push(p.group)
  const colorOf = (g?: string) => (g ? SERIES_COLORS[groups.indexOf(g) % SERIES_COLORS.length] : SERIES_COLORS[0])
  const ticks = [0, 0.25, 0.5, 0.75, 1]

  return (
    <figure className="presentation-chart">
      {props.title ? <figcaption className="presentation-chart-title">{props.title}</figcaption> : null}
      <svg viewBox={`0 0 ${W} ${H}`} className="presentation-chart-svg" role="img">
        {ticks.map((t) => (
          <g key={`gx${t}`}>
            {t > 0 && t < 1 && <line x1={ml + plotW * t} y1={14} x2={ml + plotW * t} y2={14 + plotH} stroke="var(--tm-border)" strokeWidth={0.5} strokeDasharray="2 3" />}
            <text x={ml + plotW * t} y={14 + plotH + 14} textAnchor="middle" fontSize={9.5} fill="var(--tm-text-3)">{fmt(x0 + (x1 - x0) * t)}</text>
          </g>
        ))}
        {ticks.map((t) => (
          <g key={`gy${t}`}>
            {t > 0 && t < 1 && <line x1={ml} y1={14 + plotH * t} x2={ml + plotW} y2={14 + plotH * t} stroke="var(--tm-border)" strokeWidth={0.5} strokeDasharray="2 3" />}
            <text x={ml - 6} y={14 + plotH * t + 3} textAnchor="end" fontSize={9.5} fill="var(--tm-text-3)">{fmt(y1v - (y1v - y0v) * t)}</text>
          </g>
        ))}
        <line x1={ml} y1={14} x2={ml} y2={14 + plotH} stroke="var(--tm-border)" />
        <line x1={ml} y1={14 + plotH} x2={ml + plotW} y2={14 + plotH} stroke="var(--tm-border)" />
        {data.y_title && <text x={0} y={24} fontSize={9.5} fill="var(--tm-text-3)">{clip(data.y_title, 12)}</text>}
        {data.x_title && <text x={ml + plotW / 2} y={14 + plotH + 30} textAnchor="middle" fontSize={9.5} fill="var(--tm-text-3)">{data.x_title}</text>}
        {props.quadrant && (
          <g>
            <line x1={ml} y1={14 + plotH / 2} x2={ml + plotW} y2={14 + plotH / 2} stroke="var(--tm-border)" strokeWidth={1.2} />
            <line x1={ml + plotW / 2} y1={14} x2={ml + plotW / 2} y2={14 + plotH} stroke="var(--tm-border)" strokeWidth={1.2} />
            {[
              [ml + 6, 26, 'start'],
              [ml + plotW - 6, 26, 'end'],
              [ml + 6, 14 + plotH - 6, 'start'],
              [ml + plotW - 6, 14 + plotH - 6, 'end'],
            ].map(([x, y, anchor], i) => props.quadrantLabels?.[i] ? (
              <text key={i} x={x as number} y={y as number} textAnchor={anchor as 'start' | 'end' | 'middle'} fontSize={10} fontWeight={600} fill="var(--tm-text-3)">{clip(props.quadrantLabels[i], 26)}</text>
            ) : null)}
          </g>
        )}
        {(props.refLines ?? []).map((line, i) => {
          if (line.axis === 'x' && line.at >= x0 && line.at <= x1) {
            const fx = px(line.at)
            return (
              <g key={`rl${i}`}>
                <line x1={fx} y1={14} x2={fx} y2={14 + plotH} stroke="var(--tm-amber, #e6b85c)" strokeWidth={1.2} strokeDasharray="4 3" />
                {line.label && <text x={fx + 3} y={24} fontSize={9} fill="var(--tm-text-3)">{line.label}</text>}
              </g>
            )
          }
          if (line.axis === 'y' && line.at >= y0v && line.at <= y1v) {
            const fy = py(line.at)
            return (
              <g key={`rl${i}`}>
                <line x1={ml} y1={fy} x2={ml + plotW} y2={fy} stroke="var(--tm-amber, #e6b85c)" strokeWidth={1.2} strokeDasharray="4 3" />
                {line.label && <text x={ml + plotW - 3} y={fy - 4} textAnchor="end" fontSize={9} fill="var(--tm-text-3)">{line.label}</text>}
              </g>
            )
          }
          return null
        })}
        {points.map((p, i) => {
          const cx = px(p.x)
          const cy = py(p.y)
          const r = 3 + (p.size ?? 0) * 1.2 + (p.highlight ? 1.5 : 0)
          const anchor = cx > ml + plotW * 0.72 ? 'end' : 'start'
          const lx = anchor === 'end' ? cx - r - 3 : cx + r + 3
          const label = (props.showValues !== false && points.length <= 40 && p.label) ? clip(p.note && points.length <= 20 ? `${p.label} — ${p.note}` : p.label, 26) : null
          return (
            <g key={i}>
              {p.highlight && <circle cx={cx} cy={cy} r={r + 2.5} fill={colorOf(p.group)} opacity={0.3} />}
              <circle cx={cx} cy={cy} r={r} fill={colorOf(p.group)} />
              {p.ref && <title>{p.ref.type} {p.ref.id}</title>}
              {label && <text x={lx} y={cy - 4} textAnchor={anchor} fontSize={9.5} fontWeight={p.highlight ? 600 : 400} fill="var(--tm-text)">{label}</text>}
            </g>
          )
        })}
        {(props.showLegend !== false && groups.length > 1) && groups.map((g, i) => (
          <g key={g} transform={`translate(${16 + i * 110}, ${H - 8})`}>
            <circle cx={5} cy={-3} r={4.5} fill={SERIES_COLORS[i % SERIES_COLORS.length]} />
            <text x={14} y={0} fontSize={10} fill="var(--tm-text)">{clip(g, 16)}</text>
          </g>
        ))}
      </svg>
    </figure>
  )
}

// ── Heatmap -----------------------------------------------------------------

export function HeatmapView({ component }: { component: { data?: unknown; props?: unknown } }) {
  const data = (component.data ?? {}) as { rows?: string[]; cols?: string[]; values?: Array<Array<number | null>>; unit?: string; min?: number; max?: number }
  const props = (component.props ?? {}) as { title?: string; showValues?: boolean }
  const rows = data.rows ?? []
  const cols = data.cols ?? []
  if (rows.length === 0 || cols.length === 0) return <PlaceholderLike what="heatmap without rows or columns" />
  let lo = Infinity
  let hi = -Infinity
  for (const row of data.values ?? []) for (const cell of row) {
    if (cell === null || cell === undefined) continue
    if (cell < lo) lo = cell
    if (cell > hi) hi = cell
  }
  if (data.min !== undefined) lo = data.min
  if (data.max !== undefined) hi = data.max
  if (!Number.isFinite(lo) || !Number.isFinite(hi) || lo >= hi) { lo = 0; hi = 1 }
  const W = 560
  const rowLabelW = 96
  const cellH = 26
  const colW = Math.max(28, (W - rowLabelW) / cols.length)
  const H = 26 + rows.length * cellH + 22
  const blend = (cell: number): [number, number, number] => {
    const t = Math.max(0, Math.min(1, (cell - lo) / (hi - lo)))
    const base = [122, 138, 148]
    const hot = [45, 178, 165]
    const neg = [74, 111, 212]
    const mix = cell < 0 && lo < 0 ? neg : hot
    const tt = cell < 0 && lo < 0 ? Math.min(1, -t) : t
    const r = Math.round(base[0] + (mix[0] - base[0]) * tt)
    const g = Math.round(base[1] + (mix[1] - base[1]) * tt)
    const b = Math.round(base[2] + (mix[2] - base[2]) * tt)
    return [r, g, b]
  }
  return (
    <figure className="presentation-chart">
      {props.title ? <figcaption className="presentation-chart-title">{props.title}</figcaption> : null}
      <svg viewBox={`0 0 ${W} ${H}`} className="presentation-chart-svg" role="img">
        {cols.map((col, i) => (
          <text key={col + i} x={rowLabelW + colW * (i + 0.5)} y={14} textAnchor="middle" fontSize={9.5} fill="var(--tm-text-3)">{clip(col, Math.floor(colW / 6))}</text>
        ))}
        {rows.map((row, r) => (
          <g key={row + r}>
            <text x={rowLabelW - 6} y={26 + cellH * r + cellH / 2 + 3} textAnchor="end" fontSize={9.5} fill="var(--tm-text-3)">{clip(row, 14)}</text>
            {cols.map((col, c) => {
              const cell = data.values?.[r]?.[c]
              const x = rowLabelW + colW * c
              const y = 26 + cellH * r
              if (cell === null || cell === undefined) {
                return (
                  <g key={col + c}>
                    <rect x={x + 1} y={y + 1} width={colW - 2} height={cellH - 2} rx={2} fill="var(--tm-surface)" />
                    <text x={x + colW / 2} y={y + cellH / 2 + 3} textAnchor="middle" fontSize={9.5} fill="var(--tm-text-3)">—</text>
                  </g>
                )
              }
              const [cr, cg, cb] = blend(cell)
              const fill = `rgb(${cr},${cg},${cb})`
              return (
                <g key={col + c}>
                  <rect x={x + 1} y={y + 1} width={colW - 2} height={cellH - 2} rx={2} fill={fill} />
                  {props.showValues !== false && (
                    <text x={x + colW / 2} y={y + cellH / 2 + 3} textAnchor="middle" fontSize={9.5} fill={textOnCell(cr, cg, cb)}>{fmt(cell)}</text>
                  )}
                </g>
              )
            })}
          </g>
        ))}
        {data.unit && <text x={0} y={H - 4} fontSize={9} fill="var(--tm-text-3)">{data.unit}: {fmt(lo)} … {fmt(hi)}</text>}
      </svg>
    </figure>
  )
}

// ── Histogram ---------------------------------------------------------------

export function HistogramView({ component }: { component: { data?: unknown; props?: unknown } }) {
  const data = (component.data ?? {}) as { values?: number[]; bins?: number; bin_width?: number; unit?: string; x_title?: string; y_title?: string }
  const props = (component.props ?? {}) as { title?: string; normalized?: boolean; showValues?: boolean }
  const values = data.values ?? []
  if (values.length === 0) return <PlaceholderLike what="histogram without samples" />
  let lo = Math.min(...values)
  let hi = Math.max(...values)
  if (lo === hi) { const pad = Math.abs(lo) * 0.1 + 1; lo -= pad; hi += pad }
  let bins = data.bins ?? 0
  if (data.bin_width && data.bin_width > 0) bins = Math.max(1, Math.min(60, Math.ceil((hi - lo) / data.bin_width)))
  if (bins <= 0) bins = Math.min(60, Math.max(4, Math.ceil(Math.log2(values.length)) + 1))
  const binW = (hi - lo) / bins
  const counts = new Array(bins).fill(0)
  for (const v of values) {
    let idx = Math.floor((v - lo) / binW)
    if (idx < 0) idx = 0
    if (idx >= bins) idx = bins - 1
    counts[idx]++
  }
  const peak = props.normalized ? values.length : Math.max(...counts)
  const W = 560
  const H = 260
  const ml = 46
  const plotW = W - ml - 16
  const plotH = H - 60
  const slotW = plotW / bins
  return (
    <figure className="presentation-chart">
      {props.title ? <figcaption className="presentation-chart-title">{props.title}</figcaption> : null}
      <svg viewBox={`0 0 ${W} ${H}`} className="presentation-chart-svg" role="img">
        {[0, 0.25, 0.5, 0.75, 1].map((t) => (
          <g key={t}>
            {t > 0 && t < 1 && <line x1={ml} y1={16 + plotH * t} x2={ml + plotW} y2={16 + plotH * t} stroke="var(--tm-border)" strokeWidth={0.5} strokeDasharray="2 3" />}
            <text x={ml - 6} y={16 + plotH * t + 3} textAnchor="end" fontSize={9.5} fill="var(--tm-text-3)">{fmt(peak * (1 - t))}</text>
          </g>
        ))}
        <line x1={ml} y1={16} x2={ml} y2={16 + plotH} stroke="var(--tm-border)" />
        <line x1={ml} y1={16 + plotH} x2={ml + plotW} y2={16 + plotH} stroke="var(--tm-border)" />
        {counts.map((count, i) => {
          const h = plotH * (count / Math.max(peak, 1))
          const x = ml + slotW * i
          return (
            <g key={i}>
              {h > 0 && <rect x={x + 1} y={16 + plotH - h} width={slotW - 2} height={h} fill="var(--tm-chart-1)" />}
              {props.showValues !== false && slotW >= 26 && count > 0 && (
                <text x={x + slotW / 2} y={16 + plotH - h - 4} textAnchor="middle" fontSize={9.5} fill="var(--tm-text-3)">
                  {props.normalized ? `${((count / values.length) * 100).toFixed(1)}%` : String(count)}
                </text>
              )}
              {(i === 0 || i === bins - 1 || i % (Math.floor(bins / 4) + 1) === 0) && (
                <text x={x} y={16 + plotH + 14} textAnchor="middle" fontSize={9} fill="var(--tm-text-3)">{fmt(lo + binW * i)}</text>
              )}
            </g>
          )
        })}
        <text x={ml + plotW} y={16 + plotH + 14} textAnchor="middle" fontSize={9} fill="var(--tm-text-3)">{fmt(hi)}</text>
        {data.x_title && <text x={ml + plotW / 2} y={16 + plotH + 30} textAnchor="middle" fontSize={9.5} fill="var(--tm-text-3)">{data.x_title}</text>}
        {data.y_title && <text x={0} y={26} fontSize={9.5} fill="var(--tm-text-3)">{clip(data.y_title, 12)}</text>}
        {data.unit && <text x={ml + plotW} y={H - 4} textAnchor="end" fontSize={9} fill="var(--tm-text-3)">{data.unit}</text>}
      </svg>
    </figure>
  )
}

// ── Radar -------------------------------------------------------------------

export function RadarView({ component }: { component: { data?: unknown; props?: unknown } }) {
  const data = (component.data ?? {}) as { axes?: string[]; series?: Array<{ name?: string; values?: number[] }>; min?: number; max?: number; unit?: string }
  const props = (component.props ?? {}) as { title?: string; showValues?: boolean; showLegend?: boolean; highlight?: string }
  const axes = data.axes ?? []
  const series = data.series ?? []
  if (axes.length < 3 || series.length === 0) return <PlaceholderLike what="radar needs 3+ axes and a series" />
  let lo = data.min ?? 0
  let hi = data.max ?? Math.max(...series.flatMap((s) => s.values ?? [0]))
  if (hi <= lo) hi = lo + 1
  const size = 340
  const cx = size / 2
  const cy = size / 2 + 6
  const R = 96
  const point = (axis: number, value: number): Point => {
    const angle = -Math.PI / 2 + (2 * Math.PI * axis) / axes.length
    const t = Math.max(0, Math.min(1, (value - lo) / (hi - lo)))
    return { x: cx + Math.cos(angle) * R * t, y: cy + Math.sin(angle) * R * t }
  }
  const ring = (frac: number): Point[] => axes.map((_, a) => {
    const angle = -Math.PI / 2 + (2 * Math.PI * a) / axes.length
    return { x: cx + Math.cos(angle) * R * frac, y: cy + Math.sin(angle) * R * frac }
  })
  return (
    <figure className="presentation-chart">
      {props.title ? <figcaption className="presentation-chart-title">{props.title}</figcaption> : null}
      <svg viewBox={`0 0 ${size} ${size + (props.showLegend !== false && series.length > 1 ? 26 : 0)}`} className="presentation-chart-svg presentation-chart-radar" role="img">
        {[0.25, 0.5, 0.75, 1].map((f) => (
          <polygon key={f} points={ring(f).map((p) => `${p.x},${p.y}`).join(' ')} fill="none" stroke="var(--tm-border)" strokeWidth={0.6} />
        ))}
        {axes.map((axis, a) => {
          const edge = point(a, hi)
          const angle = -Math.PI / 2 + (2 * Math.PI * a) / axes.length
          const lx = cx + Math.cos(angle) * (R + 14)
          const ly = cy + Math.sin(angle) * (R + 14)
          const anchor = Math.cos(angle) > 0.3 ? 'start' : Math.cos(angle) < -0.3 ? 'end' : 'middle'
          return (
            <g key={axis + a}>
              <line x1={cx} y1={cy} x2={edge.x} y2={edge.y} stroke="var(--tm-border)" strokeWidth={0.6} />
              <text x={lx} y={ly + 3} textAnchor={anchor} fontSize={10} fill="var(--tm-text-3)">{clip(axis, 16)}</text>
            </g>
          )
        })}
        {series.map((s, si) => {
          const color = props.highlight === s.name ? 'var(--tm-chart-1)' : SERIES_COLORS[(si + 1) % SERIES_COLORS.length]
          const pts = (s.values ?? []).map((v, a) => point(a, v))
          return (
            <g key={s.name ?? si}>
              <polygon points={pts.map((p) => `${p.x},${p.y}`).join(' ')} fill={color} opacity={props.highlight === s.name ? 0.38 : 0.18} />
              <polygon points={pts.map((p) => `${p.x},${p.y}`).join(' ')} fill="none" stroke={color} strokeWidth={props.highlight === s.name ? 2.4 : 1.6} />
              {pts.map((p, a) => (
                <g key={a}>
                  <circle cx={p.x} cy={p.y} r={props.highlight === s.name ? 2.6 : 2} fill={color} />
                  {props.showValues && <text x={p.x} y={p.y - 5} textAnchor="middle" fontSize={9} fill="var(--tm-text-3)">{fmt(s.values?.[a] ?? 0)}</text>}
                </g>
              ))}
            </g>
          )
        })}
        {props.showLegend !== false && series.length > 1 && series.map((s, si) => (
          <g key={s.name ?? si} transform={`translate(${14 + si * 120}, ${size + 14})`}>
            <circle cx={5} cy={-3} r={4.5} fill={props.highlight === s.name ? 'var(--tm-chart-1)' : SERIES_COLORS[(si + 1) % SERIES_COLORS.length]} />
            <text x={14} y={0} fontSize={10} fontWeight={props.highlight === s.name ? 600 : 400} fill="var(--tm-text)">{clip(s.name ?? `series ${si + 1}`, 16)}</text>
          </g>
        ))}
        {data.unit && <text x={size - 4} y={size - 4} textAnchor="end" fontSize={9} fill="var(--tm-text-3)">{data.unit}</text>}
      </svg>
    </figure>
  )
}

// ── Sankey ------------------------------------------------------------------

export function SankeyView({ component }: { component: { data?: unknown; props?: unknown } }) {
  const data = (component.data ?? {}) as { nodes?: Array<{ id: string; label?: string; group?: string }>; links?: Array<{ source: string; target: string; value: number; label?: string }>; unit?: string }
  const props = (component.props ?? {}) as { title?: string; showValues?: boolean }
  const nodes = data.nodes ?? []
  const links = data.links ?? []
  if (nodes.length === 0 || links.length === 0) return <PlaceholderLike what="sankey without flows" />
  const W = 560
  const rowGap = 7
  const nodeW = 12
  const byID = new Map(nodes.map((n) => [n.id, n]))
  const inSum = new Map<string, number>()
  const outSum = new Map<string, number>()
  for (const l of links) {
    outSum.set(l.source, (outSum.get(l.source) ?? 0) + l.value)
    inSum.set(l.target, (inSum.get(l.target) ?? 0) + l.value)
  }
  const column = (id: string, seen = new Set<string>()): number => {
    if (seen.has(id)) return 0
    seen.add(id)
    let best = 0
    for (const l of links) if (l.target === id) best = Math.max(best, column(l.source, seen) + 1)
    return best
  }
  const colOf = new Map<string, number>()
  let maxCol = 0
  for (const n of nodes) {
    const c = column(n.id)
    colOf.set(n.id, c)
    if (c > maxCol) maxCol = c
  }
  const columns: string[][] = Array.from({ length: maxCol + 1 }, () => [])
  for (const n of nodes) columns[colOf.get(n.id) ?? 0].push(n.id)
  const decl = new Map(nodes.map((n, i) => [n.id, i]))
  let scale = 0
  for (const col of columns) {
    const volume = col.reduce((sum, id) => sum + Math.max(inSum.get(id) ?? 0, outSum.get(id) ?? 0, 0.001), 0)
    const gaps = rowGap * (col.length + 1)
    const s = (240 - gaps) / Math.max(volume, 0.001)
    if (scale === 0 || s < scale) scale = s
  }
  if (!Number.isFinite(scale) || scale <= 0) scale = 1
  const colGap = Math.min(150, (W - 16 - nodeW) / (maxCol + 1))
  const layout = new Map<string, { x: number; y: number; h: number }>()
  columns.forEach((col, ci) => {
    const sorted = [...col].sort((a, b) => (decl.get(a) ?? 0) - (decl.get(b) ?? 0))
    let y = 8 + rowGap
    for (const id of sorted) {
      const h = Math.max(6, Math.max(inSum.get(id) ?? 0, outSum.get(id) ?? 0, 0.001) * scale)
      layout.set(id, { x: 8 + ci * colGap, y, h })
      y += h + rowGap
    }
  })
  const H = Math.max(...[...layout.values()].map((p) => p.y + p.h), 100) + 16
  const colorOf = (id: string) => SERIES_COLORS[nodes.findIndex((n) => n.id === id) % SERIES_COLORS.length]
  return (
    <figure className="presentation-chart">
      {props.title ? <figcaption className="presentation-chart-title">{props.title}</figcaption> : null}
      <svg viewBox={`0 0 ${W} ${H}`} className="presentation-chart-svg" role="img">
        {[...links].sort((a, b) => b.value - a.value).map((link, i) => {
          const src = layout.get(link.source)
          const dst = layout.get(link.target)
          if (!src || !dst) return null
          const srcOut = Math.max(outSum.get(link.source) ?? 0.001, 0.001)
          const dstIn = Math.max(inSum.get(link.target) ?? 0.001, 0.001)
          let srcOff = 0
          let dstOff = 0
          for (const other of links) {
            if (other.source === link.source && other.value < link.value) srcOff += other.value
            if (other.target === link.target && other.value < link.value) dstOff += other.value
          }
          const x0 = src.x + nodeW
          const x1 = dst.x
          const y0 = src.y + (srcOff / srcOut) * src.h
          const y1 = dst.y + (dstOff / dstIn) * dst.h
          const t0 = (link.value / srcOut) * src.h
          const t1 = (link.value / dstIn) * dst.h
          const steps = 10
          const top: string[] = []
          const bottom: string[] = []
          for (let s = 0; s <= steps; s++) {
            const t = s / steps
            const ease = t * t * (3 - 2 * t)
            const mx = x0 + (x1 - x0) * t
            top.push(`${mx},${y0 + (y1 - y0) * ease}`)
            bottom.push(`${mx},${y0 + t0 + (y1 + t1 - (y0 + t0)) * ease}`)
          }
          return (
            <polygon key={`l${i}`} points={[...top, ...bottom.reverse()].join(' ')} fill={colorOf(link.source)} opacity={0.45}>
              <title>{`${byID.get(link.source)?.label ?? link.source} → ${byID.get(link.target)?.label ?? link.target}: ${fmt(link.value)}${data.unit ? ' ' + data.unit : ''}`}</title>
            </polygon>
          )
        })}
        {nodes.map((node, i) => {
          const pos = layout.get(node.id)
          if (!pos) return null
          return (
            <g key={node.id}>
              <rect x={pos.x} y={pos.y} width={nodeW} height={pos.h} rx={2} fill={SERIES_COLORS[i % SERIES_COLORS.length]}>
                <title>{node.label ?? node.id}</title>
              </rect>
              <text x={pos.x - 4} y={pos.y + pos.h / 2 + 3} textAnchor="end" fontSize={10} fill="var(--tm-text)">{clip(node.label ?? node.id, 16)}</text>
              {props.showValues !== false && (
                <text x={pos.x + nodeW + 4} y={pos.y + pos.h / 2 + 3} textAnchor="start" fontSize={9} fill="var(--tm-text-3)">
                  {fmt(Math.max(inSum.get(node.id) ?? 0, outSum.get(node.id) ?? 0))}{data.unit ? ` ${data.unit}` : ''}
                </text>
              )}
            </g>
          )
        })}
      </svg>
    </figure>
  )
}

// ── Swimlane edge routing ─────────────────────────────────────────────────
// Orthogonal router with obstacle checks: candidate paths (straight line,
// staircase through the lane gap, column channels) are tried in order and
// the first collision-free one wins. The old fixed midpoint elbow cut
// straight through boxes whenever the target sat in the same column or to
// the left of the source — the phantom «inner rectangles» on every node.

export type SwimlaneRect = { x: number; y: number; w: number; h: number; lane: number; c0: number; c1: number }
export type SwimlaneGeom = { labelW: number; marginX: number; colW: number; laneH: number; top0: number }
type EdgeDir = 'right' | 'left' | 'up' | 'down'
type Pt = [number, number]

function swDirOf(pts: Pt[]): EdgeDir {
  const last = pts[pts.length - 1]
  const prev = pts[pts.length - 2]
  const dx = last[0] - prev[0]
  const dy = last[1] - prev[1]
  if (Math.abs(dx) >= Math.abs(dy)) return dx >= 0 ? 'right' : 'left'
  return dy >= 0 ? 'down' : 'up'
}

function swLabelAt(pts: Pt[]): Pt {
  // Label sits beside the longest segment — usually the vertical channel,
  // which is free of boxes — never across the whole path.
  let bi = 1
  let bl = -1
  for (let i = 1; i < pts.length; i++) {
    const len = Math.abs(pts[i][0] - pts[i - 1][0]) + Math.abs(pts[i][1] - pts[i - 1][1])
    if (len > bl) { bl = len; bi = i }
  }
  const [x1, y1] = pts[bi - 1]
  const [x2, y2] = pts[bi]
  if (Math.abs(y2 - y1) > Math.abs(x2 - x1)) return [(x1 + x2) / 2 + 4, (y1 + y2) / 2]
  return [(x1 + x2) / 2, (y1 + y2) / 2 - 4]
}

export function swArrowPoints(x: number, y: number, dir: EdgeDir): string {
  switch (dir) {
    case 'left': return `${x},${y} ${x + 5.5},${y - 3.5} ${x + 5.5},${y + 3.5}`
    case 'up': return `${x},${y} ${x - 3.5},${y + 5.5} ${x + 3.5},${y + 5.5}`
    case 'down': return `${x},${y} ${x - 3.5},${y - 5.5} ${x + 3.5},${y - 5.5}`
    default: return `${x},${y} ${x - 5.5},${y - 3.5} ${x - 5.5},${y + 3.5}`
  }
}

/** Route one swimlane edge around the boxes. Exported for tests. */
export function routeSwimlaneEdge(
  a: SwimlaneRect, b: SwimlaneRect, geom: SwimlaneGeom, rects: Map<string, SwimlaneRect>,
): { pts: Pt[]; dir: EdgeDir; label: Pt } {
  const { labelW, marginX, colW, laneH, top0 } = geom
  const boundary = (k: number) => labelW + marginX + colW * k
  const aCy = a.y + a.h / 2
  const bCy = b.y + b.h / 2
  const aCx = a.x + a.w / 2
  const bCx = b.x + b.w / 2
  const clear = (p: Pt[]): boolean => {
    for (let i = 1; i < p.length; i++) {
      const minX = Math.min(p[i - 1][0], p[i][0])
      const maxX = Math.max(p[i - 1][0], p[i][0])
      const minY = Math.min(p[i - 1][1], p[i][1])
      const maxY = Math.max(p[i - 1][1], p[i][1])
      for (const r of rects.values()) {
        if (r === a || r === b) continue
        if (maxX > r.x + 1 && minX < r.x + r.w - 1 && maxY > r.y + 1 && minY < r.y + r.h - 1) return false
      }
    }
    return true
  }
  const cands: Pt[][] = []
  const push = (p: Pt[]) => { if (p.length > 1) cands.push(p) }

  if (a.lane === b.lane) {
    // Same lane: straight across, or over the top when a node sits between.
    if (b.x >= a.x + a.w) push([[a.x + a.w, aCy], [b.x, bCy]])
    else if (b.x + b.w <= a.x) push([[a.x, aCy], [b.x + b.w, bCy]])
    const stripY = top0 + laneH * a.lane + 4 // the lane's top padding is always free
    push([[aCx, a.y], [aCx, stripY], [bCx, stripY], [bCx, b.y]])
  } else {
    // Different lanes.
    const oL = Math.max(a.x, b.x)
    const oR = Math.min(a.x + a.w, b.x + b.w)
    if (oL < oR) {
      // Same column family: straight vertical.
      const cx = (oL + oR) / 2
      if (b.y > a.y) push([[cx, a.y + a.h], [cx, b.y]])
      else push([[cx, a.y], [cx, b.y + b.h]])
    }
    // Staircase through the gap between the two lanes.
    const gapY = b.y > a.y ? (a.y + a.h + b.y) / 2 : (b.y + b.h + a.y) / 2
    if (b.y > a.y) push([[aCx, a.y + a.h], [aCx, gapY], [bCx, gapY], [bCx, b.y]])
    else push([[aCx, a.y], [aCx, gapY], [bCx, gapY], [bCx, b.y + b.h]])
    // Short exits through the gutter right after the source's column.
    if (a.c1 + 1 <= b.c0) {
      for (let k = a.c1 + 1; k <= b.c0; k++) {
        const ch = boundary(k)
        push([[a.x + a.w, aCy], [ch, aCy], [ch, bCy], [b.x, bCy]])
      }
    }
    if (b.c1 + 1 <= a.c0) {
      for (let k = b.c1 + 1; k <= a.c0; k++) {
        const ch = boundary(k)
        push([[a.x, aCy], [ch, aCy], [ch, bCy], [b.x + b.w, bCy]])
      }
    }
    // Corridor route: horizontal runs live in the band BETWEEN lanes (always
    // free of boxes), only the vertical crosses lanes — at a column boundary
    // channel. Works for any lane distance; the channel is checked below.
    const bandBelow = (l: number) => top0 + laneH * (l + 1)
    const bandAbove = (l: number) => top0 + laneH * l
    if (b.y > a.y) {
      const y1 = bandBelow(a.lane)
      const y2 = bandAbove(b.lane)
      for (let k = Math.min(a.c0, b.c0); k <= Math.max(a.c1, b.c1) + 1; k++) {
        const ch = boundary(k)
        push([[aCx, a.y + a.h], [aCx, y1], [ch, y1], [ch, y2], [bCx, y2], [bCx, b.y]])
      }
    } else {
      const y1 = bandAbove(a.lane)
      const y2 = bandBelow(b.lane)
      for (let k = Math.min(a.c0, b.c0); k <= Math.max(a.c1, b.c1) + 1; k++) {
        const ch = boundary(k)
        push([[aCx, a.y], [aCx, y1], [ch, y1], [ch, y2], [bCx, y2], [bCx, b.y + b.h]])
      }
    }
  }

  const pts = cands.find(clear) ?? (() => {
    // Last resort: the legacy midpoint elbow (rare — only when every
    // candidate is blocked by spanning nodes).
    const x0 = a.x + a.w
    const x1 = b.x
    const midX = (x0 + x1) / 2
    return [[x0, aCy], [midX, aCy], [midX, bCy], [x1, bCy]] as Pt[]
  })()
  return { pts, dir: swDirOf(pts), label: swLabelAt(pts) }
}

// ── Swimlane ----------------------------------------------------------------

type SwimlaneNodeIR = { id: string; lane: string; label: string; status?: string; note?: string; start?: number; span?: number; ref?: { type: string; id: string } }

export function SwimlaneView({ component }: { component: { data?: unknown; props?: unknown } }) {
  const data = (component.data ?? {}) as { lanes?: Array<{ id: string; label?: string }>; nodes?: SwimlaneNodeIR[]; edges?: Array<{ from: string; to: string; label?: string; condition?: string; dashed?: boolean }> }
  const props = (component.props ?? {}) as { title?: string }
  const lanes = data.lanes ?? []
  const nodes = data.nodes ?? []
  if (lanes.length === 0 || nodes.length === 0) return <PlaceholderLike what="swimlane without lanes or nodes" />
  const laneH = 64
  const labelW = 96
  const marginX = 8
  const laneIndex = new Map(lanes.map((l, i) => [l.id, i]))
  const byID = new Map(nodes.map((n) => [n.id, n]))
  // Automatic stage packing per lane when start is not set.
  const cursor = new Map<string, number>()
  let maxStage = 0
  for (const node of nodes) {
    if (node.start === undefined) {
      const s = cursor.get(node.lane) ?? 0
      node.start = s
      cursor.set(node.lane, s + 1)
    }
    maxStage = Math.max(maxStage, node.start)
  }
  let cols = maxStage + 1
  for (const node of nodes) cols = Math.max(cols, (node.start ?? 0) + (node.span ?? 1))
  const W = 560
  const colW = Math.max(64, (W - labelW - marginX * 2) / cols)
  const H = 10 + laneH * lanes.length + 10
  const statusColor = (status?: string): string => {
    switch (status) {
      case 'success': case 'done': case 'completed': return 'var(--tm-success, #9ece6a)'
      case 'danger': case 'failed': case 'blocked': return 'var(--tm-danger, #f7768e)'
      case 'warning': case 'pending': case 'waiting': return 'var(--tm-amber, #e6b85c)'
      default: return 'var(--tm-chart-1)'
    }
  }
  const rects = new Map<string, SwimlaneRect>()
  const geom: SwimlaneGeom = { labelW, marginX, colW, laneH, top0: 10 }
  return (
    <figure className="presentation-chart">
      {props.title ? <figcaption className="presentation-chart-title">{props.title}</figcaption> : null}
      <svg viewBox={`0 0 ${W} ${H}`} className="presentation-chart-svg" role="img">
        {lanes.map((lane, li) => (
          <g key={lane.id}>
            <rect x={0} y={10 + laneH * li} width={W} height={laneH} fill={li % 2 === 1 ? 'var(--tm-surface)' : 'transparent'} />
            <text x={8} y={10 + laneH * li + laneH / 2 + 4} fontSize={10} fontWeight={600} fill="var(--tm-text-3)">{clip(lane.label ?? lane.id, 14)}</text>
          </g>
        ))}
        {nodes.map((node) => {
          const li = laneIndex.get(node.lane) ?? 0
          const span = node.span && node.span >= 1 ? node.span : 1
          const x = labelW + marginX + colW * (node.start ?? 0) + 9
          const y = 10 + laneH * li + 10
          const w = colW * span - 18
          const h = laneH - 20
          rects.set(node.id, { x, y, w, h, lane: li, c0: node.start ?? 0, c1: (node.start ?? 0) + span - 1 })
          return (
            <g key={node.id}>
              <rect x={x} y={y} width={w} height={h} rx={5} fill="var(--tm-surface)" stroke={statusColor(node.status)} strokeWidth={1.6}>
                {node.ref && <title>{node.ref.type} {node.ref.id}</title>}
              </rect>
              <text x={x + 5} y={y + 14} fontSize={9.5} fill="var(--tm-text)">{clip(node.label, Math.floor(w / 6))}</text>
              {node.note && <text x={x + 5} y={y + h - 5} fontSize={8.5} fill="var(--tm-text-3)">{clip(node.note, Math.floor(w / 7))}</text>}
            </g>
          )
        })}
        {(data.edges ?? []).map((edge, i) => {
          const from = rects.get(edge.from)
          const to = rects.get(edge.to)
          if (!from || !to) return null
          const { pts, dir, label } = routeSwimlaneEdge(from, to, geom, rects)
          const color = 'var(--tm-text-3)'
          const last = pts[pts.length - 1]
          const arrow = swArrowPoints(last[0], last[1], dir)
          const d = 'M ' + pts.map((p) => `${p[0]} ${p[1]}`).join(' L ')
          const text = edge.label || edge.condition
          return (
            <g key={`e${i}`} stroke={color} fill="none">
              <path d={d} strokeWidth={1.2} fill="none" strokeDasharray={edge.dashed ? '3 3' : undefined} />
              <polygon points={arrow} fill={color} stroke="none" />
              {text && (
                <text x={label[0]} y={label[1]} fontSize={8.5} fill="var(--tm-text-3)" stroke="none">{clip(text, 20)}</text>
              )}
            </g>
          )
        })}
      </svg>
    </figure>
  )
}

// ── Treemap -----------------------------------------------------------------

type TreemapNodeIR = { label: string; value?: number; children?: TreemapNodeIR[]; ref?: { type: string; id: string } }

function subtreeValue(node: TreemapNodeIR): number {
  if (!node.children || node.children.length === 0) return node.value ?? 0
  return node.children.reduce((sum, child) => sum + subtreeValue(child), 0)
}

function squarify(nodes: TreemapNodeIR[], rect: { x: number; y: number; w: number; h: number }, maxDepth: number, depth: number, out: Array<{ node: TreemapNodeIR; rect: { x: number; y: number; w: number; h: number }; depth: number }>) {
  const total = nodes.reduce((sum, n) => sum + subtreeValue(n), 0)
  if (total <= 0) return
  let rest = [...nodes].sort((a, b) => subtreeValue(b) - subtreeValue(a))
  let area = rect.w * rect.h
  while (rest.length > 0) {
    const row = [rest.shift()!]
    const rowValue = () => row.reduce((sum, n) => sum + subtreeValue(n), 0)
    const worst = (candidate?: TreemapNodeIR): number => {
      const list = candidate ? [...row, candidate] : row
      const value = list.reduce((sum, n) => sum + subtreeValue(n), 0)
      const areaRow = (value / total) * area
      const side = Math.min(rect.w, rect.h)
      const len = areaRow / side
      let worstRatio = 0
      for (const n of list) {
        const thickness = (subtreeValue(n) / value) * len
        if (thickness <= 0) return Infinity
        worstRatio = Math.max(worstRatio, Math.max(len / thickness, thickness / len))
      }
      return worstRatio
    }
    while (rest.length > 0 && worst(rest[0]) <= worst()) row.push(rest.shift()!)
    const rowValueNow = rowValue()
    const rowArea = (rowValueNow / total) * area
    let strip: { x: number; y: number; w: number; h: number }
    if (rect.w >= rect.h) {
      strip = { x: rect.x, y: rect.y, w: rowArea / rect.h, h: rect.h }
      rect.x += strip.w
      rect.w -= strip.w
    } else {
      strip = { x: rect.x, y: rect.y, w: rect.w, h: rowArea / rect.w }
      rect.y += strip.h
      rect.h -= strip.h
    }
    area = rect.w * rect.h
    let offset = strip.w >= strip.h ? strip.y : strip.x
    for (const node of row) {
      const share = subtreeValue(node) / rowValueNow
      let r: { x: number; y: number; w: number; h: number }
      if (strip.w >= strip.h) {
        r = { x: strip.x, y: offset, w: strip.w, h: share * strip.h }
      } else {
        r = { x: offset, y: strip.y, w: share * strip.w, h: strip.h }
      }
      offset += strip.w >= strip.h ? r.h : r.w
      if (node.children && node.children.length > 0 && depth < maxDepth) {
        squarify(node.children, { x: r.x + 3, y: r.y + 3, w: r.w - 6, h: r.h - 6 }, maxDepth, depth + 1, out)
      } else {
        out.push({ node, rect: r, depth })
      }
    }
  }
}

export function TreemapView({ component }: { component: { data?: unknown; props?: unknown } }) {
  const data = (component.data ?? {}) as { root?: TreemapNodeIR; unit?: string }
  const props = (component.props ?? {}) as { title?: string; showValues?: boolean; maxDepth?: number }
  const root = data.root
  if (!root || !root.label) return <PlaceholderLike what="treemap without a root" />
  const W = 560
  const H = Math.round(Math.min(340, W * 0.62))
  const maxDepth = props.maxDepth && props.maxDepth > 0 ? props.maxDepth : 2
  const leaves: Array<{ node: TreemapNodeIR; rect: { x: number; y: number; w: number; h: number }; depth: number }> = []
  const values = (root.children ?? []).map((c) => subtreeValue(c))
  const total = values.reduce((s, v) => s + v, 0)
  if ((root.children?.length ?? 0) === 0) {
    leaves.push({ node: root, rect: { x: 0, y: 0, w: W, h: H }, depth: 0 })
  } else {
    squarify(root.children ?? [], { x: 0, y: 0, w: W, h: H }, maxDepth, 1, leaves)
  }
  const colorOf = (value: number): [number, number, number] => {
    const t = Math.max(0, Math.min(1, value / Math.max(...values, 0.001)))
    const base = [122, 138, 148]
    const hot = [45, 178, 165]
    const r = Math.round(base[0] + (hot[0] - base[0]) * (0.15 + 0.6 * t))
    const g = Math.round(base[1] + (hot[1] - base[1]) * (0.15 + 0.6 * t))
    const b = Math.round(base[2] + (hot[2] - base[2]) * (0.15 + 0.6 * t))
    return [r, g, b]
  }
  return (
    <figure className="presentation-chart">
      {props.title ? <figcaption className="presentation-chart-title">{props.title}</figcaption> : null}
      <svg viewBox={`0 0 ${W} ${H}`} className="presentation-chart-svg" role="img">
        {leaves.map(({ node, rect }, i) => {
          const [cr, cg, cb] = colorOf(subtreeValue(node))
          const cellFill = `rgb(${cr},${cg},${cb})`
          const labelFill = textOnCell(cr, cg, cb)
          return (
            <g key={`${node.label}-${i}`}>
              <rect x={rect.x} y={rect.y} width={rect.w} height={rect.h} rx={3} fill={cellFill} stroke="var(--tm-bg)" strokeWidth={1}>
                {node.ref && <title>{node.ref.type} {node.ref.id}</title>}
              </rect>
              {rect.w >= 34 && rect.h >= 16 && (
                <text x={rect.x + 4} y={rect.y + 13} fontSize={10} fontWeight={600} fill={labelFill}>{clip(node.label, Math.floor(rect.w / 6))}</text>
              )}
              {props.showValues !== false && node.value !== undefined && rect.h >= 30 && rect.w >= 40 && (
                <text x={rect.x + 4} y={rect.y + 26} fontSize={9} fill={labelFill} opacity={0.85}>
                  {fmt(node.value)}{data.unit ? ` ${data.unit}` : ''}{total > 0 ? ` (${((subtreeValue(node) / total) * 100).toFixed(1)}%)` : ''}
                </text>
              )}
            </g>
          )
        })}
      </svg>
    </figure>
  )
}

function PlaceholderLike({ what }: { what: string }) {
  return (
    <figure className="presentation-chart">
      <div className="presentation-placeholder" style={{ padding: '0.75rem', color: 'var(--tm-text-3)', fontSize: '0.78rem' }}>{what}</div>
    </figure>
  )
}
