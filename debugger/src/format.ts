/** Shared byte formatting: binary units up to TiB, matching quota units. */
export function fmtBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return '—'
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB']
  let value = bytes
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) { value /= 1024; unit++ }
  return `${unit === 0 || value >= 100 ? Math.round(value) : value.toFixed(1)} ${units[unit]}`
}

/** Compact token/counter formatting: 999 → 999, 12_345 → 12.3k, 2_500_000 → 2.5M. */
export function fmtNum(value: number): string {
  if (!Number.isFinite(value)) return '—'
  const abs = Math.abs(value)
  if (abs >= 1_000_000) return `${(value / 1_000_000).toFixed(1)}M`
  if (abs >= 10_000) return `${(value / 1_000).toFixed(1)}k`
  return value.toLocaleString()
}

/** Money formatting: dollars with cents below $100, whole dollars above. */
export function fmtCost(usd: number): string {
  if (!Number.isFinite(usd)) return '—'
  const digits = Math.abs(usd) >= 100 ? 0 : 2
  return `$${usd.toFixed(digits)}`
}
