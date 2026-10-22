import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Button } from '@carbon/react'
import { Copy } from '@carbon/icons-react'
import { useT } from './i18n'
import {
  applyConsoleFrame,
  consoleCopyText,
  consoleDroppedBytes,
  consoleDroppedLines,
  consoleTailShort,
  consoleURI,
  fetchConsoleTail,
  subscribeConsoleStream,
  type ConsoleFrame,
  type ConsoleState,
} from './consoleStream'
import { copyText } from './clipboard'

/** Console panel of one run_command event (v2-consolidated.md §6): separate
 *  stdout/stderr tabs, error highlighting, copy button, canonical
 *  `console://<operation_id>` address, live stream while the command runs.
 *
 *  Budget (NFR-9): exactly one subscription per expanded event — created on
 *  expand, cancelled on collapse/unmount/run switch/hidden tab via a single
 *  AbortController; `document.hidden` pauses (abort + re-fetch on resume).
 *  No redaction is applied to the output (§7: the console is not a private
 *  channel; the `secret.*` invariant is enforced upstream, the client adds
 *  no masking and removes none). */

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
 *  bottom (or press the «to tail» button). Pure decision, unit-tested. */
export function shouldAutoScroll(distanceFromBottomPx: number): boolean {
  return distanceFromBottomPx <= 24
}

interface ConsoleOutputProps {
  operationId: string
  /** The event lives under a run; the console endpoint scopes by project. */
  project: string
  /** True while tool.completed/tool.failed has not landed in the trace. */
  live: boolean
}

export default function ConsoleOutput({ operationId, project, live }: ConsoleOutputProps) {
  const t = useT()
  const [tab, setTab] = useState<ConsoleTab>('stdout')
  const [state, setState] = useState<ConsoleState | null>(null)
  const [error, setError] = useState('')
  const [copied, setCopied] = useState(false)
  /** Manual pause of the auto-scroll: set when the reader scrolls up, until
   *  they return to the bottom or press «to tail». */
  const [followPaused, setFollowPaused] = useState(false)
  const scrollRef = useRef<HTMLDivElement | null>(null)
  /** NFR-9: one abort controller owns the tail fetch AND the stream —
   *  collapse/unmount/hidden-tab all cancel through it. */
  const abortRef = useRef<AbortController | null>(null)

  const stop = useCallback(() => {
    abortRef.current?.abort()
    abortRef.current = null
  }, [])

  const applyFrame = useCallback((frame: ConsoleFrame) => {
    setState((prev) => applyConsoleFrame(prev, frame))
  }, [])

  // Visibility pause state (declared before the subscription effect: it is
  // one of that effect's deps — tsc TSB2448/2454 on a use-before-declaration
  // flagged by `tsc -b`; behavior unchanged, the hook order is identical).
  const [visibleTick, setVisibleTick] = useState(0)

  // One subscription per expanded event: the tail snapshot + the live SSE
  // stream share a single AbortController. Collapsing the event aborts both;
  // re-expanding re-subscribes (a fresh tail request also covers anything the
  // previous subscription missed while the reader was away).
  useEffect(() => {
    if (!operationId) return
    // NFR-6 spirit at event granularity: a hidden document pauses the live
    // console too — abort now, re-subscribe (fresh tail) when visible again.
    if (document.hidden) return
    stop()
    const controller = new AbortController()
    abortRef.current = controller
    setError('')
    fetchConsoleTail(operationId, project, controller.signal)
      .then((tail) => {
        if (controller.signal.aborted) return
        setState(tail)
        // A finished operation needs no live frames — the tail is the answer.
        if (tail.done) return
        subscribeConsoleStream(operationId, project, applyFrame, controller.signal, { tail: true })
      })
      .catch((failure) => {
        if (controller.signal.aborted) return
        setError(failure instanceof Error ? failure.message : String(failure))
      })
    return stop
  }, [operationId, project, applyFrame, stop, visibleTick])

  // Visibility pause: on hide, abort the network subscription; on return,
  // bump visibleTick (a subscription-effect dep) — the effect above aborts
  // nothing stale (already aborted), re-fetches a fresh tail and re-subscribes.
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

  // The scroll effect re-runs when new text arrives or the tab changes; it
  // also re-anchors after the tail snapshot lands (visibleTick bumps then).
  const tailText = tab === 'stderr' ? state?.stderr.text : state?.stdout.text

  useEffect(() => {
    if (followPaused) return
    const node = scrollRef.current
    if (!node) return
    node.scrollTop = node.scrollHeight
  }, [tailText, tab, followPaused, visibleTick])

  const onScroll = useCallback(() => {
    const node = scrollRef.current
    if (!node) return
    setFollowPaused(!shouldAutoScroll(node.scrollHeight - node.scrollTop - node.clientHeight))
  }, [])

  const toTail = useCallback(() => {
    setFollowPaused(false)
    const node = scrollRef.current
    if (node) node.scrollTop = node.scrollHeight
  }, [])

  const buffer = tab === 'stderr' ? state?.stderr : state?.stdout
  const lines = useMemo(() => consoleLines(buffer?.text ?? ''), [buffer?.text])
  const dropped = state ? { lines: consoleDroppedLines(state), bytes: consoleDroppedBytes(state) } : { lines: 0, bytes: 0 }
  const showTailShort = state ? consoleTailShort(state) : false
  const streamLive = live && !state?.done

  async function copy() {
    if (!state) return
    const ok = await copyText(consoleCopyText(state, tab))
    setCopied(ok)
    if (ok) window.setTimeout(() => setCopied(false), 1500)
  }

  if (!operationId) return null

  return (
    <div style={{ marginTop: '0.35rem', border: '1px solid var(--tm-border)', borderRadius: '4px', background: 'var(--tm-elevated)', minWidth: 0 }}>
      {/* Tab strip: stdout / stderr are separate streams (§6) — separate tabs
          instead of a merged, prefixed feed. */}
      <div role="tablist" aria-label={t('runs.console.tabs')} style={{ display: 'flex', alignItems: 'center', gap: 0, borderBottom: '1px solid var(--tm-border)', flexWrap: 'wrap' }}>
        {(['stdout', 'stderr'] as ConsoleTab[]).map((key) => {
          const active = tab === key
          const streamBuffer = key === 'stderr' ? state?.stderr : state?.stdout
          return (
            <button
              key={key}
              role="tab"
              aria-selected={active}
              onClick={() => setTab(key)}
              style={{
                padding: '0.3rem 0.7rem',
                background: active ? 'var(--tm-raised)' : 'transparent',
                border: 'none',
                borderBottom: active ? '2px solid var(--tm-teal)' : '2px solid transparent',
                color: key === 'stderr' ? '#f7768e' : active ? 'var(--tm-teal)' : 'var(--tm-text-3)',
                cursor: 'pointer',
                fontSize: '0.72rem',
                fontFamily: '"SFMono-Regular", Consolas, monospace',
                fontWeight: active ? 600 : 400,
              }}
            >
              {key}
              {streamBuffer && streamBuffer.text ? ` · ${consoleLines(streamBuffer.text).length}` : ''}
            </button>
          )
        })}
        <span style={{ flex: 1 }} />
        {streamLive && (
          <span className="live-dot" title={t('runs.console.live')} style={{ color: 'var(--tm-teal)', fontSize: '0.7rem', padding: '0 0.4rem', flexShrink: 0 }}>● {t('runs.console.live')}</span>
        )}
        {state?.done && (
          <span style={{ color: state.exitCode ? '#f7768e' : 'var(--tm-text-3)', fontSize: '0.7rem', padding: '0 0.4rem', flexShrink: 0 }}>
            {t('runs.console.exited', { code: String(state.exitCode ?? 0) })}
          </span>
        )}
        <Button
          size="sm"
          kind="ghost"
          renderIcon={Copy}
          onClick={() => void copy()}
          title={t('runs.console.copy')}
          style={{ fontSize: '0.72rem', flexShrink: 0 }}
        >
          {copied ? t('runs.console.copied') : t('runs.console.copy')}
        </Button>
      </div>

      {/* Canonical stream address (DoD-5): console://<operation_id> — shown
          and copyable; artifact:// never addresses console output. */}
      <div style={{ display: 'flex', alignItems: 'center', gap: '0.4rem', padding: '0.25rem 0.5rem', fontSize: '0.65rem', color: 'var(--tm-muted)', fontFamily: '"SFMono-Regular", Consolas, monospace', flexWrap: 'wrap' }}>
        <span style={{ overflowWrap: 'anywhere' }}>{consoleURI(operationId)}</span>
        <button
          onClick={() => void copyText(consoleURI(operationId))}
          title={t('runs.console.copy_uri')}
          style={{ background: 'none', border: '1px solid var(--tm-border)', color: 'var(--tm-text-3)', cursor: 'pointer', fontSize: '0.62rem', padding: '0 0.3rem', borderRadius: '3px', lineHeight: 1.4, flexShrink: 0 }}
        >⧉</button>
        {showTailShort && (
          <span title={t('runs.console.tail_short_hint')} style={{ color: 'var(--tm-amber)', marginLeft: 'auto' }}>
            {dropped.lines > 0
              ? t('runs.console.tail_short', { lines: String(dropped.lines), kb: String(Math.round(dropped.bytes / 1024)) })
              : t('runs.console.tail_short_server')}
          </span>
        )}
      </div>

      {error && (
        <div style={{ padding: '0.4rem 0.5rem', fontSize: '0.72rem', color: '#f7768e' }}>
          {t('runs.console.unavailable')}: {error}
        </div>
      )}

      {/* Output: the active stream's tail, error lines highlighted. A manual
          scroll up pauses the auto-follow; this button returns to the tail.
          The subscription state (visibleTick) gates a scroll reset after the
          tail snapshot lands. */}
      <div
        ref={scrollRef}
        onScroll={onScroll}
        role="log"
        aria-live={streamLive ? 'polite' : 'off'}
        style={{ maxHeight: '220px', overflowY: 'auto', padding: '0.4rem 0.5rem', fontSize: '0.72rem', lineHeight: 1.45, fontFamily: '"SFMono-Regular", Consolas, monospace', whiteSpace: 'pre-wrap', overflowWrap: 'anywhere', minWidth: 0 }}
      >
        {lines.length === 0
          ? <span style={{ color: 'var(--tm-text-3)' }}>{streamLive ? t('runs.console.waiting') : t('runs.console.empty')}</span>
          : lines.map((line, index) => (
            <div key={index} style={line.isError ? { color: '#f7768e', background: 'rgba(247,118,142,0.08)' } : undefined}>{line.text || '\u00a0'}</div>
          ))}
      </div>
      {followPaused && (
        <button
          onClick={toTail}
          style={{ width: '100%', background: 'var(--tm-raised)', border: 'none', borderTop: '1px solid var(--tm-border)', color: 'var(--tm-text-2)', cursor: 'pointer', fontSize: '0.7rem', padding: '0.25rem' }}
        >
          ↓ {t('runs.console.to_tail')}
        </button>
      )}
      <div style={{ padding: '0.2rem 0.5rem 0.3rem', fontSize: '0.62rem', color: 'var(--tm-muted)' }}>
        {t('runs.console.search_hint')}
      </div>
    </div>
  )
}
