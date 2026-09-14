import { describe, expect, it } from 'vitest'
import { renderSection, tokenUsageView } from './tokenUsage'

describe('tokenUsageView', () => {
  it('calculates readable usage values', () => {
    expect(tokenUsageView({ estimated: 860, budget: 1000 })).toEqual({ estimated: 860, budget: 1000, percent: 86, remaining: 140, warning: true })
  })

  it('keeps zero values valid without dividing by zero', () => {
    expect(tokenUsageView({ estimated: 0, budget: 0 })).toEqual({ estimated: 0, budget: 0, percent: undefined, remaining: 0, warning: false })
  })

  it('returns an empty state for missing or invalid values', () => {
    expect(tokenUsageView()).toEqual({ estimated: undefined, budget: undefined, percent: undefined, remaining: undefined, warning: false })
    expect(tokenUsageView({ estimated: Number.NaN, budget: -1 })).toEqual({ estimated: undefined, budget: undefined, percent: undefined, remaining: undefined, warning: false })
  })

  it('clamps remaining at zero while preserving over-budget percentage', () => {
    expect(tokenUsageView({ estimated: 120, budget: 100 })).toMatchObject({ percent: 120, remaining: 0, warning: true })
  })
})

describe('renderSection', () => {
  it('finds data in the RenderPacket sections array', () => {
    const packet = { render_id: 'r', frame_id: 'f', sections: [{ kind: 'working_set' as const, items: [{ id: 'x' }] }] }
    expect(renderSection(packet, 'working_set')?.items).toEqual([{ id: 'x' }])
    expect(renderSection(packet, 'focus')).toBeUndefined()
  })
})
