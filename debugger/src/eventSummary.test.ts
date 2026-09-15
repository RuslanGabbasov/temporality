import { describe, expect, it } from 'vitest'
import { describeEvent, isAttentionHint } from './eventSummary'
import type { FrpEvent } from './types'

const ROOT = '/tmp/temporality-first-contact-abc123'

function observation(payload: Record<string, unknown>): FrpEvent {
  return { type: 'world.observation', payload: { resource: `filesystem:${ROOT}`, world_id: 'w1', world_version: 1, ...payload } as FrpEvent['payload'] }
}

describe('describeEvent', () => {
  it('interprets a file content observation with a relative path and preview', () => {
    const summary = describeEvent(observation({
      observation_type: 'file_content',
      affordance_id: 'read_file',
      payload: { path: `${ROOT}/cmd/app/main.go`, size: 111, content: 'func Sum(a, b int) int {\n\treturn a - b\n}\n' },
    }))
    expect(summary.title).toBe('read_file · file_content /cmd/app/main.go')
    expect(summary.detail).toContain('func Sum(a, b int) int { return a - b }')
    expect(summary.detail).toContain('(111 B)')
    expect(summary.meta).toBe('w1@v1')
  })

  it('interprets a directory listing with entry names', () => {
    const summary = describeEvent(observation({
      observation_type: 'directory_listing',
      source_resource_id: 'repo',
      payload: { path: ROOT, entries: 4, items: [{ name: '.git' }, { name: 'README.md' }, { name: 'cmd' }, { name: 'go.mod' }] },
    }))
    expect(summary.title).toBe(`ingest:repo · directory_listing /`)
    expect(summary.detail).toContain('4 entries')
    expect(summary.detail).toContain('README.md')
    expect(summary.detail).toContain('go.mod')
  })

  it('marks truncated payloads', () => {
    const summary = describeEvent(observation({
      observation_type: 'file_content',
      affordance_id: 'read_file',
      truncated: true,
      payload: { path: `${ROOT}/big.log`, content: 'x' },
    }))
    expect(summary.meta).toContain('payload truncated')
  })

  it('shows the claim proposition with confidence and extractor', () => {
    const summary = describeEvent({ type: 'claim.candidate', payload: { claim_id: 'c1', proposition: 'Sum returns a - b instead of a + b', confidence: 0.95, extractor: 'gomod.v1', subject: 'fn:Sum', predicate: 'returns', object: 'a - b' } })
    expect(summary.title).toBe('Sum returns a - b instead of a + b')
    expect(summary.meta).toContain('confidence 95%')
    expect(summary.meta).toContain('extractor gomod.v1')
    expect(summary.meta).toContain('fn:Sum returns a - b')
  })

  it('degrades legacy pointer-only claim events honestly', () => {
    const summary = describeEvent({ type: 'claim.candidate', payload: { claim_id: '8c2911e3-7e8e-4b6c-b580-a9b42929216e', emission_id: 'em-1' } })
    expect(summary.title).toContain('claim 8c2911e3')
    expect(summary.title).toContain('proposition not stored in event')
  })

  it('compacts attention hints to short refs', () => {
    const summary = describeEvent({ type: 'attention.suggested', payload: { ref: 'event:73bddee1-28b2-47db-a589-8cfca842433d', emission_id: 'em_9f3a7c2e' } })
    expect(summary.title).toBe('hint → event:73bddee1')
    expect(summary.meta).toBe('emission em_9f3a7')
    expect(isAttentionHint({ type: 'attention.suggested' })).toBe(true)
    expect(isAttentionHint({ type: 'claim.candidate' })).toBe(false)
  })

  it('shows world binding on episode start', () => {
    const summary = describeEvent({ type: 'episode.started', payload: { world_id: 'first-contact-f50174137f', world_version: 1, bootstrap: true, objective_id: '85084423-7e9d' } })
    expect(summary.title).toBe('episode started in world first-contact-f50174137f@v1')
    expect(summary.meta).toContain('bootstrap discovery')
  })

  it('falls back to compact payload json for unknown types', () => {
    const summary = describeEvent({ type: 'future.event', payload: { depth: 2, note: 'hello world' } })
    expect(summary.title).toContain('depth')
    expect(summary.title).toContain('hello world')
  })
})
