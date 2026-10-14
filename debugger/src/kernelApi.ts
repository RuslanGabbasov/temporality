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
  // Investigation context: what was called, with which arguments, by which
  // agent, and why the effect is unresolved. Lets the operator answer without
  // opening the run trace (docs/failure-reconciliation.md).
  arguments?: string
  error_detail?: string
  agent_id?: string
  parent_run_id?: string
  title?: string
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

// First-run bootstrap status (docs/ROADMAP.md — onboarding): tells the wizard
// which pieces (admin user, provider, sandbox image) are still missing.
// INTAKE_PROJECT_ID mirrors the kernel-side system project (cmd/agent-kernel
// intake.go): it exists on every install, so UI "is this a fresh install"
// checks must ignore it.
export const INTAKE_PROJECT_ID = 'intake'

export interface SetupStatus {
  needs_setup: boolean
  auth_enabled: boolean
  master_key_configured: boolean
  model_configured: boolean
  sandbox: {
    configured: boolean
    image: string
    available: boolean
  }
}

export function setupStatus(): Promise<SetupStatus> {
  return request('/v1/setup/status')
}

export function buildSandbox(): Promise<{ image: string }> {
  return request('/v1/setup/sandbox/build', { method: 'POST' })
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

// Task budget spans every run of the task — conversation follow-ups and the
// full delegation tree — with summed time/tokens/cost (docs: long tasks that
// cross runs and agents must total their spend at the end).
export interface TaskRunBreakdown {
  run_id: string
  parent_run_id?: string
  agent_id?: string
  status: string
  started_at: string
  ended_at?: string
  duration_ms: number
  model_calls: number
  prompt_tokens: number
  cached_tokens?: number
  completion_tokens: number
  total_tokens: number
  cost_usd: number
}

export interface TaskAgentRollup {
  agent_id: string
  runs: number
  model_calls: number
  active_ms: number
  prompt_tokens: number
  cached_tokens?: number
  completion_tokens: number
  total_tokens: number
  cost_usd: number
}

export interface TaskCost {
  project: string
  task_id: string
  run_count: number
  model_calls: number
  first_started_at?: string
  last_ended_at?: string
  elapsed_ms: number
  active_ms: number
  total_prompt_tokens: number
  total_cached_tokens?: number
  total_completion_tokens: number
  total_tokens: number
  total_cost_usd: number
  runs: TaskRunBreakdown[]
  agents: TaskAgentRollup[]
}

export function taskCost(project: string, taskID: string): Promise<TaskCost> {
  return request(`/v1/agent/cost/task?project=${encodeURIComponent(project)}&task=${encodeURIComponent(taskID)}`)
}
