import { Search } from '@carbon/react'
import { useT } from './i18n'

/**
 * Shared client-side list filter for panels with long lists. Renders the
 * compact Carbon search box; matching itself stays in the caller so each
 * panel decides which fields participate (name, id, status, …).
 */
export default function ListFilter({ value, onChange, placeholder }: {
  value: string
  onChange: (value: string) => void
  placeholder?: string
}) {
  const t = useT()
  return (
    <Search
      size="sm"
      labelText=""
      placeholder={placeholder ?? t('common.filter') ?? 'Filter'}
      value={value}
      onChange={(e: React.ChangeEvent<HTMLInputElement>) => onChange(e.target.value)}
      style={{ marginBottom: '0.5rem' }}
    />
  )
}

/** Case-insensitive substring match against the meaningful fields. */
export function matchesFilter(query: string, ...fields: (string | undefined | null)[]): boolean {
  const q = query.trim().toLowerCase()
  if (!q) return true
  return fields.some((f) => (f ?? '').toLowerCase().includes(q))
}
