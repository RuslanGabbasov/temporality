import { useCallback, useEffect, useState } from 'react'
import { Button, InlineNotification, Tag } from '@carbon/react'
import { useI18n, pluralT } from './i18n'
import { formatDuration } from './eventSummary'
import { runStatusLabel } from './eventPresentation'
import { workspaceApi, type UserFact, type UserMetricsRow, type UserStats, type UserSagaStat } from './workspaceApi'

// The expanded Users-tab row (docs/system-dashboard.md): the per-period
// dossier of one person — runs and their model spend, sagas and knowledge
// they contributed to, the asks on their plate, the problems tied to their
// work — plus the stored facts agents saved about them. Every section that
// has a destination deep-links into it (runs initiator filter, artifact
// author filter, saga expansion, knowledge search, supervisor queue).

const PERIODS = ['24h', '168h', '720h', 'all'] as const
type Period = (typeof PERIODS)[number]

const PERIOD_LABELS: Record<Period, string> = {
  '24h': 'dashboard.users_period_24h',
  '168h': 'dashboard.users_period_7d',
  '720h': 'dashboard.users_period_30d',
  all: 'dashboard.users_period_all',
}

/** SPA navigation the dashboard uses everywhere: pushState + popstate. */
function go(path: string) {
  window.history.pushState(null, '', path)
  window.dispatchEvent(new PopStateEvent('popstate'))
}

/** Compact token/counter formatting: 999 → 999, 12_345 → 12.3k, 2_500_000 → 2.5M. */
function fmtNum(value: number): string {
  if (!Number.isFinite(value)) return '—'
  const abs = Math.abs(value)
  if (abs >= 1_000_000) return `${(value / 1_000_000).toFixed(1)}M`
  if (abs >= 10_000) return `${(value / 1_000).toFixed(1)}k`
  return value.toLocaleString()
}

function fmtCost(usd: number): string {
  if (!Number.isFinite(usd)) return '—'
  const digits = Math.abs(usd) >= 100 ? 0 : 2
  return `$${usd.toFixed(digits)}`
}

function fmtDate(value: string | undefined): string {
  if (!value) return '—'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? '—' : date.toLocaleString()
}

function Section({ title, extra, children }: { title: string; extra?: React.ReactNode; children: React.ReactNode }) {
  return (
    <section style={{ border: '1px solid var(--tm-border)', borderRadius: '6px', padding: '0.5rem 0.6rem 0.55rem', background: 'var(--tm-surface)', minWidth: 0 }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'baseline', gap: '0.5rem', marginBottom: '0.4rem' }}>
        <span style={{ fontSize: '0.68rem', textTransform: 'uppercase', letterSpacing: '0.04em', color: 'var(--tm-text-3)' }}>{title}</span>
        {extra}
      </div>
      {children}
    </section>
  )
}

function Tile({ label, value, hint, danger }: { label: string; value: React.ReactNode; hint?: React.ReactNode; danger?: boolean }) {
  return (
    <div title={typeof hint === 'string' ? hint : undefined} style={{ border: '1px solid var(--tm-border)', borderRadius: '5px', padding: '0.3rem 0.45rem', background: 'var(--tm-bg)', minWidth: '4.5rem' }}>
      <div style={{ fontSize: '0.66rem', color: 'var(--tm-text-3)', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{label}</div>
      <div style={{ fontSize: '0.95rem', fontWeight: 600, color: danger ? 'var(--tm-danger)' : 'var(--tm-text)' }}>{value}</div>
      {hint != null && <div style={{ fontSize: '0.64rem', color: 'var(--tm-text-3)' }}>{hint}</div>}
    </div>
  )
}

function tiles(grid: React.CSSProperties): React.CSSProperties {
  return { display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(6.5rem, 1fr))', gap: '0.35rem', ...grid }
}

function jumpStyle(active = false): React.CSSProperties {
  return {
    border: `1px solid ${active ? 'var(--tm-interactive)' : 'var(--tm-border)'}`,
    borderRadius: '999px',
    background: 'var(--tm-bg)',
    color: 'var(--tm-text-2)',
    cursor: 'pointer',
    fontSize: '0.7rem',
    padding: '0.12rem 0.55rem',
    lineHeight: 1.5,
    whiteSpace: 'nowrap',
  }
}

function ListRow({ children, onClick, title }: { children: React.ReactNode; onClick?: () => void; title?: string }) {
  return (
    <div
      onClick={onClick}
      title={title}
      style={{ display: 'flex', alignItems: 'baseline', gap: '0.5rem', fontSize: '0.74rem', padding: '0.12rem 0.15rem', borderRadius: '4px', cursor: onClick ? 'pointer' : 'default', minWidth: 0 }}
    >
      {children}
    </div>
  )
}

const sagaRoleIcon = (role: string) => (role === 'registered' ? '✎' : role === 'attached' ? '🔗' : '▸')

const askStatusLabelKey = (status: string) => {
  switch (status) {
    case 'pending': return 'dashboard.users_ask_pending'
    case 'delivered': return 'dashboard.users_ask_delivered'
    case 'answered': return 'dashboard.users_ask_answered'
    case 'expired': return 'dashboard.users_ask_expired'
    case 'cancelled': return 'dashboard.users_ask_cancelled'
    default: return null
  }
}

export function UserDetailPanel({ row }: { row: UserMetricsRow }) {
  const { t, locale } = useI18n()
  const [period, setPeriod] = useState<Period>('168h')
  const [stats, setStats] = useState<UserStats | null>(null)
  const [statsBusy, setStatsBusy] = useState(false)
  const [statsError, setStatsError] = useState('')
  const [facts, setFacts] = useState<UserFact[] | null>(null)
  const [draftFact, setDraftFact] = useState('')

  const loadStats = useCallback(async (window: string) => {
    setStatsBusy(true)
    setStatsError('')
    try {
      setStats(await workspaceApi.userStats(row.id, window))
    } catch (failure) {
      setStatsError(failure instanceof Error ? failure.message : 'Request failed')
    } finally {
      setStatsBusy(false)
    }
  }, [row.id])

  useEffect(() => { void loadStats(period) }, [period, loadStats])

  // Facts load once per expansion — they are window-independent.
  useEffect(() => {
    let alive = true
    setFacts(null)
    workspaceApi.userFacts(row.id)
      .then((data) => { if (alive) setFacts(data.facts ?? []) })
      .catch(() => { if (alive) setFacts([]) })
    return () => { alive = false }
  }, [row.id])

  const addFact = useCallback(async () => {
    const fact = draftFact.trim()
    if (!fact) return
    try {
      await workspaceApi.addUserFact(row.id, fact)
      setDraftFact('')
      const data = await workspaceApi.userFacts(row.id)
      setFacts(data.facts ?? [])
    } catch (failure) {
      setStatsError(failure instanceof Error ? failure.message : 'Request failed')
    }
  }, [draftFact, row.id])

  const removeFact = useCallback(async (factID: number) => {
    try {
      await workspaceApi.deleteUserFact(row.id, factID)
      setFacts((prev) => (prev ?? []).filter((f) => f.id !== factID))
    } catch (failure) {
      setStatsError(failure instanceof Error ? failure.message : 'Request failed')
    }
  }, [row.id])

  const runs = stats?.runs
  const sagas: UserSagaStat[] = stats?.sagas ?? []
  const problems = stats?.problems

  return (
    <div style={{ padding: '0.45rem 0.6rem 0.7rem 1.5rem', background: 'var(--tm-raised)', display: 'grid', gap: '0.55rem' }}>
      {/* Toolbar: jump links on the left, the period selector on the right —
          every number below belongs to the selected window. */}
      <div style={{ display: 'flex', flexWrap: 'wrap', gap: '0.4rem', alignItems: 'center', justifyContent: 'space-between' }}>
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: '0.35rem', alignItems: 'center' }}>
          <button type="button" style={jumpStyle()} onClick={() => go(`/agents?initiator=${encodeURIComponent(row.id)}`)} title={t('dashboard.users_jump_runs_hint')}>
            {t('dashboard.users_jump_runs')}
          </button>
          <button type="button" style={jumpStyle()} onClick={() => go(`/artifacts?author=${encodeURIComponent(row.id)}`)} title={t('dashboard.users_jump_artifacts_hint')}>
            {t('dashboard.users_jump_artifacts')}
          </button>
          <button type="button" style={jumpStyle()} onClick={() => go('/dashboard?tab=sagas')} title={t('dashboard.users_jump_sagas_hint')}>
            {t('dashboard.users_jump_sagas')}
          </button>
          <button type="button" style={jumpStyle()} onClick={() => go('/knowledge')} title={t('dashboard.users_jump_knowledge_hint')}>
            {t('dashboard.users_jump_knowledge')}
          </button>
          <button type="button" style={jumpStyle(problems != null && problems.supervisor_all > 0)} onClick={() => go('/dashboard?tab=supervisor')} title={t('dashboard.users_jump_problems_hint')}>
            {t('dashboard.users_jump_problems')}{problems != null && problems.supervisor_all > 0 ? ` · ${problems.supervisor_all}` : ''}
          </button>
        </div>
        <div style={{ display: 'flex', gap: '0.3rem', alignItems: 'center' }}>
          <span style={{ fontSize: '0.68rem', color: 'var(--tm-text-3)' }}>{t('dashboard.users_period')}</span>
          {PERIODS.map((value) => (
            <button
              key={value}
              type="button"
              onClick={() => setPeriod(value)}
              style={{
                ...jumpStyle(period === value),
                fontWeight: period === value ? 600 : 400,
                color: period === value ? 'var(--tm-text)' : undefined,
              }}
            >{t(PERIOD_LABELS[value])}</button>
          ))}
          {/* Refetching a period keeps the previous numbers on screen — the
              spinner is the only cue they are about to change. */}
          <span aria-hidden style={{ fontSize: '0.8rem', color: 'var(--tm-text-3)', visibility: statsBusy ? 'visible' : 'hidden' }}>↻</span>
        </div>
      </div>

      {statsError && (
        <InlineNotification kind="error" title={t('common.error')} subtitle={statsError} lowContrast hideCloseButton style={{ margin: 0 }} />
      )}

      {/* The tail of the row metrics: what they were last doing and what
          question is still hanging over them. */}
      {(row.last_run || row.last_ask_question) && (
        <div style={{ fontSize: '0.75rem', display: 'grid', gap: '0.15rem' }}>
          {row.last_run && (
            <span>
              {t('dashboard.users_last_run_did')}: <strong style={{ color: 'var(--tm-text)' }}>{row.last_run.title || row.last_run.run_id}</strong>
              {' '}· [{row.last_run.project}] · {runStatusLabel(row.last_run.status, t)}
              {row.failed_runs > 0 && <span style={{ color: 'var(--tm-danger)' }}> · ⚠ {pluralT(t, locale, 'dashboard.users_failed_n', row.failed_runs)}</span>}
            </span>
          )}
          {row.last_ask_question && (
            <span style={{ color: 'var(--tm-text-2)' }}>
              {t('dashboard.users_last_ask_did')}: «{row.last_ask_question}»
            </span>
          )}
        </div>
      )}

      {stats === null && statsBusy && (
        <span style={{ fontSize: '0.75rem', color: 'var(--tm-text-3)' }}>{t('common.loading')}</span>
      )}

      {stats && (
        <>
          {/* Runs + money + resources the period cost the system. */}
          <Section title={t('dashboard.users_sec_runs')} extra={<span style={{ fontSize: '0.66rem', color: 'var(--tm-text-3)' }}>{stats.from ? fmtDate(stats.from) : t('dashboard.users_period_all')}</span>}>
            <div style={tiles({})}>
              <Tile
                label={t('dashboard.users_stat_runs')}
                value={runs?.total ?? 0}
                hint={`${runs?.succeeded ?? 0} ✓ · ${runs?.failed ?? 0} ✗ · ${runs?.cancelled ?? 0} ∅`}
                danger={(runs?.failed ?? 0) > 0}
              />
              <Tile label={t('dashboard.users_stat_tokens')} value={fmtNum(runs?.tokens ?? 0)} hint={`${runs?.model_calls ?? 0} ${t('dashboard.users_stat_calls')}`} />
              <Tile label={t('dashboard.users_stat_cost')} value={fmtCost(runs?.cost_usd ?? 0)} hint={t('dashboard.users_stat_cost_hint')} />
              <Tile label={t('dashboard.users_stat_projects')} value={runs?.projects ?? 0} hint={t('dashboard.users_stat_projects_hint')} />
              <Tile label={t('dashboard.users_stat_agents')} value={runs?.agents ?? 0} hint={t('dashboard.users_stat_agents_hint')} />
              <Tile label={t('dashboard.users_asks_hab')} value={formatDuration(stats.asks.hab_seconds * 1000, t)} hint={t('dashboard.users_hab')} />
              <Tile label={t('dashboard.users_asks_wait')} value={formatDuration(stats.asks.wait_seconds * 1000, t)} hint={t('dashboard.users_wait')} />
            </div>
          </Section>

          <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(17rem, 1fr))', gap: '0.55rem' }}>
            {/* Spend per project — a row jumps to that project's sagas/budget page. */}
            <Section title={t('dashboard.users_by_project')}>
              {stats.by_project.length === 0 && <span style={{ fontSize: '0.72rem', color: 'var(--tm-text-3)' }}>—</span>}
              {stats.by_project.map((project) => (
                <ListRow
                  key={project.project_id}
                  title={`${project.project_id} · ${project.runs} ${t('dashboard.users_stat_runs')}`}
                  onClick={() => go(`/dashboard?project=${encodeURIComponent(project.project_id)}&tab=sagas`)}
                >
                  <span style={{ flex: 1, minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', color: 'var(--tm-text)' }}>{project.project_id}</span>
                  <span style={{ color: 'var(--tm-text-2)' }}>{project.runs}</span>
                  {project.failed > 0 && <span style={{ color: 'var(--tm-danger)' }}>✗{project.failed}</span>}
                  <span style={{ color: 'var(--tm-text-3)', minWidth: '3rem', textAlign: 'right' }}>{fmtNum(project.tokens)}</span>
                  <span style={{ color: 'var(--tm-text-2)', minWidth: '3.2rem', textAlign: 'right' }}>{fmtCost(project.cost_usd)}</span>
                </ListRow>
              ))}
            </Section>

            {/* Spend per agent — display-only: agent ids are the stable label. */}
            <Section title={t('dashboard.users_by_agent')}>
              {stats.by_agent.length === 0 && <span style={{ fontSize: '0.72rem', color: 'var(--tm-text-3)' }}>—</span>}
              {stats.by_agent.map((agent) => (
                <ListRow key={agent.agent_id} title={agent.agent_id}>
                  <span style={{ flex: 1, minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', color: 'var(--tm-text)' }}>{agent.agent_id}</span>
                  <span style={{ color: 'var(--tm-text-2)' }}>{agent.runs}</span>
                  {agent.failed > 0 && <span style={{ color: 'var(--tm-danger)' }}>✗{agent.failed}</span>}
                  <span style={{ color: 'var(--tm-text-3)', minWidth: '3rem', textAlign: 'right' }}>{fmtNum(agent.tokens)}</span>
                  <span style={{ color: 'var(--tm-text-2)', minWidth: '3.2rem', textAlign: 'right' }}>{fmtCost(agent.cost_usd)}</span>
                </ListRow>
              ))}
            </Section>
          </div>

          <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(17rem, 1fr))', gap: '0.55rem' }}>
            {/* Sagas: registered/attached by this person or executed through
                bound tasks — a row opens that saga expanded on the sagas tab. */}
            <Section title={t('dashboard.users_sec_sagas')}>
              {sagas.length === 0 && <span style={{ fontSize: '0.72rem', color: 'var(--tm-text-3)' }}>{t('dashboard.users_sagas_empty')}</span>}
              <div style={{ display: 'flex', flexWrap: 'wrap', gap: '0.35rem' }}>
                {sagas.map((saga) => (
                  <button
                    key={saga.saga_id}
                    type="button"
                    style={jumpStyle()}
                    title={`${saga.saga_id} · ${t(`dashboard.users_saga_role_${saga.role === 'registered' ? 'registered' : saga.role === 'attached' ? 'attached' : 'work'}`)}`}
                    onClick={() => {
                      const project = saga.project ? `project=${encodeURIComponent(saga.project)}&` : ''
                      go(`/dashboard?${project}tab=sagas&saga=${encodeURIComponent(saga.saga_id)}`)
                    }}
                  >
                    {sagaRoleIcon(saga.role)} {saga.title || saga.saga_id}{saga.runs > 0 ? ` · ${saga.runs}` : ''}
                  </button>
                ))}
              </div>
            </Section>

            {/* Knowledge contribution: proposals this person authored. */}
            <Section
              title={t('dashboard.users_sec_knowledge')}
              extra={<span style={{ fontSize: '0.66rem', color: 'var(--tm-text-3)' }}>{t('dashboard.users_knowledge_proposed')} · {stats.knowledge.proposed}</span>}
            >
              {stats.knowledge.items.length === 0 && <span style={{ fontSize: '0.72rem', color: 'var(--tm-text-3)' }}>{t('dashboard.users_knowledge_empty')}</span>}
              <div style={{ display: 'grid', gap: '0.15rem' }}>
                {stats.knowledge.items.map((item) => {
                  const query = item.proposition || item.knowledge_id
                  const project = item.project ? `project=${encodeURIComponent(item.project)}&` : ''
                  return (
                    <ListRow
                      key={`${item.knowledge_id}-${item.at}`}
                      title={item.proposition || item.knowledge_id}
                      onClick={() => go(`/knowledge?${project}view=search&q=${encodeURIComponent(query)}`)}
                    >
                      <span style={{ flex: 1, minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                        {item.proposition || item.knowledge_id}
                      </span>
                      <span style={{ color: 'var(--tm-text-3)', whiteSpace: 'nowrap' }}>{fmtDate(item.at)}</span>
                    </ListRow>
                  )
                })}
              </div>
            </Section>
          </div>

          {/* Asks: what waits on this person and what they already decided. */}
          <Section title={t('dashboard.users_sec_asks')}>
            <div style={tiles({})}>
              <Tile label={t('dashboard.users_open')} value={stats.asks.open} danger={stats.asks.open > 0} />
              <Tile label={t('dashboard.users_asks_answered')} value={stats.asks.answered} hint={formatDuration(stats.asks.wait_seconds * 1000, t)} />
              <Tile label={t('dashboard.users_asks_timedout')} value={stats.asks.timed_out} danger={stats.asks.timed_out > 0} />
            </div>
            {stats.asks.recent.length > 0 && (
              <div style={{ marginTop: '0.4rem', display: 'grid', gap: '0.15rem' }}>
                <div style={{ fontSize: '0.68rem', color: 'var(--tm-text-3)' }}>{t('dashboard.users_asks_recent')}</div>
                {stats.asks.recent.map((ask, index) => {
                  const labelKey = askStatusLabelKey(ask.status)
                  return (
                    <ListRow
                      key={`${ask.at}-${index}`}
                      title={ask.question}
                      onClick={ask.run_id ? () => go(`/agents?run=${encodeURIComponent(ask.run_id!)}`) : undefined}
                    >
                      <span style={{ flex: 1, minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>«{ask.question}»</span>
                      {labelKey && <Tag size="sm" type={ask.status === 'answered' ? 'teal' : ask.status === 'expired' ? 'red' : 'gray'}>{t(labelKey)}</Tag>}
                      <span style={{ color: 'var(--tm-text-3)', whiteSpace: 'nowrap' }}>{fmtDate(ask.at)}</span>
                    </ListRow>
                  )
                })}
              </div>
            )}
          </Section>

          {/* Problems: failures, expired asks and open supervisor cases
              about this person's runs — each row jumps to its evidence. */}
          <Section title={t('dashboard.users_sec_problems')}>
            {problems && problems.failed_runs.length === 0 && problems.timed_out_asks.length === 0 && problems.supervisor.length === 0 && (
              <span style={{ fontSize: '0.74rem', color: 'var(--tm-text-2)' }}>{t('dashboard.users_problems_none')}</span>
            )}
            <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(17rem, 1fr))', gap: '0.5rem' }}>
              {problems && problems.failed_runs.length > 0 && (
                <div style={{ display: 'grid', gap: '0.15rem', alignContent: 'start' }}>
                  <div style={{ fontSize: '0.68rem', color: 'var(--tm-danger)' }}>{t('dashboard.users_problem_failed')} · {runs?.failed ?? problems.failed_runs.length}</div>
                  {problems.failed_runs.map((run) => (
                    <ListRow
                      key={run.run_id}
                      title={run.error || run.run_id}
                      onClick={() => go(`/agents?run=${encodeURIComponent(run.run_id)}`)}
                    >
                      <span style={{ flex: 1, minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', color: 'var(--tm-text)' }}>
                        {run.title || run.run_id}
                      </span>
                      <span style={{ color: 'var(--tm-text-3)', whiteSpace: 'nowrap' }}>{fmtDate(run.at)}</span>
                    </ListRow>
                  ))}
                </div>
              )}
              {problems && problems.timed_out_asks.length > 0 && (
                <div style={{ display: 'grid', gap: '0.15rem', alignContent: 'start' }}>
                  <div style={{ fontSize: '0.68rem', color: 'var(--tm-warning)' }}>{t('dashboard.users_problem_timedout')} · {stats?.asks.timed_out ?? problems.timed_out_asks.length}</div>
                  {problems.timed_out_asks.map((ask, index) => (
                    <ListRow key={`${ask.at}-${index}`} title={ask.question} onClick={ask.run_id ? () => go(`/agents?run=${encodeURIComponent(ask.run_id!)}`) : undefined}>
                      <span style={{ flex: 1, minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>«{ask.question}»</span>
                      <span style={{ color: 'var(--tm-text-3)', whiteSpace: 'nowrap' }}>{fmtDate(ask.at)}</span>
                    </ListRow>
                  ))}
                </div>
              )}
              {problems && problems.supervisor.length > 0 && (
                <div style={{ display: 'grid', gap: '0.15rem', alignContent: 'start' }}>
                  <div style={{ fontSize: '0.68rem', color: 'var(--tm-danger)' }}>
                    {t('dashboard.users_problem_supervisor')}
                    {problems.supervisor_all > problems.supervisor.length ? ` · ${t('dashboard.users_supervisor_more', { count: String(problems.supervisor_all - problems.supervisor.length) })}` : ` · ${problems.supervisor.length}`}
                  </div>
                  {problems.supervisor.map((problem) => (
                    <ListRow key={`${problem.family}-${problem.subject}`} title={problem.summary || problem.cause || problem.subject} onClick={() => go('/dashboard?tab=supervisor')}>
                      <Tag size="sm" type="red">{problem.family}</Tag>
                      <span style={{ flex: 1, minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{problem.summary || problem.cause || problem.subject}</span>
                      <span style={{ color: 'var(--tm-text-3)', whiteSpace: 'nowrap' }}>{fmtDate(problem.last_seen)}</span>
                    </ListRow>
                  ))}
                </div>
              )}
            </div>
          </Section>
        </>
      )}

      {/* Facts: the explicit half of the per-user picture — preferences and
          durable facts the dialogs accumulate (unchanged behaviour). */}
      <section>
        <div style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)', marginBottom: '0.3rem' }}>{t('dashboard.users_facts_title')}</div>
        {facts === null ? (
          <span style={{ fontSize: '0.78rem', color: 'var(--tm-text-3)' }}>{t('common.loading')}</span>
        ) : facts.length === 0 ? (
          <span style={{ fontSize: '0.78rem', color: 'var(--tm-text-3)' }}>{t('dashboard.users_facts_empty')}</span>
        ) : (
          <div style={{ display: 'grid', gap: '0.2rem', marginBottom: '0.4rem' }}>
            {facts.map((fact) => (
              <div key={fact.id} style={{ display: 'flex', alignItems: 'baseline', gap: '0.5rem', fontSize: '0.8rem' }}>
                <span style={{ flex: 1 }}>
                  {fact.fact}
                  {fact.origin === 'agent' && <span style={{ color: 'var(--tm-text-3)', fontSize: '0.68rem' }}> · {t('dashboard.users_facts_from_dialog')}</span>}
                </span>
                <button
                  onClick={() => void removeFact(fact.id)}
                  title={t('dashboard.users_facts_delete')}
                  style={{ background: 'none', border: 'none', color: 'var(--tm-text-3)', cursor: 'pointer', lineHeight: 1 }}
                >✕</button>
              </div>
            ))}
          </div>
        )}
        <div style={{ display: 'flex', gap: '0.4rem', marginTop: '0.3rem' }}>
          <input
            value={draftFact}
            onChange={(e: React.ChangeEvent<HTMLInputElement>) => setDraftFact(e.target.value)}
            onKeyDown={(e: React.KeyboardEvent) => { if (e.key === 'Enter') void addFact() }}
            placeholder={t('dashboard.users_facts_placeholder')}
            style={{ flex: 1, maxWidth: '28rem', background: 'var(--tm-surface)', border: '1px solid var(--tm-border)', borderRadius: '4px', color: 'var(--tm-text)', padding: '0.25rem 0.5rem', fontSize: '0.78rem' }}
          />
          <Button size="sm" kind="ghost" disabled={!draftFact.trim()} onClick={() => void addFact()}>{t('dashboard.users_facts_add')}</Button>
        </div>
      </section>

      {/* The row's identity, echoed for context while scrolled deep into the dossier. */}
      <div style={{ display: 'flex', gap: '0.35rem', flexWrap: 'wrap' }}>
        <span style={{ fontSize: '0.68rem', color: 'var(--tm-text-3)' }}>{row.name || row.id}</span>
        {row.role && <Tag size="sm" type="gray">{row.role}</Tag>}
        {row.org_unit_id && <Tag size="sm" type="cool-gray">{row.org_unit_id}</Tag>}
        {!row.active && <Tag size="sm" type="gray">{t('dashboard.users_inactive')}</Tag>}
        <span style={{ fontSize: '0.66rem', color: 'var(--tm-text-3)', marginLeft: 'auto' }}>
          {t('dashboard.users_period')}: {t(PERIOD_LABELS[period])}
        </span>
      </div>
    </div>
  )
}
