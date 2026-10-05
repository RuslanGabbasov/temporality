import { describe, expect, it } from 'vitest'
import { restoreMarkdownLines } from './eventSummary'

const COLLAPSED_QA_REPORT = '## Отчёт QA-субагента: проверка lighthouse ### Статус: завершено --- ### 1. Что сделано | Шаг | Результат | Доказательство | |---|---|---| | Клонирование | OK | журнал | | Проверка | OK | отчёт |'

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
