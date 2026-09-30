import { useCallback, useEffect, useState } from 'react'
import {
  Button,
  TextInput,
  TextArea,
  Select,
  SelectItem,
  Toggle,
  InlineNotification,
  Loading,
  Tag,
  Tile,
  Grid,
  Column,
  Stack,
  Heading,
} from '@carbon/react'
import { Add, Edit, TrashCan } from '@carbon/icons-react'
import { workspaceApi, type Trigger, type Agent } from './workspaceApi'
import { useT } from './i18n'

function message(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }

const TYPE_INFO: Record<string, { icon: string; label: string; color: 'blue' | 'cyan' | 'green' }> = {
  schedule: { icon: '⏰', label: 'triggers.schedule', color: 'blue' },
  webhook: { icon: '🔗', label: 'triggers.webhook', color: 'cyan' },
  event: { icon: '📡', label: 'triggers.event', color: 'green' },
}

export default function Triggers({ project }: { project: string }) {
  const t = useT()
  const [triggers, setTriggers] = useState<Trigger[]>([])
  const [agents, setAgents] = useState<Agent[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [showForm, setShowForm] = useState(false)
  const [editing, setEditing] = useState<Trigger | null>(null)
  const [form, setForm] = useState<Partial<Trigger> & { config: any }>({ type: 'schedule', name: '', enabled: true, config: {} })

  const load = useCallback(async () => {
    if (!project.trim()) return
    setLoading(true)
    try {
      const [tData, aData] = await Promise.all([
        workspaceApi.listTriggers(project),
        workspaceApi.listAllAgents(),
      ])
      setTriggers(tData.triggers ?? [])
      setAgents(aData.agents ?? [])
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }, [project])

  useEffect(() => { void load() }, [load])

  const startCreate = (type: 'schedule' | 'webhook' | 'event') => {
    setEditing(null)
    const defaultConfig = type === 'schedule'
      ? { cron: '0 9 * * 1-5', prompt: '', timezone: 'UTC' }
      : type === 'webhook'
      ? { path: '', secret: '', prompt_template: '' }
      : { event_type: 'tool.failed', filter: {}, prompt: '' }
    setForm({ type, name: '', enabled: true, config: defaultConfig, project_id: project })
    setShowForm(true)
  }

  const startEdit = (trigger: Trigger) => {
    setEditing(trigger)
    setForm({ ...trigger, config: typeof trigger.config === 'string' ? JSON.parse(trigger.config) : trigger.config })
    setShowForm(true)
  }

  const save = async () => {
    if (!form.name?.trim()) return
    setLoading(true); setError('')
    try {
      const payload = { ...form, project_id: project, config: JSON.stringify(form.config) }
      if (editing) {
        await workspaceApi.updateTrigger(editing.id, payload)
      } else {
        await workspaceApi.createTrigger(payload)
      }
      setShowForm(false); setEditing(null)
      void load()
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }

  const remove = async (trigger: Trigger) => {
    if (!confirm(t('triggers.delete_confirm', { name: trigger.name }) ?? `Delete trigger "${trigger.name}"?`)) return
    setLoading(true); setError('')
    try {
      await workspaceApi.deleteTrigger(trigger.id)
      void load()
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }

  const toggleEnabled = async (trigger: Trigger) => {
    try {
      await workspaceApi.updateTrigger(trigger.id, { enabled: !trigger.enabled })
      void load()
    } catch (f) { setError(message(f)) }
  }

  const webhookUrl = (t: Trigger) => {
    const cfg = typeof t.config === 'string' ? JSON.parse(t.config) : t.config
    return `${window.location.origin}/kernel-api/v1/workspace/webhook/${t.project_id}/${cfg.path || ''}`
  }

  return (
    <div style={{ padding: '1rem' }}>
      {error && <InlineNotification kind="error" title={t('action.error') ?? 'Error'} subtitle={error} onClose={() => setError('')} lowContrast style={{ marginBottom: '1rem' }} />}

      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1rem' }}>
        <Heading>{t('triggers.title') ?? 'Triggers'}</Heading>
        <Button renderIcon={Add} onClick={() => startCreate('schedule')}>{t('triggers.new_trigger') ?? 'New Trigger'}</Button>
      </div>

      {triggers.length === 0 && !loading && (
        <Tile style={{ textAlign: 'center', padding: '3rem', color: 'var(--tm-text-3)' }}>
          <p>{t('triggers.no_triggers') ?? 'No triggers configured.'}</p>
          <p style={{ fontSize: '0.8rem', marginTop: '0.5rem' }}>{t('triggers.no_triggers_hint') ?? 'Triggers launch agent runs automatically based on schedules, webhooks, or events.'}</p>
        </Tile>
      )}

      <Grid>
        {triggers.map((trigger) => {
          const info = TYPE_INFO[trigger.type] ?? TYPE_INFO.schedule
          const cfg = typeof trigger.config === 'string' ? JSON.parse(trigger.config) : trigger.config
          return (
            <Column key={trigger.id} sm={4} md={4} lg={4}>
              <Tile style={{ marginBottom: '0.75rem', opacity: trigger.enabled ? 1 : 0.5 }}>
                <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start' }}>
                  <div>
                    <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', flexWrap: 'wrap' }}>
                      <span>{info.icon}</span>
                      <strong>{trigger.name}</strong>
                      <Tag type={info.color} size="sm">{t(info.label) ?? info.label}</Tag>
                      <Tag type={trigger.enabled ? 'green' : 'red'} size="sm">{trigger.enabled ? (t('triggers.on') ?? 'on') : (t('triggers.off') ?? 'off')}</Tag>
                    </div>
                    <div style={{ fontSize: '0.75rem', color: 'var(--tm-text-3)', marginTop: '0.35rem' }}>
                      {trigger.type === 'schedule' && cfg.cron && <span>{t('triggers.cron_label') ?? 'cron:'} <code>{cfg.cron}</code></span>}
                      {trigger.type === 'webhook' && cfg.path && <span>POST <code style={{ fontSize: '0.65rem' }}>{webhookUrl(trigger)}</code></span>}
                      {trigger.type === 'event' && cfg.event_type && <span>on <code>{cfg.event_type}</code></span>}
                    </div>
                    {trigger.agent_id && (
                      <div style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)', marginTop: '0.2rem' }}>
                        {t('triggers.agent_label') ?? 'agent:'} {agents.find((a) => a.id === trigger.agent_id)?.name ?? trigger.agent_id}
                      </div>
                    )}
                  </div>
                  <Stack orientation="horizontal" gap={1}>
                    <Button size="sm" kind="ghost" hasIconOnly renderIcon={Edit} iconDescription="Edit" onClick={() => startEdit(trigger)} />
                    <Button size="sm" kind="danger--ghost" hasIconOnly renderIcon={TrashCan} iconDescription="Delete" onClick={() => void remove(trigger)} />
                  </Stack>
                </div>
                <div style={{ marginTop: '0.5rem' }}>
                  <Toggle id={`toggle-${trigger.id}`} labelText="" toggled={trigger.enabled} onToggle={() => void toggleEnabled(trigger)} size="sm" />
                </div>
              </Tile>
            </Column>
          )
        })}
      </Grid>

      {loading && <Loading withOverlay={false} />}

      {/* Create/Edit modal */}
      {showForm && (
        <div className="modal-overlay">
          <div className="modal-panel" style={{ width: '600px' }}>
            <Heading>{editing ? (t('triggers.edit_trigger') ?? 'Edit Trigger') : (t('triggers.new_trigger') ?? 'New Trigger')}</Heading>
            <Select id="trigger-type" labelText={t('triggers.type') ?? 'Type'} value={form.type ?? 'schedule'} onChange={(e: React.ChangeEvent<HTMLSelectElement>) => {
              const type = e.target.value as 'schedule' | 'webhook' | 'event'
              const defaultConfig = type === 'schedule'
                ? { cron: '0 9 * * 1-5', prompt: '', timezone: 'UTC' }
                : type === 'webhook'
                ? { path: '', secret: '', prompt_template: '' }
                : { event_type: 'tool.failed', filter: {}, prompt: '' }
              setForm({ ...form, type, config: defaultConfig })
            }}>
              <SelectItem value="schedule" text={`⏰ ${t('triggers.schedule') ?? 'Schedule'}`} />
              <SelectItem value="webhook" text={`🔗 ${t('triggers.webhook') ?? 'Webhook'}`} />
              <SelectItem value="event" text={`📡 ${t('triggers.event') ?? 'Event'}`} />
            </Select>
            <TextInput id="trigger-name" labelText={t('triggers.name') ?? 'Name'} value={form.name ?? ''} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, name: e.target.value })} placeholder="Daily report" />
            <Select id="trigger-agent" labelText={t('triggers.agent') ?? 'Agent'} value={form.agent_id ?? ''} onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setForm({ ...form, agent_id: e.target.value })}>
              <SelectItem value="" text={t('triggers.default_agent') ?? 'Default agent'} />
              {agents.map((a) => <SelectItem key={a.id} value={a.id} text={a.name} />)}
            </Select>

            {/* Schedule config */}
            {form.type === 'schedule' && (
              <>
                <TextInput id="trigger-cron" labelText={t('triggers.cron_expression') ?? 'Cron expression'} value={form.config?.cron ?? ''} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, config: { ...form.config, cron: e.target.value } })} placeholder="0 9 * * 1-5" helperText={t('triggers.cron_helper') ?? 'minute hour day month weekday — e.g. 0 9 * * 1-5 = weekdays at 9:00'} />
                <TextArea id="trigger-prompt" labelText={t('triggers.prompt') ?? 'Prompt'} value={form.config?.prompt ?? ''} onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => setForm({ ...form, config: { ...form.config, prompt: e.target.value } })} rows={4} placeholder="Review recent changes and generate a daily summary." />
              </>
            )}

            {/* Webhook config */}
            {form.type === 'webhook' && (
              <>
                <TextInput id="trigger-path" labelText={t('triggers.webhook_path') ?? 'Webhook path'} value={form.config?.path ?? ''} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, config: { ...form.config, path: e.target.value } })} placeholder="deploy-notify" helperText={t('triggers.webhook_helper', { url: `${project}/${form.config?.path || '...'}` }) ?? `Endpoint: POST /kernel-api/v1/workspace/webhook/${project}/${form.config?.path || '...'}`} />
                <TextInput id="trigger-secret" labelText={t('triggers.secret_optional') ?? 'Secret (optional)'} value={form.config?.secret ?? ''} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, config: { ...form.config, secret: e.target.value } })} placeholder="hmac-secret" />
                <TextArea id="trigger-prompt-tpl" labelText={t('triggers.prompt_template') ?? 'Prompt template'} value={form.config?.prompt_template ?? ''} onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => setForm({ ...form, config: { ...form.config, prompt_template: e.target.value } })} rows={4} placeholder="Deploy completed: {{body.tag}}. Review changes and verify." helperText={t('triggers.prompt_template_helper') ?? 'Use {{body.field}} to insert values from the JSON request body.'} />
              </>
            )}

            {/* Event config */}
            {form.type === 'event' && (
              <>
                <TextInput id="trigger-event-type" labelText={t('triggers.event_type') ?? 'Event type'} value={form.config?.event_type ?? ''} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, config: { ...form.config, event_type: e.target.value } })} placeholder="tool.failed" />
                <TextArea id="trigger-prompt" labelText={t('triggers.prompt') ?? 'Prompt'} value={form.config?.prompt ?? ''} onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => setForm({ ...form, config: { ...form.config, prompt: e.target.value } })} rows={4} placeholder="A tool failed. Investigate the issue and propose a fix." />
              </>
            )}

            <div className="form-actions">
              <Button kind="secondary" onClick={() => { setShowForm(false); setEditing(null) }}>{t('action.cancel') ?? 'Cancel'}</Button>
              <Button onClick={() => void save()}>{editing ? (t('action.save') ?? 'Save') : (t('action.create') ?? 'Create')}</Button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
