import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useT } from './i18n'
import { applyConsoleFrame, consoleURI, fetchConsoleTail, subscribeConsoleStream, type ConsoleFrame, type ConsoleState } from './consoleStream'
import { copyText } from './clipboard'
import { observationApi } from './observationApi'

/** Journal fallback for a swept/in-memory-lost console: the tool.completed
 *  event of this operation carries up to 1KB of the output — recovered and
 *  shown with a "saved copy" marker instead of a dead console. The event
 *  payload is a JSON string, so what arrived may still carry escape
 *  sequences (\n, \") — unescape once for human reading. */
async function recoverSavedOutput(runId: string, operationId: string, project: string, signal: AbortSignal): Promise<string | null> {
  const page = await observationApi.events(project, undefined, undefined, undefined, { run: runId, type: 'tool.completed', limit: 500 })
  if (signal.aborted) return null
  const match = page.events.find((e) => e.data?.operation_id === operationId)
  const output = match?.data?.output
  if (typeof output !== 'string' || !output.trim()) return null
  return unescapeOnce(output)
}

/** One round of JSON-string unescaping: literal \n, \t, \", \\ become real
 *  characters. Guarded — malformed input returns unchanged. */
function unescapeOnce(text: string): string {
  try {
    const parsed = JSON.parse(`"${text.replace(/^"|"$/g, '').replace(/"/g, '\\"')}"`)
    return typeof parsed === 'string' ? parsed : text
  } catch {
    return text
  }
}

/** Console panel of one run_command event, Zed-terminal style: ONE merged
 *  output block (stderr tinted), a one-line status header, a copy icon. No
 *  tabs, no address rows, no stacked hints — the output is the interface.
 *
 *  Budget (NFR-9): exactly one subscription per expanded event — created on
 *  expand, cancelled on collapse/unmount/run switch/hidden tab via a single
 *  AbortController; `document.hidden` pauses (abort + re-fetch on resume). */

export interface ConsoleOutputProps {
  operationId: string
  /** The event lives under a run; the console endpoint scopes by project. */
  project: string
  /** The run this operation belongs to: when the in-memory console is gone
   *  (sweep, kernel restart), the saved tool.completed output is recovered
   *  from the journal instead — that copy is capped at 1KB but always there. */
  runId?: string
  /** True while tool.completed/tool.failed has not landed in the trace —
   *  i.e. the run is selected AND still live. Gates the SSE subscription:
   *  a console opened on a run that is no longer live holds the tail fetch
   *  but no stream (NFR-9: one subscription per expanded live event). */
  live: boolean
  /** §8/NFR-9: publish each settled console state to the owner (AgentRuns)
   *  so the trace text search covers exactly the loaded buffers — the
   *  search itself never fetches. */
  onStateLoaded?: (state: ConsoleState) => void
}

export type ConsoleTab = 'stdout' | 'stderr'

const errorLinePattern = /(^|\s)(error|fatal|exception|panic|traceback|ошибка|фатально|сбой)(:|\b)/i

/** One console line with its error flag. Unit-tested boundary: the pattern
 *  and the trim policy stay pinned even if the renderer changes. */
export interface ConsoleLine {
  text: string
  isError: boolean
}

/** Split a buffer into highlighted lines. Pure, exported for unit tests. */
export function consoleLines(buffer: string): ConsoleLine[] {
  if (!buffer) return []
  const lines = buffer.split('\n')
  // A trailing newline produces an empty last element — not a line.
  if (lines.length > 1 && lines[lines.length - 1] === '') lines.pop()
  return lines.map((text) => ({ text, isError: errorLinePattern.test(text) }))
}

/** Auto-scroll policy: follow the tail while the reader has not scrolled
 *  away; any manual upward scroll pauses following until they return to the
 *  bottom. Pure decision, unit-tested. */
export function shouldAutoScroll(distanceFromBottomPx: number): boolean {
  return distanceFromBottomPx <= 24
}

export default function ConsoleOutput({ operationId, project, live, onStateLoaded, runId }: ConsoleOutputProps) {
  const t = useT()
  const [state, setState] = useState<ConsoleState | null>(null)
  const [savedOutput, setSavedOutput] = useState<string | null>(null)
  const [copied, setCopied] = useState(false)
  const [followPaused, setFollowPaused] = useState(false)
  const scrollRef = useRef<HTMLDivElement | null>(null)
  const abortRef = useRef<AbortController | null>(null)
  const retryRef = useRef<number | null>(null)

  const stop = useCallback(() => {
    abortRef.current?.abort()
    abortRef.current = null
    if (retryRef.current !== null) {
      window.clearTimeout(retryRef.current)
      retryRef.current = null
    }
  }, [])

  const applyFrame = useCallback((frame: ConsoleFrame) => {
    setState((prev) => applyConsoleFrame(prev, frame))
  }, [])

  useEffect(() => {
    if (!state || !onStateLoaded) return
    onStateLoaded(state)
  }, [state, onStateLoaded])

  const [visibleTick, setVisibleTick] = useState(0)

  useEffect(() => {
    if (!operationId) return
    if (document.hidden) return
    stop()
    const controller = new AbortController()
    abortRef.current = controller
    fetchConsoleTail(operationId, project, controller.signal)
      .then((tail) => {
        if (controller.signal.aborted) return
        setState(tail)
        if (!live || tail.done) return
        subscribeConsoleStream(operationId, project, applyFrame, controller.signal, { tail: true })
      })
      .catch((failure) => {
        if (controller.signal.aborted) return
        const failureMessage = failure instanceof Error ? failure.message : String(failure)
        // A 404 tail on a LIVE run is expected before the first output chunk
        // lands (and right after a kernel restart wiped the buffers): retry
        // shortly instead of declaring the console dead.
        if (live && failureMessage.includes('404')) {
          retryRef.current = window.setTimeout(() => setVisibleTick((value) => value + 1), 2000)
          return
        }
        if (runId && !document.hidden) {
          void recoverSavedOutput(runId, operationId, project, controller.signal)
            .then((saved) => {
              if (controller.signal.aborted || !saved) return
              setSavedOutput(saved)
            })
            .catch(() => { /* no journal copy either — show the quiet empty state */ })
        }
      })
    return stop
  }, [operationId, project, live, applyFrame, stop, visibleTick])

  useEffect(() => {
    const onChange = () => {
      if (document.hidden) {
        stop()
      } else {
        setVisibleTick((tick) => tick + 1)
      }
    }
    document.addEventListener('visibilitychange', onChange)
    return () => document.removeEventListener('visibilitychange', onChange)
  }, [stop])

  // ONE merged feed: stdout and stderr interleave by arrival; stderr lines
  // carry the error tint. The tabs split what a terminal shows together —
  // reading a command output through a tab switch was the old mess.
  const merged = useMemo(() => {
    const out: Array<{ text: string; stream: 'stdout' | 'stderr' }> = []
    const push = (text: string | undefined, stream: 'stdout' | 'stderr') => {
      if (!text) return
      for (const line of text.split('\n')) out.push({ text: line, stream })
    }
    push(state?.stdout.text, 'stdout')
    push(state?.stderr.text, 'stderr')
    if (out.length > 0 && out[out.length - 1].text === '') out.pop()
    return out
  }, [state?.stdout.text, state?.stderr.text])

  const shown = savedOutput ?? merged.map((line) => line.text).join('\n')
  const streamLive = live && !state?.done && !savedOutput
  const hasContent = shown.length > 0
  const copyAll = useCallback(() => {
    void copyText(shown)
    setCopied(true)
    window.setTimeout(() => setCopied(false), 1500)
  }, [shown])

  if (!operationId) return null

  return (
    <div style={{ marginTop: '0.35rem', border: '1px solid var(--tm-border)', borderRadius: '4px', background: '#101418', minWidth: 0, overflow: 'hidden' }}>
      {/* One-line status header: live dot / exit code, saved-copy marker,
          copy action. Everything else was noise. */}
      <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', padding: '0.25rem 0.5rem', borderBottom: '1px solid rgba(255,255,255,0.06)' }}>
        {streamLive ? (
          <span style={{ color: '#7dcfff', fontSize: '0.68rem', flexShrink: 0 }}>⌁ {t('runs.console.live') ?? 'live'}</span>
        ) : state?.done ? (
          <span style={{ color: state.exitCode ? '#f7768e' : '#9ece6a', fontSize: '0.68rem', flexShrink: 0 }}>{t('runs.console.exited', { code: String(state.exitCode ?? 0) })}</span>
        ) : savedOutput ? (
          <span style={{ color: '#a9b1d6', fontSize: '0.68rem', flexShrink: 0 }}>{t('runs.console.saved_copy') ?? 'saved copy'}</span>
        ) : (
          <span style={{ color: '#565f89', fontSize: '0.68rem', flexShrink: 0 }}>console://{consoleURI(operationId).replace('console://', '')}</span>
        )}
        <span style={{ flex: 1 }} />
        {hasContent && (
          <button
            onClick={copyAll}
            title={t('runs.console.copy') ?? 'Copy'}
            style={{ background: 'none', border: 'none', color: copied ? '#9ece6a' : '#565f89', cursor: 'pointer', fontSize: '0.68rem', padding: '0 0.2rem', flexShrink: 0 }}
          >{copied ? '✓' : '⧉'}</button>
        )}
      </div>
      <div
        ref={scrollRef}
        onScroll={() => {
          const node = scrollRef.current
          if (!node) return
          setFollowPaused(!shouldAutoScroll(node.scrollHeight - node.scrollTop - node.clientHeight))
        }}
        role="log"
        aria-live={streamLive ? 'polite' : 'off'}
        style={{ maxHeight: '12rem', overflowY: 'auto', padding: '0.45rem 0.6rem', fontSize: '0.72rem', lineHeight: 1.5, fontFamily: '"SFMono-Regular", Consolas, monospace', whiteSpace: 'pre-wrap', overflowWrap: 'anywhere', minWidth: 0, color: '#c0caf5' }}
      >
        {!hasContent
          ? <span style={{ color: '#565f89' }}>{streamLive ? (t('runs.console.waiting') ?? 'Waiting for output…') : (t('runs.console.empty') ?? '(no output)')}</span>
          : (savedOutput
            ? shown
            : merged.map((line, index) => (
              <div key={index} style={line.stream === 'stderr' ? { color: '#f7768e' } : undefined}>{line.text || '\u00a0'}</div>
            )))}
      </div>
      {followPaused && (
        <button
          onClick={() => {
            setFollowPaused(false)
            const node = scrollRef.current
            if (node) node.scrollTop = node.scrollHeight
          }}
          style={{ width: '100%', background: 'rgba(255,255,255,0.04)', border: 'none', borderTop: '1px solid rgba(255,255,255,0.06)', color: '#a9b1d6', cursor: 'pointer', fontSize: '0.7rem', padding: '0.25rem' }}
        >
          ↓ {t('runs.console.to_tail') ?? 'to tail'}
        </button>
      )}
    </div>
  )
}
