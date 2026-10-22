import { afterEach, describe, expect, it, vi } from 'vitest'
import type { ObservationEvent } from './observationApi'
import { ru } from './locales/ru'
import { en } from './locales/en'
import { foldTrace } from './traceModel'
import { filterTraceItems, searchItems, EMPTY_FILTER } from './traceFilters'
import { consoleSearchIndex, traceSearchEntries } from './TraceFiltersPanel'

// NFR-9 (§8): filtering and search are pure client-side passes over the
// ALREADY-LOADED window. Typing in the search box must never touch the
// network — this file pins it by counting global.fetch calls while a
// realistic window is filtered and searched per keystroke, including the
// console-output index built from the buffers the client already holds.
// Server-side search over command output is impossible in this iteration
// (§11.1: output is not persisted) — the UI states that limit
// (runs.filters.search_hint) instead of fetching.

const t = (key: string, vars?: Record<string, string>): string => {
  const template = ru[key] ?? en[key] ?? key
  if (!vars) return template
  return template.replace(/\{\{(\w+)\}\}/g, (match, name: string) => (name in vars ? vars[name] : match))
}
const RU = 'ru' as const

let seq = 0
const ev = (type: string, data: Record<string, unknown> = {}, second = 0): ObservationEvent => {
  seq += 1
  return {
    schema: 'temporality/observation/event/v1',
    event_id: `e${seq}`,
    occurred_at: `2026-10-22T10:00:${String(second).padStart(2, '0')}Z`,
    received_at: `2026-10-22T10:00:${String(second).padStart(2, '0')}Z`,
    source: { id: 'kernel' },
    context: { run: 'r1', project: 'p' },
    type,
    data,
  } as ObservationEvent
}

/** A realistic loaded window: every family the timeline renders. */
const WINDOW: ObservationEvent[] = [
  ev('run.started', { model: 'gpt-5' }, 1),
  ev('turn.started', { turn: 1 }, 2),
  ev('model.started', { model: 'gpt-5' }, 3),
  ev('model.completed', { total_tokens: 1500, latency_ms: 4200 }, 4),
  ev('tool.started', { tool: 'run_command', operation_id: 'op-cmd', arguments: '{"command":["go","test","./..."]}' }, 5),
  ev('tool.completed', { tool: 'run_command', operation_id: 'op-cmd', exit_code: 0, latency_ms: 8100 }, 6),
  ev('approval.requested', { operation_id: 'op-ap', action: 'deploy' }, 7),
  ev('approval.granted', { operation_id: 'op-ap', approver: 'ruslan' }, 8),
  ev('hint.offered', { hint_id: 'h1', proposition: 'кэшируй сборку' }, 9),
  ev('hint.used', { hint_id: 'h1', proposition: 'кэшируй сборку' }, 10),
  ev('secret.issued', { secret: 'API_TOKEN' }, 11),
  ev('run.completed', { turns: 2 }, 12),
]

/** One client-held console buffer, keyed by event id like ConsoleOutput. */
const CONSOLE_BUFFERS = new Map([
  [WINDOW[4].event_id, 'stdout: ok 42\nstderr: warning: deprecated flag'],
])

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('NFR-9: trace filtering and search never fetch', () => {
  it('filtering/search over the loaded window (and console buffers) call global.fetch exactly 0 times', () => {
    const fetchCalls = vi.fn()
    vi.stubGlobal('fetch', fetchCalls)

    const items = foldTrace(WINDOW, RU, t)

    // Typing «go test» character by character: every committed keystroke
    // re-filters and re-searches the whole window.
    const typed = 'go test'
    for (let end = 1; end <= typed.length; end += 1) {
      const query = typed.slice(0, end)
      const filtered = filterTraceItems(items, EMPTY_FILTER, new Set(['errors']), RU, t)
      expect(filtered.length).toBeGreaterThanOrEqual(0)
      const entries = searchItems(items, query, RU, t, CONSOLE_BUFFERS)
      expect(entries.length).toBeGreaterThanOrEqual(0)
    }
    // The full query finds the command row through its haystack.
    expect(searchItems(items, 'go test', RU, t, CONSOLE_BUFFERS)).toHaveLength(1)
    // …and finds the stdout tail through the held console buffer.
    expect(searchItems(items, 'deprecated flag', RU, t, CONSOLE_BUFFERS)).toHaveLength(1)

    expect(fetchCalls).toHaveBeenCalledTimes(0)
  })

  it('the panel console index builds from held buffers only — no fetch on miss', () => {
    const fetchCalls = vi.fn()
    vi.stubGlobal('fetch', fetchCalls)
    const items = foldTrace(WINDOW, RU, t)
    // 'op-cmd' resolves to its event; an unknown operation id just stays out.
    const index = consoleSearchIndex(new Map([['op-cmd', { stdout: { text: 'ok' } }], ['op-ghost', { stdout: { text: 'nope' } }]]), items)
    expect([...index.keys()]).toEqual([WINDOW[4].event_id])
    expect(traceSearchEntries(items, 'ok', RU, t, new Map([['op-cmd', { stdout: { text: 'ok' } }]])).length).toBeGreaterThan(0)
    expect(traceSearchEntries(items, 'nope', RU, t, new Map([['op-ghost', { stdout: { text: 'nope' } }]]))).toHaveLength(0)
    expect(fetchCalls).toHaveBeenCalledTimes(0)
  })

  it('a window-wide sweep over every dimension keeps fetch at zero', () => {
    const fetchCalls = vi.fn()
    vi.stubGlobal('fetch', fetchCalls)
    const items = foldTrace(WINDOW, RU, t)
    const chips = new Set(['errors', 'tools', 'human', 'myInput'] as const)
    for (const participant of ['model', 'tool', 'human', 'system', ''] as const) {
      for (const status of ['ok', 'error', 'cancelled', ''] as const) {
        expect(filterTraceItems(items, { ...EMPTY_FILTER, participant, status }, chips, RU, t).length).toBeGreaterThanOrEqual(0)
      }
    }
    // 'gpt' hits both rows carrying the model name (run.started, model.started).
    expect(searchItems(items, 'gpt', RU, t)).toHaveLength(2)
    expect(fetchCalls).toHaveBeenCalledTimes(0)
  })
})
