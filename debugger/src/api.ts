export const API_BASE = (import.meta.env.VITE_API_URL || '/api').replace(/\/$/, '')

export function apiUrl(path: string, query?: Record<string, string | number | undefined>): string {
  const normalized = path.startsWith('/') ? path : `/${path}`
  const params = new URLSearchParams()
  Object.entries(query ?? {}).forEach(([key, value]) => {
    if (value !== undefined && value !== '') params.set(key, String(value))
  })
  const suffix = params.size ? `?${params.toString()}` : ''
  return `${API_BASE}${normalized}${suffix}`
}
