import { describe, expect, it } from 'vitest'
import { fullDateTime } from './timeHint'

/** The hover hint must carry the full date — «today or yesterday?» is the
 *  question every bare clock time in the UI leaves open. */
describe('fullDateTime', () => {
  it('formats a valid ISO stamp with date and time parts', () => {
    const text = fullDateTime('2026-11-05T06:21:10.000Z')
    expect(text).toContain('2026')
    expect(text).not.toBe('')
  })

  it('keeps the locale-independent contract: non-empty for valid, empty for garbage', () => {
    expect(fullDateTime(new Date('2026-11-05T06:21:10.000Z'))).not.toBe('')
    expect(fullDateTime('not-a-date')).toBe('')
    expect(fullDateTime('')).toBe('')
  })
})
