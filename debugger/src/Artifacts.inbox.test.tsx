// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import Artifacts from './Artifacts'
import { I18nProvider } from './i18n'
import { workspaceApi, type Artifact, type ArtifactInboxEntry, type ArtifactReview } from './workspaceApi'

// Regression: the approvals tab («На моём согласовании») is a separate fetch
// from the catalog list — mutations that change open rounds (delete, submit)
// refreshed the catalog but left the inbox stale: a deleted artifact kept
// its queue row, a just-submitted round never appeared, and the badge lied.

// Carbon's TextArea measures itself with ResizeObserver, which jsdom lacks.
class ResizeObserverStub { observe() {} unobserve() {} disconnect() {} }
;(window as unknown as { ResizeObserver: unknown }).ResizeObserver = ResizeObserverStub

vi.mock('./workspaceApi', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./workspaceApi')>()
  return {
    ...actual,
    workspaceApi: {
      ...actual.workspaceApi,
      listUsers: vi.fn(async () => ({ users: [{ id: 'me', name: 'Me Me', active: true }] })),
      artifactTags: vi.fn(async () => ({ tags: [] })),
      listArtifactAnnotations: vi.fn(async () => ({ annotations: [] })),
    },
  }
})

function makeRow(id: string, title: string, status: Artifact['status'], version = 1): Artifact {
  return {
    id, title, description: '', provider: 'builtin', size_bytes: 10,
    mime_type: 'text/markdown', tags: [], kind: 'reference', version, status,
    created_at: '2026-10-01T00:00:00Z', updated_at: '2026-11-01T00:00:00Z',
  }
}

// The server state the tests mutate; the spy implementations below read these
// at call time, so a flip emulates what the backend does after a mutation.
let rows: Artifact[]
let inboxEntries: ArtifactInboxEntry[]
let reviews: ArtifactReview[]

function reviewRound(artifact: Artifact): ArtifactReview {
  return {
    id: 'rev-1', artifact_id: artifact.id, artifact_version: (artifact.version ?? 1) + 1,
    reviewers: ['me'], mode: 'any', status: 'open',
    requested_by: 'me', requested_at: '2026-11-03T10:00:00Z', decisions: {},
  }
}

beforeAll(() => {
  vi.spyOn(workspaceApi, 'listArtifacts').mockImplementation(async () => ({ artifacts: rows, count: rows.length }))
  vi.spyOn(workspaceApi, 'getArtifact').mockImplementation(async () => ({ artifact: { ...rows[0], content: 'BODY' }, offset: 0, next_offset: 0, truncated: false }))
  vi.spyOn(workspaceApi, 'artifactInbox').mockImplementation(async () => ({ entries: inboxEntries, count: inboxEntries.length }))
  vi.spyOn(workspaceApi, 'listArtifactReviews').mockImplementation(async () => ({ reviews, count: reviews.length }))
})

afterAll(() => { vi.restoreAllMocks() })

let root: Root
let container: HTMLElement

async function settle() {
  for (let i = 0; i < 5; i++) {
    await act(async () => { await new Promise((r) => setTimeout(r, 0)) })
  }
}

async function mountPage() {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => { root.render(<I18nProvider><Artifacts /></I18nProvider>) })
  await settle()
}

async function selectRow(title: string) {
  await act(async () => {
    ;[...container.querySelectorAll('strong')]
      .find((el) => el.textContent?.includes(title))!
      .click()
  })
  await settle()
}

afterEach(() => {
  act(() => root?.unmount())
  container?.remove()
  vi.mocked(workspaceApi.artifactInbox).mockClear()
})

describe('approvals inbox freshness', () => {
  it('drops the inbox row when the artifact is deleted', async () => {
    const row = makeRow('a1', 'Deployment runbook', 'in_review', 2)
    rows = [row]
    reviews = []
    inboxEntries = [{
      review: reviewRound(row), artifact_id: row.id, artifact_title: row.title,
      status: 'in_review', version: row.version ?? 1, matched: ['user:me'],
      created_at: row.created_at, updated_at: row.updated_at,
    }]
    await mountPage()

    // The queue opens by default and shows the pending round; the approver
    // opens the artifact right from the queue row.
    expect(container.textContent ?? '').toContain('Deployment runbook')
    await selectRow('Deployment runbook')

    // The artifact dies — and the server drops its open round with it.
    const inboxCallsBefore = vi.mocked(workspaceApi.artifactInbox).mock.calls.length
    vi.spyOn(workspaceApi, 'deleteArtifact').mockResolvedValue({ deleted: true })
    rows = []
    inboxEntries = []
    const trash = container.querySelector('button[class*="btn--danger--ghost"]') as HTMLButtonElement
    expect(trash).toBeTruthy()
    await act(async () => { trash.click() })
    await settle()
    const confirm = [...container.querySelectorAll<HTMLElement>('.modal-panel button')]
      .find((b) => /^(Delete|Удалить)$/.test(b.textContent?.trim() ?? ''))
    expect(confirm).toBeTruthy()
    await act(async () => { confirm!.click() })
    await settle()

    // The inbox was refreshed in the background…
    expect(vi.mocked(workspaceApi.artifactInbox).mock.calls.length).toBeGreaterThan(inboxCallsBefore)
    // …and the queue no longer lists the deleted artifact.
    expect(container.textContent ?? '').toMatch(/(No open approvals addressed to you|Открытых согласований на тебя нет)/)
    expect(container.textContent ?? '').not.toContain('Deployment runbook')
  })

  it('lands on the refreshed review tab after submitting for approval', async () => {
    const row = makeRow('a2', 'Onboarding checklist', 'draft')
    rows = [row]
    reviews = []
    inboxEntries = []
    await mountPage()

    // A draft with an empty queue: the catalog is the default tab.
    await selectRow('Onboarding checklist')

    // Open the submit dialog and type a reviewer (Enter commits a bare token).
    const openButton = [...container.querySelectorAll('button')]
      .find((b) => /^(Send to review|На согласование)$/.test(b.textContent?.trim() ?? ''))
    expect(openButton).toBeTruthy()
    await act(async () => { openButton!.click() })
    await settle()
    const pickerInput = container.querySelector('.modal-panel input') as HTMLInputElement
    expect(pickerInput).toBeTruthy()
    const setValue = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!
    await act(async () => {
      setValue.call(pickerInput, 'me')
      pickerInput.dispatchEvent(new Event('input', { bubbles: true }))
    })
    await act(async () => {
      pickerInput.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }))
    })

    // The round is created server-side; the artifact moves to in_review.
    const inboxCallsBefore = vi.mocked(workspaceApi.artifactInbox).mock.calls.length
    const submitSpy = vi.spyOn(workspaceApi, 'submitArtifact').mockResolvedValue(reviewRound(row))
    reviews = [reviewRound(row)]
    row.status = 'in_review'
    const confirm = [...container.querySelectorAll<HTMLElement>('.modal-panel button')]
      .find((b) => /^(Send to review|На согласование)$/.test(b.textContent?.trim() ?? ''))
    expect(confirm).toBeTruthy()
    await act(async () => { confirm!.click() })
    await settle()

    expect(submitSpy).toHaveBeenCalled()
    // The inbox was quietly refreshed (the submitter may be a reviewer too).
    expect(vi.mocked(workspaceApi.artifactInbox).mock.calls.length).toBeGreaterThan(inboxCallsBefore)
    // The page landed on the review tab and the new round is right there.
    const activeTab = container.querySelector('[role="tab"][aria-selected="true"]')
    expect(activeTab?.textContent ?? '').toMatch(/(Review queue|Очередь согласования)/)
    expect(container.textContent ?? '').toMatch(/(Reviewers|Согласующие)/)
    expect(container.textContent ?? '').toContain('Me Me')
  })
})
