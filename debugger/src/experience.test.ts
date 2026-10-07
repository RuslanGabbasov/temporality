import { describe, expect, it } from 'vitest'
import type { ObservationEvent } from './observationApi'
import { aliveRowAt, buildRunChains, commandSegments, contentTokens, foldExperience, forensicOf, lifecycleKindOf, propositionMechanisms, roleOfKnowledgeId, roleOfRun, runOfEvidenceRef, scopeOfCommand, segmentSubcommand, shortKnowledge, stateBucket, windowAround, type RunInfo } from './experience'

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
    expect(scopeOfCommand('go test ./...')).toBe('test')
    expect(scopeOfCommand('go vet ./...')).toBe('vet')
    expect(scopeOfCommand('go build ./...')).toBe('build')
    expect(scopeOfCommand('gofmt -l .')).toBe('fmt')
  })
})

function runInfo(id: string, startedAt: string, overrides: Partial<RunInfo> = {}): RunInfo {
  return { id, startedAt, toolCalls: 0, modelCalls: 0, knowledgeEvents: 0, commands: [], ...overrides }
}

describe('buildRunChains', () => {
  it('groups a delegation tree under its root and flags failures', () => {
    const runs = [
      runInfo('root-1', '2026-10-01T10:00:00Z'),
      runInfo('root-1/coder', '2026-10-01T10:01:00Z', { parentRun: 'root-1', status: 'completed', endedAt: '2026-10-01T10:05:00Z' }),
      runInfo('root-1/reviewer', '2026-10-01T10:02:00Z', { parentRun: 'root-1', status: 'failed', endedAt: '2026-10-01T10:06:00Z' }),
      runInfo('root-2', '2026-10-01T11:00:00Z', { status: 'completed', endedAt: '2026-10-01T11:04:00Z' }),
    ]
    const { chains, rootOf } = buildRunChains(runs)
    expect(chains).toHaveLength(2)
    const first = chains.find((chain) => chain.root.id === 'root-1')!
    expect(first.runs.map((run) => run.id)).toEqual(['root-1', 'root-1/coder', 'root-1/reviewer'])
    expect(first.failed).toBe(true)
    expect(first.endedAt).toBe('2026-10-01T10:06:00Z')
    const second = chains.find((chain) => chain.root.id === 'root-2')!
    expect(second.failed).toBe(false)
    expect(rootOf.get('root-1/reviewer')).toBe('root-1')
    expect(rootOf.get('root-2')).toBe('root-2')
  })

  it('keeps runs whose parent falls outside the loaded window as their own roots', () => {
    const runs = [runInfo('orphan/delegate/01', '2026-10-01T10:00:00Z', { parentRun: 'orphan' })]
    const { chains, rootOf } = buildRunChains(runs)
    expect(chains).toHaveLength(1)
    expect(chains[0].root.id).toBe('orphan/delegate/01')
    expect(rootOf.get('orphan/delegate/01')).toBe('orphan/delegate/01')
  })

  it('captures actor, title and agent id from run.started during folding', () => {
    const events = [
      event('run.started', '2026-10-01T10:00:00Z', { role: 'lead', agent_id: 'lead', title: 'Fix login timeout', parent_run_id: '' }, { run: 'fix-1' }),
      event('run.started', '2026-10-01T10:01:00Z', { role: 'coder', agent_id: 'coder', parent_run_id: 'fix-1' }, { run: 'fix-1/coder' }),
    ]
    const model = foldExperience(events)
    const root = model.runs.find((run) => run.id === 'fix-1')!
    expect(root.title).toBe('Fix login timeout')
    expect(root.actor).toBe('test')
    expect(root.agentId).toBe('lead')
    const child = model.runs.find((run) => run.id === 'fix-1/coder')!
    expect(child.parentRun).toBe('fix-1')
  })
})

describe('scope derivation helpers', () => {
  it('splits compound commands into simple segments', () => {
    expect(commandSegments(['mkdir', '-p', 'out', '&&', 'go', 'run', '.', 'report'])).toEqual([['mkdir', '-p', 'out'], ['go', 'run', '.', 'report']])
  })

  it('extracts the mechanism from interpreter, wrapper and project binaries', () => {
    expect(segmentSubcommand(['go', 'run', '.', 'auth'])).toBe('auth')
    expect(segmentSubcommand(['env', 'AUTH_TOKEN=x', 'go', 'run', '.', 'auth'])).toBe('auth')
    expect(segmentSubcommand(['go', 'test', './...'])).toBe('test')
    expect(segmentSubcommand(['nb', 'report'])).toBe('report')
    expect(segmentSubcommand(['./bin/tool', 'serve'])).toBe('serve')
    expect(segmentSubcommand(['go', 'run', '.'])).toBeUndefined()
    expect(segmentSubcommand(['mkdir', '-p', 'out'])).toBeUndefined()
    expect(segmentSubcommand(['echo', 'hi'])).toBeUndefined()
  })

  it('matches proposition words against the executed-command vocabulary', () => {
    const vocab = new Set(['auth', 'report', 'fetch'])
    expect(propositionMechanisms('nb auth authenticates via .token-file', vocab)).toEqual(['auth'])
    expect(propositionMechanisms('AUTH_TOKEN env var rejected; auth ok', vocab)).toEqual(['auth'])
    expect(propositionMechanisms('setup verified end to end: auth and report', vocab)).toEqual(['auth', 'report'])
    expect(propositionMechanisms('Reviewer verdict APPROVED', vocab)).toEqual([])
  })

  it('buckets memory states for the live/dead filter', () => {
    expect(stateBucket('confirmed')).toBe('active')
    expect(stateBucket('proposed')).toBe('active')
    expect(stateBucket('challenged')).toBe('stale')
    expect(stateBucket('corrected')).toBe('stale')
    expect(stateBucket('invalidated')).toBe('invalidated')
    expect(stateBucket('superseded')).toBe('archived')
  })

  it('shortens knowledge ids into row labels', () => {
    expect(shortKnowledge('run/knowledge/82')).toBe('K82')
    expect(shortKnowledge('auto/80447996488d1cde9bbc0b8f')).toBe('…c0b8f')
    expect(shortKnowledge('plain')).toBe('plain')
  })

  it('counts the living population at a moment', () => {
    const rows = [
      { firstAt: '1', lastAt: '9', terminal: { kind: 'archived' as const, at: '5' } },
      { firstAt: '2', lastAt: '9' },
      { firstAt: '6', lastAt: '9' },
    ] as never[]
    expect(aliveRowAt(rows, '3')).toBe(2)
    expect(aliveRowAt(rows, '5')).toBe(1)
    expect(aliveRowAt(rows, '7')).toBe(2)
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

  it('records explicit lineage from corrected/superseded replacement ids', () => {
    const corrected = foldExperience([
      event('knowledge.proposed', T0, { knowledge_id: 'auto/old', proposition: 'Verification command `go vet ./...` exited 0', kind: 'observation', policy_id: 'p', command: 'go vet ./...', command_class: 'test' }, { run: 'r/coder' }),
      event('knowledge.proposed', T1, { knowledge_id: 'auto/new', proposition: 'Verification command `go vet ./...` exits 1 until generated', kind: 'observation', policy_id: 'p', command: 'go vet ./...', command_class: 'test' }, { run: 'r/coder' }),
      event('knowledge.corrected', T2, { knowledge_id: 'auto/old', replacement_id: 'auto/new', reason: 'v2 vet behavior changed' }, { run: 'r/coder' }),
    ])
    expect(corrected.lineage).toEqual([
      { fromId: 'auto/old', toId: 'auto/new', reason: 'v2 vet behavior changed', eventId: expect.any(String), at: T2, inferred: false },
    ])
  })

  it('infers lineage from invalidation reasons and ignores unknown refs', () => {
    const gatekeeper = foldExperience([
      event('knowledge.proposed', T0, { knowledge_id: 'gk-01/knowledge/77', proposition: 'gatekeeper v2 authenticates via token file; env var rejected', kind: 'claim' }, { run: 'gk-01' }),
      event('knowledge.proposed', T1, { knowledge_id: 'gk-02b/knowledge/88', proposition: 'gatekeeper end-to-end token file setup works', kind: 'claim' }, { run: 'gk-02b' }),
      event('knowledge.proposed', T2, { knowledge_id: 'gk-03/knowledge/86', proposition: 'gatekeeper v3 authenticates via env var AUTH_TOKEN', kind: 'claim' }, { run: 'gk-03' }),
      event('knowledge.invalidated', T3, { knowledge_id: 'gk-01/knowledge/77', reason: 'v2 assumptions no longer hold (see gk-03/knowledge/86 and missing/99)' }, { run: 'gk-03' }),
      event('knowledge.invalidated', T3, { knowledge_id: 'gk-02b/knowledge/88', reason: 'superseded environment, see gk-03/knowledge/86' }, { run: 'gk-03' }),
    ])
    expect(gatekeeper.lineage).toHaveLength(2)
    expect(gatekeeper.lineage.every((edge) => edge.toId === 'gk-03/knowledge/86' && edge.inferred)).toBe(true)
    expect(gatekeeper.lineage.map((edge) => edge.fromId).sort()).toEqual(['gk-01/knowledge/77', 'gk-02b/knowledge/88'])
  })

  it('joins the consuming run status onto activation links', () => {
    const failed = foldExperience([
      event('knowledge.proposed', T0, { knowledge_id: 'auto/k', proposition: 'Verification command `go test ./...` exited 0', kind: 'observation', policy_id: 'p', command: 'go test ./...', command_class: 'test' }, { run: 'r01/coder' }),
      event('run.started', '2026-09-24T14:00:00Z', {}, { run: 'r02' }),
      event('hint.offered', T1, { hint_id: 'h1', knowledge_id: 'auto/k', state: 'confirmed' }, { run: 'r02' }),
      event('knowledge.used', T1, { knowledge_id: 'auto/k', hint_id: 'h1' }, { run: 'r02' }),
      event('run.failed', '2026-09-24T14:05:00Z', { status: 'failed' }, { run: 'r02' }),
    ])
    expect(failed.links).toHaveLength(1)
    expect(failed.links[0]).toMatchObject({ usedRun: 'r02', usedRunStatus: 'failed' })
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

describe('windowAround (forensic navigation)', () => {
  const bounds = { from: '2026-09-25T04:00:00Z', to: '2026-09-25T06:00:00Z' }

  it('centers the window on the target moment', () => {
    const { t0, t1 } = windowAround('2026-09-25T05:00:00Z', bounds)
    const target = new Date('2026-09-25T05:00:00Z').getTime()
    expect(t0).toBeLessThanOrEqual(target)
    expect(t1).toBeGreaterThanOrEqual(target)
    expect((t0 + t1) / 2).toBe(target)
  })

  it('clamps to the project bounds near the edges', () => {
    const early = windowAround('2026-09-25T04:00:10Z', bounds)
    expect(early.t0).toBe(new Date(bounds.from).getTime())
    expect(early.t1).toBeGreaterThan(early.t0)
    const late = windowAround('2026-09-25T05:59:50Z', bounds)
    expect(late.t1).toBe(new Date(bounds.to).getTime())
  })

  it('never shrinks below the minimum span', () => {
    const tight = windowAround('2026-09-25T05:00:00Z', { from: '2026-09-25T04:59:40Z', to: '2026-09-25T05:00:20Z' })
    expect(tight.t1 - tight.t0).toBeGreaterThanOrEqual(30_000)
  })
})

describe('forensicOf (origin → activation → death reconstruction)', () => {
  const F0 = '2026-09-25T10:00:00Z'
  const F1 = '2026-09-25T10:05:00Z'
  const F2 = '2026-09-25T10:12:00Z'
  const F3 = '2026-09-25T11:00:00Z'
  const F4 = '2026-09-25T11:30:00Z'
  const CLAIM = 'nightly-01/knowledge/7'

  function forensicScenario(): ObservationEvent[] {
    return [
      event('run.started', '2026-09-25T09:58:00Z', { status: 'running' }, { run: 'nightly-01' }),
      event('approval.auto_granted', '2026-09-25T09:59:00Z', { operation: { type: 'sandbox.exec', arguments: { command: ['go', 'build', '-o', '/tmp/nb', '.'] } } }, { run: 'nightly-01' }),
      event('approval.auto_granted', '2026-09-25T09:59:30Z', { operation: { type: 'sandbox.exec', arguments: { command: ['env', 'LH_TOKEN=t', '/tmp/nb', 'login'] } } }, { run: 'nightly-01' }),
      event('knowledge.proposed', F0, { knowledge_id: CLAIM, proposition: 'login reads the LH_TOKEN env var', kind: 'claim' }, { run: 'nightly-01' }, [{ ref: 'nightly-01/turn/02/call_ab12', type: 'execution' }]),
      event('knowledge.confirmed', F1, { knowledge_id: CLAIM }, { run: 'nightly-01' }),
      event('run.completed', '2026-09-25T10:06:00Z', { status: 'completed' }, { run: 'nightly-01' }),
      event('run.started', '2026-09-25T10:10:00Z', { status: 'running' }, { run: 'nightly-02' }),
      event('hint.offered', F2, { hint_id: 'h1', knowledge_id: CLAIM, state: 'confirmed' }, { run: 'nightly-02' }),
      event('knowledge.used', F2, { knowledge_id: CLAIM, hint_id: 'h1' }, { run: 'nightly-02' }),
      event('approval.auto_granted', '2026-09-25T10:13:00Z', { operation: { type: 'sandbox.exec', arguments: { command: ['env', 'LH_TOKEN=t', '/tmp/nb', 'login'] } } }, { run: 'nightly-02' }),
      event('run.completed', '2026-09-25T10:20:00Z', { status: 'completed' }, { run: 'nightly-02' }),
      event('run.started', '2026-09-25T10:55:00Z', { status: 'running' }, { run: 'nightly-03' }),
      event('hint.offered', F3, { hint_id: 'h2', knowledge_id: CLAIM, state: 'confirmed' }, { run: 'nightly-03' }),
      event('run.completed', '2026-09-25T11:25:00Z', { status: 'turn_limit' }, { run: 'nightly-03' }),
      event('knowledge.proposed', '2026-09-25T11:26:00Z', { knowledge_id: 'nightly-04/knowledge/9', proposition: 'login moved to the .lh-token file', kind: 'claim' }, { run: 'nightly-04' }),
      event('knowledge.invalidated', F4, { knowledge_id: CLAIM, reason: 'superseded by nightly-04/knowledge/9' }, { run: 'nightly-04' }),
    ]
  }

  it('reconstructs formation: run, evidence with run attribution, prior commands', () => {
    const model = foldExperience(forensicScenario())
    const row = model.rows.find((item) => item.knowledgeId === CLAIM)
    const record = forensicOf(row!, model)
    expect(record.formed!.at).toBe(F0)
    expect(record.formed!.run).toBe('nightly-01')
    expect(record.formed!.evidence).toHaveLength(1)
    expect(record.formed!.evidence[0].ref).toBe('nightly-01/turn/02/call_ab12')
    // Evidence is attributable: the ref encodes the producing run.
    expect(record.formed!.evidence[0].run).toBe('nightly-01')
    // Commands the agent ran before the claim appeared, in the forming run.
    expect(record.formed!.commands.map((item) => item.command)).toEqual(['go build -o /tmp/nb .', 'env LH_TOKEN=t /tmp/nb login'])
  })

  it('reconstructs activations with outcome and unused offers separately', () => {
    const model = foldExperience(forensicScenario())
    const row = model.rows.find((item) => item.knowledgeId === CLAIM)
    const record = forensicOf(row!, model)
    expect(record.activations).toHaveLength(2)
    const used = record.activations.find((item) => item.usedAt === F2)!
    expect(used.usedRun).toBe('nightly-02')
    expect(used.usedRunStatus).toBe('completed')
    // Commands executed after the hint was used in the reusing run.
    expect(used.commands.map((item) => item.command)).toEqual(['env LH_TOKEN=t /tmp/nb login'])
    const unused = record.activations.find((item) => !item.usedAt)!
    expect(unused.offeredRun).toBe('nightly-03')
    expect(unused.commands).toEqual([])
  })

  it('reconstructs death with reason, actor and lineage successor', () => {
    const model = foldExperience(forensicScenario())
    const row = model.rows.find((item) => item.knowledgeId === CLAIM)
    const record = forensicOf(row!, model)
    expect(record.deaths).toHaveLength(1)
    expect(record.deaths[0].kind).toBe('archived')
    expect(record.deaths[0].at).toBe(F4)
    expect(record.deaths[0].reason).toBe('superseded by nightly-04/knowledge/9')
    // The inferred lineage edge names the successor, so the death is traceable
    // to the replacement claim.
    expect(record.deaths[0].supersededBy).toEqual(['nightly-04/knowledge/9'])
  })

  it('returns an empty record for knowledge without lifecycle events', () => {
    const model = foldExperience([event('knowledge.proposed', F0, { knowledge_id: 'orphan/knowledge/1', proposition: 'never confirmed', kind: 'claim' }, { run: 'nightly-01' })])
    const row = model.rows.find((item) => item.knowledgeId === 'orphan/knowledge/1')
    const record = forensicOf(row!, model)
    expect(record.formed).toBeDefined()
    expect(record.activations).toEqual([])
    expect(record.deaths).toEqual([])
  })
})
