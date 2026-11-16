// ── Cross-links inside rendered artifact bodies ────────────────────────────
// Three link kinds live in artifact markdown and must work in the read view:
// internal anchors («See [Setup](#setup)»), links to other artifacts
// («[Runbook](artifact:a1)») and @artifact:<id> mentions written by agents
// (the same handle the chat uses). Footnotes ([^1]) come from remark-gfm and
// need a content index for the hover hint. Everything here is pure or
// idempotent DOM mutation — the annotation highlight pass owns the text-node
// offsets, so mention wrapping may only WRAP (never retype) text.

export interface FootnoteDef {
  label: string
  text: string
}

// Like GitHub: a definition line «[^label]: text» continues over the
// following lines until a blank line (lazy continuation, dedented).
const FOOTNOTE_DEF_RE = /^\[\^([^\]\s]+)\]:\s?(.*)$/

function stripInlineMarkdown(raw: string): string {
  return raw
    .replace(/!?\[([^\]]*)\]\([^)]*\)/g, '$1')
    .replace(/`([^`]+)`/g, '$1')
    .replace(/[*_~]{1,3}([^*_~]+)[*_~]{1,3}/g, '$1')
    .replace(/<[^>]+>/g, '')
    .trim()
}

/** The footnote definitions of a document, source order. Code fences never
 *  contribute; multi-line definitions fold into one hint text. */
export function parseFootnotes(content: string): FootnoteDef[] {
  const out: FootnoteDef[] = []
  const lines = content.split('\n')
  let inFence = false
  let fenceMarker = ''
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i]
    if (inFence) {
      if (line.trim().startsWith(fenceMarker)) { inFence = false; fenceMarker = '' }
      continue
    }
    const fence = line.match(/^\s{0,3}(`{3,}|~{3,})/)
    if (fence) { fenceMarker = fence[1][0].repeat(3); inFence = true; continue }
    const def = line.match(FOOTNOTE_DEF_RE)
    if (def) {
      const parts = [def[2]]
      // Lazy continuation: every non-blank, non-definition line after the
      // definition belongs to it until a blank line closes the block.
      let j = i + 1
      while (j < lines.length && lines[j].trim() !== '' && !FOOTNOTE_DEF_RE.test(lines[j])) {
        parts.push(lines[j].replace(/^\s{1,4}/, ''))
        i = j
        j++
      }
      out.push({ label: def[1], text: stripInlineMarkdown(parts.join(' ')) })
    }
  }
  return out
}

/** Footnote labels in first-REFERENCE order (definitions don't count). The
 *  rendered superscripts are numbered 1..n in exactly this order, and their
 *  hrefs carry the matching number (user-content-fn-N). */
export function footnoteReferenceOrder(content: string): string[] {
  const out: string[] = []
  const seen = new Set<string>()
  let inFence = false
  let fenceMarker = ''
  for (const line of content.split('\n')) {
    if (inFence) {
      if (line.trim().startsWith(fenceMarker)) { inFence = false; fenceMarker = '' }
      continue
    }
    const fence = line.match(/^\s{0,3}(`{3,}|~{3,})/)
    if (fence) { fenceMarker = fence[1][0].repeat(3); inFence = true; continue }
    if (FOOTNOTE_DEF_RE.test(line)) continue
    for (const match of line.matchAll(/\[\^([^\]\s]+)\]/g)) {
      const label = match[1]
      if (!seen.has(label)) { seen.add(label); out.push(label) }
    }
  }
  return out
}

/** number (from the rendered user-content-fn-N href) → hint text. */
export function footnoteIndex(content: string): string[] {
  const defs = new Map(parseFootnotes(content).map((def) => [def.label, def.text]))
  return footnoteReferenceOrder(content).map((label) => defs.get(label) ?? '')
}

// Mention ids never end in punctuation — «@artifact:a3.» at a sentence end
// is a3, not a3. (ids are letters, digits and dashes per the registry).
export const ARTIFACT_MENTION_RE = /@artifact:([A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9_-])?)/g

/** «artifact:a1», «artifact://a1» → 'a1'; anything else → null. */
export function artifactIdFromHref(href: string | null | undefined): string | null {
  const match = /^artifact:(?:\/\/)?([A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9_-])?)$/.exec(href ?? '')
  return match ? match[1] : null
}

// Mentions may reference artifacts not present in the loaded catalog page;
// the caller fetches those titles once and caches them.
export interface ArtifactRefInfo {
  title: string
  status?: string
  version?: number
}

export type ArtifactRefResolver = (id: string) => ArtifactRefInfo | undefined

/** Wrap @artifact:<id> mentions in the rendered body into anchors. Text is
 *  wrapped verbatim (never retyped — annotation offsets are text-offsets),
 *  so the chip label stays the raw mention; the hover hint carries the
 *  title. Idempotent: existing .artifact-mention anchors are skipped, and
 *  mentions inside code, links or the footnotes block stay inert. */
export function linkifyArtifactMentions(root: HTMLElement): void {
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT, {
    acceptNode: (node) => {
      const parent = (node as Text).parentElement
      if (!parent) return NodeFilter.FILTER_REJECT
      if (parent.closest('a, code, pre, [data-footnotes]')) return NodeFilter.FILTER_REJECT
      return NodeFilter.FILTER_ACCEPT
    },
  })
  const targets: Array<{ node: Text; ranges: Array<{ from: number; to: number; id: string }> }> = []
  let node: Node | null
  while ((node = walker.nextNode())) {
    const text = node.textContent ?? ''
    const ranges: Array<{ from: number; to: number; id: string }> = []
    for (const match of text.matchAll(ARTIFACT_MENTION_RE)) {
      const index = match.index ?? 0
      ranges.push({ from: index, to: index + match[0].length, id: match[1] })
    }
    if (ranges.length > 0) targets.push({ node: node as Text, ranges })
  }
  // Wrap from the end: earlier offsets stay valid while later ones wrap.
  for (const { node: textNode, ranges } of targets) {
    for (const { from, to, id } of [...ranges].reverse()) {
      const anchor = document.createElement('a')
      anchor.className = 'artifact-mention'
      anchor.dataset.artifactId = id
      // A real href keeps the chip focusable and lets the delegated handlers
      // treat it exactly like a markdown artifact: link.
      anchor.href = `artifact:${id}`
      const range = document.createRange()
      range.setStart(textNode, from)
      range.setEnd(textNode, to)
      try { range.surroundContents(anchor) } catch { /* mention split across nodes — stays plain text */ }
    }
  }
}

/** Undo linkifyArtifactMentions (before the next wrap pass). */
export function stripArtifactMentions(root: HTMLElement): void {
  root.querySelectorAll('a.artifact-mention').forEach((anchor) => {
    const parent = anchor.parentNode
    if (!parent) return
    while (anchor.firstChild) parent.insertBefore(anchor.firstChild, anchor)
    anchor.remove()
    parent.normalize()
  })
}

/** Artifact ids referenced by the document (mentions + artifact: links),
 *  source order, deduplicated — the fetch-title queue. */
export function referencedArtifactIds(content: string): string[] {
  const out: string[] = []
  const seen = new Set<string>()
  const push = (id: string) => { if (!seen.has(id)) { seen.add(id); out.push(id) } }
  let inFence = false
  let fenceMarker = ''
  for (const line of content.split('\n')) {
    if (inFence) {
      if (line.trim().startsWith(fenceMarker)) { inFence = false; fenceMarker = '' }
      continue
    }
    const fence = line.match(/^\s{0,3}(`{3,}|~{3,})/)
    if (fence) { fenceMarker = fence[1][0].repeat(3); inFence = true; continue }
    for (const match of line.matchAll(ARTIFACT_MENTION_RE)) push(match[1])
    for (const match of line.matchAll(/\]\(artifact:(?:\/\/)?([A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9_-])?)\)/g)) push(match[1])
  }
  return out
}
