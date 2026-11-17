import { useCallback, useEffect, useLayoutEffect, useState } from 'react'
import { getLearningElement, scrollToLearningTarget } from './registry'

export type SpotlightMode = 'informational' | 'guided' | 'focused'

export interface SpotlightPlan {
  /** Semantic target id; the overlay resolves it to the live element. */
  targetId: string
  /** Main explanation text (markdown-lite: paragraphs and line breaks). */
  explanation: string
  /** One-line callout over the element. */
  highlight?: string
  /** Concrete example for this organization, shown in a code pane. */
  example?: string
  /** In-app route to open before showing the target. */
  navigateRoute?: string
  mode: SpotlightMode | string
  docsLink?: string
  /** Follow-up question asked from the explanation card. */
  question?: string
}

interface Rect { top: number; left: number; width: number; height: number }

const PAD = 8

function targetRect(id: string): Rect | null {
  const el = getLearningElement(id)
  if (!el) return null
  const r = el.getBoundingClientRect()
  if (r.width === 0 && r.height === 0) return null
  return { top: r.top, left: r.left, width: r.width, height: r.height }
}

/**
 * Spotlight overlay (§6.5): dims the page, highlights the target element and
 * places the explanation card next to it. Repositions on scroll/resize,
 * follows SPA navigation, handles a missing target by showing the
 * explanation as a centered card (§6.3 — the supervisor path adapts).
 * informational mode never blocks interaction outside the card.
 */
export function Spotlight({ plan, onClose, onQuestion }: { plan: SpotlightPlan; onClose: () => void; onQuestion?: (targetId: string, question: string) => void }) {
  const [rect, setRect] = useState<Rect | null>(null)
  const [resolved, setResolved] = useState(false)
  const [askOpen, setAskOpen] = useState(false)
  const [ask, setAsk] = useState('')

  const reposition = useCallback(() => {
    const found = scrollToLearningTarget(plan.targetId)
    setResolved(found)
    setRect(targetRect(plan.targetId))
  }, [plan.targetId])

  // Navigate first when the plan asks for it: the target may live on another
  // page — give the SPA a beat to mount, then resolve.
  useLayoutEffect(() => {
    if (plan.navigateRoute && window.location.pathname + window.location.search !== plan.navigateRoute) {
      window.history.pushState(null, '', plan.navigateRoute)
      window.dispatchEvent(new PopStateEvent('popstate'))
      const timer = window.setTimeout(reposition, 350)
      return () => window.clearTimeout(timer)
    }
    reposition()
  }, [plan.navigateRoute, reposition])

  useEffect(() => {
    const onReposition = () => setRect(targetRect(plan.targetId))
    window.addEventListener('resize', onReposition)
    window.addEventListener('scroll', onReposition, { passive: true })
    const mo = new MutationObserver(() => onReposition())
    mo.observe(document.body, { childList: true, subtree: true })
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    window.addEventListener('keydown', onKey)
    return () => {
      window.removeEventListener('resize', onReposition)
      window.removeEventListener('scroll', onReposition)
      mo.disconnect()
      window.removeEventListener('keydown', onKey)
    }
  }, [plan.targetId, onClose])

  const guided = plan.mode === 'guided'
  const focused = plan.mode === 'focused'
  const dim = guided || focused

  return (
    <>
      {/* The dimming layer: only in guided/focused; informational keeps the
          page interactive (§6.5). */}
      {rect && dim && (
        <div
          style={{ position: 'fixed', inset: 0, background: 'rgba(10,18,16,.62)', zIndex: 90 }}
          onClick={() => { if (!guided) onClose() }}
        />
      )}
      {rect ? (
        <div
          style={{
            position: 'fixed', top: rect.top - PAD, left: rect.left - PAD,
            width: rect.width + PAD * 2, height: rect.height + PAD * 2,
            border: '2px solid var(--tm-amber)', borderRadius: 8,
            boxShadow: '0 0 0 4px rgba(224,155,90,.18), 0 0 40px rgba(224,155,90,.25)',
            zIndex: 91, pointerEvents: 'none', transition: 'all .25s ease',
          }}
        />
      ) : null}

      <div
        style={{
          position: 'fixed', zIndex: 92, maxWidth: 420, width: 'min(420px, calc(100vw - 32px))',
          background: 'var(--tm-surface)', border: '1px solid var(--tm-border-strong)', borderRadius: 10,
          boxShadow: '0 18px 60px rgba(0,0,0,.4)', padding: '0.9rem 1rem',
          // Position: right of the target when there is room, below otherwise;
          // a missing target centers the card.
          ...(rect
            ? (rect.left + rect.width + 440 < window.innerWidth
                ? { top: Math.max(64, rect.top - 8), left: rect.left + rect.width + 18 }
                : (rect.top + rect.height + 300 < window.innerHeight
                    ? { top: rect.top + rect.height + 16, left: Math.max(16, rect.left) }
                    : { top: Math.max(64, window.innerHeight / 2 - 220), left: Math.max(16, Math.min(rect.left, window.innerWidth - 452)) }))
            : { top: '18vh', left: '50%', transform: 'translateX(-50%)' }),
        }}
      >
        {plan.highlight && (
          <div style={{ fontSize: '0.72rem', fontWeight: 700, color: 'var(--tm-amber)', marginBottom: '0.4rem' }}>{plan.highlight}</div>
        )}
        <div style={{ display: 'flex', alignItems: 'center', gap: '0.4rem', marginBottom: '0.5rem' }}>
          <span className="learn-indicator learn-indicator-available">?</span>
          <strong style={{ fontSize: '0.78rem' }}>
            {plan.mode === 'guided' ? 'Наставник ведёт' : plan.mode === 'focused' ? 'Внимание' : 'Наставник'}
          </strong>
          <button
            style={{ marginLeft: 'auto', background: 'none', border: 'none', color: 'var(--tm-text-3)', cursor: 'pointer', fontSize: '0.9rem' }}
            onClick={onClose}
            title="Esc"
          >✕</button>
        </div>
        <div style={{ fontSize: '0.8rem', lineHeight: 1.7, color: 'var(--tm-text)', whiteSpace: 'pre-wrap' }}>{plan.explanation}</div>
        {plan.example && (
          <pre style={{ marginTop: '0.6rem', padding: '0.5rem 0.6rem', background: 'var(--tm-raised)', borderRadius: 6, fontSize: '0.72rem', overflowX: 'auto', whiteSpace: 'pre-wrap' }}>{plan.example}</pre>
        )}
        <div style={{ marginTop: '0.7rem', display: 'flex', gap: '0.4rem', alignItems: 'center', flexWrap: 'wrap' }}>
          <button className="toolbar-btn" onClick={() => setAskOpen((v) => !v)}>?</button>
          {plan.docsLink && (
            <a className="gallery-doc" href={plan.docsLink} target="_blank" rel="noreferrer">Гайд ↗</a>
          )}
          <span style={{ marginLeft: 'auto', fontSize: '0.66rem', color: 'var(--tm-text-3)' }}>
            {resolved ? '' : 'элемент не найден — объяснение по контексту'}
          </span>
        </div>
        {askOpen && onQuestion && (
          <div style={{ marginTop: '0.5rem', display: 'flex', gap: '0.4rem' }}>
            <input
              autoFocus
              className="toolbar-btn"
              style={{ flex: 1, color: 'var(--tm-text)', textAlign: 'left' }}
              placeholder="Спросите наставника…"
              value={ask}
              onChange={(e) => setAsk(e.target.value)}
              onKeyDown={(e) => { if (e.key === 'Enter' && ask.trim()) { onQuestion(plan.targetId, ask.trim()); setAsk(''); setAskOpen(false) } }}
            />
            <button className="toolbar-btn" onClick={() => { if (ask.trim()) { onQuestion(plan.targetId, ask.trim()); setAsk(''); setAskOpen(false) } }}>→</button>
          </div>
        )}
      </div>
    </>
  )
}
