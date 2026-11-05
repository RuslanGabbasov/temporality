import type { ReactNode } from 'react'

/** Full date-time for hover hints: everywhere the UI shows a bare clock time
 * («14:03:11»), the hover must answer «today or yesterday?» — the same instant
 * in a long format, locale-aware, with the weekday for near dates. */
export function fullDateTime(iso: string | Date): string {
  const d = typeof iso === 'string' ? new Date(iso) : iso
  if (Number.isNaN(d.getTime())) return ''
  return d.toLocaleString([], {
    year: 'numeric',
    month: 'short',
    day: 'numeric',
    weekday: 'short',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  })
}

/** A time text with the full date-time hover hint. Drop-in for bare
 * `{shortTime(x)}` render sites: same visuals (inherit font/color), plus the
 * native title tooltip and a dotted underline signaling there is more on
 * hover. */
export function TimeHint({ value, hint, children, underline = true, style }: {
  value?: string
  hint?: string
  children?: ReactNode
  underline?: boolean
  style?: React.CSSProperties
}) {
  const text = children ?? value ?? ''
  const tooltip = hint ?? fullDateTime(value ?? '')
  if (!tooltip) return <>{text}</>
  return (
    <span
      title={tooltip}
      style={{ textDecorationLine: underline ? 'underline dotted' : undefined, textDecorationThickness: '1px', textUnderlineOffset: '2px', ...style }}
    >
      {text}
    </span>
  )
}
