// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import Artifacts from './Artifacts'
import { I18nProvider } from './i18n'
import { workspaceApi, type ArtifactAnnotation } from './workspaceApi'

// Regression: the «Подготовить редакцию с ИИ» dialog always said «В проекте
// нет доступных агентов» — the agent list state existed but nothing ever
// fetched it (setRevisionAgents had no caller). Opening the dialog must load
// the project's enabled agents.

vi.mock('./workspaceApi', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./workspaceApi')>()
  const artifact = {
    id: 'a1', title: 'Review doc', status: 'approved', version: 1,
    kind: 'reference', provider: 'builtin', size_bytes: 10, mime_type: 'text/markdown',
    created_at: '2026-11-01T00:00:00Z', updated_at: '2026-11-01T00:00:00Z',
    tags: [], reviewers: [], review_mode: 'all',
    content: 'Текст артефакта с примечанием ревьюера.',
  }
  const annotation: ArtifactAnnotation = {
    id: 'ann-1', version: 1, kind: 'change', quote: 'Текст артефакта',
    start_offset: 0, end_offset: 14, replacement: 'Исправленный текст',
    comment: 'Переформулировать', created_by: 'me', created_at: '2026-11-02T00:00:00Z',
  }
  return {
    ...actual,
    workspaceApi: {
      ...actual.workspaceApi,
      listUsers: vi.fn(async () => ({ users: [{ id: 'me', name: 'Me Me', active: true }] })),
      listArtifacts: vi.fn(async () => ({ artifacts: [artifact], count: 1 })),
      artifactInbox: vi.fn(async () => ({ entries: [], count: 0 })),
      artifactTags: vi.fn(async () => ({ tags: [] })),
      getArtifact: vi.fn(async () => ({ artifact, offset: 0, next_offset: 0, truncated: false })),
      listArtifactAnnotations: vi.fn(async () => ({ annotations: [annotation] })),
      getArtifactRevision: vi.fn(async () => ({ revision: null })),
      listArtifactReviews: vi.fn(async () => ({ reviews: [], count: 0 })),
      listAgentsByProject: vi.fn(async () => ({
        agents: [
          { id: 'ag1', name: 'Fixer', enabled: true, description: '', model: 'm', provider: 'p' },
          { id: 'ag2', name: 'Retired', enabled: false, description: '', model: 'm', provider: 'p' },
        ],
      })),
    },
  }
})

beforeAll(() => {
  // Carbon TextArea measures itself with ResizeObserver, which jsdom lacks.
  ;(globalThis as unknown as { ResizeObserver?: unknown }).ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
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
  vi.mocked(workspaceApi.listAgentsByProject).mockClear()
})

describe('AI revision dialog', () => {
  it('loads the project agents when opened and skips disabled ones', async () => {
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
    act(() => { root.render(<I18nProvider><Artifacts project="p1" /></I18nProvider>) })
    await settle()

    await act(async () => {
      ;[...container.querySelectorAll('strong')]
        .find((el) => el.textContent?.includes('Review doc'))!
        .click()
    })
    await settle()

    // The AI-revision button lives on the review tab.
    const reviewTab = [...container.querySelectorAll('[role="tab"]')]
      .find((el) => /(Review history|История согласований)/.test(el.textContent ?? ''))
    expect(reviewTab).toBeTruthy()
    await act(async () => { (reviewTab as HTMLElement).click() })
    await settle()

    const openButton = [...container.querySelectorAll('button')]
      .find((b) => /(Prepare revision with AI|Подготовить редакцию с ИИ)/.test(b.textContent ?? ''))
    expect(openButton).toBeTruthy()
    await act(async () => { openButton!.click() })
    await settle()

    expect(vi.mocked(workspaceApi.listAgentsByProject)).toHaveBeenCalledWith('p1')
    // The dialog shows the agent picker, not the «no agents» warning…
    expect(container.querySelector('.modal-panel .cds--select')).toBeTruthy()
    expect(container.textContent ?? '').not.toContain('No agents are available in this project')
    // …enabled agents only…
    const options = [...container.querySelectorAll('.modal-panel select option')].map((o) => o.textContent)
    expect(options.join('\n')).toContain('Fixer')
    expect(options.join('\n')).not.toContain('Retired')
  })

  it('shows the no-agents warning only when the project really has none', async () => {
    vi.mocked(workspaceApi.listAgentsByProject).mockResolvedValue({ agents: [] })
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
    act(() => { root.render(<I18nProvider><Artifacts project="p1" /></I18nProvider>) })
    await settle()

    await act(async () => {
      ;[...container.querySelectorAll('strong')]
        .find((el) => el.textContent?.includes('Review doc'))!
        .click()
    })
    await settle()
    const reviewTab = [...container.querySelectorAll('[role="tab"]')]
      .find((el) => /(Review history|История согласований)/.test(el.textContent ?? ''))
    await act(async () => { (reviewTab as HTMLElement).click() })
    await settle()
    const openButton = [...container.querySelectorAll('button')]
      .find((b) => /(Prepare revision with AI|Подготовить редакцию с ИИ)/.test(b.textContent ?? ''))
    await act(async () => { openButton!.click() })
    await settle()

    expect(container.textContent ?? '').toContain('No agents are available in this project')
  })
})
