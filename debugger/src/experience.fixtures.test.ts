import { describe, expect, it } from 'vitest'
import type { ObservationEvent } from './observationApi'
import { aliveRowAt, foldExperience, forensicOf, stateBucket } from './experience'
import exp4 from './fixtures/experiment4-gatekeeper.json'
import exp5 from './fixtures/experiment5-nightlybox.json'
import exp6 from './fixtures/experiment6-relay.json'

const GATEKEEPER = exp4 as unknown as ObservationEvent[]
const NIGHTLYBOX = exp5 as unknown as ObservationEvent[]
const RELAY = exp6 as unknown as ObservationEvent[]

const K = (run: string, id: string) => `${run}/knowledge/${id}`

function appearedIn(events: ObservationEvent[], knowledgeId: string): string | undefined {
  const model = foldExperience(events)
  return model.rows.find((row) => row.knowledgeId === knowledgeId)?.points.find((point) => point.kind === 'appeared')?.run
}

// §19 of the phase plan: both experiment corpora are acceptance fixtures for
// the Experience Timeline. Every assertion below reads from the fold only.
describe('Experiment 4 fixture — rejected path, contradiction, replacement', () => {
  const model = foldExperience(GATEKEEPER)

  it('folds the recorded corpus shape', () => {
    expect(model.totals.events).toBe(292)
    expect(model.rows).toHaveLength(3)
    expect(model.runs.map((run) => run.id)).toEqual(['gatekeeper-20260925-01', 'gatekeeper-20260925-02', 'gatekeeper-20260925-02b', 'gatekeeper-20260925-03'])
  })

  it('K77 appears in run 01 and is recalled/injected in 02, 02b and 03', () => {
    expect(appearedIn(GATEKEEPER, K('gatekeeper-20260925-01', '77'))).toBe('gatekeeper-20260925-01')
    const recalls = model.links.filter((link) => link.knowledgeId === K('gatekeeper-20260925-01', '77')).map((link) => link.usedRun)
    expect(recalls).toEqual(['gatekeeper-20260925-02', 'gatekeeper-20260925-02b', 'gatekeeper-20260925-03'])
  })

  it('keeps recall events of the failed run 02 with its outcome', () => {
    expect(model.runs.find((run) => run.id === 'gatekeeper-20260925-02')?.status).toBe('failed')
    const failedLink = model.links.find((link) => link.usedRun === 'gatekeeper-20260925-02')
    expect(failedLink?.usedRunStatus).toBe('failed')
  })

  it('recalls K77 and K88 together in run 03 where K86 appears', () => {
    const run03 = model.links.filter((link) => link.usedRun === 'gatekeeper-20260925-03').map((link) => link.knowledgeId)
    expect(run03).toEqual([K('gatekeeper-20260925-01', '77'), K('gatekeeper-20260925-02b', '88')])
    expect(appearedIn(GATEKEEPER, K('gatekeeper-20260925-03', '86'))).toBe('gatekeeper-20260925-03')
  })

  it('invalidates K77/K88 and infers lineage into K86', () => {
    const stateOf = (id: string) => model.rows.find((row) => row.knowledgeId === id)?.state
    expect(stateOf(K('gatekeeper-20260925-01', '77'))).toBe('invalidated')
    expect(stateOf(K('gatekeeper-20260925-02b', '88'))).toBe('invalidated')
    expect(stateOf(K('gatekeeper-20260925-03', '86'))).toBe('proposed')
    expect(model.lineage.map((edge) => `${edge.fromId}->${edge.toId}`).sort()).toEqual([
      `${K('gatekeeper-20260925-01', '77')}->${K('gatekeeper-20260925-03', '86')}`,
      `${K('gatekeeper-20260925-02b', '88')}->${K('gatekeeper-20260925-03', '86')}`,
    ])
    expect(model.lineage.every((edge) => edge.inferred)).toBe(true)
  })

  it('groups the whole corpus into one fallback scope lane', () => {
    expect(model.scopes).toHaveLength(1)
    expect(model.scopes[0].rows).toHaveLength(3)
  })
})

describe('Experiment 5 fixture — accumulation, selective death, recall hygiene', () => {
  const model = foldExperience(NIGHTLYBOX)

  const row = (run: string, id: string) => model.rows.find((item) => item.knowledgeId === K(run, id))

  it('folds the recorded corpus shape', () => {
    expect(model.totals.events).toBe(424)
    expect(model.rows).toHaveLength(6)
    expect(model.links).toHaveLength(14)
  })

  it('experiences appear in their discovery runs', () => {
    expect(appearedIn(NIGHTLYBOX, K('nightly-20260925-01b', '82'))).toBe('nightly-20260925-01b')
    expect(appearedIn(NIGHTLYBOX, K('nightly-20260925-02b', '56'))).toBe('nightly-20260925-02b')
    expect(appearedIn(NIGHTLYBOX, K('nightly-20260925-03', '40'))).toBe('nightly-20260925-03')
    expect(appearedIn(NIGHTLYBOX, K('nightly-20260925-04', '37'))).toBe('nightly-20260925-04')
    expect(appearedIn(NIGHTLYBOX, K('nightly-20260925-05', '73'))).toBe('nightly-20260925-05')
  })

  it('run 05 recalls all four accumulated experiences', () => {
    const run05 = model.links.filter((link) => link.usedRun === 'nightly-20260925-05').map((link) => link.knowledgeId)
    expect(run05).toHaveLength(4)
  })

  it('kills exactly the auth experience and the combined v2 setup', () => {
    expect(row('nightly-20260925-01b', '82')?.state).toBe('invalidated')
    expect(row('nightly-20260925-03', '40')?.state).toBe('invalidated')
    for (const alive of [['nightly-20260925-02b', '56'], ['nightly-20260925-04', '37'], ['nightly-20260925-05', '73']] as const) {
      expect(row(alive[0], alive[1])?.terminal).toBeUndefined()
    }
  })

  it('run 06 receives exactly the three living experiences', () => {
    const run06 = model.links.filter((link) => link.usedRun === 'nightly-20260925-06').map((link) => link.knowledgeId).sort()
    expect(run06).toEqual([
      K('nightly-20260925-02b', '56'),
      K('nightly-20260925-04', '37'),
      K('nightly-20260925-05', '73'),
    ].sort())
    expect(run06).not.toContain(K('nightly-20260925-01b', '82'))
    expect(run06).not.toContain(K('nightly-20260925-03', '40'))
  })

  it('derives semantic scopes from executed mechanisms, not token overlap', () => {
    expect(row('nightly-20260925-01b', '82')?.scopes.primary).toBe('auth')
    expect(row('nightly-20260925-02b', '56')?.scopes.primary).toBe('report')
    expect(row('nightly-20260925-04', '37')?.scopes.primary).toBe('fetch')
    // cross-scope members land in end-to-end with secondary scopes
    expect(row('nightly-20260925-03', '40')?.scopes).toMatchObject({ primary: 'end-to-end' })
    expect(row('nightly-20260925-03', '40')?.scopes.secondary).toEqual(expect.arrayContaining(['auth', 'report']))
    expect(row('nightly-20260925-06', '51')?.scopes.primary).toBe('end-to-end')
    // corrected auth experience inherits the scope of what it supersedes
    expect(row('nightly-20260925-05', '73')?.scopes.primary).toBe('auth')
  })

  it('keeps cross-scope members from breaking the lane structure', () => {
    const lanes = model.scopes.map((scope) => scope.id).sort()
    expect(lanes).toEqual(['auth', 'end-to-end', 'fetch', 'report'])
    for (const scope of model.scopes) {
      expect(scope.rows.length).toBeGreaterThan(0)
      expect(scope.rows.every((row_) => row_.scopes.primary === scope.id)).toBe(true)
    }
  })

  it('memory population shrinks after selective death', () => {
    const run06 = model.runs.find((run) => run.id === 'nightly-20260925-06')
    expect(run06).toBeDefined()
    expect(aliveRowAt(model.rows, run06!.startedAt)).toBe(3)
  })
})

// Experiment 6 (§ examples/experiment6/README.md): the third acceptance
// corpus adds two properties the earlier fixtures could not show — a
// supersession CHAIN across two environment flips with semantic resurrection
// (a3 restores a1's claim in a new living node), and STALE knowledge that is
// challenged but neither confirmed nor contradicted, and therefore still
// offered (with caution) instead of being retired.
describe('Experiment 6 fixture — competing experiences across two flips', () => {
  const model = foldExperience(RELAY)

  const A1 = K('relay-20260925-01', '89')
  const A2 = K('relay-20260925-04', '54')
  const A3 = K('relay-20260925-07', '80')
  const B1 = K('relay-20260925-02b', '76')
  const C1 = K('relay-20260925-05b', '78')
  const C2 = K('relay-20260925-07', '83')

  const row = (id: string) => model.rows.find((item) => item.knowledgeId === id)

  it('folds the recorded corpus shape', () => {
    expect(model.totals.events).toBe(966)
    expect(model.runs).toHaveLength(11)
    // failure lanes stay in the history: 02 (no remember) and 05 (tool schema slip)
    expect(model.runs.filter((run) => run.status === 'turn_limit').map((run) => run.id)).toEqual(['relay-20260925-01', 'relay-20260925-02', 'relay-20260925-05'])
  })

  it('auth experiences appear in their discovery runs and share the auth lane', () => {
    expect(appearedIn(RELAY, A1)).toBe('relay-20260925-01')
    expect(appearedIn(RELAY, A2)).toBe('relay-20260925-04')
    expect(appearedIn(RELAY, A3)).toBe('relay-20260925-07')
    expect(row(A1)?.scopes.primary).toBe('auth')
    expect(row(A2)?.scopes.primary).toBe('auth')
    expect(row(A3)?.scopes.primary).toBe('auth')
  })

  it('builds the full supersession chain a1 → a2 → a3 from invalidation reasons', () => {
    expect(model.lineage.map((edge) => `${edge.fromId}->${edge.toId}`)).toEqual([`${A1}->${A2}`, `${A2}->${A3}`])
    expect(model.lineage.every((edge) => edge.inferred)).toBe(true)
    expect(row(A1)?.state).toBe('invalidated')
    expect(row(A2)?.state).toBe('invalidated')
  })

  it('a3 is a living resurrection of the a1 claim', () => {
    expect(row(A3)?.state).toBe('proposed')
    expect(row(A3)?.terminal).toBeUndefined()
    expect(row(A3)?.proposition).toMatch(/API_KEY/)
    expect(row(A1)?.proposition).toMatch(/API_KEY/)
  })

  it('b1 is reused across the flips and never dies', () => {
    expect(row(B1)?.terminal).toBeUndefined()
    expect(row(B1)?.scopes.primary).toBe('push')
    const reusedIn = model.links.filter((link) => link.knowledgeId === B1).map((link) => link.usedRun)
    for (const run of ['relay-20260925-03', 'relay-20260925-06', 'relay-20260925-08', 'relay-20260925-09']) {
      expect(reusedIn).toContain(run)
    }
  })

  it('c1 is stale: challenged, not retired, and still offered in run 09', () => {
    expect(row(C1)?.state).toBe('challenged')
    expect(stateBucket(row(C1)!.state)).toBe('stale')
    expect(row(C1)?.terminal).toBeUndefined()
    const run09 = model.links.filter((link) => link.usedRun === 'relay-20260925-09').map((link) => link.knowledgeId)
    expect(run09).toContain(C1)
  })

  it('run 09 recalls the living truth and neither dead auth experience', () => {
    const run09 = model.links.filter((link) => link.usedRun === 'relay-20260925-09').map((link) => link.knowledgeId)
    for (const id of [A3, B1, C1, C2]) expect(run09).toContain(id)
    expect(run09).not.toContain(A1)
    expect(run09).not.toContain(A2)
  })
})

// §20 of the phase plan: the forensic view reconstructs, for one knowledge
// item, why it appeared (evidence + prior commands), how it was activated
// (recall → injection → run outcome) and who/what killed it.
describe('Forensic view — formation, activation, death', () => {
  const nightly = foldExperience(NIGHTLYBOX)
  const gate = foldExperience(GATEKEEPER)

  const nightlyRow = (run: string, id: string) => nightly.rows.find((item) => item.knowledgeId === K(run, id))!

  it('K82 formation exposes run, rejected-path evidence and prior commands', () => {
    const { formed } = forensicOf(nightlyRow('nightly-20260925-01b', '82'), nightly)
    expect(formed?.run).toBe('nightly-20260925-01b')
    expect(formed?.evidence.length).toBeGreaterThanOrEqual(2)
    expect(formed?.evidence.some((item) => item.ref.includes('AUTH_TOKEN'))).toBe(true)
    expect(formed?.evidence.some((item) => item.ref.includes('.token-file'))).toBe(true)
    // the failed hypothesis and the verified mechanism are both visible as commands
    expect(formed?.commands.some((command) => command.command.includes('AUTH_TOKEN'))).toBe(true)
    expect(formed?.commands.some((command) => command.command.includes('.token-file'))).toBe(true)
  })

  it('K82 activations carry the consuming run, its outcome and the commands that followed injection', () => {
    const { activations } = forensicOf(nightlyRow('nightly-20260925-01b', '82'), nightly)
    expect(activations.length).toBeGreaterThanOrEqual(3)
    const failed = activations.find((activation) => activation.usedRun === 'nightly-20260925-02')
    expect(failed?.usedRunStatus).toBe('failed')
    const run03 = activations.find((activation) => activation.usedRun === 'nightly-20260925-03')
    expect(run03?.commands.some((command) => command.command.includes('.token-file'))).toBe(true)
  })

  it('K82 death names the human operator, the reason and the successor', () => {
    const { deaths } = forensicOf(nightlyRow('nightly-20260925-01b', '82'), nightly)
    expect(deaths).toHaveLength(1)
    const death = deaths[0]
    expect(death.kind).toBe('archived')
    expect(death.actor).toBe('human-operator')
    expect(death.reason).toMatch(/v3/)
    expect(death.supersededBy).toEqual([K('nightly-20260925-05', '73')])
  })

  it('K77 death in the gatekeeper corpus points at K86', () => {
    const k77 = gate.rows.find((item) => item.knowledgeId === K('gatekeeper-20260925-01', '77'))!
    const { deaths } = forensicOf(k77, gate)
    expect(deaths).toHaveLength(1)
    expect(deaths[0].actor).toBe('human-operator')
    expect(deaths[0].reason).toMatch(/v3/)
    expect(deaths[0].supersededBy).toEqual([K('gatekeeper-20260925-03', '86')])
  })

  it('leaves experiences without recorded evidence lean but intact', () => {
    const k73 = nightly.rows.find((item) => item.knowledgeId === K('nightly-20260925-05', '73'))!
    const record = forensicOf(k73, nightly)
    expect(record.deaths).toHaveLength(0)
    expect(record.formed?.run).toBe('nightly-20260925-05')
  })
})
