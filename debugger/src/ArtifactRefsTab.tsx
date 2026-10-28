import { useEffect, useRef, useState } from 'react'
import { Tag, TextInput } from '@carbon/react'
import { Add, Close } from '@carbon/icons-react'
import { workspaceApi, type Artifact } from './workspaceApi'
import { useT } from './i18n'

export interface ArtifactRef {
  id: string
  note?: string
}

/** The skill's reference artifacts on their own tab (docs/artifact-registry.md
 *  §8): a server-side search over the registry — tens of thousands of
 *  documents never fit a combo box. Search by title, attach by click, keep
 *  the per-ref note ("when this document matters"). Approved artifacts only:
 *  skills never reference drafts (§8). */
export function ArtifactRefsTab({ refs, onChange, touch }: {
  refs: ArtifactRef[]
  onChange: (next: ArtifactRef[]) => void
  touch?: (section: string) => void
}) {
  const t = useT()
  const [query, setQuery] = useState('')
  const [results, setResults] = useState<Artifact[]>([])
  const [titles, setTitles] = useState<Map<string, string>>(new Map())
  const [searching, setSearching] = useState(false)
  const [open, setOpen] = useState(false)
  const seq = useRef(0)
  const wrapRef = useRef<HTMLDivElement | null>(null)

  const knownTitles = (id: string) => titles.get(id) ?? ''

  // Debounced server search: 300ms after the last keystroke, approved only.
  useEffect(() => {
    const needle = query.trim()
    const timer = setTimeout(() => {
      const my = ++seq.current
      setSearching(true)
      void workspaceApi.listArtifacts({ status: 'approved', q: needle || undefined, limit: 20 })
        .then((data) => {
          if (seq.current !== my) return
          const found = data.artifacts ?? []
          setResults(found)
          setTitles((prev) => {
            const next = new Map(prev)
            for (const a of found) next.set(a.id, a.title)
            return next
          })
        })
        .catch(() => { if (seq.current === my) setResults([]) })
        .finally(() => { if (seq.current === my) setSearching(false) })
    }, 300)
    return () => clearTimeout(timer)
  }, [query])

  // Selected ids whose titles are unknown still deserve their names: one
  // lookup pass per ref list change (cheap, exact ids).
  useEffect(() => {
    const missing = refs.map((r) => r.id).filter((id) => id && !titles.has(id))
    for (const id of missing) {
      void workspaceApi.getArtifact(id)
        .then((data) => setTitles((prev) => new Map(prev).set(id, data.artifact.title)))
        .catch(() => setTitles((prev) => new Map(prev).set(id, id)))
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [refs.length])

  const attach = (id: string) => {
    if (!id || refs.some((r) => r.id === id)) return
    touch?.('artifacts')
    onChange([...refs, { id }])
    setOpen(false)
  }

  const dropdownOpen = open && (query.trim() !== '' || results.length > 0)

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: '0.6rem' }}>
      <p style={{ color: 'var(--tm-text-3)', fontSize: '0.8rem', margin: 0 }}>
        {t('skills.editor.artifacts_hint') ?? 'Documents the skill points at without inlining: the agent loads them with artifact_read when the task matches. Approved documents only.'}
      </p>
      <div ref={wrapRef} style={{ position: 'relative' }} onFocus={() => setOpen(true)}>
        <div onBlur={() => { window.setTimeout(() => setOpen(false), 150) }}>
          <TextInput
            id="artifact-refs-search"
            size="sm"
            labelText={t('skills.editor.artifacts_search') ?? 'Search the registry'}
            placeholder={t('skills.editor.artifacts_search_placeholder') ?? 'Start typing a title…'}
            value={query}
            onChange={(e: React.ChangeEvent<HTMLInputElement>) => { setQuery(e.target.value); setOpen(true) }}
          />
        </div>
        {dropdownOpen && (
          <div style={{
            position: 'absolute', top: '100%', left: 0, right: 0, zIndex: 20, marginTop: '0.2rem',
            maxHeight: '16rem', overflowY: 'auto', background: 'var(--tm-elevated, #1a1a1a)',
            border: '1px solid var(--tm-border)', borderRadius: '8px', boxShadow: '0 8px 24px rgba(0,0,0,.3)',
          }}>
            {results.length === 0 && (
              <div style={{ padding: '0.5rem 0.7rem', fontSize: '0.8rem', color: 'var(--tm-text-3)' }}>
                {searching ? (t('common.loading') ?? 'Loading…') : (t('skills.editor.artifacts_none') ?? 'Nothing in the registry matches.')}
              </div>
            )}
            {results.map((a) => {
              const attached = refs.some((r) => r.id === a.id)
              return (
                <div
                  key={a.id}
                  onClick={() => attach(a.id)}
                  style={{
                    display: 'flex', alignItems: 'center', gap: '0.5rem', padding: '0.4rem 0.7rem', cursor: attached ? 'default' : 'pointer',
                    opacity: attached ? 0.55 : 1,
                  }}
                >
                  <span style={{ display: 'flex', flexDirection: 'column', minWidth: 0, flex: 1 }}>
                    <span style={{ fontSize: '0.85rem', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{a.title}</span>
                    <span style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{a.id}</span>
                  </span>
                  {(a.tags ?? []).slice(0, 3).map((tag) => <Tag key={tag} size="sm" type="cool-gray">#{tag}</Tag>)}
                  {attached
                    ? <span style={{ fontSize: '0.72rem', color: 'var(--tm-text-3)' }}>{t('skills.editor.artifact_attached') ?? 'attached'}</span>
                    : <Add size={14} />}
                </div>
              )
            })}
          </div>
        )}
      </div>

      {refs.length > 0 && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: '0.4rem' }}>
          {refs.map((ref, i) => (
            <div key={ref.id || i} style={{ display: 'flex', flexDirection: 'column', gap: '0.25rem', padding: '0.55rem 0.65rem', border: '1px solid var(--tm-border)', borderRadius: '8px' }}>
              <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
                <strong style={{ fontSize: '0.85rem', flex: 1, minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                  {knownTitles(ref.id) || ref.id}
                </strong>
                <code style={{ fontSize: '0.68rem', color: 'var(--tm-text-3)' }}>{ref.id}</code>
                <button
                  type="button"
                  aria-label={t('action.delete') ?? 'Delete'}
                  onClick={() => { touch?.('artifacts'); onChange(refs.filter((_, j) => j !== i)) }}
                  style={{ background: 'none', border: 0, cursor: 'pointer', color: 'inherit', opacity: 0.7, display: 'inline-flex', padding: '0 0.15rem' }}
                >
                  <Close size={13} />
                </button>
              </div>
              <TextInput
                id={`artifact-ref-note-${i}`}
                size="sm"
                hideLabel
                labelText="note"
                value={ref.note ?? ''}
                placeholder={t('skills.editor.artifact_note_placeholder') ?? 'When this document matters'}
                onChange={(e: React.ChangeEvent<HTMLInputElement>) => { touch?.('artifacts'); onChange(refs.map((r, j) => (j === i ? { ...r, note: e.target.value } : r))) }}
              />
            </div>
          ))}
        </div>
      )}
      {refs.length === 0 && (
        <p style={{ color: 'var(--tm-text-3)', fontSize: '0.78rem', margin: 0 }}>
          {t('skills.editor.artifacts_empty') ?? 'No artifacts attached yet — search above and click a result.'}
        </p>
      )}
    </div>
  )
}
