import { describe, expect, it, vi } from 'vitest'
import { observationApi, type ObservationEvent } from './observationApi'

vi.mock('./observationApi', () => ({
  observationApi: { events: vi.fn() },
}))

import { chainRunIds, fetchChainDisputes } from './RunDisputes'

let counter = 0
function knowledgeEvent(type: string, at: string, run: string, data: Record<string, unknown>): ObservationEvent {
  counter += 1
  return {
    schema: 'temporality.event/1',
    event_id: `ev-${String(counter).padStart(4, '0')}`,
    occurred_at: at,
    received_at: at,
    source: { id: 'temporality-agent-kernel', integration: 'temporality-agent-kernel', version: '0.1' },
    context: { project: 'calculator-e2e', run, actor: { id: 'test', type: 'agent' } },
    type,
    data,
  }
}

describe('chainRunIds', () => {
  const rows = [
    { id: 'calc-03', parent: null },
    { id: 'calc-03/coder', parent: 'calc-03' },
    { id: 'calc-03/coder/step', parent: 'calc-03/coder' },
    { id: 'calc-04', parent: null },
    { id: 'calc-04/lead', parent: 'calc-04' },
  ]

  it('walks up to the root and takes the whole subtree', () => {
    expect(chainRunIds(rows, 'calc-03/coder')).toEqual(['calc-03', 'calc-03/coder', 'calc-03/coder/step'])
    expect(chainRunIds(rows, 'calc-03')).toEqual(['calc-03', 'calc-03/coder', 'calc-03/coder/step'])
  })

  it('keeps other chains out', () => {
    expect(chainRunIds(rows, 'calc-04/lead')).toEqual(['calc-04', 'calc-04/lead'])
  })

  it('treats a run whose parent is outside the window as its own root', () => {
    expect(chainRunIds([{ id: 'calc-03/coder', parent: 'calc-03' }], 'calc-03/coder')).toEqual(['calc-03/coder'])
  })

  it('survives parent cycles', () => {
    const cyclic = [
      { id: 'x', parent: 'y' },
      { id: 'y', parent: 'x' },
    ]
    expect(chainRunIds(cyclic, 'x').sort()).toEqual(['x', 'y'])
  })

  it('returns just the selection when unknown', () => {
    expect(chainRunIds(rows, 'missing')).toEqual(['missing'])
  })
})

describe('fetchChainDisputes', () => {
  it('detects a dispute across two runs of the chain', async () => {
    vi.mocked(observationApi.events).mockImplementation(async (_project, _cursor, _src, _evt, filters) => {
      const run = filters?.run ?? ''
      const type = filters?.type ?? ''
      const byRun: Record<string, ObservationEvent[]> = {
        'calc/coder': [knowledgeEvent('knowledge.proposed', '2026-09-24T13:02:58Z', 'calc/coder', { knowledge_id: 'auto/k1', proposition: 'Tests pass without the fixture', kind: 'claim' })],
        'calc/reviewer': [knowledgeEvent('knowledge.disproved', '2026-09-24T13:17:43Z', 'calc/reviewer', { knowledge_id: 'auto/k1', reason: 'reviewer found the missing fixture' })],
      }
      const events = (byRun[run] ?? []).filter((event) => event.type === type)
      return { events, count: events.length }
    })

    const signals = await fetchChainDisputes('calculator-e2e', ['calc/coder', 'calc/reviewer'])
    expect(signals).toHaveLength(1)
    expect(signals[0].proposedBy).toBe('coder')
    expect(signals[0].challengedBy).toBe('reviewer')
    expect(signals[0].run).toBe('calc/reviewer')
    expect(signals[0].proposition).toContain('fixture')
  })

  it('stays silent when the same role corrects itself', async () => {
    vi.mocked(observationApi.events).mockImplementation(async (_project, _cursor, _src, _evt, filters) => {
      const run = filters?.run ?? ''
      const type = filters?.type ?? ''
      const events = [
        knowledgeEvent('knowledge.proposed', '2026-09-24T13:02:58Z', 'calc/coder', { knowledge_id: 'auto/k1', proposition: 'Claim', kind: 'claim' }),
        knowledgeEvent('knowledge.corrected', '2026-09-24T13:05:43Z', 'calc/coder', { knowledge_id: 'auto/k1', replacement_id: 'auto/k2' }),
      ].filter((event) => event.type === type && (!run || event.context?.run === run))
      return { events, count: events.length }
    })

    expect(await fetchChainDisputes('calculator-e2e', ['calc/coder'])).toHaveLength(0)
  })
})
