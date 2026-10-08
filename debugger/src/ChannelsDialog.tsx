import { useEffect, useState } from 'react'
import {
  Button,
  TextInput,
  Select,
  SelectItem,
  Toggle,
  InlineNotification,
  Heading,
  Tag,
} from '@carbon/react'
import { Add, TrashCan } from '@carbon/icons-react'
import { workspaceApi, type ChannelTypeSpec, type UserChannel } from './workspaceApi'
import { useT } from './i18n'

function message(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }

/** Offline fallback when the registry endpoint is unreachable: the built-in
 * transports. Anything the kernel adds beyond this list only shows up through
 * the API — that is the extensibility contract. */
const DEFAULT_TYPES: ChannelTypeSpec[] = [
  { type: 'matrix', label: 'Matrix', address_hint: 'Matrix room ID, e.g. !room:matrix.org', configured: true },
  { type: 'telegram', label: 'Telegram', address_hint: 'Telegram chat ID, e.g. 123456789', configured: true },
  { type: 'slack', label: 'Slack', address_hint: 'Slack incoming webhook URL', configured: true },
  { type: 'webhook', label: 'Webhook', address_hint: 'HTTPS URL accepting a JSON payload', configured: true },
]

/** Address field help per type: localized first, registry hint as fallback. */
function addressHelper(spec: ChannelTypeSpec | undefined, t: (k: string) => string): string | undefined {
  if (!spec) return undefined
  const localized = t(`channels.address_${spec.type}`)
  return localized || spec.address_hint
}

function addressPlaceholder(spec: ChannelTypeSpec | undefined): string {
  switch (spec?.type) {
    case 'matrix': return '!room:matrix.org'
    case 'telegram': return '123456789'
    case 'slack': return 'https://hooks.slack.com/services/…'
    case 'webhook': return 'https://example.com/temporality'
    default: return ''
  }
}

/**
 * Self-service communication channels (docs/triggers-and-escalations.md §6):
 * the user owns their delivery transports and the preferred one. The web
 * inbox is always available; the other transports come from the kernel's
 * channel registry, so new channel types appear here without UI changes.
 */
export default function ChannelsDialog({ userId, userName, onClose }: { userId: string; userName?: string; onClose: () => void }) {
  const t = useT()
  const [channels, setChannels] = useState<UserChannel[]>([])
  const [specs, setSpecs] = useState<ChannelTypeSpec[]>(DEFAULT_TYPES)
  const [preferred, setPreferred] = useState('web')
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => {
    let cancelled = false
    void (async () => {
      try {
        const [user, registry] = await Promise.all([
          workspaceApi.getUser(userId),
          workspaceApi.channelTypes().catch(() => ({ types: DEFAULT_TYPES })),
        ])
        if (cancelled) return
        setChannels((user.channels ?? []).map((c: UserChannel) => ({ ...c })))
        setPreferred(user.preferred_channel || 'web')
        if (registry.types.length > 0) setSpecs(registry.types)
      } catch (e) {
        if (!cancelled) setError(message(e))
      } finally {
        if (!cancelled) setLoading(false)
      }
    })()
    return () => { cancelled = true }
  }, [userId])

  const specOf = (type: string) => specs.find((s) => s.type === type)

  const updateChannel = (index: number, patch: Partial<UserChannel>) => {
    setChannels((current) => current.map((c, i) => (i === index ? { ...c, ...patch } : c)))
  }

  const addChannel = () => {
    const used = new Set(channels.map((c) => c.type))
    const free = specs.find((s) => !used.has(s.type))
    if (!free) return
    setChannels((current) => [...current, { type: free.type, address: '', enabled: true }])
  }

  const removeChannel = (index: number) => {
    setChannels((current) => {
      const next = current.filter((_, i) => i !== index)
      const removed = current[index]
      if (removed && preferred === removed.type) setPreferred('web')
      return next
    })
  }

  const enabledTypes = channels.filter((c) => c.enabled && c.address.trim()).map((c) => c.type)

  const save = async () => {
    setError('')
    const normalized = channels.map((c) => ({ ...c, address: c.address.trim() }))
    if (normalized.some((c) => !c.address)) {
      setError(t('channels.error_address') ?? 'Every channel needs an address.')
      return
    }
    if (preferred !== 'web' && !enabledTypes.includes(preferred)) {
      setError(t('channels.error_preferred') ?? 'The preferred channel must be web or an enabled channel with an address.')
      return
    }
    setSaving(true)
    try {
      await workspaceApi.updateUserChannels(userId, { channels: normalized, preferred_channel: preferred === 'web' ? '' : preferred })
      onClose()
    } catch (e) {
      setError(message(e))
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="modal-overlay" onClick={(e) => { if (e.target === e.currentTarget) onClose() }}>
      <div className="modal-panel" style={{ width: '480px' }}>
        <Heading style={{ fontSize: '1.1rem', marginBottom: '0.25rem' }}>{(t('channels.title') ?? 'Notification channels') + (userName ? ` — ${userName}` : '')}</Heading>
        <p style={{ fontSize: '0.8rem', color: 'var(--tm-text-3)', margin: '0 0 0.9rem' }}>
          {t('channels.description') ?? 'Where agents should reach you when they ask a question. The in-app inbox always works.'}
        </p>

        {loading ? (
          <p style={{ fontSize: '0.85rem', color: 'var(--tm-text-3)' }}>{t('common.loading') ?? 'Loading…'}</p>
        ) : (
          <>
            {channels.length === 0 && (
              <p style={{ fontSize: '0.8rem', color: 'var(--tm-text-3)', margin: '0 0 0.75rem', padding: '0.75rem', background: 'var(--tm-surface-2)', borderRadius: 'var(--tm-radius-sm)' }}>
                {t('channels.empty') ?? 'No channels yet. Add one to receive agent questions outside the app.'}
              </p>
            )}
            {channels.map((channel, index) => {
              const spec = specOf(channel.type)
              return (
              <div key={index} style={{ display: 'flex', flexDirection: 'column', gap: '0.4rem', padding: '0.75rem', marginBottom: '0.75rem', background: 'var(--tm-surface-2)', borderRadius: 'var(--tm-radius-sm)' }}>
                <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'center' }}>
                  <Select
                    id={`channel-type-${index}`}
                    labelText={t('channels.type') ?? 'Type'}
                    value={channel.type}
                    onChange={(e: React.ChangeEvent<HTMLSelectElement>) => updateChannel(index, { type: e.target.value })}
                    style={{ flex: 1 }}
                    size="sm"
                  >
                    {specs
                      .filter((s) => s.type === channel.type || !channels.some((c, i) => i !== index && c.type === s.type))
                      .map((s) => (
                        <SelectItem key={s.type} value={s.type} text={s.label} />
                      ))}
                  </Select>
                  <Toggle
                    id={`channel-enabled-${index}`}
                    labelText={t('channels.enabled') ?? 'Enabled'}
                    toggled={channel.enabled}
                    onToggle={(checked: boolean) => updateChannel(index, { enabled: checked })}
                    size="sm"
                  />
                  <Button
                    kind="ghost"
                    size="sm"
                    hasIconOnly
                    iconDescription={t('channels.remove') ?? 'Remove'}
                    tooltipPosition="left"
                    renderIcon={TrashCan}
                    onClick={() => removeChannel(index)}
                    style={{ flexShrink: 0, color: 'var(--tm-danger)' }}
                  />
                </div>
                <TextInput
                  id={`channel-address-${index}`}
                  labelText={t('channels.address') ?? 'Address'}
                  placeholder={addressPlaceholder(spec)}
                  helperText={addressHelper(spec, t)}
                  value={channel.address}
                  onChange={(e: React.ChangeEvent<HTMLInputElement>) => updateChannel(index, { address: e.target.value })}
                  size="sm"
                />
                {spec && !spec.configured && (
                  <div style={{ fontSize: '0.72rem', color: 'var(--tm-amber, #e6b85c)', display: 'flex', gap: '0.4rem', alignItems: 'baseline', flexWrap: 'wrap' }}>
                    <span>⚠ {t('channels.not_configured') ?? 'Transport is not configured on the server yet — delivery falls back to the in-app inbox.'}</span>
                    {spec.not_configured_hint && <code style={{ fontSize: '0.68rem' }}>{spec.not_configured_hint}</code>}
                  </div>
                )}
              </div>
              )
            })}
            {channels.length < specs.length && (
              <Button kind="ghost" size="sm" renderIcon={Add} onClick={addChannel} style={{ marginBottom: '0.9rem', padding: 0 }}>
                {t('channels.add') ?? 'Add channel'}
              </Button>
            )}

            <Select
              id="channel-preferred"
              labelText={t('channels.preferred') ?? 'Preferred channel'}
              value={preferred}
              onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setPreferred(e.target.value)}
              helperText={t('channels.preferred_hint') ?? 'Where agent questions are delivered first; other channels are fallback.'}
              size="sm"
            >
              <SelectItem value="web" text={t('channels.web') ?? 'In-app inbox'} />
              {channels.filter((c) => c.enabled).map((c) => (
                <SelectItem key={c.type} value={c.type} text={specOf(c.type)?.label ?? c.type} />
              ))}
            </Select>
          </>
        )}

        {error && <InlineNotification kind="error" title={t('common.error') ?? 'Error'} subtitle={error} hideCloseButton style={{ marginTop: '0.75rem' }} />}

        <div className="form-actions">
          <Button kind="secondary" onClick={onClose}>{t('action.cancel') ?? 'Cancel'}</Button>
          <Button onClick={save} disabled={loading || saving}>{saving ? (t('common.saving') ?? 'Saving…') : (t('action.save') ?? 'Save')}</Button>
        </div>
      </div>
    </div>
  )
}

/** Compact channel summary for user cards. */
export function ChannelBadges({ user }: { user: { channels?: UserChannel[]; preferred_channel?: string } }) {
  const t = useT()
  if (!user.channels?.length) return null
  return (
    <div style={{ display: 'flex', gap: '0.3rem', flexWrap: 'wrap', marginTop: '0.35rem' }}>
      {user.channels.map((channel) => (
        <Tag
          key={channel.type}
          type={user.preferred_channel === channel.type ? 'teal' : 'outline'}
          title={channel.enabled ? undefined : (t('channels.disabled') ?? 'disabled')}
        >
          {channel.type}{user.preferred_channel === channel.type ? ' ★' : ''}
        </Tag>
      ))}
    </div>
  )
}
