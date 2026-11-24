import { useEffect, useState } from 'react'
import Layout, { type AppState } from './Layout'
import { I18nProvider } from './i18n'
import { ThemePrefProvider } from './theme'
import Dashboard from './Dashboard'
import Supervisor from './Supervisor'
import KnowledgeExplorer from './KnowledgeExplorer'
import AgentRuns from './AgentRuns'
import ExperienceTimeline from './ExperienceTimeline'
import KernelDownOverlay from './KernelDownOverlay'
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
import Sandbox from './Sandbox'
import Storage from './Storage'
import Org from './Org'
import OrgActivityBoard from './OrgActivityBoard'
import OrgWorkBoard from './OrgWorkBoard'
import OrgAwarenessFlow from './OrgAwarenessFlow'
import ChannelSettingsPage from './ChannelSettings'
import InboundMessages from './InboundMessages'
import SupervisorModel from './SupervisorModel'
import Harness from './Harness'
import LearningCenter from './learning/LearningCenter'
import LessonRunner from './learning/LessonRunner'
import LessonNudge from './learning/LessonNudge'
import { whoami } from './kernelApi'

const Routes = {
  brief: /^\/brief(\/|$)/,
  dashboard: /^\/dashboard(\/|$)/,
  supervisor: /^\/supervisor(\/|$)/,
  experience: /^\/experience(\/|$)/,
  agents: /^\/agents(\/|$)/,
  teams: /^\/teams(\/|$)/,
  knowledge: /^\/knowledge(\/|$)/,
  // The map lives as a tab of the knowledge explorer now; the standalone
  // route from its brief standalone era redirects into that tab.
  'knowledge-map': /^\/knowledge-map(\/|$)/,
  workspace: /^\/workspace(\/|$)/,
  'agent-config': /^\/agent-config(\/|$)/,
  skills: /^\/skills(\/|$)/,
  artifacts: /^\/artifacts(\/|$)/,
  mcp: /^\/mcp(\/|$)/,
  secrets: /^\/secrets(\/|$)/,
  sandbox: /^\/sandbox(\/|$)/,
  storage: /^\/storage(\/|$)/,
  providers: /^\/providers(\/|$)/,
  users: /^\/users(\/$|)/,
  triggers: /^\/triggers(\/$|)/,
  org: /^\/org(\/|$)/,
  // Org operations pages live UNDER /org — they must match before the bare
  // /org route above, or pageFor would fold them into the org tree page.
  'org-activity': /^\/org\/activity(\/|$)/,
  'org-work': /^\/org\/work(\/|$)/,
  'org-flow': /^\/org\/flow(\/|$)/,
  channels: /^\/channels(\/|$)/,
  inbound: /^\/inbound(\/|$)/,
  'supervisor-model': /^\/supervisor-model(\/|$)/,
  harness: /^\/harness(\/|$)/,
  learning: /^\/learning(\/|$)/,
} as const

type Page = 'dashboard' | 'supervisor' | 'experience' | 'agents' | 'teams' | 'knowledge' | 'workspace' | 'agent-config' | 'skills' | 'artifacts' | 'mcp' | 'secrets' | 'sandbox' | 'storage' | 'providers' | 'users' | 'triggers' | 'org' | 'org/activity' | 'org/work' | 'org/flow' | 'channels' | 'inbound' | 'supervisor-model' | 'harness' | 'learning'

function pageFor(pathname: string): Page {
  // /brief folded into the dashboard (navigate() rewrites the URL before
  // pageFor runs) — no standalone brief page anymore.
  if (Routes.dashboard.test(pathname)) return 'dashboard'
  if (Routes.supervisor.test(pathname)) return 'supervisor'
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
  if (Routes.sandbox.test(pathname)) return 'sandbox'
  if (Routes.storage.test(pathname)) return 'storage'
  if (Routes.providers.test(pathname)) return 'providers'
  if (Routes.users.test(pathname)) return 'users'
  if (Routes.triggers.test(pathname)) return 'triggers'
  if (Routes['org-activity'].test(pathname)) return 'org/activity'
  if (Routes['org-work'].test(pathname)) return 'org/work'
  if (Routes['org-flow'].test(pathname)) return 'org/flow'
  if (Routes.org.test(pathname)) return 'org'
  if (Routes.channels.test(pathname)) return 'channels'
  if (Routes.inbound.test(pathname)) return 'inbound'
  if (Routes['supervisor-model'].test(pathname)) return 'supervisor-model'
  if (Routes.harness.test(pathname)) return 'harness'
  if (Routes.learning.test(pathname)) return 'learning'
  return 'workspace'
}

export default function App() {
  const [page, setPage] = useState<Page>(() => pageFor(window.location.pathname))

  useEffect(() => {
    const navigate = () => {
      // The standalone map route folds into the explorer's map tab.
      if (window.location.pathname === '/knowledge-map') {
        window.history.replaceState(null, '', '/knowledge?view=map' + window.location.search.replace(/^\?/, '&'))
      }
      // The standalone brief page folds into the dashboard's «Мой день» tab
      // (docs/system-dashboard.md): the personal brief IS a dashboard tab
      // now — old deep links (/brief) must stay shareable.
      if (/^\/brief(\/|$)/.test(window.location.pathname)) {
        window.history.replaceState(null, '', '/dashboard?tab=myday' + window.location.search.replace(/^\?/, '&'))
      }
      setPage(pageFor(window.location.pathname))
    }
    navigate()
    window.addEventListener('popstate', navigate)
    return () => window.removeEventListener('popstate', navigate)
  }, [])

  // Landing gate (docs/system-dashboard.md «Мой день»): users without the
  // full operational dashboard land on their personal brief — the dashboard's
  // myday tab. Only the bare root redirects — an explicit deep link stays
  // where it points.
  useEffect(() => {
    let alive = true
    void whoami().then((w) => {
      if (alive && w.full_dashboard === false && (window.location.pathname === '/' || window.location.pathname === '')) {
        window.history.replaceState(null, '', '/dashboard?tab=myday')
        setPage('dashboard')
      }
    }).catch(() => {})
    return () => { alive = false }
  }, [])

  return (
    <ThemePrefProvider>
    <I18nProvider>
    <KernelDownOverlay />
    <Layout activePage={page}>
      {({ project, setProject, projectDefaultAgent, projectDefaultModel, refreshProjects }: AppState) => {
        switch (page) {
          case 'dashboard':
            return <Dashboard project={project} setProject={setProject} />
          case 'supervisor':
            return <Supervisor setProject={setProject} />
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
            return <Artifacts project={project} />
          case 'mcp':
            return <Mcp />
          case 'secrets':
            return <Secrets />
          case 'sandbox':
            return <Sandbox project={project} />
          case 'storage':
            return <Storage project={project} />
          case 'providers':
            return <Providers />
          case 'users':
            // @ts-ignore — Users returns JSX but TS infers void due to bare returns in callbacks
            return <Users />
          case 'triggers':
            return <Triggers project={project} />
          case 'org':
            return <Org />
          case 'org/activity':
            return <OrgActivityBoard />
          case 'org/work':
            return <OrgWorkBoard />
          case 'org/flow':
            return <OrgAwarenessFlow />
          case 'channels':
            return <ChannelSettingsPage />
          case 'inbound':
            return <InboundMessages />
          case 'supervisor-model':
            return <SupervisorModel />
          case 'harness':
            return <Harness />
          case 'learning':
            return <LearningCenter />
          default:
            return <ExperienceTimeline project={project} />
        }
      }}
    </Layout>
    {/* The lesson runner mounts ABOVE pages (§8.3): a lesson leads the user
        across routes, so it must survive SPA navigation. */}
    <LessonRunner />
    {/* The mentor's contextual nudge: on the page a lesson teaches, offer it
        once — the same mentor-shoulder-tap, one card at a time. */}
    <LessonNudge page={page} />
    </I18nProvider>
    </ThemePrefProvider>
  )
}