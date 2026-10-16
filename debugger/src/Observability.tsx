import { useEffect, useMemo, useState } from 'react'
import { authHeaders } from './api'
import { observationApi, type KnowledgeItem } from './observationApi'
import {
  Button,
  InlineNotification,
  Heading,
  Tag,
  Tile,
  Stack,
} from '@carbon/react'
import PageLoading from './PageLoading'
import ProjectRequired from './ProjectRequired'
import { useT } from './i18n'

function errorMessage(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }
/** Partial datetime-local values must not become Invalid Date .toISOString()
 *  crashes — an unparseable value simply means "filter not applied yet". */
function utcValue(local: string): string | undefined {
  if (!local) return undefined
  const date = new Date(local)
  return Number.isNaN(date.getTime()) ? undefined : date.toISOString()
}

/** datetime-local inputs take "YYYY-MM-DDTHH:mm" in LOCAL time — a plain
 *  toISOString() slice would shift the preset by the timezone offset. */
function localDateTimeValue(date: Date): string {
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`
}

const TIME_PRESETS: { key: string; shiftMs: () => number }[] = [
  { key: 'knowledge.preset_hour', shiftMs: () => 3600e3 },
  { key: 'knowledge.preset_day', shiftMs: () => 24 * 3600e3 },
  { key: 'knowledge.preset_week', shiftMs: () => 7 * 24 * 3600e3 },
]

/** Filters auto-apply, so inputs (datetime pickers) need a debounce
 *  to avoid firing a request per keystroke. */
function useDebounced<T>(value: T, delayMs = 350): T {
  const [debounced, setDebounced] = useState(value)
  useEffect(() => {
    const timer = setTimeout(() => setDebounced(value), delayMs)
    return () => clearTimeout(timer)
  }, [value, delayMs])
  return debounced
}

type Tab = 'overview' | 'patterns'

/** Localized knowledge-state label; t() returns the key itself when a
 * translation is missing — treat that as "no translation" and keep the raw
 * state id so a new state never renders as its own key name. */
function stateLabel(state: string, t: (key: string, vars?: Record<string, string>) => string): string {
  const key = `knowledge.state_${state}`
  const text = t(key)
  return text !== key ? text : state
}

export default function Observability({ project }: { project: string }) {
  const t = useT()
  const initParams = new URLSearchParams(window.location.search)
  // Embedded inside the dashboard (/dashboard?tab=knowledge) the page-level `tab`
  // parameter belongs to the dashboard, so the inner tab lives under `ktab`.
  const [tab, setTab] = useState<Tab>((['overview', 'patterns'].includes(initParams.get('ktab') as Tab) ? initParams.get('ktab') : 'overview') as Tab)
  const [asOf, setAsOf] = useState(initParams.get('as_of') ?? '')
  const [compareAsOf, setCompareAsOf] = useState(initParams.get('compare') ?? '')
  const [knownAt, setKnownAt] = useState(initParams.get('known_at') ?? '')
  const [knowledge, setKnowledge] = useState<KnowledgeItem[]>([])
  const [comparisonKnowledge, setComparisonKnowledge] = useState<KnowledgeItem[] | null>(null)
  const [revision, setRevision] = useState(0)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [projection, setProjection] = useState<any>(null)
  const [projectionLoading, setProjectionLoading] = useState(false)
  const [timeOpen, setTimeOpen] = useState(false)

  const asOfUTC = utcValue(useDebounced(asOf, 500))
  const knownAtUTC = utcValue(useDebounced(knownAt, 500))
  const compareUTC = utcValue(useDebounced(compareAsOf, 500))

  const clusters = useMemo(() => {
    const groups = new Map<string, { dimension: 'topic' | 'entity'; items: KnowledgeItem[] }>()
    for (const item of knowledge) {
      const fromTopic = (item.topics?.length ?? 0) > 0
      const name = (fromTopic ? item.topics?.[0] : item.entities?.[0]) || item.topics?.[0] || item.entities?.[0] || (t('knowledge.no_topic') ?? 'No topic')
      const group = groups.get(name)
      if (group) group.items.push(item)
      else groups.set(name, { dimension: fromTopic ? 'topic' : 'entity', items: [item] })
    }
    return [...groups.entries()].sort((a, b) => b[1].items.length - a[1].items.length)
  }, [knowledge, t])

  // Health summary
  const health = useMemo(() => {
    const counts = { alive: 0, stale: 0, invalidated: 0, total: knowledge.length }
    for (const k of knowledge) {
      if (k.state === 'invalidated' || k.state === 'superseded' || k.state === 'corrected') counts.invalidated++
      else if (k.at_risk) counts.stale++
      else counts.alive++
    }
    return counts
  }, [knowledge])

  // Knowledge changes vs comparison
  const knowledgeChanges = useMemo(() => {
    if (!comparisonKnowledge) return null
    const oldByID = new Map(comparisonKnowledge.map((k) => [k.id, k]))
    const added = knowledge.filter((k) => !oldByID.has(k.id))
    const changed = knowledge.filter((k) => {
      const old = oldByID.get(k.id)
      return old && old.state !== k.state
    })
    return { added, changed, previousCount: comparisonKnowledge.length }
  }, [knowledge, comparisonKnowledge])

  // URL sync
  useEffect(() => {
    const urlP = new URLSearchParams(window.location.search)
    urlP.set('project', project)
    urlP.set('ktab', tab)
    if (asOf) urlP.set('as_of', asOf); else urlP.delete('as_of')
    if (compareAsOf) urlP.set('compare', compareAsOf); else urlP.delete('compare')
    if (knownAt) urlP.set('known_at', knownAt); else urlP.delete('known_at')
    window.history.replaceState(null, '', `${window.location.pathname}?${urlP.toString()}`)
  }, [project, tab, asOf, compareAsOf, knownAt])

  // Overview data: the full projection feeds health, clusters and the
  // bitemporal comparison — it is not paginated.
  async function loadOverview() {
    if (!project.trim()) { setError(t('knowledge.enter_project') ?? 'Enter a project ID.'); return }
    setLoading(true); setError('')
    try {
      const [history, comparison] = await Promise.all([
        observationApi.knowledge(project.trim(), asOfUTC, knownAtUTC),
        compareUTC ? observationApi.knowledge(project.trim(), compareUTC, knownAtUTC) : Promise.resolve(null),
      ])
      setKnowledge(history.knowledge ?? [])
      setComparisonKnowledge(comparison?.knowledge ?? null)
    } catch (reason) { setError(errorMessage(reason)) }
    finally { setLoading(false) }
  }

  // Load projection
  async function loadProjection() {
    if (!project.trim() || projection) return
    setProjectionLoading(true)
    try {
      const resp = await fetch(`/kernel-api/v1/workspace/projects/${encodeURIComponent(project.trim())}/projection`, { headers: { ...authHeaders() } })
      if (resp.ok) setProjection(await resp.json())
    } catch { /* ignore */ }
    finally { setProjectionLoading(false) }
  }

  // Bitemporal filters auto-apply (debounced); mutations bump the revision so
  // the overview refetches.
  useEffect(() => { void loadOverview() }, [project, asOfUTC, knownAtUTC, compareUTC, revision])
  useEffect(() => { if (tab === 'patterns') void loadProjection() }, [tab])

  const TABS: { key: Tab; label: string }[] = [
    { key: 'overview', label: t('knowledge.overview') ?? 'Overview' },
    { key: 'patterns', label: t('knowledge.patterns') ?? 'Patterns' },
  ]

  // The knowledge projection is a per-project view (docs/activity-scoping.md
  // §7): with the global filter active there is nothing to project.
  if (!project.trim()) return <ProjectRequired what={t('knowledge.title') ?? 'Knowledge'} />

  return (
    <div style={{ padding: '1rem' }}>
      {error && <InlineNotification kind="error" title="Error" subtitle={error} onClose={() => setError('')} lowContrast style={{ marginBottom: '1rem' }} />}

      {/* Tab bar with inline controls: refresh + bitemporal popover share the
          tab row (right side) to keep the header a single line. The three
          datetime fields live in a popover — the default view is "now", and
          the toggle stays quiet (badge count) until a historical cut is
          active. */}
      <div style={{ display: 'flex', alignItems: 'center', borderBottom: '1px solid var(--tm-border)', marginBottom: (asOfUTC || knownAtUTC || compareUTC) ? '0.4rem' : '1rem' }}>
        {TABS.map((t) => (
          <button
            key={t.key}
            onClick={() => setTab(t.key)}
            style={{
              padding: '0.5rem 1rem', background: tab === t.key ? 'var(--tm-elevated)' : 'transparent',
              border: 'none', borderBottom: tab === t.key ? '2px solid var(--tm-teal)' : '2px solid transparent',
              color: tab === t.key ? 'var(--tm-teal)' : 'var(--tm-text-3)', cursor: 'pointer', fontSize: '0.8rem', fontWeight: 600,
            }}
          >{t.label}</button>
        ))}
        <div style={{ marginLeft: 'auto', display: 'flex', gap: '0.5rem', alignItems: 'center' }}>
          <button onClick={() => setRevision((r) => r + 1)} disabled={loading} style={{ background: 'none', border: 'none', color: 'var(--tm-text-2)', cursor: 'pointer', fontSize: '1rem', padding: '0.25rem', lineHeight: 1, borderRadius: '4px' }} title="Refresh">↻</button>
          <div className="time-picker">
            {timeOpen && <div className="run-picker-backdrop" onClick={() => setTimeOpen(false)} />}
            <button
              className={`time-toggle ${timeOpen ? 'open' : ''} ${(asOfUTC || knownAtUTC || compareUTC) ? 'filtered' : ''}`}
              onClick={() => setTimeOpen((open) => !open)}
            >
              ⏱ {t('knowledge.historical_view') ?? 'Historical view'}
              {(asOfUTC || knownAtUTC || compareUTC) && (
                <span className="tune-badge">{([asOfUTC, knownAtUTC, compareUTC].filter(Boolean)).length}</span>
              )}
            </button>
            {timeOpen && (
              <div className="time-panel">
                <div className="time-field">
                  <label htmlFor="as-of">{t('knowledge.as_of') ?? 'As of'}</label>
                  <input id="as-of" type="datetime-local" value={asOf} onChange={(e) => setAsOf(e.target.value)} />
                </div>
                <div className="time-field">
                  <label htmlFor="known-at">{t('knowledge.known_by') ?? 'Known by'}</label>
                  <input id="known-at" type="datetime-local" value={knownAt} onChange={(e) => setKnownAt(e.target.value)} />
                </div>
                <div className="time-field">
                  <label htmlFor="compare">{t('knowledge.compare') ?? 'Compare'}</label>
                  <input id="compare" type="datetime-local" value={compareAsOf} onChange={(e) => setCompareAsOf(e.target.value)} />
                </div>
                <div className="time-presets">
                  {TIME_PRESETS.map((preset) => (
                    <button key={preset.key} className="preset-chip" onClick={() => { setAsOf(localDateTimeValue(new Date(Date.now() - preset.shiftMs()))); setTimeOpen(false) }}>
                      {t(preset.key) ?? preset.key}
                    </button>
                  ))}
                  {(asOf || knownAt || compareAsOf) && (
                    <button className="preset-chip tune-reset" onClick={() => { setAsOf(''); setKnownAt(''); setCompareAsOf(''); setTimeOpen(false) }}>
                      {t('knowledge.preset_reset') ?? 'Now'}
                    </button>
                  )}
                </div>
                <p className="time-hint">{t('knowledge.time_hint') ?? 'as_of cuts by when events happened; known_by by when the journal ingested them; compare adds a diff point on the overview.'}</p>
              </div>
            )}
          </div>
        </div>
      </div>

      {/* Active bitemporal view: visible reminder that the projection is cut
          at a historical point, removable in one click */}
      {(asOfUTC || knownAtUTC || compareUTC) && (
        <div style={{ display: 'flex', gap: '0.35rem', flexWrap: 'wrap', marginBottom: '0.75rem', alignItems: 'center' }}>
          <span style={{ fontSize: '0.65rem', color: 'var(--tm-text-3)', textTransform: 'uppercase', letterSpacing: '0.05em' }}>⏱ {t('knowledge.historical_view') ?? 'Historical view'}</span>
          {asOfUTC && <Tag type="purple" size="sm" filter onClose={() => setAsOf('')}>{t('knowledge.as_of')}: {new Date(asOfUTC).toLocaleString()}</Tag>}
          {knownAtUTC && <Tag type="purple" size="sm" filter onClose={() => setKnownAt('')}>{t('knowledge.known_by')}: {new Date(knownAtUTC).toLocaleString()}</Tag>}
          {compareUTC && <Tag type="purple" size="sm" filter onClose={() => setCompareAsOf('')}>{t('knowledge.compare')}: {new Date(compareUTC).toLocaleString()}</Tag>}
        </div>
      )}

      {loading && <PageLoading />}

      {/* ═══════════ OVERVIEW TAB ═══════════ */}
      {tab === 'overview' && !loading && (
        <div>
          {/* Health summary — opens the research explorer with the state
              filter applied; the knowledge tab itself moved to /knowledge
              (stage 4, docs/human-knowledge-explorer-plan.md). */}
          <div style={{ display: 'flex', gap: '1rem', marginBottom: '1.5rem', flexWrap: 'wrap' }}>
            {([
              { label: t('knowledge.total') ?? 'Total', count: health.total, color: 'var(--tm-text)', state: 'all' },
              { label: t('knowledge.alive') ?? 'Alive', count: health.alive, color: 'var(--tm-success)', state: 'alive' },
              { label: t('knowledge.stale') ?? 'Stale', count: health.stale, color: 'var(--tm-amber)', state: 'at_risk' },
              { label: t('knowledge.invalidated') ?? 'Invalidated', count: health.invalidated, color: 'var(--tm-danger)', state: 'invalidated' },
            ]).map((s) => (
              <Tile
                key={s.label}
                className="workspace-tile"
                onClick={() => {
                  const params = new URLSearchParams({ project })
                  if (s.state !== 'all') params.set('state', s.state)
                  window.location.href = `${window.location.origin}/knowledge?${params.toString()}`
                }}
                style={{ padding: '0.75rem 1.25rem', minWidth: '100px', textAlign: 'center', cursor: 'pointer' }}
              >
                <div style={{ fontSize: '1.5rem', fontWeight: 700, color: s.color }}>{s.count}</div>
                <div style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)', textTransform: 'uppercase', letterSpacing: '0.05em' }}>{s.label}</div>
              </Tile>
            ))}
          </div>

          {/* Changes vs comparison */}
          {knowledgeChanges && compareUTC && (
            <InlineNotification
              kind="info"
              title={`Change since ${new Date(compareUTC).toLocaleString()}`}
              subtitle={`+${knowledgeChanges.added.length} new · ${knowledgeChanges.changed.length} changed · ${knowledgeChanges.previousCount} at comparison point`}
              lowContrast style={{ marginBottom: '1rem' }}
            />
          )}

          {/* Cluster summary */}
          {clusters.length > 0 && (
            <Tile style={{ marginBottom: '1rem' }}>
              <Heading style={{ fontSize: '0.875rem', marginBottom: '0.5rem' }}>{t('knowledge.by_scope') ?? 'Knowledge by scope'}</Heading>
              <div style={{ display: 'flex', gap: '0.5rem', flexWrap: 'wrap' }}>
                {clusters.map(([name, group]) => {
                  const alive = group.items.filter((k) => k.state !== 'invalidated' && k.state !== 'superseded' && !k.at_risk).length
                  const params = new URLSearchParams({ project })
                  params.set(group.dimension, name)
                  return (
                    <div
                      key={name}
                      role="button" tabIndex={0}
                      onClick={() => { window.location.href = `${window.location.origin}/knowledge?${params.toString()}` }}
                      onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); window.location.href = `${window.location.origin}/knowledge?${params.toString()}` } }}
                      title={t('knowledge.cluster_open_hint') ?? `Open knowledge filtered by this ${group.dimension}`}
                      style={{ padding: '0.5rem 0.75rem', background: 'var(--tm-ink)', borderRadius: '6px', border: '1px solid var(--tm-border)', minWidth: '120px', cursor: 'pointer' }}
                    >
                      <div style={{ fontSize: '0.8rem', fontWeight: 600, marginBottom: '0.25rem' }}>{name}</div>
                      <div style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)' }}>{group.items.length} items · {alive} alive</div>
                    </div>
                  )
                })}
              </div>
            </Tile>
          )}

          {knowledge.length === 0 && !loading && (
            <div style={{ textAlign: 'center', color: 'var(--tm-text-3)', padding: '3rem 1rem' }}>
              <p>{t('knowledge.no_knowledge') ?? 'No knowledge found for this project. Load a project to see agent knowledge.'}</p>
            </div>
          )}
        </div>
      )}

      {/* ═══════════ PATTERNS TAB ═══════════ */}
      {tab === 'patterns' && (
        <div>
          {projectionLoading && <PageLoading />}
          {!projection && !projectionLoading && (
            <div style={{ textAlign: 'center', color: 'var(--tm-text-3)', padding: '3rem 1rem' }}>
              <p>{t('knowledge.no_projection') ?? 'No projection data yet. Click Load to fetch experience patterns.'}</p>
              <Button onClick={loadProjection} style={{ marginTop: '1rem' }}>{t('knowledge.load_patterns') ?? 'Load patterns'}</Button>
            </div>
          )}
          {projection && (
            <Stack gap={3}>
              {/* Summary */}
              <div style={{ display: 'flex', gap: '1rem', flexWrap: 'wrap' }}>
                {[
                  { label: t('knowledge.runs') ?? 'Runs', count: projection.run_count, color: 'var(--tm-teal)' },
                  { label: t('knowledge.tool_patterns') ?? 'Tool patterns', count: projection.summary?.total_tool_patterns ?? 0, color: 'var(--tm-success)' },
                  { label: t('knowledge.title') ?? 'Knowledge', count: projection.summary?.total_knowledge ?? 0, color: 'var(--tm-teal)' },
                  { label: t('knowledge.alive') ?? 'Alive', count: projection.summary?.alive_knowledge ?? 0, color: 'var(--tm-success)' },
                  { label: t('knowledge.dead') ?? 'Dead', count: projection.summary?.dead_knowledge ?? 0, color: 'var(--tm-danger)' },
                ].map((s) => (
                  <Tile key={s.label} style={{ padding: '0.5rem 1rem', minWidth: '80px', textAlign: 'center' }}>
                    <div style={{ fontSize: '1.25rem', fontWeight: 700, color: s.color }}>{s.count}</div>
                    <div style={{ fontSize: '0.65rem', color: 'var(--tm-text-3)', textTransform: 'uppercase', letterSpacing: '0.05em' }}>{s.label}</div>
                  </Tile>
                ))}
                {projection.summary?.avg_success_rate > 0 && (
                  <Tile style={{ padding: '0.5rem 1rem', textAlign: 'center' }}>
                    <div style={{ fontSize: '1.25rem', fontWeight: 700, color: 'var(--tm-success)' }}>{(projection.summary.avg_success_rate * 100).toFixed(0)}%</div>
                    <div style={{ fontSize: '0.65rem', color: 'var(--tm-text-3)', textTransform: 'uppercase', letterSpacing: '0.05em' }}>{t('knowledge.success_rate') ?? 'Success rate'}</div>
                  </Tile>
                )}
              </div>

              {/* Top tools */}
              {projection.summary?.most_used_tools?.length > 0 && (
                <Tile>
                  <h5 style={{ fontSize: '0.75rem', fontWeight: 600, color: 'var(--tm-text-3)', textTransform: 'uppercase', letterSpacing: '0.05em', marginBottom: '0.5rem' }}>{t('knowledge.most_used_tools') ?? 'Most used tools'}</h5>
                  <div style={{ display: 'flex', gap: '0.35rem', flexWrap: 'wrap' }}>
                    {projection.summary.most_used_tools.map((t: string) => <Tag key={t} type="blue" size="sm">{t}</Tag>)}
                  </div>
                </Tile>
              )}

              {/* Tool patterns */}
              {projection.tool_patterns?.length > 0 && (
                <Tile>
                  <h5 style={{ fontSize: '0.75rem', fontWeight: 600, color: 'var(--tm-text-3)', textTransform: 'uppercase', letterSpacing: '0.05em', marginBottom: '0.5rem' }}>{t('knowledge.tool_patterns') ?? 'Tool Patterns'}</h5>
                  <Stack gap={1}>
                    {projection.tool_patterns.map((p: any, i: number) => (
                      <div key={i} style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', fontSize: '0.8rem', padding: '0.3rem 0', borderBottom: '1px solid #1a2332' }}>
                        <Tag type="cyan" size="sm">{p.count}x</Tag>
                        <code style={{ color: '#9e8cff', fontSize: '0.78rem' }}>{p.signature}</code>
                        <span style={{ color: 'var(--tm-text-3)', marginLeft: 'auto' }}>{p.run_count} runs · {(p.success_rate * 100).toFixed(0)}% success</span>
                      </div>
                    ))}
                  </Stack>
                </Tile>
              )}

              {/* Knowledge lifecycle */}
              {projection.knowledge_lifecycle?.length > 0 && (
                <Tile>
                  <h5 style={{ fontSize: '0.75rem', fontWeight: 600, color: 'var(--tm-text-3)', textTransform: 'uppercase', letterSpacing: '0.05em', marginBottom: '0.5rem' }}>{t('knowledge.knowledge_lifecycle') ?? 'Knowledge Lifecycle'}</h5>
                  <Stack gap={1}>
                    {projection.knowledge_lifecycle.map((k: any, i: number) => (
                      <div key={i} style={{ display: 'flex', alignItems: 'flex-start', gap: '0.5rem', fontSize: '0.8rem', padding: '0.4rem 0', borderBottom: '1px solid #1a2332' }}>
                        <Tag type={k.is_alive ? 'green' : 'red'} size="sm" style={{ flexShrink: 0 }}>{stateLabel(k.current_state, t)}</Tag>
                        <span style={{ flex: 1, wordBreak: 'break-word' }}>{k.proposition || k.knowledge_id}</span>
                        <span style={{ color: 'var(--tm-text-3)', flexShrink: 0 }}>{k.run_count} runs</span>
                      </div>
                    ))}
                  </Stack>
                </Tile>
              )}

              {/* Scopes */}
              {projection.scopes?.length > 0 && (
                <Tile>
                  <h5 style={{ fontSize: '0.75rem', fontWeight: 600, color: 'var(--tm-text-3)', textTransform: 'uppercase', letterSpacing: '0.05em', marginBottom: '0.5rem' }}>{t('knowledge.scopes') ?? 'Scopes'}</h5>
                  <div style={{ display: 'flex', gap: '0.5rem', flexWrap: 'wrap' }}>
                    {projection.scopes.map((s: any, i: number) => (
                      <div key={i} style={{ padding: '0.4rem 0.6rem', background: 'var(--tm-ink)', borderRadius: '4px', border: '1px solid var(--tm-border)', fontSize: '0.8rem' }}>
                        <strong>{s.scope}</strong>
                        <span style={{ color: 'var(--tm-text-3)', marginLeft: '0.5rem' }}>{s.tool_count} tools · {s.run_ids?.length ?? 0} runs</span>
                      </div>
                    ))}
                  </div>
                </Tile>
              )}
            </Stack>
          )}
        </div>
      )}
    </div>
  )
}