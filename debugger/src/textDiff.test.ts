import { describe, expect, it } from 'vitest'
import { diffText } from './textDiff'

describe('diffText', () => {
  it('keeps indentation, case, punctuation and blank lines significant', () => {
    for (const [before, after] of [
      ['  key: value', '    key: value'],
      ['key: Value', 'key: value'],
      ['key: "value"', 'key: value'],
      ['a\nb', 'a\n\nb'],
      ['a', 'a\n'],
    ]) {
      const diff = diffText(before, after)
      expect(diff.counts.modified + diff.counts.added + diff.counts.removed).toBeGreaterThan(0)
    }
  })

  it('aligns unchanged lines between edits', () => {
    const diff = diffText('a\nx\nb\ny\nc', 'a\nX\nb\nY\nc')
    expect(diff.rows.map((row) => row.kind)).toEqual(['same', 'modified', 'same', 'modified', 'same'])
  })

  it('handles empty documents, additions, removals and CRLF', () => {
    expect(diffText('', '').rows).toEqual([])
    expect(diffText('', 'a').counts.added).toBe(1)
    expect(diffText('a', '').counts.removed).toBe(1)
    expect(diffText('a\r\nb', 'a\nb').counts.modified).toBe(0)
  })

  it('bounds memory for large changed regions without dropping source', () => {
    const before = Array(1100).fill('old').join('\n')
    const after = Array(1100).fill('new').join('\n')
    const diff = diffText(before, after)
    expect(diff.rows).toHaveLength(1)
    expect(diff.rows[0].oldBlock?.text).toBe(before)
    expect(diff.rows[0].newBlock?.text).toBe(after)
  })
})
