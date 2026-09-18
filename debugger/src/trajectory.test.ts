import { describe, expect, it } from 'vitest'
import { buildTrajectory, claimKnowledgeAt, frameCutoffIndex, memoryDelta } from './trajectory'
import type { FrpEvent } from './types'

function e(id: string, type: string, payload: Record<string, unknown>, at: string): FrpEvent {
  return { event_id: id, type, payload: payload as FrpEvent['payload'], created_at: at }
}

const base = '2026-09-18T'

describe('trajectory', () => {
  const events: FrpEvent[] = [
    e('ev-1', 'frame.created', { frame_id: 'frame-1' }, `${base}10:00:00Z`),
    e('ev-2', 'claim.candidate', { claim_id: 'claim-h', proposition: 'bug lives in internal/reports', confidence: 0.8 }, `${base}10:01:00Z`),
    e('ev-3', 'claim.supported', { claim_id: 'claim-h', proposition: 'bug lives in internal/reports' }, `${base}10:01:00Z`),
    e('ev-4', 'focus.changed', { from: 'query:failing test', to: 'query:internal/reports', trigger: 'deliberate', frame_after: 'frame-2', evidence: ['claim:claim-h', 'event:ev-x'] }, `${base}10:01:01Z`),
    e('ev-5', 'frame.transitioned', { frame_id: 'frame-2', parent_frame_id: 'frame-1' }, `${base}10:01:02Z`),
    e('ev-6', 'claim.candidate', { claim_id: 'claim-w', proposition: 'format.go misformats totals', confidence: 0.9 }, `${base}10:02:00Z`),
    e('ev-7', 'claim.refuted', { claim_id: 'claim-h' }, `${base}10:03:00Z`),
    e('ev-8', 'focus.changed', { from: 'query:internal/reports', to: 'query:internal/util/format.go', trigger: 'hypothesis_retired', frame_after: 'frame-3', evidence: ['claim:claim-h'] }, `${base}10:03:01Z`),
    e('ev-9', 'frame.transitioned', { frame_id: 'frame-3', parent_frame_id: 'frame-2' }, `${base}10:03:02Z`),
  ]

  it('builds focus and claim lanes with triggers and evidence', () => {
    const lanes = buildTrajectory(events)
    const focus = lanes.find((lane) => lane.kind === 'focus')
    expect(focus?.nodes.map((node) => node.label)).toEqual(['query:internal/reports', 'query:internal/util/format.go'])
    expect(focus?.nodes[1].trigger).toBe('hypothesis_retired')
    expect(focus?.nodes[0].evidence).toEqual(['claim:claim-h', 'event:ev-x'])
    const hypothesis = lanes.find((lane) => lane.id === 'claim:claim-h')
    // Born-supported in the same step: candidate then supported at birth.
    expect(hypothesis?.nodes.map((node) => node.kind)).toEqual(['candidate', 'supported', 'refuted'])
  })

  it('locates the frame cutoff event', () => {
    expect(frameCutoffIndex(events, 'frame-2')).toBe(4)
    expect(frameCutoffIndex(events, 'frame-1')).toBe(0)
    expect(frameCutoffIndex(events, 'missing')).toBe(events.length)
  })

  it('answers what the agent knew at frame-2 and at frame-3', () => {
    const atFrame2 = claimKnowledgeAt(events, 'frame-2')
    expect(atFrame2.find((claim) => claim.claimId === 'claim-h')?.state).toBe('supported')
    expect(atFrame2.find((claim) => claim.claimId === 'claim-w')).toBeUndefined()
    const atFrame3 = claimKnowledgeAt(events, 'frame-3')
    expect(atFrame3.find((claim) => claim.claimId === 'claim-h')?.state).toBe('refuted')
    expect(atFrame3.find((claim) => claim.claimId === 'claim-w')?.state).toBe('candidate')
  })

  it('computes the memory delta of a single step, not the whole log', () => {
    const step = memoryDelta(events, 'frame-3')
    expect(step.parentFrameId).toBe('frame-2')
    expect(step.added.map((claim) => claim.claimId)).toEqual(['claim-w'])
    expect(step.refuted.map((claim) => claim.claimId)).toEqual(['claim-h'])
    // The proposition comes from the birth event even though the refuted
    // transition payload carries only the id.
    expect(step.refuted[0].proposition).toBe('bug lives in internal/reports')
    expect(step.focus).toEqual({ from: 'query:internal/reports', to: 'query:internal/util/format.go', trigger: 'hypothesis_retired', evidence: ['claim:claim-h'] })
    const firstStep = memoryDelta(events, 'frame-2')
    expect(firstStep.added.map((claim) => claim.claimId)).toEqual(['claim-h'])
    expect(firstStep.confirmed.map((claim) => claim.claimId)).toEqual(['claim-h'])
    expect(firstStep.parentFrameId).toBe('frame-1')
  })

  it('returns an empty delta for the root frame and unknown frames', () => {
    expect(memoryDelta(events, 'frame-1').added).toHaveLength(0)
    expect(memoryDelta(events, 'not-in-log').refuted).toHaveLength(0)
  })
})
