import { useCallback, useEffect, useState } from 'react'
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
  Layer,
} from '@carbon/react'
import {
  Add,
  Play,
  ArrowRight,
  Time,
} from '@carbon/icons-react'
import { workspaceApi, type Project, type Agent, type Task, type Run } from './workspaceApi'

function message(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }
function shortTime(iso: string) {
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? '' : d.toLocaleTimeString()
}

const STATUS_TAGS: Record<string, { type: string; label: string }> = {
  completed: { type: 'green', label: 'Completed' },
  failed: { type: 'red', label: 'Failed' },
  running: { type: 'blue', label: 'Running' },
  turn_limit: { type: 'warm-gray', label: 'Turn Limit' },
  pending: { type: 'gray', label: 'Pending' },
}

export default function Workspace({ project, setProject }: { project: string; setProject: (p: string) => void }) {
  const [projects, setProjects] = useState<Project[]>([])
  const [selectedProject, setSelectedProject] = useState<Project | null>(null)
  const [agents, setAgents] = useState<Agent[]>([])
  const [tasks, setTasks] = useState<Task[]>([])
  const [selectedTask, setSelectedTask] = useState<Task | null>(null)
  const [runs, setRuns] = useState<Run[]>([])
  const [selectedRun, setSelectedRun] = useState<Run | null>(null)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)

  // Project form
  const [showProjectForm, setShowProjectForm] = useState(false)
  const [projectName, setProjectName] = useState('')
  const [projectDesc, setProjectDesc] = useState('')

  // Agent form
  const [showAgentForm, setShowAgentForm] = useState(false)
  const [agentName, setAgentName] = useState('')
  const [agentModel, setAgentModel] = useState('')
  const [agentPrompt, setAgentPrompt] = useState('')
  const [allAgents, setAllAgents] = useState<Agent[]>([])
  const [editingAgent, setEditingAgent] = useState<Agent | null>(null)

  // Task form
  const [showTaskForm, setShowTaskForm] = useState(false)
  const [taskTitle, setTaskTitle] = useState('')
  const [taskPrompt, setTaskPrompt] = useState('')
  const [taskAgentId, setTaskAgentId] = useState('')

  const loadProjects = useCallback(async () => {
    try {
      const [projectsData, agentsData] = await Promise.all([
        workspaceApi.listProjects(),
        workspaceApi.listAllAgents(),
      ])
      setProjects(projectsData.projects ?? [])
      setAllAgents(agentsData.agents ?? [])
    } catch (f) { setError(message(f)) }
  }, [])

  const loadProjectData = useCallback(async (project: Project) => {
    setSelectedProject(project)
    setSelectedTask(null)
    setSelectedRun(null)
    try {
      const [a, t] = await Promise.all([
        workspaceApi.listAgents(project.id),
        workspaceApi.listTasks(project.id),
      ])
      setAgents(a.agents ?? [])
      setTasks(t.tasks ?? [])
    } catch (f) { setError(message(f)) }
  }, [])

  const loadTaskRuns = useCallback(async (task: Task) => {
    setSelectedTask(task)
    setSelectedRun(null)
    try {
      const data = await workspaceApi.listRuns(task.id)
      setRuns(data.runs ?? [])
    } catch (f) { setError(message(f)) }
  }, [])

  const loadRun = useCallback(async (runId: string) => {
    try {
      const run = await workspaceApi.getRun(runId)
      setSelectedRun(run)
      setRuns((prev) => prev.map((r) => r.id === run.id ? run : r))
    } catch (f) { setError(message(f)) }
  }, [])

  useEffect(() => { void loadProjects() }, [loadProjects])

  useEffect(() => {
    if (!selectedRun || selectedRun.status === 'completed' || selectedRun.status === 'failed' || selectedRun.status === 'turn_limit') return
    const timer = setInterval(() => { void loadRun(selectedRun.id) }, 3000)
    return () => clearInterval(timer)
  }, [selectedRun, loadRun])

  const createProject = async () => {
    if (!projectName.trim()) return
    setLoading(true); setError('')
    try {
      await workspaceApi.createProject({ name: projectName.trim(), description: projectDesc.trim() })
      setProjectName(''); setProjectDesc(''); setShowProjectForm(false)
      await loadProjects()
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }

  const createOrUpdateAgent = async () => {
    if (!agentName.trim()) return
    setLoading(true); setError('')
    try {
      if (editingAgent) {
        await workspaceApi.updateAgent(editingAgent.id, {
          name: agentName.trim(),
          model: agentModel.trim(),
          system_prompt: agentPrompt,
        })
      } else if (selectedProject) {
        await workspaceApi.createAgent({
          project_id: selectedProject.id,
          name: agentName.trim(),
          model: agentModel.trim(),
          system_prompt: agentPrompt,
        })
      }
      setAgentName(''); setAgentModel(''); setAgentPrompt(''); setShowAgentForm(false); setEditingAgent(null)
      await loadProjects()
      if (selectedProject) await loadProjectData(selectedProject)
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }

  const deleteAgent = async (agent: Agent) => {
    if (!confirm(`Delete agent ${agent.name}?`)) return
    setLoading(true); setError('')
    try {
      await workspaceApi.deleteAgent(agent.id)
      await loadProjects()
      if (selectedProject) await loadProjectData(selectedProject)
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

  const [quickPrompt, setQuickPrompt] = useState('')
  const [quickAgentId, setQuickAgentId] = useState('')
  const quickRun = async () => {
    if (!quickPrompt.trim() || !project) return
    setLoading(true); setError('')
    try {
      // Auto-create task with prompt
      const task = await workspaceApi.createTask({
        project_id: project,
        agent_id: quickAgentId || undefined,
        title: `Quick: ${quickPrompt.slice(0, 50)}`,
        prompt: quickPrompt,
      })
      const result = await workspaceApi.startRun(task.id, { agent_id: quickAgentId || undefined })
      setQuickPrompt('')
      // Switch to the task/run view
      const createdTask: Task = task as unknown as Task
      setSelectedTask(createdTask)
      void loadTaskRuns(createdTask)
      void loadRun(result.run_id)
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }

  const createTask = async () => {
    if (!taskTitle.trim() || !taskPrompt.trim() || !selectedProject) return
    setLoading(true); setError('')
    try {
      await workspaceApi.createTask({
        project_id: selectedProject.id,
        agent_id: taskAgentId || undefined,
        title: taskTitle.trim(),
        prompt: taskPrompt.trim(),
      })
      setTaskTitle(''); setTaskPrompt(''); setTaskAgentId(''); setShowTaskForm(false)
      await loadProjectData(selectedProject)
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }

  const startRun = async (task: Task) => {
    setLoading(true); setError('')
    try {
      const result = await workspaceApi.startRun(task.id, { agent_id: task.agent_id || undefined })
      await loadTaskRuns(task)
      void loadRun(result.run_id)
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }

  return (
    <Grid fullWidth className="workspace-container">
      {/* Error notification */}
      {error && (
        <Column span={16}>
          <InlineNotification
            kind="error"
            title="Error"
            subtitle={error}
            onClose={() => setError('')}
            lowContrast
          />
        </Column>
      )}

      {/* Quick Run */}
      <Column sm={4} md={8} lg={16}>
        <Tile style={{ marginBottom: '1rem' }}>
          <Heading>Quick Run</Heading>
          <Stack gap={2}>
            <TextArea
              id="quick-prompt"
              labelText="Prompt"
              value={quickPrompt}
              onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => setQuickPrompt(e.target.value)}
              placeholder="Describe what the agent should do..."
              rows={3}
            />
            <Stack orientation="horizontal" gap={2}>
              <div style={{ flex: 1 }}>
                <Select
                  id="quick-agent"
                  labelText="Agent (optional)"
                  hideLabel
                  value={quickAgentId}
                  onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setQuickAgentId(e.target.value)}
                >
                  <SelectItem value="" text="Default" />
                  {allAgents.map((a) => (
                    <SelectItem key={a.id} value={a.id} text={`${a.name} (${a.project_id})`} />
                  ))}
                </Select>
              </div>
              <Button onClick={() => void quickRun()} disabled={loading || !quickPrompt.trim()}>
                Run
              </Button>
            </Stack>
          </Stack>
        </Tile>
      </Column>

      {/* Projects panel */}
      <Column sm={4} md={4} lg={4}>
        <Section level={2}>
          <Heading>Projects</Heading>
          <Stack gap={3}>
            {projects.map((p) => (
              <Tile
                key={p.id}
                onClick={() => void loadProjectData(p)}
                className={`workspace-tile ${selectedProject?.id === p.id ? 'selected' : ''}`}
              >
                <strong>{p.name}</strong>
                <br />
                <small>{p.id}</small>
              </Tile>
            ))}
            
            {showProjectForm ? (
              <Layer>
                <Stack gap={3}>
                  <TextInput
                    id="project-name"
                    labelText="Project name"
                    value={projectName}
                    onChange={(e: React.ChangeEvent<HTMLInputElement>) => setProjectName(e.target.value)}
                    placeholder="my-project"
                    autoFocus
                  />
                  <TextInput
                    id="project-desc"
                    labelText="Description (optional)"
                    value={projectDesc}
                    onChange={(e: React.ChangeEvent<HTMLInputElement>) => setProjectDesc(e.target.value)}
                    placeholder="What this project is about"
                  />
                  <Stack orientation="horizontal" gap={2}>
                    <Button onClick={() => void createProject()} disabled={loading}>
                      Create project
                    </Button>
                    <Button kind="secondary" onClick={() => setShowProjectForm(false)}>
                      Cancel
                    </Button>
                  </Stack>
                </Stack>
              </Layer>
            ) : (
              <Button renderIcon={Add} onClick={() => setShowProjectForm(true)}>
                New project
              </Button>
            )}
          </Stack>
        </Section>
      </Column>

      {/* Agents + Tasks panel */}
      {selectedProject && (
        <Column sm={4} md={4} lg={4}>
          <Section level={2}>
            <Heading>{selectedProject.name}</Heading>
            
            {/* Agents */}
            <Section level={3}>
              <Heading>Agents</Heading>
              <Stack gap={2}>
                {agents.map((a) => (
                  <Tile key={a.id}>
                    <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start' }}>
                      <div>
                        <strong>{a.name}</strong>
                        {a.model && <><br /><Tag type="blue">{a.model}</Tag></>}
                        {a.system_prompt && (
                          <>
                            <br />
                            <small className="ws-preview">
                              {a.system_prompt.length > 80
                                ? a.system_prompt.slice(0, 80) + '…'
                                : a.system_prompt}
                            </small>
                          </>
                        )}
                      </div>
                      <Stack orientation="horizontal" gap={1}>
                        <Button size="sm" kind="ghost" onClick={() => startEditAgent(a)}>Edit</Button>
                        <Button size="sm" kind="danger--ghost" onClick={() => void deleteAgent(a)}>Delete</Button>
                      </Stack>
                    </div>
                  </Tile>
                ))}

                {showAgentForm ? (
                  <Layer>
                    <Stack gap={3}>
                      <Heading>{editingAgent ? 'Edit Agent' : 'New Agent'}</Heading>
                      <TextInput
                        id="agent-name"
                        labelText="Agent name"
                        value={agentName}
                        onChange={(e: React.ChangeEvent<HTMLInputElement>) => setAgentName(e.target.value)}
                        placeholder="coder"
                        autoFocus
                      />
                      <TextInput
                        id="agent-model"
                        labelText="Model (optional)"
                        value={agentModel}
                        onChange={(e: React.ChangeEvent<HTMLInputElement>) => setAgentModel(e.target.value)}
                        placeholder="gpt-4o, claude-3-sonnet, etc."
                      />
                      <TextArea
                        id="agent-prompt"
                        labelText="System prompt"
                        value={agentPrompt}
                        onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => setAgentPrompt(e.target.value)}
                        placeholder="You are a careful developer..."
                        rows={6}
                      />
                      <Stack orientation="horizontal" gap={2}>
                        <Button onClick={() => void createOrUpdateAgent()} disabled={loading}>
                          {editingAgent ? 'Save changes' : 'Create agent'}
                        </Button>
                        <Button kind="secondary" onClick={() => { setShowAgentForm(false); setEditingAgent(null) }}>
                          Cancel
                        </Button>
                      </Stack>
                    </Stack>
                  </Layer>
                ) : (
                  <Button renderIcon={Add} size="sm" onClick={() => setShowAgentForm(true)}>
                    New agent
                  </Button>
                )}
              </Stack>
            </Section>

            {/* Tasks */}
            <Section level={3}>
              <Heading>Tasks</Heading>
              <Stack gap={2}>
                {tasks.map((t) => (
                  <Tile
                    key={t.id}
                    onClick={() => void loadTaskRuns(t)}
                    className={`workspace-tile ${selectedTask?.id === t.id ? 'selected' : ''}`}
                  >
                    <Grid>
                      <Column sm={3} md={6} lg={10}>
                        <strong>{t.title}</strong>
                        <br />
                        <Tag type="gray" size="sm">{t.agent_id || 'no agent'}</Tag>
                      </Column>
                      <Column sm={1} md={2} lg={6} className="ws-task-action">
                        <Button
                          renderIcon={Play}
                          size="sm"
                          onClick={(e: React.MouseEvent) => { e.stopPropagation(); void startRun(t) }}
                          disabled={loading}
                        >
                          Run
                        </Button>
                      </Column>
                    </Grid>
                  </Tile>
                ))}

                {showTaskForm ? (
                  <Layer>
                    <Stack gap={3}>
                      <TextInput
                        id="task-title"
                        labelText="Task title"
                        value={taskTitle}
                        onChange={(e: React.ChangeEvent<HTMLInputElement>) => setTaskTitle(e.target.value)}
                        placeholder="Fix the bug in calculator"
                        autoFocus
                      />
                      <TextArea
                        id="task-prompt"
                        labelText="Prompt"
                        value={taskPrompt}
                        onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => setTaskPrompt(e.target.value)}
                        placeholder="Find and fix the bug in calculator.go. Verify with tests."
                        rows={8}
                        helperText="Describe what the agent should do. Be specific."
                      />
                      <Select
                        id="task-agent"
                        labelText="Agent (optional, cross-project)"
                        value={taskAgentId}
                        onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setTaskAgentId(e.target.value)}
                      >
                        <SelectItem value="" text="Use defaults" />
                        {allAgents.map((a) => (
                          <SelectItem key={a.id} value={a.id} text={`${a.name} (${a.project_id})`} />
                        ))}
                      </Select>
                      <Stack orientation="horizontal" gap={2}>
                        <Button onClick={() => void createTask()} disabled={loading}>
                          Create task
                        </Button>
                        <Button kind="secondary" onClick={() => setShowTaskForm(false)}>
                          Cancel
                        </Button>
                      </Stack>
                    </Stack>
                  </Layer>
                ) : (
                  <Button renderIcon={Add} size="sm" onClick={() => setShowTaskForm(true)}>
                    New task
                  </Button>
                )}
              </Stack>
            </Section>
          </Section>
        </Column>
      )}

      {/* Runs panel */}
      {selectedTask && (
        <Column sm={4} md={4} lg={4}>
          <Section level={2}>
            <Heading>Runs: {selectedTask.title}</Heading>
            <Stack gap={2}>
              {runs.length === 0 && (
                <Tile><p>No runs yet</p></Tile>
              )}
              {runs.map((r) => (
                <Tile
                  key={r.id}
                  onClick={() => void loadRun(r.id)}
                  className={`workspace-tile ${selectedRun?.id === r.id ? 'selected' : ''}`}
                >
                  <Grid>
                    <Column sm={3} md={5} lg={10}>
                      <strong>{r.run_id}</strong>
                      <br />
                      <Tag type={STATUS_TAGS[r.status]?.type as any || 'gray'} size="sm">
                        {STATUS_TAGS[r.status]?.label || r.status}
                      </Tag>
                    </Column>
                    <Column sm={1} md={3} lg={6}>
                      <Time size={16} /> {shortTime(r.created_at)}
                    </Column>
                  </Grid>
                </Tile>
              ))}
            </Stack>
          </Section>
        </Column>
      )}

      {/* Run detail panel */}
      {selectedRun && (
        <Column sm={4} md={4} lg={4}>
          <Section level={2}>
            <Heading>Run: {selectedRun.run_id}</Heading>
            <Stack gap={3}>
              <Tile>
                <Grid>
                  <Column>
                    <Tag type={STATUS_TAGS[selectedRun.status]?.type as any || 'gray'}>
                      {STATUS_TAGS[selectedRun.status]?.label || selectedRun.status}
                    </Tag>
                  </Column>
                  <Column>Turns: {selectedRun.turns}</Column>
                  <Column>Model: {selectedRun.model || 'default'}</Column>
                  <Column>Started: {shortTime(selectedRun.created_at)}</Column>
                </Grid>
              </Tile>

              {selectedRun.answer && (
                <Tile>
                  <Heading>Answer</Heading>
                  <pre className="ws-answer">{selectedRun.answer}</pre>
                </Tile>
              )}

              {selectedRun.error && (
                <InlineNotification
                  kind="error"
                  title="Error"
                  subtitle={selectedRun.error}
                  lowContrast
                />
              )}

              <Button
                renderIcon={ArrowRight}
                kind="ghost"
                href={`/experience?project=${selectedRun.project_id}`}
                target="_blank"
              >
                Open in Experience Timeline
              </Button>
            </Stack>
          </Section>
        </Column>
      )}

      {loading && (
        <Column span={16}>
          <Loading withOverlay={false} />
        </Column>
      )}
    </Grid>
  )
}