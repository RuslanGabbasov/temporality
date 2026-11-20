import { renderToStaticMarkup } from 'react-dom/server'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { USERS_COLUMNS, UsersTableHead, UsersTableRow, filterUserRows, sortUserRows } from './Dashboard'
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

// The list controls (search needle, flag chips, column sort) are pure —
// they carry the whole filter behaviour of a directory that outgrew the
// one-page rollup.
describe('Users list controls', () => {
  const rows: UserMetricsRow[] = [
    row,
    { ...row, id: 'u2', name: 'Bob', active: false, failed_runs: 2, asks_open: 3, runs_total: 10, last_run_at: '2026-01-02T00:00:00Z' },
    { ...row, id: 'u3', name: 'Cy', runs_total: 5, last_run_at: '2026-01-01T00:00:00Z' },
  ]

  it('filters by needle and by flag', () => {
    expect(filterUserRows(rows, 'bo', 'all').map((r) => r.id)).toEqual(['u2'])
    expect(filterUserRows(rows, '', 'inactive').map((r) => r.id)).toEqual(['u2'])
    expect(filterUserRows(rows, '', 'problems').map((r) => r.id)).toEqual(['u2'])
    expect(filterUserRows(rows, '', 'asks').map((r) => r.id)).toEqual(['u2'])
    // The needle searches name, id, role and org unit — every fixture is role=admin.
    expect(filterUserRows(rows, 'admin', 'all')).toHaveLength(3)
    expect(filterUserRows(rows, 'nobody', 'all')).toEqual([])
  })

  it('sorts by column, breaks ties by name and sinks never-active rows last', () => {
    expect(sortUserRows(rows, { key: 'runs', dir: -1 }).map((r) => r.id)).toEqual(['u2', 'u3', 'u1'])
    // Recency desc: newest first, the row with no run at all last — and the
    // same sink holds when the direction flips.
    expect(sortUserRows(rows, { key: 'activity', dir: -1 }).map((r) => r.id)).toEqual(['u2', 'u3', 'u1'])
    expect(sortUserRows(rows, { key: 'activity', dir: 1 }).map((r) => r.id)).toEqual(['u3', 'u2', 'u1'])
    expect(sortUserRows(rows, { key: 'name', dir: 1 }).map((r) => r.id)).toEqual(['u1', 'u2', 'u3'])
    // The input array is never mutated — filtering/sorting stays referentially
    // stable for the memo in UsersTab.
    expect(rows.map((r) => r.id)).toEqual(['u1', 'u2', 'u3'])
  })
})
