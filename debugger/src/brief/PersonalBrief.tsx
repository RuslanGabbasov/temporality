import { useCallback, useEffect, useMemo, useState } from 'react'
import {
  dashboardBrief, dashboardTodos, createDashboardTodo, updateDashboardTodo, deleteDashboardTodo,
  TODO_GROUPS, normalizeTodoGroup,
  type TodoGroup, type UserBrief, type UserBriefRun, type UserTodo,
} from '../kernelApi'
import { useI18n } from '../i18n'
import {
  clusterTodos, dueState, formatCost, formatDue, formatStamp, formatTokens,
  GROUP_LABEL_KEYS, runLabel, todayKey, todayTodos,
} from './gtd'
import {
  Add, ArrowRight, Calendar, Checkmark, CheckmarkOutline, ChevronDown, ChevronRight, Close, Edit,
  Education, InProgress, Notebook, Renew, Target, Warning,
} from '@carbon/icons-react'

// «Мой день» — the personal landing tab (docs/system-dashboard.md): the
// supervisor's brief plus the live fact sections (approvals with direct
// artifact links, runs, learning, month tiles) and the TickTick-lite todo
// board (checkboxes, due dates, «Сегодня»). All lists render from the
// kernel-collected facts; only the narrative bullets are written by the model.

function navigate(path: string) {
  window.history.pushState(null, '', path)
  window.dispatchEvent(new PopStateEvent('popstate'))
}

const cardStyle: React.CSSProperties = {
  border: '1px solid var(--tm-border)',
  borderRadius: '8px',
  padding: '0.9rem 1rem',
  marginBottom: '0.8rem',
  background: 'var(--tm-surface)',
}
const sectionTitleStyle: React.CSSProperties = {
  fontSize: '0.82rem',
  fontWeight: 600,
  textTransform: 'uppercase',
  letterSpacing: '0.04em',
  color: 'var(--tm-text-3)',
  marginBottom: '0.45rem',
}
const sectionHeadStyle: React.CSSProperties = {
  ...sectionTitleStyle,
  display: 'flex',
  alignItems: 'center',
  gap: '0.4rem',
}
const sectionIconStyle: React.CSSProperties = { color: 'var(--tm-teal, #4fbdba)', flexShrink: 0 }
const rowStyle: React.CSSProperties = {
  display: 'flex',
  justifyContent: 'space-between',
  alignItems: 'baseline',
  gap: '0.5rem',
  padding: '0.28rem 0',
  fontSize: '0.84rem',
}
const linkStyle: React.CSSProperties = {
  color: 'var(--tm-teal, #4fbdba)',
  cursor: 'pointer',
  background: 'none',
  border: 'none',
  padding: 0,
  fontSize: '0.78rem',
}
const badgeStyle = (danger: boolean): React.CSSProperties => ({
  background: danger ? 'var(--tm-danger)' : 'var(--tm-raised)',
  color: danger ? '#fff' : 'var(--tm-text-2)',
  borderRadius: 999,
  fontSize: '0.68rem',
  fontWeight: 600,
  padding: '0.05rem 0.5rem',
})
const inputStyle: React.CSSProperties = {
  fontSize: '0.8rem',
  padding: '0.3rem 0.5rem',
  background: 'var(--tm-raised)',
  border: '1px solid var(--tm-border)',
  borderRadius: 6,
  color: 'var(--tm-text)',
}
const dueChipStyle = (state: 'overdue' | 'today' | 'future'): React.CSSProperties => ({
  background: state === 'overdue' ? 'var(--tm-danger)' : state === 'today' ? 'var(--tm-teal, #4fbdba)' : 'var(--tm-raised)',
  color: state === 'future' ? 'var(--tm-text-2)' : '#fff',
  borderRadius: 999,
  fontSize: '0.64rem',
  fontWeight: 600,
  padding: '0.04rem 0.42rem',
  flexShrink: 0,
})
const tileStyle: React.CSSProperties = {
  flex: 1,
  minWidth: '5.5rem',
  border: '1px solid var(--tm-border)',
  borderRadius: 8,
  padding: '0.5rem 0.6rem',
  background: 'var(--tm-raised)',
}
const checkboxStyle = (done: boolean): React.CSSProperties => ({
  width: 16,
  height: 16,
  flexShrink: 0,
  display: 'inline-flex',
  alignItems: 'center',
  justifyContent: 'center',
  border: done ? 'none' : '1px solid var(--tm-border)',
  borderRadius: 4,
  background: done ? 'var(--tm-teal, #4fbdba)' : 'transparent',
  color: '#fff',
  cursor: 'pointer',
  padding: 0,
})

function RunRow({ run, locale, showStatus }: { run: UserBriefRun; locale: string; showStatus?: boolean }) {
  const failed = run.status === 'failed'
  return (
    <div style={{ ...rowStyle, color: failed ? 'var(--tm-danger)' : undefined }}>
      <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
        <button
          type="button"
          style={{ ...linkStyle, color: failed ? 'var(--tm-danger)' : 'var(--tm-text)' }}
          onClick={() => navigate(`/agents?project=${encodeURIComponent(run.project ?? '')}&run=${encodeURIComponent(run.run_id)}`)}
          title={run.error ?? run.run_id}
        >
          {runLabel(run)}
        </button>
        {showStatus && <span style={{ marginLeft: '0.4rem', fontSize: '0.7rem', color: 'var(--tm-text-3)' }}>{run.status}</span>}
      </span>
      <span style={{ fontSize: '0.72rem', color: 'var(--tm-text-3)', flexShrink: 0 }}>{formatStamp(run.at, locale)}</span>
    </div>
  )
}

// Focus bullet: a teal dot + one line — the morning digest reads as a plan,
// not a log.
function FocusList({ lines }: { lines: string[] }) {
  return (
    <ul style={{ margin: 0, padding: 0, listStyle: 'none', fontSize: '0.88rem', lineHeight: 1.55 }}>
      {lines.map((line, i) => (
        <li key={i} style={{ display: 'flex', gap: '0.5rem', marginBottom: '0.3rem' }}>
          <span style={{ color: 'var(--tm-teal, #4fbdba)', flexShrink: 0, lineHeight: 'inherit' }}>•</span>
          <span>{line}</span>
        </li>
      ))}
    </ul>
  )
}

export default function PersonalBrief() {
  const { t, locale } = useI18n()
  const [brief, setBrief] = useState<UserBrief | null>(null)
  const [todos, setTodos] = useState<UserTodo[]>([])
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)
  const [refreshing, setRefreshing] = useState(false)
  // ---- TickTick-lite board state ----
  const [newText, setNewText] = useState('')
  const [newDue, setNewDue] = useState('')
  const [todoBusy, setTodoBusy] = useState(false)
  const [openGroups, setOpenGroups] = useState<Record<string, boolean>>({ inbox: true, next: true, waiting: true, someday: true, done: false })
  const [expandedId, setExpandedId] = useState('')
  const [draft, setDraft] = useState<{ text: string; note: string; due: string; group: TodoGroup }>({ text: '', note: '', due: '', group: 'inbox' })

  const clusters = useMemo(() => clusterTodos(todos), [todos])
  const today = useMemo(() => todayKey(), [])
  const dueToday = useMemo(() => todayTodos(todos, today), [todos, today])
  const openCount = useMemo(() => todos.filter((td) => td.group !== 'done').length, [todos])

  const loadBrief = useCallback(async (refresh?: boolean) => {
    if (refresh) setRefreshing(true)
    else setLoading(true)
    try {
      const page = await dashboardBrief(refresh)
      setBrief(page.brief)
      setError('')
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setLoading(false)
      setRefreshing(false)
    }
  }, [])

  useEffect(() => { void loadBrief() }, [loadBrief])

  useEffect(() => {
    void dashboardTodos().then((page) => setTodos(page.todos ?? [])).catch(() => {})
  }, [])

  const addTodo = useCallback(async () => {
    const text = newText.trim()
    if (!text || todoBusy) return
    setTodoBusy(true)
    try {
      const page = await createDashboardTodo({ group: 'inbox', text, due: newDue || undefined })
      setTodos((prev) => [...prev, page.todo])
      setNewText('')
      setNewDue('')
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally { setTodoBusy(false) }
  }, [newText, newDue, todoBusy])

  // Checkbox semantics (TickTick-lite): completing moves the todo to done and
  // the kernel remembers its origin group; unchecking restores it there.
  const toggleDone = useCallback(async (td: UserTodo) => {
    const target: TodoGroup = td.group === 'done' ? normalizeTodoGroup(td.return_group ?? 'inbox') : 'done'
    try {
      const page = await updateDashboardTodo(td.id, { group: target })
      setTodos((prev) => prev.map((x) => (x.id === td.id ? page.todo : x)))
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }, [])

  const moveTodo = useCallback(async (id: string, group: string) => {
    const target = normalizeTodoGroup(group)
    try {
      const page = await updateDashboardTodo(id, { group: target })
      setTodos((prev) => prev.map((td) => (td.id === id ? page.todo : td)))
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }, [])

  const removeTodo = useCallback(async (id: string) => {
    try {
      await deleteDashboardTodo(id)
      setTodos((prev) => prev.filter((td) => td.id !== id))
      if (expandedId === id) setExpandedId('')
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }, [expandedId])

  const startEdit = useCallback((td: UserTodo) => {
    setExpandedId(td.id)
    setDraft({ text: td.text, note: td.note ?? '', due: td.due?.slice(0, 10) ?? '', group: normalizeTodoGroup(td.group) })
  }, [])

  const saveEdit = useCallback(async (id: string) => {
    setTodoBusy(true)
    try {
      const page = await updateDashboardTodo(id, { text: draft.text, note: draft.note, due: draft.due, group: draft.group })
      setTodos((prev) => prev.map((td) => (td.id === id ? page.todo : td)))
      setExpandedId('')
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally { setTodoBusy(false) }
  }, [draft])

  const dayTitle = useMemo(() => {
    if (!brief?.day) return ''
    const d = new Date(brief.day + 'T00:00:00Z')
    return Number.isNaN(d.getTime()) ? brief.day : new Intl.DateTimeFormat(locale, { weekday: 'long', day: 'numeric', month: 'long' }).format(d)
  }, [brief?.day, locale])

  const learning = brief?.learning
  const metrics = brief?.metrics
  const approvalCount = brief?.approvals?.length ?? 0
  const failedCount = brief?.failed?.length ?? 0

  // One board row: checkbox, text (click to edit), due chip. The inline
  // editor opens under the row when expanded — no modals on a landing page.
  const renderTodo = (td: UserTodo) => {
    const state = dueState(td, today)
    const done = td.group === 'done'
    const expanded = expandedId === td.id
    return (
      <div key={td.id}>
        <div style={{ display: 'flex', alignItems: 'center', gap: '0.45rem', padding: '0.22rem 0', fontSize: '0.8rem' }}>
          <button
            type="button"
            style={checkboxStyle(done)}
            title={done ? t('myday.todo_reopen') : t('myday.todo_complete')}
            onClick={() => void toggleDone(td)}
          >
            {done && <Checkmark size={12} />}
          </button>
          {done ? (
            <span style={{ flex: 1, minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', textDecoration: 'line-through', color: 'var(--tm-text-3)' }} title={td.note ?? td.text}>{td.text}</span>
          ) : (
            <button
              type="button"
              onClick={() => (expanded ? setExpandedId('') : startEdit(td))}
              title={td.note ?? td.text}
              style={{ flex: 1, minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', textAlign: 'left', background: 'none', border: 'none', padding: 0, color: 'var(--tm-text)', cursor: 'pointer', fontSize: '0.8rem' }}
            >
              {td.text}
            </button>
          )}
          {state && <span style={dueChipStyle(state)} title={t('myday.todo_due')}>{formatDue(td.due, locale)}</span>}
          {!done && (
            <button type="button" style={{ ...linkStyle, color: 'var(--tm-text-3)', display: 'inline-flex' }} title={t('myday.todo_edit')} onClick={() => (expanded ? setExpandedId('') : startEdit(td))}>
              <Edit size={13} />
            </button>
          )}
          <button type="button" style={{ ...linkStyle, color: 'var(--tm-text-3)', display: 'inline-flex' }} title={t('myday.todo_delete')} onClick={() => void removeTodo(td.id)}>
            <Close size={13} />
          </button>
        </div>
        {expanded && (
          <div style={{ border: '1px solid var(--tm-border)', borderRadius: 6, padding: '0.5rem', margin: '0.15rem 0 0.35rem', background: 'var(--tm-raised)' }}>
            <input
              value={draft.text}
              onChange={(e) => setDraft({ ...draft, text: e.target.value })}
              onKeyDown={(e) => { if (e.key === 'Enter') void saveEdit(td.id) }}
              style={{ ...inputStyle, width: '100%', boxSizing: 'border-box', marginBottom: '0.35rem' }}
            />
            <input
              value={draft.note}
              onChange={(e) => setDraft({ ...draft, note: e.target.value })}
              placeholder={t('myday.todo_note_ph')}
              style={{ ...inputStyle, width: '100%', boxSizing: 'border-box', marginBottom: '0.35rem' }}
            />
            <div style={{ display: 'flex', gap: '0.35rem', alignItems: 'center', flexWrap: 'wrap' }}>
              <label style={{ display: 'inline-flex', alignItems: 'center', gap: '0.3rem', fontSize: '0.72rem', color: 'var(--tm-text-3)' }}>
                <Calendar size={13} />
                <input
                  type="date"
                  value={draft.due}
                  onChange={(e) => setDraft({ ...draft, due: e.target.value })}
                  style={{ ...inputStyle, padding: '0.15rem 0.3rem', fontSize: '0.74rem' }}
                />
              </label>
              <select
                value={draft.group}
                onChange={(e) => setDraft({ ...draft, group: normalizeTodoGroup(e.target.value) })}
                style={{ ...inputStyle, padding: '0.15rem 0.3rem', fontSize: '0.74rem' }}
              >
                {TODO_GROUPS.map((g) => <option key={g} value={g}>{t(GROUP_LABEL_KEYS[g])}</option>)}
              </select>
              <span style={{ flex: 1 }} />
              <button type="button" style={{ ...linkStyle, display: 'inline-flex', alignItems: 'center', gap: '0.25rem' }} disabled={todoBusy || !draft.text.trim()} onClick={() => void saveEdit(td.id)}>
                <CheckmarkOutline size={13} /> {t('myday.todo_save')}
              </button>
              <button type="button" style={{ ...linkStyle, color: 'var(--tm-text-3)' }} onClick={() => setExpandedId('')}>{t('myday.todo_cancel')}</button>
            </div>
          </div>
        )}
      </div>
    )
  }

  const groupSection = (g: TodoGroup) => {
    const list = clusters[g]
    const open = openGroups[g] ?? false
    return (
      <div key={g} style={{ marginBottom: '0.4rem' }}>
        <button
          type="button"
          onClick={() => setOpenGroups((prev) => ({ ...prev, [g]: !open }))}
          style={{ background: 'none', border: 'none', padding: '0.1rem 0', cursor: 'pointer', display: 'flex', alignItems: 'center', gap: '0.3rem', color: 'var(--tm-text-3)', fontSize: '0.7rem', textTransform: 'uppercase', letterSpacing: '0.04em', width: '100%' }}
        >
          {open ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
          {t(GROUP_LABEL_KEYS[g])}
          {list.length > 0 && <span style={{ color: 'var(--tm-text-2)' }}>{list.length}</span>}
        </button>
        {open && list.map(renderTodo)}
        {open && list.length === 0 && g === 'inbox' && (
          <div style={{ color: 'var(--tm-text-3)', fontSize: '0.76rem' }}>{t('myday.todo_empty')}</div>
        )}
      </div>
    )
  }

  return (
    <div>
      <div style={{ display: 'flex', alignItems: 'baseline', gap: '0.6rem', marginBottom: '0.9rem', flexWrap: 'wrap' }}>
        {dayTitle && <span style={{ color: 'var(--tm-text-2)', fontSize: '0.95rem', textTransform: 'capitalize' }}>{dayTitle}</span>}
        {brief && (
          <span style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)' }}>
            {brief.source === 'model' ? t('myday.source_model') : t('myday.source_fallback')}
          </span>
        )}
        <span style={{ flex: 1 }} />
        <button type="button" style={{ ...linkStyle, display: 'inline-flex', alignItems: 'center', gap: '0.25rem' }} disabled={refreshing} onClick={() => void loadBrief(true)}>
          <Renew size={13} /> {refreshing ? t('common.loading') : t('myday.refresh')}
        </button>
      </div>

      {error && (
        <div style={{ ...cardStyle, borderColor: 'var(--tm-danger)', color: 'var(--tm-danger)', fontSize: '0.82rem' }}>
          {t('myday.error')}: {error}
        </div>
      )}

      <div style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 2fr) minmax(300px, 1fr)', gap: '1rem', alignItems: 'start' }}>
        {/* Left column: the focus and everything it points at. */}
        <div style={{ minWidth: 0 }}>
          <section style={cardStyle}>
            <div style={sectionHeadStyle}>
              <Target size={15} style={sectionIconStyle} />
              {t('myday.focus')}
            </div>
            {loading && <div style={{ color: 'var(--tm-text-3)', fontSize: '0.84rem' }}>{t('common.loading')}</div>}
            {!loading && brief && (brief.summary?.length ?? 0) > 0 && <FocusList lines={brief.summary} />}
            {!loading && brief && (brief.summary?.length ?? 0) === 0 && (
              <div style={{ color: 'var(--tm-text-3)', fontSize: '0.84rem' }}>{t('myday.summary_empty')}</div>
            )}
          </section>

          <section style={cardStyle}>
            <div style={sectionHeadStyle}>
              <CheckmarkOutline size={15} style={sectionIconStyle} />
              {t('myday.approvals')}
              {approvalCount > 0 && <span style={badgeStyle(false)}>{approvalCount}</span>}
            </div>
            {approvalCount === 0 && <div style={{ color: 'var(--tm-text-3)', fontSize: '0.82rem' }}>{t('myday.approvals_empty')}</div>}
            {(brief?.approvals ?? []).slice(0, 6).map((a) => (
              <div key={a.review_id} style={{ ...rowStyle, alignItems: 'center' }}>
                <span style={{ overflow: 'hidden', minWidth: 0 }}>
                  <button
                    type="button"
                    style={{ ...linkStyle, color: 'var(--tm-text)', fontSize: '0.84rem' }}
                    onClick={() => navigate(`/artifacts?id=${encodeURIComponent(a.artifact_id)}`)}
                    title={t('myday.approvals_open_artifact')}
                  >
                    {a.artifact_title}
                  </button>
                  {(a.origin_user || a.mode) && (
                    <span style={{ marginLeft: '0.45rem', fontSize: '0.7rem', color: 'var(--tm-text-3)' }}>
                      {[a.origin_user, a.mode].filter(Boolean).join(' · ')}
                    </span>
                  )}
                </span>
                <span style={{ fontSize: '0.72rem', color: 'var(--tm-text-3)', flexShrink: 0 }}>{formatStamp(a.requested_at, locale)}</span>
              </div>
            ))}
            {approvalCount > 6 && <div style={{ fontSize: '0.74rem', color: 'var(--tm-text-3)' }}>+{approvalCount - 6}</div>}
            <button type="button" style={{ ...linkStyle, marginTop: '0.35rem', display: 'inline-flex', alignItems: 'center', gap: '0.25rem' }} onClick={() => navigate('/artifacts')}>
              {t('myday.approvals_open')} <ArrowRight size={13} />
            </button>
          </section>

          <section style={cardStyle}>
            <div style={sectionHeadStyle}>
              <Renew size={15} style={sectionIconStyle} />
              {t('myday.runs')}
            </div>
            <div style={{ fontSize: '0.8rem', fontWeight: 600, color: 'var(--tm-text-2)', margin: '0.3rem 0 0.15rem', display: 'flex', alignItems: 'center', gap: '0.35rem' }}>
              <InProgress size={14} />
              {t('myday.runs_running')} {(brief?.running?.length ?? 0) > 0 && <span style={badgeStyle(false)}>{brief!.running.length}</span>}
            </div>
            {(brief?.running?.length ?? 0) === 0 && <div style={{ color: 'var(--tm-text-3)', fontSize: '0.8rem' }}>{t('myday.runs_empty')}</div>}
            {(brief?.running ?? []).map((r) => <RunRow key={r.run_id} run={r} locale={locale} />)}

            {failedCount > 0 && (
              <>
                <div style={{ fontSize: '0.8rem', fontWeight: 600, color: 'var(--tm-danger)', margin: '0.5rem 0 0.15rem', display: 'flex', alignItems: 'center', gap: '0.35rem' }}>
                  <Warning size={14} />
                  {t('myday.runs_failed')} <span style={badgeStyle(true)}>{failedCount}</span>
                </div>
                {brief!.failed.map((r) => <RunRow key={r.run_id} run={r} locale={locale} />)}
              </>
            )}

            <div style={{ fontSize: '0.8rem', fontWeight: 600, color: 'var(--tm-text-2)', margin: '0.5rem 0 0.15rem' }}>{t('myday.runs_recent')}</div>
            {(brief?.recent?.length ?? 0) === 0 && <div style={{ color: 'var(--tm-text-3)', fontSize: '0.8rem' }}>{t('myday.runs_empty')}</div>}
            {(brief?.recent ?? []).map((r) => <RunRow key={r.run_id} run={r} locale={locale} showStatus />)}
          </section>
        </div>

        {/* Right column: the personal-efficiency board, learning, spend. */}
        <div style={{ minWidth: 0 }}>
          <section style={cardStyle}>
            <div style={sectionHeadStyle}>
              <Notebook size={15} style={sectionIconStyle} />
              {t('myday.todos')}
              {openCount > 0 && <span style={badgeStyle(false)}>{openCount}</span>}
            </div>
            {/* Quick add: capture now, date optional — the TickTick reflex. */}
            <div style={{ display: 'flex', gap: '0.35rem', marginBottom: '0.6rem' }}>
              <input
                value={newText}
                onChange={(e) => setNewText(e.target.value)}
                onKeyDown={(e) => { if (e.key === 'Enter') void addTodo() }}
                placeholder={t('myday.todo_placeholder')}
                style={{ ...inputStyle, flex: 1, minWidth: 0 }}
              />
              <input
                type="date"
                value={newDue}
                onChange={(e) => setNewDue(e.target.value)}
                title={t('myday.todo_due')}
                style={{ ...inputStyle, padding: '0.15rem 0.3rem', fontSize: '0.74rem', flexShrink: 0 }}
              />
              <button type="button" onClick={() => void addTodo()} disabled={todoBusy || !newText.trim()} style={{ ...linkStyle, display: 'inline-flex', alignItems: 'center' }} title={t('myday.todo_add')}>
                <Add size={15} />
              </button>
            </div>
            {/* «Сегодня»: everything due today or earlier, across groups. */}
            {dueToday.length > 0 && (
              <div style={{ marginBottom: '0.55rem', border: '1px solid var(--tm-border)', borderRadius: 6, padding: '0.45rem 0.6rem', background: 'var(--tm-raised)' }}>
                <div style={{ fontSize: '0.7rem', textTransform: 'uppercase', letterSpacing: '0.04em', color: 'var(--tm-text-2)', marginBottom: '0.2rem', display: 'flex', alignItems: 'center', gap: '0.3rem' }}>
                  <Calendar size={13} style={{ color: 'var(--tm-teal, #4fbdba)' }} />
                  {t('myday.today')}
                  <span style={{ color: 'var(--tm-text-2)' }}>{dueToday.length}</span>
                </div>
                {dueToday.map(renderTodo)}
              </div>
            )}
            {(['inbox', 'next', 'waiting', 'someday', 'done'] as TodoGroup[]).map(groupSection)}
          </section>

          <section style={cardStyle}>
            <div style={sectionHeadStyle}>
              <Education size={15} style={sectionIconStyle} />
              {t('myday.learning')}
            </div>
            {learning?.active ? (
              <>
                <div style={{ fontSize: '0.84rem' }}>
                  {t('myday.learning_progress', { done: String(learning.done), total: String(learning.total) })}
                </div>
                <div style={{ ...meterTrackStyle, margin: '0.4rem 0' }}>
                  <div style={{ ...meterFillStyle, width: `${learning.total > 0 ? Math.round((100 * learning.done) / learning.total) : 0}%` }} />
                </div>
                {learning.next_id && <div style={{ fontSize: '0.78rem', color: 'var(--tm-text-3)' }}>{t('myday.learning_next')}: {learning.next_id}</div>}
              </>
            ) : (
              <div style={{ color: 'var(--tm-text-3)', fontSize: '0.82rem' }}>{t('myday.learning_empty')}</div>
            )}
            <button type="button" style={{ ...linkStyle, marginTop: '0.35rem', display: 'inline-flex', alignItems: 'center', gap: '0.25rem' }} onClick={() => navigate('/learning')}>
              {t('myday.learning_open')} <ArrowRight size={13} />
            </button>
          </section>

          <section style={cardStyle}>
            <div style={sectionHeadStyle}>
              <Calendar size={15} style={sectionIconStyle} />
              {t('myday.metrics', { month: metrics?.month ?? '' })}
            </div>
            {metrics ? (
              <div style={{ display: 'flex', gap: '0.5rem', flexWrap: 'wrap' }}>
                <div style={tileStyle}>
                  <div style={{ fontSize: '1.05rem', fontWeight: 600 }}>{metrics.runs}</div>
                  <div style={{ fontSize: '0.68rem', color: 'var(--tm-text-3)' }}>{t('myday.tile_runs')}</div>
                </div>
                <div style={tileStyle}>
                  <div style={{ fontSize: '1.05rem', fontWeight: 600, color: metrics.failed_runs > 0 ? 'var(--tm-danger)' : undefined }}>
                    {metrics.runs > 0 ? `${Math.max(0, Math.round((100 * (metrics.runs - metrics.failed_runs)) / metrics.runs))}%` : '—'}
                  </div>
                  <div style={{ fontSize: '0.68rem', color: 'var(--tm-text-3)' }}>{t('myday.tile_success')}</div>
                </div>
                <div style={tileStyle}>
                  <div style={{ fontSize: '1.05rem', fontWeight: 600 }}>{formatTokens(metrics.tokens)}</div>
                  <div style={{ fontSize: '0.68rem', color: 'var(--tm-text-3)' }}>{t('myday.tile_tokens')}</div>
                </div>
                <div style={tileStyle}>
                  <div style={{ fontSize: '1.05rem', fontWeight: 600 }}>{formatCost(metrics.cost_usd)}</div>
                  <div style={{ fontSize: '0.68rem', color: 'var(--tm-text-3)' }}>{t('myday.tile_cost')}</div>
                </div>
              </div>
            ) : (
              <div style={{ color: 'var(--tm-text-3)', fontSize: '0.82rem' }}>{t('common.loading')}</div>
            )}
          </section>
        </div>
      </div>
    </div>
  )
}

const meterTrackStyle: React.CSSProperties = { height: '6px', borderRadius: 999, background: 'var(--tm-raised)', overflow: 'hidden' }
const meterFillStyle: React.CSSProperties = { height: '100%', borderRadius: 999, background: 'var(--tm-teal, #4fbdba)' }
