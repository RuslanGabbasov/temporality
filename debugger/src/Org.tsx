import { useEffect, useState, useCallback } from 'react'
import {
  Button,
  TextInput,
  Select,
  SelectItem,
  InlineNotification,
  Loading,
  Tag,
  Heading,
} from '@carbon/react'
import { Add, Edit, TrashCan, ChevronRight, ChevronDown, Building, Enterprise } from '@carbon/icons-react'
import { workspaceApi, type OrgUnit, type UnitResources, type OrgUnitKind, type Policy, type OrgUnitRoleGrant, type ExecutionIdentity, type User } from './workspaceApi'
import { useT } from './i18n'
import { whoami } from './kernelApi'
import ListFilter, { matchesFilter } from './ListFilter'
import AppModal from './Modal'
import { flattenUnits } from './orgUnits'

function message(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }

const KIND_TAGS: Record<string, 'cyan' | 'blue' | 'green' | 'gray'> = {
  organization: 'cyan',
  department: 'blue',
  team: 'green',
}

function kindLabel(t: (k: string, v?: Record<string, string>) => string, kind: string) {
  return t(`org.kind.${kind}`) ?? kind
}

type Node = { unit: OrgUnit; children: Node[] }

// Policy dialog form: comma-separated lists and optional numbers are edited as
// strings; empty means "no restriction" on save.
type PolicyForm = {
  name: string
  org_unit_id: string
  allowed_models: string
  allowed_mcp: string
  max_tokens: string
  max_budget_usd: string
  timeout_seconds: string
  network_mode: string
  sandbox_mode: string
  approval_mode: string
  enabled: boolean
}

const emptyPolicyForm = (): PolicyForm => ({
  name: '', org_unit_id: '', allowed_models: '', allowed_mcp: '',
  max_tokens: '', max_budget_usd: '', timeout_seconds: '',
  network_mode: '', sandbox_mode: '', approval_mode: '', enabled: true,
})

const policyFormOf = (p: Policy): PolicyForm => ({
  name: p.name,
  org_unit_id: p.org_unit_id ?? '',
  allowed_models: (p.allowed_models ?? []).join(', '),
  allowed_mcp: (p.allowed_mcp ?? []).join(', '),
  max_tokens: p.max_tokens != null ? String(p.max_tokens) : '',
  max_budget_usd: p.max_budget_usd != null ? String(p.max_budget_usd) : '',
  timeout_seconds: p.timeout_seconds != null ? String(p.timeout_seconds) : '',
  network_mode: p.network_mode ?? '',
  sandbox_mode: p.sandbox_mode ?? '',
  approval_mode: p.approval_mode ?? '',
  enabled: p.enabled,
})

const splitList = (raw: string): string[] =>
  raw.split(',').map((x) => x.trim()).filter(Boolean)

const optionalNumber = (raw: string): number | undefined => {
  const n = Number(raw.trim())
  return raw.trim() !== '' && Number.isFinite(n) && n > 0 ? n : undefined
}

function policySummaryTags(p: Policy, t: (k: string, v?: Record<string, string>) => string) {
  const tags: { key: string; text: string }[] = []
  if (p.allowed_models && p.allowed_models.length > 0) tags.push({ key: 'models', text: `${t('org.policy_models') ?? 'Models'}: ${p.allowed_models.length}` })
  if (p.allowed_mcp && p.allowed_mcp.length > 0) tags.push({ key: 'mcp', text: `${t('org.policy_mcp') ?? 'MCP'}: ${p.allowed_mcp.length}` })
  if (p.max_tokens != null) tags.push({ key: 'tokens', text: t('org.policy_tokens_tag', { count: String(p.max_tokens) }) ?? `≤ ${p.max_tokens} tokens` })
  if (p.max_budget_usd != null) tags.push({ key: 'budget', text: t('org.policy_budget_tag', { amount: String(p.max_budget_usd) }) ?? `≤ $${p.max_budget_usd}` })
  if (p.timeout_seconds != null) tags.push({ key: 'timeout', text: t('org.policy_timeout_tag', { count: String(p.timeout_seconds) }) ?? `≤ ${p.timeout_seconds}s` })
  if (p.network_mode === 'deny') tags.push({ key: 'net', text: t('org.policy_network.deny') ?? 'network denied' })
  if (p.sandbox_mode === 'read_only') tags.push({ key: 'ro', text: t('org.policy_sandbox.read_only') ?? 'read-only' })
  if (p.approval_mode === 'tools') tags.push({ key: 'approval', text: t('org.policy_approval.tools') ?? 'approval required' })
  return tags
}

// Roles grantable on a unit (docs/org-structure.md §13); the backend's
// ParseRole also accepts the workspace alias "viewer" for reader.
const UNIT_ROLES = ['reader', 'writer', 'operator', 'admin'] as const

const ROLE_TAG_COLORS: Record<string, 'gray' | 'green' | 'blue' | 'purple'> = {
  reader: 'gray',
  writer: 'green',
  operator: 'blue',
  admin: 'purple',
}

/** True when an allowed-list permits everything: "*" (the store's default
 * for absent lists) or an empty list. */
export function allowsAnything(list?: string[] | null): boolean {
  return !list || list.length === 0 || list.includes('*')
}

/** Compact per-dimension summary of what an execution identity may touch
 * (docs/org-structure.md §47): "Agents: any" / "MCP: 2". */
export function identitySummaryTags(i: ExecutionIdentity, t: (k: string, v?: Record<string, string>) => string) {
  const anyText = t('org.policy_any') ?? 'any'
  const dimension = (list: string[] | undefined, label: string) => ({
    key: label,
    text: `${label}: ${allowsAnything(list) ? anyText : String(list?.length ?? 0)}`,
  })
  return [
    dimension(i.allowed_agents, t('org.identity_sum_agents') ?? 'Agents'),
    dimension(i.allowed_mcp, t('org.identity_sum_mcp') ?? 'MCP'),
    dimension(i.allowed_providers, t('org.identity_sum_providers') ?? 'Providers'),
    dimension(i.allowed_projects, t('org.identity_sum_projects') ?? 'Projects'),
    dimension(i.human_targets, t('org.identity_sum_targets') ?? 'Human targets'),
  ]
}

// Identity dialog form: allowed-lists are edited as comma-separated strings;
// the store turns empty lists back into "*" ("any") on save.
type IdentityForm = {
  name: string
  description: string
  org_unit_id: string
  allowed_agents: string
  allowed_mcp: string
  allowed_providers: string
  allowed_projects: string
  human_targets: string
}

const emptyIdentityForm = (): IdentityForm => ({
  name: '', description: '', org_unit_id: '',
  allowed_agents: '', allowed_mcp: '', allowed_providers: '',
  allowed_projects: '', human_targets: '',
})

// "*" is hidden from the form: an empty field means the same thing and
// round-trips back to ["*"] server-side. Mixing "*" with concrete entries is
// redundant (allowsAny treats "*" as allow-all), so dropping it is neutral.
const joinList = (list?: string[] | null) => (list ?? []).filter((x) => x !== '*').join(', ')

const identityFormOf = (i: ExecutionIdentity): IdentityForm => ({
  name: i.name,
  description: i.description ?? '',
  org_unit_id: i.org_unit_id ?? '',
  allowed_agents: joinList(i.allowed_agents),
  allowed_mcp: joinList(i.allowed_mcp),
  allowed_providers: joinList(i.allowed_providers),
  allowed_projects: joinList(i.allowed_projects),
  human_targets: joinList(i.human_targets),
})

function buildTree(units: OrgUnit[]): Node[] {
  const nodes = new Map<string, Node>(units.map((u) => [u.id, { unit: u, children: [] }]))
  const roots: Node[] = []
  for (const u of units) {
    const node = nodes.get(u.id)!
    const parent = u.parent_id ? nodes.get(u.parent_id) : undefined
    if (parent) parent.children.push(node)
    else roots.push(node)
  }
  return roots
}

export default function Org() {
  const t = useT()
  const [units, setUnits] = useState<OrgUnit[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [isAdmin, setIsAdmin] = useState(false)
  const [selected, setSelected] = useState<string | null>(null)
  const [resources, setResources] = useState<UnitResources | null>(null)
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set())
  const [resourceFilter, setResourceFilter] = useState('')

  // Create / rename dialog
  const [editing, setEditing] = useState<OrgUnit | null>(null)
  const [creatingParent, setCreatingParent] = useState<string | null>(null) // '' = new root
  const [formName, setFormName] = useState('')
  const [formKind, setFormKind] = useState<OrgUnitKind | string>('department')

  // Move dialog
  const [moving, setMoving] = useState<OrgUnit | null>(null)
  const [moveTarget, setMoveTarget] = useState('')

  // Delete confirm
  const [confirmDelete, setConfirmDelete] = useState<OrgUnit | null>(null)

  // Policies (docs/org-structure.md §24)
  const [policies, setPolicies] = useState<Policy[]>([])
  const [policyFilter, setPolicyFilter] = useState('')
  const [editingPolicy, setEditingPolicy] = useState<Policy | null>(null)
  const [policyDialogOpen, setPolicyDialogOpen] = useState(false)
  const [policyForm, setPolicyForm] = useState<PolicyForm>(emptyPolicyForm())
  const [deletingPolicy, setDeletingPolicy] = useState<Policy | null>(null)
  const [effective, setEffective] = useState<{ policy: Policy; sources: Policy[] } | null>(null)

  // Unit role grants (docs/org-structure.md §13): who is upgraded on this node
  const [unitRoles, setUnitRoles] = useState<OrgUnitRoleGrant[]>([])
  const [users, setUsers] = useState<User[]>([])
  const [grantUser, setGrantUser] = useState('')
  const [grantRoleKind, setGrantRoleKind] = useState<string>('writer')
  const [revoking, setRevoking] = useState<OrgUnitRoleGrant | null>(null)

  // Execution identities (docs/org-structure.md §20)
  const [identities, setIdentities] = useState<ExecutionIdentity[]>([])
  const [identityFilter, setIdentityFilter] = useState('')
  const [editingIdentity, setEditingIdentity] = useState<ExecutionIdentity | null>(null)
  const [identityDialogOpen, setIdentityDialogOpen] = useState(false)
  const [identityForm, setIdentityForm] = useState<IdentityForm>(emptyIdentityForm())
  const [deletingIdentity, setDeletingIdentity] = useState<ExecutionIdentity | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const data = await workspaceApi.listOrgUnits()
      setUnits(data.units ?? [])
      const pl = await workspaceApi.listPolicies()
      setPolicies(pl.policies ?? [])
      // Users feed the role-grant picker; identities the section below. Both
      // degrade to empty on older kernels instead of failing the page.
      const [us, ids] = await Promise.all([
        workspaceApi.listUsers().catch(() => ({ users: [] as User[] })),
        workspaceApi.listExecutionIdentities().catch(() => ({ identities: [] as ExecutionIdentity[] })),
      ])
      setUsers(us.users ?? [])
      setIdentities(ids.identities ?? [])
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }, [])

  useEffect(() => { void load() }, [load])
  useEffect(() => {
    void whoami().then((w) => setIsAdmin(w.role === 'admin')).catch(() => setIsAdmin(false))
  }, [])
  useEffect(() => {
    if (!selected) { setResources(null); setEffective(null); setUnitRoles([]); return }
    void workspaceApi.unitResources(selected)
      .then(setResources)
      .catch(() => setResources(null))
    void workspaceApi.effectivePolicy(selected)
      .then(setEffective)
      .catch(() => setEffective(null))
    void workspaceApi.listUnitRoles(selected)
      .then((r) => setUnitRoles(r.roles ?? []))
      .catch(() => setUnitRoles([]))
  }, [selected, units])

  const roots = buildTree(units)
  const flat = flattenUnits(units)
  const selectedUnit = units.find((u) => u.id === selected) ?? null

  const openCreate = (parentID: string) => {
    setEditing(null)
    setCreatingParent(parentID)
    setFormName('')
    setFormKind(parentID === '' ? 'organization' : 'department')
  }

  const openRename = (unit: OrgUnit) => {
    setCreatingParent(null)
    setEditing(unit)
    setFormName(unit.name)
    setFormKind(unit.kind)
  }

  const saveUnit = async () => {
    const name = formName.trim()
    if (!name) return
    setLoading(true); setError('')
    try {
      if (editing) {
        await workspaceApi.updateOrgUnit(editing.id, { name, kind: formKind })
      } else {
        await workspaceApi.createOrgUnit({ kind: formKind, name, parent_id: creatingParent || undefined })
      }
      setEditing(null); setCreatingParent(null); setFormName('')
      void load()
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }

  const applyMove = async () => {
    if (!moving || !moveTarget) return
    setLoading(true); setError('')
    try {
      await workspaceApi.updateOrgUnit(moving.id, {
        name: moving.name,
        kind: moving.kind,
        parent_id: moveTarget === '(root)' ? '' : moveTarget,
      })
      setMoving(null)
      void load()
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }

  const doDelete = async () => {
    if (!confirmDelete) return
    setLoading(true); setError('')
    try {
      await workspaceApi.deleteOrgUnit(confirmDelete.id)
      if (selected === confirmDelete.id) setSelected(null)
      setConfirmDelete(null)
      void load()
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }

  const openPolicyCreate = () => {
    setEditingPolicy(null)
    setPolicyForm({ ...emptyPolicyForm(), org_unit_id: selected ?? '' })
    setPolicyDialogOpen(true)
  }

  const openPolicyEdit = (p: Policy) => {
    setEditingPolicy(p)
    setPolicyForm(policyFormOf(p))
    setPolicyDialogOpen(true)
  }

  const savePolicy = async () => {
    const name = policyForm.name.trim()
    if (!name) return
    const payload = {
      name,
      org_unit_id: policyForm.org_unit_id || '',
      allowed_models: splitList(policyForm.allowed_models),
      allowed_mcp: splitList(policyForm.allowed_mcp),
      max_tokens: optionalNumber(policyForm.max_tokens) ?? null,
      max_budget_usd: optionalNumber(policyForm.max_budget_usd) ?? null,
      timeout_seconds: optionalNumber(policyForm.timeout_seconds) ?? null,
      network_mode: policyForm.network_mode,
      sandbox_mode: policyForm.sandbox_mode,
      approval_mode: policyForm.approval_mode,
      enabled: policyForm.enabled,
    }
    setLoading(true); setError('')
    try {
      if (editingPolicy) {
        await workspaceApi.updatePolicy(editingPolicy.id, payload)
      } else {
        await workspaceApi.createPolicy(payload)
      }
      setEditingPolicy(null)
      setPolicyDialogOpen(false)
      setPolicyForm(emptyPolicyForm())
      void load()
      if (selected) {
        void workspaceApi.effectivePolicy(selected).then(setEffective).catch(() => setEffective(null))
      }
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }

  const doDeletePolicy = async () => {
    if (!deletingPolicy) return
    setLoading(true); setError('')
    try {
      await workspaceApi.deletePolicy(deletingPolicy.id)
      setDeletingPolicy(null)
      void load()
      if (selected) {
        void workspaceApi.effectivePolicy(selected).then(setEffective).catch(() => setEffective(null))
      }
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }

  const userName = (id: string) => users.find((u) => u.id === id)?.name ?? id

  // PUT is upsert: granting a user who already holds a role on the unit
  // replaces their role.
  const grantRole = async () => {
    if (!selected || !grantUser) return
    setLoading(true); setError('')
    try {
      await workspaceApi.setUnitRole(selected, grantUser, grantRoleKind)
      setGrantUser('')
      const r = await workspaceApi.listUnitRoles(selected)
      setUnitRoles(r.roles ?? [])
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }

  const doRevokeRole = async () => {
    if (!revoking || !selected) return
    setLoading(true); setError('')
    try {
      await workspaceApi.removeUnitRole(selected, revoking.user_id)
      setRevoking(null)
      const r = await workspaceApi.listUnitRoles(selected)
      setUnitRoles(r.roles ?? [])
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }

  const openIdentityCreate = () => {
    setEditingIdentity(null)
    setIdentityForm({ ...emptyIdentityForm(), org_unit_id: selected ?? '' })
    setIdentityDialogOpen(true)
  }

  const openIdentityEdit = (i: ExecutionIdentity) => {
    setEditingIdentity(i)
    setIdentityForm(identityFormOf(i))
    setIdentityDialogOpen(true)
  }

  const saveIdentity = async () => {
    const name = identityForm.name.trim()
    if (!name) return
    // Empty lists are sent as []: the store normalizes them to "*" (any),
    // mirroring the form placeholders.
    const payload = {
      name,
      description: identityForm.description.trim(),
      org_unit_id: identityForm.org_unit_id,
      allowed_agents: splitList(identityForm.allowed_agents),
      allowed_mcp: splitList(identityForm.allowed_mcp),
      allowed_providers: splitList(identityForm.allowed_providers),
      allowed_projects: splitList(identityForm.allowed_projects),
      human_targets: splitList(identityForm.human_targets),
    }
    setLoading(true); setError('')
    try {
      if (editingIdentity) {
        await workspaceApi.updateExecutionIdentity(editingIdentity.id, payload)
      } else {
        await workspaceApi.createExecutionIdentity(payload)
      }
      setEditingIdentity(null)
      setIdentityDialogOpen(false)
      setIdentityForm(emptyIdentityForm())
      void load()
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }

  const doDeleteIdentity = async () => {
    if (!deletingIdentity) return
    setLoading(true); setError('')
    try {
      await workspaceApi.deleteExecutionIdentity(deletingIdentity.id)
      setDeletingIdentity(null)
      void load()
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }

  const matches = (label: string) => matchesFilter(resourceFilter, label)

  const renderNode = (node: Node, depth: number): React.ReactNode => {
    const { unit } = node
    const isCollapsed = collapsed.has(unit.id)
    return (
      <div key={unit.id}>
        <div
          className={`org-tree-row ${selected === unit.id ? 'selected' : ''}`}
          style={{ paddingLeft: `${0.35 + depth * 1.1}rem` }}
          onClick={() => setSelected(unit.id)}
        >
          {node.children.length > 0 ? (
            <span
              className="org-tree-toggle"
              onClick={(e) => {
                e.stopPropagation()
                const next = new Set(collapsed)
                if (next.has(unit.id)) next.delete(unit.id)
                else next.add(unit.id)
                setCollapsed(next)
              }}
            >
              {isCollapsed ? <ChevronRight size={14} /> : <ChevronDown size={14} />}
            </span>
          ) : (
            <span className="org-tree-toggle" style={{ visibility: 'hidden' }}><ChevronRight size={14} /></span>
          )}
          <span className="org-tree-icon">
            {unit.kind === 'organization' ? <Building size={14} /> : <Enterprise size={14} />}
          </span>
          <span className="org-tree-name">{unit.name}</span>
          <Tag type={KIND_TAGS[unit.kind] ?? 'gray'} size="sm">{kindLabel(t, unit.kind)}</Tag>
          {isAdmin && (
            <span className="org-tree-actions" onClick={(e) => e.stopPropagation()}>
              <Button size="sm" kind="ghost" hasIconOnly renderIcon={Add} iconDescription={t('org.add_child') ?? 'Add sub-unit'} onClick={() => openCreate(unit.id)} />
              <Button size="sm" kind="ghost" hasIconOnly renderIcon={Edit} iconDescription={t('action.edit') ?? 'Rename'} onClick={() => openRename(unit)} />
              <Button size="sm" kind="ghost" hasIconOnly renderIcon={TrashCan} iconDescription={t('action.delete') ?? 'Delete'} onClick={() => setConfirmDelete(unit)} />
            </span>
          )}
        </div>
        {!isCollapsed && node.children.map((child) => renderNode(child, depth + 1))}
      </div>
    )
  }

  // Empty state: first-run wizard — create the root organization.
  if (!loading && units.length === 0) {
    return (
      <div style={{ padding: '2rem', maxWidth: '460px', margin: '0 auto', textAlign: 'center' }}>
        <Building size={40} style={{ color: 'var(--tm-text-3)', marginBottom: '0.75rem' }} />
        <Heading style={{ marginBottom: '0.5rem' }}>{t('org.empty_title') ?? 'Set up your organization'}</Heading>
        <p style={{ color: 'var(--tm-text-2)', fontSize: '0.875rem', marginBottom: '1.25rem' }}>
          {t('org.empty_hint') ?? 'Create the root unit of your company. Departments and teams can be added inside it later; resources and users are then scoped by unit.'}
        </p>
        <div style={{ textAlign: 'left' }}>
          <TextInput
            id="org-root-name"
            labelText={t('org.name_label') ?? 'Organization name'}
            value={formName}
            onChange={(e: React.ChangeEvent<HTMLInputElement>) => setFormName(e.target.value)}
            placeholder="Acme Corp"
            autoFocus
          />
          <div className="form-actions" style={{ marginTop: '0.75rem' }}>
            <Button onClick={() => { setFormKind('organization'); void saveUnit() }} disabled={!formName.trim() || !isAdmin}>
              {t('org.create_root') ?? 'Create organization'}
            </Button>
          </div>
          {!isAdmin && (
            <p style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem', marginTop: '0.5rem' }}>
              {t('org.admin_required') ?? 'Only administrators can change the org structure.'}
            </p>
          )}
        </div>
        {error && <InlineNotification kind="error" title={t('action.error') ?? 'Error'} subtitle={error} onClose={() => setError('')} lowContrast style={{ marginTop: '0.75rem' }} />}
        {loading && <Loading withOverlay={false} />}
      </div>
    )
  }

  return (
    <div style={{ padding: '1rem' }}>
      {error && <InlineNotification kind="error" title={t('action.error') ?? 'Error'} subtitle={error} onClose={() => setError('')} lowContrast style={{ marginBottom: '1rem' }} />}

      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '0.75rem' }}>
        <Heading>{t('org.title') ?? 'Organization'}</Heading>
        {isAdmin && units.length > 0 && (
          <Button size="sm" renderIcon={Add} onClick={() => openCreate('')}>{t('org.add_root') ?? 'Add organization'}</Button>
        )}
      </div>

      <div style={{ display: 'grid', gridTemplateColumns: 'minmax(280px, 1fr) minmax(300px, 1.4fr)', gap: '1rem', alignItems: 'start' }}>
        {/* Tree */}
        <div className="org-tree" style={{ border: '1px solid var(--tm-border)', borderRadius: '8px', padding: '0.5rem', background: 'var(--tm-elevated)' }}>
          {roots.map((node) => renderNode(node, 0))}
        </div>

        {/* Unit inspection */}
        <div className="org-inspector">
          {!selectedUnit ? (
            <div style={{ color: 'var(--tm-text-3)', fontSize: '0.875rem', padding: '1.5rem 0.5rem', textAlign: 'center' }}>
              {t('org.select_hint') ?? 'Select a unit to see its resources, users and projects.'}
            </div>
          ) : (
            <div>
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '0.5rem' }}>
                <div>
                  <strong style={{ fontSize: '1rem' }}>{selectedUnit.name}</strong>
                  <div style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem', marginTop: '0.15rem' }}>
                    {kindLabel(t, selectedUnit.kind)} · {selectedUnit.id}
                    {isAdmin && (
                      <>
                        {' · '}
                        <span
                          style={{ color: 'var(--tm-teal)', cursor: 'pointer', textDecoration: 'underline' }}
                          onClick={() => { setMoving(selectedUnit); setMoveTarget(selectedUnit.parent_id ?? '(root)') }}
                        >
                          {t('org.move') ?? 'Move'}
                        </span>
                      </>
                    )}
                  </div>
                </div>
              </div>
              <div style={{ maxWidth: '300px', marginBottom: '0.75rem' }}>
                <ListFilter value={resourceFilter} onChange={setResourceFilter} placeholder={t('common.filter') ?? 'Filter…'} />
              </div>
              {resources ? (
                <div style={{ display: 'grid', gap: '0.75rem' }}>
                  {([
                    ['agents', t('nav.agents') ?? 'Agents', resources.agents],
                    ['skills', t('nav.skills') ?? 'Skills', resources.skills],
                    ['mcp', t('nav.mcp') ?? 'MCP servers', resources.mcp_servers],
                    ['providers', t('nav.providers') ?? 'Providers', resources.providers],
                    ['users', t('nav.users') ?? 'Users', resources.users],
                    ['projects', t('nav.projects') ?? 'Projects', resources.projects],
                  ] as const).map(([key, label, list]) => {
                    const visible = (list ?? []).filter((x) => matches(x.name) || matches(x.id))
                    return (
                      <div key={key}>
                        <div className="org-section-title">{label} <span className="org-section-count">{visible.length}</span></div>
                        {visible.length === 0 ? (
                          <div style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem', paddingLeft: '0.25rem' }}>{t('org.no_resources') ?? 'Nothing bound to this unit'}</div>
                        ) : (
                          <div style={{ display: 'flex', flexWrap: 'wrap', gap: '0.25rem', paddingLeft: '0.25rem' }}>
                            {visible.map((x) => <Tag key={x.id} type="gray" size="sm">{x.name || x.id}</Tag>)}
                          </div>
                        )}
                      </div>
                    )
                  })}
                </div>
              ) : (
                <Loading withOverlay={false} />
              )}

              {/* Roles granted on this unit (docs/org-structure.md §13): each
                  grant upgrades the user's effective role on the unit and its
                  subtree; the installation role keeps applying everywhere. */}
              <div style={{ marginTop: '1rem', borderTop: '1px solid var(--tm-border)', paddingTop: '0.75rem' }}>
                <div className="org-section-title">{t('org.roles_title') ?? 'Roles on unit'} <span className="org-section-count">{unitRoles.length}</span></div>
                <p style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem', margin: '0.25rem 0 0.5rem' }}>
                  {t('org.roles_hint') ?? 'A grant upgrades the effective role on this unit and its whole subtree; the installation role still applies everywhere.'}
                </p>
                {unitRoles.length === 0 ? (
                  <div style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem', paddingLeft: '0.25rem' }}>{t('org.roles_empty') ?? 'No extra role grants on this unit'}</div>
                ) : (
                  <div style={{ display: 'grid', gap: '0.35rem', marginBottom: isAdmin ? '0.75rem' : 0 }}>
                    {unitRoles.map((g) => {
                      const grantee = users.find((u) => u.id === g.user_id)
                      return (
                        <div key={g.user_id} style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: '0.5rem', border: '1px solid var(--tm-border)', borderRadius: '8px', padding: '0.35rem 0.6rem' }}>
                          <div style={{ display: 'flex', alignItems: 'center', gap: '0.4rem', flexWrap: 'wrap', minWidth: 0 }}>
                            <span style={{ fontSize: '0.875rem' }}>{grantee?.name ?? g.user_id}</span>
                            <Tag type={ROLE_TAG_COLORS[g.role] ?? 'gray'} size="sm">{g.role}</Tag>
                            {grantee?.role === 'admin' && (
                              <Tag type="purple" size="sm" title={t('org.roles_installation_hint') ?? 'Already an installation-wide admin — this grant adds nothing.'}>
                                {t('org.roles_installation_admin') ?? 'admin (installation)'}
                              </Tag>
                            )}
                            {g.granted_by && (
                              <span style={{ color: 'var(--tm-text-3)', fontSize: '0.7rem' }} title={g.granted_at}>
                                {t('org.roles_granted_by', { name: g.granted_by }) ?? `by ${g.granted_by}`}
                              </span>
                            )}
                          </div>
                          {isAdmin && (
                            <Button size="sm" kind="ghost" hasIconOnly renderIcon={TrashCan} iconDescription={t('org.roles_revoke') ?? 'Revoke'} onClick={() => setRevoking(g)} />
                          )}
                        </div>
                      )
                    })}
                  </div>
                )}
                {isAdmin && (
                  <div style={{ display: 'grid', gridTemplateColumns: '1.4fr 1fr auto', gap: '0.5rem', alignItems: 'end' }}>
                    <Select
                      id="org-role-user"
                      labelText={t('org.roles_user_label') ?? 'User'}
                      value={grantUser}
                      onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setGrantUser(e.target.value)}
                    >
                      <SelectItem value="" text={t('org.roles_pick_user') ?? '— pick a user —'} />
                      {users.map((u) => <SelectItem key={u.id} value={u.id} text={u.name || u.id} />)}
                    </Select>
                    <Select
                      id="org-role-kind"
                      labelText={t('org.roles_role_label') ?? 'Role'}
                      value={grantRoleKind}
                      onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setGrantRoleKind(e.target.value)}
                    >
                      {UNIT_ROLES.map((r) => <SelectItem key={r} value={r} text={t(`org.role.${r}`) ?? r} />)}
                    </Select>
                    <Button size="sm" renderIcon={Add} onClick={() => void grantRole()} disabled={!grantUser}>
                      {t('org.roles_grant') ?? 'Grant'}
                    </Button>
                  </div>
                )}
              </div>

              {effective && (
                <div style={{ marginTop: '1rem', borderTop: '1px solid var(--tm-border)', paddingTop: '0.75rem' }}>
                  <div className="org-section-title">{t('org.policy_effective_title') ?? 'Effective policy'}</div>
                  <p style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem', margin: '0.25rem 0 0.5rem' }}>
                    {t('org.policy_effective_hint') ?? 'What applies to runs at this unit: installation-wide rows plus everything inherited from ancestors, merged restrictively.'}
                  </p>
                  {(effective.sources ?? []).length === 0 ? (
                    <div style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem' }}>{t('org.policy_effective_none') ?? 'No policy restricts runs at this unit.'}</div>
                  ) : (
                    <>
                      <div style={{ display: 'flex', flexWrap: 'wrap', gap: '0.25rem', marginBottom: '0.5rem' }}>
                        {policySummaryTags(effective.policy, t).map((tag) => (
                          <Tag key={tag.key} type="purple" size="sm">{tag.text}</Tag>
                        ))}
                        {policySummaryTags(effective.policy, t).length === 0 && (
                          <span style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem' }}>
                            {t('org.policy_effective_none') ?? 'No policy restricts runs at this unit.'}
                          </span>
                        )}
                      </div>
                      <div style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem', marginBottom: '0.25rem' }}>{t('org.policy_sources') ?? 'Contributing policies'}:</div>
                      <div style={{ display: 'flex', flexWrap: 'wrap', gap: '0.25rem' }}>
                        {(effective.sources ?? []).map((s) => (
                          <Tag key={s.id} type="gray" size="sm">{s.name}</Tag>
                        ))}
                      </div>
                    </>
                  )}
                </div>
              )}
              <p style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem', marginTop: '1rem' }}>
                {t('org.subtree_hint') ?? 'Everything bound to this unit is also visible in its sub-units.'}
              </p>
            </div>
          )}
        </div>
      </div>

      {/* Policies */}
      <div style={{ marginTop: '1.25rem', border: '1px solid var(--tm-border)', borderRadius: '8px', padding: '1rem', background: 'var(--tm-elevated)' }}>
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: '0.75rem', marginBottom: '0.5rem' }}>
          <Heading style={{ fontSize: '1rem' }}>{t('org.policies_title') ?? 'Policies'}</Heading>
          <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'center' }}>
            <div style={{ width: '220px' }}>
              <ListFilter value={policyFilter} onChange={setPolicyFilter} placeholder={t('common.filter') ?? 'Filter…'} />
            </div>
            {isAdmin && (
              <Button size="sm" renderIcon={Add} onClick={openPolicyCreate}>{t('org.policy_create') ?? 'Create policy'}</Button>
            )}
          </div>
        </div>
        <p style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem', margin: '0 0 0.75rem' }}>
          {t('org.policies_hint') ?? 'Policies constrain runs for a unit and everything below it; multiple policies merge restrictively.'}
        </p>
        {policies.length === 0 ? (
          <div style={{ color: 'var(--tm-text-3)', fontSize: '0.875rem', padding: '1rem 0.5rem', textAlign: 'center' }}>
            <div>{t('org.policy_none') ?? 'No policies yet'}</div>
            <div style={{ fontSize: '0.75rem', marginTop: '0.25rem' }}>{t('org.policy_none_hint') ?? 'Create a policy to constrain runs of a unit and its sub-units.'}</div>
          </div>
        ) : (
          <div style={{ display: 'grid', gap: '0.5rem' }}>
            {policies
              .filter((p) => matchesFilter(policyFilter, p.name) || matchesFilter(policyFilter, p.org_unit_id ?? ''))
              .map((p) => {
                const unit = units.find((u) => u.id === p.org_unit_id)
                return (
                  <div key={p.id} style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: '0.75rem', border: '1px solid var(--tm-border)', borderRadius: '8px', padding: '0.5rem 0.75rem' }}>
                    <div style={{ minWidth: 0 }}>
                      <div style={{ display: 'flex', alignItems: 'center', gap: '0.4rem', flexWrap: 'wrap' }}>
                        <strong style={{ fontSize: '0.875rem' }}>{p.name}</strong>
                        {!p.enabled && <Tag type="red" size="sm">disabled</Tag>}
                        {unit ? <Tag type="blue" size="sm">{unit.name}</Tag> : <Tag type="cyan" size="sm">{t('org.policy_scope_installation') ?? 'Whole installation'}</Tag>}
                      </div>
                      <div style={{ display: 'flex', flexWrap: 'wrap', gap: '0.25rem', marginTop: '0.35rem' }}>
                        {policySummaryTags(p, t).map((tag) => (
                          <Tag key={tag.key} type="gray" size="sm">{tag.text}</Tag>
                        ))}
                      </div>
                    </div>
                    {isAdmin && (
                      <div style={{ display: 'flex', gap: '0.25rem', flexShrink: 0 }}>
                        <Button size="sm" kind="ghost" renderIcon={Edit} iconDescription={t('action.edit') ?? 'Edit'} hasIconOnly onClick={() => openPolicyEdit(p)} />
                        <Button size="sm" kind="ghost" renderIcon={TrashCan} iconDescription={t('action.delete') ?? 'Delete'} hasIconOnly onClick={() => setDeletingPolicy(p)} />
                      </div>
                    )}
                  </div>
                )
              })}
          </div>
        )}
      </div>

      {/* Execution identities (docs/org-structure.md §20, §47): the security
          contexts trigger-fired runs act under. Mutations are admin-gated
          server-side; the UI mirrors that by hiding the controls. */}
      <div style={{ marginTop: '1.25rem', border: '1px solid var(--tm-border)', borderRadius: '8px', padding: '1rem', background: 'var(--tm-elevated)' }}>
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: '0.75rem', marginBottom: '0.5rem' }}>
          <Heading style={{ fontSize: '1rem' }}>{t('org.identities_title') ?? 'Execution identities'}</Heading>
          <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'center' }}>
            <div style={{ width: '220px' }}>
              <ListFilter value={identityFilter} onChange={setIdentityFilter} placeholder={t('common.filter') ?? 'Filter…'} />
            </div>
            {isAdmin && (
              <Button size="sm" renderIcon={Add} onClick={openIdentityCreate}>{t('org.identity_create') ?? 'Create identity'}</Button>
            )}
          </div>
        </div>
        <p style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem', margin: '0 0 0.75rem' }}>
          {t('org.identities_hint') ?? 'Security contexts for automated runs: which agents, MCP servers, providers, projects and human question recipients a trigger-fired run may touch.'}
        </p>
        {identities.length === 0 ? (
          <div style={{ color: 'var(--tm-text-3)', fontSize: '0.875rem', padding: '1rem 0.5rem', textAlign: 'center' }}>
            <div>{t('org.identity_none') ?? 'No execution identities yet'}</div>
            <div style={{ fontSize: '0.75rem', marginTop: '0.25rem' }}>{t('org.identity_none_hint') ?? 'Create one so trigger-fired runs act under their own permissions instead of the creator\'s.'}</div>
          </div>
        ) : (
          <div style={{ display: 'grid', gap: '0.5rem' }}>
            {identities
              .filter((i) =>
                matchesFilter(identityFilter, i.name, i.id, i.description) ||
                matchesFilter(identityFilter, units.find((u) => u.id === i.org_unit_id)?.name))
              .map((i) => {
                const unit = units.find((u) => u.id === i.org_unit_id)
                return (
                  <div key={i.id} style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: '0.75rem', border: '1px solid var(--tm-border)', borderRadius: '8px', padding: '0.5rem 0.75rem' }}>
                    <div style={{ minWidth: 0 }}>
                      <div style={{ display: 'flex', alignItems: 'center', gap: '0.4rem', flexWrap: 'wrap' }}>
                        <strong style={{ fontSize: '0.875rem' }}>{i.name}</strong>
                        <code style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)' }}>{i.id}</code>
                        {unit
                          ? <Tag type="blue" size="sm">{unit.name}</Tag>
                          : <Tag type="cyan" size="sm">{t('org.policy_scope_installation') ?? 'Whole installation'}</Tag>}
                      </div>
                      {i.description && (
                        <div style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem', marginTop: '0.15rem' }}>{i.description}</div>
                      )}
                      <div style={{ display: 'flex', flexWrap: 'wrap', gap: '0.25rem', marginTop: '0.35rem' }}>
                        {identitySummaryTags(i, t).map((tag) => (
                          <Tag key={tag.key} type="gray" size="sm">{tag.text}</Tag>
                        ))}
                      </div>
                    </div>
                    {isAdmin && (
                      <div style={{ display: 'flex', gap: '0.25rem', flexShrink: 0 }}>
                        <Button size="sm" kind="ghost" renderIcon={Edit} iconDescription={t('action.edit') ?? 'Edit'} hasIconOnly onClick={() => openIdentityEdit(i)} />
                        <Button size="sm" kind="ghost" renderIcon={TrashCan} iconDescription={t('action.delete') ?? 'Delete'} hasIconOnly onClick={() => setDeletingIdentity(i)} />
                      </div>
                    )}
                  </div>
                )
              })}
          </div>
        )}
      </div>

      {/* Create / rename dialog */}
      {(editing || creatingParent !== null) && (
        <AppModal onClose={() => { setEditing(null); setCreatingParent(null) }} panelStyle={{ width: '420px' }}>
            <Heading style={{ fontSize: '1.1rem', marginBottom: '0.75rem' }}>
              {editing
                ? (t('org.rename') ?? 'Rename unit')
                : creatingParent === ''
                  ? (t('org.add_root') ?? 'Add organization')
                  : (t('org.add_unit_in', { name: units.find((u) => u.id === creatingParent)?.name ?? creatingParent ?? '' }) ?? `Add unit in ${units.find((u) => u.id === creatingParent)?.name ?? creatingParent ?? ''}`)}
            </Heading>
            <div style={{ display: 'grid', gap: '0.75rem' }}>
              <TextInput
                id="org-unit-name"
                labelText={t('org.name_label') ?? 'Name'}
                value={formName}
                onChange={(e: React.ChangeEvent<HTMLInputElement>) => setFormName(e.target.value)}
                placeholder="Platform"
                autoFocus
              />
              {!editing && (
                <Select
                  id="org-unit-kind"
                  labelText={t('org.kind_label') ?? 'Kind'}
                  value={formKind}
                  onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setFormKind(e.target.value)}
                >
                  <SelectItem value="organization" text={kindLabel(t, 'organization')} />
                  <SelectItem value="department" text={kindLabel(t, 'department')} />
                  <SelectItem value="team" text={kindLabel(t, 'team')} />
                </Select>
              )}
              <div className="form-actions">
                <Button kind="secondary" onClick={() => { setEditing(null); setCreatingParent(null) }}>{t('action.cancel') ?? 'Cancel'}</Button>
                <Button onClick={() => void saveUnit()} disabled={!formName.trim()}>{editing ? (t('action.save') ?? 'Save') : (t('action.create') ?? 'Create')}</Button>
              </div>
            </div>
        </AppModal>
      )}

      {/* Move dialog */}
      {moving && (
        <AppModal onClose={() => setMoving(null)} panelStyle={{ width: '460px' }}>
            <Heading style={{ fontSize: '1.1rem', marginBottom: '0.75rem' }}>{t('org.move') ?? 'Move unit'}</Heading>
            <p style={{ color: 'var(--tm-text-2)', fontSize: '0.875rem', margin: '0 0 0.75rem' }}>
              {t('org.move_confirm', { name: moving.name }) ?? `Moving "${moving.name}" changes visibility for the whole subtree. Users of the moved branch will see resources of the new parent chain.`}
            </p>
            <Select
              id="org-move-parent"
              labelText={t('org.parent_label') ?? 'New parent'}
              value={moveTarget}
              onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setMoveTarget(e.target.value)}
            >
              <SelectItem value="(root)" text={t('org.root_level') ?? '— root level —'} />
              {flat.filter(({ unit }) => unit.id !== moving.id).map(({ unit, depth }) => (
                <SelectItem key={unit.id} value={unit.id} text={`${'　'.repeat(depth)}${unit.name}`} />
              ))}
            </Select>
            <div className="form-actions" style={{ marginTop: '0.75rem' }}>
              <Button kind="secondary" onClick={() => setMoving(null)}>{t('action.cancel') ?? 'Cancel'}</Button>
              <Button onClick={() => void applyMove()}>{t('action.confirm') ?? 'Move'}</Button>
            </div>
        </AppModal>
      )}

      {/* Delete confirm */}
      {confirmDelete && (
        <AppModal onClose={() => setConfirmDelete(null)} panelStyle={{ width: '460px' }}>
            <Heading>{t('org.delete_confirm', { name: confirmDelete.name }) ?? `Delete "${confirmDelete.name}"?`}</Heading>
            <p style={{ color: 'var(--tm-text-3)', margin: '0.75rem 0' }}>
              {t('org.delete_warning') ?? 'Only an empty unit can be deleted: move its children, unbind resources and reassign users first.'}
            </p>
            <div className="form-actions">
              <Button kind="secondary" onClick={() => setConfirmDelete(null)}>{t('action.cancel') ?? 'Cancel'}</Button>
              <Button kind="danger" onClick={() => void doDelete()}>{t('action.delete') ?? 'Delete'}</Button>
            </div>
        </AppModal>
      )}

      {/* Policy create / edit dialog */}
      {(editingPolicy || policyDialogOpen) && isAdmin && (
        <AppModal onClose={() => { setEditingPolicy(null); setPolicyDialogOpen(false); setPolicyForm(emptyPolicyForm()) }} panelStyle={{ width: '520px' }}>
            <Heading style={{ fontSize: '1.1rem', marginBottom: '0.75rem' }}>
              {editingPolicy ? (t('org.policy_edit') ?? 'Edit policy') : (t('org.policy_create') ?? 'Create policy')}
            </Heading>
            <div style={{ display: 'grid', gap: '0.75rem' }}>
              <TextInput
                id="policy-name"
                labelText={t('org.name_label') ?? 'Name'}
                value={policyForm.name}
                onChange={(e: React.ChangeEvent<HTMLInputElement>) => setPolicyForm({ ...policyForm, name: e.target.value })}
                placeholder="Tier-1 production limits"
                autoFocus
              />
              <Select
                id="policy-scope"
                labelText={t('org.policy_scope_label') ?? 'Applies to'}
                value={policyForm.org_unit_id}
                onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setPolicyForm({ ...policyForm, org_unit_id: e.target.value })}
              >
                <SelectItem value="" text={t('org.policy_scope_installation') ?? 'Whole installation'} />
                {flat.map(({ unit, depth }) => (
                  <SelectItem key={unit.id} value={unit.id} text={`${'　'.repeat(depth)}${unit.name}`} />
                ))}
              </Select>
              <TextInput
                id="policy-models"
                labelText={t('org.policy_models_label') ?? 'Allowed models'}
                value={policyForm.allowed_models}
                onChange={(e: React.ChangeEvent<HTMLInputElement>) => setPolicyForm({ ...policyForm, allowed_models: e.target.value })}
                placeholder={t('org.policy_models_placeholder') ?? 'model-a, model-b — empty = any'}
              />
              <TextInput
                id="policy-mcp"
                labelText={t('org.policy_mcp_label') ?? 'Allowed MCP servers'}
                value={policyForm.allowed_mcp}
                onChange={(e: React.ChangeEvent<HTMLInputElement>) => setPolicyForm({ ...policyForm, allowed_mcp: e.target.value })}
                placeholder={t('org.policy_mcp_placeholder') ?? 'server-a, server-b — empty = any'}
              />
              <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr 1fr', gap: '0.5rem' }}>
                <TextInput
                  id="policy-tokens"
                  labelText={t('org.policy_max_tokens_label') ?? 'Max tokens per run'}
                  value={policyForm.max_tokens}
                  onChange={(e: React.ChangeEvent<HTMLInputElement>) => setPolicyForm({ ...policyForm, max_tokens: e.target.value })}
                  placeholder="200000"
                />
                <TextInput
                  id="policy-budget"
                  labelText={t('org.policy_budget_label') ?? 'Max cost, USD'}
                  value={policyForm.max_budget_usd}
                  onChange={(e: React.ChangeEvent<HTMLInputElement>) => setPolicyForm({ ...policyForm, max_budget_usd: e.target.value })}
                  placeholder="5"
                />
                <TextInput
                  id="policy-timeout"
                  labelText={t('org.policy_timeout_label') ?? 'Max time, s'}
                  value={policyForm.timeout_seconds}
                  onChange={(e: React.ChangeEvent<HTMLInputElement>) => setPolicyForm({ ...policyForm, timeout_seconds: e.target.value })}
                  placeholder="1800"
                />
              </div>
              <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr 1fr', gap: '0.5rem' }}>
                <Select
                  id="policy-network"
                  labelText={t('org.policy_network_label') ?? 'Network'}
                  value={policyForm.network_mode}
                  onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setPolicyForm({ ...policyForm, network_mode: e.target.value })}
                >
                  <SelectItem value="" text={t('org.policy_network.default') ?? 'not restricted'} />
                  <SelectItem value="deny" text={t('org.policy_network.deny') ?? 'denied'} />
                </Select>
                <Select
                  id="policy-sandbox"
                  labelText={t('org.policy_sandbox_label') ?? 'Sandbox'}
                  value={policyForm.sandbox_mode}
                  onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setPolicyForm({ ...policyForm, sandbox_mode: e.target.value })}
                >
                  <SelectItem value="" text={t('org.policy_sandbox.default') ?? 'standard'} />
                  <SelectItem value="read_only" text={t('org.policy_sandbox.read_only') ?? 'read-only'} />
                </Select>
                <Select
                  id="policy-approval"
                  labelText={t('org.policy_approval_label') ?? 'Consequential tools'}
                  value={policyForm.approval_mode}
                  onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setPolicyForm({ ...policyForm, approval_mode: e.target.value })}
                >
                  <SelectItem value="" text={t('org.policy_approval.default') ?? 'no approval'} />
                  <SelectItem value="tools" text={t('org.policy_approval.tools') ?? 'require approval'} />
                </Select>
              </div>
              <label style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', fontSize: '0.875rem', color: 'var(--tm-text-1)' }}>
                <input
                  type="checkbox"
                  checked={policyForm.enabled}
                  onChange={(e) => setPolicyForm({ ...policyForm, enabled: e.target.checked })}
                />
                {t('org.policy_enabled_label') ?? 'Policy enabled'}
              </label>
              <div className="form-actions">
                <Button kind="secondary" onClick={() => { setEditingPolicy(null); setPolicyDialogOpen(false); setPolicyForm(emptyPolicyForm()) }}>{t('action.cancel') ?? 'Cancel'}</Button>
                <Button onClick={() => void savePolicy()} disabled={!policyForm.name.trim()}>{editingPolicy ? (t('action.save') ?? 'Save') : (t('action.create') ?? 'Create')}</Button>
              </div>
            </div>
        </AppModal>
      )}

      {/* Policy delete confirm */}
      {deletingPolicy && (
        <AppModal onClose={() => setDeletingPolicy(null)} panelStyle={{ width: '460px' }}>
            <Heading>{t('org.policy_delete_confirm', { name: deletingPolicy.name }) ?? `Delete policy "${deletingPolicy.name}"?`}</Heading>
            <div className="form-actions" style={{ marginTop: '0.75rem' }}>
              <Button kind="secondary" onClick={() => setDeletingPolicy(null)}>{t('action.cancel') ?? 'Cancel'}</Button>
              <Button kind="danger" onClick={() => void doDeletePolicy()}>{t('action.delete') ?? 'Delete'}</Button>
            </div>
        </AppModal>
      )}

      {/* Role revoke confirm */}
      {revoking && selectedUnit && (
        <AppModal onClose={() => setRevoking(null)} panelStyle={{ width: '460px' }}>
            <Heading>{t('org.roles_delete_confirm', { role: revoking.role, name: userName(revoking.user_id) }) ?? `Revoke "${revoking.role}" from ${userName(revoking.user_id)}?`}</Heading>
            <p style={{ color: 'var(--tm-text-3)', margin: '0.75rem 0' }}>
              {t('org.roles_delete_warning') ?? 'The user keeps their installation role and any grants on ancestor units.'}
            </p>
            <div className="form-actions">
              <Button kind="secondary" onClick={() => setRevoking(null)}>{t('action.cancel') ?? 'Cancel'}</Button>
              <Button kind="danger" onClick={() => void doRevokeRole()}>{t('org.roles_revoke') ?? 'Revoke'}</Button>
            </div>
        </AppModal>
      )}

      {/* Identity create / edit dialog */}
      {(editingIdentity || identityDialogOpen) && isAdmin && (
        <AppModal onClose={() => { setEditingIdentity(null); setIdentityDialogOpen(false); setIdentityForm(emptyIdentityForm()) }} panelStyle={{ width: '560px' }}>
            <Heading style={{ fontSize: '1.1rem', marginBottom: '0.75rem' }}>
              {editingIdentity ? (t('org.identity_edit') ?? 'Edit identity') : (t('org.identity_create') ?? 'Create identity')}
            </Heading>
            <div style={{ display: 'grid', gap: '0.75rem' }}>
              <TextInput
                id="identity-name"
                labelText={t('org.name_label') ?? 'Name'}
                value={identityForm.name}
                onChange={(e: React.ChangeEvent<HTMLInputElement>) => setIdentityForm({ ...identityForm, name: e.target.value })}
                placeholder="PR Reviewer"
                autoFocus
              />
              <TextInput
                id="identity-description"
                labelText={t('org.identity_description_label') ?? 'Description'}
                value={identityForm.description}
                onChange={(e: React.ChangeEvent<HTMLInputElement>) => setIdentityForm({ ...identityForm, description: e.target.value })}
                placeholder={t('org.identity_description_placeholder') ?? 'What this identity is for'}
              />
              <Select
                id="identity-scope"
                labelText={t('org.identity_unit_label') ?? 'Org unit'}
                value={identityForm.org_unit_id}
                onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setIdentityForm({ ...identityForm, org_unit_id: e.target.value })}
                helperText={t('org.identity_unit_helper') ?? 'The identity sees resources visible in this unit and its subtree.'}
              >
                <SelectItem value="" text={t('org.policy_scope_installation') ?? 'Whole installation'} />
                {flat.map(({ unit, depth }) => (
                  <SelectItem key={unit.id} value={unit.id} text={`${'　'.repeat(depth)}${unit.name}`} />
                ))}
              </Select>
              <TextInput
                id="identity-agents"
                labelText={t('org.identity_agents_label') ?? 'Allowed agents'}
                value={identityForm.allowed_agents}
                onChange={(e: React.ChangeEvent<HTMLInputElement>) => setIdentityForm({ ...identityForm, allowed_agents: e.target.value })}
                placeholder={t('org.identity_agents_placeholder') ?? 'agent-a, agent-b — empty = any'}
              />
              <TextInput
                id="identity-mcp"
                labelText={t('org.identity_mcp_label') ?? 'Allowed MCP servers'}
                value={identityForm.allowed_mcp}
                onChange={(e: React.ChangeEvent<HTMLInputElement>) => setIdentityForm({ ...identityForm, allowed_mcp: e.target.value })}
                placeholder={t('org.identity_mcp_placeholder') ?? 'server-a, server-b — empty = any'}
              />
              <TextInput
                id="identity-providers"
                labelText={t('org.identity_providers_label') ?? 'Allowed providers'}
                value={identityForm.allowed_providers}
                onChange={(e: React.ChangeEvent<HTMLInputElement>) => setIdentityForm({ ...identityForm, allowed_providers: e.target.value })}
                placeholder={t('org.identity_providers_placeholder') ?? 'provider-a, provider-b — empty = any'}
              />
              <TextInput
                id="identity-projects"
                labelText={t('org.identity_projects_label') ?? 'Allowed projects'}
                value={identityForm.allowed_projects}
                onChange={(e: React.ChangeEvent<HTMLInputElement>) => setIdentityForm({ ...identityForm, allowed_projects: e.target.value })}
                placeholder={t('org.identity_projects_placeholder') ?? 'project-a, project-b — empty = any'}
              />
              <TextInput
                id="identity-targets"
                labelText={t('org.identity_targets_label') ?? 'Human question recipients'}
                value={identityForm.human_targets}
                onChange={(e: React.ChangeEvent<HTMLInputElement>) => setIdentityForm({ ...identityForm, human_targets: e.target.value })}
                placeholder={t('org.identity_targets_placeholder') ?? 'user-a, user-b — empty = anyone'}
              />
              <div className="form-actions">
                <Button kind="secondary" onClick={() => { setEditingIdentity(null); setIdentityDialogOpen(false); setIdentityForm(emptyIdentityForm()) }}>{t('action.cancel') ?? 'Cancel'}</Button>
                <Button onClick={() => void saveIdentity()} disabled={!identityForm.name.trim()}>{editingIdentity ? (t('action.save') ?? 'Save') : (t('action.create') ?? 'Create')}</Button>
              </div>
            </div>
        </AppModal>
      )}

      {/* Identity delete confirm */}
      {deletingIdentity && (
        <AppModal onClose={() => setDeletingIdentity(null)} panelStyle={{ width: '460px' }}>
            <Heading>{t('org.identity_delete_confirm', { name: deletingIdentity.name }) ?? `Delete identity "${deletingIdentity.name}"?`}</Heading>
            <p style={{ color: 'var(--tm-text-3)', margin: '0.75rem 0' }}>
              {t('org.identity_delete_warning') ?? 'If a trigger uses this identity, deletion is refused — detach it there first.'}
            </p>
            <div className="form-actions">
              <Button kind="secondary" onClick={() => setDeletingIdentity(null)}>{t('action.cancel') ?? 'Cancel'}</Button>
              <Button kind="danger" onClick={() => void doDeleteIdentity()}>{t('action.delete') ?? 'Delete'}</Button>
            </div>
        </AppModal>
      )}

      {loading && <Loading withOverlay={false} />}
    </div>
  )
}
