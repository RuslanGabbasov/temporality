import { renderToStaticMarkup } from 'react-dom/server'
import { afterEach, describe, expect, it, vi } from 'vitest'
import Operations from './Operations'

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
    const html = renderToStaticMarkup(<Operations project="test-project" />)
    expect(html).toContain('UNRESOLVED OPERATIONS')
    expect(html).toContain('nothing to reconcile')
    expect(html).toContain('/agents')
    expect(html).toContain('/observability')
    // The verdict form only appears with a selected operation.
    expect(html).not.toContain('Record verdict')
  })
})
