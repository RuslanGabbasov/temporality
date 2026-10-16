import { describe, expect, it } from 'vitest'
import { extractExploreCalls, parseExploreArguments } from './agentExploreCalls'
import type { ObservationEvent } from '../observationApi'

function event(type: string, data: Record<string, unknown>, at = '2026-10-16T10:00:00Z'): ObservationEvent {
  return {
    schema: 'temporal.observations.v1',
    event_id: `test/${at}/${type}`,
    occurred_at: at,
    received_at: at,
    source: { id: 'test', integration: 'test' },
    context: { run: 'run-1' },
    type,
    data,
  }
}

describe('parseExploreArguments', () => {
  it('parses the JSON-string arguments the journal stores', () => {
    const call = parseExploreArguments({
      tool: 'knowledge_explore',
      turn: 3,
      arguments: JSON.stringify({ operation: 'search', question: 'миграции', targets: [' K1 ', 'K2'], max_items: 20 }),
    })
    expect(call).not.toBeNull()
    expect(call!.operation).toBe('search')
    expect(call!.question).toBe('миграции')
    expect(call!.targets).toEqual([' K1 ', 'K2'])
    expect(call!.maxItems).toBe(20)
    expect(call!.turn).toBe(3)
  })

  it('accepts an already-decoded arguments object and coerces types', () => {
    const call = parseExploreArguments({
      tool: 'knowledge_explore',
      arguments: { operation: 'synthesize', targets: [1, 'K9', ''], include_candidates: true, max_depth: '2' },
    })
    expect(call!.operation).toBe('synthesize')
    expect(call!.targets).toEqual(['K9']) // non-strings and blanks dropped
    expect(call!.includeCandidates).toBe(true)
    expect(call!.maxDepth).toBe(0) // non-number stays 0, replay omits it
  })

  it('returns null without an operation or for broken JSON', () => {
    expect(parseExploreArguments({ tool: 'knowledge_explore', arguments: '{}' })).toBeNull()
    expect(parseExploreArguments({ tool: 'knowledge_explore', arguments: '{oops' })).toBeNull()
    expect(parseExploreArguments(null)).toBeNull()
  })
})

describe('extractExploreCalls', () => {
  it('keeps only knowledge_explore tool.started events, oldest first', () => {
    const calls = extractExploreCalls([
      event('tool.started', { tool: 'run_command', arguments: '{}' }, '2026-10-16T10:00:03Z'),
      event('tool.completed', { tool: 'knowledge_explore' }, '2026-10-16T10:00:02Z'),
      event('tool.started', { tool: 'knowledge_explore', arguments: JSON.stringify({ operation: 'orient', question: 'тема?' }) }, '2026-10-16T10:00:01Z'),
      event('tool.started', { tool: 'knowledge_explore', arguments: JSON.stringify({ operation: 'trace', targets: ['K1'] }) }, '2026-10-16T10:00:00Z'),
    ])
    expect(calls).toHaveLength(2)
    expect(calls[0].operation).toBe('trace')
    expect(calls[1].operation).toBe('orient')
    expect(calls[0].eventID).toContain('tool.started')
  })

  it('tolerates an empty journal', () => {
    expect(extractExploreCalls([])).toEqual([])
  })
})
