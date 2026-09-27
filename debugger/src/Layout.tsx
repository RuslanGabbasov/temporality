import { useEffect, useState, useCallback, type ReactNode } from 'react'
import {
  Header,
  HeaderName,
  HeaderNavigation,
  HeaderMenuItem,
  HeaderGlobalBar,
  Content,
  Theme,
  Select,
  SelectItem,
} from '@carbon/react'
import Token from './Token'

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
  { path: '/agents', label: 'Runs' },
  { path: '/operations', label: 'Operations' },
  { path: '/observability', label: 'Knowledge' },
  { path: '/experience', label: 'Timeline' },
]

// Well-known projects from URL or defaults
const KNOWN_PROJECTS = ['lighthouse', 'forge', 'recon-smoke', 'quota-smoke2']

export default function Layout({ children, activePage }: LayoutProps) {
  const params = new URLSearchParams(window.location.search)
  const [project, setProject] = useState(params.get('project') ?? 'lighthouse')

  const navigate = (path: string) => {
    window.history.pushState(null, '', path)
    window.dispatchEvent(new PopStateEvent('popstate'))
  }

  // Sync project to URL
  useEffect(() => {
    const p = new URLSearchParams(window.location.search)
    p.set('project', project)
    window.history.replaceState(null, '', `${window.location.pathname}?${p}`)
  }, [project])

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
              onClick={(e: React.MouseEvent) => {
                e.preventDefault()
                navigate(path)
              }}
            >
              {label}
            </HeaderMenuItem>
          ))}
        </HeaderNavigation>

        <HeaderGlobalBar>
          <div style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', marginRight: '1rem' }}>
            <Select
              id="global-project"
              labelText=""
              hideLabel
              value={project}
              onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setProject(e.target.value)}
              size="sm"
            >
              {KNOWN_PROJECTS.map((p) => <SelectItem key={p} value={p} text={p} />)}
              {!KNOWN_PROJECTS.includes(project) && <SelectItem value={project} text={project} />}
            </Select>
            <Token />
          </div>
        </HeaderGlobalBar>
      </Header>

      <Content id="main-content">
        {children({ project, setProject })}
      </Content>
    </Theme>
  )
}