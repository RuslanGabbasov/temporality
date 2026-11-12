/**
 * Chat @-mentions of workspace entities (docs/chat-mentions.md): typing `@` in
 * the composer opens a suggestion menu over the harness' own entities —
 * artifacts, skills, knowledge, sagas, tasks, agents and teams — and inserts a
 * canonical `@kind:id` reference into the message. The kernel resolves the
 * reference at run start (kernel/agent/mentions.go), so the same text pasted
 * into Telegram or Matrix works identically: the id alone is also resolvable.
 *
 * This module is the pure logic (token detection, query split, ranking,
 * insertion); MentionMenu.tsx owns the data loading and the dropdown UI.
 */

export type MentionKind = 'artifact' | 'skill' | 'knowledge' | 'saga' | 'task' | 'agent' | 'team'

export const MENTION_KINDS: readonly MentionKind[] = ['artifact', 'skill', 'knowledge', 'saga', 'task', 'agent', 'team']

export function isMentionKind(value: string): value is MentionKind {
  return (MENTION_KINDS as readonly string[]).includes(value)
}

export interface MentionOption {
  kind: MentionKind
  id: string
  title: string
  /** Secondary line for the dropdown (status, project, enabled flag). */
  detail?: string
}

/** One dropdown row: a kind scaffold (`@artifact:`) or a concrete entity. */
export type MentionRow =
  | { type: 'kind'; kind: MentionKind }
  | { type: 'entity'; option: MentionOption }

/** The `@…` span under the caret the menu edits. `end` covers the whole token
 *  even when the caret sits mid-token, so picking a row replaces it cleanly. */
export interface MentionToken {
  start: number
  end: number
  /** Token text between `@` and the end — may contain one `kind:` prefix. */
  query: string
}

const TOKEN_CHARS = /[A-Za-z0-9_.:-]/
const WORD_CHAR = /[A-Za-z0-9_]/

/** Locate the mention token touching the caret, or null when the caret is not
 *  inside one. An `@` glued to a word (e-mail addresses) never opens the menu. */
export function mentionTokenAt(text: string, caret: number): MentionToken | null {
  let start = caret
  while (start > 0 && TOKEN_CHARS.test(text[start - 1])) start--
  if (start === 0 || text[start - 1] !== '@') return null
  const at = start - 1
  if (at > 0 && WORD_CHAR.test(text[at - 1])) return null
  let end = caret
  while (end < text.length && TOKEN_CHARS.test(text[end])) end++
  return { start: at, end, query: text.slice(start, end) }
}

/** Stable key for dismissal bookkeeping: the same token re-opens only after
 *  the text under the caret actually changes. */
export function mentionTokenKey(token: MentionToken): string {
  return `${token.start}:${token.query}`
}

export interface SplitQuery {
  /** The explicit `kind:` prefix when it names a known kind. */
  kind: MentionKind | null
  /** Text after the (first) colon, or the whole query when there is none. */
  filter: string
  /** Lowercased text before the colon ("" when the query has no colon). */
  kindText: string
}

export function splitMentionQuery(query: string): SplitQuery {
  const colon = query.indexOf(':')
  if (colon < 0) return { kind: null, filter: query, kindText: '' }
  const kindText = query.slice(0, colon).toLowerCase()
  return { kind: isMentionKind(kindText) ? kindText : null, filter: query.slice(colon + 1), kindText }
}

/** Ranked substring match over id then title: ids win (they are what the
 *  kernel resolves), titles are the human recall path. */
export function filterMentionOptions(options: MentionOption[], filter: string, limit: number): MentionOption[] {
  const needle = filter.trim().toLowerCase()
  if (!needle) return options.slice(0, limit)
  const scored: { option: MentionOption; score: number }[] = []
  for (const option of options) {
    const id = option.id.toLowerCase()
    const title = option.title.toLowerCase()
    let score = -1
    if (id.startsWith(needle)) score = 3
    else if (id.includes(needle)) score = 2
    else if (title.startsWith(needle)) score = 1
    else if (title.includes(needle)) score = 0
    if (score >= 0) scored.push({ option, score })
  }
  // Stable: equal scores keep registry order.
  scored.sort((a, b) => b.score - a.score)
  return scored.slice(0, limit).map((s) => s.option)
}

/** Build the dropdown rows for the token query. Kind scaffolds lead (they are
 *  the narrowing move), entities follow. An explicit-but-unknown kind closes
 *  the menu — `@see:docs` is plain text, not a broken mention. */
export function buildMentionRows(query: string, options: MentionOption[], limit = 12): MentionRow[] {
  const { kind, filter, kindText } = splitMentionQuery(query)
  if (kindText && !kind) return []
  if (kind) {
    return filterMentionOptions(options.filter((o) => o.kind === kind), filter, limit)
      .map((option) => ({ type: 'entity', option }))
  }
  const needle = filter.trim().toLowerCase()
  const kindRows: MentionRow[] = MENTION_KINDS
    .filter((k) => k.startsWith(needle))
    .map((kind) => ({ type: 'kind', kind }))
  // An empty query shows the kinds only — five arbitrary entities at a bare
  // `@` are noise; the entities arrive as soon as the user types.
  if (!needle) return kindRows
  const entityRows: MentionRow[] = filterMentionOptions(options, filter, Math.max(limit - kindRows.length, 3))
    .map((option) => ({ type: 'entity', option }))
  return [...kindRows, ...entityRows]
}

/** Canonical insertion text for a picked row: kinds narrow (`@artifact:`),
 *  entities close the token (`@artifact:otchet-2026 `). */
export function mentionInsertText(row: MentionRow): string {
  return row.type === 'kind' ? `@${row.kind}:` : `@${row.option.kind}:${row.option.id} `
}

/** Replace the token span with the insert text; the caller moves the caret to
 *  the returned position on the next frame. */
export function insertMentionText(text: string, token: MentionToken, insert: string): { text: string; caret: number } {
  return { text: text.slice(0, token.start) + insert + text.slice(token.end), caret: token.start + insert.length }
}

/** The reference as the kernel sees it (`@kind:id` or `@id`), for tests and
 *  the transcript rendering. */
export function parseMentionRefs(text: string): { kind: MentionKind | null; id: string }[] {
  const refs: { kind: MentionKind | null; id: string }[] = []
  const seen = new Set<string>()
  for (const match of text.matchAll(/@([A-Za-z0-9][A-Za-z0-9._-]*)(?::([A-Za-z0-9][A-Za-z0-9._-]*))?/g)) {
    const [, first, second] = match
    const index = match.index ?? 0
    const before = index > 0 ? text[index - 1] : ''
    if (before && WORD_CHAR.test(before)) continue
    // `@see:docs` names no known kind — plain text, not a broken mention.
    if (second !== undefined && !isMentionKind(first.toLowerCase())) continue
    const kind = second !== undefined ? first.toLowerCase() as MentionKind : null
    const id = second ?? first
    const key = `${kind ?? ''}:${id}`
    if (!seen.has(key)) {
      seen.add(key)
      refs.push({ kind, id })
    }
  }
  return refs
}
