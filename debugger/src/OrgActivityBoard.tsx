import { useCallback, useEffect, useMemo, useState, type ReactNode } from 'react'
import { Button, Heading, InlineNotification, Tag } from '@carbon/react'
import PageLoading from './PageLoading'
import { orgApi, type OrgActivityEvent, type OrgActivityFilters } from './orgApi'
import { OrgUnitSelect, useOrgUnits, type OrgUnitsState } from './orgUnits'
import { pluralT, useI18n } from './i18n'
import { briefDateTime, TimeHint } from './timeHint'
import { workspaceApi } from './workspaceApi'

// Activity board (docs/org-activity.md): what happened across the
// organization — run/progress/plan/task/artifact/quality/human events scoped
// to an org node, grouped by run so a manager reads one work item as a
// thread. The per-unit activity settings (GET/PUT/DELETE activity-settings)
// live here as a collapsible section: the board is where someone notices
// «why do we not see X» and the switch is one click away.

const navigate = (path: string) => { window.history.pushState(null, '', path); window.dispatchEvent(new PopStateEvent('popstate')) }

function message(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }

type TagType = 'red' | 'magenta' | 'purple' | 'blue' | 'cyan' | 'teal' | 'green' | 'gray' | 'cool-gray' | 'warm-gray' | 'high-contrast' | 'outline'

const CATEGORIES = ['run', 'progress', 'plan', 'task', 'artifact', 'quality', 'human'] as const

const CATEGORY_TAGS: Record<string, TagType> = {
  run: 'blue',
  progress: 'cyan',
  plan: 'purple',
  task: 'green',
  artifact: 'teal',
  quality: 'magenta',
  human: 'warm-gray',
}

const CATEGORY_KEYS: Record<string, string> = {
  run: 'orgops.cat_run',
  progress: 'orgops.cat_progress',
  plan: 'orgops.cat_plan',
  task: 'orgops.cat_task',
  artifact: 'orgops.cat_artifact',
  quality: 'orgops.cat_quality',
  human: 'orgops.cat_human',
}

const PAGE_SIZE = 50

// Auto-granted approvals are system bookkeeping, not organizational news —
// they never reach this board (the kernel skips folding them; this client-side
// cut also hides rows folded by an OLDER kernel until its prune catches up).
const NOISE_TYPES = new Set(['approval.auto_granted'])

// Localized labels per event type (§4.2 families). Unknown types fall back to
// the raw journal type — never a blank cell.
const TYPE_KEYS: Record<string, string> = {
  'run.started': 'orgact.type_run_started',
  'run.restarted': 'orgact.type_run_restarted',
  'run.completed': 'orgact.type_run_completed',
  'run.failed': 'orgact.type_run_failed',
  'run.cancelled': 'orgact.type_run_cancelled',
  'delegation.started': 'orgact.type_delegation_started',
  'delegation.completed': 'orgact.type_delegation_completed',
  'delegation.failed': 'orgact.type_delegation_failed',
  'plan.started': 'orgact.type_plan_started',
  'plan.completed': 'orgact.type_plan_completed',
  'plan.aborted': 'orgact.type_plan_aborted',
  'plan.stopped': 'orgact.type_plan_stopped',
  'plan.task.started': 'orgact.type_plan_task_started',
  'plan.task.completed': 'orgact.type_plan_task_completed',
  'plan.task.failed': 'orgact.type_plan_task_failed',
  'plan.task.skipped': 'orgact.type_plan_task_skipped',
  'saga.registered': 'orgact.type_saga_registered',
  'saga.attached': 'orgact.type_saga_attached',
  'saga.renamed': 'orgact.type_saga_renamed',
  'saga.merged': 'orgact.type_saga_merged',
  'saga.deleted': 'orgact.type_saga_deleted',
  'artifact.proposed': 'orgact.type_artifact_proposed',
  'artifact.updated': 'orgact.type_artifact_updated',
  'artifact.review.requested': 'orgact.type_review_requested',
  'artifact.review.approved': 'orgact.type_review_approved',
  'artifact.review.rejected': 'orgact.type_review_rejected',
  'artifact.returned_to_draft': 'orgact.type_returned_to_draft',
  'verification.reviewed': 'orgact.type_verification_reviewed',
  'behavior.reviewed': 'orgact.type_behavior_reviewed',
  'supervisor.verdict': 'orgact.type_supervisor_verdict',
  'agent.version.degraded': 'orgact.type_agent_degraded',
  'hitl.verdict.recorded': 'orgact.type_hitl_verdict',
  'human.requested': 'orgact.type_human_requested',
  'human.answered': 'orgact.type_human_answered',
  'human.timed_out': 'orgact.type_human_timed_out',
  'approval.requested': 'orgact.type_approval_requested',
  'approval.granted': 'orgact.type_approval_granted',
  'approval.rejected': 'orgact.type_approval_rejected',
}

// The fold bakes English labels into summaries («run failed: timeout»). For
// display the known head swaps to the localized type label; the tail (titles,
// goals, model-written text) is user content and stays as-is.
const SUMMARY_HEADS = [
  'run started', 'run restarted', 'run completed', 'run failed', 'run cancelled',
  'delegation started', 'delegation completed', 'delegation failed',
  'plan started', 'plan completed', 'plan aborted', 'plan stopped',
  'plan task started', 'plan task completed', 'plan task failed', 'plan task skipped',
  'saga registered', 'saga attached', 'saga renamed', 'saga merged', 'saga deleted',
  'artifact proposed', 'artifact updated', 'artifact review requested',
  'artifact review approved', 'artifact review rejected', 'artifact returned to draft',
  'verification reviewed', 'behavior reviewed', 'agent version degraded',
]

// Self-contained English labels the fold emits verbatim.
const SUMMARY_WHOLE: Record<string, string> = {
  'Waiting for human input': 'orgact.type_human_requested',
  'Human input received': 'orgact.type_human_answered',
  'Human input timed out': 'orgact.type_human_timed_out',
  'Waiting for approval': 'orgact.type_approval_requested',
  'Approval granted': 'orgact.type_approval_granted',
  'Approval rejected': 'orgact.type_approval_rejected',
}

// localizedEventText returns [primary label, secondary summary]: the type
// label localized, the fold's summary with its English head swapped.
function localizedEventText(event: OrgActivityEvent, t: (key: string) => string | undefined): { label: string; summary: string } {
  const raw = event.event_type
  const label = TYPE_KEYS[raw] ? (t(TYPE_KEYS[raw]) ?? raw) : raw
  const summary = event.summary ?? ''
  const whole = SUMMARY_WHOLE[summary]
  if (whole) return { label, summary: t(whole) ?? summary }
  const lower = summary.toLowerCase()
  for (const head of SUMMARY_HEADS) {
    if (lower === head) return { label, summary: label }
    if (lower.startsWith(head + ':')) {
      const rest = summary.slice(head.length + 1).trim()
      return { label, summary: rest ? `${label}: ${rest}` : label }
    }
  }
  return { label, summary }
}

const GRID = 'minmax(4.5rem, 0.45fr) minmax(7.5rem, 1fr) minmax(12rem, 2.2fr) minmax(5rem, 0.7fr) minmax(5.5rem, 0.8fr) minmax(5.5rem, 0.8fr) minmax(5.5rem, 0.8fr)'

function shortId(value?: string) {
  if (!value) return '—'
  return value.length > 14 ? `${value.slice(0, 13)}…` : value
}

/** datetime-local input value → RFC 3339 (UTC); empty stays empty. */
function rfc3339(value: string): string | undefined {
  if (!value) return undefined
  const d = new Date(value)
  return Number.isNaN(d.getTime()) ? undefined : d.toISOString()
}

function GridHead({ children }: { children: ReactNode }) {
  return (
    <div style={{ display: 'grid', gridTemplateColumns: GRID, gap: '0.25rem 0.6rem', fontSize: '0.68rem', textTransform: 'uppercase', letterSpacing: '0.04em', color: 'var(--tm-text-3)', padding: '0.35rem 0.6rem', background: 'var(--tm-raised)' }}>
      {children}
    </div>
  )
}

function EventRow({ event, userNameOf, onRun, onTask }: {
  event: OrgActivityEvent
  userNameOf: (id?: string) => string
  onRun: (runID: string) => void
  onTask: (taskID: string) => void
}) {
  const { t, locale } = useI18n()
  const { label, summary } = localizedEventText(event, t)
  const actor = event.actor_id
  const actorName = userNameOf(actor)
  return (
    <div style={{ display: 'grid', gridTemplateColumns: GRID, gap: '0.25rem 0.6rem', alignItems: 'baseline', padding: '0.35rem 0.6rem', borderTop: '1px solid var(--tm-border)', fontSize: '0.78rem' }}>
      <TimeHint value={event.occurred_at} style={{ color: 'var(--tm-text-3)', fontSize: '0.72rem', whiteSpace: 'nowrap' }}>
        {briefDateTime(event.occurred_at)}
      </TimeHint>
      <span style={{ display: 'flex', gap: '0.3rem', alignItems: 'baseline', flexWrap: 'wrap' }}>
        <Tag type={CATEGORY_TAGS[event.category] ?? 'gray'} size="sm" title={event.category}>{t(CATEGORY_KEYS[event.category]) ?? event.category}</Tag>
        <span title={event.event_type}>{label}</span>
      </span>
      <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={`${event.event_type}: ${event.summary}`}>{summary}</span>
      <span className="mono" style={{ fontSize: '0.7rem' }} title={actor ?? ''}>{actorName !== actor ? actorName : shortId(actor)}</span>
      {event.run_id ? (
        <button
          className="toolbar-btn"
          style={{ fontSize: '0.68rem', padding: '0.05rem 0.4rem', justifySelf: 'start' }}
          title={event.run_id}
          onClick={() => onRun(event.run_id!)}
        >{shortId(event.run_id)}</button>
      ) : <span style={{ color: 'var(--tm-text-3)' }}>—</span>}
      {event.task_id ? (
        <button
          className="toolbar-btn"
          style={{ fontSize: '0.68rem', padding: '0.05rem 0.4rem', justifySelf: 'start' }}
          title={event.task_id}
          onClick={() => onTask(event.task_id!)}
        >{shortId(event.task_id)}</button>
      ) : <span style={{ color: 'var(--tm-text-3)' }}>—</span>}
      <span className="mono" style={{ fontSize: '0.7rem' }} title={event.project_id ?? ''}>{shortId(event.project_id)}</span>
    </div>
  )
}

// embedded renders the board inside the dashboard's Activity tab: no page
// padding, no page heading — the tab strip above already names it.
export default function OrgActivityBoard({ embedded }: { embedded?: boolean }) {
  const { t, locale } = useI18n()
  const org = useOrgUnits()
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [events, setEvents] = useState<OrgActivityEvent[]>([])
  const [count, setCount] = useState(0)
  // Actor ids → display names (the directory loads once; unknown ids stay ids
  // — system actors like the supervisor have no directory row).
  const [userNames, setUserNames] = useState<Map<string, string>>(new Map())
  useEffect(() => {
    let cancelled = false
    void workspaceApi.listUsers()
      .then((page) => { if (!cancelled) setUserNames(new Map((page.users ?? []).map((u) => [u.id, u.name]))) })
      .catch(() => { /* ids remain the fallback */ })
    return () => { cancelled = true }
  }, [])
  const userNameOf = useCallback((id?: string) => (id ? userNames.get(id) ?? id : ''), [userNames])

  // A run opens in the runs console (AgentRuns reads ?run=), a task filters
  // the feed itself — tasks have no dedicated page, and the filtered feed IS
  // the task view.
  const openRun = useCallback((runID: string) => navigate(`/agents?run=${encodeURIComponent(runID)}`), [])
  const openTask = useCallback((taskID: string) => { setTaskFilter(taskID); setOffset(0) }, [])

  // Filters (all server-side; an empty unit means the whole installation).
  const [unit, setUnit] = useState('')
  const [scope, setScope] = useState('subtree')
  const [from, setFrom] = useState('')
  const [to, setTo] = useState('')
  const [category, setCategory] = useState('')
  const [typeFilter, setTypeFilter] = useState('')
  const [runFilter, setRunFilter] = useState('')
  const [taskFilter, setTaskFilter] = useState('')
  const [actorFilter, setActorFilter] = useState('')
  const [offset, setOffset] = useState(0)
  const [collapsedRuns, setCollapsedRuns] = useState<Set<string>>(new Set())

  const load = useCallback(async (opts?: { offset?: number; append?: boolean }) => {
    if (!opts?.append) setLoading(true)
    setError('')
    if (!opts?.append) setOffset(0)
    const filters: OrgActivityFilters = {
      org_node: unit || undefined,
      scope,
      from: rfc3339(from),
      to: rfc3339(to),
      category: category || undefined,
      type: typeFilter.trim() || undefined,
      run: runFilter.trim() || undefined,
      task: taskFilter.trim() || undefined,
      actor: actorFilter.trim() || undefined,
      limit: PAGE_SIZE,
      offset: opts?.offset ?? 0,
    }
    try {
      const page = await orgApi.activity(filters)
      const rows = (page.events ?? []).filter((event) => !NOISE_TYPES.has(event.event_type))
      setEvents((prev) => (opts?.append ? [...prev, ...rows] : rows))
      setCount(page.count ?? 0)
    } catch (failure) {
      setError(message(failure))
    } finally {
      setLoading(false)
    }
  }, [unit, scope, from, to, category, typeFilter, runFilter, taskFilter, actorFilter])

  useEffect(() => { void load() }, [load])

  const clearFilters = () => {
    setUnit(''); setScope('subtree'); setFrom(''); setTo(''); setCategory('')
    setTypeFilter(''); setRunFilter(''); setTaskFilter(''); setActorFilter('')
  }

  // Group events by run: one work item reads as a thread. Standalone events
  // (no run id) stay top-level, newest first — the server already pages desc.
  const groups = useMemo(() => {
    const byRun = new Map<string, OrgActivityEvent[]>()
    const standalone: OrgActivityEvent[] = []
    for (const event of events) {
      if (!event.run_id) { standalone.push(event); continue }
      const list = byRun.get(event.run_id) ?? []
      list.push(event)
      byRun.set(event.run_id, list)
    }
    return { byRun: [...byRun.entries()], standalone }
  }, [events])

  const toggleRun = (runID: string) => {
    setCollapsedRuns((prev) => {
      const next = new Set(prev)
      if (next.has(runID)) next.delete(runID)
      else next.add(runID)
      return next
    })
  }

  const canMore = events.length < count

  return (
    <div data-learn="page.org-activity" style={{ padding: embedded ? 0 : '1rem', maxWidth: embedded ? undefined : '86rem', margin: embedded ? undefined : '0 auto' }}>
      {error && (
        <InlineNotification kind="error" title={t('common.error')} subtitle={error} onClose={() => setError('')} lowContrast style={{ marginBottom: '1rem' }} />
      )}

      {!embedded && (
        <div style={{ marginBottom: '0.75rem' }}>
          <Heading style={{ fontSize: '1.1rem' }}>{t('orgops.activity_title')}</Heading>
          <p style={{ color: 'var(--tm-text-3)', fontSize: '0.8rem', margin: '0.25rem 0 0' }}>{t('orgops.activity_subtitle')}</p>
        </div>
      )}

      <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '0.5rem', padding: '0.5rem 0', borderBottom: '1px solid var(--tm-border)' }}>
        <OrgUnitSelect id="orgact-unit" compact value={unit} onChange={(v) => setUnit(v)} org={org} />
        <label className={`lens-toggle ${scope === 'subtree' ? 'on' : ''}`} title={t('orgops.scope_subtree')}>
          <input type="checkbox" checked={scope === 'subtree'} onChange={(e) => setScope(e.target.checked ? 'subtree' : 'unit')} />
          {t('orgops.include_children')}
        </label>
        <label className="toolbar-select">
          {t('orgops.period_from')}
          <input type="datetime-local" className="toolbar-search" style={{ width: 185 }} value={from} onChange={(e) => setFrom(e.target.value)} />
        </label>
        <label className="toolbar-select">
          {t('orgops.period_to')}
          <input type="datetime-local" className="toolbar-search" style={{ width: 185 }} value={to} onChange={(e) => setTo(e.target.value)} />
        </label>
        <label className="toolbar-select">
          {t('orgops.filter_category')}
          <select value={category} onChange={(e) => setCategory(e.target.value)}>
            <option value="">{t('orgops.filter_all')}</option>
            {CATEGORIES.map((c) => (
              <option key={c} value={c}>{t(CATEGORY_KEYS[c])}</option>
            ))}
          </select>
        </label>
        <input className="toolbar-search" placeholder={t('orgops.filter_type')} value={typeFilter} onChange={(e) => setTypeFilter(e.target.value)} title={t('orgops.filter_type_hint')} />
        <input className="toolbar-search" placeholder={t('orgops.filter_run')} value={runFilter} onChange={(e) => setRunFilter(e.target.value)} />
        <input className="toolbar-search" placeholder={t('orgops.filter_task')} value={taskFilter} onChange={(e) => setTaskFilter(e.target.value)} />
        <input className="toolbar-search" placeholder={t('orgops.filter_actor')} value={actorFilter} onChange={(e) => setActorFilter(e.target.value)} />
        <button className="toolbar-btn" onClick={() => void load()} disabled={loading} title={t('common.refresh')}>↻</button>
        <button className="toolbar-btn" onClick={clearFilters}>{t('common.clear_filters')}</button>
        <span style={{ marginLeft: 'auto', fontSize: '0.7rem', color: 'var(--tm-text-3)' }}>
          {count > 0 && pluralT(t, locale, 'orgops.events_n', count)}
        </span>
      </div>

      {loading ? <PageLoading /> : (
        <>
          {events.length === 0 ? (
            <div style={{ color: 'var(--tm-text-3)', fontSize: '0.8rem', padding: '1.5rem 0', textAlign: 'center' }}>
              {t('orgops.empty')}
            </div>
          ) : (
            <div style={{ marginTop: '0.75rem', border: '1px solid var(--tm-border)', borderRadius: 'var(--tm-radius-md)', overflow: 'hidden' }}>
              <GridHead>
                <span>{t('orgops.col_time')}</span>
                <span>{t('orgops.col_event')}</span>
                <span>{t('orgops.col_summary')}</span>
                <span>{t('orgops.col_actor')}</span>
                <span>{t('orgops.col_run')}</span>
                <span>{t('orgops.col_task')}</span>
                <span>{t('orgops.col_project')}</span>
              </GridHead>
              {groups.byRun.map(([runID, rows]) => {
                const collapsed = collapsedRuns.has(runID)
                return (
                  <div key={runID}>
                    <button
                      type="button"
                      onClick={() => toggleRun(runID)}
                      style={{ width: '100%', display: 'flex', alignItems: 'baseline', gap: '0.5rem', background: 'var(--tm-raised)', border: 'none', borderBottom: collapsed ? '1px solid var(--tm-border)' : 'none', padding: '0.35rem 0.6rem', cursor: 'pointer', fontSize: '0.74rem', color: 'var(--tm-text-2)', textAlign: 'left' }}
                    >
                      <span style={{ color: 'var(--tm-text-3)' }}>{collapsed ? '▸' : '▾'}</span>
                      <span>{pluralT(t, locale, 'orgops.group_n', rows.length, { run: runID })}</span>
                    </button>
                    {!collapsed && rows.map((event) => <EventRow key={event.event_id} event={event} userNameOf={userNameOf} onRun={openRun} onTask={openTask} />)}
                  </div>
                )
              })}
              {groups.standalone.map((event) => <EventRow key={event.event_id} event={event} userNameOf={userNameOf} onRun={openRun} onTask={openTask} />)}
            </div>
          )}

          {canMore && (
            <div style={{ display: 'flex', justifyContent: 'center', marginTop: '0.75rem' }}>
              <Button
                size="sm"
                kind="ghost"
                onClick={() => {
                  const next = offset + PAGE_SIZE
                  setOffset(next)
                  void load({ offset: next, append: true })
                }}
              >
                {t('orgops.load_more')}
              </Button>
            </div>
          )}
        </>
      )}
    </div>
  )
}
