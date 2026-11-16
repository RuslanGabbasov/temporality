// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import Artifacts from './Artifacts'
import { footnoteIndex, parseFootnotes, footnoteReferenceOrder, artifactIdFromHref, referencedArtifactIds } from './artifactLinks'
import { I18nProvider } from './i18n'
import { workspaceApi, type Artifact } from './workspaceApi'

// Artifact bodies cross-link: internal anchors ([Setup](#setup)), other
// artifacts ([runbook](artifact:a2), @artifact:a3 mentions), reference-style
// links ([text][ref]) and footnotes ([^1]). The read view must keep every
// href (no stripped schemes, no forced new tabs for in-page links), wrap
// mentions into chips, hint footnote text and the target artifact on hover,
// scroll to anchors and open linked artifacts in place.

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
  '',
  'See [Setup](#setup) below, external [runbook](artifact:a2) and a mention @artifact:a3.',
  '',
  'A footnote[^1] and a named one[^note], plus a [reference][ref] link.',
  '',
  '## Setup',
  '',
  'Steps live here.',
  '',
  '[^1]: First footnote text.',
  '[^note]: Named footnote with **bold**.',
  '',
  '[ref]: https://example.com/docs',
].join('\n')

const byId: Record<string, Partial<Artifact> & { id: string; title: string }> = {
  a1: { id: 'a1', title: 'Operations manual', status: 'draft', version: 1 },
  a2: { id: 'a2', title: 'Deployment runbook', status: 'approved', version: 2 },
  a3: { id: 'a3', title: 'Onboarding checklist', status: 'in_review', version: 3 },
}

function makeRow(id: string): Artifact {
  const info = byId[id]
  return {
    id, title: info.title, description: '', provider: 'builtin', size_bytes: 10,
    mime_type: 'text/markdown', tags: [], kind: 'reference', version: info.version, status: info.status as Artifact['status'],
    created_at: '2026-10-01T00:00:00Z', updated_at: '2026-11-01T00:00:00Z',
  }
}

let rows: Artifact[]

beforeAll(() => {
  vi.spyOn(workspaceApi, 'listArtifacts').mockImplementation(async () => ({ artifacts: rows, count: rows.length }))
  vi.spyOn(workspaceApi, 'getArtifact').mockImplementation(async (id: string) => {
    const info = byId[id] ?? byId.a1
    return { artifact: { ...makeRow(id), content: id === 'a1' ? DOC : `Body of ${info.title}.` }, offset: 0, next_offset: 0, truncated: false }
  })
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

// The mention linkify pass runs on a 700ms timer (after the annotation
// highlight pass) — the short settle loop never reaches it.
async function settleLong() {
  await act(async () => { await new Promise((r) => setTimeout(r, 800)) })
}

// The hint/scroll paths bail on a zero rect (no layout in jsdom) — give the
// hovered/scrolled elements a believable geometry.
function stubRect(el: Element, top: number) {
  vi.spyOn(el, 'getBoundingClientRect').mockReturnValue({ top, bottom: top + 20, width: 100, height: 20, left: 10, right: 110, x: 10, y: top, toJSON: () => '' } as DOMRect)
}

async function mountSelected() {
  rows = [makeRow('a1')]
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
  await settleLong()
}

afterEach(() => {
  act(() => root?.unmount())
  container?.remove()
})

describe('artifact cross-links', () => {
  it('keeps every link kind alive in the rendered body', async () => {
    await mountSelected()
    // The heading carries its slug id (GitHub-style) for the anchor link…
    expect(container.querySelector('h2#setup')).toBeTruthy()
    const internal = container.querySelector('a[href="#setup"]') as HTMLAnchorElement
    expect(internal).toBeTruthy()
    expect(internal.getAttribute('target')).toBeNull()
    // …the artifact link survives the URL sanitizer…
    const artifactLink = container.querySelector('a[href="artifact:a2"]') as HTMLAnchorElement
    expect(artifactLink).toBeTruthy()
    expect(artifactLink.getAttribute('target')).toBeNull()
    // …the mention became a chip…
    const mention = container.querySelector('a.artifact-mention[data-artifact-id="a3"]')
    expect(mention).toBeTruthy()
    expect(mention?.textContent).toContain('@artifact:a3')
    // …the reference-style link resolved to its definition URL…
    const ref = container.querySelector('a[href="https://example.com/docs"]')
    expect(ref?.textContent).toContain('reference')
    // …and both footnote references render as superscripts.
    expect(container.querySelectorAll('sup a[href*="user-content-fn-"]').length).toBe(2)
  })

  it('hints footnote text on hover, keeping the section at the end', async () => {
    await mountSelected()
    const refs = [...container.querySelectorAll<HTMLAnchorElement>('sup a[href*="user-content-fn-"]')]
    stubRect(refs[0], 100)
    stubRect(refs[1], 200)
    await act(async () => {
      refs[0].dispatchEvent(new Event('mouseover', { bubbles: true }))
    })
    let popover = container.querySelector('.artifact-link-popover')
    expect(popover?.textContent ?? '').toMatch(/(Footnote 1|Сноска 1)/)
    expect(popover?.textContent).toContain('First footnote text.')
    await act(async () => {
      refs[1].dispatchEvent(new Event('mouseover', { bubbles: true }))
    })
    popover = container.querySelector('.artifact-link-popover')
    // Inline markdown is stripped in the hint…
    expect(popover?.textContent).toContain('Named footnote with bold.')
    expect(popover?.textContent).not.toContain('**')
    // …and the footnotes section itself still sits at the end.
    expect(container.querySelector('[data-footnotes]')).toBeTruthy()
    // Moving the pointer to a non-link element hides the hint.
    await act(async () => {
      refs[0].closest('.artifact-body')!.dispatchEvent(new Event('mouseover', { bubbles: true }))
    })
    expect(container.querySelector('.artifact-link-popover')).toBeNull()
  })

  it('hints the target artifact on mentions and artifact: links', async () => {
    await mountSelected()
    const link = container.querySelector('a[href="artifact:a2"]') as HTMLElement
    stubRect(link, 100)
    await act(async () => { link.dispatchEvent(new Event('mouseover', { bubbles: true })) })
    const popover = container.querySelector('.artifact-link-popover')
    expect(popover?.textContent).toContain('Deployment runbook')
    expect(popover?.textContent).toMatch(/approved/i)
    const mention = container.querySelector('a.artifact-mention[data-artifact-id="a3"]') as HTMLElement
    stubRect(mention, 120)
    await act(async () => { mention.dispatchEvent(new Event('mouseover', { bubbles: true })) })
    expect(container.querySelector('.artifact-link-popover')?.textContent).toContain('Onboarding checklist')
  })

  it('scrolls to the section on an internal anchor click', async () => {
    await mountSelected()
    const heading = container.querySelector('h2#setup') as HTMLElement
    vi.spyOn(heading, 'getBoundingClientRect').mockReturnValue({ top: 480, bottom: 540, width: 100, height: 60, left: 0, right: 100, x: 0, y: 480, toJSON: () => '' } as DOMRect)
    const scrollTo = vi.fn()
    window.scrollTo = scrollTo as typeof window.scrollTo
    const link = container.querySelector('a[href="#setup"]') as HTMLElement
    await act(async () => { link.click() })
    expect(scrollTo).toHaveBeenCalledOnce()
    expect(scrollTo).toHaveBeenCalledWith({ top: 360, behavior: 'smooth' })
    // The hash stays clean — no router interference.
    expect(window.location.hash).toBe('')
  })

  it('opens the artifact linked by a mention chip on click', async () => {
    await mountSelected()
    const getArtifactSpy = vi.mocked(workspaceApi.getArtifact)
    getArtifactSpy.mockClear()
    const mention = container.querySelector('a.artifact-mention[data-artifact-id="a3"]') as HTMLElement
    await act(async () => { mention.click() })
    await settle()
    expect(getArtifactSpy).toHaveBeenCalledWith('a3')
    expect(container.textContent ?? '').toContain('Onboarding checklist')
  })

  it('opens the artifact linked by artifact: on click', async () => {
    await mountSelected()
    const getArtifactSpy = vi.mocked(workspaceApi.getArtifact)
    getArtifactSpy.mockClear()
    const link = container.querySelector('a[href="artifact:a2"]') as HTMLElement
    await act(async () => { link.click() })
    await settle()
    expect(getArtifactSpy).toHaveBeenCalledWith('a2')
    expect(container.textContent ?? '').toContain('Deployment runbook')
  })
})

describe('artifactLinks helpers', () => {
  it('parses footnotes with continuation lines and reference order', () => {
    const content = [
      'ref one[^1] then two[^note], again one[^1].',
      '',
      '[^1]: first',
      '[^note]: named,',
      '    continued line',
      '',
      '[^orphan]: never referenced',
    ].join('\n')
    expect(parseFootnotes(content).map((d) => [d.label, d.text])).toEqual([
      ['1', 'first'],
      ['note', 'named, continued line'],
      ['orphan', 'never referenced'],
    ])
    // Numbering follows first reference, not definition order; the orphan
    // would never render.
    expect(footnoteReferenceOrder(content)).toEqual(['1', 'note'])
    expect(footnoteIndex(content)).toEqual(['first', 'named, continued line'])
  })

  it('extracts artifact ids from links and mentions', () => {
    expect(artifactIdFromHref('artifact:a1')).toBe('a1')
    expect(artifactIdFromHref('artifact://a1')).toBe('a1')
    expect(artifactIdFromHref('https://x.y')).toBeNull()
    expect(artifactIdFromHref('artifact:')).toBeNull()
    expect(referencedArtifactIds('x @artifact:a1 y](artifact://a2) [z](https://q.w) @artifact:a1')).toEqual(['a1', 'a2'])
  })
})
