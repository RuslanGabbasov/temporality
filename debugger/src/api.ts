export const API_BASE = (import.meta.env.VITE_API_URL || '/api').replace(/\/$/, '')

export const TOKEN_STORAGE_KEY = 'temporality_token'

// The project the user worked in last: restored on load so a fresh visit
// does not dump them back into the default (system) project.
export const PROJECT_STORAGE_KEY = 'temporality_project'

export function storedProject(): string {
  return localStorage.getItem(PROJECT_STORAGE_KEY) ?? ''
}

export function rememberProject(project: string) {
  if (project) localStorage.setItem(PROJECT_STORAGE_KEY, project)
}

export function authToken(): string {
  return localStorage.getItem(TOKEN_STORAGE_KEY) ?? ''
}

export function authHeaders(): Record<string, string> {
  const token = authToken()
  return token ? { Authorization: `Bearer ${token}` } : {}
}

export function apiUrl(path: string, query?: Record<string, string | number | undefined>): string {
  const normalized = path.startsWith('/') ? path : `/${path}`
  const params = new URLSearchParams()
  Object.entries(query ?? {}).forEach(([key, value]) => {
    if (value !== undefined && value !== '') params.set(key, String(value))
  })
  const suffix = params.size ? `?${params.toString()}` : ''
  return `${API_BASE}${normalized}${suffix}`
}
