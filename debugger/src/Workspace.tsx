import { useCallback, useEffect, useState, useRef } from 'react'
import {
  Button,
  TextInput,
  Select,
  SelectItem,
  InlineNotification,
  Loading,
  Tag,
  Heading,
} from '@carbon/react'
import { Add, Send, TrashCan } from '@carbon/icons-react'
import { workspaceApi, type Agent } from './workspaceApi'
import { authHeaders } from './api'
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
  /** Live streaming lines shown while the run is in progress. */
  streamLines?: string[]
}

interface Conversation {
  id: string
  title: string
  messages: ChatMessage[]
  createdAt: Date
}

const STORAGE_KEY = 'temporality_chats'

function loadConversations(): Conversation[] {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (!raw) return []
    return JSON.parse(raw).map((c: any) => ({
      ...c,
      createdAt: new Date(c.createdAt),
      messages: c.messages.map((m: any) => ({ ...m, timestamp: new Date(m.timestamp) })),
    }))
  } catch { return [] }
}

function saveConversations(convs: Conversation[]) {
  localStorage.setItem(STORAGE_KEY, JSON.stringify(convs))
}

/** Format a stream event into a human-readable status line. */
function formatStreamEvent(type: string, data: any): string | null {
  switch (type) {
    case 'run.started':
      return `Run started${data.model ? ` · model ${data.model}` : ''}`
    case 'model.completed': {
      const tokens = data.usage ? ` · ${data.usage.total_tokens ?? '?'} tokens` : ''
      const latency = data.latency_ms ? ` · ${(data.latency_ms / 1000).toFixed(1)}s` : ''
      return `Model turn ${data.turn ?? ''}${tokens}${latency}`
    }
    case 'turn.completed':
      return `Turn ${data.turn ?? ''} done${data.tool_calls ? ` · ${data.tool_calls} tool calls` : ''}`
    case 'tool.completed':
      return `Tool: ${data.name ?? data.tool ?? '?'}`
    case 'knowledge.proposed':
      return `Learned: ${(data.proposition ?? '').slice(0, 60)}`
    case 'run.completed':
      return `Completed · ${data.turns ?? '?'} turns`
    case 'run.failed':
      return `Failed: ${data.error ?? 'unknown'}`
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
  const [selectedAgentId, setSelectedAgentId] = useState('')
  const chatEndRef = useRef<HTMLDivElement>(null)
  // Track active EventSource connections so we can clean up.
  const streamRef = useRef<Map<string, EventSource>>(new Map())

  const activeConv = conversations.find((c) => c.id === activeConvId) ?? null

  // Persist conversations to localStorage
  useEffect(() => { saveConversations(conversations) }, [conversations])

  // Cleanup all streams on unmount.
  useEffect(() => () => { streamRef.current.forEach((es) => es.close()); streamRef.current.clear() }, [])

  const loadAllAgents = useCallback(async () => {
    try {
      const data = await workspaceApi.listAllAgents()
      setAllAgents(data.agents ?? [])
    } catch { /* ignore */ }
  }, [])

  useEffect(() => { void loadAllAgents() }, [loadAllAgents])

  // Auto-scroll
  const scrollToBottom = () => { chatEndRef.current?.scrollIntoView({ behavior: 'smooth' }) }
  useEffect(scrollToBottom, [activeConv?.messages])

  const newConversation = () => {
    const conv: Conversation = {
      id: crypto.randomUUID(),
      title: 'New conversation',
      messages: [],
      createdAt: new Date(),
    }
    setConversations((prev) => [conv, ...prev])
    setActiveConvId(conv.id)
  }

  const deleteConversation = (id: string) => {
    // Close any active stream for this conversation.
    const es = streamRef.current.get(id)
    if (es) { es.close(); streamRef.current.delete(id) }
    setConversations((prev) => prev.filter((c) => c.id !== id))
    if (activeConvId === id) setActiveConvId(null)
  }

  /** Update a specific assistant message in a conversation by runId. */
  const updateMsg = (convId: string, runId: string, patch: Partial<ChatMessage>) => {
    setConversations((prev) => prev.map((c) => {
      if (c.id !== convId) return c
      return { ...c, messages: c.messages.map((m) => m.runId === runId ? { ...m, ...patch } : m) }
    }))
  }

  /** Start SSE streaming for a run. Shows live progress lines in the message. */
  const streamRun = (runId: string, convId: string) => {
    const token = localStorage.getItem('temporality_token') ?? ''
    const es = new EventSource(`/kernel-api/v1/workspace/runs/${encodeURIComponent(runId)}/stream${token ? `?token=${token}` : ''}`)

    // Store the EventSource so we can close it later.
    streamRef.current.set(convId, es)

    const lines: string[] = []

    es.onmessage = () => { /* default handler, not used — we listen to named events */ }

    es.addEventListener('model.completed', (e) => {
      try {
        const data = JSON.parse(e.data)
        const line = formatStreamEvent('model.completed', data)
        if (line) { lines.push(line); updateMsg(convId, runId, { streamLines: [...lines] }) }
      } catch { /* ignore parse errors */ }
    })

    es.addEventListener('turn.completed', (e) => {
      try {
        const data = JSON.parse(e.data)
        const line = formatStreamEvent('turn.completed', data)
        if (line) { lines.push(line); updateMsg(convId, runId, { streamLines: [...lines] }) }
      } catch { /* ignore */ }
    })

    es.addEventListener('tool.completed', (e) => {
      try {
        const data = JSON.parse(e.data)
        const line = formatStreamEvent('tool.completed', data)
        if (line) { lines.push(line); updateMsg(convId, runId, { streamLines: [...lines] }) }
      } catch { /* ignore */ }
    })

    es.addEventListener('knowledge.proposed', (e) => {
      try {
        const data = JSON.parse(e.data)
        const line = formatStreamEvent('knowledge.proposed', data)
        if (line) { lines.push(line); updateMsg(convId, runId, { streamLines: [...lines] }) }
      } catch { /* ignore */ }
    })

    // agent.summary carries the final answer text.
    es.addEventListener('agent.summary', (e) => {
      try {
        const data = JSON.parse(e.data)
        const answer = data.answer ?? ''
        if (answer) {
          lines.push('Answer received')
          updateMsg(convId, runId, { content: answer, status: 'completed', streamLines: [...lines] })
        }
      } catch { /* ignore */ }
    })

    es.addEventListener('run.completed', (e) => {
      try {
        const data = JSON.parse(e.data)
        const line = formatStreamEvent('run.completed', data)
        if (line) lines.push(line)
      } catch { /* ignore */ }
      // Don't close yet — agent.summary with the answer comes after.
      // The 'done' event will trigger final cleanup.
    })

    es.addEventListener('run.failed', (e) => {
      try {
        const data = JSON.parse(e.data)
        const line = formatStreamEvent('run.failed', data)
        if (line) lines.push(line)
        updateMsg(convId, runId, { content: `Failed: ${data.error ?? 'unknown'}`, status: 'failed', streamLines: [...lines] })
      } catch {
        updateMsg(convId, runId, { content: 'Run failed', status: 'failed', streamLines: [...lines] })
      }
      es.close(); streamRef.current.delete(convId)
    })

    es.addEventListener('done', () => {
      es.close(); streamRef.current.delete(convId)
      // If agent.summary already set the answer, we're done.
      // Otherwise fetch from REST API.
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

  /** Fetch the final run result after streaming ends. */
  const fetchFinalAnswer = async (runId: string, convId: string) => {
    try {
      const run = await workspaceApi.getRun(runId)
      if (run.status === 'completed' || run.status === 'failed' || run.status === 'turn_limit') {
        const answer = run.answer || run.error || 'No answer received'
        updateMsg(convId, runId, { content: answer, status: run.status })
      }
    } catch { /* ignore — will be retried by polling fallback */ }
  }

  const sendMessage = async () => {
    if (!inputValue.trim() || loading || !activeConv) return
    const content = inputValue.trim()
    setInputValue('')
    setLoading(true)
    setError('')

    // Add user message
    const userMsg: ChatMessage = { role: 'user', content, timestamp: new Date() }
    const updatedConv = { ...activeConv, messages: [...activeConv.messages, userMsg] }
    // Update title from first message
    if (updatedConv.messages.length === 1) {
      updatedConv.title = content.slice(0, 60) + (content.length > 60 ? '…' : '')
    }
    setConversations((prev) => prev.map((c) => c.id === updatedConv.id ? updatedConv : c))

    // Add placeholder assistant message
    const assistantMsg: ChatMessage = { role: 'assistant', content: '', timestamp: new Date(), status: 'running', streamLines: ['Starting run…'] }
    const withAssistant = { ...updatedConv, messages: [...updatedConv.messages, assistantMsg] }
    setConversations((prev) => prev.map((c) => c.id === withAssistant.id ? withAssistant : c))

    try {
      // Create task and start run
      const task = await workspaceApi.createTask({
        project_id: project,
        agent_id: selectedAgentId || undefined,
        title: content.slice(0, 80),
        prompt: content,
      })
      const result = await workspaceApi.startRun(task.id, { agent_id: selectedAgentId || undefined })
      const runId = result.run_id
      // Update assistant message with runId
      setConversations((prev) => prev.map((c) => {
        if (c.id !== activeConv.id) return c
        return { ...c, messages: c.messages.map((m) => m === assistantMsg ? { ...m, runId, streamLines: ['Run started · streaming…'] } : m) }
      }))
      // Start SSE streaming
      streamRun(runId, activeConv.id)
    } catch (f) {
      setError(message(f))
      setConversations((prev) => prev.map((c) => {
        if (c.id !== activeConv.id) return c
        return { ...c, messages: c.messages.map((m) => m === assistantMsg ? { ...m, content: `Error: ${message(f)}`, status: 'failed', streamLines: undefined } : m) }
      }))
    } finally { setLoading(false) }
  }

  return (
    <div style={{ display: 'flex', height: 'calc(100vh - 48px)', background: '#080b10' }}>
      {/* Left panel: conversations */}
      <div style={{ width: '280px', borderRight: '1px solid #202a38', display: 'flex', flexDirection: 'column', background: '#0d1118' }}>
        <div style={{ padding: '0.75rem', borderBottom: '1px solid #202a38' }}>
          <Button renderIcon={Add} size="sm" onClick={newConversation} style={{ width: '100%' }}>New chat</Button>
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
                    {conv.messages.length} messages · {shortTime(conv.createdAt.toISOString())}
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
      <div style={{ flex: 1, display: 'flex', flexDirection: 'column' }}>
        {activeConv ? (
          <>
            {/* Chat messages */}
            <div style={{ flex: 1, overflowY: 'auto', padding: '1rem' }}>
              {activeConv.messages.length === 0 && (
                <div style={{ textAlign: 'center', color: '#7e8a9c', padding: '3rem 1rem' }}>
                  <Heading>Temporality Agent</Heading>
                  <p>Send a message to start the conversation.</p>
                </div>
              )}
              {activeConv.messages.map((msg, i) => (
                <div key={i} style={{ marginBottom: '0.75rem', display: 'flex', justifyContent: msg.role === 'user' ? 'flex-end' : 'flex-start' }}>
                  <div style={{
                    maxWidth: '80%', padding: '0.75rem', borderRadius: '8px',
                    background: msg.role === 'user' ? '#57d7e8' : '#121823',
                    color: msg.role === 'user' ? '#080b10' : '#e5e9f0',
                    border: msg.role === 'user' ? 'none' : '1px solid #344258',
                  }}>
                    {/* Show Markdown answer if present */}
                    {msg.content && msg.role === 'assistant' && msg.status && msg.status !== 'running' ? (
                      <Markdown content={msg.content} />
                    ) : msg.content ? (
                      <div style={{ whiteSpace: 'pre-wrap' }}>{msg.content}</div>
                    ) : null}

                    {/* Show live streaming progress lines */}
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

                    {/* Status tag */}
                    {msg.runId && msg.status && msg.status !== 'running' && (
                      <div style={{ marginTop: '0.5rem', fontSize: '0.75rem', color: msg.role === 'user' ? '#080b10' : '#7e8a9c' }}>
                        <Tag type={STATUS_COLORS[msg.status] || 'gray'} size="sm">{msg.status}</Tag>
                      </div>
                    )}
                  </div>
                </div>
              ))}
              <div ref={chatEndRef} />
            </div>

            {/* Agent selector + Input */}
            <div style={{ padding: '0.75rem', borderTop: '1px solid #202a38', background: '#0d1118' }}>
              {error && <InlineNotification kind="error" title="Error" subtitle={error} onClose={() => setError('')} lowContrast style={{ marginBottom: '0.5rem' }} />}
              <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'flex-end' }}>
                <Select id="chat-agent" labelText="" hideLabel value={selectedAgentId} onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setSelectedAgentId(e.target.value)} size="sm">
                  <SelectItem value="" text="Default agent" />
                  {allAgents.map((a) => <SelectItem key={a.id} value={a.id} text={a.name} />)}
                </Select>
                <div style={{ flex: 1 }}>
                  <TextInput id="chat-input" labelText="" hideLabel value={inputValue} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setInputValue(e.target.value)}
                    placeholder="Type a message..." onKeyDown={(e: React.KeyboardEvent) => { if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); void sendMessage() } }} />
                </div>
                <Button renderIcon={Send} onClick={() => void sendMessage()} disabled={loading || !inputValue.trim()}>Send</Button>
              </div>
            </div>
          </>
        ) : (
          <div style={{ flex: 1, display: 'flex', alignItems: 'center', justifyContent: 'center', color: '#7e8a9c' }}>
            <div style={{ textAlign: 'center' }}>
              <Heading>Temporality Agent</Heading>
              <p style={{ marginTop: '0.5rem' }}>Select a conversation or start a new one.</p>
              <Button renderIcon={Add} onClick={newConversation} style={{ marginTop: '1rem' }}>New chat</Button>
            </div>
          </div>
        )}
      </div>

      {loading && <Loading withOverlay={false} />}
    </div>
  )
}
