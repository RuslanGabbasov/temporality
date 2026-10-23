import { useEffect, useState } from 'react'

/**
 * Shared debounce for client-side filter/search inputs (NFR-9): the value a
 * consumer reacts to lags the keystroke by ≥300 ms, so filtering over the
 * already-loaded window re-runs at human typing cadence, not per character.
 * The network is never touched here — the committed value feeds pure
 * client-side filtering (traceFilters) only.
 */

/** §8/NFR-9 minimum debounce. Exported so tests pin the contract. */
export const TRACE_DEBOUNCE_MS = 300

export function useDebouncedValue<T>(value: T, delay: number = TRACE_DEBOUNCE_MS): T {
  const [committed, setCommitted] = useState(value)
  useEffect(() => {
    const timer = window.setTimeout(() => setCommitted(value), delay)
    return () => window.clearTimeout(timer)
  }, [value, delay])
  return committed
}
