import type { ModelConfig } from './types'

export function formatDuration(milliseconds: number): string {
  const totalSeconds = Math.max(0, Math.floor(milliseconds / 1000))
  return `${String(Math.floor(totalSeconds / 60)).padStart(2, '0')}:${String(totalSeconds % 60).padStart(2, '0')}`
}

export function modelTimeoutMs(config: ModelConfig | null): number | undefined {
  if (!config) return undefined
  const values: Array<[unknown, number]> = [
    [config.timeout_ms, 1],
    [config.timeout_seconds, 1000],
    [config.provenance?.timeout_ms, 1],
    [config.provenance?.timeout_seconds, 1000],
  ]
  for (const [value, multiplier] of values) {
    if (typeof value === 'number' && Number.isFinite(value) && value > 0) return value * multiplier
  }
  return undefined
}

export function modelProgress(startedAt: number, now: number, timeoutMs?: number) {
  const elapsedMs = Math.max(0, now - startedAt)
  return {
    elapsedMs,
    elapsed: formatDuration(elapsedMs),
    timeout: timeoutMs === undefined ? undefined : formatDuration(Math.max(0, timeoutMs - elapsedMs)),
    timedOut: timeoutMs !== undefined && elapsedMs >= timeoutMs,
  }
}

export function isAbortError(error: unknown): boolean {
  return error instanceof DOMException ? error.name === 'AbortError' : error instanceof Error && error.name === 'AbortError'
}
