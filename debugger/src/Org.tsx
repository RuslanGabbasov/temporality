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
import { workspaceApi, type OrgUnit, type UnitResources, type OrgUnitKind } from './workspaceApi'
import { useT } from './i18n'
import { whoami } from './kernelApi'
import ListFilter, { matchesFilter } from './ListFilter'
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

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const data = await workspaceApi.listOrgUnits()
      setUnits(data.units ?? [])
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }, [])

  useEffect(() => { void load() }, [load])
  useEffect(() => {
    void whoami().then((w) => setIsAdmin(w.role === 'admin')).catch(() => setIsAdmin(false))
  }, [])
  useEffect(() => {
    if (!selected) { setResources(null); return }
    void workspaceApi.unitResources(selected)
      .then(setResources)
      .catch(() => setResources(null))
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
              <p style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem', marginTop: '1rem' }}>
                {t('org.subtree_hint') ?? 'Everything bound to this unit is also visible in its sub-units.'}
              </p>
            </div>
          )}
        </div>
      </div>

      {/* Create / rename dialog */}
      {(editing || creatingParent !== null) && (
        <div className="modal-overlay">
          <div className="modal-panel" style={{ width: '420px' }}>
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
          </div>
        </div>
      )}

      {/* Move dialog */}
      {moving && (
        <div className="modal-overlay">
          <div className="modal-panel" style={{ width: '460px' }}>
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
          </div>
        </div>
      )}

      {/* Delete confirm */}
      {confirmDelete && (
        <div className="modal-overlay">
          <div className="modal-panel" style={{ width: '460px' }}>
            <Heading>{t('org.delete_confirm', { name: confirmDelete.name }) ?? `Delete "${confirmDelete.name}"?`}</Heading>
            <p style={{ color: 'var(--tm-text-3)', margin: '0.75rem 0' }}>
              {t('org.delete_warning') ?? 'Only an empty unit can be deleted: move its children, unbind resources and reassign users first.'}
            </p>
            <div className="form-actions">
              <Button kind="secondary" onClick={() => setConfirmDelete(null)}>{t('action.cancel') ?? 'Cancel'}</Button>
              <Button kind="danger" onClick={() => void doDelete()}>{t('action.delete') ?? 'Delete'}</Button>
            </div>
          </div>
        </div>
      )}

      {loading && <Loading withOverlay={false} />}
    </div>
  )
}
