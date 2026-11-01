import { useMemo, useRef, useState } from 'react'

/** Chip editor with type-ahead suggestions (the policy dialog's models and
 *  MCP dimensions): picking from what actually exists beats typing ids from
 *  memory. Free typing stays allowed — the catalog is a hint, not a whitelist
 *  (a policy may reference a model no provider advertises yet). Enter adds
 *  the typed token or the highlighted suggestion; Backspace on an empty input
 *  pops the last chip. */
export default function SuggestChips({ id, value, onChange, suggestions, suggestionLabels, annotateChips = true, placeholder, invalid }: {
  id: string
  value: string[]
  onChange: (next: string[]) => void
  suggestions: string[]
  /** Display names for candidates (MCP server id → name); shown next to the
   *  token in the dropdown and — unless annotateChips is false — on the chip.
   *  Models use dropdown-only labels (the provider name is metadata; the chip
   *  must show exactly what the policy stores). */
  suggestionLabels?: Map<string, string>
  annotateChips?: boolean
  placeholder?: string
  /** Red border when the dialog-level save validation flagged this field. */
  invalid?: boolean
}) {
  const [text, setText] = useState('')
  const [open, setOpen] = useState(false)
  const [highlight, setHighlight] = useState(0)
  const wrapRef = useRef<HTMLDivElement | null>(null)

  const matches = useMemo(() => {
    const needle = text.trim().toLowerCase()
    const taken = new Set(value)
    return suggestions
      .filter((s) => !taken.has(s))
      .filter((s) => (needle ? s.toLowerCase().includes(needle) : true))
      .slice(0, 8)
  }, [text, suggestions, value])

  const add = (token: string) => {
    const t = token.trim()
    if (!t || value.includes(t)) { setText(''); setOpen(false); return }
    onChange([...value, t])
    setText('')
    setOpen(false)
  }

  const remove = (i: number) => onChange(value.filter((_, j) => j !== i))

  const onKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'Enter') {
      e.preventDefault()
      add(matches.length > 0 && open ? (matches[highlight] ?? matches[0]) : text)
    } else if (e.key === 'Backspace' && text === '' && value.length > 0) {
      onChange(value.slice(0, -1))
    } else if (e.key === 'ArrowDown' && open) {
      e.preventDefault()
      setHighlight((h) => Math.min(h + 1, matches.length - 1))
    } else if (e.key === 'ArrowUp' && open) {
      e.preventDefault()
      setHighlight((h) => Math.max(h - 1, 0))
    } else if (e.key === 'Escape') {
      setOpen(false)
    }
  }

  return (
    <div ref={wrapRef} style={{ position: 'relative' }}>
      <div
        style={{
          display: 'flex', flexWrap: 'wrap', gap: '0.3rem', alignItems: 'center',
          border: `1px solid ${invalid ? 'var(--tm-danger)' : 'var(--tm-border)'}`, borderRadius: '6px',
          padding: '0.3rem 0.4rem', background: 'var(--tm-surface)', minHeight: '2.2rem', cursor: 'text',
        }}
        onClick={(e) => { (e.currentTarget.querySelector('input') as HTMLInputElement | null)?.focus() }}
      >
        {value.map((chip, i) => (
          <span key={chip} style={{ display: 'inline-flex', alignItems: 'center', gap: '0.3rem', background: 'var(--tm-raised)', border: '1px solid var(--tm-border)', borderRadius: '999px', padding: '0.08rem 0.55rem', fontSize: '0.74rem', maxWidth: '100%' }}>
            <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
              {annotateChips && suggestionLabels?.get(chip) ? `${suggestionLabels.get(chip)} (${chip})` : chip}
            </span>
            <button
              type="button"
              aria-label="×"
              onClick={() => remove(i)}
              style={{ background: 'none', border: 0, cursor: 'pointer', color: 'var(--tm-text-3)', padding: 0, lineHeight: 1, fontSize: '0.8rem' }}
            >×</button>
          </span>
        ))}
        <input
          id={id}
          value={text}
          onChange={(e) => { setText(e.target.value); setOpen(true); setHighlight(0) }}
          onFocus={() => setOpen(true)}
          onBlur={() => { window.setTimeout(() => setOpen(false), 150) }}
          onKeyDown={onKeyDown}
          placeholder={value.length === 0 ? placeholder : ''}
          style={{ flex: 1, minWidth: '8rem', border: 'none', outline: 'none', background: 'none', color: 'var(--tm-text)', fontSize: '0.8rem', padding: '0.15rem 0.1rem' }}
        />
      </div>
      {open && matches.length > 0 && (
        <div style={{
          position: 'absolute', top: '100%', left: 0, right: 0, zIndex: 30, marginTop: '0.2rem',
          maxHeight: '15rem', overflowY: 'auto', background: 'var(--tm-elevated)',
          border: '1px solid var(--tm-border)', borderRadius: '8px', boxShadow: '0 8px 24px rgba(0,0,0,.3)',
        }}>
          {matches.map((s, i) => (
            <div
              key={s}
              onMouseDown={(e) => { e.preventDefault(); add(s) }}
              onMouseEnter={() => setHighlight(i)}
              style={{
                display: 'flex', alignItems: 'center', gap: '0.5rem', padding: '0.35rem 0.7rem', cursor: 'pointer',
                background: i === highlight ? 'var(--tm-raised)' : 'transparent', fontSize: '0.8rem',
              }}
            >
              <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', flex: 1, minWidth: 0 }}>{suggestionLabels?.get(s) ? `${suggestionLabels.get(s)} ` : ''}<span style={{ color: 'var(--tm-text-3)' }}>{s}</span></span>
              {value.includes(s) && <span style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)' }}>✓</span>}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
