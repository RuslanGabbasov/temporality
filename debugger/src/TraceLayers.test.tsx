import { renderToStaticMarkup } from 'react-dom/server'
import { afterEach, describe, expect, it, vi } from 'vitest'
import TraceLayers, { TraceEventRow } from './TraceLayers'
import { I18nProvider } from './i18n'
import { ru } from './locales/ru'
import type { ObservationEvent } from './observationApi'

function stubLocalStorage() {
  vi.stubGlobal('localStorage', {
    getItem: () => null,
    setItem: () => {},
    removeItem: () => {},
  })
}

afterEach(() => {
  vi.unstubAllGlobals()
})

const t = (key: string, vars?: Record<string, string>) => {
  let text = ru[key] ?? key
  if (vars) {
    for (const [name, value] of Object.entries(vars)) text = text.replace(`{{${name}}}`, value)
  }
  return text
}

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

const renderLayers = (events: ObservationEvent[]) => {
  stubLocalStorage()
  return renderToStaticMarkup(
    <I18nProvider>
      <TraceLayers events={events} locale="ru" t={t} project="p" />
    </I18nProvider>,
  )
}

describe('TraceLayers (three layers, §4)', () => {
  it('renders the timeline row: human title, fact, duration, status — no type names (§4.1)', () => {
    const html = renderLayers([
      ev('tool.completed', { tool: 'run_command', arguments: JSON.stringify({ command: ['go', 'test'] }), exit_code: 0, latency_ms: 9200 }, 1),
    ])
    expect(html).toContain('run_command')
    expect(html).toContain('go test')
    expect(html).toContain('exit 0')
    expect(html).toContain('9.2s')
    expect(html).not.toContain('tool.completed')
  })

  it('renders the «Ход N» group folded by default (§5.1)', () => {
    const html = renderLayers([ev('turn.started', { turn: 1 }, 1), ev('turn.completed', { turn: 1 }, 2)])
    expect(html).toContain('Ходы агента')
    expect(html).not.toContain('Ход 1')
  })

  it('renders the hint aggregate «6 подсказок, 2 использовано» (§5.6)', () => {
    const html = renderLayers([
      ev('hint.offered', { hint_id: 'h1' }, 1),
      ev('hint.offered', { hint_id: 'h2' }, 2),
      ev('hint.used', { hint_id: 'h1' }, 3),
    ])
    expect(html).toContain('Подсказки и знания')
    expect(html).toContain('3 подсказок, 1 применено')
  })

  it('renders the approval pair as one row with the decision (§5.5)', () => {
    const html = renderLayers([
      ev('approval.requested', { operation_id: 'op-1', action: 'run_command' }, 1),
      ev('approval.granted', { operation_id: 'op-1', approver: 'ruslan' }, 2),
    ])
    expect(html).toContain('Согласование: ')
    expect(html).toContain('Подтверждено')
    expect(html).not.toContain('approval.granted')
  })

  it('renders the ops noise group folded (§5.7)', () => {
    const html = renderLayers([ev('storage.snapshot', {}, 1), ev('notification.sent', {}, 2)])
    expect(html).toContain('Эксплуатационные события')
    expect(html).not.toContain('Уведомление')
  })

  it('the raw layer is collapsed by default and absent from the markup (§4.3, K-1)', () => {
    const html = renderLayers([ev('run.completed', { turns: 3 }, 1)])
    expect(html).not.toContain('Сырое событие')
    expect(html).not.toContain('"turns":3')
  })

  it('P-2: secret values never render; the row says name/status only (DoD-2)', () => {
    const html = renderLayers([ev('secret.value_set', { secret: 'API_TOKEN', value: 'hunter2' }, 1)])
    expect(html).toContain('API_TOKEN')
    expect(html).toContain('значение задано')
    expect(html).not.toContain('hunter2')
    expect(html).not.toContain('Сырое событие')
  })

  it('streaming types do not render as rows (§5.2)', () => {
    const html = renderLayers([ev('model.text_delta', { text: 'chunk', turn: 1 }, 1)])
    expect(html).not.toContain('chunk')
  })

  it('chips render as toggle buttons with aria-pressed (§8, DoD-9)', () => {
    const html = renderLayers([ev('tool.completed', { tool: 'read_file', exit_code: 0 }, 1)])
    expect(html).toContain('aria-pressed="true"')
    expect(html).toContain('Только ошибки')
    expect(html).toContain('Только инструменты')
    expect(html).toContain('Только решения человека')
  })

  it('a11y: row and raw disclosers carry aria-expanded and aria-labels (DoD-9)', () => {
    const { renderToStaticMarkup: render } = { renderToStaticMarkup }
    stubLocalStorage()
    // TraceEventRow is exported so the a11y contract is pinnable without
    // folding: the discloser lives in the row body.
    const html = render(
      <I18nProvider>
        <TraceEventRow event={ev('run.failed', { error: 'boom' }, 1)} locale="ru" t={t} project="p" />
      </I18nProvider>,
    )
    expect(html).toContain('aria-expanded="false"')
    expect(html).toContain('aria-label="Показать детали события: Запуск завершился ошибкой"')
    expect(html).toContain('type="button"')
  })

  it('the empty window shows the empty hint (§4.1)', () => {
    const html = renderLayers([])
    expect(html).toContain('Трасса пуста')
  })

  it('run_command rows carry the details discloser; secret rows carry none (P-2 surface)', () => {
    const withDetails = renderLayers([
      ev('tool.started', { tool: 'run_command', operation_id: 'op-1', arguments: '{"command":["ls"]}' }, 1),
    ])
    expect(withDetails).toContain('aria-label="Показать детали события: run_command"')
    const secret = renderLayers([ev('secret.issued', { secret: 'API_TOKEN' }, 1)])
    expect(secret).not.toContain('aria-label="Показать детали события')
  })
})
