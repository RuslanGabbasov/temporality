import { authHeaders } from './api'
import { isAbortError } from './runsLoad'
import { parseSSEBlock } from './journalStream'

/** Client contract of the run_command console (v2-consolidated.md §6, §11.2):
 *  `GET /v1/agent/console/{operationID}` returns the tail snapshot, the
 *  `/stream` endpoint appends live SSE frames (`event: stdout|stderr|done`,
 *  `data: {"chunk": …}`). The canonical stream address is
 *  `console://<operation_id>` — the approved final scheme; `artifact://`
 *  belongs to the artifact registry and must never address console output
 *  (DoD-5). The output itself is ephemeral: no redaction is applied on this
 *  stream (secrets live outside sandboxes, §7), and the only invariant —
 *  `secret.*` values are never displayed — is enforced upstream. */

/** Client tail limits (§11.2 accepted default): keep at most 2000 lines and
 *  256 KiB per stream; older lines are dropped and counted for the
 *  «tail shown» marker. */
export const CONSOLE_LINE_LIMIT = 2000
export const CONSOLE_BYTE_LIMIT = 256 * 1024

/** Canonical console URI for an operation id. Single construction point so
 *  the `console://` (never `artifact://`) guarantee is testable. */
export function consoleURI(operationId: string): string {
  return `console://${operationId}`
}

/** Per-stream tail buffer: text plus counters for the truncation marker.
 *  `droppedLines`/`droppedBytes` count what THIS client dropped (the server
 *  tail is capped in its own 256 KiB budget and only reports a boolean). */
export interface TailBuffer {
  text: string
  bytes: number
  lines: number
  droppedLines: number
  droppedBytes: number
}

export function emptyTailBuffer(): TailBuffer {
  return { text: '', bytes: 0, lines: 0, droppedLines: 0, droppedBytes: 0 }
}

const utf8 = new TextEncoder()
function utf8Length(text: string): number {
  return utf8.encode(text).length
}
function countNewlines(text: string): number {
  let n = 0
  for (let i = text.indexOf('\n'); i !== -1; i = text.indexOf('\n', i + 1)) n++
  return n
}

/** Append one chunk and enforce both tail limits, dropping from the OLDEST
 *  end (the console shows the tail, not the history). Line drops take whole
 *  lines (up to and including the newline); the byte limit may split the
 *  oldest partial line — the split remainder still renders, so only the
 *  removed bytes are counted. Pure: returns a new buffer. */
export function appendTail(buffer: TailBuffer, chunk: string): TailBuffer {
  if (!chunk) return buffer
  let text = buffer.text + chunk
  let bytes = buffer.bytes + utf8Length(chunk)
  let droppedLines = buffer.droppedLines
  let droppedBytes = buffer.droppedBytes
  // Line limit: drop the (newlines − limit) oldest whole lines in one pass —
  // walk to the newline that closes the kept window, then cut once.
  const newlines = countNewlines(text)
  if (newlines > CONSOLE_LINE_LIMIT) {
    const dropLines = newlines - CONSOLE_LINE_LIMIT
    let seen = 0
    let cut = -1
    for (let i = text.indexOf('\n'); i !== -1; i = text.indexOf('\n', i + 1)) {
      seen++
      if (seen === dropLines) { cut = i + 1; break }
    }
    if (cut > 0) {
      const dropped = text.slice(0, cut)
      droppedBytes += utf8Length(dropped)
      droppedLines += dropLines
      text = text.slice(cut)
      bytes -= utf8Length(dropped)
    }
  }
  // Byte limit: cut whole characters from the front until at least the
  // overflow is freed — a multi-byte boundary is never split and the invariant
  // `bytes <= CONSOLE_BYTE_LIMIT` holds after a single pass.
  if (bytes > CONSOLE_BYTE_LIMIT) {
    const overflow = bytes - CONSOLE_BYTE_LIMIT
    let cutChars = 0
    let cutBytes = 0
    for (const ch of text) {
      cutBytes += utf8Length(ch)
      cutChars += ch.length
      if (cutBytes >= overflow) break
    }
    droppedBytes += cutBytes
    text = text.slice(cutChars)
    bytes -= cutBytes
  }
  return {
    text,
    bytes,
    lines: countNewlines(text) + (text.length > 0 && !text.endsWith('\n') ? 1 : 0),
    droppedLines,
    droppedBytes,
  }
}

/** One console SSE frame: `stdout`/`stderr` carry `{"chunk": …}`, `done`
 *  carries `{"exit_code": …}`. */
export interface ConsoleFrame {
  event: string
  data: string
}

/** Full client state of one operation's console. `consoleUri` is the
 *  canonical `console://` address shown and copied in the UI. */
export interface ConsoleState {
  operationId: string
  consoleUri: string
  stdout: TailBuffer
  stderr: TailBuffer
  /** The server capped its own buffer — exact dropped volume unknown. */
  truncated: boolean
  done: boolean
  exitCode: number | null
}

export function emptyConsoleState(operationId: string, consoleUri: string): ConsoleState {
  return { operationId, consoleUri, stdout: emptyTailBuffer(), stderr: emptyTailBuffer(), truncated: false, done: false, exitCode: null }
}

/** Reduce one SSE frame into the state. Unknown events and malformed JSON
 *  leave the state unchanged (a broken frame must not kill the stream). */
export function applyConsoleFrame(state: ConsoleState | null, frame: ConsoleFrame): ConsoleState | null {
  let parsed: { chunk?: unknown; exit_code?: unknown } = {}
  try { parsed = JSON.parse(frame.data) as typeof parsed } catch { return state }
  const base = state ?? emptyConsoleState('', consoleURI(''))
  if (frame.event === 'stdout') {
    return { ...base, stdout: appendTail(base.stdout, typeof parsed.chunk === 'string' ? parsed.chunk : '') }
  }
  if (frame.event === 'stderr') {
    return { ...base, stderr: appendTail(base.stderr, typeof parsed.chunk === 'string' ? parsed.chunk : '') }
  }
  if (frame.event === 'done') {
    return { ...base, done: true, exitCode: typeof parsed.exit_code === 'number' ? parsed.exit_code : base.exitCode }
  }
  return state
}

/** Shape of `GET /v1/agent/console/{operationID}` per the T1 contract. All
 *  fields optional — an older kernel replies 404 and the component shows the
 *  failure, a newer one may add fields this client ignores. */
export interface ConsoleTailPayload {
  operation_id?: string
  console_uri?: string
  stdout_tail?: string
  stderr_tail?: string
  bytes?: number
  lines?: number
  truncated?: boolean
  done?: boolean
  exit_code?: number | null
}

/** Build the client state from a tail snapshot. The server tail is passed
 *  through the same client-side limiter, so the 2000-line invariant holds
 *  even when the server budget happens to be larger; a `console_uri` from
 *  the payload wins only if it is a `console://` address — anything else
 *  falls back to the canonical constructor (DoD-5 keeps `artifact://` out
 *  even if a peer ever sent a wrong scheme). */
export function consoleStateFromTail(operationId: string, payload: ConsoleTailPayload): ConsoleState {
  const stdout = appendTail(emptyTailBuffer(), typeof payload.stdout_tail === 'string' ? payload.stdout_tail : '')
  const stderr = appendTail(emptyTailBuffer(), typeof payload.stderr_tail === 'string' ? payload.stderr_tail : '')
  const id = String(payload.operation_id ?? operationId)
  const sent = typeof payload.console_uri === 'string' ? payload.console_uri : ''
  return {
    operationId: id,
    consoleUri: sent.startsWith('console://') ? sent : consoleURI(id),
    stdout,
    stderr,
    truncated: Boolean(payload.truncated),
    done: Boolean(payload.done),
    exitCode: typeof payload.exit_code === 'number' ? payload.exit_code : null,
  }
}

/** Tail snapshot: `GET /v1/agent/console/{operationID}?project=…`. */
export async function fetchConsoleTail(operationId: string, project: string, signal?: AbortSignal): Promise<ConsoleState> {
  const params = new URLSearchParams()
  if (project.trim()) params.set('project', project.trim())
  const qs = params.toString()
  const response = await fetch(`/kernel-api/v1/agent/console/${encodeURIComponent(operationId)}${qs ? `?${qs}` : ''}`, {
    signal,
    headers: authHeaders(),
  })
  if (!response.ok) throw new Error(`console tail ${response.status}`)
  const payload = await response.json() as ConsoleTailPayload
  return consoleStateFromTail(operationId, payload)
}

/** Live tail: `GET /v1/agent/console/{operationID}/stream?project=…&tail=1`.
 *  SSE with named events, parsed directly (the browser EventSource cannot
 *  carry the auth header and would need a listener per event type — same
 *  reasoning as `subscribeRunJournal`). Errors are swallowed: the caller
 *  already holds the tail snapshot, and a hidden-tab resume re-fetches it. */
export function subscribeConsoleStream(operationId: string, project: string, onFrame: (frame: ConsoleFrame) => void, signal: AbortSignal, opts?: { tail?: boolean }): void {
  const params = new URLSearchParams()
  if (project.trim()) params.set('project', project.trim())
  if (opts?.tail) params.set('tail', '1')
  const qs = params.toString()
  const url = `/kernel-api/v1/agent/console/${encodeURIComponent(operationId)}/stream${qs ? `?${qs}` : ''}`
  void fetch(url, { signal, headers: { Accept: 'text/event-stream', ...authHeaders() } })
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
          if (frame) onFrame(frame)
        }
      }
    })
    .catch((failure) => {
      // AbortError is the expected pause/unmount path; anything else leaves
      // the last snapshot on screen rather than blanking the console.
      if (!isAbortError(failure)) return
    })
}

/** Text behind the «copy» button: exactly the active stream's buffer. */
export function consoleCopyText(state: ConsoleState, tab: 'stdout' | 'stderr'): string {
  return tab === 'stderr' ? state.stderr.text : state.stdout.text
}

/** True when the console view has anything to flag as dropped: the client
 *  counters (exact) or the server truncation flag (exact volume unknown). */
export function consoleTailShort(state: ConsoleState): boolean {
  return state.truncated || state.stdout.droppedLines > 0 || state.stdout.droppedBytes > 0
    || state.stderr.droppedLines > 0 || state.stderr.droppedBytes > 0
}

/** «Отброшено N строк / M КБ» — client-side counters only; the server flag
 *  adds the «unknown exact volume» note in the UI instead of a fake number. */
export function consoleDroppedLines(state: ConsoleState): number {
  return state.stdout.droppedLines + state.stderr.droppedLines
}
export function consoleDroppedBytes(state: ConsoleState): number {
  return state.stdout.droppedBytes + state.stderr.droppedBytes
}
