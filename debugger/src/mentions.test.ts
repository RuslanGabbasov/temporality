import { describe, expect, it } from 'vitest'
import {
  buildMentionRows,
  filterMentionOptions,
  insertMentionText,
  mentionInsertText,
  mentionTokenAt,
  mentionTokenKey,
  parseMentionRefs,
  splitMentionQuery,
  type MentionOption,
} from './mentions'

const OPTIONS: MentionOption[] = [
  { kind: 'artifact', id: 'otchet-2026', title: 'Отчёт по продажам', detail: 'approved' },
  { kind: 'artifact', id: 'standart-koda', title: 'Coding standard' },
  { kind: 'skill', id: 'razbor-incidentov', title: 'Разбор инцидентов' },
  { kind: 'task', id: 'sozday-pr-20261021-142015', title: 'Создай PR' },
]

describe('mentionTokenAt', () => {
  it('finds the @token under the caret', () => {
    expect(mentionTokenAt('сделай @art', 11)).toEqual({ start: 7, end: 11, query: 'art' })
    expect(mentionTokenAt('@', 1)).toEqual({ start: 0, end: 1, query: '' })
  })

  it('covers the whole token when the caret sits mid-token', () => {
    expect(mentionTokenAt('@otchet-2026 x', 8)).toEqual({ start: 0, end: 12, query: 'otchet-2026' })
  })

  it('keeps the kind prefix in the query', () => {
    expect(mentionTokenAt('go @artifact:otch', 17)?.query).toBe('artifact:otch')
  })

  it('returns null outside a token and for e-mail-like glues', () => {
    expect(mentionTokenAt('обычный текст', 5)).toBeNull()
    expect(mentionTokenAt('напиши user@example-com', 23)).toBeNull()
  })
})

describe('mentionTokenKey', () => {
  it('changes when the query changes (dismissal bookkeeping)', () => {
    const a = mentionTokenAt('@art', 4)!
    const b = mentionTokenAt('@arte', 5)!
    expect(mentionTokenKey(a)).not.toBe(mentionTokenKey(b))
    expect(mentionTokenKey(a)).toBe(mentionTokenKey(mentionTokenAt('@art', 4)!))
  })
})

describe('splitMentionQuery', () => {
  it('splits a known kind', () => {
    expect(splitMentionQuery('artifact:otch')).toEqual({ kind: 'artifact', filter: 'otch', kindText: 'artifact' })
  })

  it('treats an unknown kind prefix as unrecognized', () => {
    expect(splitMentionQuery('see:docs')).toEqual({ kind: null, filter: 'docs', kindText: 'see' })
  })

  it('has no kind without a colon', () => {
    expect(splitMentionQuery('otch')).toEqual({ kind: null, filter: 'otch', kindText: '' })
  })
})

describe('filterMentionOptions', () => {
  it('ranks id matches above title matches', () => {
    const hits = filterMentionOptions(OPTIONS, 'standart', 10)
    expect(hits.map((o) => o.id)).toEqual(['standart-koda'])
    expect(filterMentionOptions(OPTIONS, 'инцидент', 10).map((o) => o.id)).toEqual(['razbor-incidentov'])
    expect(filterMentionOptions(OPTIONS, 'отчёт', 10).map((o) => o.id)).toEqual(['otchet-2026'])
  })

  it('bounds the result list', () => {
    expect(filterMentionOptions(OPTIONS, '', 2)).toHaveLength(2)
  })
})

describe('buildMentionRows', () => {
  it('shows kind scaffolds for an empty query', () => {
    const rows = buildMentionRows('', OPTIONS)
    expect(rows).toHaveLength(7)
    expect(rows.every((r) => r.type === 'kind')).toBe(true)
  })

  it('narrows to one kind after the scaffold is picked', () => {
    const rows = buildMentionRows('artifact:otch', OPTIONS)
    expect(rows).toEqual([{ type: 'entity', option: OPTIONS[0] }])
  })

  it('offers kinds and entities together while typing', () => {
    const rows = buildMentionRows('art', OPTIONS)
    expect(rows[0]?.type).toBe('kind') // @art… still narrows to @artifact:
    expect(rows.some((r) => r.type === 'entity' && r.option.id === 'standart-koda')).toBe(true)
  })

  it('closes for an explicit unknown kind', () => {
    expect(buildMentionRows('see:docs', OPTIONS)).toEqual([])
  })
})

describe('insertion', () => {
  it('replaces the token span and reports the new caret', () => {
    const text = 'обнови @otch и проверь'
    const token = mentionTokenAt(text, 12)!
    const insert = mentionInsertText({ type: 'entity', option: OPTIONS[0] })
    expect(insert).toBe('@artifact:otchet-2026 ')
    expect(insertMentionText(text, token, insert)).toEqual({
      text: 'обнови @artifact:otchet-2026  и проверь',
      caret: 7 + insert.length,
    })
  })

  it('kind rows insert the narrowing scaffold', () => {
    expect(mentionInsertText({ type: 'kind', kind: 'skill' })).toBe('@skill:')
  })
})

describe('parseMentionRefs', () => {
  it('collects explicit and bare-at references without duplicates', () => {
    const refs = parseMentionRefs('возьми @artifact:otchet-2026, @otchet-2026 и @skill:razbor-incidentov')
    expect(refs).toEqual([
      { kind: 'artifact', id: 'otchet-2026' },
      { kind: null, id: 'otchet-2026' },
      { kind: 'skill', id: 'razbor-incidentov' },
    ])
  })

  it('skips e-mail-like and unknown-kind mentions', () => {
    expect(parseMentionRefs('напиши user@example-com про @see:docs')).toEqual([])
  })
})
