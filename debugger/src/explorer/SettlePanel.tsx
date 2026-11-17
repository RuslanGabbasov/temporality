import { useCallback, useEffect, useMemo, useState } from 'react'
import { Button, InlineNotification, Tag, TextInput } from '@carbon/react'
import { observationApi, type KnowledgeItem } from '../observationApi'
import { whoami } from '../kernelApi'
import { useT } from '../i18n'
import { TimeHint } from '../timeHint'
import { briefDateTime } from '../timeHint'

function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : 'Request failed'
}

function shortID(value?: string) {
  if (!value) return '—'
  return value.length > 14 ? `${value.slice(0, 13)}…` : value
}

// The challenge provenance of an item: the newest challenged transition in
// its history names the rule (aging.v1, extraction-contradiction.v1, …) and
// the recorded reason — the two facts a reader needs to settle the item.
function challengeOf(item: KnowledgeItem): { rule: string; reason: string } {
  for (let i = item.history.length - 1; i >= 0; i--) {
    const transition = item.history[i]
    if (transition.type === 'knowledge.challenged') {
      return { rule: transition.rule ?? '', reason: transition.reason ?? '' }
    }
  }
  return { rule: '', reason: '' }
}

const RULE_LABELS: Record<string, string> = {
  'aging.v1': 'aging (14д без подтверждения/использования)',
  'extraction-contradiction.v1': 'противоречие из рана',
  'hint-outcome.v1': 'вердикт по подсказке',
  'manual.v1': 'вручную',
}

// The settle queue for a backlog of challenged knowledge (a live installation
// accumulated 2289 items — more than any human clicks through one by one):
// paged challenged list with the challenge rule/reason per row, rule filter,
// select-all, and one bulk verdict per click. The projection refreshes via
// the revision callback so the map repaints immediately.
export default function SettlePanel({ project, onRevision }: { project: string; onRevision: () => void }) {
  const t = useT()
  const [items, setItems] = useState<KnowledgeItem[]>([])
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [note, setNote] = useState('')
  const [ruleFilter, setRuleFilter] = useState('')
  const [busy, setBusy] = useState(false)
  const [done, setDone] = useState('')
  const [actorID, setActorID] = useState('human-requester')

  useEffect(() => {
    void whoami().then((identity) => {
      if (identity.user_id || identity.subject) setActorID(identity.user_id || identity.subject)
    }).catch(() => { /* the fallback actor id works — the gate still enforces rights */ })
  }, [])

  const load = useCallback(async () => {
    setLoading(true); setError('')
    try {
      const page = await observationApi.knowledge(project, undefined, undefined, { state: 'challenged', limit: 500 })
      setItems(page.knowledge ?? [])
      setSelected(new Set())
    } catch (failure) { setError(errorMessage(failure)) }
    finally { setLoading(false) }
  }, [project])

  useEffect(() => { if (project) void load() }, [project, load])

  const rows = useMemo(() => {
    if (!ruleFilter) return items
    return items.filter((item) => challengeOf(item).rule === ruleFilter)
  }, [items, ruleFilter])

  const rules = useMemo(() => {
    const counts = new Map<string, number>()
    for (const item of items) {
      const rule = challengeOf(item).rule || 'manual'
      counts.set(rule, (counts.get(rule) ?? 0) + 1)
    }
    return [...counts.entries()].sort((a, b) => b[1] - a[1])
  }, [items])

  const toggle = (id: string) => {
    setSelected((current) => {
      const next = new Set(current)
      if (next.has(id)) next.delete(id); else next.add(id)
      return next
    })
  }

  const apply = async (action: 'confirm' | 'invalidate') => {
    if (selected.size === 0) return
    setBusy(true); setError(''); setDone('')
    try {
      let recorded = 0
      const ids = [...selected]
      // The server caps one call at 500 ids; page the batch.
      for (let i = 0; i < ids.length; i += 500) {
        const reply = await observationApi.bulkKnowledge({
          project, actor: { id: actorID, type: 'human' }, action,
          reason: note.trim() || undefined, ids: ids.slice(i, i + 500),
        })
        recorded += reply.recorded ?? 0
      }
      setDone(`${recorded}`)
      setSelected(new Set())
      onRevision()
      await load()
    } catch (failure) { setError(errorMessage(failure)) }
    finally { setBusy(false) }
  }

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: '0.6rem', minHeight: 0 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', flexWrap: 'wrap' }}>
        <strong>{t('explorer.settle_title') ?? 'Разбор оспоренных'}</strong>
        <Tag size="sm" type="warm-gray">{items.length}</Tag>
        <select
          value={ruleFilter}
          onChange={(e) => setRuleFilter(e.target.value)}
          className="toolbar-btn"
          style={{ maxWidth: '22rem' }}
          title={t('explorer.settle_rule_filter') ?? 'Фильтр по правилу, которым знание оспорено'}
        >
          <option value="">{t('explorer.settle_rule_all') ?? 'все правила'}</option>
          {rules.map(([rule, count]) => (
            <option key={rule} value={rule}>{(RULE_LABELS[rule] ?? rule)} · {count}</option>
          ))}
        </select>
        <button className="toolbar-btn" onClick={() => void load()} disabled={loading} title={t('common.refresh')}>↻</button>
        <span style={{ marginLeft: 'auto', display: 'flex', gap: '0.4rem', alignItems: 'center' }}>
          <span style={{ fontSize: '0.72rem', color: 'var(--tm-text-3)' }}>{t('explorer.settle_selected', { count: String(selected.size) }) ?? `выбрано: ${selected.size}`}</span>
          <button className="toolbar-btn" onClick={() => setSelected(new Set(rows.map((row) => row.id)))} disabled={rows.length === 0}>
            {t('explorer.settle_select_page') ?? 'выбрать всё'}
          </button>
          <button className="toolbar-btn" onClick={() => setSelected(new Set())} disabled={selected.size === 0}>
            {t('explorer.settle_clear') ?? 'снять'}
          </button>
        </span>
      </div>

      <TextInput
        id="settle-note"
        labelText={t('explorer.settle_reason_label') ?? 'Причина вердикта (необязательно)'}
        value={note}
        onChange={(e: React.ChangeEvent<HTMLInputElement>) => setNote(e.target.value)}
        placeholder={t('explorer.settle_reason_placeholder') ?? 'например: проверено на стенде / неактуально после миграции'}
        size="sm"
      />

      {error && <InlineNotification kind="error" title={t('common.error')} subtitle={error} onClose={() => setError('')} lowContrast />}
      {done && (
        <InlineNotification kind="success" title={t('explorer.settle_done') ?? 'Применено'} subtitle={t('explorer.settle_done_n', { count: done }) ?? `вердиктов записано: ${done}`} onClose={() => setDone('')} lowContrast />
      )}

      <div style={{ display: 'flex', gap: '0.5rem' }}>
        <Button size="sm" kind="primary" disabled={busy || selected.size === 0} onClick={() => void apply('confirm')}>
          {busy ? '…' : (t('explorer.settle_confirm') ?? 'Подтвердить выбранное')}
        </Button>
        <Button size="sm" kind="danger--primary" disabled={busy || selected.size === 0} onClick={() => void apply('invalidate')}>
          {busy ? '…' : (t('explorer.settle_invalidate') ?? 'Инвалидировать выбранное')}
        </Button>
        <span style={{ fontSize: '0.72rem', color: 'var(--tm-text-3)', alignSelf: 'center' }}>
          {t('explorer.settle_hint') ?? 'Подтверждение возвращает знание в подсказки и сбрасывает часы старения; инвалидация убирает навсегда.'}
        </span>
      </div>

      <div style={{ overflowY: 'auto', flex: 1, minHeight: 0, border: '1px solid var(--tm-border)', borderRadius: '6px' }}>
        {loading && <div style={{ padding: '0.6rem', color: 'var(--tm-text-3)', fontSize: '0.78rem' }}>…</div>}
        {!loading && rows.length === 0 && (
          <div style={{ padding: '0.8rem', color: 'var(--tm-text-3)', fontSize: '0.78rem' }}>
            {t('explorer.settle_empty') ?? 'Оспоренных знаний по этому фильтру нет.'}
          </div>
        )}
        {rows.map((item) => {
          const challenge = challengeOf(item)
          const rule = challenge.rule || 'manual'
          return (
            <label key={item.id} style={{ display: 'flex', gap: '0.5rem', alignItems: 'baseline', padding: '0.4rem 0.6rem', borderTop: '1px solid var(--tm-border)', fontSize: '0.78rem', cursor: 'pointer', flexWrap: 'wrap' }}>
              <input
                type="checkbox"
                checked={selected.has(item.id)}
                onChange={() => toggle(item.id)}
                onClick={(e) => e.stopPropagation()}
              />
              <Tag size="sm" type="warm-gray">{RULE_LABELS[rule] ?? shortID(rule || '—')}</Tag>
              <span style={{ flex: 1, minWidth: '14rem' }}>{item.proposition}</span>
              {challenge.reason && <span style={{ color: 'var(--tm-text-3)', fontSize: '0.7rem', maxWidth: '24rem' }} title={challenge.reason}>{challenge.reason}</span>}
              <TimeHint value={item.updated_at} style={{ color: 'var(--tm-text-3)', fontSize: '0.68rem', whiteSpace: 'nowrap' }}>
                {briefDateTime(item.updated_at)}
              </TimeHint>
              <span className="mono" style={{ fontSize: '0.66rem', color: 'var(--tm-text-3)' }} title={item.id}>{shortID(item.id)}</span>
            </label>
          )
        })}
      </div>
    </div>
  )
}
