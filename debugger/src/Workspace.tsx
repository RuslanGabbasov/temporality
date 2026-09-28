import { useCallback, useEffect, useState, useRef } from 'react'
import {
  Button,
  TextInput,
  TextArea,
  Select,
  SelectItem,
  InlineNotification,
  Loading,
  Tag,
  Heading,
  Modal,
} from '@carbon/react'
import { Add, Send, TrashCan } from '@carbon/icons-react'
import { workspaceApi, type Agent } from './workspaceApi'
import Markdown from './Markdown'

function message(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }
function shortTime(iso: string) {
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? '' : d.toLocaleTimeString()
}

const STATUS_COLORS: Record<string, 'blue' | 'green' | 'warm-gray' | 'gray' | 'red'> = {
  completed: 'green', failed: 'red', running: 'blue', turn_limit: 'warm-gray', pending: 'gray',
  started: 'blue',
}

interface ChatMessage {
  role: 'user' | 'assistant'
  content: string
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

export default function Workspace({ project }: { project: string }) {
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

  const activeConv = conversations.find((c) => c.id === activeConvId) ?? null

  useEffect(() => { saveConversations(conversations) }, [conversations])
  useEffect(() => () => { streamRef.current.forEach((es) => es.close()); streamRef.current.clear() }, [])

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

  const agentName = (id: string) => allAgents.find((a) => a.id === id)?.name ?? 'Default'

  const createConversation = () => {
    const conv: Conversation = {
      id: crypto.randomUUID(),
      title: 'New conversation',
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

    // agent.summary arrives via SSE but the answer in observation_events
    // loses newlines (JSONB storage). Always fetch the real answer from REST.
    es.addEventListener('agent.summary', (e) => {
      try {
        const outer = JSON.parse(e.data)
        const d = outer.data ?? outer
        if (d.answer) {
          lines.push('Answer received')
          updateMsg(convId, runId, { streamLines: [...lines] })
          void fetchFinalAnswer(runId, convId)
        }
      } catch { /* ignore */ }
    })

    es.addEventListener('run.completed', handleEvent('run.completed'))

    es.addEventListener('run.failed', (e) => {
      try {
        const outer = JSON.parse(e.data)
        const d = outer.data ?? outer
        const line = formatStreamEvent('run.failed', outer)
        if (line) lines.push(line)
        updateMsg(convId, runId, { content: `Failed: ${d.error ?? 'unknown'}`, status: 'failed', streamLines: [...lines] })
      } catch {
        updateMsg(convId, runId, { content: 'Run failed', status: 'failed', streamLines: [...lines] })
      }
      es.close(); streamRef.current.delete(convId)
    })

    es.addEventListener('done', () => {
      es.close(); streamRef.current.delete(convId)
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
      void fetchFinalAnswer(runId, convId)
    }
  }

  const fetchFinalAnswer = async (runId: string, convId: string) => {
    try {
      const run = await workspaceApi.getRun(runId)
      if (run.status === 'completed' || run.status === 'failed' || run.status === 'turn_limit') {
        const answer = run.answer || run.error || 'No answer received'
        updateMsg(convId, runId, { content: answer, status: run.status })
      }
    } catch { /* ignore */ }
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
        // Update the existing task's prompt with the full conversation
        await workspaceApi.updateTask(taskId, { prompt: fullPrompt, title: content.slice(0, 80) })
      }

      const result = await workspaceApi.startRun(taskId, { agent_id: activeConv.agentId || undefined })
      const runId = result.run_id

      const assistantMsg: ChatMessage = { role: 'assistant', content: '', timestamp: new Date(), status: 'running', runId, streamLines: ['Run started · streaming…'] }
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
        <div style={{ padding: '0.75rem', borderBottom: '1px solid #202a38' }}>
          <Button renderIcon={Add} size="sm" onClick={() => setShowNewChat(true)} style={{ width: '100%' }}>New chat</Button>
        </div>
        <div style={{ flex: 1, overflowY: 'auto', padding: '0.5rem' }}>
          {conversations.length === 0 && (
            <p style={{ color: '#7e8a9c', fontSize: '0.75rem', padding: '1rem', textAlign: 'center' }}>No conversations yet</p>
          )}
          {conversations.map((conv) => (
            <div
              key={conv.id}
              onClick={() => setActiveConvId(conv.id)}
              style={{
                padding: '0.75rem',
                marginBottom: '0.25rem',
                borderRadius: '6px',
                cursor: 'pointer',
                background: conv.id === activeConvId ? '#121823' : 'transparent',
                border: conv.id === activeConvId ? '1px solid #344258' : '1px solid transparent',
              }}
            >
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start' }}>
                <div style={{ flex: 1, minWidth: 0 }}>
                  <div style={{ fontSize: '0.875rem', fontWeight: 500, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{conv.title}</div>
                  <div style={{ fontSize: '0.75rem', color: '#7e8a9c', marginTop: '0.25rem' }}>
                    {conv.agentId ? agentName(conv.agentId) : 'Default'} · {conv.messages.length} msgs
                  </div>
                </div>
                <button
                  onClick={(e) => { e.stopPropagation(); deleteConversation(conv.id) }}
                  style={{ background: 'none', border: 'none', color: '#7e8a9c', cursor: 'pointer', padding: '0.25rem' }}
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
            <div style={{ padding: '0.5rem 1rem', borderBottom: '1px solid #202a38', background: '#0d1118', display: 'flex', alignItems: 'center', gap: '0.75rem', flexShrink: 0 }}>
              <strong style={{ fontSize: '0.875rem' }}>{activeConv.title}</strong>
              {activeConv.agentId && <Tag type="blue" size="sm">{agentName(activeConv.agentId)}</Tag>}
            </div>

            {/* Chat messages */}
            <div className="workspace-messages">
              {activeConv.messages.length === 0 && (
                <div style={{ textAlign: 'center', color: '#7e8a9c', padding: '3rem 1rem' }}>
                  <Heading>Temporality Agent</Heading>
                  <p>Send a message to start the conversation.</p>
                </div>
              )}
              {activeConv.messages.map((msg, i) => (
                <div key={i} style={{ marginBottom: '0.75rem', display: 'flex', justifyContent: msg.role === 'user' ? 'flex-end' : 'flex-start' }}>
                  <div style={{
                    maxWidth: '80%', padding: '0.75rem 1rem', borderRadius: '8px',
                    background: msg.role === 'user' ? '#57d7e8' : '#121823',
                    color: msg.role === 'user' ? '#080b10' : '#e5e9f0',
                    border: msg.role === 'user' ? 'none' : '1px solid #344258',
                  }}>
                    {msg.content && msg.role === 'assistant' && msg.status && msg.status !== 'running' ? (
                      <Markdown content={msg.content} />
                    ) : msg.content ? (
                      <div style={{ whiteSpace: 'pre-wrap' }}>{msg.content}</div>
                    ) : null}

                    {msg.status === 'running' && msg.streamLines && msg.streamLines.length > 0 && (
                      <div style={{ marginTop: msg.content ? '0.5rem' : 0 }}>
                        {msg.streamLines.map((line, li) => (
                          <div key={li} style={{ fontSize: '0.75rem', color: '#7e8a9c', fontFamily: '"SFMono-Regular", Consolas, monospace', lineHeight: 1.6 }}>
                            <span style={{ color: '#4fd6be', marginRight: '0.4rem' }}>›</span>{line}
                          </div>
                        ))}
                        <div style={{ display: 'flex', alignItems: 'center', gap: '0.4rem', marginTop: '0.25rem' }}>
                          <span className="spinner" />
                          <span style={{ fontSize: '0.7rem', color: '#57d7e8' }}>streaming…</span>
                        </div>
                      </div>
                    )}

                    {msg.runId && msg.status && msg.status !== 'running' && (
                      <div style={{ marginTop: '0.5rem', fontSize: '0.75rem', color: msg.role === 'user' ? '#080b10' : '#7e8a9c', display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
                        <Tag type={STATUS_COLORS[msg.status] || 'gray'} size="sm">{msg.status}</Tag>
                        {activeConv && (
                          <button
                            onClick={() => branchConversation(activeConv.id, i)}
                            style={{ background: 'none', border: '1px solid #344258', borderRadius: '4px', color: '#57d7e8', cursor: 'pointer', fontSize: '0.7rem', padding: '0.15rem 0.5rem' }}
                            title="Branch conversation from this point"
                          >Branch</button>
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
                    placeholder="Type a message… (Shift+Enter for newline)"
                    rows={3}
                    onKeyDown={(e: React.KeyboardEvent<HTMLTextAreaElement>) => {
                      if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); void sendMessage() }
                    }}
                    style={{ resize: 'vertical', minHeight: '72px' }}
                  />
                </div>
                <Button renderIcon={Send} onClick={() => void sendMessage()} disabled={loading || !inputValue.trim()} style={{ marginBottom: '2px' }}>Send</Button>
              </div>
            </div>
          </>
        ) : (
          <div style={{ flex: 1, display: 'flex', alignItems: 'center', justifyContent: 'center', color: '#7e8a9c' }}>
            <div style={{ textAlign: 'center' }}>
              <Heading>Temporality Agent</Heading>
              <p style={{ marginTop: '0.5rem' }}>Select a conversation or start a new one.</p>
              <Button renderIcon={Add} onClick={() => setShowNewChat(true)} style={{ marginTop: '1rem' }}>New chat</Button>
            </div>
          </div>
        )}
      </div>

      {/* New chat modal — agent selector lives here, not in the input bar */}
      <Modal
        open={showNewChat}
        onRequestClose={() => setShowNewChat(false)}
        modalHeading="New conversation"
        primaryButtonText="Create"
        secondaryButtonText="Cancel"
        onRequestSubmit={createConversation}
      >
        <Select
          id="new-chat-agent"
          labelText="Agent"
          value={newChatAgentId}
          onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setNewChatAgentId(e.target.value)}
        >
          <SelectItem value="" text="Default agent" />
          {allAgents.map((a) => <SelectItem key={a.id} value={a.id} text={`${a.name}${a.model ? ` (${a.model})` : ''}`} />)}
        </Select>
      </Modal>

      {loading && <Loading withOverlay={false} />}
    </div>
  )
}
