import { describe, expect, it } from 'vitest'
import { sendFileChip, formatStreamEvent, chatFragmentLine, normalizeRunStatus, runTraceUrl, sortConversationsByActivity } from './Workspace'
import { pluralT } from './i18n'
import { en } from './locales/en'
import { ru } from './locales/ru'

/** Mirror of the i18n t() implementation, backed by a locale bundle. */
function translator(messages: Record<string, string>) {
  return (key: string, vars?: Record<string, string>) => {
    let text = messages[key] ?? key
    if (vars) {
      Object.entries(vars).forEach(([k, v]) => { text = text.replace(`{{${k}}}`, v) })
    }
    return text
  }
}

const enT = translator(en)
const ruT = translator(ru)

describe('runTraceUrl', () => {
  it('points at the run\'s trace view; project and run id are URL-encoded', () => {
    expect(runTraceUrl('omni', 'abc-123')).toBe('/agents?project=omni&run=abc-123&view=trace')
    expect(runTraceUrl('a b', 'x/y')).toBe('/agents?project=a%20b&run=x%2Fy&view=trace')
  })

  it('the chat trace link is localized in both bundles', () => {
    expect(en['chat.trace']).toBeDefined()
    expect(ru['chat.trace']).toBeDefined()
    expect(en['chat.trace_title']).toBeDefined()
    expect(ru['chat.trace_title']).toBeDefined()
  })
})

describe('pluralT', () => {
  it('picks Russian one/few/many forms', () => {
    expect(pluralT(ruT, 'ru', 'chat.stream.turns_n', 1)).toBe('1 ход')
    expect(pluralT(ruT, 'ru', 'chat.stream.turns_n', 2)).toBe('2 хода')
    expect(pluralT(ruT, 'ru', 'chat.stream.turns_n', 5)).toBe('5 ходов')
    expect(pluralT(ruT, 'ru', 'chat.stream.turns_n', 11)).toBe('11 ходов')
    expect(pluralT(ruT, 'ru', 'chat.stream.turns_n', 21)).toBe('21 ход')
  })

  it('picks English singular/plural', () => {
    expect(pluralT(enT, 'en', 'chat.stream.turns_n', 1)).toBe('1 turn')
    expect(pluralT(enT, 'en', 'chat.stream.turns_n', 3)).toBe('3 turns')
  })
})

describe('formatStreamEvent', () => {
  it('renders the compact per-turn model line: tokens, latency, elapsed', () => {
    expect(formatStreamEvent('model.completed', { data: { turn: 2, total_tokens: 1248, latency_ms: 23300, elapsed_s: 755 } }, ruT, 'ru'))
      .toBe('+12м 35с Ход 2 · 1.2k ток · 23.3с')
    expect(formatStreamEvent('model.completed', { data: { turn: 2, total_tokens: 980, latency_ms: 4200 } }, enT, 'en'))
      .toBe('Turn 2 · 980 tok · 4.2s')
  })

  it('adds the live-measured thinking time when reasoning deltas were seen', () => {
    expect(formatStreamEvent('model.completed', { data: { turn: 3, total_tokens: 2100, latency_ms: 14000, thinking_s: 6.38, elapsed_s: 20 } }, ruT, 'ru'))
      .toBe('+20с Ход 3 · 💭 6.4с · 2.1k ток · 14.0с')
    expect(formatStreamEvent('model.completed', { data: { turn: 3, total_tokens: 2100, latency_ms: 14000, thinking_s: 6.38 } }, enT, 'en'))
      .toBe('Turn 3 · 💭 6.4s · 2.1k tok · 14.0s')
  })

  it('keeps silent recovery markers and knowledge lines', () => {
    expect(formatStreamEvent('model.attempt', { data: { turn: 3, attempt: 2, reason: 'stream_timeout' } }, ruT, 'ru'))
      .toBe('Провайдер молчит до первого токена — повтор на новом соединении (попытка 2)')
    expect(formatStreamEvent('knowledge.proposed', { data: { proposition: 'x'.repeat(80) } }, ruT, 'ru'))
      .toBe('Усвоено: ' + 'x'.repeat(60))
  })

  it('shows lasting milestones and blocked attempts, not policy chatter', () => {
    // A blocked tool is a failed attempt: the observer must see why nothing
    // happened after the ⚙ line.
    expect(formatStreamEvent('tool.blocked', { data: { reason: 'approval_timeout' } }, ruT, 'ru'))
      .toBe('⚠ действие не выполнено: истекло одобрение')
    expect(formatStreamEvent('tool.blocked', { data: { reason: 'approval_rejected' } }, enT, 'en'))
      .toBe('⚠ action not run: rejected by a human')
    // The run produced lasting documents: created, submitted, resolved.
    expect(formatStreamEvent('artifact.authored', { data: { title: 'Смета' } }, ruT, 'ru'))
      .toBe('📝 создан артефакт: Смета')
    expect(formatStreamEvent('artifact.submitted', { data: { artifact_id: 'smeta' } }, enT, 'en'))
      .toBe('📝 submitted for review: smeta')
    expect(formatStreamEvent('artifact.review.approved', { data: { title: 'Смета' } }, ruT, 'ru'))
      .toBe('✅ артефакт одобрен: Смета')
    // A skill proposal is the reusable twin of knowledge.proposed.
    expect(formatStreamEvent('skill.proposed', { data: { skill_name: 'deploy-check' } }, ruT, 'ru'))
      .toBe('🧩 предложен навык: deploy-check')
    // The supervisor's verdict on an uncertain step carries the note.
    expect(formatStreamEvent('operation.reconciled', { data: { effect: 'occurred', note: 'go build ok' } }, ruT, 'ru'))
      .toBe('🔍 шаг: подтверждён супервизором — выполнялся — go build ok')
  })

  it('turn.completed and tool.completed are silent in the compact trace', () => {
    expect(formatStreamEvent('turn.completed', { data: { turn: 1, tool_calls: 3 } }, enT, 'en')).toBeNull()
    expect(formatStreamEvent('tool.completed', { data: { name: 'run_command' } }, enT, 'en')).toBeNull()
  })

  it('tool.started shows what the call is doing; failures are visible', () => {
    const args = JSON.stringify({ command: ['go', 'test', './...'] })
    expect(formatStreamEvent('tool.started', { data: { tool: 'run_command', arguments: args, elapsed_s: 61 } }, ruT, 'ru'))
      .toBe('+1м 1с ⚙ run_command: go test ./...')
    expect(formatStreamEvent('tool.started', { data: { tool: 'read_file', arguments: JSON.stringify({ path: '/workspace/report.md' }) } }, enT, 'en'))
      .toBe('⚙ read_file: /workspace/report.md')
    expect(formatStreamEvent('tool.failed', { data: { tool: 'run_command', error: 'exit_code=1 build failed' } }, ruT, 'ru'))
      .toBe('⚙ run_command ✗ exit_code=1 build failed')
  })

  it('localizes silent-recovery markers for a stalled provider', () => {
    expect(formatStreamEvent('model.attempt', { data: { turn: 3, attempt: 2, reason: 'stream_timeout' } }, ruT, 'ru'))
      .toBe('Провайдер молчит до первого токена — повтор на новом соединении (попытка 2)')
    expect(formatStreamEvent('model.attempt', { data: { turn: 3, attempt: 2, reason: 'stream_timeout' } }, enT, 'en'))
      .toBe('Provider silent before the first token — retrying on a fresh connection (attempt 2)')

    expect(formatStreamEvent('model.attempt', { data: { turn: 3, attempt: 1, reason: 'stream_fallback' } }, ruT, 'ru'))
      .toBe('Стрим оборвался — повтор без потоковой передачи (попытка 1)')
    expect(formatStreamEvent('model.attempt', { data: { turn: 3, attempt: 1, reason: 'stream_fallback' } }, enT, 'en'))
      .toBe('Stream broke — retrying without streaming (attempt 1)')

    expect(formatStreamEvent('model.attempt', { data: { turn: 3, attempt: 4, reason: 'activity_retry' } }, ruT, 'ru'))
      .toBe('Повтор вызова модели (попытка 4)')
    expect(formatStreamEvent('model.attempt', { data: { turn: 3, attempt: 4, reason: 'activity_retry' } }, enT, 'en'))
      .toBe('Retrying the model call (attempt 4)')
  })

  it('returns null for unknown events', () => {
    expect(formatStreamEvent('mystery', { data: {} }, enT, 'en')).toBeNull()
  })

  it('formats HITL question events in both locales', () => {
    expect(formatStreamEvent('human.requested', { data: { question: 'Deploy to prod?\nExtra context' } }, ruT, 'ru'))
      .toBe('❓ Deploy to prod? Extra context')
    expect(formatStreamEvent('human.requested', { data: { question: 'x'.repeat(120) } }, enT, 'en'))
      .toBe(`❓ ${'x'.repeat(97)}…`)
    expect(formatStreamEvent('human.reasked', { data: { policy: 'escalate' } }, enT, 'en'))
      .toBe('↻ Question re-asked (escalate)')
    expect(formatStreamEvent('human.answered', { data: { response: 'yes, deploy' } }, ruT, 'ru'))
      .toBe('✓ Отвечено: yes, deploy')
    expect(formatStreamEvent('human.cancelled', { data: {} }, enT, 'en'))
      .toBe('✗ Question cancelled')
    expect(formatStreamEvent('human.timed_out', { data: { timeout_policy: 'fallback' } }, ruT, 'ru'))
      .toBe('⏱ Время ожидания истекло')
    expect(formatStreamEvent('human.rejected', { data: {} }, enT, 'en'))
      .toBe('⚠ Question not deliverable')
  })
})

describe('normalizeRunStatus', () => {
  it('maps Temporal "canceled" onto the canonical "cancelled"', () => {
    expect(normalizeRunStatus('canceled')).toBe('cancelled')
    expect(normalizeRunStatus('cancelled')).toBe('cancelled')
    expect(normalizeRunStatus('completed')).toBe('completed')
    expect(normalizeRunStatus(undefined)).toBe('')
  })
})

describe('chatFragmentLine', () => {
  it('marks intermediate answers as fragment lines; short elapsed gets no prefix', () => {
    const line = chatFragmentLine(3, 'Checking the build status', ruT, 'ru')
    expect(line.fragment).toBe(true)
    expect(line.text).toBe('✎ Checking the build status')
  })

  it('prefixes long elapsed like the trace lines do', () => {
    expect(chatFragmentLine(61, 'Промежуточный вывод', ruT, 'ru').text).toBe('+1м 1с ✎ Промежуточный вывод')
  })

  it('caps the text beyond what the two-line CSS clamp will ever show', () => {
    const line = chatFragmentLine(undefined, 'x'.repeat(500), enT, 'en')
    expect(line.text).toBe(`✎ ${'x'.repeat(237)}…`)
    expect(line.fragment).toBe(true)
  })
})

describe('sendFileChip', () => {
  it('extracts path and name from a send_file start (the started event carries arguments)', () => {
    expect(sendFileChip({ tool: 'send_file', arguments: JSON.stringify({ path: '/workspace/27.1/oneslide/preview.png', note: 'x' }) }))
      .toEqual({ path: '/workspace/27.1/oneslide/preview.png', name: 'preview.png' })
  })

  it('returns null for other tools and unparseable shapes', () => {
    expect(sendFileChip({ tool: 'run_command', arguments: '{"path":"/x"}' })).toBeNull()
    expect(sendFileChip({ tool: 'send_file', arguments: '{broken' })).toBeNull()
    expect(sendFileChip({ tool: 'send_file', arguments: '{"note":"no path"}' })).toBeNull()
    expect(sendFileChip({})).toBeNull()
  })
})

// Quota queue: the run waits for a slot — the chat line must say so instead
// of claiming the run started.
describe('formatStreamEvent run.queued', () => {
  it('renders the queue state in the active locale', () => {
    const ruLine = formatStreamEvent('run.queued', { data: {} }, translator(ru) as any, 'ru')
    const enLine = formatStreamEvent('run.queued', { data: {} }, translator(en) as any, 'en')
    expect(ruLine).toContain('очереди')
    expect(enLine).toContain('Queued')
    expect(ruLine).not.toMatch(/run\.queued/)
  })
})

// Sidebar order: the conversation that spoke last floats up — recency is the
// last message, not the creation date.
describe('sortConversationsByActivity', () => {
  const conv = (id: string, created: string, last?: string) => ({
    id,
    createdAt: new Date(created),
    messages: last ? [{ timestamp: new Date(last) }] : [],
  })

  it('orders by the last message, not by creation', () => {
    const sorted = sortConversationsByActivity([
      conv('old-but-speaking', '2026-10-01T10:00:00Z', '2026-11-03T12:00:00Z'),
      conv('fresh-but-silent', '2026-11-03T11:00:00Z'),
      conv('quiet-yesterday', '2026-10-20T10:00:00Z', '2026-11-02T09:00:00Z'),
    ])
    // The old chat that spoke a minute ago leads; the chat created today but
    // never spoken in competes by creation (above yesterday's conversation).
    expect(sorted.map((c) => c.id)).toEqual(['old-but-speaking', 'fresh-but-silent', 'quiet-yesterday'])
  })

  it('an empty conversation competes by its creation time', () => {
    const sorted = sortConversationsByActivity([
      conv('empty-monday', '2026-11-03T08:00:00Z'),
      conv('empty-friday', '2026-10-31T08:00:00Z'),
    ])
    expect(sorted.map((c) => c.id)).toEqual(['empty-monday', 'empty-friday'])
  })
})
