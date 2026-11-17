// Lightweight client for pages that need the learning explain call without
// the whole spotlight component (the plan type lives with the Spotlight).
import { authHeaders } from '../api'
import { KERNEL_API } from '../kernelApi'
import type { SpotlightPlan } from './Spotlight'

/** Asks the supervisor to explain one learning target in the context of the
 *  current organization (docs/ai-guided-onboarding-and-learning.md §6, P0).
 *  Errors propagate — the caller decides how to degrade. */
export async function explainLearningTarget(project: string, targetId: string, page: string, question?: string): Promise<SpotlightPlan> {
  const response = await fetch(`${KERNEL_API}/v1/learning/explain`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', ...authHeaders() },
    body: JSON.stringify({ project, target_id: targetId, page, question: question ?? '' }),
  })
  if (!response.ok) throw new Error(`HTTP ${response.status}`)
  const data = await response.json()
  return { ...data, targetId } as SpotlightPlan
}
