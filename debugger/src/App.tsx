import { useEffect, useState } from 'react'
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
    const navigate = () => {
      const next = pageFor(window.location.pathname)
      if (!Routes[next].test(window.location.pathname)) {
        // The product home is the Experience Timeline; legacy deep links to
        // legacy root paths land there too.
        window.history.replaceState(null, '', `/experience${window.location.search}`)
      }
      setPage(next)
    }
    navigate()
    window.addEventListener('popstate', navigate)
    return () => window.removeEventListener('popstate', navigate)
  }, [])

  switch (page) {
    case 'agents':
      return <AgentRuns />
    case 'operations':
      return <Operations />
    case 'observability':
      return <Observability />
    case 'workspace':
      return <Workspace />
    default:
      return <ExperienceTimeline />
  }
}
