import type { ReactNode } from 'react'
import { uiLocale } from './i18n'

/** Full date-time for hover hints: everywhere the UI shows a bare clock time
 * («14:03:11»), the hover must answer «today or yesterday?» — the same instant
 * in a long format, locale-aware, with the weekday for near dates. */
export function fullDateTime(iso: string | Date): string {
  const d = typeof iso === 'string' ? new Date(iso) : iso
  if (Number.isNaN(d.getTime())) return ''
  return d.toLocaleString([uiLocale()], {
    year: 'numeric',
    month: 'short',
    day: 'numeric',
    weekday: 'short',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  })
}

/** Compact date-time for tight list rows («14:03» when the change is today,
 * «d mon hh:mm» otherwise, year only when it differs from the current one):
 * the date AND time both survive, just without the seconds/year bulk — the
 * full value stays one hover away via TimeHint. */
export function briefDateTime(iso: string | Date): string {
  const d = typeof iso === 'string' ? new Date(iso) : iso
  if (Number.isNaN(d.getTime())) return ''
  const culture = [uiLocale()]
  if (d.toDateString() === new Date().toDateString()) {
    return d.toLocaleTimeString(culture, { hour: '2-digit', minute: '2-digit' })
  }
  const sameYear = d.getFullYear() === new Date().getFullYear()
  return d.toLocaleString(culture, {
    day: 'numeric',
    month: 'short',
    ...(sameYear ? {} : { year: 'numeric' }),
    hour: '2-digit',
    minute: '2-digit',
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
