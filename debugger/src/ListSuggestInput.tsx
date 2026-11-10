import { useEffect, useMemo, useRef, useState } from 'react'
import { Close } from '@carbon/icons-react'

export interface SuggestOption {
  /** The exact value stored in the list — the id the kernel checks against
   *  (agent id, project id, MCP server id, provider id, user id). */
  value: string
  /** The human name shown as the dropdown's main line and on the chip. */
  label: string
  /** Secondary line (raw id, email, server type) — the disambiguator. */
  sub?: string
}

export function filterOptions(options: SuggestOption[], needle: string): SuggestOption[] {
  const q = needle.trim().toLowerCase()
  if (!q) return options
  const hit = (s: string | undefined) => !!s && s.toLowerCase().includes(q)
  return options.filter((o) => hit(o.label) || hit(o.value) || hit(o.sub))
}

/** Multi-value input for allow-lists (execution identities): chips plus an
 *  autocomplete that opens on focus with the full live option list and
 *  narrows as you type. Picking does not close the dropdown — allow-lists
 *  are multi-pick by nature. Free text still works (Enter or comma commits
 *  what was typed): the backend keeps validating exact values, so a hand-
 *  typed id for an option this dialog did not know about is not lost.
 *  An empty list means "any" — the caller renders that hint. */
export function ListSuggestInput({ value, onChange, options, placeholder, invalid }: {
  value: string[]
  onChange: (next: string[]) => void
  options: SuggestOption[]
  placeholder?: string
  invalid?: boolean
}) {
  const [query, setQuery] = useState('')
  const [cursor, setCursor] = useState(0)
  const [open, setOpen] = useState(false)
  const wrapRef = useRef<HTMLDivElement | null>(null)
  const inputRef = useRef<HTMLInputElement | null>(null)

  // Close the dropdown on any outside click.
  useEffect(() => {
    const onDown = (event: MouseEvent) => {
      if (wrapRef.current && !wrapRef.current.contains(event.target as Node)) setOpen(false)
    }
    document.addEventListener('mousedown', onDown)
    return () => document.removeEventListener('mousedown', onDown)
  }, [])

  // The dropdown is the "what can I still add" list: already-chosen values
  // drop out, so every visible suggestion is actionable.
  const chosen = useMemo(() => new Set(value), [value])
  const suggestions = useMemo(
    () => filterOptions(options.filter((o) => !chosen.has(o.value)), query).slice(0, 50),
    [options, chosen, query],
  )

  const labelOf = (raw: string) => options.find((o) => o.value === raw)?.label ?? raw

  const add = (raw: string) => {
    const v = raw.trim()
    if (!v || chosen.has(v)) return
    onChange([...value, v])
    setQuery('')
    setCursor(0)
    inputRef.current?.focus()
  }
  const remove = (raw: string) => onChange(value.filter((x) => x !== raw))

  const onKeyDown = (event: React.KeyboardEvent<HTMLInputElement>) => {
    if (event.key === 'Enter' || event.key === ',') {
      event.preventDefault()
      if (open && suggestions[cursor]) add(suggestions[cursor].value)
      else if (query.trim()) add(query)
      return
    }
    if (event.key === 'Backspace' && query === '' && value.length > 0) {
      remove(value[value.length - 1])
      return
    }
    if (event.key === 'ArrowDown') {
      event.preventDefault()
      setOpen(true)
      setCursor((c) => Math.min(c + 1, Math.max(suggestions.length - 1, 0)))
    }
    if (event.key === 'ArrowUp') {
      event.preventDefault()
      setCursor((c) => Math.max(c - 1, 0))
    }
  }

  return (
    <div ref={wrapRef} style={{ position: 'relative' }}>
      <div
        onClick={() => { inputRef.current?.focus(); setOpen(true) }}
        style={{
          display: 'flex', flexWrap: 'wrap', gap: '0.3rem', alignItems: 'center',
          border: `1px solid ${invalid ? 'var(--tm-danger, #da1e28)' : 'var(--tm-border)'}`,
          borderRadius: '6px', padding: '0.35rem 0.5rem', minHeight: '2.2rem', cursor: 'text',
          background: 'var(--tm-surface-2, transparent)',
        }}
      >
        {value.map((raw) => (
          <span
            key={raw}
            className="suggest-chip"
            style={{
              display: 'inline-flex', alignItems: 'center', gap: '0.25rem',
              padding: '0.15rem 0.3rem 0.15rem 0.45rem', borderRadius: '5px',
              background: 'var(--tm-surface-3, rgba(127,127,127,.15))',
              fontSize: '0.78rem', maxWidth: '100%',
            }}
            title={raw}
          >
            <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{labelOf(raw)}</span>
            <button
              type="button"
              aria-label={`remove ${raw}`}
              onClick={(e) => { e.stopPropagation(); remove(raw) }}
              style={{ background: 'none', border: 0, cursor: 'pointer', padding: '0 0.1rem', display: 'inline-flex', color: 'inherit', opacity: 0.7 }}
            >
              <Close size={12} />
            </button>
          </span>
        ))}
        <input
          ref={inputRef}
          value={query}
          onChange={(e) => { setQuery(e.target.value); setOpen(true); setCursor(0) }}
          onFocus={() => setOpen(true)}
          onKeyDown={onKeyDown}
          placeholder={value.length === 0 ? placeholder : ''}
          style={{ flex: 1, minWidth: '8rem', border: 0, outline: 0, background: 'transparent', color: 'inherit', fontSize: '0.85rem', padding: '0.2rem 0' }}
        />
      </div>
      {open && suggestions.length > 0 && (
        <div
          style={{
            position: 'absolute', top: '100%', left: 0, right: 0, zIndex: 30,
            marginTop: '0.2rem', maxHeight: '15rem', overflowY: 'auto',
            background: 'var(--tm-elevated, #1a1a1a)', border: '1px solid var(--tm-border)',
            borderRadius: '8px', boxShadow: '0 8px 24px rgba(0,0,0,.3)',
          }}
        >
          {suggestions.map((o, i) => (
            <div
              key={o.value}
              className="suggest-row"
              onClick={() => add(o.value)}
              onMouseEnter={() => setCursor(i)}
              style={{
                display: 'flex', alignItems: 'center', gap: '0.5rem',
                padding: '0.4rem 0.7rem', cursor: 'pointer',
                background: i === cursor ? 'rgba(127,127,127,.14)' : 'transparent',
              }}
            >
              <span style={{ display: 'flex', flexDirection: 'column', minWidth: 0, flex: 1 }}>
                <span className="suggest-row-label" style={{ fontSize: '0.85rem', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{o.label}</span>
                {o.sub && <span className="suggest-row-sub" style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{o.sub}</span>}
              </span>
              <span className="suggest-row-value" style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)', fontFamily: 'monospace', flexShrink: 0 }}>{o.value}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
