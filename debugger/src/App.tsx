import { useEffect, useState } from 'react'
import Observability from './Observability'
import AgentRuns from './AgentRuns'
import ExperienceTimeline from './ExperienceTimeline'

const Routes = {
  experience: /^\/experience(\/|$)/,
  agents: /^\/agents(\/|$)/,
  observability: /^\/observability(\/|$)/,
} as const

type Page = 'experience' | 'agents' | 'observability'

function pageFor(pathname: string): Page {
  if (Routes.agents.test(pathname)) return 'agents'
  if (Routes.observability.test(pathname)) return 'observability'
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
    case 'observability':
      return <Observability />
    default:
      return <ExperienceTimeline />
  }
}
