import { describe, expect, it } from 'vitest'
import { API_BASE, apiUrl } from './api'

describe('apiUrl', () => {
  it('uses the configured base and encodes query values', () => {
    expect(apiUrl('/v1/events', { episode_id: 'ep one', limit: 50 })).toBe(`${API_BASE}/v1/events?episode_id=ep+one&limit=50`)
  })

  it('omits empty and undefined values', () => {
    expect(apiUrl('v1/events', { episode_id: '', limit: undefined })).toBe(`${API_BASE}/v1/events`)
  })
})
