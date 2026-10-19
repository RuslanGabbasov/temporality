import type { ObservationEvent } from './observationApi'

// Per-run human factors (docs/supervisor-model.md §7): the client-side port of
// the journal's estimation (observation/humanfactors.go) over the run events
// the trace page already loaded — no second journal scan. Defaults match the
// workspace coefficients (45s/decision, 20s answer base, 180 WPM reading).
export interface RunHumanFactors {
  approvalDecisions: number
  askHumanAnswered: number
  hwtSeconds: number
  hwtCount: number
  habSeconds: number
}

const APPROVAL_SECONDS = 45
const ANSWER_BASE_SECONDS = 20
const READING_WPM = 180

export function humanFactorsOf(events: ObservationEvent[]): RunHumanFactors {
  const factors: RunHumanFactors = { approvalDecisions: 0, askHumanAnswered: 0, hwtSeconds: 0, hwtCount: 0, habSeconds: 0 }
  const ordered = [...events].sort((a, b) => a.occurred_at.localeCompare(b.occurred_at) || a.event_id.localeCompare(b.event_id))
  const pending = new Map<string, string>() // key → occurred_at
  const key = (event: ObservationEvent) => `${event.context?.run ?? ''}\x00${event.data?.operation_id ?? ''}\x00${event.data?.arguments_hash ?? ''}`
  // Reading volume: the final answer read in full, the rest of the generated
  // text as a 10% skim, prompts not at all — a human never reads the context.
  let completionTokens = 0
  let answerWords = 0
  let waitMS = 0
  for (const event of ordered) {
    switch (event.type) {
      case 'approval.requested':
      case 'human.requested':
        pending.set(key(event), event.occurred_at)
        break
      case 'approval.granted':
      case 'approval.rejected': {
        factors.approvalDecisions++
        const raised = pending.get(key(event))
        if (raised) {
          waitMS += Math.max(0, new Date(event.occurred_at).getTime() - new Date(raised).getTime())
          factors.hwtCount++
          pending.delete(key(event))
        }
        break
      }
      case 'approval.auto_granted':
        break
      case 'approval.timed_out':
        pending.delete(key(event))
        break
      case 'human.answered': {
        factors.approvalDecisions++
        factors.askHumanAnswered++
        const raised = pending.get(key(event))
        if (raised) {
          waitMS += Math.max(0, new Date(event.occurred_at).getTime() - new Date(raised).getTime())
          factors.hwtCount++
          pending.delete(key(event))
        }
        factors.habSeconds += ANSWER_BASE_SECONDS
        const response = String(event.data?.response ?? '')
        if (response) factors.habSeconds += Math.floor(response.length / 5 * 60 / READING_WPM)
        break
      }
      case 'human.cancelled':
        factors.approvalDecisions++
        pending.delete(key(event))
        break
      case 'human.timed_out':
        pending.delete(key(event))
        break
      case 'model.completed': {
        const tokens = Number(event.data?.completion_tokens ?? 0)
        if (tokens > 0) completionTokens += tokens
        break
      }
      case 'agent.summary': {
        const answer = String(event.data?.answer ?? '')
        if (answer) answerWords += answer.split(/\s+/).filter(Boolean).length
        break
      }
    }
  }
  factors.hwtSeconds = Math.floor(waitMS / 1000)
  const answerTokens = answerWords * 4 / 3
  const skimWords = Math.max(0, (completionTokens - answerTokens) * 3 / 4 * 0.1)
  factors.habSeconds += Math.floor((answerWords + skimWords) * 60 / READING_WPM)
  return factors
}
