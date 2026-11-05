// Evolution analytics for agents (docs/agent-evolution.md §6): pure view
// helpers for the «Эволюция» tab — SLI table formatting, verdict/kind badge
// mapping and prompt-diff tone. Kept free of I/O so every rule stays testable.

import type { MetricDelta } from './kernelApi'

/** The Carbon <Tag type> palette — the badge helpers return only these. */
export type TagTone = 'blue' | 'cool-gray' | 'cyan' | 'gray' | 'green' | 'high-contrast' | 'magenta' | 'outline' | 'purple' | 'red' | 'teal' | 'warm-gray'

/** Normalize the §5 «signal» marker («signal» | «noise»); the metrics rollup
 * may also send a plain bool — both fold to "is this a real signal?". */
export function isSignalMark(signal: MetricDelta['signal'] | undefined): boolean {
  return signal === true || signal === 'signal'
}

/** True for SLIs where a negative delta is the bad direction (success rate).
 * Used only for coloring; the signal/noise mark carries the verdict. */
export function isLowerBetter(metric: string): boolean {
  return metric !== 'success_rate'
}

const groupThousands = (value: number): string =>
  String(value).replace(/\B(?=(\d{3})+(?!\d))/g, ' ')

/** True for informational cost/time metrics (§3 «Цена и время»): rendered as
 * informational rows, never carrying a signal/noise mark. */
export function isCostTimeMetric(metric: string): boolean {
  return metric.includes('wall') || metric.includes('cost') || metric.includes('tokens')
}

/** Render one metric cell: rates as percent, tokens as grouped counts, cost
 * in dollars, wall-time through the injected ms formatter (the component
 * passes the locale-aware duration formatter). Unknown codes stay numeric. */
export function metricValueText(metric: string, value: number, fmtMs?: (ms: number) => string): string {
  if (!Number.isFinite(value)) return '—'
  if (metric.endsWith('_rate')) return `${Math.round(value * 100)}%`
  if (metric.includes('wall')) return fmtMs ? fmtMs(value) : `${Math.round(value / 1000)}s`
  if (metric.includes('cost')) return `$${value.toFixed(2)}`
  if (metric.includes('tokens')) return groupThousands(Math.round(value))
  return value.toFixed(2)
}

/** Render a signed delta cell: '+14%' / '-7%' — a delta that formats to the
 * same text as zero carries no sign. */
export function metricDeltaText(metric: string, delta: number, fmtMs?: (ms: number) => string): string {
  const text = metricValueText(metric, Math.abs(delta), fmtMs)
  if (text === metricValueText(metric, 0, fmtMs)) return text
  return `${delta > 0 ? '+' : '-'}${text}`
}

/** Carbon Tag type per observation verdict (§3.1): improved/stable/degraded/
 * insufficient. Unknown or absent verdicts stay neutral gray. */
export function verdictTagType(verdict: string | null | undefined): TagTone {
  switch (verdict) {
    case 'improved': return 'green'
    case 'stable': return 'cyan'
    case 'degraded': return 'red'
    case 'insufficient': return 'warm-gray'
    default: return 'gray'
  }
}

/** Carbon Tag type per version kind (§2.1). */
export function versionKindTagType(kind: string): TagTone {
  switch (kind) {
    case 'edit': return 'blue'
    case 'proposal': return 'purple'
    case 'rollback': return 'warm-gray'
    default: return 'gray'
  }
}

const KIND_LABEL_KEYS: Record<string, string> = {
  edit: 'agents.kind_edit',
  proposal: 'agents.kind_proposal',
  rollback: 'agents.kind_rollback',
}

/** Locale key of a version-kind badge; null for unknown kinds — callers show
 * the raw kind instead of a raw locale key. */
export function versionKindLabelKey(kind: string): string | null {
  return KIND_LABEL_KEYS[kind] ?? null
}

/** Tone of one compiled-prompt diff line: the diff (§4) lists changed lines
 * only, prefixed «- » (old) and «+ » (new). */
export function promptDiffTone(line: string): 'add' | 'del' | 'same' {
  if (line.startsWith('+')) return 'add'
  if (line.startsWith('-')) return 'del'
  return 'same'
}
