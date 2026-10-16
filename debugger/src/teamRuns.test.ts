import { describe, expect, it } from 'vitest'
import type { ObservationEvent } from './observationApi'
import { teamRunsFromEvents } from './teamRuns'

function event(type: string, run: string, data: Record<string, unknown>, occurredAt = '2026-10-16T10:00:00Z', project = 'omni'): ObservationEvent {
  return {
    schema: 'temporality/observation/event/v1',
    event_id: `${type}-${run}-${occurredAt}`,
    occurred_at: occurredAt,
    received_at: occurredAt,
    source: { id: 'temporality-agent-kernel', integration: 'temporality-agent-kernel' },
    context: { run, project },
    type,
    data,
  }
}

describe('teamRunsFromEvents', () => {
  const started = [
    event('team.started', 'code-delivery-20261016-1', { team_id: 'code-delivery', version: '1.0.0', goal: 'ship it' }, '2026-10-16T10:00:00Z'),
    event('team.started', 'code-delivery-20261016-2', { team_id: 'code-delivery', version: '1.1.0', goal: 'fix it' }, '2026-10-16T11:00:00Z'),
    event('team.started', 'other-team-1', { team_id: 'other-team', version: '1.0.0', goal: 'not ours' }, '2026-10-16T12:00:00Z'),
  ]
  const completed = [
    event('team.completed', 'code-delivery-20261016-1', { team_id: 'code-delivery', status: 'completed', total_tokens: 175, cost_usd: 0.0123, rework_rounds: 1 }, '2026-10-16T10:05:00Z'),
  ]
  const failed = [
    event('team.failed', 'code-delivery-20261016-2', { team_id: 'code-delivery', step: 'coder', error: 'boom' }, '2026-10-16T11:02:00Z'),
  ]

  it('builds rows for this team only, newest first, with totals and failure anatomy', () => {
    const rows = teamRunsFromEvents('code-delivery', started, completed, failed, [])
    expect(rows).toHaveLength(2)
    expect(rows[0].run_id).toBe('code-delivery-20261016-2')
    expect(rows[0].status).toBe('failed')
    expect(rows[0].failed_step).toBe('coder')
    expect(rows[0].error).toBe('boom')
    expect(rows[1].run_id).toBe('code-delivery-20261016-1')
    expect(rows[1].status).toBe('completed')
    expect(rows[1].total_tokens).toBe(175)
    expect(rows[1].cost_usd).toBeCloseTo(0.0123)
    expect(rows[1].rework_rounds).toBe(1)
  })

  it('marks a still-running team run as running and honours run.cancelled', () => {
    const onlyStarted = [event('team.started', 'code-delivery-20261016-1', { team_id: 'code-delivery', version: '1.0.0', goal: 'ship it' })]
    expect(teamRunsFromEvents('code-delivery', onlyStarted, [], [], [])[0].status).toBe('running')
    const cancelled = [event('run.cancelled', 'code-delivery-20261016-1', {}, '2026-10-16T10:01:00Z')]
    expect(teamRunsFromEvents('code-delivery', onlyStarted, [], [], cancelled)[0].status).toBe('cancelled')
    // cancellation after a terminal team event must not overwrite it
    expect(teamRunsFromEvents('code-delivery', started, completed, [], [event('run.cancelled', 'code-delivery-20261016-1', {})])[1].status).toBe('completed')
  })

  it('ignores events without a run context', () => {
    const orphan = { ...event('team.started', '', { team_id: 'code-delivery', version: '1', goal: 'x' }) }
    orphan.context = {}
    expect(teamRunsFromEvents('code-delivery', [orphan], [], [], [])).toHaveLength(0)
  })
})
