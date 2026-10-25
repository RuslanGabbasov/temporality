import { useEffect, useState, useCallback } from 'react'
import {
  Button,
  TextInput,
  InlineNotification,
  Tag,
  Tile,
  Grid,
  Column,
  Stack,
  Heading,
} from '@carbon/react'
import PageLoading from './PageLoading'
import { Add, Edit, TrashCan, Renew } from '@carbon/icons-react'
import { workspaceApi, type Provider, type Secret } from './workspaceApi'
import { useT } from './i18n'
import ListFilter, { matchesFilter } from './ListFilter'
import AppModal from './Modal'
import { useConfirm } from './ConfirmDialog'
import { useOrgUnits, OrgUnitSelect, OrgBadge } from './orgUnits'

function message(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }

// How many model tags a provider card shows before collapsing the rest —
// auto-discovered catalogs can be large and must not flood the grid.
const MODELS_PREVIEW = 8

export default function Providers() {
  const t = useT()
  const { confirmDialog, dialog: confirmDialogNode } = useConfirm()
  const org = useOrgUnits()
  const [providers, setProviders] = useState<Provider[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [showForm, setShowForm] = useState(false)
  const [editing, setEditing] = useState<Provider | null>(null)
  const [form, setForm] = useState<Partial<Provider>>({})
  const [filter, setFilter] = useState('')
  // Secrets list for the api_key_ref picker: a provider credential is a
  // secret://name reference first, an env name or literal second.
  const [secrets, setSecrets] = useState<Secret[]>([])
  // Catalog discovery (probe) inside the form, and per-card sync state.
  const [fetching, setFetching] = useState(false)
  const [fetchNote, setFetchNote] = useState('')
  const [syncingId, setSyncingId] = useState('')
  const [expanded, setExpanded] = useState<ReadonlySet<string>>(new Set())

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
    setFetchNote('')
    loadSecretOptions()
    setShowForm(true)
  }

  const startEdit = (p: Provider) => {
    setEditing(p)
    setForm({ ...p })
    setFetchNote('')
    loadSecretOptions()
    setShowForm(true)
  }

  const loadSecretOptions = () => {
    workspaceApi.listSecrets()
      .then((data) => setSecrets(data.secrets ?? []))
      .catch(() => setSecrets([])) // picker is optional; the field works without it
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
    if (!(await confirmDialog({ title: t('providers.delete_confirm', { name: p.name }) ?? `Delete provider "${p.name}"?`, body: t('action.delete_warning') ?? 'This cannot be undone.', danger: true, confirmLabel: t('action.delete') ?? 'Delete' }))) return
    setLoading(true); setError('')
    try {
      await workspaceApi.deleteProvider(p.id)
      void load()
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }

  // Discover previews the catalog straight from the form's base URL/key —
  // works for unsaved providers too, nothing is persisted.
  const discoverModels = async () => {
    if (!form.base_url?.trim()) return
    setFetching(true); setFetchNote('')
    try {
      const data = await workspaceApi.probeProviderModels({ base_url: form.base_url.trim(), api_key_ref: form.api_key_ref ?? '' })
      setForm((f) => ({ ...f, models: data.models ?? [] }))
      setFetchNote(t('providers.fetched_n', { count: String(data.count ?? 0) }) ?? `${data.count ?? 0}`)
    } catch (f) {
      setFetchNote(`${t('providers.fetch_failed') ?? 'Failed'}: ${message(f)}`)
    } finally { setFetching(false) }
  }

  // Sync mirrors the stored provider's catalog now (the background loop
  // already does this on a schedule; this covers "do it immediately").
  const syncProvider = async (p: Provider) => {
    setSyncingId(p.id); setError('')
    try {
      await workspaceApi.refreshProviderModels(p.id)
      await load()
    } catch (f) { setError(message(f)) }
    finally { setSyncingId('') }
  }

  return (
    <div style={{ padding: '1rem' }}>
      {confirmDialogNode}
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
                    {p.labels?.models_source === 'manual'
                      ? <Tag type="gray" size="sm">{t('providers.catalog_manual') ?? 'manual'}</Tag>
                      : (p.models?.length ? <Tag type="green" size="sm">{t('providers.catalog_auto') ?? 'auto'}</Tag> : null)}
                  </div>
                  <p style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem', marginTop: '0.25rem', wordBreak: 'break-all' }}>{p.base_url}</p>
                  {p.api_key_ref && (
                    <Tag type="gray" size="sm" style={{ marginTop: '0.25rem' }}>
                      {p.api_key_ref.startsWith('secret://') ? `secret: ${p.api_key_ref.slice('secret://'.length)}` : 'key: ••••••••'}
                    </Tag>
                  )}
                  {p.models?.length ? (
                    <div style={{ display: 'flex', gap: '0.25rem', flexWrap: 'wrap', marginTop: '0.5rem' }}>
                      {(expanded.has(p.id) ? p.models : p.models.slice(0, MODELS_PREVIEW)).map((m) => <Tag key={m} type="blue" size="sm">{m}</Tag>)}
                      {p.models.length > MODELS_PREVIEW && !expanded.has(p.id) && (
                        <Tag size="sm" type="outline" style={{ cursor: 'pointer' }} onClick={() => setExpanded(new Set([...expanded, p.id]))}>
                          {t('providers.more_models', { count: String(p.models.length - MODELS_PREVIEW) }) ?? `+${p.models.length - MODELS_PREVIEW}`}
                        </Tag>
                      )}
                    </div>
                  ) : <p style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem', marginTop: '0.5rem' }}>{t('providers.no_models') ?? 'No models listed'}</p>}
                </div>
                <Stack orientation="horizontal" gap={1}>
                  <Button size="sm" kind="ghost" hasIconOnly renderIcon={Renew} iconDescription={t('providers.sync_now') ?? 'Sync catalog now'}
                    disabled={syncingId === p.id} onClick={() => void syncProvider(p)} />
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
              {/* One-click presets for the two direct-connection hosts people
                  reach for first; everything OpenAI-compatible works by URL. */}
              <div style={{ display: 'flex', gap: '0.3rem', flexWrap: 'wrap', marginTop: '-0.75rem' }}>
                {[
                  { label: 'Anthropic', url: 'https://api.anthropic.com/v1' },
                  { label: 'OpenRouter', url: 'https://openrouter.ai/api/v1' },
                ].map((preset) => (
                  <Tag
                    key={preset.label}
                    size="sm"
                    type={form.base_url === preset.url ? 'green' : 'outline'}
                    style={{ cursor: 'pointer' }}
                    onClick={() => setForm({ ...form, base_url: preset.url, name: form.name?.trim() || preset.label })}
                  >
                    {preset.label}
                  </Tag>
                ))}
              </div>
              <TextInput id="prov-key" labelText={t('providers.key_label') ?? 'API Key'} value={form.api_key_ref ?? ''} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, api_key_ref: e.target.value })} placeholder="secret://my-api-key, Z_AI_API_KEY or the key itself" helperText={t('providers.key_helper') ?? 'secret://name (recommended), an env var name, or the key itself'} />
              {secrets.length > 0 && (
                <div style={{ display: 'flex', gap: '0.3rem', flexWrap: 'wrap', marginTop: '-0.75rem' }}>
                  {secrets.map((s) => (
                    <Tag key={s.id} size="sm" type={form.api_key_ref === `secret://${s.name}` ? 'green' : 'outline'} style={{ cursor: 'pointer' }} onClick={() => setForm({ ...form, api_key_ref: `secret://${s.name}` })}>
                      secret://{s.name}
                    </Tag>
                  ))}
                </div>
              )}
              <div>
                <TextInput id="prov-models" labelText={t('providers.models_label') ?? 'Models (comma-separated)'} value={form.models?.join(', ') ?? ''} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, models: e.target.value.split(',').map((m) => m.trim()).filter(Boolean) })} placeholder="z-ai-turbo, z-ai-pro"
                  helperText={t('providers.models_helper') ?? 'Leave empty to auto-discover from the provider'} />
                <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', marginTop: '-0.5rem' }}>
                  <Button size="sm" kind="secondary" renderIcon={Renew} disabled={fetching || !form.base_url?.trim()} onClick={() => void discoverModels()}>
                    {fetching ? (t('providers.fetching_models') ?? 'Discovering…') : (t('providers.fetch_models') ?? 'Discover models')}
                  </Button>
                  {fetchNote && <span style={{ fontSize: '0.75rem', color: 'var(--tm-text-3)' }}>{fetchNote}</span>}
                </div>
              </div>
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

      {loading && <PageLoading />}
    </div>
  )
}