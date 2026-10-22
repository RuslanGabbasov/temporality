import { describe, expect, it } from 'vitest'
import {
  CONSOLE_BYTE_LIMIT,
  CONSOLE_LINE_LIMIT,
  appendTail,
  applyConsoleFrame,
  consoleCopyText,
  consoleDroppedBytes,
  consoleDroppedLines,
  consoleStateFromTail,
  consoleTailShort,
  consoleURI,
  emptyConsoleState,
  emptyTailBuffer,
  type ConsoleState,
} from './consoleStream'

describe('console:// addressing (DoD-5)', () => {
  it('builds the canonical console:// URI', () => {
    expect(consoleURI('op-1')).toBe('console://op-1')
  })

  it('never lets a wrong scheme through — artifact:// stays with the artifact registry', () => {
    const state = consoleStateFromTail('op-1', { console_uri: 'artifact://op-1', stdout_tail: 'x' })
    expect(state.consoleUri.startsWith('artifact://')).toBe(false)
    expect(state.consoleUri).toBe('console://op-1')
  })
})

describe('client tail buffer (§11.2: 2000 lines / 256 KiB)', () => {
  it('keeps the newest 2000 lines and counts the dropped ones', () => {
    let buffer = emptyTailBuffer()
    for (let i = 1; i <= CONSOLE_LINE_LIMIT + 120; i += 1) buffer = appendTail(buffer, `line-${i}\n`)
    const lines = buffer.text.split('\n').filter(Boolean)
    expect(lines.length).toBe(CONSOLE_LINE_LIMIT)
    expect(lines[0]).toBe('line-121')
    expect(lines[lines.length - 1]).toBe(`line-${CONSOLE_LINE_LIMIT + 120}`)
    expect(buffer.droppedLines).toBe(120)
    expect(buffer.lines).toBe(CONSOLE_LINE_LIMIT)
  })

  it('keeps at most 256 KiB and counts the dropped bytes', () => {
    let buffer = emptyTailBuffer()
    for (let i = 0; i < 300; i += 1) buffer = appendTail(buffer, `${'x'.repeat(1024)}\n`)
    expect(buffer.bytes).toBeLessThanOrEqual(CONSOLE_BYTE_LIMIT)
    expect(buffer.droppedBytes).toBeGreaterThan(0)
    expect(buffer.bytes + buffer.droppedBytes).toBe(300 * 1025)
  })

  it('never splits a multi-byte character at the tail boundary', () => {
    let buffer = appendTail(emptyTailBuffer(), 'a'.repeat(CONSOLE_BYTE_LIMIT))
    buffer = appendTail(buffer, 'ж')
    expect(buffer.bytes).toBeLessThanOrEqual(CONSOLE_BYTE_LIMIT)
    expect(buffer.bytes + buffer.droppedBytes).toBe(CONSOLE_BYTE_LIMIT + 2)
    expect(buffer.text.endsWith('ж')).toBe(true)
  })

  it('counts an unterminated last line', () => {
    let buffer = emptyTailBuffer()
    buffer = appendTail(buffer, 'one\ntwo')
    expect(buffer.lines).toBe(2)
    buffer = appendTail(buffer, '\n')
    expect(buffer.lines).toBe(2)
  })

  it('ignores empty chunks', () => {
    const buffer = appendTail(emptyTailBuffer(), 'keep')
    expect(appendTail(buffer, '')).toBe(buffer)
  })
})

describe('SSE frames → console state', () => {
  it('routes stdout and stderr frames into separate streams', () => {
    let state: ConsoleState | null = emptyConsoleState('op-1', consoleURI('op-1'))
    state = applyConsoleFrame(state, { event: 'stdout', data: '{"chunk":"hello\\n"}' })
    state = applyConsoleFrame(state, { event: 'stderr', data: '{"chunk":"boom\\n"}' })
    expect(state!.stdout.text).toBe('hello\n')
    expect(state!.stderr.text).toBe('boom\n')
  })

  it('done carries the exit code and ends the stream without touching the buffers', () => {
    let state: ConsoleState | null = emptyConsoleState('op-1', consoleURI('op-1'))
    state = applyConsoleFrame(state, { event: 'stdout', data: '{"chunk":"work\\n"}' })
    state = applyConsoleFrame(state, { event: 'done', data: '{"exit_code":2}' })
    expect(state!.done).toBe(true)
    expect(state!.exitCode).toBe(2)
    expect(state!.stdout.text).toBe('work\n')
  })

  it('malformed payloads and unknown events leave the state unchanged', () => {
    const state = emptyConsoleState('op-1', consoleURI('op-1'))
    expect(applyConsoleFrame(state, { event: 'stdout', data: '{broken' })).toBe(state)
    expect(applyConsoleFrame(state, { event: 'ping', data: '{}' })).toBe(state)
  })
})

describe('tail snapshot mapping (T1 contract)', () => {
  it('maps the payload fields onto the client state', () => {
    const state = consoleStateFromTail('op-9', {
      operation_id: 'op-9',
      console_uri: 'console://op-9',
      stdout_tail: 'out\n',
      stderr_tail: 'err\n',
      bytes: 8,
      lines: 2,
      truncated: true,
      done: true,
      exit_code: 0,
    })
    expect(state.stdout.text).toBe('out\n')
    expect(state.stderr.text).toBe('err\n')
    expect(state.truncated).toBe(true)
    expect(state.done).toBe(true)
    expect(state.exitCode).toBe(0)
  })

  it('still applies the client line limit to an oversized server tail', () => {
    const state = consoleStateFromTail('op-9', {
      stdout_tail: Array.from({ length: CONSOLE_LINE_LIMIT + 5 }, (_, i) => `l${i}`).join('\n'),
    })
    expect(state.stdout.droppedLines).toBe(4)
    expect(consoleTailShort(state)).toBe(true)
  })
})

describe('tail-short marker and copy source', () => {
  it('flags client drops and the server truncation flag, nothing else', () => {
    expect(consoleTailShort(consoleStateFromTail('op', { truncated: true }))).toBe(true)
    expect(consoleTailShort(emptyConsoleState('op', consoleURI('op')))).toBe(false)
    let dropped: ConsoleState | null = emptyConsoleState('op', consoleURI('op'))
    for (let i = 0; i < CONSOLE_LINE_LIMIT + 3; i += 1) dropped = applyConsoleFrame(dropped, { event: 'stdout', data: `{"chunk":"line ${i}\\n"}` })
    expect(consoleTailShort(dropped!)).toBe(true)
    expect(consoleDroppedLines(dropped!)).toBe(3)
    expect(consoleDroppedBytes(dropped!)).toBeGreaterThan(0)
  })

  it('copy text is exactly the active stream buffer', () => {
    let state: ConsoleState | null = emptyConsoleState('op', consoleURI('op'))
    state = applyConsoleFrame(state, { event: 'stdout', data: '{"chunk":"out text"}' })
    state = applyConsoleFrame(state, { event: 'stderr', data: '{"chunk":"err text"}' })
    expect(consoleCopyText(state!, 'stdout')).toBe('out text')
    expect(consoleCopyText(state!, 'stderr')).toBe('err text')
  })
})
