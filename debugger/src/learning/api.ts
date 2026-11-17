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
