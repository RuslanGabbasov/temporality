import { describe, expect, it } from 'vitest'
import type { ObservationEvent } from './observationApi'
import { AUTO_LOAD_BUDGET, DEFAULT_RANGE, fetchBudgeted, HARD_PAGE_CAP, initialRange, PAGE_LIMIT } from './timelineLoad'

const ev = (id: string) => ({ event_id: id }) as unknown as ObservationEvent

describe('initialRange', () => {
  it('opens on the 24-hour window by default', () => {
    expect(DEFAULT_RANGE).toBe('24h')
    expect(initialRange(new URLSearchParams(''))).toBe('24h')
    expect(initialRange(new URLSearchParams('range=nope'))).toBe('24h')
  })

  it('honours a valid range from the URL', () => {
    for (const value of ['24h', '7d', '30d', 'all'] as const) {
      expect(initialRange(new URLSearchParams(`range=${value}`))).toBe(value)
    }
  })
})

/** Fake journal: `pageSize` events per page, cursors `pN` until `pages` run out. */
function fakeJournal(pages: number, pageSize: number) {
  const calls: (string | undefined)[] = []
  const fetchPage = async (cursor?: string) => {
    calls.push(cursor)
    const index = calls.length // 1-based page number
    return {
      events: Array.from({ length: pageSize }, (_, i) => ev(`p${index}-${i}`)),
      next_cursor: index < pages ? `p${index}` : undefined,
    }
  }
  return { calls, fetchPage }
}

describe('fetchBudgeted', () => {
  it('streams pages progressively and stops at the lazy budget', async () => {
    const { calls, fetchPage } = fakeJournal(20, PAGE_LIMIT) // 10k events available
    const seen: number[] = []
    const page = await fetchBudgeted(fetchPage, undefined, (_events, loaded) => seen.push(loaded))
    // The old full-corpus loop would make 20 requests before rendering.
    expect(calls.length).toBe(AUTO_LOAD_BUDGET / PAGE_LIMIT)
    expect(page.events.length).toBe(AUTO_LOAD_BUDGET)
    expect(page.next_cursor).toBe(`p${AUTO_LOAD_BUDGET / PAGE_LIMIT}`)
    // Per-page callback: the caller renders after each page, not at the end.
    expect(seen).toEqual([500, 1000, 1500, 2000])
  })

  it('stops early when the cursor runs out', async () => {
    const { calls, fetchPage } = fakeJournal(2, PAGE_LIMIT)
    const page = await fetchBudgeted(fetchPage)
    expect(calls.length).toBe(2)
    expect(page.events.length).toBe(2 * PAGE_LIMIT)
    expect(page.next_cursor).toBeUndefined()
  })

  it('resumes from the cursor the previous load stopped at', async () => {
    const { calls, fetchPage } = fakeJournal(3, PAGE_LIMIT)
    await fetchBudgeted(fetchPage, 'resume-here')
    expect(calls[0]).toBe('resume-here')
  })

  it('keeps the hard page cap even when the budget is never reached', async () => {
    let n = 0
    const fetchPage = async () => { n += 1; return { events: [ev(`e${n}`)], next_cursor: `c${n}` } }
    const page = await fetchBudgeted(fetchPage)
    expect(n).toBe(HARD_PAGE_CAP)
    expect(page.events.length).toBe(HARD_PAGE_CAP)
    expect(page.next_cursor).toBe(`c${HARD_PAGE_CAP}`)
  })
})
