import { useCallback, useEffect, useState } from 'react'
import { Button, InlineNotification, Tag, Toggle } from '@carbon/react'
import PageLoading from './PageLoading'
import { orgApi, type OrgActivitySettings, type OrgSettingSource } from './orgApi'
import { useI18n, useT } from './i18n'
import type { OrgUnitsState } from './orgUnits'

// Per-unit activity settings (docs/temporality-organizational-operations-
// spec.md §5), rendered on the Org structure page under the selected unit.
// ONE preset switch decides the essentials — collection, boards, awareness —
// with template presets; the full 20-key grammar stays available behind the
// advanced expander for the rare fine-tuning case. Every value shows its
// origin (local / inherited / system / default), every key is localized.

type TagType = 'red' | 'magenta' | 'purple' | 'blue' | 'cyan' | 'teal' | 'green' | 'gray' | 'cool-gray' | 'warm-gray' | 'high-contrast' | 'outline'

function message(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }

/** snake_case storage key → localized label. */
const LABEL_KEYS: Record<string, string> = {
  activity_enabled: 'orgset.activity_enabled',
  activity_board_enabled: 'orgset.activity_board_enabled',
  agent_awareness_enabled: 'orgset.agent_awareness_enabled',
  management_view_enabled: 'orgset.management_view_enabled',
  publish_run_starts: 'orgset.publish_run_starts',
  publish_run_completions: 'orgset.publish_run_completions',
  publish_run_failures: 'orgset.publish_run_failures',
  publish_significant_progress: 'orgset.publish_significant_progress',
  publish_artifact_events: 'orgset.publish_artifact_events',
  publish_quality_events: 'orgset.publish_quality_events',
  allow_trace_discovery: 'orgset.allow_trace_discovery',
  awareness_scope: 'orgset.awareness_scope',
  awareness_scope_units: 'orgset.awareness_scope_units',
  batch_interval_minutes: 'orgset.batch_interval_minutes',
  urgent_processing_enabled: 'orgset.urgent_processing_enabled',
  max_events_per_batch: 'orgset.max_events_per_batch',
  max_recipients_per_event: 'orgset.max_recipients_per_event',
  max_deliveries_per_run_per_window: 'orgset.max_deliveries_per_run_per_window',
  activity_retention: 'orgset.activity_retention',
  delivery_retention: 'orgset.delivery_retention',
}

const SCOPE_LABEL_KEYS: Record<string, string> = {
  unit: 'orgset.scope_unit',
  subtree: 'orgset.scope_subtree',
  custom: 'orgset.scope_custom',
}

// The four keys a preset decides (spec §5: the features are independent, the
// presets just make the everyday choice one click).
const PRESET_KEYS = ['activity_enabled', 'activity_board_enabled', 'agent_awareness_enabled', 'management_view_enabled'] as const

type PresetName = 'off' | 'observe' | 'full'

const PRESET_VALUES: Record<PresetName, Record<string, string>> = {
  off: {
    activity_enabled: 'false',
    activity_board_enabled: 'false',
    agent_awareness_enabled: 'false',
    management_view_enabled: 'false',
  },
  observe: {
    activity_enabled: 'true',
    activity_board_enabled: 'true',
    agent_awareness_enabled: 'false',
    management_view_enabled: 'true',
  },
  full: {
    activity_enabled: 'true',
    activity_board_enabled: 'true',
    agent_awareness_enabled: 'true',
    management_view_enabled: 'true',
  },
}

const PRESET_LABEL_KEYS: Record<PresetName, string> = {
  off: 'orgops.preset_off',
  observe: 'orgops.preset_observe',
  full: 'orgops.preset_full',
}

const PRESET_HINT_KEYS: Record<PresetName, string> = {
  off: 'orgops.preset_off_hint',
  observe: 'orgops.preset_observe_hint',
  full: 'orgops.preset_full_hint',
}

/** Which template the current effective values match; 'custom' when none. */
function detectPreset(data: OrgActivitySettings): PresetName | 'custom' {
  const value = (key: string): boolean => {
    const source = data.sources?.[key]
    const raw = source && 'value' in source ? source.value : data.values?.[key]
    return raw === 'true'
  }
  for (const preset of ['full', 'observe', 'off'] as PresetName[]) {
    const template = PRESET_VALUES[preset]
    if (PRESET_KEYS.every((key) => value(key) === (template[key] === 'true'))) return preset
  }
  return 'custom'
}

// ── advanced table (localized rows, origin badges, inherit) ─────────────────

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
  if (key === 'awareness_scope') return 'scope'
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
  const { t } = useI18n()
  const [draft, setDraft] = useState(() => displayText(item.effective))
  useEffect(() => { setDraft(displayText(item.effective)) }, [item.effective])

  const boolValue = item.effective === true || item.effective === 'true'
  const changed = draft !== displayText(item.effective)
  const label = t(LABEL_KEYS[item.key]) ?? item.key

  const commit = () => {
    if (!changed || busy) return
    onSave(item.key, draft.trim())
  }

  return (
    <div style={{ display: 'grid', gridTemplateColumns: 'minmax(11rem, 1fr) minmax(9rem, 1.1fr) auto auto', gap: '0.4rem 0.75rem', alignItems: 'center', padding: '0.3rem 0.25rem', borderTop: '1px solid var(--tm-border)', fontSize: '0.78rem' }}>
      <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={item.key}>{label}</span>
      {item.kind === 'bool' && (
        <span title={t('orgops.settings_bool_hint')}>
          <Toggle
            id={`orgset-${item.key}`}
            labelText=""
            hideLabel
            aria-label={label}
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
          {[...new Set(['unit', 'subtree', 'custom', draft].filter(Boolean))].map((opt) => (
            <option key={opt} value={opt}>{t(SCOPE_LABEL_KEYS[opt]) ?? opt}</option>
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

// ── the panel: preset switch + advanced expander ────────────────────────────

export default function OrgActivitySettingsPanel({ unitID, org }: {
  unitID: string
  org: OrgUnitsState
}) {
  const { t } = useI18n()
  const [data, setData] = useState<OrgActivitySettings | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [advancedOpen, setAdvancedOpen] = useState(false)
  const [savingKey, setSavingKey] = useState('')

  const reload = useCallback(async () => {
    try {
      setData(await orgApi.activitySettings(unitID))
    } catch (failure) {
      setError(message(failure))
    } finally {
      setLoading(false)
    }
  }, [unitID])

  useEffect(() => { setLoading(true); void reload() }, [reload])

  const applyValues = useCallback(async (values: Record<string, string>) => {
    setBusy(true)
    setError('')
    try {
      await orgApi.putActivitySettings(unitID, values)
      await reload()
    } catch (failure) {
      setError(message(failure))
    } finally {
      setBusy(false)
    }
  }, [unitID, reload])

  const save = useCallback(async (key: string, value: string) => {
    setSavingKey(key)
    try {
      await orgApi.putActivitySetting(unitID, key, value)
      await reload()
    } catch (failure) {
      setError(message(failure))
    } finally {
      setSavingKey('')
    }
  }, [unitID, reload])

  const inherit = useCallback(async (key: string) => {
    setSavingKey(key)
    try {
      await orgApi.deleteActivitySetting(unitID, key)
      await reload()
    } catch (failure) {
      setError(message(failure))
    } finally {
      setSavingKey('')
    }
  }, [unitID, reload])

  if (loading && !data) return <PageLoading />

  const preset = data ? detectPreset(data) : 'custom'
  const keys = data?.keys?.length
    ? data.keys
    : [...new Set([...Object.keys(data?.values ?? {}), ...Object.keys(data?.sources ?? {}), ...Object.keys(data?.settings ?? {})])].sort()

  return (
    <div>
      {error && (
        <InlineNotification kind="error" title={t('common.error')} subtitle={error} onClose={() => setError('')} lowContrast style={{ marginBottom: '0.5rem' }} />
      )}
      <p style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem', margin: '0.25rem 0 0.5rem' }}>
        {t('orgops.preset_hint')}
      </p>

      {/* The one switch: template presets as pills, teal when active. */}
      <div style={{ display: 'flex', gap: '0.4rem', flexWrap: 'wrap', alignItems: 'center' }}>
        {(['off', 'observe', 'full'] as PresetName[]).map((name) => (
          <label key={name} className={`lens-toggle ${preset === name ? 'on' : ''}`}>
            <input
              type="checkbox"
              checked={preset === name}
              disabled={busy}
              onChange={() => void applyValues(PRESET_VALUES[name])}
              title={t(PRESET_HINT_KEYS[name]) ?? undefined}
            />
            {t(PRESET_LABEL_KEYS[name])}
          </label>
        ))}
        {preset === 'custom' && (
          <Tag type="purple" size="sm" title={t('orgops.preset_custom_hint')}>{t('orgops.preset_custom')}</Tag>
        )}
      </div>
      {preset !== 'custom' && (
        <p style={{ color: 'var(--tm-text-3)', fontSize: '0.72rem', margin: '0.4rem 0 0' }}>
          {t(PRESET_HINT_KEYS[preset as PresetName])}
        </p>
      )}

      {/* Fine tuning: the full localized grammar with origins. */}
      <div style={{ marginTop: '0.7rem' }}>
        <button
          type="button"
          onClick={() => setAdvancedOpen((v) => !v)}
          style={{ background: 'none', border: 'none', padding: 0, cursor: 'pointer', fontSize: '0.76rem', color: 'var(--tm-teal)', display: 'flex', alignItems: 'baseline', gap: '0.4rem' }}
        >
          <span>{advancedOpen ? '▾' : '▸'}</span>
          <span>{t('orgops.advanced')}</span>
        </button>
        {advancedOpen && data && (
          <div style={{ marginTop: '0.4rem', border: '1px solid var(--tm-border)', borderRadius: 'var(--tm-radius-md)', padding: '0.35rem 0.5rem' }}>
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
        )}
      </div>
    </div>
  )
}
