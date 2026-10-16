import { useEffect, useState } from 'react'
import Layout, { type AppState } from './Layout'
import { I18nProvider } from './i18n'
import { ThemePrefProvider } from './theme'
import Dashboard from './Dashboard'
import KnowledgeExplorer from './KnowledgeExplorer'
import AgentRuns from './AgentRuns'
import ExperienceTimeline from './ExperienceTimeline'
import Workspace from './Workspace'
import Agents from './Agents'
import Teams from './Teams'
import Providers from './Providers'
import Users from './Users'
import Triggers from './Triggers'
import Skills from './Skills'
import Artifacts from './Artifacts'
import Mcp from './Mcp'
import Secrets from './Secrets'
import Org from './Org'
import ChannelSettingsPage from './ChannelSettings'
import InboundMessages from './InboundMessages'

const Routes = {
  project: /^\/project(\/|$)/,
  dashboard: /^\/dashboard(\/|$)/,
  experience: /^\/experience(\/|$)/,
  agents: /^\/agents(\/|$)/,
  teams: /^\/teams(\/|$)/,
  observability: /^\/observability(\/|$)/,
  knowledge: /^\/knowledge(\/|$)/,
  workspace: /^\/workspace(\/|$)/,
  'agent-config': /^\/agent-config(\/|$)/,
  skills: /^\/skills(\/|$)/,
  artifacts: /^\/artifacts(\/|$)/,
  mcp: /^\/mcp(\/|$)/,
  secrets: /^\/secrets(\/|$)/,
  providers: /^\/providers(\/|$)/,
  users: /^\/users(\/$|)/,
  triggers: /^\/triggers(\/$|)/,
  org: /^\/org(\/|$)/,
  channels: /^\/channels(\/|$)/,
  inbound: /^\/inbound(\/|$)/,
} as const

type Page = 'project' | 'dashboard' | 'experience' | 'agents' | 'teams' | 'observability' | 'knowledge' | 'workspace' | 'agent-config' | 'skills' | 'artifacts' | 'mcp' | 'secrets' | 'providers' | 'users' | 'triggers' | 'org' | 'channels' | 'inbound'

function pageFor(pathname: string): Page {
  // /observability is now a tab of the system dashboard (docs/system-dashboard.md
  // §2): the legacy path redirects, carrying the knowledge params over.
  if (Routes.observability.test(pathname)) return 'observability'
  if (Routes.project.test(pathname)) return 'project'
  if (Routes.dashboard.test(pathname)) return 'dashboard'
  if (Routes.agents.test(pathname)) return 'agents'
  if (Routes.teams.test(pathname)) return 'teams'
  if (Routes.knowledge.test(pathname)) return 'knowledge'
  if (Routes.workspace.test(pathname)) return 'workspace'
  if (Routes.experience.test(pathname)) return 'experience'
  if (Routes['agent-config'].test(pathname)) return 'agent-config'
  if (Routes.skills.test(pathname)) return 'skills'
  if (Routes.artifacts.test(pathname)) return 'artifacts'
  if (Routes.mcp.test(pathname)) return 'mcp'
  if (Routes.secrets.test(pathname)) return 'secrets'
  if (Routes.providers.test(pathname)) return 'providers'
  if (Routes.users.test(pathname)) return 'users'
  if (Routes.triggers.test(pathname)) return 'triggers'
  if (Routes.org.test(pathname)) return 'org'
  if (Routes.channels.test(pathname)) return 'channels'
  if (Routes.inbound.test(pathname)) return 'inbound'
  return 'workspace'
}

export default function App() {
  const [page, setPage] = useState<Page>(() => pageFor(window.location.pathname))

  useEffect(() => {
    const navigate = () => setPage(pageFor(window.location.pathname))
    navigate()
    window.addEventListener('popstate', navigate)
    return () => window.removeEventListener('popstate', navigate)
  }, [])

  // Legacy /observability and /project redirect to /dashboard (the Observability
  // page now lives inside the dashboard; its own `tab` param becomes `ktab`).
  useEffect(() => {
    if (page !== 'observability' && page !== 'project') return
    const params = new URLSearchParams(window.location.search)
    if (page === 'observability') {
      const innerTab = params.get('tab')
      params.delete('tab')
      if (innerTab) params.set('ktab', innerTab)
      params.set('tab', 'knowledge')
    }
    const qs = params.toString()
    window.history.replaceState(null, '', qs ? `/dashboard?${qs}` : '/dashboard')
    setPage('dashboard')
  }, [page])

  return (
    <ThemePrefProvider>
    <I18nProvider>
    <Layout activePage={page}>
      {({ project, setProject, projectDefaultAgent, projectDefaultModel, refreshProjects }: AppState) => {
        switch (page) {
          case 'dashboard':
            return <Dashboard project={project} setProject={setProject} />
          case 'agents':
            return <AgentRuns project={project} />
          case 'teams':
            return <Teams project={project} />
          case 'knowledge':
            return <KnowledgeExplorer project={project} />
          case 'workspace':
            // @ts-ignore
            return <Workspace project={project} setProject={setProject} defaultAgentId={projectDefaultAgent} defaultModel={projectDefaultModel} />
          case 'agent-config':
            return <Agents project={project} defaultAgentId={projectDefaultAgent} refreshProjects={refreshProjects} />
          case 'skills':
            return <Skills />
          case 'artifacts':
            return <Artifacts />
          case 'mcp':
            return <Mcp />
          case 'secrets':
            return <Secrets />
          case 'providers':
            return <Providers />
          case 'users':
            // @ts-ignore — Users returns JSX but TS infers void due to bare returns in callbacks
            return <Users />
          case 'triggers':
            return <Triggers project={project} />
          case 'org':
            return <Org />
          case 'channels':
            return <ChannelSettingsPage />
          case 'inbound':
            return <InboundMessages />
          default:
            return <ExperienceTimeline project={project} />
        }
      }}
    </Layout>
    </I18nProvider>
    </ThemePrefProvider>
  )
}