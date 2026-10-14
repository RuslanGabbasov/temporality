import { describe, expect, it } from 'vitest'
import { parseSSEBlock } from './journalStream'

describe('parseSSEBlock', () => {
  it('parses a named event with its data payload', () => {
    const frame = parseSSEBlock('event: model.completed\ndata: {"type":"model.completed","turn":3}')
    expect(frame).toEqual({ event: 'model.completed', data: '{"type":"model.completed","turn":3}' })
  })

  it('joins multi-line data payloads', () => {
    const frame = parseSSEBlock('event: agent.summary\ndata: line one\ndata: line two')
    expect(frame?.data).toBe('line one\nline two')
  })

  it('returns null for blocks without data', () => {
    expect(parseSSEBlock('event: done')).toBeNull()
    expect(parseSSEBlock('')).toBeNull()
  })

  it('ignores comment lines', () => {
    const frame = parseSSEBlock(': keepalive\nevent: turn.completed\ndata: {}')
    expect(frame?.event).toBe('turn.completed')
    expect(frame?.data).toBe('{}')
  })
})
