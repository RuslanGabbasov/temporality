// Semantic learning-target registry (docs/ai-guided-onboarding-and-learning.md
// §6.3): every teachable UI element carries a stable id —
// organization.roles.reviewer, agent.delegation.configuration — independent
// of layout, CSS classes, localized text and window size. Components either
// wrap themselves in <LearningTarget id="…"> or set data-learn="…" on a DOM
// node; the registry keeps id → element so the supervisor's commands
// (highlight / explain) can resolve a target wherever it currently lives.

export interface LearningTargetInfo {
  id: string
  /** Optional capability grouping (§4.5 mastery is per capability). */
  featureId?: string
  element: HTMLElement
}

const registered = new Map<string, LearningTargetInfo>()
const listeners = new Set<() => void>()

function notify() {
  for (const listener of listeners) listener()
}

export function registerLearningTarget(info: LearningTargetInfo): () => void {
  registered.set(info.id, info)
  notify()
  return () => {
    if (registered.get(info.id) === info) registered.delete(info.id)
    notify()
  }
}

export function getLearningElement(id: string): HTMLElement | null {
  const info = registered.get(id)
  if (info && info.element.isConnected) return info.element
  // Fallback: a declarative data-learn marker anywhere in the document —
  // pages that never import the registry still expose their elements.
  const found = document.querySelector<HTMLElement>(`[data-learn="${CSS.escape(id)}"]`)
  return found
}

export function subscribeLearningTargets(listener: () => void): () => void {
  listeners.add(listener)
  return () => listeners.delete(listener)
}

export function learningTargetIds(): string[] {
  return [...registered.keys()]
}

/** Scrolls a target into view; resolves false when the element is absent
 *  (the supervisor picks an alternative path per §6.3). */
export function scrollToLearningTarget(id: string): boolean {
  const el = getLearningElement(id)
  if (!el) return false
  el.scrollIntoView({ behavior: 'smooth', block: 'center' })
  return true
}
