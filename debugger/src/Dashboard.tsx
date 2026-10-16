import { useCallback, useEffect, useMemo, useRef, useState, type CSSProperties, type ReactNode } from 'react'
import { Heading, InlineNotification, Tag, Tile } from '@carbon/react'
import { observationApi, type HealthStats, type HealthModelStats } from './observationApi'
import {
  projectsOverview,
  qualitySummary,
  operations,
  type ProjectsOverview,
  type QualitySummary,
  type QualitySummaryAgent,
} from './kernelApi'
import ProjectOverview from './ProjectOverview'
import Observability from './Observability'
import { useI18n, pluralT } from './i18n'
import { formatDuration } from './eventSummary'

function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : 'Request failed'
}

type Tab = 'overview' | 'quality' | 'sagas' | 'knowledge'
const TAB_KEYS: Tab[] = ['overview', 'quality', 'sagas', 'knowledge']

// The journal stats window and the kernel overview window share one selector.
// `go` is the Go duration the kernel endpoint understands (ParseDuration has
// no "d" unit, so a week is 168h).
const WINDOWS: { key: string; ms: number; go: string }[] = [
  { key: 'dashboard.window_hour', ms: 3600e3, go: '1h' },
  { key: 'dashboard.window_day', ms: 24 * 3600e3, go: '24h' },
  { key: 'dashboard.window_week', ms: 7 * 24 * 3600e3, go: '168h' },
]

const REFRESH_MS = 30_000

function verdictColor(verdict: string): string {
  switch (verdict) {
    case 'pass':
      return 'var(--tm-success)'
    case 'fail':
      return 'var(--tm-danger)'
    case 'error':
    case 'unknown':
      return 'var(--tm-warning)'
    default:
      return 'var(--tm-text-3)'
  }
}

/** One clickable health-strip chip: a counter with a drill-down link. */
function HealthChip({ label, value, title, danger, onClick }: { label: string; value: number | string; title?: string; danger?: boolean; onClick?: () => void }) {
  return (
    <button
      onClick={onClick}
      disabled={!onClick}
      title={title}
      style={{
        display: 'inline-flex', alignItems: 'baseline', gap: '0.35rem',
        padding: '0.3rem 0.7rem', borderRadius: '999px',
        border: `1px solid ${danger && Number(value) > 0 ? 'var(--tm-danger)' : 'var(--tm-border)'}`,
        background: 'var(--tm-surface)', color: 'var(--tm-text-2)',
        cursor: onClick ? 'pointer' : 'default', fontSize: '0.78rem',
      }}
    >
      <strong style={{ color: danger && Number(value) > 0 ? 'var(--tm-danger)' : 'var(--tm-text)', fontSize: '0.95rem' }}>{value}</strong>
      <span>{label}</span>
    </button>
  )
}

function PanelHeader({ title, hint }: { title: string; hint?: string }) {
  return (
    <div style={{ display: 'flex', alignItems: 'baseline', gap: '0.5rem', marginBottom: '0.4rem' }}>
      <Heading style={{ fontSize: '0.95rem' }}>{title}</Heading>
      {hint && <span style={{ fontSize: '0.72rem', color: 'var(--tm-text-3)' }}>{hint}</span>}
    </div>
  )
}

const tableGrid = (columns: string): CSSProperties => ({ display: 'grid', gridTemplateColumns: columns, gap: '0.25rem 0.6rem', alignItems: 'baseline' })

function ColHead({ children, columns }: { children: ReactNode; columns: string }) {
  return (
    <div style={{ ...tableGrid(columns), fontSize: '0.68rem', textTransform: 'uppercase', letterSpacing: '0.04em', color: 'var(--tm-text-3)', padding: '0.35rem 0.6rem', background: 'var(--tm-raised)' }}>
      {children}
    </div>
  )
}

interface Props {
  project: string
  setProject: (p: string) => void
}

/**
 * System dashboard (docs/system-dashboard.md): the operational health surface —
 * live health strip, provider/model stats, per-project rollup with org grouping,
 * the quality loop summary, plus sagas and knowledge observability as tabs.
 */
export default function Dashboard({ project, setProject }: Props) {
  const { t, locale } = useI18n()
  const initTab = new URLSearchParams(window.location.search).get('tab') as Tab | null
  const [tab, setTab] = useState<Tab>(TAB_KEYS.includes(initTab as Tab) ? (initTab as Tab) : 'overview')
  const [windowIdx, setWindowIdx] = useState(1) // 24h by default
  const [health, setHealth] = useState<HealthStats | null>(null)
  const [overview, setOverview] = useState<ProjectsOverview | null>(null)
  const [opsCount, setOpsCount] = useState<number | null>(null)
  const [quality, setQuality] = useState<QualitySummary | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [updatedAt, setUpdatedAt] = useState<Date | null>(null)
  const timerRef = useRef<ReturnType<typeof setInterval> | null>(null)

  const window_ = WINDOWS[windowIdx] ?? WINDOWS[1]

  const navigate = (path: string) => { window.history.pushState(null, '', path); window.dispatchEvent(new PopStateEvent('popstate')) }

  const loadOverviewData = useCallback(async () => {
    setBusy(true)
    setError('')
    const since = new Date(Date.now() - window_.ms).toISOString()
    try {
      const [stats, projects, ops] = await Promise.all([
        observationApi.healthStats(undefined, since),
        projectsOverview(window_.go),
        operations().catch(() => null),
      ])
      setHealth(stats)
      setOverview(projects)
      setOpsCount(ops ? ops.count : null)
      setUpdatedAt(new Date())
    } catch (failure) {
      setError(errorMessage(failure))
    } finally {
      setBusy(false)
    }
  }, [window_.go, window_.ms])

  const loadQuality = useCallback(async () => {
    setBusy(true)
    setError('')
    try {
      setQuality(await qualitySummary())
      setUpdatedAt(new Date())
    } catch (failure) {
      setError(errorMessage(failure))
    } finally {
      setBusy(false)
    }
  }, [])

  useEffect(() => {
    if (tab === 'overview') void loadOverviewData()
    if (tab === 'quality') void loadQuality()
  }, [tab, loadOverviewData, loadQuality])

  // Auto-refresh the live tab while it is visible; a hidden tab (background
  // window) must not keep hammering the journal.
  useEffect(() => {
    if (timerRef.current) { clearInterval(timerRef.current); timerRef.current = null }
    if (tab !== 'overview') return
    timerRef.current = setInterval(() => {
      if (document.visibilityState === 'visible') void loadOverviewData()
    }, REFRESH_MS)
    return () => { if (timerRef.current) clearInterval(timerRef.current) }
  }, [tab, loadOverviewData])

  // Keep `tab` in the URL (preserving foreign params like the knowledge tab's
  // ktab/as_of) so links and reloads land on the same view.
  useEffect(() => {
    const params = new URLSearchParams(window.location.search)
    params.set('tab', tab)
    window.history.replaceState(null, '', `${window.location.pathname}?${params.toString()}`)
  }, [tab])

  const projectsByOrg = useMemo(() => {
    const groups = new Map<string, ProjectsOverview['projects']>()
    for (const row of overview?.projects ?? []) {
      const key = row.org_units?.[0] ?? ''
      groups.set(key, [...(groups.get(key) ?? []), row])
    }
    return [...groups.entries()].sort((a, b) => (a[0] === '' ? 1 : b[0] === '' ? -1 : a[0].localeCompare(b[0])))
  }, [overview])

  const pickProject = (id: string) => {
    setProject(id)
    setTab('sagas')
  }

  const tabs: { key: Tab; label: string }[] = [
    { key: 'overview', label: t('dashboard.tab_overview') ?? 'Overview' },
    { key: 'quality', label: t('dashboard.tab_quality') ?? 'Quality' },
    { key: 'sagas', label: t('dashboard.tab_sagas') ?? 'Sagas' },
    { key: 'knowledge', label: t('dashboard.tab_knowledge') ?? 'Knowledge' },
  ]

  return (
    <div style={{ padding: '1rem' }}>
      {error && (
        <InlineNotification kind="error" title={t('common.error') ?? 'Error'} subtitle={error} onClose={() => setError('')} lowContrast style={{ marginBottom: '1rem' }} />
      )}

      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: '0.75rem', flexWrap: 'wrap', marginBottom: '0.75rem' }}>
        <Heading style={{ fontSize: '1.1rem' }}>{t('dashboard.title') ?? 'System dashboard'}</Heading>
        <div style={{ display: 'flex', alignItems: 'center', gap: '0.6rem' }}>
          {updatedAt && (
            <span title={updatedAt.toISOString()} style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)' }}>
              {t('dashboard.updated') ?? 'updated'} {updatedAt.toLocaleTimeString()}
            </span>
          )}
          {(tab === 'overview' || tab === 'quality') && (
            <>
              {tab === 'overview' && (
                <label className="toolbar-select">
                  {t('dashboard.window') ?? 'Window'}
                  <select value={windowIdx} onChange={(e) => setWindowIdx(Number(e.target.value))}>
                    {WINDOWS.map((w, i) => (
                      <option key={w.key} value={i}>{t(w.key) ?? w.key}</option>
                    ))}
                  </select>
                </label>
              )}
              <button className="toolbar-btn" onClick={() => (tab === 'overview' ? void loadOverviewData() : void loadQuality())} disabled={busy} title={t('common.refresh') ?? 'Refresh'}>↻</button>
            </>
          )}
        </div>
      </div>

      {/* Tab strip */}
      <div style={{ display: 'flex', gap: '0.2rem', borderBottom: '1px solid var(--tm-border)', marginBottom: '0.9rem' }}>
        {tabs.map((item) => (
          <button
            key={item.key}
            onClick={() => setTab(item.key)}
            style={{
              padding: '0.45rem 0.8rem', background: 'none', border: 'none', borderBottom: `2px solid ${tab === item.key ? 'var(--tm-teal)' : 'transparent'}`,
              color: tab === item.key ? 'var(--tm-text)' : 'var(--tm-text-3)', cursor: 'pointer', fontSize: '0.82rem', fontWeight: tab === item.key ? 600 : 400,
            }}
          >
            {item.label}
          </button>
        ))}
      </div>

      {tab === 'overview' && (
        <>
          {/* Health strip */}
          <div style={{ display: 'flex', gap: '0.5rem', flexWrap: 'wrap', marginBottom: '1rem' }}>
            <HealthChip
              value={health?.runs.running_now ?? 0}
              label={pluralT(t, locale, 'dashboard.running_n', health?.runs.running_now ?? 0)}
              title={t('dashboard.running_hint') ?? undefined}
              onClick={() => navigate('/agents')}
            />
            <HealthChip
              value={health?.totals.in_flight ?? 0}
              label={pluralT(t, locale, 'dashboard.inflight_n', health?.totals.in_flight ?? 0)}
              title={t('dashboard.inflight_hint') ?? undefined}
              onClick={() => navigate('/agents')}
            />
            <HealthChip
              value={health?.totals.model_failures ?? 0}
              label={pluralT(t, locale, 'dashboard.failures_n', health?.totals.model_failures ?? 0)}
              danger
              title={t('dashboard.failures_hint') ?? undefined}
              onClick={() => navigate('/agents')}
            />
            <HealthChip
              value={health?.approvals.pending ?? 0}
              label={pluralT(t, locale, 'dashboard.approvals_n', health?.approvals.pending ?? 0)}
              title={health?.approvals.wait_ms_avg ? `${t('dashboard.wait_avg') ?? 'avg wait'}: ${formatDuration(health.approvals.wait_ms_avg)}` : undefined}
              onClick={() => navigate('/agents')}
            />
            {opsCount !== null && (
              <HealthChip
                value={opsCount}
                label={pluralT(t, locale, 'dashboard.ops_n', opsCount)}
                title={t('dashboard.ops_hint') ?? undefined}
                onClick={() => navigate('/agents')}
              />
            )}
          </div>

          {/* Models / providers */}
          <PanelHeader title={t('dashboard.models') ?? 'Models & providers'} hint={t(windows_label(windowIdx)) ?? undefined} />
          {health && health.models.length > 0 ? (
            <div style={{ border: '1px solid var(--tm-border)', borderRadius: '6px', overflow: 'hidden', marginBottom: '1rem' }}>
              <ColHead columns="minmax(9rem, 2fr) minmax(5rem, 1fr) minmax(3.5rem, 0.7fr) minmax(3.5rem, 0.7fr) minmax(4.5rem, 1fr) minmax(5.5rem, 1.1fr) minmax(5rem, 1fr)">
                <span>{t('dashboard.col_model') ?? 'Model'}</span>
                <span>{t('dashboard.col_calls') ?? 'Calls'}</span>
                <span title={t('dashboard.inflight_hint') ?? undefined}>⇄ {t('dashboard.col_inflight') ?? 'in flight'}</span>
                <span title={t('dashboard.failures_hint') ?? undefined}>✗ {t('dashboard.col_failed') ?? 'failed'}</span>
                <span title={t('dashboard.retries_hint') ?? undefined}>↻ {t('dashboard.col_retries') ?? 'retries'}</span>
                <span>p50 / p95</span>
                <span>{t('runs.tokens') ?? 'Tokens'}</span>
              </ColHead>
              {health.models.map((m: HealthModelStats) => (
                <div key={`${m.provider ?? ''}/${m.model}`} style={{ ...tableGrid("minmax(9rem, 2fr) minmax(5rem, 1fr) minmax(3.5rem, 0.7fr) minmax(3.5rem, 0.7fr) minmax(4.5rem, 1fr) minmax(5.5rem, 1.1fr) minmax(5rem, 1fr)"), fontSize: '0.78rem', padding: '0.35rem 0.6rem', borderTop: '1px solid var(--tm-border)' }}>
                  <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={`${m.model}${m.provider ? ` · ${m.provider}` : ''}`}>
                    {m.model}
                    {m.provider && <span style={{ color: 'var(--tm-text-3)' }}> · {m.provider}</span>}
                  </span>
                  <span>{m.calls}</span>
                  <span style={{ color: m.in_flight > 0 ? 'var(--tm-text)' : 'var(--tm-text-3)' }}>{m.in_flight}</span>
                  <span style={{ color: m.failed > 0 ? 'var(--tm-danger)' : 'var(--tm-text-3)' }}>{m.failed}</span>
                  <span style={{ color: 'var(--tm-text-3)' }}>{[m.retries, m.nudged].some((n) => n > 0) ? `${m.retries}${m.nudged > 0 ? ` +${m.nudged}⚡` : ''}` : '—'}</span>
                  <span style={{ color: 'var(--tm-text-3)' }}>{m.p50_latency_ms ? formatDuration(m.p50_latency_ms) : '—'} / {m.p95_latency_ms ? formatDuration(m.p95_latency_ms) : '—'}</span>
                  <span>{m.total_tokens.toLocaleString()}</span>
                </div>
              ))}
            </div>
          ) : !busy && (
            <Tile style={{ color: 'var(--tm-text-3)', marginBottom: '1rem' }}>{t('dashboard.no_models') ?? 'No model calls in the window'}</Tile>
          )}

          {/* Projects grouped by org unit */}
          <PanelHeader title={t('dashboard.projects') ?? 'Projects'} hint={t('dashboard.projects_hint') ?? undefined} />
          {overview && overview.projects.length > 0 ? (
            projectsByOrg.map(([org, rows]) => (
              <div key={org || 'none'} style={{ marginBottom: '0.9rem' }}>
                {org ? (
                  <div style={{ fontSize: '0.7rem', textTransform: 'uppercase', letterSpacing: '0.05em', color: 'var(--tm-teal)', marginBottom: '0.25rem' }}>{org}</div>
                ) : (
                  <div style={{ fontSize: '0.7rem', textTransform: 'uppercase', letterSpacing: '0.05em', color: 'var(--tm-text-3)', marginBottom: '0.25rem' }}>{t('dashboard.org_none') ?? 'No org unit'}</div>
                )}
                <div style={{ border: '1px solid var(--tm-border)', borderRadius: '6px', overflow: 'hidden' }}>
                  <ColHead columns="minmax(8rem, 1.6fr) minmax(4.5rem, 1fr) minmax(3.5rem, 0.7fr) minmax(3.5rem, 0.7fr) minmax(4.5rem, 1fr) minmax(5rem, 1fr) minmax(3.5rem, 0.8fr) minmax(6.5rem, 1.2fr)">
                    <span>{t('dashboard.col_project') ?? 'Project'}</span>
                    <span>{t('runs.task_col_runs') ?? 'Runs'}</span>
                    <span>⇄</span>
                    <span>✗</span>
                    <span>{t('dashboard.col_calls') ?? 'Calls'}</span>
                    <span>{t('runs.tokens') ?? 'Tokens'}</span>
                    <span>{t('runs.cost') ?? 'Cost'}</span>
                    <span title={t('dashboard.approvals_hint') ?? undefined}>👤</span>
                  </ColHead>
                  {rows.map((row) => (
                    <div
                      key={row.project}
                      onClick={() => pickProject(row.project)}
                      title={`${row.project}\n${row.last_activity_at ?? ''}`}
                      style={{ ...tableGrid("minmax(8rem, 1.6fr) minmax(4.5rem, 1fr) minmax(3.5rem, 0.7fr) minmax(3.5rem, 0.7fr) minmax(4.5rem, 1fr) minmax(5rem, 1fr) minmax(3.5rem, 0.8fr) minmax(6.5rem, 1.2fr)"), fontSize: '0.8rem', padding: '0.35rem 0.6rem', borderTop: '1px solid var(--tm-border)', cursor: 'pointer' }}
                    >
                      <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', color: row.project === project ? 'var(--tm-teal)' : 'var(--tm-text)', fontWeight: row.project === project ? 600 : 400 }}>
                        {row.project}
                        {row.runs_running > 0 && <Tag type="cool-gray" size="sm" style={{ marginLeft: '0.35rem' }}>{pluralT(t, locale, 'runs.task_running_n', row.runs_running)}</Tag>}
                      </span>
                      <span>{row.runs_started}</span>
                      <span style={{ color: row.runs_running > 0 ? 'var(--tm-text)' : 'var(--tm-text-3)' }}>{row.runs_running}</span>
                      <span style={{ color: row.runs_failed > 0 ? 'var(--tm-danger)' : 'var(--tm-text-3)' }}>{row.runs_failed}</span>
                      <span style={{ color: 'var(--tm-text-3)' }}>{row.model_calls}</span>
                      <span>{row.total_tokens.toLocaleString()}</span>
                      <span>{row.total_cost_usd > 0 ? `$${row.total_cost_usd.toFixed(2)}` : '—'}</span>
                      <span style={{ whiteSpace: 'nowrap', color: row.pending_approvals > 0 ? 'var(--tm-warning)' : 'var(--tm-text-3)' }}>
                        {row.pending_approvals > 0 ? `⏳ ${row.pending_approvals}` : '—'}
                      </span>
                    </div>
                  ))}
                </div>
              </div>
            ))
          ) : !busy && (
            <Tile style={{ color: 'var(--tm-text-3)' }}>{t('dashboard.no_projects') ?? 'No project activity in the window'}</Tile>
          )}
        </>
      )}

      {tab === 'quality' && (
        <QualityTab quality={quality} busy={busy} t={t} navigate={navigate} />
      )}

      {tab === 'sagas' && <ProjectOverview project={project} />}

      {tab === 'knowledge' && <Observability project={project} />}
    </div>
  )
}

function windows_label(idx: number): string {
  return WINDOWS[idx]?.key ?? 'dashboard.window_day'
}

function QualityTab({ quality, busy, t, navigate }: {
  quality: QualitySummary | null
  busy: boolean
  t: (key: string, vars?: Record<string, string>) => string
  navigate: (path: string) => void
}) {
  const loop = quality?.loop
  const overrideHot = (loop?.override_rate ?? 0) > 0.05
  return (
    <>
      <PanelHeader title={t('dashboard.quality_loop') ?? 'Quality loop'} hint={loop ? `${loop.window_days}d` : undefined} />
      {loop ? (
        <div style={{ display: 'flex', gap: '0.5rem', flexWrap: 'wrap', marginBottom: '1rem' }}>
          <HealthChip value={`✓ ${loop.gate_passed}`} label={t('dashboard.gate_passed') ?? 'gates passed'} />
          <HealthChip value={`✗ ${loop.gate_blocked}`} label={t('dashboard.gate_blocked') ?? 'blocked'} danger />
          <HealthChip
            value={`${(loop.override_rate * 100).toFixed(1)}%`}
            label={t('dashboard.override_rate') ?? 'overrides'}
            danger={overrideHot}
            title={overrideHot ? (t('dashboard.override_warn') ?? undefined) : `overridden: ${loop.gate_overridden}`}
          />
          <HealthChip
            value={`${loop.judge_degraded_runs}/${loop.evaluation_runs}`}
            label={t('dashboard.judge_degraded') ?? 'judge-degraded'}
            danger={loop.judge_degraded_runs > 0}
            title={t('dashboard.judge_degraded_hint') ?? undefined}
          />
        </div>
      ) : !busy && (
        <Tile style={{ color: 'var(--tm-text-3)', marginBottom: '1rem' }}>{t('dashboard.no_quality') ?? 'No quality loop data'}</Tile>
      )}

      <PanelHeader title={t('dashboard.agents') ?? 'Agents'} hint={t('dashboard.agents_hint') ?? undefined} />
      {quality && quality.agents.length > 0 ? (
        <div style={{ border: '1px solid var(--tm-border)', borderRadius: '6px', overflow: 'hidden' }}>
          <ColHead columns="minmax(9rem, 1.8fr) minmax(6rem, 1fr) minmax(3rem, 0.6fr) minmax(9rem, 1.6fr) minmax(4.5rem, 1fr) minmax(4rem, 0.8fr) minmax(4rem, 0.8fr)">
            <span>{t('dashboard.col_agent') ?? 'Agent'}</span>
            <span>{t('dashboard.col_project') ?? 'Project'}</span>
            <span>v</span>
            <span>{t('dashboard.col_last_eval') ?? 'Last evaluation'}</span>
            <span>{t('dashboard.col_pass_rate') ?? 'pass rate'}</span>
            <span title={t('dashboard.pending_hint') ?? undefined}>⚡</span>
            <span title={t('dashboard.stale_hint') ?? undefined}>⌛</span>
          </ColHead>
          {quality.agents.map((a: QualitySummaryAgent) => (
            <div key={a.agent_id} style={{ ...tableGrid("minmax(9rem, 1.8fr) minmax(6rem, 1fr) minmax(3rem, 0.6fr) minmax(9rem, 1.6fr) minmax(4.5rem, 1fr) minmax(4rem, 0.8fr) minmax(4rem, 0.8fr)"), fontSize: '0.8rem', padding: '0.35rem 0.6rem', borderTop: '1px solid var(--tm-border)', alignItems: 'center' }}>
              <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                <button onClick={() => navigate('/agent-config')} style={{ background: 'none', border: 'none', padding: 0, color: 'var(--tm-text)', cursor: 'pointer', font: 'inherit', textDecoration: 'underline dotted' }} title={a.agent_id}>
                  {a.name || a.agent_id}
                </button>
              </span>
              <span style={{ color: 'var(--tm-text-3)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{a.project || '—'}</span>
              <span style={{ color: 'var(--tm-text-3)' }}>{a.definition_version || '—'}</span>
              {a.last_evaluation ? (
                <span style={{ whiteSpace: 'nowrap' }}>
                  <Tag
                    size="sm"
                    style={{ background: 'transparent', border: `1px solid ${verdictColor(a.last_evaluation.verdict)}`, color: verdictColor(a.last_evaluation.verdict) }}
                  >
                    {t(`dashboard.verdict_${a.last_evaluation.verdict}`) !== `dashboard.verdict_${a.last_evaluation.verdict}` ? t(`dashboard.verdict_${a.last_evaluation.verdict}`) : a.last_evaluation.verdict}
                  </Tag>
                  <span style={{ color: 'var(--tm-text-3)', marginLeft: '0.4rem', fontSize: '0.72rem' }}>
                    {new Date(a.last_evaluation.at).toLocaleDateString()}
                    {a.last_evaluation.judge_agreement != null && ` · 🧑‍⚖️ ${(a.last_evaluation.judge_agreement * 100).toFixed(0)}%`}
                  </span>
                </span>
              ) : (
                <span style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem' }}>{t('dashboard.no_eval') ?? 'no runs'}</span>
              )}
              <span>{a.last_evaluation ? `${(a.last_evaluation.pass_rate * 100).toFixed(0)}%` : '—'}</span>
              <span style={{ color: a.pending_proposals > 0 ? 'var(--tm-info)' : 'var(--tm-text-3)' }}>{a.pending_proposals > 0 ? a.pending_proposals : '—'}</span>
              <span style={{ color: a.stale_goldens > 0 ? 'var(--tm-warning)' : 'var(--tm-text-3)' }}>{a.stale_goldens > 0 ? a.stale_goldens : '—'}</span>
            </div>
          ))}
        </div>
      ) : !busy && (
        <Tile style={{ color: 'var(--tm-text-3)' }}>{t('dashboard.no_agents') ?? 'No agents visible'}</Tile>
      )}
    </>
  )
}
