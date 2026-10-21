import { renderToStaticMarkup } from 'react-dom/server'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { USERS_COLUMNS, UsersTableHead, UsersTableRow } from './Dashboard'
import { I18nProvider } from './i18n'
import type { UserMetricsRow } from './workspaceApi'

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

/** A plain active user: no Tag, no nested spans — every cell is one <span>. */
const row: UserMetricsRow = {
  id: 'u1',
  name: 'Ada',
  role: 'admin',
  active: true,
  runs_total: 3,
  runs_week: 2,
  failed_runs: 0,
  timed_out_asks: 0,
  asks_open: 0,
  asks_total: 1,
  wait_seconds: 0,
  hab_seconds: 0,
}

const gridTemplates = (html: string) =>
  [...html.matchAll(/grid-template-columns:([^;"]+)/g)].map((match) => match[1].trim())

const cellCount = (html: string) => (html.match(/<span/g) ?? []).length

function renderHead() {
  return renderToStaticMarkup(
    <I18nProvider>
      <UsersTableHead />
    </I18nProvider>,
  )
}

function renderRow() {
  return renderToStaticMarkup(
    <I18nProvider>
      <UsersTableRow row={row} expanded={false} onOpen={() => {}} />
    </I18nProvider>,
  )
}

// The Users tab once shipped a header on 8 grid tracks against 10 header
// cells and 10-cell rows (commit 8c81742): the ⚠ and «Last ask» labels wrapped
// onto a second line and the columns drifted away from the data. One shared
// template plus this lockstep check is the guard against it happening again.
describe('Users table grid', () => {
  it('renders the header and the rows on one shared column template', () => {
    stubLocalStorage()
    const head = renderHead()
    const body = renderRow()
    expect(gridTemplates(head)).toEqual([USERS_COLUMNS])
    expect(gridTemplates(body)).toEqual([USERS_COLUMNS])
    expect(gridTemplates(head)[0]).toBe(gridTemplates(body)[0])
  })

  it('keeps cells and tracks in lockstep in every block — no header cell overflows its grid', () => {
    stubLocalStorage()
    for (const html of [renderHead(), renderRow()]) {
      const template = gridTemplates(html)[0] ?? ''
      const tracks = template.match(/minmax\([^)]*\)/g) ?? []
      expect(tracks.length, template).toBeGreaterThan(0)
      expect(cellCount(html), template).toBe(tracks.length)
    }
  })
})
