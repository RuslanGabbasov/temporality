import { describe, expect, it } from 'vitest'
import { TRACE_WINDOW, TRACE_WINDOW_STEP, windowedTrace, runAttempt, buildRunRows } from './AgentRuns'
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
