/** Harness budget knobs (docs/harness-settings.md §6): the editable budget
 *  surface of the «Харнес» page. The order is the display order; ceilings
 *  mirror the server's budgetCeilings so the form can validate before a
 *  round-trip (the server still has the final say — its 422 message is shown
 *  as-is). */

export interface HarnessKnob {
  key: string
  /** 'bytes' knobs get the «≈ … КиБ» readout; 'count' knobs are plain numbers. */
  kind: 'bytes' | 'count'
  /** Inclusive per-key ceiling, mirrored from the server. */
  ceiling: number
}

/** The ceiling for byte knobs unless a key needs a tighter/looser bound. */
const BYTES_CEILING = 67108864 // 64 MiB

export const HARNESS_KNOBS: HarnessKnob[] = [
  // 2 MiB: above the Temporal payload limit the run wedges in TMPRL1103 retries.
  { key: 'max_model_request_bytes', kind: 'bytes', ceiling: 2097152 },
  { key: 'compaction_trigger_bytes', kind: 'bytes', ceiling: BYTES_CEILING },
  { key: 'compaction_card_max_bytes', kind: 'bytes', ceiling: BYTES_CEILING },
  { key: 'compaction_tail_bytes', kind: 'bytes', ceiling: BYTES_CEILING },
  { key: 'compaction_keep_groups_min', kind: 'count', ceiling: 64 },
  { key: 'compaction_keep_groups_max', kind: 'count', ceiling: 64 },
  { key: 'compaction_digest_head_bytes', kind: 'bytes', ceiling: BYTES_CEILING },
  { key: 'compaction_digest_tail_bytes', kind: 'bytes', ceiling: BYTES_CEILING },
  { key: 'tool_output_max_bytes', kind: 'bytes', ceiling: BYTES_CEILING },
  { key: 'tool_output_tail_bytes', kind: 'bytes', ceiling: BYTES_CEILING },
  // 512 MiB: the execution-history budget that triggers ContinueAsNew.
  { key: 'continue_as_new_bytes', kind: 'bytes', ceiling: 536870912 },
]

/** Rough human readout for byte knobs: 512 → «512 Б», 16384 → «16 КиБ»,
 *  2097152 → «2 МиБ» (locale picks Б/КиБ/МиБ vs B/KiB/MiB). */
export function formatBytes(value: number, locale: string): string {
  const units = locale === 'ru' ? ['Б', 'КиБ', 'МиБ'] : ['B', 'KiB', 'MiB']
  const rounded = (n: number) => String(Math.round(n * 10) / 10)
  if (value < 1024) return `${value} ${units[0]}`
  if (value < 1024 * 1024) return `${rounded(value / 1024)} ${units[1]}`
  return `${rounded(value / (1024 * 1024))} ${units[2]}`
}

/** A validation failure the UI localizes as quality.harness_err_<code>. */
export interface KnobError {
  code: 'integer' | 'min' | 'max' | 'keep_groups' | 'trigger'
  vars?: Record<string, string>
}

/** Client-side mirror of the server budget validation: whole numbers, ≥ 1,
 *  per-key ceiling, plus the cross-checks compaction_keep_groups_min ≤
 *  compaction_keep_groups_max and compaction_trigger_bytes ≤
 *  max_model_request_bytes. Returns per-key errors: value errors point at the
 *  knob, cross-checks point at the constrained knob. */
export function validateBudgetDrafts(drafts: Record<string, string>, effective: Record<string, number>): Record<string, KnobError> {
  const errors: Record<string, KnobError> = {}
  const values: Record<string, number> = {}
  for (const knob of HARNESS_KNOBS) {
    const raw = (drafts[knob.key] ?? '').trim()
    if (!/^\d+$/.test(raw)) { errors[knob.key] = { code: 'integer' }; continue }
    const value = Number(raw)
    if (value < 1) { errors[knob.key] = { code: 'min' }; continue }
    if (value > knob.ceiling) { errors[knob.key] = { code: 'max', vars: { max: String(knob.ceiling) } }; continue }
    values[knob.key] = value
  }
  // Untouched knobs fall back to the effective value so a single edit can
  // still trip a cross-check against its counterpart.
  const at = (key: string) => values[key] ?? effective[key]
  const min = at('compaction_keep_groups_min')
  const max = at('compaction_keep_groups_max')
  if (typeof min === 'number' && typeof max === 'number' && min > max && !errors['compaction_keep_groups_min']) {
    errors['compaction_keep_groups_min'] = { code: 'keep_groups' }
  }
  const trigger = at('compaction_trigger_bytes')
  const request = at('max_model_request_bytes')
  if (typeof trigger === 'number' && typeof request === 'number' && trigger > request && !errors['compaction_trigger_bytes']) {
    errors['compaction_trigger_bytes'] = { code: 'trigger' }
  }
  return errors
}

/** The PUT harness map: changed keys as numbers, staged resets as nulls,
 *  untouched keys omitted (the server keeps them as-is). */
export function budgetEdits(drafts: Record<string, string>, effective: Record<string, number>, stagedResets: ReadonlySet<string>): Record<string, number | null> {
  const edits: Record<string, number | null> = {}
  for (const key of stagedResets) edits[key] = null
  for (const knob of HARNESS_KNOBS) {
    if (stagedResets.has(knob.key)) continue
    const raw = (drafts[knob.key] ?? '').trim()
    if (!/^\d+$/.test(raw)) continue
    const value = Number(raw)
    if (value !== effective[knob.key]) edits[knob.key] = value
  }
  return edits
}
