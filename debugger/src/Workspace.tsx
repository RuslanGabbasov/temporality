import { useCallback, useEffect, useState } from 'react'
import { workspaceApi, type Project, type Agent, type Task, type Run } from './workspaceApi'

function message(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }
function shortTime(iso: string) {
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? '' : d.toLocaleTimeString()
}

const STATUS_TAGS: Record<string, { className: string; label: string }> = {
  completed: { className: 'tag--green', label: 'Completed' },
  failed: { className: 'tag--red', label: 'Failed' },
  running: { className: 'tag--blue', label: 'Running' },
  turn_limit: { className: 'tag--warm-gray', label: 'Turn Limit' },
  pending: { className: 'tag--gray', label: 'Pending' },
}

export default function Workspace() {
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

  // Task form
  const [showTaskForm, setShowTaskForm] = useState(false)
  const [taskTitle, setTaskTitle] = useState('')
  const [taskPrompt, setTaskPrompt] = useState('')
  const [taskAgentId, setTaskAgentId] = useState('')

  const loadProjects = useCallback(async () => {
    try {
      const data = await workspaceApi.listProjects()
      setProjects(data.projects ?? [])
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

  const createAgent = async () => {
    if (!agentName.trim() || !selectedProject) return
    setLoading(true); setError('')
    try {
      await workspaceApi.createAgent({
        project_id: selectedProject.id,
        name: agentName.trim(),
        model: agentModel.trim(),
        system_prompt: agentPrompt,
      })
      setAgentName(''); setAgentModel(''); setAgentPrompt(''); setShowAgentForm(false)
      await loadProjectData(selectedProject)
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
    <div className="workspace-container">
      {/* Error notification */}
      {error && (
        <div className="notification notification--error">
          <div className="notification__content">
            <div className="notification__title">Error</div>
            <div className="notification__subtitle">{error}</div>
          </div>
          <button className="notification__close" onClick={() => setError('')}>×</button>
        </div>
      )}

      <div className="grid grid--4">
        {/* Projects panel */}
        <div>
          <h2 style={{ marginBottom: '1rem' }}>Projects</h2>
          <div className="stack">
            {projects.map((p) => (
              <div
                key={p.id}
                className={`tile tile--clickable ${selectedProject?.id === p.id ? 'tile--selected' : ''}`}
                onClick={() => void loadProjectData(p)}
              >
                <strong>{p.name}</strong>
                <br />
                <small className="text-secondary">{p.id}</small>
              </div>
            ))}
            
            {showProjectForm ? (
              <div className="stack">
                <div className="form-group">
                  <label className="form-label">Project name</label>
                  <input
                    className="form-input"
                    value={projectName}
                    onChange={(e) => setProjectName(e.target.value)}
                    placeholder="my-project"
                    autoFocus
                  />
                </div>
                <div className="form-group">
                  <label className="form-label">Description (optional)</label>
                  <input
                    className="form-input"
                    value={projectDesc}
                    onChange={(e) => setProjectDesc(e.target.value)}
                    placeholder="What this project is about"
                  />
                </div>
                <div className="stack stack--horizontal stack--gap-2">
                  <button className="btn btn--primary" onClick={() => void createProject()} disabled={loading}>
                    Create project
                  </button>
                  <button className="btn btn--secondary" onClick={() => setShowProjectForm(false)}>
                    Cancel
                  </button>
                </div>
              </div>
            ) : (
              <button className="btn btn--secondary" onClick={() => setShowProjectForm(true)}>
                + New project
              </button>
            )}
          </div>
        </div>

        {/* Agents + Tasks panel */}
        {selectedProject && (
          <div>
            <h2 style={{ marginBottom: '1rem' }}>{selectedProject.name}</h2>
            
            {/* Agents */}
            <h3 style={{ marginBottom: '0.75rem' }}>Agents</h3>
            <div className="stack" style={{ marginBottom: '1.5rem' }}>
              {agents.map((a) => (
                <div key={a.id} className="tile">
                  <strong>{a.name}</strong>
                  {a.model && <><br /><span className="tag tag--blue">{a.model}</span></>}
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
              ))}
              
              {showAgentForm ? (
                <div className="stack">
                  <div className="form-group">
                    <label className="form-label">Agent name</label>
                    <input
                      className="form-input"
                      value={agentName}
                      onChange={(e) => setAgentName(e.target.value)}
                      placeholder="coder"
                      autoFocus
                    />
                  </div>
                  <div className="form-group">
                    <label className="form-label">Model (optional)</label>
                    <input
                      className="form-input"
                      value={agentModel}
                      onChange={(e) => setAgentModel(e.target.value)}
                      placeholder="gpt-4o, claude-3-sonnet, etc."
                    />
                  </div>
                  <div className="form-group">
                    <label className="form-label">System prompt</label>
                    <textarea
                      className="form-textarea"
                      value={agentPrompt}
                      onChange={(e) => setAgentPrompt(e.target.value)}
                      placeholder="You are a careful developer..."
                      rows={6}
                    />
                  </div>
                  <div className="stack stack--horizontal stack--gap-2">
                    <button className="btn btn--primary" onClick={() => void createAgent()} disabled={loading}>
                      Create agent
                    </button>
                    <button className="btn btn--secondary" onClick={() => setShowAgentForm(false)}>
                      Cancel
                    </button>
                  </div>
                </div>
              ) : (
                <button className="btn btn--secondary btn--sm" onClick={() => setShowAgentForm(true)}>
                  + New agent
                </button>
              )}
            </div>

            {/* Tasks */}
            <h3 style={{ marginBottom: '0.75rem' }}>Tasks</h3>
            <div className="stack">
              {tasks.map((t) => (
                <div
                  key={t.id}
                  className={`tile tile--clickable ${selectedTask?.id === t.id ? 'tile--selected' : ''}`}
                  onClick={() => void loadTaskRuns(t)}
                >
                  <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start' }}>
                    <div>
                      <strong>{t.title}</strong>
                      <br />
                      <span className="tag tag--gray">{t.agent_id || 'no agent'}</span>
                    </div>
                    <button
                      className="btn btn--primary btn--sm"
                      onClick={(e) => { e.stopPropagation(); void startRun(t) }}
                      disabled={loading}
                    >
                      Run
                    </button>
                  </div>
                </div>
              ))}

              {showTaskForm ? (
                <div className="stack">
                  <div className="form-group">
                    <label className="form-label">Task title</label>
                    <input
                      className="form-input"
                      value={taskTitle}
                      onChange={(e) => setTaskTitle(e.target.value)}
                      placeholder="Fix the bug in calculator"
                      autoFocus
                    />
                  </div>
                  <div className="form-group">
                    <label className="form-label">Prompt</label>
                    <textarea
                      className="form-textarea"
                      value={taskPrompt}
                      onChange={(e) => setTaskPrompt(e.target.value)}
                      placeholder="Find and fix the bug in calculator.go. Verify with tests."
                      rows={8}
                    />
                    <div className="form-helper">Describe what the agent should do. Be specific.</div>
                  </div>
                  <div className="form-group">
                    <label className="form-label">Agent (optional)</label>
                    <select
                      className="form-select"
                      value={taskAgentId}
                      onChange={(e) => setTaskAgentId(e.target.value)}
                    >
                      <option value="">Use defaults</option>
                      {agents.map((a) => (
                        <option key={a.id} value={a.id}>{a.name}</option>
                      ))}
                    </select>
                  </div>
                  <div className="stack stack--horizontal stack--gap-2">
                    <button className="btn btn--primary" onClick={() => void createTask()} disabled={loading}>
                      Create task
                    </button>
                    <button className="btn btn--secondary" onClick={() => setShowTaskForm(false)}>
                      Cancel
                    </button>
                  </div>
                </div>
              ) : (
                <button className="btn btn--secondary btn--sm" onClick={() => setShowTaskForm(true)}>
                  + New task
                </button>
              )}
            </div>
          </div>
        )}

        {/* Runs panel */}
        {selectedTask && (
          <div>
            <h3 style={{ marginBottom: '0.75rem' }}>Runs: {selectedTask.title}</h3>
            <div className="stack">
              {runs.length === 0 && (
                <div className="tile"><p className="text-secondary">No runs yet</p></div>
              )}
              {runs.map((r) => (
                <div
                  key={r.id}
                  className={`tile tile--clickable ${selectedRun?.id === r.id ? 'tile--selected' : ''}`}
                  onClick={() => void loadRun(r.id)}
                >
                  <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
                    <div>
                      <strong>{r.run_id}</strong>
                      <br />
                      <span className={`tag ${STATUS_TAGS[r.status]?.className || 'tag--gray'}`}>
                        {STATUS_TAGS[r.status]?.label || r.status}
                      </span>
                    </div>
                    <small className="text-secondary">{shortTime(r.created_at)}</small>
                  </div>
                </div>
              ))}
            </div>
          </div>
        )}

        {/* Run detail panel */}
        {selectedRun && (
          <div>
            <h3 style={{ marginBottom: '0.75rem' }}>Run: {selectedRun.run_id}</h3>
            <div className="stack">
              <div className="tile">
                <div style={{ display: 'flex', gap: '1rem', flexWrap: 'wrap' }}>
                  <span className={`tag ${STATUS_TAGS[selectedRun.status]?.className || 'tag--gray'}`}>
                    {STATUS_TAGS[selectedRun.status]?.label || selectedRun.status}
                  </span>
                  <span>Turns: {selectedRun.turns}</span>
                  <span>Model: {selectedRun.model || 'default'}</span>
                  <span>Started: {shortTime(selectedRun.created_at)}</span>
                </div>
              </div>

              {selectedRun.answer && (
                <div className="tile">
                  <h4 style={{ marginBottom: '0.5rem' }}>Answer</h4>
                  <pre className="ws-answer">{selectedRun.answer}</pre>
                </div>
              )}

              {selectedRun.error && (
                <div className="notification notification--error">
                  <div className="notification__content">
                    <div className="notification__title">Error</div>
                    <div className="notification__subtitle">{selectedRun.error}</div>
                  </div>
                </div>
              )}

              <a
                className="btn btn--ghost"
                href={`/experience?project=${selectedRun.project_id}`}
                target="_blank"
                rel="noopener noreferrer"
              >
                Open in Experience Timeline →
              </a>
            </div>
          </div>
        )}
      </div>

      {loading && (
        <div className="loading">
          <div className="loading__spinner" />
        </div>
      )}
    </div>
  )
}