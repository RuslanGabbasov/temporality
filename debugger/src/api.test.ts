import { afterEach, describe, expect, it, vi } from 'vitest'
import { API_BASE, api, apiUrl } from './api'

afterEach(() => vi.unstubAllGlobals())

describe('apiUrl', () => {
  it('uses the configured base and encodes query values', () => {
    expect(apiUrl('/v1/events', { episode_id: 'ep one', limit: 50 })).toBe(`${API_BASE}/v1/events?episode_id=ep+one&limit=50`)
  })

  it('omits empty and undefined values', () => {
    expect(apiUrl('v1/events', { episode_id: '', limit: undefined })).toBe(`${API_BASE}/v1/events`)
  })
})

describe('operational API', () => {
  it('uses typed endpoint methods and JSON request bodies', async () => {
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(new Response(JSON.stringify({ configured: true }), { status: 200, headers: { 'Content-Type': 'application/json' } })))
    vi.stubGlobal('fetch', fetchMock)
    await api.createObjective({ objective: { objective_id: 'o', episode_id: 'e', text: 'work', success_conditions: [], constraints: {} }, event: { payload: {}, provenance: {} } })
    await api.createFrame({ frame: { frame_id: 'f', agent_id: 'a', episode_id: 'e', branch_id: 'b', objective_id: 'o', focus: { type: 'query', query: 'work' }, mode: 'explore', attention: { policy: 'balanced', deliberate: true, ambient: true, max_candidates: 32 }, filters: { trust_min: 0.5 }, budget: { tokens: 1000 } }, event: { payload: {}, provenance: {} } })
    await api.rebuildRegions('e', 'b')
    await api.modelStep({ frame_id: 'f', objective_id: 'o', budget_tokens: 1000, definitions: [] })
    await api.modelConfig()
    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual([`${API_BASE}/v1/objectives`, `${API_BASE}/v1/frames`, `${API_BASE}/v1/projections/regions/rebuild`, `${API_BASE}/v1/model-step`, `${API_BASE}/v1/model/config`])
    expect(fetchMock.mock.calls.slice(0, 4).every(([, init]) => init.method === 'POST')).toBe(true)
    expect(JSON.parse(fetchMock.mock.calls[3][1].body)).toEqual({ frame_id: 'f', objective_id: 'o', budget_tokens: 1000, definitions: [] })
  })

  it('sends the exact frame transition contract', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ frame: { frame_id: 'instruction' }, event: {} }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
    vi.stubGlobal('fetch', fetchMock)
    await api.transitionFrame('frame/one', {
      transition: { operations: [{ op: 'attend', focus: { type: 'query', query: 'What next?' } }] },
      event: { payload: {}, provenance: { source: 'debugger', kind: 'user_follow_up' } },
    })
    expect(fetchMock).toHaveBeenCalledWith(`${API_BASE}/v1/frames/frame%2Fone/transitions`, expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({ transition: { operations: [{ op: 'attend', focus: { type: 'query', query: 'What next?' } }] }, event: { payload: {}, provenance: { source: 'debugger', kind: 'user_follow_up' } } }),
    }))
  })

  it('sends the exact render, blame, and atomic fork contracts', async () => {
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(new Response(JSON.stringify({ fork_group_id: 'group', source_frame_id: 'frame', branches: [] }), { status: 200, headers: { 'Content-Type': 'application/json' } })))
    vi.stubGlobal('fetch', fetchMock)
    await api.render({ frame_id: 'frame', objective_id: 'objective', budget_tokens: 4000 })
    await api.blame({ root_id: 'claim-or-event-or-frame', max_depth: 8 })
    await api.fork({ source_frame_id: 'frame', branches: [{ branch_id: 'a', label: 'A', model_config: {} }, { branch_id: 'b', label: 'B', model_config: { model: 'test' } }] })

    expect(fetchMock.mock.calls.map(([url, init]) => ({ url, method: init.method, body: JSON.parse(init.body) }))).toEqual([
      { url: `${API_BASE}/v1/render`, method: 'POST', body: { frame_id: 'frame', objective_id: 'objective', budget_tokens: 4000 } },
      { url: `${API_BASE}/v1/blame`, method: 'POST', body: { root_id: 'claim-or-event-or-frame', max_depth: 8 } },
      { url: `${API_BASE}/v1/fork`, method: 'POST', body: { source_frame_id: 'frame', branches: [{ branch_id: 'a', label: 'A', model_config: {} }, { branch_id: 'b', label: 'B', model_config: { model: 'test' } }] } },
    ])
  })

  it('passes an AbortSignal to the model-step fetch contract', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } }))
    vi.stubGlobal('fetch', fetchMock)
    const controller = new AbortController()
    await api.modelStep({ frame_id: 'f', objective_id: 'o', budget_tokens: 1, definitions: [] }, controller.signal)
    expect(fetchMock.mock.calls[0][1].signal).toBe(controller.signal)
  })

  it('includes HTTP response details in errors', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('{"error":"bad model"}', { status: 502, statusText: 'Bad Gateway' })))
    await expect(api.modelStep({ frame_id: 'f', objective_id: 'o', budget_tokens: 1, definitions: [] })).rejects.toThrow('502 Bad Gateway — {"error":"bad model"}')
  })
})
