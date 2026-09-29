import { useEffect, useState } from 'react'
import Layout, { type AppState } from './Layout'
import { I18nProvider } from './i18n'
import Observability from './Observability'
import AgentRuns from './AgentRuns'
import Operations from './Operations'
import ExperienceTimeline from './ExperienceTimeline'
import Workspace from './Workspace'
import Agents from './Agents'
import Providers from './Providers'
import Users from './Users'
import Triggers from './Triggers'

const Routes = {
  experience: /^\/experience(\/|$)/,
  agents: /^\/agents(\/|$)/,
  operations: /^\/operations(\/|$)/,
  observability: /^\/observability(\/|$)/,
  workspace: /^\/workspace(\/|$)/,
  'agent-config': /^\/agent-config(\/|$)/,
  providers: /^\/providers(\/|$)/,
  users: /^\/users(\/|$)/,
  triggers: /^\/triggers(\/|$)/,
} as const

type Page = 'experience' | 'agents' | 'operations' | 'observability' | 'workspace' | 'agent-config' | 'providers' | 'users' | 'triggers'

function pageFor(pathname: string): Page {
  if (Routes.agents.test(pathname)) return 'agents'
  if (Routes.operations.test(pathname)) return 'operations'
  if (Routes.observability.test(pathname)) return 'observability'
  if (Routes.workspace.test(pathname)) return 'workspace'
  if (Routes['agent-config'].test(pathname)) return 'agent-config'
  if (Routes.providers.test(pathname)) return 'providers'
  if (Routes.users.test(pathname)) return 'users'
  if (Routes.triggers.test(pathname)) return 'triggers'
  return 'experience'
}

export default function App() {
  const [page, setPage] = useState<Page>(() => pageFor(window.location.pathname))

  useEffect(() => {
    const navigate = () => setPage(pageFor(window.location.pathname))
    navigate()
    window.addEventListener('popstate', navigate)
    return () => window.removeEventListener('popstate', navigate)
  }, [])

  return (
    <I18nProvider>
    <Layout activePage={page}>
      {({ project, setProject, projectDefaultAgent, projectDefaultModel }: AppState) => {
        switch (page) {
          case 'agents':
            return <AgentRuns project={project} />
          case 'operations':
            return <Operations project={project} />
          case 'observability':
            return <Observability project={project} />
          case 'workspace':
            // @ts-ignore
            return <Workspace project={project} setProject={setProject} defaultAgentId={projectDefaultAgent} defaultModel={projectDefaultModel} />
          case 'agent-config':
            return <Agents defaultAgentId={projectDefaultAgent} />
          case 'providers':
            return <Providers />
          case 'users':
            // @ts-ignore — Users returns JSX but TS infers void due to bare returns in callbacks
            return <Users />
          case 'triggers':
            return <Triggers project={project} />
          default:
            return <ExperienceTimeline project={project} />
        }
      }}
    </Layout>
    </I18nProvider>
  )
}