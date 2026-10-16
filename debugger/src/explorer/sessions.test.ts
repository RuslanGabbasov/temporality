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
    expect(restored!.areaKey).toBe('topic:db')
    expect(restored!.view).toBe('conflicts')
    expect(restored!.dossierID).toBe('k-42')
    expect(restored!.notes).toBe('проверить смены схемы')
    expect(restored!.conclusion).toBe('миграции обязательны')
  })

  it('keeps references shallow — filters are copies, not the same object', () => {
    const filters = { state: 'confirmed', scope: '', topic: '', entity: '', origin: '', kind: '' }
    const payload = sessionSnapshot({
      question: '', searchInput: '', filters, areaKey: '', view: 'search', dossierID: '', notes: '', conclusion: '',
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
      notes: 'заметка',
      futureField: { deep: true },
    })
    expect(restored).not.toBeNull()
    expect(restored!.question).toBe('')
    expect(restored!.searchInput).toBe('') // falls back to question, also empty
    expect(restored!.filters.state).toBe('confirmed')
    expect('weird' in restored!.filters).toBe(false)
    expect(restored!.view).toBe('agent')
    expect(restored!.notes).toBe('заметка')
    expect(restored!.conclusion).toBe('')
  })

  it('falls back searchInput to question for older payloads without it', () => {
    const restored = applySessionPayload({ question: 'docker' })
    expect(restored!.searchInput).toBe('docker')
  })

  it('ignores the historical nav list from old saved sessions', () => {
    const restored = applySessionPayload({
      question: 'docker',
      nav: ['junk', { label: 'ok', view: 'conflicts' }],
    })
    expect(restored).not.toBeNull()
    expect(restored!.question).toBe('docker')
    expect('nav' in restored!).toBe(false)
  })
})
