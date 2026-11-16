import { useEffect, useMemo, useState, type RefObject } from 'react'
import { ChevronRight } from '@carbon/icons-react'
import { useT } from './i18n'

export interface OutlineHeading {
  level: number
  /** Display text with the inline markdown stripped. */
  text: string
  /** Stable slug, deduplicated across the document. */
  id: string
}

// A fenced code block: ``` / ~~~ (the closing fence may be longer, never
// shorter). Presentation fences (```presentation) are skipped the same way —
// their headings live in the IR, not in the markdown outline.
const FENCE_RE = /^\s{0,3}(`{3,}|~{3,})/
// ATX heading, optionally inside a block quote.
const ATX_RE = /^\s{0,3}(?:>\s?)*?(#{1,6})\s+(.+?)\s*#*\s*$/
// Setext underline (=== → h1, --- → h2) — only valid right after a paragraph
// line; the paragraph guard lives in parseOutline.
const SETEXT_RE = /^\s{0,3}(?:>\s?)*?([=-])[=-]*\s*$/

/** Strip the inline markdown a heading may carry: links keep the label,
 *  emphasis and code marks drop away, raw HTML tags disappear. */
function headingText(raw: string): string {
  return raw
    .replace(/!?\[([^\]]*)\]\([^)]*\)/g, '$1')
    .replace(/`([^`]+)`/g, '$1')
    .replace(/[*_~]{1,3}([^*_~]+)[*_~]{1,3}/g, '$1')
    .replace(/<[^>]+>/g, '')
    .trim()
}

function slugify(text: string): string {
  const base = text.toLowerCase().replace(/[^\p{L}\p{N}]+/gu, '-').replace(/^-+|-+$/g, '')
  return base || 'section'
}

/** The document's heading tree, flattened in source order. Pure and exported
 *  for tests: this is the Notion-style section navigation of a large
 *  artifact. Code fences never contribute headings; setext underlines count
 *  only when the line above is prose (a table separator is not a heading). */
export function parseOutline(content: string): OutlineHeading[] {
  const out: OutlineHeading[] = []
  const seen = new Map<string, number>()
  const push = (level: number, rawText: string) => {
    const text = headingText(rawText)
    if (!text) return
    const base = slugify(text)
    const n = (seen.get(base) ?? 0) + 1
    seen.set(base, n)
    out.push({ level, text, id: n === 1 ? base : `${base}-${n}` })
  }
  let inFence = false
  let fenceMarker = ''
  let prevText = ''
  for (const raw of content.split('\n')) {
    if (inFence) {
      if (raw.trim().startsWith(fenceMarker)) { inFence = false; fenceMarker = '' }
      continue
    }
    const fence = raw.match(FENCE_RE)
    if (fence) {
      // A fence closes on a marker of at least the opening length; tracking
      // the opening run is exact, a 3-char run of the same char is enough.
      fenceMarker = fence[1][0].repeat(3)
      inFence = true
      prevText = ''
      continue
    }
    const atx = raw.match(ATX_RE)
    if (atx) { push(atx[1].length, atx[2]); prevText = ''; continue }
    const setext = raw.match(SETEXT_RE)
    // Setext applies to a paragraph: the previous line must be non-blank
    // prose — not a list item, not a table row, not another heading.
    const prose = prevText && !/^([-*+]|\d+[.)])\s/.test(prevText) && !prevText.includes('|')
    if (setext && prose) { push(setext[1] === '=' ? 1 : 2, prevText); prevText = ''; continue }
    prevText = raw.trim() === '' ? '' : headingText(raw)
  }
  return out
}

/** Map outline entries onto the rendered heading elements. Both come from the
 *  same source, so plain document order matches 1:1; presentation fences add
 *  their own headings, and there the text match keeps the alignment. */
function resolveHeadingEls(root: HTMLElement, outline: OutlineHeading[]): Array<HTMLElement | undefined> {
  const els = Array.from(root.querySelectorAll('h1, h2, h3, h4, h5, h6')) as HTMLElement[]
  if (els.length === outline.length) return els
  const norm = (s: string | null) => (s ?? '').replace(/\s+/g, ' ').trim().toLowerCase()
  const used = new Set<number>()
  return outline.map((item) => {
    const at = els.findIndex((el, i) => !used.has(i) && norm(el.textContent) === norm(item.text))
    if (at < 0) return undefined
    used.add(at)
    return els[at]
  })
}

/** The viewport offset under which a heading counts as "reached" — the app
 *  header plus a comfortable margin. */
const REACH_OFFSET = 130

/**
 * Notion-style section navigation for a rendered artifact body: a sticky
 * outline beside the text; a click scrolls the section into view, the current
 * section highlights as the page scrolls. Hidden when the document has fewer
 * than two headings — a single section gives nothing to jump to.
 */
export default function DocumentOutline({ content, rootRef }: {
  content: string
  rootRef: RefObject<HTMLElement | null>
}) {
  const t = useT()
  const outline = useMemo(() => parseOutline(content), [content])
  const [activeId, setActiveId] = useState('')
  const [open, setOpen] = useState(true)

  // Anchor ids for in-document links («See [Setup](#setup)»): the outline's
  // deduplicated slugs land on the rendered headings, GitHub-style. Runs
  // before the < 2 headings guard — a single-section doc still has anchors.
  useEffect(() => {
    const root = rootRef.current
    if (!root || outline.length === 0) return
    const els = resolveHeadingEls(root, outline)
    els.forEach((el, i) => {
      const item = outline[i]
      if (el && item && el.id !== item.id) el.id = item.id
    })
  }, [outline, rootRef])

  // Highlight the section the reader is in: the last heading above the
  // viewport line. rAF-throttled — scroll fires per frame at most.
  useEffect(() => {
    if (outline.length < 2) return
    const root = rootRef.current
    if (!root) return
    let frame = 0
    const update = () => {
      frame = 0
      const els = resolveHeadingEls(root, outline)
      let current = ''
      for (let i = 0; i < els.length; i++) {
        const el = els[i]
        if (!el) continue
        if (el.getBoundingClientRect().top <= REACH_OFFSET) current = outline[i].id
      }
      setActiveId(current || (outline.find((_, i) => els[i])?.id ?? ''))
    }
    const schedule = () => { if (!frame) frame = requestAnimationFrame(update) }
    update()
    window.addEventListener('scroll', schedule, { passive: true })
    window.addEventListener('resize', schedule)
    return () => {
      window.removeEventListener('scroll', schedule)
      window.removeEventListener('resize', schedule)
      if (frame) cancelAnimationFrame(frame)
    }
  }, [outline, rootRef])

  if (outline.length < 2) return null

  const jumpTo = (index: number) => {
    const root = rootRef.current
    if (!root) return
    const el = resolveHeadingEls(root, outline)[index]
    if (!el) return
    setActiveId(outline[index].id)
    const rect = el.getBoundingClientRect()
    // jsdom reports a zero rect (no layout) — scrolling there is meaningless.
    if (rect.top === 0 && rect.bottom === 0) return
    window.scrollTo({ top: window.scrollY + rect.top - REACH_OFFSET + 20, behavior: 'smooth' })
  }

  const minLevel = Math.min(...outline.map((item) => item.level))

  return (
    <nav className="artifact-outline" aria-label={t('artifacts.outline_title') ?? 'Contents'}>
      <button
        type="button"
        className="artifact-outline-title"
        onClick={() => setOpen((v) => !v)}
        title={t('artifacts.outline_toggle') ?? undefined}
      >
        <span>{t('artifacts.outline_title') ?? 'Contents'}</span>
        <ChevronRight size={12} style={{ transform: open ? 'rotate(90deg)' : 'none', transition: 'transform 120ms' }} />
      </button>
      {open && outline.map((item, index) => (
        <button
          key={`${item.id}:${index}`}
          type="button"
          className={`artifact-outline-item${activeId === item.id ? ' active' : ''}`}
          style={{ paddingLeft: `${0.3 + (item.level - minLevel) * 0.65}rem` }}
          title={item.text}
          onClick={() => jumpTo(index)}
        >
          {item.text}
        </button>
      ))}
    </nav>
  )
}
