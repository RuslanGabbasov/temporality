import { useEffect, useState, useCallback } from 'react'
import {
  Button,
  TextInput,
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
import { workspaceApi, type Provider } from './workspaceApi'
import { useT } from './i18n'
import ListFilter, { matchesFilter } from './ListFilter'
import AppModal from './Modal'
import { useOrgUnits, OrgUnitSelect, OrgBadge } from './orgUnits'

function message(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }

export default function Providers() {
  const t = useT()
  const org = useOrgUnits()
  const [providers, setProviders] = useState<Provider[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [showForm, setShowForm] = useState(false)
  const [editing, setEditing] = useState<Provider | null>(null)
  const [form, setForm] = useState<Partial<Provider>>({})
  const [filter, setFilter] = useState('')

  const visibleProviders = providers.filter((p) => matchesFilter(filter, p.name, p.base_url, p.id, ...(p.models ?? [])))

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const data = await workspaceApi.listProviders()
      setProviders(data.providers ?? [])
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }, [])

  useEffect(() => { void load() }, [load])

  const startCreate = () => {
    setEditing(null)
    setForm({ name: '', base_url: '', api_key_ref: '', models: [] })
    setShowForm(true)
  }

  const startEdit = (p: Provider) => {
    setEditing(p)
    setForm({ ...p })
    setShowForm(true)
  }

  const save = async () => {
    if (!form.name?.trim() || !form.base_url?.trim()) return
    setLoading(true); setError('')
    try {
      if (editing) {
        await workspaceApi.updateProvider(editing.id, form)
        // PUT payloads ignore org bindings: re-scoping goes through the admin
        // binding endpoint (docs/org-structure.md §39).
        if (org.isAdmin && (form.org_unit_id ?? '') !== (editing.org_unit_id ?? '')) {
          await workspaceApi.setResourceBinding('provider', editing.id, form.org_unit_id ?? '')
        }
      } else {
        await workspaceApi.createProvider(form as Provider & { name: string; base_url: string })
      }
      setShowForm(false); setEditing(null)
      void load()
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }

  const remove = async (p: Provider) => {
    if (!confirm(t('providers.delete_confirm', { name: p.name }) ?? `Delete provider "${p.name}"?`)) return
    setLoading(true); setError('')
    try {
      await workspaceApi.deleteProvider(p.id)
      void load()
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }

  return (
    <div style={{ padding: '1rem' }}>
      {error && <InlineNotification kind="error" title={t('action.error') ?? 'Error'} subtitle={error} onClose={() => setError('')} lowContrast style={{ marginBottom: '1rem' }} />}

      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '0.5rem' }}>
        <Heading>{t('providers.title') ?? 'Model Providers'}</Heading>
        <Button renderIcon={Add} onClick={startCreate}>{t('providers.new_provider') ?? 'New Provider'}</Button>
      </div>
      <div style={{ position: 'sticky', top: '3rem', background: 'var(--tm-bg)', zIndex: 1, paddingBottom: '0.5rem', marginBottom: '0.5rem', maxWidth: '420px' }}>
        <ListFilter value={filter} onChange={setFilter} placeholder={t('common.filter_providers') ?? 'Filter providers…'} />
      </div>

      <Grid>
        {visibleProviders.map((p) => (
          <Column key={p.id} sm={4} md={4} lg={4}>
            <Tile style={{ marginBottom: '0.75rem' }}>
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start' }}>
                <div>
                  <div style={{ display: 'flex', alignItems: 'center', gap: '0.3rem', flexWrap: 'wrap' }}>
                    <strong>{p.name}</strong>
                    <OrgBadge orgUnitID={p.org_unit_id} org={org} />
                  </div>
                  <p style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem', marginTop: '0.25rem', wordBreak: 'break-all' }}>{p.base_url}</p>
                  {p.api_key_ref && <Tag type="gray" size="sm" style={{ marginTop: '0.25rem' }}>key: {'••••••••'}</Tag>}
                  {p.models?.length ? (
                    <div style={{ display: 'flex', gap: '0.25rem', flexWrap: 'wrap', marginTop: '0.5rem' }}>
                      {p.models.map((m) => <Tag key={m} type="blue" size="sm">{m}</Tag>)}
                    </div>
                  ) : <p style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem', marginTop: '0.5rem' }}>{t('providers.no_models') ?? 'No models listed'}</p>}
                </div>
                <Stack orientation="horizontal" gap={1}>
                  <Button size="sm" kind="ghost" hasIconOnly renderIcon={Edit} iconDescription="Edit" onClick={() => startEdit(p)} />
                  <Button size="sm" kind="danger--ghost" hasIconOnly renderIcon={TrashCan} iconDescription="Delete" onClick={() => void remove(p)} />
                </Stack>
              </div>
            </Tile>
          </Column>
        ))}
      </Grid>

      {showForm && (
        <AppModal onClose={() => { setShowForm(false); setEditing(null) }}>
            <Heading>{editing ? (t('providers.edit_provider') ?? 'Edit Provider') : (t('providers.new_provider') ?? 'New Provider')}</Heading>
            <Stack gap={3}>
              <TextInput id="prov-name" labelText={t('providers.name_label') ?? 'Name'} value={form.name ?? ''} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, name: e.target.value })} placeholder="z.ai" autoFocus />
              <TextInput id="prov-url" labelText={t('providers.url_label') ?? 'Base URL'} value={form.base_url ?? ''} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, base_url: e.target.value })} placeholder="https://api.z-ai.com/v1" />
              <TextInput id="prov-key" labelText={t('providers.key_label') ?? 'API Key / Env Var'} value={form.api_key_ref ?? ''} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, api_key_ref: e.target.value })} placeholder="Z_AI_API_KEY or actual key" helperText={t('providers.key_helper') ?? 'Env var name (e.g. Z_AI_API_KEY) or the actual key'} />
              <TextInput id="prov-models" labelText={t('providers.models_label') ?? 'Models (comma-separated)'} value={form.models?.join(', ') ?? ''} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, models: e.target.value.split(',').map((m) => m.trim()).filter(Boolean) })} placeholder="z-ai-turbo, z-ai-pro" />
              {org.units.length > 0 && (
                <>
                  <OrgUnitSelect
                    id="prov-org"
                    value={form.org_unit_id ?? ''}
                    onChange={(orgUnitID) => setForm({ ...form, org_unit_id: orgUnitID })}
                    org={org}
                    disabled={!!editing && !org.isAdmin}
                  />
                  {!!editing && !org.isAdmin && (
                    <p style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)', margin: 0 }}>{t('org.binding_admin_only') ?? 'Only an administrator can change the availability of an existing resource.'}</p>
                  )}
                </>
              )}
              <div className="form-actions">
                <Button kind="secondary" onClick={() => { setShowForm(false); setEditing(null) }}>{t('action.cancel') ?? 'Cancel'}</Button>
                <Button onClick={() => void save()}>{editing ? (t('action.save') ?? 'Save') : (t('action.create') ?? 'Create')}</Button>
              </div>
            </Stack>
        </AppModal>
      )}

      {loading && <Loading withOverlay={false} />}
    </div>
  )
}