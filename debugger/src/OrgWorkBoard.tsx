import { useCallback, useEffect, useMemo, useState } from 'react'
import { Button, Heading, InlineNotification, Tag } from '@carbon/react'
import PageLoading from './PageLoading'
import { orgApi, type OrgAwarenessSummary, type OrgCardRun, type OrgPlanGraphResponse, type OrgWorkCard, type OrgWorkMetrics, type OrgWorkView } from './orgApi'
import { OrgUnitSelect, useOrgUnits, type OrgUnitsState } from './orgUnits'
import { pluralT, useI18n, useT } from './i18n'
import { briefDateTime, TimeHint } from './timeHint'

// Work board (docs/org-work.md management view): every task of the unit (and
// its subtree) as a kanban card, positioned by the stage the server reports —
// plus the problems strip on top (waiting on a human, failed runs, stalled
// work) and the awareness badges that show whether context actually reached
// the runs. A collapsible plan-graph section renders the task/run/artifact
// relations below the board.

const navigate = (path: string) => { window.history.pushState(null, '', path); window.dispatchEvent(new PopStateEvent('popstate')) }

function message(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }

type TagType = 'red' | 'magenta' | 'purple' | 'blue' | 'cyan' | 'teal' | 'green' | 'gray' | 'cool-gray' | 'warm-gray' | 'high-contrast' | 'outline'

function shortId(value?: string) {
  if (!value) return '—'
  return value.length > 14 ? `${value.slice(0, 13)}…` : value
}

function statusTag(status?: string): TagType {
  switch (status) {
    case 'running':
    case 'in_progress':
      return 'blue'
    case 'completed':
    case 'consumed':
    case 'acted_upon':
      return 'green'
    case 'failed':
      return 'red'
    case 'cancelled':
    case 'dismissed':
      return 'gray'
    case 'delivered':
      return 'cyan'
    case 'waiting':
    case 'pending':
    case 'planned':
      return 'warm-gray'
    default:
      return 'warm-gray'
  }
}

const COLUMN_KEYS: Record<string, string> = {
  planned: 'orgwork.col_planned',
  in_progress: 'orgwork.col_in_progress',
  waiting: 'orgwork.col_waiting',
  review: 'orgwork.col_review',
  completed: 'orgwork.col_completed',
}

const METRIC_KEYS: Record<string, string> = {
  planned: 'orgwork.metric_planned',
  active: 'orgwork.metric_active',
  waiting: 'orgwork.metric_waiting',
  review: 'orgwork.metric_review',
  completed: 'orgwork.metric_completed',
  failed_runs: 'orgwork.metric_failed_runs',
  completed_runs_24h: 'orgwork.metric_completed_runs_24h',
}

const METRIC_ORDER: (keyof OrgWorkMetrics)[] = ['planned', 'active', 'waiting', 'review', 'completed', 'failed_runs', 'completed_runs_24h']

const PROBLEM_KEYS: Record<string, string> = {
  waiting_on_human: 'orgwork.problem_waiting_on_human',
  failed_run: 'orgwork.problem_failed_run',
  no_movement: 'orgwork.problem_no_movement',
}

const PROBLEM_TAGS: Record<string, TagType> = {
  waiting_on_human: 'cyan',
  failed_run: 'red',
  no_movement: 'purple',
}

const KIND_KEYS: Record<string, string> = {
  task: 'orgwork.kind_task',
  run: 'orgwork.kind_run',
  artifact: 'orgwork.kind_artifact',
}

const EDGE_KEYS: Record<string, string> = {
  executes: 'orgwork.edge_executes',
  delegates: 'orgwork.edge_delegates',
  produces: 'orgwork.edge_produces',
  related: 'orgwork.edge_related',
}

function MetricChip({ value, label }: { value: number; label: string }) {
  return (
    <div style={{ background: 'var(--tm-surface-2)', borderRadius: 'var(--tm-radius-sm)', padding: '0.3rem 0.7rem', textAlign: 'center' }}>
      <div style={{ fontSize: '1rem', fontWeight: 600 }}>{value}</div>
      <div style={{ fontSize: '0.62rem', color: 'var(--tm-text-3)', whiteSpace: 'nowrap' }}>{label}</div>
    </div>
  )
}

function AwarenessBadge({ awareness }: { awareness: OrgAwarenessSummary }) {
  const t = useT()
  const chips: { label: string; value?: number; type: TagType; title: string }[] = [
    { label: 'S', value: awareness.selected, type: 'blue', title: t('orgwork.aw_selected') },
    { label: 'D', value: awareness.delivered, type: 'cyan', title: t('orgwork.aw_delivered') },
    { label: 'C', value: awareness.consumed, type: 'green', title: t('orgwork.aw_consumed') },
    { label: 'i', value: awareness.details, type: 'purple', title: t('orgwork.aw_details') },
  ]
  const visible = chips.filter((chip) => (chip.value ?? 0) > 0)
  if (!visible.length) return null
  return (
    <div style={{ display: 'flex', gap: '0.25rem', marginTop: '0.3rem', flexWrap: 'wrap' }}>
      {visible.map((chip) => (
        <Tag key={chip.title} type={chip.type} size="sm" title={chip.title}>{chip.label} {chip.value}</Tag>
      ))}
    </div>
  )
}

function WorkCard({ card, selected, onOpen }: { card: OrgWorkCard; selected: boolean; onOpen: () => void }) {
  const { t, locale } = useI18n()
  return (
    <div
      onClick={onOpen}
      style={{
        background: 'var(--tm-surface)',
        border: `1px solid ${selected ? 'var(--tm-teal)' : 'var(--tm-border)'}`,
        borderRadius: 'var(--tm-radius-sm)',
        padding: '0.45rem 0.5rem',
        marginBottom: '0.4rem',
        cursor: 'pointer',
        fontSize: '0.76rem',
      }}
    >
      <div style={{ display: 'flex', gap: '0.3rem', alignItems: 'baseline' }}>
        {card.runs.some((run) => run.urgent) && <span title={t('orgwork.urgent')}>⚡</span>}
        <span style={{ fontWeight: 600, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={card.title}>{card.title}</span>
      </div>
      <div style={{ display: 'flex', gap: '0.3rem', flexWrap: 'wrap', marginTop: '0.25rem', alignItems: 'center' }}>
        {(card.flags ?? []).includes('failed') && <Tag type="red" size="sm">{t('orgwork.flag_failed')}</Tag>}
        {card.run_count > 0 && (
          <span style={{ color: 'var(--tm-text-3)', fontSize: '0.66rem' }}>
            {pluralT(t, locale, 'orgwork.runs_n', card.run_count)}
          </span>
        )}
        {card.actors.slice(0, 3).map((actor) => (
          <Tag key={actor} type="cool-gray" size="sm" title={actor}>{shortId(actor)}</Tag>
        ))}
        {card.actors.length > 3 && <span style={{ fontSize: '0.66rem', color: 'var(--tm-text-3)' }}>+{card.actors.length - 3}</span>}
      </div>
      {card.awareness && <AwarenessBadge awareness={card.awareness} />}
    </div>
  )
}

function RunRow({ run, onFlow }: { run: OrgCardRun; onFlow: (runID: string) => void }) {
  const t = useT()
  return (
    <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'baseline', padding: '0.3rem 0', borderTop: '1px solid var(--tm-border)', fontSize: '0.76rem', flexWrap: 'wrap' }}>
      {run.urgent && <span title={t('orgwork.urgent')}>⚡</span>}
      <Tag type={statusTag(run.status)} size="sm">{run.status}</Tag>
      <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', maxWidth: '22rem' }} title={run.title ?? run.run_id}>{run.title || run.run_id}</span>
      <span className="mono" style={{ fontSize: '0.68rem', color: 'var(--tm-text-3)' }} title={run.run_id}>{shortId(run.run_id)}</span>
      {run.actor_id && <span className="mono" style={{ fontSize: '0.68rem', color: 'var(--tm-text-3)' }} title={run.actor_id}>{shortId(run.actor_id)}</span>}
      {run.last_event_at && (
        <TimeHint value={run.last_event_at} style={{ color: 'var(--tm-text-3)', fontSize: '0.68rem', whiteSpace: 'nowrap' }}>
          {briefDateTime(run.last_event_at)}
        </TimeHint>
      )}
      <button className="toolbar-btn" onClick={() => onFlow(run.run_id)} title={t('orgwork.open_flow')}>
        {t('orgwork.open_flow')}
      </button>
    </div>
  )
}

function PlanGraphSection({ unit, scope, org }: { unit: string; scope: string; org: OrgUnitsState }) {
  const { t } = useI18n()
  const [open, setOpen] = useState(false)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [graph, setGraph] = useState<OrgPlanGraphResponse | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    setError('')
    try {
      setGraph(await orgApi.planGraph({ org_node: unit || undefined, scope }))
    } catch (failure) {
      setError(message(failure))
    } finally {
      setLoading(false)
    }
  }, [unit, scope])

  useEffect(() => {
    if (open) void load()
  }, [open, load])

  const nodeById = useMemo(() => new Map((graph?.nodes ?? []).map((node) => [node.id, node])), [graph])

  return (
    <div style={{ marginTop: '1.25rem', borderTop: '1px solid var(--tm-border)', paddingTop: '0.6rem' }}>
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        style={{ background: 'none', border: 'none', padding: 0, cursor: 'pointer', fontSize: '0.8rem', color: 'var(--tm-teal)', display: 'flex', alignItems: 'baseline', gap: '0.4rem' }}
      >
        <span>{open ? '▾' : '▸'}</span>
        <span>{t('orgwork.graph_title')}</span>
      </button>
      {!open && (
        <span style={{ marginLeft: '0.5rem', fontSize: '0.7rem', color: 'var(--tm-text-3)' }}>{t('orgwork.graph_hint')}</span>
      )}
      {open && (
        loading && !graph ? <PageLoading /> : (
          <>
            {error && <InlineNotification kind="error" title={t('common.error')} subtitle={error} onClose={() => setError('')} lowContrast style={{ marginTop: '0.5rem' }} />}
            {graph && graph.nodes.length === 0 && (
              <div style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem', padding: '0.5rem 0' }}>{t('orgwork.graph_empty')}</div>
            )}
            {graph && graph.nodes.length > 0 && (
              <div style={{ marginTop: '0.5rem', display: 'flex', flexDirection: 'column', gap: '0.75rem' }}>
                {(['task', 'run', 'artifact'] as const).map((kind) => {
                  const nodes = graph.nodes.filter((node) => node.kind === kind)
                  if (!nodes.length) return null
                  return (
                    <div key={kind}>
                      <div style={{ fontSize: '0.72rem', textTransform: 'uppercase', letterSpacing: '0.04em', color: 'var(--tm-text-3)', marginBottom: '0.3rem' }}>
                        {t(KIND_KEYS[kind] ?? kind)} <span style={{ textTransform: 'none' }}>({nodes.length})</span>
                      </div>
                      <div style={{ display: 'flex', flexWrap: 'wrap', gap: '0.3rem' }}>
                        {nodes.map((node) => (
                          <Tag
                            key={node.id}
                            type={statusTag(node.status)}
                            size="sm"
                            title={`${node.id}${node.org_node_id ? ` · ${org.nameOf(node.org_node_id)}` : ''}`}
                          >
                            {node.label}{node.status ? ` · ${node.status}` : ''}
                          </Tag>
                        ))}
                      </div>
                    </div>
                  )
                })}
                {graph.edges.length > 0 && (
                  <div>
                    <div style={{ fontSize: '0.72rem', textTransform: 'uppercase', letterSpacing: '0.04em', color: 'var(--tm-text-3)', marginBottom: '0.3rem' }}>
                      {t('orgwork.graph_edges')} <span style={{ textTransform: 'none' }}>({graph.edges.length})</span>
                    </div>
                    <div style={{ display: 'flex', flexDirection: 'column', gap: '0.15rem' }}>
                      {graph.edges.map((edge, i) => {
                        const source = nodeById.get(edge.source)
                        const target = nodeById.get(edge.target)
                        return (
                          <div key={`${edge.source}-${edge.target}-${i}`} style={{ fontSize: '0.72rem', display: 'flex', gap: '0.4rem', alignItems: 'baseline', flexWrap: 'wrap' }}>
                            <span className="mono" title={edge.source}>{source?.label ?? shortId(edge.source)}</span>
                            <span style={{ color: 'var(--tm-text-3)' }}>→</span>
                            <span className="mono" title={edge.target}>{target?.label ?? shortId(edge.target)}</span>
                            <Tag type="gray" size="sm">{t(EDGE_KEYS[edge.kind] ?? edge.kind)}</Tag>
                            {edge.evidence && (
                              <span className="mono" style={{ color: 'var(--tm-text-3)', fontSize: '0.66rem' }} title={edge.evidence}>{shortId(edge.evidence)}</span>
                            )}
                          </div>
                        )
                      })}
                    </div>
                  </div>
                )}
              </div>
            )}
          </>
        )
      )}
    </div>
  )
}

/** The live strip: runs currently executing in scope (docs/org-activity.md
 *  active-runs). The kanban groups work by task — this lists the raw runs,
 *  urgent first, each with the flow entry point. */
function ActiveRunsSection({ unit, scope }: { unit: string; scope: string }) {
  const { t } = useI18n()
  const [open, setOpen] = useState(true)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [runs, setRuns] = useState<OrgCardRun[]>([])

  const load = useCallback(async () => {
    setLoading(true)
    setError('')
    try {
      const page = await orgApi.activeRuns({ org_node: unit || undefined, scope, status: 'running', limit: 100 })
      const rows = (page.runs ?? []).map((run) => ({
        run_id: run.run_id,
        status: run.status,
        title: run.title,
        urgent: run.urgent,
        actor_id: run.actor_id,
        last_event_at: run.last_event_at,
      }))
      rows.sort((a, b) => Number(b.urgent ?? false) - Number(a.urgent ?? false))
      setRuns(rows)
    } catch (failure) {
      setError(message(failure))
    } finally {
      setLoading(false)
    }
  }, [unit, scope])

  useEffect(() => {
    if (open) void load()
  }, [open, load])

  const openFlow = (runID: string) => navigate(`/org/flow?target_run=${encodeURIComponent(runID)}`)

  return (
    <div style={{ marginTop: '1.25rem', borderTop: '1px solid var(--tm-border)', paddingTop: '0.6rem' }}>
      <div style={{ display: 'flex', alignItems: 'baseline', gap: '0.6rem' }}>
        <button
          type="button"
          onClick={() => setOpen((v) => !v)}
          style={{ background: 'none', border: 'none', padding: 0, cursor: 'pointer', fontSize: '0.8rem', color: 'var(--tm-teal)', display: 'flex', alignItems: 'baseline', gap: '0.4rem' }}
        >
          <span>{open ? '▾' : '▸'}</span>
          <span>{t('orgwork.active_title')}</span>
        </button>
        {!open && runs.length > 0 && <span style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)' }}>{runs.length}</span>}
        <button className="toolbar-btn" style={{ marginLeft: 'auto' }} onClick={() => void load()} disabled={loading} title={t('common.refresh')}>↻</button>
      </div>
      {open && (
        loading && runs.length === 0 ? <PageLoading /> : (
          <>
            {error && <InlineNotification kind="error" title={t('common.error')} subtitle={error} onClose={() => setError('')} lowContrast style={{ marginTop: '0.5rem' }} />}
            {runs.length === 0 && !error && (
              <div style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem', padding: '0.5rem 0' }}>{t('orgwork.active_empty')}</div>
            )}
            {runs.map((run) => <RunRow key={run.run_id} run={run} onFlow={openFlow} />)}
          </>
        )
      )}
    </div>
  )
}

export default function OrgWorkBoard() {
  const { t, locale } = useI18n()
  const org = useOrgUnits()
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [view, setView] = useState<OrgWorkView | null>(null)
  const [unit, setUnit] = useState('')
  const [scope, setScope] = useState('subtree')
  const [selectedTask, setSelectedTask] = useState<string | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    setError('')
    try {
      setView(await orgApi.workView({ org_node: unit || undefined, scope }))
    } catch (failure) {
      setError(message(failure))
    } finally {
      setLoading(false)
    }
  }, [unit, scope])

  useEffect(() => { void load() }, [load])

  const columns = view?.columns ?? []
  const cards = view?.cards ?? []
  const selectedCard = selectedTask ? cards.find((card) => card.task_id === selectedTask) ?? null : null
  const problems = view?.problems ?? []
  const metrics = view?.metrics ?? {}

  const openFlow = (runID: string) => navigate(`/org/flow?target_run=${encodeURIComponent(runID)}`)

  return (
    <div style={{ padding: '1rem', maxWidth: '86rem', margin: '0 auto' }}>
      {error && (
        <InlineNotification kind="error" title={t('common.error')} subtitle={error} onClose={() => setError('')} lowContrast style={{ marginBottom: '1rem' }} />
      )}

      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: '0.75rem', flexWrap: 'wrap', marginBottom: '0.75rem' }}>
        <div>
          <Heading style={{ fontSize: '1.1rem' }}>{t('orgwork.title')}</Heading>
          <p style={{ color: 'var(--tm-text-3)', fontSize: '0.8rem', margin: '0.25rem 0 0' }}>{t('orgwork.subtitle')}</p>
        </div>
        <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
          <OrgUnitSelect id="orgwork-unit" value={unit} onChange={(v) => { setUnit(v); setSelectedTask(null) }} org={org} label={t('orgops.unit')} size="sm" />
          <label className="toolbar-select">
            {t('orgops.scope')}
            <select value={scope} onChange={(e) => { setScope(e.target.value); setSelectedTask(null) }}>
              <option value="unit">{t('orgops.scope_unit')}</option>
              <option value="subtree">{t('orgops.scope_subtree')}</option>
            </select>
          </label>
          <button className="toolbar-btn" onClick={() => void load()} disabled={loading} title={t('common.refresh')}>↻</button>
        </div>
      </div>

      {loading && !view ? <PageLoading /> : (
        <>
          {/* Metrics strip */}
          {METRIC_ORDER.some((key) => metrics[key] !== undefined) && (
            <div style={{ display: 'flex', gap: '0.5rem', flexWrap: 'wrap', marginBottom: '0.9rem' }}>
              {METRIC_ORDER.map((key) => (
                metrics[key] !== undefined && <MetricChip key={key} value={metrics[key] as number} label={t(METRIC_KEYS[key])} />
              ))}
            </div>
          )}

          {/* Problems first — this is the management view */}
          {problems.length > 0 && (
            <div style={{ border: '1px solid var(--tm-danger)', borderRadius: 'var(--tm-radius-md)', padding: '0.6rem 0.75rem', marginBottom: '0.9rem' }}>
              <div style={{ fontSize: '0.72rem', textTransform: 'uppercase', letterSpacing: '0.04em', color: 'var(--tm-text-3)', marginBottom: '0.35rem' }}>
                {t('orgwork.problems_title')} <span style={{ textTransform: 'none' }}>({problems.length})</span>
              </div>
              <div style={{ display: 'flex', flexDirection: 'column', gap: '0.3rem' }}>
                {problems.map((problem, i) => (
                  <div key={`${problem.kind}-${problem.run_id ?? problem.task_id ?? i}`} style={{ display: 'flex', gap: '0.5rem', alignItems: 'baseline', fontSize: '0.78rem', flexWrap: 'wrap' }}>
                    <Tag type={PROBLEM_TAGS[problem.kind] ?? 'gray'} size="sm">{t(PROBLEM_KEYS[problem.kind] ?? problem.kind)}</Tag>
                    {problem.title && <span style={{ fontWeight: 600 }}>{problem.title}</span>}
                    {problem.task_id && <span className="mono" style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)' }} title={problem.task_id}>{shortId(problem.task_id)}</span>}
                    {problem.reason && <span style={{ color: 'var(--tm-text-2)' }}>{problem.reason}</span>}
                    {problem.since && (
                      <TimeHint value={problem.since} style={{ color: 'var(--tm-text-3)', fontSize: '0.7rem' }}>
                        {briefDateTime(problem.since)}
                      </TimeHint>
                    )}
                    {problem.run_id && (
                      <button className="toolbar-btn" onClick={() => openFlow(problem.run_id!)} title={t('orgwork.open_flow')}>
                        {t('orgwork.open_flow')}
                      </button>
                    )}
                  </div>
                ))}
              </div>
            </div>
          )}

          {/* Kanban */}
          {columns.length > 0 && (
            <div style={{ display: 'grid', gridTemplateColumns: `repeat(${columns.length}, minmax(11rem, 1fr))`, gap: '0.6rem', alignItems: 'start' }}>
              {columns.map((column) => {
                const columnCards = cards.filter((card) => card.column === column)
                return (
                  <div key={column} style={{ background: 'var(--tm-surface-2)', borderRadius: 'var(--tm-radius-sm)', padding: '0.5rem' }}>
                    <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'baseline', padding: '0.1rem 0.2rem 0.4rem', fontSize: '0.72rem', textTransform: 'uppercase', letterSpacing: '0.04em', color: 'var(--tm-text-3)' }}>
                      <span>{COLUMN_KEYS[column] ? t(COLUMN_KEYS[column]) : column}</span>
                      <span style={{ textTransform: 'none' }}>{columnCards.length}</span>
                    </div>
                    {columnCards.map((card) => (
                      <WorkCard
                        key={card.task_id}
                        card={card}
                        selected={card.task_id === selectedTask}
                        onOpen={() => setSelectedTask((prev) => (prev === card.task_id ? null : card.task_id))}
                      />
                    ))}
                    {columnCards.length === 0 && (
                      <div style={{ color: 'var(--tm-text-3)', fontSize: '0.7rem', padding: '0.4rem 0.2rem' }}>{t('orgwork.col_empty')}</div>
                    )}
                  </div>
                )
              })}
            </div>
          )}

          {!loading && cards.length === 0 && (view?.ungrouped_runs ?? []).length === 0 && (
            <div style={{ color: 'var(--tm-text-3)', fontSize: '0.8rem', padding: '1.5rem 0', textAlign: 'center' }}>
              {t('orgwork.empty')}
            </div>
          )}

          {/* Card detail: runs with status/urgent + the flow entry point */}
          {selectedCard && (
            <div style={{ marginTop: '0.9rem', border: '1px solid var(--tm-teal)', borderRadius: 'var(--tm-radius-md)', padding: '0.75rem', background: 'var(--tm-surface)' }}>
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'baseline', gap: '0.5rem', flexWrap: 'wrap' }}>
                <strong style={{ fontSize: '0.85rem' }}>{selectedCard.title}</strong>
                <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'baseline' }}>
                  <span className="mono" style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)' }} title={selectedCard.task_id}>{shortId(selectedCard.task_id)}</span>
                  <Button size="sm" kind="ghost" onClick={() => setSelectedTask(null)}>✕</Button>
                </div>
              </div>
              <div style={{ display: 'flex', gap: '0.4rem', flexWrap: 'wrap', alignItems: 'center', marginTop: '0.35rem', fontSize: '0.74rem' }}>
                <Tag type="gray" size="sm">{COLUMN_KEYS[selectedCard.column] ? t(COLUMN_KEYS[selectedCard.column]) : selectedCard.column}</Tag>
                {selectedCard.project_id && <span className="mono" style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)' }} title={selectedCard.project_id}>{shortId(selectedCard.project_id)}</span>}
                {selectedCard.actors.map((actor) => <Tag key={actor} type="cool-gray" size="sm" title={actor}>{shortId(actor)}</Tag>)}
                {(selectedCard.org_node_ids ?? []).map((nodeID) => (
                  <Tag key={nodeID} type="cyan" size="sm" title={nodeID}>{org.nameOf(nodeID)}</Tag>
                ))}
                {selectedCard.waiting_reason && (
                  <span style={{ color: 'var(--tm-text-2)' }}>{t('orgwork.waiting_reason')}: {selectedCard.waiting_reason}</span>
                )}
                {selectedCard.last_event_at && (
                  <TimeHint value={selectedCard.last_event_at} style={{ color: 'var(--tm-text-3)', fontSize: '0.7rem' }}>
                    {briefDateTime(selectedCard.last_event_at)}
                  </TimeHint>
                )}
                {selectedCard.awareness && <AwarenessBadge awareness={selectedCard.awareness} />}
              </div>
              <div style={{ marginTop: '0.5rem' }}>
                <div style={{ fontSize: '0.72rem', textTransform: 'uppercase', letterSpacing: '0.04em', color: 'var(--tm-text-3)', padding: '0.2rem 0' }}>
                  {t('orgwork.runs_title')} <span style={{ textTransform: 'none' }}>({selectedCard.runs.length})</span>
                </div>
                {selectedCard.runs.map((run) => <RunRow key={run.run_id} run={run} onFlow={openFlow} />)}
                {selectedCard.runs.length === 0 && (
                  <div style={{ color: 'var(--tm-text-3)', fontSize: '0.74rem', padding: '0.3rem 0' }}>{t('orgwork.runs_empty')}</div>
                )}
              </div>
            </div>
          )}

          {/* Runs not attached to any task */}
          {(view?.ungrouped_runs ?? []).length > 0 && (
            <div style={{ marginTop: '0.9rem', border: '1px solid var(--tm-border)', borderRadius: 'var(--tm-radius-md)', padding: '0.75rem' }}>
              <div style={{ fontSize: '0.72rem', textTransform: 'uppercase', letterSpacing: '0.04em', color: 'var(--tm-text-3)', padding: '0.2rem 0' }}>
                {t('orgwork.ungrouped_title')} <span style={{ textTransform: 'none' }}>({(view?.ungrouped_runs ?? []).length})</span>
              </div>
              {(view?.ungrouped_runs ?? []).map((run) => <RunRow key={run.run_id} run={run} onFlow={openFlow} />)}
            </div>
          )}

          <ActiveRunsSection unit={unit} scope={scope} />

          <PlanGraphSection unit={unit} scope={scope} org={org} />
        </>
      )}
    </div>
  )
}
