import { useCallback, useEffect, useRef, useState } from 'react'
import { Tag } from '@carbon/react'
import { observationApi, type ObservationEvent } from './observationApi'
import { type Agent } from './workspaceApi'
import Markdown from './Markdown'
import { useT } from './i18n'
import { eventSummary, shortTime, explainKernelError, restoreMarkdownLines } from './eventSummary'

export interface DelegationNodeInfo {
  childRunId: string
  agentId: string
  ordinal: number
  status: 'completed' | 'failed' | 'running'
  turns?: number
  error?: string
}

/** Fold delegation.* events (emitted by the parent run) into node info. */
export function foldDelegations(events: ObservationEvent[]): DelegationNodeInfo[] {
  const nodes = new Map<string, DelegationNodeInfo>()
  const order: string[] = []
  for (const event of events) {
    if (event.type !== 'delegation.started' && event.type !== 'delegation.completed' && event.type !== 'delegation.failed') continue
    const d = (event.data ?? {}) as Record<string, any>
    const child = d.child_run_id ?? ''
    if (!child) continue
    if (!nodes.has(child)) {
      nodes.set(child, { childRunId: child, agentId: d.agent_id ?? '', ordinal: d.ordinal ?? order.length + 1, status: 'running' })
      order.push(child)
    }
    const node = nodes.get(child)!
    if (event.type === 'delegation.started') {
      node.agentId = d.agent_id ?? node.agentId
      node.ordinal = d.ordinal ?? node.ordinal
    } else if (event.type === 'delegation.completed') {
      node.status = 'completed'
      node.turns = d.turns ?? node.turns
    } else if (event.type === 'delegation.failed') {
      node.status = 'failed'
      node.error = d.error ?? node.error
    }
  }
  return order.map((id) => nodes.get(id)!)
}

/** Fetch all delegation events for a run and fold them into node info. */
export async function fetchDelegations(project: string, runId: string): Promise<DelegationNodeInfo[]> {
  const types = ['delegation.started', 'delegation.completed', 'delegation.failed'] as const
  const pages = await Promise.all(types.map((type) =>
    observationApi.events(project, undefined, undefined, undefined, { run: runId, type, limit: 100 }).catch(() => ({ events: [] as ObservationEvent[], count: 0 }))
  ))
  return foldDelegations(pages.flatMap((p) => p.events))
}

/**
 * One request per child run gives everything: its own trace and status
 * (run.started/run.completed/run.failed), the answer (agent.summary) and the
 * delegation.* events of its grandchildren (the child emits them as parent).
 * `terminal` is the status from the parent's delegation events: the journal
 * returns oldest-first, so on very long runs the terminal/summary events can
 * fall off the page and the parent's view fills the gap.
 */
interface ChildPayload {
  events: ObservationEvent[]
  answer: string | null
  status: DelegationNodeInfo['status']
  error?: string
  children: DelegationNodeInfo[]
}

const TRACE_SKIP = new Set([
  'model.text_delta',
  'model.reasoning',
  'run.started', // the node header already says who started
  'agent.summary', // the answer is rendered separately
  'delegation.started', // grandchildren are rendered as nested nodes
  'delegation.completed',
  'delegation.failed',
])

async function fetchChildPayload(project: string, childRunId: string, terminal?: DelegationNodeInfo['status']): Promise<ChildPayload> {
  const page = await observationApi.events(project, undefined, undefined, undefined, { run: childRunId, limit: 500 })
    .catch(() => ({ events: [] as ObservationEvent[], count: 0 }))
  const events = page.events
  const failed = events.find((e) => e.type === 'run.failed')
  const completed = events.find((e) => e.type === 'run.completed')
  const ownStatus: DelegationNodeInfo['status'] = failed ? 'failed' : completed ? 'completed' : 'running'
  const status = ownStatus !== 'running' ? ownStatus : (terminal ?? 'running')
  let summary = events.find((e) => e.type === 'agent.summary')?.data as Record<string, any> | undefined
  if (!summary && status !== 'running') {
    // The events page may have been truncated before the summary — a typed
    // query finds it regardless of run length.
    const answerPage = await observationApi.events(project, undefined, undefined, undefined, { run: childRunId, type: 'agent.summary', limit: 1 })
      .catch(() => ({ events: [] as ObservationEvent[], count: 0 }))
    summary = answerPage.events[0]?.data as Record<string, any> | undefined
  }
  // Older journal events carry the answer newline-collapsed; restore the
  // markdown structure for display (new events keep their line breaks).
  const answer = typeof summary?.answer === 'string' && summary.answer.trim() ? restoreMarkdownLines(summary.answer) : null
  return {
    events,
    answer,
    status,
    error: failed ? String((failed.data as Record<string, any> | undefined)?.error ?? '') || undefined : undefined,
    children: foldDelegations(events),
  }
}

interface DelegationNodeProps {
  project: string
  info: DelegationNodeInfo
  agents: Agent[]
  depth: number
  onOpenRun?: (runId: string) => void
}

function DelegationNode({ project, info, agents, depth, onOpenRun }: DelegationNodeProps) {
  const t = useT()
  const [open, setOpen] = useState(false)
  const [payload, setPayload] = useState<ChildPayload | null>(null)
  const [loading, setLoading] = useState(false)
  const traceRef = useRef<HTMLDivElement | null>(null)

  const agent = agents.find((a) => a.id === info.agentId)
  const agentTitle = agent?.name ?? info.agentId ?? t('chat.delegation.unknown_agent')
  const status = payload?.status ?? info.status
  const live = status === 'running'
  const turns = payload?.status === 'completed'
    ? ((payload.events.find((e) => e.type === 'run.completed')?.data as Record<string, any> | undefined)?.turns ?? info.turns)
    : info.turns

  const load = useCallback(() => {
    setLoading(true)
    return fetchChildPayload(project, info.childRunId, info.status)
      .then(setPayload)
      .catch(() => setPayload({ events: [], answer: null, status: info.status, children: [] }))
      .finally(() => setLoading(false))
  }, [project, info.childRunId, info.status])

  // Load on expand; while the child is running keep polling so the trace,
  // answer and status stay live.
  useEffect(() => {
    if (!open) return
    void load()
    if (!live) return
    const timer = window.setInterval(() => { void load() }, 5000)
    return () => window.clearInterval(timer)
  }, [open, live, load])

  // Keep the activity feed pinned to the newest line while the child runs.
  useEffect(() => {
    const el = traceRef.current
    if (el && live) el.scrollTop = el.scrollHeight
  }, [payload, live])

  const toggle = useCallback(() => { setOpen((prev) => !prev) }, [])

  const statusTag = status === 'completed'
    ? <Tag type="green" size="sm">{t('chat.delegation.completed')}</Tag>
    : status === 'failed'
      ? <Tag type="red" size="sm">{t('chat.delegation.failed')}</Tag>
      : <Tag type="blue" size="sm">{t('chat.delegation.running')}</Tag>

  const openRun = (e: React.MouseEvent) => {
    e.stopPropagation()
    if (onOpenRun) onOpenRun(info.childRunId)
    else window.location.assign(`/agents?project=${encodeURIComponent(project)}&run=${encodeURIComponent(info.childRunId)}`)
  }

  const traceEvents = (payload?.events ?? []).filter((event) => !TRACE_SKIP.has(event.type))

  return (
    <div className={depth > 0 ? 'chat-delegation-nested' : undefined}>
      <div
        className="chat-delegation-row"
        role="button"
        tabIndex={0}
        onClick={toggle}
        onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); toggle() } }}
      >
        <span className="chat-delegation-arrow">{open ? '▾' : '▸'}</span>
        <span className="chat-delegation-agent">{agentTitle}</span>
        {statusTag}
        {typeof turns === 'number' && (
          <span className="chat-delegation-meta">{t('chat.delegation.turns', { count: String(turns) })}</span>
        )}
        <button
          type="button"
          className="chat-delegation-open"
          onClick={openRun}
          title={t('chat.delegation.open_run')}
        >↗</button>
      </div>
      {open && (
        <div className="chat-delegation-body">
          {loading && !payload && <div className="chat-delegation-answer">{t('chat.delegation.loading')}</div>}
          {payload && (
            <div className="chat-delegation-activity">
              <div className="chat-delegation-activity-title">{t('chat.delegation.activity')}</div>
              <div className="chat-delegation-trace" ref={traceRef}>
                {traceEvents.map((event) => {
                  const line = eventSummary(event, t)
                  return (
                    <div key={`${event.source.id}:${event.event_id}`} className="chat-delegation-trace-row">
                      <span className="chat-delegation-trace-icon" style={{ color: line.color }}>{line.icon}</span>
                      <span className="chat-delegation-trace-label" style={{ color: line.color }}>{line.label}</span>
                      <span className="chat-delegation-trace-detail">{line.detail}</span>
                      <span className="chat-delegation-trace-time">{shortTime(event.occurred_at)}</span>
                    </div>
                  )
                })}
                {traceEvents.length === 0 && live && <div className="chat-delegation-answer">{t('chat.delegation.loading')}</div>}
              </div>
            </div>
          )}
          {!loading && payload?.answer && (
            <div className="chat-delegation-answer">
              <div className="chat-delegation-answer-title">{t('chat.delegation.answer')}</div>
              <Markdown content={payload.answer} />
            </div>
          )}
          {!loading && payload && !payload.answer && status === 'completed' && (
            <div className="chat-delegation-answer">{t('chat.delegation.no_answer')}</div>
          )}
          {status === 'failed' && (payload?.error || info.error) && (
            <div className="chat-delegation-error" title={payload?.error || info.error}>
              {explainKernelError(payload?.error || info.error || '').cause}
            </div>
          )}
          {payload?.children.map((child) => (
            <DelegationNode key={child.childRunId} project={project} info={child} agents={agents} depth={depth + 1} onOpenRun={onOpenRun} />
          ))}
        </div>
      )}
    </div>
  )
}

/**
 * Tree of delegations spawned by a run. Renders nothing when the run delegated
 * nothing. While `live` (parent run still running) or while any child is
 * running, the tree refreshes itself every few seconds.
 */
export default function DelegationTree({ project, runId, agents, live = false, onOpenRun }: {
  project: string
  runId: string
  agents: Agent[]
  live?: boolean
  onOpenRun?: (runId: string) => void
}) {
  const t = useT()
  const [nodes, setNodes] = useState<DelegationNodeInfo[] | null>(null)

  const refresh = useCallback(() => fetchDelegations(project, runId).then(setNodes).catch(() => setNodes([])), [project, runId])
  const hasRunning = Boolean(nodes?.some((node) => node.status === 'running'))

  useEffect(() => {
    void refresh()
    if (!live && !hasRunning) return
    const timer = window.setInterval(() => { void refresh() }, 5000)
    return () => window.clearInterval(timer)
  }, [refresh, live, hasRunning])

  if (!nodes || nodes.length === 0) return null

  return (
    <div className="chat-delegation">
      <div className="chat-delegation-title">{t('chat.delegation.title')}</div>
      {nodes.map((info) => (
        <DelegationNode key={info.childRunId} project={project} info={info} agents={agents} depth={0} onOpenRun={onOpenRun} />
      ))}
    </div>
  )
}
