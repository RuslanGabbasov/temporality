import type { BlameRequest, CreateFrameRequest, CreateFrameResponse, CreateObjectiveRequest, CreateObjectiveResponse, Execution, ForkGroup, ForkRequest, Frame, FrpEvent, ModelConfig, ModelStepRequest, ModelStepResponse, RenderRequest, RenderResponse } from './types'

export const API_BASE = (import.meta.env.VITE_FRP_API_URL || '/api').replace(/\/$/, '')

export function apiUrl(path: string, query?: Record<string, string | number | undefined>): string {
  const normalized = path.startsWith('/') ? path : `/${path}`
  const params = new URLSearchParams()
  Object.entries(query ?? {}).forEach(([key, value]) => {
    if (value !== undefined && value !== '') params.set(key, String(value))
  })
  const suffix = params.size ? `?${params.toString()}` : ''
  return `${API_BASE}${normalized}${suffix}`
}

async function request<T>(path: string, init?: RequestInit, query?: Record<string, string | number | undefined>): Promise<T> {
  const response = await fetch(apiUrl(path, query), {
    ...init,
    headers: { Accept: 'application/json', ...(init?.body ? { 'Content-Type': 'application/json' } : {}), ...init?.headers },
  })
  if (!response.ok) {
    const detail = await response.text().catch(() => '')
    throw new Error(`${response.status} ${response.statusText}${detail ? ` — ${detail}` : ''}`)
  }
  if (response.status === 204) return undefined as T
  return response.json() as Promise<T>
}

const post = <T>(path: string, body: unknown) => request<T>(path, { method: 'POST', body: JSON.stringify(body) })

export const api = {
  events: (episodeId: string, limit: number) => request<FrpEvent[] | { events: FrpEvent[] }>('/v1/events', undefined, { episode_id: episodeId, limit }),
  frame: (frameId: string) => request<Frame>(`/v1/frames/${encodeURIComponent(frameId)}`),
  execution: (executionId: string) => request<Execution>(`/v1/executions/${encodeURIComponent(executionId)}`),
  render: (body: RenderRequest) => post<RenderResponse>('/v1/render', body),
  replay: (frameId: string) => post<unknown>('/v1/replay', { frame_id: frameId }),
  blame: (body: BlameRequest) => post<unknown>('/v1/blame', body),
  fork: (body: ForkRequest) => post<ForkGroup>('/v1/fork', body),
  createObjective: (body: CreateObjectiveRequest) => post<CreateObjectiveResponse>('/v1/objectives', body),
  createFrame: (body: CreateFrameRequest) => post<CreateFrameResponse>('/v1/frames', body),
  modelStep: (body: ModelStepRequest) => post<ModelStepResponse>('/v1/model-step', body),
  modelConfig: () => request<ModelConfig>('/v1/model/config'),
  rebuildRegions: (episodeId: string, branchId: string) => post<unknown>('/v1/projections/regions/rebuild', { episode_id: episodeId, branch_id: branchId }),
}
