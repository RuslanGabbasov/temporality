import { renderToStaticMarkup } from 'react-dom/server'
import { afterEach, describe, expect, it, vi } from 'vitest'
import Artifacts from './Artifacts'
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

describe('Artifacts list header', () => {
  it('sticks the list header under the app bar while the reader pane scrolls', () => {
    stubLocalStorage()
    const html = renderToStaticMarkup(<I18nProvider><Artifacts /></I18nProvider>)
    // The list header (search + status chips + source/unit selects) must carry
    // the sticky styles: without them it scrolls away with the reader pane —
    // the regression the two-column redesign (d9193a5) introduced.
    expect(html).toMatch(/position:\s*sticky/)
    expect(html).toMatch(/top:\s*3rem/)
    expect(html).toMatch(/z-index:\s*1/)
  })
})
