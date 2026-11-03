import { describe, expect, it } from 'vitest'
import { sideBySideDiff } from './diffRows'

describe('sideBySideDiff', () => {
  it('has no rows and zero counters for identical texts', () => {
    expect(sideBySideDiff('a\nb', 'a\nb')).toEqual({ rows: [], added: 0, removed: 0 })
  })

  it('places a rewritten line opposite its replacement', () => {
    const { rows, added, removed } = sideBySideDiff('a\nb\nc', 'a\nB\nc')
    expect({ added, removed }).toEqual({ added: 1, removed: 1 })
    expect(rows).toContainEqual({
      kind: 'changed',
      left: { lineNumber: 2, text: 'b' },
      right: { lineNumber: 2, text: 'B' },
    })
  })

  it('leaves the opposite side empty for pure additions and removals', () => {
    const addition = sideBySideDiff('a', 'a\nb')
    expect(addition.rows).toContainEqual({ kind: 'changed', left: null, right: { lineNumber: 2, text: 'b' } })

    const removal = sideBySideDiff('a\nb', 'a')
    expect(removal.rows).toContainEqual({ kind: 'changed', left: { lineNumber: 2, text: 'b' }, right: null })
  })

  it('pairs unequal blocks row by row and pads the shorter side', () => {
    const { rows, added, removed } = sideBySideDiff('x\ny\nz', 'p')
    expect({ added, removed }).toEqual({ added: 1, removed: 3 })
    expect(rows).toHaveLength(3)
    expect(rows[1]).toEqual({ kind: 'changed', left: { lineNumber: 2, text: 'y' }, right: null })
  })

  it('collapses long unchanged runs into a gap row keeping three lines of context', () => {
    const lines = Array.from({ length: 20 }, (_, i) => `line ${i + 1}`)
    const changed = [...lines]
    changed[9] = 'changed'
    const { rows } = sideBySideDiff(lines.join('\n'), changed.join('\n'))
    expect(rows.filter((row) => row.kind === 'gap')).toEqual([
      { kind: 'gap', hidden: 6 },
      { kind: 'gap', hidden: 7 },
    ])
  })

  it('treats CRLF and LF as the same line endings', () => {
    expect(sideBySideDiff('a\r\nb', 'a\nb').rows).toEqual([])
  })
})
