import { useEffect, useMemo, useRef, useState } from 'react'
import { Button, InlineNotification, Tag, TextInput } from '@carbon/react'
import { observationApi, type ExploreConflict, type KnowledgeItem } from '../observationApi'
import { useT } from '../i18n'
import { stateLabel } from './shared'

/** Contradiction discovery and comparison (ТЗ §10). Discovery mode runs
 *  compare over the whole visible base (no targets); the dossier passes
 *  conflicts it derived from the item's contradict-edges. Resolution goes
 *  only through the existing lifecycle mechanisms (§10.3): challenging a
 *  side, or superseding it in favor of the other statement. The panel never
 *  treats the newer statement as the true one (§10.2). */

export function conflictBasisKey(basis: string): string {
  if (basis === 'explicit:contradicts') return 'explorer.conflict_basis_explicit'
  if (basis === 'candidate:same-topic-different-state') return 'explorer.conflict_basis_candidate'
  if (basis === 'resolved:replacement') return 'explorer.conflict_basis_resolved'
  return 'explorer.conflict_basis_other'
}

export function conflictBasisLabel(basis: string, t: (key: string) => string): string {
  const key = conflictBasisKey(basis)
  const text = t(key)
  return text !== key ? text : basis
}

const BASIS_TAG: Record<string, 'red' | 'warm-gray' | 'green'> = {
  'explicit:contradicts': 'red',
  'candidate:same-topic-different-state': 'warm-gray',
  'resolved:replacement': 'green',
}

/** Fetch propositions behind knowledge ids (bounded, tolerant of misses). */
function useKnowledgeIndex(project: string, ids: string[], limit = 64): Map<string, KnowledgeItem> {
  const [index, setIndex] = useState<Map<string, KnowledgeItem>>(new Map())
  const key = useMemo(() => ids.slice(0, limit).sort().join('\x00'), [ids, limit])
  useEffect(() => {
    if (!project.trim() || !key) return
    let cancelled = false
    const wanted = key.split('\x00').filter((id) => id.trim() !== '')
    ;(async () => {
      const fetched: KnowledgeItem[] = []
      const CHUNK = 8
      for (let i = 0; i < wanted.length; i += CHUNK) {
        if (cancelled) return
        const chunk = await Promise.all(wanted.slice(i, i + CHUNK).map(async (id) => {
          try {
            const page = await observationApi.knowledge(project.trim(), undefined, undefined, { knowledge_id: id, limit: 1 })
            return page.knowledge[0]
          } catch { return undefined }
        }))
        for (const item of chunk) if (item) fetched.push(item)
      }
      if (!cancelled) setIndex(new Map(fetched.map((item) => [item.id, item])))
    })()
    return () => { cancelled = true }
  }, [project, key])
  return index
}

function Side({ item, id, index, t, onOpenDossier }: {
  item?: KnowledgeItem
  id: string
  index: Map<string, KnowledgeItem>
  t: (key: string) => string
  onOpenDossier?: (id: string) => void
}) {
  const known = item ?? index.get(id)
  return (
    <div style={{ flex: 1, minWidth: '220px', border: '1px solid var(--tm-border)', borderRadius: 'var(--tm-radius-md)', padding: '0.5rem 0.6rem' }}>
      <div style={{ display: 'flex', gap: '0.4rem', alignItems: 'center', marginBottom: '0.3rem' }}>
        {known
          ? <Tag type={known.state === 'invalidated' || known.state === 'superseded' || known.state === 'corrected' ? 'red' : known.state === 'challenged' ? 'warm-gray' : 'green'} size="sm">{stateLabel(known.state, t)}</Tag>
          : <Tag type="gray" size="sm">{t('explorer.conflict_unknown_state') ?? 'state n/a'}</Tag>}
        <span style={{ fontSize: '0.65rem', color: 'var(--tm-muted)', fontFamily: 'var(--tm-font-mono)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{id}</span>
      </div>
      <div style={{ fontSize: '0.78rem', lineHeight: 1.45, marginBottom: '0.35rem' }}>{known?.proposition ?? t('explorer.conflict_proposition_missing') ?? '—'}</div>
      {known && (known.created_at || known.project) && (
        <div style={{ fontSize: '0.65rem', color: 'var(--tm-muted)' }}>
          {known.created_at ? new Date(known.created_at).toLocaleDateString() : ''}{known.project ? ` · ${known.project}` : ''}
        </div>
      )}
      {onOpenDossier && <Button size="sm" kind="ghost" onClick={() => onOpenDossier(id)}>{t('explorer.dossier_open') ?? 'Open dossier'}</Button>}
    </div>
  )
}

export default function ConflictPanel({ project, conflicts: given, revision = 0, onChanged, onOpenDossier }: {
  project: string
  /** Dossier mode: conflicts derived from the item's edges. Omitted → discovery. */
  conflicts?: ExploreConflict[]
  revision?: number
  onChanged?: () => void
  onOpenDossier?: (id: string) => void
}) {
  const t = useT()
  const discovery = given === undefined
  const [conflicts, setConflicts] = useState<ExploreConflict[]>([])
  const [notes, setNotes] = useState<string[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [reason, setReason] = useState('')
  const [busy, setBusy] = useState(false)
  const [actionError, setActionError] = useState('')
  const [localRev, setLocalRev] = useState(0)
  const seq = useRef(0)

  useEffect(() => {
    if (!discovery) { setConflicts(given ?? []); return }
    if (!project.trim()) return
    const current = ++seq.current
    setLoading(true); setError('')
    ;(async () => {
      try {
        const res = await observationApi.explore({ project: project.trim(), operation: 'compare', max_items: 30 })
        if (current !== seq.current) return
        setConflicts(res.conflicts ?? [])
        setNotes(res.notes ?? [])
      } catch (e) {
        if (current === seq.current) setError(e instanceof Error ? e.message : 'Request failed')
      } finally {
        if (current === seq.current) setLoading(false)
      }
    })()
    return () => { seq.current++ }
  }, [project, discovery, given, revision, localRev])

  const ids = useMemo(() => {
    const unique: string[] = []
    for (const conflict of conflicts) {
      for (const id of conflict.knowledge_ids) if (!unique.includes(id)) unique.push(id)
    }
    return unique
  }, [conflicts])
  const index = useKnowledgeIndex(project, ids)

  async function resolve(kind: 'challenge' | 'prefer', subjectID: string, preferredID?: string) {
    if (!reason.trim()) return
    setBusy(true); setActionError('')
    try {
      const actor = { id: 'human', type: 'human' as const }
      if (kind === 'challenge') {
        await observationApi.challenge({ knowledge_id: subjectID, project, actor, reason: reason.trim() })
      } else if (preferredID) {
        await observationApi.invalidate({
          knowledge_id: subjectID, project, actor,
          reason: reason.trim(), replacement_id: preferredID,
        })
      }
      setReason('')
      setLocalRev((r) => r + 1)
      onChanged?.()
    } catch (e) {
      setActionError(e instanceof Error ? e.message : 'Request failed')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div>
      {discovery && (
        <div style={{ fontSize: '0.72rem', color: 'var(--tm-muted)', marginBottom: '0.6rem' }}>
          {t('explorer.conflicts_hint') ?? ''}
        </div>
      )}
      {error && <InlineNotification kind="error" title="Error" subtitle={error} onClose={() => setError('')} lowContrast style={{ marginBottom: '0.6rem' }} />}
      {actionError && <InlineNotification kind="error" title="Error" subtitle={actionError} onClose={() => setActionError('')} lowContrast style={{ marginBottom: '0.6rem' }} />}
      {loading && <div style={{ fontSize: '0.75rem', color: 'var(--tm-muted)', marginBottom: '0.5rem' }}>…</div>}

      {conflicts.length === 0 && !loading && (
        <div style={{ border: '1px dashed var(--tm-border)', borderRadius: 'var(--tm-radius-md)', padding: '0.75rem', color: 'var(--tm-muted)', fontSize: '0.8rem' }}>
          {t('explorer.conflicts_empty') ?? 'No contradictions found in the visible base.'}
        </div>
      )}

      {conflicts.map((conflict, i) => {
        const [leftID, rightID] = conflict.knowledge_ids
        return (
          <div key={`${conflict.knowledge_ids.join('-')}-${i}`} style={{ border: '1px solid var(--tm-border)', borderRadius: 'var(--tm-radius-md)', padding: '0.6rem 0.75rem', marginBottom: '0.6rem' }}>
            <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'center', flexWrap: 'wrap', marginBottom: '0.45rem' }}>
              <Tag type={BASIS_TAG[conflict.basis] ?? 'gray'} size="sm">{conflictBasisLabel(conflict.basis, t)}</Tag>
              {conflict.resolved && <Tag type="green" size="sm">{t('explorer.conflict_resolved_tag') ?? 'resolved'}</Tag>}
              <span style={{ fontSize: '0.7rem', color: 'var(--tm-text-2)' }}>{conflict.detail}</span>
            </div>

            {(conflict.context_differences ?? []).length > 0 && (
              <div style={{ fontSize: '0.7rem', color: 'var(--tm-muted)', marginBottom: '0.45rem' }}>
                {t('explorer.conflict_context') ?? 'Context differences'}: {(conflict.context_differences ?? []).join('; ')}
              </div>
            )}

            <div style={{ display: 'flex', gap: '0.6rem', flexWrap: 'wrap' }}>
              <Side item={index.get(leftID)} id={leftID} index={index} t={t} onOpenDossier={onOpenDossier} />
              <Side item={index.get(rightID)} id={rightID} index={index} t={t} onOpenDossier={onOpenDossier} />
            </div>

            {conflict.resolved ? (
              <div style={{ fontSize: '0.75rem', color: 'var(--tm-success)', marginTop: '0.4rem' }}>
                {t('explorer.conflict_resolution') ?? 'Resolution'}: {conflict.resolution}
              </div>
            ) : (
              <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'center', flexWrap: 'wrap', marginTop: '0.5rem' }}>
                <TextInput
                  id={`conflict-reason-${i}`}
                  size="sm" labelText="" hideLabel style={{ flex: 1, minWidth: '220px' }}
                  placeholder={t('runs.knowledge_reason_placeholder') ?? 'Reason'}
                  value={reason} onChange={(e) => setReason(e.target.value)}
                />
                <Button size="sm" kind="secondary" disabled={!reason.trim() || busy} onClick={() => void resolve('challenge', leftID)}>{t('explorer.conflict_challenge_left') ?? 'Challenge ←'}</Button>
                <Button size="sm" kind="secondary" disabled={!reason.trim() || busy} onClick={() => void resolve('challenge', rightID)}>{t('explorer.conflict_challenge_right') ?? 'Challenge →'}</Button>
                <Button size="sm" kind="secondary" disabled={!reason.trim() || busy} onClick={() => void resolve('prefer', leftID, rightID)}>{t('explorer.conflict_prefer_left') ?? 'Keep ←'}</Button>
                <Button size="sm" kind="secondary" disabled={!reason.trim() || busy} onClick={() => void resolve('prefer', rightID, leftID)}>{t('explorer.conflict_prefer_right') ?? 'Keep →'}</Button>
              </div>
            )}
          </div>
        )
      })}

      {notes.length > 0 && !loading && (
        <div style={{ fontSize: '0.7rem', color: 'var(--tm-muted)' }}>{notes.join(' · ')}</div>
      )}
    </div>
  )
}
