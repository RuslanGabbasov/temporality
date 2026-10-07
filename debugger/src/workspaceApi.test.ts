import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { workspaceApi, type ExecutionIdentity } from './workspaceApi'

// workspaceApi rides the kernel proxy and the shared bearer token (api.ts).
const BASE = '/kernel-api'

function stubFetch(handler: (url: string, init?: RequestInit) => { status?: number; body: unknown }) {
  const calls: { url: string; init?: RequestInit }[] = []
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    calls.push({ url, init })
    const { status = 200, body } = handler(url, init)
    return {
      ok: status >= 200 && status < 300,
      status,
      json: async () => body,
      text: async () => JSON.stringify(body),
    }
  }))
  return calls
}

beforeEach(() => {
  vi.stubGlobal('localStorage', {
    getItem: (key: string) => (key === 'temporality_token' ? 'admin-token' : null),
    setItem: () => {},
    removeItem: () => {},
  })
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('workspaceApi unit roles', () => {
  it('lists the role grants of a unit', async () => {
    const calls = stubFetch(() => ({
      body: { roles: [{ user_id: 'ruslan', org_unit_id: 'unit-1', role: 'writer', granted_by: 'admin', granted_at: '2026-10-01T09:00:00Z' }] },
    }))
    const page = await workspaceApi.listUnitRoles('unit-1')
    expect(calls[0].url).toBe(`${BASE}/v1/org/units/unit-1/roles`)
    expect((calls[0].init?.headers as Record<string, string>).Authorization).toBe('Bearer admin-token')
    expect(page.roles[0].user_id).toBe('ruslan')
    expect(page.roles[0].role).toBe('writer')
  })

  it('grants a role via PUT with the role in the body', async () => {
    const calls = stubFetch(() => ({ body: { user_id: 'ruslan', org_unit_id: 'unit-1', role: 'writer' } }))
    const res = await workspaceApi.setUnitRole('unit-1', 'ruslan', 'writer')
    expect(calls[0].url).toBe(`${BASE}/v1/org/units/unit-1/roles/ruslan`)
    expect(calls[0].init?.method).toBe('PUT')
    expect(JSON.parse(String(calls[0].init?.body))).toEqual({ role: 'writer' })
    expect(res.role).toBe('writer')
  })

  it('revokes a role via DELETE', async () => {
    const calls = stubFetch(() => ({ body: { removed: true } }))
    const res = await workspaceApi.removeUnitRole('unit-1', 'ruslan')
    expect(calls[0].url).toBe(`${BASE}/v1/org/units/unit-1/roles/ruslan`)
    expect(calls[0].init?.method).toBe('DELETE')
    expect(res.removed).toBe(true)
  })
})

describe('workspaceApi execution identities', () => {
  const sample: ExecutionIdentity = {
    id: 'exec-1',
    name: 'PR Reviewer',
    description: 'Nightly reviews',
    org_unit_id: 'unit-1',
    allowed_agents: ['coder'],
    allowed_mcp: ['github'],
    allowed_providers: ['anthropic'],
    allowed_projects: ['temporality'],
    human_targets: ['ruslan'],
    created_at: '2026-10-01T09:00:00Z',
    updated_at: '2026-10-01T09:00:00Z',
  }

  it('creates an identity via POST', async () => {
    const calls = stubFetch(() => ({ status: 201, body: sample }))
    const res = await workspaceApi.createExecutionIdentity({ name: 'PR Reviewer', allowed_agents: ['coder'] })
    expect(calls[0].url).toBe(`${BASE}/v1/workspace/execution-identities`)
    expect(calls[0].init?.method).toBe('POST')
    expect(JSON.parse(String(calls[0].init?.body))).toMatchObject({ name: 'PR Reviewer', allowed_agents: ['coder'] })
    expect(res.id).toBe('exec-1')
  })

  it('updates an identity via PUT against its id', async () => {
    const calls = stubFetch(() => ({ body: sample }))
    const res = await workspaceApi.updateExecutionIdentity('exec-1', { name: 'PR Reviewer', human_targets: [] })
    expect(calls[0].url).toBe(`${BASE}/v1/workspace/execution-identities/exec-1`)
    expect(calls[0].init?.method).toBe('PUT')
    expect(JSON.parse(String(calls[0].init?.body))).toMatchObject({ name: 'PR Reviewer', human_targets: [] })
    expect(res.name).toBe('PR Reviewer')
  })

  it('deletes an identity via DELETE', async () => {
    const calls = stubFetch(() => ({ body: { deleted: true } }))
    const res = await workspaceApi.deleteExecutionIdentity('exec-1')
    expect(calls[0].url).toBe(`${BASE}/v1/workspace/execution-identities/exec-1`)
    expect(calls[0].init?.method).toBe('DELETE')
    expect(res.deleted).toBe(true)
  })

  it('lists identities', async () => {
    const calls = stubFetch(() => ({ body: { identities: [sample] } }))
    const page = await workspaceApi.listExecutionIdentities()
    expect(calls[0].url).toBe(`${BASE}/v1/workspace/execution-identities`)
    expect(page.identities).toHaveLength(1)
    expect(page.identities[0].human_targets).toEqual(['ruslan'])
  })
})

describe('workspaceApi error surface', () => {
  it('surfaces the error message from the response body', async () => {
    stubFetch(() => ({ status: 422, body: { error: 'role must be one of reader, writer, operator, admin' } }))
    await expect(workspaceApi.setUnitRole('unit-1', 'ruslan', 'root')).rejects.toThrow('role must be one of reader')
  })
})
