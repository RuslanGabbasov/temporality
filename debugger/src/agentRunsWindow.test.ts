import { describe, expect, it } from 'vitest'
import { TRACE_WINDOW, TRACE_WINDOW_STEP, windowedTrace } from './AgentRuns'

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
