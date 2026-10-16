import { Fragment, useCallback, useEffect, useMemo, useRef, useState, type CSSProperties, type ReactNode } from 'react'
import { Heading, InlineNotification, Tag, Tile } from '@carbon/react'
import { observationApi, type HealthStats, type HealthModelStats, type ObservationEvent } from './observationApi'
import {
  projectsOverview,
  qualitySummary,
  operations,
  storageOverview,
  systemHealth,
  type ProjectsOverview,
  type QualitySummary,
  type QualitySummaryAgent,
  type StorageSnapshot,
  type SystemHealth,
  type SandboxContainerInfo,
} from './kernelApi'
import ProjectOverview from './ProjectOverview'
import Observability from './Observability'
import { useI18n, pluralT } from './i18n'
import { formatDuration } from './eventSummary'
import { sandboxLoad, maxSandboxNet, sandboxDiameter, sandboxColor, topSandboxes, sandboxShortName } from './sandboxWidgets'

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
  const [storage_, setStorage] = useState<StorageSnapshot | null>(null)
  const [syshealth, setSyshealth] = useState<SystemHealth | null>(null)
  // Run id → title from run.started events: the sandbox digest shows the human
  // name of the run, not the opaque id. Best-effort — failures leave ids.
  const [runTitles, setRunTitles] = useState<Map<string, string>>(new Map())
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
      const [stats, projects, ops, storageSnap, syshealthSnap, startedPage] = await Promise.all([
        observationApi.healthStats(undefined, since),
        projectsOverview(window_.go),
        operations().catch(() => null),
        storageOverview().catch(() => null),
        systemHealth().catch(() => null),
        observationApi.events(undefined, undefined, undefined, undefined, { type: 'run.started', limit: 200 }).catch(() => ({ events: [] as ObservationEvent[], count: 0 })),
      ])
      setHealth(stats)
      setOverview(projects)
      setOpsCount(ops ? ops.count : null)
      setStorage(storageSnap)
      setSyshealth(syshealthSnap)
      const titles = new Map<string, string>()
      for (const event of startedPage.events) {
        const run = event.context?.run ?? ''
        const title = event.data?.title
        if (run && typeof title === 'string' && title.trim()) titles.set(run, title.trim())
      }
      setRunTitles(titles)
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

  // Top-5 storage consumers (docs/sandbox-storage.md §7.1): the dashboard
  // summary, the full table lands with the phase-2 storage page.
  const storageTop = useMemo(() => {
    const rows = [...(storage_?.projects ?? [])]
    rows.sort((a, b) => (b.workspace_bytes + b.cache_bytes) - (a.workspace_bytes + a.cache_bytes))
    return rows.slice(0, 5)
  }, [storage_])

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

          {/* Storage + Server health side by side (docs/sandbox-storage.md §5.4, §7.1) */}
          {(storage_ || syshealth) && (
          <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(360px, 1fr))', gap: '1rem', alignItems: 'start', marginBottom: '1rem' }}>
            {storage_ && (
              <section style={{ minWidth: 0 }}>
                <PanelHeader title={t('dashboard.storage') ?? 'Storage'} hint={t('dashboard.storage_hint') ?? undefined} />
                <div style={{ border: '1px solid var(--tm-border)', borderRadius: '6px', padding: '0.6rem 0.75rem', fontSize: '0.78rem' }}>
                <div style={{ display: 'flex', justifyContent: 'space-between', gap: '0.75rem', flexWrap: 'wrap', marginBottom: '0.45rem' }}>
                  <span title={storage_.root} style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', maxWidth: '20rem', color: 'var(--tm-text-3)' }}>{storage_.root}</span>
                  <span>
                    <strong>{fmtBytes(storage_.disk.total_bytes - storage_.disk.free_bytes)}</strong> / {fmtBytes(storage_.disk.total_bytes)}
                    <span style={{ color: 'var(--tm-text-3)' }}> · {t('dashboard.storage_free') ?? 'free'} {fmtBytes(storage_.disk.free_bytes)}</span>
                  </span>
                </div>
                <div style={meterTrackStyle}>
                  <div style={{ ...meterFillStyle, width: meterWidth(storage_.disk.total_bytes > 0 ? (storage_.disk.total_bytes - storage_.disk.free_bytes) / storage_.disk.total_bytes : 0), background: meterColor(storage_.disk.total_bytes > 0 ? (storage_.disk.total_bytes - storage_.disk.free_bytes) / storage_.disk.total_bytes : 0) }} />
                </div>
                {storage_.truncated && (
                  <div style={{ color: 'var(--tm-warning)', margin: '0.4rem 0' }}>⚠ {t('dashboard.storage_truncated') ?? 'walk incomplete — numbers are partial'}</div>
                )}
                {storageTop.length > 0 ? (
                  <div style={{ marginTop: '0.5rem' }}>
                    <div style={{ display: 'grid', gridTemplateColumns: 'minmax(6rem, 1.3fr) minmax(5rem, 1fr) minmax(4.5rem, 0.7fr)', gap: '0.6rem', fontSize: '0.66rem', textTransform: 'uppercase', letterSpacing: '0.05em', color: 'var(--tm-text-3)', padding: '0.2rem 0' }}>
                      <span>{t('dashboard.col_project') ?? 'Project'}</span>
                      <span title={t('dashboard.storage_quota_hint') ?? undefined}>{t('dashboard.storage_quota') ?? 'quota'}</span>
                      <span style={{ textAlign: 'right' }}>{t('dashboard.storage_occupied') ?? 'used'}</span>
                    </div>
                    {storageTop.map((row) => {
                      const used = row.workspace_bytes + row.cache_bytes
                      const hint = [`${t('dashboard.storage_workspace') ?? 'workspace'}: ${fmtBytes(row.workspace_bytes)} (${row.workspace_files.toLocaleString()})`, `${t('dashboard.storage_cache') ?? 'cache'}: ${fmtBytes(row.cache_bytes)}`, ...row.top_paths.map((p) => `${p.path}: ${fmtBytes(p.bytes)}`)].join('\n')
                      return (
                        <div key={row.project} title={hint} style={{ display: 'grid', gridTemplateColumns: 'minmax(6rem, 1.3fr) minmax(5rem, 1fr) minmax(4.5rem, 0.7fr)', gap: '0.6rem', alignItems: 'center', padding: '0.25rem 0', borderTop: '1px solid var(--tm-border)' }}>
                          <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', color: row.project === project ? 'var(--tm-teal)' : 'var(--tm-text)' }}>{row.project}</span>
                          <div style={meterTrackStyle}>
                            <div style={{ ...meterFillStyle, width: meterWidth(row.quota_used), background: meterColor(row.quota_used) }} />
                          </div>
                          <span style={{ color: 'var(--tm-text-3)', whiteSpace: 'nowrap', textAlign: 'right' }}>{fmtBytes(used)}</span>
                        </div>
                      )
                    })}
                  </div>
                ) : (
                  <div style={{ color: 'var(--tm-text-3)', marginTop: '0.5rem' }}>{t('dashboard.storage_empty') ?? 'No project storage yet'}</div>
                )}
              </div>
              </section>
            )}
            {syshealth && (
              <section style={{ minWidth: 0 }}>
                <PanelHeader title={t('dashboard.server') ?? 'Server'} hint={t('dashboard.server_hint') ?? undefined} />
                <div style={{ border: '1px solid var(--tm-border)', borderRadius: '6px', padding: '0.6rem 0.75rem', fontSize: '0.78rem', display: 'grid', gridTemplateColumns: 'minmax(7rem, 0.8fr) minmax(9rem, 1.4fr)', rowGap: '0.45rem', alignItems: 'center' }}>
                <span style={{ color: 'var(--tm-text-3)' }}>{t('dashboard.server_load') ?? 'load 1/5m'}</span>
                <span style={{ color: syshealth.cpu.cores > 0 && syshealth.cpu.load1 > syshealth.cpu.cores ? 'var(--tm-danger)' : 'var(--tm-text)' }}>
                  {syshealth.cpu.load1.toFixed(2)} / {syshealth.cpu.load5.toFixed(2)}
                  <span style={{ color: 'var(--tm-text-3)' }}> · {syshealth.cpu.cores} vCPU</span>
                </span>
                <span style={{ color: 'var(--tm-text-3)' }}>{t('dashboard.server_memory') ?? 'Memory'}</span>
                {syshealth.memory.total_bytes > 0 ? (() => {
                  const used = syshealth.memory.total_bytes - syshealth.memory.available_bytes
                  const ratio = used / syshealth.memory.total_bytes
                  return (
                    <span>
                      <span style={{ display: 'flex', justifyContent: 'space-between', gap: '0.5rem' }}>
                        <span>{fmtBytes(used)} / {fmtBytes(syshealth.memory.total_bytes)}</span>
                        <span style={{ color: 'var(--tm-text-3)' }}>{t('dashboard.storage_free') ?? 'free'} {fmtBytes(syshealth.memory.available_bytes)}</span>
                      </span>
                      <span style={{ ...meterTrackStyle, display: 'block', marginTop: '0.2rem' }}>
                        <span style={{ ...meterFillStyle, display: 'block', width: meterWidth(ratio), background: meterColor(ratio) }} />
                      </span>
                    </span>
                  )
                })() : <span style={{ color: 'var(--tm-text-3)' }}>—</span>}
                {syshealth.disk.map((d) => (
                  <Fragment key={d.path}>
                    <span title={d.path} style={{ color: 'var(--tm-text-3)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{t('dashboard.server_disk') ?? 'disk'} {d.path}</span>
                    <span style={{ color: d.total_bytes > 0 && d.free_bytes / d.total_bytes < 0.1 ? 'var(--tm-danger)' : 'var(--tm-text)' }}>
                      {fmtBytes(d.free_bytes)}
                      <span style={{ color: 'var(--tm-text-3)' }}> / {fmtBytes(d.total_bytes)}</span>
                    </span>
                  </Fragment>
                ))}
                <span style={{ color: 'var(--tm-text-3)' }}>docker</span>
                {syshealth.docker.available ? (
                  <span>
                    {fmtBytes(syshealth.docker.images_bytes)}
                    <span style={{ color: 'var(--tm-text-3)' }}> · {pluralT(t, locale, 'dashboard.containers_n', syshealth.docker.containers_running)}</span>
                  </span>
                ) : (
                  <span style={{ color: 'var(--tm-text-3)' }}>{t('dashboard.server_no_docker') ?? 'docker unreachable'}</span>
                )}
              </div>
              </section>
            )}
          </div>
          )}

          {/* Sandbox bubbles: one ball per active sandbox container, size and
              color = integral load (CPU + memory + network). Hover shows the
              digest (who initiated, which agent, which run), click opens the
              run. Only the top-N load hogs get balls; the rest fold into +N. */}
          {syshealth && syshealth.docker.available && (syshealth.sandboxes?.length ?? 0) > 0 && (
            <SandboxBubbles
              containers={syshealth.sandboxes ?? []}
              runTitles={runTitles}
              onOpenRun={(run, sandboxProject) => navigate(`/agents?project=${encodeURIComponent(sandboxProject)}&run=${encodeURIComponent(run)}`)}
            />
          )}

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
                  <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={`${m.model}${m.provider && m.provider !== m.model ? ` · ${m.provider}` : ''}`}>
                    {m.model}
                    {m.provider && m.provider !== m.model && <span style={{ color: 'var(--tm-text-3)' }}> · {m.provider}</span>}
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

/** Storage-widget byte sizes: binary units up to TiB, matching quota units. */
function fmtBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return '—'
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB']
  let value = bytes
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) { value /= 1024; unit++ }
  return `${unit === 0 || value >= 100 ? Math.round(value) : value.toFixed(1)} ${units[unit]}`
}

/** Meter colors follow the health-strip thresholds: 80% warns, 100% alerts. */
function meterColor(ratio: number): string {
  if (ratio >= 1) return 'var(--tm-danger)'
  if (ratio >= 0.8) return 'var(--tm-warning)'
  return 'var(--tm-teal)'
}

function meterWidth(ratio: number): string {
  if (!Number.isFinite(ratio) || ratio <= 0) return '0%'
  return `${Math.min(100, ratio * 100).toFixed(1)}%`
}

const meterTrackStyle: CSSProperties = { height: '6px', borderRadius: '3px', background: 'var(--tm-border)', overflow: 'hidden' }
const meterFillStyle: CSSProperties = { height: '100%', borderRadius: '3px' }

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

/** Sandbox bubble cloud (dashboard): one ball per active sandbox container,
 *  size and color encode the integral resource load so the run hogging the
 *  host is visible at a glance. Hovering a ball swaps in a digest strip with
 *  the run identity; clicking it opens the run. */
function SandboxBubbles({ containers, runTitles, onOpenRun }: {
  containers: SandboxContainerInfo[]
  runTitles: Map<string, string>
  onOpenRun: (run: string, project: string) => void
}) {
  const { t } = useI18n()
  const [hovered, setHovered] = useState<string | null>(null)
  const { top, rest } = useMemo(() => topSandboxes(containers), [containers])
  const maxNet = useMemo(() => maxSandboxNet(containers), [containers])
  const active = top.find((c) => c.name === hovered) ?? null
  const activeRun = active?.labels?.['temporality.run'] ?? ''
  const activeProject = active?.labels?.['temporality.project'] ?? ''
  const activeTitle = activeRun ? (runTitles.get(activeRun) ?? activeRun) : activeRun

  const digestRow = (label: string, value: ReactNode) => (
    <span style={{ display: 'inline-flex', gap: '0.3rem', whiteSpace: 'nowrap' }}>
      <span style={{ color: 'var(--tm-text-3)' }}>{label}</span>
      <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', maxWidth: '16rem' }}>{value || '—'}</span>
    </span>
  )

  return (
    <>
      <PanelHeader title={t('dashboard.sandboxes') ?? 'Sandboxes'} hint={t('dashboard.sandboxes_hint') ?? undefined} />
      <div style={{ border: '1px solid var(--tm-border)', borderRadius: '6px', padding: '0.75rem', marginBottom: '1rem' }}>
        <div style={{ display: 'flex', alignItems: 'flex-end', gap: '0.9rem', flexWrap: 'wrap' }}>
          {top.map((container) => {
            const score = sandboxLoad(container, maxNet)
            const size = sandboxDiameter(score)
            const run = container.labels?.['temporality.run'] ?? ''
            const project = container.labels?.['temporality.project'] ?? ''
            const agent = container.labels?.['temporality.agent'] ?? ''
            const digest = [
              `${t('dashboard.sandboxes_agent') ?? 'Agent'}: ${agent || '—'}`,
              `${t('dashboard.sandboxes_actor') ?? 'Initiator'}: ${container.labels?.['temporality.actor'] || '—'}`,
              `${t('dashboard.sandboxes_run') ?? 'Run'}: ${runTitles.get(run) || run || container.name}`,
              `CPU ${container.cpu_percent.toFixed(0)}% · ${t('dashboard.server_memory') ?? 'Memory'} ${fmtBytes(container.mem_used_bytes)}/${fmtBytes(container.mem_limit_bytes)} · ${t('dashboard.sandboxes_project') ?? 'Project'}: ${project || '—'}`,
            ].join('\n')
            return (
              <button
                key={container.name}
                onClick={() => run && project && onOpenRun(run, project)}
                onMouseEnter={() => setHovered(container.name)}
                onMouseLeave={() => setHovered((name) => (name === container.name ? null : name))}
                title={digest}
                aria-label={digest}
                style={{
                  width: size, height: size, borderRadius: '50%', cursor: run && project ? 'pointer' : 'default',
                  border: `2px solid ${sandboxColor(score)}`,
                  background: `color-mix(in srgb, ${sandboxColor(score)} 18%, transparent)`,
                  color: 'var(--tm-text)', display: 'flex', alignItems: 'center', justifyContent: 'center',
                  padding: 0, fontSize: '0.6rem', fontWeight: 600, overflow: 'hidden', flexShrink: 0,
                }}
              >
                <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', padding: '0 0.3rem' }}>
                  {size >= 44 && agent ? agent : ''}
                </span>
              </button>
            )
          })}
          {rest.length > 0 && (
            <span
              title={`${rest.length} · ${rest.map((c) => c.labels?.['temporality.agent'] || sandboxShortName(c.name)).join(', ')}`}
              style={{ border: '1px dashed var(--tm-border)', borderRadius: '999px', padding: '0.3rem 0.7rem', color: 'var(--tm-text-3)', fontSize: '0.72rem' }}
            >
              {(t('dashboard.sandboxes_more', { n: String(rest.length) }) ?? `+${rest.length} more`)}
            </span>
          )}
        </div>

        {/* Digest strip for the hovered bubble — also the no-hover placeholder
            keeps the row height stable. */}
        <div style={{ borderTop: '1px solid var(--tm-border)', marginTop: '0.7rem', paddingTop: '0.5rem', fontSize: '0.72rem', display: 'flex', gap: '1rem', flexWrap: 'wrap', alignItems: 'center', minHeight: '1.6rem' }}>
          {active ? (
            <>
              {digestRow(t('dashboard.sandboxes_agent') ?? 'Agent', active.labels?.['temporality.agent'])}
              {digestRow(t('dashboard.sandboxes_actor') ?? 'Initiator', active.labels?.['temporality.actor'])}
              {digestRow(t('dashboard.sandboxes_run') ?? 'Run', activeTitle)}
              {digestRow(t('dashboard.sandboxes_project') ?? 'Project', activeProject)}
              {digestRow('CPU', `${active.cpu_percent.toFixed(0)}% · ${fmtBytes(active.mem_used_bytes)}/${fmtBytes(active.mem_limit_bytes)} · ↓${fmtBytes(active.net_in_bytes)} ↑${fmtBytes(active.net_out_bytes)}`)}
              {activeRun && activeProject && (
                <a href={`/agents?project=${encodeURIComponent(activeProject)}&run=${encodeURIComponent(activeRun)}`} style={{ color: 'var(--tm-teal)', whiteSpace: 'nowrap' }}>
                  {t('dashboard.sandboxes_open_run') ?? 'Open run'} →
                </a>
              )}
            </>
          ) : (
            <span style={{ color: 'var(--tm-text-3)' }}>{t('dashboard.sandboxes_hover') ?? 'Hover a bubble for the run digest'}</span>
          )}
        </div>
      </div>
    </>
  )
}
