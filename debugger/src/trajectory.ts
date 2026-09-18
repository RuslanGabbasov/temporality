import type { FrpEvent, Json } from './types'
import { eventLabel, eventTime } from './timeline'

/** Pivot Этап 5: the debugger shows the history of investigation as a
 * trajectory — focus moves with their triggers (why did the agent go there)
 * and claim lifecycles (hypothesis → evidence → confirmed/refuted) — plus the
 * Memory Delta between a frame and its parent (what the agent knew, when).
 * Everything derives from the event log the debugger already loads. */

export type ClaimState = 'candidate' | 'supported' | 'refuted' | 'superseded'

export interface TrajectoryNode {
  at: string
  kind: 'focus' | 'candidate' | 'supported' | 'refuted' | 'superseded'
  label: string
  trigger?: string
  evidence?: string[]
  frameId?: string
  eventId: string
}

export interface TrajectoryLane {
  id: string
  kind: 'focus' | 'claim'
  label: string
  nodes: TrajectoryNode[]
}

export interface ClaimKnowledge {
  claimId: string
  proposition: string
  confidence?: number
  state: ClaimState
  since: string
  evidence: string[]
}

function payloadOf(event: FrpEvent): Record<string, Json> {
  return (event.payload ?? {}) as Record<string, Json>
}

function stringOf(value: Json | undefined): string {
  return typeof value === 'string' ? value : ''
}

function idOf(event: FrpEvent): string {
  return event.event_id ?? event.id ?? ''
}

/** buildTrajectory folds the event log into investigation lanes. Focus lanes
 * answer "why did the agent go there" (from/to/trigger/evidence), claim lanes
 * answer "where did a hypothesis appear and how did it end". */
export function buildTrajectory(events: FrpEvent[]): TrajectoryLane[] {
  const focusLane: TrajectoryLane = { id: 'focus', kind: 'focus', label: 'focus trajectory', nodes: [] }
  const claimLanes = new Map<string, TrajectoryLane>()
  for (const event of events) {
    const payload = payloadOf(event)
    const time = eventTime(event) || `#${idOf(event).slice(0, 8)}`
    switch (eventLabel(event)) {
      case 'focus.changed': {
        const to = stringOf(payload.to) || '?'
        focusLane.nodes.push({ at: time, kind: 'focus', label: to, trigger: stringOf(payload.trigger) || undefined, evidence: evidenceRefs(payload.evidence), frameId: stringOf(payload.frame_after) || undefined, eventId: idOf(event) })
        break
      }
      case 'claim.candidate':
      case 'claim.supported':
      case 'claim.refuted':
      case 'claim.superseded': {
        const claimId = stringOf(payload.claim_id)
        if (!claimId) break
        const kind = eventLabel(event).slice('claim.'.length) as TrajectoryNode['kind']
        let lane = claimLanes.get(claimId)
        if (!lane) {
          lane = { id: `claim:${claimId}`, kind: 'claim', label: stringOf(payload.proposition) || `claim ${claimId.slice(0, 8)}`, nodes: [] }
          claimLanes.set(claimId, lane)
        }
        lane.nodes.push({ at: time, kind, label: stringOf(payload.proposition) || kind, evidence: evidenceRefs(payload.evidence), frameId: stringOf(payload.frame_id) || undefined, eventId: idOf(event) })
        break
      }
      default:
        break
    }
  }
  const lanes: TrajectoryLane[] = []
  if (focusLane.nodes.length > 0) lanes.push(focusLane)
  lanes.push(...claimLanes.values())
  return lanes
}

function evidenceRefs(value: Json | undefined): string[] | undefined {
  if (!Array.isArray(value)) return undefined
  const refs = value.filter((item): item is string => typeof item === 'string')
  return refs.length ? refs : undefined
}

/** The frame a memory snapshot is taken "as of": the frame.transitioned event
 * that produced it (or the frame.created event for the root frame). Events
 * after that index are the future the agent had not seen yet. */
export function frameCutoffIndex(events: FrpEvent[], frameId: string | undefined): number {
  if (!frameId) return events.length
  let created = -1
  for (let index = 0; index < events.length; index += 1) {
    const payload = payloadOf(events[index])
    if (eventLabel(events[index]) === 'frame.transitioned' && stringOf(payload.frame_id) === frameId) return index
    if (eventLabel(events[index]) === 'frame.created' && stringOf(payload.frame_id) === frameId) created = index
  }
  return created >= 0 ? created : events.length
}

/** claimKnowledgeAt folds the event log up to (and including) a frame's own
 * creation event: the claims the agent knew at that moment, with the state
 * they were in then — the "what did the agent know at T" guarantee. */
export function claimKnowledgeAt(events: FrpEvent[], frameId: string | undefined): ClaimKnowledge[] {
  const cutoff = frameCutoffIndex(events, frameId)
  const knowledge = new Map<string, ClaimKnowledge>()
  for (let index = 0; index <= cutoff && index < events.length; index += 1) {
    const event = events[index]
    const payload = payloadOf(event)
    const claimId = stringOf(payload.claim_id)
    if (!claimId) continue
    const label = eventLabel(event)
    const time = eventTime(event)
    if (label === 'claim.candidate') {
      knowledge.set(claimId, { claimId, proposition: stringOf(payload.proposition) || `claim ${claimId.slice(0, 8)}`, confidence: typeof payload.confidence === 'number' ? payload.confidence : undefined, state: 'candidate', since: time, evidence: evidenceRefs(payload.evidence) ?? [] })
      continue
    }
    if (label === 'claim.supported' || label === 'claim.refuted' || label === 'claim.superseded') {
      const existing = knowledge.get(claimId)
      if (existing) {
        existing.state = label.slice('claim.'.length) as ClaimState
        existing.since = time
      }
    }
  }
  return [...knowledge.values()]
}

export interface MemoryDelta {
  frameId?: string
  parentFrameId?: string
  added: ClaimKnowledge[]
  confirmed: ClaimKnowledge[]
  refuted: ClaimKnowledge[]
  superseded: ClaimKnowledge[]
  focus?: { from: string; to: string; trigger?: string; evidence?: string[] }
}

/** memoryDelta answers "what changed between two frames": claims born,
 * confirmed, refuted or superseded by the step that produced the frame, plus
 * the focus move that step made (with its trigger). The window is exactly
 * the step — events after the parent frame's own creation, up to and
 * including this frame's transition. */
export function memoryDelta(events: FrpEvent[], frameId: string | undefined): MemoryDelta {
  const delta: MemoryDelta = { frameId, added: [], confirmed: [], refuted: [], superseded: [] }
  if (!frameId) return delta
  const cutoff = frameCutoffIndex(events, frameId)
  const cutoffEvent = events[cutoff]
  if (!cutoffEvent) return delta
  const cutoffPayload = payloadOf(cutoffEvent)
  const cutoffLabel = eventLabel(cutoffEvent)
  if (cutoffLabel === 'frame.created' && stringOf(cutoffPayload.frame_id) === frameId) {
    return delta // root frame: nothing precedes it in this episode
  }
  if (cutoffLabel !== 'frame.transitioned' || stringOf(cutoffPayload.frame_id) !== frameId) {
    return delta // frame not part of this event list
  }
  delta.parentFrameId = stringOf(cutoffPayload.parent_frame_id) || undefined
  // Transitions carry only claim ids; propositions come from the birth events.
  const propositions = new Map<string, string>()
  for (const event of events.slice(0, cutoff + 1)) {
    const payload = payloadOf(event)
    const claimId = stringOf(payload.claim_id)
    const proposition = stringOf(payload.proposition)
    if (claimId && proposition && eventLabel(event) === 'claim.candidate') propositions.set(claimId, proposition)
  }
  const parentCutoff = delta.parentFrameId ? frameCutoffIndex(events, delta.parentFrameId) : -1
  const entry = (event: FrpEvent, state: ClaimState): ClaimKnowledge => {
    const payload = payloadOf(event)
    const claimId = stringOf(payload.claim_id)
    return { claimId, proposition: propositions.get(claimId) ?? `claim ${claimId.slice(0, 8)}`, confidence: typeof payload.confidence === 'number' ? payload.confidence : undefined, state, since: eventTime(event), evidence: evidenceRefs(payload.evidence) ?? [] }
  }
  for (let index = parentCutoff + 1; index <= cutoff; index += 1) {
    const event = events[index]
    const payload = payloadOf(event)
    const label = eventLabel(event)
    if (label === 'focus.changed' && stringOf(payload.frame_after) === frameId) {
      delta.focus = { from: stringOf(payload.from), to: stringOf(payload.to), trigger: stringOf(payload.trigger) || undefined, evidence: evidenceRefs(payload.evidence) }
      continue
    }
    if (label === 'claim.candidate') delta.added.push(entry(event, 'candidate'))
    if (label === 'claim.supported') delta.confirmed.push(entry(event, 'supported'))
    if (label === 'claim.refuted') delta.refuted.push(entry(event, 'refuted'))
    if (label === 'claim.superseded') delta.superseded.push(entry(event, 'superseded'))
  }
  return delta
}
