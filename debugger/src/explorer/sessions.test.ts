import { describe, expect, it } from 'vitest'
import { applySessionPayload, sessionSnapshot } from './sessions'

describe('sessionSnapshot / applySessionPayload roundtrip', () => {
  it('restores exactly what was saved', () => {
    const payload = sessionSnapshot({
      question: 'как делать миграции',
      searchInput: 'как делать миграции',
      filters: { state: 'confirmed', scope: '', topic: 'db', entity: '', origin: '', kind: '' },
      areaKey: 'topic:db',
      view: 'conflicts',
      dossierID: 'k-42',
      nav: [
        { id: 1, label: 'start', question: '', filters: { state: '', scope: '', topic: '', entity: '', origin: '', kind: '' }, areaKey: '', view: 'search', dossierID: '' },
        { id: 2, label: 'db', question: 'миграции', filters: { state: 'confirmed', scope: '', topic: 'db', entity: '', origin: '', kind: '' }, areaKey: 'topic:db', view: 'search', dossierID: 'k-42' },
      ],
      notes: 'проверить смены схемы',
      conclusion: 'миграции обязательны',
    })
    // The payload is references only: JSON-stable, no knowledge copies.
    const parsed = JSON.parse(JSON.stringify(payload))
    const restored = applySessionPayload(parsed)
    expect(restored).not.toBeNull()
    expect(restored!.question).toBe('как делать миграции')
    expect(restored!.filters.state).toBe('confirmed')
    expect(restored!.filters.topic).toBe('db')
    expect(restored!.view).toBe('conflicts')
    expect(restored!.dossierID).toBe('k-42')
    expect(restored!.nav).toHaveLength(2)
    expect(restored!.nav[1].dossierID).toBe('k-42')
    expect(restored!.notes).toBe('проверить смены схемы')
    expect(restored!.conclusion).toBe('миграции обязательны')
  })

  it('keeps references shallow — nav filters are copies, not the same object', () => {
    const filters = { state: 'confirmed', scope: '', topic: '', entity: '', origin: '', kind: '' }
    const payload = sessionSnapshot({
      question: '', searchInput: '', filters, areaKey: '', view: 'search', dossierID: '', nav: [], notes: '', conclusion: '',
    })
    payload.filters.state = 'invalidated'
    expect(filters.state).toBe('confirmed')
  })
})

describe('applySessionPayload tolerance', () => {
  it('returns null for non-objects', () => {
    expect(applySessionPayload(null)).toBeNull()
    expect(applySessionPayload('x')).toBeNull()
    expect(applySessionPayload([1])).toBeNull()
  })

  it('drops foreign fields, keeps known ones and heals missing pieces', () => {
    const restored = applySessionPayload({
      version: 99,
      question: 42,
      filters: { state: 'confirmed', weird: 'x', topic: 7 },
      view: 'agent',
      nav: [
        'junk',
        { label: 'ok', filters: null, view: 'conflicts' },
      ],
      notes: 'заметка',
      futureField: { deep: true },
    })
    expect(restored).not.toBeNull()
    expect(restored!.question).toBe('')
    expect(restored!.searchInput).toBe('') // falls back to question, also empty
    expect(restored!.filters.state).toBe('confirmed')
    expect('weird' in restored!.filters).toBe(false)
    expect(restored!.view).toBe('agent')
    expect(restored!.nav).toHaveLength(1)
    expect(restored!.nav[0].label).toBe('ok')
    expect(restored!.nav[0].view).toBe('conflicts')
    expect(restored!.nav[0].id).toBe(2) // positional fallback id
    expect(restored!.notes).toBe('заметка')
    expect(restored!.conclusion).toBe('')
  })

  it('falls back searchInput to question for older payloads without it', () => {
    const restored = applySessionPayload({ question: 'docker' })
    expect(restored!.searchInput).toBe('docker')
  })
})
