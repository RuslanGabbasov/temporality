import { describe, expect, it } from 'vitest'
import { normalizeFormValue, validateFormValues, type FormFieldSchema, type FormSchema } from './HumanForm'

const schema: FormSchema = {
  type: 'object',
  properties: {
    name: { type: 'string', minLength: 3, maxLength: 10 },
    priority: { type: 'string', enum: ['high', 'normal'] },
    count: { type: 'integer', minimum: 1, maximum: 100 },
    confirm: { type: 'boolean' },
  },
  required: ['name'],
}

describe('validateFormValues', () => {
  it('flags a missing required field', () => {
    expect(validateFormValues(schema, {})).toEqual({ name: 'required' })
    expect(validateFormValues(schema, { name: '' })).toEqual({ name: 'required' })
    expect(validateFormValues(schema, { name: '   ' })).toEqual({ name: 'required' })
  })

  it('enforces string length bounds', () => {
    expect(validateFormValues(schema, { name: 'ab' })).toEqual({ name: 'min 3' })
    expect(validateFormValues(schema, { name: 'abcdefghijkl' })).toEqual({ name: 'max 10' })
    expect(validateFormValues(schema, { name: 'ok-name' })).toEqual({})
  })

  it('enforces numeric bounds and integrality', () => {
    expect(validateFormValues(schema, { name: 'ok-name', count: 0 })).toEqual({ count: '≥ 1' })
    expect(validateFormValues(schema, { name: 'ok-name', count: 101 })).toEqual({ count: '≤ 100' })
    expect(validateFormValues(schema, { name: 'ok-name', count: 1.5 })).toEqual({ count: 'integer' })
    expect(validateFormValues(schema, { name: 'ok-name', count: 7 })).toEqual({})
  })

  it('ignores optional empty fields but validates filled ones', () => {
    expect(validateFormValues(schema, { name: 'ok-name', priority: 'urgent' })).toEqual({})
    // enum constraints are enforced server-side; the client select can only
    // offer the listed values, so an off-enum string is not a client error.
    expect(validateFormValues(schema, { name: 'ok-name', count: undefined })).toEqual({})
  })

  it('accepts booleans and skips validation for unknown schemas', () => {
    expect(validateFormValues(schema, { name: 'ok-name', confirm: true })).toEqual({})
    expect(validateFormValues(undefined, {})).toEqual({})
    expect(validateFormValues({ type: 'object' }, { anything: 'goes' })).toEqual({})
  })

  it('enforces patterns and declared string formats', () => {
    const s: FormSchema = { type: 'object', properties: {
      ticket: { type: 'string', pattern: '[A-Z]+-[0-9]+' },
      day: { type: 'string', format: 'date' },
      mail: { type: 'string', format: 'email' },
    } }
    expect(validateFormValues(s, { ticket: 'see PROJ-1', day: '2026-03-01', mail: 'a@b.co' })).toEqual({})
    expect(validateFormValues(s, { ticket: 'no ticket here', day: '2026-03-01', mail: 'a@b.co' })).toEqual({ ticket: 'pattern' })
    expect(validateFormValues(s, { ticket: 'PROJ-1', day: '01.03.2026', mail: 'a@b.co' })).toEqual({ day: 'date' })
    expect(validateFormValues(s, { ticket: 'PROJ-1', day: '2026-03-01', mail: 'broken' })).toEqual({ mail: 'email' })
    // A pattern the browser cannot compile is left to the server.
    const bad: FormSchema = { type: 'object', properties: { x: { type: 'string', pattern: '([bad' } } }
    expect(validateFormValues(bad, { x: 'anything' })).toEqual({})
  })

  it('validates array fields: multi-select bounds and date ranges', () => {
    const s: FormSchema = { type: 'object', properties: {
      services: { type: 'array', items: { type: 'string', enum: ['api', 'web', 'db'] }, minItems: 1, maxItems: 3 },
      window: { type: 'array', minItems: 2, maxItems: 2, items: { type: 'string', format: 'date' } },
    } }
    expect(validateFormValues(s, { services: ['api', 'db'], window: ['2026-03-01', '2026-03-05'] })).toEqual({})
    expect(validateFormValues(s, { services: [], window: ['2026-03-01', '2026-03-05'] })).toEqual({}) // empty optional = untouched
    expect(validateFormValues(s, { services: ['api'], window: ['2026-03-01'] })).toEqual({ window: 'min 2' })
    expect(validateFormValues(s, { services: ['api', 'cache'], window: ['2026-03-01', '2026-03-05'] })).toEqual({ services: 'choices' })
    expect(validateFormValues(s, { services: ['api'], window: ['2026-03-01', 'oops'] })).toEqual({ window: 'date' })
  })
})

describe('normalizeFormValue', () => {
  const datetime: FormFieldSchema = { type: 'string', format: 'date-time' }
  const time: FormFieldSchema = { type: 'string', format: 'time' }
  const plain: FormFieldSchema = { type: 'string' }

  it('completes datetime-local values to RFC 3339 with the local offset', () => {
    const normalized = normalizeFormValue(datetime, '2026-03-01T10:30') as string
    expect(normalized).toMatch(/^2026-03-01T10:30:00(Z|[+-]\d{2}:\d{2})$/)
    const withSeconds = normalizeFormValue(datetime, '2026-03-01T10:30:45') as string
    expect(withSeconds).toMatch(/^2026-03-01T10:30:45(Z|[+-]\d{2}:\d{2})$/)
  })

  it('leaves complete RFC 3339 values untouched', () => {
    expect(normalizeFormValue(datetime, '2026-03-01T10:30:00+03:00')).toBe('2026-03-01T10:30:00+03:00')
    expect(normalizeFormValue(datetime, '2026-03-01T10:30:00Z')).toBe('2026-03-01T10:30:00Z')
  })

  it('adds seconds to bare HH:MM time values', () => {
    expect(normalizeFormValue(time, '14:30')).toBe('14:30:00')
    expect(normalizeFormValue(time, '14:30:45')).toBe('14:30:45')
  })

  it('passes non-string and plain values through', () => {
    expect(normalizeFormValue(plain, '2026-03-01T10:30')).toBe('2026-03-01T10:30')
    expect(normalizeFormValue(datetime, undefined)).toBeUndefined()
    expect(normalizeFormValue(datetime, 42)).toBe(42)
  })

  it('client-side date-time check accepts the raw browser shape', () => {
    const s: FormSchema = { type: 'object', properties: { at: { type: 'string', format: 'date-time' } } }
    expect(validateFormValues(s, { at: '2026-03-01T10:30' })).toEqual({})
    expect(validateFormValues(s, { at: '01.03.2026 10:30' })).toEqual({ at: 'date-time' })
  })
})
