import { describe, it, expect } from 'vitest'
import { EVENT_CATALOG, EVENT_GROUPS, eventInfo, localizedEventGroups } from './eventCatalog'

describe('eventCatalog', () => {
  it('has unique event types', () => {
    const types = EVENT_CATALOG.map((e) => e.type)
    expect(new Set(types).size).toBe(types.length)
  })

  it('every event references a known group and carries both locales', () => {
    const groups = new Set(EVENT_GROUPS.map((g) => g.id))
    for (const e of EVENT_CATALOG) {
      expect(groups.has(e.group), `unknown group ${e.group} for ${e.type}`).toBe(true)
      expect(e.ru.title.length).toBeGreaterThan(0)
      expect(e.ru.desc.length).toBeGreaterThan(0)
      expect(e.en.title.length).toBeGreaterThan(0)
      expect(e.en.desc.length).toBeGreaterThan(0)
    }
  })

  it('exposes groups in display order without empty ones', () => {
    const grouped = localizedEventGroups('en')
    expect(grouped.length).toBeGreaterThan(5)
    for (const g of grouped) expect(g.events.length).toBeGreaterThan(0)
  })

  it('looks up by raw type', () => {
    expect(eventInfo('run.completed')?.en.title).toBe('Run completed')
    expect(eventInfo('no.such.event')).toBeUndefined()
  })
})
