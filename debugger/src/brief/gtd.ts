import { normalizeTodoGroup, TODO_GROUPS, type TodoGroup, type UserBriefRun, type UserTodo } from '../kernelApi'

// GTD clustering helpers for the personal brief's todo widget (docs/system-
// dashboard.md «Мой день»). Pure functions — unit-tested.

export const GROUP_LABEL_KEYS: Record<TodoGroup, string> = {
  inbox: 'myday.group_inbox',
  next: 'myday.group_next',
  waiting: 'myday.group_waiting',
  someday: 'myday.group_someday',
  done: 'myday.group_done',
}

export type TodoClusters = Record<TodoGroup, UserTodo[]>

/** Cluster the flat todo list by GTD group, in the canonical group order.
 *  Unknown groups fold into the inbox (same rule the kernel applies on
 *  write), so a stale row can never vanish from the board. */
export function clusterTodos(todos: UserTodo[] | null | undefined): TodoClusters {
  const out = { inbox: [], next: [], waiting: [], someday: [], done: [] } as TodoClusters
  for (const todo of todos ?? []) {
    out[normalizeTodoGroup(todo.group)].push(todo)
  }
  return out
}

export function todoTotal(clusters: TodoClusters): number {
  return TODO_GROUPS.reduce((sum, g) => sum + clusters[g].length, 0)
}

/** One line for a run row: the task title when the kernel resolved it, else
 *  a short run id — never a raw empty string. */
export function runLabel(run: UserBriefRun): string {
  const title = (run.title ?? '').trim()
  if (title) return title
  return run.run_id ? run.run_id.slice(0, 8) : '—'
}

/** Compact token counts: 12.3k / 2.4M — the widget line stays one line. */
export function formatTokens(n: number): string {
  if (!Number.isFinite(n) || n <= 0) return '0'
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}k`
  return String(n)
}

export function formatCost(usd: number): string {
  if (!Number.isFinite(usd) || usd <= 0) return '$0.00'
  return `$${usd.toFixed(2)}`
}

/** «24.11 08:31»-style stamp for run/approval rows. */
export function formatStamp(iso: string | undefined, locale = 'ru'): string {
  if (!iso) return ''
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  return new Intl.DateTimeFormat(locale, { day: '2-digit', month: '2-digit', hour: '2-digit', minute: '2-digit' }).format(d)
}
