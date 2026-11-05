import { describe, expect, it } from 'vitest'
import {
  isCostTimeMetric,
  isLowerBetter,
  isSignalMark,
  metricDeltaText,
  metricValueText,
  promptDiffTone,
  verdictTagType,
  versionKindLabelKey,
  versionKindTagType,
} from './agentEvolution'

describe('isSignalMark', () => {
  it('folds the §5 string markers', () => {
    expect(isSignalMark('signal')).toBe(true)
    expect(isSignalMark('noise')).toBe(false)
  })
  it('folds the bool a rollup may send instead', () => {
    expect(isSignalMark(true)).toBe(true)
    expect(isSignalMark(false)).toBe(false)
    expect(isSignalMark(undefined)).toBe(false)
  })
})

describe('isCostTimeMetric', () => {
  it('flags the informational cost/time metrics', () => {
    expect(isCostTimeMetric('tokens_avg')).toBe(true)
    expect(isCostTimeMetric('cost_avg')).toBe(true)
    expect(isCostTimeMetric('cost_usd_avg')).toBe(true)
    expect(isCostTimeMetric('wall_avg')).toBe(true)
    expect(isCostTimeMetric('wall_ms_avg')).toBe(true)
    expect(isCostTimeMetric('success_rate')).toBe(false)
    expect(isCostTimeMetric('incident_rate')).toBe(false)
  })
})

describe('isLowerBetter', () => {
  it('rates success as the only higher-is-better SLI', () => {
    expect(isLowerBetter('success_rate')).toBe(false)
    expect(isLowerBetter('correction_rate')).toBe(true)
    expect(isLowerBetter('incident_rate')).toBe(true)
  })
})

describe('metricValueText', () => {
  it('renders rates as percents', () => {
    expect(metricValueText('success_rate', 0.83)).toBe('83%')
    expect(metricValueText('correction_rate', 0.25)).toBe('25%')
    expect(metricValueText('success_rate', 1)).toBe('100%')
  })
  it('renders tokens as grouped counts and cost in dollars', () => {
    expect(metricValueText('tokens_avg', 41000)).toBe('41 000')
    expect(metricValueText('cost_avg', 0.21)).toBe('$0.21')
    expect(metricValueText('cost_usd_avg', 1.5)).toBe('$1.50')
  })
  it('renders wall-time through the injected duration formatter', () => {
    expect(metricValueText('wall_avg', 183000, (ms) => `${ms / 1000}s`)).toBe('183s')
    // Without one the plain seconds fallback stands.
    expect(metricValueText('wall_ms_avg', 183000)).toBe('183s')
  })
  it('keeps unknown metrics numeric and non-finite values honest', () => {
    expect(metricValueText('reworks', 2)).toBe('2.00')
    expect(metricValueText('reworks', Number.NaN)).toBe('—')
  })
})

describe('metricDeltaText', () => {
  it('signs deltas and formats them in the metric unit', () => {
    expect(metricDeltaText('success_rate', -0.07)).toBe('-7%')
    expect(metricDeltaText('correction_rate', 0.14)).toBe('+14%')
    expect(metricDeltaText('cost_avg', 0.05)).toBe('+$0.05')
  })
  it('drops the sign when the formatted delta is zero', () => {
    expect(metricDeltaText('success_rate', -0.001)).toBe('0%')
    expect(metricDeltaText('tokens_avg', 0)).toBe('0')
  })
})

describe('verdictTagType', () => {
  it('maps the §3.1 verdicts to tag colors', () => {
    expect(verdictTagType('improved')).toBe('green')
    expect(verdictTagType('stable')).toBe('cyan')
    expect(verdictTagType('degraded')).toBe('red')
    expect(verdictTagType('insufficient')).toBe('warm-gray')
    expect(verdictTagType(null)).toBe('gray')
    expect(verdictTagType('')).toBe('gray')
    expect(verdictTagType('mystery')).toBe('gray')
  })
})

describe('version kind badges', () => {
  it('maps kinds to tag colors', () => {
    expect(versionKindTagType('edit')).toBe('blue')
    expect(versionKindTagType('proposal')).toBe('purple')
    expect(versionKindTagType('rollback')).toBe('warm-gray')
    expect(versionKindTagType('mystery')).toBe('gray')
  })
  it('maps known kinds to locale keys and refuses unknown ones', () => {
    expect(versionKindLabelKey('edit')).toBe('agents.kind_edit')
    expect(versionKindLabelKey('proposal')).toBe('agents.kind_proposal')
    expect(versionKindLabelKey('rollback')).toBe('agents.kind_rollback')
    expect(versionKindLabelKey('mystery')).toBeNull()
  })
})

describe('promptDiffTone', () => {
  it('reads the «- » / «+ » prefixes of changed prompt lines', () => {
    expect(promptDiffTone('- Never delete files.')).toBe('del')
    expect(promptDiffTone('+ Never delete files or dirs.')).toBe('add')
    expect(promptDiffTone('  unchanged')).toBe('same')
    expect(promptDiffTone('')).toBe('same')
  })
})
