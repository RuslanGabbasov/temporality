import { useCallback, useEffect, useId, useMemo, useRef, useState } from 'react'
import { Tag } from '@carbon/react'
import { observationApi, type ObservationEvent } from './observationApi'
import { type Agent } from './workspaceApi'
import { useT } from './i18n'
import { explainKernelError } from './eventSummary'

export type PlanTaskStatus = 'pending' | 'running' | 'completed' | 'failed' | 'skipped' | 'invalidated'

export interface PlanTaskInfo {
  id: string
  agentId: string
  dependsOn: string[]
  reviewOf?: string[]
  status: PlanTaskStatus
  childRunId?: string
  turns?: number
  round?: number
  verdict?: string
  error?: string
  blockedBy?: string
  reason?: string
  rejectedBy?: string
  feedback?: string
}

export interface PlanInfo {
  operationId: string
  goal: string
  tasks: PlanTaskInfo[]
}

// Edge color follows the source (dependency) status — the same palette the
// trace rows use, so a green edge means "input ready", red means "this is
// what blocks the branch".
const EDGE_COLORS: Record<PlanTaskStatus, string> = {
  completed: '#9ece6a',
  failed: '#f7768e',
  running: '#7aa2f7',
  pending: '#565f89',
  skipped: '#565f89',
  invalidated: '#e6b85c',
}

/** Longest-path layering: a task's column is one past its deepest dependency. */
function taskLayers(tasks: PlanTaskInfo[]): Map<string, number> {
  const layers = new Map<string, number>()
  const byId = new Map(tasks.map((task) => [task.id, task]))
  const visit = (task: PlanTaskInfo, seen: Set<string>): number => {
    const known = layers.get(task.id)
    if (known !== undefined) return known
    let depth = 0
    if (!seen.has(task.id)) {
      seen.add(task.id)
      for (const dep of task.dependsOn) {
        const upstream = byId.get(dep)
        if (upstream) depth = Math.max(depth, visit(upstream, seen) + 1)
      }
    }
    layers.set(task.id, depth)
    return depth
  }
  for (const task of tasks) visit(task, new Set())
  return layers
}

/**
 * Fold plan.* events (emitted by the parent run) into per-plan DAG info.
 * A run may hold several plans (re-plans); each is kept separate, keyed by
 * operation_id in start order.
 */
export function foldPlans(events: ObservationEvent[]): PlanInfo[] {
  const plans = new Map<string, PlanInfo>()
  const order: string[] = []
  for (const event of events) {
    const d = (event.data ?? {}) as Record<string, any>
    const operationId = typeof d.operation_id === 'string' ? d.operation_id : ''
    if (event.type === 'plan.started') {
      const key = operationId || `plan-${order.length + 1}`
      if (!plans.has(key)) {
        plans.set(key, { operationId: key, goal: typeof d.goal === 'string' ? d.goal : '', tasks: [] })
        order.push(key)
      }
      const plan = plans.get(key)!
      const seen = new Set(plan.tasks.map((task) => task.id))
      for (const raw of Array.isArray(d.tasks) ? d.tasks : []) {
        const task = (raw ?? {}) as Record<string, any>
        const id = String(task.id ?? '')
        if (!id || seen.has(id)) continue
        seen.add(id)
        plan.tasks.push({
          id,
          agentId: String(task.agent_id ?? ''),
          dependsOn: Array.isArray(task.depends_on) ? task.depends_on.map(String) : [],
          reviewOf: Array.isArray(task.review_of) ? task.review_of.map(String) : undefined,
          status: 'pending',
        })
      }
      continue
    }
    if (!operationId) continue
    // Task updates may arrive for a plan whose plan.started fell off the page —
    // synthesize the shell so the outcome is still visible.
    const key = plans.has(operationId) ? operationId : (() => {
      plans.set(operationId, { operationId, goal: '', tasks: [] })
      order.push(operationId)
      return operationId
    })()
    const plan = plans.get(key)!
    const taskID = String(d.task_id ?? '')
    if (!taskID) continue
    let task = plan.tasks.find((item) => item.id === taskID)
    if (!task) {
      task = { id: taskID, agentId: String(d.agent_id ?? ''), dependsOn: [], status: 'pending' }
      plan.tasks.push(task)
    }
    if (typeof d.agent_id === 'string' && d.agent_id && !task.agentId) task.agentId = d.agent_id
    if (typeof d.child_run_id === 'string' && d.child_run_id) task.childRunId = d.child_run_id
    if (event.type === 'plan.task.started') {
      task.status = 'running'
      if (d.round != null) task.round = Number(d.round)
      if (Array.isArray(d.review_of) && d.review_of.length) task.reviewOf = d.review_of.map(String)
    } else if (event.type === 'plan.task.completed') {
      task.status = 'completed'
      task.turns = d.turns ?? task.turns
      if (d.round != null) task.round = Number(d.round)
      if (typeof d.verdict === 'string') task.verdict = d.verdict
    } else if (event.type === 'plan.task.failed') {
      task.status = 'failed'
      task.error = typeof d.error === 'string' ? d.error : task.error
      if (d.round != null) task.round = Number(d.round)
    } else if (event.type === 'plan.task.skipped') {
      task.status = 'skipped'
      task.reason = typeof d.reason === 'string' ? d.reason : task.reason
      task.blockedBy = typeof d.blocked_by === 'string' ? d.blocked_by : task.blockedBy
    } else if (event.type === 'plan.task.rejected') {
      if (typeof d.rejected_by === 'string') task.rejectedBy = d.rejected_by
      if (typeof d.feedback === 'string') task.feedback = d.feedback
    } else if (event.type === 'plan.task.reopened') {
      // Back to the gate: the task waits for its rework launch.
      task.status = 'pending'
      if (d.round != null) task.round = Number(d.round)
    } else if (event.type === 'plan.task.invalidated') {
      task.status = 'invalidated'
      task.reason = typeof d.reason === 'string' ? d.reason : task.reason
    }
  }
  return order.map((key) => plans.get(key)!).filter((plan) => plan.tasks.length > 0)
}

/** Fetch all plan events for a run and fold them into per-plan DAG info. */
export async function fetchPlans(project: string, runId: string): Promise<PlanInfo[]> {
  const types = ['plan.started', 'plan.task.started', 'plan.task.completed', 'plan.task.failed', 'plan.task.skipped', 'plan.task.rejected', 'plan.task.reopened', 'plan.task.invalidated'] as const
  const pages = await Promise.all(types.map((type) =>
    observationApi.events(project, undefined, undefined, undefined, { run: runId, type, limit: 100 }).catch(() => ({ events: [] as ObservationEvent[], count: 0 }))
  ))
  // Typed pages arrive grouped by type — sort back into journal order so the
  // fold sees statuses in the sequence they actually happened.
  const events = pages.flatMap((page) => page.events).sort((left, right) => left.occurred_at.localeCompare(right.occurred_at))
  return foldPlans(events)
}

/** Mini status glyph of a dependency, so a pending task shows how ready its inputs are. */
function depStatusIcon(status: PlanTaskStatus | undefined): { icon: string; color: string } {
  switch (status) {
    case 'completed': return { icon: '✓', color: '#9ece6a' }
    case 'failed': return { icon: '✗', color: '#f7768e' }
    case 'running': return { icon: '●', color: '#7aa2f7' }
    case 'skipped': return { icon: '–', color: '#565f89' }
    case 'invalidated': return { icon: '↺', color: '#e6b85c' }
    default: return { icon: '○', color: '#565f89' }
  }
}

function PlanTaskChip({ task, agents, t, onOpenRun, statuses, chipRef }: {
  task: PlanTaskInfo
  agents: Agent[]
  t: ReturnType<typeof useT>
  onOpenRun?: (runId: string) => void
  statuses: Map<string, PlanTaskStatus>
  chipRef?: (el: HTMLDivElement | null) => void
}) {
  const agent = agents.find((a) => a.id === task.agentId)
  const agentTitle = agent?.name ?? task.agentId ?? t('chat.delegation.unknown_agent')
  const skipped = task.status === 'skipped'
  const failed = task.status === 'failed'
  const running = task.status === 'running'
  const pending = task.status === 'pending'
  const invalidated = task.status === 'invalidated'
  const tag = failed
    ? <Tag type="red" size="sm">{t('chat.delegation.failed')}</Tag>
    : task.status === 'completed'
      ? <Tag type="green" size="sm">{t('chat.delegation.completed')}</Tag>
      : running
        ? <Tag type="blue" size="sm">{t('chat.delegation.running')}</Tag>
        : skipped
          ? <Tag type="warm-gray" size="sm">{t('chat.plan.skipped')}</Tag>
          : invalidated
            ? <Tag type="warm-gray" size="sm">{t('chat.plan.status_invalidated')}</Tag>
            : <Tag type="warm-gray" size="sm">{t('chat.plan.pending')}</Tag>
  const reason = skipped
    ? task.reason === 'run_time_limit' || task.reason === 'execution_budget'
      ? t(task.reason === 'execution_budget' ? 'chat.plan.execution_budget' : 'chat.plan.run_time_limit')
      : `${t('chat.plan.upstream_failed')}${task.blockedBy ? ` · ${task.blockedBy}` : ''}`
    : failed
      ? explainKernelError(task.error ?? '').cause
      : invalidated
        ? t(task.reason === 'upstream_rework_exhausted' ? 'chat.plan.upstream_rework_exhausted' : 'chat.plan.upstream_rework')
        : task.feedback
          ? `${t('runs.event.plan_task_rejected')} · ${task.rejectedBy ?? ''}: ${task.feedback}`
          : ''
  const open = (e: React.MouseEvent) => {
    e.stopPropagation()
    if (onOpenRun && task.childRunId) onOpenRun(task.childRunId)
  }
  return (
    <div
      ref={chipRef}
      className={`plan-task ${skipped ? 'plan-task-skipped' : ''} ${running ? 'plan-task-running' : ''} ${task.childRunId ? 'plan-task-openable' : ''}`}
      title={[reason, task.childRunId].filter(Boolean).join('\n')}
      role={task.childRunId ? 'button' : undefined}
      tabIndex={task.childRunId ? 0 : undefined}
      onClick={task.childRunId ? open : undefined}
      onKeyDown={task.childRunId ? (e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); open(e as unknown as React.MouseEvent) } } : undefined}
    >
      <div className="plan-task-main">
        <span className="plan-task-id">{task.id}</span>
        {task.reviewOf && task.reviewOf.length > 0 && (
          <span className="plan-task-meta" title={t('chat.plan.reviews')}>⌾ {task.reviewOf.join(', ')}</span>
        )}
        <span className="plan-task-agent">{agentTitle}</span>
        {tag}
        {typeof task.round === 'number' && task.round > 1 && (
          <span className="plan-task-meta">×{task.round}</span>
        )}
        {task.status === 'completed' && task.verdict === 'accept' && <span className="plan-task-meta" style={{ color: '#9ece6a' }}>✓</span>}
        {task.status === 'completed' && task.verdict === 'rework' && <span className="plan-task-meta" style={{ color: '#e6b85c' }}>↺</span>}
        {task.status === 'completed' && typeof task.turns === 'number' && (
          <span className="plan-task-meta">{t('chat.delegation.turns', { count: String(task.turns) })}</span>
        )}
      </div>
      {(pending || skipped) && task.dependsOn.length > 0 && (
        <div className="plan-task-waits">
          <span className="plan-task-waits-label">{t('chat.plan.waits')}</span>
          {task.dependsOn.map((dep) => {
            const depStatus = depStatusIcon(statuses.get(dep))
            return (
              <span key={dep} className="plan-task-dep">
                <span style={{ color: depStatus.color }}>{depStatus.icon}</span> {dep}
              </span>
            )
          })}
        </div>
      )}
    </div>
  )
}

/** One dependency edge in canvas coordinates (measured from the DOM). */
interface PlanEdge {
  key: string
  x1: number
  y1: number
  x2: number
  y2: number
  status: PlanTaskStatus
}

/**
 * One plan's DAG: layered task chips plus SVG dependency edges drawn between
 * the chips. Edge geometry is measured from the DOM after layout and kept in
 * sync via a ResizeObserver (live statuses resize chips; the canvas reflows).
 */
function PlanDAG({ plan, planIndex, planCount, agents, t, onOpenRun }: {
  plan: PlanInfo
  planIndex: number
  planCount: number
  agents: Agent[]
  t: ReturnType<typeof useT>
  onOpenRun?: (runId: string) => void
}) {
  const statuses = useMemo(() => new Map(plan.tasks.map((task) => [task.id, task.status])), [plan])
  const columns = useMemo(() => {
    const layers = taskLayers(plan.tasks)
    const cols: PlanTaskInfo[][] = []
    for (const task of plan.tasks) {
      const layer = layers.get(task.id) ?? 0
      while (cols.length <= layer) cols.push([])
      cols[layer].push(task)
    }
    return cols
  }, [plan])

  const canvasRef = useRef<HTMLDivElement | null>(null)
  const chipRefs = useRef(new Map<string, HTMLDivElement>())
  const [edges, setEdges] = useState<PlanEdge[]>([])
  const [size, setSize] = useState({ width: 0, height: 0 })
  const markerId = useId()

  const measure = useCallback(() => {
    const canvas = canvasRef.current
    if (!canvas) return
    const cRect = canvas.getBoundingClientRect()
    const next: PlanEdge[] = []
    for (const task of plan.tasks) {
      for (const dep of task.dependsOn) {
        const from = chipRefs.current.get(dep)
        const to = chipRefs.current.get(task.id)
        if (!from || !to) continue
        const f = from.getBoundingClientRect()
        const o = to.getBoundingClientRect()
        if (f.width === 0 || o.width === 0) continue
        next.push({
          key: `${dep}→${task.id}`,
          x1: f.right - cRect.left,
          y1: f.top + f.height / 2 - cRect.top,
          x2: o.left - cRect.left,
          y2: o.top + o.height / 2 - cRect.top,
          status: statuses.get(dep) ?? 'pending',
        })
      }
    }
    setEdges(next)
    setSize({ width: canvas.scrollWidth, height: canvas.scrollHeight })
  }, [plan, statuses])

  useEffect(() => {
    measure()
    const canvas = canvasRef.current
    if (!canvas || typeof ResizeObserver === 'undefined') return
    const observer = new ResizeObserver(() => measure())
    observer.observe(canvas)
    return () => observer.disconnect()
  }, [measure])

  return (
    <div className="chat-plan-graph">
      <div className="chat-plan-goal">
        {plan.goal || t('chat.plan.untitled')}
        {planCount > 1 && <span className="chat-plan-meta"> · #{planIndex + 1}</span>}
      </div>
      <div className="chat-plan-scroll">
        <div className="chat-plan-canvas" ref={canvasRef}>
          <svg className="plan-edges" width={size.width} height={size.height} aria-hidden="true">
            <defs>
              {(Object.keys(EDGE_COLORS) as PlanTaskStatus[]).map((status) => (
                <marker
                  key={status}
                  id={`${markerId}-${status}`}
                  viewBox="0 0 8 8"
                  refX="6.5"
                  refY="4"
                  markerWidth="4.5"
                  markerHeight="4.5"
                  orient="auto"
                >
                  <path d="M0,1 L7,4 L0,7 z" fill={EDGE_COLORS[status]} />
                </marker>
              ))}
            </defs>
            {edges.map((edge) => {
              const dx = Math.min(Math.max((edge.x2 - edge.x1) / 2, 12), 40)
              const d = `M ${edge.x1 + 2} ${edge.y1} C ${edge.x1 + 2 + dx} ${edge.y1}, ${edge.x2 - 2 - dx} ${edge.y2}, ${edge.x2 - 3} ${edge.y2}`
              return (
                <path
                  key={edge.key}
                  d={d}
                  fill="none"
                  stroke={EDGE_COLORS[edge.status]}
                  strokeWidth={1}
                  strokeDasharray={edge.status === 'skipped' || edge.status === 'invalidated' ? '3 3' : undefined}
                  markerEnd={`url(#${markerId}-${edge.status})`}
                  opacity={0.8}
                />
              )
            })}
          </svg>
          <div className="chat-plan-columns">
            {columns.map((column, columnIndex) => (
              <div key={columnIndex} className="chat-plan-column">
                {column.map((task) => (
                  <PlanTaskChip
                    key={task.id}
                    task={task}
                    agents={agents}
                    t={t}
                    onOpenRun={onOpenRun}
                    statuses={statuses}
                    chipRef={(el) => { if (el) chipRefs.current.set(task.id, el); else chipRefs.current.delete(task.id) }}
                  />
                ))}
              </div>
            ))}
          </div>
        </div>
      </div>
    </div>
  )
}

/**
 * DAG view of the plans executed by a run: one graph per plan call, tasks
 * layered left→right (a task sits one column past its deepest dependency)
 * with dependency edges colored by the upstream status. Renders nothing when
 * the run never called plan.
 */
export default function PlanGraph({ project, runId, agents, live = false, onOpenRun }: {
  project: string
  runId: string
  agents: Agent[]
  live?: boolean
  onOpenRun?: (runId: string) => void
}) {
  const t = useT()
  const [plans, setPlans] = useState<PlanInfo[] | null>(null)

  const refresh = useCallback(() => fetchPlans(project, runId).then(setPlans).catch(() => setPlans([])), [project, runId])
  const hasRunning = Boolean(plans?.some((plan) => plan.tasks.some((task) => task.status === 'running' || task.status === 'pending')))

  useEffect(() => {
    void refresh()
    if (!live && !hasRunning) return
    const timer = window.setInterval(() => { void refresh() }, 5000)
    return () => window.clearInterval(timer)
  }, [refresh, live, hasRunning])

  if (!plans || plans.length === 0) return null

  return (
    <div className="chat-plan">
      <div className="chat-plan-title">{t('chat.plan.title')}</div>
      {plans.map((plan, index) => (
        <PlanDAG key={plan.operationId} plan={plan} planIndex={index} planCount={plans.length} agents={agents} t={t} onOpenRun={onOpenRun} />
      ))}
    </div>
  )
}
