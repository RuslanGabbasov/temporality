import { describe, expect, it } from 'vitest'
import { eventLabel, executionIdOf, frameIdOf, unwrapEvents } from './timeline'

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
})
