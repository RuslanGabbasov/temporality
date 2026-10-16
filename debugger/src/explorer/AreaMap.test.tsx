import { describe, expect, it } from 'vitest'
import { dominantState, layoutAreas, squarify, areaKey, STATE_FILL } from './AreaMap'
import type { ExploreArea } from '../observationApi'

const area = (name: string, count: number, states: Record<string, number> = { proposed: count }, kind: 'topic' | 'entity' = 'topic'): ExploreArea =>
  ({ name, kind, knowledge_count: count, states })

describe('dominantState', () => {
  it('picks the most frequent state', () => {
    expect(dominantState({ confirmed: 3, invalidated: 1 })).toBe('confirmed')
    expect(dominantState({ invalidated: 3, confirmed: 1 })).toBe('invalidated')
  })

  it('resolves ties towards the healthier state so one dead item cannot paint an area red', () => {
    expect(dominantState({ confirmed: 2, invalidated: 2 })).toBe('confirmed')
    expect(dominantState({})).toBe('proposed')
  })

  it('every dominant state maps to a fill color', () => {
    for (const state of ['confirmed', 'proposed', 'challenged', 'corrected', 'superseded', 'invalidated']) {
      expect(STATE_FILL[state], state).toBeTruthy()
    }
  })
})

describe('layoutAreas', () => {
  it('tiles the canvas so tile area stays proportional to the count', () => {
    const [big, small] = layoutAreas([area('big', 100), area('small', 25)], 320, 260)
    const bigArea = big.w * big.h
    const smallArea = small.w * small.h
    expect(bigArea / smallArea).toBeCloseTo(4, 1)
    // The treemap covers the whole canvas — no bubble corners wasted.
    expect(bigArea + smallArea).toBeCloseTo(320 * 260, -1)
  })

  it('sorts by descending size and stays deterministic', () => {
    const input = [area('b', 2), area('a', 9), area('c', 5)]
    const first = layoutAreas(input)
    const second = layoutAreas([...input].reverse())
    expect(first.map((p) => p.area.name)).toEqual(['a', 'c', 'b'])
    expect(second.map((p) => p.area.name)).toEqual(first.map((p) => p.area.name))
    expect(first[0].w * first[0].h).toBeGreaterThanOrEqual(first[1].w * first[1].h)
    expect(first[1].w * first[1].h).toBeGreaterThanOrEqual(first[2].w * first[2].h)
  })

  it('keeps every tile inside the canvas without overlaps', () => {
    const many = Array.from({ length: 30 }, (_, i) => area(`t${i}`, (i % 5) + 1))
    const positioned = layoutAreas(many, 320, 260)
    for (const p of positioned) {
      expect(p.x).toBeGreaterThanOrEqual(-1e-6)
      expect(p.y).toBeGreaterThanOrEqual(-1e-6)
      expect(p.x + p.w).toBeLessThanOrEqual(320 + 1e-6)
      expect(p.y + p.h).toBeLessThanOrEqual(260 + 1e-6)
      expect(p.w).toBeGreaterThan(0)
      expect(p.h).toBeGreaterThan(0)
    }
    for (let i = 0; i < positioned.length; i++) {
      for (let j = i + 1; j < positioned.length; j++) {
        const a = positioned[i]
        const b = positioned[j]
        const overlapX = Math.min(a.x + a.w, b.x + b.w) - Math.max(a.x, b.x)
        const overlapY = Math.min(a.y + a.h, b.y + b.h) - Math.max(a.y, b.y)
        // Shared edges are fine; positive area overlap is not.
        expect(overlapX <= 1e-6 || overlapY <= 1e-6, `${a.area.name} overlaps ${b.area.name}`).toBe(true)
      }
    }
  })

  it('squarify keeps tiles close to square for mixed sizes', () => {
    const rects = squarify([50, 25, 12, 6, 3, 2, 1], 320, 260)
    for (const r of rects) {
      const ratio = Math.max(r.w / r.h, r.h / r.w)
      expect(ratio).toBeLessThan(5)
    }
  })

  it('keys areas by kind and name', () => {
    expect(areaKey({ kind: 'entity', name: 'postgres' })).toBe('entity:postgres')
  })
})
