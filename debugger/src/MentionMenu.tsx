import { useCallback, useEffect, useMemo, useRef, useState, type CSSProperties, type KeyboardEvent, type RefObject } from 'react'
import { workspaceApi } from './workspaceApi'
import { projectBudget } from './kernelApi'
import { observationApi } from './observationApi'
import { useT } from './i18n'
import {
  buildMentionRows,
  insertMentionText,
  mentionInsertText,
  mentionTokenAt,
  mentionTokenKey,
  type MentionOption,
  type MentionRow,
  type MentionToken,
} from './mentions'

/** Cap per source: the menu is a recall aid, not a registry browser — the
 *  kernel resolves any id regardless of whether the menu knew it. */
const OPTIONS_PER_SOURCE = 200

async function loadMentionOptions(project: string): Promise<MentionOption[]> {
  const safe = <T,>(promise: Promise<T>, fallback: T): Promise<T> => promise.catch(() => fallback)
  const [artifacts, skills, teams, agents, tasks, sagas, knowledge] = await Promise.all([
    safe(workspaceApi.listArtifacts({ limit: OPTIONS_PER_SOURCE }), { artifacts: [], count: 0 }),
    safe(workspaceApi.listSkills(), { skills: [], count: 0 }),
    safe(workspaceApi.listTeams(), { teams: [], count: 0 }),
    safe(workspaceApi.listAllAgents(), { agents: [], pending_proposals: {} }),
    safe(workspaceApi.listTasks(project), { tasks: [] }),
    safe(projectBudget(project).then((budget) => ({ sagas: budget.sagas ?? [] })), { sagas: [] }),
    safe(observationApi.knowledge(project, undefined, undefined, { limit: OPTIONS_PER_SOURCE }), { knowledge: [], count: 0 }),
  ])
  const clip = (text: string, n = 120) => (text.length > n ? text.slice(0, n - 1) + '…' : text)
  const options: MentionOption[] = [
    ...(artifacts?.artifacts ?? []).map((a) => ({ kind: 'artifact' as const, id: a.id, title: a.title || a.id, detail: a.status })),
    ...(skills?.skills ?? []).map((s) => ({ kind: 'skill' as const, id: s.id, title: s.name || s.id, detail: s.enabled === false ? 'disabled' : undefined })),
    ...(teams?.teams ?? []).map((tm) => ({ kind: 'team' as const, id: tm.id, title: tm.name || tm.id, detail: tm.enabled === false ? 'disabled' : undefined })),
    ...(agents?.agents ?? []).map((a) => ({ kind: 'agent' as const, id: a.id, title: a.name || a.id, detail: a.enabled === false ? 'disabled' : undefined })),
    ...(tasks?.tasks ?? []).map((task) => ({ kind: 'task' as const, id: task.id, title: task.title || task.id })),
    ...(sagas?.sagas ?? []).map((s) => ({ kind: 'saga' as const, id: s.saga_id, title: s.title || s.saga_id, detail: s.goal ? clip(s.goal, 80) : undefined })),
    ...(knowledge?.knowledge ?? []).map((k) => ({ kind: 'knowledge' as const, id: k.id, title: clip(k.proposition || k.id), detail: k.state })),
  ]
  options.sort((a, b) => a.title.localeCompare(b.title))
  return options
}

export interface MentionComposer {
  rows: MentionRow[]
  activeIndex: number
  loading: boolean
  open: boolean
  setActiveIndex: (index: number) => void
  /** Pick the active/pointed row. Returns true when the key was consumed. */
  selectRow: (row: MentionRow) => void
  /** Re-derive the token from text + caret (input, caret moves, mouse). */
  sync: (text?: string, caret?: number) => void
  /** Enter/arrows/Escape while the menu is open; call before send-on-Enter. */
  onKeyDown: (event: KeyboardEvent) => boolean
}

/** The composer side of @-mentions: watches the textarea for an `@token`,
 *  lazy-loads the entity registry on first open and edits the token span on
 *  pick. The dropdown itself is the MentionMenu component. */
export function useMentionMenu({ value, textareaRef, setValue, project }: {
  value: string
  textareaRef: RefObject<HTMLTextAreaElement | null>
  /** Apply the edited text and move the caret on the next frame. */
  setValue: (text: string, caret: number) => void
  project: string
}): MentionComposer {
  const [token, setToken] = useState<MentionToken | null>(null)
  const [options, setOptions] = useState<MentionOption[] | null>(null)
  const [loading, setLoading] = useState(false)
  const [activeIndex, setActiveIndex] = useState(0)

  const valueRef = useRef(value)
  valueRef.current = value
  const optionsRef = useRef<MentionOption[] | null>(null)
  const loadingRef = useRef(false)
  const dismissedRef = useRef('')

  const rows = useMemo(() => (token ? buildMentionRows(token.query, options ?? []) : []), [token, options])
  // Fresh token or fresh rows: start at the top again.
  useEffect(() => { setActiveIndex(0) }, [token, rows])

  const sync = useCallback((text?: string, caret?: number) => {
    const full = text ?? valueRef.current
    const pos = caret ?? textareaRef.current?.selectionStart ?? full.length
    const found = mentionTokenAt(full, pos)
    const usable = found && mentionTokenKey(found) !== dismissedRef.current ? found : null
    setToken((prev) => {
      if (prev && usable && mentionTokenKey(prev) === mentionTokenKey(usable)) return prev
      return usable
    })
    if (usable && optionsRef.current === null && !loadingRef.current) {
      loadingRef.current = true
      setLoading(true)
      void loadMentionOptions(project)
        .then((loaded) => { optionsRef.current = loaded; setOptions(loaded) })
        .finally(() => { loadingRef.current = false; setLoading(false) })
    }
  }, [project, textareaRef])

  const selectRow = useCallback((row: MentionRow) => {
    const current = token
    if (!current) return
    const insert = mentionInsertText(row)
    const { text, caret } = insertMentionText(valueRef.current, current, insert)
    valueRef.current = text
    if (row.type === 'entity') {
      // Closed token (`@artifact:id `) — no menu until a new `@` appears.
      dismissedRef.current = mentionTokenKey(current)
      setToken(null)
    } else {
      // The scaffold keeps the menu open, now narrowed to the picked kind.
      setToken({ start: current.start, end: current.start + insert.length, query: `${row.kind}:` })
    }
    setValue(text, caret)
  }, [token, setValue])

  const onKeyDown = useCallback((event: KeyboardEvent) => {
    if (!token || rows.length === 0) return false
    if (event.key === 'ArrowDown') {
      event.preventDefault()
      setActiveIndex((i) => (i + 1) % rows.length)
      return true
    }
    if (event.key === 'ArrowUp') {
      event.preventDefault()
      setActiveIndex((i) => (i - 1 + rows.length) % rows.length)
      return true
    }
    if (event.key === 'Enter' || event.key === 'Tab') {
      event.preventDefault()
      selectRow(rows[Math.min(activeIndex, rows.length - 1)] ?? rows[0])
      return true
    }
    if (event.key === 'Escape') {
      event.preventDefault()
      dismissedRef.current = mentionTokenKey(token)
      setToken(null)
      return true
    }
    return false
  }, [token, rows, activeIndex, selectRow])

  return {
    rows,
    activeIndex,
    loading,
    open: token !== null && (rows.length > 0 || loading),
    setActiveIndex,
    selectRow,
    sync,
    onKeyDown,
  }
}

/** The dropdown itself: kinds and entities, hover + click pick (mousedown so
 *  the textarea keeps focus), opens upward from the composer. */
export function MentionMenu({ rows, activeIndex, loading, onSelect, onHover }: {
  rows: MentionRow[]
  activeIndex: number
  loading: boolean
  onSelect: (row: MentionRow) => void
  onHover: (index: number) => void
}) {
  const t = useT()
  return (
    <div
      role="listbox"
      style={{
        position: 'absolute', bottom: 'calc(100% + 6px)', left: 0, right: 0, zIndex: 30,
        maxHeight: '17rem', overflowY: 'auto', padding: '0.25rem',
        background: 'var(--tm-elevated, #1a1a1a)', border: '1px solid var(--tm-border)',
        borderRadius: '8px', boxShadow: '0 8px 24px rgba(0,0,0,.3)',
      }}
    >
      {loading && rows.length === 0 && (
        <div style={{ padding: '0.45rem 0.7rem', fontSize: '0.78rem', color: 'var(--tm-text-3)' }}>
          {t('mentions.loading') ?? 'Loading…'}
        </div>
      )}
      {rows.map((row, i) => {
        const active = i === activeIndex
        const style: CSSProperties = {
          display: 'flex', alignItems: 'center', gap: '0.5rem', width: '100%',
          padding: '0.35rem 0.7rem', cursor: 'pointer', border: 0, textAlign: 'left',
          color: 'inherit', fontFamily: 'inherit', fontSize: '0.85rem',
          background: active ? 'rgba(127,127,127,.14)' : 'transparent',
        }
        if (row.type === 'kind') {
          return (
            <button
              key={`kind:${row.kind}`}
              type="button"
              role="option"
              aria-selected={active}
              onMouseDown={(e) => { e.preventDefault(); onSelect(row) }}
              onMouseEnter={() => onHover(i)}
              style={style}
            >
              <span style={{ fontFamily: 'monospace', fontSize: '0.78rem', color: 'var(--tm-accent, #78a9ff)' }}>@{row.kind}</span>
              <span style={{ fontSize: '0.78rem', color: 'var(--tm-text-3)' }}>{t(`mentions.kind.${row.kind}`) ?? row.kind}</span>
            </button>
          )
        }
        const { option } = row
        return (
          <button
            key={`${option.kind}:${option.id}`}
            type="button"
            role="option"
            aria-selected={active}
            onMouseDown={(e) => { e.preventDefault(); onSelect(row) }}
            onMouseEnter={() => onHover(i)}
            style={style}
          >
            <span style={{ display: 'flex', flexDirection: 'column', minWidth: 0, flex: 1 }}>
              <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{option.title}</span>
              <span style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                {t(`mentions.kind.${option.kind}`) ?? option.kind}{option.detail ? ` · ${option.detail}` : ''}
              </span>
            </span>
            <span style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)', fontFamily: 'monospace', flexShrink: 0 }}>
              @{option.kind}:{option.id}
            </span>
          </button>
        )
      })}
    </div>
  )
}
