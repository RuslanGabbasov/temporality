// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import Artifacts from './Artifacts'
import { I18nProvider } from './i18n'

// Regression: an artifact WITH tags renders its modification time inside the
// tag-chip row, where it inherited the card's base font — it showed up larger
// and brighter than the tiny muted chips it shares the line with (the no-tags
// branch styled it mono 0.7rem muted on the meta line). The tag-row date must
// carry the same muted mono look and never split its text across lines.

vi.mock('./workspaceApi', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./workspaceApi')>()
  const artifact = {
    id: 'tagged', title: 'Tagged runbook', status: 'approved', version: 1,
    kind: 'reference', provider: 'store', size_bytes: 10, mime_type: 'text/markdown',
    created_at: '2026-10-01T00:00:00Z', updated_at: '2026-10-01T12:34:00Z',
    tags: ['deploy', 'runbook'], reviewers: [], review_mode: 'all',
  }
  return {
    ...actual,
    workspaceApi: {
      ...actual.workspaceApi,
      listUsers: vi.fn(async () => ({ users: [] })),
      listArtifacts: vi.fn(async () => ({ artifacts: [artifact] })),
      artifactInbox: vi.fn(async () => ({ entries: [] })),
      artifactTags: vi.fn(async () => ({ tags: [{ tag: 'deploy', count: 1 }, { tag: 'runbook', count: 1 }] })),
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

afterEach(() => {
  act(() => root?.unmount())
  container?.remove()
})

describe('list card date with tags', () => {
  it('styles the tag-row date like the meta line, never wrapping mid-date', async () => {
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
    act(() => { root.render(<I18nProvider><Artifacts /></I18nProvider>) })
    await settle()

    const chip = [...container.querySelectorAll('span')].find((el) => el.textContent === '#deploy')
    expect(chip).toBeTruthy()
    const tagRow = chip!.parentElement
    expect(tagRow).toBeTruthy()

    // Exactly one date per card, sitting in the tag row: the meta line stays
    // date-free when tags carry it. (jsdom drops text-decoration-line, so the
    // TimeHint is identified by its title tooltip instead.)
    const dates = [...tagRow!.querySelectorAll('span[title]')]
    expect(dates).toHaveLength(1)
    const date = dates[0]
    expect(date.style.fontFamily).toContain('monospace')
    expect(date.style.fontSize).toBe('0.7rem')
    expect(date.style.color).toBe('var(--tm-text-3)')
    // A tight row wraps the whole date to its own line; it must never split
    // «10 Nov, 12:34» into two fragments.
    expect(date.style.whiteSpace).toBe('nowrap')
  })
})
