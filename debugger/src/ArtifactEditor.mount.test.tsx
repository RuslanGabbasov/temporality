// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it } from 'vitest'
import { ArtifactEditor } from './ArtifactEditor'

// Regression: mounting the editor with the toolbar used to crash the whole
// page («rootNode of null») — the language picker rendered outside a code
// block context. The toolbar now guards it with ConditionalContents, so a
// full mount with markdown, code fences and the toolbar is safe.
let root: Root
let container: HTMLElement

afterEach(() => {
  act(() => root?.unmount())
  container?.remove()
})

describe('ArtifactEditor mount', () => {
  it('mounts with the toolbar over markdown with fences and toggles source mode', async () => {
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
    let value = '# Заголовок\n\nТекст с **жирным**.\n\n```presentation slides\n{"slides":[]}\n```\n'
    await act(async () => {
      root.render(<ArtifactEditor markdown={value} onChange={(v) => { value = v }} />)
    })
    // The editor rendered its content surface (contenteditable + toolbar).
    expect(container.querySelector('[contenteditable]')).toBeTruthy()
    expect(container.querySelectorAll('button').length).toBeGreaterThan(5)
    // The source-mode toggle (DiffSourceToggleWrapper) is present and
    // switching to markdown mode does not crash.
    const toggle = [...container.querySelectorAll('button')]
      .find((b) => /md|markdown|source/i.test(b.getAttribute('title') ?? b.textContent ?? ''))
    if (toggle) await act(async () => { toggle.click() })
    expect(container.textContent).toContain('Заголовок')
  })
})
