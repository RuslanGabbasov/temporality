import { useCallback, useEffect, useRef, useState } from 'react'
import { useT } from '../i18n'
import { Spotlight, type SpotlightMode, type SpotlightPlan } from './Spotlight'
import {
  fetchCurrentSession, fetchLearningState, postSessionStep, resumeLearningSession, startLearningSession,
  type LearningLessonDef, type LearningSession, type LearningStep,
} from './api'

// The lesson runner (docs/ai-guided-onboarding-and-learning.md §8): a floating
// panel that leads the user through a lesson's steps ACROSS pages — so it
// mounts at the App level and survives SPA navigation. The step DEFINITION is
// declarative; the panel executes it: navigate opens the route, highlight
// spots the semantic target, user_action waits for the user's «Готово», and a
// verification step is checked by the kernel against the factual
// configuration (§7.7) — the panel only reports the click.

export const LEARNING_SESSION_EVENT = 'learning:session-changed'

/** Fired whenever a session starts/advances elsewhere, so the runner (and the
 *  learning center) reload without a poll loop. */
export function notifyLearningSessionChanged() {
  window.dispatchEvent(new Event(LEARNING_SESSION_EVENT))
}

function navigateTo(route: string) {
  if (window.location.pathname + window.location.search === route) return
  window.history.pushState(null, '', route)
  window.dispatchEvent(new PopStateEvent('popstate'))
}

function stepNeedsTarget(step: LearningStep | undefined): boolean {
  return !!step?.target_id && step.type !== 'navigate'
}

export default function LessonRunner() {
  const t = useT()
  const [session, setSession] = useState<LearningSession | null>(null)
  const [lesson, setLesson] = useState<LearningLessonDef | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [minimized, setMinimized] = useState(false)
  const [finishedMastery, setFinishedMastery] = useState('')
  const [spotlightOpen, setSpotlightOpen] = useState(false)
  /** The kernel's verdict on the last verification step: ok = advanced with
   *  a green note, failed = the step STAYS and the detail says why. */
  const [verification, setVerification] = useState<{ ok: boolean; detail?: string } | null>(null)
  /** The program's next pending lesson — the completion card chains into it
   *  instead of dead-ending («ничего не переключается между уроками»). */
  const [nextLesson, setNextLesson] = useState<{ id: string; title: string } | null>(null)
  const reloadRef = useRef<() => void>(() => {})

  const reload = useCallback(async () => {
    try {
      const data = await fetchCurrentSession()
      setSession(data.session)
      setLesson(data.lesson)
      if (!data.session) { setFinishedMastery(''); setSpotlightOpen(false) }
    } catch { /* the panel is optional: never nag about its own failures */ }
  }, [])
  reloadRef.current = reload

  useEffect(() => {
    void reload()
    const onChange = () => void reload()
    window.addEventListener(LEARNING_SESSION_EVENT, onChange)
    window.addEventListener('popstate', onChange)
    return () => {
      window.removeEventListener(LEARNING_SESSION_EVENT, onChange)
      window.removeEventListener('popstate', onChange)
    }
  }, [reload])

  const step: LearningStep | undefined = lesson?.steps?.[session?.step_index ?? 0]
  const stepIndex = session?.step_index ?? 0
  const total = lesson?.steps?.length ?? 0

  // Highlight-style steps open the spotlight once the (possibly just-navigated)
  // page has mounted; Esc closes it and the «Показать» button reopens.
  // Primitive deps only: fetches replace the session/step objects wholesale,
  // and identity-based deps would re-trigger (flicker) on every reload.
  const targetKey = stepNeedsTarget(step) ? step!.target_id! : ''
  const status = session?.status
  useEffect(() => {
    if (!session?.id || status !== 'active' || !targetKey) {
      setSpotlightOpen(false)
      return
    }
    setSpotlightOpen(false)
    const timer = window.setTimeout(() => setSpotlightOpen(true), 450)
    return () => window.clearTimeout(timer)
  }, [session?.id, status, session?.step_index, targetKey])

  const advance = useCallback(async (action: 'done' | 'next' | 'skip' | 'pause' | 'abort') => {
    if (!session || busy) return
    setBusy(true); setError(''); setVerification(null)
    try {
      const reply = await postSessionStep(session.id, action)
      setSession(reply.session)
      setLesson(reply.lesson)
      setSpotlightOpen(false)
      setVerification(reply.verification ?? null)
      if (reply.session.status === 'completed') {
        // Completion ≠ mastery (§4.5): show the FACTUAL verdict the kernel
        // recorded during the lesson's verification step — and chain into
        // the program's next pending lesson so the path continues.
        try {
          const state = await fetchLearningState()
          const evidence = state.capabilities.find((cap) => cap.id === reply.lesson?.feature_id)
          setFinishedMastery(evidence?.mastery ?? 'introduced')
          const pending = (state.program?.items ?? []).find((item) =>
            ['pending', 'scheduled', 'available', 'in_progress'].includes(item.state))
          if (pending && pending.lesson_id !== reply.lesson?.id) {
            setNextLesson({ id: pending.lesson_id, title: pending.lesson_id })
          } else {
            setNextLesson(null)
          }
        } catch { setFinishedMastery('introduced') }
      }
      notifyLearningSessionChanged()
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : 'Request failed')
    } finally { setBusy(false) }
  }, [session, busy])

  const startNext = useCallback(async () => {
    if (!nextLesson || busy) return
    setBusy(true); setError('')
    try {
      await startLearningSession(nextLesson.id)
      setNextLesson(null); setFinishedMastery(''); setVerification(null)
      notifyLearningSessionChanged()
      await reload()
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : 'Request failed')
    } finally { setBusy(false) }
  }, [nextLesson, busy, reload])

  const resume = useCallback(async () => {
    if (!session || busy) return
    setBusy(true); setError('')
    try {
      const reply = await resumeLearningSession(session.id)
      setSession(reply.session); setLesson(reply.lesson)
      notifyLearningSessionChanged()
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : 'Request failed')
    } finally { setBusy(false) }
  }, [session, busy])

  if (!session || !lesson) return null

  const waitingForUser = session.status === 'active' && (step?.type === 'user_action' || step?.type === 'configuration')
  const isVerification = session.status === 'active' && step?.type === 'verification'

  const spotlightPlan: SpotlightPlan | null = spotlightOpen && stepNeedsTarget(step) ? {
    targetId: step!.target_id!,
    explanation: [step!.text, step!.action_hint ? `${t('learning.runner.action_hint', { hint: step!.action_hint })}` : ''].filter(Boolean).join('\n'),
    highlight: step!.title,
    example: step!.example,
    mode: 'guided' as SpotlightMode,
  } : null

  const stepButton = () => {
    if (!step) return null
    switch (step.type) {
      case 'navigate':
        return (
          <button className="toolbar-btn" disabled={busy} onClick={() => { if (step.route) navigateTo(step.route); void advance('done') }}>
            {t('learning.runner.open_page')} →
          </button>
        )
      case 'user_action':
      case 'configuration':
      case 'verification':
        return (
          <span style={{ display: 'inline-flex', gap: '0.4rem' }}>
            <button className="toolbar-btn primary" disabled={busy} onClick={() => void advance('done')}>
              {t('learning.runner.done')}
            </button>
            {verification && !verification.ok && (
              <button className="toolbar-btn" disabled={busy} onClick={() => void advance('skip')}>
                {t('learning.runner.skip_step')}
              </button>
            )}
          </span>
        )
      case 'summary':
        return <button className="toolbar-btn primary" disabled={busy} onClick={() => void advance('done')}>{t('learning.runner.finish')}</button>
      default:
        return <button className="toolbar-btn primary" disabled={busy} onClick={() => void advance('next')}>{t('learning.runner.next')}</button>
    }
  }

  const header = (
    <div style={{ display: 'flex', alignItems: 'center', gap: '0.4rem', marginBottom: '0.35rem' }}>
      <span className="learn-indicator learn-indicator-available">?</span>
      <strong style={{ fontSize: '0.78rem' }}>{lesson.title}</strong>
      {/* Step dots: a novice sees where they are and how long the road is
          without reading numbers. */}
      {session.status === 'active' && total > 0 && (
        <span style={{ display: 'inline-flex', gap: '2px', alignItems: 'center', marginLeft: '0.3rem' }} title={t('learning.runner.step_of', { current: String(Math.min(stepIndex + 1, total)), total: String(total) })}>
          {Array.from({ length: total }, (_, i) => (
            <span key={i} style={{ width: 5, height: 5, borderRadius: '50%', display: 'inline-block', background: i === stepIndex ? 'var(--tm-amber)' : i < stepIndex ? 'var(--tm-teal)' : 'var(--tm-border-strong)' }} />
          ))}
        </span>
      )}
      <span style={{ marginLeft: 'auto', fontSize: '0.66rem', color: 'var(--tm-text-3)' }}>
        {session.status === 'active' && total > 0 && t('learning.runner.step_of', { current: String(Math.min(stepIndex + 1, total)), total: String(total) })}
      </span>
      <button className="toolbar-btn" style={{ padding: '0 0.35rem' }} title={t('learning.runner.pause')} disabled={busy} onClick={() => void advance('pause')}>⏸</button>
      <button className="toolbar-btn" style={{ padding: '0 0.35rem' }} title={t('learning.runner.abort')} disabled={busy} onClick={() => void advance('abort')}>✕</button>
    </div>
  )

  // Completed: the factual mastery verdict + the chain into the next lesson.
  if (session.status === 'completed') {
    return (
      <div className="lesson-runner" style={{ zIndex: 96 }}>
        {header}
        <div style={{ fontSize: '0.8rem', lineHeight: 1.6 }}>
          <div>✅ {t('learning.runner.completed')}</div>
          <div style={{ color: 'var(--tm-text-3)', marginTop: '0.3rem' }}>
            {t('learning.runner.mastery', { mastery: t(`learning.mastery.${finishedMastery}`) })}
          </div>
        </div>
        <div style={{ marginTop: '0.6rem', display: 'flex', gap: '0.4rem', flexWrap: 'wrap' }}>
          {nextLesson && (
            <button className="toolbar-btn primary" disabled={busy} onClick={() => void startNext()}>
              {t('learning.runner.next_lesson')} →
            </button>
          )}
          <button className="toolbar-btn" onClick={() => { setSession(null); setLesson(null); setFinishedMastery(''); setNextLesson(null); navigateTo('/learning') }}>
            {t('learning.runner.back_to_center')}
          </button>
        </div>
      </div>
    )
  }

  // Paused: one click back into the lesson (§18 E).
  if (session.status === 'paused') {
    return (
      <div className="lesson-runner" style={{ zIndex: 96 }}>
        {header}
        <div style={{ fontSize: '0.8rem', color: 'var(--tm-text-3)' }}>{t('learning.runner.paused')}</div>
        <div style={{ marginTop: '0.6rem', display: 'flex', gap: '0.4rem', justifyContent: 'flex-end' }}>
          <button className="toolbar-btn primary" disabled={busy} onClick={() => void resume()}>{t('learning.runner.resume')}</button>
          <button className="toolbar-btn" disabled={busy} onClick={() => void advance('abort')}>{t('learning.runner.abort')}</button>
        </div>
      </div>
    )
  }

  if (minimized) {
    return (
      <button className="lesson-runner lesson-runner-min" onClick={() => setMinimized(false)}>
        <span className="learn-indicator learn-indicator-available">?</span>
        {lesson.title}
        {total > 0 && <span style={{ color: 'var(--tm-text-3)', fontSize: '0.66rem' }}>{t('learning.runner.step_of', { current: String(stepIndex + 1), total: String(total) })}</span>}
      </button>
    )
  }

  return (
    <>
      {spotlightPlan && (
        <Spotlight plan={spotlightPlan} onClose={() => setSpotlightOpen(false)} />
      )}
      <div className="lesson-runner" style={{ zIndex: 96 }}>
        {header}
        {step ? (
          <div style={{ fontSize: '0.8rem', lineHeight: 1.6 }}>
            <div style={{ fontWeight: 600, marginBottom: '0.25rem' }}>{step.title}</div>
            {step.text && <div style={{ whiteSpace: 'pre-wrap' }}>{step.text}</div>}
            {step.type === 'example' && step.example && (
              <pre style={{ marginTop: '0.45rem', padding: '0.45rem 0.5rem', background: 'var(--tm-raised)', borderRadius: 6, fontSize: '0.7rem', overflowX: 'auto', whiteSpace: 'pre-wrap' }}>{step.example}</pre>
            )}
            {waitingForUser && step.action_hint && (
              <div style={{ marginTop: '0.4rem', color: 'var(--tm-amber)', fontSize: '0.72rem' }}>
                {t('learning.runner.waiting_hint')} {t('learning.runner.action_hint', { hint: step.action_hint })}
              </div>
            )}
            {isVerification && (
              <div style={{ marginTop: '0.4rem', color: 'var(--tm-text-3)', fontSize: '0.72rem' }}>{t('learning.runner.verify_hint')}</div>
            )}
            {verification && verification.ok && (
              <div style={{ marginTop: '0.4rem', color: 'var(--tm-teal)', fontSize: '0.75rem' }}>
                ✓ {t('learning.runner.check_passed')}{verification.detail ? `: ${verification.detail}` : ''}
              </div>
            )}
            {verification && !verification.ok && (
              <div style={{ marginTop: '0.4rem', padding: '0.4rem 0.55rem', border: '1px solid var(--tm-amber)', borderRadius: 6, background: 'rgba(230,184,92,0.08)' }}>
                <div style={{ color: 'var(--tm-amber)', fontSize: '0.75rem', fontWeight: 600 }}>{t('learning.runner.check_failed')}</div>
                {verification.detail && <div style={{ color: 'var(--tm-text-2)', fontSize: '0.74rem', marginTop: '0.15rem' }}>{verification.detail}</div>}
              </div>
            )}
          </div>
        ) : null}
        <div style={{ marginTop: '0.6rem', display: 'flex', gap: '0.4rem', alignItems: 'center', flexWrap: 'wrap' }}>
          {stepNeedsTarget(step) && !spotlightOpen && (
            <button className="toolbar-btn" onClick={() => setSpotlightOpen(true)}>{t('learning.runner.show')}</button>
          )}
          {stepButton()}
          <button className="toolbar-btn" style={{ marginLeft: 'auto', padding: '0 0.35rem' }} title={t('learning.runner.minimize')} onClick={() => setMinimized(true)}>—</button>
        </div>
        {error && <div style={{ marginTop: '0.4rem', color: '#ffb4ab', fontSize: '0.72rem' }}>{error}</div>}
      </div>
    </>
  )
}
