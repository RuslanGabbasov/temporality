import { useEffect, useRef, type CSSProperties, type ReactNode } from 'react'

// Module-level stack of mounted modals: Escape always closes only the
// top-most dialog, whatever the panel nesting or open order.
const stack: { close: () => void }[] = []

/**
 * Shared shell for the app's custom dialogs: renders the overlay + panel and
 * wires the interactions the raw divs never had — Escape closes, overlay
 * click closes. onClose must reset the caller's open state (use the same
 * handler as the dialog's cancel button). panelClassName ("tabbed") and
 * panelStyle (width, maxHeight…) pass through to the inner panel.
 */
export default function AppModal({ onClose, children, panelClassName, panelStyle }: {
  onClose: () => void
  children: ReactNode
  panelClassName?: string
  panelStyle?: CSSProperties
}) {
  const closeRef = useRef(onClose)
  closeRef.current = onClose
  useEffect(() => {
    const entry = { close: () => closeRef.current() }
    stack.push(entry)
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return
      if (stack[stack.length - 1] !== entry) return
      e.preventDefault()
      closeRef.current()
    }
    window.addEventListener('keydown', onKey)
    return () => {
      window.removeEventListener('keydown', onKey)
      const i = stack.indexOf(entry)
      if (i >= 0) stack.splice(i, 1)
    }
  }, [])
  return (
    <div className="modal-overlay" onClick={(e) => { if (e.target === e.currentTarget) closeRef.current() }}>
      <div className={`modal-panel ${panelClassName ?? ''}`.trimEnd()} style={panelStyle}>
        {children}
      </div>
    </div>
  )
}
