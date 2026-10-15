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

describe('model.nudged summary', () => {
  it('localizes the manual nudge of a stuck model call', () => {
    const info = eventSummary(event('model.nudged', { turn: 2, actor_id: 'human-1' }), t)
    expect(info.icon).toBe('↻')
    expect(info.label).toBe('runs.event.model_nudged')
    expect(info.detail).toBe('runs.event.model_nudged_detail')
  })
})

describe('model.attempt summary', () => {
  it('surfaces a Temporal retry of the model call', () => {
    const info = eventSummary(event('model.attempt', { turn: 3, attempt: 2, reason: 'activity_retry' }), t)
    expect(info.icon).toBe('↻')
    expect(info.label).toBe('runs.event.model_attempt')
    expect(info.detail).toContain('runs.event.model_attempt_retry')
    expect(info.detail).toContain('runs.event.model_attempt_n')
  })

  it('surfaces the stream fallback with its lifetime and error', () => {
    const info = eventSummary(event('model.attempt', { turn: 3, attempt: 1, reason: 'stream_fallback', stream_ms: 91200, error: 'context deadline exceeded' }), t)
    expect(info.detail).toContain('runs.event.model_attempt_fallback')
    expect(info.detail).toContain('91.2s')
    expect(info.detail).toContain('context deadline exceeded')
  })

  it('surfaces the first-token watchdog timeout as a fresh-connection retry', () => {
    const info = eventSummary(event('model.attempt', { turn: 3, attempt: 1, reason: 'stream_timeout', stream_ms: 120400, error: 'first token timeout: no generated tokens for 2m0s' }), t)
    expect(info.detail).toContain('runs.event.model_attempt_timeout')
    expect(info.detail).toContain('120.4s')
    expect(info.detail).toContain('first token timeout')
  })
})

describe('knowledge, hint and skill lifecycle summaries', () => {
  it('localizes knowledge lifecycle events', () => {
    expect(eventSummary(event('knowledge.used', { knowledge_id: 'K1', hint_id: 'h1' }), t).label).toBe('runs.event.knowledge_used')
    expect(eventSummary(event('knowledge.confirmed', { knowledge_id: 'K1' }), t).label).toBe('runs.event.knowledge_confirmed')
    expect(eventSummary(event('knowledge.challenged', { knowledge_id: 'K1' }), t).label).toBe('runs.event.knowledge_challenged')
    expect(eventSummary(event('knowledge.corrected', { knowledge_id: 'K1' }), t).label).toBe('runs.event.knowledge_corrected')
    const invalidated = eventSummary(event('knowledge.invalidated', { knowledge_id: 'K1', reason: 'environment changed' }), t)
    expect(invalidated.label).toBe('runs.event.knowledge_invalidated')
    expect(invalidated.detail).toContain('environment changed')
    expect(eventSummary(event('knowledge.superseded', { knowledge_id: 'K1' }), t).label).toBe('runs.event.knowledge_superseded')
    expect(eventSummary(event('knowledge.disproved', { knowledge_id: 'K1', reason: 'wrong' }), t).label).toBe('runs.event.knowledge_disproved')
    const linked = eventSummary(event('knowledge.linked', { knowledge_id: 'K1', target_id: 'K2', relation: 'related_to' }), t)
    expect(linked.label).toBe('runs.event.knowledge_linked')
    expect(linked.detail).toContain('K1 → K2')
  })

  it('localizes hint lifecycle events and outcome values', () => {
    const offered = eventSummary(event('hint.offered', { hint_id: 'h1', knowledge_id: 'K1', state: 'confirmed', matched_by: 'lexical' }), t)
    expect(offered.label).toBe('runs.event.hint_offered')
    expect(offered.detail).toContain('lexical')
    expect(eventSummary(event('hint.used', { hint_id: 'h1', knowledge_id: 'K1' }), t).label).toBe('runs.event.hint_used')
    expect(eventSummary(event('hint.ignored', { hint_id: 'h1', knowledge_id: 'K1' }), t).label).toBe('runs.event.hint_ignored')
    const outcome = eventSummary(event('hint.outcome', { hint_id: 'h1', knowledge_id: 'K1', outcome: 'helpful' }), t)
    expect(outcome.label).toBe('runs.event.hint_outcome')
    expect(outcome.detail).toBe('runs.event.hint_outcome_helpful')
  })

  it('shows the embedded proposition instead of ids for hint.offered', () => {
    const offered = eventSummary(event('hint.offered', { hint_id: 'h1', knowledge_id: 'ext/k1', proposition: 'Запускать тесты через CI, а не локально', matched_by: ['term:тесты'] }), t)
    expect(offered.detail).toBe('Запускать тесты через CI, а не локально')
  })

  it('falls back to cleaned matched_by terms without a proposition', () => {
    const offered = eventSummary(event('hint.offered', { hint_id: 'h1', knowledge_id: 'ext/k1', matched_by: ['term:не', 'term:узел'] }), t)
    expect(offered.detail).toBe('runs.matched_by: не · узел')
  })

  it('shows the embedded proposition for knowledge.used', () => {
    const used = eventSummary(event('knowledge.used', { hint_id: 'h1', knowledge_id: 'ext/k1', proposition: 'GraphMap слушает на 8099' }), t)
    expect(used.detail).toBe('GraphMap слушает на 8099')
  })

  it('resolves historical knowledge ids through the resolver', () => {
    const resolve = (id: string) => (id === 'ext/k1' ? 'Стандартный путь сборки — docker compose' : undefined)
    const used = eventSummary(event('knowledge.used', { hint_id: 'h1', knowledge_id: 'ext/k1' }), t, resolve)
    expect(used.detail).toBe('Стандартный путь сборки — docker compose')
    const unknown = eventSummary(event('knowledge.used', { hint_id: 'h2', knowledge_id: 'ext/missing' }), t, resolve)
    expect(unknown.detail).toBe('ext/missing')
  })

  it('shows the proposition on confirmed and challenged knowledge events', () => {
    const confirmed = eventSummary(event('knowledge.confirmed', { knowledge_id: 'ext/k1', proposition: 'GraphMap слушает на 8099', rule: 'extraction-reverification.v1' }), t)
    expect(confirmed.detail).toBe('GraphMap слушает на 8099')
    const challenged = eventSummary(event('knowledge.challenged', { knowledge_id: 'ext/k2', proposition: 'Fetch принимает -limit, а не --count', rule: 'aging.v1', reason: 'unconfirmed for 21 days' }), t)
    expect(challenged.detail).toBe('Fetch принимает -limit, а не --count')
    const legacy = eventSummary(event('knowledge.challenged', { knowledge_id: 'ext/k3', reason: 'unconfirmed for 30 days' }), t)
    expect(legacy.detail).toBe('unconfirmed for 30 days')
  })

  it('localizes skill lifecycle events with version and change summary', () => {
    const proposed = eventSummary(event('skill.proposed', { skill_id: 'deploy', skill_name: 'Deploy service', version: 2, origin: 'agent-proposal', change_summary: 'add fallback check' }), t)
    expect(proposed.label).toBe('runs.event.skill_proposed')
    expect(proposed.detail).toContain('Deploy service v2')
    expect(proposed.detail).toContain('add fallback check')
    const applied = eventSummary(event('skill.applied', { skill_id: 'deploy', skill_name: 'Deploy service', version: 2 }), t)
    expect(applied.label).toBe('runs.event.skill_applied')
    expect(applied.color).toBe('#9ece6a')
  })
})

describe('trace row informativeness', () => {
  const resolve = (id: string) => {
    if (id === 'ext/k1') return 'При изменении API нужна миграция клиента'
    if (id === 'ext/k2') return 'Для интеграционных тестов нужен Docker'
    return undefined
  }

  it('shows the agent and short child path on delegation rows', () => {
    const started = eventSummary(event('delegation.started', { agent_id: 'qa', child_run_id: 'root-20261013/delegate/01', ordinal: 1, depth: 1 }), t)
    expect(started.detail).toBe('→ qa · delegate/01')
    const completed = eventSummary(event('delegation.completed', { agent_id: 'qa', child_run_id: 'root-20261013/delegate/01', child_status: 'completed', turns: 7 }), t)
    expect(completed.detail).toBe('qa · 7 runs.turns')
    const failedChild = eventSummary(event('delegation.completed', { agent_id: 'qa', child_run_id: 'x/delegate/02', child_status: 'failed', turns: 2 }), t)
    expect(failedChild.detail).toContain('failed')
  })

  it('resolves replacement knowledge on corrected and superseded rows', () => {
    const corrected = eventSummary(event('knowledge.corrected', { knowledge_id: 'ext/k1', replacement_id: 'ext/k2' }), t, resolve)
    expect(corrected.detail).toContain('При изменении API нужна миграция клиента')
    expect(corrected.detail).toContain('→ Для интеграционных тестов нужен Docker')
    const superseded = eventSummary(event('knowledge.superseded', { knowledge_id: 'ext/k1', replacement_id: 'ext/k2' }), t, resolve)
    expect(superseded.icon).toBe('⇄')
    expect(superseded.detail).toContain('→ Для интеграционных тестов нужен Docker')
  })

  it('shortens unresolvable ids instead of showing full guids', () => {
    const corrected = eventSummary(event('knowledge.corrected', { knowledge_id: 'ext/aaaaaaaaaaaaaaaaaaaaaaaa', replacement_id: 'ext/bbbbbbbbbbbbbbbbbbbbbbbb' }), t)
    expect(corrected.detail).toContain('ext/aaaa…')
    expect(corrected.detail).toContain('→ ext/bbbb…')
  })

  it('shows proposition and scope on promoted knowledge', () => {
    const promoted = eventSummary(event('knowledge.promoted', { knowledge_id: 'ext/k1', scope_kind: 'organization' }), t, resolve)
    expect(promoted.detail).toContain('При изменении API нужна миграция клиента')
    expect(promoted.detail).toContain('→ runs.event.scope_organization')
  })

  it('resolves both endpoints of a knowledge link', () => {
    const linked = eventSummary(event('knowledge.linked', { knowledge_id: 'ext/k1', target_id: 'ext/k2', relation: 'complements' }), t, resolve)
    expect(linked.detail).toBe('complements · При изменении API нужна миграция клиента → Для интеграционных тестов нужен Docker')
  })

  it('shows the judged proposition on hint outcomes', () => {
    const helpful = eventSummary(event('hint.outcome', { hint_id: 'h1', knowledge_id: 'ext/k1', outcome: 'helpful' }), t, resolve)
    expect(helpful.detail).toContain('runs.event.hint_outcome_helpful')
    expect(helpful.detail).toContain('При изменении API нужна миграция клиента')
  })

  it('shows the answer preview on agent summary rows', () => {
    const summary = eventSummary(event('agent.summary', { kind: 'narrative', answer: 'Проверка завершена:  covering  все тесты зелёные,  два замечания отправлены на доработку.' }), t)
    expect(summary.detail).toContain('Проверка завершена')
    expect(summary.detail).not.toContain('narrative')
  })

  it('falls back to the kind when the summary has no answer', () => {
    const summary = eventSummary(event('agent.summary', { kind: 'narrative' }), t)
    expect(summary.detail).toBe('narrative')
  })

  it('shows result size on completed MCP calls', () => {
    const small = eventSummary(event('mcp.call.completed', { tool: 'graphmap__connect', server: 'graphmap', result_bytes: 512 }), t)
    expect(small.detail).toContain('512 B')
    const large = eventSummary(event('mcp.call.completed', { tool: 'fetch', server: 'web', result_bytes: 250_000 }), t)
    expect(large.detail).toContain('244.1 KB')
  })

  it('shows the agent on plan task rows', () => {
    const started = eventSummary(event('plan.task.started', { task_id: 't1', agent_id: 'coder', round: 1 }), t)
    expect(started.detail).toBe('t1 · coder')
    const rework = eventSummary(event('plan.task.started', { task_id: 't2', agent_id: 'reviewer', round: 2, review_of: ['t1'] }), t)
    expect(rework.detail).toBe('t2 · reviewer · ×2 · ⌾ t1')
  })

  it('previews delegate tool calls with the target agent and prompt gist', () => {
    const started = eventSummary(event('tool.started', { tool: 'delegate', arguments: JSON.stringify({ agent_id: 'qa', prompt: 'Проверь миграции  в отдельной  ветке и\nверни отчёт' }) }), t)
    expect(started.detail).toBe('→ qa · Проверь миграции в отдельной ветке и верни отчёт')
  })
})

describe('team run summaries', () => {
  it('localizes team lifecycle events', () => {
    const started = eventSummary(event('team.started', { team_id: 'code-delivery', team_name: 'Code Delivery', version: '1.0.0', goal: 'ship the feature', bindings: { coder: 'coder' } }), t)
    expect(started.label).toBe('runs.event.team_started')
    expect(started.detail).toContain('Code Delivery')
    expect(started.detail).toContain('ship the feature')
    expect(started.detail).toContain('v1.0.0')

    const bound = eventSummary(event('slot.bound', { slot_id: 'coder', agent_id: 'qa-agent', mode: 'matched' }), t)
    expect(bound.label).toBe('runs.event.slot_bound')
    expect(bound.detail).toContain('coder → qa-agent')
    expect(bound.detail).toContain('runs.event.slot_mode_matched')

    const completed = eventSummary(event('team.completed', { team_id: 'code-delivery', version: '1.0.0', status: 'completed', total_tokens: 175, cost_usd: 0.0123, rework_rounds: 1 }), t)
    expect(completed.label).toBe('runs.event.team_completed')
    expect(completed.detail).toContain('175 tok')
    expect(completed.detail).toContain('$0.0123')
    expect(completed.detail).toContain('runs.event.team_rework_rounds')

    const failed = eventSummary(event('team.failed', { team_id: 'code-delivery', version: '1.0.0', error: 'child workflow execution error', step: 'coder', child_run_id: 'root/team/01' }), t)
    expect(failed.label).toBe('runs.event.team_failed')
    expect(failed.color).toBe('#f7768e')
    expect(failed.detail).toContain('coder')
    expect(failed.detail).toContain('child workflow execution error')
    expect(failed.detail).toContain('→ 01')
  })

  it('localizes pseudo tool names team and plan in tool events', () => {
    expect(eventSummary(event('tool.started', { tool: 'team' }), t).label).toBe('runs.event.team')
    expect(eventSummary(event('tool.completed', { tool: 'team' }), t).label).toBe('runs.event.team')
    expect(eventSummary(event('tool.completed', { tool: 'plan' }), t).label).toBe('runs.event.plan')
    expect(eventSummary(event('tool.failed', { tool: 'team', error: 'boom' }), t).label).toBe('runs.event.team')
    // Real tool identifiers stay verbatim.
    expect(eventSummary(event('tool.completed', { tool: 'run_command', exit_code: 0 }), t).label).toBe('run_command')
    expect(eventSummary(event('tool.completed', { tool: 'mcp__graphmap__connect' }), t).label).toBe('mcp__graphmap__connect')
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

  it('folds a policy-limited executor as incomplete and skips its dependent', () => {
    const events = [
      ev('plan.started', { operation_id: 'op', goal: 'g', tasks: [
        { id: 'build', agent_id: 'coder', depends_on: [] },
        { id: 'ship', agent_id: 'devops', depends_on: ['build'] },
      ] }, 0),
      ev('plan.task.started', { operation_id: 'op', task_id: 'build', agent_id: 'coder', child_run_id: 'r/plan/01-build', round: 1 }, 1),
      ev('plan.task.incomplete', { operation_id: 'op', task_id: 'build', child_run_id: 'r/plan/01-build', child_status: 'turn_limit', turns: 17, round: 1 }, 2),
      ev('plan.task.skipped', { operation_id: 'op', task_id: 'ship', reason: 'upstream_incomplete', blocked_by: 'build' }, 3),
    ]
    const plans = foldPlans(events)
    expect(plans).toHaveLength(1)
    const build = plans[0].tasks.find((task) => task.id === 'build')!
    expect(build.status).toBe('incomplete')
    expect(build.childStatus).toBe('turn_limit')
    expect(build.turns).toBe(17)
    const ship = plans[0].tasks.find((task) => task.id === 'ship')!
    expect(ship.status).toBe('skipped')
    expect(ship.reason).toBe('upstream_incomplete')
    expect(ship.blockedBy).toBe('build')
    // The delegation tree marks the same child run incomplete too.
    const nodes = foldDelegations(events)
    const node = nodes.find((n) => n.childRunId === 'r/plan/01-build')!
    expect(node.status).toBe('incomplete')
  })

  it('marks a delegation node incomplete when the child finished from a forced finale', () => {
    const events = [
      ev('delegation.started', { child_run_id: 'r/delegate/01', agent_id: 'helper', ordinal: 1 }, 1),
      ev('delegation.completed', { child_run_id: 'r/delegate/01', agent_id: 'helper', child_status: 'time_limit', turns: 3 }, 2),
    ]
    const nodes = foldDelegations(events)
    expect(nodes[0].status).toBe('incomplete')
  })
})

describe('quality loop and saga summaries', () => {
  it('localizes saga register and attach events', () => {
    const registered = eventSummary(event('saga.registered', { saga_id: 'saga-api-migration-a1b2c3d4', title: 'Миграция API', origin: 'agent-tool' }), t)
    expect(registered.label).toBe('runs.event.saga_registered')
    expect(registered.detail).toContain('Миграция API')
    const attached = eventSummary(event('saga.attached', { saga_id: 'saga-x', task_id: 'task-12', note: 'продолжение работы' }), t)
    expect(attached.label).toBe('runs.event.saga_attached')
    expect(attached.detail).toContain('task-12')
  })

  it('localizes operation reconcile effects', () => {
    const occurred = eventSummary(event('operation.reconciled', { operation_id: 'op-1', effect: 'occurred', note: 'проверено вручную' }), t)
    expect(occurred.label).toBe('runs.event.operation_reconciled')
    expect(occurred.detail).toContain('runs.event.reconcile_effect_occurred')
    const none = eventSummary(event('operation.reconciled', { operation_id: 'op-1', effect: 'none' }), t)
    expect(none.detail).toContain('runs.event.reconcile_effect_none')
  })

  it('localizes artifact read events with a human chunk size', () => {
    const read = eventSummary(event('artifact.read', { artifact_id: 'coding-standard', title: 'Стандарт кодирования', checksum: 'sha256:abc', offset: 0, bytes: 12698 }), t)
    expect(read.label).toBe('runs.event.artifact_read')
    expect(read.detail).toContain('coding-standard')
    expect(read.detail).toContain('12.4 KiB')
  })

  it('localizes hitl verdict types with matching colors', () => {
    const rejected = eventSummary(event('hitl.verdict.recorded', { type: 'rejected', answered_by: 'ruslan' }), t)
    expect(rejected.label).toBe('runs.event.hitl_verdict')
    expect(rejected.detail).toContain('runs.event.hitl_verdict_rejected')
    expect(rejected.color).toBe('#f7768e')
    const corrected = eventSummary(event('hitl.verdict.recorded', { type: 'corrected', answered_by: 'ruslan' }), t)
    expect(corrected.detail).toContain('runs.event.hitl_verdict_corrected')
    expect(corrected.color).toBe('#e6b85c')
  })

  it('localizes evaluation completion with counts colored by failures', () => {
    const failed = eventSummary(event('agent.evaluation.completed', { agent_name: 'developer', version: 3, passed: 5, failed: 2, tokens_in: 100, tokens_out: 50 }), t)
    expect(failed.label).toBe('runs.event.evaluation_completed')
    expect(failed.detail).toContain('developer')
    expect(failed.detail).toContain('v3')
    expect(failed.color).toBe('#f7768e')
    const green = eventSummary(event('agent.evaluation.completed', { agent_name: 'developer', version: 3, passed: 7, failed: 0 }), t)
    expect(green.color).toBe('#9ece6a')
  })

  it('localizes gate lifecycle events with cause and justification', () => {
    const blocked = eventSummary(event('agent.evaluation.gate.blocked', { agent_id: 'lead', proposal_id: 7, cause: 'gate_red', evaluation_run_id: 'run-uuid-1234' }), t)
    expect(blocked.label).toBe('runs.event.eval_gate_blocked')
    expect(blocked.detail).toContain('lead')
    expect(blocked.detail).toContain('#7')
    expect(blocked.color).toBe('#f7768e')
    const overridden = eventSummary(event('agent.evaluation.gate.overridden', { agent_id: 'lead', proposal_id: 7, cause: 'gate_red', override_justification: 'hotfix window' }), t)
    expect(overridden.label).toBe('runs.event.eval_gate_overridden')
    expect(overridden.detail).toContain('hotfix window')
    expect(overridden.color).toBe('#e6b85c')
  })

  it('localizes judge configuration and calibration', () => {
    const configured = eventSummary(event('agent.evaluation.judge.configured', { model: 'gpt-4o', configured: true, source: 'env' }), t)
    expect(configured.label).toBe('runs.event.judge_configured')
    expect(configured.detail).toContain('gpt-4o')
    expect(configured.detail).toContain('runs.event.judge_enabled')
    const calibrated = eventSummary(event('agent.evaluation.judge.calibrated', { agent_id: 'lead', judge_model: 'gpt-4o', agreement_rate: 0.75, degraded: false }), t)
    expect(calibrated.label).toBe('runs.event.judge_calibrated')
    expect(calibrated.detail).toContain('75%')
    const degraded = eventSummary(event('agent.evaluation.judge.calibrated', { agent_id: 'lead', agreement_rate: 0.6, degraded: true }), t)
    expect(degraded.color).toBe('#e6b85c')
  })

  it('localizes schedule changes and eval case candidates', () => {
    const schedule = eventSummary(event('agent.evaluation.schedule.changed', { interval_hours: 24 }), t)
    expect(schedule.label).toBe('runs.event.quality_schedule_changed')
    expect(schedule.detail).toContain('runs.event.interval_hours')
    const created = eventSummary(event('eval.case_candidate.created', { agent_id: 'lead', run_id: 'r1', source: 'human_corrected', redaction_applied: true }), t)
    expect(created.label).toBe('runs.event.eval_case_created')
    expect(created.detail).toContain('lead')
    expect(created.detail).toContain('runs.event.redaction_applied')
    const decided = eventSummary(event('eval.case_candidate.decided', { candidate_id: 5, verdict: 'rejected', reviewed_by: 'ruslan' }), t)
    expect(decided.label).toBe('runs.event.eval_case_decided')
    expect(decided.detail).toContain('runs.event.eval_verdict_rejected')
    expect(decided.color).toBe('#f7768e')
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
