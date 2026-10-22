import { afterEach, describe, expect, it, vi } from 'vitest'
import type { ObservationEvent } from './observationApi'
import {
  OPEN_REQUEST_BUDGET,
  RUN_LIST_PAGE,
  SELECTION_EVENTS_REQUESTS,
  TIMELINE_CATCHUP_PAGE,
  TIMELINE_FIRST_PAGE,
  buildRunRows,
  clearRunTimelineCache,
  fetchRunList,
  isAbortError,
  mergeRunEntries,
  openTabLoads,
  selectRunEvents,
  type RunListEntry,
} from './runsLoad'

// ── fixtures ─────────────────────────────────────────────────────────────────

function entry(overrides: Partial<RunListEntry> & { run_id: string }): RunListEntry {
  return {
    title: `run ${overrides.run_id}`,
    role: 'agent',
    parent_run_id: '',
    started_at: '2026-10-21T10:00:00Z',
    status: 'completed',
    source_id: 'kernel',
    ...overrides,
  }
}

function event(run: string, id: string, overrides: Partial<ObservationEvent> = {}): ObservationEvent {
  return {
    schema: 'temporality/observation/event/v1',
    event_id: id,
    occurred_at: `2026-10-21T10:00:0${id.length}Z`,
    received_at: `2026-10-21T10:00:0${id.length}Z`,
    source: { id: 'temporality-agent-kernel', integration: 'temporality-agent-kernel' },
    context: { run },
    type: 'tool.started',
    data: {},
    ...overrides,
  }
}

function abortError(): Error {
  const error = new Error('The operation was aborted')
  error.name = 'AbortError'
  return error
}

interface StubRoute {
  match: (url: string) => boolean
  status?: number
  body?: unknown
  /** Custom impl (hanging requests for abort tests). */
  impl?: (url: string, init?: RequestInit) => Promise<unknown>
}

/** fetch stub: routes by URL, records calls, speaks the Response subset every
 *  api layer in the app consumes ({ok,status,json,text}). */
function stubFetch(routes: StubRoute[]) {
  const fetchMock = vi.fn(async (input: unknown, init?: RequestInit) => {
    const url = String(input)
    const route = routes.find((candidate) => candidate.match(url))
    if (!route) throw new Error(`unrouted request: ${url}`)
    const status = route.status ?? 200
    const body = route.impl ? await route.impl(url, init) : route.body
    return {
      ok: status < 400,
      status,
      statusText: 'OK',
      json: async () => body,
      text: async () => JSON.stringify(body ?? ''),
    }
  })
  vi.stubGlobal('fetch', fetchMock)
  vi.stubGlobal('localStorage', { getItem: () => null, setItem: () => {}, removeItem: () => {}, clear: () => {} })
  return fetchMock
}

const isRunList = (url: string) => /\/v1\/agent\/runs\?/.test(url)
const isHumanRequests = (url: string) => url.includes('/v1/workspace/human-requests')
const isOperations = (url: string) => url.includes('/v1/agent/operations')
const isEvents = (url: string) => url.includes('/v1/observations/events')

afterEach(() => {
  vi.unstubAllGlobals()
  clearRunTimelineCache()
})

// ── perf-guard: request budgets (NFR-1 / NFR-2, DoD-3) ──────────────────────

describe('perf-guard budgets', () => {
  it('pins the documented budgets', () => {
    expect(OPEN_REQUEST_BUDGET).toBe(3)
    expect(SELECTION_EVENTS_REQUESTS).toBe(1)
    // spec §3E: list pages and timeline first pages are 50–100 rows/events
    expect(RUN_LIST_PAGE).toBeGreaterThanOrEqual(50)
    expect(RUN_LIST_PAGE).toBeLessThanOrEqual(100)
    expect(TIMELINE_FIRST_PAGE).toBeGreaterThanOrEqual(50)
    expect(TIMELINE_FIRST_PAGE).toBeLessThanOrEqual(100)
  })

  it('openTabLoads performs at most OPEN_REQUEST_BUDGET requests (NFR-1)', async () => {
    const fetchMock = stubFetch([
      { match: isRunList, body: { runs: [entry({ run_id: 'r1' })], count: 1 } },
      { match: isHumanRequests, body: { requests: [{ run_id: 'r2' }] } },
      { match: isOperations, body: { operations: [{ run_id: 'r1', operation_id: 'op1' }], count: 1 } },
    ])
    const opened = await openTabLoads({ project: 'lighthouse' })
    expect(fetchMock.mock.calls.length).toBeLessThanOrEqual(OPEN_REQUEST_BUDGET)
    expect(fetchMock.mock.calls.length).toBe(3)
    expect(opened.page.runs).toHaveLength(1)
    expect(opened.questionRunIDs).toEqual(['r2'])
    expect(opened.opsByRun.get('r1')?.[0]?.operation_id).toBe('op1')
    expect(opened.errors).toEqual({})
  })

  it('selectRunEvents costs exactly one journal request for a fresh run (NFR-2)', async () => {
    const fetchMock = stubFetch([
      { match: isEvents, body: { events: [event('r1', 'e1'), event('r1', 'e2')], count: 2 } },
    ])
    await selectRunEvents('r1')
    expect(fetchMock.mock.calls.length).toBe(SELECTION_EVENTS_REQUESTS)
    const [url] = fetchMock.mock.calls[0]
    expect(String(url)).toContain('run=r1')
    expect(String(url)).toContain(`limit=${TIMELINE_FIRST_PAGE}`)
  })

  it('a cached or in-flight run costs zero requests', async () => {
    let resolvePage: (value: unknown) => void = () => {}
    const fetchMock = vi.fn((_input: unknown, _init?: RequestInit) => new Promise((resolve) => { resolvePage = resolve }))
    vi.stubGlobal('fetch', fetchMock)
    vi.stubGlobal('localStorage', { getItem: () => null, setItem: () => {}, removeItem: () => {}, clear: () => {} })

    const first = selectRunEvents('r1')
    const second = selectRunEvents('r1') // joins the in-flight load
    expect(fetchMock.mock.calls.length).toBe(1)
    resolvePage({ ok: true, status: 200, statusText: 'OK', json: async () => ({ events: [event('r1', 'e1')], count: 1 }), text: async () => '' })
    await first
    await second

    await selectRunEvents('r1') // served from the cache
    expect(fetchMock.mock.calls.length).toBe(1)
  })
})

// ── open path / batch list ───────────────────────────────────────────────────

describe('fetchRunList', () => {
  it('requests the default batch page with the project and abort signal', async () => {
    const controller = new AbortController()
    const fetchMock = stubFetch([{ match: isRunList, body: { runs: [], count: 0 } }])
    await fetchRunList({ project: 'lighthouse', signal: controller.signal })
    const [url, init] = fetchMock.mock.calls[0]
    expect(String(url)).toContain('/kernel-api/v1/agent/runs?')
    expect(String(url)).toContain('project=lighthouse')
    expect(String(url)).toContain(`limit=${RUN_LIST_PAGE}`)
    expect(String(url)).not.toContain('cursor=')
    expect(init?.signal).toBe(controller.signal)
  })

  it('follows the cursor for older pages', async () => {
    const fetchMock = stubFetch([{ match: isRunList, body: { runs: [], count: 0 } }])
    await fetchRunList({ cursor: 'c-next', limit: 60 })
    const [url] = fetchMock.mock.calls[0]
    expect(String(url)).toContain('cursor=c-next')
    expect(String(url)).toContain('limit=60')
  })

  it('rejects on a non-OK response', async () => {
    stubFetch([{ match: isRunList, status: 500, body: { error: 'boom' } }])
    await expect(fetchRunList()).rejects.toThrow('500')
  })

  it('surfaces a partial page with unknown statuses instead of swallowing it (K-3)', async () => {
    stubFetch([{
      match: isRunList,
      body: {
        runs: [entry({ run_id: 'r1', status: 'unknown' })],
        count: 1,
        partial: true,
        partial_error: 'status lookup failed: db down',
      },
    }])
    const page = await fetchRunList()
    expect(page.partial).toBe(true)
    expect(page.partial_error).toContain('db down')
    expect(page.runs[0].status).toBe('unknown')
    // rows keep rendering — the flag drives the «statuses unavailable» UI
    const rows = buildRunRows(page.runs)
    expect(rows[0].status).toBe('unknown')
  })
})

describe('openTabLoads degradation', () => {
  it('keeps the list and reports the companion failure reason (K-3 spirit)', async () => {
    const fetchMock = stubFetch([
      { match: isRunList, body: { runs: [entry({ run_id: 'r1' })], count: 1 } },
      { match: isHumanRequests, status: 500, body: { error: 'nope' } },
      { match: isOperations, status: 503, body: { error: 'down' } },
    ])
    const opened = await openTabLoads()
    expect(fetchMock.mock.calls.length).toBe(3)
    expect(opened.page.runs).toHaveLength(1)
    // the reason reaches the caller verbatim (workspaceApi: body.error,
    // kernelApi: "status body") — the UI logs it instead of hiding it
    expect(opened.errors.questions).toBe('nope')
    expect(opened.errors.ops).toContain('503')
  })

  it('fails the open only when the batch list itself fails', async () => {
    stubFetch([
      { match: isRunList, status: 500, body: { error: 'boom' } },
      { match: isHumanRequests, body: { requests: [] } },
      { match: isOperations, body: { operations: [], count: 0 } },
    ])
    await expect(openTabLoads()).rejects.toThrow('500')
  })
})

describe('buildRunRows / mergeRunEntries', () => {
  it('nests children under loaded parents, roots first, newest runs first', () => {
    const rows = buildRunRows([
      entry({ run_id: 'root-old', started_at: '2026-10-21T09:00:00Z' }),
      entry({ run_id: 'root-new', started_at: '2026-10-21T11:00:00Z' }),
      entry({ run_id: 'child-1', parent_run_id: 'root-new', started_at: '2026-10-21T10:30:00Z' }),
      entry({ run_id: 'orphan', parent_run_id: 'not-loaded', started_at: '2026-10-21T08:00:00Z' }),
    ])
    expect(rows.map((row) => row.id)).toEqual(['root-new', 'child-1', 'root-old', 'orphan'])
    expect(rows[0].depth).toBe(0)
    expect(rows[1].depth).toBe(1)
    expect(rows[1].parent).toBe('root-new')
    expect(rows[3].depth).toBe(0) // unloaded parent → treated as a root
  })

  it('carries status and server-resolved agent names through (no extra fetch)', () => {
    const [row] = buildRunRows([entry({ run_id: 'r1', status: 'running', agent_id: 'dev', agent_name: 'Developer' })])
    expect(row.status).toBe('running')
    expect(row.agentId).toBe('dev')
    expect(row.agentName).toBe('Developer')
  })

  it('merges fresh pages: known runs update in place, loaded older rows survive', () => {
    const accumulated = new Map<string, RunListEntry>([
      ['r-old', entry({ run_id: 'r-old', status: 'running' })],
      ['r-keep', entry({ run_id: 'r-keep' })],
    ])
    const merged = mergeRunEntries(accumulated, [
      entry({ run_id: 'r-old', status: 'failed', title: 'renamed' }),
      entry({ run_id: 'r-new' }),
    ])
    expect([...merged.keys()].sort()).toEqual(['r-keep', 'r-new', 'r-old'])
    expect(merged.get('r-old')?.status).toBe('failed')
    expect(merged.get('r-old')?.title).toBe('renamed')
    // the input map is not mutated — silent refreshes diff against prev state
    expect(accumulated.get('r-old')?.status).toBe('running')
  })
})

// ── selection timeline: cursor catch-up, cache, abort ───────────────────────

describe('selectRunEvents', () => {
  it('catches up journal cursors after the first page and dedupes page seams', async () => {
    const fetchMock = stubFetch([
      { match: isEvents, impl: async (url) => url.includes('cursor=')
        ? { events: [event('r1', 'e2-dup'), event('r1', 'e3')], count: 2 }
        : { events: [event('r1', 'e1'), event('r1', 'e2-dup')], count: 2, next_cursor: 'c1' } },
    ])
    const events = await selectRunEvents('r1')
    // first page = the selection request, second = the preserved catch-up
    expect(fetchMock.mock.calls.length).toBe(2)
    expect(String(fetchMock.mock.calls[0][0])).toContain(`limit=${TIMELINE_FIRST_PAGE}`)
    expect(String(fetchMock.mock.calls[1][0])).toContain('cursor=c1')
    expect(String(fetchMock.mock.calls[1][0])).toContain(`limit=${TIMELINE_CATCHUP_PAGE}`)
    // same source:event_id across the page seam collapses to one row
    expect(events.map((item) => item.event_id)).toEqual(['e1', 'e2-dup', 'e3'])
  })

  it('refresh=true bypasses the cache and replaces the snapshot', async () => {
    const fetchMock = stubFetch([
      { match: isEvents, body: { events: [event('r1', 'e1')], count: 1 } },
    ])
    await selectRunEvents('r1')
    expect(fetchMock.mock.calls.length).toBe(1)
    fetchMock.mockImplementation(async (input: unknown, init?: RequestInit) => ({
      ok: true,
      status: 200,
      statusText: 'OK',
      json: async () => ({ events: [event('r1', 'e1'), event('r1', 'e2')], count: 2 }),
      text: async () => '',
    }) as never)
    const fresh = await selectRunEvents('r1', { refresh: true })
    expect(fetchMock.mock.calls.length).toBe(2)
    expect(fresh.map((item) => item.event_id)).toEqual(['e1', 'e2'])
    // the refreshed snapshot is the one served afterwards
    await selectRunEvents('r1')
    expect(fetchMock.mock.calls.length).toBe(2)
  })

  it('a failed refresh keeps serving the last good snapshot', async () => {
    const fetchMock = stubFetch([
      { match: isEvents, body: { events: [event('r1', 'e1')], count: 1 } },
    ])
    await selectRunEvents('r1')
    fetchMock.mockImplementation(async () => ({
      ok: false,
      status: 500,
      statusText: 'Internal Server Error',
      json: async () => ({ error: 'boom' }),
      text: async () => 'boom',
    }) as never)
    await expect(selectRunEvents('r1', { refresh: true })).rejects.toThrow('500')
    const cached = await selectRunEvents('r1')
    expect(cached.map((item) => item.event_id)).toEqual(['e1'])
    expect(fetchMock.mock.calls.length).toBe(2) // no extra request for the cached read
  })

  it('an aborted load rejects with AbortError and does not poison the cache', async () => {
    const abortable = vi.fn((input: unknown, init?: RequestInit) => {
      const url = String(input)
      const signal = init?.signal
      if (!signal) {
        return Promise.resolve({
          ok: true, status: 200, statusText: 'OK',
          json: async () => ({ events: [event('r1', 'e1')], count: 1 }),
          text: async () => '',
        })
      }
      return new Promise((_resolve, reject) => {
        const fail = () => reject(abortError())
        if (signal.aborted) fail()
        else signal.addEventListener('abort', fail)
      })
    })
    vi.stubGlobal('fetch', abortable)
    vi.stubGlobal('localStorage', { getItem: () => null, setItem: () => {}, removeItem: () => {}, clear: () => {} })

    const controller = new AbortController()
    const pending = selectRunEvents('r1', { signal: controller.signal })
    controller.abort()
    const failure = await pending.then(() => null, (error: unknown) => error)
    expect(failure).not.toBeNull()
    expect(isAbortError(failure)).toBe(true)

    // nothing cached → the next selection goes back to the network and works
    const events = await selectRunEvents('r1')
    expect(events.map((item) => item.event_id)).toEqual(['e1'])
    expect(abortable.mock.calls.length).toBe(2)
  })
})
