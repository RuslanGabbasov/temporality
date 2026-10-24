// Minimal ANSI escape handling for the console panel (docs/agent-behavior-
// monitoring.md neighbor: the console renders what a terminal renders).
// No dependency: SGR color/bold codes become styled segments, every other
// escape sequence (cursor moves, OSC payloads) is stripped — raw ESC bytes
// in a web panel read as garbage and help nobody.

export interface AnsiSegment {
  text: string
  color?: string
  bold?: boolean
}

/** Dark-background palette (Tokyo-Night family, matches the trace UI). */
const SGR_COLORS: Record<number, string> = {
  30: '#565f89', 31: '#f7768e', 32: '#9ece6a', 33: '#e0af68',
  34: '#7aa2f7', 35: '#bb9af7', 36: '#7dcfff', 37: '#c0caf5',
  90: '#a9b1d6', 91: '#ff7a93', 92: '#b9f27c', 93: '#efc986',
  94: '#89ddff', 95: '#c792ea', 96: '#89ddff', 97: '#e6f0fa',
}

const ESC = '\u001b'

/** Parse a text with ANSI SGR sequences into styled segments. Reset (0),
 *  default-fg (39) and bold-off (22) manage the state; every other code is
 *  consumed and ignored. Non-SGR escapes are stripped. */
export function parseAnsiSegments(text: string): AnsiSegment[] {
  const segments: AnsiSegment[] = []
  let color: string | undefined
  let bold: boolean | undefined
  let plain = ''
  let i = 0
  const flush = () => {
    if (plain) {
      segments.push({ text: plain, color, bold })
      plain = ''
    }
  }
  while (i < text.length) {
    if (text[i] !== ESC) {
      plain += text[i]
      i += 1
      continue
    }
    // A style change lands here: everything accumulated so far belongs to
    // the OLD state — flush it before the state mutates.
    flush()
    // ESC [ ... final-byte — CSI (m = SGR, everything else stripped as-is).
    if (text[i + 1] === '[') {
      let j = i + 2
      while (j < text.length && !/[a-zA-Z]/.test(text[j])) j += 1
      if (j >= text.length) break // truncated escape at the buffer edge
      const final = text[j]
      const params = text.slice(i + 2, j)
      i = j + 1
      if (final !== 'm') continue // cursor movement, private modes: strip
      if (params === '' || params === '0') {
        color = undefined
        bold = undefined
        continue
      }
      for (const raw of params.split(';')) {
        const code = parseInt(raw || '0', 10)
        if (SGR_COLORS[code]) color = SGR_COLORS[code]
        else if (code === 1) bold = true
        else if (code === 22) bold = undefined
        else if (code === 39) color = undefined
        // Unknown codes: consumed, state untouched.
      }
      continue
    }
    // OSC (ESC ] … BEL / ST) and two-byte escapes: strip whole payload.
    if (text[i + 1] === ']') {
      const bell = text.indexOf('\u0007', i)
      const st = text.indexOf(`${ESC}\\`, i + 2)
      if (bell === -1 && st === -1) break // truncated payload
      i = bell !== -1 && (st === -1 || bell < st) ? bell + 1 : st + 2
      continue
    }
    i += 2 // ESC + one final byte (e.g. ESC ( B)
  }
  flush()
  return segments
}

/** The same walk without styles: what copy-to-clipboard and search should see. */
export function stripAnsi(text: string): string {
  return parseAnsiSegments(text).map((segment) => segment.text).join('')
}
