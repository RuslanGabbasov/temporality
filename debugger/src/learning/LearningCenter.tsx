import { useCallback, useEffect, useState } from 'react'
import { InlineLoading } from '@carbon/react'
import { useT } from '../i18n'
import {
  buildLearningProgram, fetchLearningLessonsMeta, fetchLearningState, generateLearningLesson,
  setSuggestionStatus, startLearningSession,
  type LearningCapabilityState, type LearningLessonDef, type LearningState,
} from './api'
import { notifyLearningSessionChanged } from './LessonRunner'

// The learning center (docs/ai-guided-onboarding-and-learning.md §12.4): one
// place where the user sees their program, the capability mastery (FACTUAL —
// checked against the configuration, §4.5), the supervisor's suggestions and
// the full lesson catalog. Starting a lesson hands over to the floating
// LessonRunner mounted at the App level — lessons lead across pages.

function MasteryBadge({ mastery }: { mastery?: string }) {
  const t = useT()
  const rank = mastery === 'verified' ? 3 : mastery === 'practiced' ? 2 : mastery === 'introduced' ? 1 : 0
  return (
    <span className={`learn-mastery learn-mastery-${rank}`} style={{ fontSize: '0.66rem', padding: '0.1rem 0.5rem', borderRadius: 999, whiteSpace: 'nowrap' }}>
      {t(`learning.mastery.${mastery || 'unknown'}`)}
    </span>
  )
}

export default function LearningCenter() {
  const t = useT()
  const [state, setState] = useState<LearningState | null>(null)
  const [lessons, setLessons] = useState<LearningLessonDef[]>([])
  const [whatsNew, setWhatsNew] = useState<{ available: boolean; version?: string } | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState('')

  const load = useCallback(async () => {
    try {
      const [st, meta] = await Promise.all([fetchLearningState(), fetchLearningLessonsMeta()])
      setState(st)
      setLessons(meta.lessons)
      setWhatsNew(meta.whats_new)
      setError('')
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : 'Request failed')
    } finally { setLoading(false) }
  }, [])

  useEffect(() => { void load() }, [load])
  useEffect(() => {
    const onChange = () => void load()
    window.addEventListener('learning:session-changed', onChange)
    return () => window.removeEventListener('learning:session-changed', onChange)
  }, [load])

  const lessonById = useCallback((id: string) => lessons.find((l) => l.id === id) ?? null, [lessons])
  const lessonByFeature = useCallback((featureId: string) => lessons.find((l) => l.feature_id === featureId) ?? null, [lessons])

  const startLesson = useCallback(async (lessonId: string, featureId?: string) => {
    setBusy(lessonId); setError('')
    try {
      await startLearningSession(lessonId)
      if (featureId) await setSuggestionStatus(featureId, 'accepted').catch(() => {})
      notifyLearningSessionChanged()
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : 'Request failed')
    } finally { setBusy('') }
  }, [])

  const build = useCallback(async () => {
    setBusy('build'); setError('')
    try {
      await buildLearningProgram()
      await load()
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : 'Request failed')
    } finally { setBusy('') }
  }, [load])

  const dismiss = useCallback(async (featureId: string) => {
    setBusy(`dismiss-${featureId}`)
    try {
      await setSuggestionStatus(featureId, 'dismissed')
      await load()
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : 'Request failed')
    } finally { setBusy('') }
  }, [load])

  // «Что нового»: the release's increment as a lesson — generated against
  // this user's delta, served from the store at the same version.
  const startWhatsNew = useCallback(async () => {
    setBusy('whats-new')
    try {
      const lesson = await generateLearningLesson('whats_new')
      await startLearningSession(lesson.id)
      notifyLearningSessionChanged()
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : 'Request failed')
    } finally { setBusy('') }
  }, [])

  if (loading) return <div style={{ padding: '2rem' }}><InlineLoading description={t('common.loading')} /></div>
  if (error && !state) {
    return (
      <div style={{ padding: '1.25rem' }}>
        <div role="alert" style={{ color: '#ffb4ab', fontSize: '0.82rem', marginBottom: '0.5rem' }}>{error}</div>
        <button className="toolbar-btn" onClick={() => { setLoading(true); void load() }}>{t('common.retry') ?? 'Retry'}</button>
      </div>
    )
  }

  const program = state?.program ?? null
  const capabilities = state?.capabilities ?? []
  const suggestions = (state?.suggestions ?? []).filter((s) => s.status === 'suggested')
  const programLessons = program?.items ?? []
  // The mentor's ONE next step: the first not-yet-done item of the program —
  // a course led by the supervisor, not a catalog to browse.
  const nextItem = programLessons.find((item) => item.state !== 'completed' && item.state !== 'skipped' && item.state !== 'not_applicable') ?? null
  const nextLesson = nextItem ? lessonById(nextItem.lesson_id) : null
  const doneCount = programLessons.filter((item) => item.state === 'completed').length

  return (
    <div data-learn="page.learning" style={{ padding: '1.25rem', maxWidth: '1080px', margin: '0 auto' }}>
      <h2 style={{ margin: '0 0 0.25rem' }}>{t('learning.title')}</h2>
      <p style={{ color: 'var(--tm-text-3)', margin: '0 0 1.25rem', fontSize: '0.85rem' }}>{t('learning.subtitle')}</p>
      {error && <div role="alert" style={{ color: '#ffb4ab', fontSize: '0.78rem', marginBottom: '0.75rem' }}>{error}</div>}

      {/* The release increment (§10.2 extended): one button, one generated
          lesson — for users who already know the old behaviour. */}
      {whatsNew?.available && (
        <section className="learn-section">
          <div className="learn-card" style={{ padding: '0.9rem 1rem', borderLeft: '3px solid var(--tm-teal)' }}>
            <div style={{ fontSize: '0.7rem', fontWeight: 700, color: 'var(--tm-teal)', textTransform: 'uppercase', letterSpacing: '0.05em', marginBottom: '0.3rem' }}>
              {t('learning.generate.whats_new', { version: whatsNew.version ?? '' })}
            </div>
            <div style={{ fontSize: '0.78rem', color: 'var(--tm-text-2)', lineHeight: 1.55 }}>
              {t('learning.generate.whats_new_desc')}
            </div>
            <div style={{ marginTop: '0.6rem' }}>
              <button className="toolbar-btn primary" disabled={busy === 'whats-new'} onClick={() => void startWhatsNew()}>
                {busy === 'whats-new' ? '…' : t('learning.generate.build')}
              </button>
            </div>
          </div>
        </section>
      )}

      {/* The mentor's next step (§4.1): ONE lesson with the supervisor's own
          «why», program progress under it — an individual course, not a
          catalog to browse. */}
      {!program && (
        <section className="learn-section">
          <div className="learn-card" style={{ padding: '0.9rem 1rem', borderLeft: '3px solid var(--tm-amber)' }}>
            <div style={{ fontSize: '0.7rem', fontWeight: 700, color: 'var(--tm-amber)', textTransform: 'uppercase', letterSpacing: '0.05em', marginBottom: '0.3rem' }}>
              {t('learning.next.title')}
            </div>
            <div style={{ fontSize: '0.8rem', color: 'var(--tm-text-2)', lineHeight: 1.55 }}>{t('learning.program.none')}</div>
            <div style={{ marginTop: '0.6rem' }}>
              <button className="toolbar-btn primary" disabled={busy === 'build'} onClick={() => void build()}>
                {busy === 'build' ? '…' : t('learning.next.build')}
              </button>
            </div>
          </div>
        </section>
      )}
      {program && nextItem && (
        <section className="learn-section" aria-label={t('learning.next.title') ?? ''}>
          <div className="learn-card" style={{ padding: '0.9rem 1rem', borderLeft: '3px solid var(--tm-amber)' }}>
            <div style={{ fontSize: '0.7rem', fontWeight: 700, color: 'var(--tm-amber)', textTransform: 'uppercase', letterSpacing: '0.05em', marginBottom: '0.3rem' }}>
              {t('learning.next.title')}
            </div>
            <div style={{ fontSize: '0.95rem', fontWeight: 600 }}>{nextLesson?.title ?? nextItem.lesson_id}</div>
            {(nextItem.why || nextLesson?.goal) && (
              <div style={{ fontSize: '0.78rem', color: 'var(--tm-text-2)', lineHeight: 1.55, marginTop: '0.3rem' }}>
                {nextItem.why || nextLesson?.goal}
              </div>
            )}
            <div style={{ marginTop: '0.7rem', display: 'flex', alignItems: 'center', gap: '0.6rem', flexWrap: 'wrap' }}>
              {nextLesson && (
                <button className="toolbar-btn primary" disabled={busy === nextItem.lesson_id} onClick={() => void startLesson(nextItem.lesson_id)}>
                  {busy === nextItem.lesson_id ? '…' : t('learning.next.continue')}
                </button>
              )}
              <span style={{ fontSize: '0.74rem', color: 'var(--tm-text-3)' }}>
                {t('learning.next.progress', { done: String(doneCount), total: String(programLessons.length) })}
              </span>
              {nextLesson?.duration_min ? (
                <span style={{ fontSize: '0.74rem', color: 'var(--tm-text-3)' }}>{t('learning.lesson.duration', { count: String(nextLesson.duration_min) })}</span>
              ) : null}
            </div>
          </div>
        </section>
      )}

      {/* Supervisor suggestions (P3): capped, dismissible — never a flood. */}
      {suggestions.length > 0 && (
        <section className="learn-section">
          <h3 style={{ margin: '0 0 0.6rem' }}>{t('learning.suggestions.title')}</h3>
          <div className="learn-list">
            {suggestions.map((sugg) => {
              const lesson = lessonByFeature(sugg.feature_id)
              const cap = capabilities.find((c) => c.id === sugg.feature_id)
              return (
                <div key={sugg.feature_id} className="learn-row">
                  <div style={{ minWidth: 0 }}>
                    <div style={{ fontSize: '0.82rem', fontWeight: 600 }}>{cap?.title ?? sugg.feature_id}</div>
                    {cap?.about && <div style={{ fontSize: '0.74rem', color: 'var(--tm-text-3)', marginTop: '0.15rem' }}>{cap.about}</div>}
                  </div>
                  <div style={{ display: 'flex', gap: '0.4rem', alignItems: 'center', flexShrink: 0 }}>
                    {lesson && (
                      <button className="toolbar-btn primary" disabled={busy === sugg.feature_id} onClick={() => void startLesson(lesson.id, sugg.feature_id)}>
                        {busy === sugg.feature_id ? '…' : t('learning.suggestion.accept')}
                      </button>
                    )}
                    <button className="toolbar-btn" disabled={busy === `dismiss-${sugg.feature_id}`} onClick={() => void dismiss(sugg.feature_id)}>
                      {t('learning.suggestion.dismiss')}
                    </button>
                  </div>
                </div>
              )
            })}
          </div>
        </section>
      )}

      {/* Capability mastery (§4.5/§10.1): what the kernel can SEE in the
          configuration, not what was clicked. */}
      <section className="learn-section">
        <h3 style={{ margin: '0 0 0.6rem' }}>{t('learning.capabilities.title')}</h3>
        <div className="learn-grid">
          {capabilities.map((cap: LearningCapabilityState) => (
            <div key={cap.id} className="learn-card" style={{ opacity: cap.applies ? 1 : 0.55 }}>
              <div style={{ display: 'flex', alignItems: 'center', gap: '0.4rem', marginBottom: '0.3rem' }}>
                <strong style={{ fontSize: '0.8rem' }}>{cap.title}</strong>
                <span style={{ marginLeft: 'auto' }}><MasteryBadge mastery={cap.mastery} /></span>
              </div>
              {cap.about && <div style={{ fontSize: '0.74rem', color: 'var(--tm-text-3)', lineHeight: 1.5 }}>{cap.about}</div>}
              {!cap.applies && <div style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)', marginTop: '0.3rem' }}>{t('learning.capabilities.not_applicable')}</div>}
            </div>
          ))}
        </div>
      </section>

      {/* The shipped catalog (§10.1): everything teachable, always visible. */}
      <section className="learn-section">
        <h3 style={{ margin: '0 0 0.6rem' }}>{t('learning.lessons.title')}</h3>
        <div className="learn-grid">
          {lessons.map((lesson) => {
            const inProgram = programLessons.some((item) => item.lesson_id === lesson.id && item.state === 'completed')
            return (
              <div key={lesson.id} className="learn-card">
                <div style={{ display: 'flex', alignItems: 'center', gap: '0.4rem', marginBottom: '0.3rem' }}>
                  <strong style={{ fontSize: '0.8rem' }}>{lesson.title}</strong>
                  {inProgram && <span style={{ color: 'var(--tm-teal)', fontSize: '0.72rem' }}>✓</span>}
                </div>
                {lesson.goal && <div style={{ fontSize: '0.74rem', color: 'var(--tm-text-3)', lineHeight: 1.5 }}>{lesson.goal}</div>}
                <div style={{ marginTop: '0.5rem', display: 'flex', gap: '0.4rem', alignItems: 'center' }}>
                  <button className="toolbar-btn primary" disabled={busy === lesson.id} onClick={() => void startLesson(lesson.id)}>
                    {busy === lesson.id ? '…' : t('learning.lesson.start')}
                  </button>
                  {!!lesson.duration_min && <span style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)' }}>{t('learning.lesson.duration', { count: String(lesson.duration_min) })}</span>}
                </div>
              </div>
            )
          })}
        </div>
      </section>
    </div>
  )
}
