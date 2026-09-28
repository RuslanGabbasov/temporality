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
  Tile,
  Grid,
  Column,
  Stack,
  Section,
  Heading,
} from '@carbon/react'
import { Add, Play, ArrowRight, Send, Time, Edit, TrashCan } from '@carbon/icons-react'
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
  const [agents, setAgents] = useState<Agent[]>([])
  const [allAgents, setAllAgents] = useState<Agent[]>([])
  const [tasks, setTasks] = useState<Task[]>([])
  const [runs, setRuns] = useState<Run[]>([])
  const [selectedRun, setSelectedRun] = useState<Run | null>(null)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)

  // Chat state
  const [messages, setMessages] = useState<ChatMessage[]>([])
  const [inputValue, setInputValue] = useState('')
  const [selectedAgentId, setSelectedAgentId] = useState('')
  const chatEndRef = useRef<HTMLDivElement>(null)

  // Agent form
  const [showAgentForm, setShowAgentForm] = useState(false)
  const [agentName, setAgentName] = useState('')
  const [agentModel, setAgentModel] = useState('')
  const [agentPrompt, setAgentPrompt] = useState('')
  const [editingAgent, setEditingAgent] = useState<Agent | null>(null)

  // Task form
  const [showTaskForm, setShowTaskForm] = useState(false)
  const [taskTitle, setTaskTitle] = useState('')
  const [taskPrompt, setTaskPrompt] = useState('')
  const [taskAgentId, setTaskAgentId] = useState('')

  const loadProjectInfo = useCallback(async () => {
    if (!project) return
    try {
      const [a, t] = await Promise.all([
        workspaceApi.listAgentsByProject(project),
        workspaceApi.listTasks(project),
      ])
      setAgents(a.agents ?? [])
      setTasks(t.tasks ?? [])
    } catch {
      setAgents([])
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
      // Auto-create task from chat message
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
      // Start polling for the run
      pollRun(result.run_id, assistantMsg)
    } catch (f) {
      setError(message(f))
      setMessages((prev) => [...prev, { role: 'assistant', content: `Error: ${message(f)}`, timestamp: new Date(), status: 'failed' }])
    } finally { setLoading(false) }
  }

  const pollRun = (runId: string, msg: ChatMessage) => {
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

  // Agent CRUD
  const createOrUpdateAgent = async () => {
    if (!agentName.trim()) return
    setLoading(true); setError('')
    try {
      if (editingAgent) {
        await workspaceApi.updateAgent(editingAgent.id, { name: agentName.trim(), model: agentModel.trim(), system_prompt: agentPrompt })
      } else {
        await workspaceApi.createAgent({ project_id: project, name: agentName.trim(), model: agentModel.trim(), system_prompt: agentPrompt })
      }
      setAgentName(''); setAgentModel(''); setAgentPrompt(''); setShowAgentForm(false); setEditingAgent(null)
      void loadProjectInfo()
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }

  const deleteAgent = async (agent: Agent) => {
    if (!confirm(`Delete agent ${agent.name}?`)) return
    setLoading(true); setError('')
    try {
      await workspaceApi.deleteAgent(agent.id)
      void loadProjectInfo()
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }

  const startEditAgent = (agent: Agent) => {
    setEditingAgent(agent)
    setAgentName(agent.name)
    setAgentModel(agent.model)
    setAgentPrompt(agent.system_prompt)
    setShowAgentForm(true)
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
      // Add to chat
      setMessages((prev) => [...prev, { role: 'user', content: `[Task] ${task.title}`, timestamp: new Date() }])
      const assistantMsg: ChatMessage = { role: 'assistant', content: `Run started: ${result.run_id}`, timestamp: new Date(), runId: result.run_id, status: 'running' }
      setMessages((prev) => [...prev, assistantMsg])
      pollRun(result.run_id, assistantMsg)
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }

  const loadRun = useCallback(async (runId: string) => {
    try {
      const run = await workspaceApi.getRun(runId)
      setSelectedRun(run)
      setRuns((prev) => prev.map((r) => r.id === run.id ? run : r))
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
                  <p>Send a message to start a conversation with the agent.</p>
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
                        <Button size="sm" kind="ghost" onClick={() => { if (msg.runId) void loadRun(msg.runId) }} style={{ marginLeft: '0.5rem' }}>View details</Button>
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
                {allAgents.map((a) => <SelectItem key={a.id} value={a.id} text={`${a.name} (${a.project_id})`} />)}
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

        {/* Sidebar: Agents + Tasks + Runs */}
        <Column sm={4} md={3} lg={5}>
          <Stack gap={3}>
            {/* Agents */}
            <Section level={3}>
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '0.5rem' }}>
                <Heading>Agents</Heading>
                <Button size="sm" kind="ghost" renderIcon={Add} onClick={() => setShowAgentForm(true)}>New</Button>
              </div>
              <Stack gap={1}>
                {agents.map((a) => (
                  <Tile key={a.id} style={{ padding: '0.5rem' }}>
                    <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
                      <div>
                        <strong>{a.name}</strong>
                        {a.model && <Tag type="blue" size="sm" style={{ marginLeft: '0.25rem' }}>{a.model}</Tag>}
                      </div>
                      <Stack orientation="horizontal" gap={1}>
                        <Button size="sm" kind="ghost" hasIconOnly renderIcon={Edit} iconDescription="Edit" onClick={() => startEditAgent(a)} />
                        <Button size="sm" kind="danger--ghost" hasIconOnly renderIcon={TrashCan} iconDescription="Delete" onClick={() => void deleteAgent(a)} />
                      </Stack>
                    </div>
                    {a.system_prompt && <small style={{ color: '#7e8a9c', display: 'block', marginTop: '0.25rem' }}>{a.system_prompt.length > 60 ? a.system_prompt.slice(0, 60) + '…' : a.system_prompt}</small>}
                  </Tile>
                ))}
              </Stack>
              {showAgentForm && (
                <Tile style={{ marginTop: '0.5rem' }}>
                  <Stack gap={2}>
                    <Heading>{editingAgent ? 'Edit Agent' : 'New Agent'}</Heading>
                    <TextInput id="agent-name" labelText="Name" value={agentName} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setAgentName(e.target.value)} placeholder="coder" autoFocus />
                    <TextInput id="agent-model" labelText="Model" value={agentModel} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setAgentModel(e.target.value)} placeholder="gpt-4o" />
                    <TextArea id="agent-prompt" labelText="System prompt" value={agentPrompt} onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => setAgentPrompt(e.target.value)} rows={4} placeholder="You are a careful developer..." />
                    <Stack orientation="horizontal" gap={2}>
                      <Button size="sm" onClick={() => void createOrUpdateAgent()}>{editingAgent ? 'Save' : 'Create'}</Button>
                      <Button size="sm" kind="secondary" onClick={() => { setShowAgentForm(false); setEditingAgent(null) }}>Cancel</Button>
                    </Stack>
                  </Stack>
                </Tile>
              )}
            </Section>

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
                    <TextArea id="task-prompt" labelText="Prompt" value={taskPrompt} onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => setTaskPrompt(e.target.value)} rows={5} placeholder="Describe what the agent should do..." />
                    <Select id="task-agent" labelText="Agent" value={taskAgentId} onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setTaskAgentId(e.target.value)}>
                      <SelectItem value="" text="Default" />
                      {allAgents.map((a) => <SelectItem key={a.id} value={a.id} text={`${a.name} (${a.project_id})`} />)}
                    </Select>
                    <Stack orientation="horizontal" gap={2}>
                      <Button size="sm" onClick={() => void createTask()}>Create</Button>
                      <Button size="sm" kind="secondary" onClick={() => setShowTaskForm(false)}>Cancel</Button>
                    </Stack>
                  </Stack>
                </Tile>
              )}
            </Section>

            {/* Recent Runs */}
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