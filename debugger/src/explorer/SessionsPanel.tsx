import { useCallback, useEffect, useState } from 'react'
import { Button, InlineNotification, TextArea, TextInput } from '@carbon/react'
import { workspaceApi, type ResearchSession } from '../workspaceApi'
import { sessionSnapshot, applySessionPayload, type SessionSnapshot } from './sessions'
import { useT } from '../i18n'

/** Research sessions (ТЗ §11): save the investigation, come back to it,
 *  continue from it. The payload is references only — opening re-resolves the
 *  current projection, notes and the conclusion are the researcher's own
 *  words and render visibly distinct from knowledge. */
export default function SessionsPanel({
  project, open, snapshot, currentID, onLoaded, onClose,
}: {
  project: string
  open: boolean
  /** Live explorer state, captured on every save. */
  snapshot: () => SessionSnapshot
  /** Session currently bound to the editor (updates on save), 0 = none. */
  currentID: number
  onLoaded: (id: number, title: string, state: SessionSnapshot) => void
  onClose: () => void
}) {
  const t = useT()
  const [sessions, setSessions] = useState<ResearchSession[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [title, setTitle] = useState('')
  const [notes, setNotes] = useState('')
  const [conclusion, setConclusion] = useState('')

  const reload = useCallback(async () => {
    if (!project.trim()) return
    setLoading(true); setError('')
    try {
      const page = await workspaceApi.listResearchSessions(project.trim())
      setSessions(page.sessions ?? [])
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Request failed')
    } finally {
      setLoading(false)
    }
  }, [project])

  useEffect(() => { if (open) void reload() }, [open, reload])

  // The editor edits the CURRENT position: opening a session loads its notes
  // and conclusion; moving the explorer afterwards keeps them until the next
  // open or save.
  async function save(asNew: boolean) {
    const effectiveTitle = title.trim() || currentTitle()
    if (!effectiveTitle) { setError(t('explorer.sessions_title_required') ?? 'Title is required'); return }
    const payload = sessionSnapshot({ ...snapshot(), notes, conclusion })
    setBusy(true); setError('')
    try {
      if (!asNew && currentID > 0) {
        await workspaceApi.updateResearchSession(currentID, { title: effectiveTitle, payload })
      } else {
        const created = await workspaceApi.createResearchSession({ project: project.trim(), title: effectiveTitle, payload })
        // The saved position is exactly the snapshot we just sent.
        onLoaded(created.id, created.title, { ...snapshot(), notes, conclusion })
      }
      await reload()
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Request failed')
    } finally {
      setBusy(false)
    }
  }

  function currentTitle(): string {
    return sessions.find((session) => session.id === currentID)?.title ?? ''
  }

  async function openSession(id: number) {
    setBusy(true); setError('')
    try {
      const session = await workspaceApi.getResearchSession(id)
      const state = applySessionPayload(session.payload)
      if (!state) { setError(t('explorer.sessions_broken') ?? 'Session payload is unreadable'); return }
      onLoaded(session.id, session.title, state)
      setTitle(session.title)
      setNotes(state.notes)
      setConclusion(state.conclusion)
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Request failed')
    } finally {
      setBusy(false)
    }
  }

  async function remove(id: number) {
    setBusy(true); setError('')
    try {
      await workspaceApi.deleteResearchSession(id)
      await reload()
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Request failed')
    } finally {
      setBusy(false)
    }
  }

  if (!open) return null

  return (
    <div style={{ border: '1px solid var(--tm-border)', borderRadius: 'var(--tm-radius-md)', padding: '0.75rem', marginBottom: '0.75rem' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', marginBottom: '0.6rem' }}>
        <div style={{ fontSize: '0.8rem', fontWeight: 600 }}>{t('explorer.sessions_title_panel') ?? 'Research sessions'}</div>
        <small style={{ color: 'var(--tm-muted)', fontSize: '0.7rem' }}>{t('explorer.sessions_hint') ?? ''}</small>
        <button onClick={onClose} style={{ marginLeft: 'auto', background: 'none', border: 'none', color: 'var(--tm-text-3)', cursor: 'pointer', fontSize: '0.9rem', lineHeight: 1 }}>✕</button>
      </div>

      {error && <InlineNotification kind="error" title="Error" subtitle={error} onClose={() => setError('')} lowContrast style={{ marginBottom: '0.5rem' }} />}

      {/* Save editor: the current research position + the researcher's own words */}
      <div style={{ display: 'grid', gap: '0.5rem', marginBottom: '0.75rem' }}>
        <TextInput
          id="session-title" size="sm" labelText={t('explorer.sessions_name') ?? 'Title'}
          value={title} onChange={(e) => setTitle(e.target.value)}
          placeholder={t('explorer.sessions_name_placeholder') ?? 'What was this investigation about?'}
        />
        <TextArea
          id="session-notes" rows={2} labelText={t('explorer.sessions_notes') ?? 'Notes'}
          value={notes} onChange={(e) => setNotes(e.target.value)}
          placeholder={t('explorer.sessions_notes_hint') ?? ''}
        />
        <TextArea
          id="session-conclusion" rows={2} labelText={t('explorer.sessions_conclusion') ?? 'Conclusion'}
          value={conclusion} onChange={(e) => setConclusion(e.target.value)}
          placeholder={t('explorer.sessions_conclusion_hint') ?? ''}
        />
        <div style={{ display: 'flex', gap: '0.5rem', flexWrap: 'wrap', alignItems: 'center' }}>
          <Button size="sm" onClick={() => void save(false)} disabled={busy}>
            {currentID > 0 ? (t('explorer.sessions_update') ?? 'Save') : (t('explorer.sessions_save') ?? 'Save session')}
          </Button>
          {currentID > 0 && (
            <Button size="sm" kind="secondary" onClick={() => void save(true)} disabled={busy}>
              {t('explorer.sessions_save_new') ?? 'Save as new'}
            </Button>
          )}
          {currentID > 0 && (
            <small style={{ color: 'var(--tm-muted)', fontSize: '0.7rem' }}>
              {t('explorer.sessions_bound') ?? 'editing'}: {currentTitle() || `#${currentID}`}
            </small>
          )}
        </div>
      </div>

      {/* Saved sessions of this project */}
      {loading ? (
        <small style={{ color: 'var(--tm-muted)', fontSize: '0.75rem' }}>…</small>
      ) : sessions.length === 0 ? (
        <small style={{ color: 'var(--tm-muted)', fontSize: '0.75rem' }}>{t('explorer.sessions_empty') ?? 'No saved sessions yet.'}</small>
      ) : (
        <div>
          {sessions.map((session) => (
            <div key={session.id} style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', padding: '0.35rem 0', borderBottom: '1px solid var(--tm-border)', fontSize: '0.78rem' }}>
              <button
                onClick={() => void openSession(session.id)}
                style={{ background: 'none', border: 'none', color: session.id === currentID ? 'var(--tm-teal)' : 'var(--tm-text)', cursor: 'pointer', padding: 0, fontSize: '0.78rem', textAlign: 'left', flex: 1, minWidth: 0 }}
                title={new Date(session.updated_at).toLocaleString()}
              >
                {session.id === currentID ? '● ' : ''}{session.title}
                <small style={{ display: 'block', color: 'var(--tm-muted)', fontSize: '0.68rem' }}>
                  {session.created_by || '—'} · {new Date(session.updated_at).toLocaleString()}
                </small>
              </button>
              <Button size="sm" kind="ghost" onClick={() => void openSession(session.id)} disabled={busy}>{t('explorer.sessions_open') ?? 'Open'}</Button>
              <Button size="sm" kind="danger--ghost" onClick={() => void remove(session.id)} disabled={busy}>{t('explorer.sessions_delete') ?? 'Delete'}</Button>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
