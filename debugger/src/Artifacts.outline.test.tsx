// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import Artifacts from './Artifacts'
import { parseOutline } from './DocumentOutline'
import { I18nProvider } from './i18n'
import { workspaceApi, type Artifact } from './workspaceApi'

// Large artifacts are a wall of markdown: the outline (Notion-style section
// navigation) must list the headings beside the body, jump on click, and stay
// out of the way for documents without sections.

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

const DOC = [
  '# Intro',
  'lead paragraph',
  '',
  '```bash',
  '# not a heading',
  '```',
  '',
  '## Setup',
  'text',
  '',
  '### Details',
  'more text',
  '',
  '## Wrap-up',
  'final',
].join('\n')

function makeRow(content: string): Artifact {
  return {
    id: 'a1', title: 'Operations manual', description: '', provider: 'builtin',
    size_bytes: content.length, mime_type: 'text/markdown', tags: [],
    kind: 'reference', version: 1, status: 'draft', content,
    created_at: '2026-10-01T00:00:00Z', updated_at: '2026-11-01T00:00:00Z',
  }
}

let rows: Artifact[]

beforeAll(() => {
  vi.spyOn(workspaceApi, 'listArtifacts').mockImplementation(async () => ({ artifacts: rows, count: rows.length }))
  vi.spyOn(workspaceApi, 'getArtifact').mockImplementation(async () => ({ artifact: rows[0], offset: 0, next_offset: 0, truncated: false }))
  vi.spyOn(workspaceApi, 'artifactInbox').mockImplementation(async () => ({ entries: [], count: 0 }))
})

afterAll(() => { vi.restoreAllMocks() })

let root: Root
let container: HTMLElement

async function settle() {
  for (let i = 0; i < 5; i++) {
    await act(async () => { await new Promise((r) => setTimeout(r, 0)) })
  }
}

async function mountSelected(content: string) {
  rows = [makeRow(content)]
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => { root.render(<I18nProvider><Artifacts /></I18nProvider>) })
  await settle()
  await act(async () => {
    ;[...container.querySelectorAll('strong')]
      .find((el) => el.textContent?.includes('Operations manual'))!
      .click()
  })
  await settle()
}

afterEach(() => {
  act(() => root?.unmount())
  container?.remove()
})

describe('document outline', () => {
  it('lists the headings beside the body, skipping code fences', async () => {
    await mountSelected(DOC)
    const items = [...container.querySelectorAll('.artifact-outline-item')]
    expect(items.map((el) => el.textContent)).toEqual(['Intro', 'Setup', 'Details', 'Wrap-up'])
    // The fenced pseudo-heading renders as code (fine) but never becomes a
    // navigation entry.
    expect(items.map((el) => el.textContent).join('\n')).not.toContain('not a heading')
  })

  it('scrolls to the section on click', async () => {
    await mountSelected(DOC)
    const heading = container.querySelector('.artifact-body h2') as HTMLElement
    expect(heading?.textContent).toContain('Setup')
    vi.spyOn(heading, 'getBoundingClientRect').mockReturnValue({ top: 500, bottom: 560, width: 100, height: 60, left: 0, right: 100, x: 0, y: 500, toJSON: () => '' } as DOMRect)
    const scrollTo = vi.fn()
    window.scrollTo = scrollTo as typeof window.scrollTo
    const setup = [...container.querySelectorAll<HTMLElement>('.artifact-outline-item')]
      .find((el) => el.textContent === 'Setup')!
    await act(async () => { setup.click() })
    expect(scrollTo).toHaveBeenCalledOnce()
    // window.scrollY (0) + heading top (500) − the header offset + margin.
    expect(scrollTo).toHaveBeenCalledWith({ top: 390, behavior: 'smooth' })
  })

  it('collapses and expands on the title click', async () => {
    await mountSelected(DOC)
    const title = container.querySelector('.artifact-outline-title') as HTMLButtonElement
    expect(title).toBeTruthy()
    await act(async () => { title.click() })
    expect(container.querySelectorAll('.artifact-outline-item').length).toBe(0)
    await act(async () => { title.click() })
    expect(container.querySelectorAll('.artifact-outline-item').length).toBe(4)
  })

  it('stays hidden for documents without sections', async () => {
    await mountSelected('Just a paragraph, no sections.')
    expect(container.querySelector('.artifact-outline')).toBeNull()
  })
})

describe('parseOutline', () => {
  it('parses ATX and setext headings with deduplicated slugs', () => {
    const outline = parseOutline('# Plan\n\n## Plan\n\nSame\n===\n\nSame\n===')
    expect(outline.map((h) => [h.level, h.text])).toEqual([[1, 'Plan'], [2, 'Plan'], [1, 'Same'], [1, 'Same']])
    expect(outline.map((h) => h.id)).toEqual(['plan', 'plan-2', 'same', 'same-2'])
  })

  it('ignores code fences, table separators and thematic breaks', () => {
    const outline = parseOutline([
      'text',
      '---',
      '',
      '| a | b |',
      '|---|---|',
      '',
      '```',
      '# fenced',
      '~~~',
      'also fenced',
      '```',
      '',
      '- list item',
      '---',
    ].join('\n'))
    // text + --- is a setext h2; the rest is noise.
    expect(outline.map((h) => h.text)).toEqual(['text'])
  })

  it('strips inline markdown from heading labels', () => {
    const outline = parseOutline('## The `run.sh` **script** and [docs](https://x.y)')
    expect(outline[0].text).toBe('The run.sh script and docs')
  })
})
