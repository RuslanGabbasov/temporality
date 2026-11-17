import { useCallback, useEffect, useMemo, useState } from 'react'
import { Button, InlineNotification, Tag, TextInput } from '@carbon/react'
import { observationApi, type KnowledgeItem } from '../observationApi'
import { whoami } from '../kernelApi'
import { useT } from '../i18n'
import { briefDateTime, TimeHint } from '../timeHint'

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

// Localized rule label: every known challenge rule has an
// explorer.settle_rule_<id> key (dots → underscores); unknown rules fall
// back to the raw id — a filter option never renders empty.
function ruleLabel(rule: string, t: (key: string) => string): string {
  const key = `explorer.settle_rule_${rule.replace(/\./g, '_')}`
  const label = t(key)
  return label === key ? rule : label
}

// Rows rendered at once; «показать ещё» extends by the same step. Thousands
// of rows cannot mount in one go — the DOM would choke long before the bulk
// verdict did.
const RENDER_STEP = 250

// The settle queue for a backlog of challenged knowledge (a live installation
// accumulated 2289 items — more than any human clicks through one by one).
// Composition: a sticky toolbar (rule filter, selection controls) above, the
// paged list in the middle, and a sticky action bar (reason + bulk verdicts)
// below — the verdict buttons stay reachable at any scroll depth. «Выбрать
// всё» selects every FILTERED item, not just the rendered ones: the bulk call
// pages ids to the server anyway.
export default function SettlePanel({ project, onRevision }: { project: string; onRevision: () => void }) {
  const t = useT()
  const [items, setItems] = useState<KnowledgeItem[]>([])
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [note, setNote] = useState('')
  const [ruleFilter, setRuleFilter] = useState('')
  const [busy, setBusy] = useState(false)
  const [busyProgress, setBusyProgress] = useState('')
  const [done, setDone] = useState('')
  const [actorID, setActorID] = useState('human-requester')
  const [visibleCount, setVisibleCount] = useState(RENDER_STEP)

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
      setVisibleCount(RENDER_STEP)
    } catch (failure) { setError(errorMessage(failure)) }
    finally { setLoading(false) }
  }, [project])

  useEffect(() => { if (project) void load() }, [project, load])

  const rows = useMemo(() => {
    if (!ruleFilter) return items
    return items.filter((item) => challengeOf(item).rule === ruleFilter)
  }, [items, ruleFilter])

  const visibleRows = useMemo(() => rows.slice(0, visibleCount), [rows, visibleCount])

  const rules = useMemo(() => {
    const counts = new Map<string, number>()
    for (const item of items) {
      const rule = challengeOf(item).rule || 'manual'
      counts.set(rule, (counts.get(rule) ?? 0) + 1)
    }
    return [...counts.entries()].sort((a, b) => b[1] - a[1])
  }, [items])

  const allFilteredSelected = rows.length > 0 && rows.every((row) => selected.has(row.id))

  const toggle = (id: string) => {
    setSelected((current) => {
      const next = new Set(current)
      if (next.has(id)) next.delete(id); else next.add(id)
      return next
    })
  }

  const toggleAll = () => {
    if (allFilteredSelected) {
      setSelected(new Set())
    } else {
      setSelected(new Set(rows.map((row) => row.id)))
    }
  }

  const apply = async (action: 'confirm' | 'invalidate') => {
    if (selected.size === 0) return
    setBusy(true); setError(''); setDone('')
    try {
      let recorded = 0
      const ids = [...selected]
      // The server caps one call at 500 ids; page the batch and show progress.
      for (let i = 0; i < ids.length; i += 500) {
        const chunk = ids.slice(i, i + 500)
        setBusyProgress(`${Math.min(i + chunk.length, ids.length)}/${ids.length}`)
        const reply = await observationApi.bulkKnowledge({
          project, actor: { id: actorID, type: 'human' }, action,
          reason: note.trim() || undefined, ids: chunk,
        })
        recorded += reply.recorded ?? 0
      }
      setDone(`${recorded}`)
      setSelected(new Set())
      setNote('')
      onRevision()
      await load()
    } catch (failure) { setError(errorMessage(failure)) }
    finally { setBusyProgress(''); setBusy(false) }
  }

  return (
    <div style={{ display: 'flex', flexDirection: 'column', minHeight: 0, flex: 1, gap: '0.6rem' }}>
      {/* Sticky toolbar: what's in scope and what's selected. */}
      <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', flexWrap: 'wrap', flexShrink: 0 }}>
        <strong>{t('explorer.settle_title') ?? 'Разбор оспоренных'}</strong>
        <Tag size="sm" type="warm-gray">{rows.length}{ruleFilter ? ` / ${items.length}` : ''}</Tag>
        <select
          value={ruleFilter}
          onChange={(e) => { setRuleFilter(e.target.value); setVisibleCount(RENDER_STEP) }}
          className="toolbar-btn"
          style={{ maxWidth: '24rem' }}
          title={t('explorer.settle_rule_filter') ?? 'Фильтр по правилу, которым знание оспорено'}
        >
          <option value="">{t('explorer.settle_rule_all') ?? 'все правила'}</option>
          {rules.map(([rule, count]) => (
            <option key={rule} value={rule}>{ruleLabel(rule, t)} · {count}</option>
          ))}
        </select>
        <button className="toolbar-btn" onClick={() => void load()} disabled={loading} title={t('common.refresh')}>↻</button>
        <span style={{ marginLeft: 'auto', display: 'flex', gap: '0.4rem', alignItems: 'center' }}>
          <span style={{ fontSize: '0.72rem', color: 'var(--tm-text-3)' }}>
            {t('explorer.settle_selected', { count: String(selected.size) }) ?? `выбрано: ${selected.size}`}
          </span>
          <button className="toolbar-btn" onClick={toggleAll} disabled={rows.length === 0}>
            {allFilteredSelected
              ? (t('explorer.settle_deselect_all') ?? 'снять всё')
              : (t('explorer.settle_select_all') ?? 'Выбрать всё')}
          </button>
        </span>
      </div>

      {error && <InlineNotification kind="error" title={t('common.error')} subtitle={error} onClose={() => setError('')} lowContrast />}
      {done && (
        <InlineNotification kind="success" title={t('explorer.settle_done') ?? 'Применено'} subtitle={t('explorer.settle_done_n', { count: done }) ?? `вердиктов записано: ${done}`} onClose={() => setDone('')} lowContrast />
      )}

      {/* The list: sticky column header, rows mounted in chunks. */}
      <div style={{ flex: 1, minHeight: 0, overflowY: 'auto', border: '1px solid var(--tm-border)', borderRadius: '6px' }}>
        <div style={{ position: 'sticky', top: 0, zIndex: 1, display: 'flex', gap: '0.5rem', padding: '0.35rem 0.6rem', background: 'var(--tm-bg)', borderBottom: '1px solid var(--tm-border)', fontSize: '0.68rem', color: 'var(--tm-text-3)', textTransform: 'uppercase', letterSpacing: '0.04em' }}>
          <span style={{ width: '9rem', flexShrink: 0 }}>{t('explorer.settle_col_rule') ?? 'правило'}</span>
          <span style={{ flex: 1 }}>{t('explorer.settle_col_knowledge') ?? 'знание'}</span>
          <span style={{ width: '16rem', flexShrink: 0 }}>{t('explorer.settle_col_reason') ?? 'причина оспаривания'}</span>
          <span style={{ width: '7.5rem', flexShrink: 0 }}>{t('explorer.settle_col_when') ?? 'когда'}</span>
        </div>
        {loading && <div style={{ padding: '0.8rem', color: 'var(--tm-text-3)', fontSize: '0.78rem' }}>…</div>}
        {!loading && rows.length === 0 && (
          <div style={{ padding: '0.9rem', color: 'var(--tm-text-3)', fontSize: '0.78rem' }}>
            {t('explorer.settle_empty') ?? 'Оспоренных знаний по этому фильтру нет.'}
          </div>
        )}
        {visibleRows.map((item) => {
          const challenge = challengeOf(item)
          const rule = challenge.rule || 'manual'
          return (
            <label key={item.id} style={{ display: 'flex', gap: '0.5rem', alignItems: 'baseline', padding: '0.4rem 0.6rem', borderTop: '1px solid var(--tm-border)', fontSize: '0.78rem', cursor: 'pointer', flexWrap: 'nowrap' }}>
              <input
                type="checkbox"
                checked={selected.has(item.id)}
                onChange={() => toggle(item.id)}
                onClick={(e) => e.stopPropagation()}
              />
              <Tag size="sm" type="warm-gray" style={{ width: '9rem', flexShrink: 0, overflow: 'hidden', textOverflow: 'ellipsis' }} title={ruleLabel(rule, t)}>
                {ruleLabel(rule, t)}
              </Tag>
              <span style={{ flex: 1, minWidth: 0, overflowWrap: 'anywhere' }} title={item.id}>{item.proposition}</span>
              <span style={{ width: '16rem', flexShrink: 0, color: 'var(--tm-text-3)', fontSize: '0.7rem', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={challenge.reason}>
                {challenge.reason || '—'}
              </span>
              <TimeHint value={item.updated_at} style={{ width: '7.5rem', flexShrink: 0, color: 'var(--tm-text-3)', fontSize: '0.68rem', whiteSpace: 'nowrap' }}>
                {briefDateTime(item.updated_at)}
              </TimeHint>
            </label>
          )
        })}
        {rows.length > visibleRows.length && (
          <div style={{ display: 'flex', justifyContent: 'center', padding: '0.5rem' }}>
            <Button size="sm" kind="ghost" onClick={() => setVisibleCount((count) => count + RENDER_STEP)}>
              {t('explorer.settle_more', { count: String(Math.min(RENDER_STEP, rows.length - visibleRows.length)) }) ?? `Показать ещё ${Math.min(RENDER_STEP, rows.length - visibleRows.length)}`}
            </Button>
          </div>
        )}
      </div>

      {/* Sticky action bar: the verdicts stay reachable at any scroll depth. */}
      <div style={{ flexShrink: 0, display: 'flex', gap: '0.6rem', alignItems: 'center', flexWrap: 'wrap', borderTop: '1px solid var(--tm-border)', paddingTop: '0.6rem' }}>
        <TextInput
          id="settle-note"
          size="sm"
          labelText=""
          hideLabel
          placeholder={t('explorer.settle_reason_placeholder') ?? 'Причина вердикта (необязательно): проверено на стенде / неактуально после миграции…'}
          value={note}
          onChange={(e: React.ChangeEvent<HTMLInputElement>) => setNote(e.target.value)}
          style={{ flex: 1, minWidth: '16rem' }}
          disabled={busy}
        />
        <Button size="sm" kind="primary" disabled={busy || selected.size === 0} onClick={() => void apply('confirm')}>
          {busy ? `${t('explorer.settle_working') ?? 'Работаю…'} ${busyProgress}` : (t('explorer.settle_confirm') ?? 'Подтвердить выбранное')}
        </Button>
        <Button size="sm" kind="danger--primary" disabled={busy || selected.size === 0} onClick={() => void apply('invalidate')}>
          {busy ? '' : (t('explorer.settle_invalidate') ?? 'Инвалидировать выбранное')}
        </Button>
        <span style={{ fontSize: '0.72rem', color: 'var(--tm-text-3)' }}>
          {t('explorer.settle_hint') ?? 'Подтверждение возвращает знание в подсказки и сбрасывает часы старения; инвалидация убирает навсегда.'}
        </span>
      </div>
    </div>
  )
}
