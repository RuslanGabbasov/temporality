import { useEffect, useState } from 'react'
import Layout, { type AppState } from './Layout'
import Observability from './Observability'
import AgentRuns from './AgentRuns'
import Operations from './Operations'
import ExperienceTimeline from './ExperienceTimeline'
import Workspace from './Workspace'

const Routes = {
  experience: /^\/experience(\/|$)/,
  agents: /^\/agents(\/|$)/,
  operations: /^\/operations(\/|$)/,
  observability: /^\/observability(\/|$)/,
  workspace: /^\/workspace(\/|$)/,
} as const

type Page = 'experience' | 'agents' | 'operations' | 'observability' | 'workspace'

function pageFor(pathname: string): Page {
  if (Routes.agents.test(pathname)) return 'agents'
  if (Routes.operations.test(pathname)) return 'operations'
  if (Routes.observability.test(pathname)) return 'observability'
  if (Routes.workspace.test(pathname)) return 'workspace'
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
    <Layout activePage={page}>
      {({ project, setProject }: AppState) => {
        switch (page) {
          case 'agents':
            return <AgentRuns project={project} />
          case 'operations':
            return <Operations project={project} />
          case 'observability':
            return <Observability project={project} />
          case 'workspace':
            // @ts-ignore — Workspace returns JSX but TS infers void due to bare returns in callbacks
            return <Workspace project={project} setProject={setProject} />
          default:
            return <ExperienceTimeline project={project} />
        }
      }}
    </Layout>
  )
}