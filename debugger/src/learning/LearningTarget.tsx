import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { registerLearningTarget, getLearningElement } from './registry'

export type GuidanceState = 'none' | 'available' | 'recommended' | 'new' | 'resumed' | 'completed'

/** The mentor icon: quiet by default, an accent only when there is genuinely
 *  new or recommended material (§6.1 — no visual noise). */
const INDICATOR_CLASS: Record<string, string> = {
  available: 'learn-indicator learn-indicator-available',
  recommended: 'learn-indicator learn-indicator-recommended',
  new: 'learn-indicator learn-indicator-new',
  resumed: 'learn-indicator learn-indicator-resumed',
  completed: 'learn-indicator learn-indicator-completed',
}

export interface LearningTargetProps {
  /** Stable semantic id (§6.3): organization.roles.reviewer, … */
  id: string
  /** Capability the element belongs to (§4.5 mastery is per capability). */
  featureId?: string
  /** Mentor icon next to the element; hover explains, click opens guidance. */
  showIndicator?: boolean
  /** Current guidance state (drives the indicator's look). */
  state?: GuidanceState
  /** Called when the user opens the guidance for this target. */
  onOpen?: (id: string) => void
  children: React.ReactNode
  style?: React.CSSProperties
}

/**
 * Declares a teachable UI element (§6.4): registers the wrapped DOM node in
 * the semantic registry under `id` and optionally shows the mentor icon.
 * The wrapper itself is layout-neutral (display: contents) — it never
 * changes the element's own styling or position.
 */
export function LearningTarget({ id, featureId, showIndicator, state = 'available', onOpen, children, style }: LearningTargetProps) {
  const ref = useRef<HTMLDivElement | null>(null)

  useLayoutEffect(() => {
    const el = ref.current
    if (!el) return
    return registerLearningTarget({ id, featureId, element: el })
  }, [id, featureId])

  return (
    <div ref={ref} style={{ display: 'contents' }}>
      {children}
      {showIndicator && state !== 'none' && (
        <button
          type="button"
          className={INDICATOR_CLASS[state] ?? 'learn-indicator'}
          title="?"
          onClick={(e) => { e.stopPropagation(); onOpen?.(id) }}
        >?</button>
      )}
    </div>
  )
}

/** Page-level mentor entry: an indicator near the page title that opens the
 *  section's guidance (§6.2 Page Level). */
export function PageLearningIndicator({ pageId, onOpen, state = 'available' }: { pageId: string; onOpen: (id: string) => void; state?: GuidanceState }) {
  const [present, setPresent] = useState(false)
  useEffect(() => {
    const el = getLearningElement(pageId)
    if (el) setPresent(true)
    const timer = window.setTimeout(() => setPresent(true), 1200)
    return () => window.clearTimeout(timer)
  }, [pageId])
  if (!present) return null
  return (
    <button
      type="button"
      className={INDICATOR_CLASS[state] ?? 'learn-indicator'}
      title="?"
      onClick={() => onOpen(`page.${pageId}`)}
    >?</button>
  )
}
