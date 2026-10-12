import { useEffect, useState } from 'react'
import { Button, TextInput, InlineNotification, Tag, Stack } from '@carbon/react'
import { TrashCan, Save, Renew } from '@carbon/icons-react'
import {
  workspaceApi,
  type ChannelSettings,
  type ChannelSettingsField,
  type ChannelSettingsGroupSchema,
  type ChannelTypeSpec,
  type ChannelSettingsUpdate,
} from './workspaceApi'
import { useT } from './i18n'

function message(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }

/** Field state for one secret input: what the admin typed plus a pending
 * clear flag. Empty input with no clear = keep the stored value. */
interface SecretFieldState {
  typed: string
  clear: boolean
}

/**
 * Admin channel settings (docs/channels.md §6): per-channel driver
 * credentials, edited in the UI instead of redeploying with new env vars.
 * The form renders from the driver registry schema — adding a channel needs
 * no UI work. The stored payload overlays the KERNEL_CHANNEL__* env vars per
 * field; secrets never travel back from the kernel — only a set-flag and a
 * non-reversible hint.
 */
export default function ChannelSettingsPage() {
  const t = useT()
  const [settings, setSettings] = useState<ChannelSettings | null>(null)
  const [types, setTypes] = useState<ChannelTypeSpec[]>([])
  // Plain (non-secret) field inputs, keyed "type.field". Inputs edit the
  // database overlay; env-sourced fields show empty with the env value as
  // placeholder — saving an empty input keeps the env fallback instead of
  // silently copying env into the database.
  const [values, setValues] = useState<Record<string, string>>({})
  const [uiUrl, setUiUrl] = useState('')
  const [secrets, setSecrets] = useState<Record<string, SecretFieldState>>({})
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const [saved, setSaved] = useState(false)

  const secretState = (key: string): SecretFieldState => secrets[key] ?? { typed: '', clear: false }
  const setSecret = (key: string, patch: Partial<SecretFieldState>) =>
    setSecrets((current) => ({ ...current, [key]: { ...(current[key] ?? { typed: '', clear: false }), ...patch } }))

  const fieldKey = (type: string, name: string) => `${type}.${name}`

  const apply = (data: ChannelSettings) => {
    setSettings(data)
    const next: Record<string, string> = {}
    for (const group of data.schema) {
      for (const field of group.fields) {
        if (field.secret) continue
        const view = data.channels[group.type]?.[field.name]
        next[fieldKey(group.type, field.name)] = view?.source === 'database' ? (view.value ?? '') : ''
      }
    }
    setValues(next)
    setUiUrl(data.ui_url.source === 'database' ? (data.ui_url.value ?? '') : '')
    setSecrets({})
  }

  const load = async () => {
    setLoading(true)
    setError('')
    try {
      const [data, registry] = await Promise.all([
        workspaceApi.getChannelSettings(),
        workspaceApi.channelTypes().catch(() => ({ types: [] as ChannelTypeSpec[] })),
      ])
      apply(data)
      setTypes(registry.types)
    } catch (e) {
      setError(message(e))
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => { void load() }, [])

  const save = async () => {
    setError('')
    setSaved(false)
    const update: ChannelSettingsUpdate = { ui_url: uiUrl }
    const channels: Record<string, Record<string, string>> = {}
    const clear: string[] = []
    const addValue = (type: string, name: string, value: string) => {
      if (!channels[type]) channels[type] = {}
      channels[type][name] = value
    }
    for (const group of settings?.schema ?? []) {
      for (const field of group.fields) {
        const key = fieldKey(group.type, field.name)
        if (!field.secret) {
          addValue(group.type, field.name, values[key] ?? '')
          continue
        }
        const state = secretState(key)
        if (state.typed.trim()) addValue(group.type, field.name, state.typed.trim())
        if (state.clear) clear.push(key)
      }
    }
    if (Object.keys(channels).length > 0) update.channels = channels
    if (clear.length > 0) update.clear = clear
    setSaving(true)
    try {
      const data = await workspaceApi.updateChannelSettings(update)
      apply(data)
      const registry = await workspaceApi.channelTypes().catch(() => ({ types: [] as ChannelTypeSpec[] }))
      setTypes(registry.types)
      setSaved(true)
    } catch (e) {
      setError(message(e))
    } finally {
      setSaving(false)
    }
  }

  const sourceLabel = (source: string) =>
    source === 'database' ? (t('channels_admin.source_db') ?? 'settings')
      : source === 'env' ? (t('channels_admin.source_env') ?? 'env')
        : (t('channels_admin.source_none') ?? 'not set')

  /** Row for a secret field: current state (set + hint + source), an input
   * that replaces the value when non-empty, and a clear action. */
  const secretRow = (group: ChannelSettingsGroupSchema, name: string, label: string) => {
    const key = fieldKey(group.type, name)
    const field: ChannelSettingsField | undefined = settings ? settings.channels[group.type]?.[name] : undefined
    const state = secretState(key)
    const willClear = state.clear
    const willSet = state.typed.trim() !== ''
    return (
      <div key={key} style={{ marginBottom: '1rem' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', marginBottom: '0.25rem' }}>
          <span className="cds--label" style={{ marginBottom: 0 }}>{label}</span>
          {field?.set && !willClear && !willSet && <Tag size="sm" type="green">{t('channels_admin.set') ?? 'configured'} · {field.hint}</Tag>}
          {field?.set && field.source !== 'none' && <Tag size="sm" type="gray">{sourceLabel(field.source)}</Tag>}
          {willClear && <Tag size="sm" type="red">{t('channels_admin.will_clear') ?? 'will be cleared'}</Tag>}
          {field && !field.set && !willSet && <Tag size="sm" type="gray">{t('channels_admin.not_set') ?? 'not configured'}</Tag>}
          {field?.set && !willClear && (
            <button
              type="button"
              className="tm-icon-button"
              title={t('channels_admin.clear') ?? 'Clear stored value'}
              onClick={() => setSecret(key, { clear: true })}
              style={{ background: 'none', border: 'none', cursor: 'pointer', padding: '0.15rem', color: 'var(--tm-text-3)', display: 'flex' }}
            >
              <TrashCan size={14} />
            </button>
          )}
        </div>
        <TextInput
          id={`channel-${key}`}
          type="password"
          labelText=""
          placeholder={willClear ? (t('channels_admin.cleared_placeholder') ?? 'will be cleared') : (t('channels_admin.replace_hint') ?? 'leave empty to keep the stored value')}
          value={state.typed}
          disabled={willClear}
          onChange={(e: React.ChangeEvent<HTMLInputElement>) => setSecret(key, { typed: e.target.value, clear: false })}
        />
      </div>
    )
  }

  /** Row for a plain field: editable input; the env value rides as the
   * placeholder so the admin sees what applies without copying it. */
  const plainRow = (group: ChannelSettingsGroupSchema, name: string, label: string, placeholder: string) => {
    const key = fieldKey(group.type, name)
    const field: ChannelSettingsField | undefined = settings ? settings.channels[group.type]?.[name] : undefined
    return (
      <div key={key} style={{ marginBottom: '1rem' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', marginBottom: '0.25rem' }}>
          <span className="cds--label" style={{ marginBottom: 0 }}>{label}</span>
          {field && field.source !== 'none' && <Tag size="sm" type="gray">{sourceLabel(field.source)}</Tag>}
        </div>
        <TextInput
          id={`channel-${key}`}
          labelText=""
          placeholder={field?.source === 'env' ? field.value : placeholder}
          value={values[key] ?? ''}
          onChange={(e: React.ChangeEvent<HTMLInputElement>) => setValues((current) => ({ ...current, [key]: e.target.value }))}
        />
      </div>
    )
  }

  if (loading) {
    return <div className="tm-page"><p style={{ color: 'var(--tm-text-3)' }}>{t('common.loading') ?? 'Loading…'}</p></div>
  }

  return (
    <div className="tm-page">
      <h2 style={{ marginTop: 0 }}>{t('channels_admin.title') ?? 'Delivery channels'}</h2>
      <p style={{ color: 'var(--tm-text-3)', maxWidth: '46rem' }}>
        {t('channels_admin.intro') ?? 'Kernel-side credentials for delivering agent questions. Values set here take effect immediately and override the environment variables.'}
      </p>

      <div style={{ display: 'flex', gap: '0.5rem', flexWrap: 'wrap', marginBottom: '1.5rem' }}>
        {types.map((spec) => (
          <Tag key={spec.type} size="sm" type={spec.configured ? 'green' : 'gray'}>
            {spec.label}: {spec.configured ? (t('channels_admin.ready') ?? 'ready') : (spec.not_configured_hint ?? 'not configured')}
          </Tag>
        ))}
      </div>

      {error && <InlineNotification kind="error" title={t('common.error') ?? 'Error'} subtitle={error} lowContrast />}
      {saved && <InlineNotification kind="success" title={t('channels_admin.saved') ?? 'Saved'} subtitle={t('channels_admin.saved_body') ?? 'Transport settings applied.'} lowContrast />}

      <div style={{ maxWidth: '34rem' }}>
        <Stack gap={4}>
          {(settings?.schema ?? []).map((group) => (
            <div key={group.type}>
              <h3 style={{ fontSize: '0.875rem', marginBottom: '0.5rem' }}>{group.label}</h3>
              {group.fields.map((field) =>
                field.secret
                  ? secretRow(group, field.name, field.label)
                  : plainRow(group, field.name, field.label, field.name === 'homeserver' ? 'https://matrix.org' : ''),
              )}
            </div>
          ))}

          <div>
            <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', marginBottom: '0.25rem' }}>
              <span className="cds--label" style={{ marginBottom: 0 }}>UI URL</span>
              {settings && settings.ui_url.source !== 'none' && <Tag size="sm" type="gray">{sourceLabel(settings.ui_url.source)}</Tag>}
            </div>
            <TextInput
              id="channel-ui-url"
              labelText=""
              placeholder={settings && settings.ui_url.source === 'env' ? settings.ui_url.value : 'http://localhost:3000'}
              value={uiUrl}
              onChange={(e: React.ChangeEvent<HTMLInputElement>) => setUiUrl(e.target.value)}
            />
            <p style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem', margin: '0.25rem 0 0' }}>
              {t('channels_admin.ui_url_hint') ?? 'Base URL of this UI — used for answer links inside notifications.'}
            </p>
          </div>

          <div style={{ display: 'flex', gap: '0.5rem' }}>
            <Button size="sm" renderIcon={Save} disabled={saving} onClick={() => void save()}>
              {saving ? (t('common.saving') ?? 'Saving…') : (t('action.save') ?? 'Save')}
            </Button>
            <Button size="sm" kind="secondary" renderIcon={Renew} disabled={saving} onClick={() => void load()}>
              {t('action.refresh') ?? 'Refresh'}
            </Button>
          </div>
        </Stack>
      </div>
    </div>
  )
}
