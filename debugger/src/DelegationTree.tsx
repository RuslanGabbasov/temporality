import { useCallback, useEffect, useState } from 'react'
import { Tag } from '@carbon/react'
import { observationApi, type ObservationEvent } from './observationApi'
import { type Agent } from './workspaceApi'
import Markdown from './Markdown'
import { useT } from './i18n'

export interface DelegationNodeInfo {
  childRunId: string
  agentId: string
  ordinal: number
  status: 'completed' | 'failed' | 'running'
  turns?: number
  error?: string
}

interface ChildPayload {
  answer?: string | null
  error?: string | null
  children: DelegationNodeInfo[]
}

/** Fetch all delegation events for a run and fold them into node info. */
export async function fetchDelegations(project: string, runId: string): Promise<DelegationNodeInfo[]> {
  const types = ['delegation.started', 'delegation.completed', 'delegation.failed'] as const
  const pages = await Promise.all(types.map((type) =>
    observationApi.events(project, undefined, undefined, undefined, { run: runId, type, limit: 100 }).catch(() => ({ events: [] as ObservationEvent[], count: 0 }))
  ))
  const nodes = new Map<string, DelegationNodeInfo>()
  const order: string[] = []
  for (const event of pages.flatMap((p) => p.events)) {
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

async function fetchChildPayload(project: string, childRunId: string): Promise<ChildPayload> {
  const [answerPage, children] = await Promise.all([
    observationApi.events(project, undefined, undefined, undefined, { run: childRunId, type: 'agent.summary', limit: 1 }).catch(() => ({ events: [] as ObservationEvent[], count: 0 })),
    fetchDelegations(project, childRunId),
  ])
  const summary = answerPage.events[0]?.data as Record<string, any> | undefined
  return { answer: summary?.answer ?? null, error: null, children }
}

interface DelegationNodeProps {
  project: string
  info: DelegationNodeInfo
  agents: Agent[]
  depth: number
}

function DelegationNode({ project, info, agents, depth }: DelegationNodeProps) {
  const t = useT()
  const [open, setOpen] = useState(false)
  const [payload, setPayload] = useState<ChildPayload | null>(null)
  const [loading, setLoading] = useState(false)

  const agent = agents.find((a) => a.id === info.agentId)
  const agentTitle = agent?.name ?? info.agentId ?? t('chat.delegation.unknown_agent') ?? 'agent'

  const toggle = useCallback(() => {
    setOpen((prev) => {
      const next = !prev
      if (next && !payload && !loading) {
        setLoading(true)
        fetchChildPayload(project, info.childRunId)
          .then(setPayload)
          .catch(() => setPayload({ answer: null, error: 'failed to load', children: [] }))
          .finally(() => setLoading(false))
      }
      return next
    })
  }, [project, info.childRunId, payload, loading])

  const statusTag = info.status === 'completed'
    ? <Tag type="green" size="sm">{t('chat.delegation.completed') ?? 'completed'}</Tag>
    : info.status === 'failed'
      ? <Tag type="red" size="sm">{t('chat.delegation.failed') ?? 'failed'}</Tag>
      : <Tag type="blue" size="sm">{t('chat.delegation.running') ?? 'running'}</Tag>

  return (
    <div className={depth > 0 ? 'chat-delegation-nested' : undefined}>
      <button type="button" className="chat-delegation-row" onClick={toggle}>
        <span className="chat-delegation-arrow">{open ? '▾' : '▸'}</span>
        <span className="chat-delegation-agent">{agentTitle}</span>
        {statusTag}
        {typeof info.turns === 'number' && (
          <span className="chat-delegation-meta">{t('chat.delegation.turns', { count: String(info.turns) }) ?? `${info.turns} turns`}</span>
        )}
      </button>
      {open && (
        <div className="chat-delegation-body">
          {loading && <div className="chat-delegation-answer">{t('chat.delegation.loading') ?? 'Loading…'}</div>}
          {!loading && payload?.error && <div className="chat-delegation-error">{payload.error}</div>}
          {!loading && payload?.answer && <div className="chat-delegation-answer"><Markdown content={payload.answer} /></div>}
          {!loading && !payload?.answer && !payload?.error && (
            <div className="chat-delegation-answer">{t('chat.delegation.no_answer') ?? 'No answer recorded'}</div>
          )}
          {!loading && payload?.children.map((child) => (
            <DelegationNode key={child.childRunId} project={project} info={child} agents={agents} depth={depth + 1} />
          ))}
        </div>
      )}
    </div>
  )
}

/** Tree of delegations spawned by a run. Renders nothing when the run delegated nothing. */
export default function DelegationTree({ project, runId, agents }: { project: string; runId: string; agents: Agent[] }) {
  const t = useT()
  const [nodes, setNodes] = useState<DelegationNodeInfo[] | null>(null)

  useEffect(() => {
    let cancelled = false
    fetchDelegations(project, runId)
      .then((list) => { if (!cancelled) setNodes(list) })
      .catch(() => { if (!cancelled) setNodes([]) })
    return () => { cancelled = true }
  }, [project, runId])

  if (!nodes || nodes.length === 0) return null

  return (
    <div className="chat-delegation">
      <div className="chat-delegation-title">{t('chat.delegation.title') ?? 'Delegation'}</div>
      {nodes.map((info) => (
        <DelegationNode key={info.childRunId} project={project} info={info} agents={agents} depth={0} />
      ))}
    </div>
  )
}

