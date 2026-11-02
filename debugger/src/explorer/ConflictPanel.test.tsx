// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import ConflictPanel from './ConflictPanel'
import { I18nProvider } from '../i18n'
import { observationApi, type ExploreConflict } from '../observationApi'

// Pair-level verdicts and the tier split: heuristic candidates collapse by
// default (they are queue noise, not recorded contradictions), «Не
// противоречие» records a sticky dismiss, «Пропустить» hides without a
// verdict.

vi.mock('../observationApi', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../observationApi')>()
  return {
    ...actual,
    observationApi: {
      ...actual.observationApi,
      dismissRelation: vi.fn(async () => ({})),
    },
  }
})

let root: Root
let container: HTMLElement

function mount(conflicts: ExploreConflict[]) {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => {
    root.render(
      <I18nProvider>
        <ConflictPanel project="" conflicts={conflicts} />
      </I18nProvider>
    )
  })
}

afterEach(() => {
  act(() => root?.unmount())
  container?.remove()
  vi.mocked(observationApi.dismissRelation).mockClear()
})

const explicit: ExploreConflict = {
  knowledge_ids: ['K-a', 'K-b'],
  basis: 'explicit:contradicts',
  detail: 'связаны явным отношением «противоречит»',
  resolved: false,
}

const candidate: ExploreConflict = {
  knowledge_ids: ['K-c', 'K-d'],
  basis: 'candidate:same-topic-different-state',
  detail: 'items share topic "git"',
  similarity: 0.42,
  resolved: false,
}

describe('ConflictPanel pair verdicts and tiers', () => {
  it('collapses heuristic candidates behind their own header', () => {
    mount([explicit, candidate])
    const text = container.textContent ?? ''
    // The real contradiction is on screen; the candidate is folded away.
    expect(text).toContain('связаны явным отношением')
    expect(text).not.toContain('items share topic')
    // The collapsed tier still reports its size.
    expect(text).toMatch(/Кандидаты|Candidates/)
    expect(text).toContain('(1)')
  })

  it('records «Не противоречие» as a sticky pair dismiss', () => {
    mount([explicit])
    const button = [...container.querySelectorAll('button')]
      .find((b) => /Не противоречие|Not a conflict/.test(b.textContent ?? ''))
    expect(button).toBeTruthy()
    act(() => button!.click())
    expect(observationApi.dismissRelation).toHaveBeenCalledWith(
      expect.objectContaining({ knowledge_id: 'K-a', target_id: 'K-b' })
    )
  })

  it('«Пропустить» hides the pair without any verdict', () => {
    mount([explicit])
    expect(container.textContent).toContain('связаны явным отношением')
    const button = [...container.querySelectorAll('button')]
      .find((b) => /Пропустить|Skip/.test(b.textContent ?? ''))
    expect(button).toBeTruthy()
    act(() => button!.click())
    expect(container.textContent).not.toContain('связаны явным отношением')
    expect(observationApi.dismissRelation).not.toHaveBeenCalled()
  })
})
