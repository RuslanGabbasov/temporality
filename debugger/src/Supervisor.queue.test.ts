import { describe, expect, it } from 'vitest'
import { traceHref, supervisorAckedIds, supervisorPendingCount } from './Supervisor'
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

/** Behavior suspicion rows deep-link straight at the alarm source: the trace
 *  opens filtered to agent.behavior events and searched by the guard
 *  pattern — hundreds of steps collapse to the two rows the case is about. */
describe('traceHref', () => {
  const row = (over: Partial<Parameters<typeof traceHref>[0]> = {}) => ({
    key: 'k', cause: 'unknown', pattern: '', caseID: 'run_failed', subject: '',
    summary: '', recommendation: '', project: 'omni', count: 1, runs: [],
    eventIDs: [], lastAt: '', status: 'open', ...over,
  })

  it('plain cases link to the bare run trace', () => {
    expect(traceHref(row(), 'root')).toBe('/agents?project=omni&run=root')
  })

  it('behavior suspicions filter the trace to agent.behavior and search the pattern', () => {
    const href = traceHref(row({ caseID: 'behavior_suspicion', pattern: 'azure-devops tech.directum.ru' }), 'root/delegate/01')
    expect(href).toContain('view=trace')
    expect(href).toContain('traceType=agent.behavior')
    expect(href).toContain('traceQ=azure-devops')
    expect(href).toContain('run=root%2Fdelegate%2F01')
  })

  it('a behavior verdict without a pattern skips the search', () => {
    const href = traceHref(row({ caseID: 'behavior_suspicion' }), 'root')
    expect(href).toContain('traceType=agent.behavior')
    expect(href).not.toContain('traceQ')
  })
})
