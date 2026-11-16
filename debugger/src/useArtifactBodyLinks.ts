import { useEffect, useMemo, useRef, useState, type RefObject } from 'react'
import { artifactIdFromHref, footnoteReferenceOrder, parseFootnotes, type ArtifactRefInfo } from './artifactLinks'

export interface BodyLinkHint {
  x: number
  y: number
  placement: 'below' | 'above'
  title: string
  body: string
}

/** The rendered footnote refs carry the LABEL in the href:
 *  #user-content-fn-note (the visible text is the number). Backrefs
 *  (#user-content-fnref-…) don't match the fn- pattern. */
function footnoteLabelFromHref(href: string | null): string | null {
  const match = /#user-content-fn-(.+)$/.exec(href ?? '')
  if (!match) return null
  try { return decodeURIComponent(match[1]) } catch { return match[1] }
}

const ANCHOR_SCROLL_OFFSET = 120

/**
 * In-document link behaviour of the rendered artifact body: footnote
 * references show their text as a hover hint (the footnotes section stays at
 * the end), @artifact mentions and artifact: links hint the target's
 * title/status, anchor links (#section) smooth-scroll to the heading, and
 * artifact links open the referenced document via onOpenArtifact. Everything
 * is delegated from rootRef, so no per-link wiring survives re-renders.
 */
export function useArtifactBodyLinks({ enabled, rootRef, content, resolveArtifact, describeArtifact, onOpenArtifact, footnoteTitle }: {
  enabled: boolean
  rootRef: RefObject<HTMLElement | null>
  content: string
  /** id → { title, status, version }; the caller keeps the fetched cache. */
  resolveArtifact: (id: string) => ArtifactRefInfo | undefined
  /** Human line under the hint title: «approved · v2». */
  describeArtifact: (info: ArtifactRefInfo) => string
  onOpenArtifact: (id: string) => void
  /** Localized «Footnote {{n}}» for the hint title. */
  footnoteTitle: (n: number) => string
}) {
  // label → { hint text, 1-based reference number } — the numbering the
  // rendered superscripts show.
  const footnotes = useMemo(() => {
    const texts = new Map(parseFootnotes(content).map((def) => [def.label, def.text]))
    const map = new Map<string, { n: number; text: string }>()
    footnoteReferenceOrder(content).forEach((label, index) => {
      const text = texts.get(label)
      if (text) map.set(label, { n: index + 1, text })
    })
    return map
  }, [content])
  const [hint, setHint] = useState<BodyLinkHint | null>(null)
  // Callbacks land in refs: the delegated listeners bind once per (enabled,
  // content) and always read the freshest resolver/closure.
  const resolveRef = useRef(resolveArtifact)
  resolveRef.current = resolveArtifact
  const describeRef = useRef(describeArtifact)
  describeRef.current = describeArtifact
  const openRef = useRef(onOpenArtifact)
  openRef.current = onOpenArtifact
  const footnoteTitleRef = useRef(footnoteTitle)
  footnoteTitleRef.current = footnoteTitle

  useEffect(() => {
    const root = rootRef.current
    if (!enabled || !root) return
    const show = (anchor: Element, title: string, body: string) => {
      const rect = anchor.getBoundingClientRect()
      if (rect.top === 0 && rect.bottom === 0) return
      const below = window.innerHeight - rect.bottom
      const placement: BodyLinkHint['placement'] = below < 150 && below <= rect.top ? 'above' : 'below'
      setHint({
        x: Math.min(Math.max(12, rect.left), window.innerWidth - 372),
        y: placement === 'below' ? rect.bottom + 8 : rect.top - 8,
        placement,
        title,
        body,
      })
    }
    const onOver = (event: Event) => {
      const target = event.target as HTMLElement
      const anchor = target.closest('a')
      if (!anchor || !root.contains(anchor)) { setHint(null); return }
      const fnLabel = footnoteLabelFromHref(anchor.getAttribute('href'))
      if (fnLabel) {
        const footnote = footnotes.get(fnLabel)
        if (footnote) show(anchor, footnoteTitleRef.current(footnote.n), footnote.text)
        else setHint(null)
        return
      }
      const id = anchor.dataset.artifactId || artifactIdFromHref(anchor.getAttribute('href'))
      if (id) {
        const info = resolveRef.current(id)
        // Unknown id (fetch still in flight): the raw handle beats nothing.
        show(anchor, info?.title ?? `artifact:${id}`, info ? describeRef.current(info) : '')
        return
      }
      setHint(null)
    }
    const onClick = (event: Event) => {
      const target = event.target as HTMLElement
      const anchor = target.closest('a')
      if (!anchor || !root.contains(anchor)) return
      const href = anchor.getAttribute('href') ?? ''
      const id = anchor.dataset.artifactId || artifactIdFromHref(href)
      if (id) {
        event.preventDefault()
        setHint(null)
        openRef.current(id)
        return
      }
      if (href.startsWith('#') && href.length > 1) {
        event.preventDefault()
        setHint(null)
        let el: HTMLElement | null = null
        try { el = document.getElementById(decodeURIComponent(href.slice(1))) } catch { /* malformed escape */ }
        if (!el) return
        const rect = el.getBoundingClientRect()
        // jsdom reports a zero rect (no layout) — nothing to scroll.
        if (rect.top === 0 && rect.bottom === 0) return
        window.scrollTo({ top: window.scrollY + rect.top - ANCHOR_SCROLL_OFFSET, behavior: 'smooth' })
      }
    }
    const onScroll = () => setHint(null)
    root.addEventListener('mouseover', onOver)
    root.addEventListener('click', onClick)
    window.addEventListener('scroll', onScroll, { passive: true })
    return () => {
      root.removeEventListener('mouseover', onOver)
      root.removeEventListener('click', onClick)
      window.removeEventListener('scroll', onScroll)
      setHint(null)
    }
  }, [enabled, rootRef, footnotes])

  return { hint }
}
