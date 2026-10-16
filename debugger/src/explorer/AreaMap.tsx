import { useMemo, useState } from 'react'
import { useT } from '../i18n'
import type { ExploreArea } from '../observationApi'

/** Deterministic bubble layout for the knowledge area map (ТЗ §4.3).
 *  Size encodes the amount of knowledge (never veracity), color encodes the
 *  dominant lifecycle state, a badge marks unresolved contradictions. The
 *  layout is row-packed: circles flow left→right by descending size and wrap
 *  at the canvas width, so the same areas always produce the same map. */
export interface PositionedArea {
  area: ExploreArea
  x: number
  y: number
  r: number
}

export function areaKey(area: Pick<ExploreArea, 'kind' | 'name'>): string {
  return `${area.kind}:${area.name}`
}

/** Dominant lifecycle state of an area decides its color. Ties resolve in the
 *  order healthy → fresh → disputed → retired so a single invalidated item
 *  never paints a large healthy area red; an empty split is neutral. */
export function dominantState(states: Record<string, number>): string {
  const priority = ['confirmed', 'proposed', 'challenged', 'corrected', 'superseded', 'invalidated']
  let best = 'proposed'
  let bestCount = 0
  for (const state of priority) {
    const count = states[state] ?? 0
    if (count > bestCount) {
      best = state
      bestCount = count
    }
  }
  return best
}

export const STATE_FILL: Record<string, string> = {
  confirmed: 'var(--tm-success)',
  proposed: 'var(--tm-info)',
  challenged: 'var(--tm-amber)',
  corrected: 'var(--tm-text-3)',
  superseded: 'var(--tm-text-3)',
  invalidated: 'var(--tm-danger)',
}

/** Row-pack circles into a fixed-width canvas. Radius grows with sqrt(count):
 *  area, not diameter, stays proportional to the amount of knowledge. */
export function layoutAreas(areas: ExploreArea[], width = 560, minRadius = 22, maxRadius = 58): PositionedArea[] {
  const sorted = [...areas].sort((a, b) => (b.knowledge_count || 0) - (a.knowledge_count || 0) || a.name.localeCompare(b.name))
  const maxCount = sorted[0]?.knowledge_count || 1
  const positioned: PositionedArea[] = []
  let x = 0
  let y = 0
  let rowHeight = 0
  for (const area of sorted) {
    const share = Math.sqrt((area.knowledge_count || 1) / maxCount)
    const r = minRadius + share * (maxRadius - minRadius)
    if (x > 0 && x + r * 2 > width) {
      x = 0
      y += rowHeight + 8
      rowHeight = 0
    }
    const cx = x + r
    const cy = y + r
    positioned.push({ area, x: cx, y: cy, r })
    x += r * 2 + 8
    rowHeight = Math.max(rowHeight, r * 2)
  }
  return positioned
}

function truncate(text: string, max: number): string {
  return text.length > max ? `${text.slice(0, max - 1)}…` : text
}

export default function AreaMap({ areas, selectedKey, onSelect }: {
  areas: ExploreArea[]
  selectedKey?: string
  onSelect: (area: ExploreArea) => void
}) {
  const t = useT()
  const [hover, setHover] = useState<string | null>(null)
  const positioned = useMemo(() => layoutAreas(areas), [areas])
  const height = positioned.reduce((max, p) => Math.max(max, p.y + p.r), 0) + 8
  const width = 560

  if (positioned.length === 0) {
    return <div style={{ padding: '1rem 0', color: 'var(--tm-muted)', fontSize: '0.8rem' }}>{t('explorer.map_empty') ?? 'No areas recorded yet.'}</div>
  }

  return (
    <svg role="group" aria-label={t('explorer.map_title') ?? 'Knowledge area map'} width="100%" viewBox={`0 0 ${width} ${Math.max(height, 120)}`} style={{ display: 'block' }}>
      {positioned.map(({ area, x, y, r }) => {
        const key = areaKey(area)
        const active = key === selectedKey
        const dominant = dominantState(area.states ?? {})
        const label = truncate(area.name, active ? 18 : 12)
        return (
          <g
            key={key}
            role="button"
            tabIndex={0}
            aria-label={`${area.name}: ${area.knowledge_count}`}
            onClick={() => onSelect(area)}
            onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); onSelect(area) } }}
            onMouseEnter={() => setHover(key)}
            onMouseLeave={() => setHover(null)}
            style={{ cursor: 'pointer' }}
          >
            <title>{`${area.name} (${area.kind}) — ${area.knowledge_count}${area.contradictions ? `, ${area.contradictions} ⚠` : ''}`}</title>
            <circle
              cx={x} cy={y} r={r}
              fill={STATE_FILL[dominant] ?? 'var(--tm-info)'}
              fillOpacity={active ? 0.5 : hover === key ? 0.4 : 0.28}
              stroke={active ? 'var(--tm-teal)' : hover === key ? 'var(--tm-text-2)' : 'var(--tm-border)'}
              strokeWidth={active ? 2.5 : 1}
            />
            {(area.contradictions ?? 0) > 0 && (
              <circle cx={x + r * 0.72} cy={y - r * 0.72} r={Math.max(7, r * 0.22)} fill="var(--tm-danger)" fillOpacity={0.85} stroke="var(--tm-bg)" strokeWidth={1.5} />
            )}
            <text x={x} y={y - 2} textAnchor="middle" fill="var(--tm-text)" fontSize={Math.min(13, 8 + r / 6)} fontWeight={600} style={{ pointerEvents: 'none', userSelect: 'none' }}>
              {label}
            </text>
            <text x={x} y={y + 13} textAnchor="middle" fill="var(--tm-text-3)" fontSize={10} style={{ pointerEvents: 'none', userSelect: 'none' }}>
              {area.knowledge_count}
            </text>
          </g>
        )
      })}
    </svg>
  )
}
