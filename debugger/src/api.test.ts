import { afterEach, describe, expect, it, vi } from 'vitest'
import { API_BASE, apiUrl, authHeaders, authToken } from './api'

describe('apiUrl', () => {
  it('uses the configured base and encodes query values', () => {
    expect(apiUrl('/v1/observations/events', { project: 'lighthouse', limit: 50 })).toBe(`${API_BASE}/v1/observations/events?project=lighthouse&limit=50`)
  })

  it('omits empty and undefined values', () => {
    expect(apiUrl('v1/observations/events', { project: '', limit: undefined })).toBe(`${API_BASE}/v1/observations/events`)
  })
})

describe('authHeaders', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('sends the stored token as a bearer header', () => {
    vi.stubGlobal('localStorage', {
      getItem: (key: string) => (key === 'temporality_token' ? 'reader-token' : null),
      setItem: () => {},
      removeItem: () => {},
      clear: () => {},
    })
    expect(authToken()).toBe('reader-token')
    expect(authHeaders()).toEqual({ Authorization: 'Bearer reader-token' })
  })

  it('sends no authorization header without a token', () => {
    vi.stubGlobal('localStorage', {
      getItem: () => null,
      setItem: () => {},
      removeItem: () => {},
      clear: () => {},
    })
    expect(authToken()).toBe('')
    expect(authHeaders()).toEqual({})
  })
})
