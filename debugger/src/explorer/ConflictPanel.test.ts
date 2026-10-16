import { describe, expect, it } from 'vitest'
import { conflictBasisKey, conflictBasisLabel } from './ConflictPanel'

describe('conflict basis', () => {
  it('maps engine bases onto locale keys', () => {
    expect(conflictBasisKey('explicit:contradicts')).toBe('explorer.conflict_basis_explicit')
    expect(conflictBasisKey('candidate:same-topic-different-state')).toBe('explorer.conflict_basis_candidate')
    expect(conflictBasisKey('resolved:replacement')).toBe('explorer.conflict_basis_resolved')
    expect(conflictBasisKey('something:new')).toBe('explorer.conflict_basis_other')
  })

  it('falls back to the raw basis when the locale lacks the key', () => {
    const t = (key: string) => (key === 'explorer.conflict_basis_explicit' ? 'Явное противоречие' : key)
    expect(conflictBasisLabel('explicit:contradicts', t)).toBe('Явное противоречие')
    expect(conflictBasisLabel('novel:basis', t)).toBe('novel:basis')
  })
})
