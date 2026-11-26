import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { observationApi } from './observationApi'

// The pending-request storm (net::ERR_INSUFFICIENT_RESOURCES while a run sat
// on a human form): journal GETs had NO deadline, so a wedged journal piled up
// every poller's requests as eternally-pending fetches. request() must fail a
// hung call at the deadline instead of pinning the socket forever.

// observationApi rides the shared bearer token (api.ts) — stub the storage
// like workspaceApi.timeout.test.ts does.
beforeEach(() => {
  vi.stubGlobal('localStorage', {
    getItem: (key: string) => (key === 'temporality_token' ? 'admin-token' : null),
    setItem: () => {},
    removeItem: () => {},
  })
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

describe('observationApi request timeout', () => {
  it('fails a hung request after 60s instead of pinning it forever', async () => {
    vi.useFakeTimers()
    // A wedged journal: no response ever arrives; the request can only settle
    // when its AbortSignal fires.
    vi.stubGlobal('fetch', vi.fn((_input: unknown, init?: RequestInit) =>
      new Promise<Response>((_resolve, reject) => {
        const signal = init?.signal ?? undefined
        if (signal) {
          signal.addEventListener('abort', () => reject(signal.reason ?? new DOMException('aborted', 'AbortError')), { once: true })
        }
      })))

    let caught: unknown = null
    const pending = observationApi.events('proj', undefined, undefined, undefined, { type: 'run.started', limit: 100 }).catch((e: unknown) => { caught = e })
    await vi.advanceTimersByTimeAsync(60_000)
    await pending

    expect(caught).toBeInstanceOf(Error)
    expect((caught as Error).message).toContain('timed out after 60s')
  })

  it('leaves fast responses untouched by the deadline', async () => {
    vi.useFakeTimers()
    vi.stubGlobal('fetch', vi.fn(async () => ({
      ok: true,
      status: 200,
      json: async () => ({ events: [], count: 0 }),
    } as unknown as Response)))

    const page = await observationApi.events('proj', undefined, undefined, undefined, { types: ['run.started'], limit: 10 })
    expect(page.events).toHaveLength(0)
    expect(page.count).toBe(0)
  })

  it('honors a longer timeoutMs override (embedding map keeps its 120s window)', async () => {
    vi.useFakeTimers()
    vi.stubGlobal('fetch', vi.fn((_input: unknown, init?: RequestInit) =>
      new Promise<Response>((_resolve, reject) => {
        const signal = init?.signal ?? undefined
        if (signal) {
          signal.addEventListener('abort', () => reject(signal.reason ?? new DOMException('aborted', 'AbortError')), { once: true })
        }
      })))

    let caught: unknown = null
    const pending = observationApi.knowledgeEmbeddingMap({ project: 'proj' }).catch((e: unknown) => { caught = e })
    // The default 60s deadline must NOT kill it — the call opted into 120s.
    await vi.advanceTimersByTimeAsync(60_000)
    expect(caught).toBeNull()
    await vi.advanceTimersByTimeAsync(60_000)
    await pending

    expect(caught).toBeInstanceOf(Error)
    expect((caught as Error).message).toContain('timed out after 120s')
  })
})
