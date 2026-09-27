// Custom layout shell for Temporality
// Provides consistent navigation, header, and content area

import { useState, type ReactNode } from 'react'

interface LayoutProps {
  children: ReactNode
  activePage: string
}

const NAV_ITEMS = [
  { path: '/experience', label: 'Experience Timeline' },
  { path: '/agents', label: 'Agent Runs' },
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
    <div>
      <header className="header">
        <a href="/experience" className="header__name" onClick={(e) => { e.preventDefault(); navigate('/experience') }}>
          Temporality
        </a>
        
        <nav className="header__nav">
          {NAV_ITEMS.map(({ path, label }) => (
            <a
              key={path}
              href={path}
              className={`header__link ${activePage === path.slice(1) ? 'header__link--active' : ''}`}
              onClick={(e) => {
                e.preventDefault()
                navigate(path)
              }}
            >
              {label}
            </a>
          ))}
        </nav>
      </header>

      <div className="layout">
        <nav className="sidenav">
          <div className="sidenav__section">
            <div className="sidenav__title">Navigation</div>
            {NAV_ITEMS.map(({ path, label }) => (
              <a
                key={path}
                href={path}
                className={`sidenav__link ${activePage === path.slice(1) ? 'sidenav__link--active' : ''}`}
                onClick={(e) => {
                  e.preventDefault()
                  navigate(path)
                }}
              >
                {label}
              </a>
            ))}
          </div>
        </nav>

        <main className="layout__content">
          {children}
        </main>
      </div>
    </div>
  )
}