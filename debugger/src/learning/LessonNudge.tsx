import { useCallback, useEffect, useRef, useState } from 'react'
import { useT } from '../i18n'
import {
  fetchCurrentSession, fetchLearningLessonsMeta, fetchLearningState, generateLearningLesson, startLearningSession,
  type LearningLessonDef, type LearningProgramItem,
} from './api'
import { notifyLearningSessionChanged } from './LessonRunner'

// The contextual lesson nudge (docs/ai-guided-onboarding-and-learning.md
// §12.5): while you are ON the page a lesson teaches, the mentor taps your
// shoulder — one small floating card, once per lesson. «Зашел первый раз в
// настройки агентов — плывет подсказка: здесь можно пройти урок». Not a
// popup storm: one card at a time, a gentle entrance animation, dismiss is
// remembered (localStorage per lesson id), and an active lesson session
// silences every nudge.
//
// Pages without a shipped lesson are not dead ends: when the page has a
// supervisor highlight, the card offers to GENERATE a lesson for it against
// this user's progress delta (§10.2 extended).

const DISMISS_KEY = 'learn-nudge-dismissed:'
const GEN_DISMISS_KEY = 'learn-nudge-gen-dismissed:'
const LOAD_DELAY_MS = 2500   // let the page settle before the card floats in
const SESSION_CACHE_TTL = 5 * 60 * 1000

let cache: { lessons: LearningLessonDef[]; done: Set<string>; pages: string[]; at: number } | null = null

function dismissed(lessonId: string): boolean {
  try { return localStorage.getItem(DISMISS_KEY + lessonId) === '1' } catch { return false }
}

function markDismissed(lessonId: string) {
  try { localStorage.setItem(DISMISS_KEY + lessonId, '1') } catch { /* private mode */ }
}

function genDismissed(route: string): boolean {
  try { return localStorage.getItem(GEN_DISMISS_KEY + route) === '1' } catch { return false }
}

function markGenDismissed(route: string) {
  try { localStorage.setItem(GEN_DISMISS_KEY + route, '1') } catch { /* private mode */ }
}

// A failed auto-generation is remembered for the TAB lifetime: when the
// model is down, every page visit must not hammer /lessons/generate — the
// card with the manual retry still shows once per visit.
const ATTEMPT_KEY = 'learn-nudge-gen-attempted:'

function genAttempted(route: string): boolean {
  try { return sessionStorage.getItem(ATTEMPT_KEY + route) === '1' } catch { return false }
}

function markGenAttempted(route: string) {
  try { sessionStorage.setItem(ATTEMPT_KEY + route, '1') } catch { /* private mode */ }
}

interface Props {
  /** The active page id (App's page state — matches lesson.page values). */
  page: string
}

export default function LessonNudge({ page }: Props) {
  const t = useT()
  const [lesson, setLesson] = useState<LearningLessonDef | null>(null)
  const [genRoute, setGenRoute] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [visible, setVisible] = useState(false)
  const [expanded, setExpanded] = useState(false)
  const [startError, setStartError] = useState('')
  const timerRef = useRef<number | null>(null)

  const pick = useCallback(async (currentPage: string) => {
    if (!currentPage || currentPage === 'learning') return
    try {
      if (!cache || Date.now() - cache.at > SESSION_CACHE_TTL) {
        const [meta, state] = await Promise.all([fetchLearningLessonsMeta(), fetchLearningState()])
        const done = new Set<string>((state.program?.items ?? [])
          .filter((item: LearningProgramItem) => item.state === 'completed' || item.state === 'skipped' || item.state === 'not_applicable')
          .map((item) => item.lesson_id))
        cache = { lessons: meta.lessons, done, pages: meta.pages, at: Date.now() }
      }
      // One lesson per page: first the program's pending item (the mentor's
      // order matters), then any other catalog lesson anchored to this page.
      // Catalog lessons anchor by ROUTE ("/agent-config"), the app's page is
      // an id ("agent-config") — compare route-style.
      const route = '/' + currentPage
      const candidates = cache.lessons.filter((l) => l.page === route || l.page === currentPage)
      const next = candidates.find((l) => !dismissed(l.id) && !cache!.done.has(l.id))
      if (next) {
        setLesson(next)
        return
      }
      // No lesson for this screen AT ALL (a dismissed or completed lesson
      // is a decision, not an absence): when the supervisor has material for
      // the page, write the lesson NOW — as the user explores, against their
      // delta (§10.2 extended). Same-version content is served from the
      // store, so this costs one model call per page per release.
      if (candidates.length === 0 && cache.pages.includes(route) && !genDismissed(route) && !genAttempted(route)) {
        markGenAttempted(route)
        try {
          const generated = await generateLearningLesson('page', route)
          cache.lessons.push(generated)
          setLesson(generated)
        } catch {
          // The model did not answer — offer the card with a manual retry
          // instead of silently never teaching this page.
          setGenRoute(route)
        }
      } else if (candidates.length === 0 && cache.pages.includes(route) && !genDismissed(route)) {
        setGenRoute(route) // auto-generation was attempted this tab: offer retry
      } else {
        setGenRoute(null)
      }
    } catch { /* learning is optional: the page must not break without it */ }
  }, [])

  useEffect(() => {
    setLesson(null)
    setGenRoute(null)
    setVisible(false)
    setExpanded(false)
    setStartError('')
    if (timerRef.current) window.clearTimeout(timerRef.current)
    // The session runner owns attention while ANY lesson session is live —
    // the old program.status === 'in_progress' check never matched (the
    // program status vocabulary is active|paused|completed|cancelled), so
    // the nudge used to float next to the runner it duplicated.
    void fetchCurrentSession().then(({ session }) => {
      if (session) return
      timerRef.current = window.setTimeout(() => void pick(page), LOAD_DELAY_MS)
    }).catch(() => {
      timerRef.current = window.setTimeout(() => void pick(page), LOAD_DELAY_MS)
    })
    return () => { if (timerRef.current) window.clearTimeout(timerRef.current) }
  }, [page, pick])

  const start = useCallback(async () => {
    if (!lesson || busy) return
    setBusy(true); setStartError('')
    try {
      await startLearningSession(lesson.id)
      if (cache) cache.done.add(lesson.id)
      notifyLearningSessionChanged()
      setLesson(null)
      setVisible(false)
    } catch (failure) {
      // A silent catch made the button look dead — say what happened.
      setStartError(failure instanceof Error ? failure.message : 'Request failed')
    } finally { setBusy(false) }
  }, [lesson, busy])

  const generate = useCallback(async () => {
    if (!genRoute || busy) return
    setBusy(true); setStartError('')
    try {
      const generated = await generateLearningLesson('page', genRoute)
      if (cache) cache.lessons.push(generated)
      await startLearningSession(generated.id)
      notifyLearningSessionChanged()
      setGenRoute(null); setLesson(null); setVisible(false)
    } catch (failure) {
      setStartError(failure instanceof Error ? failure.message : 'Request failed')
    } finally { setBusy(false) }
  }, [genRoute, busy])

  const dismissNow = useCallback(() => {
    if (lesson) markDismissed(lesson.id)
    if (genRoute) markGenDismissed(genRoute)
    setLesson(null)
    setGenRoute(null)
    setVisible(false)
  }, [lesson, genRoute])

  // Float in after the delay; unmount otherwise.
  useEffect(() => { if (lesson || genRoute) setVisible(true) }, [lesson, genRoute])

  // Collapsed: the round academic-cap button. Expanded: the small card with
  // the details — ✕ collapses back to the button, only «Не сейчас» removes
  // the nudge for good. Without a lesson (generation offered) the same FAB
  // opens the «Собрать урок» card — one interaction pattern either way.
  if ((!lesson && !genRoute) || !visible) return null

  if (!expanded) {
    const fabTitle = lesson?.title ?? t('learning.generate.for_page') ?? 'Урок для этой страницы'
    return (
      <button
        className="learn-nudge-fab"
        title={fabTitle}
        aria-label={`${t('learning.nudge.aria') ?? 'Lesson'}: ${fabTitle}`}
        onClick={() => setExpanded(true)}
      >
        <svg viewBox="0 0 24 24" width="22" height="22" fill="currentColor" aria-hidden="true">
          <path d="M12 3 1 8l11 5 9-4.09V15h2V8L12 3z" />
          <path d="M5 10.18V14c0 1.66 3.13 3 7 3s7-1.34 7-3v-3.82l-7 3.18-7-3.18z" />
        </svg>
      </button>
    )
  }

  if (!lesson && genRoute) {
    return (
      <div className="learn-nudge" role="complementary" aria-label={t('learning.nudge.aria') ?? 'Lesson suggestion'}>
        <button className="learn-nudge-close" title={t('learning.nudge.collapse') ?? 'Свернуть'} onClick={() => setExpanded(false)}>✕</button>
        <div className="learn-nudge-mentor">
          <span className="learn-indicator learn-indicator-available">?</span>
          <span>{t('learning.nudge.mentor') ?? 'Наставник'}</span>
        </div>
        <div className="learn-nudge-title">{t('learning.generate.for_page') ?? 'Урок для этой страницы'}</div>
        <div className="learn-nudge-goal">{t('learning.generate.for_page_desc') ?? 'Наставник напишет урок по этой странице — под ваш прогресс и конфигурацию.'}</div>
        <div className="learn-nudge-actions">
          <button className="toolbar-btn primary" disabled={busy} onClick={() => void generate()}>
            {busy ? '…' : t('learning.generate.build') ?? 'Собрать урок'}
          </button>
          <button className="toolbar-btn" onClick={dismissNow}>
            {t('learning.nudge.dismiss') ?? 'Не сейчас'}
          </button>
        </div>
        {startError && (
          <div style={{ marginTop: '0.4rem', fontSize: '0.72rem', color: '#ffb4ab' }} role="alert">{startError}</div>
        )}
      </div>
    )
  }

  if (!lesson) return null

  return (
    <div
      className="learn-nudge"
      role="complementary"
      aria-label={t('learning.nudge.aria') ?? 'Lesson suggestion'}
    >
      <button className="learn-nudge-close" title={t('learning.nudge.collapse') ?? 'Свернуть'} onClick={() => setExpanded(false)}>✕</button>
      <div className="learn-nudge-mentor">
        <span className="learn-indicator learn-indicator-available">?</span>
        <span>{t('learning.nudge.mentor') ?? 'Наставник'}</span>
      </div>
      <div className="learn-nudge-title">{lesson.title}</div>
      {(lesson.goal || lesson.description) && (
        <div className="learn-nudge-goal">{lesson.goal || lesson.description}</div>
      )}
      <div className="learn-nudge-actions">
        <button className="toolbar-btn primary" disabled={busy} onClick={() => void start()}>
          {busy ? '…' : t('learning.nudge.start') ?? 'Пройти урок'}
        </button>
        <button className="toolbar-btn" onClick={dismissNow}>
          {t('learning.nudge.dismiss') ?? 'Не сейчас'}
        </button>
        {!!lesson.duration_min && (
          <span style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)' }}>
            {t('learning.lesson.duration', { count: String(lesson.duration_min) })}
          </span>
        )}
      </div>
      {startError && (
        <div style={{ marginTop: '0.4rem', fontSize: '0.72rem', color: '#ffb4ab' }} role="alert">{startError}</div>
      )}
    </div>
  )
}
