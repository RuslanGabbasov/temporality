// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import Artifacts from './Artifacts'
import { I18nProvider } from './i18n'

// Regression: picking «примечание»/«предложить правку» from the selection
// menu blinked and vanished — the comment textarea's autofocus collapses the
// document selection, the selectionchange handler read that as «clicked
// outside» and discarded the draft. The caret inside the popup must keep it
// open; a genuine outside click must still discard.

// jsdom has neither ResizeObserver (Carbon TextArea needs it) nor
// Range.getBoundingClientRect (the hook substitutes a zero rect).
;(globalThis as unknown as { ResizeObserver?: unknown }).ResizeObserver = class {
  observe() {}
  unobserve() {}
  disconnect() {}
}

vi.mock('./workspaceApi', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./workspaceApi')>()
  const artifact = {
    id: 'a1', title: 'Review doc', status: 'approved', version: 1,
    kind: 'reference', provider: 'builtin', size_bytes: 10, mime_type: 'text/markdown',
    created_at: '2026-11-01T00:00:00Z', updated_at: '2026-11-01T00:00:00Z',
    tags: [], reviewers: [], review_mode: 'all',
    content: 'Первый абзац с текстом для выделения.',
  }
  return {
    ...actual,
    workspaceApi: {
      ...actual.workspaceApi,
      listUsers: vi.fn(async () => ({ users: [] })),
      listArtifacts: vi.fn(async () => ({ artifacts: [artifact] })),
      artifactInbox: vi.fn(async () => ({ entries: [] })),
      artifactTags: vi.fn(async () => ({ tags: [] })),
      getArtifact: vi.fn(async () => ({ artifact, truncated: false })),
      listArtifactAnnotations: vi.fn(async () => ({ annotations: [] })),
      getArtifactRevision: vi.fn(async () => ({ revision: null })),
    },
  }
})

vi.mock('./kernelApi', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./kernelApi')>()
  return {
    ...actual,
    whoami: vi.fn(async () => ({ subject: 'writer', role: 'writer', projects: ['*'], auth_enabled: false, user_id: 'writer' })),
  }
})

let root: Root
let container: HTMLElement

async function settle() {
  for (let i = 0; i < 5; i++) {
    await act(async () => { await new Promise((r) => setTimeout(r, 0)) })
  }
}

afterEach(() => {
  act(() => root?.unmount())
  container?.remove()
})

describe('fragment note popup', () => {
  it('keeps the draft open when the caret moves into the comment field', async () => {
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
    act(() => { root.render(<I18nProvider><Artifacts project="p" /></I18nProvider>) })
    await settle()

    const row = [...container.querySelectorAll('strong')].find((el) => el.textContent?.includes('Review doc'))
    expect(row).toBeTruthy()
    await act(async () => { row!.click() })
    // The highlight repaints settle (~60ms/700ms passes) before the range is
    // captured — a range over the pre-repaint nodes is detached by them.
    await new Promise((r) => setTimeout(r, 800))
    await settle()

    const body = container.querySelector('.artifact-review-body') as HTMLElement
    expect(body).toBeTruthy()
    const textNode = body.querySelector('p')?.firstChild as Text
    expect(textNode?.nodeType).toBe(3)

    // Select the first word inside the body and fire selectionchange the way
    // a drag would. Everything runs inside act: the rAF-throttled handler and
    // its flushSync render commit before the act closes.
    await act(async () => {
      const range = document.createRange()
      range.setStart(textNode, 0)
      range.setEnd(textNode, 6)
      const selection = window.getSelection()!
      selection.removeAllRanges()
      selection.addRange(range)
      document.dispatchEvent(new Event('selectionchange'))
      await new Promise((r) => setTimeout(r, 80))
    })

    // The icon menu appeared; pick «comment» (the amber chat bubble).
    const comment = container.querySelector('button.artifact-ann-pick-comment') as HTMLButtonElement
    expect(comment).toBeTruthy()
    await act(async () => { comment.click() })
    await settle()
    const editor = container.querySelector('.artifact-annotation-editor')
    expect(editor).toBeTruthy()
    const field = container.querySelector('#artifact-annotation-comment') as HTMLTextAreaElement | null
    expect(field).toBeTruthy()

    // The textarea autofocus collapses the document selection and fires
    // selectionchange — the popup must survive it.
    await act(async () => {
      document.dispatchEvent(new Event('selectionchange'))
      await new Promise((r) => setTimeout(r, 60))
    })
    expect(container.querySelector('.artifact-annotation-editor')).toBeTruthy()

    // A genuine outside collapse still discards: clear the selection with the
    // focus outside the popup, the way a document click would.
    await act(async () => {
      field!.blur()
      window.getSelection()!.removeAllRanges()
      document.dispatchEvent(new Event('selectionchange'))
      await new Promise((r) => setTimeout(r, 60))
    })
    expect(container.querySelector('.artifact-annotation-editor')).toBeNull()
  })
})
