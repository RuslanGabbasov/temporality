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

function message(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }

export default function Providers() {
  const [providers, setProviders] = useState<Provider[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [showForm, setShowForm] = useState(false)
  const [editing, setEditing] = useState<Provider | null>(null)
  const [form, setForm] = useState<Partial<Provider>>({})

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
      } else {
        await workspaceApi.createProvider(form as Provider & { name: string; base_url: string })
      }
      setShowForm(false); setEditing(null)
      void load()
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }

  const remove = async (p: Provider) => {
    if (!confirm(`Delete provider "${p.name}"?`)) return
    setLoading(true); setError('')
    try {
      await workspaceApi.deleteProvider(p.id)
      void load()
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }

  return (
    <div style={{ padding: '1rem' }}>
      {error && <InlineNotification kind="error" title="Error" subtitle={error} onClose={() => setError('')} lowContrast style={{ marginBottom: '1rem' }} />}

      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1rem' }}>
        <Heading>Model Providers</Heading>
        <Button renderIcon={Add} onClick={startCreate}>New Provider</Button>
      </div>

      <Grid>
        {providers.map((p) => (
          <Column key={p.id} sm={4} md={4} lg={4}>
            <Tile style={{ marginBottom: '0.75rem' }}>
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start' }}>
                <div>
                  <strong>{p.name}</strong>
                  <p style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem', marginTop: '0.25rem', wordBreak: 'break-all' }}>{p.base_url}</p>
                  {p.api_key_ref && <Tag type="gray" size="sm" style={{ marginTop: '0.25rem' }}>key: {'••••••••'}</Tag>}
                  {p.models?.length ? (
                    <div style={{ display: 'flex', gap: '0.25rem', flexWrap: 'wrap', marginTop: '0.5rem' }}>
                      {p.models.map((m) => <Tag key={m} type="blue" size="sm">{m}</Tag>)}
                    </div>
                  ) : <p style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem', marginTop: '0.5rem' }}>No models listed</p>}
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
        <div className="modal-overlay">
          <div className="modal-panel">
            <Heading>{editing ? 'Edit Provider' : 'New Provider'}</Heading>
            <Stack gap={3}>
              <TextInput id="prov-name" labelText="Name" value={form.name ?? ''} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, name: e.target.value })} placeholder="z.ai" autoFocus />
              <TextInput id="prov-url" labelText="Base URL" value={form.base_url ?? ''} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, base_url: e.target.value })} placeholder="https://api.z-ai.com/v1" />
              <TextInput id="prov-key" labelText="API Key / Env Var" value={form.api_key_ref ?? ''} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, api_key_ref: e.target.value })} placeholder="Z_AI_API_KEY or actual key" helperText="Env var name (e.g. Z_AI_API_KEY) or the actual key" />
              <TextInput id="prov-models" labelText="Models (comma-separated)" value={form.models?.join(', ') ?? ''} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, models: e.target.value.split(',').map((m) => m.trim()).filter(Boolean) })} placeholder="z-ai-turbo, z-ai-pro" />
              <div className="form-actions">
                <Button kind="secondary" onClick={() => { setShowForm(false); setEditing(null) }}>Cancel</Button>
                <Button onClick={() => void save()}>{editing ? 'Save' : 'Create'}</Button>
              </div>
            </Stack>
          </div>
        </div>
      )}

      {loading && <Loading withOverlay={false} />}
    </div>
  )
}