import { useMemo, useState } from 'react'

/** One filter pick: a directory person or an org unit. The kind decides the
 *  matching semantics at the call site (exact id vs the unit's member set). */
export interface ActorPick {
  kind: 'user' | 'unit'
  id: string
  name: string
}

/** The universal «person or org unit» filter field (docs/org-structure.md):
 *  typing offers matching directory users AND matching org units; picking one
 *  pins the exact filter (chip with the display name + ✕ clears). The suggest
 *  candidates are the call site's domain — passed as an options callback (the
 *  artifacts page searches the whole directory; the runs list narrows to the
 *  loaded window's actors). Free text is reported through onDraftChange for
 *  call sites that keep a substring fallback (runs); the chip only exists for
 *  a picked entry. Purely presentational: no fetching, no filtering logic. */
export function ActorSuggestFilter({ pick, onPick, onDraftChange, options, placeholder, title, kindLabels, inputStyle, minQueryLength = 2 }: {
  pick: ActorPick | null
  onPick: (pick: ActorPick | null) => void
  /** Free-text echo for call sites with a substring fallback; absent — the
   *  draft stays fully local (artifacts: the same field is the title search
   *  and owns its own state). */
  onDraftChange?: (draft: string) => void
  /** Suggest candidates for the typed fragment (already domain-limited). */
  options: (draft: string) => ActorPick[]
  placeholder?: string
  title?: string
  /** Per-kind right-aligned labels in the dropdown («сотрудник» / «подразделение»). */
  kindLabels?: { user?: string; unit?: string }
  /** Merge with an existing field's styling (artifacts' unified search). */
  inputStyle?: React.CSSProperties
  minQueryLength?: number
}) {
  const [draft, setDraft] = useState('')
  const [focused, setFocused] = useState(false)
  const suggest = useMemo(() => {
    if (pick) return []
    const needle = draft.trim().toLowerCase()
    if (needle.length < minQueryLength) return []
    return options(needle)
  }, [draft, pick, options, minQueryLength])
  return (
    <div style={{ position: 'relative', minWidth: 0, width: '100%' }}>
      <input
        value={draft}
        onChange={(e) => {
          setDraft(e.target.value)
          // Typing releases the pin immediately (the chip's ✕ clears fully) —
          // the same release behavior both current pages implement by hand.
          onPick(null)
          onDraftChange?.(e.target.value)
        }}
        onFocus={() => setFocused(true)}
        onBlur={() => setTimeout(() => setFocused(false), 150)}
        placeholder={placeholder}
        title={title}
        style={{ width: '100%', background: 'var(--tm-bg)', border: `1px solid ${pick ? 'var(--tm-teal)' : 'var(--tm-border)'}`, borderRadius: '6px', color: 'var(--tm-text)', fontSize: '0.78rem', padding: '0.35rem 0.55rem', outline: 'none', minWidth: 0, ...inputStyle }}
      />
      {focused && suggest.length > 0 && (
        <div style={{ position: 'absolute', left: 0, right: 0, top: '100%', background: 'var(--tm-elevated)', border: '1px solid var(--tm-border)', borderRadius: '6px', zIndex: 3, overflow: 'hidden' }}>
          {suggest.map((candidate) => (
            <button
              key={`${candidate.kind}:${candidate.id}`}
              type="button"
              onClick={() => {
                setDraft('')
                setFocused(false)
                onPick(candidate)
              }}
              style={{ display: 'flex', gap: '0.4rem', alignItems: 'center', width: '100%', padding: '0.35rem 0.6rem', background: 'none', border: 'none', cursor: 'pointer', color: 'var(--tm-text)', fontSize: '0.78rem', textAlign: 'left' }}
            >
              {candidate.kind === 'user' ? '👤' : '🏢'} {candidate.name}
              <span style={{ color: 'var(--tm-text-3)', fontSize: '0.68rem', marginLeft: 'auto' }}>
                {candidate.kind === 'user' ? (kindLabels?.user ?? 'сотрудник') : (kindLabels?.unit ?? 'подразделение')}
              </span>
            </button>
          ))}
        </div>
      )}
      {pick && (
        <button
          type="button"
          onClick={() => { setDraft(''); onPick(null) }}
          title={pick.kind === 'user' ? (kindLabels?.user ?? 'сотрудник') : (kindLabels?.unit ?? 'подразделение')}
          style={{ position: 'absolute', right: '0.4rem', top: '50%', transform: 'translateY(-50%)', background: 'var(--tm-surface-3, rgba(127,127,127,.15))', border: 'none', borderRadius: 999, color: 'var(--tm-text)', cursor: 'pointer', fontSize: '0.7rem', padding: '0.05rem 0.45rem', maxWidth: '60%', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}
        >{pick.kind === 'user' ? '👤' : '🏢'} {pick.name} ✕</button>
      )}
    </div>
  )
}
