import { authHeaders } from './api'
import type { Agent, AgentDefinition, AgentProposal, AgentVersion } from './workspaceApi'

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

// Federation login surface (docs/auth-federation.md §8-§9): which buttons the
// login screen may offer. Fetched without credentials — before any exist.
export interface AuthMethods {
  sso: { enabled: boolean; display_name: string }
  local: boolean
}

export function authMethods(): Promise<AuthMethods> {
  return request('/v1/auth/methods')
}

// Best-effort server-side session drop (204, no body): a stale cookie is
// cleared either way, so failures are irrelevant for the client flow.
export function authLogout(): Promise<void> {
  return fetch(`${KERNEL_API}/v1/auth/logout`, { method: 'POST' }).then(() => undefined)
}

// A signed-in user changes their own local password. The server re-checks
// the current one when the account has a password (an SSO account without
// one sets its first), enforces the same 8-character bar the admin path
// uses, and keeps the bearer token alive — the session survives the change.
export function changePassword(currentPassword: string, newPassword: string): Promise<{ ok: boolean }> {
  return request('/v1/auth/password', {
    method: 'POST',
    body: JSON.stringify({ current_password: currentPassword, new_password: newPassword }),
  })
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
    /** KERNEL_SANDBOX_IMAGE pinned by the operator's env: the build step
     *  refuses to override it, so the wizard offers skip instead. */
    pinned_by_env?: boolean
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
  human?: HumanStats
  runs: TaskRunBreakdown[]
  agents: TaskAgentRollup[]
}

export function taskCost(project: string, taskID: string): Promise<TaskCost> {
  return request(`/v1/agent/cost/task?project=${encodeURIComponent(project)}&task=${encodeURIComponent(taskID)}`)
}

/** Effort profile per agent definition version: tokens and turns averaged
 * over the version's completed runs (docs/agent-quality-loop.md, effort
 * signal) — «стало ли дороже» рядом с pass-rate регрессией. */
export interface VersionEffort {
  agent_version: number
  runs: number
  total_tokens: number
  avg_tokens: number
  avg_turns: number
  total_cost_usd?: number
}

export function versionEffort(project: string): Promise<{ versions: VersionEffort[]; count: number }> {
  return request(`/v1/agent/cost/versions?project=${encodeURIComponent(project)}`)
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

// Persistent human-factor rollup of a scope (docs/supervisor-model.md §5):
// HAB — estimated attention (decisions, answers, reading conclusions);
// HWT — wall-clock wait on humans; flow efficiency — the attention share.
export interface HumanStats {
  decisions: number
  answers: number
  hab_seconds: number
  hwt_seconds: number
  flow_efficiency: number
}

// One explicitly registered saga (docs/sagas.md): a long-running effort
// uniting one or more tasks — each with its runs, agents and delegation
// stages — with summed time/tokens/cost.
export interface SagaRollup {
  saga_id: string
  title?: string
  goal?: string
  merged_into?: string
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
  human?: HumanStats
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
  human?: HumanStats
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
  human?: HumanStats
  sagas: SagaRollup[]
  tasks: TaskRollup[]
}

export function projectBudget(project: string): Promise<ProjectBudget> {
  return request(`/v1/agent/cost/project-budget?project=${encodeURIComponent(project)}`)
}

// ── Manual saga management (docs/sagas.md §8) ──────────────────────────────────
// Event-sourced on the server: these calls only emit journal events, the
// projection (rename/merge/detach replay) happens on read.

export function sagaCreate(project: string, body: { title: string; goal?: string; task_id?: string }): Promise<{ saga_id: string; status: string }> {
  return request(`/v1/workspace/sagas?project=${encodeURIComponent(project)}`, { method: 'POST', body: JSON.stringify(body) })
}

export function sagaRename(project: string, sagaID: string, body: { title?: string; goal?: string }): Promise<{ status: string }> {
  return request(`/v1/workspace/sagas/${encodeURIComponent(sagaID)}/rename?project=${encodeURIComponent(project)}`, { method: 'POST', body: JSON.stringify(body) })
}

export function sagaMerge(project: string, targetSagaID: string, sourceSagaIDs: string[]): Promise<{ status: string }> {
  return request(`/v1/workspace/sagas/${encodeURIComponent(targetSagaID)}/merge?project=${encodeURIComponent(project)}`, { method: 'POST', body: JSON.stringify({ source_saga_ids: sourceSagaIDs }) })
}

export function sagaDelete(project: string, sagaID: string): Promise<{ status: string }> {
  return request(`/v1/workspace/sagas/${encodeURIComponent(sagaID)}/delete?project=${encodeURIComponent(project)}`, { method: 'POST', body: JSON.stringify({}) })
}

export function taskSagaBind(project: string, taskID: string, sagaID: string): Promise<{ status: string }> {
  return request(`/v1/workspace/tasks/${encodeURIComponent(taskID)}/saga?project=${encodeURIComponent(project)}`, { method: 'POST', body: JSON.stringify({ saga_id: sagaID }) })
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

// ── Storage inventory & host health (docs/sandbox-storage.md §5) ──────────

export interface StorageTopPath {
  path: string
  bytes: number
}

export interface StorageProjectRow {
  project: string
  workspace_bytes: number
  workspace_files: number
  cache_bytes: number
  quota_bytes: number
  quota_used: number
  top_paths: StorageTopPath[]
  last_activity_at?: string
}

export interface StorageSnapshot {
  generated_at: string
  root: string
  disk: { total_bytes: number; free_bytes: number }
  truncated: boolean
  projects: StorageProjectRow[]
}

export function storageOverview(): Promise<StorageSnapshot> {
  return request('/v1/workspace/storage')
}

// File-level browser over one project's persistent areas (workspace/cache).
export interface StorageFileEntry {
  name: string
  path: string
  dir: boolean
  bytes: number
  mod_at?: string
  entries?: number
}

export interface StorageFileEntry {
  name: string
  path: string
  dir: boolean
  bytes: number
  mod_at?: string
  entries?: number
}

// NOTE the /file suffix — without it the request lands on the tree endpoint
// and a JSON document gets rendered instead of the file content.
function storageFileQuery(project: string, area: string, path: string): string {
  return `/v1/workspace/storage/projects/${encodeURIComponent(project)}/file` +
    `?area=${encodeURIComponent(area)}&path=${encodeURIComponent(path)}`
}

export function storageListDir(project: string, area: 'workspace' | 'cache', path: string): Promise<{ entries: StorageFileEntry[] }> {
  return request(`/v1/workspace/storage/projects/${encodeURIComponent(project)}/dir?area=${encodeURIComponent(area)}&path=${encodeURIComponent(path)}`)
}

/** Raw file content for inline preview (text) and object URLs (images). */
export async function storageFileContent(project: string, area: 'workspace' | 'cache', path: string): Promise<Response> {
  const response = await fetch(`${KERNEL_API}${storageFileQuery(project, area, path)}`, { headers: authHeaders() })
  if (!response.ok) throw new Error(`HTTP ${response.status}`)
  return response
}

/** Direct content URL is not usable for <img>/<a> with header auth — callers
 *  fetch via storageFileContent and object URLs; download goes through
 *  storageDownloadFile so auth headers ride along. */
export function storageFileURL(project: string, area: 'workspace' | 'cache', path: string, download = false): string {
  return `${KERNEL_API}${storageFileQuery(project, area, path)}${download ? '&download=1' : ''}`
}

/** Download via fetch so auth headers ride along, then hand the blob to the
 *  browser's save dialog. */
export async function storageDownloadFile(project: string, area: 'workspace' | 'cache', path: string): Promise<void> {
  const response = await storageFileContent(project, area, path)
  const blob = await response.blob()
  const url = URL.createObjectURL(blob)
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = path.split('/').pop() || 'file'
  anchor.click()
  URL.revokeObjectURL(url)
}

export function storageDeleteFile(project: string, area: 'workspace' | 'cache', path: string): Promise<{ deleted: string }> {
  return request(storageFileQuery(project, area, path), { method: 'DELETE' })
}

export async function storageUploadFile(project: string, area: 'workspace' | 'cache', path: string, data: Blob | ArrayBuffer, overwrite = false): Promise<{ path: string }> {
  const response = await fetch(`${KERNEL_API}/v1/workspace/storage/projects/${encodeURIComponent(project)}/files?area=${encodeURIComponent(area)}&path=${encodeURIComponent(path)}${overwrite ? '&overwrite=1' : ''}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/octet-stream', ...authHeaders() },
    body: data,
  })
  if (!response.ok) throw new Error(`HTTP ${response.status}: ${await response.text().catch(() => response.statusText)}`)
  return response.json()
}

export interface SandboxContainerInfo {
  name: string
  image: string
  labels?: Record<string, string>
  cpu_percent: number
  mem_used_bytes: number
  mem_limit_bytes: number
  mem_percent: number
  net_in_bytes: number
  net_out_bytes: number
}

export interface SystemHealth {
  disk: { path: string; total_bytes: number; free_bytes: number }[]
  cpu: { load1: number; load5: number; cores: number }
  memory: { total_bytes: number; available_bytes: number }
  docker: { images_bytes: number; containers_running: number; available: boolean }
  sandboxes?: SandboxContainerInfo[]
  // Memory breakdown over ALL running containers (top consumers first):
  // answers «куда ушла память» when the host line dwarfs the sandbox list.
  containers?: { name: string; image?: string; used_mb: number; limit_mb?: number; percent?: number }[]
}

export function systemHealth(): Promise<SystemHealth> {
  return request('/v1/system/health')
}

// Tool readiness (docs/onboarding-readiness): every background feature that
// silently degrades when a piece of configuration is missing — which tools
// stop working, and what to toggle. The dashboard renders this as a checklist.
export interface ReadinessCheck {
  key: string
  configured: boolean
  used_by?: string[]
  fix?: string
  detail?: string
}

export function readiness(): Promise<{ checks: ReadinessCheck[] }> {
  return request('/v1/setup/readiness')
}

// ── Agent evolution v2 (docs/agent-evolution.md §4–§5) ────────────────────
//
// Versions, proposals, the observation window, per-version metrics and the
// control check-cases. Gate-free by contract (§1): apply always lands and
// degradation is answered by a one-click rollback — never silently.

/** Proposal origin (§2.2): agent_propose, the "suggest an improvement"
 * button (user) or the supervisor. */
export type ProposalSource = 'agent' | 'user' | 'supervisor' | string

/** Where a proposal was born (§2.2): the run that surfaced the problem. */
export interface ProposalContext {
  run_id?: string
  project?: string
}

/** A proposal as served by the v2 API (§4): the old anatomy plus source,
 * behavioral change_summary, origin context and the version it landed in. */
export interface EvolutionProposal extends AgentProposal {
  source: ProposalSource
  change_summary: string
  context?: ProposalContext | null
  applied_version?: number
}

/** Version row as served by the v2 API (§4): snapshot metadata plus how the
 * version came to be and its observation verdict. */
export interface EvolutionVersion extends AgentVersion {
  kind: 'edit' | 'proposal' | 'rollback' | string
  baseline_version?: number
  rollback_of?: number
  change_summary?: string
  verdict?: string | null
}

/** One structural difference between two definition snapshots (§4 diff):
 * purpose, constraints, completion items and capability names. */
export interface DefinitionChange {
  field: string
  kind: 'added' | 'removed' | 'changed' | string
  value: string
  from?: string
  redacted?: boolean
}

/** Structural + compiled-prompt diff of two versions (§4). A proposal diff
 * is the same shape plus the behavioral change_summary. Prompt lines are
 * prefixed "- " / "+ " (changed lines only). */
export interface VersionDiff {
  from: number
  to: number
  definition_changes: DefinitionChange[]
  prompt_diff: string[]
  redacted?: boolean
  change_summary?: string
}

/** Draft returned by proposals/from-text (§4): a pending source=user
 * proposal; the definition is the full target definition. */
export interface ProposalDraft {
  proposal_id: number
  change_summary: string
  definition: AgentDefinition
}

/** Apply always succeeds (§1): the answer is just the new version (§4). */
export interface ApplyProposalReceipt {
  version: number
}

/** Observation window config (§2.3): the window closes when both the day
 * count and the run minimum are reached. */
export interface ObservationSettings {
  days: number
  min_runs: number
}

/** Observation window state (§5 «observation»): verdict is null/empty while
 * the window is still open. */
export interface ObservationState {
  state: 'observing' | 'closed' | string
  verdict: 'improved' | 'stable' | 'degraded' | 'insufficient' | '' | null
  days: number
  min_runs: number
  started_at?: string
  elapsed_days: number
  runs: number
}

/** Aggregated metrics of one comparison arm (§5 «current» / «baseline»). */
export interface WindowSummary {
  runs: number
  succeeded: number
  failed: number
  cancelled: number
  turn_limited: number
  verdicts: { accepted: number; corrected: number; rejected: number }
  interventions: number
  reworks: number
  tokens_avg: number
  cost_usd_avg: number
  wall_ms_avg: number
  uncertain_ops_avg: number
  incidents: number
}

/** One row of the «сейчас / база / Δ» table (§5 «delta»). `signal` is the
 * §5 "signal"/"noise" marker; the rollup may also send a plain bool —
 * isSignalMark() in agentEvolution.ts folds both. */
export interface MetricDelta {
  metric: string
  current: number
  baseline: number
  delta: number
  signal: 'signal' | 'noise' | boolean
}

/** Daily rollup row (§5 «daily»). */
export interface MetricsDay {
  day: string
  runs: number
  succeeded?: number
  [key: string]: unknown
}

/** GET versions/{v}/metrics payload (§5). */
export interface VersionMetrics {
  observation: ObservationState
  current: WindowSummary
  baseline: WindowSummary | null
  delta: MetricDelta[]
  daily: MetricsDay[]
}

/** Control check-case (§2.4): a task input plus answer pattern checks. */
export interface CheckCase {
  id: number
  agent_id: string
  input: string
  must_contain: string[]
  must_not_contain: string[]
  from_run_id?: string
  created_by?: string
  created_at?: string
}

/** Synchronous smoke run of the control cases (§4): an informational signal,
 * never a condition of applying. */
export interface CheckRunResult {
  passed: number
  failed: number
  results: { input: string; pass: boolean; detail?: string }[]
}

const agentPath = (agentID: string) => `/v1/workspace/agents/${encodeURIComponent(agentID)}`

// Versions (§4)
export function listEvolutionVersions(agentID: string): Promise<{ versions: EvolutionVersion[] }> {
  return request(`${agentPath(agentID)}/versions`)
}

export function evolutionVersionDiff(agentID: string, from: number, to: number): Promise<VersionDiff> {
  return request(`${agentPath(agentID)}/versions/diff?from=${from}&to=${to}`)
}

/** Rollback creates a NEW version with the old content (§1): history is
 * append-only and the rollback itself is revertible. */
export function rollbackEvolutionVersion(agentID: string, version: number): Promise<Agent> {
  return request(`${agentPath(agentID)}/versions/${version}/rollback`, { method: 'POST' })
}

// Proposals (§2.2)
export function listEvolutionProposals(agentID: string): Promise<{ proposals: EvolutionProposal[] }> {
  return request(`${agentPath(agentID)}/proposals`)
}

export function createEvolutionProposal(agentID: string, data: {
  problem: string
  change: string
  effect?: string
  description?: string
  definition?: { constraints?: string[]; completion?: string[] }
  evidence?: string[]
  source?: ProposalSource
  context?: ProposalContext
}): Promise<EvolutionProposal> {
  return request(`${agentPath(agentID)}/proposals`, { method: 'POST', body: JSON.stringify(data) })
}

/** Free-text problem → LLM draft proposal (§4): lands as pending source=user.
 * May take a while — the caller shows a generating state. */
export function proposalFromText(agentID: string, data: { problem: string; context?: ProposalContext }): Promise<ProposalDraft> {
  return request(`${agentPath(agentID)}/proposals/from-text`, { method: 'POST', body: JSON.stringify(data) })
}

export function evolutionProposalDiff(agentID: string, proposalID: number): Promise<VersionDiff> {
  return request(`${agentPath(agentID)}/proposals/${proposalID}/diff`)
}

/** Gate-free apply (§1): always succeeds, no body, answer is {version}. */
export function applyEvolutionProposal(agentID: string, proposalID: number): Promise<ApplyProposalReceipt> {
  return request(`${agentPath(agentID)}/proposals/${proposalID}/apply`, { method: 'POST' })
}

export function rejectEvolutionProposal(agentID: string, proposalID: number): Promise<EvolutionProposal> {
  return request(`${agentPath(agentID)}/proposals/${proposalID}/reject`, { method: 'POST' })
}

// Observation window (§2.3)
export function getObservationSettings(agentID: string): Promise<ObservationSettings> {
  return request(`${agentPath(agentID)}/observation`)
}

export function setObservationSettings(agentID: string, settings: ObservationSettings): Promise<ObservationSettings> {
  return request(`${agentPath(agentID)}/observation`, { method: 'PUT', body: JSON.stringify(settings) })
}

// Version metrics (§5): observation state + current/baseline windows +
// signal-folded deltas over `days` of real work.
export function evolutionVersionMetrics(agentID: string, version: number, days?: number): Promise<VersionMetrics> {
  const suffix = days && days > 0 ? `?days=${days}` : ''
  return request(`${agentPath(agentID)}/versions/${version}/metrics${suffix}`)
}

// Control check-cases (§2.4)
export async function listAgentCheckCases(agentID: string): Promise<{ cases: CheckCase[] }> {
  // The contract fixes the list, not its envelope: accept both the
  // {cases:[…]} convention and a bare array.
  const page = await request<{ cases?: CheckCase[] } | CheckCase[]>(`${agentPath(agentID)}/check-cases`)
  return { cases: Array.isArray(page) ? page : page.cases ?? [] }
}

export function addAgentCheckCase(agentID: string, data: {
  input: string
  must_contain: string[]
  must_not_contain: string[]
  from_run_id?: string
}): Promise<CheckCase> {
  return request(`${agentPath(agentID)}/check-cases`, { method: 'POST', body: JSON.stringify(data) })
}

export function deleteAgentCheckCase(agentID: string, caseID: number): Promise<{ deleted?: boolean }> {
  return request(`${agentPath(agentID)}/check-cases/${caseID}`, { method: 'DELETE' })
}

/** Synchronous smoke run (§4): against a stored version or a pending
 * proposal's draft definition — informational, never a condition of apply. */
export function runAgentCheckCases(agentID: string, target: number | { proposal: number }): Promise<CheckRunResult> {
  const query = typeof target === 'number' ? `version=${target}` : `proposal=${target.proposal}`
  return request(`${agentPath(agentID)}/check-cases/run?${query}`)
}
