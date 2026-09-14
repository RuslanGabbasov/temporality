import { afterEach, describe, expect, it, vi } from 'vitest'
import { formatDuration, modelProgress, modelTimeoutMs } from './modelProgress'

afterEach(() => vi.useRealTimers())

describe('model progress', () => {
  it('updates elapsed time and configured timeout countdown with fake timers', () => {
    vi.useFakeTimers(); vi.setSystemTime(new Date('2026-01-01T00:00:00Z'))
    const startedAt = Date.now()
    vi.advanceTimersByTime(65_000)
    expect(modelProgress(startedAt, Date.now(), 120_000)).toEqual({ elapsedMs: 65_000, elapsed: '01:05', timeout: '00:55', timedOut: false })
    vi.advanceTimersByTime(55_000)
    expect(modelProgress(startedAt, Date.now(), 120_000)).toMatchObject({ elapsed: '02:00', timeout: '00:00', timedOut: true })
  })

  it('formats durations and reads timeout from model config provenance', () => {
    expect(formatDuration(9_999)).toBe('00:09')
    expect(modelTimeoutMs({ configured: true, provenance: { timeout_seconds: 90 } })).toBe(90_000)
    expect(modelTimeoutMs({ configured: true })).toBeUndefined()
  })
})
