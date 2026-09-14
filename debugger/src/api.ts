import type { Execution, Frame, FrpEvent, RenderResponse } from './types'

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
  render: (frameId: string, objectiveId?: string) => post<RenderResponse>('/v1/render', { frame_id: frameId, ...(objectiveId ? { objective_id: objectiveId } : {}) }),
  replay: (frameId: string) => post<unknown>('/v1/replay', { frame_id: frameId }),
  blame: (frameId: string, query: string) => post<unknown>('/v1/blame', { frame_id: frameId, query }),
  fork: (frameId: string, branchId: string, label: string) => post<unknown>('/v1/fork', { frame_id: frameId, branch_id: branchId, label }),
}
