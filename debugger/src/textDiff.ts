import type { DiffRow, MarkdownDiff, MdBlock, WordChange, WordOp } from './markdownDiff'

const block = (text: string): MdBlock => ({ kind: 'code', text, signature: text })

/** Literal tokens retain case, whitespace and source punctuation. */
function diffSourceWords(oldText: string, newText: string): WordChange[] {
  const tokens = (text: string) => text.match(/[\p{L}\p{N}_]+|\s+|[^\s\p{L}\p{N}_]/gu) ?? []
  const a = tokens(oldText)
  const b = tokens(newText)
  const changes: WordChange[] = []
  const push = (op: WordOp, text: string) => {
    if (!text) return
    const last = changes[changes.length - 1]
    if (last?.op === op) last.text += text
    else changes.push({ op, text })
  }
  let start = 0
  while (start < a.length && start < b.length && a[start] === b[start]) start++
  push('same', a.slice(0, start).join(''))
  let endA = a.length
  let endB = b.length
  while (endA > start && endB > start && a[endA - 1] === b[endB - 1]) { endA--; endB-- }
  const n = endA - start
  const m = endB - start
  if ((n + 1) * (m + 1) > 1_000_000) {
    push('del', a.slice(start, endA).join(''))
    push('add', b.slice(start, endB).join(''))
  } else {
    const table = Array.from({ length: n + 1 }, () => new Uint32Array(m + 1))
    for (let i = n - 1; i >= 0; i--) {
      for (let j = m - 1; j >= 0; j--) {
        table[i][j] = a[start + i] === b[start + j] ? table[i + 1][j + 1] + 1 : Math.max(table[i + 1][j], table[i][j + 1])
      }
    }
    let i = 0
    let j = 0
    while (i < n || j < m) {
      if (i < n && j < m && a[start + i] === b[start + j]) {
        push('same', a[start + i++]); j++
      } else if (i < n && (j === m || table[i + 1][j] >= table[i][j + 1])) {
        push('del', a[start + i++])
      } else {
        push('add', b[start + j++])
      }
    }
  }
  push('same', a.slice(endA).join(''))
  return changes
}

/** Exact line alignment for source text. No parsing or whitespace normalization.
 * Large inputs fall back to a single changed region to bound memory use. */
export function diffText(oldText: string, newText: string): MarkdownDiff {
  const lines = (text: string) => text === '' ? [] : text.replace(/\r\n/g, '\n').split('\n')
  const a = lines(oldText)
  const b = lines(newText)
  const rows: DiffRow[] = []
  const emit = (oldLines: string[], newLines: string[]) => {
    if (!oldLines.length && !newLines.length) return
    rows.push({
      kind: oldLines.length && newLines.length ? 'modified' : oldLines.length ? 'removed' : 'added',
      oldBlock: oldLines.length ? block(oldLines.join('\n')) : undefined,
      newBlock: newLines.length ? block(newLines.join('\n')) : undefined,
      changes: oldLines.length && newLines.length ? diffSourceWords(oldLines.join('\n'), newLines.join('\n')) : undefined,
    })
  }
  let start = 0
  while (start < a.length && start < b.length && a[start] === b[start]) start++
  if (start) rows.push({ kind: 'same', oldBlock: block(a.slice(0, start).join('\n')), newBlock: block(b.slice(0, start).join('\n')) })
  let endA = a.length
  let endB = b.length
  while (endA > start && endB > start && a[endA - 1] === b[endB - 1]) { endA--; endB-- }
  const n = endA - start
  const m = endB - start
  if ((n + 1) * (m + 1) > 1_000_000) {
    emit(a.slice(start, endA), b.slice(start, endB))
  } else {
    const table = Array.from({ length: n + 1 }, () => new Uint32Array(m + 1))
    for (let i = n - 1; i >= 0; i--) {
      for (let j = m - 1; j >= 0; j--) {
        table[i][j] = a[start + i] === b[start + j] ? table[i + 1][j + 1] + 1 : Math.max(table[i + 1][j], table[i][j + 1])
      }
    }
    let i = 0
    let j = 0
    let removed: string[] = []
    let added: string[] = []
    while (i < n || j < m) {
      if (i < n && j < m && a[start + i] === b[start + j]) {
        emit(removed, added)
        removed = []; added = []
        const text = a[start + i]
        rows.push({ kind: 'same', oldBlock: block(text), newBlock: block(text) })
        i++; j++
      } else if (i < n && (j === m || table[i + 1][j] >= table[i][j + 1])) {
        removed.push(a[start + i++])
      } else {
        added.push(b[start + j++])
      }
    }
    emit(removed, added)
  }
  if (endA < a.length) rows.push({ kind: 'same', oldBlock: block(a.slice(endA).join('\n')), newBlock: block(b.slice(endB).join('\n')) })
  const counts = { same: 0, added: 0, removed: 0, modified: 0 }
  for (const row of rows) counts[row.kind]++
  return { rows, counts }
}
