import { authHeaders } from './api'

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

export type OrgResourceKind = 'agent' | 'skill' | 'mcp-server' | 'provider' | 'trigger'

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

export interface BuiltinAgentSpec {
  slug: string
  name: string
  description: string
  definition: AgentDefinition
  sandbox_profile: string
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

export interface SkillInput {
  name: string
  description: string
  version: string
  markdown: string
  manifest_yaml: string
  org_unit_id?: string // honored on create; updates keep the stored binding
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

/** Admin transport settings (channel_transport row over env): non-secrets
 * come back as {value, source}, secrets as {set, hint, source} — the value
 * itself never leaves the kernel. Source says who wins: database | env | none. */
export interface ChannelSettingsField {
  value?: string
  set?: boolean
  hint?: string
  source: 'database' | 'env' | 'none'
}

export interface ChannelSettings {
  matrix_homeserver: ChannelSettingsField
  matrix_access_token: ChannelSettingsField
  telegram_bot_token: ChannelSettingsField
  webhook_secret: ChannelSettingsField
  ui_url: ChannelSettingsField
  updated_at?: string
}

export interface ChannelSettingsUpdate {
  matrix_homeserver?: string // present (even "") sets/clears the plain field
  ui_url?: string
  matrix_access_token?: string // non-empty sets the secret; absent keeps
  telegram_bot_token?: string
  webhook_secret?: string
  clear?: string[] // field names to wipe (secrets and plain alike)
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
  org_unit_id?: string // primary unit; empty = unassigned (sees everything, transition)
  active: boolean
  channels?: UserChannel[]
  preferred_channel?: string // '' / 'web', or an enabled channel type
  created_at: string
  updated_at: string
}

const headers = () => ({ 'Content-Type': 'application/json', ...authHeaders() })

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(`${KERNEL_API}${path}`, { ...init, headers: headers() })
  if (!response.ok) {
    const body = await response.json().catch(() => ({}))
    throw new Error(body.error ?? `HTTP ${response.status}`)
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

  // Agents
  listAllAgents: () => request<{ agents: Agent[] }>('/v1/workspace/agents'),
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
  listBuiltinAgents: () =>
    request<{ builtins: BuiltinAgentSpec[] }>('/v1/workspace/agents/builtins'),
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

  // Triggers
  listTriggers: (projectId: string) =>
    request<{ triggers: Trigger[] }>(`/v1/workspace/projects/${projectId}/triggers`),
  createTrigger: (data: Partial<Trigger>) =>
    request<Trigger>('/v1/workspace/triggers', { method: 'POST', body: JSON.stringify(data) }),
  updateTrigger: (id: string, data: Partial<Trigger>) =>
    request<Trigger>(`/v1/workspace/triggers/${id}`, { method: 'PUT', body: JSON.stringify(data) }),
  deleteTrigger: (id: string) =>
    request<{ deleted: boolean }>(`/v1/workspace/triggers/${id}`, { method: 'DELETE' }),

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
  listSkillExecutions: (id: string) =>
    request<{ executions: SkillExecution[] }>(`/v1/workspace/skills/${id}/executions`),
  listSkillMemory: (id: string) =>
    request<{ memory: SkillMemoryItem[] }>(`/v1/workspace/skills/${id}/memory`),

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

  // Org structure (docs/org-structure.md §38-39)
  listOrgUnits: () => request<{ units: OrgUnit[] }>('/v1/org/units'),
  createOrgUnit: (data: { kind: string; name: string; parent_id?: string }) =>
    request<OrgUnit>('/v1/org/units', { method: 'POST', body: JSON.stringify(data) }),
  updateOrgUnit: (id: string, data: { name: string; kind?: string; parent_id?: string }) =>
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