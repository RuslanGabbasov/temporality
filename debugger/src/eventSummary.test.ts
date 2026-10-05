import { describe, expect, it } from 'vitest'
import { eventSummary, restoreMarkdownLines, unwrapJsonString } from './eventSummary'
import type { ObservationEvent } from './observationApi'

const t = (key: string) => key
const event = (type: string, data: Record<string, unknown>): ObservationEvent =>
  ({ type, data } as unknown as ObservationEvent)

const COLLAPSED_QA_REPORT = '## Отчёт QA-субагента: проверка lighthouse ### Статус: завершено --- ### 1. Что сделано | Шаг | Результат | Доказательство | |---|---|---| | Клонирование | OK | журнал | | Проверка | OK | отчёт |'

describe('unwrapJsonString', () => {
  it('unwraps a single JSON-string layer', () => {
    expect(unwrapJsonString('"{\\"connected\\":true,\\"mapId\\":\\"2\\"}"')).toBe('{"connected":true,"mapId":"2"}')
  })

  it('unwraps doubly encoded MCP results', () => {
    const once = JSON.stringify(JSON.stringify({ id: 'graphmap-qa-workflow', name: 'QA' }))
    expect(unwrapJsonString(once)).toBe('{"id":"graphmap-qa-workflow","name":"QA"}')
  })

  it('leaves plain JSON and text untouched', () => {
    expect(unwrapJsonString('{"connected":true}')).toBe('{"connected":true}')
    expect(unwrapJsonString('plain output')).toBe('plain output')
  })
})

describe('tool.completed summary', () => {
  it('marks MCP and kernel tools without exit_code as successful', () => {
    const info = eventSummary(event('tool.completed', { tool: 'mcp__graphmap__connect', output: JSON.stringify({ connected: true }) }), t)
    expect(info.icon).toBe('✓')
    expect(info.color).toBe('#9ece6a')
    expect(info.detail).toContain('{"connected":true}')
  })

  it('marks skill tools without exit_code as successful', () => {
    const info = eventSummary(event('tool.completed', { tool: 'skill_inspect', output: JSON.stringify(JSON.stringify({ id: 'x' })) }), t)
    expect(info.icon).toBe('✓')
    expect(info.detail).toContain('{"id":"x"}')
  })

  it('keeps a non-zero run_command exit code an error', () => {
    const info = eventSummary(event('tool.completed', { tool: 'run_command', exit_code: 1, output: 'boom' }), t)
    expect(info.icon).toBe('✗')
    expect(info.color).toBe('#f7768e')
    expect(info.detail).toContain('exit 1')
  })

  it('marks a zero run_command exit code as successful', () => {
    const info = eventSummary(event('tool.completed', { tool: 'run_command', exit_code: 0, output: 'ok' }), t)
    expect(info.icon).toBe('✓')
    expect(info.detail).toContain('exit 0')
  })
})

describe('restoreMarkdownLines', () => {
  it('keeps texts that already have line structure unchanged', () => {
    const text = '## Заголовок\n\n| a | b |\n|---|---|\n| 1 | 2 |'
    expect(restoreMarkdownLines(text)).toBe(text)
  })

  it('keeps plain single-line prose unchanged', () => {
    expect(restoreMarkdownLines('Всё проверено, ошибок нет.')).toBe('Всё проверено, ошибок нет.')
  })

  it('returns empty text unchanged', () => {
    expect(restoreMarkdownLines('')).toBe('')
  })

  it('restores headings, hr and table rows in a collapsed QA report', () => {
    const restored = restoreMarkdownLines(COLLAPSED_QA_REPORT)
    const lines = restored.split('\n')
    expect(lines[0]).toBe('## Отчёт QA-субагента: проверка lighthouse')
    expect(lines).toContain('### Статус: завершено')
    expect(lines).toContain('---')
    expect(lines).toContain('### 1. Что сделано')
    expect(lines).toContain('| Шаг | Результат | Доказательство |')
    expect(lines).toContain('|---|---|---|')
    expect(lines).toContain('| Клонирование | OK | журнал |')
    expect(lines).toContain('| Проверка | OK | отчёт |')
  })

  it('restores bullet and numbered lists', () => {
    const restored = restoreMarkdownLines('Итог: - пункт один - пункт два 1. первый шаг 2. второй шаг')
    expect(restored.split('\n')).toEqual([
      'Итог:',
      '- пункт один',
      '- пункт два',
      '1. первый шаг',
      '2. второй шаг',
    ])
  })

  it('does not split a numbered heading from its marker', () => {
    const restored = restoreMarkdownLines('введение ### 1. Что сделано и проверено')
    expect(restored).toBe('введение\n### 1. Что сделано и проверено')
  })
})
