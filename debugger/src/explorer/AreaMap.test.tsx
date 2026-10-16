import { describe, expect, it } from 'vitest'
import { dominantState, layoutAreas, areaKey, STATE_FILL } from './AreaMap'
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
  it('scales radius with the square root of the count so area stays proportional', () => {
    const [big, small] = layoutAreas([area('big', 100), area('small', 25)])
    expect(big.r).toBeGreaterThan(small.r)
    // 4x the knowledge must not mean 4x the diameter — sqrt scaling.
    expect(big.r / small.r).toBeLessThan(2)
  })

  it('sorts by descending size and stays deterministic', () => {
    const input = [area('b', 2), area('a', 9), area('c', 5)]
    const first = layoutAreas(input)
    const second = layoutAreas([...input].reverse())
    expect(first.map((p) => p.area.name)).toEqual(['a', 'c', 'b'])
    expect(second.map((p) => p.area.name)).toEqual(first.map((p) => p.area.name))
  })

  it('wraps to a new row instead of overflowing the canvas width', () => {
    const many = Array.from({ length: 30 }, (_, i) => area(`t${i}`, 50))
    const positioned = layoutAreas(many)
    for (const p of positioned) {
      expect(p.x + p.r).toBeLessThanOrEqual(560 + 1e-6)
      expect(p.y).toBeGreaterThanOrEqual(p.r - 1e-6)
    }
    // With 30 equal circles the map must actually use several rows.
    const rows = new Set(positioned.map((p) => Math.round(p.y)))
    expect(rows.size).toBeGreaterThan(1)
  })

  it('keys areas by kind and name', () => {
    expect(areaKey({ kind: 'entity', name: 'postgres' })).toBe('entity:postgres')
  })
})
