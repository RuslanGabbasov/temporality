// PresentationView tests (docs/presentation-engine.md §18.1): the web
// renderer maps every MVP component onto real DOM, degrades unknown/missing
// blocks to visible placeholders, and never emits raw HTML from the IR
// (the sanitized Markdown component handles markdown; text is text).

import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import PresentationView, { PresentationBlock, type PresentationDocument } from './PresentationView'

const sample: PresentationDocument = {
  version: '1.0',
  type: 'presentation',
  id: 'presentation-123',
  title: 'Test execution report',
  content: [
    { type: 'text', content: 'Тестирование завершено.' },
    { type: 'heading', content: 'Итоги', props: { level: 2 } },
    {
      type: 'kpi',
      items: [
        { label: 'Passed', value: 942, status: 'success' },
        { label: 'Failed', value: 31, status: 'danger', delta: '+4' },
      ],
    },
    {
      type: 'chart',
      chartType: 'pie',
      data: { labels: ['Passed', 'Failed', 'Skipped'], values: [942, 31, 27] },
    },
    {
      type: 'table',
      data: {
        columns: [{ key: 'suite', title: 'Suite' }, { key: 'result', title: 'Result', kind: 'status' }],
        rows: [{ suite: 'auth', result: { text: 'pass', status: 'success' } }],
      },
    },
    { type: 'alert', content: 'billing suite needs attention', props: { severity: 'warning', title: 'Check' } },
    { type: 'markdown', content: '**Жирный** и <script>alert(1)</script>' },
    {
      type: 'progress',
      items: [{ label: 'coverage', value: 40, max: 100, status: 'info' }],
    },
    {
      type: 'timeline',
      items: [
        { label: 'Run started', at: '10:00', status: 'info' },
        { label: 'Failure found', at: '10:07', status: 'danger', description: 'billing timeout' },
      ],
    },
  ],
}

describe('PresentationView', () => {
  const html = renderToStaticMarkup(<PresentationView document={sample} />)

  it('renders the title and every MVP component kind', () => {
    expect(html).toContain('Test execution report')
    expect(html).toContain('Тестирование завершено.')
    expect(html).toContain('Итоги')
    expect(html).toContain('Passed')
    expect(html).toContain('942')
    expect(html).toContain('presentation-kpi')
    // The pie chart renders as svg wedges with the legend.
    expect(html).toContain('presentation-chart-pie')
    expect(html).toContain('Passed · ')
    expect(html).toContain('presentation-table')
    expect(html).toContain('auth')
    expect(html).toContain('presentation-progress-fill')
    expect(html).toContain('coverage')
    expect(html).toContain('presentation-timeline')
    expect(html).toContain('Failure found')
  })

  it('never leaks raw HTML from markdown content', () => {
    expect(html).not.toContain('<script>alert(1)</script>')
    expect(html).toContain('&lt;script&gt;')
  })

  it('renders nothing for an empty or broken document', () => {
    expect(renderToStaticMarkup(<PresentationView document={null} />)).toBe('')
    expect(renderToStaticMarkup(<PresentationView document={{ content: [] }} />)).toBe('')
    expect(renderToStaticMarkup(<PresentationView document={'not an object'} />)).toBe('')
  })

  it('degrades unknown component types to a visible placeholder', () => {
    const out = renderToStaticMarkup(<PresentationBlock component={{ type: 'holocard' }} />)
    expect(out).toContain('presentation-placeholder')
    expect(out).toContain('holocard')
  })

  it('refuses javascript: URLs at render time too', () => {
    const out = renderToStaticMarkup(<PresentationBlock component={{ type: 'link', content: 'x', props: { href: 'javascript:alert(1)' } }} />)
    expect(out).toContain('presentation-placeholder')
    expect(out).not.toContain('javascript:alert(1)')
    const image = renderToStaticMarkup(<PresentationBlock component={{ type: 'image', props: { src: 'javascript:alert(1)' } }} />)
    expect(image).not.toContain('javascript:')
  })

  it('renders the flowchart IR as a mermaid placeholder container', () => {
    const flow = renderToStaticMarkup(
      <PresentationBlock
        component={{
          type: 'flowchart',
          data: {
            nodes: [{ id: 'a', label: 'Build' }, { id: 'b', label: 'Test' }],
            edges: [{ from: 'a', to: 'b' }],
          },
        }}
      />,
    )
    expect(flow).toContain('mermaid-diagram')
  })

  it('renders the gauge as an svg arc with the value', () => {
    const out = renderToStaticMarkup(<PresentationBlock component={{ type: 'gauge', data: { value: 72, label: 'загрузка', unit: '%' } }} />)
    expect(out).toContain('presentation-chart-gauge')
    expect(out).toContain('72 %')
    expect(out).toContain('загрузка')
  })

  it('renders gantt bars with a grid', () => {
    const out = renderToStaticMarkup(
      <PresentationBlock
        component={{
          type: 'gantt',
          props: { title: 'План' },
          data: {
            tasks: [
              { id: 't1', label: 'Design', start: '2026-03-01', end: '2026-03-05', status: 'success' },
              { id: 't2', label: 'Implement', start: '2026-03-05', end: '2026-03-12' },
            ],
          },
        }}
      />,
    )
    expect(out).toContain('presentation-chart-gantt')
    expect(out).toContain('Design')
    expect(out).toContain('План')
  })

  it('refuses gantt tasks without dates', () => {
    const out = renderToStaticMarkup(<PresentationBlock component={{ type: 'gantt', data: { tasks: [{ label: 'no dates' }] } }} />)
    expect(out).toContain('presentation-placeholder')
  })

  it('renders mindmap/state/er/class via the mermaid container', () => {
    const mindmap = renderToStaticMarkup(
      <PresentationBlock component={{ type: 'mindmap', data: { root: { label: 'root', children: [{ label: 'child' }] } } }} />,
    )
    expect(mindmap).toContain('mermaid-diagram')
    const state = renderToStaticMarkup(
      <PresentationBlock component={{ type: 'state-diagram', data: { states: [{ id: 'a', label: 'Draft' }, { id: 'b', label: 'Done' }], transitions: [{ from: 'a', to: 'b', label: 'finish' }] } }} />,
    )
    expect(state).toContain('mermaid-diagram')
    const er = renderToStaticMarkup(
      <PresentationBlock component={{ type: 'er-diagram', data: { entities: [{ name: 'User', attributes: ['string id'] }], relations: [{ from: 'User', to: 'Order', cardinality: '1:N', label: 'places' }] } }} />,
    )
    expect(er).toContain('mermaid-diagram')
    const cls = renderToStaticMarkup(
      <PresentationBlock component={{ type: 'class-diagram', data: { classes: [{ name: 'Animal', attributes: ['String name'] }], relations: [{ from: 'Animal', to: 'Dog', kind: 'inheritance' }] } }} />,
    )
    expect(cls).toContain('mermaid-diagram')
  })

  it('renders a button as a static chip when no interaction handler is given', () => {
    const out = renderToStaticMarkup(
      <PresentationBlock component={{ type: 'button', id: 'b1', content: 'Approve', actions: [{ type: 'submit', target: 'ask-1' }] }} />,
    )
    expect(out).toContain('presentation-button-static')
    expect(out).toContain('Approve')
  })

  it('renders the form read-only through HumanForm when no handler is given', () => {
    const out = renderToStaticMarkup(
      <PresentationBlock
        component={{
          type: 'form',
          id: 'f1',
          content: 'Review',
          props: { schema: { type: 'object', properties: { name: { type: 'string' } }, required: ['name'] }, submit: 'Send' },
          actions: [{ type: 'submit', target: 'ask-2' }],
        }}
      />,
    )
    expect(out).toContain('presentation-form-static')
  })
})
