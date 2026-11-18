import { useCallback, useEffect, useRef, useState } from 'react'
import { useT } from '../i18n'
import {
  fetchLearningLessons, fetchLearningState, startLearningSession,
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

const DISMISS_KEY = 'learn-nudge-dismissed:'
const LOAD_DELAY_MS = 2500   // let the page settle before the card floats in
const SESSION_CACHE_TTL = 5 * 60 * 1000

let cache: { lessons: LearningLessonDef[]; done: Set<string>; at: number } | null = null

function dismissed(lessonId: string): boolean {
  try { return localStorage.getItem(DISMISS_KEY + lessonId) === '1' } catch { return false }
}

function markDismissed(lessonId: string) {
  try { localStorage.setItem(DISMISS_KEY + lessonId, '1') } catch { /* private mode */ }
}

interface Props {
  /** The active page id (App's page state — matches lesson.page values). */
  page: string
}

export default function LessonNudge({ page }: Props) {
  const t = useT()
  const [lesson, setLesson] = useState<LearningLessonDef | null>(null)
  const [busy, setBusy] = useState(false)
  const [visible, setVisible] = useState(false)
  const timerRef = useRef<number | null>(null)

  const pick = useCallback(async (currentPage: string) => {
    if (!currentPage || currentPage === 'learning') return
    try {
      if (!cache || Date.now() - cache.at > SESSION_CACHE_TTL) {
        const [lessons, state] = await Promise.all([fetchLearningLessons(), fetchLearningState()])
        const done = new Set<string>((state.program?.items ?? [])
          .filter((item: LearningProgramItem) => item.state === 'completed' || item.state === 'skipped' || item.state === 'not_applicable')
          .map((item) => item.lesson_id))
        cache = { lessons, done, at: Date.now() }
      }
      // One lesson per page: first the program's pending item (the mentor's
      // order matters), then any other catalog lesson anchored to this page.
      // Catalog lessons anchor by ROUTE ("/agent-config"), the app's page is
      // an id ("agent-config") — compare route-style.
      const route = '/' + currentPage
      const candidates = cache.lessons.filter((l) => l.page === route || l.page === currentPage)
      const next = candidates.find((l) => !dismissed(l.id) && !cache!.done.has(l.id))
      setLesson(next ?? null)
    } catch { /* learning is optional: the page must not break without it */ }
  }, [])

  useEffect(() => {
    setLesson(null)
    setVisible(false)
    if (timerRef.current) window.clearTimeout(timerRef.current)
    // The session runner owns attention while a lesson is active.
    void fetchLearningState().then((state) => {
      const active = state.program && state.program.status === 'in_progress'
      if (active) return
      timerRef.current = window.setTimeout(() => void pick(page), LOAD_DELAY_MS)
    }).catch(() => {
      timerRef.current = window.setTimeout(() => void pick(page), LOAD_DELAY_MS)
    })
    return () => { if (timerRef.current) window.clearTimeout(timerRef.current) }
  }, [page, pick])

  const start = useCallback(async () => {
    if (!lesson || busy) return
    setBusy(true)
    try {
      await startLearningSession(lesson.id)
      if (cache) cache.done.add(lesson.id)
      notifyLearningSessionChanged()
      setLesson(null)
      setVisible(false)
    } catch { /* the runner reports session errors on its own */ } finally { setBusy(false) }
  }, [lesson, busy])

  const dismissNow = useCallback(() => {
    if (lesson) markDismissed(lesson.id)
    setLesson(null)
    setVisible(false)
  }, [lesson])

  // Float in after the delay; unmount otherwise.
  useEffect(() => { if (lesson) setVisible(true) }, [lesson])

  return lesson && visible ? (
    <div
      className="learn-nudge"
      role="complementary"
      aria-label={t('learning.nudge.aria') ?? 'Lesson suggestion'}
    >
      <button className="learn-nudge-close" title={t('learning.nudge.dismiss') ?? 'Не сейчас'} onClick={dismissNow}>✕</button>
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
        {!!lesson.duration_min && (
          <span style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)' }}>
            {t('learning.lesson.duration', { count: String(lesson.duration_min) })}
          </span>
        )}
      </div>
    </div>
  ) : null
}
