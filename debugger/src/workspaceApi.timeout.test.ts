import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, workspaceApi } from './workspaceApi'

// Incident 2026-10-21 (spec R3): request() must not hang forever on a wedged
// kernel — after the overall deadline it fails with a typed ApiError.

// workspaceApi rides the shared bearer token (api.ts) — stub the storage like
// workspaceApi.test.ts does.
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

describe('workspaceApi request timeout (spec R3, incident 2026-10-21)', () => {
  it('fails a hung request with a timeout ApiError after 60s', async () => {
    vi.useFakeTimers()
    // A wedged kernel: no response ever arrives; the request can only settle
    // when its AbortSignal fires.
    vi.stubGlobal('fetch', vi.fn((_input: unknown, init?: RequestInit) =>
      new Promise<Response>((_resolve, reject) => {
        const signal = init?.signal ?? undefined
        if (signal) {
          signal.addEventListener('abort', () => reject(signal.reason ?? new DOMException('aborted', 'AbortError')), { once: true })
        }
      })))

    let caught: unknown = null
    const pending = workspaceApi.listProjects().catch((e: unknown) => { caught = e })
    await vi.advanceTimersByTimeAsync(60_000)
    await pending

    expect(caught).toBeInstanceOf(ApiError)
    expect((caught as Error).message).toBe('request timed out after 60s')
    expect((caught as ApiError).status).toBe(408)
    expect((caught as ApiError).code).toBe('request timed out after 60s')
  })

  it('leaves fast responses untouched by the deadline', async () => {
    vi.useFakeTimers()
    vi.stubGlobal('fetch', vi.fn(async () => ({
      ok: true,
      status: 200,
      json: async () => ({ projects: [{ id: 'alpha', name: 'Alpha' }] }),
    } as unknown as Response)))

    const page = await workspaceApi.listProjects()
    expect(page.projects).toHaveLength(1)
    expect(page.projects[0].id).toBe('alpha')
  })
})
