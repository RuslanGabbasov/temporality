import { describe, expect, it } from 'vitest'
import { routeSwimlaneEdge, type SwimlaneGeom, type SwimlaneRect } from './PresentationViz2'

// The router must never cut through a box: the old midpoint elbow went
// straight through nodes whenever the target sat in the same column or to
// the left of the source (the phantom «inner rectangles» bug).

const geom: SwimlaneGeom = { labelW: 96, marginX: 8, colW: 100, laneH: 64, top0: 10 }

function rect(id: string, lane: number, c0: number, c1: number, rects: Map<string, SwimlaneRect>): SwimlaneRect {
  const x = geom.labelW + geom.marginX + geom.colW * c0 + 9
  const y = geom.top0 + geom.laneH * lane + 10
  const w = geom.colW * (c1 - c0 + 1) - 18
  const h = geom.laneH - 20
  const r: SwimlaneRect = { x, y, w, h, lane, c0, c1 }
  rects.set(id, r)
  return r
}

/** True when the polyline passes through the interior of any box other than
 *  the two endpoints (edge-touching is allowed — that's how it enters). */
function cutsBox(pts: Array<[number, number]>, rects: Map<string, SwimlaneRect>, a: SwimlaneRect, b: SwimlaneRect): boolean {
  for (let i = 1; i < pts.length; i++) {
    const minX = Math.min(pts[i - 1][0], pts[i][0])
    const maxX = Math.max(pts[i - 1][0], pts[i][0])
    const minY = Math.min(pts[i - 1][1], pts[i][1])
    const maxY = Math.max(pts[i - 1][1], pts[i][1])
    for (const r of rects.values()) {
      if (r === a || r === b) continue
      // Shrink by 1px: segments that merely graze an edge are fine.
      if (maxX > r.x + 1 && minX < r.x + r.w - 1 && maxY > r.y + 1 && minY < r.y + r.h - 1) return true
    }
  }
  return false
}

describe('routeSwimlaneEdge', () => {
  it('routes same-column edges vertically without cutting boxes', () => {
    // The exact screenshot case: source lane 0 col 0, target lane 1 col 0,
    // with a third box in between at lane 2 col 0.
    const rects = new Map<string, SwimlaneRect>()
    const a = rect('a', 0, 0, 0, rects)
    const mid = rect('mid', 1, 0, 0, rects)
    const b = rect('b', 2, 0, 0, rects)
    const { pts, dir } = routeSwimlaneEdge(a, b, geom, rects)
    expect(cutsBox(pts, rects, a, b)).toBe(false)
    // Must not run horizontally through `mid`: the vertical channel dodges it.
    expect(dir === 'down' || dir === 'up').toBe(true)
    void mid
  })

  it('routes backward edges (target left of source) around boxes', () => {
    const rects = new Map<string, SwimlaneRect>()
    const a = rect('a', 1, 2, 2, rects) // lane 1, column 2
    const blocker = rect('blocker', 1, 0, 1, rects) // same lane, between them
    const b = rect('b', 2, 0, 0, rects) // lane 2, column 0
    const { pts } = routeSwimlaneEdge(a, b, geom, rects)
    expect(cutsBox(pts, rects, a, b)).toBe(false)
    void blocker
  })

  it('routes same-lane forward edges as a straight line when clear', () => {
    const rects = new Map<string, SwimlaneRect>()
    const a = rect('a', 0, 0, 0, rects)
    const b = rect('b', 0, 1, 1, rects)
    const { pts, dir } = routeSwimlaneEdge(a, b, geom, rects)
    expect(cutsBox(pts, rects, a, b)).toBe(false)
    expect(dir).toBe('right')
    expect(pts).toHaveLength(2)
  })

  it('goes over the top when a same-lane blocker sits between the nodes', () => {
    const rects = new Map<string, SwimlaneRect>()
    const a = rect('a', 0, 0, 0, rects)
    rect('blocker', 0, 1, 1, rects)
    const b = rect('b', 0, 2, 2, rects)
    const { pts } = routeSwimlaneEdge(a, b, geom, rects)
    expect(cutsBox(pts, rects, a, b)).toBe(false)
  })

  it('routes cross-lane diagonal edges through the column gutter', () => {
    const rects = new Map<string, SwimlaneRect>()
    const a = rect('a', 0, 0, 0, rects)
    rect('noise', 1, 1, 1, rects)
    const b = rect('b', 2, 2, 2, rects)
    const { pts } = routeSwimlaneEdge(a, b, geom, rects)
    expect(cutsBox(pts, rects, a, b)).toBe(false)
  })

  it('never emits a path through boxes for any pair in a packed grid', () => {
    // Brute force: pack a 3x3 grid with a blocker and check every pair.
    const rects = new Map<string, SwimlaneRect>()
    const grid: SwimlaneRect[] = []
    for (let lane = 0; lane < 3; lane++) {
      for (let col = 0; col < 3; col++) {
        grid.push(rect(`n${lane}_${col}`, lane, col, col, rects))
      }
    }
    for (const a of grid) {
      for (const b of grid) {
        if (a === b) continue
        const { pts } = routeSwimlaneEdge(a, b, geom, rects)
        expect(cutsBox(pts, rects, a, b), `edge ${a.c0}/${a.lane} → ${b.c0}/${b.lane}`).toBe(false)
      }
    }
  })
})
