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
  liveness_error: 'workflow state could not be checked — the effect question stays open until reconciled',
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

// The project filter is optional (docs/activity-scoping.md §5): an empty
// value asks for the global listing, which the server constrains to the
// caller's visible projects.
export function operations(project?: string): Promise<{ operations: UncertainOperation[]; count: number }> {
  const suffix = project && project.trim() ? `?project=${encodeURIComponent(project.trim())}` : ''
  return request(`/v1/agent/operations${suffix}`)
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
  approvals: ApprovalStats
  runs: TaskRunBreakdown[]
  agents: TaskAgentRollup[]
}

export function taskCost(project: string, taskID: string): Promise<TaskCost> {
  return request(`/v1/agent/cost/task?project=${encodeURIComponent(project)}&task=${encodeURIComponent(taskID)}`)
}

// Human-approval budget of a scope: decisions asked of people, how they
// resolved and how long the work waited on them.
export interface ApprovalStats {
  requested: number
  granted: number
  rejected: number
  timed_out: number
  auto_granted?: number
  pending?: number
  wait_ms: number
  max_wait_ms?: number
}

// One explicitly registered saga (docs/sagas.md): a long-running effort
// uniting one or more tasks — each with its runs, agents and delegation
// stages — with summed time/tokens/cost.
export interface SagaRollup {
  saga_id: string
  title?: string
  goal?: string
  task_ids: string[]
  root_run_id?: string
  registered_by_run?: string
  registered_at?: string
  run_count: number
  running: number
  model_calls: number
  agent_count: number
  first_started_at?: string
  last_ended_at?: string
  elapsed_ms: number
  active_ms: number
  total_prompt_tokens: number
  total_cached_tokens?: number
  total_completion_tokens: number
  total_tokens: number
  total_cost_usd: number
  approvals: ApprovalStats
}

// One task inside a project: totals across every run it spans (follow-ups +
// delegation tree) and the saga it is bound to, if any.
export interface TaskRollup {
  task_id: string
  saga_id?: string
  title?: string
  root_run_id?: string
  run_count: number
  running: number
  model_calls: number
  agent_count: number
  first_started_at?: string
  last_ended_at?: string
  elapsed_ms: number
  active_ms: number
  total_prompt_tokens: number
  total_cached_tokens?: number
  total_completion_tokens: number
  total_tokens: number
  total_cost_usd: number
  approvals: ApprovalStats
}

// Project rollup: totals across every task plus a row per saga and per task
// (unbound tasks carry no saga_id).
export interface ProjectBudget {
  project: string
  saga_count: number
  task_count: number
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
  approvals: ApprovalStats
  sagas: SagaRollup[]
  tasks: TaskRollup[]
}

export function projectBudget(project: string): Promise<ProjectBudget> {
  return request(`/v1/agent/cost/project-budget?project=${encodeURIComponent(project)}`)
}

// ── System dashboard (docs/system-dashboard.md §3.2–§3.3) ─────────────────────

export interface OverviewProjectRow {
  project: string
  org_units?: string[]
  runs_started: number
  runs_running: number
  runs_failed: number
  model_calls: number
  total_tokens: number
  total_cost_usd: number
  pending_approvals: number
  last_activity_at?: string
}

export interface ProjectsOverview {
  window: string
  generated_at: string
  projects: OverviewProjectRow[]
}

export function projectsOverview(window?: string): Promise<ProjectsOverview> {
  const suffix = window && window.trim() ? `?window=${encodeURIComponent(window.trim())}` : ''
  return request(`/v1/agent/cost/projects-overview${suffix}`)
}

export interface QualitySummaryEvaluation {
  at: string
  verdict: string
  passed: number
  failed: number
  pass_rate: number
  judge_agreement?: number
}

export interface QualitySummaryAgent {
  agent_id: string
  name: string
  project: string
  definition_version: number
  last_evaluation?: QualitySummaryEvaluation
  pending_proposals: number
  stale_goldens: number
}

export interface QualitySummary {
  agents: QualitySummaryAgent[]
  loop: {
    window_days: number
    gate_passed: number
    gate_blocked: number
    gate_overridden: number
    override_rate: number
    judge_degraded_runs: number
    evaluation_runs: number
  }
}

export function qualitySummary(project?: string): Promise<QualitySummary> {
  const suffix = project && project.trim() ? `?project=${encodeURIComponent(project.trim())}` : ''
  return request(`/v1/workspace/quality/summary${suffix}`)
}
