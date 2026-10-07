import { describe, expect, it } from 'vitest'
import type { ObservationEvent } from './observationApi'
import { aliveRowAt, foldExperience, forensicOf, stateBucket } from './experience'
import exp4 from './fixtures/experiment4-gatekeeper.json'
import exp5 from './fixtures/experiment5-nightlybox.json'
import exp6 from './fixtures/experiment6-relay.json'
import exp7 from './fixtures/experiment7-forge.json'
import exp8 from './fixtures/experiment8-lighthouse.json'
import exp9 from './fixtures/experiment9-webconfig.json'

const GATEKEEPER = exp4 as unknown as ObservationEvent[]
const NIGHTLYBOX = exp5 as unknown as ObservationEvent[]
const RELAY = exp6 as unknown as ObservationEvent[]
const FORGE = exp7 as unknown as ObservationEvent[]
const LIGHTHOUSE = exp8 as unknown as ObservationEvent[]
const WEBCONFIG = exp9 as unknown as ObservationEvent[]

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

// Experiment 7 (§ examples/experiment7/README.md): the scale corpus. Five
// forge mechanisms, two of which flip in v2; 16 runs; reinforcement loops
// (stable experiences recalled in EVERY later run); a competing build pair
// (b1 challenged, b2 appears while b1 is still alive, b1 dies only after the
// operator resolves the competition); and the budget-displacement finding —
// the kernel's fixed hint budget let stable auto observations crowd the
// corrected v2 claims out of recall entirely, so run 13 re-derived pack
// semantics and recorded a duplicate. The displacement assertions pin the
// OBSERVED behaviour (used=0), not the desired one.
describe('Experiment 7 fixture — reinforcement, competing pair, scale', () => {
  const model = foldExperience(FORGE)

  const A1 = K('forge-20260925-01', '99') // login via FORGE_TOKEN
  const P1 = K('forge-20260925-02', '90') // publish requires CHANNEL
  const B1 = K('forge-20260925-03', '89') // build v1: no cache layer
  const K1 = K('forge-20260925-04', '69') // pack v1: dist.tar.gz
  const S1 = K('forge-20260925-05', '104') // sign v1: SIGN_KEY
  const E1 = K('forge-20260925-06', '102') // end-to-end v1
  const B2 = K('forge-20260925-08b', '118') // build v2: cache layer
  const K2 = K('forge-20260925-09', '99') // pack v2: dist.zip
  const S2 = K('forge-20260925-10b', '88') // sign v2: SIGNING_KEY
  const E2 = K('forge-20260925-11', '105') // end-to-end v2
  const E3 = K('forge-20260925-12', '112') // end-to-end v3
  const DUP = K('forge-20260925-13', '99') // duplicate pack v2 rediscovery
  const GO_TEST = 'auto/36ddc4e27eb32101c7b39641'

  const row = (id: string) => model.rows.find((item) => item.knowledgeId === id)
  const usedIn = (id: string) => model.links.filter((link) => link.knowledgeId === id).map((link) => link.usedRun)

  it('folds the recorded corpus shape', () => {
    expect(model.totals.events).toBe(1884)
    expect(model.rows).toHaveLength(20)
    expect(model.runs).toHaveLength(16)
    expect(model.links).toHaveLength(110)
  })

  it('reinforcement: stable experiences are recalled in every later run', () => {
    expect(usedIn(A1)).toHaveLength(15)
    expect(usedIn(P1)).toHaveLength(14)
    expect(usedIn(GO_TEST)).toHaveLength(15)
  })

  it('the build pair competes: b2 appears while b1 is alive, b1 dies later', () => {
    const b1 = row(B1)!
    const b2 = row(B2)!
    expect(b1.scopes.primary).toBe('build')
    expect(b2.scopes.primary).toBe('build')
    expect(b2.terminal).toBeUndefined()
    // b2 appeared in 08b; b1's invalidation only came after run 12
    expect(b1.terminal).toBeDefined()
    expect(b2.firstAt < b1.terminal!.at).toBe(true)
    // the operator challenge of run 07 is visible in b1's lifecycle points
    expect(b1.points.map((point) => point.kind)).toContain('contradicted')
  })

  it('derives the full supersession set from operator invalidations', () => {
    expect(model.lineage.map((edge) => `${edge.fromId}->${edge.toId}`).sort()).toEqual(
      [`${B1}->${B2}`, `${E1}->${E2}`, `${K1}->${K2}`, `${S1}->${S2}`].sort(),
    )
    expect(model.lineage.every((edge) => edge.inferred)).toBe(true)
  })

  it('selective death: v1 mechanisms die, login and publish survive', () => {
    for (const id of [B1, K1, S1, E1]) expect(row(id)?.state).toBe('invalidated')
    for (const id of [A1, P1, B2, K2, S2, E2, E3]) expect(row(id)?.terminal).toBeUndefined()
  })

  it('runs 13/14 recall only living experiences', () => {
    for (const runId of ['forge-20260925-13', 'forge-20260925-14']) {
      const got = model.links.filter((link) => link.usedRun === runId).map((link) => link.knowledgeId)
      for (const dead of [B1, K1, S1, E1]) expect(got).not.toContain(dead)
      for (const alive of [A1, P1, B2]) expect(got).toContain(alive)
    }
  })

  it('hint budget displacement: corrected v2 claims never recalled, run 13 duplicates pack', () => {
    for (const id of [K2, S2, E2, E3]) expect(usedIn(id)).toHaveLength(0)
    expect(row(DUP)?.proposition).toMatch(/pack/)
    expect(row(DUP)?.scopes.primary).toBe('end-to-end')
  })

  it('derives per-mechanism lanes for the forge corpus', () => {
    expect(model.scopes.map((scope) => scope.id).sort()).toEqual(['build', 'end-to-end', 'login', 'pack', 'publish', 'sign', 'test'])
    expect(row(A1)?.scopes.primary).toBe('login')
    expect(row(P1)?.scopes.primary).toBe('publish')
    expect(row(K1)?.scopes.primary).toBe('pack')
    expect(row(K2)?.scopes.primary).toBe('pack')
    expect(row(S1)?.scopes.primary).toBe('sign')
    expect(row(S2)?.scopes.primary).toBe('sign')
    expect(row(B1)?.scopes.primary).toBe('build')
    expect(row(B2)?.scopes.primary).toBe('build')
    expect(row(E1)?.scopes.primary).toBe('end-to-end')
    expect(row(E2)?.scopes.primary).toBe('end-to-end')
    expect(row(E3)?.scopes.primary).toBe('end-to-end')
  })
})

// Experiment 8 (§ examples/experiment8/README.md): the fourth acceptance
// corpus targets LONG-HORIZON evolution across three versions of one CLI —
// a resurrection chain (login flips LH_TOKEN → .lh-token → LH_TOKEN back),
// a composite note whose deploy half died while its report half was re-derived
// as new knowledge, competing corrections that coexist for several runs, and
// kernel-side dedup of canonicalized build commands into a single auto node.
describe('Experiment 8 fixture — long-horizon evolution, resurrection, composite death', () => {
  const model = foldExperience(LIGHTHOUSE)

  const A1 = K('lighthouse-20260925-01', '101') // v1 login: LH_TOKEN env
  const D1 = K('lighthouse-20260925-02', '83') // v1 composite: deploy --env + report.txt
  const E1 = K('lighthouse-20260925-03', '43') // v1 pipeline
  const A2 = K('lighthouse-20260925-04', '55') // v2 login: .lh-token file
  const D2 = K('lighthouse-20260925-05', '56') // v2 deploy: LH_STAGE
  const E2 = K('lighthouse-20260925-06', '48') // v2 pipeline
  const H1 = K('lighthouse-20260925-07', '92') // harness quirks (stable anchor)
  const A3 = K('lighthouse-20260925-08', '41') // v3 login: LH_TOKEN again (resurrection)
  const R2 = K('lighthouse-20260925-09', '37') // v3 report: --format json
  const E3 = K('lighthouse-20260925-10', '49') // v3 pipeline
  const C1 = K('lighthouse-20260925-11', '43') // final supersession summary
  const BUILD = 'auto/eb2d265c3abc778ffbd624ee'

  const row = (id: string) => model.rows.find((item) => item.knowledgeId === id)
  const usedIn = (id: string) => model.links.filter((link) => link.knowledgeId === id).map((link) => link.usedRun)
  const edges = () => model.lineage.map((edge) => `${edge.fromId}->${edge.toId}`)

  it('folds the recorded corpus shape', () => {
    expect(model.totals.events).toBe(834)
    expect(model.rows).toHaveLength(13)
    expect(model.runs).toHaveLength(11)
    expect(model.links).toHaveLength(52)
  })

  it('resurrection: login flips LH_TOKEN → token-file → LH_TOKEN, only the last note lives', () => {
    expect(row(A1)?.proposition).toMatch(/LH_TOKEN/)
    expect(row(A1)?.state).toBe('invalidated')
    expect(row(A2)?.state).toBe('invalidated')
    expect(row(A3)?.terminal).toBeUndefined()
    // the resurrected claim restores the v1 mechanism in a new living node
    expect(row(A3)?.proposition).toMatch(/LH_TOKEN/)
    expect(row(A3)?.proposition).toMatch(/\.lh-token/)
    expect(edges()).toContain(`${A1}->${A2}`)
    expect(edges()).toContain(`${A2}->${A3}`)
  })

  it('composite death: the v1 deploy+report note dies in two steps with two successors', () => {
    expect(row(D1)?.state).toBe('invalidated')
    const { deaths } = forensicOf(row(D1)!, model)
    expect(deaths).toHaveLength(2)
    // first the operator challenges the stale deploy half without replacing it
    expect(deaths[0].kind).toBe('contradicted')
    expect(deaths[0].supersededBy).toEqual([])
    // then the invalidation names two refs: the v2 deploy fix and the v2 pipeline
    expect(deaths[1].kind).toBe('archived')
    expect(deaths[1].supersededBy).toEqual([D2, E2])
    expect(edges()).toContain(`${D1}->${D2}`)
    expect(edges()).toContain(`${D1}->${E2}`)
  })

  it('pipeline notes supersede per flip; only the v3 pipeline survives', () => {
    expect(row(E1)?.state).toBe('invalidated')
    expect(row(E2)?.state).toBe('invalidated')
    expect(row(E3)?.terminal).toBeUndefined()
    expect(edges()).toContain(`${E1}->${E2}`)
    expect(edges()).toContain(`${E2}->${E3}`)
  })

  it('derives the full supersession set from operator invalidations', () => {
    expect(edges().sort()).toEqual(
      [`${A1}->${A2}`, `${A2}->${A3}`, `${D1}->${D2}`, `${D1}->${E2}`, `${E1}->${E2}`, `${E2}->${E3}`].sort(),
    )
    expect(model.lineage.every((edge) => edge.inferred)).toBe(true)
  })

  it('competing deploy notes coexist until the operator retires the v1 one after run 07', () => {
    expect(usedIn(D1)).toEqual([
      'lighthouse-20260925-03',
      'lighthouse-20260925-04',
      'lighthouse-20260925-05',
      'lighthouse-20260925-06',
      'lighthouse-20260925-07',
    ])
    expect(usedIn(D2)).toEqual([
      'lighthouse-20260925-06',
      'lighthouse-20260925-07',
      'lighthouse-20260925-08',
      'lighthouse-20260925-09',
      'lighthouse-20260925-10',
      'lighthouse-20260925-11',
    ])
  })

  it('the harness note is a stable anchor for the late corpus', () => {
    expect(usedIn(H1)).toEqual([
      'lighthouse-20260925-08',
      'lighthouse-20260925-09',
      'lighthouse-20260925-10',
      'lighthouse-20260925-11',
    ])
  })

  it('kernel dedup: every build invocation folds into one canonical auto node', () => {
    expect(model.rows.filter((item) => item.scopes.primary === 'build')).toHaveLength(1)
    expect(row(BUILD)?.command).toBe('go build -o OUT .')
    expect(usedIn(BUILD)).toHaveLength(7)
    // the stable verification command stays confirmed across the whole corpus
    expect(model.rows.filter((item) => item.scopes.primary === 'test')).toHaveLength(1)
  })

  it('run 11 recalls exactly the living claims and none of the dead experiences', () => {
    const got = model.links
      .filter((link) => link.usedRun === 'lighthouse-20260925-11')
      .map((link) => link.knowledgeId)
    for (const dead of [A1, D1, E1, A2, E2]) expect(got).not.toContain(dead)
    for (const alive of [D2, H1, A3, R2, E3]) expect(got).toContain(alive)
    // the audit summary itself is formed here and never recalled
    expect(usedIn(C1)).toHaveLength(0)
  })

  it('memory population after selective death: 7 alive rows at run 11', () => {
    const run11 = model.runs.find((run) => run.id === 'lighthouse-20260925-11')
    expect(aliveRowAt(model.rows, run11!.startedAt)).toBe(7)
  })

  it('derives mechanism lanes for the lighthouse corpus', () => {
    expect(model.scopes.map((scope) => scope.id).sort()).toEqual(['build', 'end-to-end', 'report', 'test'])
    const report = model.scopes.find((scope) => scope.id === 'report')!
    expect(report.rows.map((item) => item.knowledgeId).sort()).toEqual([E1, E2, R2, E3].sort())
  })

  it('forensic: the resurrected note forms in run 08 and activates cleanly', () => {
    const record = forensicOf(row(A3)!, model)
    expect(record.formed?.run).toBe('lighthouse-20260925-08')
    expect(record.activations).toHaveLength(3)
    expect(record.activations.every((activation) => activation.usedRunStatus === 'completed')).toBe(true)
    expect(record.deaths).toHaveLength(0)
  })

  it('the v2 login death records the revert and names the resurrecting successor', () => {
    const { deaths } = forensicOf(row(A2)!, model)
    expect(deaths).toHaveLength(1)
    expect(deaths[0].actor).toBe('human-operator')
    expect(deaths[0].reason).toMatch(/revert/i)
    expect(deaths[0].supersededBy).toEqual([A3])
  })
})

// Experiment 9 (docs/experiment-9-corpus.md): the comprehension corpus. Three
// competing configuration hypotheses (env var / config file / flag) live
// through repeated confirmation, contradiction, selective death, stale use,
// supersession and resurrection, spread over five mechanism scope lanes. Every
// assertion mirrors one acceptance question the operator must answer from the
// Experience Timeline alone.
describe('Experiment 9 fixture — competing hypotheses, resurrection, stale use', () => {
  const model = foldExperience(WEBCONFIG)

  const A1 = K('webconfig-20261007-01', '41') // env var DATABASE_URL (v1)
  const B1 = K('webconfig-20261007-02', '43') // config.yaml (competing, survives)
  const C1 = K('webconfig-20261007-03', '47') // --db-url flag (dies in v2)
  const E1 = K('webconfig-20261007-04', '51') // end-to-end v2 pipeline
  const A2 = K('webconfig-20261007-06', '57') // v3 restores the env mechanism
  const AUDIT = K('webconfig-20261007-07', '61') // final audit note, never recalled
  const GO_TEST = 'auto/9b4e27c0d8a1f536b0e7c2d4'
  const GO_BUILD = 'auto/3f9c1d84a2b7e6d50c91f4a2'

  const row = (id: string) => model.rows.find((item) => item.knowledgeId === id)
  const usedIn = (id: string) => model.links.filter((link) => link.knowledgeId === id && link.usedRun).map((link) => link.usedRun)
  const pointsOf = (id: string, kind: string) => row(id)!.points.filter((point) => point.kind === kind)

  it('folds the recorded corpus shape', () => {
    expect(model.totals.events).toBe(161)
    expect(model.rows).toHaveLength(8)
    expect(model.runs).toHaveLength(8)
    expect(model.links).toHaveLength(22)
  })

  it('which hypothesis appeared first, and which died', () => {
    expect(appearedIn(WEBCONFIG, A1)).toBe('webconfig-20261007-01')
    expect(row(A1)!.firstAt < row(B1)!.firstAt).toBe(true)
    expect(row(B1)!.firstAt < row(C1)!.firstAt).toBe(true)
    expect(row(C1)!.state).toBe('invalidated')
    expect(row(C1)!.terminal).toBeDefined()
    expect(row(B1)!.terminal).toBeUndefined()
  })

  it('repeated confirmation: A1 validated once by re-derivation, B1 twice after the flip', () => {
    expect(pointsOf(A1, 'validated')).toHaveLength(1)
    expect(pointsOf(A1, 'validated').every((point) => point.rule === 'extraction-reverification.v1')).toBe(true)
    expect(pointsOf(B1, 'validated')).toHaveLength(2)
    expect(pointsOf(B1, 'validated').map((point) => point.run)).toEqual(['webconfig-20261007-04', 'webconfig-20261007-05'])
  })

  it('contradiction: the flag hypothesis challenges both elders with evidence-carrying rules', () => {
    for (const id of [A1, B1]) {
      const contradicted = pointsOf(id, 'contradicted')
      expect(contradicted).toHaveLength(1)
      expect(contradicted[0].run).toBe('webconfig-20261007-03')
      expect(contradicted[0].rule).toBe('extraction-contradiction.v1')
    }
    expect(row(C1)!.firstAt < pointsOf(B1, 'contradicted')[0].at).toBe(true)
  })

  it('stale knowledge: challenged A1 is still offered, consumed in run 04 and offered-but-ignored in run 05', () => {
    expect(usedIn(A1)).toEqual(['webconfig-20261007-02', 'webconfig-20261007-04'])
    const used04 = model.links.find((link) => link.knowledgeId === A1 && link.usedRun === 'webconfig-20261007-04')
    expect(used04).toBeDefined()
    // the death only comes later, so run 04 consumed a challenged (stale) item
    expect(row(A1)!.terminal!.at > used04!.usedAt!).toBe(true)
    // run 05 offers A1 again but the agent ignores it (link without usedRun)
    const ignored05 = model.links.find((link) => link.knowledgeId === A1 && link.offeredRun === 'webconfig-20261007-05' && !link.usedRun)
    expect(ignored05).toBeDefined()
  })

  it('supersession and resurrection: the v1 env note dies naming its v3 restatement', () => {
    expect(row(A1)!.state).toBe('invalidated')
    expect(row(A2)!.state).toBe('proposed')
    expect(row(A2)!.terminal).toBeUndefined()
    expect(row(A1)!.proposition).toMatch(/DATABASE_URL/)
    expect(row(A2)!.proposition).toMatch(/DATABASE_URL/)
    expect(model.lineage.map((edge) => `${edge.fromId}->${edge.toId}`)).toEqual([`${A1}->${A2}`])
    // C1 dies without a direct replacement: nothing supersedes the dead flag note
    const { deaths } = forensicOf(row(C1)!, model)
    expect(deaths).toHaveLength(1)
    expect(deaths[0].kind).toBe('archived')
    expect(deaths[0].actor).toBe('human-operator')
    expect(deaths[0].supersededBy).toEqual([])
  })

  it('different scopes: five mechanism lanes with the competing pair sharing serve', () => {
    expect(model.scopes.map((scope) => scope.id).sort()).toEqual(['build', 'end-to-end', 'migrate', 'serve', 'test'])
    expect(row(A1)!.scopes.primary).toBe('migrate')
    expect(row(A2)!.scopes.primary).toBe('migrate')
    expect(row(B1)!.scopes.primary).toBe('serve')
    expect(row(C1)!.scopes.primary).toBe('serve')
    expect(row(E1)!.scopes.primary).toBe('end-to-end')
    expect(row(E1)!.scopes.secondary).toEqual(expect.arrayContaining(['build', 'migrate', 'serve']))
    expect(row(GO_TEST)!.scopes.primary).toBe('test')
    expect(row(GO_BUILD)!.scopes.primary).toBe('build')
    const serve = model.scopes.find((scope) => scope.id === 'serve')!
    expect(serve.rows.map((item) => item.knowledgeId).sort()).toEqual([B1, C1].sort())
  })

  it('what is valid now and what was actually used: run 08 recalls the living set', () => {
    const got = model.links.filter((link) => link.usedRun === 'webconfig-20261007-08').map((link) => link.knowledgeId)
    for (const dead of [A1, C1]) expect(got).not.toContain(dead)
    for (const alive of [A2, B1, E1, GO_TEST, GO_BUILD]) expect(got).toContain(alive)
    expect(usedIn(AUDIT)).toEqual([])
    // kernel-side execution knowledge: go test confirmed once, then reused by rule
    expect(row(GO_TEST)!.state).toBe('confirmed')
    expect(pointsOf(GO_TEST, 'reused').every((point) => point.rule === 'execution-reuse.v1')).toBe(true)
    expect(pointsOf(GO_TEST, 'reused').map((point) => point.run)).toEqual([
      'webconfig-20261007-04',
      'webconfig-20261007-05',
      'webconfig-20261007-07',
      'webconfig-20261007-08',
    ])
  })

  it('memory population after the flips: 6 alive rows at run 08', () => {
    const run08 = model.runs.find((run) => run.id === 'webconfig-20261007-08')
    expect(run08).toBeDefined()
    expect(aliveRowAt(model.rows, run08!.startedAt)).toBe(6)
  })

  it('forensic: the resurrected note forms in run 06 with clean activations', () => {
    const record = forensicOf(row(A2)!, model)
    expect(record.formed?.run).toBe('webconfig-20261007-06')
    expect(record.activations).toHaveLength(2)
    expect(record.activations.every((activation) => activation.usedRunStatus === 'completed')).toBe(true)
    expect(record.deaths).toHaveLength(0)
    // the v1 note is first contradicted by the flag run, then archived by the
    // operator naming the resurrected successor
    const v1 = forensicOf(row(A1)!, model)
    expect(v1.deaths).toHaveLength(2)
    expect(v1.deaths[0].kind).toBe('contradicted')
    expect(v1.deaths[0].run).toBe('webconfig-20261007-03')
    expect(v1.deaths[1].kind).toBe('archived')
    expect(v1.deaths[1].actor).toBe('human-operator')
    expect(v1.deaths[1].supersededBy).toEqual([A2])
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
