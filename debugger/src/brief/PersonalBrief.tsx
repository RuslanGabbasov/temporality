import { useCallback, useEffect, useMemo, useState } from 'react'
import {
  dashboardBrief, dashboardTodos, createDashboardTodo, updateDashboardTodo, deleteDashboardTodo,
  TODO_GROUPS, normalizeTodoGroup,
  type TodoGroup, type UserBrief, type UserBriefRun, type UserTodo,
} from '../kernelApi'
import { useI18n } from '../i18n'
import { clusterTodos, formatCost, formatStamp, formatTokens, GROUP_LABEL_KEYS, runLabel, todoTotal } from './gtd'

// «Мой день» — the personal landing page (docs/system-dashboard.md): the
// supervisor's daily brief plus the live fact sections (runs, approvals,
// learning, month metrics) and the small GTD todo widget. All lists render
// from the kernel-collected facts; only the narrative bullets are written
// by the model.

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

export default function PersonalBrief() {
  const { t, locale } = useI18n()
  const [brief, setBrief] = useState<UserBrief | null>(null)
  const [todos, setTodos] = useState<UserTodo[]>([])
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)
  const [refreshing, setRefreshing] = useState(false)
  const [newText, setNewText] = useState('')
  const [newGroup, setNewGroup] = useState<TodoGroup>('inbox')
  const [todoBusy, setTodoBusy] = useState(false)

  const clusters = useMemo(() => clusterTodos(todos), [todos])

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
      const page = await createDashboardTodo({ group: newGroup, text })
      setTodos((prev) => [...prev, page.todo])
      setNewText('')
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally { setTodoBusy(false) }
  }, [newText, newGroup, todoBusy])

  const moveTodo = useCallback(async (id: string, group: string) => {
    const target = normalizeTodoGroup(group)
    try {
      const page = await updateDashboardTodo(id, { group: target })
      setTodos((prev) => prev.map((todo) => (todo.id === id ? page.todo : todo)))
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }, [])

  const removeTodo = useCallback(async (id: string) => {
    try {
      await deleteDashboardTodo(id)
      setTodos((prev) => prev.filter((todo) => todo.id !== id))
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }, [])

  const dayTitle = useMemo(() => {
    if (!brief?.day) return ''
    const d = new Date(brief.day + 'T00:00:00Z')
    return Number.isNaN(d.getTime()) ? brief.day : new Intl.DateTimeFormat(locale, { weekday: 'long', day: 'numeric', month: 'long' }).format(d)
  }, [brief?.day, locale])

  const learning = brief?.learning
  const metrics = brief?.metrics
  const approvalCount = brief?.approvals?.length ?? 0
  const failedCount = brief?.failed?.length ?? 0

  return (
    <div style={{ maxWidth: '1100px', margin: '0 auto', padding: '1.2rem 1rem' }}>
      <div style={{ display: 'flex', alignItems: 'baseline', gap: '0.6rem', marginBottom: '0.9rem', flexWrap: 'wrap' }}>
        <h2 style={{ margin: 0, fontSize: '1.25rem' }}>{t('myday.title')}</h2>
        {dayTitle && <span style={{ color: 'var(--tm-text-3)', fontSize: '0.85rem' }}>{dayTitle}</span>}
        {brief && (
          <span style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)' }}>
            {brief.source === 'model' ? t('myday.source_model') : t('myday.source_fallback')}
          </span>
        )}
        <span style={{ flex: 1 }} />
        <button type="button" style={linkStyle} disabled={refreshing} onClick={() => void loadBrief(true)}>
          {refreshing ? t('common.loading') : t('myday.refresh')}
        </button>
      </div>

      {error && (
        <div style={{ ...cardStyle, borderColor: 'var(--tm-danger)', color: 'var(--tm-danger)', fontSize: '0.82rem' }}>
          {t('myday.error')}: {error}
        </div>
      )}

      <div style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 2fr) minmax(280px, 1fr)', gap: '1rem', alignItems: 'start' }}>
        {/* Left column: the brief and its fact sections. */}
        <div style={{ minWidth: 0 }}>
          <section style={cardStyle}>
            <div style={sectionTitleStyle}>{t('myday.focus')}</div>
            {loading && <div style={{ color: 'var(--tm-text-3)', fontSize: '0.84rem' }}>{t('common.loading')}</div>}
            {!loading && brief && (brief.summary?.length ?? 0) > 0 && (
              <ul style={{ margin: 0, paddingLeft: '1.1rem', fontSize: '0.88rem', lineHeight: 1.55 }}>
                {brief.summary.map((line, i) => <li key={i} style={{ marginBottom: '0.25rem' }}>{line}</li>)}
              </ul>
            )}
            {!loading && brief && (brief.summary?.length ?? 0) === 0 && (
              <div style={{ color: 'var(--tm-text-3)', fontSize: '0.84rem' }}>{t('myday.summary_empty')}</div>
            )}
          </section>

          <section style={cardStyle}>
            <div style={{ ...sectionTitleStyle, display: 'flex', alignItems: 'center', gap: '0.45rem' }}>
              {t('myday.approvals')}
              {approvalCount > 0 && <span style={badgeStyle(false)}>{approvalCount}</span>}
            </div>
            {approvalCount === 0 && <div style={{ color: 'var(--tm-text-3)', fontSize: '0.82rem' }}>{t('myday.approvals_empty')}</div>}
            {(brief?.approvals ?? []).slice(0, 6).map((a) => (
              <div key={a.review_id} style={rowStyle}>
                <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{a.artifact_title}</span>
                <span style={{ fontSize: '0.72rem', color: 'var(--tm-text-3)', flexShrink: 0 }}>{formatStamp(a.requested_at, locale)}</span>
              </div>
            ))}
            {approvalCount > 6 && <div style={{ fontSize: '0.74rem', color: 'var(--tm-text-3)' }}>+{approvalCount - 6}</div>}
            <button type="button" style={{ ...linkStyle, marginTop: '0.35rem' }} onClick={() => navigate('/artifacts')}>
              {t('myday.approvals_open')} →
            </button>
          </section>

          <section style={cardStyle}>
            <div style={sectionTitleStyle}>{t('myday.runs')}</div>
            <div style={{ fontSize: '0.8rem', fontWeight: 600, color: 'var(--tm-text-2)', margin: '0.3rem 0 0.15rem' }}>
              {t('myday.runs_running')} {(brief?.running?.length ?? 0) > 0 && <span style={badgeStyle(false)}>{brief!.running.length}</span>}
            </div>
            {(brief?.running?.length ?? 0) === 0 && <div style={{ color: 'var(--tm-text-3)', fontSize: '0.8rem' }}>{t('myday.runs_empty')}</div>}
            {(brief?.running ?? []).map((r) => <RunRow key={r.run_id} run={r} locale={locale} />)}

            {failedCount > 0 && (
              <>
                <div style={{ fontSize: '0.8rem', fontWeight: 600, color: 'var(--tm-danger)', margin: '0.5rem 0 0.15rem' }}>
                  {t('myday.runs_failed')} <span style={badgeStyle(true)}>{failedCount}</span>
                </div>
                {brief!.failed.map((r) => <RunRow key={r.run_id} run={r} locale={locale} />)}
              </>
            )}

            <div style={{ fontSize: '0.8rem', fontWeight: 600, color: 'var(--tm-text-2)', margin: '0.5rem 0 0.15rem' }}>{t('myday.runs_recent')}</div>
            {(brief?.recent?.length ?? 0) === 0 && <div style={{ color: 'var(--tm-text-3)', fontSize: '0.8rem' }}>{t('myday.runs_empty')}</div>}
            {(brief?.recent ?? []).map((r) => <RunRow key={r.run_id} run={r} locale={locale} showStatus />)}
          </section>

          <section style={cardStyle}>
            <div style={sectionTitleStyle}>{t('myday.learning')}</div>
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
            <button type="button" style={{ ...linkStyle, marginTop: '0.35rem' }} onClick={() => navigate('/learning')}>
              {t('myday.learning_open')} →
            </button>
          </section>

          <section style={cardStyle}>
            <div style={sectionTitleStyle}>{t('myday.metrics', { month: metrics?.month ?? '' })}</div>
            {metrics ? (
              <div style={{ fontSize: '0.84rem', display: 'flex', gap: '1.2rem', flexWrap: 'wrap' }}>
                <span>{t('myday.metrics_runs', { runs: String(metrics.runs), failed: String(metrics.failed_runs) })}</span>
                <span>{t('myday.metrics_tokens', { tokens: formatTokens(metrics.tokens) })}</span>
                <span>{formatCost(metrics.cost_usd)}</span>
              </div>
            ) : (
              <div style={{ color: 'var(--tm-text-3)', fontSize: '0.82rem' }}>{t('common.loading')}</div>
            )}
          </section>
        </div>

        {/* Right column: the personal-efficiency GTD widget. */}
        <div style={{ minWidth: 0 }}>
          <section style={cardStyle}>
            <div style={{ ...sectionTitleStyle, display: 'flex', alignItems: 'center', gap: '0.45rem' }}>
              {t('myday.todos')}
              {todoTotal(clusters) > 0 && <span style={badgeStyle(false)}>{todoTotal(clusters)}</span>}
            </div>
            <div style={{ display: 'flex', gap: '0.35rem', marginBottom: '0.6rem' }}>
              <input
                value={newText}
                onChange={(e) => setNewText(e.target.value)}
                onKeyDown={(e) => { if (e.key === 'Enter') void addTodo() }}
                placeholder={t('myday.todo_placeholder')}
                style={{ flex: 1, minWidth: 0, fontSize: '0.8rem', padding: '0.3rem 0.5rem', background: 'var(--tm-raised)', border: '1px solid var(--tm-border)', borderRadius: 6, color: 'var(--tm-text)' }}
              />
              <select
                value={newGroup}
                onChange={(e) => setNewGroup(normalizeTodoGroup(e.target.value))}
                style={{ fontSize: '0.76rem', padding: '0.25rem', background: 'var(--tm-raised)', border: '1px solid var(--tm-border)', borderRadius: 6, color: 'var(--tm-text)' }}
              >
                {TODO_GROUPS.map((g) => <option key={g} value={g}>{t(GROUP_LABEL_KEYS[g])}</option>)}
              </select>
              <button type="button" onClick={() => void addTodo()} disabled={todoBusy || !newText.trim()} style={{ ...linkStyle, fontSize: '0.8rem' }}>+</button>
            </div>
            {TODO_GROUPS.map((g) => (
              <div key={g} style={{ marginBottom: '0.55rem' }}>
                <div style={{ fontSize: '0.7rem', textTransform: 'uppercase', letterSpacing: '0.04em', color: 'var(--tm-text-3)', marginBottom: '0.15rem' }}>
                  {t(GROUP_LABEL_KEYS[g])} {clusters[g].length > 0 && <span style={{ color: 'var(--tm-text-2)' }}>{clusters[g].length}</span>}
                </div>
                {clusters[g].map((todo) => (
                  <div key={todo.id} style={{ display: 'flex', alignItems: 'center', gap: '0.35rem', padding: '0.15rem 0', fontSize: '0.8rem' }}>
                    <span style={{ flex: 1, minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={todo.note ?? todo.text}>{todo.text}</span>
                    <select
                      value={normalizeTodoGroup(todo.group)}
                      onChange={(e) => void moveTodo(todo.id, e.target.value)}
                      style={{ fontSize: '0.68rem', padding: '0.05rem', background: 'var(--tm-raised)', border: '1px solid var(--tm-border)', borderRadius: 4, color: 'var(--tm-text-2)' }}
                      title={t('myday.todo_move')}
                    >
                      {TODO_GROUPS.map((og) => <option key={og} value={og}>{t(GROUP_LABEL_KEYS[og])}</option>)}
                    </select>
                    <button type="button" onClick={() => void removeTodo(todo.id)} style={{ ...linkStyle, color: 'var(--tm-text-3)', fontSize: '0.75rem' }} title={t('myday.todo_delete')}>✕</button>
                  </div>
                ))}
                {clusters[g].length === 0 && g === 'inbox' && (
                  <div style={{ color: 'var(--tm-text-3)', fontSize: '0.76rem' }}>{t('myday.todo_empty')}</div>
                )}
              </div>
            ))}
          </section>
        </div>
      </div>
    </div>
  )
}

const meterTrackStyle: React.CSSProperties = { height: '6px', borderRadius: 999, background: 'var(--tm-raised)', overflow: 'hidden' }
const meterFillStyle: React.CSSProperties = { height: '100%', borderRadius: 999, background: 'var(--tm-teal, #4fbdba)' }
