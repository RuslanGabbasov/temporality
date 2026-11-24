import { describe, expect, it } from 'vitest'
import { byUrgency, clusterTodos, dueKey, dueState, formatCost, formatDue, formatTokens, runLabel, todoTotal, todayKey, todayTodos } from './gtd'
import type { UserTodo } from '../kernelApi'

function todo(group: string, text: string, extra: Partial<UserTodo> = {}): UserTodo {
  return { id: text, user_id: 'u', group: group as UserTodo['group'], text, sort: 0, created_at: '', updated_at: '', ...extra }
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

describe('due dates (TickTick-lite)', () => {
  const today = '2026-11-24'

  it('todayKey renders a local YYYY-MM-DD', () => {
    expect(todayKey(new Date(2026, 10, 24, 23, 5))).toBe('2026-11-24')
  })

  it('classifies due state; done work never alarms', () => {
    expect(dueState(todo('next', 'a', { due: '2026-11-20T00:00:00Z' }), today)).toBe('overdue')
    expect(dueState(todo('next', 'b', { due: '2026-11-24' }), today)).toBe('today')
    expect(dueState(todo('next', 'c', { due: '2026-12-01' }), today)).toBe('future')
    expect(dueState(todo('next', 'd'))).toBeNull()
    expect(dueState(todo('done', 'e', { due: '2026-11-20' }), today)).toBeNull()
    expect(dueKey(todo('waiting', 'f', { due: '2026-11-24T13:45:00Z' }))).toBe('2026-11-24')
  })

  it('todayTodos lists undone due-today-and-overdue, urgent first', () => {
    const list = [
      todo('someday', 'undated'),
      todo('done', 'finished', { due: '2026-11-01' }),
      todo('next', 'future', { due: '2026-12-01' }),
      todo('waiting', 'today', { due: '2026-11-24' }),
      todo('inbox', 'overdue', { due: '2026-11-21' }),
    ]
    expect(todayTodos(list, today).map((t) => t.text)).toEqual(['overdue', 'today'])
    expect(todayTodos(null)).toEqual([])
  })

  it('byUrgency puts dated work first, then manual sort', () => {
    const a = todo('inbox', 'a', { sort: 5 })
    const b = todo('inbox', 'b', { due: '2026-11-24' })
    const c = todo('inbox', 'c', { sort: 1 })
    expect([a, b, c].sort(byUrgency).map((t) => t.text)).toEqual(['b', 'c', 'a'])
  })

  it('formatDue renders a short label', () => {
    expect(formatDue('2026-11-24')).toBe('24.11')
    expect(formatDue(undefined)).toBe('')
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
