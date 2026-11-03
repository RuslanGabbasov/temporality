/** Semantic Markdown diff (AST-ish): documents are compared as blocks of
 *  meaning (headings, paragraphs, lists, tables, code) rather than raw lines,
 *  so a reflowed paragraph reads as one change — not fifty. Business users
 *  see WHAT changed; the raw markup stays in the panels.
 *
 *  Pure functions, no dependencies: a block segmenter, a block-level
 *  alignment (exact anchors + similarity pairing) and a word-level diff for
 *  modified pairs. */

export type MdBlockKind = 'heading' | 'paragraph' | 'list' | 'code' | 'table' | 'quote' | 'other'

export interface MdBlock {
  kind: MdBlockKind
  /** The raw markdown of the block, preserved for rendering. */
  text: string
  /** Whitespace- and syntax-normalized text: the matching identity. */
  signature: string
}

export type WordOp = 'same' | 'add' | 'del'

export interface WordChange {
  op: WordOp
  text: string
}

export type DiffRowKind = 'same' | 'added' | 'removed' | 'modified'

export interface DiffRow {
  kind: DiffRowKind
  oldBlock?: MdBlock
  newBlock?: MdBlock
  /** Word-level diff of a modified pair: what actually changed inside it. */
  changes?: WordChange[]
}

export interface MarkdownDiff {
  rows: DiffRow[]
  counts: { same: number; added: number; removed: number; modified: number }
}

const FENCE_RE = /^\s*(```|~~~)/

/** Split markdown into semantic blocks: blank lines separate blocks, fenced
 *  code is atomic (blank lines inside a fence never split), and every block
 *  is classified by its leading shape. */
export function segmentMarkdown(text: string): MdBlock[] {
  const lines = (text ?? '').replace(/\r\n/g, '\n').split('\n')
  const blocks: MdBlock[] = []
  let current: string[] = []
  let inFence = false
  const flush = () => {
    if (current.length === 0) return
    const raw = current.join('\n').trim()
    if (raw) blocks.push({ kind: blockKind(raw), text: raw, signature: blockSignature(raw) })
    current = []
  }
  for (const line of lines) {
    if (FENCE_RE.test(line)) inFence = !inFence
    if (!inFence && line.trim() === '') {
      flush()
      continue
    }
    current.push(line)
  }
  flush()
  return blocks
}

function blockKind(raw: string): MdBlockKind {
  const first = raw.split('\n')[0].trim()
  if (FENCE_RE.test(first)) return 'code'
  if (/^#{1,6}\s/.test(first)) return 'heading'
  const lines = raw.split('\n').filter((l) => l.trim() !== '')
  if (lines.length > 0 && lines.every((l) => l.trim().startsWith('>'))) return 'quote'
  if (lines.length > 0 && lines.every((l) => /\|/.test(l))) return 'table'
  if (lines.length > 0 && lines.every((l) => /^\s*([-*+]|\d+[.)])\s+/.test(l))) return 'list'
  return 'paragraph'
}

/** The matching identity of a block: markdown punctuation removed, words
 *  lowercased — «## Утверждение» and «Утверждение» are the same words, and a
 *  reflowed paragraph keeps its identity too. */
export function blockSignature(raw: string): string {
  return raw
    .replace(/```[\s\S]*?```/g, ' ')
    .replace(/`([^`]*)`/g, '$1')
    .replace(/^#{1,6}\s+/gm, '')
    .replace(/^\s*([-*+]|\d+[.)])\s+/gm, '')
    .replace(/^\s*>\s?/gm, '')
    .replace(/\|/g, ' ')
    .replace(/[*_~#]/g, '')
    .replace(/\[([^\]]*)\]\([^)]*\)/g, '$1')
    .toLowerCase()
    .replace(/\s+/g, ' ')
    .trim()
}

/** Word tokens for the word-level diff: words and punctuation keep their
 *  trailing whitespace, so the runs can be concatenated back into readable
 *  text; emphasis/heading markers («**», «_», «`») are dropped so the story
 *  reads as prose, not raw markup. */
function words(text: string): string[] {
  const plain = (text ?? '')
    .replace(/(?:\*\*|__|[*_`])(?![\p{L}\p{N}])|(?<![\p{L}\p{N}])(?:\*\*|__|[*_`])/gu, '')
  return plain.match(/[\p{L}\p{N}][\p{L}\p{N}'’\-]*\s*|[^\s\p{L}\p{N}]\s*/gu) ?? []
}

function normalizeWord(word: string): string {
  const core = word.toLowerCase().replace(/[^\p{L}\p{N}]/gu, '')
  // Pure punctuation compares literally — «.» matches «.» but never «word».
  return core || word.toLowerCase()
}

/** Longest-common-subsequence alignment over normalized tokens. */
function lcsPairs(a: string[], b: string[]): Array<[number, number]> {
  const normA = a.map(normalizeWord)
  const normB = b.map(normalizeWord)
  const n = normA.length
  const m = normB.length
  const table: number[][] = Array.from({ length: n + 1 }, () => new Array<number>(m + 1).fill(0))
  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) {
      table[i][j] = normA[i] === normB[j] && normA[i] !== ''
        ? table[i + 1][j + 1] + 1
        : Math.max(table[i + 1][j], table[i][j + 1])
    }
  }
  const pairs: Array<[number, number]> = []
  let i = 0
  let j = 0
  while (i < n && j < m) {
    if (normA[i] === normB[j] && normA[i] !== '') {
      pairs.push([i, j])
      i++
      j++
    } else if (table[i + 1][j] >= table[i][j + 1]) {
      i++
    } else {
      j++
    }
  }
  return pairs
}

/** Word-level diff of two texts: unchanged words ride through, additions and
 *  removals sit in their own runs. */
export function diffWords(oldText: string, newText: string): WordChange[] {
  const oldWords = words(oldText)
  const newWords = words(newText)
  const pairs = lcsPairs(oldWords, newWords)
  const changes: WordChange[] = []
  let oi = 0
  let ni = 0
  const push = (op: WordOp, word: string) => {
    const last = changes[changes.length - 1]
    if (last && last.op === op) {
      last.text += word
    } else {
      changes.push({ op, text: word })
    }
  }
  for (const [pi, pj] of [...pairs, [oldWords.length, newWords.length] as [number, number]]) {
    while (oi < pi) push('del', oldWords[oi++])
    while (ni < pj) push('add', newWords[ni++])
    if (pi < oldWords.length) {
      push('same', oldWords[pi])
      oi = pi + 1
      ni = pj + 1
    }
  }
  return changes
}

/** Sequence similarity of two blocks' words: 2·LCS / (|a|+|b|). */
function similarity(a: string, b: string): number {
  const wa = words(a)
  const wb = words(b)
  if (wa.length === 0 && wb.length === 0) return 1
  if (wa.length === 0 || wb.length === 0) return 0
  const common = lcsPairs(wa, wb).length
  return (2 * common) / (wa.length + wb.length)
}

/** The semantic diff: blocks align on exact identity first (anchors), then
 *  the leftovers between anchors pair up by similarity — a rewritten
 *  paragraph becomes one «modified» row with its word-level story, not a
 *  delete plus an add. */
export function diffMarkdown(oldText: string, newText: string): MarkdownDiff {
  const oldBlocks = segmentMarkdown(oldText)
  const newBlocks = segmentMarkdown(newText)
  const rows: DiffRow[] = []

  // Exact anchors: LCS over block signatures.
  const anchors = lcsPairs(
    oldBlocks.map((b) => b.signature),
    newBlocks.map((b) => b.signature)
  )
  // Signature equality tolerates empty-signature twins (e.g. images): treat
  // equal-normalized blocks as matched through the same LCS walk.
  const anchorSet = new Set(anchors.map(([i, j]) => `${i}:${j}`))

  let oi = 0
  let ni = 0
  const emitGap = (oldFrom: number, oldTo: number, newFrom: number, newTo: number) => {
    const olds = oldBlocks.slice(oldFrom, oldTo)
    const news = newBlocks.slice(newFrom, newTo)
    const usedNew = new Set<number>()
    for (const oldBlock of olds) {
      let best = -1
      let bestScore = 0
      for (let k = 0; k < news.length; k++) {
        if (usedNew.has(k)) continue
        const score = similarity(oldBlock.text, news[k].text)
        if (score > bestScore) {
          bestScore = score
          best = k
        }
      }
      // A modified pair must look like the same block: same kind or a very
      // close word picture. Otherwise it is a delete AND an add.
      if (best >= 0 && bestScore >= 0.45 && (news[best].kind === oldBlock.kind || bestScore >= 0.7)) {
        usedNew.add(best)
        rows.push({ kind: 'modified', oldBlock, newBlock: news[best], changes: diffWords(oldBlock.text, news[best].text) })
      } else {
        rows.push({ kind: 'removed', oldBlock })
      }
    }
    for (let k = 0; k < news.length; k++) {
      if (!usedNew.has(k)) rows.push({ kind: 'added', newBlock: news[k] })
    }
  }

  for (const [ai, aj] of [...anchors, [oldBlocks.length, newBlocks.length] as [number, number]]) {
    emitGap(oi, ai, ni, aj)
    if (ai < oldBlocks.length) {
      rows.push({ kind: 'same', oldBlock: oldBlocks[ai], newBlock: newBlocks[aj] })
      oi = ai + 1
      ni = aj + 1
    }
  }

  const counts = { same: 0, added: 0, removed: 0, modified: 0 }
  for (const row of rows) counts[row.kind]++
  return { rows, counts }
}
