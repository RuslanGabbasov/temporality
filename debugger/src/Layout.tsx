import { type ReactNode } from 'react'
import {
  Header,
  HeaderName,
  HeaderNavigation,
  HeaderMenuItem,
  Content,
  Theme,
} from '@carbon/react'

interface LayoutProps {
  children: ReactNode
  activePage: string
}

const NAV_ITEMS = [
  { path: '/experience', label: 'Timeline' },
  { path: '/agents', label: 'Runs' },
  { path: '/operations', label: 'Operations' },
  { path: '/observability', label: 'Knowledge' },
  { path: '/workspace', label: 'Workspace' },
]

export default function Layout({ children, activePage }: LayoutProps) {
  const navigate = (path: string) => {
    window.history.pushState(null, '', path)
    window.dispatchEvent(new PopStateEvent('popstate'))
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
              onClick={(e: React.MouseEvent) => {
                e.preventDefault()
                navigate(path)
              }}
            >
              {label}
            </HeaderMenuItem>
          ))}
        </HeaderNavigation>
      </Header>
      <Content id="main-content">
        {children}
      </Content>
    </Theme>
  )
}