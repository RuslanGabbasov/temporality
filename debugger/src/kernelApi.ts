import { authHeaders } from './api'

export const KERNEL_API = '/kernel-api'

export interface UncertainOperation {
  project: string
  run_id: string
  operation_id: string
  tool: string
  server?: string
  arguments_hash?: string
  started_at: string
  started_event_id: string
  source_id: string
  state: string
  reason: string
  // Delegation context (docs/failure-reconciliation.md): set when the failed
  // tool is a delegate call, so the UI can point at the child trajectory.
  error_type?: string
  child_run_id?: string
  child_ops_total?: number
  child_ops_unresolved?: number
}

export interface Whoami {
  subject: string
  role: string
  projects: string[]
  auth_enabled: boolean
  user_id?: string // workspace_user id when the token belongs to a DB user
  org_unit_id?: string // caller's primary unit (docs/org-structure.md §11)
}

export interface LoginResponse {
  token: string
  user_id: string
  subject: string
}

export type ReconcileEffect = 'none' | 'occurred' | 'unknown'

export interface ReconcileRequest {
  project: string
  run_id: string
  operation_id: string
  effect: ReconcileEffect
  note?: string
  actor_id: string
  started_event_id?: string
}

export interface ReconcileReceipt {
  event_id: string
  operation_id: string
  effect: string
  recorded: boolean
}

const EFFECTS: ReconcileEffect[] = ['none', 'occurred', 'unknown']

export function isReconcileEffect(value: string): value is ReconcileEffect {
  return EFFECTS.includes(value as ReconcileEffect)
}

// Roles that may record reconciliation verdicts (the kernel enforces the
// same ladder server-side; this drives the UI affordances).
export function canReconcile(role: string): boolean {
  return role === 'operator' || role === 'admin'
}

const REASON_TEXT: Record<string, string> = {
  crash_window: 'workflow ended before the terminal event landed — the effect may or may not have executed',
  stale_in_flight: 'no terminal event while the workflow still runs — the activity appears stuck',
  failed_uncertain: 'the tool failed at the execution boundary — the effect may have landed downstream',
}

export function reasonText(reason: string): string {
  return REASON_TEXT[reason] ?? reason
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(`${KERNEL_API}${path}`, {
    ...init,
    headers: { ...(init?.body ? { 'Content-Type': 'application/json' } : {}), ...authHeaders() },
  })
  if (!response.ok) throw new Error(`${response.status} ${await response.text()}`)
  return response.json() as Promise<T>
}

export function operations(project: string): Promise<{ operations: UncertainOperation[]; count: number }> {
  return request(`/v1/agent/operations?project=${encodeURIComponent(project)}`)
}

export function whoami(): Promise<Whoami> {
  return request('/v1/agent/whoami')
}

// Password login: exchanges name-or-email + password for the user's bearer
// token, which then lives in localStorage exactly like a pasted token.
export function login(login: string, password: string): Promise<LoginResponse> {
  return request('/v1/auth/login', { method: 'POST', body: JSON.stringify({ login, password }) })
}

export function reconcile(body: ReconcileRequest): Promise<ReconcileReceipt> {
  return request('/v1/agent/operations/reconcile', { method: 'POST', body: JSON.stringify(body) })
}

// Run cost is aggregated by the kernel from model.completed events
// (docs: cost accounting); cached prompt tokens are billed at half price.
export interface RunCost {
  run_id: string
  project: string
  total_cost_usd: number
  total_prompt_tokens: number
  total_cached_tokens?: number
  total_completion_tokens: number
  call_count: number
}

export function runCost(project: string, runID: string): Promise<RunCost> {
  return request(`/v1/agent/cost/run?project=${encodeURIComponent(project)}&run=${encodeURIComponent(runID)}`)
}
