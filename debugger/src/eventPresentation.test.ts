import { describe, expect, it } from 'vitest'
import { EVENT_CATALOG } from './eventCatalog'
import { eventDetails, rawEnvelope, timelineRow, runningBackgroundOps, PRESENTATION_TABLES, type TimelineRow } from './eventPresentation'
import { ru } from './locales/ru'
import { en } from './locales/en'
import type { Locale } from './locales'

/** The test harness t(): resolves real locale messages with {{var}} interpolation. */
const t = (key: string, vars?: Record<string, string>): string => {
  const template = ru[key] ?? en[key] ?? key
  if (!vars) return template
  return template.replace(/\{\{(\w+)\}\}/g, (match, name: string) => (name in vars ? vars[name] : match))
}

const RU: Locale = 'ru'
const EN: Locale = 'en'

/** §4: the raw event type must never surface as the row title. */
const DOTTED_TYPE_RE = /^[a-z0-9_]+(\.[a-z0-9_]+)+$/
const modelEvent = (type: string, data: Record<string, unknown> = {}): Parameters<typeof timelineRow>[0] =>
  ({ type, data, event_id: `${type}-1`, occurred_at: '2026-10-22T10:00:00Z', source: { id: 'kernel' }, context: { run: 'r1', project: 'p' } } as unknown as Parameters<typeof timelineRow>[0])

/** Pseudo-tools the kernel runs as tool.* payloads (§5.3). */
const PSEUDO_TOOLS = [
  'run_command',
  'read_file',
  'write_file',
  'edit_file',
  'search',
  'browser',
  'send_file',
  'delegate',
  'remember',
  'web_search',
  'web_fetch',
  'fetch_image',
  'view_image',
  'skill_propose',
  'skill_inspect',
  'skill_evaluate',
  'skill_diff',
  'skill_history',
  'skill_executions',
  'skill_memory',
  'skill_validate',
  'mcp__graphmap__connect',
  'mcp__web__fetch',
]

describe('presentation coverage (DoD-1)', () => {
  it('maps every catalog type: explicit override or the generic row — never a raw type title', () => {
    const covered: string[] = []
    const generic: string[] = []
    for (const meta of EVENT_CATALOG) {
      for (const locale of [RU, EN] as const) {
        const row = timelineRow(modelEvent(meta.type, { title: 'X' }), locale, t)
        expect(row, `row of ${meta.type}`).toBeTruthy()
        // §4 principle: the type name never shows as the title.
        expect(DOTTED_TYPE_RE.test(row.title), `${meta.type} → "${row.title}" leaks the type name`).toBe(false)
        expect(row.title.length, `${meta.type} title over 120 chars`).toBeLessThanOrEqual(120)
        expect(row.category, `${meta.type} category`).toBe(meta.group)
        expect(row.icon.length).toBeGreaterThan(0)
      }
      if (PRESENTATION_TABLES.OVERRIDES[meta.type]) covered.push(meta.type)
      else generic.push(meta.type)
    }
    // The catalog is fully walked and fully covered by (override ∪ generic).
    expect(covered.length + generic.length).toBe(EVENT_CATALOG.length)
    // The generic template is the fallback, not the majority path: the
    // catalog groups with UX tables in §5 must be explicitly mapped.
    expect(covered.length).toBeGreaterThan(EVENT_CATALOG.length / 2)
  })

  it('maps every pseudo tool into a titled tool row (§5.3)', () => {
    for (const tool of PSEUDO_TOOLS) {
      for (const phase of ['tool.started', 'tool.completed', 'tool.failed']) {
        const row = timelineRow(modelEvent(phase, { tool, arguments: JSON.stringify({ command: ['go', 'test', './...'] }) }), RU, t)
        expect(row.title, `${phase}(${tool})`).toBe(tool)
        expect(row.fact).toContain('go test ./...')
        expect(DOTTED_TYPE_RE.test(row.title)).toBe(false)
      }
    }
  })

  it('maps the streaming types to the stream class — they never render as rows (§5.2)', () => {
    for (const type of ['model.text_delta', 'model.reasoning']) {
      const row = timelineRow(modelEvent(type, { text: 'chunk', turn: 3 }), RU, t)
      expect(row.kind).toBe('stream')
      expect(row.hasDetails).toBe(false)
      expect(DOTTED_TYPE_RE.test(row.title)).toBe(false)
    }
    expect(PRESENTATION_TABLES.STREAM_TYPES.has('model.text_delta')).toBe(true)
    expect(PRESENTATION_TABLES.STREAM_TYPES.has('model.reasoning')).toBe(true)
  })

  it('the generic template renders a semantic line, never raw JSON (DoD-1)', () => {
    const row = timelineRow(modelEvent('brand.new.event', { note: 'что-то случилось' }), RU, t)
    expect(row.title).toBe('Событие: что-то случилось')
    expect(row.title).not.toContain('brand.new.event')
    expect(row.title).not.toContain('{')
    const empty = timelineRow(modelEvent('brand.new.event', {}), RU, t)
    expect(empty.title).toBe('Событие: …')
    expect(empty.fact).toBe('')
  })

  it('unknown events still land in a category look and capped titles', () => {
    const row = timelineRow(modelEvent('brand.new.event', { note: 'x'.repeat(300) }), EN, t)
    expect(row.title.length).toBeLessThanOrEqual(120)
    expect(row.category).toBe('ops')
  })
})

describe('timeline row semantics (§4.1)', () => {
  it('run_command timeline shows the command, exit status and duration — not the type name', () => {
    const done = timelineRow(modelEvent('tool.completed', { tool: 'run_command', arguments: JSON.stringify({ command: ['docker', 'compose', 'up', '-d'] }), exit_code: 0, latency_ms: 91200 }), RU, t)
    expect(done.title).toBe('run_command')
    expect(done.fact).toContain('docker compose up -d')
    expect(done.fact).toContain('exit 0')
    expect(done.duration).toBe('1m 31s')
    expect(done.status).toBe('ok')
    expect(done.category).toBe('tools')

    const failed = timelineRow(modelEvent('tool.completed', { tool: 'run_command', exit_code: 1 }), RU, t)
    expect(failed.status).toBe('error')
  })

  it('run.failed and model.failed carry the peeled cause as the key fact', () => {
    const failed = timelineRow(modelEvent('run.failed', { error: 'child workflow execution error (type: AgentRun, unresolved): activity error (type: kernel.call_model, w): context deadline exceeded' }), RU, t)
    expect(failed.status).toBe('error')
    expect(failed.fact).toContain('context deadline exceeded')
    expect(failed.fact).not.toContain('child workflow execution error')
  })

  it('model.completed aggregates tokens/latency/tools into one fact', () => {
    const row = timelineRow(modelEvent('model.completed', { total_tokens: 1234, latency_ms: 2500, tool_call_count: 3 }), RU, t)
    expect(row.title).toBe('Ответ модели')
    expect(row.fact).toContain('1234 tok')
    expect(row.fact).toContain('2.5s')
    expect(row.fact).toContain('3 tools')
    expect(row.duration).toBe('2.5s')
  })

  it('secret rows carry name/status only (P-2) and skip the details layer', () => {
    for (const type of ['secret.created', 'secret.updated', 'secret.deleted', 'secret.value_set', 'secret.issued']) {
      const row = timelineRow(modelEvent(type, { secret: 'API_TOKEN', value: 'super-secret-value' }), RU, t)
      expect(row.title).toContain('API_TOKEN')
      expect(row.title).not.toContain('super-secret-value')
      expect(JSON.stringify(row)).not.toContain('super-secret-value')
      expect(row.hasDetails).toBe(false)
    }
    const denied = timelineRow(modelEvent('secret.denied', { secret: 'API_TOKEN', reason: 'policy', value: 'nope' }), RU, t)
    expect(denied.status).toBe('error')
    expect(denied.fact).toBe('policy')
    expect(JSON.stringify(denied)).not.toContain('nope')
  })
})

describe('details layer (§4.2)', () => {
  it('builds human-form arguments, result, cause, links and the console slot', () => {
    const details = eventDetails(modelEvent('tool.started', {
      tool: 'run_command',
      operation_id: 'op-1',
      arguments: JSON.stringify({ command: ['go', 'test'], cwd: '/workspace' }),
      child_run_id: 'root/delegate/01',
      artifact_id: 'coding-standard',
      knowledge_id: 'ext/k1',
    }), t)
    expect(details.actor).toBe('run_command')
    expect(details.argumentLines).toEqual([{ key: 'command', value: '["go","test"]' }, { key: 'cwd', value: '/workspace' }])
    expect(details.links.map((l) => l.kind)).toEqual(['child_run', 'artifact', 'knowledge'])
    expect(details.links[0].url).toContain('/agents?project=p&run=root%2Fdelegate%2F01')
    expect(details.links[1].url).toContain('/artifacts?project=p&id=coding-standard')
    expect(details.links[2].url).toContain('/knowledge?project=p&dossier=ext%2Fk1')
    expect(details.consoleOperationId).toBe('op-1')
  })

  it('reserves the console slot for run_command only (console:// scheme, §6)', () => {
    const other = eventDetails(modelEvent('tool.started', { tool: 'read_file', operation_id: 'op-2', arguments: '{"path":"a.go"}' }), t)
    expect(other.consoleOperationId).toBe('')
    const noOp = eventDetails(modelEvent('tool.started', { tool: 'run_command' }), t)
    expect(noOp.consoleOperationId).toBe('')
  })

  it('peels the failure cause and flags model timeouts', () => {
    const details = eventDetails(modelEvent('run.failed', { error: 'child workflow execution error (type: AgentRun, x): activity error (type: kernel.call_model, y): context deadline exceeded' }), t)
    expect(details.failureCause).toBe('context deadline exceeded')
    expect(details.timeout).toBe(true)
    expect(details.failed).toBe(true)
  })

  it('caps long human-form argument lines', () => {
    const details = eventDetails(modelEvent('tool.started', { tool: 'run_command', arguments: JSON.stringify({ command: ['x'.repeat(500)] }) }), t)
    expect(details.argumentLines[0].value.length).toBeLessThanOrEqual(300)
  })

  it('write_file details show the path and a content fragment — not a JSON envelope', () => {
    // The kernel fragment arrives truncated (compactJSON ≤500 chars) but now
    // VALID, so the parsed args reach the special cases.
    const args = JSON.stringify({ path: '/workspace/src/main.go', content: 'package main\n\nfunc main() { ... }' })
    const details = eventDetails(modelEvent('tool.started', { tool: 'write_file', operation_id: 'op-3', arguments: args }), t)
    expect(details.argumentLines[0]).toEqual({ key: 'path', value: '/workspace/src/main.go' })
    expect(details.argumentLines[1].key).toBe('content')
    expect(details.argumentLines[1].value).toContain('package main')
  })

  it('remember_many details list the propositions instead of raw JSON', () => {
    const args = JSON.stringify({ items: [
      { proposition: 'Billing covered by e2e/billing tests', evidence: ['e1'] },
      { proposition: 'Auth tokens live in /cache' },
    ] })
    const details = eventDetails(modelEvent('tool.started', { tool: 'remember_many', operation_id: 'op-4', arguments: args }), t)
    expect(details.argumentLines).toEqual([
      { key: '#1', value: 'Billing covered by e2e/billing tests' },
      { key: '#2', value: 'Auth tokens live in /cache' },
    ])
  })

  it('remember_many row fact names the first proposition and the batch size', () => {
    const row = timelineRow(modelEvent('tool.started', {
      tool: 'remember_many',
      arguments: JSON.stringify({ items: [{ proposition: 'Billing covered by e2e/billing tests' }, { proposition: 'Second fact' }] }),
    }), RU, t)
    expect(row.fact).toContain('Billing covered by e2e/billing tests')
    expect(row.fact).toContain('+1')
  })

  it('makes the human verdict expandable and shows what the human said', () => {
    const row = timelineRow(modelEvent('hitl.verdict.recorded', {
      type: 'accepted', answered_by: 'user:ivan',
      response: 'Берём вариант Б, но с оговоркой про кэш',
      question: 'Какой вариант закладываем в план?',
    }), RU, t)
    expect(row.hasDetails).toBe(true)
    expect(row.fact).toContain('Берём вариант Б')
    const details = eventDetails(modelEvent('hitl.verdict.recorded', {
      type: 'accepted', response: 'Берём вариант Б, но с оговоркой про кэш',
    }), t)
    expect(details.result).toContain('оговоркой про кэш')
  })
})

describe('raw layer (§4.3, K-1, P-2)', () => {
  it('returns the full envelope for ordinary events', () => {
    const envelope = JSON.parse(rawEnvelope(modelEvent('tool.completed', { tool: 'run_command', output: 'ok' })))
    expect(envelope.type).toBe('tool.completed')
    expect(envelope.data.tool).toBe('run_command')
  })

  it('caps secret envelopes to name/status (P-2) — the payload never renders', () => {
    for (const type of ['secret.created', 'secret.value_set', 'secret.issued', 'secret.denied']) {
      const envelope = rawEnvelope(modelEvent(type, { secret: 'API_TOKEN', value: 'hunter2', denied_reason: 'policy' }))
      expect(envelope).not.toContain('hunter2')
      expect(envelope).toContain('API_TOKEN')
      const parsed = JSON.parse(envelope)
      expect(Object.keys(parsed.data)).toEqual(['name'])
    }
  })
})

describe('§5 folding contract of the presentation classes', () => {
  it('classifies turn boundaries, minor plan transitions, noise and hints', () => {
    const expectKind = (type: string, kind: TimelineRow['kind']) => {
      const row = timelineRow(modelEvent(type, {}), RU, t)
      expect(row.kind, type).toBe(kind)
    }
    expectKind('turn.started', 'minor')
    expectKind('turn.completed', 'minor')
    expectKind('plan.task.skipped', 'minor')
    expectKind('plan.task.invalidated', 'minor')
    expectKind('plan.task.reopened', 'minor')
    expectKind('plan.task.incomplete', 'minor')
    expectKind('storage.snapshot', 'noise')
    expectKind('operation.reconciled', 'noise')
    expectKind('notification.sent', 'noise')
    expectKind('memory.read.failed', 'noise')
    expectKind('hint.offered', 'normal')
    expectKind('plan.task.rejected', 'normal')
    expectKind('approval.requested', 'normal')
    expectKind('saga.registered', 'normal')
  })
})

describe('runningBackgroundOps (docs/agent-async-operations.md trace panel)', () => {
  const bg = (operationId: string, command: string[] = ['yarn', 'install']): ReturnType<typeof modelEvent> =>
    modelEvent('tool.started', { tool: 'run_command', operation_id: operationId, arguments: JSON.stringify({ command, background: true }) })

  it('pins live detached commands, drops completed, in-window and foreground ones', () => {
    const fg = modelEvent('tool.started', { tool: 'run_command', operation_id: 'op-fg', arguments: JSON.stringify({ command: ['ls'] }) })
    const otherTool = modelEvent('tool.started', { tool: 'read_file', operation_id: 'op-read', arguments: JSON.stringify({ path: '/x' }) })
    const noArgs = modelEvent('tool.started', { tool: 'run_command', operation_id: 'op-noargs' })
    const foreign = { ...bg('op-foreign'), context: { ...bg('op-foreign').context, run: 'r2' } }
    const timeline = [bg('op-window'), bg('op-out'), fg, otherTool, noArgs, foreign, bg('op-out') /* duplicate */]
    const window = [bg('op-window')]
    const ops = runningBackgroundOps(timeline, window, new Set<string>(), 'r1')
    // op-window stays inline (its row is in the render window); op-out is the
    // ONLY panel entry — foreground/other-tool/foreign-run rows never pin.
    expect(ops).toEqual([{ operationId: 'op-out', command: 'yarn install' }])
  })

  it('drops an operation once its tool.completed lands in completedOps', () => {
    const bg = modelEvent('tool.started', { tool: 'run_command', operation_id: 'op-1', arguments: JSON.stringify({ command: ['sleep', '30'], background: true }) })
    const ops = runningBackgroundOps([bg], [], new Set(['op-1']), 'r1')
    expect(ops).toEqual([])
  })
})
