import { afterEach, describe, expect, it, vi } from 'vitest'
import { observationApi } from './observationApi'

function stubFetch(response: unknown) {
  const fetchMock = vi.fn().mockResolvedValue({
    ok: true,
    status: 200,
    statusText: 'OK',
    text: () => Promise.resolve(''),
    json: () => Promise.resolve(response),
  })
  vi.stubGlobal('fetch', fetchMock)
  vi.stubGlobal('localStorage', {
    getItem: () => null,
    setItem: () => {},
    removeItem: () => {},
    clear: () => {},
  })
  return fetchMock
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('observationApi.knowledge', () => {
  it('passes the explorer facet filters through', async () => {
    const fetchMock = stubFetch({ knowledge: [], count: 0 })
    await observationApi.knowledge('lighthouse', undefined, undefined, {
      q: 'docker', state: 'confirmed', topic: 'deploy', entity: 'tmpfs', origin: 'other', kind: 'observation',
    })
    const url = String(fetchMock.mock.calls[0][0])
    expect(url).toContain('project=lighthouse')
    expect(url).toContain('q=docker')
    expect(url).toContain('state=confirmed')
    expect(url).toContain('topic=deploy')
    expect(url).toContain('entity=tmpfs')
    expect(url).toContain('origin=other')
    expect(url).toContain('kind=observation')
  })
})

describe('observationApi.explore', () => {
  it('posts the operation body to the explore endpoint', async () => {
    const fetchMock = stubFetch({ operation: 'orient', areas: [], limits: { max_items: 0 }, completeness: 'complete' })
    await observationApi.explore({ project: 'lighthouse', operation: 'orient', question: 'миграции', max_items: 24 })
    const [url, init] = fetchMock.mock.calls[0]
    expect(String(url).endsWith('/v1/observations/knowledge/explore')).toBe(true)
    expect(init.method).toBe('POST')
    expect(JSON.parse(init.body)).toEqual({ project: 'lighthouse', operation: 'orient', question: 'миграции', max_items: 24 })
  })
})

describe('observationApi.eventById', () => {
  it('encodes the event id into the path', async () => {
    const fetchMock = stubFetch({ event: { event_id: 'r/event/000001' } })
    const result = await observationApi.eventById('r/event/000001', { source_id: 'kernel', project: 'lighthouse' })
    const url = String(fetchMock.mock.calls[0][0])
    expect(url).toContain(`/v1/observations/events/${encodeURIComponent('r/event/000001')}`)
    expect(url).toContain('source_id=kernel')
    expect(url).toContain('project=lighthouse')
    expect(result.event.event_id).toBe('r/event/000001')
  })
})
