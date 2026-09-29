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
  Heading,
} from '@carbon/react'
import { User, Settings, Add, Edit, TrashCan, Folder } from '@carbon/icons-react'
import { Menu } from '@carbon/icons-react'
import { TOKEN_STORAGE_KEY, authToken, authHeaders } from './api'
import Onboarding from './Onboarding'
import { useI18n, type Locale } from './i18n'
import { whoami, type Whoami } from './kernelApi'

const KERNEL_API = '/kernel-api'

export interface AppState {
  project: string
  setProject: (p: string) => void
  projectDefaultAgent?: string
  projectDefaultModel?: string
}

interface LayoutProps {
  children: (state: AppState) => ReactNode
  activePage: string
}

const NAV_ITEMS = [
  { path: '/workspace', label: 'Workspace' },
  { path: '/agents', label: 'nav.runs' },
  { path: '/operations', label: 'nav.operations' },
  { path: '/observability', label: 'nav.knowledge' },
  { path: '/experience', label: 'nav.timeline' },
  { path: '/agent-config', label: 'nav.agents' },
  { path: '/triggers', label: 'nav.triggers' },
  { path: '/providers', label: 'nav.providers' },
  { path: '/users', label: 'nav.users' },
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
  const [project, setProject] = useState(params.get('project') ?? '')
  const [projects, setProjects] = useState<Project[]>([])
  const [allAgents, setAllAgents] = useState<{ id: string; name: string }[]>([])
  const [showProjectPanel, setShowProjectPanel] = useState(false)
  const [showNewProject, setShowNewProject] = useState(false)
  const [showLogin, setShowLogin] = useState(false)
  const [mobileNavOpen, setMobileNavOpen] = useState(false)
  const [confirmDelete, setConfirmDelete] = useState<string | null>(null)
  const [identity, setIdentity] = useState<Whoami | null>(null)
  const [showUserMenu, setShowUserMenu] = useState(false)
  const { locale, setLocale, t } = useI18n()
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
        const list: Project[] = data.projects ?? []
        setProjects(list)
        setProject((current) => {
          if (!current || !list.some((p) => p.id === current)) return list[0]?.id ?? ''
          return current
        })
      }
    } catch { /* ignore */ }
  }, [])

  useEffect(() => { void loadProjects() }, [loadProjects])
  useEffect(() => { void whoami().then(setIdentity).catch(() => {}) }, [])

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
    await fetch(`/kernel-api/v1/workspace/projects/${id}`, { method: 'DELETE', headers: { ...authHeaders() } })
    if (project === id) setProject(projects.find((p) => p.id !== id)?.id ?? '')
    void loadProjects()
  }

  return (
    <Theme theme="g100">
      <Header aria-label="Temporality">
        <HeaderName href="/workspace" prefix="" onClick={(e: React.MouseEvent) => { e.preventDefault(); navigate('/workspace') }}>
          <img src="/temporality.svg" alt="" style={{ height: '20px', width: 'auto', marginRight: '0.5rem', verticalAlign: 'middle' }} />
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
              {t(label)}
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
          {/* User menu */}
          <div style={{ position: 'relative' }}>
            <button
              onClick={() => setShowUserMenu(!showUserMenu)}
              style={{
                display: 'flex', alignItems: 'center', gap: '0.4rem',
                background: showUserMenu ? 'rgba(255,255,255,0.08)' : 'transparent',
                border: '1px solid var(--tm-border)', borderRadius: 'var(--tm-radius-sm)',
                padding: '0.3rem 0.6rem', cursor: 'pointer',
                color: 'var(--tm-text)', fontSize: 'var(--tm-text-sm)',
                fontFamily: 'var(--tm-font)',
              }}
            >
              <User size={14} />
              <span>{identity?.subject ?? 'User'}</span>
              <span style={{ fontSize: '0.6rem', color: 'var(--tm-muted)' }}>{showUserMenu ? '▴' : '▾'}</span>
            </button>
            {showUserMenu && (
              <HeaderPanel expanded>
                <div style={{ padding: '0.75rem', minWidth: '180px' }}>
                  {identity && (
                    <div style={{ marginBottom: '0.5rem', paddingBottom: '0.5rem', borderBottom: '1px solid var(--tm-border)' }}>
                      <div style={{ fontSize: '0.8rem', fontWeight: 500 }}>{identity.subject}</div>
                      <div style={{ fontSize: '0.7rem', color: 'var(--tm-muted)', marginTop: '0.15rem' }}>{identity.role}</div>
                    </div>
                  )}
                  <div
                    onClick={() => { setLocale(locale === 'en' ? 'ru' : 'en'); setShowUserMenu(false) }}
                    style={{ padding: '0.4rem 0.5rem', cursor: 'pointer', fontSize: '0.8rem', borderRadius: '4px', color: 'var(--tm-text-2)' }}
                    onMouseEnter={(e) => (e.currentTarget.style.background = 'rgba(255,255,255,0.05)')}
                    onMouseLeave={(e) => (e.currentTarget.style.background = 'transparent')}
                  >
                    {t('settings.language')}: {locale === 'en' ? 'English' : 'Русский'}
                  </div>
                  <div
                    onClick={() => { doLogout(); setShowUserMenu(false) }}
                    style={{ padding: '0.4rem 0.5rem', cursor: 'pointer', fontSize: '0.8rem', borderRadius: '4px', color: 'var(--tm-danger)', marginTop: '0.25rem' }}
                    onMouseEnter={(e) => (e.currentTarget.style.background = 'rgba(255,255,255,0.05)')}
                    onMouseLeave={(e) => (e.currentTarget.style.background = 'transparent')}
                  >
                    {loggedIn ? 'Logout' : 'Login'}
                  </div>
                </div>
              </HeaderPanel>
            )}
          </div>
          <div style={{ position: 'relative', marginRight: '0.5rem' }}>
            <button
              onClick={() => setShowProjectPanel(!showProjectPanel)}
              style={{
                display: 'flex', alignItems: 'center', gap: '0.4rem',
                background: showProjectPanel ? 'rgba(255,255,255,0.08)' : 'transparent',
                border: '1px solid var(--tm-border)', borderRadius: 'var(--tm-radius-sm)',
                padding: '0.3rem 0.6rem', cursor: 'pointer',
                color: 'var(--tm-text)', fontSize: 'var(--tm-text-sm)',
                fontFamily: 'var(--tm-font)',
              }}
            >
              <Folder size={14} style={{ color: 'var(--tm-teal)' }} />
              <span>{project}</span>
              <span style={{ fontSize: '0.6rem', color: 'var(--tm-muted)' }}>{showProjectPanel ? '▴' : '▾'}</span>
            </button>
            {showProjectPanel && (
              <HeaderPanel expanded>
                <div style={{ padding: '0.5rem' }}>
                  <div style={{ fontSize: '0.75rem', color: '#7e8a9c', padding: '0.5rem 0.5rem 0.25rem', fontWeight: 600, letterSpacing: '0.05em', textTransform: 'uppercase' }}>{t('projects.title')}</div>
                  <div style={{ display: 'grid', gap: '1px' }}>
                    {projects.map((p) => {
                      const defaultAgent = allAgents.find((a) => a.id === p.default_agent_id)
                      return (
                      <div
                        key={p.id}
                        onClick={() => { setProject(p.id); setShowProjectPanel(false) }}
                        style={{ display: 'grid', gridTemplateColumns: '1fr auto', alignItems: 'center', padding: '0.5rem 0.5rem', cursor: 'pointer', background: p.id === project ? '#121823' : 'transparent', borderRadius: '4px', minWidth: 0 }}
                      >
                        <div style={{ minWidth: 0 }}>
                          <div style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{p.name}</div>
                          {(defaultAgent || p.default_model) && (
                            <div style={{ fontSize: '0.65rem', color: 'var(--tm-muted)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', marginTop: '1px' }}>
                              {defaultAgent ? defaultAgent.name : 'no agent'}{p.default_model ? ` · ${p.default_model}` : ''}
                            </div>
                          )}
                        </div>
                        <div style={{ display: 'flex', gap: '0.25rem', flexShrink: 0 }}>
                          <Button size="sm" kind="ghost" hasIconOnly renderIcon={Edit} iconDescription="Edit" onClick={(e: React.MouseEvent) => { e.stopPropagation(); setEditProject(p); setShowProjectPanel(false) }} />
                          <Button size="sm" kind="danger--ghost" hasIconOnly renderIcon={TrashCan} iconDescription="Delete" onClick={(e: React.MouseEvent) => { e.stopPropagation(); setConfirmDelete(p.id) }} />
                        </div>
                      </div>
                      )
                    })}
                    <div
                      onClick={() => { setNewProjectName(''); setNewProjectDesc(''); setShowProjectPanel(false); setShowNewProject(true) }}
                      style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', padding: '0.5rem 0.5rem', cursor: 'pointer', color: '#57d7e8', borderRadius: '4px' }}
                    >
                      <Add size={20} /> {t('new.project')}
                    </div>
                  </div>
                </div>
              </HeaderPanel>
            )}
          </div>
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
                {t(label)}
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

      {/* Project edit/create modal */}
      <Modal
        open={!!editProject || showNewProject}
        onRequestClose={() => { setEditProject(null); setShowNewProject(false); setNewProjectName(''); setNewProjectDesc('') }}
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

      {confirmDelete && (
        <div className="modal-overlay">
          <div className="modal-panel" style={{ width: '460px' }}>
            <Heading>{t('projects.delete_confirm')}</Heading>
            <p style={{ color: 'var(--tm-text-3)', margin: '0.75rem 0' }}>
              {t('projects.delete_warning', { name: projects.find((p) => p.id === confirmDelete)?.name ?? confirmDelete })}
            </p>
            <div className="form-actions">
              <Button kind="secondary" onClick={() => setConfirmDelete(null)}>{t('action.cancel')}</Button>
              <Button kind="danger" onClick={() => { const id = confirmDelete; setConfirmDelete(null); void deleteProject(id) }}>{t('action.delete')}</Button>
            </div>
          </div>
        </div>
      )}

      {projects.length === 0 && <Onboarding onComplete={() => void loadProjects()} />}
      <Content id="main-content">
        {children({ project, setProject, projectDefaultAgent: projects.find((p) => p.id === project)?.default_agent_id, projectDefaultModel: projects.find((p) => p.id === project)?.default_model })}
      </Content>
    </Theme>
  )
}