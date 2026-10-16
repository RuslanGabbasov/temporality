import { useEffect, useMemo, useRef, useState } from 'react'
import { Accordion, AccordionItem, Button, TextInput, InlineNotification, Tag } from '@carbon/react'
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
 *
 * One accordion section per channel: configured channels come first and open
 * by default, the rest stay collapsed with a "not configured" tag. One Save
 * persists the whole page.
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
  // Which accordion sections are open, keyed by channel type.
  const [open, setOpen] = useState<Record<string, boolean>>({})
  // Until the admin toggles a section, (re)loading applies the defaults
  // (configured sections open); after that their choice survives reloads.
  const userToggled = useRef(false)
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const [saved, setSaved] = useState(false)

  const secretState = (key: string): SecretFieldState => secrets[key] ?? { typed: '', clear: false }
  const setSecret = (key: string, patch: Partial<SecretFieldState>) =>
    setSecrets((current) => ({ ...current, [key]: { ...(current[key] ?? { typed: '', clear: false }), ...patch } }))

  const fieldKey = (type: string, name: string) => `${type}.${name}`

  /** A channel is configured when the kernel registry says so; when the
   * registry is unreachable, the masked settings view is the fallback —
   * every schema field effectively set (stored or env). */
  const channelConfigured = (
    group: ChannelSettingsGroupSchema,
    spec: ChannelTypeSpec | undefined,
    data: ChannelSettings | null,
  ): boolean => {
    if (spec) return spec.configured
    const fields = data?.channels[group.type] ?? {}
    return group.fields.every((field) => {
      const view: ChannelSettingsField | undefined = fields[field.name]
      if (!view) return false
      return field.secret ? view.set === true : view.source !== 'none'
    })
  }

  const isConfigured = (group: ChannelSettingsGroupSchema): boolean =>
    channelConfigured(group, types.find((s) => s.type === group.type), settings)

  // Default sections: configured ones open (the first one when nothing is
  // configured yet). Computed from freshly loaded data — state updates are
  // async, so reading types/settings here would race one render behind.
  const defaultOpen = (
    schema: ChannelSettingsGroupSchema[],
    registry: ChannelTypeSpec[],
    data: ChannelSettings,
  ): Record<string, boolean> => {
    const configured = schema.filter((group) =>
      channelConfigured(group, registry.find((s) => s.type === group.type), data))
    const openTypes = configured.length > 0 ? configured : schema.slice(0, 1)
    return Object.fromEntries(openTypes.map((group) => [group.type, true]))
  }

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
      if (!userToggled.current) setOpen(defaultOpen(data.schema, registry.types, data))
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
      if (!userToggled.current) setOpen(defaultOpen(data.schema, registry.types, data))
      setSaved(true)
    } catch (e) {
      setError(message(e))
    } finally {
      setSaving(false)
    }
  }

  const toggleSection = (type: string, next: boolean) => {
    userToggled.current = true
    setOpen((current) => ({ ...current, [type]: next }))
  }

  // Configured channels first, schema order preserved within each group.
  const groups = useMemo(() => {
    const all = settings?.schema ?? []
    return [...all].sort((a, b) => Number(isConfigured(b)) - Number(isConfigured(a)))
  }, [settings, types])

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

      {error && <InlineNotification kind="error" title={t('common.error') ?? 'Error'} subtitle={error} lowContrast />}
      {saved && <InlineNotification kind="success" title={t('channels_admin.saved') ?? 'Saved'} subtitle={t('channels_admin.saved_body') ?? 'Transport settings applied.'} lowContrast />}

      <div style={{ maxWidth: '40rem' }}>
        <Accordion size="lg">
          {groups.map((group) => {
            const configured = isConfigured(group)
            const hint = types.find((s) => s.type === group.type)?.not_configured_hint
            return (
              <AccordionItem
                key={group.type}
                title={
                  <span style={{ display: 'inline-flex', alignItems: 'center', gap: '0.5rem' }}>
                    {group.label}
                    <Tag size="sm" type={configured ? 'green' : 'gray'} title={hint}>
                      {configured
                        ? (t('channels_admin.ready') ?? 'ready')
                        : (t('channels_admin.not_set') ?? 'not configured')}
                    </Tag>
                  </span>
                }
                open={open[group.type] === true}
                onToggle={(next: boolean) => toggleSection(group.type, next)}
              >
                {group.fields.map((field) =>
                  field.secret
                    ? secretRow(group, field.name, field.label)
                    : plainRow(group, field.name, field.label, field.name === 'homeserver' ? 'https://matrix.org' : ''),
                )}
              </AccordionItem>
            )
          })}
        </Accordion>

        <div style={{ marginTop: '1.5rem' }}>
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

        <div className="form-actions" style={{ marginTop: '1rem' }}>
          <Button size="sm" renderIcon={Save} disabled={saving} onClick={() => void save()}>
            {saving ? (t('common.saving') ?? 'Saving…') : (t('action.save') ?? 'Save')}
          </Button>
          <Button size="sm" kind="secondary" renderIcon={Renew} disabled={saving} onClick={() => void load()}>
            {t('action.refresh') ?? 'Refresh')}
          </Button>
        </div>
      </div>
    </div>
  )
}
