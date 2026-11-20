// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { UserDetailPanel } from './UserDetail'
import { I18nProvider } from './i18n'
import type { UserMetricsRow, UserStats } from './workspaceApi'

// Expanding a user must survive a kernel that serialises empty Go slices as
// JSON null (nil slice → null): every list in the dossier is read with
// .length, and `null.length` used to crash the whole tab with
// «can't access property "length", b.failed_runs is null».

vi.mock('./workspaceApi', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./workspaceApi')>()
  return {
    ...actual,
    workspaceApi: {
      ...actual.workspaceApi,
      userStats: vi.fn(async () => nullStats),
      userFacts: vi.fn(async () => ({ facts: [] })),
    },
  }
})

// The exact crash payload: arrays present as keys, values null.
const nullStats = {
  user_id: 'u1',
  window: '168h',
  runs: { total: 3, succeeded: 2, failed: 1, cancelled: 0, turn_limit: 0, other: 0, tokens: 1000, cost_usd: 0.5, model_calls: 4, projects: 1, agents: 1 },
  by_project: null,
  by_agent: null,
  sagas: null,
  knowledge: { proposed: 0, items: null },
  asks: { open: 0, answered: 2, timed_out: 1, wait_seconds: 30, hab_seconds: 60, recent: null },
  problems: { failed_runs: null, timed_out_asks: null, supervisor: null, supervisor_all: 0 },
} as unknown as UserStats

const row: UserMetricsRow = {
  id: 'u1',
  name: 'Ada',
  role: 'admin',
  active: true,
  runs_total: 3,
  runs_week: 2,
  failed_runs: 1,
  timed_out_asks: 1,
  asks_open: 0,
  asks_total: 2,
  wait_seconds: 30,
  hab_seconds: 60,
}

let container: HTMLDivElement
let root: Root

afterEach(() => {
  root?.unmount()
  container?.remove()
})

async function renderPanel() {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  await act(async () => {
    root.render(
      <I18nProvider>
        <UserDetailPanel row={row} />
      </I18nProvider>,
    )
  })
  // Land the userStats/userFacts promises and the setState they trigger.
  await act(async () => { await Promise.resolve() })
  await act(async () => { await Promise.resolve() })
}

describe('UserDetailPanel', () => {
  it('renders the dossier when every list in the payload is null', async () => {
    await renderPanel()
    // The identity footer echoes the person — proves the sections rendered
    // past the null lists instead of throwing mid-tree.
    expect(container.textContent).toContain('Ada')
    // Jump links (5) + period presets (4) + facts «Add» button.
    expect(container.querySelectorAll('button').length).toBeGreaterThanOrEqual(10)
  })

  it('keeps rendering when the payload carries real lists', async () => {
    const filled = {
      ...nullStats,
      by_project: [{ project_id: 'p1', runs: 3, failed: 1, tokens: 1000, cost_usd: 0.5 }],
      by_agent: [{ agent_id: 'a1', runs: 3, failed: 1, tokens: 1000, cost_usd: 0.5 }],
      sagas: [{ saga_id: 's1', title: 'Первая', project: 'p1', runs: 2, role: 'registered' }],
      knowledge: { proposed: 1, items: [{ knowledge_id: 'k1', proposition: 'fact', project: 'p1', at: '2026-11-01T00:00:00Z' }] },
      asks: { ...nullStats.asks, recent: [{ question: 'ok?', status: 'answered', at: '2026-11-01T00:00:00Z', run_id: 'r1' }] },
      problems: {
        failed_runs: [{ run_id: 'r2', title: 'boom', project: 'p1', status: 'failed', at: '2026-11-02T00:00:00Z' }],
        timed_out_asks: [{ question: 'lost?', status: 'expired', at: '2026-11-02T00:00:00Z' }],
        supervisor: [{ family: 'run_failure', subject: 'r2', status: 'open', last_seen: '2026-11-02T00:00:00Z' }],
        supervisor_all: 1,
      },
    } as unknown as UserStats
    vi.mocked((await import('./workspaceApi')).workspaceApi.userStats).mockResolvedValueOnce(filled)

    await renderPanel()
    expect(container.textContent).toContain('Ada')
    expect(container.textContent).toContain('p1')
    expect(container.textContent).toContain('Первая')
  })
})
