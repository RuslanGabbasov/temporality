/** Journal run-stream SSE reader. The kernel's /runs/{id}/stream endpoint
 * emits one NAMED event per journal row (`event: <type>`), so the browser
 * EventSource is a poor fit — it would need an addEventListener per event
 * type and silently drop any type added later. Parsing the stream directly
 * keeps the live trace agnostic to new event types. */

export interface JournalFrame {
  event: string
  data: string
}

/** Parse one blank-line-terminated SSE block into a frame. */
export function parseSSEBlock(block: string): JournalFrame | null {
  let event = ''
  const dataLines: string[] = []
  for (const line of block.split('\n')) {
    if (line.startsWith('event:')) event = line.slice(6).trim()
    else if (line.startsWith('data:')) dataLines.push(line.slice(5).replace(/^ /, ''))
  }
  if (dataLines.length === 0) return null
  return { event, data: dataLines.join('\n') }
}

/** Subscribe to a run's journal stream; onEvent fires once per frame. The
 * fetch resolves and the subscription ends when the server closes the stream
 * (run reached a terminal event) or the signal aborts. With tail=true the
 * server skips the journal backlog and sends only the recent past plus live
 * events — the right mode when the caller already has the history (REST
 * snapshot) and dedupes by event_id. A network drop or an unreachable
 * endpoint reconnects with capped backoff while the signal lives: a kernel
 * restart must not downgrade the trace to polling for the page's lifetime.
 * Errors that persist still degrade to the caller's polling fallback. */
export function subscribeRunJournal(runId: string, onEvent: (frame: JournalFrame) => void, signal: AbortSignal, opts?: { tail?: boolean }): void {
  const token = localStorage.getItem('temporality_token') ?? ''
  let backoff = 1_000
  const connect = () => {
    if (signal.aborted) return
    const params = new URLSearchParams()
    if (token) params.set('token', token)
    if (opts?.tail) params.set('tail', '1')
    const qs = params.toString()
    const url = `/kernel-api/v1/workspace/runs/${encodeURIComponent(runId)}/stream${qs ? `?${qs}` : ''}`
    void fetch(url, { signal, headers: { Accept: 'text/event-stream' } })
      .then(async (response) => {
        if (!response.ok || !response.body) {
          schedule()
          return
        }
        const reader = response.body.getReader()
        const decoder = new TextDecoder()
        let buffer = ''
        backoff = 1_000 // a successful open resets the ladder
        for (;;) {
          const { done, value } = await reader.read()
          if (done) break
          buffer += decoder.decode(value, { stream: true })
          // Frames are separated by a blank line; the trailing partial stays
          // buffered until the next chunk completes it.
          const blocks = buffer.split('\n\n')
          buffer = blocks.pop() ?? ''
          for (const block of blocks) {
            const frame = parseSSEBlock(block)
            if (frame) onEvent(frame)
          }
        }
        // The server closed a healthy stream: the run reached a terminal
        // event — this subscription is complete, no reconnect.
      })
      .catch(() => {
        // aborted (caller moved on) or the transport dropped mid-stream —
        // reconnect unless the caller aborted.
        schedule()
      })
  }
  const schedule = () => {
    if (signal.aborted) return
    const delay = backoff
    backoff = Math.min(backoff * 2, 30_000)
    const timer = window.setTimeout(connect, delay)
    signal.addEventListener('abort', () => window.clearTimeout(timer), { once: true })
  }
  connect()
}
