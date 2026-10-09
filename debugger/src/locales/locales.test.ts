import { describe, expect, it } from 'vitest'
import { DEFAULT_LOCALE, LOCALES } from './index'

describe('locales', () => {
  it('every locale defines exactly the same keys as the default', () => {
    const base = Object.keys(LOCALES[DEFAULT_LOCALE].messages).sort()
    expect(base.length).toBeGreaterThan(0)
    for (const [code, { messages }] of Object.entries(LOCALES)) {
      expect(Object.keys(messages).sort(), `locale "${code}" key set`).toEqual(base)
    }
  })

  it('no empty translations', () => {
    for (const [code, { messages }] of Object.entries(LOCALES)) {
      for (const [key, value] of Object.entries(messages)) {
        expect(value.trim(), `locale "${code}" key "${key}"`).not.toBe('')
      }
    }
  })
})
