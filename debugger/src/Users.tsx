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

function message(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }

export default function Users() {
  const [users, setUsers] = useState<User[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [showForm, setShowForm] = useState(false)
  const [editing, setEditing] = useState<User | null>(null)
  const [form, setForm] = useState<Partial<User>>({})
  const [generatedToken, setGeneratedToken] = useState<string | null>(null)
  const [tokenUser, setTokenUser] = useState<User | null>(null)

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
      if (editing) {
        await workspaceApi.updateUser(editing.id, form)
      } else {
        await workspaceApi.createUser(form as User & { name: string; role: string })
      }
      setShowForm(false); setEditing(null)
      void load()
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }

  const remove = async (u: User) => {
    if (!confirm(`Delete user "${u.name}"?`)) return
    setLoading(true); setError('')
    try {
      await workspaceApi.deleteUser(u.id)
      void load()
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }

  const generateToken = (u: User) => {
    const bytes = new Uint8Array(32)
    crypto.getRandomValues(bytes)
    const token = Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('')
    setGeneratedToken(token)
    setTokenUser(u)
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
      {error && <InlineNotification kind="error" title="Error" subtitle={error} onClose={() => setError('')} lowContrast style={{ marginBottom: '1rem' }} />}

      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1rem' }}>
        <Heading>Users</Heading>
        <Button renderIcon={Add} onClick={startCreate}>New User</Button>
      </div>

      <Grid>
        {users.map((u) => (
          <Column key={u.id} sm={4} md={4} lg={4}>
            <Tile style={{ marginBottom: '0.75rem' }}>
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start' }}>
                <div>
                  <strong>{u.name}</strong>
                  {u.email && <p style={{ color: '#7e8a9c', fontSize: '0.75rem', marginTop: '0.25rem' }}>{u.email}</p>}
                  <div style={{ display: 'flex', gap: '0.25rem', flexWrap: 'wrap', marginTop: '0.5rem' }}>
                    <Tag type={ROLE_COLORS[u.role] || 'gray'} size="sm">{u.role}</Tag>
                    <Tag type={u.active ? 'green' : 'red'} size="sm">{u.active ? 'active' : 'inactive'}</Tag>
                    {u.projects?.length ? u.projects.map((p) => <Tag key={p} type="gray" size="sm">{p}</Tag>) : <Tag type="gray" size="sm">all projects</Tag>}
                  </div>
                </div>
                <Stack orientation="horizontal" gap={1}>
                  <Button size="sm" kind="ghost" renderIcon={Key} iconDescription="Generate Token" onClick={() => generateToken(u)} />
                  <Button size="sm" kind="ghost" renderIcon={Edit} iconDescription="Edit" onClick={() => startEdit(u)} />
                  <Button size="sm" kind="danger--ghost" renderIcon={TrashCan} iconDescription="Delete" onClick={() => void remove(u)} />
                </Stack>
              </div>
            </Tile>
          </Column>
        ))}
      </Grid>

      {showForm && (
        <div style={{ position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.6)', zIndex: 1000, display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
          <div style={{ background: '#0d1118', border: '1px solid #344258', borderRadius: '8px', padding: '1.5rem', width: '500px' }}>
            <Heading>{editing ? 'Edit User' : 'New User'}</Heading>
            <Stack gap={3}>
              <TextInput id="user-name" labelText="Name" value={form.name ?? ''} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, name: e.target.value })} placeholder="John Doe" autoFocus />
              <TextInput id="user-email" labelText="Email" value={form.email ?? ''} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, email: e.target.value })} placeholder="john@example.com" />
              <Select id="user-role" labelText="Role" value={form.role ?? 'viewer'} onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setForm({ ...form, role: e.target.value })}>
                <SelectItem value="viewer" text="Viewer (read-only)" />
                <SelectItem value="operator" text="Operator (run + reconcile)" />
                <SelectItem value="admin" text="Admin (full access)" />
              </Select>
              <Stack orientation="horizontal" gap={2}>
                <Button onClick={() => void save()}>{editing ? 'Save' : 'Create'}</Button>
                <Button kind="secondary" onClick={() => { setShowForm(false); setEditing(null) }}>Cancel</Button>
              </Stack>
            </Stack>
          </div>
        </div>
      )}

      {generatedToken && tokenUser && (
        <div style={{ position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.6)', zIndex: 1000, display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
          <div style={{ background: '#0d1118', border: '1px solid #344258', borderRadius: '8px', padding: '1.5rem', width: '600px' }}>
            <Heading>Token for {tokenUser.name}</Heading>
            <p style={{ color: '#7e8a9c', marginBottom: '1rem' }}>Copy this token now — it will not be shown again.</p>
            <div style={{ background: '#121823', border: '1px solid #344258', borderRadius: '4px', padding: '0.75rem', fontFamily: 'monospace', fontSize: '0.875rem', wordBreak: 'break-all', marginBottom: '1rem' }}>
              {generatedToken}
            </div>
            <p style={{ color: '#7e8a9c', marginBottom: '0.5rem' }}>Add this line to <code>.env</code> (append to KERNEL_AUTH_TOKENS):</p>
            <div style={{ background: '#121823', border: '1px solid #344258', borderRadius: '4px', padding: '0.75rem', fontFamily: 'monospace', fontSize: '0.75rem', wordBreak: 'break-all', marginBottom: '1rem' }}>
              {generatedToken}:{tokenUser.name}:{tokenUser.role}:{tokenUser.projects?.length ? tokenUser.projects.join(',') : '*'}
            </div>
            <Stack orientation="horizontal" gap={2}>
              <Button onClick={() => { copyToClipboard(generatedToken + ':' + tokenUser.name + ':' + tokenUser.role + ':' + (tokenUser.projects?.length ? tokenUser.projects.join(',') : '*')); setGeneratedToken(null); setTokenUser(null) }}>Copy & Close</Button>
              <Button kind="secondary" onClick={() => { setGeneratedToken(null); setTokenUser(null) }}>Close</Button>
            </Stack>
          </div>
        </div>
      )}

      {loading && <Loading withOverlay={false} />}
    </div>
  )
}