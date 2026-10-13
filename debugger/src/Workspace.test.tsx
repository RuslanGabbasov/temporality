import { describe, expect, it } from 'vitest'
import { formatStreamEvent, normalizeRunStatus } from './Workspace'
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
    expect(pluralT(ruT, 'ru', 'chat.stream.tool_calls_n', 1)).toBe('1 вызов инструмента')
    expect(pluralT(ruT, 'ru', 'chat.stream.tool_calls_n', 2)).toBe('2 вызова инструментов')
    expect(pluralT(ruT, 'ru', 'chat.stream.tool_calls_n', 5)).toBe('5 вызовов инструментов')
    expect(pluralT(ruT, 'ru', 'chat.stream.tool_calls_n', 11)).toBe('11 вызовов инструментов')
    expect(pluralT(ruT, 'ru', 'chat.stream.tool_calls_n', 21)).toBe('21 вызов инструмента')
  })

  it('picks English singular/plural', () => {
    expect(pluralT(enT, 'en', 'chat.stream.tool_calls_n', 1)).toBe('1 tool call')
    expect(pluralT(enT, 'en', 'chat.stream.tool_calls_n', 3)).toBe('3 tool calls')
  })
})

describe('formatStreamEvent', () => {
  it('localizes every chat stream line type', () => {
    expect(formatStreamEvent('model.completed', { data: { turn: 2, total_tokens: 1000, latency_ms: 23300 } }, ruT, 'ru'))
      .toBe('Ход модели 2 · 1000 токенов · 23.3с')
    expect(formatStreamEvent('model.completed', { data: { turn: 2, total_tokens: 1000, latency_ms: 23300 } }, enT, 'en'))
      .toBe('Model turn 2 · 1000 tokens · 23.3s')

    expect(formatStreamEvent('turn.completed', { data: { turn: 1, tool_calls: 2 } }, ruT, 'ru'))
      .toBe('Ход 1 завершён · 2 вызова инструментов')
    expect(formatStreamEvent('turn.completed', { data: { turn: 1, tool_calls: 1 } }, enT, 'en'))
      .toBe('Turn 1 done · 1 tool call')

    expect(formatStreamEvent('tool.completed', { data: { name: 'run_command' } }, ruT, 'ru'))
      .toBe('Инструмент: run_command')

    expect(formatStreamEvent('run.completed', { data: { turns: 4 } }, ruT, 'ru'))
      .toBe('Завершено · 4 хода')

    expect(formatStreamEvent('run.failed', { data: { error: 'boom' } }, ruT, 'ru'))
      .toBe('Ошибка: boom')

    expect(formatStreamEvent('run.started', { data: { model: 'gpt' } }, ruT, 'ru'))
      .toBe('Запуск начат · модель gpt')

    expect(formatStreamEvent('knowledge.proposed', { data: { proposition: 'x'.repeat(80) } }, ruT, 'ru'))
      .toBe('Усвоено: ' + 'x'.repeat(60))
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
})

describe('normalizeRunStatus', () => {
  it('maps Temporal "canceled" onto the canonical "cancelled"', () => {
    expect(normalizeRunStatus('canceled')).toBe('cancelled')
    expect(normalizeRunStatus('cancelled')).toBe('cancelled')
    expect(normalizeRunStatus('completed')).toBe('completed')
    expect(normalizeRunStatus(undefined)).toBe('')
  })
})
