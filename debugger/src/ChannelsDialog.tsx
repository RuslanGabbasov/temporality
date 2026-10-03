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
import { workspaceApi, type UserChannel } from './workspaceApi'
import { useT } from './i18n'

function message(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }

const CHANNEL_TYPES: { value: UserChannel['type']; label: string }[] = [
  { value: 'matrix', label: 'Matrix' },
  { value: 'telegram', label: 'Telegram' },
]

/**
 * Self-service communication channels (docs/triggers-and-escalations.md §6):
 * the user owns their delivery transports and the preferred one. The web
 * inbox is always available; Matrix/Telegram carry the question to wherever
 * the user actually is.
 */
export default function ChannelsDialog({ userId, onClose }: { userId: string; onClose: () => void }) {
  const t = useT()
  const [channels, setChannels] = useState<UserChannel[]>([])
  const [preferred, setPreferred] = useState('web')
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => {
    let cancelled = false
    void (async () => {
      try {
        const user = await workspaceApi.getUser(userId)
        if (cancelled) return
        setChannels((user.channels ?? []).map((c: UserChannel) => ({ ...c })))
        setPreferred(user.preferred_channel || 'web')
      } catch (e) {
        if (!cancelled) setError(message(e))
      } finally {
        if (!cancelled) setLoading(false)
      }
    })()
    return () => { cancelled = true }
  }, [userId])

  const updateChannel = (index: number, patch: Partial<UserChannel>) => {
    setChannels((current) => current.map((c, i) => (i === index ? { ...c, ...patch } : c)))
  }

  const addChannel = () => {
    const used = new Set(channels.map((c) => c.type))
    const free = CHANNEL_TYPES.find((type) => !used.has(type.value))
    if (!free) return
    setChannels((current) => [...current, { type: free.value, address: '', enabled: true }])
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
    if (preferred !== 'web' && !enabledTypes.includes(preferred as UserChannel['type'])) {
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
        <Heading style={{ fontSize: '1.1rem', marginBottom: '0.25rem' }}>{t('channels.title') ?? 'Notification channels'}</Heading>
        <p style={{ fontSize: '0.8rem', color: 'var(--tm-text-3)', margin: '0 0 0.9rem' }}>
          {t('channels.description') ?? 'Where agents should reach you when they ask a question. The in-app inbox always works.'}
        </p>

        {loading ? (
          <p style={{ fontSize: '0.85rem', color: 'var(--tm-text-3)' }}>{t('common.loading') ?? 'Loading…'}</p>
        ) : (
          <>
            {channels.length === 0 && (
              <p style={{ fontSize: '0.8rem', color: 'var(--tm-text-3)', margin: '0 0 0.75rem', padding: '0.75rem', background: 'var(--tm-surface-2)', borderRadius: 'var(--tm-radius-sm)' }}>
                {t('channels.empty') ?? 'No channels yet. Add Matrix or Telegram to receive agent questions outside the app.'}
              </p>
            )}
            {channels.map((channel, index) => (
              <div key={index} style={{ display: 'flex', flexDirection: 'column', gap: '0.4rem', padding: '0.75rem', marginBottom: '0.75rem', background: 'var(--tm-surface-2)', borderRadius: 'var(--tm-radius-sm)' }}>
                <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'center' }}>
                  <Select
                    id={`channel-type-${index}`}
                    labelText={t('channels.type') ?? 'Type'}
                    value={channel.type}
                    onChange={(e: React.ChangeEvent<HTMLSelectElement>) => updateChannel(index, { type: e.target.value as UserChannel['type'] })}
                    style={{ flex: 1 }}
                    size="sm"
                  >
                    {CHANNEL_TYPES.filter((type) => type.value === channel.type || !channels.some((c, i) => i !== index && c.type === type.value)).map((type) => (
                      <SelectItem key={type.value} value={type.value} text={type.label} />
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
                  placeholder={channel.type === 'matrix' ? '!room:matrix.org' : '123456789'}
                  helperText={channel.type === 'matrix'
                    ? (t('channels.address_matrix') ?? 'Matrix room ID the kernel bot has joined')
                    : (t('channels.address_telegram') ?? 'Telegram chat ID the bot can message')}
                  value={channel.address}
                  onChange={(e: React.ChangeEvent<HTMLInputElement>) => updateChannel(index, { address: e.target.value })}
                  size="sm"
                />
              </div>
            ))}
            {channels.length < CHANNEL_TYPES.length && (
              <Button kind="ghost" size="sm" renderIcon={Add} onClick={addChannel} style={{ marginBottom: '0.9rem', padding: 0 }}>
                {t('channels.add') ?? 'Add channel'}
              </Button>
            )}

            <Select
              id="channel-preferred"
              labelText={t('channels.preferred') ?? 'Preferred channel'}
              value={preferred}
              onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setPreferred(e.target.value)}
              helperText={t('channels.preferred_hint') ?? 'Where ask_human questions are delivered first; other channels are fallback.'}
              size="sm"
            >
              <SelectItem value="web" text={t('channels.web') ?? 'In-app inbox'} />
              {channels.filter((c) => c.enabled).map((c) => (
                <SelectItem key={c.type} value={c.type} text={c.type === 'matrix' ? 'Matrix' : 'Telegram'} />
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
