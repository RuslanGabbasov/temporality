import { useEffect, useState, useCallback } from 'react'
import {
  Button,
  TextInput,
  TextArea,
  InlineNotification,
  Tag,
  Tile,
  Grid,
  Column,
  Stack,
  Heading,
  Select,
  SelectItem,
  Toggle,
} from '@carbon/react'
import PageLoading from './PageLoading'
import { Add, Edit, TrashCan } from '@carbon/icons-react'
import { workspaceApi, type Secret } from './workspaceApi'
import { useT } from './i18n'
import ListFilter, { matchesFilter } from './ListFilter'
import AppModal from './Modal'
import { useConfirm } from './ConfirmDialog'
import { useOrgUnits, OrgUnitSelect, OrgBadge } from './orgUnits'

function message(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }

type SecretForm = Partial<Secret> & { value?: string }

export default function Secrets() {
  const t = useT()
  const { confirmDialog, dialog: confirmDialogNode } = useConfirm()
  const org = useOrgUnits()
  const [secrets, setSecrets] = useState<Secret[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [showForm, setShowForm] = useState(false)
  const [editing, setEditing] = useState<Secret | null>(null)
  const [form, setForm] = useState<SecretForm>({})
  const [filter, setFilter] = useState('')

  const visibleSecrets = secrets.filter((s) => matchesFilter(filter, s.name, s.id, s.description))

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const data = await workspaceApi.listSecrets()
      setSecrets(data.secrets ?? [])
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }, [])

  useEffect(() => { void load() }, [load])

  // The uniform switch (the trigger pattern): a disabled secret stops
  // resolving. Optimistic flip, rollback on failure.
  const toggleSecretEnabled = async (s: Secret) => {
    const next = s.enabled === false
    setSecrets((prev) => prev.map((x) => (x.id === s.id ? { ...x, enabled: next } : x)))
    try {
      const updated = await workspaceApi.setSecretEnabled(s.id, next)
      setSecrets((prev) => prev.map((x) => (x.id === s.id ? updated : x)))
    } catch (f) {
      setSecrets((prev) => prev.map((x) => (x.id === s.id ? { ...x, enabled: s.enabled } : x)))
      setError(message(f))
    }
  }

  const startCreate = () => {
    setEditing(null)
    setForm({ name: '', provider: 'builtin', description: '', value: '' })
    setShowForm(true)
  }

  // The edit dialog's copy of the uniform switch: applied through the
  // /enabled endpoint after the metadata save (the PUT never carries it).
  const [formEnabled, setFormEnabled] = useState(true)

  const startEdit = (s: Secret) => {
    setEditing(s)
    setFormEnabled(s.enabled !== false)
    setForm({ ...s, value: '' })
    setShowForm(true)
  }

  const save = async () => {
    if (!form.name?.trim()) return
    setLoading(true); setError('')
    try {
      if (editing) {
        await workspaceApi.updateSecret(editing.id, {
          name: form.name, provider: form.provider, description: form.description,
          ...(form.value ? { value: form.value } : {}),
        })
        // PUT payloads ignore org bindings: re-scoping goes through the admin
        // binding endpoint (docs/org-structure.md §39).
        if (org.isAdmin && (form.org_unit_id ?? '') !== (editing.org_unit_id ?? '')) {
          await workspaceApi.setResourceBinding('secret', editing.id, form.org_unit_id ?? '')
        }
        if (formEnabled !== (editing.enabled !== false)) {
          await workspaceApi.setSecretEnabled(editing.id, formEnabled)
        }
      } else {
        const created = await workspaceApi.createSecret({
          name: form.name, provider: form.provider ?? 'builtin',
          description: form.description, org_unit_id: form.org_unit_id,
          ...(form.provider !== 'env' && form.value ? { value: form.value } : {}),
        })
        if (!formEnabled) {
          await workspaceApi.setSecretEnabled(created.id, false)
        }
      }
      setShowForm(false); setEditing(null)
      void load()
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }

  const remove = async (s: Secret) => {
    if (!(await confirmDialog({ title: t('secrets.delete_confirm', { name: s.name }) ?? `Delete secret "${s.name}"?`, body: t('action.delete_warning') ?? 'This cannot be undone.', danger: true, confirmLabel: t('action.delete') ?? 'Delete' }))) return
    setLoading(true); setError('')
    try {
      await workspaceApi.deleteSecret(s.id)
      void load()
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }

  return (
    <div style={{ padding: '1rem' }}>
      {confirmDialogNode}
      {error && <InlineNotification kind="error" title={t('action.error') ?? 'Error'} subtitle={error} onClose={() => setError('')} lowContrast style={{ marginBottom: '1rem' }} />}

      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '0.5rem' }}>
        <Heading>{t('secrets.title') ?? 'Secrets'}</Heading>
        <Button renderIcon={Add} onClick={startCreate}>{t('secrets.new_secret') ?? 'New Secret'}</Button>
      </div>
      <div style={{ position: 'sticky', top: '3rem', background: 'var(--tm-bg)', zIndex: 1, paddingBottom: '0.5rem', marginBottom: '0.5rem', maxWidth: '420px' }}>
        <ListFilter value={filter} onChange={setFilter} placeholder={t('secrets.filter_placeholder') ?? 'Filter secrets…'} />
      </div>

      {secrets.length === 0 && !loading && (
        <Tile style={{ textAlign: 'center', padding: '3rem', color: 'var(--tm-text-3)' }}>
          <p>{t('secrets.no_secrets') ?? 'No secrets yet.'}</p>
          <p style={{ fontSize: '0.8rem', marginTop: '0.5rem' }}>
            {t('secrets.no_secrets_hint') ?? 'Store access credentials once; agents reference them by name and receive the values only inside the execution sandbox.'}
          </p>
        </Tile>
      )}

      <Grid>
        {visibleSecrets.map((s) => (
          <Column key={s.id} sm={4} md={4} lg={4}>
            <Tile style={{ marginBottom: '0.75rem', opacity: s.enabled === false ? 0.5 : 1, display: 'flex', flexDirection: 'column' }}>
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', flex: 1 }}>
                <div>
                  <div style={{ display: 'flex', alignItems: 'center', gap: '0.3rem', flexWrap: 'wrap' }}>
                    <strong>{s.name}</strong>
                    <OrgBadge orgUnitID={s.org_unit_id} org={org} />
                  </div>
                  {s.description && (
                    <p style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem', marginTop: '0.25rem', wordBreak: 'break-word' }}>{s.description}</p>
                  )}
                  <div style={{ display: 'flex', gap: '0.25rem', flexWrap: 'wrap', marginTop: '0.5rem', alignItems: 'center' }}>
                    <Tag type={s.provider === 'builtin' ? 'blue' : 'gray'} size="sm">{s.provider}</Tag>
                    {s.provider === 'builtin' && (
                      s.has_value
                        ? <Tag type="green" size="sm">{t('secrets.value_set') ?? 'value set'}</Tag>
                        : <Tag type="red" size="sm">{t('secrets.value_missing') ?? 'no value'}</Tag>
                    )}
                  </div>
                  <p style={{ color: 'var(--tm-text-3)', fontSize: '0.7rem', marginTop: '0.5rem' }}>
                    {t('secrets.usage_hint') ?? 'Use as'} <code>secret://{s.name}</code> {t('secrets.usage_hint_mcp') ?? 'in MCP env/headers, or grant to an agent for SECRET_' + s.name.toUpperCase().replace(/[-.]/g, '_')}
                  </p>
                </div>
                <Stack orientation="horizontal" gap={1}>
                  <Button size="sm" kind="ghost" hasIconOnly renderIcon={Edit} iconDescription="Edit" onClick={() => startEdit(s)} />
                  <Button size="sm" kind="danger--ghost" hasIconOnly renderIcon={TrashCan} iconDescription="Delete" onClick={() => void remove(s)} />
                </Stack>
              </div>
              {/* The uniform switch, bottom-right of the mini-card: a disabled
                  secret stops resolving — runs degrade without it. */}
              <div style={{ display: 'flex', justifyContent: 'flex-end', marginTop: '0.5rem', paddingTop: '0.4rem', borderTop: '1px solid var(--tm-border)' }} title={t('secrets.enabled_hint') ?? 'Disabled secrets stop resolving'}>
                <Toggle id={`secret-enabled-${s.id}`} labelText="" hideLabel aria-label={t('secrets.enabled_hint') ?? 'Toggle secret'} toggled={s.enabled !== false} onToggle={() => void toggleSecretEnabled(s)} size="sm" />
              </div>
            </Tile>
          </Column>
        ))}
      </Grid>

      {showForm && (
        <AppModal onClose={() => { setShowForm(false); setEditing(null) }} panelStyle={{ width: '560px' }}>
          <Heading>{editing ? (t('secrets.edit_secret') ?? 'Edit Secret') : (t('secrets.new_secret') ?? 'New Secret')}</Heading>
          <Stack gap={3}>
            <TextInput
              id="secret-name"
              labelText={t('secrets.name_label') ?? 'Name'}
              value={form.name ?? ''}
              onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, name: e.target.value })}
              placeholder="github-token"
              helperText={t('secrets.name_helper') ?? 'Referenced as secret://name in MCP configs; injected as SECRET_<NAME> for agents'}
              autoFocus
            />
            <Select
              id="secret-provider"
              labelText={t('secrets.provider_label') ?? 'Provider'}
              value={form.provider ?? 'builtin'}
              disabled={!!editing}
              onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setForm({ ...form, provider: e.target.value as Secret['provider'] })}
            >
              <SelectItem value="builtin" text={t('secrets.provider_builtin') ?? 'builtin — encrypted in the workspace database'} />
              <SelectItem value="env" text={t('secrets.provider_env') ?? 'env — SECRET_<NAME> variable of the kernel process'} />
            </Select>
            <TextInput
              id="secret-description"
              labelText={t('secrets.description_label') ?? 'Description'}
              value={form.description ?? ''}
              onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, description: e.target.value })}
              placeholder={t('secrets.description_placeholder') ?? 'What this credential is for'}
            />
            {(form.provider ?? 'builtin') === 'builtin' && (
              <TextArea
                id="secret-value"
                labelText={editing ? (t('secrets.value_rotate_label') ?? 'New value (leave empty to keep)') : (t('secrets.value_label') ?? 'Value')}
                value={form.value ?? ''}
                onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => setForm({ ...form, value: e.target.value })}
                rows={2}
                placeholder="ghp_…"
                helperText={t('secrets.value_helper') ?? 'Encrypted with AES-256-GCM before storage; never displayed again'}
              />
            )}
            {org.units.length > 0 && (
              <OrgUnitSelect
                id="secret-org"
                label={t('secrets.org_label') ?? 'Availability'}
                org={org}
                value={form.org_unit_id ?? ''}
                onChange={(v: string) => setForm({ ...form, org_unit_id: v })}
              />
            )}
            <div className="form-actions">
              {/* The uniform switch at the bottom-left of the button block. */}
              <div style={{ marginRight: 'auto', display: 'flex', alignItems: 'center' }} title={t('secrets.enabled_hint') ?? 'Disabled secrets stop resolving'}>
                <Toggle id="secret-form-enabled" labelText="" hideLabel aria-label={t('secrets.enabled_hint') ?? 'Enabled'} labelA={t('common.off') ?? 'Off'} labelB={t('common.on') ?? 'On'} toggled={formEnabled} onToggle={(v: boolean) => setFormEnabled(v)} size="sm" />
              </div>
              <Button onClick={() => void save()} disabled={!form.name?.trim()}>{t('action.save') ?? 'Save'}</Button>
              <Button kind="secondary" onClick={() => { setShowForm(false); setEditing(null) }}>{t('action.cancel') ?? 'Cancel'}</Button>
            </div>
          </Stack>
        </AppModal>
      )}

      {loading && <PageLoading />}
    </div>
  )
}
