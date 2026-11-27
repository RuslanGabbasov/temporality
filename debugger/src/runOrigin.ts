/** What a run is relative to its parent. An auto-continuation (a turn-limit
 *  restart `-r2`, or a `/continue/NN` spawned for an undelivered ask) runs the
 *  SAME agent on the SAME task — it is not a delegation, yet it carries
 *  parent_run_id, nests identically in the runs tree and inherits the parent's
 *  title, so without this classification a continuation reads as «the
 *  developer delegating to itself». */
export type RunOrigin = 'root' | 'continuation' | 'delegated'

/** Classify a run.started event. The explicit restart_attempt the kernel
 *  journals is the primary signal; the id suffixes are the history fallback
 *  for journals written before it existed — the same rules runAttempt in the
 *  runs list reads for the «× N» badge. Classification is independent of the
 *  parent being loaded: a continuation whose parent fell out of the list
 *  window still must not read as an independent run.
 *
 *  A trailing `-rN` is a restart signal only when the parent IS the stripped
 *  base run (or is unknown — the legacy journal). Plan-task rework rounds
 *  reuse the same suffix for a DELEGATED retry: `<run>/plan2/01-x-r2` hangs
 *  off the plan runner, not off the task's own first attempt, so counting it
 *  as an attempt inflated the head's chain (a ⟳ 7 badge against a restart
 *  budget of 5 — the kernel honored the budget, the classifier lied).
 *
 *  Structurally typed so the runs list and the trace timeline (which holds a
 *  decoded payload rather than the whole event) share one implementation. */
export function runOrigin(event: { data?: Record<string, unknown>; context?: { run?: string } }): RunOrigin {
  const data = event.data ?? {}
  const explicit = data.restart_attempt
  if (typeof explicit === 'number' && explicit > 0) return 'continuation'
  if (typeof explicit === 'string' && Number(explicit) > 0) return 'continuation'
  const id = event.context?.run ?? ''
  const restart = /-r\d+$/.exec(id)
  const resume = /\/continue\/\d+$/.exec(id)
  if (restart || resume) {
    const base = resume ? id.slice(0, -resume[0].length) : id.slice(0, -restart![0].length)
    const parent = data.parent_run_id ? String(data.parent_run_id) : ''
    if (!parent || parent === base) return 'continuation'
    return 'delegated'
  }
  const parent = data.parent_run_id ? String(data.parent_run_id) : ''
  return parent ? 'delegated' : 'root'
}
