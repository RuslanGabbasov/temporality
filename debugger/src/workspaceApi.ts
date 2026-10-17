import { authHeaders } from './api'
import type { ResearchSessionPayload } from './explorer/sessions'

const KERNEL_API = '/kernel-api'

// Org structure (docs/org-structure.md): units form a tree; a resource with
// an empty org_unit_id is global, otherwise it is visible to the unit and
// its subtree.
export type OrgUnitKind = 'organization' | 'department' | 'team'

export interface OrgUnit {
  id: string
  parent_id?: string
  kind: OrgUnitKind | string
  name: string
  path?: string
  /** Explicit manager edge: escalation target and team-ask priority. */
  head_user_id?: string
  created_at: string
  updated_at: string
}

export interface NamedRef {
  id: string
  name: string
}

/** Policy (docs/org-structure.md §24): org-bound run constraints that inherit
 * top-down and merge restrictively. Empty lists mean unrestricted. */
export interface Policy {
  id: string
  org_unit_id?: string // empty = installation-wide
  name: string
  allowed_models?: string[] | null
  allowed_mcp?: string[] | null
  max_tokens?: number | null
  max_budget_usd?: number | null
  timeout_seconds?: number | null
  network_mode?: string // '' | 'deny'
  sandbox_mode?: string // '' | 'standard' | 'read_only'
  approval_mode?: string // '' | 'auto' | 'tools'
  intake_mode?: string // '' | 'deny' — overlay that can only narrow, never widen (docs/inbound-messages.md §12)
  enabled: boolean
  created_at: string
  updated_at: string
}

export interface UnitResources {
  agents: NamedRef[]
  skills: NamedRef[]
  mcp_servers: NamedRef[]
  providers: NamedRef[]
  users: NamedRef[]
  projects: NamedRef[]
}

/** Role grant on an org unit (docs/org-structure.md §13): upgrades the
 * grantee's effective role on the unit and its whole subtree. */
export interface OrgUnitRoleGrant {
  user_id: string
  org_unit_id: string
  role: string // reader | writer | operator | admin
  granted_by?: string
  granted_at: string
}

export type OrgResourceKind = 'agent' | 'skill' | 'mcp-server' | 'provider' | 'trigger' | 'team' | 'secret' | 'artifact'

export interface Project {
  id: string
  name: string
  description: string
  default_agent_id?: string
  default_model?: string
  archived?: boolean
  org_units?: string[] // org areas the project spans; empty = org-neutral
  created_at: string
  updated_at: string
}

/** Explicit project membership (docs/org-structure.md §16): a member sees
 * the project even when their org chain does not intersect its units. */
export interface ProjectMember {
  user_id: string
  role: string
  created_at: string
  created_by?: string
}

export interface AgentCapabilities {
  read_files?: boolean
  modify_files?: boolean
  run_commands?: boolean
  network?: boolean
  skills?: boolean
  knowledge?: boolean
  delegation?: boolean
}

/** Structured definition (docs/evaluable-agent.md): capabilities, hard
 * constraints and verifiable completion criteria. The system prompt is
 * compiled from these — the user never writes it by hand. */
export interface AgentDefinition {
  capabilities?: AgentCapabilities
  constraints?: string[]
  completion?: string[]
  prompt_override?: string
}

export interface Agent {
  id: string
  project_id?: string
  name: string
  description: string
  model: string
  provider: string
  system_prompt: string
  temperature?: number
  max_tokens?: number
  skills: string[]
  mcp_servers: string[]
  secrets?: string[]
  tools?: string[]
  definition?: AgentDefinition
  definition_version?: number
  org_unit_id?: string
  sandbox_profile: string
  network_access?: boolean
  read_only?: boolean
  max_turns?: number
  approval_mode: string
  labels: Record<string, string>
  created_at: string
  updated_at: string
}

// Sandbox profile registry row (docs/sandbox-profiles.md §3.1) plus the local
// availability flag: the digest-pinned image exists in the docker store.
/** Sandbox wizard draft (docs/sandbox-profiles.md): the supervisor model's
 *  proposal, landed in the regular profile form for editing. */
export interface SandboxDraft {
  id: string
  name: string
  description: string
  base: string
  packages?: string[]
  toolchains?: string[]
  memory?: string
  cpus?: string
  scratch_size?: string
  tmp_size?: string
  rationale?: string
}

export interface SandboxProfile {
  id: string
  name: string
  description?: string
  image?: string
  /** Build recipe: the known base the custom profile composes on; the built
   *  digest lands in image. Empty for external-image and builtin rows. */
  base?: string
  packages?: string[]
  toolchains?: string[]
  memory?: string
  cpus?: string
  scratch_size?: string
  tmp_size?: string
  cache_env?: string[]
  builtin: boolean
  available?: boolean
  created_at: string
  updated_at: string
}

export interface AgentVersion {
  agent_id: string
  version: number
  definition: AgentDefinition
  description: string
  compiled_prompt: string
  prompt_source: string
  generator_model?: string
  author?: string
  created_at: string
}

export interface AgentEvaluationCase {
  name: string
  input: string
  must_contain?: string[]
  must_not_contain?: string[]
}

export interface AgentEvaluationSuite {
  agent_id: string
  cases: AgentEvaluationCase[]
  updated_at?: string
}

export interface AgentEvaluationCaseResult {
  name: string
  passed: boolean
  answer?: string
  missed?: string[]
  unexpected?: string[]
}

// Installation-wide judge resolution (GET/PUT /v1/workspace/evaluation-judge):
// configured=false means the judge is off; source says which knob is in
// effect — "setting" (UI) or "env".
export interface JudgeConfig {
  model: string
  provider_id?: string
  configured: boolean
  source?: string
}

/** Supervisor panel (docs/supervisor-model.md): models working for the
 *  administrator plus the human-attention estimate coefficients. Empty model
 *  = feature off; HAB values are seconds / words-per-minute. */
export interface SupervisorSettings {
  judge_model: string
  vision_model: string
  hab: {
    approval_seconds: number
    answer_base_seconds: number
    reading_wpm: number
  }
}

// Result of the judge-driven knowledge-area normalization sweep: how many
// raw values were merged into canonical ones and how many were hidden.
export interface AreaNormalizationResult {
  areas: number
  merged: number
  hidden: number
}

// Scheduled quality-run cadence (GET/PUT /v1/workspace/quality-schedule):
// interval_hours=0 disables; next_run_at is the approximate fire time.
export interface QualitySchedule {
  interval_hours: number
  enabled: boolean
  last_run_at?: string
  next_run_at?: string
}

export interface AgentEvaluationRun {
  id: number
  agent_id: string
  agent_version: number
  passed: number
  failed: number
  cases: AgentEvaluationCaseResult[]
  created_at: string
  verdict?: 'pass' | 'fail' | 'error' | 'unknown' | string
  cause?: string
  trigger?: string
  proposal_id?: number
  judge_degraded?: boolean
}

/** Evolution proposal (plan §2.2 stage 10): a pending definition change with
 * full provenance. Apply is human-only and goes through the regular version
 * path — capabilities are never part of a proposal. */
export interface AgentProposal {
  id: number
  agent_id: string
  base_version: number
  definition: AgentDefinition
  description: string
  problem: string
  change: string
  effect: string
  evidence: string[]
  status: 'pending' | 'applied' | 'rejected' | string
  author: string
  generator_model?: string
  created_at: string
  decided_at?: string
  decided_by?: string
}

/** Gated apply result: either the proposal landed (200) or the eval gate
 * started/waited on a run (202) — the UI shows the run and asks for a second,
 * conscious apply once it turns green. */
export type ApplyProposalResult =
  | { kind: 'applied'; agent: Agent; proposal: AgentProposal }
  | { kind: 'evaluating'; evaluation_run_id: number; status: string }

export interface BuiltinAgentSpec {
  slug: string
  name: string
  description: string
  definition: AgentDefinition
  network_access?: boolean
  read_only?: boolean
  labels?: Record<string, string>
}

export interface AgentDraftQuestion { field: string; question: string }

export interface AgentDraft {
  name: string
  description: string
  capabilities: AgentCapabilities
  constraints?: string[]
  completion?: string[]
  suggested: { model?: string; sandbox?: string; network?: boolean }
  questions?: AgentDraftQuestion[]
}

export interface Task {
  id: string
  project_id: string
  agent_id: string
  title: string
  prompt: string
  created_at: string
  updated_at: string
}

export interface Run {
  id: string
  task_id: string
  project_id: string
  agent_id: string
  agent_version?: number
  run_id: string
  status: string
  model: string
  answer: string
  turns: number
  error: string
  created_at: string
  updated_at: string
}

export interface Trigger {
  id: string
  project_id: string
  agent_id?: string
  name: string
  type: 'schedule' | 'webhook' | 'event'
  enabled: boolean
  config: any
  org_unit_id?: string
  execution_identity_id?: string
  created_at: string
  updated_at: string
}

// Execution identity (docs/org-structure.md §20): the security context of
// automated runs — which agents, MCP servers, providers, projects and human
// request targets a trigger-driven run may touch.
export interface ExecutionIdentity {
  id: string
  name: string
  description: string
  org_unit_id?: string
  allowed_agents: string[]
  allowed_mcp: string[]
  allowed_providers: string[]
  allowed_projects: string[]
  human_targets: string[]
  created_at: string
  updated_at: string
}

export interface Skill {
  id: string
  name: string
  description: string
  version: string
  markdown: string
  manifest: Record<string, unknown>
  org_unit_id?: string
  /** draft = the current version awaits human apply; never served to agents */
  version_status?: string
  created_at: string
  updated_at: string
}

export interface SkillVersion {
  skill_id: string
  version: string
  markdown: string
  manifest: Record<string, unknown>
  origin?: string
  source_runs?: string[]
  evidence_refs?: string[]
  knowledge_ids?: string[]
  change_summary?: string
  /** Evolution proposal anatomy (docs/living-skills.md §21). */
  observed_problem?: string
  proposed_change?: string
  expected_effect?: string
  /** draft | active | rejected */
  status?: string
  created_at: string
}

/** One case of a skill evaluation suite: a task input + expected patterns. */
export interface SkillEvaluationCase {
  name: string
  input: string
  must_contain?: string[]
  must_not_contain?: string[]
}

export interface SkillEvaluationSuite {
  skill_id: string
  cases: SkillEvaluationCase[]
  updated_at?: string
}

export interface SkillEvaluationCaseResult {
  name: string
  passed: boolean
  answer?: string
  missed?: string[]
  unexpected?: string[]
}

export interface SkillEvaluationRun {
  id: number
  skill_id: string
  skill_version: string
  passed: number
  failed: number
  cases: SkillEvaluationCaseResult[]
  created_at: string
}

export interface SkillExecution {
  id: string
  skill_id: string
  skill_version: string
  project_id: string
  run_id: string
  agent_id: string
  started_at: string
}

export interface SkillMemoryItem {
  knowledge_id: string
  proposition: string
  capability: string
  state: string
  run_id: string
  occurred_at: string
}

export interface SkillValidationIssue {
  field: string
  message: string
}

export interface SkillValidation {
  valid: boolean
  issues: SkillValidationIssue[]
}

export interface SkillDraftQuestion {
  field: string
  question: string
}

export interface SkillDraft {
  name: string
  description: string
  markdown: string
  manifest: Record<string, unknown>
  questions?: SkillDraftQuestion[]
}

/** agentskills.io import result: a draft plus notes about what the importer
 * could not carry over verbatim (license, unknown files, tool names). */
export interface SkillImportResult extends SkillDraft {
  version?: string
  warnings?: string[]
}

export interface SkillInput {
  name: string
  description: string
  version: string
  markdown: string
  manifest_yaml: string
  org_unit_id?: string // honored on create; updates keep the stored binding
}

// Agent teams (docs/agent-teams.md). A team is a versioned definition of a
// multi-agent composition; the manifest is structured data — team.yaml never
// appears in the API surface, the UI edits protocol/slots/defaults directly.

/** Team protocol kinds — the small topology vocabulary (§6). */
export type TeamProtocolKind = 'pipeline' | 'lead_workers' | 'fan_out' | 'review_gate' | 'dag'
export const TEAM_PROTOCOL_KINDS: TeamProtocolKind[] = ['pipeline', 'lead_workers', 'fan_out', 'review_gate', 'dag']

export interface TeamStep {
  id: string
  slot_id: string
  depends_on?: string[]
  review_of?: string[]
}

export interface TeamProtocol {
  kind: TeamProtocolKind
  max_rework?: number
  steps?: TeamStep[]
}

/** The seven canonical capabilities (write_files aliases modify_files
 *  server-side; the UI offers the canonical names only). */
export const TEAM_CAPABILITIES = ['read_files', 'modify_files', 'run_commands', 'network', 'skills', 'knowledge', 'delegation'] as const

export interface TeamRequirements {
  capabilities?: string[]
  skills?: string[]
}

export interface TeamBinding {
  mode: 'fixed' | 'role'
  agent_id?: string
  preferred_agent_id?: string
}

export interface TeamSlot {
  id: string
  title: string
  responsibility?: string
  requirements?: TeamRequirements
  contract?: string
  binding: TeamBinding
}

export interface TeamDefaults {
  max_turns?: number
  budget_usd?: number
  parallel_limit?: number
}

export interface TeamManifest {
  protocol: TeamProtocol
  slots: TeamSlot[]
  defaults?: TeamDefaults
}

export interface Team {
  id: string
  name: string
  description: string
  version: string
  manifest: TeamManifest
  org_unit_id?: string
  /** Lifecycle status of the current version row: "draft" awaits a human
   *  apply and is never launched; empty when no version row matches. */
  version_status?: string
  created_at: string
  updated_at: string
}

export interface TeamVersion {
  team_id: string
  version: string
  manifest: TeamManifest
  origin?: string
  change_summary?: string
  observed_problem?: string
  proposed_change?: string
  expected_effect?: string
  source_runs?: string[]
  status: string
  created_at: string
}

/** One slot's resolution from the launch preview (wave C): who fills it,
 *  why, and — when ambiguous — which candidates existed. */
export interface TeamSlotChoice {
  slot_id: string
  agent_id?: string
  mode?: string // fixed | matched
  reason?: string
  candidates?: string[]
  requirements?: string[]
}

export interface TeamResolvePreview {
  team_id: string
  version: string
  project: string
  slots: TeamSlotChoice[]
  resolvable: boolean
  reason?: string
  slot_id?: string
  candidates?: string[]
  requirements?: string[]
}

/** Team builder wizard result (wave E): a prefilled team definition plus
 *  clarifying questions the model could not resolve on its own. */
export interface TeamDraft {
  name: string
  description: string
  manifest: TeamManifest
  questions?: SkillDraftQuestion[]
}

/** Honest rollup of one team program run: every event of the run tree folded
 *  into totals, a per-run breakdown and approvals (wave G). */
export interface TeamRunCost {
  project: string
  run_id: string
  team_id?: string
  version?: string
  status?: string
  rework_rounds?: number
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
  approvals?: { requested: number; granted: number; rejected: number; timed_out: number; auto_granted: number; pending: number; wait_ms: number; max_wait_ms: number }
  human?: { decisions: number; answers: number; hab_seconds: number; hwt_seconds: number; flow_efficiency: number }
  runs: Array<{
    run_id: string
    parent_run_id?: string
    agent_id?: string
    status: string
    started_at: string
    ended_at?: string
    duration_ms: number
    model_calls: number
    total_tokens: number
    cost_usd: number
  }>
}

/** Reviewer rejections of one executor slot across a team's runs (wave G). */
export interface TeamSlotDispute {
  slot_id: string
  rejected_by: string
  rejections: number
  runs: number
  last_round?: number
  last_feedback?: string
  source_runs?: string[]
}

/** Per-version telemetry of one team (wave G). */
export interface TeamVersionMetrics {
  version: string
  runs: number
  completed: number
  failed: number
  accept_first_try: number
  total_rework_rounds: number
  avg_rework_rounds: number
  total_tokens: number
  total_cost_usd: number
  duration_ms: number
  disputes?: TeamSlotDispute[]
}

export interface TeamMetrics {
  team_id: string
  project: string
  versions: TeamVersionMetrics[]
}

export interface TeamRunStartResult {
  run_id: string
  project: string
  team_id: string
  workflow_id: string
  temporal_run_id: string
  status: string
}

export interface Secret {
  id: string
  name: string
  provider: 'builtin' | 'env'
  external_ref?: string
  description?: string
  has_value: boolean
  org_unit_id?: string
  created_at: string
  updated_at: string
}

/** Reference artifact (docs/artifact-registry.md §3.1): a regulation,
 * approved instruction or checklist that skill manifests point at but never
 * inline into the prompt — the agent loads the body on demand. Description
 * is the applicability condition, not a content summary. */
export interface Artifact {
  id: string
  title: string
  description: string
  provider: 'builtin' | 'url'
  external_ref?: string
  /** Builtin bodies are stripped from list responses; the editor fetches
   * the full document via getArtifact. */
  content?: string
  content_hash?: string
  size_bytes: number
  mime_type?: string
  org_unit_id?: string
  /** Lifecycle (docs/artifact-lifecycle.md): draft → in_review →
   * approved/rejected; agent-created rows start as drafts, human creates land
   * approved. kind separates reference documents from saga work products. */
  status?: 'draft' | 'in_review' | 'approved' | 'rejected'
  kind?: 'reference' | 'work'
  version?: number
  origin_type?: 'human' | 'agent'
  origin_user?: string
  origin_run?: string
  saga_id?: string
  created_at: string
  updated_at: string
}

/** One review round of one artifact version (docs/artifact-lifecycle.md §3.3):
 * mode=all closes when every reviewer approved (a reject closes immediately),
 * mode=any closes on the first decision. */
export interface ArtifactReview {
  id: string
  artifact_id: string
  artifact_version: number
  reviewers: string[]
  mode: 'all' | 'any'
  status: 'open' | 'approved' | 'rejected' | 'cancelled'
  note?: string
  requested_by: string
  requested_at: string
  decisions?: Record<string, { decision: 'approve' | 'reject'; comment?: string; at: string; decided_by?: string }>
  resolved_by?: string
  resolved_at?: string
}

export interface MCPServer {
  id: string
  name: string
  type: 'stdio' | 'sse' | 'http'
  url?: string
  command?: string
  args: string[]
  env: string[]
  headers: Record<string, string>
  allowed_tools: string[]
  approval_tools: string[]
  enabled: boolean
  org_unit_id?: string
  created_at: string
  updated_at: string
}

export interface MCPToolInfo {
  name: string
  description?: string
  model_name: string
  requires_approval: boolean
}

export interface MCPServerTools {
  id: string
  name: string
  type: string
  tools: MCPToolInfo[]
}

export interface MCPDiscoverResult {
  tools: MCPToolInfo[]
}

export interface Provider {
  id: string
  name: string
  base_url: string
  api_key_ref: string
  models: string[]
  labels: Record<string, string>
  org_unit_id?: string
  created_at: string
  updated_at: string
}

export interface UserChannel {
  // Transport slug from the kernel's channel registry (matrix, telegram,
  // slack, webhook, …) — see channelTypes().
  type: string
  address: string
  enabled: boolean
  // The owner's identity inside the channel (mxid, chat id): initiative
  // messages are accepted only from exactly this identity (closed world,
  // docs/inbound-messages.md §5). Empty = receive only.
  sender_id?: string
}

/** One transport from the kernel's channel registry: what it is, what the
 * address means and whether the kernel can deliver through it right now. */
export interface ChannelTypeSpec {
  type: string
  label: string
  address_hint: string
  configured: boolean
  not_configured_hint?: string
}

// One recorded channel-initiated message and its intake state machine
// (docs/inbound-messages.md §4.1): received → classified/clarifying → routed,
// or rejected/expired.
export interface InboundMessage {
  id: string
  channel: string
  message_id: string
  sender_raw: string
  sender_user_id?: string
  room: string
  body: string
  attachments?: string[]
  status: 'received' | 'classified' | 'clarifying' | 'routed' | 'rejected' | 'expired'
  classification?: 'task' | 'amendment' | 'status' | 'cancel' | 'chatter' | 'reply'
  intake_run_id?: string
  target_run_id?: string
  project_id?: string
  // Retention (docs/inbound-messages.md §12): set when the body was wiped
  // after KERNEL_INTAKE_RETENTION_DAYS; only the audit shell remains.
  retired_at?: string
  created_at: string
  updated_at: string
}

/** Admin channel settings (channel_transport payload over env), rendered
 * generically from the driver registry schema. Non-secrets come back as
 * {value, source}, secrets as {set, hint, source} — the value itself never
 * leaves the kernel. Source says who wins: database | env | none. */
export interface ChannelSettingsField {
  value?: string
  set?: boolean
  hint?: string
  source: 'database' | 'env' | 'none'
}

/** One channel's settings schema entry — the admin form renders from this,
 * so a new channel needs no UI work. */
export interface ChannelSettingsFieldSchema {
  name: string
  label: string
  secret: boolean
}

export interface ChannelSettingsGroupSchema {
  type: string
  label: string
  fields: ChannelSettingsFieldSchema[]
}

export interface ChannelSettings {
  schema: ChannelSettingsGroupSchema[]
  channels: Record<string, Record<string, ChannelSettingsField>>
  ui_url: ChannelSettingsField
  updated_at?: string
}

export interface ChannelSettingsUpdate {
  ui_url?: string // present (even "") sets/clears the plain field
  channels?: Record<string, Record<string, string>> // plain fields always sent; secrets only when non-empty
  clear?: string[] // "type.field" names (or "ui_url") to wipe
}

// Human-in-the-loop request (docs/org-structure.md §28): a question an agent
// paused on, delivered to a resolved recipient. The inbox lists open rows;
// answers ride the existing approval endpoint.
export type HumanRequestStatus = 'pending' | 'delivered' | 'answered' | 'expired' | 'cancelled' | 'rejected'

export interface HumanRequest {
  id: string // operation id — stable across redeliveries
  run_id: string
  project_id: string
  agent_id?: string
  recipient: string // logical recipient as requested (§26)
  resolved_user?: string // concrete user after resolution
  question: string
  context?: string
  options?: string[]
  status: HumanRequestStatus | string
  channel?: string
  response?: string
  answered_by?: string
  timeout_seconds: number
  timeout_policy?: string
  execution_identity_id?: string
  created_at: string
  delivered_at?: string
  answered_at?: string
  expires_at?: string
}

export interface User {
  id: string
  name: string
  email: string
  role: string
  token?: string
  has_token?: boolean // list responses: whether a token exists (value is never exposed)
  password?: string // create/update only: set or replace the password login
  has_password?: boolean // responses: whether password login is configured
  org_unit_id?: string // primary unit; empty = unassigned (sees everything, transition)
  active: boolean
  channels?: UserChannel[]
  preferred_channel?: string // '' / 'web', or an enabled channel type
  created_at: string
  updated_at: string
}

const headers = () => ({ 'Content-Type': 'application/json', ...authHeaders() })

/** Saved knowledge-explorer investigation (docs/human-knowledge-explorer.md
 *  §11). `payload` is present on get/create/update responses; the list
 *  endpoint omits it to stay light. */
export interface ResearchSession {
  id: number
  project: string
  title: string
  created_by: string
  payload?: ResearchSessionPayload | null
  created_at: string
  updated_at: string
}

/** Uploaded project file: `path` is inside the agent sandbox (/workspace/uploads/…). */
export interface UploadedFile {
  name: string
  path: string
  size: number
}

/** API error with the structured body intact: `code` is the machine-readable
 * `error` field (e.g. `gate_blocked`, `suite_not_ready`) and `body` carries
 * the details (`failed_cases`, `stale_cases`, versions) callers render. */
export class ApiError extends Error {
  status: number
  code: string
  body: Record<string, any>
  constructor(status: number, body: Record<string, any>) {
    super(typeof body?.error === 'string' && body.error ? body.error : `HTTP ${status}`)
    this.status = status
    this.code = typeof body?.error === 'string' ? body.error : ''
    this.body = body ?? {}
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(`${KERNEL_API}${path}`, { ...init, headers: headers() })
  if (!response.ok) {
    const body = await response.json().catch(() => ({}))
    throw new ApiError(response.status, body)
  }
  return response.json()
}

export const workspaceApi = {
  // Projects
  listProjects: () => request<{ projects: Project[] }>('/v1/workspace/projects'),
  getProject: (id: string) => request<Project>(`/v1/workspace/projects/${id}`),
  createProject: (data: { id?: string; name: string; description?: string; org_units?: string[] }) =>
    request<Project>('/v1/workspace/projects', { method: 'POST', body: JSON.stringify(data) }),
  updateProject: (id: string, data: { name: string; description?: string; default_agent_id?: string; default_model?: string; org_units?: string[] }) =>
    request<Project>(`/v1/workspace/projects/${id}`, { method: 'PUT', body: JSON.stringify(data) }),
  deleteProject: (id: string) =>
    request<{ deleted: boolean }>(`/v1/workspace/projects/${id}`, { method: 'DELETE' }),
  listProjectMembers: (id: string) =>
    request<{ members: ProjectMember[] }>(`/v1/workspace/projects/${id}/members`),
  addProjectMember: (id: string, data: { user_id: string; role: string }) =>
    request<{ added: boolean }>(`/v1/workspace/projects/${id}/members`, { method: 'POST', body: JSON.stringify(data) }),
  removeProjectMember: (id: string, userID: string) =>
    request<{ removed: boolean }>(`/v1/workspace/projects/${id}/members/${userID}`, { method: 'DELETE' }),

  // Files (multipart — bypasses the JSON request() helper)
  uploadFile: async (projectId: string, file: File): Promise<UploadedFile> => {
    const body = new FormData()
    body.append('file', file)
    const response = await fetch(`${KERNEL_API}/v1/workspace/projects/${projectId}/files`, {
      method: 'POST',
      body,
      headers: authHeaders(),
    })
    if (!response.ok) {
      const payload = await response.json().catch(() => ({}))
      throw new Error(payload.error ?? `HTTP ${response.status}`)
    }
    return response.json()
  },

  // File download from the project sandbox: chat messages carry /workspace/...
  // paths produced by agents; this fetches them with auth and hands the blob
  // to the browser as a download.
  downloadFile: async (projectId: string, path: string): Promise<void> => {
    const query = new URLSearchParams({ path })
    const response = await fetch(`${KERNEL_API}/v1/workspace/projects/${projectId}/files?${query}`, {
      headers: authHeaders(),
    })
    if (!response.ok) {
      const payload = await response.json().catch(() => ({}))
      throw new Error(payload.error ?? `HTTP ${response.status}`)
    }
    const blob = await response.blob()
    const disposition = response.headers.get('Content-Disposition') ?? ''
    const match = disposition.match(/filename\*?=(?:UTF-8'')?"?([^";]+)/i)
    const fallback = path.split('/').filter(Boolean).pop() ?? 'download'
    const name = match ? decodeURIComponent(match[1]) : fallback
    const url = URL.createObjectURL(blob)
    const anchor = document.createElement('a')
    anchor.href = url
    anchor.download = name
    document.body.appendChild(anchor)
    anchor.click()
    anchor.remove()
    URL.revokeObjectURL(url)
  },

  // Agents
  listAllAgents: () => request<{ agents: Agent[]; pending_proposals?: Record<string, number> }>('/v1/workspace/agents'),
  listAgentsByProject: (projectId: string) =>
    request<{ agents: Agent[] }>(`/v1/workspace/projects/${projectId}/agents`),
  getAgent: (id: string) => request<Agent>(`/v1/workspace/agents/${id}`),
  createAgent: (data: Partial<Agent> & { name: string }) =>
    request<Agent>('/v1/workspace/agents', { method: 'POST', body: JSON.stringify(data) }),
  updateAgent: (id: string, data: Partial<Agent>) =>
    request<Agent>(`/v1/workspace/agents/${id}`, { method: 'PUT', body: JSON.stringify(data) }),
  deleteAgent: (id: string) =>
    request<{ deleted: boolean }>(`/v1/workspace/agents/${id}`, { method: 'DELETE' }),
  listAgentVersions: (id: string) =>
    request<{ versions: AgentVersion[] }>(`/v1/workspace/agents/${id}/versions`),
  listAgentRuns: (id: string) =>
    request<{ runs: Run[] }>(`/v1/workspace/agents/${id}/runs`),
  getAgentPrompt: (id: string) =>
    request<{ agent_id: string; version: number; source: string; prompt: string }>(`/v1/workspace/agents/${id}/prompt`),
  getAgentEvaluationSuite: (id: string) =>
    request<AgentEvaluationSuite>(`/v1/workspace/agents/${id}/evaluation-suite`),
  saveAgentEvaluationSuite: (id: string, cases: AgentEvaluationCase[]) =>
    request<AgentEvaluationSuite>(`/v1/workspace/agents/${id}/evaluation-suite`, { method: 'PUT', body: JSON.stringify({ cases }) }),
  runAgentEvaluation: (id: string, version?: number) =>
    request<AgentEvaluationRun>(`/v1/workspace/agents/${id}/evaluations`, { method: 'POST', body: JSON.stringify(version ? { version } : {}) }),
  listAgentEvaluationRuns: (id: string) =>
    request<{ runs: AgentEvaluationRun[] }>(`/v1/workspace/agents/${id}/evaluations`),
  listAgentProposals: (id: string) =>
    request<{ proposals: AgentProposal[] }>(`/v1/workspace/agents/${id}/proposals`),
  // Installation-level quality settings (docs/agent-quality-loop.md §15):
  // the judge model and the scheduled re-run cadence, owned by the UI.
  getJudgeConfig: () =>
    request<JudgeConfig>('/v1/workspace/evaluation-judge'),
  updateJudgeConfig: (model: string) =>
    request<JudgeConfig>('/v1/workspace/evaluation-judge', { method: 'PUT', body: JSON.stringify({ model }) }),
  getQualitySchedule: () =>
    request<QualitySchedule>('/v1/workspace/quality-schedule'),
  updateQualitySchedule: (intervalHours: number) =>
    request<QualitySchedule>('/v1/workspace/quality-schedule', { method: 'PUT', body: JSON.stringify({ interval_hours: intervalHours }) }),
  // Supervisor panel (docs/supervisor-model.md): the models that work for the
  // administrator plus the human-attention estimate coefficients.
  getSupervisorSettings: () =>
    request<SupervisorSettings>('/v1/workspace/supervisor'),
  updateSupervisorSettings: (settings: Partial<SupervisorSettings>) =>
    request<SupervisorSettings>('/v1/workspace/supervisor', { method: 'PUT', body: JSON.stringify(settings) }),
  // Judge-driven knowledge-area normalization: merge duplicate/raw topic and
  // entity values into canonical areas, hide garbage (file names etc.).
  normalizeKnowledgeAreas: (project: string) =>
    request<AreaNormalizationResult>('/v1/workspace/knowledge-areas/normalize', { method: 'POST', body: JSON.stringify({ project }) }),
  createAgentProposal: (id: string, data: { problem: string; change: string; effect?: string; description?: string; definition?: { constraints?: string[]; completion?: string[] }; evidence?: string[] }) =>
    request<AgentProposal>(`/v1/workspace/agents/${id}/proposals`, { method: 'POST', body: JSON.stringify(data) }),
  // Gated apply (docs/agent-quality-loop.md §7.1): the answer is one of two
  // shapes — the applied proposal (200) or the accepted-for-evaluation gate
  // run (202) that a repeat apply waits on. `gate:false` skips the gate and is
  // audited server-side; override paths stay API-only for now.
  applyAgentProposal: async (id: string, proposalId: number, opts?: { gate?: boolean }): Promise<ApplyProposalResult> => {
    const init = opts?.gate === false ? { method: 'POST' as const, body: JSON.stringify({ gate: false }) } : { method: 'POST' as const }
    const result = await request<any>(`/v1/workspace/agents/${id}/proposals/${proposalId}/apply`, init)
    if (result && typeof result.evaluation_run_id === 'number' && !result.proposal) {
      return { kind: 'evaluating', evaluation_run_id: result.evaluation_run_id, status: result.status ?? 'running' }
    }
    return { kind: 'applied', agent: result.agent, proposal: result.proposal }
  },
  rejectAgentProposal: (id: string, proposalId: number) =>
    request<AgentProposal>(`/v1/workspace/agents/${id}/proposals/${proposalId}/reject`, { method: 'POST' }),
  listBuiltinAgents: () =>
    request<{ builtins: BuiltinAgentSpec[] }>('/v1/workspace/agents/builtins'),
  // Sandbox profiles (docs/sandbox-profiles.md §6)
  listSandboxProfiles: () =>
    request<{ profiles: SandboxProfile[] }>('/v1/workspace/sandbox-profiles'),
  // Sandbox wizard (docs/sandbox-profiles.md): plain-language task
  // description -> sandbox profile draft from the supervisor-scope model.
  sandboxBuilder: (description: string) =>
    request<SandboxDraft>('/v1/workspace/sandbox-profiles/builder', { method: 'POST', body: JSON.stringify({ description }) }),
  buildSandboxProfile: (id: string) =>
    request<SandboxProfile>(`/v1/workspace/sandbox-profiles/${id}/build`, { method: 'POST' }),
  createSandboxProfile: (data: Partial<SandboxProfile> & { id: string; name: string }) =>
    request<SandboxProfile>('/v1/workspace/sandbox-profiles', { method: 'POST', body: JSON.stringify(data) }),
  updateSandboxProfile: (id: string, data: Partial<SandboxProfile>) =>
    request<SandboxProfile>(`/v1/workspace/sandbox-profiles/${id}`, { method: 'PUT', body: JSON.stringify(data) }),
  deleteSandboxProfile: (id: string) =>
    request<{ deleted: string }>(`/v1/workspace/sandbox-profiles/${id}`, { method: 'DELETE' }),
  draftAgent: (description: string, final?: boolean) =>
    request<AgentDraft>('/v1/workspace/agents/draft', { method: 'POST', body: JSON.stringify({ description, final: final ?? false }) }),

  // Tasks
  listTasks: (projectId: string) =>
    request<{ tasks: Task[] }>(`/v1/workspace/projects/${projectId}/tasks`),
  getTask: (id: string) => request<Task>(`/v1/workspace/tasks/${id}`),
  createTask: (data: { id?: string; project_id: string; agent_id?: string; title: string; prompt: string }) =>
    request<Task>('/v1/workspace/tasks', { method: 'POST', body: JSON.stringify(data) }),
  updateTask: (id: string, data: { prompt?: string; title?: string }) =>
    request<{ updated: boolean }>(`/v1/workspace/tasks/${id}`, { method: 'PATCH', body: JSON.stringify(data) }),
  deleteTask: (id: string) =>
    request<{ deleted: boolean }>(`/v1/workspace/tasks/${id}`, { method: 'DELETE' }),

  // Runs
  startRun: (taskId: string, data?: { agent_id?: string; model?: string }) =>
    request<{ run_id: string; task_id: string; project: string; status: string }>(
      `/v1/workspace/tasks/${taskId}/runs`, { method: 'POST', body: JSON.stringify(data ?? {}) }),
  getRun: (runId: string) => request<Run>(`/v1/workspace/runs/${runId}`),
  cancelRun: (runId: string) => request<{ run_id: string; status: string }>(`/v1/workspace/runs/${runId}/cancel`, { method: 'POST' }),
  // Mid-run user note (kernel.user_note signal): the message is appended to
  // the RUNNING run's conversation at the nearest turn boundary instead of
  // starting a parallel run. A 409 means the run is already terminal — the
  // caller falls back to the normal new-run flow.
  appendRunNote: (runId: string, text: string, project: string) =>
    request<{ accepted: boolean }>(`/v1/agent/runs/${encodeURIComponent(runId)}/note?project=${encodeURIComponent(project)}`, { method: 'POST', body: JSON.stringify({ text }) }),
  getTrajectory: (runId: string) => request<any>(`/v1/workspace/runs/${runId}/trajectory`),
  getProjection: (projectId: string) => request<any>(`/v1/workspace/projects/${projectId}/projection`),
  compareRuns: (runA: string, runB: string) => request<any>(`/v1/workspace/runs/compare?a=${runA}&b=${runB}`),
  getArtifacts: (runId: string) => request<any>(`/v1/workspace/runs/${runId}/artifacts`),
  listRuns: (taskId: string) =>
    request<{ runs: Run[] }>(`/v1/workspace/tasks/${taskId}/runs`),

  // Providers
  listProviders: () => request<{ providers: Provider[] }>('/v1/workspace/providers'),
  getProvider: (id: string) => request<Provider>(`/v1/workspace/providers/${id}`),
  createProvider: (data: { id?: string; name: string; base_url: string; api_key_ref?: string; models?: string[]; org_unit_id?: string }) =>
    request<Provider>('/v1/workspace/providers', { method: 'POST', body: JSON.stringify(data) }),
  updateProvider: (id: string, data: Partial<Provider>) =>
    request<Provider>(`/v1/workspace/providers/${id}`, { method: 'PUT', body: JSON.stringify(data) }),
  deleteProvider: (id: string) =>
    request<{ deleted: boolean }>(`/v1/workspace/providers/${id}`, { method: 'DELETE' }),
  // Catalog sync (providermodels.go): refresh mirrors the stored provider's
  // catalog; probe previews a not-yet-saved form's catalog without persisting.
  refreshProviderModels: (id: string) =>
    request<{ models: string[]; count: number }>(`/v1/workspace/providers/${id}/refresh-models`, { method: 'POST' }),
  probeProviderModels: (data: { base_url: string; api_key_ref?: string }) =>
    request<{ models: string[]; count: number }>('/v1/workspace/providers/probe-models', { method: 'POST', body: JSON.stringify(data) }),

  // Triggers. listTriggers without a project hits the global listing
  // (docs/activity-scoping.md §5) — server-side constrained to the visible
  // projects — and includes a per-trigger project attribution for the UI.
  listTriggers: (projectId?: string) =>
    request<{ triggers: Trigger[] }>(projectId && projectId.trim() ? `/v1/workspace/projects/${encodeURIComponent(projectId.trim())}/triggers` : '/v1/workspace/triggers'),
  createTrigger: (data: Partial<Trigger>) =>
    request<Trigger>('/v1/workspace/triggers', { method: 'POST', body: JSON.stringify(data) }),
  updateTrigger: (id: string, data: Partial<Trigger>) =>
    request<Trigger>(`/v1/workspace/triggers/${id}`, { method: 'PUT', body: JSON.stringify(data) }),
  deleteTrigger: (id: string) =>
    request<{ deleted: boolean }>(`/v1/workspace/triggers/${id}`, { method: 'DELETE' }),
  // Per-trigger webhook delivery token: generate returns the raw "whk_…"
  // credential exactly once; revoke clears it (deliveries fall back to the
  // regular writer authorization).
  generateWebhookToken: (id: string) =>
    request<{ trigger_id: string; token: string }>(`/v1/workspace/triggers/${id}/webhook-token`, { method: 'POST' }),
  revokeWebhookToken: (id: string) =>
    request<{ trigger_id: string; revoked: boolean }>(`/v1/workspace/triggers/${id}/webhook-token`, { method: 'DELETE' }),

  // Execution identities (org-structure.md §20): CRUD is admin-gated; the
  // store defaults absent allowed-lists to ["*"], so an empty list = any.
  listExecutionIdentities: () =>
    request<{ identities: ExecutionIdentity[] }>('/v1/workspace/execution-identities'),
  createExecutionIdentity: (data: Partial<ExecutionIdentity> & { name: string }) =>
    request<ExecutionIdentity>('/v1/workspace/execution-identities', { method: 'POST', body: JSON.stringify(data) }),
  updateExecutionIdentity: (id: string, data: Partial<ExecutionIdentity> & { name: string }) =>
    request<ExecutionIdentity>(`/v1/workspace/execution-identities/${id}`, { method: 'PUT', body: JSON.stringify(data) }),
  deleteExecutionIdentity: (id: string) =>
    request<{ deleted: boolean }>(`/v1/workspace/execution-identities/${id}`, { method: 'DELETE' }),

  // Skills (workspace-global registry)
  listSkills: () =>
    request<{ skills: Skill[]; count: number }>('/v1/workspace/skills'),
  getSkill: (id: string) => request<Skill>(`/v1/workspace/skills/${id}`),
  createSkill: (data: { id: string; name: string; description: string; version: string; markdown: string; manifest_yaml: string; org_unit_id?: string }) =>
    request<Skill>('/v1/workspace/skills', { method: 'POST', body: JSON.stringify(data) }),
  updateSkill: (id: string, data: SkillInput) =>
    request<Skill>(`/v1/workspace/skills/${id}`, { method: 'PUT', body: JSON.stringify(data) }),
  deleteSkill: (id: string) =>
    request<{ status: string }>(`/v1/workspace/skills/${id}`, { method: 'DELETE' }),
  listSkillVersions: (id: string) =>
    request<{ versions: SkillVersion[] }>(`/v1/workspace/skills/${id}/versions`),
  applySkillVersion: (id: string, version: string) =>
    request<Skill>(`/v1/workspace/skills/${id}/versions/${version}/apply`, { method: 'POST' }),
  rejectSkillVersion: (id: string, version: string) =>
    request<SkillVersion>(`/v1/workspace/skills/${id}/versions/${version}/reject`, { method: 'POST' }),
  getSkillEvaluationSuite: (id: string) =>
    request<SkillEvaluationSuite>(`/v1/workspace/skills/${id}/evaluation-suite`),
  saveSkillEvaluationSuite: (id: string, cases: SkillEvaluationCase[]) =>
    request<SkillEvaluationSuite>(`/v1/workspace/skills/${id}/evaluation-suite`, { method: 'PUT', body: JSON.stringify({ cases }) }),
  runSkillEvaluation: (id: string, version?: string) =>
    request<SkillEvaluationRun>(`/v1/workspace/skills/${id}/evaluations`, { method: 'POST', body: JSON.stringify(version ? { version } : {}) }),
  listSkillEvaluationRuns: (id: string) =>
    request<{ runs: SkillEvaluationRun[] }>(`/v1/workspace/skills/${id}/evaluations`),
  validateSkill: (id: string, data: { markdown: string; manifest_yaml: string }) =>
    request<SkillValidation>(`/v1/workspace/skills/${id}/validate`, { method: 'POST', body: JSON.stringify(data) }),
  draftSkill: (description: string, final?: boolean) =>
    request<SkillDraft>('/v1/workspace/skills/draft', { method: 'POST', body: JSON.stringify({ description, final: final ?? false }) }),
  // agentskills.io import (multipart — bypasses the JSON request() helper).
  // `paths` mirrors `files`: Go's multipart reader strips directories from
  // filenames, so folder uploads (webkitRelativePath) travel side by side.
  importSkill: async (files: File[]): Promise<SkillImportResult> => {
    const body = new FormData()
    for (const file of files) {
      const relative = (file as File & { webkitRelativePath?: string }).webkitRelativePath
      body.append('paths', relative && relative.length > 0 ? relative : file.name)
      body.append('files', file)
    }
    const response = await fetch(`${KERNEL_API}/v1/workspace/skills/import`, {
      method: 'POST',
      body,
      headers: authHeaders(),
    })
    if (!response.ok) {
      const payload = await response.json().catch(() => ({}))
      throw new Error(payload.error ?? `HTTP ${response.status}`)
    }
    return response.json()
  },
  listSkillExecutions: (id: string) =>
    request<{ executions: SkillExecution[] }>(`/v1/workspace/skills/${id}/executions`),
  listSkillMemory: (id: string) =>
    request<{ memory: SkillMemoryItem[] }>(`/v1/workspace/skills/${id}/memory`),

  // Teams (docs/agent-teams.md). CRUD mirrors skills: a human save (PUT) is
  // itself the approval for the same version string; draft versions come from
  // the (wave E) builder / (wave G) evolution proposals and need an explicit
  // apply before they can launch.
  listTeams: () =>
    request<{ teams: Team[]; count: number }>('/v1/workspace/teams'),
  getTeam: (id: string) => request<Team>(`/v1/workspace/teams/${id}`),
  createTeam: (data: { id?: string; name: string; description?: string; version?: string; manifest: TeamManifest; org_unit_id?: string }) =>
    request<Team>('/v1/workspace/teams', { method: 'POST', body: JSON.stringify(data) }),
  updateTeam: (id: string, data: { name: string; description?: string; version?: string; manifest: TeamManifest }) =>
    request<Team>(`/v1/workspace/teams/${id}`, { method: 'PUT', body: JSON.stringify(data) }),
  deleteTeam: (id: string) =>
    request<{ deleted: boolean }>(`/v1/workspace/teams/${id}`, { method: 'DELETE' }),
  listTeamVersions: (id: string) =>
    request<{ versions: TeamVersion[] }>(`/v1/workspace/teams/${id}/versions`),
  applyTeamVersion: (id: string, version: string) =>
    request<Team>(`/v1/workspace/teams/${id}/versions/${version}/apply`, { method: 'POST' }),
  rejectTeamVersion: (id: string, version: string) =>
    request<TeamVersion>(`/v1/workspace/teams/${id}/versions/${version}/reject`, { method: 'POST' }),
  // Launch preview (wave C): resolves every slot against the caller's roster
  // without starting anything — a preview never fails as a whole, ambiguous
  // and unresolvable slots are reported per slot.
  resolveTeam: (id: string, project: string, version?: string) => {
    const query = new URLSearchParams({ project })
    if (version) query.set('version', version)
    return request<TeamResolvePreview>(`/v1/agent/teams/${id}/resolve?${query.toString()}`)
  },
  // Start a team run: bindings are explicit launch-time overrides (slot →
  // agent); a 409 lists candidates for an ambiguous slot, a 422 lists the
  // requirements no visible agent satisfies.
  startTeamRun: (id: string, data: { project: string; goal: string; version?: string; bindings?: Record<string, string> }) =>
    request<TeamRunStartResult>(`/v1/agent/teams/${id}/runs`, { method: 'POST', body: JSON.stringify(data) }),

  // Team telemetry (wave G): run-tree rollup, per-version metrics and the
  // deterministic evolution detector.
  teamRunCost: (project: string, runId: string) => {
    const query = new URLSearchParams({ project, run_id: runId })
    return request<TeamRunCost>(`/v1/agent/cost/team-run?${query.toString()}`)
  },
  teamMetrics: (id: string, project: string) => {
    const query = new URLSearchParams({ project })
    return request<TeamMetrics>(`/v1/workspace/teams/${id}/metrics?${query.toString()}`)
  },
  detectTeamEvolution: (id: string, project: string) =>
    request<{ detected: boolean; version?: TeamVersion; reason?: string }>(`/v1/workspace/teams/${id}/evolution/detect?project=${encodeURIComponent(project)}`, { method: 'POST' }),

  // Team creation wizard (wave E): natural language in, a validated manifest
  // draft out. Creates nothing — the draft only prefills the form.
  draftTeam: (description: string, final?: boolean) =>
    request<TeamDraft>('/v1/workspace/teams/builder', { method: 'POST', body: JSON.stringify({ description, final: final ?? false }) }),

  // MCP servers (workspace-global registry)
  listMCPServers: () =>
    request<{ servers: MCPServer[]; count: number }>('/v1/workspace/mcp-servers'),
  getMCPServer: (id: string) => request<MCPServer>(`/v1/workspace/mcp-servers/${id}`),
  createMCPServer: (data: Partial<MCPServer> & { name: string }) =>
    request<MCPServer>('/v1/workspace/mcp-servers', { method: 'POST', body: JSON.stringify(data) }),
  updateMCPServer: (id: string, data: Partial<MCPServer>) =>
    request<MCPServer>(`/v1/workspace/mcp-servers/${id}`, { method: 'PUT', body: JSON.stringify(data) }),
  deleteMCPServer: (id: string) =>
    request<{ deleted: boolean }>(`/v1/workspace/mcp-servers/${id}`, { method: 'DELETE' }),
  discoverMCPServer: (data: Partial<MCPServer>) =>
    request<MCPDiscoverResult>('/v1/workspace/mcp-servers/discover', { method: 'POST', body: JSON.stringify(data) }),
  listMCPTools: () =>
    request<{ servers: Record<string, MCPServerTools>; builtins: MCPToolInfo[] }>('/v1/workspace/mcp-tools'),

  // Secrets (docs/secrets-management.md Phase 1): values are write-only.
  listSecrets: () =>
    request<{ secrets: Secret[]; count: number }>('/v1/workspace/secrets'),
  createSecret: (data: Partial<Secret> & { name: string; value?: string }) =>
    request<Secret>('/v1/workspace/secrets', { method: 'POST', body: JSON.stringify(data) }),
  updateSecret: (id: string, data: Partial<Secret> & { value?: string }) =>
    request<Secret>(`/v1/workspace/secrets/${id}`, { method: 'PUT', body: JSON.stringify(data) }),
  deleteSecret: (id: string) =>
    request<{ deleted: boolean }>(`/v1/workspace/secrets/${id}`, { method: 'DELETE' }),
  setSecretValue: (id: string, value: string) =>
    request<{ stored: boolean }>(`/v1/workspace/secrets/${id}/value`, { method: 'POST', body: JSON.stringify({ value }) }),

  // Reference artifacts (docs/artifact-registry.md phase 2): bodies load on
  // demand; lifecycle filters (status/kind/saga) narrow the listing.
  listArtifacts: (params?: { status?: string; kind?: string; saga_id?: string }) => {
    const query = new URLSearchParams()
    if (params?.status) query.set('status', params.status)
    if (params?.kind) query.set('kind', params.kind)
    if (params?.saga_id) query.set('saga_id', params.saga_id)
    const suffix = query.size > 0 ? `?${query.toString()}` : ''
    return request<{ artifacts: Artifact[]; count: number }>(`/v1/workspace/artifacts${suffix}`)
  },
  getArtifact: (id: string) =>
    request<{ artifact: Artifact; offset: number; next_offset: number; truncated: boolean }>(`/v1/workspace/artifacts/${id}`),
  createArtifact: (data: Partial<Artifact> & { id: string; title: string }) =>
    request<Artifact>('/v1/workspace/artifacts', { method: 'POST', body: JSON.stringify(data) }),
  updateArtifact: (id: string, data: Partial<Artifact>) =>
    request<Artifact>(`/v1/workspace/artifacts/${id}`, { method: 'PUT', body: JSON.stringify(data) }),
  deleteArtifact: (id: string) =>
    request<{ deleted: boolean }>(`/v1/workspace/artifacts/${id}`, { method: 'DELETE' }),
  submitArtifact: (id: string, data: { reviewers: string[]; mode?: string; note?: string }) =>
    request<ArtifactReview>(`/v1/workspace/artifacts/${id}/submit`, { method: 'POST', body: JSON.stringify(data) }),
  listArtifactReviews: (id: string) =>
    request<{ reviews: ArtifactReview[] }>(`/v1/workspace/artifacts/${id}/reviews`),
  resolveArtifactReview: (id: string, reviewId: string, approve: boolean, comment: string) =>
    request<{ review: ArtifactReview; artifact: Artifact }>(`/v1/workspace/artifacts/${id}/reviews/${reviewId}/${approve ? 'approve' : 'reject'}`, { method: 'POST', body: JSON.stringify({ comment }) }),
  artifactReferences: () =>
    request<{ references: Record<string, string[]> }>('/v1/workspace/artifacts/references'),

  // Research sessions (docs/human-knowledge-explorer.md §11): saved
  // knowledge-explorer investigations. The payload is references only; the
  // list stays light, the full payload arrives on get.
  listResearchSessions: (project: string) =>
    request<{ sessions: ResearchSession[] }>(`/v1/workspace/research-sessions?project=${encodeURIComponent(project)}`),
  createResearchSession: (data: { project: string; title: string; payload: ResearchSessionPayload }) =>
    request<ResearchSession>('/v1/workspace/research-sessions', { method: 'POST', body: JSON.stringify(data) }),
  getResearchSession: (id: number) =>
    request<ResearchSession>(`/v1/workspace/research-sessions/${id}`),
  updateResearchSession: (id: number, data: { title: string; payload: ResearchSessionPayload }) =>
    request<ResearchSession>(`/v1/workspace/research-sessions/${id}`, { method: 'PUT', body: JSON.stringify(data) }),
  deleteResearchSession: (id: number) =>
    request<{ deleted: boolean }>(`/v1/workspace/research-sessions/${id}`, { method: 'DELETE' }),

  // Users
  listUsers: () => request<{ users: User[] }>('/v1/workspace/users'),
  getUser: (id: string) => request<User>(`/v1/workspace/users/${id}`),
  createUser: (data: { id?: string; name: string; email?: string; role: string; org_unit_id?: string }) =>
    request<User>('/v1/workspace/users', { method: 'POST', body: JSON.stringify(data) }),
  updateUser: (id: string, data: Partial<User>) =>
    request<User>(`/v1/workspace/users/${id}`, { method: 'PUT', body: JSON.stringify(data) }),
  regenerateUserToken: (id: string) =>
    request<{ id: string; token: string }>(`/v1/workspace/users/${id}/token`, { method: 'POST' }),
  updateUserChannels: (id: string, data: { channels: UserChannel[]; preferred_channel: string }) =>
    request<User>(`/v1/workspace/users/${id}/channels`, { method: 'PUT', body: JSON.stringify(data) }),
  // Kernel transport registry: the channel types users may configure
  channelTypes: () => request<{ types: ChannelTypeSpec[] }>('/v1/workspace/channel-types'),
  // Admin transport credentials (masked view + upsert)
  getChannelSettings: () => request<ChannelSettings>('/v1/workspace/channel-settings'),
  updateChannelSettings: (data: ChannelSettingsUpdate) =>
    request<ChannelSettings>('/v1/workspace/channel-settings', { method: 'PUT', body: JSON.stringify(data) }),
  deleteUser: (id: string) =>
    request<{ deleted: boolean }>(`/v1/workspace/users/${id}`, { method: 'DELETE' }),

  // Human-in-the-loop requests (docs/org-structure.md §28, §37)
  listHumanRequests: (params?: { project?: string; status?: string; user?: string; onlyOpen?: boolean }) => {
    const query = new URLSearchParams()
    if (params?.project) query.set('project', params.project)
    if (params?.status) query.set('status', params.status)
    if (params?.user) query.set('user', params.user)
    if (params?.onlyOpen) query.set('open', '1')
    const suffix = query.size > 0 ? `?${query.toString()}` : ''
    return request<{ requests: HumanRequest[] }>(`/v1/workspace/human-requests${suffix}`)
  },

  // Intake inbox (docs/inbound-messages.md §11): channel-initiated messages
  // and where the intake pipeline routed them.
  listInboundMessages: (params?: { status?: string; channel?: string; limit?: number }) => {
    const query = new URLSearchParams()
    if (params?.status) query.set('status', params.status)
    if (params?.channel) query.set('channel', params.channel)
    if (params?.limit) query.set('limit', String(params.limit))
    const suffix = query.size > 0 ? `?${query.toString()}` : ''
    return request<{ messages: InboundMessage[]; counts?: Record<string, number> }>(`/v1/workspace/inbound-messages${suffix}`)
  },

  // Org structure (docs/org-structure.md §38-39)
  listOrgUnits: () => request<{ units: OrgUnit[] }>('/v1/org/units'),
  createOrgUnit: (data: { kind: string; name: string; parent_id?: string }) =>
    request<OrgUnit>('/v1/org/units', { method: 'POST', body: JSON.stringify(data) }),
  updateOrgUnit: (id: string, data: { name: string; kind?: string; parent_id?: string; head_user_id?: string }) =>
    request<OrgUnit>(`/v1/org/units/${id}`, { method: 'PUT', body: JSON.stringify(data) }),
  deleteOrgUnit: (id: string) =>
    request<{ deleted: boolean }>(`/v1/org/units/${id}`, { method: 'DELETE' }),
  unitResources: (id: string) => request<UnitResources>(`/v1/org/units/${id}/resources`),
  effectivePolicy: (id: string) =>
    request<{ policy: Policy; sources: Policy[] }>(`/v1/org/units/${id}/effective-policy`),

  // Unit role grants (org-structure.md §13): mutations are admin-gated server-side.
  listUnitRoles: (unitId: string) =>
    request<{ roles: OrgUnitRoleGrant[] }>(`/v1/org/units/${unitId}/roles`),
  setUnitRole: (unitId: string, userId: string, role: string) =>
    request<{ user_id: string; org_unit_id: string; role: string }>(`/v1/org/units/${unitId}/roles/${userId}`, { method: 'PUT', body: JSON.stringify({ role }) }),
  removeUnitRole: (unitId: string, userId: string) =>
    request<{ removed: boolean }>(`/v1/org/units/${unitId}/roles/${userId}`, { method: 'DELETE' }),

  // Policies (org-structure.md §24)
  listPolicies: () => request<{ policies: Policy[] }>('/v1/workspace/policies'),
  createPolicy: (data: Partial<Policy> & { name: string }) =>
    request<Policy>('/v1/workspace/policies', { method: 'POST', body: JSON.stringify(data) }),
  updatePolicy: (id: string, data: Partial<Policy>) =>
    request<Policy>(`/v1/workspace/policies/${id}`, { method: 'PUT', body: JSON.stringify(data) }),
  deletePolicy: (id: string) =>
    request<{ deleted: boolean }>(`/v1/workspace/policies/${id}`, { method: 'DELETE' }),
  setResourceBinding: (kind: OrgResourceKind, id: string, orgUnitID: string) =>
    request<{ kind: string; resource_id: string; org_unit_id: string }>(`/v1/org/resources/${kind}/${id}/binding`, { method: 'PUT', body: JSON.stringify({ org_unit_id: orgUnitID }) }),
}