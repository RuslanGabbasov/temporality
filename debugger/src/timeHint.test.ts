// @vitest-environment jsdom
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

  // The UI culture, not the browser default: a Russian interface must not
  // render «Fri, Nov 6» — the artifact dates bug (Nov 2026).
  it('follows the stored UI locale (ru renders a Russian month)', () => {
    const fixed = new Date(2026, 10, 6, 17, 20, 37) // local Nov 6 2026, no TZ drift
    localStorage.setItem('temporality_locale', 'ru')
    try {
      const text = fullDateTime(fixed)
      expect(text).toMatch(/нояб/)
      expect(text).not.toMatch(/Nov/)
    } finally {
      localStorage.removeItem('temporality_locale')
    }
  })
})
