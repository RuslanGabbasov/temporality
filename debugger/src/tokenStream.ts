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
    }
    // Tool args count toward liveness/rate: on a tool-only turn they ARE the
    // streamed output (the answer text stays empty until the next turn).
    const chunk = (frame.text?.length ?? 0) + (frame.reasoning?.length ?? 0) + (frame.args?.length ?? 0)
    if (frame.type === 'snapshot') {
      // The snapshot replaces the accumulated state (reconnect mid-turn).
      this.chars = chunk
      this.lastAt = now
      if (frame.tool) this.tool = frame.tool
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

  metrics(now: number = Date.now()): RunTokenMetrics {
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
    }
  }
}

/** Subscribe to the run's ephemeral token stream while `active` and expose
 * live metrics at ~1Hz. Returns null until the first frame arrives (auth
 * failure, non-streaming run or a pre-first-token thinking phase all land
 * there — the caller falls back to the static model.started row). */
export function useRunTokenMetrics(runId: string, active: boolean): RunTokenMetrics | null {
  const [metrics, setMetrics] = useState<RunTokenMetrics | null>(null)
  const meterRef = useRef(new TokenMeter())

  useEffect(() => {
    meterRef.current = new TokenMeter()
    setMetrics(null)
    if (!active || !runId) return
    const meter = meterRef.current
    const token = localStorage.getItem('temporality_token') ?? ''
    const es = new EventSource(`/kernel-api/v1/workspace/runs/${encodeURIComponent(runId)}/tokens${token ? `?token=${token}` : ''}`)
    const touch = () => setMetrics(meter.metrics())
    const onFrame = (e: MessageEvent) => {
      try {
        meter.apply(JSON.parse(e.data) as TokenStreamFrame)
        touch()
      } catch { /* a malformed frame must not kill the stream */ }
    }
    es.addEventListener('snapshot', onFrame)
    es.addEventListener('delta', onFrame)
    es.addEventListener('done', () => {
      meter.apply({ type: 'done' })
      touch()
      es.close()
    })
    es.onerror = () => es.close()
    // 1Hz refresh keeps "last chunk Ns ago" ticking between deltas.
    const ticker = window.setInterval(touch, 1000)
    return () => {
      window.clearInterval(ticker)
      es.close()
    }
  }, [runId, active])

  return metrics
}
