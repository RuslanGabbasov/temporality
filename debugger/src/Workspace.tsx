import { useCallback, useEffect, useState, useRef } from 'react'
import {
  Button,
  TextInput,
  Select,
  SelectItem,
  InlineNotification,
  Loading,
  Tag,
  Tile,
  Grid,
  Column,
  Stack,
  Section,
  Heading,
} from '@carbon/react'
import { Add, Play, ArrowRight, Send, Edit, TrashCan } from '@carbon/icons-react'
import { workspaceApi, type Project, type Agent, type Task, type Run } from './workspaceApi'

function message(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }
function shortTime(iso: string) {
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? '' : d.toLocaleTimeString()
}

const STATUS_COLORS: Record<string, 'blue' | 'green' | 'warm-gray' | 'gray' | 'red'> = {
  completed: 'green', failed: 'red', running: 'blue', turn_limit: 'warm-gray', pending: 'gray',
}

interface ChatMessage {
  role: 'user' | 'assistant'
  content: string
  timestamp: Date
  runId?: string
  status?: string
}

export default function Workspace({ project }: { project: string }) {
  const [allAgents, setAllAgents] = useState<Agent[]>([])
  const [tasks, setTasks] = useState<Task[]>([])
  const [selectedRun, setSelectedRun] = useState<Run | null>(null)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)

  // Chat state
  const [messages, setMessages] = useState<ChatMessage[]>([])
  const [inputValue, setInputValue] = useState('')
  const [selectedAgentId, setSelectedAgentId] = useState('')
  const chatEndRef = useRef<HTMLDivElement>(null)

  // Task form
  const [showTaskForm, setShowTaskForm] = useState(false)
  const [taskTitle, setTaskTitle] = useState('')
  const [taskPrompt, setTaskPrompt] = useState('')
  const [taskAgentId, setTaskAgentId] = useState('')

  const loadProjectInfo = useCallback(async () => {
    if (!project) return
    try {
      const t = await workspaceApi.listTasks(project)
      setTasks(t.tasks ?? [])
    } catch {
      setTasks([])
    }
  }, [project])

  const loadAllAgents = useCallback(async () => {
    try {
      const data = await workspaceApi.listAllAgents()
      setAllAgents(data.agents ?? [])
    } catch { /* ignore */ }
  }, [])

  useEffect(() => { void loadProjectInfo(); void loadAllAgents() }, [loadProjectInfo, loadAllAgents])

  // Chat functions
  const scrollToBottom = () => { chatEndRef.current?.scrollIntoView({ behavior: 'smooth' }) }
  useEffect(scrollToBottom, [messages])

  const sendMessage = async () => {
    if (!inputValue.trim() || loading) return
    const userMsg: ChatMessage = { role: 'user', content: inputValue.trim(), timestamp: new Date() }
    setMessages((prev) => [...prev, userMsg])
    setInputValue('')
    setLoading(true)
    setError('')
    try {
      const task = await workspaceApi.createTask({
        project_id: project,
        agent_id: selectedAgentId || undefined,
        title: userMsg.content.slice(0, 80),
        prompt: userMsg.content,
      })
      const result = await workspaceApi.startRun(task.id, { agent_id: selectedAgentId || undefined })
      const assistantMsg: ChatMessage = {
        role: 'assistant',
        content: `Run started: ${result.run_id}`,
        timestamp: new Date(),
        runId: result.run_id,
        status: 'running',
      }
      setMessages((prev) => [...prev, assistantMsg])
      pollRun(result.run_id)
    } catch (f) {
      setError(message(f))
      setMessages((prev) => [...prev, { role: 'assistant', content: `Error: ${message(f)}`, timestamp: new Date(), status: 'failed' }])
    } finally { setLoading(false) }
  }

  const pollRun = (runId: string) => {
    const poll = async () => {
      try {
        const run = await workspaceApi.getRun(runId)
        if (run.status === 'completed' || run.status === 'failed' || run.status === 'turn_limit') {
          setMessages((prev) => prev.map((m) =>
            m.runId === runId ? { ...m, content: run.answer || run.error || 'No answer', status: run.status } : m
          ))
          return
        }
        setTimeout(poll, 3000)
      } catch { setTimeout(poll, 5000) }
    }
    setTimeout(poll, 2000)
  }

  const createTask = async () => {
    if (!taskTitle.trim() || !taskPrompt.trim()) return
    setLoading(true); setError('')
    try {
      await workspaceApi.createTask({ project_id: project, agent_id: taskAgentId || undefined, title: taskTitle.trim(), prompt: taskPrompt.trim() })
      setTaskTitle(''); setTaskPrompt(''); setTaskAgentId(''); setShowTaskForm(false)
      void loadProjectInfo()
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }

  const startRun = async (task: Task) => {
    setLoading(true); setError('')
    try {
      const result = await workspaceApi.startRun(task.id, { agent_id: task.agent_id || undefined })
      setMessages((prev) => [...prev, { role: 'user', content: `[Task] ${task.title}`, timestamp: new Date() }])
      const assistantMsg: ChatMessage = { role: 'assistant', content: `Run started: ${result.run_id}`, timestamp: new Date(), runId: result.run_id, status: 'running' }
      setMessages((prev) => [...prev, assistantMsg])
      pollRun(result.run_id)
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }

  const loadRun = useCallback(async (runId: string) => {
    try {
      const run = await workspaceApi.getRun(runId)
      setSelectedRun(run)
    } catch { /* ignore */ }
  }, [])

  return (
    <div style={{ padding: '1rem' }}>
      {error && <InlineNotification kind="error" title="Error" subtitle={error} onClose={() => setError('')} lowContrast style={{ marginBottom: '1rem' }} />}

      <Grid>
        {/* Chat panel */}
        <Column sm={4} md={5} lg={7}>
          <Tile style={{ display: 'flex', flexDirection: 'column', height: 'calc(100vh - 140px)' }}>
            <div style={{ flex: 1, overflowY: 'auto', padding: '0.5rem 0' }}>
              {messages.length === 0 && (
                <div style={{ textAlign: 'center', color: '#7e8a9c', padding: '3rem 1rem' }}>
                  <Heading>Temporality Agent</Heading>
                  <p>Send a message to start a conversation.</p>
                </div>
              )}
              {messages.map((msg, i) => (
                <div key={i} style={{ marginBottom: '0.75rem', display: 'flex', justifyContent: msg.role === 'user' ? 'flex-end' : 'flex-start' }}>
                  <div style={{
                    maxWidth: '80%', padding: '0.75rem', borderRadius: '8px',
                    background: msg.role === 'user' ? '#57d7e8' : '#121823',
                    color: msg.role === 'user' ? '#080b10' : '#e5e9f0',
                    border: msg.role === 'user' ? 'none' : '1px solid #344258',
                  }}>
                    <div style={{ whiteSpace: 'pre-wrap' }}>{msg.content}</div>
                    {msg.runId && (
                      <div style={{ marginTop: '0.5rem', fontSize: '0.75rem', color: msg.role === 'user' ? '#080b10' : '#7e8a9c' }}>
                        <Tag type={STATUS_COLORS[msg.status || 'pending'] || 'gray'} size="sm">{msg.status || 'pending'}</Tag>
                        <Button size="sm" kind="ghost" onClick={() => { if (msg.runId) void loadRun(msg.runId) }} style={{ marginLeft: '0.5rem' }}>Details</Button>
                      </div>
                    )}
                  </div>
                </div>
              ))}
              <div ref={chatEndRef} />
            </div>

            {/* Agent selector */}
            <div style={{ marginBottom: '0.5rem' }}>
              <Select id="chat-agent" labelText="" hideLabel value={selectedAgentId} onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setSelectedAgentId(e.target.value)} size="sm">
                <SelectItem value="" text="Default agent" />
                {allAgents.map((a) => <SelectItem key={a.id} value={a.id} text={`${a.name} (${a.project_id || 'global'})`} />)}
              </Select>
            </div>

            {/* Input */}
            <div style={{ display: 'flex', gap: '0.5rem' }}>
              <TextInput id="chat-input" labelText="" hideLabel value={inputValue} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setInputValue(e.target.value)}
                placeholder="Type a message..." onKeyDown={(e: React.KeyboardEvent) => { if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); void sendMessage() } }} />
              <Button renderIcon={Send} onClick={() => void sendMessage()} disabled={loading || !inputValue.trim()}>Send</Button>
            </div>
          </Tile>
        </Column>

        {/* Sidebar: Tasks + Run detail */}
        <Column sm={4} md={3} lg={5}>
          <Stack gap={3}>
            {/* Agent link */}
            <Tile style={{ padding: '0.75rem', background: '#0d1118' }}>
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
                <span style={{ fontSize: '0.875rem', color: '#7e8a9c' }}>Agents configured in <a href="/agent-config" style={{ color: '#57d7e8' }}>/agent-config</a></span>
              </div>
            </Tile>

            {/* Tasks */}
            <Section level={3}>
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '0.5rem' }}>
                <Heading>Tasks</Heading>
                <Button size="sm" kind="ghost" renderIcon={Add} onClick={() => setShowTaskForm(true)}>New</Button>
              </div>
              <Stack gap={1}>
                {tasks.map((t) => (
                  <Tile key={t.id} style={{ padding: '0.5rem', cursor: 'pointer' }} onClick={() => void startRun(t)}>
                    <strong>{t.title}</strong>
                    <br />
                    <Tag type="gray" size="sm">{t.agent_id || 'default'}</Tag>
                  </Tile>
                ))}
              </Stack>
              {showTaskForm && (
                <Tile style={{ marginTop: '0.5rem' }}>
                  <Stack gap={2}>
                    <Heading>New Task</Heading>
                    <TextInput id="task-title" labelText="Title" value={taskTitle} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setTaskTitle(e.target.value)} placeholder="Fix the bug" autoFocus />
                    <TextInput id="task-prompt" labelText="Prompt" value={taskPrompt} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setTaskPrompt(e.target.value)} placeholder="Describe what the agent should do..." />
                    <Select id="task-agent" labelText="Agent" value={taskAgentId} onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setTaskAgentId(e.target.value)}>
                      <SelectItem value="" text="Default" />
                      {allAgents.map((a) => <SelectItem key={a.id} value={a.id} text={`${a.name} (${a.project_id || 'global'})`} />)}
                    </Select>
                    <Stack orientation="horizontal" gap={2}>
                      <Button size="sm" onClick={() => void createTask()}>Create</Button>
                      <Button size="sm" kind="secondary" onClick={() => setShowTaskForm(false)}>Cancel</Button>
                    </Stack>
                  </Stack>
                </Tile>
              )}
            </Section>

            {/* Selected Run */}
            {selectedRun && (
              <Section level={3}>
                <Heading>Run: {selectedRun.run_id}</Heading>
                <Tile>
                  <Tag type={STATUS_COLORS[selectedRun.status] || 'gray'}>{selectedRun.status}</Tag>
                  <span style={{ marginLeft: '0.5rem' }}>Turns: {selectedRun.turns}</span>
                  {selectedRun.answer && (
                    <pre style={{ background: '#121823', padding: '0.75rem', marginTop: '0.5rem', fontSize: '0.75rem', overflow: 'auto', maxHeight: '300px', border: '1px solid #344258' }}>
                      {selectedRun.answer}
                    </pre>
                  )}
                  <Button kind="ghost" size="sm" renderIcon={ArrowRight} href={`/experience?project=${selectedRun.project_id}`} target="_blank" style={{ marginTop: '0.5rem' }}>
                    Timeline
                  </Button>
                </Tile>
              </Section>
            )}
          </Stack>
        </Column>
      </Grid>

      {loading && <Loading withOverlay={false} />}
    </div>
  )
}