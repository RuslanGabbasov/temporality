// Evolution analytics for agents (docs/plan-evaluable-agent.md §2.2 stage 9):
// per-version eval pass rate, regression detection and run outcomes, joined
// client-side from agent versions × evaluation runs × run statistics. Kept as
// pure functions so the aggregation and the regression rule are testable.

import type { AgentEvaluationRun, Run } from './workspaceApi'

/** Latest evaluation run per definition version (runs arrive newest-first). */
export function latestEvalByVersion(runs: AgentEvaluationRun[]): Map<number, AgentEvaluationRun> {
  const latest = new Map<number, AgentEvaluationRun>()
  for (const run of runs) {
    if (!latest.has(run.agent_version)) latest.set(run.agent_version, run)
  }
  return latest
}

/** Pass rate 0..1 for a run; null when the run has no cases. */
export function passRate(run: AgentEvaluationRun): number | null {
  const total = run.passed + run.failed
  return total > 0 ? run.passed / total : null
}

export interface RunStats {
  total: number
  completed: number
  failed: number
  avgTurns: number | null
}

/** Run outcomes grouped by agent definition version. */
export function runsByVersion(runs: Run[]): Map<number, RunStats> {
  const acc = new Map<number, { stats: RunStats; turns: number; withTurns: number }>()
  for (const run of runs) {
    const key = run.agent_version ?? 0
    const entry = acc.get(key) ?? { stats: { total: 0, completed: 0, failed: 0, avgTurns: null }, turns: 0, withTurns: 0 }
    entry.stats.total++
    if (run.status === 'completed') entry.stats.completed++
    if (run.status === 'failed' || run.status === 'cancelled') entry.stats.failed++
    if (typeof run.turns === 'number' && run.turns > 0) {
      entry.turns += run.turns
      entry.withTurns++
    }
    acc.set(key, entry)
  }
  const result = new Map<number, RunStats>()
  for (const [key, entry] of acc) {
    result.set(key, entry.withTurns > 0 ? { ...entry.stats, avgTurns: entry.turns / entry.withTurns } : entry.stats)
  }
  return result
}

/** True when both compared eval runs were executed against the current suite —
 * a pass-rate drop only means regression when the questions did not change. */
function sameSuite(runA?: AgentEvaluationRun, runB?: AgentEvaluationRun, suiteUpdatedAt?: string): boolean {
  if (!runA || !runB || !suiteUpdatedAt) return false
  const changed = Date.parse(suiteUpdatedAt)
  if (Number.isNaN(changed)) return false
  return Date.parse(runA.created_at) >= changed && Date.parse(runB.created_at) >= changed
}

export interface Regression {
  /** Version whose latest pass rate dropped. */
  version: number
  /** The version it dropped against. */
  previousVersion: number
  from: number
  to: number
}

/** Detect pass-rate regressions between consecutive definition versions:
 * version N regressed when its latest run is strictly worse than the previous
 * version's latest run AND both runs used the suite as it stands now (an
 * unchanged suite makes the comparison fair; otherwise the drop may just be
 * new, harder cases). */
export function detectRegressions(evalRuns: AgentEvaluationRun[], suiteUpdatedAt?: string): Regression[] {
  const latest = latestEvalByVersion(evalRuns)
  const versions = [...latest.keys()].sort((a, b) => a - b)
  const result: Regression[] = []
  for (const version of versions) {
    const previousVersion = version - 1
    if (!latest.has(previousVersion)) continue
    const current = passRate(latest.get(version)!)
    const previous = passRate(latest.get(previousVersion)!)
    if (current === null || previous === null) continue
    if (current < previous && sameSuite(latest.get(version), latest.get(previousVersion), suiteUpdatedAt)) {
      result.push({ version, previousVersion, from: previous, to: current })
    }
  }
  return result
}
