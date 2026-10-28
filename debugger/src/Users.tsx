import { useEffect, useState, useCallback } from 'react'
import {
  Button,
  TextInput,
  Select,
  SelectItem,
  InlineNotification,
  Tag,
  Tile,
  Grid,
  Column,
  Stack,
  Heading,
  Toggle,
} from '@carbon/react'
import PageLoading from './PageLoading'
import { Add, Edit, TrashCan, Key, Chat, ChevronRight, ChevronDown } from '@carbon/icons-react'
import { workspaceApi, type User, type OrgUnit } from './workspaceApi'
import { useT } from './i18n'
import ListFilter, { matchesFilter } from './ListFilter'
import AppModal from './Modal'
import { useConfirm } from './ConfirmDialog'
import { copyText } from './clipboard'
import ChannelsDialog, { ChannelBadges } from './ChannelsDialog'
import { useOrgUnits, OrgUnitSelect, OrgBadge, flattenUnits } from './orgUnits'

function message(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }

/** Unit ids in the subtree of `unitID` (inclusive). A user whose primary unit
 *  is in the subtree belongs to this branch of the organization. */
function unitSubtreeIDs(units: OrgUnit[], unitID: string): Set<string> {
  const childrenOf = new Map<string, string[]>()
  for (const unit of units) {
    if (unit.parent_id) {
      const list = childrenOf.get(unit.parent_id) ?? []
      list.push(unit.id)
      childrenOf.set(unit.parent_id, list)
    }
  }
  const out = new Set<string>([unitID])
  const walk = (id: string) => {
    for (const child of childrenOf.get(id) ?? []) {
      if (!out.has(child)) { out.add(child); walk(child) }
    }
  }
  walk(unitID)
  return out
}

export default function Users() {
  const t = useT()
  const { confirmDialog, dialog: confirmDialogNode } = useConfirm()
  const org = useOrgUnits()
  const [users, setUsers] = useState<User[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [showForm, setShowForm] = useState(false)
  const [editing, setEditing] = useState<User | null>(null)
  const [form, setForm] = useState<Partial<User>>({})
  const [generatedToken, setGeneratedToken] = useState<string | null>(null)
  const [tokenUser, setTokenUser] = useState<User | null>(null)
  const [confirmRegen, setConfirmRegen] = useState<User | null>(null)
  const [channelsUser, setChannelsUser] = useState<User | null>(null)
  const [filter, setFilter] = useState('')
  // Org-tree navigation: '' = every user, a unit id = users whose primary
  // unit is in that unit's subtree, '__none__' = users without any unit.
  const [selectedUnit, setSelectedUnit] = useState('')
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set())

  const visibleUsers = users.filter((u) => {
    if (!matchesFilter(filter, u.name, u.email, u.role, u.id, u.job_title ?? '')) return false
    if (selectedUnit === '') return true
    if (selectedUnit === '__none__') return !u.org_unit_id
    return u.org_unit_id ? unitSubtreeIDs(org.units, selectedUnit).has(u.org_unit_id) : false
  })

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const data = await workspaceApi.listUsers()
      setUsers(data.users ?? [])
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }, [])

  useEffect(() => { void load() }, [load])

  // The uniform switch (the trigger pattern): an inactive user cannot sign
  // in. Optimistic flip, rollback on failure.
  const toggleUserEnabled = async (u: User) => {
    const next = !u.active
    setUsers((prev) => prev.map((x) => (x.id === u.id ? { ...x, active: next } : x)))
    try {
      const updated = await workspaceApi.setUserEnabled(u.id, next)
      setUsers((prev) => prev.map((x) => (x.id === u.id ? updated : x)))
    } catch (f) {
      setUsers((prev) => prev.map((x) => (x.id === u.id ? { ...x, active: u.active } : x)))
      setError(message(f))
    }
  }

  const startCreate = () => {
    setEditing(null)
    setFormEnabled(true)
    // The org tree selection seeds the form: filling a branch means every
    // new user lands in that branch by default.
    setForm({ name: '', email: '', role: 'viewer', active: true, org_unit_id: selectedUnit !== '' && selectedUnit !== '__none__' ? selectedUnit : '', job_title: '', password: '' })
    setShowForm(true)
  }

  // The edit dialog's copy of the uniform switch: applied through the
  // /enabled endpoint after the profile save (the PUT never carries it).
  const [formEnabled, setFormEnabled] = useState(true)

  const startEdit = (u: User) => {
    setEditing(u)
    setFormEnabled(u.active)
    setForm({ ...u, password: '' })
    setShowForm(true)
  }

  const save = async () => {
    if (!form.name?.trim() || !form.role?.trim()) return
    setLoading(true); setError('')
    try {
      // Password travels only when the admin typed one: absent on edit keeps
      // the current hash, so ordinary role/name edits never lock users out.
      const payload: Partial<User> = { ...form }
      if (!payload.password) delete payload.password
      let result: User
      if (editing) {
        result = await workspaceApi.updateUser(editing.id, payload)
        if (formEnabled !== editing.active) {
          await workspaceApi.setUserEnabled(editing.id, formEnabled)
        }
      } else {
        result = await workspaceApi.createUser(payload as User & { name: string; role: string })
        if (!formEnabled) {
          await workspaceApi.setUserEnabled(result.id, false)
        }
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
    if (!(await confirmDialog({ title: t('users.delete_confirm', { name: u.name }) ?? `Delete user "${u.name}"?`, body: t('action.delete_warning') ?? 'This cannot be undone.', danger: true, confirmLabel: t('action.delete') ?? 'Delete' }))) return
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
    // Shared helper with an http-LAN fallback: navigator.clipboard is undefined
    // in insecure contexts and calling it threw, breaking Copy & Close here.
    void copyText(text)
  }

  const ROLE_COLORS: Record<string, 'blue' | 'green' | 'warm-gray' | 'red'> = {
    admin: 'blue',
    operator: 'green',
    viewer: 'warm-gray',
  }

  return (
    <div style={{ padding: '1rem' }}>
      {confirmDialogNode}
      {error && <InlineNotification kind="error" title={t('action.error') ?? 'Error'} subtitle={error} onClose={() => setError('')} lowContrast style={{ marginBottom: '1rem' }} />}

      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '0.5rem' }}>
        <Heading>{t('users.title') ?? 'Users'}</Heading>
        <Button renderIcon={Add} onClick={startCreate}>{org.units.length > 0 && selectedUnit !== '' && selectedUnit !== '__none__' ? (t('users.add_to_unit') ?? 'Add to unit') : (t('new.user') ?? 'New User')}</Button>
      </div>

      <div style={{ display: 'grid', gridTemplateColumns: org.units.length > 0 ? 'minmax(230px, 280px) 1fr' : '1fr', gap: '1.25rem', alignItems: 'start' }}>
        {/* Left: the org unit tree — fill the company branch by branch. */}
        {org.units.length > 0 && (() => {
          const directByUnit = new Map<string, number>()
          for (const u of users) {
            if (u.org_unit_id) directByUnit.set(u.org_unit_id, (directByUnit.get(u.org_unit_id) ?? 0) + 1)
          }
          const flattened = flattenUnits(org.units)
          const noneCount = users.filter((u) => !u.org_unit_id).length
          const toggle = (id: string) => setCollapsed((current) => {
            const next = new Set(current)
            if (next.has(id)) next.delete(id)
            else next.add(id)
            return next
          })
          const row = (label: string, value: string, count: number, depth: number, hasChildren: boolean, isCollapsed: boolean, onToggle: (() => void) | null, icon?: string) => (
            <div
              role="button" tabIndex={0}
              onClick={() => setSelectedUnit(value)}
              onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); setSelectedUnit(value) } }}
              style={{
                display: 'flex', alignItems: 'center', gap: '0.35rem', padding: '0.22rem 0.35rem', cursor: 'pointer', borderRadius: '4px',
                fontSize: '0.78rem', paddingLeft: `${0.35 + depth * 0.85}rem`,
                background: selectedUnit === value ? 'var(--tm-elevated)' : 'transparent',
                color: selectedUnit === value ? 'var(--tm-cream)' : 'var(--tm-text-2)',
              }}
            >
              <span className="chevron-hit" onClick={(e) => { if (onToggle) { e.stopPropagation(); onToggle() } }} style={{ cursor: onToggle ? 'pointer' : 'default' }}>
                {hasChildren ? (isCollapsed ? <ChevronRight size={13} /> : <ChevronDown size={13} />) : icon ? <span style={{ fontSize: '0.72rem' }}>{icon}</span> : null}
              </span>
              <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', flex: 1 }}>{label}</span>
              <span style={{ color: 'var(--tm-text-3)', fontSize: '0.68rem', flexShrink: 0 }}>{count}</span>
            </div>
          )
          const subtreeCount = (id: string) => unitSubtreeIDs(org.units, id) && users.filter((u) => u.org_unit_id && unitSubtreeIDs(org.units, id).has(u.org_unit_id)).length
          return (
            <div style={{ border: '1px solid var(--tm-border)', borderRadius: '6px', padding: '0.6rem 0.7rem', maxHeight: 'calc(100vh - 12rem)', overflowY: 'auto' }}>
              {row(t('users.all_users') ?? 'All users', '', users.length, 0, true, collapsed.has('__root__'), () => toggle('__root__'), '◉')}
              {!collapsed.has('__root__') && (
                <>
                  {flattened.filter(({ unit }) => {
                    // A collapsed ancestor hides the whole branch: walk the
                    // parent chain and drop any node under a collapsed unit.
                    let cursor = unit.parent_id
                    while (cursor) {
                      if (collapsed.has(cursor)) return false
                      cursor = org.units.find((u) => u.id === cursor)?.parent_id
                    }
                    return true
                  }).map(({ unit, depth }) => {
                    const children = org.units.filter((u) => u.parent_id === unit.id)
                    const isCollapsed = collapsed.has(unit.id)
                    return row(unit.name, unit.id, subtreeCount(unit.id), depth, children.length > 0, isCollapsed, children.length > 0 ? () => toggle(unit.id) : null)
                  })}
                  {noneCount > 0 && row(t('org.unassigned_badge') ?? 'no unit', '__none__', noneCount, 0, false, false, null, '◌')}
                </>
              )}
            </div>
          )
        })()}

        {/* Right: the users of the selected branch */}
        <div>
          <div style={{ position: 'sticky', top: '3rem', background: 'var(--tm-bg)', zIndex: 1, paddingBottom: '0.5rem', marginBottom: '0.5rem', display: 'flex', gap: '0.6rem', alignItems: 'center' }}>
            <div style={{ flex: 1, maxWidth: '420px' }}>
              <ListFilter value={filter} onChange={setFilter} placeholder={t('common.filter_users') ?? 'Filter users…'} />
            </div>
            {selectedUnit !== '' && (
              <button
                onClick={() => setSelectedUnit('')}
                title={t('users.clear_unit_filter') ?? 'Show every user'}
                style={{ background: 'none', border: '1px solid var(--tm-border-strong)', borderRadius: '999px', color: 'var(--tm-text-2)', cursor: 'pointer', fontSize: '0.68rem', padding: '0.15rem 0.6rem', whiteSpace: 'nowrap', flexShrink: 0 }}
              >
                {selectedUnit === '__none__' ? (t('org.unassigned_badge') ?? 'no unit') : (org.units.find((u) => u.id === selectedUnit)?.name ?? selectedUnit)} ✕
              </button>
            )}
          </div>

          <Grid>
            {visibleUsers.map((u) => (
          <Column key={u.id} sm={4} md={4} lg={4}>
            <Tile style={{ marginBottom: '0.75rem', opacity: u.active ? 1 : 0.5, display: 'flex', flexDirection: 'column' }}>
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', flex: 1 }}>
                				<div style={{ minWidth: 0, flex: 1 }}>
                					<div style={{ display: 'flex', alignItems: 'center', gap: '0.3rem', flexWrap: 'wrap' }}>
                						<strong>{u.name}</strong>
                						{u.org_unit_id ? <OrgBadge orgUnitID={u.org_unit_id} org={org} /> : (org.units.length > 0 && <Tag type="gray" size="sm">{t('org.unassigned_badge') ?? 'no unit'}</Tag>)}
                					</div>
                					{u.email && <p style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem', marginTop: '0.25rem', overflowWrap: 'anywhere', wordBreak: 'break-word' }}>{u.email}</p>}
					{u.job_title && <p style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem', marginTop: '0.15rem' }}>{u.job_title}</p>}
                  <div style={{ display: 'flex', gap: '0.25rem', flexWrap: 'wrap', marginTop: '0.5rem' }}>
                    <Tag type={ROLE_COLORS[u.role] || 'gray'} size="sm">{u.role}</Tag>
                    <Tag type={u.active ? 'green' : 'red'} size="sm">{u.active ? (t('users.active') ?? 'active') : (t('users.inactive') ?? 'inactive')}</Tag>
                    {u.has_password && <Tag type="outline" size="sm">{t('users.has_password') ?? 'password login'}</Tag>}
                  </div>
                  <ChannelBadges user={u} />
                </div>
                {/* Actions live in a right-hand column: four icon buttons in a
                    horizontal row overflew the card on narrow grids. */}
                <Stack orientation="vertical" gap={1} style={{ flexShrink: 0 }}>
                  <Button size="sm" kind="ghost" hasIconOnly renderIcon={Chat} iconDescription={t('users.edit_channels') ?? 'Communication channels'} onClick={() => setChannelsUser(u)} />
                  <Button size="sm" kind="ghost" hasIconOnly renderIcon={Key} iconDescription={t('users.generate_token') ?? 'Generate Token'} onClick={() => generateToken(u)} />
                  <Button size="sm" kind="ghost" hasIconOnly renderIcon={Edit} iconDescription={t('action.edit') ?? 'Edit'} onClick={() => startEdit(u)} />
                  <Button size="sm" kind="danger--ghost" hasIconOnly renderIcon={TrashCan} iconDescription={t('action.delete') ?? 'Delete'} onClick={() => void remove(u)} />
                </Stack>
              </div>
              {/* The uniform switch sits on the card's own bottom edge — clear
                  of the vertical action stack, so the two never fight for the
                  same corner and the toggle never dances. */}
              <div style={{ display: 'flex', justifyContent: 'flex-end', marginTop: '0.5rem', paddingTop: '0.4rem', borderTop: '1px solid var(--tm-border)' }} title={t('users.enabled_hint') ?? 'Inactive users cannot sign in'}>
                <Toggle
                  id={`user-enabled-${u.id}`}
                  labelText=""
                  hideLabel
                  aria-label={t('users.enabled_hint') ?? 'Toggle user'}
                  toggled={u.active}
                  onToggle={() => void toggleUserEnabled(u)}
                  size="sm"
                />
              </div>
            </Tile>
          </Column>
        ))}
          </Grid>
          {visibleUsers.length === 0 && !loading && (
            <Tile style={{ color: 'var(--tm-text-3)', textAlign: 'center' }}>{t('users.none_in_unit') ?? 'No users in this branch yet.'}</Tile>
          )}
        </div>
      </div>

      {showForm && (
        <AppModal onClose={() => { setShowForm(false); setEditing(null) }}>
            <Heading>{editing ? (t('users.edit_user') ?? 'Edit User') : (t('new.user') ?? 'New User')}</Heading>
            <Stack gap={3}>
              <TextInput id="user-name" labelText={t('users.name_label') ?? 'Name'} value={form.name ?? ''} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, name: e.target.value })} placeholder="John Doe" autoFocus />
              <TextInput id="user-email" labelText={t('users.email_label') ?? 'Email'} value={form.email ?? ''} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, email: e.target.value })} placeholder="john@example.com" />
              <TextInput id="user-job-title" labelText={t('users.job_title_label') ?? 'Job title'} value={form.job_title ?? ''} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, job_title: e.target.value })} placeholder={t('users.job_title_placeholder') ?? 'QA engineer, разработчик — the group address for asks'} helperText={t('users.job_title_helper') ?? 'The natural way to address a group: ask the agent to invite every QA engineer or notify all developers.'} />
              <Select id="user-role" labelText={t('users.role_label') ?? 'Role'} value={form.role ?? 'viewer'} onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setForm({ ...form, role: e.target.value })}>
                <SelectItem value="viewer" text={t('users.role_viewer') ?? 'Viewer (read-only)'} />
                <SelectItem value="operator" text={t('users.role_operator') ?? 'Operator (run + reconcile)'} />
                <SelectItem value="admin" text={t('users.role_admin') ?? 'Admin (full access)'} />
              </Select>
              {org.units.length > 0 && (
                <OrgUnitSelect
                  id="user-org"
                  label={t('users.org_unit') ?? 'Primary unit'}
                  value={form.org_unit_id ?? ''}
                  onChange={(orgUnitID) => setForm({ ...form, org_unit_id: orgUnitID })}
                  org={org}
                  allowUnassigned
                />
              )}
              <TextInput
                id="user-password"
                labelText={t('users.password') ?? 'Password'}
                type="password"
                value={form.password ?? ''}
                onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, password: e.target.value })}
                placeholder="••••••••"
                helperText={editing
                  ? (t('users.password_hint_edit') ?? 'Leave empty to keep the current password.')
                  : (t('users.password_hint_create') ?? 'Optional — lets the user sign in with name and password.')}
              />
              <div className="form-actions">
                {/* The uniform switch at the bottom-left of the button block. */}
                <div style={{ marginRight: 'auto', display: 'flex', alignItems: 'center' }} title={t('users.enabled_hint') ?? 'Inactive users cannot sign in'}>
                  <Toggle id="user-form-enabled" labelText="" hideLabel aria-label={t('users.enabled_hint') ?? 'Enabled'} labelA={t('common.off') ?? 'Off'} labelB={t('common.on') ?? 'On'} toggled={formEnabled} onToggle={(v: boolean) => setFormEnabled(v)} size="sm" />
                </div>
                <Button kind="secondary" onClick={() => { setShowForm(false); setEditing(null) }}>{t('action.cancel') ?? 'Cancel'}</Button>
                <Button onClick={() => void save()}>{editing ? (t('action.save') ?? 'Save') : (t('action.create') ?? 'Create')}</Button>
              </div>
            </Stack>
        </AppModal>
      )}

      {generatedToken && tokenUser && (
        <AppModal onClose={() => { setGeneratedToken(null); setTokenUser(null) }} panelStyle={{ width: '600px' }}>
            <Heading>{t('users.token_for', { name: tokenUser.name }) ?? `Token for ${tokenUser.name}`}</Heading>
            <p style={{ color: 'var(--tm-text-3)', marginBottom: '1rem' }}>{t('users.token_hint') ?? 'Token stored in database. User can log in immediately after kernel restart.'}</p>
            <div style={{ background: 'var(--tm-elevated)', border: '1px solid var(--tm-border)', borderRadius: '4px', padding: '0.75rem', fontFamily: 'monospace', fontSize: '0.875rem', wordBreak: 'break-all', marginBottom: '1rem' }}>
              {generatedToken}
            </div>
            <div className="form-actions">
              <Button kind="secondary" onClick={() => { setGeneratedToken(null); setTokenUser(null) }}>{t('action.close') ?? 'Close'}</Button>
              <Button onClick={() => { copyToClipboard(generatedToken); setGeneratedToken(null); setTokenUser(null) }}>{t('users.copy_close') ?? 'Copy & Close'}</Button>
            </div>
        </AppModal>
      )}

      {confirmRegen && (
        <AppModal onClose={() => setConfirmRegen(null)} panelStyle={{ width: '460px' }}>
            <Heading>{t('users.regenerate_confirm') ?? 'Regenerate token?'}</Heading>
            <p style={{ color: 'var(--tm-text-3)', margin: '0.75rem 0' }}>
              {t('users.regenerate_warning', { name: confirmRegen.name }) ?? `${confirmRegen.name} already has an active token. Generating a new one will invalidate the current token immediately — the user will be logged out.`}
            </p>
            <div className="form-actions">
              <Button kind="secondary" onClick={() => setConfirmRegen(null)}>{t('action.cancel') ?? 'Cancel'}</Button>
              <Button kind="danger" onClick={() => { const u = confirmRegen; setConfirmRegen(null); doGenerateToken(u) }}>{t('action.regenerate') ?? 'Regenerate'}</Button>
            </div>
        </AppModal>
      )}

      {channelsUser && (
        <ChannelsDialog
          userId={channelsUser.id}
          userName={channelsUser.name}
          onClose={() => setChannelsUser(null)}
        />
      )}

      {loading && <PageLoading />}
    </div>
  )
}
