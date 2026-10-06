import { describe, expect, it } from 'vitest'
import { eventSummary, restoreMarkdownLines, unwrapJsonString } from './eventSummary'
import { foldDelegations } from './DelegationTree'
import { foldPlans } from './PlanGraph'
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

describe('plan rework folding', () => {
  const ts = (n: number) => new Date(Date.UTC(2026, 0, 1, 0, 0, n)).toISOString()
  const ev = (type: string, data: Record<string, unknown>, second = 0): ObservationEvent =>
    ({ type, data, occurred_at: ts(second) } as unknown as ObservationEvent)

  it('folds a rework round into one node with round and verdict', () => {
    const events = [
      ev('plan.task.started', { task_id: 'build', child_run_id: 'r/plan/01-build', agent_id: 'coder', round: 1 }, 1),
      ev('plan.task.completed', { task_id: 'build', child_run_id: 'r/plan/01-build', round: 1 }, 2),
      ev('plan.task.completed', { task_id: 'gate', child_run_id: 'r/plan/02-gate', round: 1, verdict: 'rework' }, 3),
      ev('plan.task.rejected', { task_id: 'build', rejected_by: 'gate', round: 1, feedback: 'fix it' }, 4),
      ev('plan.task.reopened', { task_id: 'build', round: 1 }, 5),
      ev('plan.task.started', { task_id: 'build', child_run_id: 'r/plan/01-build', agent_id: 'coder', round: 2 }, 6),
      ev('plan.task.completed', { task_id: 'build', child_run_id: 'r/plan/01-build', round: 2 }, 7),
      ev('plan.task.started', { task_id: 'gate', child_run_id: 'r/plan/02-gate', agent_id: 'reviewer', round: 2 }, 8),
      ev('plan.task.completed', { task_id: 'gate', child_run_id: 'r/plan/02-gate', round: 2, verdict: 'accept' }, 9),
    ]
    const nodes = foldDelegations(events)
    expect(nodes).toHaveLength(2)
    const build = nodes.find((n) => n.taskId === 'build')!
    expect(build.status).toBe('completed')
    expect(build.round).toBe(2)
    expect(build.verdict).toBeUndefined()
    const gate = nodes.find((n) => n.taskId === 'gate')!
    expect(gate.status).toBe('completed')
    expect(gate.round).toBe(2)
    expect(gate.verdict).toBe('accept')
  })

  it('marks a branch invalidated by exhausted rework', () => {
    const events = [
      ev('plan.task.started', { task_id: 'build', child_run_id: 'r/plan/01-build', agent_id: 'coder', round: 1 }, 1),
      ev('plan.task.completed', { task_id: 'build', child_run_id: 'r/plan/01-build', round: 1 }, 2),
      ev('plan.task.completed', { task_id: 'gate', child_run_id: 'r/plan/02-gate', round: 1, verdict: 'rework' }, 3),
      ev('plan.task.rejected', { task_id: 'build', rejected_by: 'gate', round: 1 }, 4),
      ev('plan.task.reopened', { task_id: 'build', round: 1 }, 5),
      ev('plan.task.started', { task_id: 'build', child_run_id: 'r/plan/01-build', round: 2 }, 6),
      ev('plan.task.completed', { task_id: 'build', child_run_id: 'r/plan/01-build', round: 2 }, 7),
      ev('plan.task.completed', { task_id: 'gate', child_run_id: 'r/plan/02-gate', round: 2, verdict: 'rework' }, 8),
      ev('plan.task.rejected', { task_id: 'build', rejected_by: 'gate', round: 2 }, 9),
      ev('plan.task.failed', { task_id: 'build', child_run_id: 'r/plan/01-build', error_type: 'rework_exhausted', error: 'rework exhausted', round: 2 }, 10),
      ev('plan.task.invalidated', { task_id: 'gate', reason: 'upstream_rework_exhausted' }, 11),
    ]
    const nodes = foldDelegations(events)
    const build = nodes.find((n) => n.taskId === 'build')!
    expect(build.status).toBe('failed')
    const gate = nodes.find((n) => n.childRunId === 'r/plan/02-gate')!
    // The gate node went back to 'running' on invalidation; its own child run
    // outcome still shows when expanded.
    expect(gate.status).toBe('running')
  })

  it('folds plan DAG statuses through a rework round in chronological order', () => {
    const events = [
      ev('plan.started', { operation_id: 'op', goal: 'g', tasks: [
        { id: 'build', agent_id: 'coder', depends_on: [] },
        { id: 'gate', agent_id: 'reviewer', depends_on: ['build'], review_of: ['build'] },
      ] }, 0),
      ev('plan.task.started', { operation_id: 'op', task_id: 'build', agent_id: 'coder', child_run_id: 'r/plan/01-build', round: 1 }, 1),
      ev('plan.task.completed', { operation_id: 'op', task_id: 'build', round: 1 }, 2),
      ev('plan.task.started', { operation_id: 'op', task_id: 'gate', agent_id: 'reviewer', round: 1, review_of: ['build'] }, 3),
      ev('plan.task.completed', { operation_id: 'op', task_id: 'gate', round: 1, verdict: 'rework' }, 4),
      ev('plan.task.rejected', { operation_id: 'op', task_id: 'build', rejected_by: 'gate', round: 1, feedback: 'deliver V2' }, 5),
      ev('plan.task.reopened', { operation_id: 'op', task_id: 'build', round: 1 }, 6),
      ev('plan.task.started', { operation_id: 'op', task_id: 'build', agent_id: 'coder', round: 2 }, 7),
      ev('plan.task.completed', { operation_id: 'op', task_id: 'build', round: 2 }, 8),
    ]
    const plans = foldPlans(events)
    expect(plans).toHaveLength(1)
    const build = plans[0].tasks.find((task) => task.id === 'build')!
    expect(build.status).toBe('completed')
    expect(build.round).toBe(2)
    expect(build.feedback).toBe('deliver V2')
    expect(build.rejectedBy).toBe('gate')
    const gate = plans[0].tasks.find((task) => task.id === 'gate')!
    expect(gate.status).toBe('completed')
    expect(gate.verdict).toBe('rework')
    expect(gate.reviewOf).toEqual(['build'])
  })

  it('resolves rework events without child_run_id through the task map', () => {
    const events = [
      ev('plan.task.started', { task_id: 'a', child_run_id: 'r/plan/01-a', agent_id: 'x', round: 1 }, 1),
      ev('plan.task.completed', { task_id: 'a', child_run_id: 'r/plan/01-a', round: 1 }, 2),
      ev('plan.task.reopened', { task_id: 'a', round: 1 }, 3),
      ev('plan.task.started', { task_id: 'a', child_run_id: 'r/plan/01-a', agent_id: 'x', round: 2 }, 4),
    ]
    const nodes = foldDelegations(events)
    expect(nodes).toHaveLength(1)
    expect(nodes[0].taskId).toBe('a')
    expect(nodes[0].status).toBe('running')
    expect(nodes[0].round).toBe(2)
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
