import { renderToStaticMarkup } from 'react-dom/server'
import { afterEach, describe, expect, it, vi } from 'vitest'
import Dashboard, { OVERVIEW_CACHE_KEY } from './Dashboard'
import { I18nProvider } from './i18n'
import { cacheSet } from './tabCache'

function stubBrowser() {
  const storage = {
    getItem: () => null,
    setItem: () => {},
    removeItem: () => {},
  }
  vi.stubGlobal('sessionStorage', storage)
  vi.stubGlobal('localStorage', storage)
  // The dashboard initializers read window.location (the ?tab= param) and
  // window.localStorage (the last-tab memory) — the test env is plain node.
  vi.stubGlobal('window', { location: { search: '', pathname: '/dashboard' }, localStorage: storage })
}

afterEach(() => {
  vi.unstubAllGlobals()
})

// renderToStaticMarkup runs the state initializers but not the effects —
// exactly the hydration path: the cache seeds the first render, the network
// plays no role. Seeding goes through cacheSet (the production write path):
// tabCache memoizes storage reads in its module memory, so a raw sessionStorage
// stub would be ignored after the first test in this file.

// One shipped build cached the raw operations summary ({count, operations})
// where the state holds a bare number: hydration fed the object into a
// HealthChip and the dashboard died with React error #31 before any fetch
// could save it. The hydration guard must contain poisoned entries.
describe('Dashboard overview cache hydration', () => {
  it('survives a poisoned cache entry (opsCount as an object)', () => {
    stubBrowser()
    cacheSet(OVERVIEW_CACHE_KEY, { window: 1, at: Date.now(), opsCount: { count: 5, operations: [] } })
    expect(() => renderDashboard()).not.toThrow()
    const html = renderDashboard()
    expect(html).not.toContain('"count"')
    expect(html).not.toContain('"operations"')
  })

  it('hydrates a valid ops count into the health strip', () => {
    stubBrowser()
    cacheSet(OVERVIEW_CACHE_KEY, { window: 1, at: Date.now(), opsCount: 7 })
    expect(renderDashboard()).toContain('>7<')
  })
})

function renderDashboard() {
  return renderToStaticMarkup(
    <I18nProvider>
      <Dashboard project="" setProject={() => {}} />
    </I18nProvider>,
  )
}
