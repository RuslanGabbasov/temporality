import { useCallback, useEffect, useMemo, useState, type ReactNode } from 'react'
import { Button, Heading, InlineNotification, Tag, Toggle } from '@carbon/react'
import PageLoading from './PageLoading'
import { orgApi, type OrgActivityEvent, type OrgActivityFilters, type OrgActivitySettings, type OrgSettingSource } from './orgApi'
import { OrgUnitSelect, useOrgUnits, type OrgUnitsState } from './orgUnits'
import { pluralT, useI18n, useT } from './i18n'
import { briefDateTime, TimeHint } from './timeHint'

// Activity board (docs/org-activity.md): what happened across the
// organization — run/progress/plan/task/artifact/quality/human events scoped
// to an org node, grouped by run so a manager reads one work item as a
// thread. The per-unit activity settings (GET/PUT/DELETE activity-settings)
// live here as a collapsible section: the board is where someone notices
// «why do we not see X» and the switch is one click away.

function message(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }

type TagType = 'red' | 'magenta' | 'purple' | 'blue' | 'cyan' | 'teal' | 'green' | 'gray' | 'cool-gray' | 'warm-gray' | 'high-contrast' | 'outline'

const CATEGORIES = ['run', 'progress', 'plan', 'task', 'artifact', 'quality', 'human'] as const

const CATEGORY_TAGS: Record<string, TagType> = {
  run: 'blue',
  progress: 'cyan',
  plan: 'purple',
  task: 'green',
  artifact: 'teal',
  quality: 'magenta',
  human: 'warm-gray',
}

const CATEGORY_KEYS: Record<string, string> = {
  run: 'orgops.cat_run',
  progress: 'orgops.cat_progress',
  plan: 'orgops.cat_plan',
  task: 'orgops.cat_task',
  artifact: 'orgops.cat_artifact',
  quality: 'orgops.cat_quality',
  human: 'orgops.cat_human',
}

const PAGE_SIZE = 50

const GRID = 'minmax(4.5rem, 0.45fr) minmax(7.5rem, 1fr) minmax(12rem, 2.2fr) minmax(5rem, 0.7fr) minmax(5.5rem, 0.8fr) minmax(5.5rem, 0.8fr) minmax(5.5rem, 0.8fr)'

function shortId(value?: string) {
  if (!value) return '—'
  return value.length > 14 ? `${value.slice(0, 13)}…` : value
}

/** datetime-local input value → RFC 3339 (UTC); empty stays empty. */
function rfc3339(value: string): string | undefined {
  if (!value) return undefined
  const d = new Date(value)
  return Number.isNaN(d.getTime()) ? undefined : d.toISOString()
}

function GridHead({ children }: { children: ReactNode }) {
  return (
    <div style={{ display: 'grid', gridTemplateColumns: GRID, gap: '0.25rem 0.6rem', fontSize: '0.68rem', textTransform: 'uppercase', letterSpacing: '0.04em', color: 'var(--tm-text-3)', padding: '0.35rem 0.6rem', background: 'var(--tm-raised)' }}>
      {children}
    </div>
  )
}

function EventRow({ event }: { event: OrgActivityEvent }) {
  return (
    <div style={{ display: 'grid', gridTemplateColumns: GRID, gap: '0.25rem 0.6rem', alignItems: 'baseline', padding: '0.35rem 0.6rem', borderTop: '1px solid var(--tm-border)', fontSize: '0.78rem' }}>
      <TimeHint value={event.occurred_at} style={{ color: 'var(--tm-text-3)', fontSize: '0.72rem', whiteSpace: 'nowrap' }}>
        {briefDateTime(event.occurred_at)}
      </TimeHint>
      <span style={{ display: 'flex', gap: '0.3rem', alignItems: 'baseline', flexWrap: 'wrap' }}>
        <Tag type={CATEGORY_TAGS[event.category] ?? 'gray'} size="sm">{event.category}</Tag>
        <span className="mono" style={{ fontSize: '0.7rem', color: 'var(--tm-text-2)' }}>{event.event_type}</span>
      </span>
      <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={event.summary}>{event.summary}</span>
      <span className="mono" style={{ fontSize: '0.7rem' }} title={event.actor_id ?? ''}>{shortId(event.actor_id)}</span>
      <span className="mono" style={{ fontSize: '0.7rem' }} title={event.run_id ?? ''}>{shortId(event.run_id)}</span>
      <span className="mono" style={{ fontSize: '0.7rem' }} title={event.task_id ?? ''}>{shortId(event.task_id)}</span>
      <span className="mono" style={{ fontSize: '0.7rem' }} title={event.project_id ?? ''}>{shortId(event.project_id)}</span>
    </div>
  )
}

export default function OrgActivityBoard() {
  const { t, locale } = useI18n()
  const org = useOrgUnits()
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [events, setEvents] = useState<OrgActivityEvent[]>([])
  const [count, setCount] = useState(0)

  // Filters (all server-side; an empty unit means the whole installation).
  const [unit, setUnit] = useState('')
  const [scope, setScope] = useState('subtree')
  const [from, setFrom] = useState('')
  const [to, setTo] = useState('')
  const [category, setCategory] = useState('')
  const [typeFilter, setTypeFilter] = useState('')
  const [runFilter, setRunFilter] = useState('')
  const [taskFilter, setTaskFilter] = useState('')
  const [actorFilter, setActorFilter] = useState('')
  const [offset, setOffset] = useState(0)
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [collapsedRuns, setCollapsedRuns] = useState<Set<string>>(new Set())

  const load = useCallback(async (opts?: { offset?: number; append?: boolean }) => {
    if (!opts?.append) setLoading(true)
    setError('')
    if (!opts?.append) setOffset(0)
    const filters: OrgActivityFilters = {
      org_node: unit || undefined,
      scope,
      from: rfc3339(from),
      to: rfc3339(to),
      category: category || undefined,
      type: typeFilter.trim() || undefined,
      run: runFilter.trim() || undefined,
      task: taskFilter.trim() || undefined,
      actor: actorFilter.trim() || undefined,
      limit: PAGE_SIZE,
      offset: opts?.offset ?? 0,
    }
    try {
      const page = await orgApi.activity(filters)
      setEvents((prev) => (opts?.append ? [...prev, ...(page.events ?? [])] : (page.events ?? [])))
      setCount(page.count ?? 0)
    } catch (failure) {
      setError(message(failure))
    } finally {
      setLoading(false)
    }
  }, [unit, scope, from, to, category, typeFilter, runFilter, taskFilter, actorFilter])

  useEffect(() => { void load() }, [load])

  const clearFilters = () => {
    setUnit(''); setScope('subtree'); setFrom(''); setTo(''); setCategory('')
    setTypeFilter(''); setRunFilter(''); setTaskFilter(''); setActorFilter('')
  }

  // Group events by run: one work item reads as a thread. Standalone events
  // (no run id) stay top-level, newest first — the server already pages desc.
  const groups = useMemo(() => {
    const byRun = new Map<string, OrgActivityEvent[]>()
    const standalone: OrgActivityEvent[] = []
    for (const event of events) {
      if (!event.run_id) { standalone.push(event); continue }
      const list = byRun.get(event.run_id) ?? []
      list.push(event)
      byRun.set(event.run_id, list)
    }
    return { byRun: [...byRun.entries()], standalone }
  }, [events])

  const toggleRun = (runID: string) => {
    setCollapsedRuns((prev) => {
      const next = new Set(prev)
      if (next.has(runID)) next.delete(runID)
      else next.add(runID)
      return next
    })
  }

  const canMore = events.length < count

  return (
    <div style={{ padding: '1rem', maxWidth: '86rem', margin: '0 auto' }}>
      {error && (
        <InlineNotification kind="error" title={t('common.error')} subtitle={error} onClose={() => setError('')} lowContrast style={{ marginBottom: '1rem' }} />
      )}

      <div style={{ marginBottom: '0.75rem' }}>
        <Heading style={{ fontSize: '1.1rem' }}>{t('orgops.activity_title')}</Heading>
        <p style={{ color: 'var(--tm-text-3)', fontSize: '0.8rem', margin: '0.25rem 0 0' }}>{t('orgops.activity_subtitle')}</p>
      </div>

      <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '0.5rem', padding: '0.5rem 0', borderBottom: '1px solid var(--tm-border)' }}>
        <OrgUnitSelect id="orgact-unit" value={unit} onChange={(v) => setUnit(v)} org={org} label={t('orgops.unit')} size="sm" />
        <label className="toolbar-select">
          {t('orgops.scope')}
          <select value={scope} onChange={(e) => setScope(e.target.value)}>
            <option value="unit">{t('orgops.scope_unit')}</option>
            <option value="subtree">{t('orgops.scope_subtree')}</option>
          </select>
        </label>
        <label className="toolbar-select">
          {t('orgops.period_from')}
          <input type="datetime-local" className="toolbar-search" style={{ width: 185 }} value={from} onChange={(e) => setFrom(e.target.value)} />
        </label>
        <label className="toolbar-select">
          {t('orgops.period_to')}
          <input type="datetime-local" className="toolbar-search" style={{ width: 185 }} value={to} onChange={(e) => setTo(e.target.value)} />
        </label>
        <label className="toolbar-select">
          {t('orgops.filter_category')}
          <select value={category} onChange={(e) => setCategory(e.target.value)}>
            <option value="">{t('orgops.filter_all')}</option>
            {CATEGORIES.map((c) => (
              <option key={c} value={c}>{t(CATEGORY_KEYS[c])}</option>
            ))}
          </select>
        </label>
        <input className="toolbar-search" placeholder={t('orgops.filter_type')} value={typeFilter} onChange={(e) => setTypeFilter(e.target.value)} title={t('orgops.filter_type_hint')} />
        <input className="toolbar-search" placeholder={t('orgops.filter_run')} value={runFilter} onChange={(e) => setRunFilter(e.target.value)} />
        <input className="toolbar-search" placeholder={t('orgops.filter_task')} value={taskFilter} onChange={(e) => setTaskFilter(e.target.value)} />
        <input className="toolbar-search" placeholder={t('orgops.filter_actor')} value={actorFilter} onChange={(e) => setActorFilter(e.target.value)} />
        <button className="toolbar-btn" onClick={() => void load()} disabled={loading} title={t('common.refresh')}>↻</button>
        <button className="toolbar-btn" onClick={clearFilters}>{t('common.clear_filters')}</button>
        <span style={{ marginLeft: 'auto', fontSize: '0.7rem', color: 'var(--tm-text-3)' }}>
          {count > 0 && pluralT(t, locale, 'orgops.events_n', count)}
        </span>
      </div>

      {loading ? <PageLoading /> : (
        <>
          {events.length === 0 ? (
            <div style={{ color: 'var(--tm-text-3)', fontSize: '0.8rem', padding: '1.5rem 0', textAlign: 'center' }}>
              {t('orgops.empty')}
            </div>
          ) : (
            <div style={{ marginTop: '0.75rem', border: '1px solid var(--tm-border)', borderRadius: 'var(--tm-radius-md)', overflow: 'hidden' }}>
              <GridHead>
                <span>{t('orgops.col_time')}</span>
                <span>{t('orgops.col_event')}</span>
                <span>{t('orgops.col_summary')}</span>
                <span>{t('orgops.col_actor')}</span>
                <span>{t('orgops.col_run')}</span>
                <span>{t('orgops.col_task')}</span>
                <span>{t('orgops.col_project')}</span>
              </GridHead>
              {groups.byRun.map(([runID, rows]) => {
                const collapsed = collapsedRuns.has(runID)
                return (
                  <div key={runID}>
                    <button
                      type="button"
                      onClick={() => toggleRun(runID)}
                      style={{ width: '100%', display: 'flex', alignItems: 'baseline', gap: '0.5rem', background: 'var(--tm-raised)', border: 'none', borderBottom: collapsed ? '1px solid var(--tm-border)' : 'none', padding: '0.35rem 0.6rem', cursor: 'pointer', fontSize: '0.74rem', color: 'var(--tm-text-2)', textAlign: 'left' }}
                    >
                      <span style={{ color: 'var(--tm-text-3)' }}>{collapsed ? '▸' : '▾'}</span>
                      <span>{pluralT(t, locale, 'orgops.group_n', rows.length, { run: runID })}</span>
                    </button>
                    {!collapsed && rows.map((event) => <EventRow key={event.event_id} event={event} />)}
                  </div>
                )
              })}
              {groups.standalone.map((event) => <EventRow key={event.event_id} event={event} />)}
            </div>
          )}

          {canMore && (
            <div style={{ display: 'flex', justifyContent: 'center', marginTop: '0.75rem' }}>
              <Button
                size="sm"
                kind="ghost"
                onClick={() => {
                  const next = offset + PAGE_SIZE
                  setOffset(next)
                  void load({ offset: next, append: true })
                }}
              >
                {t('orgops.load_more')}
              </Button>
            </div>
          )}
        </>
      )}

      {/* Node settings entry point: effective activity settings for the
          selected unit, with the origin of every value and local overrides. */}
      <div style={{ marginTop: '1.25rem', borderTop: '1px solid var(--tm-border)', paddingTop: '0.6rem' }}>
        <button
          type="button"
          onClick={() => setSettingsOpen((v) => !v)}
          style={{ background: 'none', border: 'none', padding: 0, cursor: 'pointer', fontSize: '0.8rem', color: 'var(--tm-teal)', display: 'flex', alignItems: 'baseline', gap: '0.4rem' }}
        >
          <span>{settingsOpen ? '▾' : '▸'}</span>
          <span>{t('orgops.settings_title')}</span>
        </button>
        {!settingsOpen && unit && (
          <span style={{ marginLeft: '0.5rem', fontSize: '0.7rem', color: 'var(--tm-text-3)' }}>{org.nameOf(unit)}</span>
        )}
        {settingsOpen && (
          unit
            ? <ActivitySettingsPanel key={unit} unitID={unit} org={org} onError={setError} />
            : <p style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem', margin: '0.5rem 0 0' }}>{t('orgops.settings_need_unit')}</p>
        )}
      </div>
    </div>
  )
}

// ── Per-unit activity settings ──────────────────────────────────────────────

type SettingKind = 'bool' | 'number' | 'scope' | 'text'

interface SettingItem {
  key: string
  kind: SettingKind
  effective: unknown
  source?: OrgSettingSource
  isLocal: boolean
}

function effectiveValue(data: OrgActivitySettings, key: string): unknown {
  if (data.settings && key in data.settings) return data.settings[key]
  if (data.values && key in data.values) return data.values[key]
  const source = data.sources?.[key]
  if (source && 'value' in source) return source.value
  return undefined
}

function kindOf(key: string, value: unknown): SettingKind {
  if (key === 'AwarenessScope') return 'scope'
  if (typeof value === 'boolean') return 'bool'
  if (typeof value === 'number') return 'number'
  const text = String(value ?? '')
  if (text === 'true' || text === 'false') return 'bool'
  if (text !== '' && !Number.isNaN(Number(text))) return 'number'
  return 'text'
}

function displayText(value: unknown): string {
  if (Array.isArray(value)) return value.join(', ')
  if (typeof value === 'boolean') return value ? 'true' : 'false'
  return value === undefined || value === null ? '' : String(value)
}

function settingItem(data: OrgActivitySettings, key: string): SettingItem {
  const effective = effectiveValue(data, key)
  return {
    key,
    kind: kindOf(key, effective),
    effective,
    source: data.sources?.[key],
    isLocal: Boolean(data.local && key in data.local),
  }
}

const SCOPE_OPTIONS = ['unit', 'subtree', 'custom']

function OriginBadge({ source, org }: { source?: OrgSettingSource; org: OrgUnitsState }) {
  const t = useT()
  if (!source) return null
  const origin = source.origin
  let type: TagType = 'gray'
  let text = origin
  if (origin === 'local') { type = 'green'; text = t('orgops.origin_local') }
  else if (origin === 'inherited') {
    type = 'cyan'
    text = source.org_unit_id
      ? t('orgops.origin_inherited', { unit: org.nameOf(source.org_unit_id) })
      : t('orgops.origin_inherited_plain')
  } else if (origin === 'system') { type = 'purple'; text = t('orgops.origin_system') }
  else if (origin === 'default') { type = 'warm-gray'; text = t('orgops.origin_default') }
  return (
    <Tag type={type} size="sm" title={displayText(source.value)}>
      {text}
    </Tag>
  )
}

function SettingsRow({ item, org, busy, onSave, onInherit }: {
  item: SettingItem
  org: OrgUnitsState
  busy: boolean
  onSave: (key: string, value: string) => void
  onInherit: (key: string) => void
}) {
  const t = useT()
  const [draft, setDraft] = useState(() => displayText(item.effective))
  useEffect(() => { setDraft(displayText(item.effective)) }, [item.effective])

  const boolValue = item.effective === true || item.effective === 'true'
  const changed = draft !== displayText(item.effective)

  const commit = () => {
    if (!changed || busy) return
    onSave(item.key, draft.trim())
  }

  return (
    <div style={{ display: 'grid', gridTemplateColumns: 'minmax(11rem, 1fr) minmax(9rem, 1.1fr) auto auto', gap: '0.4rem 0.75rem', alignItems: 'center', padding: '0.3rem 0.25rem', borderTop: '1px solid var(--tm-border)', fontSize: '0.78rem' }}>
      <span className="mono" style={{ fontSize: '0.72rem', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={item.key}>{item.key}</span>
      {item.kind === 'bool' && (
        <span title={t('orgops.settings_bool_hint')}>
          <Toggle
            id={`orgact-setting-${item.key}`}
            labelText=""
            hideLabel
            aria-label={item.key}
            toggled={boolValue}
            onToggle={(checked: boolean) => onSave(item.key, checked ? 'true' : 'false')}
            size="sm"
            disabled={busy}
          />
        </span>
      )}
      {item.kind === 'number' && (
        <input
          type="number"
          className="toolbar-search"
          style={{ width: 110 }}
          value={draft}
          disabled={busy}
          onChange={(e) => setDraft(e.target.value)}
          onBlur={commit}
          onKeyDown={(e) => { if (e.key === 'Enter') commit() }}
        />
      )}
      {item.kind === 'scope' && (
        <select
          value={draft}
          disabled={busy}
          className="toolbar-select"
          style={{ fontSize: '0.72rem', padding: '0.26rem 0.35rem', border: '1px solid var(--tm-border)', borderRadius: 'var(--tm-radius-sm)', background: 'var(--tm-surface)', color: 'var(--tm-text)' }}
          onChange={(e) => { setDraft(e.target.value); onSave(item.key, e.target.value) }}
        >
          {[...new Set([...SCOPE_OPTIONS, draft].filter(Boolean))].map((opt) => (
            <option key={opt} value={opt}>{opt}</option>
          ))}
        </select>
      )}
      {item.kind === 'text' && (
        <input
          type="text"
          className="toolbar-search"
          value={draft}
          disabled={busy}
          onChange={(e) => setDraft(e.target.value)}
          onBlur={commit}
          onKeyDown={(e) => { if (e.key === 'Enter') commit() }}
        />
      )}
      <OriginBadge source={item.source} org={org} />
      {item.isLocal
        ? (
          <Button size="sm" kind="ghost" disabled={busy} onClick={() => onInherit(item.key)} title={t('orgops.inherit_hint')}>
            {busy ? '…' : t('orgops.inherit_btn')}
          </Button>
        )
        : <span />}
    </div>
  )
}

function ActivitySettingsPanel({ unitID, org, onError }: {
  unitID: string
  org: OrgUnitsState
  onError: (message: string) => void
}) {
  const { t } = useI18n()
  const [data, setData] = useState<OrgActivitySettings | null>(null)
  const [loading, setLoading] = useState(true)
  const [savingKey, setSavingKey] = useState('')

  const reload = useCallback(async () => {
    setLoading(true)
    try {
      setData(await orgApi.activitySettings(unitID))
    } catch (failure) {
      onError(message(failure))
    } finally {
      setLoading(false)
    }
  }, [unitID, onError])

  useEffect(() => { void reload() }, [reload])

  const save = useCallback(async (key: string, value: string) => {
    setSavingKey(key)
    try {
      await orgApi.putActivitySetting(unitID, key, value)
      await reload()
    } catch (failure) {
      onError(message(failure))
    } finally {
      setSavingKey('')
    }
  }, [unitID, reload, onError])

  const inherit = useCallback(async (key: string) => {
    setSavingKey(key)
    try {
      await orgApi.deleteActivitySetting(unitID, key)
      await reload()
    } catch (failure) {
      onError(message(failure))
    } finally {
      setSavingKey('')
    }
  }, [unitID, reload, onError])

  if (loading && !data) return <PageLoading />
  if (!data) return null

  const keys = data.keys?.length
    ? data.keys
    : [...new Set([...Object.keys(data.values ?? {}), ...Object.keys(data.sources ?? {}), ...Object.keys(data.settings ?? {})])].sort()

  return (
    <div style={{ marginTop: '0.6rem' }}>
      <p style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem', margin: '0 0 0.5rem' }}>
        {t('orgops.settings_hint')} <span className="mono">{org.nameOf(unitID)}</span>
      </p>
      <div style={{ border: '1px solid var(--tm-border)', borderRadius: 'var(--tm-radius-md)', padding: '0.35rem 0.5rem' }}>
        {keys.map((key) => (
          <SettingsRow
            key={key}
            item={settingItem(data, key)}
            org={org}
            busy={savingKey === key}
            onSave={(k, v) => void save(k, v)}
            onInherit={(k) => void inherit(k)}
          />
        ))}
        {keys.length === 0 && (
          <div style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem', padding: '0.5rem 0.25rem' }}>{t('orgops.settings_empty')}</div>
        )}
      </div>
    </div>
  )
}
