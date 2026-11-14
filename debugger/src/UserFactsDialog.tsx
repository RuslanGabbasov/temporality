import { useCallback, useEffect, useState } from 'react'
import { Button, TextInput, Heading } from '@carbon/react'
import AppModal from './Modal'
import { workspaceApi, type UserFact } from './workspaceApi'
import { useT } from './i18n'

/** Self-service profile facts (docs/inbound-messages.md §4.3): the explicit
 *  half of the per-user picture — preferences and durable facts the dialogs
 *  work against. Agents accumulate them with user_remember (supervisor-
 *  validated against duplicates before saving); the owner curates the profile
 *  here — edit, add and delete. */
export default function UserFactsDialog({ userId, onClose }: { userId: string; onClose: () => void }) {
  const t = useT()
  const [facts, setFacts] = useState<UserFact[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [draft, setDraft] = useState('')
  const [editingId, setEditingId] = useState<number | null>(null)
  const [editDraft, setEditDraft] = useState('')
  const [busy, setBusy] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const page = await workspaceApi.userFacts(userId)
      setFacts(page.facts ?? [])
      setError('')
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setLoading(false)
    }
  }, [userId])
  useEffect(() => { void load() }, [load])

  const fail = (err: unknown) => setError(err instanceof Error ? err.message : String(err))

  const add = async () => {
    const text = draft.trim()
    if (!text || busy) return
    setBusy(true)
    setError('')
    try {
      const created = await workspaceApi.addUserFact(userId, text)
      setFacts((prev) => (prev.some((f) => f.id === created.id) ? prev : [created, ...prev]))
      setDraft('')
    } catch (err) { fail(err) }
    finally { setBusy(false) }
  }

  const saveEdit = async (fact: UserFact) => {
    const text = editDraft.trim()
    if (!text || busy) return
    setBusy(true)
    setError('')
    try {
      const updated = await workspaceApi.updateUserFact(userId, fact.id, text)
      setFacts((prev) => prev.map((f) => (f.id === fact.id ? updated : f)))
      setEditingId(null)
    } catch (err) { fail(err) }
    finally { setBusy(false) }
  }

  const remove = async (fact: UserFact) => {
    if (busy) return
    setBusy(true)
    setError('')
    try {
      await workspaceApi.deleteUserFact(userId, fact.id)
      setFacts((prev) => prev.filter((f) => f.id !== fact.id))
    } catch (err) { fail(err) }
    finally { setBusy(false) }
  }

  return (
    <AppModal onClose={onClose} panelStyle={{ width: '480px', maxHeight: '85vh', overflowY: 'auto' }}>
      <Heading style={{ fontSize: '1.05rem', marginBottom: '0.35rem' }}>{t('facts.title') ?? 'Мои факты и предпочтения'}</Heading>
      <p style={{ color: 'var(--tm-text-2)', fontSize: '0.78rem', margin: '0 0 0.75rem' }}>
        {t('facts.hint') ?? 'Что агенты знают о вас и используют в диалогах: стиль ответов, чем вы руководите, рабочие договорённости. Агенты добавляют факты с валидацией на дубликаты; здесь вы можете их редактировать, дополнять и удалять.'}
      </p>
      <div style={{ display: 'flex', gap: '0.5rem', marginBottom: '0.75rem' }}>
        <TextInput
          id="fact-new"
          labelText=""
          hideLabel
          placeholder={t('facts.add_placeholder') ?? 'Новый факт, например: предпочитает краткие ответы'}
          value={draft}
          onChange={(e: React.ChangeEvent<HTMLInputElement>) => setDraft(e.target.value)}
          onKeyDown={(e: React.KeyboardEvent<HTMLInputElement>) => { if (e.key === 'Enter') { e.preventDefault(); void add() } }}
          style={{ flex: 1 }}
        />
        <Button size="sm" onClick={() => void add()} disabled={busy || !draft.trim()}>{t('action.add') ?? 'Добавить'}</Button>
      </div>
      {error && (
        <p style={{ color: 'var(--tm-danger)', fontSize: '0.75rem', marginBottom: '0.5rem' }}>{error}</p>
      )}
      {loading && <p style={{ color: 'var(--tm-text-3)', fontSize: '0.8rem' }}>{t('facts.loading') ?? 'Загрузка…'}</p>}
      {!loading && facts.length === 0 && (
        <p style={{ color: 'var(--tm-text-3)', fontSize: '0.8rem' }}>{t('facts.empty') ?? 'Пока нет фактов — агенты начнут наполнять профиль по мере диалогов.'}</p>
      )}
      <div style={{ display: 'flex', flexDirection: 'column', gap: '0.4rem' }}>
        {facts.map((fact) => (
          <div key={fact.id} style={{ border: '1px solid var(--tm-border)', borderRadius: '6px', padding: '0.45rem 0.6rem', display: 'flex', flexDirection: 'column', gap: '0.3rem' }}>
            {editingId === fact.id ? (
              <>
                <TextInput
                  id={`fact-edit-${fact.id}`}
                  labelText=""
                  hideLabel
                  value={editDraft}
                  onChange={(e: React.ChangeEvent<HTMLInputElement>) => setEditDraft(e.target.value)}
                  onKeyDown={(e: React.KeyboardEvent<HTMLInputElement>) => { if (e.key === 'Enter') { e.preventDefault(); void saveEdit(fact) } }}
                />
                <div style={{ display: 'flex', gap: '0.4rem', justifyContent: 'flex-end' }}>
                  <Button size="sm" kind="ghost" onClick={() => void saveEdit(fact)} disabled={busy || !editDraft.trim()}>{t('action.save') ?? 'Сохранить'}</Button>
                  <Button size="sm" kind="ghost" onClick={() => setEditingId(null)}>{t('action.cancel') ?? 'Отмена'}</Button>
                </div>
              </>
            ) : (
              <div style={{ display: 'flex', alignItems: 'flex-start', gap: '0.5rem' }}>
                <span style={{ flex: 1, fontSize: '0.82rem', color: 'var(--tm-text)', wordBreak: 'break-word' }}>
                  {fact.fact}
                  <span style={{ marginLeft: '0.4rem', fontSize: '0.66rem', color: 'var(--tm-text-3)' }}>
                    {fact.origin === 'agent' ? (t('facts.origin_agent') ?? 'от агента') : (t('facts.origin_manual') ?? 'вами')}
                  </span>
                </span>
                <span style={{ display: 'flex', gap: '0.2rem', flexShrink: 0 }}>
                  <button
                    type="button"
                    title={t('action.edit') ?? 'Редактировать'}
                    onClick={() => { setEditingId(fact.id); setEditDraft(fact.fact) }}
                    style={{ background: 'none', border: 0, color: 'var(--tm-text-3)', cursor: 'pointer', fontSize: '0.8rem', padding: '0 0.15rem' }}
                  >✎</button>
                  <button
                    type="button"
                    title={t('action.delete') ?? 'Удалить'}
                    onClick={() => void remove(fact)}
                    style={{ background: 'none', border: 0, color: 'var(--tm-text-3)', cursor: 'pointer', fontSize: '0.8rem', padding: '0 0.15rem' }}
                  >✕</button>
                </span>
              </div>
            )}
          </div>
        ))}
      </div>
      <div style={{ display: 'flex', justifyContent: 'flex-end', marginTop: '0.85rem' }}>
        <Button onClick={onClose}>{t('action.close') ?? 'Закрыть'}</Button>
      </div>
    </AppModal>
  )
}
