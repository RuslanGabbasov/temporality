import { useEffect, useState, useCallback, useRef, type ReactNode } from 'react'
import {
  Header,
  HeaderName,
  HeaderGlobalBar,
  HeaderPanel,
  Content,
  Theme,
  Button,
  TextInput,
  Select,
  SelectItem,
  Modal,
} from '@carbon/react'
import { User, Settings, Add, Edit, TrashCan } from '@carbon/icons-react'
import { Menu } from '@carbon/icons-react'
import { TOKEN_STORAGE_KEY, authToken, authHeaders } from './api'

const KERNEL_API = '/kernel-api'

export interface AppState {
  project: string
  setProject: (project: string) => void
}

interface LayoutProps {
  children: (state: AppState) => ReactNode
  activePage: string
}

const NAV_ITEMS = [
  { path: '/workspace', label: 'Workspace' },
  { path: '/agent-config', label: 'Agents' },
  { path: '/agents', label: 'Runs' },
  { path: '/operations', label: 'Operations' },
  { path: '/observability', label: 'Knowledge' },
  { path: '/experience', label: 'Timeline' },
  { path: '/providers', label: 'Providers' },
  { path: '/triggers', label: 'Triggers' },
  { path: '/users', label: 'Users' },
]

interface Project {
  id: string
  name: string
  description: string
  default_agent_id?: string
  default_model?: string
}

export default function Layout({ children, activePage }: LayoutProps) {
  const params = new URLSearchParams(window.location.search)
  const [project, setProject] = useState(params.get('project') ?? 'lighthouse')
  const [projects, setProjects] = useState<Project[]>([])
  const [allAgents, setAllAgents] = useState<{ id: string; name: string }[]>([])
  const [showProjectPanel, setShowProjectPanel] = useState(false)
  const [showLogin, setShowLogin] = useState(false)
  const [mobileNavOpen, setMobileNavOpen] = useState(false)
  const [token, setToken] = useState(authToken() ?? '')
  const [loggedIn, setLoggedIn] = useState(!!authToken())

  // Project management
  const [editProject, setEditProject] = useState<Project | null>(null)
  const [newProjectName, setNewProjectName] = useState('')
  const [newProjectDesc, setNewProjectDesc] = useState('')

  // Pending operations badge
  const [pendingOps, setPendingOps] = useState(0)
  // Pending approvals badge (for Runs tab)
  const [pendingApprovals, setPendingApprovals] = useState(0)

  useEffect(() => {
    if (!authToken()) return
    const poll = async () => {
      try {
        const resp = await fetch(`${KERNEL_API}/v1/agent/operations?project=${encodeURIComponent(project)}`, { headers: { ...authHeaders() } })
        if (resp.ok) {
          const data = await resp.json()
          setPendingOps(data.count ?? (data.operations ?? []).length)
        }
      } catch { /* ignore */ }
      try {
        // Count unresolved approval requests in this project
        const resp = await fetch(`/api/v1/observations/events?project=${encodeURIComponent(project)}&type=approval.requested&limit=50`, { headers: { ...authHeaders() } })
        if (resp.ok) {
          const data = await resp.json()
          const requested: string[] = []
          for (const ev of (data.events ?? []) as { data?: { operation_id?: string } }[]) {
            if (ev.data?.operation_id) requested.push(ev.data.operation_id)
          }
          if (requested.length > 0) {
            // Check which ones have been resolved
            const resp2 = await fetch(`/api/v1/observations/events?project=${encodeURIComponent(project)}&type=approval.granted&limit=50`, { headers: { ...authHeaders() } })
            const resp3 = await fetch(`/api/v1/observations/events?project=${encodeURIComponent(project)}&type=approval.rejected&limit=50`, { headers: { ...authHeaders() } })
            const resolved = new Set<string>()
            for (const r of [resp2, resp3]) {
              if (r.ok) {
                const d = await r.json()
                for (const ev of (d.events ?? []) as { data?: { operation_id?: string } }[]) {
                  if (ev.data?.operation_id) resolved.add(ev.data.operation_id)
                }
              }
            }
            setPendingApprovals(requested.filter((id) => !resolved.has(id)).length)
          } else {
            setPendingApprovals(0)
          }
        }
      } catch { /* ignore */ }
    }
    void poll()
    const timer = window.setInterval(poll, 15000)
    return () => window.clearInterval(timer)
  }, [project])

  const loadProjects = useCallback(async () => {
    try {
      const resp = await fetch('/kernel-api/v1/workspace/projects', { headers: { ...authHeaders() } })
      if (resp.ok) {
        const data = await resp.json()
        setProjects(data.projects ?? [])
      }
    } catch { /* ignore */ }
  }, [])

  useEffect(() => { void loadProjects() }, [loadProjects])

  useEffect(() => {
    const loadAgents = async () => {
      try {
        const resp = await fetch('/kernel-api/v1/workspace/agents', { headers: { ...authHeaders() } })
        if (resp.ok) { const data = await resp.json(); setAllAgents(data.agents ?? []) }
      } catch { /* ignore */ }
    }
    void loadAgents()
  }, [])

  const navigate = (path: string) => {
    window.history.pushState(null, '', path)
    window.dispatchEvent(new PopStateEvent('popstate'))
  }

  useEffect(() => {
    const p = new URLSearchParams(window.location.search)
    p.set('project', project)
    window.history.replaceState(null, '', `${window.location.pathname}?${p}`)
  }, [project])

  // Login
  const doLogin = () => {
    const t = token.trim()
    if (!t) return
    localStorage.setItem(TOKEN_STORAGE_KEY, t)
    setLoggedIn(true)
    setShowLogin(false)
    window.location.reload()
  }

  const doLogout = () => {
    localStorage.removeItem(TOKEN_STORAGE_KEY)
    setLoggedIn(false)
    setToken('')
    window.location.reload()
  }

  // Project CRUD
  const saveProject = async () => {
    const id = editProject?.id ?? newProjectName.trim().toLowerCase().replace(/\s+/g, '-')
    const body = {
      id,
      name: editProject ? editProject.name : newProjectName.trim(),
      description: editProject ? editProject.description : newProjectDesc.trim(),
      default_agent_id: editProject?.default_agent_id ?? '',
      default_model: editProject?.default_model ?? '',
    }
    const url = editProject ? `/kernel-api/v1/workspace/projects/${id}` : '/kernel-api/v1/workspace/projects'
    const method = editProject ? 'PUT' : 'POST'
    await fetch(url, { method, headers: { 'Content-Type': 'application/json', ...authHeaders() }, body: JSON.stringify(body) })
    setEditProject(null); setNewProjectName(''); setNewProjectDesc('')
    void loadProjects()
  }

  const deleteProject = async (id: string) => {
    if (!confirm('Delete this project?')) return
    await fetch(`/kernel-api/v1/workspace/projects/${id}`, { method: 'DELETE', headers: { ...authHeaders() } })
    if (project === id) setProject('lighthouse')
    void loadProjects()
  }

  return (
    <Theme theme="g100">
      <Header aria-label="Temporality">
        <HeaderName href="/experience" prefix="" onClick={(e: React.MouseEvent) => { e.preventDefault(); navigate('/experience') }}>
          Temporality
        </HeaderName>

        {/* Desktop nav — hidden below 1100px */}
        <nav className="header-nav-desktop" aria-label="Main navigation">
          {NAV_ITEMS.map(({ path, label }) => (
            <a
              key={path}
              href={path}
              className={`header-nav-link ${activePage === path.slice(1) ? 'active' : ''}`}
              onClick={(e: React.MouseEvent) => { e.preventDefault(); navigate(path) }}
            >
              {label}
              {path === '/operations' && pendingOps > 0 && (
                <span className="nav-badge" style={{ background: '#f7768e' }}>{pendingOps}</span>
              )}
              {path === '/agents' && pendingApprovals > 0 && (
                <span className="nav-badge" style={{ background: '#e6b85c' }}>{pendingApprovals}</span>
              )}
            </a>
          ))}
        </nav>

        {/* Mobile hamburger — visible below 1100px */}
        <button
          className="header-hamburger"
          onClick={() => setMobileNavOpen(!mobileNavOpen)}
          aria-label="Toggle navigation"
        >
          <Menu size={20} />
        </button>

        <HeaderGlobalBar>
          {/* Project switcher */}
          <div style={{ position: 'relative', marginRight: '0.5rem' }}>
            <Button
              kind="ghost"
              size="sm"
              onClick={() => setShowProjectPanel(!showProjectPanel)}
              style={{ color: '#e5e9f0' }}
            >
              {project}
            </Button>
            {showProjectPanel && (
              <HeaderPanel expanded>
                <div style={{ padding: '0.5rem' }}>
                  <div style={{ fontSize: '0.75rem', color: '#7e8a9c', padding: '0.5rem 0.5rem 0.25rem', fontWeight: 600, letterSpacing: '0.05em', textTransform: 'uppercase' }}>Projects</div>
                  <div style={{ display: 'grid', gap: '1px' }}>
                    {projects.map((p) => (
                      <div
                        key={p.id}
                        onClick={() => { setProject(p.id); setShowProjectPanel(false) }}
                        style={{ display: 'grid', gridTemplateColumns: '1fr auto', alignItems: 'center', padding: '0.5rem 0.5rem', cursor: 'pointer', background: p.id === project ? '#121823' : 'transparent', borderRadius: '4px', minWidth: 0 }}
                      >
                        <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{p.name}</span>
                        <div style={{ display: 'flex', gap: '0.25rem', flexShrink: 0 }}>
                          <Button size="sm" kind="ghost" hasIconOnly renderIcon={Edit} iconDescription="Edit" onClick={(e: React.MouseEvent) => { e.stopPropagation(); setEditProject(p); setShowProjectPanel(false) }} />
                          <Button size="sm" kind="danger--ghost" hasIconOnly renderIcon={TrashCan} iconDescription="Delete" onClick={(e: React.MouseEvent) => { e.stopPropagation(); void deleteProject(p.id) }} />
                        </div>
                      </div>
                    ))}
                    <div
                      onClick={() => { setEditProject(null); setShowProjectPanel(false); setNewProjectName(''); setNewProjectDesc('') }}
                      style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', padding: '0.5rem 0.5rem', cursor: 'pointer', color: '#57d7e8', borderRadius: '4px' }}
                    >
                      <Add size={16} /> New project
                    </div>
                  </div>
                </div>
              </HeaderPanel>
            )}
          </div>

          {/* Login/Logout */}
          {loggedIn ? (
            <Button kind="ghost" size="sm" renderIcon={User} onClick={doLogout} style={{ color: '#e5e9f0' }}>
              Logout
            </Button>
          ) : (
            <Button kind="ghost" size="sm" renderIcon={User} onClick={() => setShowLogin(true)} style={{ color: '#e5e9f0' }}>
              Login
            </Button>
          )}
        </HeaderGlobalBar>

        {/* Mobile nav dropdown */}
        {mobileNavOpen && (
          <div className="header-mobile-nav">
            {NAV_ITEMS.map(({ path, label }) => (
              <a
                key={path}
                href={path}
                className={`header-mobile-link ${activePage === path.slice(1) ? 'active' : ''}`}
                onClick={(e: React.MouseEvent) => { e.preventDefault(); navigate(path); setMobileNavOpen(false) }}
              >
                {label}
                {path === '/operations' && pendingOps > 0 && (
                  <span className="nav-badge" style={{ background: '#f7768e' }}>{pendingOps}</span>
                )}
                {path === '/agents' && pendingApprovals > 0 && (
                  <span className="nav-badge" style={{ background: '#e6b85c' }}>{pendingApprovals}</span>
                )}
              </a>
            ))}
          </div>
        )}
      </Header>

      {/* Login modal */}
      <Modal
        open={showLogin}
        onRequestClose={() => setShowLogin(false)}
        modalHeading="Sign in"
        primaryButtonText="Sign in"
        secondaryButtonText="Cancel"
        onRequestSubmit={doLogin}
      >
        <p style={{ marginBottom: '1rem', color: '#7e8a9c' }}>
          Enter your API token to access the Temporality workspace.
        </p>
        <TextInput
          id="api-token"
          labelText="API Token"
          type="password"
          value={token}
          onChange={(e: React.ChangeEvent<HTMLInputElement>) => setToken(e.target.value)}
          placeholder="Enter your bearer token"
        />
      </Modal>

      {/* Project edit modal */}
      <Modal
        open={!!editProject || (showProjectPanel === false && newProjectName !== '')}
        onRequestClose={() => { setEditProject(null); setNewProjectName(''); setNewProjectDesc('') }}
        modalHeading={editProject ? 'Edit Project' : 'New Project'}
        primaryButtonText={editProject ? 'Save' : 'Create'}
        secondaryButtonText="Cancel"
        onRequestSubmit={saveProject}
      >
        {!editProject && (
          <TextInput
            id="project-id"
            labelText="Project ID"
            value={newProjectName}
            onChange={(e: React.ChangeEvent<HTMLInputElement>) => setNewProjectName(e.target.value)}
            placeholder="my-project"
          />
        )}
        <TextInput
          id="project-name"
          labelText="Name"
          value={editProject?.name ?? newProjectName}
          onChange={(e: React.ChangeEvent<HTMLInputElement>) => {
            if (editProject) setEditProject({ ...editProject, name: e.target.value })
            else setNewProjectName(e.target.value)
          }}
          placeholder="My Project"
        />
        <TextInput
          id="project-desc"
          labelText="Description"
          value={editProject?.description ?? newProjectDesc}
          onChange={(e: React.ChangeEvent<HTMLInputElement>) => {
            if (editProject) setEditProject({ ...editProject, description: e.target.value })
            else setNewProjectDesc(e.target.value)
          }}
          placeholder="What this project is about"
        />
        {editProject && (
          <>
            <Select
              id="project-agent"
              labelText="Default agent"
              value={editProject.default_agent_id ?? ''}
              onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setEditProject({ ...editProject, default_agent_id: e.target.value })}
            >
              <SelectItem value="" text="None (user chooses)" />
              {allAgents.map((a) => <SelectItem key={a.id} value={a.id} text={a.name} />)}
            </Select>
            <TextInput
              id="project-model"
              labelText="Default model (override)"
              value={editProject.default_model ?? ''}
              onChange={(e: React.ChangeEvent<HTMLInputElement>) => setEditProject({ ...editProject, default_model: e.target.value })}
              placeholder="Leave empty to use agent's model"
            />
          </>
        )}
      </Modal>

      <Content id="main-content">
        {children({ project, setProject })}
      </Content>
    </Theme>
  )
}