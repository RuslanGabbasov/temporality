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
 * (run reached a terminal event) or the signal aborts. Errors are swallowed:
 * the polling fallback in the caller stays the safety net. */
export function subscribeRunJournal(runId: string, onEvent: (frame: JournalFrame) => void, signal: AbortSignal): void {
  const token = localStorage.getItem('temporality_token') ?? ''
  const url = `/kernel-api/v1/workspace/runs/${encodeURIComponent(runId)}/stream${token ? `?token=${token}` : ''}`
  void fetch(url, { signal, headers: { Accept: 'text/event-stream' } })
    .then(async (response) => {
      if (!response.ok || !response.body) return
      const reader = response.body.getReader()
      const decoder = new TextDecoder()
      let buffer = ''
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
    })
    .catch(() => { /* aborted or network gone — polling covers the gap */ })
}
