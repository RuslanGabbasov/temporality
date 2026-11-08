// PresentationView (docs/presentation-engine.md §8): the web renderer of the
// Presentation Engine. It consumes a validated Presentation IR document —
// plain data, never code — and maps every component onto the shared design
// system (Carbon + `--tm-*` tokens). Missing or wrong-typed blocks degrade to
// a visible placeholder instead of breaking the answer bubble, and nothing
// here can execute code from the IR: markdown goes through the sanitized
// Markdown component (raw HTML is not rendered), URLs are scheme-checked
// again at render time (defense in depth — the server already validated).

import { memo, useEffect, useMemo, useState, ReactNode } from 'react'
import { InlineNotification, Tag } from '@carbon/react'
import HumanForm, { type FormSchema } from './HumanForm'
import Markdown from './Markdown'
import MermaidDiagram from './MermaidDiagram'
import { storageDownloadFile, storageFileContent, storageFileURL } from './kernelApi'
import { splitMarkdownBlocks } from './markdownBlocks'

// ---------- IR shapes (mirror kernel/presentation) --------------------------

export interface PresentationAction {
  type?: string
  label?: string
  target?: string
  payload?: unknown
}

export interface PresentationComponent {
  type?: string
  id?: string
  content?: string
  chartType?: string
  data?: Record<string, unknown>
  props?: Record<string, unknown>
  actions?: PresentationAction[]
  items?: unknown[]
  children?: PresentationComponent[]
}

export interface PresentationDocument {
  version?: string
  type?: string
  id?: string
  title?: string
  content?: PresentationComponent[]
}

type StatusTone = 'neutral' | 'success' | 'info' | 'warning' | 'danger'

// ---------- small render helpers --------------------------------------------

/** The URL check repeats the server rule: http(s) or in-app paths only. */
function safeURL(raw: unknown): string | undefined {
  if (typeof raw !== 'string' || raw === '') return undefined
  if (raw.startsWith('/') && !raw.startsWith('//')) return raw
  try {
    const parsed = new URL(raw)
    if ((parsed.protocol === 'http:' || parsed.protocol === 'https:') && parsed.host) return raw
  } catch { /* not a URL */ }
  return undefined
}

function text(value: unknown): string {
  return typeof value === 'string' ? value : value === undefined || value === null ? '' : String(value)
}

function toneOf(status: unknown): StatusTone {
  const value = typeof status === 'string' ? status : ''
  if (value === 'success' || value === 'info' || value === 'warning' || value === 'danger') return value
  return 'neutral'
}

const TAG_COLORS: Record<StatusTone, string> = {
  neutral: 'gray',
  success: 'green',
  info: 'blue',
  warning: 'warn',
  danger: 'red',
}

function StatusTag({ status, children }: { status: unknown; children: ReactNode }) {
  const tone = toneOf(status)
  return <Tag size="sm" type={tone === 'warning' ? 'warm-gray' : TAG_COLORS[tone] as 'gray'} className="presentation-tag">{children}</Tag>
}

function Placeholder({ what }: { what: string }) {
  return <div className="presentation-placeholder">⚠ {what}</div>
}

// ---------- component renderers ---------------------------------------------

function TextView({ component }: { component: PresentationComponent }) {
  const value = text(component.content)
  if (!value) return <Placeholder what="empty text" />
  return <div className="presentation-text">{value}</div>
}

function MarkdownView({ component }: { component: PresentationComponent }) {
  const value = text(component.content)
  if (!value) return <Placeholder what="empty markdown" />
  return <div className="presentation-markdown"><Markdown content={value} /></div>
}

function HeadingView({ component }: { component: PresentationComponent }) {
  const value = text(component.content)
  const level = Math.min(6, Math.max(1, Number(component.props?.level ?? 2) || 2))
  if (!value) return <Placeholder what="empty heading" />
  switch (level) {
    case 1: return <h1 className="presentation-heading presentation-h1">{value}</h1>
    case 3: return <h3 className="presentation-heading presentation-h3">{value}</h3>
    case 4: return <h4 className="presentation-heading presentation-h4">{value}</h4>
    case 5: return <h5 className="presentation-heading presentation-h5">{value}</h5>
    case 6: return <h6 className="presentation-heading presentation-h6">{value}</h6>
    default: return <h2 className="presentation-heading presentation-h2">{value}</h2>
  }
}

interface ListItemIR { label?: string; description?: string }

function ListView({ component }: { component: PresentationComponent }) {
  const ordered = Boolean(component.props?.ordered)
  const items = Array.isArray(component.items) ? component.items : []
  if (items.length === 0) return <Placeholder what="empty list" />
  return (
    <ol className={ordered ? 'presentation-list presentation-list-ordered' : 'presentation-list'}>
      {items.map((item, i) => {
        const entry: ListItemIR = typeof item === 'string' ? { label: item } : (item as ListItemIR)
        return (
          <li key={i}>
            <span className="presentation-list-label">{text(entry.label)}</span>
            {entry.description ? <span className="presentation-list-desc">{text(entry.description)}</span> : null}
          </li>
        )
      })}
    </ol>
  )
}

function QuoteView({ component }: { component: PresentationComponent }) {
  const value = text(component.content)
  const attribution = text(component.props?.attribution)
  if (!value) return <Placeholder what="empty quote" />
  return (
    <blockquote className="presentation-quote">
      {value}
      {attribution ? <footer>— {attribution}</footer> : null}
    </blockquote>
  )
}

function CodeView({ component }: { component: PresentationComponent }) {
  const value = text(component.content)
  const language = text(component.props?.language)
  if (!value) return <Placeholder what="empty code block" />
  return (
    <pre className="presentation-code"><code data-language={language}>{value}</code></pre>
  )
}

function LinkView({ component }: { component: PresentationComponent }) {
  const href = safeURL(component.props?.href)
  const label = text(component.content) || href || ''
  if (!href) return <Placeholder what="link without a safe href" />
  return (
    <a className="presentation-link" href={href} target={href.startsWith('/') ? undefined : '_blank'} rel="noreferrer">
      {label} ↗
    </a>
  )
}

function BadgeView({ component }: { component: PresentationComponent }) {
  const label = text(component.content)
  if (!label) return <Placeholder what="empty badge" />
  return <StatusTag status={component.props?.status}>{label}</StatusTag>
}

function DividerView() {
  return <hr className="presentation-divider" />
}

interface TableColumnIR { key?: string; title?: string; align?: string; kind?: string }
interface TableCellIR { text?: string; status?: string; href?: string }
type CellValue = string | number | boolean | null | TableCellIR

function cellText(value: CellValue): string {
  if (value === null || value === undefined) return ''
  if (typeof value === 'object') return text((value as TableCellIR).text)
  if (typeof value === 'boolean') return value ? 'true' : 'false'
  return String(value)
}

function cellStatus(value: CellValue): string | undefined {
  if (value && typeof value === 'object') return (value as TableCellIR).status
  return undefined
}

function cellHref(value: CellValue): string | undefined {
  if (value && typeof value === 'object') return safeURL((value as TableCellIR).href)
  return undefined
}

function TableView({ component }: { component: PresentationComponent }) {
  const data = (component.data ?? {}) as { columns?: TableColumnIR[]; rows?: Record<string, CellValue>[]; title?: string }
  const columns = Array.isArray(data.columns) ? data.columns : []
  const rows = Array.isArray(data.rows) ? data.rows : []
  if (columns.length === 0 || rows.length === 0) return <Placeholder what="table without columns or rows" />
  return (
    <div className="presentation-table-wrap">
      {data.title ? <div className="presentation-table-title">{text(data.title)}</div> : null}
      <table className="presentation-table">
        <thead>
          <tr>
            {columns.map((col, i) => (
              <th key={i} style={{ textAlign: (col.align as 'left' | 'center' | 'right') ?? 'left' }}>
                {text(col.title) || text(col.key)}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((row, r) => (
            <tr key={r}>
              {columns.map((col, c) => {
                const value = row[col.key ?? '']
                const href = cellHref(value)
                const status = cellStatus(value)
                return (
                  <td key={c} style={{ textAlign: (col.align ?? (col.kind === 'number' ? 'right' : 'left')) as 'left' | 'center' | 'right' }}>
                    {href
                      ? <a className="presentation-link" href={href} target={href.startsWith('/') ? undefined : '_blank'} rel="noreferrer">{cellText(value)} ↗</a>
                      : status
                        ? <StatusTag status={status}>{cellText(value)}</StatusTag>
                        : cellText(value)}
                  </td>
                )
              })}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

function CardView({ component, renderChildren }: { component: PresentationComponent; renderChildren: (children?: PresentationComponent[]) => ReactNode }) {
  const title = text(component.props?.title)
  return (
    <div className="presentation-card">
      {title ? <div className="presentation-card-title">{title}</div> : null}
      {renderChildren(component.children)}
    </div>
  )
}

function CardGridView({ component, renderChildren }: { component: PresentationComponent; renderChildren: (children?: PresentationComponent[]) => ReactNode }) {
  const columns = Math.min(4, Math.max(1, Number(component.props?.columns ?? 2) || 2))
  return <div className="presentation-card-grid" style={{ gridTemplateColumns: `repeat(${columns}, minmax(0, 1fr))` }}>{renderChildren(component.children)}</div>
}

interface KeyValueIR { label?: string; value?: string; status?: string }

function KeyValueView({ component }: { component: PresentationComponent }) {
  const items = Array.isArray(component.items) ? (component.items as KeyValueIR[]) : []
  if (items.length === 0) return <Placeholder what="empty key-value" />
  return (
    <dl className="presentation-keyvalue">
      {items.map((item, i) => (
        <div key={i} className="presentation-keyvalue-row">
          <dt>{text(item.label)}</dt>
          <dd>{item.status && item.status !== 'neutral' ? <StatusTag status={item.status}>{text(item.value)}</StatusTag> : text(item.value)}</dd>
        </div>
      ))}
    </dl>
  )
}

interface KPIIR { label?: string; value?: unknown; status?: string; delta?: string; unit?: string; hint?: string }

function KPIView({ component }: { component: PresentationComponent }) {
  const items = Array.isArray(component.items) ? (component.items as KPIIR[]) : []
  if (items.length === 0) return <Placeholder what="empty kpi" />
  return (
    <div className="presentation-kpi-grid">
      {items.map((item, i) => (
        <div key={i} className={`presentation-kpi presentation-tone-${toneOf(item.status)}`}>
          <div className="presentation-kpi-label">{text(item.label)}</div>
          <div className="presentation-kpi-value">{text(item.value)}{item.unit ? <span className="presentation-kpi-unit"> {text(item.unit)}</span> : null}</div>
          {item.delta || item.hint ? <div className="presentation-kpi-note">{text(item.delta)}{item.delta && item.hint ? ' · ' : ''}{text(item.hint)}</div> : null}
        </div>
      ))}
    </div>
  )
}

interface ProgressIR { label?: string; value?: number; max?: number; status?: string; unit?: string }

function ProgressView({ component }: { component: PresentationComponent }) {
  const items = Array.isArray(component.items) ? (component.items as ProgressIR[]) : []
  if (items.length === 0) return <Placeholder what="empty progress" />
  return (
    <div className="presentation-progress-list">
      {items.map((item, i) => {
        const max = Number(item.max) > 0 ? Number(item.max) : 100
        const value = Number(item.value) || 0
        const share = Math.min(1, Math.max(0, value / max))
        return (
          <div key={i} className="presentation-progress">
            <div className="presentation-progress-head">
              <span className="presentation-progress-label">{text(item.label)}</span>
              <span className="presentation-progress-value">{value} / {max}{item.unit ? ` ${text(item.unit)}` : ''}</span>
            </div>
            <div className="presentation-progress-track">
              <div className={`presentation-progress-fill presentation-tone-${toneOf(item.status)}`} style={{ width: `${share * 100}%` }} />
            </div>
          </div>
        )
      })}
    </div>
  )
}

function StatusView({ component }: { component: PresentationComponent }) {
  const value = text(component.content)
  if (!value) return <Placeholder what="empty status" />
  const detail = text(component.props?.detail)
  return (
    <div className={`presentation-status presentation-tone-${toneOf(component.props?.status)}`}>
      <span className="presentation-status-dot" />
      <div>
        <div className="presentation-status-text">{value}</div>
        {detail ? <div className="presentation-status-detail">{detail}</div> : null}
      </div>
    </div>
  )
}

function AlertView({ component }: { component: PresentationComponent }) {
  const value = text(component.content)
  if (!value) return <Placeholder what="empty alert" />
  // IR severity "danger" maps onto Carbon's "error" kind.
  const kind = ({ info: 'info', success: 'success', warning: 'warning', danger: 'error' } as const)[
    text(component.props?.severity) as 'info' | 'success' | 'warning' | 'danger'
  ] ?? 'info'
  const title = text(component.props?.title) || undefined
  return (
    <InlineNotification kind={kind} title={title ?? text(component.props?.severity).toUpperCase()} subtitle={value} lowContrast hideCloseButton className="presentation-alert" />
  )
}

interface ComparisonIR { label?: string; status?: string; fields?: { label?: string; value?: string }[] }

function ComparisonView({ component }: { component: PresentationComponent }) {
  const items = Array.isArray(component.items) ? (component.items as ComparisonIR[]) : []
  if (items.length < 2) return <Placeholder what="comparison needs at least two options" />
  const fields: string[] = []
  const seen = new Set<string>()
  for (const item of items) {
    for (const field of item.fields ?? []) {
      const key = text(field.label)
      if (key && !seen.has(key)) { seen.add(key); fields.push(key) }
    }
  }
  return (
    <div className="presentation-table-wrap">
      <table className="presentation-table">
        <thead>
          <tr>
            <th>Option</th>
            {fields.map((f) => <th key={f}>{f}</th>)}
          </tr>
        </thead>
        <tbody>
          {items.map((item, i) => (
            <tr key={i}>
              <td>{item.status && item.status !== 'neutral' ? <StatusTag status={item.status}>{text(item.label)}</StatusTag> : text(item.label)}</td>
              {fields.map((f) => {
                const field = (item.fields ?? []).find((entry) => text(entry.label) === f)
                return <td key={f}>{text(field?.value)}</td>
              })}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

interface AccordionIR { label?: string; content?: string }

function AccordionView({ component }: { component: PresentationComponent }) {
  const items = Array.isArray(component.items) ? (component.items as AccordionIR[]) : []
  if (items.length === 0) return <Placeholder what="empty accordion" />
  return (
    <div className="presentation-accordion">
      {items.map((item, i) => (
        <details key={i} className="presentation-accordion-item" open={i === 0}>
          <summary>{text(item.label)}</summary>
          <div className="presentation-accordion-body"><Markdown content={text(item.content)} /></div>
        </details>
      ))}
    </div>
  )
}

// ---------- charts -----------------------------------------------------------

const SERIES_COLORS = ['var(--tm-chart-1)', 'var(--tm-chart-2)', 'var(--tm-chart-3)', 'var(--tm-chart-4)', 'var(--tm-chart-5)', 'var(--tm-chart-6)']

interface ChartDataIR {
  labels?: string[]
  values?: number[]
  series?: { name?: string; values?: number[] }[]
  unit?: string
  x_title?: string
  y_title?: string
}

function ChartView({ component }: { component: PresentationComponent }) {
  const data = (component.data ?? {}) as ChartDataIR
  const props = (component.props ?? {}) as { chartType?: string; title?: string; showValues?: boolean; showLegend?: boolean }
  const chartType = (props.chartType || component.chartType || 'line').toLowerCase()
  const labels = Array.isArray(data.labels) ? data.labels.map((l) => text(l)) : []
  const series: { name: string; values: number[]; color: string }[] = []
  if (Array.isArray(data.values) && data.values.length > 0) {
    series.push({ name: text(props.title) || 'Series 1', values: data.values.map(Number), color: SERIES_COLORS[0] })
  }
  if (Array.isArray(data.series)) {
    data.series.forEach((s, i) => series.push({ name: text(s.name), values: (Array.isArray(s.values) ? s.values : []).map(Number), color: SERIES_COLORS[(i + 1) % SERIES_COLORS.length] }))
  }
  if (series.length === 0 || series[0].values.length === 0) return <Placeholder what="chart without data" />

  if (chartType === 'pie' || chartType === 'donut') {
    return <PieChart chartType={chartType} title={text(props.title)} labels={labels} values={series[0].values} unit={text(data.unit)} showLegend={props.showLegend !== false} />
  }
  return <XYChart chartType={chartType} title={text(props.title)} labels={labels} series={series} unit={text(data.unit)} xTitle={text(data.x_title)} yTitle={text(data.y_title)} showValues={Boolean(props.showValues)} showLegend={props.showLegend !== false} />
}

function PieChart({ chartType, title, labels, values, showLegend }: {
  chartType: string; title: string; labels: string[]; values: number[]; unit: string; showLegend: boolean
}) {
  const total = values.reduce((acc, v) => acc + v, 0)
  if (total <= 0) return <Placeholder what="pie chart needs positive values" />
  const size = 220
  const cx = size / 2
  const cy = size / 2
  const r = size / 2 - 14
  const inner = chartType === 'donut' ? r * 0.58 : 0
  let angle = -Math.PI / 2
  const slices: ReactNode[] = []
  values.forEach((value, i) => {
    if (value <= 0) return
    const sweep = (value / total) * 2 * Math.PI
    const end = angle + sweep
    const large = sweep > Math.PI ? 1 : 0
    const x1 = cx + r * Math.cos(angle)
    const y1 = cy + r * Math.sin(angle)
    const x2 = cx + r * Math.cos(end)
    const y2 = cy + r * Math.sin(end)
    if (inner > 0) {
      const ix1 = cx + inner * Math.cos(end)
      const iy1 = cy + inner * Math.sin(end)
      const ix2 = cx + inner * Math.cos(angle)
      const iy2 = cy + inner * Math.sin(angle)
      slices.push(<path key={i} d={`M ${x1} ${y1} A ${r} ${r} 0 ${large} 1 ${x2} ${y2} L ${ix1} ${iy1} A ${inner} ${inner} 0 ${large} 0 ${ix2} ${iy2} Z`} fill={SERIES_COLORS[i % SERIES_COLORS.length]} stroke="var(--tm-bg)" strokeWidth="1.5" />)
    } else if (sweep >= 2 * Math.PI - 0.001) {
      slices.push(<circle key={i} cx={cx} cy={cy} r={r} fill={SERIES_COLORS[i % SERIES_COLORS.length]} />)
    } else {
      slices.push(<path key={i} d={`M ${cx} ${cy} L ${x1} ${y1} A ${r} ${r} 0 ${large} 1 ${x2} ${y2} Z`} fill={SERIES_COLORS[i % SERIES_COLORS.length]} stroke="var(--tm-bg)" strokeWidth="1.5" />)
    }
    angle = end
  })
  return (
    <figure className="presentation-chart">
      {title ? <figcaption className="presentation-chart-title">{title}</figcaption> : null}
      <svg viewBox={`0 0 ${size} ${size}`} className="presentation-chart-svg presentation-chart-pie" role="img">
        {slices}
      </svg>
      {showLegend ? (
        <div className="presentation-legend">
          {values.map((value, i) => (
            <span key={i} className="presentation-legend-item">
              <span className="presentation-legend-chip" style={{ background: SERIES_COLORS[i % SERIES_COLORS.length] }} />
              {labels[i] ?? String(i + 1)} · {total > 0 ? Math.round((value / total) * 1000) / 10 : 0}%
            </span>
          ))}
        </div>
      ) : null}
    </figure>
  )
}

function XYChart({ chartType, title, labels, series, unit, xTitle, yTitle, showValues, showLegend }: {
  chartType: string; title: string; labels: string[]; series: { name: string; values: number[]; color: string }[]
  unit: string; xTitle: string; yTitle: string; showValues: boolean; showLegend: boolean
}) {
  const width = 460
  const height = 240
  const margin = { top: 14, right: 16, bottom: 30, left: 48 }
  const plotW = width - margin.left - margin.right
  const plotH = height - margin.top - margin.bottom
  const count = Math.max(...series.map((s) => s.values.length), 1)
  let min = Infinity
  let max = -Infinity
  for (const s of series) {
    for (const v of s.values) {
      if (v < min) min = v
      if (v > max) max = v
    }
  }
  if (!Number.isFinite(min) || !Number.isFinite(max)) return <Placeholder what="chart data is not numeric" />
  if (min === max) { min -= Math.abs(min) * 0.1 + 1; max += Math.abs(max) * 0.1 + 1 }
  const x = (i: number) => margin.left + plotW * ((i + 0.5) / count)
  const y = (v: number) => margin.top + plotH * (max - v) / (max - min)
  const ticks = [0, 0.25, 0.5, 0.75, 1].map((f) => max - (max - min) * f)
  const fmt = (v: number) => Number.isInteger(v) ? String(v) : String(Math.round(v * 100) / 100)
  const labelStep = count > 10 ? Math.ceil(count / 10) : 1

  const seriesNodes: ReactNode[] = []
  series.forEach((s, si) => {
    if (chartType === 'bar') {
      const barW = Math.min(plotW / count * 0.62, 46) / (series.length > 1 ? series.length : 1)
      s.values.forEach((v, i) => {
        const bx = x(i) - barW * (series.length > 1 ? series.length : 1) / 2 + si * barW
        const by = y(v)
        seriesNodes.push(<rect key={`${si}-${i}`} x={bx} y={by} width={Math.max(barW, 2)} height={Math.max(margin.top + plotH - by, 0)} fill={s.color} rx="2" />)
        if (showValues && barW >= 22) {
          seriesNodes.push(<text key={`v-${si}-${i}`} x={bx + barW / 2} y={by - 4} textAnchor="middle" className="presentation-chart-value">{fmt(v)}</text>)
        }
      })
    } else {
      const points = s.values.map((v, i) => `${x(i)},${y(v)}`).join(' ')
      if (chartType === 'area' && s.values.length > 1) {
        seriesNodes.push(<polygon key={`a-${si}`} points={`${points} ${x(s.values.length - 1)},${margin.top + plotH} ${x(0)},${margin.top + plotH}`} fill={s.color} opacity="0.16" />)
      }
      seriesNodes.push(<polyline key={`l-${si}`} points={points} fill="none" stroke={s.color} strokeWidth="2" strokeLinejoin="round" />)
      s.values.forEach((v, i) => {
        seriesNodes.push(<circle key={`p-${si}-${i}`} cx={x(i)} cy={y(v)} r="2.5" fill={s.color} />)
        if (showValues && series.length === 1) {
          seriesNodes.push(<text key={`v-${si}-${i}`} x={x(i)} y={y(v) - 6} textAnchor="middle" className="presentation-chart-value">{fmt(v)}</text>)
        }
      })
    }
  })

  return (
    <figure className="presentation-chart">
      {title ? <figcaption className="presentation-chart-title">{title}</figcaption> : null}
      <svg viewBox={`0 0 ${width} ${height}`} className="presentation-chart-svg" role="img">
        {ticks.map((tick, i) => (
          <g key={i}>
            {i > 0 && i < ticks.length - 1 ? <line x1={margin.left} y1={y(tick)} x2={margin.left + plotW} y2={y(tick)} className="presentation-chart-grid" /> : null}
            <text x={margin.left - 6} y={y(tick) + 3} textAnchor="end" className="presentation-chart-axis">{fmt(tick)}</text>
          </g>
        ))}
        <line x1={margin.left} y1={margin.top + plotH} x2={margin.left + plotW} y2={margin.top + plotH} className="presentation-chart-axis-line" />
        <line x1={margin.left} y1={margin.top} x2={margin.left} y2={margin.top + plotH} className="presentation-chart-axis-line" />
        {seriesNodes}
        {labels.map((label, i) => (i % labelStep === 0
          ? <text key={i} x={x(i)} y={margin.top + plotH + 16} textAnchor="middle" className="presentation-chart-axis">{label}</text>
          : null))}
        {yTitle ? <text x={0} y={margin.top + 8} className="presentation-chart-axis">{yTitle}</text> : null}
        {xTitle ? <text x={margin.left + plotW / 2} y={height - 4} textAnchor="middle" className="presentation-chart-axis">{xTitle}</text> : null}
      </svg>
      {unit ? <div className="presentation-chart-unit">{unit}</div> : null}
      {showLegend && series.length > 1 ? (
        <div className="presentation-legend">
          {series.map((s, i) => (
            <span key={i} className="presentation-legend-item">
              <span className="presentation-legend-chip" style={{ background: s.color }} />
              {s.name}
            </span>
          ))}
        </div>
      ) : null}
    </figure>
  )
}

// ---------- diagrams (Mermaid conversion — the IR stays engine-neutral) -----

// ---------- mermaid flow conversion -----------------------------------------

interface FlowNodeIR { id?: string; label?: string; shape?: string }
interface FlowEdgeIR { from?: string; to?: string; label?: string; dashed?: boolean }

function flowMermaid(data: { nodes?: FlowNodeIR[]; edges?: FlowEdgeIR[] }, direction: string): string | undefined {
  const nodes = Array.isArray(data.nodes) ? data.nodes : []
  const edges = Array.isArray(data.edges) ? data.edges : []
  if (nodes.length === 0) return undefined
  const shape = (node: FlowNodeIR): [string, string] => {
    if (node.shape === 'round') return ['(', ')']
    if (node.shape === 'diamond') return ['{', '}']
    return ['[', ']']
  }
  const clean = (value: unknown) => text(value).replace(/"/g, "'").replace(/\n/g, ' ')
  const lines = [`flowchart ${direction || 'TB'}`]
  for (const node of nodes) {
    const id = text(node.id)
    if (!id) return undefined
    const [open, close] = shape(node)
    lines.push(`    ${id}${open}"${clean(node.label)}"${close}`)
  }
  for (const edge of edges) {
    const from = text(edge.from)
    const to = text(edge.to)
    if (!from || !to) return undefined
    const label = clean(edge.label)
    if (edge.dashed) {
      lines.push(label ? `    ${from} -.${label}.-> ${to}` : `    ${from} -.-> ${to}`)
    } else {
      lines.push(label ? `    ${from} -->|"${label}"| ${to}` : `    ${from} --> ${to}`)
    }
  }
  return lines.join('\n')
}

function FlowchartView({ component }: { component: PresentationComponent }) {
  const data = (component.data ?? {}) as { nodes?: FlowNodeIR[]; edges?: FlowEdgeIR[] }
  const direction = text(component.props?.direction) || 'TB'
  const source = useMemo(() => flowMermaid(data, direction), [data, direction])
  const title = text(component.props?.title)
  if (!source) return <Placeholder what="flowchart without nodes" />
  return (
    <figure className="presentation-diagram">
      {title ? <figcaption className="presentation-chart-title">{title}</figcaption> : null}
      <MermaidDiagram chart={source} />
    </figure>
  )
}

interface SequenceIR { participants?: { id?: string; label?: string }[]; messages?: { from?: string; to?: string; label?: string; kind?: string }[] }

function sequenceMermaid(data: SequenceIR): string | undefined {
  const participants = Array.isArray(data.participants) ? data.participants : []
  const messages = Array.isArray(data.messages) ? data.messages : []
  if (participants.length < 2 || messages.length === 0) return undefined
  const clean = (value: unknown) => text(value).replace(/"/g, "'").replace(/\n/g, ' ')
  const lines = ['sequenceDiagram']
  for (const p of participants) {
    const id = text(p.id)
    if (!id) return undefined
    lines.push(`    participant ${id} as "${clean(p.label)}"`)
  }
  for (const msg of messages) {
    const from = text(msg.from)
    const to = text(msg.to)
    if (!from || !to) return undefined
    const arrow = msg.kind === 'return' ? '-->>' : '->>'
    const label = clean(msg.label)
    lines.push(label ? `    ${from} ${arrow} ${to} : ${label}` : `    ${from} ${arrow} ${to}`)
  }
  return lines.join('\n')
}

function SequenceView({ component }: { component: PresentationComponent }) {
  const data = (component.data ?? {}) as SequenceIR
  const source = useMemo(() => sequenceMermaid(data), [data])
  const title = text(component.props?.title)
  if (!source) return <Placeholder what="sequence diagram without participants" />
  return (
    <figure className="presentation-diagram">
      {title ? <figcaption className="presentation-chart-title">{title}</figcaption> : null}
      <MermaidDiagram chart={source} />
    </figure>
  )
}

// ---------- planning ---------------------------------------------------------

interface TimelineIR { label?: string; at?: string; status?: string; description?: string }

function TimelineView({ component }: { component: PresentationComponent }) {
  const items = Array.isArray(component.items) ? (component.items as TimelineIR[]) : []
  if (items.length === 0) return <Placeholder what="empty timeline" />
  return (
    <ol className="presentation-timeline">
      {items.map((item, i) => (
        <li key={i} className={`presentation-timeline-item presentation-tone-${toneOf(item.status)}`}>
          <span className="presentation-timeline-dot" />
          <span className="presentation-timeline-at">{text(item.at)}</span>
          <div>
            <div className="presentation-timeline-label">{text(item.label)}</div>
            {item.description ? <div className="presentation-timeline-desc">{text(item.description)}</div> : null}
          </div>
        </li>
      ))}
    </ol>
  )
}

// ---------- media ------------------------------------------------------------

/** In-app references (`/workspace/<rel>`, `/cache/<rel>`) point at files
 *  behind the kernel storage API: the bare URL 404s on the debugger origin
 *  and the endpoint wants the auth header, so <img>/<a> cannot use it
 *  directly. This pure mapper classifies a src — http(s) URLs and plain
 *  origin paths pass through untouched — and resolves storage references to
 *  the project/area/path triple plus the storage URL (content fetches still
 *  need the auth header; see storageFileContent in kernelApi.ts). */
export interface ResolvedImageRef {
  /** <img src> value for pass-through references, visible fallback otherwise. */
  src: string
  storage?: { project: string; area: 'workspace' | 'cache'; path: string; url: string }
}

const IN_APP_STORAGE_PATH = /^\/(workspace|cache)\/(.+)$/

export function resolveImageSource(raw: string, project?: string): ResolvedImageRef {
  const match = IN_APP_STORAGE_PATH.exec(raw)
  if (!match || !project) return { src: raw }
  const area = match[1] as 'workspace' | 'cache'
  const path = match[2]
  return { src: raw, storage: { project, area, path, url: storageFileURL(project, area, path) } }
}

// Blobs are cached per src+project so repeated renders of the same artifact
// never refetch. Blobs (not object URLs) are what gets cached because object
// URLs are revoked on unmount — every mount creates and revokes its own.
const imageBlobCache = new Map<string, Promise<Blob>>()

/** Resolve an image src for <img>: http(s) passes through untouched; in-app
 *  storage paths become blob object URLs (fetched with the auth header,
 *  revoked on unmount). Undefined while a storage image is in flight; on
 *  failure the raw path stays as the src — a visible fallback, never silent. */
export function useResolvedImage(src: string | undefined, project?: string): string | undefined {
  const ref = useMemo(() => (src ? resolveImageSource(src, project) : undefined), [src, project])
  const [resolved, setResolved] = useState<string | undefined>(undefined)

  useEffect(() => {
    const storage = ref?.storage
    if (!storage) return
    setResolved(undefined)
    const key = `${storage.project}|${ref.src}`
    let entry = imageBlobCache.get(key)
    if (!entry) {
      entry = storageFileContent(storage.project, storage.area, storage.path).then((response) => response.blob())
      imageBlobCache.set(key, entry)
      // A failed fetch must not poison the cache — the next mount retries.
      entry.catch(() => { imageBlobCache.delete(key) })
    }
    let cancelled = false
    let objectUrl: string | undefined
    entry
      .then((blob) => {
        objectUrl = URL.createObjectURL(blob)
        if (cancelled) URL.revokeObjectURL(objectUrl)
        else setResolved(objectUrl)
      })
      .catch(() => { if (!cancelled) setResolved(ref.src) })
    return () => {
      cancelled = true
      if (objectUrl) URL.revokeObjectURL(objectUrl)
    }
  }, [ref])

  return ref ? (ref.storage ? resolved : ref.src) : undefined
}

function ImageView({ component, project }: { component: PresentationComponent; project?: string }) {
  const src = safeURL(component.props?.src)
  const resolved = useResolvedImage(src, project)
  if (!src) return <Placeholder what="image without a safe src" />
  const caption = text(component.props?.caption) || text(component.content)
  return (
    <figure className="presentation-image">
      {resolved ? <img src={resolved} alt={caption || 'image'} loading="lazy" /> : null}
      {caption ? <figcaption>{caption}</figcaption> : null}
    </figure>
  )
}

interface GalleryIR { src?: string; alt?: string; caption?: string }

// One gallery item per component instance: hooks cannot run in a list map.
function GalleryItem({ item, project }: { item: GalleryIR & { src: string }; project?: string }) {
  const resolved = useResolvedImage(item.src, project)
  return (
    <figure className="presentation-image">
      {resolved ? <img src={resolved} alt={text(item.alt) || text(item.caption) || 'image'} loading="lazy" /> : null}
      {(item.caption || item.alt) ? <figcaption>{text(item.caption) || text(item.alt)}</figcaption> : null}
    </figure>
  )
}

function GalleryView({ component, project }: { component: PresentationComponent; project?: string }) {
  const items = Array.isArray(component.items) ? (component.items as GalleryIR[]) : []
  const columns = Math.min(4, Math.max(1, Number(component.props?.columns ?? 3) || 3))
  const safe = items
    .map((item) => ({ ...item, src: safeURL(item.src) }))
    .filter((item): item is GalleryIR & { src: string } => Boolean(item.src))
  if (safe.length === 0) return <Placeholder what="gallery without safe images" />
  return (
    <div className="presentation-gallery" style={{ gridTemplateColumns: `repeat(${columns}, minmax(0, 1fr))` }}>
      {safe.map((item, i) => <GalleryItem key={i} item={item} project={project} />)}
    </div>
  )
}

interface FileIR { name?: string; href?: string; artifact_id?: string; size?: string }

function FileView({ component, onFile, project }: { component: PresentationComponent; onFile?: (path: string) => void; project?: string }) {
  const props = (component.props ?? {}) as FileIR
  const name = text(props.name)
  if (!name) return <Placeholder what="file without a name" />
  const href = safeURL(props.href)
  const open = () => {
    if (!href) return
    if (onFile) { onFile(href); return }
    // No handler at this call site: in-app files download through the storage
    // API (fetch + object URL) so the auth header rides along.
    const ref = resolveImageSource(href, project)
    if (ref.storage) void storageDownloadFile(ref.storage.project, ref.storage.area, ref.storage.path).catch(() => {})
  }
  return (
    <span className="presentation-file">
      {href ? (
        <button className="chat-attachment-chip presentation-file-chip" onClick={open} title={href}>📎 {name}{props.size ? ` · ${text(props.size)}` : ''}</button>
      ) : (
        <span className="chat-attachment-chip">📎 {name}{props.size ? ` · ${text(props.size)}` : ''}</span>
      )}
      {component.content ? <span className="presentation-file-note">{text(component.content)}</span> : null}
    </span>
  )
}

// ---------- gauge (half-donut arc) ------------------------------------------

interface GaugeIR { value?: number; min?: number; max?: number; label?: string; unit?: string }

function GaugeView({ component }: { component: PresentationComponent }) {
  const data = (component.data ?? {}) as GaugeIR
  const min = Number.isFinite(Number(data.min)) ? Number(data.min) : 0
  const max = Number(data.max) > min ? Number(data.max) : 100
  const value = Number(data.value) || min
  const share = Math.min(1, Math.max(0, (value - min) / (max - min)))
  const size = 160
  const cx = size / 2
  const cy = size / 2 + 10
  const r = size / 2 - 16
  const strokeWidth = r * 0.34
  // Half-donut from 180° to 360° (over the top); SVG arcs cannot sweep a
  // full circle, but a half circle at large-arc=0 works fine.
  const background = `M ${cx - r} ${cy} A ${r} ${r} 0 0 1 ${cx + r} ${cy}`
  const sweep = Math.PI * share
  const endX = cx + r * Math.cos(Math.PI + sweep)
  const endY = cy + r * Math.sin(Math.PI + sweep)
  // The value arc covers sweep = π·share ≤ π — NEVER a large arc. The old
  // `share > 0.5 ? 1 : 0` flag made every gauge above 50% draw the long way
  // around (72% wrapped under the bottom, past the track) — the crooked
  // presentation chart. large-arc stays 0 for the whole half-donut.
  const valuePath = sweep > 0.01 ? `M ${cx - r} ${cy} A ${r} ${r} 0 0 1 ${endX} ${endY}` : ''
  return (
    <figure className="presentation-chart">
      <svg viewBox={`0 0 ${size} ${size * 0.72}`} className="presentation-chart-svg presentation-chart-gauge" role="img">
        <path d={background} fill="none" stroke="var(--tm-border)" strokeWidth={strokeWidth} strokeLinecap="round" />
        {valuePath ? <path d={valuePath} fill="none" stroke="var(--tm-teal)" strokeWidth={strokeWidth} strokeLinecap="round" /> : null}
        <text x={cx} y={cy - 8} textAnchor="middle" className="presentation-gauge-value">{value}{data.unit ? ` ${text(data.unit)}` : ''}</text>
        {data.label ? <text x={cx} y={cy + 12} textAnchor="middle" className="presentation-chart-axis">{text(data.label)}</text> : null}
      </svg>
    </figure>
  )
}

// ---------- gantt (bars over a day grid) -------------------------------------

interface GanttTaskIR { id?: string; label?: string; start?: string; end?: string; status?: string; assignee?: string }

const DAY_MS = 86_400_000

function parseDay(raw: unknown): number | undefined {
  if (typeof raw !== 'string') return undefined
  const t = Date.parse(raw.length === 10 ? `${raw}T00:00:00Z` : raw)
  return Number.isFinite(t) ? t : undefined
}

function GanttView({ component }: { component: PresentationComponent }) {
  const data = (component.data ?? {}) as { tasks?: GanttTaskIR[] }
  const props = (component.props ?? {}) as { title?: string }
  const tasks = Array.isArray(data.tasks) ? data.tasks : []
  const parsed = tasks.map((task) => ({ ...task, startTs: parseDay(task.start), endTs: parseDay(task.end) }))
  const valid = parsed.filter((task) => task.startTs !== undefined && task.endTs !== undefined)
  if (valid.length === 0) return <Placeholder what="gantt tasks need start and end dates" />
  const minStart = Math.min(...valid.map((t) => t.startTs as number))
  const maxEnd = Math.max(...valid.map((t) => t.endTs as number))
  const totalDays = Math.max(1, (maxEnd - minStart) / DAY_MS)
  const labelW = 150
  const width = 520
  const rowH = 26
  const axisH = 22
  const height = axisH + valid.length * rowH + 6
  const chartW = width - labelW - 10
  const x = (ts: number) => labelW + ((ts - minStart) / DAY_MS) * (chartW / totalDays)
  // Ticks: day/week/month steps by scale, labeled dd.mm.
  const tickStep = totalDays > 120 ? 30 : totalDays > 30 ? 7 : 1
  const ticks: number[] = []
  for (let day = 0; day <= totalDays; day += tickStep) ticks.push(minStart + day * DAY_MS)
  return (
    <figure className="presentation-chart">
      {props.title ? <figcaption className="presentation-chart-title">{text(props.title)}</figcaption> : null}
      <svg viewBox={`0 0 ${width} ${height}`} className="presentation-chart-svg presentation-chart-gantt" role="img">
        {ticks.map((ts, i) => (
          <g key={i}>
            <line x1={x(ts)} y1={axisH} x2={x(ts)} y2={height - 4} className="presentation-chart-grid" />
            <text x={x(ts) + 2} y={axisH - 6} className="presentation-chart-axis">{new Date(ts).toISOString().slice(8, 10)}.{new Date(ts).toISOString().slice(5, 7)}</text>
          </g>
        ))}
        <text x={0} y={axisH - 6} className="presentation-chart-axis">Task</text>
        {valid.map((task, i) => {
          const y = axisH + i * rowH + (rowH - 12) / 2
          const startX = x(task.startTs as number)
          const endX = Math.max(x(task.endTs as number), startX + 3)
          const tone = toneOf(task.status)
          const barColor = tone === 'success' ? 'var(--tm-success)' : tone === 'warning' ? 'var(--tm-warning)' : tone === 'danger' ? 'var(--tm-danger)' : tone === 'info' ? 'var(--tm-teal)' : 'var(--tm-chart-1)'
          return (
            <g key={i}>
              <text x={0} y={y + 9} className="presentation-chart-axis">{text(task.label).slice(0, 24)}</text>
              <rect x={startX} y={y} width={endX - startX} height={12} rx={3} fill={barColor} />
              {task.assignee ? <text x={endX + 5} y={y + 9} className="presentation-chart-axis">{text(task.assignee).slice(0, 16)}</text> : null}
            </g>
          )
        })}
      </svg>
    </figure>
  )
}

// ---------- mindmap / state / er / class (mermaid conversions) ---------------

export interface MindNodeIR { label?: string; note?: string; children?: MindNodeIR[] }

// Mermaid's mindmap lexer treats a bare line as a shapeless node: a child
// label that is exactly the `mindmap` keyword (any case) at line start breaks
// the parse, and shape/quote characters (`()[]{}"'#;`) corrupt the node
// grammar the same way. Fragile labels are emitted in the `(text)`
// rounded-shape form — `(mindmap)` parses fine — while safe labels stay
// byte-identical bare text. The root line is already quoted `root((label))`.
const MINDMAP_HOSTILE = /[()[\]{}"'#;]/

/** Mindmap node shape: the rounded `(label)` form — the xmind-like look —
 *  is also the escape hatch for grammar-hostile text (the bare `mindmap`
 *  keyword breaks the lexer; stray brackets break the shape). Anything the
 *  rounded form cannot carry goes into the quoted square form, which takes
 *  arbitrary text after clean()'s quote swap. */
export function sanitizeMindmapLabel(label: string): string {
  if (!/^mindmap$/i.test(label) && !MINDMAP_HOSTILE.test(label)) return `(${label})`
  return `["${label}"]`
}

export function mindmapMermaid(root: MindNodeIR): string | undefined {
  if (!root || !text(root.label)) return undefined
  const clean = (value: unknown) => text(value).replace(/"/g, "'").replace(/\n/g, ' ')
  // The root only accepts the shaped forms (`root((label))` parses; the
  // quoted-square root does NOT), so grammar-hostile characters are stripped
  // from the root label instead of shape-wrapped — the rare edge case trades
  // two brackets for a parseable diagram.
  const rootLabel = clean(root.label).replace(/[()[\]{}'"#;]/g, ' ').replace(/\s+/g, ' ').trim()
  const lines = ['mindmap', `  root((${rootLabel}))`]
  const walk = (node: MindNodeIR, depth: number) => {
    for (const child of node.children ?? []) {
      const label = clean(child.label)
      if (!label) continue
      lines.push(`${'  '.repeat(depth + 2)}${sanitizeMindmapLabel(label)}`)
      walk(child, depth + 1)
    }
  }
  walk(root, 1)
  return lines.join('\n')
}

function MindmapView({ component }: { component: PresentationComponent }) {
  const data = (component.data ?? {}) as { root?: MindNodeIR }
  const source = useMemo(() => mindmapMermaid(data.root ?? {}), [data])
  if (!source) return <Placeholder what="mindmap without a root" />
  return <figure className="presentation-diagram"><MermaidDiagram chart={source} /></figure>
}

interface StateIR { initial?: string; states?: FlowNodeIR[]; transitions?: { from?: string; to?: string; label?: string; dashed?: boolean }[] }

function stateMermaid(data: StateIR): string | undefined {
  const states = Array.isArray(data.states) ? data.states : []
  const transitions = Array.isArray(data.transitions) ? data.transitions : []
  if (states.length === 0 || transitions.length === 0) return undefined
  const ids = new Set(states.map((s) => text(s.id)).filter(Boolean))
  const clean = (value: unknown) => text(value).replace(/"/g, "'").replace(/\n/g, ' ')
  const lines = ['stateDiagram-v2']
  if (data.initial && ids.has(data.initial)) lines.push(`    [*] --> ${data.initial}`)
  for (const t of transitions) {
    const from = text(t.from)
    const to = text(t.to)
    if (!ids.has(from) || !ids.has(to)) continue
    const label = clean(t.label)
    lines.push(label ? `    ${from} --> ${to} : ${label}` : `    ${from} --> ${to}`)
  }
  return lines.length > 1 ? lines.join('\n') : undefined
}

function StateView({ component }: { component: PresentationComponent }) {
  const data = (component.data ?? {}) as StateIR
  const source = useMemo(() => stateMermaid(data), [data])
  const title = text((component.props ?? {}).title)
  if (!source) return <Placeholder what="state diagram without transitions" />
  return (
    <figure className="presentation-diagram">
      {title ? <figcaption className="presentation-chart-title">{title}</figcaption> : null}
      <MermaidDiagram chart={source} />
    </figure>
  )
}

interface ERIR { entities?: { name?: string; attributes?: string[] }[]; relations?: { from?: string; to?: string; label?: string; cardinality?: string }[] }

// cardinalityMermaid maps the plain IR form onto Mermaid's crow's foot.
const CARDINALITY_MAP: Record<string, string> = {
  '1:1': '||--||',
  '1:N': '||--o{',
  'N:1': '}o--||',
  'N:M': '}o--o{',
  '0:1': '|o--o|',
  '0:N': '|o--o{',
}

function erMermaid(data: ERIR): string | undefined {
  const entities = Array.isArray(data.entities) ? data.entities : []
  const relations = Array.isArray(data.relations) ? data.relations : []
  if (entities.length === 0) return undefined
  const sanitize = (name: unknown) => text(name).replace(/[^A-Za-z0-9_]/g, '_')
  const lines = ['erDiagram']
  for (const entity of entities) {
    const name = sanitize(entity.name)
    const attrs = (entity.attributes ?? []).slice(0, 8)
    if (!name || attrs.length === 0) continue
    lines.push(`    ${name} {`)
    for (const attr of attrs) {
      const parts = text(attr).split(/\s+/)
      const type = (parts[0] || 'string').replace(/[^A-Za-z0-9_]/g, '_')
      const rest = parts.slice(1).join('_').replace(/[^A-Za-z0-9_]/g, '_') || 'field'
      lines.push(`        ${type} ${rest}`)
    }
    lines.push('    }')
  }
  for (const relation of relations) {
    const from = sanitize(relation.from)
    const to = sanitize(relation.to)
    if (!from || !to) continue
    const crow = CARDINALITY_MAP[text(relation.cardinality)] ?? '||--o{'
    const label = (text(relation.label).replace(/[^A-Za-z0-9_]/g, '_') || 'relates_to').slice(0, 40)
    lines.push(`    ${from} ${crow} ${to} : ${label}`)
  }
  return lines.length > 1 ? lines.join('\n') : undefined
}

function ERView({ component }: { component: PresentationComponent }) {
  const data = (component.data ?? {}) as ERIR
  const source = useMemo(() => erMermaid(data), [data])
  const title = text((component.props ?? {}).title)
  if (!source) return <Placeholder what="er diagram without entities" />
  return (
    <figure className="presentation-diagram">
      {title ? <figcaption className="presentation-chart-title">{title}</figcaption> : null}
      <MermaidDiagram chart={source} />
    </figure>
  )
}

interface ClassIR { classes?: { name?: string; attributes?: string[]; methods?: string[] }[]; relations?: { from?: string; to?: string; label?: string; kind?: string }[] }

const CLASS_RELATION_MAP: Record<string, string> = {
  inheritance: '<|--',
  composition: '*--',
  association: '-->',
}

function classMermaid(data: ClassIR): string | undefined {
  const classes = Array.isArray(data.classes) ? data.classes : []
  const relations = Array.isArray(data.relations) ? data.relations : []
  if (classes.length === 0) return undefined
  const sanitize = (name: unknown) => text(name).replace(/[^A-Za-z0-9_]/g, '_')
  const lines = ['classDiagram']
  for (const cls of classes) {
    const name = sanitize(cls.name)
    if (!name) continue
    const attrs = (cls.attributes ?? []).slice(0, 10)
    const methods = (cls.methods ?? []).slice(0, 10)
    if (attrs.length === 0 && methods.length === 0) {
      lines.push(`    class ${name}`)
      continue
    }
    lines.push(`    class ${name} {`)
    for (const attr of attrs) lines.push(`        ${text(attr).replace(/[{}]/g, '').slice(0, 60)}`)
    for (const method of methods) lines.push(`        ${text(method).replace(/[{}]/g, '').slice(0, 60)}`)
    lines.push('    }')
  }
  for (const relation of relations) {
    const from = sanitize(relation.from)
    const to = sanitize(relation.to)
    if (!from || !to) continue
    const arrow = CLASS_RELATION_MAP[text(relation.kind)] ?? '-->'
    const label = text(relation.label)
    lines.push(label ? `    ${from} ${arrow} ${to} : ${text(label).slice(0, 40)}` : `    ${from} ${arrow} ${to}`)
  }
  return lines.length > 1 ? lines.join('\n') : undefined
}

function ClassView({ component }: { component: PresentationComponent }) {
  const data = (component.data ?? {}) as ClassIR
  const source = useMemo(() => classMermaid(data), [data])
  const title = text((component.props ?? {}).title)
  if (!source) return <Placeholder what="class diagram without classes" />
  return (
    <figure className="presentation-diagram">
      {title ? <figcaption className="presentation-chart-title">{title}</figcaption> : null}
      <MermaidDiagram chart={source} />
    </figure>
  )
}

// ---------- interactive (§11): buttons and declarative forms -----------------

function ButtonView({ component, onInteract, presentationID }: {
  component: PresentationComponent
  onInteract?: PresentationInteract
  presentationID?: string
}) {
  const action = (component.actions ?? []).find((a) => a.type === 'submit' || a.type === 'select')
  const label = text(component.content) || 'Action'
  if (!action || !onInteract || !presentationID) {
    // Without a handler the button renders as a static chip — the meaning
    // survives, the execution stays in the web UI.
    return <span className="chat-attachment-chip presentation-button-static">{label}{action?.target ? ` → ${text(action.target)}` : ''}</span>
  }
  return (
    <button
      className="presentation-button"
      onClick={() => void onInteract(presentationID, text(component.id), text(action.target), (action.payload as Record<string, unknown>) ?? undefined)}
    >
      {label}
    </button>
  )
}

function FormView({ component, onInteract, presentationID }: {
  component: PresentationComponent
  onInteract?: PresentationInteract
  presentationID?: string
}) {
  const props = (component.props ?? {}) as { schema?: FormSchema; submit?: string }
  const action = (component.actions ?? []).find((a) => a.type === 'submit')
  const schema = props.schema
  if (!schema || typeof schema !== 'object') return <Placeholder what="form without a schema" />
  if (!action || !onInteract || !presentationID) {
    // Static fallback: the fields render disabled and the submit chip names
    // the operation — the same meaning the text fallback carries (§7.6).
    return (
      <div className="presentation-form presentation-form-static">
        {component.content ? <div className="presentation-form-note">{text(component.content)}</div> : null}
        <HumanForm schema={schema} disabled submitLabel={props.submit ?? 'Submit'} onValidSubmit={() => { /* disabled */ }} />
      </div>
    )
  }
  return (
    <div className="presentation-form">
      {component.content ? <div className="presentation-form-note">{text(component.content)}</div> : null}
      <HumanForm
        schema={schema}
        submitLabel={props.submit ?? 'Submit'}
        onValidSubmit={(values) => void onInteract(presentationID, text(component.id), text(action.target), values)}
      />
    </div>
  )
}

// ---------- dispatcher -------------------------------------------------------

export interface PresentationInteract {
  /** Submit one declared action of the presentation (§11): the handler
   *  routes it into the HITL machinery. Returns false on refusal so the
   *  UI can show the error inline. */
  (presentationID: string, componentID: string, target?: string, payload?: Record<string, unknown>): Promise<boolean>
}

export function PresentationBlock({ component, onFile, onInteract, presentationID, project }: {
  component: PresentationComponent
  onFile?: (path: string) => void
  onInteract?: PresentationInteract
  presentationID?: string
  /** Project whose workspace storage serves in-app `/workspace/...` refs. */
  project?: string
}): ReactNode {
  const renderChildren = (children?: PresentationComponent[]): ReactNode => (
    <>{(children ?? []).map((child, i) => <PresentationBlock key={child.id ?? i} component={child} onFile={onFile} onInteract={onInteract} presentationID={presentationID} project={project} />)}</>
  )
  switch (component.type) {
    case 'text': return <TextView component={component} />
    case 'markdown': return <MarkdownView component={component} />
    case 'heading': return <HeadingView component={component} />
    case 'paragraph': return <TextView component={component} />
    case 'list': return <ListView component={component} />
    case 'quote': return <QuoteView component={component} />
    case 'code': return <CodeView component={component} />
    case 'link': return <LinkView component={component} />
    case 'badge': return <BadgeView component={component} />
    case 'divider': return <DividerView />
    case 'table': return <TableView component={component} />
    case 'card': return <CardView component={component} renderChildren={renderChildren} />
    case 'card-grid': return <CardGridView component={component} renderChildren={renderChildren} />
    case 'comparison': return <ComparisonView component={component} />
    case 'key-value': return <KeyValueView component={component} />
    case 'kpi': return <KPIView component={component} />
    case 'progress': return <ProgressView component={component} />
    case 'status': return <StatusView component={component} />
    case 'alert': return <AlertView component={component} />
    case 'accordion': return <AccordionView component={component} />
    case 'chart': case 'line-chart': case 'bar-chart': case 'pie-chart': case 'area-chart': case 'donut-chart':
      return <ChartView component={component} />
    case 'gauge': return <GaugeView component={component} />
    case 'flowchart': case 'dependency-graph': return <FlowchartView component={component} />
    case 'sequence-diagram': return <SequenceView component={component} />
    case 'mindmap': return <MindmapView component={component} />
    case 'state-diagram': return <StateView component={component} />
    case 'er-diagram': return <ERView component={component} />
    case 'class-diagram': return <ClassView component={component} />
    case 'timeline': return <TimelineView component={component} />
    case 'gantt': case 'roadmap': return <GanttView component={component} />
    case 'image': return <ImageView component={component} project={project} />
    case 'image-gallery': return <GalleryView component={component} project={project} />
    case 'file': return <FileView component={component} onFile={onFile} project={project} />
    case 'button': return <ButtonView component={component} onInteract={onInteract} presentationID={presentationID} />
    case 'form': return <FormView component={component} onInteract={onInteract} presentationID={presentationID} />
    default: return <Placeholder what={`unknown component "${text(component.type)}"`} />
  }
}

export const PresentationView = memo(function PresentationView({ document: doc, onFile, onInteract, project }: { document: unknown; onFile?: (path: string) => void; onInteract?: PresentationInteract; project?: string }) {
  if (!doc || typeof doc !== 'object') return null
  const parsed = doc as PresentationDocument
  const content = Array.isArray(parsed.content) ? parsed.content : []
  if (content.length === 0) return null
  return (
    <div className="presentation-view">
      {parsed.title ? <div className="presentation-title">{parsed.title}</div> : null}
      {content.map((component, i) => (
        <PresentationBlock key={component.id ?? i} component={component} onFile={onFile} onInteract={onInteract} presentationID={parsed.id} project={project} />
      ))}
    </div>
  )
})

export default PresentationView

// MarkdownWithPresentations renders artifact markdown: presentation fences
// become live PresentationView blocks (the same pattern mermaid fences use in
// Markdown.tsx), everything else goes through the sanitized Markdown
// component. A fence with invalid JSON stays visible as plain text rather
// than disappearing (docs/presentation-engine.md × docs/artifact-registry.md).
export function MarkdownWithPresentations({ content, project }: { content: string; project?: string }) {
  const blocks = useMemo(() => splitMarkdownBlocks(content), [content])
  return (
    <>
      {blocks.map((block) =>
        block.kind === 'presentation'
          ? <PresentationView key={block.id} document={block.ir} project={project} />
          : <Markdown key={md5like(block.text)} content={block.text} />,
      )}
    </>
  )
}

// md5like builds a stable render key from the block text (position + length
// is enough for React reconciliation; no crypto needed).
function md5like(text: string): string {
  return `md-${text.length}-${text.slice(0, 32).replace(/\W/g, '')}`
}

export { splitMarkdownBlocks } from './markdownBlocks'
