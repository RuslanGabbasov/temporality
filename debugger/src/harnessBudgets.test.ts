import { describe, expect, it } from 'vitest'
import { budgetEdits, formatBytes, validateBudgetDrafts } from './harnessBudgets'

describe('formatBytes', () => {
  it('formats the spec examples (ru/en)', () => {
    expect(formatBytes(512, 'ru')).toBe('512 Б')
    expect(formatBytes(16384, 'ru')).toBe('16 КиБ')
    expect(formatBytes(2097152, 'ru')).toBe('2 МиБ')
    expect(formatBytes(512, 'en')).toBe('512 B')
    expect(formatBytes(16384, 'en')).toBe('16 KiB')
    expect(formatBytes(2097152, 'en')).toBe('2 MiB')
  })

  it('rounds fractional scales to one decimal', () => {
    expect(formatBytes(2560, 'en')).toBe('2.5 KiB')
    expect(formatBytes(1024 * 1024 + 512 * 1024, 'ru')).toBe('1.5 МиБ')
  })
})

describe('validateBudgetDrafts', () => {
  // The UI always holds a draft for every knob; fixtures fill the rest.
  const effective: Record<string, number> = {
    max_model_request_bytes: 2097152,
    compaction_trigger_bytes: 262144,
    compaction_card_max_bytes: 65536,
    compaction_tail_bytes: 32768,
    compaction_keep_groups_min: 2,
    compaction_keep_groups_max: 8,
    compaction_digest_head_bytes: 2048,
    compaction_digest_tail_bytes: 2048,
    tool_output_max_bytes: 65536,
    tool_output_tail_bytes: 4096,
    continue_as_new_bytes: 268435456,
  }
  const drafts = (overrides: Record<string, string>) => {
    const out: Record<string, string> = {}
    for (const [key, value] of Object.entries(effective)) out[key] = String(value)
    return { ...out, ...overrides }
  }

  it('accepts valid drafts', () => {
    expect(validateBudgetDrafts(drafts({ max_model_request_bytes: '1048576', compaction_keep_groups_min: '4' }), effective)).toEqual({})
  })

  it('rejects non-integers, values below 1 and values over the ceiling', () => {
    expect(validateBudgetDrafts(drafts({ max_model_request_bytes: 'abc' }), effective)['max_model_request_bytes']?.code).toBe('integer')
    expect(validateBudgetDrafts(drafts({ max_model_request_bytes: '' }), effective)['max_model_request_bytes']?.code).toBe('integer')
    expect(validateBudgetDrafts(drafts({ max_model_request_bytes: '0' }), effective)['max_model_request_bytes']?.code).toBe('min')
    const over = validateBudgetDrafts(drafts({ max_model_request_bytes: '2097153' }), effective)['max_model_request_bytes']
    expect(over?.code).toBe('max')
    expect(over?.vars?.max).toBe('2097152')
    expect(validateBudgetDrafts(drafts({ compaction_keep_groups_min: '65' }), effective)['compaction_keep_groups_min']?.code).toBe('max')
    expect(validateBudgetDrafts(drafts({ continue_as_new_bytes: '536870913' }), effective)['continue_as_new_bytes']?.vars?.max).toBe('536870912')
  })

  it('cross-checks min ≤ max and trigger ≤ request ceiling against effective counterparts', () => {
    expect(validateBudgetDrafts(drafts({ compaction_keep_groups_min: '9' }), effective)['compaction_keep_groups_min']?.code).toBe('keep_groups')
    expect(validateBudgetDrafts(drafts({ compaction_trigger_bytes: '3145728' }), effective)['compaction_trigger_bytes']?.code).toBe('trigger')
    // Lowering the request ceiling below the effective trigger trips the
    // cross-check on the constrained knob.
    expect(validateBudgetDrafts(drafts({ max_model_request_bytes: '65536' }), effective)['compaction_trigger_bytes']?.code).toBe('trigger')
  })
})

describe('budgetEdits', () => {
  const effective = { max_model_request_bytes: 2097152, tool_output_max_bytes: 65536 }

  it('sends changed keys as numbers and staged resets as nulls, omitting the rest', () => {
    const drafts = { max_model_request_bytes: '1048576', tool_output_max_bytes: '65536', compaction_tail_bytes: '' }
    expect(budgetEdits(drafts, effective, new Set(['tool_output_max_bytes']))).toEqual({
      max_model_request_bytes: 1048576,
      tool_output_max_bytes: null,
    })
  })

  it('omits untouched keys', () => {
    expect(budgetEdits({ max_model_request_bytes: '2097152' }, effective, new Set())).toEqual({})
  })
})
