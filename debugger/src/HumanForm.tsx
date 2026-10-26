import { useState } from 'react'
import { useI18n } from './i18n'

// HITL form renderer (docs/interactive-HITL.md §7): one component shared by
// the chat question card and the runs page tiles. Renders the supported
// JSON Schema subset the kernel enforces server-side (workspace/forms.go):
// string (+enum, pattern, minLength/maxLength, format), integer/number
// (+minimum/maximum), boolean, array (+enum → multi-select checkbox group,
// minItems/maxItems, items format date + minItems=maxItems=2 → date range).
// Unknown keywords are ignored.

export interface FormSchema {
  type?: string
  properties?: Record<string, FormFieldSchema>
  required?: string[]
}

export interface FormFieldSchema {
  type?: string
  title?: string
  description?: string
  enum?: string[]
  minLength?: number
  maxLength?: number
  pattern?: string
  minimum?: number
  maximum?: number
  format?: string
  items?: FormFieldSchema
  minItems?: number
  maxItems?: number
}

// Bounded client-side format checks — the server is the authority. They run
// on the RAW control value (what the browser emitted), before normalization.
const FORMAT_CHECKS: Record<string, RegExp> = {
  date: /^\d{4}-\d{2}-\d{2}$/,
  'date-time': /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(:\d{2})?(Z|[+-]\d{2}:?\d{2})?$/,
  time: /^\d{2}:\d{2}(:\d{2})?$/,
  email: /^[^@\s]+@[^@\s]+\.[^@\s]+$/,
}

/** Human-readable pattern decoder: turns the common RE2 shapes into a plain
 * phrase so a field with a regex but no title/description is still enterable:
 * ^[A-Z]{2,4}-[0-9]{3,6}$ → "2–4 × A–Z + "-" + 3–6 × 0–9, whole value".
 * Returns null when the pattern has constructs the decoder does not know —
 * the caller falls back to showing the raw pattern. */
export function patternHint(pattern: string, locale: string): string | null {
  if (!pattern || pattern.length > 256) return null
  const ru = locale.startsWith('ru')
  const word = (en: string, ruText: string) => (ru ? ruText : en)
  let body = pattern
  let whole = false
  if (body.startsWith('^')) { body = body.slice(1); whole = true }
  if (body.endsWith('$') && !body.endsWith('\\$')) { body = body.slice(0, -1); whole = true }
  const classText = (cls: string): string | null => {
    if (cls.startsWith('^')) return null // negated classes: not worth guessing
    // Ranges and single symbols: A-Z, 0-9, abc → "A–Z 0–9 a b c"
    const parts: string[] = []
    let i = 0
    while (i < cls.length) {
      if (i + 2 < cls.length && cls[i + 1] === '-') {
        parts.push(`${cls[i]}–${cls[i + 2]}`)
        i += 3
      } else {
        parts.push(cls[i])
        i += 1
      }
    }
    if (!parts.length) return null
    return parts.join(' ')
  }
  const quantifier = (): string => {
    const m = body.match(/^\{(\d+)(,(\d+))?\}/)
    if (m) {
      body = body.slice(m[0].length)
      if (m[2] === undefined) return word(`×${m[1]}`, `×${m[1]}`)
      return m[3] ? word(`×${m[1]}–${m[3]}`, `×${m[1]}–${m[3]}`) : word(`×${m[1]}+`, `×${m[1]}+`)
    }
    if (body.startsWith('+')) { body = body.slice(1); return word('×1+', '×1+') }
    if (body.startsWith('*')) { body = body.slice(1); return word('×0+', '×0+') }
    if (body.startsWith('?')) { body = body.slice(1); return word('×0–1', '×0–1') }
    return ''
  }
  const pieces: string[] = []
  let alternation: string[] | null = null
  while (body.length) {
    if (body.startsWith('|')) {
      // Top-level alternation: everything so far was option 1.
      if (alternation === null) alternation = [pieces.join('')]
      else alternation.push(pieces.join(''))
      pieces.length = 0
      body = body.slice(1)
      continue
    }
    const classMatch = body.match(/^\[([^\]]*)\]/)
    if (classMatch) {
      const text = classText(classMatch[1])
      if (text === null) return null
      body = body.slice(classMatch[0].length)
      const q = quantifier()
      pieces.push(text + q)
      continue
    }
    const escape = body.match(/^\\([dwsDWS])/)
    if (escape) {
      const names: Record<string, [string, string]> = {
        d: ['digits 0–9', 'цифры 0–9'], w: ['letters, digits, _', 'буквы, цифры, _'], s: ['whitespace', 'пробелы'],
      }
      const key = escape[1].toLowerCase()
      const name = names[key]
      if (!name) return null
      body = body.slice(escape[0].length)
      const q = quantifier()
      pieces.push(word(name[0], name[1]) + q)
      continue
    }
    if (body.startsWith('(')) {
      const close = body.indexOf(')')
      if (close < 0) return null
      const options = body.slice(1, close).split('|')
      if (options.some((o) => /[{+*?\\[\]()]/.test(o))) return null
      body = body.slice(close + 1)
      const q = quantifier()
      pieces.push(word(`one of: ${options.join(', ')}`, `одно из: ${options.join(', ')}`) + q)
      continue
    }
    const char = body[0]
    if ('\\[](){}.+*?$^'.includes(char) && !(char === '.')) return null
    body = body.slice(1)
    const q = quantifier()
    pieces.push(char === '-' ? word('"-"', '"-"') : char + q)
  }
  if (alternation !== null) {
    alternation.push(pieces.join(''))
    return word(`one of the shapes: ${alternation.join(' / ')}`, `один из вариантов: ${alternation.join(' / ')}`) + (whole ? word(', whole value', ', строка целиком') : '')
  }
  if (!pieces.length) return null
  // A run of single-character pieces is a literal word: ["f","o","o"] reads
  // better as "foo" than "f + o + o". Multi-char pieces stay as they are.
  let phrase = ''
  let literal = ''
  const flush = () => {
    if (literal) { phrase += literal; literal = '' }
  }
  for (const piece of pieces) {
    if (piece.length === 1) { flush(); literal += piece; continue }
    flush()
    phrase += (phrase ? ' + ' : '') + piece
  }
  flush()
  return phrase + (whole ? word(', whole value', ', строка целиком') : '')
}

/** Local UTC offset as ±HH:MM — the datetime-local control carries no zone,
 * so the submission adds the user's own. */
function localOffsetSuffix(): string {
  const minutes = new Date().getTimezoneOffset()
  if (minutes === 0) return 'Z'
  const sign = minutes > 0 ? '-' : '+'
  const abs = Math.abs(minutes)
  const hh = String(Math.floor(abs / 60)).padStart(2, '0')
  const mm = String(abs % 60).padStart(2, '0')
  return `${sign}${hh}:${mm}`
}

/** Normalize one raw control value into the shape the server validates:
 * datetime-local gains seconds and the user's offset, time gains seconds.
 * Pure for the values it can decide locally; already-complete values pass
 * through untouched. */
export function normalizeFormValue(field: FormFieldSchema, value: unknown): unknown {
  if (typeof value !== 'string') return value
  if (field.type === 'string' && field.format === 'date-time') {
    if (/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$/.test(value)) return `${value}:00${localOffsetSuffix()}`
    if (/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}$/.test(value)) return value + localOffsetSuffix()
    return value
  }
  if (field.type === 'string' && field.format === 'time') {
    if (/^\d{2}:\d{2}$/.test(value)) return `${value}:00`
    return value
  }
  return value
}

/** Normalize every value in the submission against the schema. */
export function normalizeFormValues(schema: FormSchema | undefined | null, values: Record<string, unknown>): Record<string, unknown> {
  if (!schema?.properties) return values
  const out: Record<string, unknown> = { ...values }
  for (const [name, field] of Object.entries(schema.properties)) {
    if (name in out) out[name] = normalizeFormValue(field, out[name])
  }
  return out
}

/** Pure client-side mirror of workspace.ValidateFormValues: quick feedback
 * before submission. The server re-checks everything — a value that passes
 * here can still be rejected with 422. `locale` shapes the human-readable
 * pattern hint (the field title may be absent — the decoded pattern is what
 * the user can actually act on). */
export function validateFormValues(schema: FormSchema | undefined | null, values: Record<string, unknown>, locale = 'en'): Record<string, string> {
  const errors: Record<string, string> = {}
  if (!schema?.properties) return errors
  for (const [name, field] of Object.entries(schema.properties)) {
    const raw = values[name]
    const empty = raw === undefined || raw === null || (typeof raw === 'string' && raw.trim() === '') || (Array.isArray(raw) && raw.length === 0)
    const required = schema.required?.includes(name)
    if (empty) {
      if (required) errors[name] = 'required'
      continue
    }
    if (field.type === 'string' && typeof raw === 'string') {
      const len = [...raw].length
      if (field.minLength !== undefined && len < field.minLength) errors[name] = `min ${field.minLength}`
      else if (field.maxLength !== undefined && len > field.maxLength) errors[name] = `max ${field.maxLength}`
      else if (field.pattern) {
        try {
          if (!new RegExp(field.pattern, 'u').test(raw)) errors[name] = patternHint(field.pattern, locale) ?? `format: ${field.pattern}`
        } catch { // a pattern the browser cannot compile — the server decides
        }
      } else if (field.format && FORMAT_CHECKS[field.format] && !FORMAT_CHECKS[field.format].test(raw)) errors[name] = field.format
    } else if ((field.type === 'number' || field.type === 'integer') && typeof raw === 'number') {
      if (field.type === 'integer' && !Number.isInteger(raw)) errors[name] = 'integer'
      else if (field.minimum !== undefined && raw < field.minimum) errors[name] = `≥ ${field.minimum}`
      else if (field.maximum !== undefined && raw > field.maximum) errors[name] = `≤ ${field.maximum}`
    } else if (field.type === 'array' && Array.isArray(raw)) {
      if (field.minItems !== undefined && raw.length < field.minItems) errors[name] = `min ${field.minItems}`
      else if (field.maxItems !== undefined && raw.length > field.maxItems) errors[name] = `max ${field.maxItems}`
      else {
        for (const item of raw) {
          const itemField = field.items ?? {}
          if (itemField.enum && (typeof item !== 'string' || !itemField.enum.includes(item))) { errors[name] = 'choices'; break }
          if (itemField.type === 'string' && typeof item === 'string' && itemField.format === 'date' && !FORMAT_CHECKS.date.test(item)) { errors[name] = 'date'; break }
        }
      }
    }
  }
  return errors
}

const inputStyle: React.CSSProperties = {
  width: '100%', background: 'var(--tm-raised)', border: '1px solid var(--tm-border)', borderRadius: '4px',
  color: 'var(--tm-text)', fontSize: '0.8rem', padding: '0.3rem 0.5rem', boxSizing: 'border-box',
}

function FieldLabel({ name, field, required }: { name: string; field: FormFieldSchema; required: boolean }) {
  return (
    <label style={{ display: 'block', fontSize: '0.72rem', color: 'var(--tm-text-2)', marginBottom: '0.15rem' }}>
      {field.title || name}{required && <span style={{ color: 'var(--tm-danger)' }}> *</span>}
    </label>
  )
}

function ArrayInput({ name, field, value, disabled, onChange }: {
  name: string
  field: FormFieldSchema
  value: unknown
  disabled?: boolean
  onChange: (value: unknown) => void
}) {
  const current = Array.isArray(value) ? (value as string[]) : []
  const itemType = field.items?.type ?? 'string'
  // Date range: exactly two dates — two pickers producing [from, to].
  if (itemType === 'string' && field.items?.format === 'date' && field.minItems === 2 && field.maxItems === 2) {
    const from = current[0] ?? ''
    const to = current[1] ?? ''
    const set = (index: number, date: string) => {
      const next = [...current]
      next[index] = date
      const cleaned = next.filter(Boolean)
      onChange(cleaned.length ? cleaned : undefined)
    }
    return (
      <div style={{ display: 'flex', gap: '0.35rem', alignItems: 'center' }}>
        <input type="date" aria-label={`${field.title || name} from`} disabled={disabled} value={from} onChange={(e) => set(0, e.target.value)} style={inputStyle} />
        <span style={{ color: 'var(--tm-text-3)', fontSize: '0.72rem' }}>→</span>
        <input type="date" aria-label={`${field.title || name} to`} disabled={disabled} value={to} onChange={(e) => set(1, e.target.value)} style={inputStyle} />
      </div>
    )
  }
  // Multi-select over an enum: a checkbox group (survey-style choices).
  if (field.enum && field.enum.length > 0) {
    const toggle = (option: string) => {
      const next = current.includes(option) ? current.filter((v) => v !== option) : [...current, option]
      onChange(next.length ? next : undefined)
    }
    return (
      <div style={{ display: 'flex', flexDirection: 'column', gap: '0.25rem' }} role="group" aria-label={field.title || name}>
        {field.enum.map((option) => (
          <label key={option} style={{ display: 'flex', alignItems: 'center', gap: '0.4rem', fontSize: '0.8rem', cursor: 'pointer' }}>
            <input type="checkbox" disabled={disabled} checked={current.includes(option)} onChange={() => toggle(option)} />
            <span>{option}</span>
          </label>
        ))}
      </div>
    )
  }
  return null
}

function FieldInput({ name, field, value, disabled, onChange }: {
  name: string
  field: FormFieldSchema
  value: unknown
  disabled?: boolean
  onChange: (value: unknown) => void
}) {
  const { t, locale } = useI18n()
  const isNumber = field.type === 'number' || field.type === 'integer'
  const formatType: Record<string, string> = { date: 'date', 'date-time': 'datetime-local', time: 'time', email: 'email', url: 'url' }
  const htmlType = field.format && formatType[field.format] ? formatType[field.format] : field.format === 'password' ? 'password' : isNumber ? 'number' : 'text'
  const multiline = field.type === 'string' && !field.pattern && !field.format && (field.maxLength ?? 0) >= 120
  const stringValue = value === undefined || value === null ? '' : String(value)
  const isDateLike = field.type === 'string' && !!field.format && ['date', 'date-time', 'time'].includes(field.format)
  const hintKey = field.format === 'date-time' ? 'form.hint_datetime' : field.format === 'time' ? 'form.hint_time' : field.format === 'date' ? 'form.hint_date' : ''
  if (field.enum && field.enum.length > 0) {
    return (
      <select
        value={typeof value === 'string' ? (value as string) : ''}
        disabled={disabled}
        onChange={(e) => onChange(e.target.value)}
        style={inputStyle}
      >
        <option value="" disabled>—</option>
        {field.enum.map((option) => <option key={option} value={option}>{option}</option>)}
      </select>
    )
  }
  if (multiline) {
    return <textarea rows={3} disabled={disabled} value={stringValue} onChange={(e) => onChange(e.target.value)} style={{ ...inputStyle, resize: 'vertical' }} />
  }
  return (
    <>
      <input
        type={htmlType}
        lang={isDateLike ? locale : undefined}
        step={field.type === 'integer' ? 1 : field.format === 'time' ? 60 : 'any'}
        min={field.minimum}
        max={field.maximum}
        disabled={disabled}
        value={stringValue}
        onChange={(e) => onChange(isNumber ? (e.target.value === '' ? undefined : Number(e.target.value)) : e.target.value)}
        style={inputStyle}
      />
      {isDateLike && hintKey && (
        <div style={{ fontSize: '0.68rem', color: 'var(--tm-text-3)', marginTop: '0.1rem' }}>{t(hintKey)}</div>
      )}
    </>
  )
}

/** Interactive form for one HITL question. Local state; onValidSubmit hands
 * the collected values to the caller (chat card / runs tile) which submits
 * them through the approval endpoint. */
export default function HumanForm({ schema, disabled, submitLabel, onValidSubmit }: {
  schema: FormSchema
  disabled?: boolean
  submitLabel: string
  onValidSubmit: (values: Record<string, unknown>) => void
}) {
  const [values, setValues] = useState<Record<string, unknown>>({})
  const { locale } = useI18n()
  const errors = validateFormValues(schema, values, locale)
  const fields = Object.entries(schema.properties ?? {})

  const set = (name: string, value: unknown) => {
    setValues((prev) => {
      const next = { ...prev }
      if (value === '' || value === undefined) delete next[name]
      else next[name] = value
      return next
    })
  }

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: '0.45rem', marginTop: '0.5rem' }}>
      {fields.map(([name, field]) => {
        const required = schema.required?.includes(name) ?? false
        const invalid = errors[name]
        if (field.type === 'boolean') {
          return (
            <label key={name} style={{ display: 'flex', alignItems: 'center', gap: '0.4rem', fontSize: '0.8rem', cursor: 'pointer' }}>
              <input type="checkbox" disabled={disabled} checked={values[name] === true} onChange={(e) => set(name, e.target.checked)} />
              <span>{field.title || name}{required && <span style={{ color: 'var(--tm-danger)' }}> *</span>}</span>
            </label>
          )
        }
        if (field.type === 'array') {
          return (
            <div key={name}>
              <FieldLabel name={name} field={field} required={required} />
              <ArrayInput name={name} field={field} value={values[name]} disabled={disabled} onChange={(v) => set(name, v)} />
              {field.description && <div style={{ fontSize: '0.68rem', color: 'var(--tm-text-3)', marginTop: '0.1rem' }}>{field.description}</div>}
              {invalid && <div style={{ fontSize: '0.68rem', color: 'var(--tm-danger)', marginTop: '0.1rem' }}>{invalid}</div>}
            </div>
          )
        }
        return (
          <div key={name}>
            <FieldLabel name={name} field={field} required={required} />
            <FieldInput name={name} field={field} value={values[name]} disabled={disabled} onChange={(v) => set(name, v)} />
            {field.description && <div style={{ fontSize: '0.68rem', color: 'var(--tm-text-3)', marginTop: '0.1rem' }}>{field.description}</div>}
            {!field.description && field.pattern && (
              <div style={{ fontSize: '0.68rem', color: 'var(--tm-text-3)', marginTop: '0.1rem' }}>
                ⌗ {patternHint(field.pattern, locale) ?? field.pattern}
              </div>
            )}
            {invalid && <div style={{ fontSize: '0.68rem', color: 'var(--tm-danger)', marginTop: '0.1rem' }}>{invalid}</div>}
          </div>
        )
      })}
      <button
        disabled={disabled || Object.keys(errors).length > 0 || fields.length === 0}
        onClick={() => onValidSubmit(normalizeFormValues(schema, values))}
        style={{ alignSelf: 'flex-start', background: 'var(--tm-teal)', border: 'none', borderRadius: '4px', color: '#fff', cursor: 'pointer', fontSize: '0.75rem', padding: '0.3rem 0.7rem' }}
      >{submitLabel}</button>
    </div>
  )
}
