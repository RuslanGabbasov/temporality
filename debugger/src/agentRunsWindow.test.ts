import { describe, expect, it } from 'vitest'
import { TRACE_WINDOW, TRACE_WINDOW_STEP, windowedTrace, runAttempt, buildRunRows, attemptChain, missingParents, attachParents, withParentRuns } from './AgentRuns'
import { runOrigin } from './runOrigin'
import type { ObservationEvent } from './observationApi'

const startedAt = (run: string, data?: Record<string, unknown>): ObservationEvent =>
  ({ type: 'run.started', occurred_at: '2026-11-05T06:21:10Z', source: { id: 's' }, context: { run }, data } as unknown as ObservationEvent)

/** The «× N» rerun badge source: a restarted or re-delegated run must be
 *  recognizable at a glance — by id suffix for history (…-r2, …/delegate/02)
 *  and by the explicit restart_attempt newer kernels journal. */
describe('runAttempt', () => {
  it('reads the explicit restart_attempt when the kernel journaled it', () => {
    expect(runAttempt(startedAt('task-20261105'))).toBe(0)
    expect(runAttempt(startedAt('task-20261105-r2', { restart_attempt: 3 }))).toBe(3)
  })

  it('parses the attempt from continuation and delegation id suffixes (history)', () => {
    expect(runAttempt(startedAt('opyat-…-045911-r1'))).toBe(1)
    expect(runAttempt(startedAt('opyat-…-045911-r12'))).toBe(12)
    expect(runAttempt(startedAt('opyat-…-045911/delegate/02'))).toBe(2)
    expect(runAttempt(startedAt('plain-run-id'))).toBe(0)
  })

  it('never badges the first attempt or garbage', () => {
    expect(runAttempt(startedAt('task/delegate/01'))).toBe(1)
    expect(runAttempt(startedAt('model-r'))).toBe(0)
    expect(runAttempt(startedAt('x/delegate/0'))).toBe(0)
  })
})

/** NFR-5 (docs/trace-rework/01-spec §3E): the trace renders a bounded window
 *  of the newest rows (50–100), extended by explicit request only — a
 *  4000-event run must never materialize thousands of DOM nodes. */
describe('NFR-5 trace window', () => {
  it('keeps the window inside the 50–100 range with an explicit 50-row step', () => {
    expect(TRACE_WINDOW).toBeGreaterThanOrEqual(50)
    expect(TRACE_WINDOW).toBeLessThanOrEqual(100)
    expect(TRACE_WINDOW_STEP).toBeGreaterThanOrEqual(50)
    expect(TRACE_WINDOW_STEP).toBeLessThanOrEqual(100)
  })

  it('renders only the newest TRACE_WINDOW rows of a huge run, newest first', () => {
    const rows = Array.from({ length: 4000 }, (_, index) => index)
    const windowed = windowedTrace(rows, TRACE_WINDOW)
    expect(windowed).toHaveLength(TRACE_WINDOW)
    expect(windowed[0]).toBe(3999) // newest row first — index 0 is the live row
    expect(windowed[windowed.length - 1]).toBe(4000 - TRACE_WINDOW)
  })

  it('shows the whole trace when it is shorter than the window', () => {
    expect(windowedTrace([1, 2, 3], TRACE_WINDOW)).toEqual([3, 2, 1])
    expect(windowedTrace([], TRACE_WINDOW)).toEqual([])
  })

  it('extension step keeps the window within the NFR-5 node budget', () => {
    // Per-row DOM of a collapsed trace row (row shell + summary line +
    // collapsed EventDetail) is bounded — see AgentRuns.tsx trace render.
    // 25 nodes × the worst-case window after many extensions stays explicit:
    // rows are added only by user clicks, never automatically.
    const rows = Array.from({ length: 4000 }, (_, index) => index)
    for (let extensions = 0; extensions < 5; extensions += 1) {
      const limit = TRACE_WINDOW + extensions * TRACE_WINDOW_STEP
      expect(windowedTrace(rows, limit).length).toBe(limit)
      expect(limit).toBeLessThanOrEqual(300)
    }
  })
})

/** Child runs read chronologically under their parent (the delegation story,
 *  oldest first) while roots keep the list's newest-first order. */
describe('buildRunRows', () => {
  const started = (run: string, at: string, parent?: string): ObservationEvent => ({
    type: 'run.started', occurred_at: at, source: { id: 's' }, context: { run },
    data: parent ? { parent_run_id: parent } : {},
  } as unknown as ObservationEvent)

  it('orders children chronologically, newest roots first', () => {
    const rows = buildRunRows([
      started('root-2', '2026-11-05T10:00:00Z'),
      started('root-1', '2026-11-05T09:00:00Z'),
      started('root-1/delegate/02', '2026-11-05T09:40:00Z', 'root-1'),
      started('root-1/delegate/01', '2026-11-05T09:20:00Z', 'root-1'),
    ], new Map())
    expect(rows.map((r) => r.id)).toEqual(['root-2', 'root-1', 'root-1/delegate/01', 'root-1/delegate/02'])
    expect(rows[0].depth).toBe(0)
    expect(rows[2].depth).toBe(1)
    expect(rows[2].parent).toBe('root-1')
  })
})

/** The list loads only the newest RUNS_LIMIT run.started events, so a
 *  delegation parent older than that window is absent from the page and
 *  buildRunRows would promote its child to a root — a run launched by
 *  delegation reading as an independent one. */
describe('delegation parents outside the loaded window', () => {
  const started = (run: string, at: string, parent?: string): ObservationEvent => ({
    type: 'run.started', occurred_at: at, source: { id: 's' }, context: { run },
    data: parent ? { parent_run_id: parent } : {},
  } as unknown as ObservationEvent)
  const runsOf = (events: ObservationEvent[]) => events.map((event) => event.context?.run ?? '')

  it('lists only the parents the page does not carry, deduplicated', () => {
    const page = [
      started('root/delegate/02', '2026-11-05T10:00:00Z', 'root'),
      started('root/delegate/01', '2026-11-05T09:00:00Z', 'root'),
      started('other/delegate/01', '2026-11-05T08:00:00Z', 'other'),
      started('in-page-parent', '2026-11-05T07:00:00Z'),
      started('child-of-loaded', '2026-11-05T06:00:00Z', 'in-page-parent'),
      started('self-parent', '2026-11-05T05:00:00Z', 'self-parent'),
    ]
    expect(missingParents(page)).toEqual(['root', 'other'])
  })

  it('splices a resolved parent before its newest child, leaving the rest in place', () => {
    const page = [
      started('new-root', '2026-11-05T10:00:00Z'),
      started('root/delegate/01', '2026-11-05T09:00:00Z', 'root'),
      started('older-root', '2026-11-05T08:00:00Z'),
    ]
    const attached = attachParents(page, [started('root', '2026-11-01T00:00:00Z')])
    expect(runsOf(attached)).toEqual(['new-root', 'root', 'root/delegate/01', 'older-root'])
  })

  it('attaches nothing when there is nothing to attach', () => {
    const page = [started('root', '2026-11-05T10:00:00Z')]
    expect(attachParents(page, [])).toBe(page)
    expect(runsOf(attachParents(page, [started('root', '2026-11-01T00:00:00Z')]))).toEqual(['root'])
  })

  it('never duplicates a parent the page already carries', () => {
    const page = [
      started('root', '2026-11-05T10:00:00Z'),
      started('root/delegate/01', '2026-11-05T09:00:00Z', 'root'),
    ]
    const attached = attachParents(page, [started('root', '2026-11-05T10:00:00Z')])
    expect(runsOf(attached)).toEqual(['root', 'root/delegate/01'])
  })

  it('restores the nesting buildRunRows could not do on its own', async () => {
    const page = [started('root/delegate/01', '2026-11-05T10:00:00Z', 'root')]
    let calls = 0
    const { events, statuses } = await withParentRuns(page, 'proj-nest', async (run) => {
      calls += 1
      return { started: started(run, '2026-11-01T00:00:00Z'), status: 'completed' as const }
    })
    expect(calls).toBe(1)
    expect(statuses.get('root')).toBe('completed')
    const rows = buildRunRows(events, statuses)
    expect(rows.map((row) => [row.id, row.depth])).toEqual([['root', 0], ['root/delegate/01', 1]])
    expect(runsOf(events)).toEqual(['root', 'root/delegate/01'])
  })

  it('caches resolved parents so the 30s poll does not refetch them', async () => {
    const page = [started('root/delegate/01', '2026-11-05T10:00:00Z', 'root')]
    let calls = 0
    const load = async (run: string) => {
      calls += 1
      return { started: started(run, '2026-11-01T00:00:00Z'), status: 'completed' as const }
    }
    await withParentRuns(page, 'proj-cache-a', load)
    const second = await withParentRuns(page, 'proj-cache-a', load)
    expect(calls).toBe(1)
    expect(runsOf(second.events)).toEqual(['root', 'root/delegate/01'])
    expect(second.statuses.get('root')).toBe('completed')
  })

  it('bounds one load to the fetch cap', async () => {
    const page = Array.from({ length: 5 }, (_, index) =>
      started(`root-${index}/delegate/01`, `2026-11-05T10:00:0${index}Z`, `root-${index}`))
    let calls = 0
    const load = async (run: string) => {
      calls += 1
      return { started: started(run, '2026-11-01T00:00:00Z'), status: 'completed' as const }
    }
    const { events } = await withParentRuns(page, 'proj-cap', load, 2)
    expect(calls).toBe(2)
    // Two groups restored, the three still missing stay roots as before.
    expect(runsOf(events)).toHaveLength(7)
  })

  it('keeps the child a root when its parent cannot be loaded', async () => {
    const page = [started('root/delegate/01', '2026-11-05T10:00:00Z', 'root')]
    const { events, statuses } = await withParentRuns(page, 'proj-gone', async () => { throw new Error('gone') })
    expect(events).toBe(page)
    expect(statuses.size).toBe(0)
    expect(buildRunRows(events, new Map()).map((row) => [row.id, row.depth])).toEqual([['root/delegate/01', 0]])
  })

  it('caches an authoritative miss instead of re-requesting it on every poll', async () => {
    const page = [started('root/delegate/01', '2026-11-05T10:00:00Z', 'root')]
    let calls = 0
    const load = async () => { calls += 1; return {} }
    const first = await withParentRuns(page, 'proj-miss', load)
    const second = await withParentRuns(page, 'proj-miss', load)
    expect(calls).toBe(1)
    expect(first.events).toBe(page)
    expect(second.events).toBe(page)
    expect(second.statuses.size).toBe(0)
  })
})

/** An auto-continuation runs the SAME agent on the SAME task and inherits the
 *  parent's title, so it nests and reads exactly like a delegation unless the
 *  row says what it is. */
describe('runOrigin', () => {
  const started = (run: string, data?: Record<string, unknown>, parent?: string): ObservationEvent => ({
    type: 'run.started', occurred_at: '2026-11-05T06:21:10Z', source: { id: 's' }, context: { run },
    data: { ...(data ?? {}), ...(parent ? { parent_run_id: parent } : {}) },
  } as unknown as ObservationEvent)

  it('reads the kernel-published restart_attempt first', () => {
    expect(runOrigin(started('task', { restart_attempt: 1 }, 'task'))).toBe('continuation')
    expect(runOrigin(started('task', { restart_attempt: '2' }, 'task'))).toBe('continuation')
  })

  it('falls back to the id suffixes for journals written before it', () => {
    expect(runOrigin(started('task-r2', {}, 'task'))).toBe('continuation')
    expect(runOrigin(started('task/continue/01', {}, 'task'))).toBe('continuation')
  })

  it('treats every other child as a delegation and a parentless run as a root', () => {
    expect(runOrigin(started('task/delegate/02', {}, 'task'))).toBe('delegated')
    expect(runOrigin(started('task/coder', {}, 'task'))).toBe('delegated')
    expect(runOrigin(started('task'))).toBe('root')
  })

  it('still marks a continuation whose parent is not in the list', () => {
    // No parent_run_id row to nest under — the badge must carry the origin.
    expect(runOrigin(started('task-r2'))).toBe('continuation')
  })

  it('lands on the rows buildRunRows emits', () => {
    const rows = buildRunRows([
      started('task', undefined, undefined),
      started('task/delegate/01', undefined, 'task'),
      started('task-r1', undefined, 'task'),
    ], new Map())
    expect(rows.map((row) => [row.id, row.origin])).toEqual([
      ['task', 'root'],
      ['task/delegate/01', 'delegated'],
      ['task-r1', 'continuation'],
    ])
  })
})

/** A restart is the same agent resuming the same task: the list folds the
 *  chain into one row with an ⟳ N marker instead of N rows that read as
 *  delegations to oneself (docs/agent-turn-limit-restarts.md). */
describe('continuation folding', () => {
  const started = (run: string, at: string, parent?: string): ObservationEvent => ({
    type: 'run.started', occurred_at: at, source: { id: 's' }, context: { run },
    data: parent ? { parent_run_id: parent } : {},
  } as unknown as ObservationEvent)
  const at = (minute: number) => `2026-11-05T10:${String(minute).padStart(2, '0')}:00Z`
  const row = (rows: ReturnType<typeof buildRunRows>, id: string) => {
    const found = rows.find((item) => item.id === id)
    if (!found) throw new Error(`no row ${id}`)
    return found
  }

  it('folds the whole attempt chain into its head', () => {
    const rows = buildRunRows([
      started('task', at(0)),
      started('task-r1', at(10), 'task'),
      started('task-r1-r2', at(20), 'task-r1'),
      started('task/delegate/01', at(30), 'task'),
    ], new Map())
    expect(row(rows, 'task').continuations).toBe(2)
    expect(row(rows, 'task').folded).toBe(false)
    expect(row(rows, 'task-r1').folded).toBe(true)
    expect(row(rows, 'task-r1-r2').folded).toBe(true)
    // Delegations are work items, not attempts: they stay their own rows.
    expect(row(rows, 'task/delegate/01').folded).toBe(false)
    expect(row(rows, 'task/delegate/01').depth).toBe(1)
  })

  it('re-parents a folded attempt\u2019s delegations to the head', () => {
    const rows = buildRunRows([
      started('task', at(0)),
      started('task-r1', at(10), 'task'),
      started('task-r1/delegate/01', at(20), 'task-r1'),
    ], new Map())
    const child = row(rows, 'task-r1/delegate/01')
    expect(child.folded).toBe(false)
    expect(child.parent).toBe('task')
    expect(child.depth).toBe(1)
  })

  it('folds a loaded segment into its topmost attempt when the head is gone', () => {
    const rows = buildRunRows([
      started('task-r1', at(10), 'task'),
      started('task-r1-r2', at(20), 'task-r1'),
    ], new Map())
    expect(row(rows, 'task-r1').folded).toBe(false)
    expect(row(rows, 'task-r1').depth).toBe(0)
    expect(row(rows, 'task-r1').continuations).toBe(1)
    expect(row(rows, 'task-r1-r2').folded).toBe(true)
  })

  it('leaves a lone attempt with no loaded ancestor as it was', () => {
    const rows = buildRunRows([
      started('task-r2', at(10), 'task'),
      started('task-r2/delegate/01', at(20), 'task-r2'),
    ], new Map())
    expect(row(rows, 'task-r2').folded).toBe(false)
    expect(row(rows, 'task-r2').depth).toBe(0)
    expect(row(rows, 'task-r2').continuations).toBe(0)
    expect(row(rows, 'task-r2/delegate/01').depth).toBe(1)
  })
})

/** The run card's attempt switcher: one control for the chain the list
 *  folded into a single row. */
describe('attemptChain', () => {
  const started = (run: string, at: string, parent?: string): ObservationEvent => ({
    type: 'run.started', occurred_at: at, source: { id: 's' }, context: { run },
    data: parent ? { parent_run_id: parent } : {},
  } as unknown as ObservationEvent)
  const at = (minute: number) => `2026-11-05T10:${String(minute).padStart(2, '0')}:00Z`
  const chain = (rows: ReturnType<typeof buildRunRows>, id: string) => attemptChain(rows, id).map((item) => item.id)

  it('walks head \u2192 attempts, oldest first, without delegations', () => {
    const rows = buildRunRows([
      started('task', at(0)),
      started('task-r1', at(10), 'task'),
      started('task-r1-r2', at(20), 'task-r1'),
      started('task/delegate/01', at(30), 'task'),
    ], new Map())
    expect(chain(rows, 'task')).toEqual(['task', 'task-r1', 'task-r1-r2'])
    expect(chain(rows, 'task-r1-r2')).toEqual(['task', 'task-r1', 'task-r1-r2'])
    expect(chain(rows, 'task/delegate/01')).toEqual(['task/delegate/01'])
    expect(chain(rows, 'missing')).toEqual([])
  })

  it('keeps the loaded segment when the head is not in the list', () => {
    const rows = buildRunRows([
      started('task-r1', at(10), 'task'),
      started('task-r1-r2', at(20), 'task-r1'),
    ], new Map())
    expect(chain(rows, 'task-r1-r2')).toEqual(['task-r1', 'task-r1-r2'])
  })

  it('is a single-entry chain for a run with no continuations', () => {
    const rows = buildRunRows([started('task', at(0))], new Map())
    expect(chain(rows, 'task')).toEqual(['task'])
  })

  it('keeps a delegated run and its continuations in their own chain', () => {
    const rows = buildRunRows([
      started('task', at(0)),
      started('task/delegate/01', at(10), 'task'),
      started('task/delegate/01-r1', at(20), 'task/delegate/01'),
      started('task-r1', at(30), 'task'),
    ], new Map())
    // A delegation never joins its parent's chain, and its continuation never
    // climbs past it: otherwise the selected run falls out of its own chain
    // and the switcher vanishes the moment you step onto it.
    expect(chain(rows, 'task/delegate/01')).toEqual(['task/delegate/01', 'task/delegate/01-r1'])
    expect(chain(rows, 'task/delegate/01-r1')).toEqual(['task/delegate/01', 'task/delegate/01-r1'])
    expect(chain(rows, 'task')).toEqual(['task', 'task-r1'])
  })
})
