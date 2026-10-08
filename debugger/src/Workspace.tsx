import { useCallback, useEffect, useState, useRef } from 'react'
import {
  Button,
  TextInput,
  TextArea,
  Select,
  SelectItem,
  InlineNotification,
  Tag,
  Heading,
  Modal,
} from '@carbon/react'
import { Add, Send, TrashCan } from '@carbon/icons-react'
import { workspaceApi, type Agent } from './workspaceApi'
import Markdown from './Markdown'
import DelegationTree from './DelegationTree'
import PlanGraph from './PlanGraph'
import ListFilter, { matchesFilter } from './ListFilter'
import { useT } from './i18n'

function message(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }
function shortTime(iso: string) {
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? '' : d.toLocaleTimeString()
}

const STATUS_COLORS: Record<string, 'blue' | 'green' | 'warm-gray' | 'gray' | 'red'> = {
  completed: 'green', failed: 'red', running: 'blue', turn_limit: 'warm-gray', pending: 'gray',
  started: 'blue', cancelled: 'warm-gray',
  time_limit: 'warm-gray', token_limit: 'warm-gray', budget_limit: 'warm-gray',
}

interface ChatMessage {
  role: 'user' | 'assistant'
  content: string
  reasoning?: string
  timestamp: Date
  runId?: string
  status?: string
  streamLines?: string[]
}

interface Conversation {
  id: string
  title: string
  messages: ChatMessage[]
  agentId: string
  taskId: string
  createdAt: Date
}

const STORAGE_KEY = 'temporality_chats'

function loadConversations(): Conversation[] {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (!raw) return []
    return JSON.parse(raw).map((c: any) => ({
      ...c,
      taskId: c.taskId ?? '',
      agentId: c.agentId ?? '',
      createdAt: new Date(c.createdAt),
      messages: c.messages.map((m: any) => ({ ...m, timestamp: new Date(m.timestamp) })),
    }))
  } catch { return [] }
}

function saveConversations(convs: Conversation[]) {
  localStorage.setItem(STORAGE_KEY, JSON.stringify(convs))
}

/** Format a stream event into a human-readable status line. */
function formatStreamEvent(type: string, outer: any): string | null {
  const d = outer.data ?? outer
  switch (type) {
    case 'run.started':
      return `Run started${d.model ? ` · model ${d.model}` : ''}`
    case 'model.completed': {
      const tokens = d.total_tokens ? ` · ${d.total_tokens} tokens` : ''
      const latency = d.latency_ms ? ` · ${(d.latency_ms / 1000).toFixed(1)}s` : ''
      return `Model turn ${d.turn ?? ''}${tokens}${latency}`
    }
    case 'turn.completed':
      return `Turn ${d.turn ?? ''} done${d.tool_calls ? ` · ${d.tool_calls} tool calls` : ''}`
    case 'tool.completed':
      return `Tool: ${d.name ?? d.tool ?? '?'}`
    case 'knowledge.proposed':
      return `Learned: ${(d.proposition ?? '').slice(0, 60)}`
    case 'run.completed':
      return `Completed · ${d.turns ?? '?'} turns`
    case 'run.failed':
      return `Failed: ${d.error ?? 'unknown'}`
    default:
      return null
  }
}

/** Live-activity feed with capped height. Auto-scrolls to the newest line
 * unless the user has scrolled up to read earlier activity. */
function ChatStreamLines({ lines }: { lines: string[] }) {
  const ref = useRef<HTMLDivElement>(null)
  const mounted = useRef(false)
  useEffect(() => {
    const el = ref.current
    if (!el) return
    if (!mounted.current || el.scrollHeight - el.scrollTop - el.clientHeight < 48) {
      el.scrollTop = el.scrollHeight
    }
    mounted.current = true
  }, [lines.length])
  return (
    <div ref={ref} className="chat-activity-scroll">
      {lines.map((line, li) => (
        <div key={li} className="chat-stream-line">
          <span className="chat-stream-prefix">›</span>{line}
        </div>
      ))}
    </div>
  )
}

export default function Workspace({ project, defaultAgentId, defaultModel }: { project: string; defaultAgentId?: string; defaultModel?: string }) {
  const t = useT()
  const [allAgents, setAllAgents] = useState<Agent[]>([])
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)

  // Chat state
  const [conversations, setConversations] = useState<Conversation[]>(() => loadConversations())
  const [activeConvId, setActiveConvId] = useState<string | null>(null)
  const [inputValue, setInputValue] = useState('')
  const [showNewChat, setShowNewChat] = useState(false)
  const [newChatAgentId, setNewChatAgentId] = useState('')
  const chatEndRef = useRef<HTMLDivElement>(null)
  const streamRef = useRef<Map<string, EventSource>>(new Map())
  // Live token streams (ephemeral /runs/{id}/tokens SSE), one per conversation.
  const tokenStreamRef = useRef<Map<string, EventSource>>(new Map())

  const activeConv = conversations.find((c) => c.id === activeConvId) ?? null

  useEffect(() => { saveConversations(conversations) }, [conversations])
  useEffect(() => () => {
    streamRef.current.forEach((es) => es.close()); streamRef.current.clear()
    tokenStreamRef.current.forEach((es) => es.close()); tokenStreamRef.current.clear()
  }, [])

  // Reconnect SSE streams for in-progress messages on mount (e.g. after
  // navigating away and back to the Workspace tab). Also poll for stale
  // running messages as a safety net.
  useEffect(() => {
    for (const conv of conversations) {
      for (const msg of conv.messages) {
        if (msg.status === 'running' && msg.runId && !streamRef.current.has(conv.id)) {
          streamRun(msg.runId, conv.id)
        }
      }
    }
    // Poll for stale running messages every 30s
    const timer = window.setInterval(() => {
      setConversations((prev) => {
        for (const conv of prev) {
          for (const msg of conv.messages) {
            if (msg.status === 'running' && msg.runId && !streamRef.current.has(conv.id)) {
              void fetchFinalAnswer(msg.runId, conv.id)
            }
          }
        }
        return prev
      })
    }, 30000)
    return () => window.clearInterval(timer)
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  const loadAllAgents = useCallback(async () => {
    try {
      const data = await workspaceApi.listAllAgents()
      setAllAgents(data.agents ?? [])
    } catch { /* ignore */ }
  }, [])

  useEffect(() => { void loadAllAgents() }, [loadAllAgents])

  const scrollToBottom = () => { chatEndRef.current?.scrollIntoView({ behavior: 'smooth' }) }
  useEffect(scrollToBottom, [activeConv?.messages])

  const agentName = (id: string) => allAgents.find((a) => a.id === id)?.name ?? (t('chat.default_agent') ?? 'Default')

  // Client-side filter for the conversation list sidebar.
  const [convFilter, setConvFilter] = useState('')
  const visibleConversations = conversations.filter((conv) => matchesFilter(convFilter, conv.title, conv.agentId ? agentName(conv.agentId) : ''))

  const createConversation = () => {
    const conv: Conversation = {
      id: crypto.randomUUID(),
      title: t('new.conversation') ?? 'New conversation',
      messages: [],
      agentId: newChatAgentId,
      taskId: '',
      createdAt: new Date(),
    }
    setConversations((prev) => [conv, ...prev])
    setActiveConvId(conv.id)
    setShowNewChat(false)
    setNewChatAgentId('')
  }

  const deleteConversation = (id: string) => {
    const es = streamRef.current.get(id)
    if (es) { es.close(); streamRef.current.delete(id) }
    const tes = tokenStreamRef.current.get(id)
    if (tes) { tes.close(); tokenStreamRef.current.delete(id) }
    setConversations((prev) => prev.filter((c) => c.id !== id))
    if (activeConvId === id) setActiveConvId(null)
  }

  const branchConversation = (convId: string, messageIndex: number) => {
    const conv = conversations.find((c) => c.id === convId)
    if (!conv) return
    // Create a new conversation with messages up to (and including) the clicked message
    const branchMessages = conv.messages.slice(0, messageIndex + 1)
    const branch: Conversation = {
      id: crypto.randomUUID(),
      title: conv.title + ' (branch)',
      messages: branchMessages,
      agentId: conv.agentId,
      taskId: '',
      createdAt: new Date(),
    }
    setConversations((prev) => [branch, ...prev])
    setActiveConvId(branch.id)
  }

  const updateMsg = (convId: string, runId: string, patch: Partial<ChatMessage>) => {
    setConversations((prev) => prev.map((c) => {
      if (c.id !== convId) return c
      return { ...c, messages: c.messages.map((m) => m.runId === runId ? { ...m, ...patch } : m) }
    }))
  }

  const streamRun = (runId: string, convId: string) => {
    const token = localStorage.getItem('temporality_token') ?? ''
    const es = new EventSource(`/kernel-api/v1/workspace/runs/${encodeURIComponent(runId)}/stream${token ? `?token=${token}` : ''}`)
    streamRef.current.set(convId, es)
    const lines: string[] = []

    // Live token stream (ephemeral SSE, never journaled): preview the model's
    // answer and reasoning while it is being generated. The journal's
    // model.text_delta / model.reasoning events remain the authoritative
    // per-turn text and replace the live preview once the turn completes.
    const closeTokenStream = () => {
      const tes = tokenStreamRef.current.get(convId)
      if (tes) { tes.close(); tokenStreamRef.current.delete(convId) }
    }
    const tes = new EventSource(`/kernel-api/v1/workspace/runs/${encodeURIComponent(runId)}/tokens${token ? `?token=${token}` : ''}`)
    tokenStreamRef.current.set(convId, tes)
    let liveTurn = -1
    let liveText = ''
    let liveReasoning = ''
    const applyLive = (data: { type?: string; turn?: number; text?: string; reasoning?: string }) => {
      if (data.type === 'done') { closeTokenStream(); return }
      const turn = Number(data.turn ?? 0)
      if (turn !== liveTurn) {
        liveTurn = turn
        liveText = ''
        liveReasoning = ''
      }
      if (data.text) liveText += data.text
      if (data.reasoning) liveReasoning += data.reasoning
      updateMsg(convId, runId, { content: liveText, reasoning: liveReasoning })
    }
    tes.addEventListener('snapshot', (e) => {
      try { applyLive(JSON.parse((e as MessageEvent).data)) } catch { /* ignore */ }
    })
    tes.addEventListener('delta', (e) => {
      try { applyLive(JSON.parse((e as MessageEvent).data)) } catch { /* ignore */ }
    })
    tes.addEventListener('done', closeTokenStream)
    tes.onerror = closeTokenStream

    es.onmessage = () => {}

    const handleEvent = (type: string) => (e: MessageEvent) => {
      try {
        const outer = JSON.parse(e.data)
        const line = formatStreamEvent(type, outer)
        if (line) { lines.push(line); updateMsg(convId, runId, { streamLines: [...lines] }) }
      } catch { /* ignore */ }
    }

    es.addEventListener('model.completed', handleEvent('model.completed'))
    es.addEventListener('turn.completed', handleEvent('turn.completed'))
    es.addEventListener('tool.completed', handleEvent('tool.completed'))
    es.addEventListener('knowledge.proposed', handleEvent('knowledge.proposed'))

    // model.text_delta carries the model's response text — show it in real-time
    es.addEventListener('model.text_delta', (e) => {
      try {
        const outer = JSON.parse(e.data)
        const d = outer.data ?? outer
        const text = d.text ?? ''
        if (text) {
          // Update the message content with the model's response
          setConversations((prev) => prev.map((c) => {
            if (c.id !== convId) return c
            return { ...c, messages: c.messages.map((m) => {
              if (m.runId !== runId) return m
              // Only update if we don't already have a final answer
              if (m.status && m.status !== 'running') return m
              return { ...m, content: text }
            })}
          }))
        }
      } catch { /* ignore */ }
    })

    // model.reasoning carries the model's thinking/reasoning text
    es.addEventListener('model.reasoning', (e) => {
      try {
        const outer = JSON.parse(e.data)
        const d = outer.data ?? outer
        const text = d.text ?? ''
        if (text) {
          setConversations((prev) => prev.map((c) => {
            if (c.id !== convId) return c
            return { ...c, messages: c.messages.map((m) => {
              if (m.runId !== runId) return m
              if (m.status && m.status !== 'running') return m
              return { ...m, reasoning: text }
            })}
          }))
        }
      } catch { /* ignore */ }
    })

    // agent.summary arrives via SSE but the answer in observation_events
    // loses newlines (JSONB storage). Always fetch the real answer from REST.
    es.addEventListener('agent.summary', (e) => {
      try {
        const outer = JSON.parse(e.data)
        const d = outer.data ?? outer
        if (d.answer) {
          lines.push(t('chat.answer_received') ?? 'Answer received')
          updateMsg(convId, runId, { streamLines: [...lines] })
          void fetchFinalAnswer(runId, convId)
        }
      } catch { /* ignore */ }
    })

    // Approval events — show in stream lines
    es.addEventListener('approval.requested', (e) => {
      try {
        const outer = JSON.parse(e.data)
        const d = outer.data ?? outer
        const action = d.action ?? d.operation?.tool ?? 'action'
        lines.push(`⚠ Approval needed: ${action}`)
        updateMsg(convId, runId, { streamLines: [...lines] })
      } catch { /* ignore */ }
    })
    es.addEventListener('approval.granted', (e) => {
      try {
        lines.push('✓ Approval granted')
        updateMsg(convId, runId, { streamLines: [...lines] })
      } catch { /* ignore */ }
    })
    es.addEventListener('approval.rejected', (e) => {
      try {
        lines.push('✗ Approval rejected')
        updateMsg(convId, runId, { streamLines: [...lines] })
      } catch { /* ignore */ }
    })
    es.addEventListener('approval.auto_granted', (e) => {
      try {
        const outer = JSON.parse(e.data)
        const d = outer.data ?? outer
        const tool = d.operation?.tool ?? d.policy_id ?? 'tool'
        lines.push(`✓ Auto-approved: ${tool}`)
        updateMsg(convId, runId, { streamLines: [...lines] })
      } catch { /* ignore */ }
    })

    es.addEventListener('run.completed', handleEvent('run.completed'))

    // Delegation events — surface child agent activity in the stream
    const delegationAgentName = (id: unknown) => {
      const s = typeof id === 'string' ? id : ''
      return allAgents.find((a) => a.id === s)?.name ?? s ?? 'agent'
    }
    es.addEventListener('delegation.started', (e) => {
      try {
        const outer = JSON.parse(e.data)
        const d = outer.data ?? outer
        const name = delegationAgentName(d.agent_id)
        lines.push(t('chat.delegation.stream_started', { name }) ?? `Delegated to ${name}`)
        updateMsg(convId, runId, { streamLines: [...lines] })
      } catch { /* ignore */ }
    })
    es.addEventListener('delegation.completed', (e) => {
      try {
        const outer = JSON.parse(e.data)
        const d = outer.data ?? outer
        const name = delegationAgentName(d.agent_id)
        const turns = d.turns != null ? String(d.turns) : ''
        const line = turns
          ? (t('chat.delegation.stream_completed', { name, count: turns }))
          : `${name} · ${t('chat.delegation.completed')}`
        lines.push(line)
        updateMsg(convId, runId, { streamLines: [...lines] })
      } catch { /* ignore */ }
    })
    es.addEventListener('delegation.failed', (e) => {
      try {
        const outer = JSON.parse(e.data)
        const d = outer.data ?? outer
        const name = delegationAgentName(d.agent_id)
        lines.push(t('chat.delegation.stream_failed', { name }) ?? `${name} · failed`)
        updateMsg(convId, runId, { streamLines: [...lines] })
      } catch { /* ignore */ }
    })
    // Plan events — DAG task progress of plan calls in the stream
    es.addEventListener('plan.started', (e) => {
      try {
        const outer = JSON.parse(e.data)
        const d = outer.data ?? outer
        const goal = typeof d.goal === 'string' && d.goal.trim() ? d.goal : t('chat.plan.untitled')
        lines.push(t('chat.plan.stream_started', { goal }) ?? `Plan started: ${goal}`)
        updateMsg(convId, runId, { streamLines: [...lines] })
      } catch { /* ignore */ }
    })
    es.addEventListener('plan.task.started', (e) => {
      try {
        const outer = JSON.parse(e.data)
        const d = outer.data ?? outer
        const round = Number(d.round ?? 1)
        const roundSuffix = round > 1 ? ` ×${round}` : ''
        const gate = Array.isArray(d.review_of) && d.review_of.length ? ` ⌾ ${d.review_of.join(', ')}` : ''
        lines.push((t('chat.plan.stream_task_started', { task: String(d.task_id ?? '') + roundSuffix + gate, name: delegationAgentName(d.agent_id) })) ?? `Plan · ${d.task_id}`)
        updateMsg(convId, runId, { streamLines: [...lines] })
      } catch { /* ignore */ }
    })
    es.addEventListener('plan.task.completed', (e) => {
      try {
        const outer = JSON.parse(e.data)
        const d = outer.data ?? outer
        const round = Number(d.round ?? 1)
        const roundSuffix = round > 1 ? ` ×${round}` : ''
        const verdict = String(d.verdict ?? '')
        const verdictSuffix = verdict === 'accept' ? ' ✓' : verdict === 'rework' ? ' ↺' : ''
        lines.push((t('chat.plan.stream_task_completed', { task: String(d.task_id ?? '') + roundSuffix + verdictSuffix, name: delegationAgentName(d.agent_id) })) ?? `Plan · ${d.task_id} · completed`)
        updateMsg(convId, runId, { streamLines: [...lines] })
      } catch { /* ignore */ }
    })
    es.addEventListener('plan.task.failed', (e) => {
      try {
        const outer = JSON.parse(e.data)
        const d = outer.data ?? outer
        lines.push(t('chat.plan.stream_task_failed', { task: String(d.task_id ?? ''), name: delegationAgentName(d.agent_id) }) ?? `Plan · ${d.task_id} · failed`)
        updateMsg(convId, runId, { streamLines: [...lines] })
      } catch { /* ignore */ }
    })
    es.addEventListener('plan.task.rejected', (e) => {
      try {
        const outer = JSON.parse(e.data)
        const d = outer.data ?? outer
        lines.push(t('chat.plan.stream_task_rejected', { task: String(d.task_id ?? ''), by: delegationAgentName(d.rejected_by), feedback: String(d.feedback ?? '') }) ?? `Plan · ${d.task_id} · rework`)
        updateMsg(convId, runId, { streamLines: [...lines] })
      } catch { /* ignore */ }
    })
    es.addEventListener('plan.task.reopened', (e) => {
      try {
        const outer = JSON.parse(e.data)
        const d = outer.data ?? outer
        const round = Number(d.round ?? 0)
        lines.push(t('chat.plan.stream_task_reopened', { task: String(d.task_id ?? ''), round: String(round + 1) }) ?? `Plan · ${d.task_id} · reopened`)
        updateMsg(convId, runId, { streamLines: [...lines] })
      } catch { /* ignore */ }
    })
    es.addEventListener('plan.task.invalidated', (e) => {
      try {
        const outer = JSON.parse(e.data)
        const d = outer.data ?? outer
        lines.push(t('chat.plan.stream_task_invalidated', { task: String(d.task_id ?? '') }) ?? `Plan · ${d.task_id} · discarded`)
        updateMsg(convId, runId, { streamLines: [...lines] })
      } catch { /* ignore */ }
    })
    es.addEventListener('plan.task.skipped', (e) => {
      try {
        const outer = JSON.parse(e.data)
        const d = outer.data ?? outer
        lines.push(t('chat.plan.stream_task_skipped', { task: String(d.task_id ?? '') }) ?? `Plan · ${d.task_id} · skipped`)
        updateMsg(convId, runId, { streamLines: [...lines] })
      } catch { /* ignore */ }
    })
    es.addEventListener('plan.completed', (e) => {
      try {
        const outer = JSON.parse(e.data)
        const d = outer.data ?? outer
        const statuses = (d.statuses ?? {}) as Record<string, unknown>
        const counts = new Map<string, number>()
        for (const value of Object.values(statuses)) counts.set(String(value), (counts.get(String(value)) ?? 0) + 1)
        const summary = ['completed', 'failed', 'skipped', 'invalidated']
          .map((status) => counts.get(status) ? `${counts.get(status)} ${t(`chat.plan.status_${status}`)}` : '')
          .filter(Boolean).join(' · ')
        lines.push(t('chat.plan.stream_completed', { summary }) ?? `Plan finished: ${summary}`)
        updateMsg(convId, runId, { streamLines: [...lines] })
      } catch { /* ignore */ }
    })

    es.addEventListener('run.failed', (e) => {
      try {
        const outer = JSON.parse(e.data)
        const d = outer.data ?? outer
        const line = formatStreamEvent('run.failed', outer)
        if (line) lines.push(line)
        updateMsg(convId, runId, { content: `Failed: ${d.error ?? 'unknown'}`, status: 'failed', streamLines: [...lines] })
      } catch {
        updateMsg(convId, runId, { content: t('chat.run_failed') ?? 'Run failed', status: 'failed', streamLines: [...lines] })
      }
      es.close(); streamRef.current.delete(convId)
      closeTokenStream()
    })

    es.addEventListener('done', () => {
      es.close(); streamRef.current.delete(convId)
      closeTokenStream()
      setConversations((prev) => {
        const conv = prev.find((c) => c.id === convId)
        const m = conv?.messages.find((x) => x.runId === runId)
        if (m?.content) return prev
        void fetchFinalAnswer(runId, convId)
        return prev
      })
    })

    es.onerror = () => {
      es.close(); streamRef.current.delete(convId)
      closeTokenStream()
      void fetchFinalAnswer(runId, convId)
    }
  }

  const fetchFinalAnswer = async (runId: string, convId: string) => {
    // run.completed lands in the journal while knowledge extraction (another
    // model call, up to minutes) still keeps the Temporal workflow running —
    // the run row reaches its terminal status noticeably later than the
    // event. A single check here loses that race and the chat spinner never
    // stops; poll with a backoff until the run is actually terminal.
    const terminal = ['completed', 'failed', 'turn_limit', 'cancelled', 'time_limit', 'token_limit', 'budget_limit']
    // ~5 minutes total: extraction StartToClose is 3m, cover its retries too.
    const delays = [...Array(10).fill(2000), ...Array(55).fill(5000)]
    for (const delay of delays) {
      try {
        const run = await workspaceApi.getRun(runId)
        if (terminal.includes(run.status)) {
          const answer = run.answer || run.error || 'No answer received'
          updateMsg(convId, runId, { content: answer, status: run.status })
          return
        }
      } catch { /* transient — keep polling */ }
      await new Promise((resolve) => setTimeout(resolve, delay))
    }
  }

  const cancelRun = async (runId: string) => {
    try {
      await workspaceApi.cancelRun(runId)
      if (activeConv) {
        const es = streamRef.current.get(activeConv.id)
        if (es) { es.close(); streamRef.current.delete(activeConv.id) }
        const tes = tokenStreamRef.current.get(activeConv.id)
        if (tes) { tes.close(); tokenStreamRef.current.delete(activeConv.id) }
        updateMsg(activeConv.id, runId, { status: 'cancelled', content: t('chat.run_cancelled') ?? 'Run cancelled by user', streamLines: [] })
      }
    } catch (f) { setError(message(f)) }
  }

  const sendMessage = async () => {
    if (!inputValue.trim() || loading || !activeConv) return
    const content = inputValue.trim()
    setInputValue('')
    setLoading(true)
    setError('')

    const userMsg: ChatMessage = { role: 'user', content, timestamp: new Date() }
    const updatedConv = { ...activeConv, messages: [...activeConv.messages, userMsg] }
    if (updatedConv.messages.length === 1) {
      updatedConv.title = content.slice(0, 60) + (content.length > 60 ? '…' : '')
    }
    setConversations((prev) => prev.map((c) => c.id === updatedConv.id ? updatedConv : c))

    try {
      // Build full conversation history as the prompt
      const historyLines: string[] = []
      for (const msg of updatedConv.messages) {
        if (msg.role === 'user') {
          historyLines.push(`User: ${msg.content}`)
        } else if (msg.role === 'assistant' && msg.content) {
          historyLines.push(`Assistant: ${msg.content}`)
        }
      }
      const fullPrompt = historyLines.join('\n\n')

      // Reuse existing task for follow-up messages, create new one for first message
      let taskId = activeConv.taskId
      if (!taskId) {
        const task = await workspaceApi.createTask({
          project_id: project,
          agent_id: activeConv.agentId || undefined,
          title: content.slice(0, 80),
          prompt: fullPrompt,
        })
        taskId = task.id
        // Store taskId on the conversation
        setConversations((prev) => prev.map((c) => c.id === activeConv.id ? { ...c, taskId } : c))
      } else {
        // Update the existing task's prompt with the full conversation. The
        // title is intentionally omitted: it was set from the first message
        // and the store keeps the stored one when the field is empty.
        await workspaceApi.updateTask(taskId, { prompt: fullPrompt })
      }

      const result = await workspaceApi.startRun(taskId, { agent_id: activeConv.agentId || undefined })
      const runId = result.run_id

      const assistantMsg: ChatMessage = { role: 'assistant', content: '', timestamp: new Date(), status: 'running', runId, streamLines: [`${t('chat.run_started') ?? 'Run started'} · ${t('chat.streaming') ?? 'streaming…'}`] }
      const withAssistant = { ...updatedConv, messages: [...updatedConv.messages, assistantMsg] }
      setConversations((prev) => prev.map((c) => c.id === withAssistant.id ? withAssistant : c))

      streamRun(runId, activeConv.id)
    } catch (f) {
      setError(message(f))
      const errMsg: ChatMessage = { role: 'assistant', content: `Error: ${message(f)}`, timestamp: new Date(), status: 'failed' }
      const withErr = { ...updatedConv, messages: [...updatedConv.messages, errMsg] }
      setConversations((prev) => prev.map((c) => c.id === withErr.id ? withErr : c))
    } finally { setLoading(false) }
  }

  return (
    <div className="workspace-root">
      {/* Left panel: conversations */}
      <div className="workspace-sidebar">
        <div style={{ padding: '0.75rem', borderBottom: '1px solid var(--tm-border)' }}>
          <Button renderIcon={Add} size="sm" onClick={() => { setNewChatAgentId(defaultAgentId ?? allAgents.find((a) => !a.project_id || a.project_id === project)?.id ?? ''); setShowNewChat(true) }} style={{ width: '100%' }}>{t('new.chat') ?? 'New chat'}</Button>
          <div style={{ marginTop: '0.5rem' }}>
            <ListFilter value={convFilter} onChange={setConvFilter} placeholder={t('common.filter_chats') ?? 'Filter chats…'} />
          </div>
        </div>
        <div style={{ flex: 1, overflowY: 'auto', padding: '0.5rem' }}>
          {conversations.length === 0 && (
            <p style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem', padding: '1rem', textAlign: 'center' }}>No conversations yet</p>
          )}
          {conversations.length > 0 && visibleConversations.length === 0 && (
            <p style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem', padding: '1rem', textAlign: 'center' }}>{t('common.no_matches') ?? 'Nothing matches the filter'}</p>
          )}
          {visibleConversations.map((conv) => (
            <div
              key={conv.id}
              onClick={() => setActiveConvId(conv.id)}
              style={{
                padding: '0.75rem',
                marginBottom: '0.25rem',
                borderRadius: '6px',
                cursor: 'pointer',
                background: conv.id === activeConvId ? 'var(--tm-elevated)' : 'transparent',
                border: conv.id === activeConvId ? '1px solid var(--tm-border)' : '1px solid transparent',
              }}
            >
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start' }}>
                <div style={{ flex: 1, minWidth: 0 }}>
                  <div style={{ fontSize: '0.875rem', fontWeight: 500, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{conv.title}</div>
                  <div style={{ fontSize: '0.75rem', color: 'var(--tm-text-3)', marginTop: '0.25rem' }}>
                    {conv.agentId ? agentName(conv.agentId) : (defaultAgentId ? agentName(defaultAgentId) : (t('chat.no_agent') ?? 'No agent'))}{defaultModel ? ` · ${defaultModel}` : ''} · {conv.messages.length} {t('chat.msgs') ?? 'msgs'}
                  </div>
                </div>
                <button
                  onClick={(e) => { e.stopPropagation(); deleteConversation(conv.id) }}
                  style={{ background: 'none', border: 'none', color: 'var(--tm-text-3)', cursor: 'pointer', padding: '0.25rem' }}
                >
                  <TrashCan size={16} />
                </button>
              </div>
            </div>
          ))}
        </div>
      </div>

      {/* Right panel: active chat */}
      <div className="workspace-main">
        {activeConv ? (
          <>
            {/* Chat header */}
            <div style={{ padding: '0.5rem 1rem', borderBottom: '1px solid var(--tm-border)', background: 'var(--tm-surface)', display: 'flex', alignItems: 'center', gap: '0.75rem', flexShrink: 0 }}>
              <strong style={{ fontSize: '0.875rem' }}>{activeConv.title}</strong>
              {activeConv.agentId && <Tag type="blue" size="sm">{agentName(activeConv.agentId)}</Tag>}
            </div>

            {/* Chat messages */}
            <div className="workspace-messages">
              {activeConv.messages.length === 0 && (
                <div style={{ textAlign: 'center', color: 'var(--tm-text-3)', padding: '3rem 1rem' }}>
                  <Heading>Temporality Agent</Heading>
                  <p>Send a message to start the conversation.</p>
                </div>
              )}
              {activeConv.messages.map((msg, i) => (
                <div key={i} style={{ marginBottom: '0.75rem', display: 'flex', justifyContent: msg.role === 'user' ? 'flex-end' : 'flex-start' }}>
                  <div className={msg.role === 'user' ? 'chat-bubble-user' : 'chat-bubble-assistant'}>
                    {/* Reasoning/thinking block — collapsible */}
                    {msg.reasoning && (
                      <details className="chat-thinking">
                        <summary>
                          💭 {t('chat.thinking') ?? 'Thinking'}
                        </summary>
                        <div className="chat-thinking-content">
                          {msg.reasoning}
                        </div>
                      </details>
                    )}

                    {msg.content && msg.role === 'assistant' ? (
                      <Markdown content={msg.content} />
                    ) : msg.content ? (
                      <div style={{ whiteSpace: 'pre-wrap' }}>{msg.content}</div>
                    ) : null}

                    {msg.role === 'assistant' && msg.runId && msg.status && (
                      <>
                        <DelegationTree project={project} runId={msg.runId} agents={allAgents} live={msg.status === 'running'} />
                        <PlanGraph project={project} runId={msg.runId} agents={allAgents} live={msg.status === 'running'} />
                      </>
                    )}

                    {msg.status === 'running' && msg.streamLines && msg.streamLines.length > 0 && (
                      <div style={{ marginTop: msg.content ? '0.5rem' : 0 }}>
                        <ChatStreamLines lines={msg.streamLines} />
                        <div style={{ display: 'flex', alignItems: 'center', gap: '0.4rem', marginTop: '0.25rem' }}>
                          <span className="spinner" />
                          <span className="chat-streaming">{t('chat.streaming') ?? 'streaming…'}</span>
                          {msg.runId && (
                            <button
                              onClick={() => cancelRun(msg.runId!)}
                              style={{ background: 'none', border: '1px solid var(--tm-danger)', borderRadius: '4px', color: 'var(--tm-danger)', cursor: 'pointer', fontSize: '0.65rem', padding: '0.1rem 0.4rem', marginLeft: 'auto' }}
                              title={t('chat.cancel_run') ?? 'Cancel this run'}
                            >{t('chat.stop') ?? 'Stop'}</button>
                          )}
                        </div>
                      </div>
                    )}

                    {msg.runId && msg.status && msg.status !== 'running' && (
                      <div className={msg.role === 'user' ? 'chat-status chat-status-user' : 'chat-status'}>
                        <Tag type={STATUS_COLORS[msg.status] || 'gray'} size="sm">{msg.status}</Tag>
                        {activeConv && (
                          <button
                            onClick={() => branchConversation(activeConv.id, i)}
                            style={{ background: 'none', border: '1px solid var(--tm-border)', borderRadius: '4px', color: 'var(--tm-teal)', cursor: 'pointer', fontSize: '0.7rem', padding: '0.15rem 0.5rem', marginLeft: 'auto' }}
                            title={t('chat.branch_title') ?? 'Branch conversation from this point'}
                          >{t('chat.branch') ?? 'Branch'}</button>
                        )}
                      </div>
                    )}
                  </div>
                </div>
              ))}
              <div ref={chatEndRef} />
            </div>

            {/* Input area */}
            <div className="workspace-input-bar">
              {error && <InlineNotification kind="error" title="Error" subtitle={error} onClose={() => setError('')} lowContrast style={{ marginBottom: '0.5rem' }} />}
              <div style={{ display: 'flex', gap: '0.75rem', alignItems: 'flex-end' }}>
                <div style={{ flex: 1 }}>
                  <TextArea
                    id="chat-input"
                    labelText=""
                    hideLabel
                    value={inputValue}
                    onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => setInputValue(e.target.value)}
                    placeholder={t('chat.placeholder') ?? 'Type a message… (Shift+Enter for newline)'}
                    rows={3}
                    onKeyDown={(e: React.KeyboardEvent<HTMLTextAreaElement>) => {
                      if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); void sendMessage() }
                    }}
                    style={{ resize: 'vertical', minHeight: '72px' }}
                  />
                </div>
                {loading && <span className="spinner" title={t('chat.sending') ?? 'Sending…'} style={{ alignSelf: 'center', flexShrink: 0 }} />}
                <Button renderIcon={Send} onClick={() => void sendMessage()} disabled={loading || !inputValue.trim()} style={{ marginBottom: '2px' }}>{t('chat.send') ?? 'Send'}</Button>
              </div>
            </div>
          </>
        ) : (
          <div style={{ flex: 1, display: 'flex', alignItems: 'center', justifyContent: 'center', color: 'var(--tm-text-3)' }}>
            <div style={{ textAlign: 'center' }}>
              <Heading>{t('chat.title') ?? 'Temporality Agent'}</Heading>
              <p style={{ marginTop: '0.5rem' }}>{t('chat.select_conversation') ?? 'Select a conversation or start a new one.'}</p>
              <Button renderIcon={Add} onClick={() => { setNewChatAgentId(defaultAgentId ?? allAgents.find((a) => !a.project_id || a.project_id === project)?.id ?? ''); setShowNewChat(true) }} style={{ marginTop: '1rem' }}>{t('new.chat') ?? 'New chat'}</Button>
            </div>
          </div>
        )}
      </div>

      {/* New chat modal */}
      {showNewChat && (
        <div className="modal-overlay">
          <div className="modal-panel" style={{ width: '420px' }}>
            <Heading style={{ fontSize: '1.1rem', marginBottom: '0.75rem' }}>{t('new.conversation') ?? 'New conversation'}</Heading>
            <Select
              id="new-chat-agent"
              labelText={t('chat.agent') ?? 'Agent'}
              value={newChatAgentId}
              onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setNewChatAgentId(e.target.value)}
            >
              {defaultAgentId && (
                <SelectItem value="" text={`${t('chat.project_default') ?? 'Project default'} (${allAgents.find((a) => a.id === defaultAgentId)?.name ?? defaultAgentId})`} />
              )}
              {allAgents.filter((a) => a.id !== defaultAgentId && (!a.project_id || a.project_id === project)).map((a) => <SelectItem key={a.id} value={a.id} text={`${a.name}${a.model ? ` (${a.model})` : ''}`} />)}
            </Select>
            {!defaultAgentId && (
              <p style={{ fontSize: '0.75rem', color: 'var(--tm-text-3)', margin: '0.5rem 0 0' }}>{t('chat.no_default_hint') ?? 'This project has no default agent yet — set one on the Agents page.'}</p>
            )}
            <div className="form-actions">
              <Button kind="secondary" onClick={() => setShowNewChat(false)}>{t('action.cancel') ?? 'Cancel'}</Button>
              <Button onClick={createConversation}>{t('action.create') ?? 'Create'}</Button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
