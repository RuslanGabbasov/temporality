import { describe, expect, it } from 'vitest'
import type { ObservationEvent } from './observationApi'
import { canonicalCommand, contentTokens, foldExperience, lifecycleKindOf, roleOfKnowledgeId, roleOfRun, runOfEvidenceRef, scopeOfCommand } from './experience'

let counter = 0

function event(type: string, at: string, data: Record<string, unknown>, context: { run?: string; project?: string } = {}, evidence?: { ref: string; type: string }[]): ObservationEvent {
  counter += 1
  return {
    schema: 'temporality.event/1',
    event_id: `ev-${String(counter).padStart(4, '0')}`,
    occurred_at: at,
    received_at: at,
    source: { id: 'temporality-agent-kernel', integration: 'temporality-agent-kernel', version: '0.1' },
    context: { project: context.project ?? 'calculator-e2e', run: context.run, actor: { id: 'test', type: 'agent' } },
    type,
    data,
    evidence,
  }
}

const T0 = '2026-09-24T13:02:58Z'
const T1 = '2026-09-24T13:05:43Z'
const T2 = '2026-09-24T13:17:43Z'
const T3 = '2026-09-24T13:36:33Z'

function teamScenario(): ObservationEvent[] {
  return [
    event('run.started', '2026-09-24T13:01:00Z', { workflow: 'LeadCoderReviewerQA' }, { run: 'calc-03' }),
    event('run.started', '2026-09-24T13:01:05Z', { role: 'coder', parent_run_id: 'calc-03' }, { run: 'calc-03/coder' }),
    event('model.started', '2026-09-24T13:01:10Z', { turn: 1 }, { run: 'calc-03/coder' }),
    event('tool.started', '2026-09-24T13:02:00Z', { tool: 'run_command' }, { run: 'calc-03/coder' }),
    event('knowledge.proposed', T0, { knowledge_id: 'auto/80447996488d1cde9bbc0b8f', proposition: 'Verification command `go test ./...` exited 0', kind: 'observation', policy_id: 'kernel-heuristic/execution-observation.v1', command: 'go test ./...', command_class: 'test' }, { run: 'calc-03/coder' }, [{ ref: 'calc-03/coder/turn/07/call_8aae', type: 'execution' }]),
    event('knowledge.confirmed', T1, { knowledge_id: 'auto/80447996488d1cde9bbc0b8f', rule: 'execution-reverification.v1' }, { run: 'calc-03/reviewer' }),
    event('run.completed', '2026-09-24T13:10:00Z', { status: 'completed' }, { run: 'calc-03/coder' }),
    event('run.started', '2026-09-24T13:17:00Z', { workflow: 'LeadCoderReviewerQA' }, { run: 'calc-04' }),
    event('run.started', '2026-09-24T13:17:05Z', { role: 'lead', parent_run_id: 'calc-04' }, { run: 'calc-04/lead' }),
    event('hint.offered', T2, { hint_id: 'hint-1', knowledge_id: 'auto/80447996488d1cde9bbc0b8f', state: 'confirmed', matched_by: ['command'] }, { run: 'calc-04/lead' }),
    event('knowledge.used', T2, { knowledge_id: 'auto/80447996488d1cde9bbc0b8f', hint_id: 'hint-1' }, { run: 'calc-04/lead' }),
    event('knowledge.proposed', '2026-09-24T13:18:52Z', { knowledge_id: 'auto/e780a2444b56d70a141c78be', proposition: 'Build command `go build ./...` exited 0', kind: 'observation', policy_id: 'kernel-heuristic/execution-observation.v1', command: 'go build ./...', command_class: 'build' }, { run: 'calc-04/lead' }, [{ ref: 'calc-04/lead/turn/05/call_8d989', type: 'execution' }]),
    event('run.started', '2026-09-24T13:36:00Z', { workflow: 'LeadCoderReviewerQA' }, { run: 'calc-05' }),
    event('hint.offered', T3, { hint_id: 'hint-2', knowledge_id: 'auto/80447996488d1cde9bbc0b8f', state: 'confirmed' }, { run: 'calc-05/qa' }),
    event('knowledge.used', T3, { knowledge_id: 'auto/80447996488d1cde9bbc0b8f', hint_id: 'hint-2' }, { run: 'calc-05/qa' }),
    event('knowledge.proposed', '2026-09-24T13:39:35Z', { knowledge_id: 'calc-05/reviewer/knowledge/61', proposition: 'Reviewer verdict: APPROVED', kind: 'claim' }, { run: 'calc-05/reviewer' }),
  ]
}

describe('lifecycleKindOf', () => {
  it('maps observation events onto the experience lifecycle vocabulary', () => {
    expect(lifecycleKindOf(event('knowledge.proposed', T0, { knowledge_id: 'k' }))).toBe('appeared')
    expect(lifecycleKindOf(event('knowledge.confirmed', T0, { knowledge_id: 'k' }))).toBe('validated')
    expect(lifecycleKindOf(event('knowledge.challenged', T0, { knowledge_id: 'k' }))).toBe('contradicted')
    expect(lifecycleKindOf(event('knowledge.disproved', T0, { knowledge_id: 'k', reason: 'r' }))).toBe('contradicted')
    expect(lifecycleKindOf(event('knowledge.corrected', T0, { knowledge_id: 'k' }))).toBe('weakened')
    expect(lifecycleKindOf(event('knowledge.invalidated', T0, { knowledge_id: 'k', reason: 'r' }))).toBe('archived')
    expect(lifecycleKindOf(event('knowledge.superseded', T0, { knowledge_id: 'k' }))).toBe('archived')
    expect(lifecycleKindOf(event('knowledge.used', T0, { knowledge_id: 'k', hint_id: 'h' }))).toBe('injected')
    expect(lifecycleKindOf(event('knowledge.used', T0, { knowledge_id: 'k', rule: 'execution-reuse.v1' }))).toBe('reused')
    expect(lifecycleKindOf(event('hint.offered', T0, { hint_id: 'h', knowledge_id: 'k' }))).toBe('recalled')
    expect(lifecycleKindOf(event('tool.started', T0, {}))).toBeUndefined()
  })
})

describe('canonicalCommand / scopeOfCommand', () => {
  it('merges flag variations of one verification into a canonical command', () => {
    expect(canonicalCommand('go test -count=1 -v ./...')).toBe('go test ./...')
    expect(canonicalCommand('go test ./...')).toBe('go test ./...')
    expect(scopeOfCommand('go test ./...')).toBe('test')
    expect(scopeOfCommand('go vet ./...')).toBe('vet')
    expect(scopeOfCommand('go build ./...')).toBe('build')
    expect(scopeOfCommand('gofmt -l .')).toBe('fmt')
  })
})

describe('role parsing', () => {
  it('derives roles from run and knowledge ids', () => {
    expect(roleOfRun('calc-03/coder')).toBe('coder')
    expect(roleOfRun('calc-03')).toBeUndefined()
    expect(roleOfKnowledgeId('calc-05/reviewer/knowledge/61')).toBe('reviewer')
    expect(roleOfKnowledgeId('auto/80447996488d1cde9bbc0b8f')).toBeUndefined()
    expect(runOfEvidenceRef('calc-03/coder/turn/07/call_8aae')).toEqual({ run: 'calc-03/coder', role: 'coder' })
    expect(runOfEvidenceRef('not-a-ref')).toBeUndefined()
  })
})

describe('foldExperience', () => {
  const model = foldExperience(teamScenario())

  it('folds runs with roles, parents and trajectory counters', () => {
    const coder = model.runs.find((run) => run.id === 'calc-03/coder')
    expect(coder).toMatchObject({ role: 'coder', parentRun: 'calc-03', toolCalls: 1, modelCalls: 1, status: 'completed' })
    const root = model.runs.find((run) => run.id === 'calc-03')
    expect(root?.role).toBeUndefined()
    // calc-04/lead never completed: end falls back to the stream end.
    const open = model.runs.find((run) => run.id === 'calc-04/lead')
    expect(open?.status).toBeUndefined()
    expect(open?.endedAt).toBe('2026-09-24T13:39:35Z')
  })

  it('clusters flag-variant verification commands into one experience', () => {
    const goTest = model.clusters.find((cluster) => cluster.id === 'exec:go test ./...')
    expect(goTest).toBeDefined()
    expect(goTest?.kind).toBe('execution')
    expect(goTest?.scope).toBe('test')
    expect(goTest?.members.map((member) => member.knowledgeId)).toContain('auto/80447996488d1cde9bbc0b8f')
    const kinds = goTest?.points.map((point) => point.kind)
    expect(kinds).toEqual(['appeared', 'validated', 'recalled', 'injected', 'recalled', 'injected'])
    expect(goTest?.state).toBe('confirmed')
  })

  it('keeps build verification separate from tests', () => {
    const build = model.clusters.find((cluster) => cluster.id === 'exec:go build ./...')
    expect(build?.scope).toBe('build')
    expect(build?.points.map((point) => point.kind)).toEqual(['appeared'])
  })

  it('keeps single unrelated claims in their own cluster', () => {
    const claims = model.clusters.filter((cluster) => cluster.kind === 'claim')
    expect(claims).toHaveLength(1)
    expect(claims[0].members[0].proposition).toContain('APPROVED')
    expect(claims[0].roles).toContain('reviewer')
  })

  it('clusters semantically overlapping claims across runs into one experience', () => {
    const gatekeeper = foldExperience([
      event('knowledge.proposed', T0, { knowledge_id: 'gk-01/knowledge/1', proposition: 'gatekeeper v2 authenticates via token file; env var rejected', kind: 'claim' }, { run: 'gk-01' }),
      event('knowledge.proposed', T1, { knowledge_id: 'gk-02/knowledge/2', proposition: 'gatekeeper token file setup verified access granted', kind: 'claim' }, { run: 'gk-02' }),
      event('knowledge.proposed', T2, { knowledge_id: 'other/knowledge/3', proposition: 'Reviewer verdict APPROVED unrelated topic', kind: 'claim' }, { run: 'other' }),
    ])
    const claims = gatekeeper.clusters.filter((cluster) => cluster.kind === 'claim')
    expect(claims).toHaveLength(2)
    const auth = claims.find((cluster) => cluster.scope === 'gatekeeper')
    expect(auth?.title).toBe('gatekeeper token')
    expect(auth?.members).toHaveLength(2)
    expect(auth?.runs).toEqual(['gk-01', 'gk-02'])
    expect(claims.find((cluster) => cluster.scope !== 'gatekeeper')?.members).toHaveLength(1)
  })

  it('tokenizes propositions into content tokens like the runtime matcher', () => {
    expect(contentTokens('The AUTH_TOKEN env var is unsupported')).toEqual(['auth', 'token', 'env', 'var', 'unsupported'])
    expect(contentTokens('')).toEqual([])
  })

  it('records episodes from execution evidence refs', () => {
    const goTest = model.clusters.find((cluster) => cluster.id === 'exec:go test ./...')
    expect(goTest?.episodes).toEqual([{ knowledgeId: 'auto/80447996488d1cde9bbc0b8f', run: 'calc-03/coder', role: 'coder', ref: 'calc-03/coder/turn/07/call_8aae', kind: 'execution', at: T0 }])
  })

  it('pairs hint offers with injections into activation links', () => {
    expect(model.links).toHaveLength(2)
    const link = model.links.find((item) => item.hintId === 'hint-1')
    expect(link).toMatchObject({ knowledgeId: 'auto/80447996488d1cde9bbc0b8f', clusterId: 'exec:go test ./...', offeredRun: 'calc-04/lead', usedRun: 'calc-04/lead' })
  })

  it('derives strength from state and reuse and bounds from the stream', () => {
    const goTest = model.clusters.find((cluster) => cluster.id === 'exec:go test ./...')
    expect(goTest?.strength).toBeGreaterThan(0.7)
    expect(model.bounds).toEqual({ from: '2026-09-24T13:01:00Z', to: '2026-09-24T13:39:35Z' })
    expect(model.totals.knowledge).toBe(3)
  })

  it('lets contradicted members dominate the aggregate cluster state', () => {
    const contradicted = foldExperience([
      ...teamScenario(),
      event('knowledge.challenged', '2026-09-24T14:00:00Z', { knowledge_id: 'auto/80447996488d1cde9bbc0b8f' }, { run: 'calc-05/qa' }),
    ])
    const goTest = contradicted.clusters.find((cluster) => cluster.id === 'exec:go test ./...')
    expect(goTest?.state).toBe('challenged')
    expect(goTest?.strength).toBeLessThan(0.5)
  })

  it('marks archived knowledge and keeps usage events recordable after terminal states', () => {
    const archived = foldExperience([
      event('knowledge.proposed', T0, { knowledge_id: 'auto/x', proposition: 'Verification command `go vet ./...` exited 0', kind: 'observation', policy_id: 'p', command: 'go vet ./...', command_class: 'test' }, { run: 'r/coder' }),
      event('knowledge.invalidated', T1, { knowledge_id: 'auto/x', reason: 'workspace changed' }, { run: 'r/coder' }),
      event('knowledge.used', T2, { knowledge_id: 'auto/x', rule: 'execution-reuse.v1' }, { run: 'r/qa' }),
    ])
    const vet = archived.clusters.find((cluster) => cluster.id === 'exec:go vet ./...')
    expect(vet?.points.map((point) => point.kind)).toEqual(['appeared', 'archived', 'reused'])
    expect(vet?.state).toBe('invalidated')
    // UI scope follows the command, not the kernel's coarse command_class.
    expect(vet?.scope).toBe('vet')
  })
})
