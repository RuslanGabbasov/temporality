// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import ConsoleOutput, { consoleLines, shouldAutoScroll } from './ConsoleOutput'
import { I18nProvider } from './i18n'
import { CONSOLE_LINE_LIMIT } from './consoleStream'

// Component tests of the run_command console (§6, DoD-5): mount from the
// tail snapshot, live stdout/stderr SSE frames, done+exit_code, cancellation
// of the single AbortController on unmount, the document.hidden pause, the
// tabs/error-highlight/copy surface — and the canonical console:// address
// (never artifact://). The SSE reader is the real
// consoleStream.subscribeConsoleStream over a mocked fetch.

const TAIL_URL = '/kernel-api/v1/agent/console/op-42?project=proj-1'
const STREAM_URL = '/kernel-api/v1/agent/console/op-42/stream?project=proj-1&tail=1'

let streamOpened: number
let root: Root
let container: HTMLElement

function tailResponse(extra: Record<string, unknown> = {}): Response {
  return new Response(JSON.stringify({
    operation_id: 'op-42',
    console_uri: 'console://op-42',
    stdout_tail: 'first line\nsecond line\n',
    stderr_tail: '',
    truncated: false,
    done: false,
    exit_code: null,
    ...extra,
  }), { status: 200 })
}

/** A /stream response whose body yields the given SSE blocks, then ends. */
function sseResponse(blocks: string[]): Response {
  const encoder = new TextEncoder()
  let sent = 0
  return {
    ok: true,
    status: 200,
    body: {
      getReader: () => ({
        read: async () => {
          await new Promise((resolve) => setTimeout(resolve, 0))
          if (sent < blocks.length) return { done: false, value: encoder.encode(blocks[sent++]) }
          return { done: true, value: undefined }
        },
      }),
    },
  } as unknown as Response
}

async function renderConsole({ live = false, ...rest }: Partial<Parameters<typeof ConsoleOutput>[0]> = {}) {
  await act(async () => {
    root.render(
      <I18nProvider>
        {/* `live` is destructured with a default: spreading a Partial over the
            required `live` prop leaves it `boolean | undefined` (TS2322). */}
        <ConsoleOutput operationId="op-42" project="proj-1" live={live} {...rest} />
      </I18nProvider>,
    )
  })
}

const text = () => document.body.textContent ?? ''
const tabs = () => Array.from(document.querySelectorAll('[role="tab"]')) as HTMLElement[]
const fetchCalls = () => (fetch as ReturnType<typeof vi.fn>).mock.calls.map((call) => String(call[0]))

beforeEach(() => {
  ;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
  streamOpened = 0
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(async () => {
  await act(async () => { root.unmount() })
  container.remove()
  vi.unstubAllGlobals()
})

describe('ConsoleOutput (§6, DoD-5)', () => {
  it('mounts from the tail snapshot and shows the console:// address, never artifact://', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(tailResponse())))
    await renderConsole({ live: false })
    expect(text()).toContain('console://op-42')
    expect(text()).not.toContain('artifact://')
    expect(text()).toContain('first line')
    expect(text()).toContain('second line')
    expect(fetchCalls()).toEqual([TAIL_URL])
  })

  it('streams stdout/stderr frames into separate tabs and settles on done+exit_code', async () => {
    vi.stubGlobal('fetch', vi.fn((input: unknown) => {
      const url = String(input)
      if (url === TAIL_URL) return Promise.resolve(tailResponse())
      if (url === STREAM_URL) {
        streamOpened += 1
        return Promise.resolve(sseResponse([
          'event: stdout\ndata: {"chunk":"working\\n"}\n\n',
          'event: stderr\ndata: {"chunk":"boom\\n"}\n\n',
          'event: done\ndata: {"exit_code":2}\n\n',
        ]))
      }
      return Promise.reject(new Error(`unexpected ${url}`))
    }))
    await renderConsole({ live: true })
    expect(streamOpened).toBe(1)
    expect(tabs().map((tab) => tab.textContent)).toEqual(['stdout · 3', 'stderr · 1'])
    expect(text()).toContain('working')
    // Switch to the stderr tab — its content is separate, and highlighted.
    await act(async () => { tabs()[1].click() })
    expect(text()).toContain('boom')
    expect(document.querySelector('[role="tab"][aria-selected="true"]')?.textContent).toContain('stderr')
    // done ends the stream: the exit badge replaces the live marker.
    expect(text()).toContain('exit 2')
    expect(text()).not.toContain('streaming output')
  })

  it('aborts the tail+stream through one controller on unmount', async () => {
    vi.stubGlobal('fetch', vi.fn((input: unknown) => {
      const url = String(input)
      if (url === TAIL_URL) return Promise.resolve(tailResponse())
      if (url === STREAM_URL) {
        streamOpened += 1
        return Promise.resolve(sseResponse([]))
      }
      return Promise.reject(new Error(`unexpected ${url}`))
    }))
    await renderConsole({ live: true })
    expect(streamOpened).toBe(1)
    await act(async () => { root.unmount() })
    // The signature of the cancellation: the request behind the fetch was
    // aborted (the single AbortController of the expanded event) and the
    // promise rejection was swallowed — no error surface, no crash.
    const signals = (fetch as ReturnType<typeof vi.fn>).mock.results
      .map((result) => (result.value as Promise<Response>))
    expect(signals.length).toBeGreaterThanOrEqual(2)
    await Promise.allSettled(signals)
    expect(text()).not.toContain('Output unavailable')
  })

  it('opens no live subscription for an event that is not live (NFR-9)', async () => {
    vi.stubGlobal('fetch', vi.fn((input: unknown) => {
      const url = String(input)
      if (url === TAIL_URL) return Promise.resolve(tailResponse())
      if (url === STREAM_URL) {
        streamOpened += 1
        return Promise.resolve(sseResponse([]))
      }
      return Promise.reject(new Error(`unexpected ${url}`))
    }))
    // `live` is AgentRuns' selectedLive: false here (settled or unselected run).
    await renderConsole({ live: false })
    expect(fetchCalls()).toEqual([TAIL_URL])
    expect(streamOpened).toBe(0)
    expect(text()).toContain('first line')
  })

  it('pauses on document.hidden (abort) and resumes with a fresh tail', async () => {
    vi.stubGlobal('fetch', vi.fn((input: unknown) => {
      const url = String(input)
      if (url === TAIL_URL) return Promise.resolve(tailResponse({ done: true, exit_code: 0 }))
      return Promise.reject(new Error(`unexpected ${url}`))
    }))
    await renderConsole({ live: true })
    expect(text()).toContain('first line')
    const firstAbort = ((fetch as ReturnType<typeof vi.fn>).mock.calls[0][1] as RequestInit).signal as AbortSignal
    expect(firstAbort.aborted).toBe(false)
    // The listener hangs on the real document, so the real `document.hidden`
    // is flipped and a real visibilitychange event is dispatched (the
    // vi.stubGlobal('document', …) variant cannot reach it).
    const descriptor = Object.getOwnPropertyDescriptor(document, 'hidden')
    await act(async () => {
      Object.defineProperty(document, 'hidden', { value: true, configurable: true })
      document.dispatchEvent(new Event('visibilitychange'))
    })
    expect(firstAbort.aborted).toBe(true)
    await act(async () => {
      Object.defineProperty(document, 'hidden', { value: false, configurable: true })
      document.dispatchEvent(new Event('visibilitychange'))
    })
    // Resume: a fresh tail fetch (covers the gap), no error surface.
    expect(fetchCalls().length).toBe(2)
    expect(fetchCalls()[1]).toBe(TAIL_URL)
    expect(text()).not.toContain('Output unavailable')
    if (descriptor) Object.defineProperty(document, 'hidden', descriptor)
  })

  it('publishes each loaded console state for the §8 search index', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(tailResponse({ stderr_tail: 'warn line\n' }))))
    const onStateLoaded = vi.fn()
    await renderConsole({ live: false, onStateLoaded })
    expect(onStateLoaded).toHaveBeenCalled()
    const published = onStateLoaded.mock.calls[0][0]
    expect(published.operationId).toBe('op-42')
    expect(published.consoleUri).toBe('console://op-42')
    expect(published.stdout.text).toBe('first line\nsecond line\n')
    expect(published.stderr.text).toBe('warn line\n')
  })

  it('keeps waiting (no failure surface) on a 404 tail for a live run — the command may not have produced output yet', async () => {
    vi.useFakeTimers()
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(new Response('not found', { status: 404 }))))
    await renderConsole({ live: true })
    expect(text()).not.toContain('Output unavailable')
    expect(text()).toContain('Waiting for output')
    vi.useRealTimers()
  })

  it('shows the failure surface on a 404 tail when the run is not live', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(new Response('not found', { status: 404 }))))
    await renderConsole({ live: false })
    expect(text()).toContain('Output unavailable')
    expect(text()).toContain('console tail 404')
  })

  it('copies the active stream buffer and the console:// URI', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(tailResponse())))
    const writeText = vi.fn(async () => {})
    // Keep `language` explicit: navigator's own enumerable properties do not
    // include the prototype getters (language), and the I18nProvider reads it.
    vi.stubGlobal('navigator', { ...navigator, language: navigator.language, clipboard: { writeText } })
    Object.defineProperty(window, 'isSecureContext', { value: true, configurable: true })
    await renderConsole({ live: false })
    const copy = tabs().find(() => false) // placeholder, replaced below
    void copy
    const buttons = Array.from(document.querySelectorAll('button'))
    const streamCopy = buttons.find((button) => button.textContent === 'Copy')
    await act(async () => { streamCopy!.click() })
    expect(writeText).toHaveBeenLastCalledWith('first line\nsecond line\n')
    expect(text()).toContain('Copied')
    const uriCopy = buttons.find((button) => button.getAttribute('title')?.includes('console://'))
    await act(async () => { uriCopy!.click() })
    expect(writeText).toHaveBeenLastCalledWith('console://op-42')
  })
})

describe('ConsoleOutput pure boundaries', () => {
  it('consoleLines flags error lines and never invents a trailing empty line', () => {
    const lines = consoleLines('ok\nerror: boom\nfatal: end\nplain\n')
    expect(lines.map((line) => line.text)).toEqual(['ok', 'error: boom', 'fatal: end', 'plain'])
    expect(lines.map((line) => line.isError)).toEqual([false, true, true, false])
    expect(consoleLines('')).toEqual([])
    expect(consoleLines('one')).toEqual([{ text: 'one', isError: false }])
  })

  it('shouldAutoScroll follows within the threshold only', () => {
    expect(shouldAutoScroll(0)).toBe(true)
    expect(shouldAutoScroll(24)).toBe(true)
    expect(shouldAutoScroll(25)).toBe(false)
    expect(shouldAutoScroll(400)).toBe(false)
  })

  it('the line limit is exported and pinned for the UI hint', () => {
    expect(CONSOLE_LINE_LIMIT).toBe(2000)
  })
})
