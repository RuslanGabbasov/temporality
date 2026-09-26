import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  canReconcile,
  isReconcileEffect,
  operations,
  reasonText,
  reconcile,
  whoami,
  KERNEL_API,
} from './kernelApi'

const sampleOp = {
  project: 'repo-a',
  run_id: 'run-1',
  operation_id: 'run-1/turn/01/call-9',
  tool: 'run_command',
  arguments_hash: 'sha256:abc',
  started_at: '2026-09-25T18:17:33Z',
  started_event_id: 'agent-run-1/event/000007',
  source_id: 'temporality-agent-kernel',
  state: 'uncertain',
  reason: 'failed_uncertain',
}

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
    getItem: (key: string) => (key === 'temporality_token' ? 'operator-token' : null),
    setItem: () => {},
    removeItem: () => {},
  })
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('kernelApi', () => {
  it('operations lists uncertain operations for a project', async () => {
    const calls = stubFetch(() => ({ body: { operations: [sampleOp], count: 1 } }))
    const page = await operations('repo a')
    expect(calls[0].url).toBe(`${KERNEL_API}/v1/agent/operations?project=repo%20a`)
    expect((calls[0].init?.headers as Record<string, string>).Authorization).toBe('Bearer operator-token')
    expect(page.count).toBe(1)
    expect(page.operations[0].operation_id).toBe('run-1/turn/01/call-9')
    expect(page.operations[0].reason).toBe('failed_uncertain')
  })

  it('whoami reports the token identity', async () => {
    stubFetch(() => ({ body: { subject: 'ruslan', role: 'operator', projects: ['repo-a'], auth_enabled: true } }))
    const identity = await whoami()
    expect(identity.role).toBe('operator')
    expect(identity.auth_enabled).toBe(true)
  })

  it('reconcile posts the verdict and returns the receipt', async () => {
    const calls = stubFetch(() => ({ status: 201, body: { event_id: 'reconciled/abc', operation_id: sampleOp.operation_id, effect: 'occurred', recorded: true } }))
    const receipt = await reconcile({
      project: 'repo-a',
      run_id: 'run-1',
      operation_id: sampleOp.operation_id,
      effect: 'occurred',
      note: 'verified downstream',
      actor_id: 'ruslan',
      started_event_id: sampleOp.started_event_id,
    })
    expect(calls[0].url).toBe(`${KERNEL_API}/v1/agent/operations/reconcile`)
    expect(calls[0].init?.method).toBe('POST')
    const body = JSON.parse(String(calls[0].init?.body))
    expect(body).toMatchObject({ effect: 'occurred', actor_id: 'ruslan', started_event_id: sampleOp.started_event_id })
    expect(receipt.recorded).toBe(true)
    expect(receipt.event_id).toBe('reconciled/abc')
  })

  it('surfaces HTTP errors with the status code', async () => {
    stubFetch(() => ({ status: 403, body: { error: 'token has role reader, need operator' } }))
    await expect(reconcile({ project: 'repo-a', run_id: 'run-1', operation_id: 'op', effect: 'none', actor_id: 'x' })).rejects.toThrow(/^403 /)
  })
})

describe('operations UI helpers', () => {
  it('only operator and admin may record verdicts', () => {
    expect(canReconcile('reader')).toBe(false)
    expect(canReconcile('writer')).toBe(false)
    expect(canReconcile('operator')).toBe(true)
    expect(canReconcile('admin')).toBe(true)
  })

  it('exposes the closed effect vocabulary', () => {
    expect(isReconcileEffect('none')).toBe(true)
    expect(isReconcileEffect('occurred')).toBe(true)
    expect(isReconcileEffect('unknown')).toBe(true)
    expect(isReconcileEffect('maybe')).toBe(false)
  })

  it('explains the reasons in operator language', () => {
    expect(reasonText('crash_window')).toContain('terminal event')
    expect(reasonText('failed_uncertain')).toContain('execution boundary')
    expect(reasonText('something-new')).toBe('something-new')
  })
})
