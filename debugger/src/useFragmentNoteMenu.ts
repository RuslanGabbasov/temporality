import { useCallback, useEffect, useRef, useState } from 'react'
import { flushSync } from 'react-dom'
import type { ArtifactAnnotation } from './workspaceApi'

/** Selection state the fragment-note popup ({x, y} viewport coords, the
 *  quoted text and its stored offsets). `below` flips the popup under the
 *  selection when there is no room above; `seq` increments on every fresh
 *  selection so consumers can tell a new anchor from a reposition. */
export interface FragmentNoteMenu {
  x: number
  y: number
  below: boolean
  quote: string
  start: number
  end: number
  seq: number
}

export interface FragmentNoteDraft {
  quote: string
  kind: ArtifactAnnotation['kind']
  comment: string
  replacement: string
}

/** Viewport rect of a DOM range. jsdom's Range lacks
 *  getBoundingClientRect — tests substitute a zero rect there, the popup
 *  still opens (its position only matters in a real browser). */
function rangeRect(range: Range): { top: number; bottom: number; left: number; width: number } {
  if (typeof range.getBoundingClientRect === 'function') {
    const rect = range.getBoundingClientRect()
    return { top: rect.top, bottom: rect.bottom, left: rect.left, width: rect.width }
  }
  return { top: 0, bottom: 0, left: 0, width: 0 }
}

/** The Notion-model controller of the fragment-note popup: it tracks the
 *  live document selection (`selectionchange`, mouse or keyboard alike),
 *  opens the menu anchored to the selection, and closes it when the
 *  selection collapses or leaves the root — EXCEPT when the collapse is the
 *  caret moving into the popup itself (an autofocused comment field does
 *  that; it is not a discard). A bare menu hides on scroll (the anchor left
 *  the viewport); a typing draft stays glued to its fragment. Escape and a
 *  genuine outside click discard everything.
 *
 * `flushSync` commits the popup in the same frame as the trigger: a batched
 * update would leave it dangling a tick behind the text. */
export function useFragmentNoteMenu({ enabled, rootRef }: { enabled: boolean; rootRef: React.RefObject<HTMLElement | null> }) {
  const [menu, setMenu] = useState<FragmentNoteMenu | null>(null)
  const [draft, setDraft] = useState<FragmentNoteDraft | null>(null)
  const anchorRef = useRef<Range | null>(null)
  const draftOpenRef = useRef(false)
  draftOpenRef.current = draft !== null

  const close = useCallback(() => {
    flushSync(() => {
      anchorRef.current = null
      setMenu(null)
      setDraft(null)
    })
  }, [])

  const sync = useCallback(() => {
    const root = rootRef.current
    if (!root || !enabled) {
      if (!draftOpenRef.current) close()
      return
    }
    const selection = window.getSelection()
    if (!selection || selection.isCollapsed || selection.rangeCount === 0) {
      // Clicked outside (or dragged to zero): discard the menu and a pending
      // draft with it. The caret moving into the popup (textarea autofocus)
      // also collapses the document selection — that is not a discard.
      const active = document.activeElement as HTMLElement | null
      if (active?.closest?.('.artifact-selection-menu, .artifact-annotation-editor')) return
      close()
      return
    }
    const range = selection.getRangeAt(0)
    if (!root.contains(range.commonAncestorContainer)) {
      close()
      return
    }
    const quote = range.toString().trim()
    if (!quote) { close(); return }
    const prefix = document.createRange()
    prefix.selectNodeContents(root)
    prefix.setEnd(range.startContainer, range.startOffset)
    const start = (root.textContent ?? '').indexOf(quote, prefix.toString().length)
    if (start < 0) { close(); return }
    anchorRef.current = range.cloneRange()
    const rect = rangeRect(range)
    const below = rect.top < 120
    flushSync(() => {
      setMenu((prev) => ({
        x: Math.min(Math.max(90, rect.left + rect.width / 2), window.innerWidth - 90),
        y: below ? rect.bottom + 10 : rect.top - 10,
        below,
        quote,
        start,
        end: start + quote.length,
        seq: (prev?.seq ?? 0) + (prev && prev.start === start && prev.end === start + quote.length ? 0 : 1),
      }))
      // A fresh selection resets the kind picker; the previous draft was for
      // a different fragment.
      setDraft(null)
    })
  }, [enabled, rootRef, close])

  useEffect(() => {
    // Direct call, no rAF: the handler is cheap (one indexOf over the body
    // text), and a synchronous run commits the popup in the same frame the
    // selection settles.
    document.addEventListener('selectionchange', sync)
    return () => document.removeEventListener('selectionchange', sync)
  }, [sync])

  // Scroll: a bare menu disappears (the anchor left the viewport — Notion
  // hides it too); a typing draft stays glued to its fragment. Resize
  // follows the same rule.
  const menuOpen = menu !== null
  useEffect(() => {
    if (!menuOpen) return
    const onScroll = () => {
      if (!draftOpenRef.current) { close(); return }
      const anchor = anchorRef.current
      if (!anchor) { close(); return }
      try {
        const rect = rangeRect(anchor)
        if (rect.top === 0 && rect.bottom === 0) { close(); return }
        const below = rect.top < 120
        flushSync(() => {
          setMenu((prev) => prev ? {
            ...prev,
            x: Math.min(Math.max(90, rect.left + rect.width / 2), window.innerWidth - 90),
            y: below ? rect.bottom + 10 : rect.top - 10,
            below,
          } : prev)
        })
      } catch { close() }
    }
    window.addEventListener('scroll', onScroll, true)
    window.addEventListener('resize', onScroll)
    return () => {
      window.removeEventListener('scroll', onScroll, true)
      window.removeEventListener('resize', onScroll)
    }
  }, [menuOpen, close])

  useEffect(() => {
    if (!menuOpen) return
    const onKey = (event: KeyboardEvent) => { if (event.key === 'Escape') close() }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [menuOpen, close])

  return { menu, draft, setDraft, close, anchorRef }
}
