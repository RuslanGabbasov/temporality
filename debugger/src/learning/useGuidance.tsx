import { useCallback, useState } from 'react'
import { explainLearningTarget, type GuidancePlan } from './api'
import { Spotlight, type SpotlightMode } from './Spotlight'

/** One guidance interaction: open a target → the supervisor explains it in
 *  the context of the current organization → the UI shows the spotlight. A
 *  follow-up question re-asks the supervisor with the same target (§12.3),
 *  and the whole thing closes with Esc or ✕ (§12.2 «остановить обучение»). */
export function useGuidance(project: string, page: string) {
  const [plan, setPlan] = useState<GuidancePlan & { targetId: string } | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')

  const open = useCallback(async (targetId: string, question?: string) => {
    setLoading(true); setError('')
    try {
      const reply = await explainLearningTarget(project, targetId, page, question)
      setPlan({ ...reply, targetId, mode: (reply.mode as GuidancePlan['mode']) ?? 'informational' })
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : 'Request failed')
    } finally { setLoading(false) }
  }, [project, page])

  const close = useCallback(() => setPlan(null), [])

  const ask = useCallback(async (targetId: string, question: string) => {
    await open(targetId, question)
  }, [open])

  return {
    open, close, ask,
    plan, loading, error,
    spotlight: plan ? (
      <Spotlight plan={{ ...plan, targetId: plan.targetId, mode: (plan.mode as SpotlightMode) ?? 'informational' }} onClose={close} onQuestion={(id, q) => void ask(id, q)} />
    ) : null,
  }
}
