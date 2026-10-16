import { useMemo, useState } from 'react'
import { useT } from '../i18n'
import type { ExploreArea } from '../observationApi'
import { stateLabel } from './shared'

/** Treemap of the knowledge areas (ТЗ §4.3). Area encodes the amount of
 *  knowledge (never veracity), color encodes the dominant lifecycle state,
 *  a red badge marks unresolved contradictions. The squarified layout keeps
 *  tiles close to square so labels stay readable — bubbles wasted the corners
 *  and forced tiny text. The layout is deterministic: the same areas always
 *  produce the same map. */
export interface PositionedArea {
  area: ExploreArea
  x: number
  y: number
  w: number
  h: number
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

/** Legend order: healthy → disputed → retired. Corrected and superseded share
 *  the neutral gray, which is exactly what the map paints them with. */
const LEGEND_STATES = ['confirmed', 'proposed', 'challenged', 'corrected', 'superseded', 'invalidated']

interface Rect {
  x: number
  y: number
  w: number
  h: number
}

/** Squarified treemap (Bruls, Huizing & van Wijk): rows are laid along the
 *  shorter side of the remaining rectangle while that keeps aspect ratios
 *  closer to square, then the strip is cut off and the remainder recurses.
 *  Tile areas are exactly proportional to the input values and the tiles
 *  cover the canvas without overlap. */
export function squarify(values: number[], width: number, height: number): Rect[] {
  const total = values.reduce((sum, v) => sum + v, 0)
  if (values.length === 0 || total <= 0 || width <= 0 || height <= 0) {
    return values.map(() => ({ x: 0, y: 0, w: 0, h: 0 }))
  }
  const scaled = values.map((v) => (v / total) * width * height)
  const rects: Rect[] = []

  const worstRatio = (row: number[], rowArea: number, side: number): number => {
    const thickness = rowArea / side
    let worst = 0
    for (const a of row) {
      const length = a / thickness
      worst = Math.max(worst, thickness / length, length / thickness)
    }
    return worst
  }

  let x = 0
  let y = 0
  let w = width
  let h = height
  let i = 0
  while (i < scaled.length) {
    const side = Math.min(w, h)
    const row = [scaled[i]]
    let rowArea = scaled[i]
    i += 1
    while (i < scaled.length) {
      const before = worstRatio(row, rowArea, side)
      const after = worstRatio([...row, scaled[i]], rowArea + scaled[i], side)
      if (after <= before) {
        row.push(scaled[i])
        rowArea += scaled[i]
        i += 1
      } else {
        break
      }
    }
    const thickness = rowArea / side
    if (w >= h) {
      let iy = y
      for (const a of row) {
        const ih = a / thickness
        rects.push({ x, y: iy, w: thickness, h: ih })
        iy += ih
      }
      x += thickness
      w -= thickness
    } else {
      let ix = x
      for (const a of row) {
        const iw = a / thickness
        rects.push({ x: ix, y, w: iw, h: thickness })
        ix += iw
      }
      y += thickness
      h -= thickness
    }
  }
  return rects
}

/** Tile the canvas in the caller's order (KnowledgeExplorer sorts by size or
 *  name and also applies the area filter; the layout itself stays order-agnostic). */
export function layoutAreas(areas: ExploreArea[], width = 320, height = 260): PositionedArea[] {
	if (areas.length === 0) return []
	const rects = squarify(areas.map((a) => a.knowledge_count || 1), width, height)
	return areas.map((area, i) => ({ area, ...rects[i] }))
}

/** Truncate a label to the number of characters that fit the tile width
 *  (rough 0.58em average glyph width for the semibold sans face). */
function fitLabel(text: string, rectWidth: number, fontSize: number): string {
  const max = Math.max(2, Math.floor((rectWidth - 6) / (fontSize * 0.58)))
  return text.length > max ? `${text.slice(0, Math.max(2, max - 1))}…` : text
}

export default function AreaMap({ areas, selectedKey, onSelect }: {
  areas: ExploreArea[]
  selectedKey?: string
  onSelect: (area: ExploreArea) => void
}) {
  const t = useT()
  const [hover, setHover] = useState<string | null>(null)
  // viewBox matches the rendered column width (~280-360px), so SVG text keeps
  // its pixel size instead of being scaled down 2x as in the old 560-wide one.
  const width = 320
  const height = 260
  const positioned = useMemo(() => layoutAreas(areas, width, height), [areas])

  if (positioned.length === 0) {
    return <div style={{ padding: '1rem 0', color: 'var(--tm-muted)', fontSize: '0.8rem' }}>{t('explorer.map_empty') ?? 'No areas recorded yet.'}</div>
  }

  return (
    <div>
      <svg role="group" aria-label={t('explorer.map_title') ?? 'Knowledge area map'} width="100%" viewBox={`0 0 ${width} ${height}`} style={{ display: 'block' }}>
        {positioned.map(({ area, x, y, w, h }) => {
          const key = areaKey(area)
          const active = key === selectedKey
          const dominant = dominantState(area.states ?? {})
          const fontSize = Math.min(12, Math.max(9, Math.sqrt(w * h) / 7))
          const showName = h >= 24 && w >= 34
          const showCount = h >= 42
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
              <rect
                x={x + 1} y={y + 1} width={Math.max(0, w - 2)} height={Math.max(0, h - 2)} rx={4}
                fill={STATE_FILL[dominant] ?? 'var(--tm-info)'}
                fillOpacity={active ? 0.55 : hover === key ? 0.48 : 0.35}
                stroke={active ? 'var(--tm-teal)' : hover === key ? 'var(--tm-text-2)' : 'transparent'}
                strokeWidth={active ? 2 : 1.5}
              />
              {(area.contradictions ?? 0) > 0 && (
                <circle cx={x + w - 7} cy={y + 7} r={4.5} fill="var(--tm-danger)" stroke="var(--tm-bg)" strokeWidth={1.2} />
              )}
              {showName && (
                <text
                  x={x + w / 2}
                  y={showCount ? y + h / 2 - 2 : y + h / 2 + fontSize / 3}
                  textAnchor="middle"
                  fill="var(--tm-text)"
                  fontSize={fontSize}
                  fontWeight={600}
                  style={{ pointerEvents: 'none', userSelect: 'none' }}
                >
                  {fitLabel(area.name, w, fontSize)}
                </text>
              )}
              {showCount && (
                <text x={x + w / 2} y={y + h / 2 + 12} textAnchor="middle" fill="var(--tm-text-3)" fontSize={10} style={{ pointerEvents: 'none', userSelect: 'none' }}>
                  {area.knowledge_count}
                </text>
              )}
            </g>
          )
        })}
      </svg>
      <div style={{ display: 'flex', flexWrap: 'wrap', gap: '0.25rem 0.7rem', marginTop: '0.45rem' }}>
        {LEGEND_STATES.map((state) => (
          <span key={state} style={{ display: 'inline-flex', alignItems: 'center', gap: '0.28rem', fontSize: '0.62rem', color: 'var(--tm-muted)' }}>
            <span style={{ width: 9, height: 9, borderRadius: 2, background: STATE_FILL[state], opacity: 0.55 }} />
            {stateLabel(state, t)}
          </span>
        ))}
      </div>
    </div>
  )
}
