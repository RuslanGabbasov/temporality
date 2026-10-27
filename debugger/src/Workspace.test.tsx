import { describe, expect, it } from 'vitest'
import { sendFileChip, formatStreamEvent, normalizeRunStatus } from './Workspace'
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
      .toBe('+12m 35s Ход 2 · 1.2k ток · 23.3с')
    expect(formatStreamEvent('model.completed', { data: { turn: 2, total_tokens: 980, latency_ms: 4200 } }, enT, 'en'))
      .toBe('Turn 2 · 980 tok · 4.2s')
  })

  it('adds the live-measured thinking time when reasoning deltas were seen', () => {
    expect(formatStreamEvent('model.completed', { data: { turn: 3, total_tokens: 2100, latency_ms: 14000, thinking_s: 6.38, elapsed_s: 20 } }, ruT, 'ru'))
      .toBe('+20s Ход 3 · 💭 6.4с · 2.1k ток · 14.0с')
    expect(formatStreamEvent('model.completed', { data: { turn: 3, total_tokens: 2100, latency_ms: 14000, thinking_s: 6.38 } }, enT, 'en'))
      .toBe('Turn 3 · 💭 6.4s · 2.1k tok · 14.0s')
  })

  it('keeps silent recovery markers and knowledge lines', () => {
    expect(formatStreamEvent('model.attempt', { data: { turn: 3, attempt: 2, reason: 'stream_timeout' } }, ruT, 'ru'))
      .toBe('Провайдер молчит до первого токена — повтор на новом соединении (попытка 2)')
    expect(formatStreamEvent('knowledge.proposed', { data: { proposition: 'x'.repeat(80) } }, ruT, 'ru'))
      .toBe('Усвоено: ' + 'x'.repeat(60))
  })

  it('turn.completed and tool.completed are silent in the compact trace', () => {
    expect(formatStreamEvent('turn.completed', { data: { turn: 1, tool_calls: 3 } }, enT, 'en')).toBeNull()
    expect(formatStreamEvent('tool.completed', { data: { name: 'run_command' } }, enT, 'en')).toBeNull()
  })

  it('tool.started shows what the call is doing; failures are visible', () => {
    const args = JSON.stringify({ command: ['go', 'test', './...'] })
    expect(formatStreamEvent('tool.started', { data: { tool: 'run_command', arguments: args, elapsed_s: 61 } }, ruT, 'ru'))
      .toBe('+1m 1s ⚙ run_command: go test ./...')
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

describe('sendFileChip', () => {
  it('extracts path and name from a send_file completion', () => {
    expect(sendFileChip({ tool: 'send_file', arguments: JSON.stringify({ path: '/workspace/27.1/oneslide/preview.png', note: 'x' }) }))
      .toEqual({ path: '/workspace/27.1/oneslide/preview.png', name: 'preview.png' })
  })

  it('returns null for other tools and unparseable shapes', () => {
    expect(sendFileChip({ tool: 'run_command', arguments: '{"path":"/x"}' })).toBeNull()
    expect(sendFileChip({ tool: 'send_file', arguments: '{broken' })).toBeNull()
    expect(sendFileChip({ tool: 'send_file', arguments: '{"note":"no path"}' })).toBeNull()
    expect(sendFileChip({}, {})).toBeNull()
  })
})
