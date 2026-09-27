// Carbon-based layout shell for Temporality
// Provides consistent navigation, header, and content area

import { useState, type ReactNode } from 'react'
import {
  Header,
  HeaderName,
  HeaderNavigation,
  HeaderMenuItem,
  HeaderGlobalBar,
  HeaderGlobalAction,
  SideNav,
  SideNavItems,
  SideNavMenuItem,
  SideNavMenu,
  Content,
  Theme,
} from '@carbon/react'
import {
  Dashboard,
  Activity,
  Settings,
  Document,
  Time,
  User,
} from '@carbon/icons-react'

interface LayoutProps {
  children: ReactNode
  activePage: string
}

const NAV_ITEMS = [
  { path: '/experience', label: 'Experience Timeline', icon: Time },
  { path: '/agents', label: 'Agent Runs', icon: Activity },
  { path: '/operations', label: 'Operations', icon: Settings },
  { path: '/observability', label: 'Knowledge', icon: Document },
  { path: '/workspace', label: 'Workspace', icon: Dashboard },
]

export default function Layout({ children, activePage }: LayoutProps) {
  const [isSideNavExpanded, setIsSideNavExpanded] = useState(true)

  const navigate = (path: string) => {
    window.history.pushState(null, '', path)
    window.dispatchEvent(new PopStateEvent('popstate'))
  }

  return (
    <Theme theme="g100">
      <Header aria-label="Temporality">
        <HeaderName href="/experience" prefix="">
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
          <HeaderGlobalAction
            aria-label="Toggle side navigation"
            onClick={() => setIsSideNavExpanded(!isSideNavExpanded)}
            isActive={isSideNavExpanded}
            tooltipAlignment="end"
          >
            <Settings size={20} />
          </HeaderGlobalAction>
        </HeaderGlobalBar>
      </Header>

      <SideNav
        aria-label="Side navigation"
        expanded={isSideNavExpanded}
        onOverlayClick={() => setIsSideNavExpanded(false)}
        href="#main-content"
      >
        <SideNavItems>
          <SideNavMenu title="Navigation" defaultExpanded>
            {NAV_ITEMS.map(({ path, label, icon: Icon }) => (
              <SideNavMenuItem
                key={path}
                href={path}
                isActive={activePage === path.slice(1)}
                onClick={(e: React.MouseEvent) => {
                  e.preventDefault()
                  navigate(path)
                }}
              >
                <Icon size={16} style={{ marginRight: '0.5rem' }} />
                {label}
              </SideNavMenuItem>
            ))}
          </SideNavMenu>
          
          <SideNavMenu title="Settings">
            <SideNavMenuItem href="#token">
              <User size={16} style={{ marginRight: '0.5rem' }} />
              Authentication
            </SideNavMenuItem>
          </SideNavMenu>
        </SideNavItems>
      </SideNav>

      <Content id="main-content">
        {children}
      </Content>
    </Theme>
  )
}