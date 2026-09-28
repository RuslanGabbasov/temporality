import { useEffect, useState, useCallback, useRef, type ReactNode } from 'react'
import {
  Header,
  HeaderName,
  HeaderNavigation,
  HeaderMenuItem,
  HeaderGlobalBar,
  HeaderPanel,
  Switcher,
  SwitcherItem,
  Content,
  Theme,
  Button,
  TextInput,
  Modal,
} from '@carbon/react'
import { User, Settings, Add, Edit, TrashCan } from '@carbon/icons-react'
import { TOKEN_STORAGE_KEY, authToken, authHeaders } from './api'

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
  { path: '/users', label: 'Users' },
]

interface Project {
  id: string
  name: string
  description: string
}

export default function Layout({ children, activePage }: LayoutProps) {
  const params = new URLSearchParams(window.location.search)
  const [project, setProject] = useState(params.get('project') ?? 'lighthouse')
  const [projects, setProjects] = useState<Project[]>([])
  const [showProjectPanel, setShowProjectPanel] = useState(false)
  const [showLogin, setShowLogin] = useState(false)
  const [token, setToken] = useState(authToken() ?? '')
  const [loggedIn, setLoggedIn] = useState(!!authToken())

  // Project management
  const [editProject, setEditProject] = useState<Project | null>(null)
  const [newProjectName, setNewProjectName] = useState('')
  const [newProjectDesc, setNewProjectDesc] = useState('')

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
    const body = { id, name: editProject ? editProject.name : newProjectName.trim(), description: editProject ? editProject.description : newProjectDesc.trim() }
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

        <HeaderNavigation aria-label="Main navigation">
          {NAV_ITEMS.map(({ path, label }) => (
            <HeaderMenuItem
              key={path}
              href={path}
              isActive={activePage === path.slice(1)}
              onClick={(e: React.MouseEvent) => { e.preventDefault(); navigate(path) }}
            >
              {label}
            </HeaderMenuItem>
          ))}
        </HeaderNavigation>

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
                <Switcher aria-label="Projects">
                  {projects.map((p) => (
                    <SwitcherItem
                      key={p.id}
                      aria-labelledby={p.id}
                      onClick={() => { setProject(p.id); setShowProjectPanel(false) }}
                    >
                      <span>{p.name}</span>
                      <div className="project-actions">
                        <Button size="sm" kind="ghost" renderIcon={Edit} iconDescription="Edit" onClick={(e: React.MouseEvent) => { e.stopPropagation(); setEditProject(p); setShowProjectPanel(false) }} />
                        <Button size="sm" kind="danger--ghost" renderIcon={TrashCan} iconDescription="Delete" onClick={(e: React.MouseEvent) => { e.stopPropagation(); void deleteProject(p.id) }} />
                      </div>
                    </SwitcherItem>
                  ))}
                  <SwitcherItem onClick={() => { setEditProject(null); setShowProjectPanel(false); setNewProjectName(''); setNewProjectDesc('') }} aria-labelledby="new-project">
                    <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
                      <Add size={16} /> New project
                    </div>
                  </SwitcherItem>
                </Switcher>
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
      </Modal>

      <Content id="main-content">
        {children({ project, setProject })}
      </Content>
    </Theme>
  )
}