import { describe, expect, it } from 'vitest'
import { collapseRouteMarkers } from './traceModel'
import type { ObservationEvent } from './observationApi'

const routed = (route: string, id: string): ObservationEvent => ({
  event_id: id,
  type: 'model.attempt',
  occurred_at: '2026-11-02T00:00:00Z',
  source: { id: 'kernel', integration: 'x', version: '1' },
  context: { project: 'p', actor: { id: 'system', type: 'agent' } },
  data: { reason: 'routed', route },
} as unknown as ObservationEvent)

const other = (type: string, id: string): ObservationEvent => ({
  event_id: id,
  type,
  occurred_at: '2026-11-02T00:00:01Z',
  source: { id: 'kernel', integration: 'x', version: '1' },
  context: { project: 'p', actor: { id: 'system', type: 'agent' } },
  data: {},
} as unknown as ObservationEvent)

describe('collapseRouteMarkers', () => {
  it('keeps the first route and drops identical repeats around other rows', () => {
    const events = [
      other('run.started', 'e0'),
      routed('provider Xiaomi', 'e1'),
      other('model.completed', 'e2'),
      routed('provider Xiaomi', 'e3'),
      other('tool.started', 'e4'), // retry markers are not routes — keep
      routed('provider Xiaomi', 'e5'),
    ]
    const out = collapseRouteMarkers(events)
    expect(out.map((e) => e.event_id)).toEqual(['e0', 'e1', 'e2', 'e4'])
  })

  it('keeps every route CHANGE — a different provider is news', () => {
    const events = [
      routed('provider Xiaomi', 'e1'),
      routed('provider Xiaomi', 'e2'),
      routed('env fallback', 'e3'),
      routed('provider Xiaomi', 'e4'),
    ]
    const out = collapseRouteMarkers(events)
    expect(out.map((e) => e.event_id)).toEqual(['e1', 'e3', 'e4'])
  })

  it('leaves non-route events untouched', () => {
    const events = [other('run.started', 'e0'), other('model.attempt', 'e1'), other('run.completed', 'e2')]
    expect(collapseRouteMarkers(events)).toHaveLength(3)
  })
})
