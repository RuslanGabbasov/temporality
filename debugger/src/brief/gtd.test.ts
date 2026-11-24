import { describe, expect, it } from 'vitest'
import { clusterTodos, formatCost, formatTokens, runLabel, todoTotal } from './gtd'
import type { UserTodo } from '../kernelApi'

function todo(group: string, text: string): UserTodo {
  return { id: text, user_id: 'u', group: group as UserTodo['group'], text, sort: 0, created_at: '', updated_at: '' }
}

describe('clusterTodos', () => {
  it('clusters by GTD group in the canonical order', () => {
    const clusters = clusterTodos([todo('next', 'a'), todo('inbox', 'b'), todo('done', 'c')])
    expect(clusters.next.map((t) => t.text)).toEqual(['a'])
    expect(clusters.inbox.map((t) => t.text)).toEqual(['b'])
    expect(clusters.done.map((t) => t.text)).toEqual(['c'])
    expect(todoTotal(clusters)).toBe(3)
  })

  it('folds unknown groups into the inbox instead of dropping the row', () => {
    const clusters = clusterTodos([todo('archive', 'stale'), todo('', 'blank')])
    expect(clusters.inbox.map((t) => t.text)).toEqual(['stale', 'blank'])
    expect(todoTotal(clusters)).toBe(2)
  })

  it('survives null/undefined lists', () => {
    expect(todoTotal(clusterTodos(null))).toBe(0)
    expect(todoTotal(clusterTodos(undefined))).toBe(0)
  })
})

describe('runLabel', () => {
  it('prefers the task title', () => {
    expect(runLabel({ run_id: 'abc123def', status: 'failed', at: '', title: 'Импорт данных' })).toBe('Импорт данных')
  })
  it('falls back to a short run id', () => {
    expect(runLabel({ run_id: 'abc123def456', status: 'failed', at: '' })).toBe('abc123de')
    expect(runLabel({ run_id: '', status: 'failed', at: '' })).toBe('—')
  })
})

describe('formatters', () => {
  it('compacts token counts', () => {
    expect(formatTokens(0)).toBe('0')
    expect(formatTokens(950)).toBe('950')
    expect(formatTokens(3400)).toBe('3.4k')
    expect(formatTokens(2_400_000)).toBe('2.4M')
  })
  it('formats cost', () => {
    expect(formatCost(0)).toBe('$0.00')
    expect(formatCost(1.5)).toBe('$1.50')
  })
})
