import { renderToStaticMarkup } from 'react-dom/server'
import { afterEach, describe, expect, it, vi } from 'vitest'
import Operations from './Operations'
import { I18nProvider } from './i18n'

function stubLocalStorage() {
  vi.stubGlobal('localStorage', {
    getItem: () => null,
    setItem: () => {},
    removeItem: () => {},
  })
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('Operations', () => {
  it('renders the reconciliation console shell and empty state', () => {
    stubLocalStorage()
    const html = renderToStaticMarkup(
      <I18nProvider>
        <Operations project="test-project" />
      </I18nProvider>,
    )
    expect(html).toContain('Operations')
    expect(html).toContain('No unresolved operations')
    // The verdict form only appears with a selected operation.
    expect(html).not.toContain('Record verdict')
  })
})
