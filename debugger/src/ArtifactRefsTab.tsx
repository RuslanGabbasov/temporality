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
    <div style={{ display: 'flex', flexDirection: 'column', gap: '0.6rem', minHeight: '26rem', flex: 1 }}>
      <p style={{ color: 'var(--tm-text-3)', fontSize: '0.8rem', margin: 0 }}>
        {t('skills.editor.artifacts_hint') ?? 'Documents the skill points at without inlining: the agent loads them with artifact_read when the task matches. Approved documents only.'}
      </p>
      {/* Registry search above the table (docs/artifact-registry.md §8): the
          dropdown hangs from the input, attached refs below are a plain
          two-column table — artifact | applies-when note. The tab keeps a
          solid minHeight so the dialog opens tall on this tab, not collapsed
          to two cramped side-by-side columns. */}
      <div ref={wrapRef} style={{ position: 'relative', minWidth: 0 }}>
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
            maxHeight: '24rem', overflowY: 'auto', background: 'var(--tm-elevated, #1a1a1a)',
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
      {/* The refs table: artifact (title + id + remove) on the left, the
          per-ref note — the condition under which the agent loads the
          document — on the right. One grid template shared by the header and
          every row, like the other tables in the app. */}
      <div style={{ flex: 1, minHeight: 0, display: 'flex', flexDirection: 'column', border: '1px solid var(--tm-border)', borderRadius: '8px', overflow: 'hidden' }}>
        <div style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 1.4fr) minmax(0, 1fr)', gap: '0.6rem', padding: '0.45rem 0.7rem', background: 'var(--tm-raised)', fontSize: '0.68rem', textTransform: 'uppercase', letterSpacing: '0.04em', color: 'var(--tm-text-3)' }}>
          <span>{t('skills.editor.artifact_col') ?? 'Artifact'}</span>
          <span>{t('skills.editor.artifact_note_col') ?? 'Applies when'}</span>
        </div>
        <div style={{ flex: 1, minHeight: 0, overflowY: 'auto' }}>
          {refs.length === 0 && (
            <p style={{ color: 'var(--tm-text-3)', fontSize: '0.78rem', margin: 0, padding: '0.7rem' }}>
              {t('skills.editor.artifacts_empty') ?? 'No artifacts attached yet — search above and click a result.'}
            </p>
          )}
          {refs.map((ref, i) => (
            <div key={ref.id || i} style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 1.4fr) minmax(0, 1fr)', gap: '0.6rem', alignItems: 'center', padding: '0.45rem 0.7rem', borderTop: '1px solid var(--tm-border)' }}>
              <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', minWidth: 0 }}>
                <span style={{ display: 'flex', flexDirection: 'column', minWidth: 0, flex: 1 }}>
                  <strong style={{ fontSize: '0.85rem', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={knownTitles(ref.id) || ref.id}>
                    {knownTitles(ref.id) || ref.id}
                  </strong>
                  <code style={{ fontSize: '0.66rem', color: 'var(--tm-text-3)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{ref.id}</code>
                </span>
                <button
                  type="button"
                  aria-label={t('action.delete') ?? 'Delete'}
                  onClick={() => { touch?.('artifacts'); onChange(refs.filter((_, j) => j !== i)) }}
                  style={{ background: 'none', border: 0, cursor: 'pointer', color: 'inherit', opacity: 0.7, display: 'inline-flex', padding: '0 0.15rem', flexShrink: 0 }}
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
      </div>
      {refs.length > 0 && (
        <p style={{ fontSize: '0.72rem', color: 'var(--tm-text-3)', margin: 0 }}>
          {t('skills.editor.artifacts_count', { count: String(refs.length) }) ?? `Linked: ${refs.length}`}
        </p>
      )}
    </div>
  )
}
