import { describe, expect, it } from 'vitest'
import { eventLabel, executionIdOf, frameIdOf, newestFirst, unwrapEvents } from './timeline'

const event = { type: 'frame.committed', payload: { frame_id: 'f-7', execution_id: 'x-2' } }

describe('timeline helpers', () => {
  it('unwraps both supported event response shapes', () => {
    expect(unwrapEvents([event])).toEqual([event])
    expect(unwrapEvents({ events: [event] })).toEqual([event])
  })

  it('extracts navigable identifiers and labels', () => {
    expect(frameIdOf(event)).toBe('f-7')
    expect(executionIdOf(event)).toBe('x-2')
    expect(eventLabel(event)).toBe('frame.committed')
  })

  it('orders the timeline newest first without mutating the input', () => {
    const source = [
      { type: 'episode.started', created_at: '2026-09-15T10:00:00Z' },
      { type: 'world.observation', created_at: '2026-09-15T10:00:05Z' },
      { type: 'claim.candidate', created_at: '2026-09-15T10:00:05Z' },
      { type: 'frame.transitioned', created_at: '2026-09-15T10:00:09Z' },
    ]
    const ordered = newestFirst(source)
    expect(ordered.map((item) => item.type)).toEqual(['frame.transitioned', 'world.observation', 'claim.candidate', 'episode.started'])
    expect(source[0].type).toBe('episode.started')
  })

  it('keeps events without timestamps last and stable', () => {
    const source = [
      { type: 'untimed' },
      { type: 'timed', created_at: '2026-09-15T10:00:00Z' },
      { type: 'untimed-too' },
    ]
    expect(newestFirst(source).map((item) => item.type)).toEqual(['timed', 'untimed', 'untimed-too'])
  })
})
