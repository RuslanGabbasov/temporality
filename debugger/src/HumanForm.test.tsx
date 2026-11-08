// HumanForm rendering tests (docs/interactive-HITL.md §7): the enum choice
// control switches by list length — <= 5 options render as radio buttons
// (all choices visible, one click to pick), longer lists stay a dropdown.
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import HumanForm, { type FormSchema } from './HumanForm'

const radioSchema: FormSchema = {
  type: 'object',
  properties: {
    priority: { type: 'string', enum: ['high', 'normal', 'low'], title: 'Priority' },
  },
  required: ['priority'],
}

const dropdownSchema: FormSchema = {
  type: 'object',
  properties: {
    region: { type: 'string', enum: ['eu', 'us', 'ap', 'sa', 'af', 'me'] },
  },
}

describe('HumanForm enum rendering', () => {
  it('renders a short enum as a radio group with every option visible', () => {
    const html = renderToStaticMarkup(<HumanForm schema={radioSchema} submitLabel="Send" />)
    expect(html).toContain('type="radio"')
    expect((html.match(/type="radio"/g) ?? []).length).toBe(3)
    for (const option of ['high', 'normal', 'low']) {
      expect(html).toContain(`>${option}</span>`)
    }
    expect(html).toContain('radiogroup')
  })

  it('groups the radios by field name so choices are mutually exclusive', () => {
    const html = renderToStaticMarkup(
      <HumanForm
        schema={{
          type: 'object',
          properties: {
            priority: { type: 'string', enum: ['high', 'normal', 'low'] },
            channel: { type: 'string', enum: ['matrix', 'telegram'] },
          },
        }}
        submitLabel="Send"
      />,
    )
    // Radios of one field share the name; different fields do not interfere.
    expect((html.match(/name="priority"/g) ?? []).length).toBe(3)
    expect((html.match(/name="channel"/g) ?? []).length).toBe(2)
  })

  it('renders a long enum (6 options) as a dropdown, not radios', () => {
    const html = renderToStaticMarkup(<HumanForm schema={dropdownSchema} submitLabel="Send" />)
    expect(html).not.toContain('type="radio"')
    expect(html).toContain('<select')
    for (const option of ['eu', 'us', 'ap', 'sa', 'af', 'me']) {
      expect(html).toContain(`value="${option}"`)
    }
  })

  it('renders an array enum as the multi-select checkbox group (unchanged)', () => {
    const html = renderToStaticMarkup(
      <HumanForm
        schema={{ type: 'object', properties: { tags: { type: 'array', enum: ['a', 'b', 'c'] } } }}
        submitLabel="Send"
      />,
    )
    expect((html.match(/type="checkbox"/g) ?? []).length).toBe(3)
    expect(html).not.toContain('type="radio"')
  })
})
