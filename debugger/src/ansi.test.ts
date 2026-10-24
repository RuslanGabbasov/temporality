import { describe, expect, it } from 'vitest'
import { parseAnsiSegments, stripAnsi } from './ansi'

describe('parseAnsiSegments', () => {
  it('maps SGR colors to the palette and tracks bold', () => {
    const segments = parseAnsiSegments('\u001b[31mfail\u001b[0m ok')
    expect(segments).toEqual([
      { text: 'fail', color: '#f7768e' },
      { text: ' ok' },
    ])
  })

  it('combines bold with color and clears them independently', () => {
    const segments = parseAnsiSegments('\u001b[1;32mgreen bold\u001b[22mplain green\u001b[39mdefault')
    expect(segments).toEqual([
      { text: 'green bold', color: '#9ece6a', bold: true },
      { text: 'plain green', color: '#9ece6a' },
      { text: 'default' },
    ])
  })

  it('strips cursor movement and OSC payloads entirely', () => {
    // Cursor-position commands vanish, leaving the printed text and its
    // spaces; removing an OSC hyperlink sequence glues its anchor text to
    // the neighbors — that is what stripping means.
    expect(stripAnsi('a\u001b[2K\u001b[1G b c')).toBe('a b c')
    expect(stripAnsi('x\u001b]8;;https://y\u0007z\u001b]8;;\u0007 w')).toBe('xz w')
  })

  it('bright colors land on their own palette entries', () => {
    expect(parseAnsiSegments('\u001b[91mhot')[0].color).toBe('#ff7a93')
  })

  it('never emits a color for plain text', () => {
    expect(stripAnsi('plain output')).toBe('plain output')
    expect(parseAnsiSegments('plain output')).toEqual([{ text: 'plain output' }])
  })

  it('strips ANSI from copy text', () => {
    expect(stripAnsi('\u001b[32mok\u001b[0m done')).toBe('ok done')
  })
})
