import { useEffect, useMemo, useRef, useState } from 'react'
import { Tag } from '@carbon/react'
import { Close, Person, Building, Group, Star } from '@carbon/icons-react'
import type { User } from './workspaceApi'
import type { OrgUnitsState } from './orgUnits'
import { useT } from './i18n'

/** Reviewer token kinds mirror the ask_human recipient syntax
 *  (docs/artifact-lifecycle.md §6): user:<id>, org:<unit id>, role:<role>,
 *  project_owner — plus a bare id/name typed by hand. */
export type ReviewerKind = 'user' | 'org' | 'role' | 'owner' | 'bare'

export interface ReviewerToken {
  raw: string
  kind: ReviewerKind
  label: string
  sub?: string
}

interface Suggestion extends ReviewerToken {
  group: 'users' | 'orgs' | 'roles'
  searchText: string
}

const ROLE_LABELS: Record<string, { en: string; ru: string }> = {
  viewer: { en: 'viewer', ru: 'читатель' },
  operator: { en: 'operator', ru: 'оператор' },
  writer: { en: 'writer', ru: 'редактор' },
  admin: { en: 'admin', ru: 'администратор' },
}

/** Who may sign off: a chip input with directory autocomplete. In a company
 *  of ten thousand nobody knows ids — people pick colleagues by name, job
 *  title or unit, units by name, and the chip keeps the exact recipient
 *  syntax underneath. Free text still works: anything typed without picking
 *  a suggestion becomes a bare token, validated by the backend as before. */
export function ReviewerPicker({ value, onChange, users, org, invalid }: {
  value: string[]
  onChange: (tokens: string[]) => void
  users: User[]
  org: OrgUnitsState
  invalid?: boolean
}) {
  const t = useT()
  const [query, setQuery] = useState('')
  const [cursor, setCursor] = useState(0)
  const [open, setOpen] = useState(false)
  const wrapRef = useRef<HTMLDivElement | null>(null)
  const inputRef = useRef<HTMLInputElement | null>(null)

  // Close the dropdown on any outside click.
  useEffect(() => {
    const onDown = (event: MouseEvent) => {
      if (wrapRef.current && !wrapRef.current.contains(event.target as Node)) setOpen(false)
    }
    document.addEventListener('mousedown', onDown)
    return () => document.removeEventListener('mousedown', onDown)
  }, [])

  const tokenLabel = (raw: string): ReviewerToken => {
    if (raw === 'project_owner') return { raw, kind: 'owner', label: t('reviewers.owner_label') ?? 'Project owner (admins)' }
    if (raw.startsWith('org:')) {
      const id = raw.slice(4)
      return { raw, kind: 'org', label: org.nameOf(id) || id, sub: t('reviewers.org_unit') ?? 'org unit' }
    }
    if (raw.startsWith('role:')) {
      const role = raw.slice(5)
      const ru = ROLE_LABELS[role]?.ru
      const en = ROLE_LABELS[role]?.en ?? role
      const label = t('reviewers.role_label', { role: t(`reviewers.role_${role}`) ?? (document.documentElement.lang === 'ru' ? (ru ?? role) : en) })
      return { raw, kind: 'role', label: label ?? `role: ${role}` }
    }
    if (raw.startsWith('user:')) {
      const id = raw.slice(5)
      const u = users.find((x) => x.id === id)
      return { raw, kind: 'user', label: u?.name ?? id, sub: u?.job_title }
    }
    const u = users.find((x) => x.id === raw || x.name === raw)
    return { raw, kind: 'bare', label: u?.name ?? raw, sub: u?.job_title }
  }

  const suggestions = useMemo<Suggestion[]>(() => {
    const needle = query.trim().toLowerCase()
    const out: Suggestion[] = []
    const match = (text: string | undefined) => !!text && text.toLowerCase().includes(needle)
    // People: name, job title, email — the three things colleagues know.
    for (const u of users) {
      if (!u.active) continue
      if (needle && !(match(u.name) || match(u.job_title) || match(u.email) || match(u.id))) continue
      const unit = u.org_unit_id ? org.nameOf(u.org_unit_id) : ''
      out.push({
        raw: `user:${u.id}`, kind: 'user', label: u.name,
        sub: [u.job_title, unit].filter(Boolean).join(' · '),
        group: 'users', searchText: '',
      })
      if (out.length >= 8) break
    }
    // Units: where the team lives, regardless of who is in it today.
    for (const { unit, depth } of org.flat) {
      if (needle && !(match(unit.name) || match(unit.id))) continue
      out.push({
        raw: `org:${unit.id}`, kind: 'org',
        label: `${'　'.repeat(depth)}${unit.name}`,
        sub: t('reviewers.org_unit') ?? 'org unit',
        group: 'orgs', searchText: '',
      })
      if (out.filter((x) => x.group === 'orgs').length >= 5) break
    }
    // Roles: the platform roles are few and fixed.
    const roles = ['viewer', 'operator', 'writer', 'admin']
    for (const role of roles) {
      if (needle && !(match(role) || match(ROLE_LABELS[role]?.ru) || match(ROLE_LABELS[role]?.en))) continue
      const label = t(`reviewers.role_${role}`) ?? ROLE_LABELS[role]?.en ?? role
      out.push({ raw: `role:${role}`, kind: 'role', label, sub: t('reviewers.role_group') ?? 'role', group: 'roles', searchText: '' })
    }
    if (needle && match('project owner') || needle && match('владелец')) {
      out.push({ raw: 'project_owner', kind: 'owner', label: t('reviewers.owner_label') ?? 'Project owner (admins)', sub: t('reviewers.role_group') ?? 'special', group: 'roles', searchText: '' })
    }
    // The raw text itself: a hand-typed id, name or prefix stays available.
    if (needle) {
      const raw = query.trim()
      if (!value.includes(raw)) {
        out.push({ raw, kind: 'bare', label: raw, sub: t('reviewers.free_text') ?? 'as typed', group: 'roles', searchText: '' })
      }
    }
    return out
  }, [query, users, org, value, t])

  const add = (raw: string) => {
    const rawTrimmed = raw.trim()
    if (!rawTrimmed || value.includes(rawTrimmed)) return
    onChange([...value, rawTrimmed])
    setQuery('')
    setCursor(0)
    inputRef.current?.focus()
  }

  const remove = (raw: string) => onChange(value.filter((v) => v !== raw))

  const onKeyDown = (event: React.KeyboardEvent<HTMLInputElement>) => {
    if (event.key === 'Enter' || event.key === ',') {
      event.preventDefault()
      if (open && suggestions[cursor]) add(suggestions[cursor].raw)
      else if (query.trim()) add(query)
      return
    }
    if (event.key === 'Backspace' && query === '' && value.length > 0) {
      remove(value[value.length - 1])
      return
    }
    if (event.key === 'ArrowDown') { event.preventDefault(); setOpen(true); setCursor((c) => Math.min(c + 1, suggestions.length - 1)) }
    if (event.key === 'ArrowUp') { event.preventDefault(); setCursor((c) => Math.max(c - 1, 0)) }
  }

  const chips = value.map(tokenLabel)

  const groupTitle = (group: Suggestion['group']) => {
    if (group === 'users') return t('reviewers.group_people') ?? 'People'
    if (group === 'orgs') return t('reviewers.group_units') ?? 'Org units'
    return t('reviewers.group_roles') ?? 'Roles & special'
  }

  let lastGroup: Suggestion['group'] | null = null

  return (
    <div ref={wrapRef} style={{ position: 'relative' }}>
      <div
        onClick={() => inputRef.current?.focus()}
        style={{
          display: 'flex', flexWrap: 'wrap', gap: '0.3rem', alignItems: 'center',
          border: `1px solid ${invalid ? 'var(--tm-danger, #da1e28)' : 'var(--tm-border)'}`,
          borderRadius: '6px', padding: '0.35rem 0.5rem', minHeight: '2.2rem', cursor: 'text',
          background: 'var(--tm-surface-2, transparent)',
        }}
      >
        {chips.map((chip) => (
          <span
            key={chip.raw}
            style={{
              display: 'inline-flex', alignItems: 'center', gap: '0.25rem',
              padding: '0.15rem 0.3rem 0.15rem 0.45rem', borderRadius: '5px',
              background: 'var(--tm-surface-3, rgba(127,127,127,.15))',
              fontSize: '0.78rem', maxWidth: '100%',
            }}
            title={chip.raw}
          >
            {chip.kind === 'org' ? <Building size={12} /> : chip.kind === 'role' || chip.kind === 'owner' ? <Group size={12} /> : <Person size={12} />}
            <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{chip.label}</span>
            <button
              type="button"
              aria-label={`remove ${chip.raw}`}
              onClick={(e) => { e.stopPropagation(); remove(chip.raw) }}
              style={{ background: 'none', border: 0, cursor: 'pointer', padding: '0 0.1rem', display: 'inline-flex', color: 'inherit', opacity: 0.7 }}
            >
              <Close size={12} />
            </button>
          </span>
        ))}
        <input
          ref={inputRef}
          value={query}
          onChange={(e) => { setQuery(e.target.value); setOpen(true); setCursor(0) }}
          onFocus={() => setOpen(true)}
          onKeyDown={onKeyDown}
          placeholder={value.length === 0 ? (t('reviewers.placeholder') ?? 'Pick people, units or roles…') : ''}
          style={{ flex: 1, minWidth: '8rem', border: 0, outline: 0, background: 'transparent', color: 'inherit', fontSize: '0.85rem', padding: '0.2rem 0' }}
        />
      </div>
      {open && suggestions.length > 0 && (
        <div
          style={{
            position: 'absolute', top: '100%', left: 0, right: 0, zIndex: 20,
            marginTop: '0.2rem', maxHeight: '18rem', overflowY: 'auto',
            background: 'var(--tm-elevated, #1a1a1a)', border: '1px solid var(--tm-border)',
            borderRadius: '8px', boxShadow: '0 8px 24px rgba(0,0,0,.3)',
          }}
        >
          {suggestions.map((s, i) => {
            const header = s.group !== lastGroup ? groupTitle(s.group) : null
            lastGroup = s.group
            return (
              <div key={`${s.raw}:${i}`}>
                {header && (
                  <div style={{ padding: '0.35rem 0.7rem 0.15rem', fontSize: '0.66rem', letterSpacing: '0.08em', textTransform: 'uppercase', color: 'var(--tm-text-3)' }}>
                    {header}
                  </div>
                )}
                <div
                  onClick={() => { add(s.raw); setOpen(false) }}
                  onMouseEnter={() => setCursor(i)}
                  style={{
                    display: 'flex', alignItems: 'center', gap: '0.5rem',
                    padding: '0.4rem 0.7rem', cursor: 'pointer',
                    background: i === cursor ? 'rgba(127,127,127,.14)' : 'transparent',
                  }}
                >
                  <span style={{ opacity: 0.7, display: 'inline-flex', flexShrink: 0 }}>
                    {s.kind === 'org' ? <Building size={14} /> : s.kind === 'role' ? <Group size={14} /> : s.kind === 'owner' ? <Star size={14} /> : <Person size={14} />}
                  </span>
                  <span style={{ display: 'flex', flexDirection: 'column', minWidth: 0, flex: 1 }}>
                    <span style={{ fontSize: '0.85rem', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{s.label}</span>
                    {s.sub && <span style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{s.sub}</span>}
                  </span>
                  {s.kind === 'user' && <Tag size="sm" type="cool-gray">{t('reviewers.person') ?? 'person'}</Tag>}
                </div>
              </div>
            )
          })}
        </div>
      )}
    </div>
  )
}
