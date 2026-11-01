// Client-side mirror of workspace/fspolicy.go (docs/filesystem-policy.md §3):
// the same glob semantics, validation and path verdicts, so the policy editor
// can validate masks, offer presets and answer «а этот путь будет доступен?»
// without a server round-trip. The backend remains the source of truth — the
// kernel re-validates every mask on save and re-checks every path on use.

/** One policy row's filesystem dimension: masks are OR within the dimension;
 *  an empty dimension is «not set» = unrestricted (the backend normalizes a
 *  submitted empty list back to nil). Deny is absolute; write requires read. */
export interface FilesystemForm {
  read: string[]
  write: string[]
  deny: string[]
}

/** Match one normalized path against one mask. `**` is special only as a
 *  whole segment and spans zero or more segments (`src/**` matches `src`
 *  itself and everything below; a leading `**` segment matches `x` at any
 *  depth). Every other segment is a single-segment glob (`*`, `?`, `[a-z]`,
 *  `[^abc]`). */
export function matchFSPath(mask: string, cleanPath: string): boolean {
  return matchSegments(mask.split('/'), cleanPath.split('/'))
}

function matchSegments(mask: string[], segs: string[]): boolean {
  if (mask.length === 0) return segs.length === 0
  if (mask[0] === '**') {
    for (let i = 0; i <= segs.length; i++) {
      if (matchSegments(mask.slice(1), segs.slice(i))) return true
    }
    return false
  }
  if (segs.length === 0) return false
  if (!matchSegment(mask[0], segs[0])) return false
  return matchSegments(mask.slice(1), segs.slice(1))
}

/** Single-segment glob against one path segment — path.Match semantics for
 *  the constructs the backend validator lets through (no backslashes there,
 *  so no escape handling). Returns false for malformed patterns. */
function matchSegment(pattern: string, s: string): boolean {
  const re = segmentRegex(pattern)
  return re ? re.test(s) : false
}

const segmentRegexCache = new Map<string, RegExp | null>()

function segmentRegex(pattern: string): RegExp | null {
  const cached = segmentRegexCache.get(pattern)
  if (cached !== undefined) return cached
  let out = '^'
  let i = 0
  let ok = true
  while (i < pattern.length && ok) {
    const ch = pattern[i]
    if (ch === '*') { out += '[^/]*'; i++ }
    else if (ch === '?') { out += '[^/]'; i++ }
    else if (ch === '[') {
      // Character class: scan to the closing bracket (a ']' right after '['
      // or '[^' is literal, as in path.Match).
      let j = i + 1
      if (pattern[j] === '!' || pattern[j] === '^') j++
      if (pattern[j] === ']') j++
      while (j < pattern.length && pattern[j] !== ']') j++
      if (j >= pattern.length) { ok = false; break }
      let body = pattern.slice(i + 1, j)
      const negated = body.startsWith('!') || body.startsWith('^')
      if (negated) body = body.slice(1)
      // Escape regex specials; keep ranges (a-z) intact.
      body = body.replace(/[\\^\]]/g, '\\$&')
      out += `[${negated ? '^' : ''}${body}]`
      i = j + 1
    }
    else { out += ch.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'); i++ }
  }
  let re: RegExp | null = null
  if (ok) {
    try { re = new RegExp(`${out}$`) } catch { re = null }
  }
  segmentRegexCache.set(pattern, re)
  return re
}

export type MaskErrorKind = 'empty' | 'spaces' | 'backslash' | 'absolute' | 'dots' | 'pattern'

/** The same discipline as ValidateFilesystemMask: a bad glob must fail at
 *  policy-save time (in the editor), not mid-run. Returns null when valid. */
export function validateFilesystemMask(mask: string): MaskErrorKind | null {
  if (mask.trim() === '') return 'empty'
  if (mask !== mask.trim()) return 'spaces'
  if (mask.includes('\\')) return 'backslash'
  if (mask.startsWith('/')) return 'absolute'
  for (const seg of mask.split('/')) {
    if (seg === '' || seg === '.' || seg === '..') return 'dots'
    if (seg === '**') continue
    if (segmentRegex(seg) === null) return 'pattern'
  }
  return null
}

/** Render a tool/model-supplied path as the canonical workspace-relative
 *  form for the tester: strips the /workspace prefix the file tools accept,
 *  resolves ./ and //, rejects absolute and dotted-through paths. */
export function normalizeFSPath(p: string): string | null {
  let rel = p.trim()
  if (rel === '') return null
  rel = rel.replaceAll('\\', '/')
  if (rel.startsWith('/workspace/')) rel = rel.slice('/workspace/'.length)
  else if (rel === '/workspace') return null
  else if (rel.startsWith('/')) return null
  const out: string[] = []
  for (const seg of rel.split('/')) {
    if (seg === '' || seg === '.') continue
    if (seg === '..') {
      if (out.length === 0 || out[out.length - 1] === '..') return null
      out.pop()
      continue
    }
    out.push(seg)
  }
  if (out.length === 0) return null
  return out.join('/')
}

export interface PathVerdict {
  denied: boolean
  readable: boolean
  writable: boolean
}

/** The verdict for one path under ONE policy row (no inheritance layers —
 *  the dialog edits a single row; the run-time effective policy intersects
 *  rows across the org tree). Mirrors EffectiveFilesystem.AllowRead /
 *  AllowWrite for the single-layer case; an empty dimension restricts
 *  nothing. */
export function pathVerdict(rules: FilesystemForm, cleanPath: string): PathVerdict {
  const denied = rules.deny.some((mask) => matchFSPath(mask, cleanPath))
  const readable = !denied && (rules.read.length === 0 || rules.read.some((mask) => matchFSPath(mask, cleanPath)))
  const writable = readable && (rules.write.length === 0 || rules.write.some((mask) => matchFSPath(mask, cleanPath)))
  return { denied, readable, writable }
}

/** True when the row's filesystem dimension constrains nothing — the tag in
 *  the policies list hides for such rows. */
export function filesystemEmpty(rules: FilesystemForm | undefined | null): boolean {
  return !rules || (rules.read.length === 0 && rules.write.length === 0 && rules.deny.length === 0)
}
