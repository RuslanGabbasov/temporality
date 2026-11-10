import { useEffect, useRef } from 'react'
import { ComboBox, type ComboBoxProps } from '@carbon/react'

/** Carbon ComboBox whose open menu escapes clipping contexts. Carbon anchors
 *  the listbox menu to the control with `position: absolute; top: 100%` —
 *  inside a scrollable modal that spills past the dialog's bottom border and
 *  drags the panel into a scroll just to reach the items. This happens with
 *  every big combobox in a dialog, so the fix lives here, reusable.
 *
 *  The menu element is NOT moved (React owns it — relocating it corrupts the
 *  reconciliation). Instead, while expanded it is pinned to the viewport with
 *  `position: fixed` coordinates: same element, same handlers, but ancestor
 *  overflow can no longer clip it. Opens upward when the space below the
 *  field is tighter than above; re-anchors on scroll and resize (capture
 *  listener catches the modal panel's own scroll); unpins when closed. */
export function FloatingComboBox<ItemType>(props: ComboBoxProps<ItemType>) {
  const wrapRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    const root = wrapRef.current
    if (!root) return

    // Pin the menu while the listbox is expanded; unpin (drop our inline
    // styles) once it closes, so nothing but Carbon's CSS controls it then.
    const sync = () => {
      const box = root.querySelector<HTMLElement>('.cds--list-box')
      const menu = root.querySelector<HTMLElement>('.cds--list-box__menu')
      if (!box || !menu) return
      const expanded = box.classList.contains('cds--list-box--expanded')
      if (!expanded) {
        if (menu.dataset.floatingPinned) {
          delete menu.dataset.floatingPinned
          menu.removeAttribute('style')
        }
        return
      }
      const rect = box.getBoundingClientRect()
      const below = window.innerHeight - rect.bottom
      const above = rect.top
      const openUp = above > below
      menu.dataset.floatingPinned = 'yes'
      menu.style.position = 'fixed'
      menu.style.left = `${rect.left}px`
      menu.style.width = `${rect.width}px`
      menu.style.maxHeight = `${Math.max(120, Math.min(320, Math.max(above, below) - 8))}px`
      if (openUp) {
        menu.style.top = 'auto'
        menu.style.bottom = `${window.innerHeight - rect.top + 2}px`
      } else {
        menu.style.bottom = 'auto'
        menu.style.top = `${rect.bottom + 2}px`
      }
    }

    // Carbon toggles the expanded class on the listbox — that flip is the
    // open/close event. Style writes are NOT watched (the observer filters to
    // class changes), so the pinning itself cannot loop the observer.
    const observer = new MutationObserver(sync)
    observer.observe(root, { subtree: true, attributes: true, attributeFilter: ['class'] })
    const reposition = () => sync()
    window.addEventListener('scroll', reposition, true)
    window.addEventListener('resize', reposition)
    return () => {
      observer.disconnect()
      window.removeEventListener('scroll', reposition, true)
      window.removeEventListener('resize', reposition)
    }
  }, [])

  return (
    <div ref={wrapRef} style={{ position: 'relative' }}>
      <ComboBox {...props} />
    </div>
  )
}
