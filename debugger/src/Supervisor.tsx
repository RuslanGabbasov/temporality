import { useEffect, useState, useCallback } from 'react'
import { Tag } from '@carbon/react'
import PageLoading from './PageLoading'
import { observationApi, type ObservationEvent } from './observationApi'
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
  summary: string
  recommendation: string
  project: string
  count: number
  runs: { run: string; at: string }[]
  lastAt: string
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
      summary: String(event.data?.summary ?? ''),
      recommendation: String(event.data?.recommendation ?? ''),
      project: event.context?.project ?? '',
      count: 1,
      runs: run ? [{ run, at: event.occurred_at }] : [],
      lastAt: event.occurred_at,
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
    if (event.occurred_at > existing.lastAt) {
      existing.lastAt = event.occurred_at
      // The newest verdict speaks for the cluster.
      existing.summary = row.summary
      existing.recommendation = row.recommendation
      existing.pattern = row.pattern || existing.pattern
      existing.project = row.project || existing.project
    }
  }
  const clustered = [...byKey.values()]
  clustered.sort((a, b) => (b.count - a.count) || (a.lastAt < b.lastAt ? 1 : -1))
  return [...clustered, ...loose.sort((a, b) => (a.lastAt < b.lastAt ? 1 : -1))]
}

export default function Supervisor() {
  const t = useT()
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [verdicts, setVerdicts] = useState<ObservationEvent[]>([])
  const [insights, setInsights] = useState<ObservationEvent[]>([])
  const [expanded, setExpanded] = useState<string | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    setError('')
    try {
      const [verdictPage, insightPage] = await Promise.all([
        observationApi.events(undefined, undefined, undefined, undefined, { type: 'supervisor.verdict', limit: 200 }),
        observationApi.events(undefined, undefined, undefined, undefined, { type: 'supervisor.insight', limit: 50 }),
      ])
      setVerdicts(verdictPage.events ?? [])
      setInsights(insightPage.events ?? [])
    } catch (failure) {
      setError(message(failure))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => { void load() }, [load])

  const clusters = clusterVerdicts(verdicts)

  return (
    <div style={{ padding: '1rem', maxWidth: '60rem', margin: '0 auto' }}>
      <div style={{ marginBottom: '0.75rem' }}>
        <h2 style={{ margin: 0 }}>{t('supervisor.title')}</h2>
        <p style={{ color: 'var(--tm-text-3)', fontSize: '0.8rem', margin: '0.25rem 0 0' }}>{t('supervisor.subtitle')}</p>
      </div>
      {error && <p style={{ color: 'var(--tm-danger)', fontSize: '0.8rem' }}>{error}</p>}

      {loading ? <PageLoading /> : (
        <>
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
            <h3 style={{ fontSize: '0.85rem', margin: '0 0 0.4rem' }}>{t('supervisor.problems_title')}</h3>
            {clusters.length === 0 ? (
              <p style={{ color: 'var(--tm-text-3)', fontSize: '0.78rem', margin: 0 }}>{t('supervisor.problems_empty')}</p>
            ) : (
              <div style={{ border: '1px solid var(--tm-border)', borderRadius: '6px', overflow: 'hidden' }}>
                {clusters.map((row) => {
                  const open = expanded === row.key
                  return (
                    <div key={row.key} style={{ padding: '0.55rem 0.75rem', borderTop: '1px solid var(--tm-border)', fontSize: '0.78rem' }}>
                      <div style={{ display: 'flex', alignItems: 'center', gap: '0.45rem', flexWrap: 'wrap' }}>
                        <Tag type={causeTag(row.cause)} size="sm">{row.cause}</Tag>
                        {row.pattern && <span style={{ fontSize: '0.74rem', color: 'var(--tm-text-2)' }}>{row.pattern}</span>}
                        {row.project && <span style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)' }}>{row.project}</span>}
                        {row.count > 1 && <Tag type="cool-gray" size="sm">×{row.count}</Tag>}
                        <span style={{ marginLeft: 'auto', fontSize: '0.68rem', color: 'var(--tm-text-3)', flexShrink: 0 }}>{new Date(row.lastAt).toLocaleString()}</span>
                      </div>
                      <div style={{ marginTop: '0.3rem', lineHeight: 1.5 }}>{row.summary}</div>
                      {row.recommendation && (
                        <div style={{ marginTop: '0.2rem', fontSize: '0.72rem', color: 'var(--tm-text-2)' }}>
                          <span style={{ color: 'var(--tm-amber)' }}>→</span> {row.recommendation}
                        </div>
                      )}
                      {row.runs.length > 0 && (
                        <div style={{ marginTop: '0.35rem' }}>
                          <button
                            type="button"
                            onClick={() => setExpanded(open ? null : row.key)}
                            style={{ background: 'none', border: 'none', color: 'var(--tm-teal)', cursor: 'pointer', fontSize: '0.72rem', padding: 0 }}
                          >
                            {open ? t('supervisor.hide_runs') : t('supervisor.show_runs', { count: String(row.runs.length) })}
                          </button>
                          {open && (
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
                      )}
                    </div>
                  )
                })}
              </div>
            )}
          </section>
        </>
      )}
    </div>
  )
}
