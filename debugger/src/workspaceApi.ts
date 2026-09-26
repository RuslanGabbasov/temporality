import { authHeaders } from './api'

// Workspace endpoints live on the kernel API, not the journal.
const KERNEL_API = '/kernel-api'

export interface Project {
  id: string
  name: string
  description: string
  created_at: string
  updated_at: string
}

export interface Agent {
  id: string
  project_id: string
  name: string
  model: string
  system_prompt: string
  skills: string[]
  mcp_servers: string[]
  sandbox_profile: string
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
  createProject: (data: { id?: string; name: string; description?: string }) =>
    request<Project>('/v1/workspace/projects', { method: 'POST', body: JSON.stringify(data) }),
  updateProject: (id: string, data: { name: string; description?: string }) =>
    request<Project>(`/v1/workspace/projects/${id}`, { method: 'PUT', body: JSON.stringify(data) }),
  deleteProject: (id: string) =>
    request<{ deleted: boolean }>(`/v1/workspace/projects/${id}`, { method: 'DELETE' }),

  // Agents
  listAgents: (projectId: string) =>
    request<{ agents: Agent[] }>(`/v1/workspace/projects/${projectId}/agents`),
  getAgent: (id: string) => request<Agent>(`/v1/workspace/agents/${id}`),
  createAgent: (data: { id?: string; project_id: string; name: string; model?: string; system_prompt?: string; skills?: string[]; mcp_servers?: string[]; sandbox_profile?: string }) =>
    request<Agent>('/v1/workspace/agents', { method: 'POST', body: JSON.stringify(data) }),
  updateAgent: (id: string, data: { project_id?: string; name: string; model?: string; system_prompt?: string; skills?: string[]; mcp_servers?: string[]; sandbox_profile?: string }) =>
    request<Agent>(`/v1/workspace/agents/${id}`, { method: 'PUT', body: JSON.stringify(data) }),
  deleteAgent: (id: string) =>
    request<{ deleted: boolean }>(`/v1/workspace/agents/${id}`, { method: 'DELETE' }),

  // Tasks
  listTasks: (projectId: string) =>
    request<{ tasks: Task[] }>(`/v1/workspace/projects/${projectId}/tasks`),
  getTask: (id: string) => request<Task>(`/v1/workspace/tasks/${id}`),
  createTask: (data: { id?: string; project_id: string; agent_id?: string; title: string; prompt: string }) =>
    request<Task>('/v1/workspace/tasks', { method: 'POST', body: JSON.stringify(data) }),
  deleteTask: (id: string) =>
    request<{ deleted: boolean }>(`/v1/workspace/tasks/${id}`, { method: 'DELETE' }),

  // Runs
  startRun: (taskId: string, data?: { agent_id?: string; model?: string }) =>
    request<{ run_id: string; task_id: string; project: string; status: string }>(
      `/v1/workspace/tasks/${taskId}/runs`, { method: 'POST', body: JSON.stringify(data ?? {}) }),
  getRun: (runId: string) => request<Run>(`/v1/workspace/runs/${runId}`),
  listRuns: (taskId: string) =>
    request<{ runs: Run[] }>(`/v1/workspace/tasks/${taskId}/runs`),
}