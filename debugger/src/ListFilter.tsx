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
  // NB: Carbon Search forwards the `style` prop down to the <input>, so a
  // margin on it becomes the input's own margin; with `align-items: center`
  // that grows the flex container and drops the magnifier icon below the
  // input's optical center. Spacing therefore lives on this wrapper.
  return (
    <div style={{ marginBottom: '0.5rem' }}>
      <Search
        size="sm"
        labelText=""
        placeholder={placeholder ?? t('common.filter') ?? 'Filter'}
        value={value}
        onChange={(e: React.ChangeEvent<HTMLInputElement>) => onChange(e.target.value)}
      />
    </div>
  )
}

/** Case-insensitive substring match against the meaningful fields. */
export function matchesFilter(query: string, ...fields: (string | undefined | null)[]): boolean {
  const q = query.trim().toLowerCase()
  if (!q) return true
  return fields.some((f) => (f ?? '').toLowerCase().includes(q))
}
