// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import Layout from './Layout'
import { I18nProvider } from './i18n'

// Incident 2026-10-21 (spec R3/F5): while the kernel API is unreachable, the
// project selector must show a visible error with a Retry — not silently go
// empty — and a successful retry must bring the list back.

const PROJECTS_URL = '/kernel-api/v1/workspace/projects'
const PROJECTS = [{ id: 'alpha', name: 'Alpha', description: '' }]

let kernelDown: boolean
let projectFetches: number
let root: Root
let container: HTMLElement

function okJson(body: unknown): Response {
  return { ok: true, status: 200, json: async () => body } as unknown as Response
}

function buttons(): HTMLButtonElement[] {
  return Array.from(document.querySelectorAll('button'))
}

function buttonWithText(text: string): HTMLButtonElement | undefined {
  return buttons().find((b) => b.textContent?.trim() === text)
}

async function renderLayout() {
  await act(async () => {
    root.render(
      <I18nProvider>
        <Layout activePage="workspace">
          {() => <div>workspace page</div>}
        </Layout>
      </I18nProvider>,
    )
  })
}

beforeEach(() => {
  ;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
  kernelDown = true
  projectFetches = 0
  localStorage.clear()
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  vi.stubGlobal('fetch', vi.fn((input: unknown) => {
    const url = String(input)
    if (url === PROJECTS_URL) {
      projectFetches += 1
      return kernelDown
        ? Promise.reject(new TypeError('NetworkError: failed to fetch'))
        : Promise.resolve(okJson({ projects: PROJECTS }))
    }
    // Everything else Layout pokes at (whoami, agents, users, org units) is
    // down in this scenario; those failures stay swallowed as before.
    return Promise.reject(new TypeError('kernel unreachable'))
  }))
})

afterEach(async () => {
  await act(async () => { root.unmount() })
  container.remove()
  vi.unstubAllGlobals()
})

describe('Layout project loading (incident 2026-10-21, spec R3)', () => {
  it('shows a visible error with a Retry button when the load fails', async () => {
    await renderLayout()
    expect(projectFetches).toBe(1)
    expect(document.body.textContent).toContain('Failed to load projects')
    expect(document.body.textContent).toContain('NetworkError: failed to fetch')
    expect(buttonWithText('Retry')).toBeTruthy()
  })

  it('retry restores the project list and clears the error', async () => {
    await renderLayout()
    const retry = buttonWithText('Retry')
    expect(retry).toBeTruthy()

    kernelDown = false
    await act(async () => { retry!.click() })

    expect(projectFetches).toBe(2)
    expect(document.body.textContent).not.toContain('Failed to load projects')

    // The selector is visible again and the loaded projects are listed.
    const selector = buttons().find((b) => b.textContent?.includes('All projects'))
    expect(selector).toBeTruthy()
    await act(async () => { selector!.click() })
    expect(document.body.textContent).toContain('Alpha')
    expect(document.body.textContent).toContain('All projects')
  })
})
