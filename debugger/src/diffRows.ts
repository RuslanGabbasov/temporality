import { diffLines } from 'diff'

export interface DiffCell {
  lineNumber: number
  text: string
}

export type DiffRow =
  | { kind: 'same'; left: DiffCell; right: DiffCell }
  | { kind: 'changed'; left: DiffCell | null; right: DiffCell | null }
  | { kind: 'gap'; hidden: number }

const CONTEXT_LINES = 3

function splitLines(value: string): string[] {
  const lines = value.split('\n')
  if (lines[lines.length - 1] === '') lines.pop()
  return lines
}

function normalize(text: string): string {
  return text.replace(/\r\n/g, '\n')
}

/** Pairs consecutive removed/added blocks into rows, so a rewritten line sits
 *  opposite its replacement. */
function buildRows(oldText: string, newText: string): DiffRow[] {
  const rows: DiffRow[] = []
  let leftNumber = 1
  let rightNumber = 1
  let removed: string[] = []

  const flushRemoved = (added: string[]) => {
    const height = Math.max(removed.length, added.length)
    for (let i = 0; i < height; i++) {
      rows.push({
        kind: 'changed',
        left: i < removed.length ? { lineNumber: leftNumber++, text: removed[i] } : null,
        right: i < added.length ? { lineNumber: rightNumber++, text: added[i] } : null,
      })
    }
    removed = []
  }

  for (const part of diffLines(normalize(oldText), normalize(newText))) {
    const lines = splitLines(part.value)
    if (part.removed) {
      removed = lines
    } else if (part.added) {
      flushRemoved(lines)
    } else {
      flushRemoved([])
      for (const text of lines) {
        rows.push({ kind: 'same', left: { lineNumber: leftNumber++, text }, right: { lineNumber: rightNumber++, text } })
      }
    }
  }
  flushRemoved([])
  return rows
}

/** Replaces long runs of unchanged rows with a single gap row, keeping
 *  CONTEXT_LINES of context around every change. */
function collapseUnchanged(rows: DiffRow[]): DiffRow[] {
  const visible = rows.map(() => false)
  rows.forEach((row, index) => {
    if (row.kind !== 'changed') return
    const from = Math.max(0, index - CONTEXT_LINES)
    const to = Math.min(rows.length - 1, index + CONTEXT_LINES)
    for (let i = from; i <= to; i++) visible[i] = true
  })

  const result: DiffRow[] = []
  let hidden = 0
  rows.forEach((row, index) => {
    if (visible[index]) {
      if (hidden > 0) result.push({ kind: 'gap', hidden })
      hidden = 0
      result.push(row)
    } else {
      hidden++
    }
  })
  if (hidden > 0) result.push({ kind: 'gap', hidden })
  return result
}

export interface SideBySideDiff {
  rows: DiffRow[]
  added: number
  removed: number
}

export function sideBySideDiff(oldText: string, newText: string): SideBySideDiff {
  const rows = buildRows(oldText, newText)
  let added = 0
  let removed = 0
  for (const row of rows) {
    if (row.kind !== 'changed') continue
    if (row.left) removed++
    if (row.right) added++
  }
  return { rows: added + removed === 0 ? [] : collapseUnchanged(rows), added, removed }
}
