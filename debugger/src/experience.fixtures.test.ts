import { describe, expect, it } from 'vitest'
import type { ObservationEvent } from './observationApi'
import { aliveRowAt, foldExperience } from './experience'
import exp4 from './fixtures/experiment4-gatekeeper.json'
import exp5 from './fixtures/experiment5-nightlybox.json'

const GATEKEEPER = exp4 as unknown as ObservationEvent[]
const NIGHTLYBOX = exp5 as unknown as ObservationEvent[]

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
