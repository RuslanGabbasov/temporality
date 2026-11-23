import { authHeaders } from '../api'
import { KERNEL_API } from '../kernelApi'

export interface GuidancePlan {
  explanation: string
  highlight?: string
  example?: string
  navigate_route?: string
  mode?: 'informational' | 'guided' | 'focused' | string
  docs_link?: string
}

/** Asks the supervisor to explain one learning target in the context of the
 *  current organization (docs/ai-guided-onboarding-and-learning.md §6, P0).
 *  Errors propagate — the caller decides how to degrade. */
export async function explainLearningTarget(project: string, targetId: string, page: string, question?: string): Promise<GuidancePlan> {
  const response = await fetch(`${KERNEL_API}/v1/learning/explain`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', ...authHeaders() },
    body: JSON.stringify({ project, target_id: targetId, page, question: question ?? '' }),
  })
  if (!response.ok) throw new Error(`HTTP ${response.status}`)
  return response.json()
}

export async function fetchLearningCatalog(): Promise<Array<{ id: string; title: string; description: string; page: string; docs_link?: string; feature_id?: string }>> {
  const response = await fetch(`${KERNEL_API}/v1/learning/catalog`, { headers: { ...authHeaders() } })
  if (!response.ok) return []
  const data = await response.json()
  return data.targets ?? []
}

// ── Learning center (P1–P4) ────────────────────────────────────────────────
// Types mirror the kernel's JSON (cmd/agent-kernel/learning.go,
// learningcatalog.go). The lesson DEFINITIONS are declarative: the frontend
// executes steps (navigate/highlight), the kernel keeps the session state.

export interface LearningStep {
  type: string // explain|navigate|highlight|example|user_action|configuration|verification|summary
  title: string
  text?: string
  target_id?: string
  route?: string
  example?: string
  action_hint?: string
}

export interface LearningLessonDef {
  id: string
  version: number
  title: string
  goal?: string
  description?: string
  audience?: string[]
  page: string
  feature_id: string
  steps: LearningStep[]
  verify_feature?: string
  priority: number
  duration_min?: number
}

export interface LearningProgramItem {
  lesson_id: string
  state: string // pending|scheduled|available|in_progress|completed|skipped|not_applicable
  why?: string
  scheduled_at?: string
}

export interface LearningProgram {
  id: string
  user_id: string
  kind: string
  goal?: string
  status: string
  items: LearningProgramItem[]
  created_at: string
  updated_at: string
}

export interface LearningSession {
  id: string
  user_id: string
  lesson_id: string
  program_id?: string
  status: string // active|waiting_for_user|paused|completed|aborted
  step_index: number
  created_at: string
  updated_at: string
}

export interface LearningCapabilityState {
  id: string
  title: string
  about?: string
  applies: boolean
  mastery?: string // unknown|introduced|practiced|verified|needs_refresh
  mastery_source?: string
  mastery_at?: string
}

export interface LearningSuggestion {
  user_id: string
  feature_id: string
  status: string // suggested|dismissed|accepted
  at: string
}

export interface LearningState {
  program: LearningProgram | null
  capabilities: LearningCapabilityState[]
  has_program: boolean
  suggestions: LearningSuggestion[]
}

async function kernelFetch(path: string, init?: RequestInit): Promise<Response> {
  return fetch(`${KERNEL_API}${path}`, {
    ...init,
    headers: { 'Content-Type': 'application/json', ...(init?.headers ?? {}), ...authHeaders() },
  })
}

export async function fetchLearningState(): Promise<LearningState> {
  const response = await kernelFetch('/v1/learning/state')
  if (!response.ok) throw new Error(`HTTP ${response.status}`)
  const data = await response.json()
  return {
    program: data.program ?? null,
    capabilities: data.capabilities ?? [],
    has_program: !!data.has_program,
    suggestions: data.suggestions ?? [],
  }
}

export interface LearningCatalogMeta {
  /** Routes the supervisor can write a lesson for (page highlights). */
  pages: string[]
  /** The current release's «what's new» material, when the build has any. */
  whats_new: { available: boolean; version?: string; changes?: number }
}

export async function fetchLearningLessonsMeta(): Promise<{ lessons: LearningLessonDef[] } & LearningCatalogMeta> {
  const response = await kernelFetch('/v1/learning/lessons')
  if (!response.ok) return { lessons: [], pages: [], whats_new: { available: false } }
  const data = await response.json()
  return {
    lessons: data.lessons ?? [],
    pages: data.pages ?? [],
    whats_new: data.whats_new ?? { available: false },
  }
}

export async function fetchLearningLessons(): Promise<LearningLessonDef[]> {
  return (await fetchLearningLessonsMeta()).lessons
}

/** Ask the supervisor to write a lesson for this user right now: a screen
 *  lesson from its page highlight, or the release's «what's new» lesson.
 *  The kernel serves a fresh same-version lesson from the store (cached). */
export async function generateLearningLesson(kind: 'page' | 'whats_new', page?: string, force = false): Promise<LearningLessonDef> {
  const response = await kernelFetch('/v1/learning/lessons/generate', {
    method: 'POST',
    body: JSON.stringify({ kind, page: page ?? '', force }),
  })
  if (!response.ok) {
    let detail = ''
    try { detail = ((await response.json())?.error as string) ?? '' } catch { /* body may be empty */ }
    throw new Error(detail || `HTTP ${response.status}`)
  }
  const data = await response.json()
  return data.lesson as LearningLessonDef
}

export async function buildLearningProgram(): Promise<LearningProgram> {
  const response = await kernelFetch('/v1/learning/program/build', { method: 'POST', body: '{}' })
  if (!response.ok) throw new Error(`HTTP ${response.status}`)
  return response.json()
}

export async function startLearningSession(lessonId: string): Promise<LearningSession> {
  const response = await kernelFetch('/v1/learning/sessions/start', { method: 'POST', body: JSON.stringify({ lesson_id: lessonId }) })
  if (!response.ok) throw new Error(`HTTP ${response.status}`)
  return response.json()
}

export async function fetchCurrentSession(): Promise<{ session: LearningSession | null; lesson: LearningLessonDef | null }> {
  const response = await kernelFetch('/v1/learning/sessions/current')
  if (!response.ok) throw new Error(`HTTP ${response.status}`)
  const data = await response.json()
  return { session: data.session ?? null, lesson: data.lesson ?? null }
}

export async function postSessionStep(sessionId: string, action: string, evidence?: string): Promise<{ session: LearningSession; lesson: LearningLessonDef | null; verification?: { ok: boolean; detail?: string } }> {
  const response = await kernelFetch(`/v1/learning/sessions/${sessionId}/step`, { method: 'POST', body: JSON.stringify({ action, evidence: evidence ?? '' }) })
  if (!response.ok) throw new Error(`HTTP ${response.status}`)
  return response.json()
}

export async function resumeLearningSession(sessionId: string): Promise<{ session: LearningSession; lesson: LearningLessonDef | null }> {
  const response = await kernelFetch(`/v1/learning/sessions/${sessionId}/resume`, { method: 'POST' })
  if (!response.ok) throw new Error(`HTTP ${response.status}`)
  return response.json()
}

export async function setSuggestionStatus(featureId: string, status: 'accepted' | 'dismissed'): Promise<void> {
  const response = await kernelFetch('/v1/learning/suggestions/set', { method: 'POST', body: JSON.stringify({ feature_id: featureId, status }) })
  if (!response.ok) throw new Error(`HTTP ${response.status}`)
}
