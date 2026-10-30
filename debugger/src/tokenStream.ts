import { useEffect, useRef, useState } from 'react'

/** Live-progress metrics for an in-flight model call. Tokens are not durable
 * facts, so these are estimates from the ephemeral SSE stream (the same
 * /tokens endpoint the chat previews use) — never from the journal. */
export interface RunTokenMetrics {
  /** Approximate tokens streamed this turn so far (chars / 4). */
  approxTokens: number
  /** Rolling tokens-per-second over the last ~5s of deltas; 0 when idle. */
  tps: number
  /** ms since the last received chunk; Infinity before the first one. */
  lastChunkAgoMs: number
  /** True between the first received chunk and the done event. */
  streaming: boolean
  /** Tool name when the model is currently streaming a tool call (no answer
   * text this turn — the args are the output). Feeds the live “⚙ tool” hint. */
  tool?: string
  /** Tail (last ~200 chars) of the streamed tool arguments — the pending-tool
   * preview in the running turn shows what command is being formed. */
  argsTail?: string
  /** False while the EventSource has not opened (or died): without it the UI
   * cannot tell a thinking model from a dead token channel, and a broken
   * transport reads as "waiting for the first token" forever. */
  connected: boolean
  /** How many times the channel reconnected (or is about to): a non-zero
   * count with connected=false means the view is mid-backoff, not dead. */
  reconnects: number
}

/** Sliding window for the rate estimate: long enough to smooth out bursty
 * providers, short enough to notice a stall within seconds. */
const RATE_WINDOW_MS = 5000
/** Chars per token: rough cross-language middle ground (en ~4, ru ~3). Only
 * feeds a "~N tok" estimate labelled as approximate in the UI. */
const CHARS_PER_TOKEN = 4

export interface TokenStreamFrame {
  type?: string
  turn?: number
  text?: string
  reasoning?: string
  /** Live tool-call streaming: tool name + argument fragment. Most agent
   * turns produce no text — args are the actual streamed output. */
  tool?: string
  args?: string
}

/** Pure accumulator over token-stream frames — kept free of EventSource so the
 * turn-reset / rate-window logic is unit-testable. */
export class TokenMeter {
  private turn = -1
  private chars = 0
  private window: { t: number; chars: number }[] = []
  private firstAt = 0
  private lastAt = 0
  private live = false
  private tool = ''
  /** Tail of the streamed tool arguments — feeds the pending-tool preview in
   *  the running turn (the human sees what command is being formed). */
  private argsTail = ''

  apply(frame: TokenStreamFrame, now: number = Date.now()) {
    if (frame.type === 'done') {
      this.live = false
      return
    }
    const turn = Number(frame.turn ?? 0)
    if (turn !== this.turn) {
      // A turn change resets the accumulated text (one live segment per model
      // call), so the meter must reset with it.
      this.turn = turn
      this.chars = 0
      this.window = []
      this.firstAt = 0
      this.tool = ''
      this.argsTail = ''
    }
    // Tool args count toward liveness/rate: on a tool-only turn they ARE the
    // streamed output (the answer text stays empty until the next turn).
    const chunk = (frame.text?.length ?? 0) + (frame.reasoning?.length ?? 0) + (frame.args?.length ?? 0)
    if (frame.args) {
      // Keep only the tail: the preview shows what is being typed right now,
      // not the whole payload.
      this.argsTail = (this.argsTail + frame.args).slice(-200)
    }
    if (frame.type === 'snapshot') {
      // The snapshot replaces the accumulated state (reconnect mid-turn).
      this.chars = chunk
      this.lastAt = now
      if (frame.tool) this.tool = frame.tool
      if (frame.args) this.argsTail = frame.args.slice(-200)
      if (chunk > 0) {
        this.live = true
        this.firstAt = now
      }
      return
    }
    if (chunk > 0) {
      this.chars += chunk
      this.window.push({ t: now, chars: chunk })
      this.lastAt = now
      this.live = true
      if (!this.firstAt) this.firstAt = now
    }
    // The tool name arrives on the first args chunk and persists for the turn;
    // a later text chunk clears it (the model moved on to the answer).
    if (frame.tool) this.tool = frame.tool
    else if (frame.text) this.tool = ''
  }

  metrics(now: number = Date.now()): Omit<RunTokenMetrics, 'connected' | 'reconnects'> {
    while (this.window.length > 0 && now - this.window[0].t > RATE_WINDOW_MS) this.window.shift()
    const spanS = (now - (this.window[0]?.t ?? now)) / 1000
    const windowChars = this.window.reduce((sum, sample) => sum + sample.chars, 0)
    // A sub-second span of one chunk says nothing about the rate yet.
    const tps = spanS >= 0.5 ? windowChars / CHARS_PER_TOKEN / spanS : 0
    return {
      approxTokens: Math.round(this.chars / CHARS_PER_TOKEN),
      tps: Math.round(tps * 10) / 10,
      lastChunkAgoMs: this.lastAt ? now - this.lastAt : Number.POSITIVE_INFINITY,
      streaming: this.live,
      tool: this.tool || undefined,
      argsTail: this.tool ? this.argsTail : undefined,
    }
  }
}

/** Subscribe to the run's ephemeral token stream while `active` and expose
 * live metrics at ~1Hz. Reconnects with capped backoff — a kernel restart or
 * a proxy hiccup must not kill the live view for the page's lifetime (the
 * server replays the accumulated text as a snapshot on re-subscribe).
 * Returns null while the subscription has not settled; `connected` tells a
 * live channel from one that never opened or is mid-backoff. */
export function useRunTokenMetrics(runId: string, active: boolean): RunTokenMetrics | null {
  const [metrics, setMetrics] = useState<RunTokenMetrics | null>(null)
  const meterRef = useRef(new TokenMeter())
  const connectedRef = useRef(false)
  const reconnectsRef = useRef(0)

  useEffect(() => {
    meterRef.current = new TokenMeter()
    connectedRef.current = false
    reconnectsRef.current = 0
    setMetrics(null)
    if (!active || !runId) return
    const meter = meterRef.current
    const token = localStorage.getItem('temporality_token') ?? ''
    let disposed = false
    let es: EventSource | null = null
    let retryTimer: number | undefined
    let openWatchdog: number | undefined
    let backoff = 1_000

    const touch = () => setMetrics({ ...meter.metrics(), connected: connectedRef.current, reconnects: reconnectsRef.current })
    const onFrame = (e: MessageEvent) => {
      try {
        meter.apply(JSON.parse(e.data) as TokenStreamFrame)
        touch()
      } catch { /* a malformed frame must not kill the stream */ }
    }
    const teardown = () => {
      if (openWatchdog) window.clearTimeout(openWatchdog)
      openWatchdog = undefined
      es?.close()
      es = null
    }
    const connect = () => {
      if (disposed) return
      es = new EventSource(`/kernel-api/v1/workspace/runs/${encodeURIComponent(runId)}/tokens${token ? `?token=${token}` : ''}`)
      // A pending EventSource fires NEITHER open nor error when the browser is
      // queuing the request (HTTP/1.1 caps ~6 connections per origin; a few
      // open SSE tabs eat that) — without a watchdog the channel sits silent
      // forever showing «канал не подключился». If the socket has not opened
      // within 5s, tear it down and re-enter the backoff loop: the retry
      // either catches a freed slot or keeps the label honest.
      if (openWatchdog) window.clearTimeout(openWatchdog)
      openWatchdog = window.setTimeout(() => {
        if (disposed || connectedRef.current) return
        teardown()
        schedule()
      }, 5000)
      es.addEventListener('snapshot', onFrame)
      es.addEventListener('delta', onFrame)
      es.addEventListener('done', () => {
        meter.apply({ type: 'done' })
        touch()
        teardown()
        schedule()
      })
      // The kernel flushes an SSE comment right after the headers, so 'open'
      // fires within one round-trip — before it, the channel is blind and the
      // caller must not read the silence as a thinking model.
      es.onopen = () => {
        if (openWatchdog) window.clearTimeout(openWatchdog)
        openWatchdog = undefined
        backoff = 1_000
        connectedRef.current = true
        touch()
      }
      es.onerror = () => {
        // The browser cannot tell a transient drop from a dead endpoint, and
        // its own retry only works while the connection is kept — close and
        // schedule a capped manual backoff instead.
        connectedRef.current = false
        touch()
        teardown()
        schedule()
      }
    }
    const schedule = () => {
      if (disposed) return
      reconnectsRef.current += 1
      touch()
      retryTimer = window.setTimeout(connect, backoff)
      backoff = Math.min(backoff * 2, 30_000)
    }
    connect()
    // 1Hz refresh keeps "last chunk Ns ago" ticking between deltas.
    const ticker = window.setInterval(touch, 1000)
    return () => {
      disposed = true
      if (retryTimer) window.clearTimeout(retryTimer)
      window.clearInterval(ticker)
      teardown()
    }
  }, [runId, active])

  return metrics
}
