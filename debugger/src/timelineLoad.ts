import type { ObservationEvent } from './observationApi'

// Load policy for the experience timeline (page `/experience`).
//
// The journal pages forward only — the cursor walks from the OLDEST event of
// the window towards the newest (`observation/cursor.go`, `AfterCursor`), so
// lazily loaded pages fill the window start-first. "Newest first" would need
// backward pagination on the backend, which does not exist.

/** Server-side load window: the timeline never needs the full history to be
 * useful — a week of runs is already thousands of events at team scale. */
export type RangeFilter = '24h' | '7d' | '30d' | 'all'
const RANGE_VALUES = ['24h', '7d', '30d', 'all'] as const

/** Default window on first open: a day of activity, not the whole corpus. */
export const DEFAULT_RANGE: RangeFilter = '24h'

export const RANGE_MS: Record<Exclude<RangeFilter, 'all'>, number> = { '24h': 86400e3, '7d': 7 * 86400e3, '30d': 30 * 86400e3 }

/** URL wins over the default; anything else falls back to the default. */
export function initialRange(params: URLSearchParams): RangeFilter {
  const value = params.get('range')
  return (RANGE_VALUES as readonly string[]).includes(value ?? '') ? (value as RangeFilter) : DEFAULT_RANGE
}

/** Events per journal request (the server caps `limit` at 500). */
export const PAGE_LIMIT = 500
/** Lazy-load budget: pages stream in until this many events are folded and
 * rendered; everything past it waits for an explicit «load more». */
export const AUTO_LOAD_BUDGET = 2000
/** Safety ceiling regardless of budget — the old full-corpus loop stopped at
 * 60 pages (30k events); keep that as the hard stop. */
export const HARD_PAGE_CAP = 60

export interface EventPage { events: ObservationEvent[]; next_cursor?: string }

/** Fetch journal pages up to the lazy budget (or the cursor end / hard cap) and
 * return the collected events with the cursor to resume from.
 *
 * `onPage` fires after every page so the caller can render progressively
 * instead of waiting for the whole budget — the first page paints immediately.
 */
export async function fetchBudgeted(
  fetchPage: (cursor?: string) => Promise<EventPage>,
  cursor?: string,
  onPage?: (events: ObservationEvent[], loaded: number, more: boolean) => void,
): Promise<EventPage> {
  const collected: ObservationEvent[] = []
  let current = cursor
  let pages = 0
  do {
    const page = await fetchPage(current)
    collected.push(...page.events)
    current = page.next_cursor || undefined
    pages += 1
    onPage?.(page.events, collected.length, Boolean(current))
    if (collected.length >= AUTO_LOAD_BUDGET) break
  } while (current && pages < HARD_PAGE_CAP)
  return { events: collected, next_cursor: current }
}
