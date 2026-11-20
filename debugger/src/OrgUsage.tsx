import { useCallback, useEffect, useMemo, useState } from 'react'
import { Select, SelectItem } from '@carbon/react'
import { useT } from './i18n'
import { orgApi, type OrgUsageResponse } from './orgApi'
import type { OrgUnit } from './workspaceApi'

// §24 usage stats: the org_month_usage table as a unit × month grid — the
// same rows the monthly policy admission gate enforces, so what a unit SEES
// here is exactly what LIMITS it.

const compactTokens = (n: number): string => {
  if (n >= 1e9) return `${(n / 1e9).toFixed(1)}B`
  if (n >= 1e6) return `${(n / 1e6).toFixed(1)}M`
  if (n >= 1e3) return `${(n / 1e3).toFixed(1)}k`
  return String(n)
}

const compactCost = (n: number): string => {
  if (n >= 1000) return `$${(n / 1000).toFixed(1)}k`
  return `$${n.toFixed(2)}`
}

/** '2026-11' → "Nov 26" in the browser's locale (the backend stores UTC
 *  months; the label is presentation-only). */
const monthLabel = (month: string): string => {
  const date = new Date(`${month}-01T00:00:00Z`)
  if (Number.isNaN(date.getTime())) return month
  return new Intl.DateTimeFormat(undefined, { month: 'short', year: '2-digit', timeZone: 'UTC' }).format(date)
}

type UnitRow = {
  id: string
  name: string
  cells: Map<string, { tokens: number; cost: number; runs: number }>
  tokens: number
  cost: number
}

export default function OrgUsageSection({ units }: { units: OrgUnit[] }) {
  const t = useT()
  const [window, setWindow] = useState(6)
  const [data, setData] = useState<OrgUsageResponse | null>(null)
  const [error, setError] = useState('')

  const load = useCallback(async (months: number) => {
    try {
      const response = await orgApi.usage({ months })
      setData(response)
      setError('')
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : 'Request failed')
    }
  }, [])

  useEffect(() => { void load(window) }, [load, window])

  const { rows, totals, months } = useMemo(() => {
    const byUnit = new Map<string, UnitRow>()
    const monthList = data?.months ?? []
    for (const row of data?.rows ?? []) {
      let unit = byUnit.get(row.org_node_id)
      if (!unit) {
        const known = units.find((u) => u.id === row.org_node_id)
        unit = { id: row.org_node_id, name: known?.name ?? row.org_node_id, cells: new Map(), tokens: 0, cost: 0 }
        byUnit.set(row.org_node_id, unit)
      }
      unit.cells.set(row.month, { tokens: row.tokens, cost: row.cost_usd, runs: row.runs })
      unit.tokens += row.tokens
      unit.cost += row.cost_usd
    }
    const sorted = [...byUnit.values()].sort((a, b) => b.tokens - a.tokens)
    const totalByMonth = new Map<string, { tokens: number; cost: number }>()
    for (const month of monthList) {
      let tokens = 0
      let cost = 0
      for (const unit of sorted) {
        const cell = unit.cells.get(month)
        if (cell) { tokens += cell.tokens; cost += cell.cost }
      }
      totalByMonth.set(month, { tokens, cost })
    }
    return { rows: sorted, totals: totalByMonth, months: monthList }
  }, [data, units])

  return (
    <div style={{ marginTop: '0.25rem', border: '1px solid var(--tm-border)', borderRadius: '8px', padding: '1rem', background: 'var(--tm-elevated)' }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: '0.75rem', marginBottom: '0.5rem' }}>
        <div style={{ fontSize: '0.75rem', color: 'var(--tm-text-3)' }}>{t('org.usage_hint') ?? 'Month-to-date tokens and provider cost per org unit — the same numbers the monthly policy caps enforce.'}</div>
        <div style={{ width: '160px' }}>
          <Select
            id="org-usage-window"
            hideLabel
            value={String(window)}
            onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setWindow(Number(e.target.value))}
          >
            {[3, 6, 12, 24].map((n) => (
              <SelectItem key={n} value={String(n)} text={t('org.usage_window_months', { count: String(n) }) ?? `${n} months`} />
            ))}
          </Select>
        </div>
      </div>
      {error ? (
        <div style={{ color: 'var(--tm-danger, #f7768e)', fontSize: '0.78rem' }}>{error}</div>
      ) : rows.length === 0 ? (
        <div style={{ color: 'var(--tm-text-3)', fontSize: '0.875rem', padding: '1rem 0.5rem', textAlign: 'center' }}>
          {t('org.usage_none') ?? 'No usage recorded yet'}
        </div>
      ) : (
        <div style={{ overflowX: 'auto' }}>
          <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: '0.78rem' }}>
            <thead>
              <tr style={{ color: 'var(--tm-text-3)', textAlign: 'right' }}>
                <th style={{ padding: '0.2rem 0.5rem 0.2rem 0', fontWeight: 500, textAlign: 'left' }}>{t('org.usage_unit') ?? 'Unit'}</th>
                {months.map((month) => (
                  <th key={month} style={{ padding: '0.2rem 0.5rem', fontWeight: 500, whiteSpace: 'nowrap' }}>{monthLabel(month)}</th>
                ))}
                <th style={{ padding: '0.2rem 0 0.2rem 0.5rem', fontWeight: 500, whiteSpace: 'nowrap' }}>{t('org.usage_total') ?? 'Total'}</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((unit) => (
                <tr key={unit.id} style={{ borderTop: '1px solid var(--tm-border)' }}>
                  <td style={{ padding: '0.3rem 0.5rem 0.3rem 0', whiteSpace: 'nowrap', maxWidth: '220px', overflow: 'hidden', textOverflow: 'ellipsis' }}>
                    {unit.name}
                  </td>
                  {months.map((month) => {
                    const cell = unit.cells.get(month)
                    return (
                      <td key={month} style={{ padding: '0.3rem 0.5rem', whiteSpace: 'nowrap', textAlign: 'right', color: cell ? 'var(--tm-text-1)' : 'var(--tm-text-3)' }}>
                        {cell ? `${compactTokens(cell.tokens)} · ${compactCost(cell.cost)}` : '—'}
                      </td>
                    )
                  })}
                  <td style={{ padding: '0.3rem 0 0.3rem 0.5rem', whiteSpace: 'nowrap', textAlign: 'right', fontWeight: 600 }}>
                    {`${compactTokens(unit.tokens)} · ${compactCost(unit.cost)}`}
                  </td>
                </tr>
              ))}
              <tr style={{ borderTop: '2px solid var(--tm-border)' }}>
                <td style={{ padding: '0.3rem 0.5rem 0.3rem 0', fontWeight: 600, whiteSpace: 'nowrap' }}>{t('org.usage_total') ?? 'Total'}</td>
                {months.map((month) => {
                  const total = totals.get(month)
                  return (
                    <td key={month} style={{ padding: '0.3rem 0.5rem', whiteSpace: 'nowrap', textAlign: 'right', fontWeight: 600 }}>
                      {total ? `${compactTokens(total.tokens)} · ${compactCost(total.cost)}` : '—'}
                    </td>
                  )
                })}
                <td />
              </tr>
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}
