import { useEffect, useState, useCallback } from 'react'
import { Select, SelectItem, Tag } from '@carbon/react'
import { workspaceApi, type OrgUnit } from './workspaceApi'
import { whoami } from './kernelApi'
import { useT } from './i18n'

/** Shared org-structure context for resource forms and cards: the unit tree,
 * lookup helpers and the caller's admin flag (binding changes are admin-only,
 * docs/org-structure.md §39). */
export interface OrgUnitsState {
  units: OrgUnit[]
  byId: Map<string, OrgUnit>
  /** Tree-ordered units with depth, ready for indented selects. */
  flat: { unit: OrgUnit; depth: number }[]
  nameOf: (id?: string) => string
  isAdmin: boolean
  loading: boolean
}

export function useOrgUnits(): OrgUnitsState {
  const t = useT()
  const [units, setUnits] = useState<OrgUnit[]>([])
  const [isAdmin, setIsAdmin] = useState(false)
  const [loading, setLoading] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const data = await workspaceApi.listOrgUnits()
      setUnits(data.units ?? [])
    } catch {
      // Org endpoints may be unavailable (older kernel) — forms degrade to
      // "whole organization" silently.
      setUnits([])
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => { void load() }, [load])
  useEffect(() => {
    void whoami().then((w) => setIsAdmin(w.role === 'admin')).catch(() => setIsAdmin(false))
  }, [])

  const byId = new Map(units.map((u) => [u.id, u]))
  return {
    units,
    byId,
    flat: flattenUnits(units),
    nameOf: (id?: string) => (id ? (byId.get(id)?.name ?? byId.get(id)?.id ?? id) : (t('org.global') ?? 'Whole organization')),
    isAdmin,
    loading,
  }
}

/** Depth-first flattening in tree order. Units arrive parent-first from the
 * API (ordered by path length), so children always follow their parents. */
export function flattenUnits(units: OrgUnit[]): { unit: OrgUnit; depth: number }[] {
  const childrenOf = new Map<string, OrgUnit[]>()
  const roots: OrgUnit[] = []
  for (const u of units) {
    if (u.parent_id && units.some((x) => x.id === u.parent_id)) {
      const list = childrenOf.get(u.parent_id) ?? []
      list.push(u)
      childrenOf.set(u.parent_id, list)
    } else {
      roots.push(u)
    }
  }
  const out: { unit: OrgUnit; depth: number }[] = []
  const walk = (list: OrgUnit[], depth: number) => {
    for (const u of list) {
      out.push({ unit: u, depth })
      walk(childrenOf.get(u.id) ?? [], depth + 1)
    }
  }
  walk(roots, 0)
  return out
}

/** Availability selector for resource forms: "Whole organization" plus every
 * unit, indented by depth. Disabled in edit mode for non-admins — re-scoping
 * an existing resource goes through the admin binding endpoint. */
export function OrgUnitSelect({ id, value, onChange, org, disabled, allowUnassigned, label }: {
  id: string
  value: string
  onChange: (orgUnitID: string) => void
  org: OrgUnitsState
  disabled?: boolean
  /** Users: empty means "not assigned" rather than "whole organization". */
  allowUnassigned?: boolean
  /** Optional label override (users say "Primary unit"). */
  label?: string
}) {
  const t = useT()
  return (
    <Select
      id={id}
      labelText={label ?? (t('common.availability') ?? 'Availability')}
      value={value}
      disabled={disabled}
      onChange={(e: React.ChangeEvent<HTMLSelectElement>) => onChange(e.target.value)}
    >
      <SelectItem
        value=""
        text={allowUnassigned
          ? (t('org.unassigned') ?? 'Not assigned (sees everything)')
          : (t('org.global') ?? 'Whole organization')}
      />
      {org.flat.map(({ unit, depth }) => (
        <SelectItem key={unit.id} value={unit.id} text={`${'　'.repeat(depth)}${unit.name}`} />
      ))}
    </Select>
  )
}

/** Card badge: the unit name, or "Global" for installation-wide resources. */
export function OrgBadge({ orgUnitID, org }: { orgUnitID?: string; org: OrgUnitsState }) {
  const t = useT()
  if (!org.units.length) return null
  return orgUnitID
    ? <Tag type="cyan" size="sm" title={orgUnitID}>{org.nameOf(orgUnitID)}</Tag>
    : <Tag type="gray" size="sm">{t('org.global_badge') ?? 'Global'}</Tag>
}
