import { describe, expect, it } from 'vitest'
import { asExLocal, asUTC, stateAsOf, stepIcon, stepLabel } from './HistoryTimeline'
import type { ExploreHistoryStep } from '../observationApi'

const step = (type: string, at: string, state?: string): ExploreHistoryStep => ({ event_id: `${type}-${at}`, type, at, state })

describe('stateAsOf', () => {
  const steps = [
    step('knowledge.proposed', '2026-10-01T10:00:00Z', 'proposed'),
    step('knowledge.confirmed', '2026-10-02T10:00:00Z', 'confirmed'),
    step('knowledge.challenged', '2026-10-05T10:00:00Z', 'challenged'),
    step('knowledge.invalidated', '2026-10-07T10:00:00Z', 'invalidated'),
  ]

  it('walks forward through the lifecycle up to the cut', () => {
    expect(stateAsOf(steps, '2026-10-01T12:00:00Z')?.state).toBe('proposed')
    expect(stateAsOf(steps, '2026-10-03T00:00:00Z')?.state).toBe('confirmed')
    expect(stateAsOf(steps, '2026-10-06T00:00:00Z')?.state).toBe('challenged')
    expect(stateAsOf(steps, '2026-10-08T00:00:00Z')?.state).toBe('invalidated')
  })

  it('returns undefined before the knowledge existed and for a bad cut', () => {
    expect(stateAsOf(steps, '2026-09-30T00:00:00Z')).toBeUndefined()
    expect(stateAsOf(steps, 'not-a-date')).toBeUndefined()
  })

  it('handles unsorted steps and stateless steps', () => {
    const shuffled = [...steps].reverse()
    expect(stateAsOf(shuffled, '2026-10-06T00:00:00Z')?.state).toBe('challenged')
    const withStateless = [...steps, step('knowledge.linked', '2026-10-03T10:00:00Z')]
    const at = stateAsOf(withStateless, '2026-10-04T00:00:00Z')
    expect(at?.state).toBe('confirmed')
    expect(at?.step.type).toBe('knowledge.linked')
  })
})

describe('step presentation', () => {
  it('gives every lifecycle event a visual anchor', () => {
    for (const type of ['knowledge.proposed', 'knowledge.confirmed', 'knowledge.challenged', 'knowledge.corrected', 'knowledge.superseded', 'knowledge.invalidated', 'knowledge.promoted', 'knowledge.linked', 'something.else']) {
      expect(stepIcon(type), type).toBeTruthy()
    }
  })

  it('labels catalog events by locale and keeps unknown types verbatim', () => {
    expect(stepLabel('knowledge.proposed', 'ru')).toBe('Знание предложено')
    expect(stepLabel('knowledge.proposed', 'en')).toBe('Knowledge proposed')
    expect(stepLabel('custom.event', 'ru')).toBe('custom.event')
  })
})

describe('datetime conversion', () => {
  it('round-trips UTC instants through datetime-local', () => {
    const utc = '2026-10-01T10:00:00.000Z'
    expect(asUTC(asExLocal(utc))).toBe(utc)
  })
})
