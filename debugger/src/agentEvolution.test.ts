import { describe, expect, it } from 'vitest'
import { detectRegressions, latestEvalByVersion, passRate, runsByVersion } from './agentEvolution'
import type { AgentEvaluationRun, Run } from './workspaceApi'

const evalRun = (id: number, version: number, passed: number, failed: number, createdAt: string): AgentEvaluationRun =>
  ({ id, agent_id: 'qa', agent_version: version, passed, failed, cases: [], created_at: createdAt })

const run = (version: number, status: string, turns: number): Run =>
  ({ id: `r-${version}-${status}-${turns}`, task_id: 't', project_id: 'p', agent_id: 'qa', agent_version: version, run_id: 'x', status, model: 'm', answer: '', turns, error: '', created_at: '', updated_at: '' })

describe('latestEvalByVersion', () => {
  it('keeps the newest run per version', () => {
    const runs = [
      evalRun(3, 2, 2, 0, '2026-10-10T10:00:00Z'),
      evalRun(2, 2, 1, 1, '2026-10-09T10:00:00Z'),
      evalRun(1, 1, 2, 0, '2026-10-08T10:00:00Z'),
    ]
    const latest = latestEvalByVersion(runs)
    expect(latest.size).toBe(2)
    expect(latest.get(2)!.id).toBe(3)
    expect(latest.get(1)!.id).toBe(1)
  })
})

describe('passRate', () => {
  it('returns null for empty runs', () => {
    expect(passRate(evalRun(1, 1, 0, 0, ''))).toBeNull()
  })
  it('computes the ratio', () => {
    expect(passRate(evalRun(1, 1, 3, 1, ''))).toBe(0.75)
  })
})

describe('runsByVersion', () => {
  it('aggregates outcomes and average turns', () => {
    const stats = runsByVersion([
      run(2, 'completed', 4),
      run(2, 'failed', 6),
      run(2, 'completed', 8),
      run(1, 'completed', 5),
    ])
    expect(stats.get(2)).toEqual({ total: 3, completed: 2, failed: 1, avgTurns: 6 })
    expect(stats.get(1)!.avgTurns).toBe(5)
  })
  it('keeps avgTurns null when no run reports turns', () => {
    const stats = runsByVersion([run(3, 'completed', 0)])
    expect(stats.get(3)).toEqual({ total: 1, completed: 1, failed: 0, avgTurns: null })
  })
})

describe('detectRegressions', () => {
  const SUITE_SAVED = '2026-10-01T00:00:00Z'

  it('flags a version whose pass rate dropped on the unchanged suite', () => {
    const regressions = detectRegressions([
      evalRun(2, 2, 1, 3, '2026-10-05T00:00:00Z'),
      evalRun(1, 1, 4, 0, '2026-10-03T00:00:00Z'),
    ], SUITE_SAVED)
    expect(regressions).toEqual([{ version: 2, previousVersion: 1, from: 1, to: 0.25 }])
  })

  it('ignores the drop when the suite changed between runs', () => {
    const regressions = detectRegressions([
      evalRun(2, 2, 1, 3, '2026-10-05T00:00:00Z'),
      evalRun(1, 1, 4, 0, '2026-10-03T00:00:00Z'),
    ], '2026-10-04T00:00:00Z') // v1 ran before the suite changed
    expect(regressions).toEqual([])
  })

  it('ignores improvements and equal rates', () => {
    const regressions = detectRegressions([
      evalRun(2, 2, 4, 0, '2026-10-05T00:00:00Z'),
      evalRun(1, 1, 2, 2, '2026-10-03T00:00:00Z'),
    ], SUITE_SAVED)
    expect(regressions).toEqual([])
  })

  it('needs a previous version to compare against', () => {
    expect(detectRegressions([evalRun(1, 1, 0, 4, '2026-10-05T00:00:00Z')], SUITE_SAVED)).toEqual([])
    // v3 without v2: no comparison
    expect(detectRegressions([
      evalRun(2, 3, 0, 4, '2026-10-05T00:00:00Z'),
      evalRun(1, 1, 4, 0, '2026-10-03T00:00:00Z'),
    ], SUITE_SAVED)).toEqual([])
  })
})
