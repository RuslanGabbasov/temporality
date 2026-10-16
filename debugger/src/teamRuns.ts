import type { ObservationEvent } from './observationApi'

/** One team run row for the team card's «Запуски» tab. */
export interface TeamRunRow {
  run_id: string
  project: string
  version: string
  goal: string
  started_at: string
  status: 'completed' | 'failed' | 'cancelled' | 'running'
  /** Totals from team.completed (honest subtree rollup). */
  total_tokens?: number
  cost_usd?: number
  rework_rounds?: number
  /** team.failed anatomy: which step broke and why. */
  failed_step?: string
  error?: string
}

const dataOf = (event: ObservationEvent) => (event.data ?? {}) as Record<string, unknown>

/**
 * Build a team's run history from journal events: team.started rows enriched
 * with their terminal team.completed / team.failed (run.cancelled covers
 * operator cancellation — those runs never reach a team.* terminal event).
 * Pure so it can be unit-tested against event fixtures.
 */
export function teamRunsFromEvents(teamID: string, started: ObservationEvent[], completed: ObservationEvent[], failed: ObservationEvent[], cancelled: ObservationEvent[]): TeamRunRow[] {
  const rows = new Map<string, TeamRunRow>()
  for (const event of started) {
    const data = dataOf(event)
    if (String(data.team_id ?? '') !== teamID) continue
    const runID = event.context?.run ?? ''
    if (!runID) continue
    rows.set(runID, {
      run_id: runID,
      project: event.context?.project ?? '',
      version: String(data.version ?? ''),
      goal: String(data.goal ?? ''),
      started_at: event.occurred_at,
      status: 'running',
    })
  }
  for (const event of completed) {
    const runID = event.context?.run ?? ''
    const row = rows.get(runID)
    if (!row) continue
    const data = dataOf(event)
    row.status = 'completed'
    if (typeof data.total_tokens === 'number') row.total_tokens = data.total_tokens
    if (typeof data.cost_usd === 'number') row.cost_usd = data.cost_usd
    if (typeof data.rework_rounds === 'number') row.rework_rounds = data.rework_rounds
  }
  for (const event of failed) {
    const runID = event.context?.run ?? ''
    const row = rows.get(runID)
    if (!row) continue
    const data = dataOf(event)
    row.status = 'failed'
    if (typeof data.step === 'string') row.failed_step = data.step
    if (typeof data.error === 'string') row.error = data.error
  }
  for (const event of cancelled) {
    const runID = event.context?.run ?? ''
    const row = rows.get(runID)
    if (row && row.status === 'running') row.status = 'cancelled'
  }
  return [...rows.values()].sort((a, b) => b.started_at.localeCompare(a.started_at))
}
