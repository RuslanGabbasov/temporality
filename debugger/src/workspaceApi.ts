import { authHeaders } from './api'

const KERNEL_API = '/kernel-api'

export interface Project {
  id: string
  name: string
  description: string
  default_agent_id?: string
  default_model?: string
  archived?: boolean
  allowed_users?: string[] // '*' = all, [] = admin only, ['user-1'] = specific users
  created_at: string
  updated_at: string
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
  sandbox_profile: string
  network_access?: boolean
  read_only?: boolean
  max_turns?: number
  approval_mode: string
  labels: Record<string, string>
  created_at: string
  updated_at: string
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
  created_at: string
  updated_at: string
}

export interface Skill {
  id: string
  project_id: string
  name: string
  description: string
  version: string
  markdown: string
  manifest: Record<string, unknown>
  created_at: string
  updated_at: string
}

export interface SkillVersion {
  skill_id: string
  version: string
  markdown: string
  manifest: Record<string, unknown>
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

export interface SkillInput {
  name: string
  description: string
  version: string
  markdown: string
  manifest_yaml: string
}

export interface MCPServer {
  id: string
  project_id: string
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
  created_at: string
  updated_at: string
}

export interface User {
  id: string
  name: string
  email: string
  role: string
  token?: string
  projects: string[]
  active: boolean
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
  createProject: (data: { id?: string; name: string; description?: string; allowed_users?: string[] }) =>
    request<Project>('/v1/workspace/projects', { method: 'POST', body: JSON.stringify(data) }),
  updateProject: (id: string, data: { name: string; description?: string; allowed_users?: string[] }) =>
    request<Project>(`/v1/workspace/projects/${id}`, { method: 'PUT', body: JSON.stringify(data) }),
  deleteProject: (id: string) =>
    request<{ deleted: boolean }>(`/v1/workspace/projects/${id}`, { method: 'DELETE' }),

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
  createProvider: (data: { id?: string; name: string; base_url: string; api_key_ref?: string; models?: string[] }) =>
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

  // Skills
  listSkills: (projectId: string) =>
    request<{ skills: Skill[]; count: number }>(`/v1/workspace/skills?project=${encodeURIComponent(projectId)}`),
  getSkill: (id: string) => request<Skill>(`/v1/workspace/skills/${id}`),
  createSkill: (data: { id: string; project_id: string; name: string; description: string; version: string; markdown: string; manifest_yaml: string }) =>
    request<Skill>('/v1/workspace/skills', { method: 'POST', body: JSON.stringify(data) }),
  updateSkill: (id: string, data: SkillInput) =>
    request<Skill>(`/v1/workspace/skills/${id}`, { method: 'PUT', body: JSON.stringify(data) }),
  deleteSkill: (id: string) =>
    request<{ status: string }>(`/v1/workspace/skills/${id}`, { method: 'DELETE' }),
  listSkillVersions: (id: string) =>
    request<{ versions: SkillVersion[] }>(`/v1/workspace/skills/${id}/versions`),
  validateSkill: (id: string, data: { markdown: string; manifest_yaml: string }) =>
    request<SkillValidation>(`/v1/workspace/skills/${id}/validate`, { method: 'POST', body: JSON.stringify(data) }),
  listSkillExecutions: (id: string) =>
    request<{ executions: SkillExecution[] }>(`/v1/workspace/skills/${id}/executions`),
  listSkillMemory: (id: string) =>
    request<{ memory: SkillMemoryItem[] }>(`/v1/workspace/skills/${id}/memory`),

  // MCP servers
  listMCPServers: (projectId?: string) =>
    request<{ servers: MCPServer[]; count: number }>(
      projectId
        ? `/v1/workspace/mcp-servers?project=${encodeURIComponent(projectId)}`
        : '/v1/workspace/mcp-servers'),
  getMCPServer: (id: string) => request<MCPServer>(`/v1/workspace/mcp-servers/${id}`),
  createMCPServer: (data: Partial<MCPServer> & { name: string; project_id: string }) =>
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
  createUser: (data: { id?: string; name: string; email?: string; role: string; projects?: string[] }) =>
    request<User>('/v1/workspace/users', { method: 'POST', body: JSON.stringify(data) }),
  updateUser: (id: string, data: Partial<User>) =>
    request<User>(`/v1/workspace/users/${id}`, { method: 'PUT', body: JSON.stringify(data) }),
  deleteUser: (id: string) =>
    request<{ deleted: boolean }>(`/v1/workspace/users/${id}`, { method: 'DELETE' }),
}