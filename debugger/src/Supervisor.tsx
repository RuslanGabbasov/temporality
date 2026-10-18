import { useEffect, useState, useCallback } from 'react'
import { Button, Tag } from '@carbon/react'
import PageLoading from './PageLoading'
import { observationApi, type ObservationEvent } from './observationApi'
import { workspaceApi } from './workspaceApi'
import { useT } from './i18n'

const navigate = (path: string) => { window.history.pushState(null, '', path); window.dispatchEvent(new PopStateEvent('popstate')) }

// Supervisor page (docs/supervisor-model.md §3): the installation's problem
// queue. Verdicts (wave A1) collapse into clusters of same-root failures
// (wave A2, keyed by cluster_key); insights (wave A3) explain health
// anomalies in plain language above them. All content is journaled — the
// page is a read-only view over supervisor.* events.

const CLUSTER_WINDOW_MS = 7 * 24 * 60 * 60 * 1000

function message(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }

interface ClusterRow {
  key: string
  cause: string
  pattern: string
  caseID: string
  subject: string
  summary: string
  recommendation: string
  project: string
  count: number
  runs: { run: string; at: string }[]
  eventIDs: string[]
  lastAt: string
  // knowledge_conflict case payload: the pair to compare and act on.
  conflict?: { newID: string; newProp: string; newProject: string; oldID: string; oldProp: string; oldProject: string; score: number }
}

// causeTag picks the tag color for a cause class.
function causeTag(cause: string): 'red' | 'warm-gray' | 'blue' {
  if (cause === 'harness_bug' || cause === 'model_error') return 'red'
  if (cause === 'agent_prompt' || cause === 'user_error') return 'warm-gray'
  return 'blue'
}

function clusterVerdicts(events: ObservationEvent[]): ClusterRow[] {
  const byKey = new Map<string, ClusterRow>()
  const loose: ClusterRow[] = []
  for (const event of events) {
    const run = String(event.data?.run_id ?? '')
    const cause = String(event.data?.cause_class ?? 'unknown')
    const key = String(event.data?.cluster_key ?? '')
    const row: ClusterRow = {
      key: key || `solo:${event.event_id}`,
      cause,
      pattern: String(event.data?.pattern ?? ''),
      caseID: String(event.data?.case ?? ''),
      subject: String(event.data?.subject ?? ''),
      summary: String(event.data?.summary ?? ''),
      recommendation: String(event.data?.recommendation ?? ''),
      project: event.context?.project ?? '',
      count: 1,
      runs: run ? [{ run, at: event.occurred_at }] : [],
      eventIDs: [event.event_id],
      lastAt: event.occurred_at,
    }
    if (event.data?.case === 'knowledge_conflict' && event.data?.knowledge_id && event.data?.neighbor_id) {
      row.conflict = {
        newID: String(event.data.knowledge_id),
        newProp: String(event.data.new_proposition ?? ''),
        newProject: String(event.data.new_project ?? event.context?.project ?? ''),
        oldID: String(event.data.neighbor_id),
        oldProp: String(event.data.old_proposition ?? ''),
        oldProject: String(event.data.old_project ?? event.context?.project ?? ''),
        score: Number(event.data.score ?? 0),
      }
    }
    if (!key) {
      // Legacy verdicts without a cluster key stay standalone rows.
      loose.push(row)
      continue
    }
    const existing = byKey.get(key)
    if (!existing) {
      byKey.set(key, row)
      continue
    }
    existing.count++
    existing.runs.push(...row.runs)
    existing.eventIDs.push(...row.eventIDs)
    if (event.occurred_at > existing.lastAt) {
      existing.lastAt = event.occurred_at
      // The newest verdict speaks for the cluster.
      existing.summary = row.summary
      existing.recommendation = row.recommendation
      existing.pattern = row.pattern || existing.pattern
      existing.project = row.project || existing.project
      if (row.conflict) existing.conflict = row.conflict
    }
  }
  const clustered = [...byKey.values()]
  clustered.sort((a, b) => (b.count - a.count) || (a.lastAt < b.lastAt ? 1 : -1))
  return [...clustered, ...loose.sort((a, b) => (a.lastAt < b.lastAt ? 1 : -1))]
}

// SupervisorPanels is the whole supervisor surface without the page chrome —
// the /supervisor page and the dashboard's supervisor tab render the same
// panels (docs/supervisor-model.md §7).
export function SupervisorPanels() {
  const t = useT()
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [verdicts, setVerdicts] = useState<ObservationEvent[]>([])
  const [insights, setInsights] = useState<ObservationEvent[]>([])
  const [expanded, setExpanded] = useState<string | null>(null)
  // Queue filters (docs/supervisor-model.md §7): case family, project and
  // acknowledged visibility — the queue becomes navigable once waves B/C
  // produce many rows.
  const [caseFilter, setCaseFilter] = useState('')
  const [projectFilter, setProjectFilter] = useState('')
  const [showResolved, setShowResolved] = useState(false)
  const [acknowledged, setAcknowledged] = useState<Set<string>>(new Set())
  const [busyAck, setBusyAck] = useState('')
  // Semantic-layer health (§6.5): read-only monitoring. Configuration lives
  // in Agents → quality settings.
  const [embeddings, setEmbeddings] = useState<{ model: string; ready: boolean; error?: string } | null>(null)
  const [tiers, setTiers] = useState<{ tier: string; helpful: number; harmful: number; helpful_rate_pct: number }[] | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    setError('')
    try {
      const [verdictPage, insightPage, ackPage, settings, effect] = await Promise.all([
        observationApi.events(undefined, undefined, undefined, undefined, { type: 'supervisor.verdict', limit: 300 }),
        observationApi.events(undefined, undefined, undefined, undefined, { type: 'supervisor.insight', limit: 50 }),
        observationApi.events(undefined, undefined, undefined, undefined, { type: 'supervisor.verdict.acknowledged', limit: 300 }),
        workspaceApi.getSupervisorSettings().catch(() => null),
        workspaceApi.hintEffectiveness().catch(() => null),
      ])
      setVerdicts(verdictPage.events ?? [])
      setInsights(insightPage.events ?? [])
      const ackSet = new Set<string>()
      for (const event of ackPage.events ?? []) {
        const id = String(event.data?.verdict_event_id ?? '')
        if (id) ackSet.add(id)
      }
      setAcknowledged(ackSet)
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      setEmbeddings((settings as any)?.embeddings ?? null)
      setTiers(effect?.tiers ?? [])
    } catch (failure) {
      setError(message(failure))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => { void load() }, [load])

  const acknowledge = async (eventID: string) => {
    setBusyAck(eventID)
    try {
      await workspaceApi.acknowledgeSupervisorVerdict(eventID)
      setAcknowledged((prev) => new Set(prev).add(eventID))
    } catch (failure) {
      setError(message(failure))
    } finally {
      setBusyAck('')
    }
  }

  // One-click resolutions for a knowledge_conflict verdict: invalidate the
  // losing knowledge through the knowledge API, then acknowledge the verdict
  // (it leaves the queue). The keep-both path only acknowledges.
  const [busyCase, setBusyCase] = useState('')
  const resolveConflict = async (row: ClusterRow, kind: 'invalidate_new' | 'invalidate_old' | 'keep_both') => {
    const conflict = row.conflict!
    setBusyCase(row.key)
    try {
      if (kind !== 'keep_both') {
        const target = kind === 'invalidate_new'
          ? { id: conflict.newID, project: conflict.newProject }
          : { id: conflict.oldID, project: conflict.oldProject }
        await observationApi.invalidate({
          knowledge_id: target.id,
          project: target.project,
          actor: { id: 'supervisor-queue', type: 'human' },
          reason: t('supervisor.action_invalidated_reason', { score: (conflict.score || 0).toFixed(2) }),
        })
      }
      for (const eventID of row.eventIDs) {
        try { await workspaceApi.acknowledgeSupervisorVerdict(eventID) } catch { /* queue hygiene is best-effort */ }
      }
      await load()
    } catch (failure) {
      setError(message(failure))
    } finally {
      setBusyCase('')
    }
  }

  const clusters = clusterVerdicts(verdicts)
  const projectOptions = [...new Set(clusters.map((row) => row.project).filter(Boolean))]
  const caseOptions = [...new Set(clusters.map((row) => row.caseID).filter(Boolean))]
  const resolved = (row: ClusterRow) => row.eventIDs.every((id) => acknowledged.has(id))
  const visibleClusters = clusters.filter((row) => {
    if (caseFilter && row.caseID !== caseFilter) return false
    if (projectFilter && row.project !== projectFilter) return false
    if (!showResolved && resolved(row)) return false
    return true
  })

  return (
    <>
      {error && <p style={{ color: 'var(--tm-danger)', fontSize: '0.8rem' }}>{error}</p>}

      {loading ? <PageLoading /> : (
        <>
          {/* Monitoring only (§6.5): semantic-layer health and hint-tier
              effectiveness. Configuration (models, HAB coefficients) lives in
              Agents → quality settings — a monitoring tab must not be a
              second editing surface for the same values. */}
          <div style={{ border: '1px solid var(--tm-border)', borderRadius: '6px', padding: '0.6rem 0.75rem', fontSize: '0.78rem', marginBottom: '1.25rem' }}>
            <div style={{ fontWeight: 600, marginBottom: '0.35rem' }}>{t('supervisor.semantic_title') ?? 'Semantic layer'}</div>
              {embeddings && (
                <div style={{ marginBottom: '0.4rem' }}>
                  <Tag type={embeddings.ready ? 'green' : 'red'} size="sm">{embeddings.ready ? t('supervisor.semantic_ready') ?? 'ready' : t('supervisor.semantic_degraded') ?? 'degraded'}</Tag>{' '}
                  <span style={{ color: 'var(--tm-text-3)', fontSize: '0.72rem' }}>{embeddings.model}{embeddings.error ? ` — ${embeddings.error}` : ''}</span>
                </div>
              )}
              {tiers && tiers.length > 0 ? (
                <div style={{ display: 'grid', gap: '0.15rem' }}>
                  <div style={{ display: 'grid', gridTemplateColumns: '1fr repeat(3, minmax(3.5rem, auto))', gap: '0.4rem', fontSize: '0.68rem', color: 'var(--tm-text-3)', textTransform: 'uppercase', letterSpacing: '0.04em' }}>
                    <span>{t('supervisor.tier') ?? 'tier'}</span>
                    <span>👍</span>
                    <span>👎</span>
                    <span>%</span>
                  </div>
                  {tiers.map((row) => (
                    <div key={row.tier} style={{ display: 'grid', gridTemplateColumns: '1fr repeat(3, minmax(3.5rem, auto))', gap: '0.4rem' }}>
                      <span style={{ color: row.tier === 'semantic' ? 'var(--tm-teal)' : 'var(--tm-text-2)' }}>{t(`supervisor.tier_${row.tier}`) ?? row.tier}</span>
                      <span>{row.helpful}</span>
                      <span>{row.harmful}</span>
                      <span>{row.helpful_rate_pct}</span>
                    </div>
                  ))}
                </div>
              ) : (
                <p style={{ color: 'var(--tm-text-3)', fontSize: '0.72rem', margin: 0 }}>{t('supervisor.tiers_empty') ?? 'No hint outcomes in the window yet.'}</p>
              )}
          </div>

          {/* Wave A3: health anomaly explanations, newest first. */}
          <section style={{ marginBottom: '1.5rem' }}>
            <h3 style={{ fontSize: '0.85rem', margin: '0 0 0.4rem' }}>{t('supervisor.insights_title')}</h3>
            {insights.length === 0 ? (
              <p style={{ color: 'var(--tm-text-3)', fontSize: '0.78rem', margin: 0 }}>{t('supervisor.insights_empty')}</p>
            ) : (
              <div style={{ border: '1px solid var(--tm-border)', borderRadius: '6px', overflow: 'hidden' }}>
                {insights.map((event) => {
                  const severity = String(event.data?.severity ?? 'info')
                  const title = String(event.data?.title ?? '')
                  const explanation = String(event.data?.explanation ?? '')
                  const recommendation = String(event.data?.recommendation ?? '')
                  const current = String(event.data?.current ?? '')
                  const baseline = String(event.data?.baseline ?? '')
                  return (
                    <div key={event.event_id} style={{ padding: '0.55rem 0.75rem', borderTop: '1px solid var(--tm-border)', fontSize: '0.8rem' }}>
                      <div style={{ display: 'flex', alignItems: 'center', gap: '0.45rem', flexWrap: 'wrap' }}>
                        <Tag type={severity === 'warning' ? 'red' : 'blue'} size="sm">{severity === 'warning' ? t('supervisor.severity_warning') : t('supervisor.severity_info')}</Tag>
                        <strong>{title}</strong>
                        <span style={{ marginLeft: 'auto', fontSize: '0.68rem', color: 'var(--tm-text-3)', flexShrink: 0 }}>{new Date(event.occurred_at).toLocaleString()}</span>
                      </div>
                      <div style={{ marginTop: '0.3rem', lineHeight: 1.5 }}>{explanation}</div>
                      {(current || baseline) && (
                        <div style={{ marginTop: '0.2rem', fontSize: '0.7rem', color: 'var(--tm-text-3)' }}>
                          {current}{baseline ? ` · ${t('supervisor.vs_baseline')} ${baseline}` : ''}
                        </div>
                      )}
                      {recommendation && (
                        <div style={{ marginTop: '0.2rem', fontSize: '0.72rem', color: 'var(--tm-text-2)' }}>
                          <span style={{ color: 'var(--tm-amber)' }}>→</span> {recommendation}
                        </div>
                      )}
                    </div>
                  )
                })}
              </div>
            )}
          </section>

          {/* Waves A1+A2: triage verdicts collapsed into same-root clusters. */}
          <section>
            <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', flexWrap: 'wrap', marginBottom: '0.4rem' }}>
              <h3 style={{ fontSize: '0.85rem', margin: 0 }}>{t('supervisor.problems_title')}</h3>
              <label className="toolbar-select">
                {t('supervisor.filter_case') ?? 'Case'}
                <select value={caseFilter} onChange={(e) => setCaseFilter(e.target.value)}>
                  <option value="">{t('supervisor.filter_all_cases') ?? 'All cases'}</option>
                  {caseOptions.map((caseID) => <option key={caseID} value={caseID}>{t(`supervisor.case_${caseID}`) ?? caseID}</option>)}
                </select>
              </label>
              {projectOptions.length > 0 && (
                <label className="toolbar-select">
                  {t('supervisor.filter_project') ?? 'Project'}
                  <select value={projectFilter} onChange={(e) => setProjectFilter(e.target.value)}>
                    <option value="">{t('supervisor.filter_all_projects') ?? 'All projects'}</option>
                    {projectOptions.map((p) => <option key={p} value={p}>{p}</option>)}
                  </select>
                </label>
              )}
              <label style={{ fontSize: '0.74rem', color: 'var(--tm-text-3)', display: 'flex', alignItems: 'center', gap: '0.3rem', cursor: 'pointer' }}>
                <input type="checkbox" checked={showResolved} onChange={(e) => setShowResolved(e.target.checked)} />
                {t('supervisor.show_resolved') ?? 'Show resolved'}
              </label>
            </div>
            {visibleClusters.length === 0 ? (
              <p style={{ color: 'var(--tm-text-3)', fontSize: '0.78rem', margin: 0 }}>{t('supervisor.problems_empty')}</p>
            ) : (
              <div style={{ border: '1px solid var(--tm-border)', borderRadius: '6px', overflow: 'hidden' }}>
                {visibleClusters.map((row) => {
                  const open = expanded === row.key
                  const isResolved = resolved(row)
                  return (
                    <div key={row.key} style={{ padding: '0.55rem 0.75rem', borderTop: '1px solid var(--tm-border)', fontSize: '0.78rem', opacity: isResolved ? 0.55 : 1 }}>
                      <div style={{ display: 'flex', alignItems: 'center', gap: '0.45rem', flexWrap: 'wrap' }}>
                        <Tag type={causeTag(row.cause)} size="sm">{row.cause}</Tag>
                        {row.pattern && <span style={{ fontSize: '0.74rem', color: 'var(--tm-text-2)' }}>{row.pattern}</span>}
                        {row.caseID && <Tag type="purple" size="sm">{t(`supervisor.case_${row.caseID}`) ?? row.caseID}</Tag>}
                        {row.project && <span style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)' }}>{row.project}</span>}
                        {row.count > 1 && <Tag type="cool-gray" size="sm">×{row.count}</Tag>}
                        {isResolved && <Tag type="green" size="sm">{t('supervisor.resolved') ?? 'resolved'}</Tag>}
                        <span style={{ marginLeft: 'auto', fontSize: '0.68rem', color: 'var(--tm-text-3)', flexShrink: 0 }}>{new Date(row.lastAt).toLocaleString()}</span>
                      </div>
                      <div style={{ marginTop: '0.3rem', lineHeight: 1.5 }}>{row.summary}</div>
                      {row.recommendation && (
                        <div style={{ marginTop: '0.2rem', fontSize: '0.72rem', color: 'var(--tm-text-2)' }}>
                          <span style={{ color: 'var(--tm-amber)' }}>→</span> {row.recommendation}
                        </div>
                      )}
                      {row.conflict && (() => {
                      const cf = row.conflict
                      const openDossier = (id: string, project: string) => (e: React.MouseEvent) => {
                        e.preventDefault()
                        navigate(`/knowledge?project=${encodeURIComponent(project)}&dossier=${encodeURIComponent(id)}`)
                      }
                      return (
                      <div style={{ marginTop: '0.4rem', border: '1px solid var(--tm-border)', borderRadius: '4px', padding: '0.4rem 0.6rem', fontSize: '0.75rem' }}>
                          <div style={{ display: 'grid', gap: '0.25rem' }}>
                            <div>
                              <Tag type="blue" size="sm">новое</Tag>{' '}
                              <a
                                href={`/knowledge?project=${encodeURIComponent(cf.newProject || row.project)}&dossier=${encodeURIComponent(cf.newID)}`}
                                onClick={openDossier(cf.newID, cf.newProject || row.project)}
                                style={{ color: 'var(--tm-teal)', fontSize: '0.72rem' }}
                              >{cf.newID}</a>
                              <div style={{ color: 'var(--tm-text-2)', marginTop: '0.1rem' }}>{cf.newProp || '—'}</div>
                            </div>
                            <div style={{ color: 'var(--tm-text-3)', fontSize: '0.7rem' }}>против</div>
                            <div>
                              <Tag type="warm-gray" size="sm">существующее</Tag>{' '}
                              <a
                                href={`/knowledge?project=${encodeURIComponent(cf.oldProject || row.project)}&dossier=${encodeURIComponent(cf.oldID)}`}
                                onClick={openDossier(cf.oldID, cf.oldProject || row.project)}
                                style={{ color: 'var(--tm-teal)', fontSize: '0.72rem' }}
                              >{cf.oldID}</a>
                              <div style={{ color: 'var(--tm-text-2)', marginTop: '0.1rem' }}>{cf.oldProp || '—'}</div>
                            </div>
                          </div>
                          <div style={{ display: 'flex', gap: '0.4rem', flexWrap: 'wrap', marginTop: '0.4rem' }}>
                            <Button size="sm" kind="danger" disabled={busyCase === row.key} onClick={() => void resolveConflict(row, 'invalidate_new')}>
                              {busyCase === row.key ? '…' : t('supervisor.action_invalidate_new') ?? 'Invalidate new'}
                            </Button>
                            <Button size="sm" kind="danger" disabled={busyCase === row.key} onClick={() => void resolveConflict(row, 'invalidate_old')}>
                              {busyCase === row.key ? '…' : t('supervisor.action_invalidate_old') ?? 'Invalidate existing'}
                            </Button>
                            <Button size="sm" kind="secondary" disabled={busyCase === row.key} onClick={() => void resolveConflict(row, 'keep_both')}>
                              {busyCase === row.key ? '…' : t('supervisor.action_keep_both') ?? 'Keep both'}
                            </Button>
                          </div>
                        </div>
                      )
                      })()}
                      <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'center', marginTop: '0.35rem', flexWrap: 'wrap' }}>
                        {!isResolved && (
                          <Button
                            size="sm"
                            kind="ghost"
                            disabled={busyAck === row.key}
                            onClick={() => { for (const id of row.eventIDs) { void acknowledge(id) } setBusyAck('') }}
                          >
                            {t('supervisor.acknowledge') ?? 'Mark resolved'}
                          </Button>
                        )}
                        {row.runs.length > 0 && (
                          <button
                            type="button"
                            onClick={() => setExpanded(open ? null : row.key)}
                            style={{ background: 'none', border: 'none', color: 'var(--tm-teal)', cursor: 'pointer', fontSize: '0.72rem', padding: 0 }}
                          >
                            {open ? t('supervisor.hide_runs') : t('supervisor.show_runs', { count: String(row.runs.length) })}
                          </button>
                        )}
                      </div>
                      {open && row.runs.length > 0 && (
                        <div style={{ marginTop: '0.3rem', display: 'flex', flexDirection: 'column', gap: '0.2rem' }}>
                              {row.runs.slice(0, 20).map(({ run, at }) => (
                                <div key={run + at} style={{ display: 'flex', gap: '0.5rem', alignItems: 'baseline' }}>
                                  <a
                                    href={`/agents?project=${encodeURIComponent(row.project)}&run=${encodeURIComponent(run)}`}
                                    onClick={(e) => { e.preventDefault(); navigate(`/agents?project=${encodeURIComponent(row.project)}&run=${encodeURIComponent(run)}`) }}
                                    style={{ fontSize: '0.72rem', color: 'var(--tm-teal)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}
                                    title={run}
                                  >{run}</a>
                                  <span style={{ fontSize: '0.66rem', color: 'var(--tm-text-3)', flexShrink: 0 }}>{new Date(at).toLocaleString()}</span>
                                </div>
                              ))}
                        </div>
                      )}
                    </div>
                  )
                })}
              </div>
            )}
          </section>
        </>
      )}
    </>
  )
}

export default function Supervisor() {
  const t = useT()
  return (
    <div style={{ padding: '1rem', maxWidth: '60rem', margin: '0 auto' }}>
      <div style={{ marginBottom: '0.75rem' }}>
        <h2 style={{ margin: 0 }}>{t('supervisor.title')}</h2>
        <p style={{ color: 'var(--tm-text-3)', fontSize: '0.8rem', margin: '0.25rem 0 0' }}>{t('supervisor.subtitle')}</p>
      </div>
      <SupervisorPanels />
    </div>
  )
}
