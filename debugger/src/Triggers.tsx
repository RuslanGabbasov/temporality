import { useCallback, useEffect, useState } from 'react'
import {
  Button,
  TextInput,
  TextArea,
  Select,
  SelectItem,
  Toggle,
  InlineNotification,
  Tag,
  Tile,
  Grid,
  Column,
  Stack,
  Heading,
} from '@carbon/react'
import PageLoading from './PageLoading'
import { Add, Copy, Edit, TrashCan } from '@carbon/icons-react'
import { workspaceApi, type Trigger, type Agent, type ExecutionIdentity } from './workspaceApi'
import { copyText } from './clipboard'
import { useT } from './i18n'
import ListFilter, { matchesFilter } from './ListFilter'
import AppModal from './Modal'
import { useOrgUnits, OrgUnitSelect, OrgBadge } from './orgUnits'
import { observationApi, type ObservationEvent } from './observationApi'

function message(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }

const TYPE_INFO: Record<string, { icon: string; label: string; color: 'blue' | 'cyan' | 'green' }> = {
  schedule: { icon: '⏰', label: 'triggers.schedule', color: 'blue' },
  webhook: { icon: '🔗', label: 'triggers.webhook', color: 'cyan' },
  event: { icon: '📡', label: 'triggers.event', color: 'green' },
}

// Delivery outcomes (trigger.accepted/rejected/duplicate journal events):
// what a webhook call actually did — the UI answer to "did it fire?".
const DELIVERY_INFO: Record<string, { label: string; tag: 'green' | 'gray' | 'red' }> = {
  'trigger.accepted': { label: 'triggers.delivery_accepted', tag: 'green' },
  'trigger.duplicate': { label: 'triggers.delivery_duplicate', tag: 'gray' },
  'trigger.rejected': { label: 'triggers.delivery_rejected', tag: 'red' },
}

const deliveryTime = (iso: string) => {
  const d = new Date(iso)
  return d.toDateString() === new Date().toDateString()
    ? d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })
    : d.toLocaleString()
}

export default function Triggers({ project }: { project: string }) {
  const t = useT()
  const org = useOrgUnits()
  const [triggers, setTriggers] = useState<Trigger[]>([])
  const [agents, setAgents] = useState<Agent[]>([])
  const [identities, setIdentities] = useState<ExecutionIdentity[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [showForm, setShowForm] = useState(false)
  const [editing, setEditing] = useState<Trigger | null>(null)
  const [form, setForm] = useState<Partial<Trigger> & { config: any }>({ type: 'schedule', name: '', enabled: true, config: {} })
  const [filter, setFilter] = useState('')
  // Per-trigger webhook delivery token (shown once, hash lives server-side):
  // generatedToken is displayed in a dedicated modal exactly like user tokens.
  const [generatedToken, setGeneratedToken] = useState<string | null>(null)
  const [tokenBusy, setTokenBusy] = useState(false)
  const [tokenError, setTokenError] = useState('')
  // Recent webhook deliveries (trigger.accepted/rejected/duplicate journal
  // events): the answer to "did the webhook actually start anything?".
  const [deliveries, setDeliveries] = useState<ObservationEvent[]>([])

  const visibleTriggers = triggers.filter((tr) => matchesFilter(filter, tr.name, tr.type, tr.id))

  const loadDeliveries = useCallback(async () => {
    if (!project.trim()) return
    // Outcome events only; a failure here degrades silently (empty section)
    // — deliveries are diagnostics, not a blocker for trigger management.
    try {
      const kinds = ['trigger.accepted', 'trigger.rejected', 'trigger.duplicate']
      const pages = await Promise.all(kinds.map((type) => observationApi.events(project, undefined, undefined, undefined, { type, limit: 30 }).catch(() => ({ events: [] as ObservationEvent[] }))))
      const merged = pages.flatMap((p) => p.events ?? [])
      merged.sort((a, b) => (a.occurred_at < b.occurred_at ? 1 : -1))
      setDeliveries(merged.slice(0, 20))
    } catch { setDeliveries([]) }
  }, [project])

  const load = useCallback(async () => {
    if (!project.trim()) return
    setLoading(true)
    try {
      const [tData, aData, idData] = await Promise.all([
        workspaceApi.listTriggers(project),
        workspaceApi.listAllAgents(),
        workspaceApi.listExecutionIdentities().catch(() => ({ identities: [] })),
      ])
      setTriggers(tData.triggers ?? [])
      setAgents(aData.agents ?? [])
      setIdentities(idData.identities ?? [])
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }, [project])

  useEffect(() => { void load() }, [load])
  useEffect(() => {
    void loadDeliveries()
    const timer = setInterval(() => void loadDeliveries(), 15000)
    return () => clearInterval(timer)
  }, [loadDeliveries])

  const startCreate = (type: 'schedule' | 'webhook' | 'event') => {
    setEditing(null)
    const defaultConfig = type === 'schedule'
      ? { cron: '0 9 * * 1-5', prompt: '', timezone: 'UTC' }
      : type === 'webhook'
      ? { path: '', secret: '', prompt_template: '' }
      : { event_type: 'tool.failed', filter: {}, prompt: '' }
    setForm({ type, name: '', enabled: true, config: defaultConfig, project_id: project, org_unit_id: '', execution_identity_id: '' })
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
      // config travels as a JSON object, not a stringified one: the server
      // stores jsonb and every consumer unmarshals it into a struct. (The
      // stringified form used to double-encode and broke matching/firing.)
      const payload = { ...form, project_id: project, config: form.config }
      if (editing) {
        await workspaceApi.updateTrigger(editing.id, payload)
        // PUT payloads ignore org bindings: re-scoping goes through the admin
        // binding endpoint (docs/org-structure.md §39).
        if (org.isAdmin && (form.org_unit_id ?? '') !== (editing.org_unit_id ?? '')) {
          await workspaceApi.setResourceBinding('trigger', editing.id, form.org_unit_id ?? '')
        }
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

  // Full delivery address as the external system sees it (origin included):
  // the form hint must be pasteable into TFS/CI as-is, not a path fragment.
  const webhookUrlFor = (projectId: string, path: string) =>
    `${window.location.origin}/kernel-api/v1/workspace/webhook/${projectId}/${path || ''}`

  const webhookUrl = (t: Trigger) => {
    const cfg = typeof t.config === 'string' ? JSON.parse(t.config) : t.config
    return webhookUrlFor(t.project_id, cfg.path)
  }

  const copyToClipboard = (text: string) => {
    void copyText(text)
  }

  const genToken = async () => {
    if (!editing) return
    setTokenBusy(true); setTokenError('')
    try {
      const res = await workspaceApi.generateWebhookToken(editing.id)
      setGeneratedToken(res.token)
      // Placeholder flag only: the real hash never reaches the form and PUT
      // preserves the stored one server-side.
      setForm((f) => ({ ...f, config: { ...f.config, auth_token_hash: 'set' } }))
    } catch (f) { setTokenError(message(f)) }
    finally { setTokenBusy(false) }
  }

  const revokeToken = async () => {
    if (!editing) return
    setTokenBusy(true); setTokenError('')
    try {
      await workspaceApi.revokeWebhookToken(editing.id)
      const cfg = { ...form.config }
      delete cfg.auth_token_hash
      setForm((f) => ({ ...f, config: cfg }))
      setGeneratedToken(null)
    } catch (f) { setTokenError(message(f)) }
    finally { setTokenBusy(false) }
  }

  return (
    <div style={{ padding: '1rem' }}>
      {error && <InlineNotification kind="error" title={t('action.error') ?? 'Error'} subtitle={error} onClose={() => setError('')} lowContrast style={{ marginBottom: '1rem' }} />}

      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '0.5rem' }}>
        <Heading>{t('triggers.title') ?? 'Triggers'}</Heading>
        <Button renderIcon={Add} onClick={() => startCreate('schedule')}>{t('triggers.new_trigger') ?? 'New Trigger'}</Button>
      </div>
      <div style={{ position: 'sticky', top: '3rem', background: 'var(--tm-bg)', zIndex: 1, paddingBottom: '0.5rem', marginBottom: '0.5rem', maxWidth: '420px' }}>
        <ListFilter value={filter} onChange={setFilter} placeholder={t('common.filter_triggers') ?? 'Filter triggers…'} />
      </div>

      {triggers.length === 0 && !loading && (
        <Tile style={{ textAlign: 'center', padding: '3rem', color: 'var(--tm-text-3)' }}>
          <p>{t('triggers.no_triggers') ?? 'No triggers configured.'}</p>
          <p style={{ fontSize: '0.8rem', marginTop: '0.5rem' }}>{t('triggers.no_triggers_hint') ?? 'Triggers launch agent runs automatically based on schedules, webhooks, or events.'}</p>
        </Tile>
      )}

      <Grid>
        {visibleTriggers.map((trigger) => {
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
                    <div style={{ display: 'flex', alignItems: 'center', gap: '0.25rem', flexWrap: 'wrap', marginTop: '0.3rem' }}>
                      <OrgBadge orgUnitID={trigger.org_unit_id} org={org} />
                      {trigger.execution_identity_id && (
                        <Tag type="purple" size="sm" title={trigger.execution_identity_id}>
                          {t('triggers.identity_label') ?? 'identity:'} {identities.find((i) => i.id === trigger.execution_identity_id)?.name ?? trigger.execution_identity_id}
                        </Tag>
                      )}
                    </div>
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

      {/* Delivery history: journal outcome events for the project's webhook
          triggers, so a fired webhook is never a black box — accepted /
          duplicate / rejected, with the reason and a link to the run. */}
      {(triggers.some((tr) => tr.type === 'webhook') || deliveries.length > 0) && (
        <div style={{ marginTop: '1.5rem' }}>
          <div style={{ fontSize: '0.875rem', fontWeight: 600, marginBottom: '0.25rem' }}>{t('triggers.deliveries_title') ?? 'Recent webhook deliveries'}</div>
          {deliveries.length === 0 ? (
            <p style={{ fontSize: '0.8rem', color: 'var(--tm-text-3)', margin: 0 }}>{t('triggers.delivery_empty') ?? 'No deliveries yet.'}</p>
          ) : (
            deliveries.map((e) => {
              const info = DELIVERY_INFO[e.type] ?? DELIVERY_INFO['trigger.rejected']
              const reason = typeof e.data?.reason === 'string' ? e.data.reason : ''
              // Accepted events carry the run in the context; duplicates — in
              // data.run_id; rejected deliveries have no run to link to.
              const runID = typeof e.data?.run_id === 'string' && e.data.run_id
                ? e.data.run_id
                : e.type === 'trigger.accepted' && typeof e.context?.run === 'string' ? e.context.run : ''
              const name = typeof e.data?.trigger_name === 'string' && e.data.trigger_name ? e.data.trigger_name : '—'
              return (
                <div key={e.event_id} style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', flexWrap: 'wrap', padding: '0.4rem 0', borderBottom: '1px solid var(--tm-border)', fontSize: '0.8rem' }}>
                  <span style={{ color: 'var(--tm-text-3)', fontVariantNumeric: 'tabular-nums' }}>{deliveryTime(e.occurred_at)}</span>
                  <span style={{ fontWeight: 600 }}>{name}</span>
                  <Tag type={info.tag} size="sm">{t(info.label) ?? info.label}</Tag>
                  {reason && <span style={{ color: 'var(--tm-text-3)', flexBasis: '100%', wordBreak: 'break-word' }}>{reason}</span>}
                  {runID && (
                    <a href={`/agents?project=${encodeURIComponent(project)}&run=${encodeURIComponent(runID)}`} style={{ marginLeft: 'auto' }}>{t('triggers.open_run') ?? 'Open run →'}</a>
                  )}
                </div>
              )
            })
          )}
        </div>
      )}

      {loading && <PageLoading />}

      {/* Create/Edit modal */}
      {showForm && (
        <AppModal onClose={() => { setShowForm(false); setEditing(null) }} panelStyle={{ width: '600px' }}>
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
              {agents.filter((a) => !a.project_id || a.project_id === project).map((a) => <SelectItem key={a.id} value={a.id} text={a.name} />)}
            </Select>

            {/* Org visibility (org-structure.md §46): the unit the trigger
                belongs to; re-scoping after creation is admin-only. */}
            <OrgUnitSelect
              id="trigger-org-unit"
              org={org}
              value={form.org_unit_id ?? ''}
              onChange={(orgUnitID) => setForm({ ...form, org_unit_id: orgUnitID })}
              disabled={!!editing && !org.isAdmin}
            />

            {/* Execution identity (org-structure.md §20): the security context
                the fired run acts under. Empty = legacy behavior (no
                identity-scoped re-checks at fire time). */}
            <Select
              id="trigger-identity"
              labelText={t('triggers.execution_identity') ?? 'Execution identity'}
              value={form.execution_identity_id ?? ''}
              onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setForm({ ...form, execution_identity_id: e.target.value })}
              helperText={t('triggers.identity_helper') ?? 'The permissions the fired run acts under: agents, MCP servers, providers and projects it may touch. Without an identity the run is not permission-scoped.'}
            >
              <SelectItem value="" text={t('triggers.no_identity') ?? 'Without identity (not recommended)'} />
              {identities.map((i) => <SelectItem key={i.id} value={i.id} text={i.name} />)}
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
                <TextInput id="trigger-path" labelText={t('triggers.webhook_path') ?? 'Webhook path'} value={form.config?.path ?? ''} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, config: { ...form.config, path: e.target.value } })} placeholder="deploy-notify" helperText={t('triggers.webhook_helper') ?? 'Authorization required: a writer Bearer token, this trigger’s delivery token, Basic (token as password) or ?token= in the URL.'} />
                <div style={{ display: 'flex', alignItems: 'flex-start', gap: '0.5rem', margin: '-0.5rem 0 0.75rem', flexWrap: 'wrap' }}>
                  <span style={{ fontSize: '0.75rem', color: 'var(--tm-text-3)', fontFamily: 'monospace' }}>POST</span>
                  <code style={{ fontSize: '0.7rem', wordBreak: 'break-all', color: 'var(--tm-text-2)', minWidth: '0' }}>{webhookUrlFor(project, form.config?.path || '…')}</code>
                  {!!form.config?.path && (
                    <Button size="sm" kind="ghost" hasIconOnly renderIcon={Copy} iconDescription={t('action.copy') ?? 'Copy'} onClick={() => copyToClipboard(webhookUrlFor(project, form.config?.path ?? ''))} />
                  )}
                </div>
                <TextInput id="trigger-secret" labelText={t('triggers.secret_optional') ?? 'Signing secret (optional)'} value={form.config?.secret ?? ''} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, config: { ...form.config, secret: e.target.value } })} placeholder="hmac-secret" helperText={t('triggers.secret_hint') ?? 'When set, deliveries must be signed: X-Temporality-Signature: sha256-<HMAC-SHA256 of the body>'} />
                <TextArea id="trigger-prompt-tpl" labelText={t('triggers.prompt_template') ?? 'Prompt template'} value={form.config?.prompt_template ?? ''} onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => setForm({ ...form, config: { ...form.config, prompt_template: e.target.value } })} rows={4} placeholder="Deploy completed: {{body.tag}}. Review changes and verify." helperText={t('triggers.prompt_template_helper') ?? 'Use {{body.field}} to insert values from the JSON request body.'} />
                {/* Delivery token exists only for a saved webhook trigger —
                    generation needs the trigger id. The hash itself never
                    leaves the server; the form only tracks set/unset. */}
                {editing && (
                  <div style={{ border: '1px solid var(--tm-border)', borderRadius: '4px', padding: '0.75rem' }}>
                    <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', flexWrap: 'wrap' }}>
                      <span style={{ fontSize: '0.875rem', fontWeight: 600 }}>{t('triggers.token_title') ?? 'Delivery token'}</span>
                      {!!form.config?.auth_token_hash && <Tag type="green" size="sm">{t('triggers.token_active') ?? 'active'}</Tag>}
                    </div>
                    <p style={{ fontSize: '0.75rem', color: 'var(--tm-text-3)', margin: '0.5rem 0' }}>
                      {t('triggers.token_hint') ?? 'A dedicated token for this webhook: put it into the external system (TFS, CI, chat bot) instead of your user token. Send it as Bearer or as the password in Basic auth — it works only on this trigger’s endpoint.'}
                    </p>
                    {tokenError && <InlineNotification kind="error" title={t('action.error') ?? 'Error'} subtitle={tokenError} onClose={() => setTokenError('')} lowContrast style={{ marginBottom: '0.5rem' }} />}
                    <Stack orientation="horizontal" gap={3}>
                      <Button size="sm" kind="secondary" disabled={tokenBusy} onClick={() => void genToken()}>
                        {form.config?.auth_token_hash ? (t('triggers.token_regenerate') ?? 'Regenerate token') : (t('triggers.token_generate') ?? 'Generate token')}
                      </Button>
                      {!!form.config?.auth_token_hash && (
                        <Button size="sm" kind="danger--ghost" disabled={tokenBusy} onClick={() => void revokeToken()}>{t('triggers.token_revoke') ?? 'Revoke'}</Button>
                      )}
                    </Stack>
                  </div>
                )}
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
        </AppModal>
      )}

      {/* One-time delivery token reveal. Rendered AFTER the form modal so the
          equal z-index stacks it on top (later sibling wins) — it used to sit
          underneath the still-open edit form. */}
      {generatedToken && (
        <AppModal onClose={() => setGeneratedToken(null)} panelStyle={{ width: '600px' }}>
          <Heading>{t('triggers.token_modal_title') ?? 'Webhook delivery token'}</Heading>
          <p style={{ color: 'var(--tm-text-3)', marginBottom: '1rem' }}>{t('triggers.token_modal_hint') ?? 'The token is shown exactly once — copy it now. Lost it? Regenerate.'}</p>
          <div style={{ background: 'var(--tm-elevated)', border: '1px solid var(--tm-border)', borderRadius: '4px', padding: '0.75rem', fontFamily: 'monospace', fontSize: '0.875rem', wordBreak: 'break-all', marginBottom: '1rem' }}>
            {generatedToken}
          </div>
          <div className="form-actions">
            <Button kind="secondary" onClick={() => setGeneratedToken(null)}>{t('action.close') ?? 'Close'}</Button>
            <Button onClick={() => { copyToClipboard(generatedToken); setGeneratedToken(null) }}>{t('triggers.token_copy_close') ?? 'Copy & Close'}</Button>
          </div>
        </AppModal>
      )}
    </div>
  )
}
