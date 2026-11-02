import { describe, expect, it } from 'vitest'
import { supervisorAckedIds, supervisorPendingCount } from './Supervisor'
import type { ObservationEvent } from './observationApi'

function verdict(eventID: string, data: Record<string, unknown>): ObservationEvent {
  return {
    schema: 'temporality.event/1',
    event_id: eventID,
    occurred_at: new Date().toISOString(),
    received_at: new Date().toISOString(),
    source: { id: 'kernel', integration: 'temporality-agent-kernel', version: '1' },
    context: { run: 'run-1', project: 'p' },
    type: 'supervisor.verdict',
    data,
  }
}

function ackEvent(eventID: string, data: Record<string, unknown>): ObservationEvent {
  return { ...verdict(eventID, data), type: 'supervisor.verdict.acknowledged' }
}

// The badge and the queue MUST share one bookkeeping: the durable ack table
// (where batch bulk-skips live), the legacy journal acks in both shapes, the
// reconciliation exclusion and the cluster-row counting. Divergence here was
// the phantom «97 unresolved» badge over an empty queue.
describe('supervisor queue accounting', () => {
  const verdicts = [
    verdict('v1', { case: 'run_failed', cluster_key: 'c1', summary: 'a' }),
    verdict('v2', { case: 'run_failed', cluster_key: 'c1', summary: 'a again' }),
    verdict('v3', { case: 'skill_candidate', cluster_key: 'c2', summary: 'b' }),
    // The supervisor's applied work: renders on its run, never a queue row.
    verdict('v4', { kind: 'reconciliation', run_id: 'run-9', summary: 'auto-closed' }),
  ]

  it('counts unresolved cluster rows, not raw events', () => {
    // v1+v2 cluster into ONE row: three events, two pending rows (v4 excluded).
    expect(supervisorPendingCount(verdicts, supervisorAckedIds([], []))).toBe(2)
  })

  it('honors durable acks (batch bulk-skips) and both journal ack shapes', () => {
    const durable = supervisorAckedIds([], ['v1']) // table row
    const journal = supervisorAckedIds(
      [ackEvent('ack-1', { verdict_event_id: 'v3' }), ackEvent('ack-2', { verdict_event_ids: ['v2'] })],
      [],
    )
    expect(supervisorPendingCount(verdicts, durable)).toBe(2)
    expect(supervisorPendingCount(verdicts, journal)).toBe(1) // only v1 left
  })

  it('excludes reconciliation verdicts even without acks', () => {
    const only = supervisorPendingCount([verdict('v4', { kind: 'reconciliation', run_id: 'r' })], new Set())
    expect(only).toBe(0)
  })

  it('a cluster row is pending until EVERY of its event ids is acked', () => {
    const partially = supervisorAckedIds([], ['v1'])
    expect(supervisorPendingCount(verdicts, partially)).toBe(2)
    const fully = supervisorAckedIds([], ['v1', 'v2'])
    expect(supervisorPendingCount(verdicts, fully)).toBe(1)
  })
})
