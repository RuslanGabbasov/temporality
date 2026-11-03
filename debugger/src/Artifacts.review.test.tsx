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
      listUsers: vi.fn(async () => ({ users: [] })),
      listArtifacts: vi.fn(async () => ({ artifacts: [artifact] })),
      artifactInbox: vi.fn(async () => ({ inbox: [] })),
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
          id: 'rev-1', artifact_id: 'a1', artifact_version: 2, reviewers: ['user:me'],
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
    // The review surface is reachable: the open round carries its decisions.
    expect(text).toMatch(/(Approve|Утвердить)/)
    expect(text).toMatch(/(Reject|Отклонить)/)
  })
})
