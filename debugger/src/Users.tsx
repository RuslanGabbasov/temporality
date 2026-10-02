import { useEffect, useState, useCallback } from 'react'
import {
  Button,
  TextInput,
  Select,
  SelectItem,
  InlineNotification,
  Loading,
  Tag,
  Tile,
  Grid,
  Column,
  Stack,
  Heading,
} from '@carbon/react'
import { Add, Edit, TrashCan, Key } from '@carbon/icons-react'
import { workspaceApi, type User } from './workspaceApi'
import { useT } from './i18n'

function message(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }

export default function Users() {
  const t = useT()
  const [users, setUsers] = useState<User[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [showForm, setShowForm] = useState(false)
  const [editing, setEditing] = useState<User | null>(null)
  const [form, setForm] = useState<Partial<User>>({})
  const [generatedToken, setGeneratedToken] = useState<string | null>(null)
  const [tokenUser, setTokenUser] = useState<User | null>(null)
  const [confirmRegen, setConfirmRegen] = useState<User | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const data = await workspaceApi.listUsers()
      setUsers(data.users ?? [])
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }, [])

  useEffect(() => { void load() }, [load])

  const startCreate = () => {
    setEditing(null)
    setForm({ name: '', email: '', role: 'viewer', projects: [], active: true })
    setShowForm(true)
  }

  const startEdit = (u: User) => {
    setEditing(u)
    setForm({ ...u })
    setShowForm(true)
  }

  const save = async () => {
    if (!form.name?.trim() || !form.role?.trim()) return
    setLoading(true); setError('')
    try {
      let result: User
      if (editing) {
        result = await workspaceApi.updateUser(editing.id, form)
      } else {
        result = await workspaceApi.createUser(form as User & { name: string; role: string })
      }
      setShowForm(false); setEditing(null)
      // Show token if created
      if (result.token) {
        setGeneratedToken(result.token)
        setTokenUser(result)
      }
      void load()
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }

  const remove = async (u: User) => {
    if (!confirm(t('users.delete_confirm', { name: u.name }) ?? `Delete user "${u.name}"?`)) return
    setLoading(true); setError('')
    try {
      await workspaceApi.deleteUser(u.id)
      void load()
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }

  // Tokens are generated and stored server-side; the response returns the
  // only copy the UI will ever see.
  const doGenerateToken = async (u: User) => {
    try {
      const result = await workspaceApi.regenerateUserToken(u.id)
      setGeneratedToken(result.token)
      setTokenUser(u)
      void load() // refresh has_token flags
    } catch (f) { setError(message(f)) }
  }

  const generateToken = (u: User) => {
    if (u.has_token) {
      setConfirmRegen(u)
    } else {
      void doGenerateToken(u)
    }
  }

  const copyToClipboard = (text: string) => {
    void navigator.clipboard.writeText(text)
  }

  const ROLE_COLORS: Record<string, 'blue' | 'green' | 'warm-gray' | 'red'> = {
    admin: 'blue',
    operator: 'green',
    viewer: 'warm-gray',
  }

  return (
    <div style={{ padding: '1rem' }}>
      {error && <InlineNotification kind="error" title={t('action.error') ?? 'Error'} subtitle={error} onClose={() => setError('')} lowContrast style={{ marginBottom: '1rem' }} />}

      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1rem' }}>
        <Heading>{t('users.title') ?? 'Users'}</Heading>
        <Button renderIcon={Add} onClick={startCreate}>{t('new.user') ?? 'New User'}</Button>
      </div>

      <Grid>
        {users.map((u) => (
          <Column key={u.id} sm={4} md={4} lg={4}>
            <Tile style={{ marginBottom: '0.75rem' }}>
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start' }}>
                <div>
                  <strong>{u.name}</strong>
                  {u.email && <p style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem', marginTop: '0.25rem' }}>{u.email}</p>}
                  <div style={{ display: 'flex', gap: '0.25rem', flexWrap: 'wrap', marginTop: '0.5rem' }}>
                    <Tag type={ROLE_COLORS[u.role] || 'gray'} size="sm">{u.role}</Tag>
                    <Tag type={u.active ? 'green' : 'red'} size="sm">{u.active ? (t('users.active') ?? 'active') : (t('users.inactive') ?? 'inactive')}</Tag>
                    {u.projects?.length ? u.projects.map((p) => <Tag key={p} type="gray" size="sm">{p}</Tag>) : <Tag type="gray" size="sm">{t('users.all_projects') ?? 'all projects'}</Tag>}
                  </div>
                </div>
                <Stack orientation="horizontal" gap={1}>
                  <Button size="sm" kind="ghost" hasIconOnly renderIcon={Key} iconDescription={t('users.generate_token') ?? 'Generate Token'} onClick={() => generateToken(u)} />
                  <Button size="sm" kind="ghost" hasIconOnly renderIcon={Edit} iconDescription={t('action.edit') ?? 'Edit'} onClick={() => startEdit(u)} />
                  <Button size="sm" kind="danger--ghost" hasIconOnly renderIcon={TrashCan} iconDescription={t('action.delete') ?? 'Delete'} onClick={() => void remove(u)} />
                </Stack>
              </div>
            </Tile>
          </Column>
        ))}
      </Grid>

      {showForm && (
        <div className="modal-overlay">
          <div className="modal-panel">
            <Heading>{editing ? (t('users.edit_user') ?? 'Edit User') : (t('new.user') ?? 'New User')}</Heading>
            <Stack gap={3}>
              <TextInput id="user-name" labelText={t('users.name_label') ?? 'Name'} value={form.name ?? ''} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, name: e.target.value })} placeholder="John Doe" autoFocus />
              <TextInput id="user-email" labelText={t('users.email_label') ?? 'Email'} value={form.email ?? ''} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, email: e.target.value })} placeholder="john@example.com" />
              <Select id="user-role" labelText={t('users.role_label') ?? 'Role'} value={form.role ?? 'viewer'} onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setForm({ ...form, role: e.target.value })}>
                <SelectItem value="viewer" text={t('users.role_viewer') ?? 'Viewer (read-only)'} />
                <SelectItem value="operator" text={t('users.role_operator') ?? 'Operator (run + reconcile)'} />
                <SelectItem value="admin" text={t('users.role_admin') ?? 'Admin (full access)'} />
              </Select>
              <div className="form-actions">
                <Button kind="secondary" onClick={() => { setShowForm(false); setEditing(null) }}>{t('action.cancel') ?? 'Cancel'}</Button>
                <Button onClick={() => void save()}>{editing ? (t('action.save') ?? 'Save') : (t('action.create') ?? 'Create')}</Button>
              </div>
            </Stack>
          </div>
        </div>
      )}

      {generatedToken && tokenUser && (
        <div className="modal-overlay">
          <div className="modal-panel" style={{ width: '600px' }}>
            <Heading>{t('users.token_for', { name: tokenUser.name }) ?? `Token for ${tokenUser.name}`}</Heading>
            <p style={{ color: 'var(--tm-text-3)', marginBottom: '1rem' }}>{t('users.token_hint') ?? 'Token stored in database. User can log in immediately after kernel restart.'}</p>
            <div style={{ background: 'var(--tm-elevated)', border: '1px solid var(--tm-border)', borderRadius: '4px', padding: '0.75rem', fontFamily: 'monospace', fontSize: '0.875rem', wordBreak: 'break-all', marginBottom: '1rem' }}>
              {generatedToken}
            </div>
            <div className="form-actions">
              <Button kind="secondary" onClick={() => { setGeneratedToken(null); setTokenUser(null) }}>{t('action.close') ?? 'Close'}</Button>
              <Button onClick={() => { copyToClipboard(generatedToken); setGeneratedToken(null); setTokenUser(null) }}>{t('users.copy_close') ?? 'Copy & Close'}</Button>
            </div>
          </div>
        </div>
      )}

      {confirmRegen && (
        <div className="modal-overlay">
          <div className="modal-panel" style={{ width: '460px' }}>
            <Heading>{t('users.regenerate_confirm') ?? 'Regenerate token?'}</Heading>
            <p style={{ color: 'var(--tm-text-3)', margin: '0.75rem 0' }}>
              {t('users.regenerate_warning', { name: confirmRegen.name }) ?? `${confirmRegen.name} already has an active token. Generating a new one will invalidate the current token immediately — the user will be logged out.`}
            </p>
            <div className="form-actions">
              <Button kind="secondary" onClick={() => setConfirmRegen(null)}>{t('action.cancel') ?? 'Cancel'}</Button>
              <Button kind="danger" onClick={() => { const u = confirmRegen; setConfirmRegen(null); doGenerateToken(u) }}>{t('action.regenerate') ?? 'Regenerate'}</Button>
            </div>
          </div>
        </div>
      )}

      {loading && <Loading withOverlay={false} />}
    </div>
  )
}
