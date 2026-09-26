import { useCallback, useEffect, useState } from 'react'
import { workspaceApi, type Project, type Agent, type Task, type Run } from './workspaceApi'

function message(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }
function shortTime(iso: string) {
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? '' : d.toLocaleTimeString()
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

  // Poll selected run while in-flight
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
      // Poll for the run
      void loadRun(result.run_id)
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }

  return <div className="workspace-shell">
    <header className="topbar obs-topbar">
      <div><span className="eyebrow">TEMPORALITY</span><h1>Workspace</h1></div>
      <div className="header-actions">
        <span className="connection">Kernel API</span>
        <a className="obs-link" href="/experience">Experience</a>
        <a className="obs-link" href="/agents">Agent runs</a>
        <a className="obs-link" href="/operations">Operations</a>
      </div>
    </header>
    {error && <div className="obs-error" role="alert">{error}<button onClick={() => setError('')}>dismiss</button></div>}
    <main className="workspace-grid">
      {/* Projects panel */}
      <section className="ws-panel ws-projects">
        <h2>Projects</h2>
        <ul className="ws-list">
          {projects.map((p) => (
            <li key={p.id} className={selectedProject?.id === p.id ? 'selected' : ''} onClick={() => void loadProjectData(p)}>
              <strong>{p.name}</strong>
              <small>{p.id}</small>
            </li>
          ))}
        </ul>
        {showProjectForm ? <div className="ws-form">
          <input value={projectName} onChange={(e) => setProjectName(e.target.value)} placeholder="Project name" autoFocus />
          <input value={projectDesc} onChange={(e) => setProjectDesc(e.target.value)} placeholder="Description (optional)" />
          <div className="ws-form-actions">
            <button className="primary" onClick={() => void createProject()} disabled={loading}>Create</button>
            <button onClick={() => setShowProjectForm(false)}>Cancel</button>
          </div>
        </div> : <button onClick={() => setShowProjectForm(true)}>+ New project</button>}
      </section>

      {/* Agents + Tasks panel */}
      {selectedProject && <section className="ws-panel ws-agents-tasks">
        <h2>{selectedProject.name}</h2>
        <div className="ws-subsection">
          <h3>Agents</h3>
          <ul className="ws-list">
            {agents.map((a) => (
              <li key={a.id}>
                <strong>{a.name}</strong>
                {a.model && <small>{a.model}</small>}
                {a.system_prompt && <small className="ws-preview">{a.system_prompt.slice(0, 60)}…</small>}
              </li>
            ))}
          </ul>
          {showAgentForm ? <div className="ws-form">
            <input value={agentName} onChange={(e) => setAgentName(e.target.value)} placeholder="Agent name" autoFocus />
            <input value={agentModel} onChange={(e) => setAgentModel(e.target.value)} placeholder="Model (optional, e.g. gpt-4o)" />
            <textarea value={agentPrompt} onChange={(e) => setAgentPrompt(e.target.value)} placeholder="System prompt (optional)" rows={4} />
            <div className="ws-form-actions">
              <button className="primary" onClick={() => void createAgent()} disabled={loading}>Create</button>
              <button onClick={() => setShowAgentForm(false)}>Cancel</button>
            </div>
          </div> : <button onClick={() => setShowAgentForm(true)}>+ New agent</button>}
        </div>
        <div className="ws-subsection">
          <h3>Tasks</h3>
          <ul className="ws-list">
            {tasks.map((t) => (
              <li key={t.id} className={selectedTask?.id === t.id ? 'selected' : ''}>
                <div onClick={() => void loadTaskRuns(t)}>
                  <strong>{t.title}</strong>
                  <small>{t.agent_id || 'no agent'}</small>
                </div>
                <button className="ws-run-btn" onClick={() => void startRun(t)} disabled={loading}>Run</button>
              </li>
            ))}
          </ul>
          {showTaskForm ? <div className="ws-form">
            <input value={taskTitle} onChange={(e) => setTaskTitle(e.target.value)} placeholder="Task title" autoFocus />
            <textarea value={taskPrompt} onChange={(e) => setTaskPrompt(e.target.value)} placeholder="Prompt" rows={4} />
            <select value={taskAgentId} onChange={(e) => setTaskAgentId(e.target.value)}>
              <option value="">No agent (use defaults)</option>
              {agents.map((a) => <option key={a.id} value={a.id}>{a.name}</option>)}
            </select>
            <div className="ws-form-actions">
              <button className="primary" onClick={() => void createTask()} disabled={loading}>Create</button>
              <button onClick={() => setShowTaskForm(false)}>Cancel</button>
            </div>
          </div> : <button onClick={() => setShowTaskForm(true)}>+ New task</button>}
        </div>
      </section>}

      {/* Runs panel */}
      {selectedTask && <section className="ws-panel ws-runs">
        <h3>Runs for: {selectedTask.title}</h3>
        <ul className="ws-list">
          {runs.map((r) => (
            <li key={r.id} className={selectedRun?.id === r.id ? 'selected' : ''} onClick={() => void loadRun(r.id)}>
              <strong>{r.run_id}</strong>
              <span className={`ws-status ws-status-${r.status}`}>{r.status}</span>
              <small>{shortTime(r.created_at)}</small>
            </li>
          ))}
          {runs.length === 0 && <li className="ws-empty">No runs yet</li>}
        </ul>
      </section>}

      {/* Run detail panel */}
      {selectedRun && <section className="ws-panel ws-run-detail">
        <h3>Run: {selectedRun.run_id}</h3>
        <div className="ws-run-meta">
          <span>status: <strong className={`ws-status ws-status-${selectedRun.status}`}>{selectedRun.status}</strong></span>
          <span>turns: {selectedRun.turns}</span>
          <span>model: {selectedRun.model || 'default'}</span>
          <span>started: {shortTime(selectedRun.created_at)}</span>
        </div>
        {selectedRun.answer && <div className="ws-run-answer">
          <h4>Answer</h4>
          <pre>{selectedRun.answer}</pre>
        </div>}
        {selectedRun.error && <div className="ws-run-error">
          <h4>Error</h4>
          <pre>{selectedRun.error}</pre>
        </div>}
        <div className="ws-run-actions">
          <a className="obs-link" href={`/experience?project=${selectedRun.project_id}`} target="_blank" rel="noopener noreferrer">
            Open in Experience Timeline →
          </a>
        </div>
      </section>}
    </main>
  </div>
}