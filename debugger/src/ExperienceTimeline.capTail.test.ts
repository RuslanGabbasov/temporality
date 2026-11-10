import { describe, expect, it } from 'vitest'
import { capTail } from './ExperienceTimeline'

/** Timeline lane labels live in a fixed-width SVG gutter next to the ↗ jump
 *  button: a long run title must be capped (with an ellipsis, newlines
 *  squashed) instead of overflowing over the button. The suffixes (+N runs,
 *  failed ✕) are appended outside the cap so they survive any title. */
describe('capTail', () => {
  it('leaves short titles alone', () => {
    expect(capTail('forge 13', 24)).toBe('forge 13')
  })

  it('caps long titles with an ellipsis and squashes newlines', () => {
    const long = 'very long run title\nspanning several lines of the task prompt'
    const capped = capTail(long, 24)
    expect(capped).toHaveLength(24)
    expect(capped.endsWith('…')).toBe(true)
    expect(capped).not.toContain('\n')
  })

  it('the suffix budget keeps +N and ✕ visible outside the cap', () => {
    const title = 'an enormously verbose task title that overflows everything'
    const suffix = ' +3 ✕'
    const capped = capTail(title, 24 - suffix.length) + suffix
    expect(capped.endsWith(suffix)).toBe(true)
    expect(capped).toHaveLength(24)
  })
})
