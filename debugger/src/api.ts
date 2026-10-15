export const API_BASE = (import.meta.env.VITE_API_URL || '/api').replace(/\/$/, '')

export const TOKEN_STORAGE_KEY = 'temporality_token'

// The project the user worked in last: restored on load so a fresh visit
// does not dump them back into the default (system) project.
export const PROJECT_STORAGE_KEY = 'temporality_project'

export function storedProject(): string {
  // The empty string is a real choice ("All projects", docs/activity-scoping.md
  // §7) — only a missing key means "no preference yet" and also defaults to
  // the global view.
  return localStorage.getItem(PROJECT_STORAGE_KEY) ?? ''
}

export function rememberProject(project: string) {
  // Store unconditionally: remembering "All projects" must survive reloads
  // just like a concrete project does.
  localStorage.setItem(PROJECT_STORAGE_KEY, project)
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
  // NB: no URLSearchParams.size here — it is a recent addition and the
  // HTTP stand must keep working on older engines (cf. crypto.randomUUID).
  const suffix = params.toString() ? `?${params.toString()}` : ''
  return `${API_BASE}${normalized}${suffix}`
}
