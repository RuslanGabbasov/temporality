// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import Artifacts from './Artifacts'
import { I18nProvider } from './i18n'
import { workspaceApi } from './workspaceApi'

// Regression: an approved artifact with a revision IN REVIEW used to hide the
// whole review surface — the queue button keyed on the live row's status
// (approved), so the pending version could be neither read nor approved. The
// card must show the revision's body by default and the approve/reject
// buttons without a hunt.

vi.mock('./workspaceApi', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./workspaceApi')>()
  const artifact = {
    id: 'a1', title: 'Deployment runbook', status: 'approved', version: 1,
    kind: 'reference', provider: 'store', size_bytes: 10, mime_type: 'text/markdown',
    created_at: '2026-10-01T00:00:00Z', updated_at: '2026-10-01T00:00:00Z',
    tags: [], reviewers: [], review_mode: 'all',
  }
  return {
    ...actual,
    workspaceApi: {
      ...actual.workspaceApi,
      listUsers: vi.fn(async () => ({ users: [{ id: 'user-22d0f237249ad45b', name: 'Руслан Габбасов', active: true }, { id: 'me', name: 'Me Me', active: true }] })),
      listArtifacts: vi.fn(async () => ({ artifacts: [artifact] })),
      artifactInbox: vi.fn(async () => ({ entries: [] })),
      artifactTags: vi.fn(async () => ({ tags: [] })),
      getArtifact: vi.fn(async () => ({ artifact: { ...artifact, content: 'APPROVED BODY' }, truncated: false })),
      getArtifactRevision: vi.fn(async () => ({
        revision: {
          version: 2, title: artifact.title, content: 'REVISION BODY TO APPROVE',
          status: 'in_review', size_bytes: 10, mime_type: 'text/markdown',
          created_at: '2026-11-01T00:00:00Z',
        },
      })),
      listArtifactReviews: vi.fn(async () => ({
        reviews: [{
          id: 'rev-1', artifact_id: 'a1', artifact_version: 2,
          // The agent submits raw tokens: a bare directory id, a bare name,
          // a proper user: token — all three must read as people.
          reviewers: ['user-22d0f237249ad45b', 'Руслан Габбасов', 'user:me', 'role:admin'],
          mode: 'any', status: 'open', requested_at: '2026-11-03T10:00:00Z',
          note: 'please check the rework', decisions: {},
        }],
      })),
    },
  }
})

let root: Root
let container: HTMLElement

async function settle() {
  for (let i = 0; i < 5; i++) {
    await act(async () => { await new Promise((r) => setTimeout(r, 0)) })
  }
}

async function mountSelectedArtifact() {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => { root.render(<I18nProvider><Artifacts /></I18nProvider>) })
  await settle()
  // Open the artifact from the catalog list (clicking the title bubbles to
  // the row's selection handler).
  const row = [...container.querySelectorAll('strong')]
    .find((el) => el.textContent?.includes('Deployment runbook'))
  expect(row).toBeTruthy()
  await act(async () => { row!.click() })
  await settle()
}

/** The detail card is tabbed: switch by the localized tab label. */
function switchDetailTab(scope: HTMLElement, tab: 'versions' | 'review' | 'settings' | 'document') {
  const labels: Record<string, RegExp> = {
    document: /^(Document|Документ)$/,
    review: /(Review queue|Согласование|Review history)/,
    versions: /(Version history|Версии)$/,
    settings: /(Settings|Настройки)$/,
  }
  const tabButton = [...scope.querySelectorAll('[role="tab"]')]
    .find((el) => labels[tab].test(el.textContent?.trim() ?? ''))
  expect(tabButton).toBeTruthy()
  ;(tabButton as HTMLButtonElement).click()
}

afterEach(() => {
  act(() => root?.unmount())
  container?.remove()
  vi.mocked(workspaceApi.listArtifactReviews).mockClear()
})

describe('approved artifact with a revision in review', () => {
  it('reads the revision and shows the approve/reject decisions', async () => {
    await mountSelectedArtifact()
    const text = container.textContent ?? ''
    // The body pane defaults to the version being decided…
    expect(text).toContain('REVISION BODY TO APPROVE')
    // …the approved text is one switch away, clearly labelled.
    expect(text).toMatch(/v1 · (current|текущая)/)
    expect(text).toMatch(/v2 · (revision|редакция)/)
    // The review surface lives in its own tab and opens automatically for a
    // round under review; the decision buttons are right there.
    expect(text).toMatch(/(Approve|Утвердить)/)
    expect(text).toMatch(/(Reject|Отклонить)/)
  })

  it('compares versions as a rich diff against the previous one by default', async () => {
    await mountSelectedArtifact()
    await act(async () => { switchDetailTab(container, 'versions') })
    await settle()
    const compare = [...container.querySelectorAll('button')]
      .find((b) => /(Compare versions|Сравнить версии)/.test(b.textContent ?? ''))
    expect(compare).toBeTruthy()
    await act(async () => { compare!.click() })
    await settle()
    const text = container.textContent ?? ''
    // The diff opens against the PREVIOUS version (v1 — the approved body)
    // and names both sides…
    expect(text).toContain('APPROVED BODY')
    expect(text).toContain('REVISION BODY TO APPROVE')
    // …with the word-level story and the added/removed accounting visible.
    expect(text).toMatch(/(What changed|Что изменилось)/)
    expect(text).toMatch(/[+~−-][0-9]/)
  })

  it('reads review rounds as people, not raw reviewer tokens', async () => {
    await mountSelectedArtifact()
    await act(async () => { switchDetailTab(container, 'review') })
    await settle()
    const text = container.textContent ?? ''
    expect(text).toMatch(/(Reviewers|Согласующие)/)
    // The review mode is human text, never the raw enum value.
    expect(text).toMatch(/(any reviewer|любой рецензент)/)
    expect(text).not.toMatch(/(режим|mode): any\b(?! reviewer)/)
    // A bare directory id resolves to the human name…
    expect(text).toContain('Руслан Габбасов')
    // …a proper user: token too…
    expect(text).toContain('Me Me')
    // …and no raw ids leak through (the role token localizes by locale).
    expect(text).toMatch(/admin/)
    expect(text).not.toContain('user-22d0f237249ad45b')
    expect(text).not.toContain('user:me')
    expect(text).not.toContain('role:admin')
  })
})

describe('my approvals tab', () => {
  it('stays put on a hand-picked tab when the queue is empty', async () => {
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
    act(() => { root.render(<I18nProvider><Artifacts /></I18nProvider>) })
    await settle()
    // The mount probe falls back to the catalog when the queue is empty…
    expect(container.textContent ?? '').not.toMatch(/(No open approvals|Открытых согласований)/)
    const tab = [...container.querySelectorAll('button')]
      .find((b) => /(My approvals|На моём согласовании)/.test(b.textContent ?? ''))
    expect(tab).toBeTruthy()
    // …but a hand-picked tab stays put and explains the empty queue instead
    // of bouncing back.
    await act(async () => { tab!.click() })
    await settle()
    expect(container.textContent ?? '').toMatch(/(No open approvals addressed to you|Открытых согласований на тебя нет)/)
    await act(async () => { tab!.click() })
    await settle()
    expect(container.textContent ?? '').toMatch(/(No open approvals addressed to you|Открытых согласований на тебя нет)/)
  })
})
