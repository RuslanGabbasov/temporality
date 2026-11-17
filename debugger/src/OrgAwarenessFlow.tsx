import { useCallback, useEffect, useState, type ReactNode } from 'react'
import { Button, Heading, InlineNotification, Tag } from '@carbon/react'
import PageLoading from './PageLoading'
import AppModal from './Modal'
import {
  orgApi,
  type OrgActivityBatch,
  type OrgBatchDecision,
  type OrgDetailLevel,
  type OrgFlowResponse,
  type OrgFlowRow,
  type OrgAwarenessFlowFilters,
} from './orgApi'
import { OrgUnitSelect, useOrgUnits, type OrgUnitsState } from './orgUnits'
import { pluralT, useI18n, useT } from './i18n'
import { briefDateTime, TimeHint } from './timeHint'

// Awareness flow (docs/org-awareness.md): the pipeline that carries context
// between runs — source event → batch → decision → delivery — with the
// manual levers underneath: dismiss a delivery, register the action a run
// took, request richer details from the source run. The Batches section
// exposes the batching machinery itself: manual batch creation and the
// decisions every batch produced.

function message(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }

type TagType = 'red' | 'magenta' | 'purple' | 'blue' | 'cyan' | 'teal' | 'green' | 'gray' | 'cool-gray' | 'warm-gray' | 'high-contrast' | 'outline'

function shortId(value?: string) {
  if (!value) return '—'
  return value.length > 14 ? `${value.slice(0, 13)}…` : value
}

const PAGE_SIZE = 50

const STATUSES = ['selected', 'queued', 'delivered', 'consumed', 'follow_up_requested', 'details_delivered', 'acted_upon', 'dismissed', 'expired', 'failed'] as const
const DECISIONS = ['notify', 'recommend_lookup', 'urgent_steer', 'update_awareness', 'ignore'] as const
const LEVELS: OrgDetailLevel[] = ['summary', 'findings', 'decisions', 'artifacts', 'trace']

const STATUS_KEYS: Record<string, string> = {
  selected: 'orgflow.status_selected',
  queued: 'orgflow.status_queued',
  delivered: 'orgflow.status_delivered',
  consumed: 'orgflow.status_consumed',
  follow_up_requested: 'orgflow.status_follow_up',
  details_delivered: 'orgflow.status_details_delivered',
  acted_upon: 'orgflow.status_acted_upon',
  dismissed: 'orgflow.status_dismissed',
  expired: 'orgflow.status_expired',
  failed: 'orgflow.status_failed',
}

// One tag color per state — ten states must be tellable apart at a glance.
const STATUS_TAGS: Record<string, TagType> = {
  selected: 'blue',
  queued: 'cyan',
  delivered: 'teal',
  consumed: 'green',
  follow_up_requested: 'purple',
  details_delivered: 'magenta',
  acted_upon: 'high-contrast',
  dismissed: 'cool-gray',
  expired: 'warm-gray',
  failed: 'red',
}

const DECISION_KEYS: Record<string, string> = {
  notify: 'orgflow.decision_notify',
  recommend_lookup: 'orgflow.decision_recommend',
  urgent_steer: 'orgflow.decision_steer',
  update_awareness: 'orgflow.decision_update',
  ignore: 'orgflow.decision_ignore',
}

const DECISION_TAGS: Record<string, TagType> = {
  notify: 'blue',
  recommend_lookup: 'cyan',
  urgent_steer: 'red',
  update_awareness: 'purple',
  ignore: 'warm-gray',
}

const LEVEL_KEYS: Record<string, string> = {
  summary: 'orgflow.level_summary',
  findings: 'orgflow.level_findings',
  decisions: 'orgflow.level_decisions',
  artifacts: 'orgflow.level_artifacts',
  trace: 'orgflow.level_trace',
}

/** datetime-local input value → RFC 3339 (UTC); empty stays empty. */
function rfc3339(value: string): string | undefined {
  if (!value) return undefined
  const d = new Date(value)
  return Number.isNaN(d.getTime()) ? undefined : d.toISOString()
}

function ChainBlock({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div style={{ flex: '1 1 200px', minWidth: 0, display: 'flex', flexDirection: 'column', gap: '0.15rem' }}>
      <span style={{ fontSize: '0.6rem', textTransform: 'uppercase', letterSpacing: '0.06em', color: 'var(--tm-text-3)' }}>{label}</span>
      {children}
    </div>
  )
}

function ChainRow({ row, onDismiss, onAction, onDetails, busy }: {
  row: OrgFlowRow
  onDismiss: (row: OrgFlowRow) => void
  onAction: (row: OrgFlowRow) => void
  onDetails: (row: OrgFlowRow) => void
  busy: boolean
}) {
  const t = useT()
  const { delivery, batch, source_event: sourceEvent } = row
  return (
    <div style={{ border: '1px solid var(--tm-border)', borderRadius: 'var(--tm-radius-sm)', padding: '0.5rem 0.6rem', marginBottom: '0.5rem', background: 'var(--tm-surface)' }}>
      <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'stretch', flexWrap: 'wrap' }}>
        <ChainBlock label={t('orgflow.chain_event')}>
          {sourceEvent ? (
            <>
              <span className="mono" style={{ fontSize: '0.68rem', color: 'var(--tm-text-2)' }}>{sourceEvent.event_type ?? '—'}</span>
              <span style={{ fontSize: '0.74rem', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={sourceEvent.summary ?? ''}>{sourceEvent.summary ?? '—'}</span>
              {sourceEvent.occurred_at && (
                <TimeHint value={sourceEvent.occurred_at} style={{ fontSize: '0.66rem', color: 'var(--tm-text-3)', whiteSpace: 'nowrap' }}>
                  {briefDateTime(sourceEvent.occurred_at)}
                </TimeHint>
              )}
            </>
          ) : (
            <span style={{ fontSize: '0.74rem', color: 'var(--tm-text-3)' }}>—</span>
          )}
        </ChainBlock>
        <span style={{ alignSelf: 'center', color: 'var(--tm-text-3)', flexShrink: 0 }}>→</span>
        <ChainBlock label={t('orgflow.chain_batch')}>
          {batch ? (
            <>
              <span className="mono" style={{ fontSize: '0.68rem' }} title={batch.id}>{shortId(batch.id)}</span>
              <span style={{ fontSize: '0.7rem', color: 'var(--tm-text-2)' }}>
                {batch.trigger ?? '—'} · {batch.status}
              </span>
              <span style={{ fontSize: '0.66rem', color: 'var(--tm-text-3)' }}>
                {batch.event_count ?? 0} → {batch.decision_count ?? 0} → {batch.delivery_count ?? 0}
              </span>
            </>
          ) : (
            <span style={{ fontSize: '0.74rem', color: 'var(--tm-text-3)' }}>—</span>
          )}
        </ChainBlock>
        <span style={{ alignSelf: 'center', color: 'var(--tm-text-3)', flexShrink: 0 }}>→</span>
        <ChainBlock label={t('orgflow.chain_decision')}>
          <span style={{ display: 'flex', gap: '0.3rem', alignItems: 'baseline', flexWrap: 'wrap' }}>
            <Tag type={DECISION_TAGS[delivery.decision] ?? 'gray'} size="sm">{t(DECISION_KEYS[delivery.decision] ?? delivery.decision)}</Tag>
            {(delivery.relevance !== undefined) && <span style={{ fontSize: '0.66rem', color: 'var(--tm-text-3)' }} title="relevance">r {delivery.relevance}</span>}
            {(delivery.urgency !== undefined) && <span style={{ fontSize: '0.66rem', color: 'var(--tm-text-3)' }} title="urgency">u {delivery.urgency}</span>}
          </span>
          {delivery.reason && <span style={{ fontSize: '0.7rem', color: 'var(--tm-text-2)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={delivery.reason}>{delivery.reason}</span>}
        </ChainBlock>
        <span style={{ alignSelf: 'center', color: 'var(--tm-text-3)', flexShrink: 0 }}>→</span>
        <ChainBlock label={t('orgflow.chain_delivery')}>
          <span style={{ display: 'flex', gap: '0.3rem', alignItems: 'baseline', flexWrap: 'wrap' }}>
            <Tag type={STATUS_TAGS[delivery.status] ?? 'gray'} size="sm">{t(STATUS_KEYS[delivery.status] ?? delivery.status)}</Tag>
            <span className="mono" style={{ fontSize: '0.68rem' }} title={delivery.target_run_id}>{shortId(delivery.target_run_id)}</span>
          </span>
          {delivery.message && <span style={{ fontSize: '0.7rem', color: 'var(--tm-text-2)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={delivery.message}>{delivery.message}</span>}
          <span style={{ fontSize: '0.66rem', color: 'var(--tm-text-3)', display: 'flex', gap: '0.4rem', flexWrap: 'wrap' }}>
            {delivery.detail_level && <span>{delivery.detail_level}</span>}
            {(delivery.attempts !== undefined) && <span title="attempts">×{delivery.attempts}</span>}
            {delivery.delivered_at && (
              <TimeHint value={delivery.delivered_at}>{briefDateTime(delivery.delivered_at)}</TimeHint>
            )}
            {delivery.consumed_at && (
              <TimeHint value={delivery.consumed_at}>{briefDateTime(delivery.consumed_at)}</TimeHint>
            )}
          </span>
        </ChainBlock>
      </div>
      <div style={{ display: 'flex', gap: '0.4rem', flexWrap: 'wrap', marginTop: '0.45rem', borderTop: '1px dashed var(--tm-border)', paddingTop: '0.4rem' }}>
        <Button size="sm" kind="danger" disabled={busy} onClick={() => onDismiss(row)}>{t('orgflow.dismiss')}</Button>
        <Button size="sm" kind="ghost" disabled={busy} onClick={() => onAction(row)}>{t('orgflow.register_action')}</Button>
        {delivery.source_run_id && (
          <Button size="sm" kind="ghost" disabled={busy} onClick={() => onDetails(row)} title={t('orgflow.details_level')}>
            {t('orgflow.request_details')}
          </Button>
        )}
        {delivery.source_run_id && (
          <span className="mono" style={{ fontSize: '0.66rem', color: 'var(--tm-text-3)', alignSelf: 'center' }} title={delivery.source_run_id}>
            ← {shortId(delivery.source_run_id)}
          </span>
        )}
      </div>
    </div>
  )
}

// embedded renders the flow inside the dashboard's Flow tab: no page
// padding, no page heading — the tab strip above already names it.
export default function OrgAwarenessFlow({ embedded }: { embedded?: boolean }) {
  const { t, locale } = useI18n()
  const org = useOrgUnits()
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [rows, setRows] = useState<OrgFlowRow[]>([])
  const [count, setCount] = useState(0)

  // Filters. target_run arrives from the work board via ?target_run= —
  // initialized from the URL (so the first fetch is already scoped), then
  // re-read on every back/forward navigation.
  const [unit, setUnit] = useState('')
  const [scope, setScope] = useState('subtree')
  const [status, setStatus] = useState('')
  const [decision, setDecision] = useState('')
  const [targetRun, setTargetRun] = useState(() => new URLSearchParams(window.location.search).get('target_run') ?? '')
  const [sourceRun, setSourceRun] = useState('')
  const [batchId, setBatchId] = useState('')
  const [from, setFrom] = useState('')
  const [to, setTo] = useState('')
  const [offset, setOffset] = useState(0)

  // Row actions
  const [busyId, setBusyId] = useState('')
  const [actionFor, setActionFor] = useState<OrgFlowRow | null>(null)
  const [actionRef, setActionRef] = useState('')
  const [actionBusy, setActionBusy] = useState(false)
  const [actionError, setActionError] = useState('')
  const [detailsLevel, setDetailsLevel] = useState<OrgDetailLevel>('summary')
  const [detailsFor, setDetailsFor] = useState<{ row: OrgFlowRow; data: Awaited<ReturnType<typeof orgApi.awarenessDetails>> } | null>(null)

  useEffect(() => {
    const readTarget = () => setTargetRun(new URLSearchParams(window.location.search).get('target_run') ?? '')
    readTarget()
    window.addEventListener('popstate', readTarget)
    return () => window.removeEventListener('popstate', readTarget)
  }, [])

  const load = useCallback(async (opts?: { offset?: number; append?: boolean }) => {
    if (!opts?.append) setLoading(true)
    setError('')
    if (!opts?.append) setOffset(0)
    const filters: OrgAwarenessFlowFilters = {
      org_node: unit || undefined,
      scope,
      status: status || undefined,
      decision: decision || undefined,
      target_run: targetRun.trim() || undefined,
      source_run: sourceRun.trim() || undefined,
      batch_id: batchId.trim() || undefined,
      from: rfc3339(from),
      to: rfc3339(to),
      limit: PAGE_SIZE,
      offset: opts?.offset ?? 0,
    }
    try {
      const page = await orgApi.awarenessFlow(filters)
      setRows((prev) => (opts?.append ? [...prev, ...(page.flow ?? [])] : (page.flow ?? [])))
      setCount(page.count ?? 0)
    } catch (failure) {
      setError(message(failure))
    } finally {
      setLoading(false)
    }
  }, [unit, scope, status, decision, targetRun, sourceRun, batchId, from, to])

  useEffect(() => { void load() }, [load])

  const flipStatus = (deliveryID: string, next: string) => {
    setRows((prev) => prev.map((row) => (row.delivery.id === deliveryID ? { ...row, delivery: { ...row.delivery, status: next } } : row)))
  }

  const dismiss = async (row: OrgFlowRow) => {
    setBusyId(row.delivery.id)
    setActionError('')
    try {
      await orgApi.dismissDelivery(row.delivery.id)
      flipStatus(row.delivery.id, 'dismissed')
    } catch (failure) {
      setActionError(message(failure))
    } finally {
      setBusyId('')
    }
  }

  const submitAction = async () => {
    if (!actionFor || !actionRef.trim()) return
    setActionBusy(true)
    setActionError('')
    try {
      await orgApi.awarenessFollowUp(actionFor.delivery.id, actionRef.trim())
      flipStatus(actionFor.delivery.id, 'acted_upon')
      setActionFor(null)
      setActionRef('')
    } catch (failure) {
      setActionError(message(failure))
    } finally {
      setActionBusy(false)
    }
  }

  const requestDetails = async (row: OrgFlowRow) => {
    const source = row.delivery.source_run_id
    if (!source) return
    setBusyId(row.delivery.id)
    setActionError('')
    try {
      const data = await orgApi.awarenessDetails(source, detailsLevel, row.delivery.id)
      setDetailsFor({ row, data })
    } catch (failure) {
      setActionError(message(failure))
    } finally {
      setBusyId('')
    }
  }

  const clearFilters = () => {
    setUnit(''); setScope('subtree'); setStatus(''); setDecision('')
    setTargetRun(''); setSourceRun(''); setBatchId(''); setFrom(''); setTo('')
  }

  const canMore = rows.length < count

  return (
    <div style={{ padding: embedded ? 0 : '1rem', maxWidth: embedded ? undefined : '86rem', margin: embedded ? undefined : '0 auto' }}>
      {error && (
        <InlineNotification kind="error" title={t('common.error')} subtitle={error} onClose={() => setError('')} lowContrast style={{ marginBottom: '1rem' }} />
      )}
      {actionError && (
        <InlineNotification kind="error" title={t('common.error')} subtitle={actionError} onClose={() => setActionError('')} lowContrast style={{ marginBottom: '1rem' }} />
      )}

      {!embedded && (
      <div style={{ marginBottom: '0.75rem' }}>
        <Heading style={{ fontSize: '1.1rem' }}>{t('orgflow.title')}</Heading>
        <p style={{ color: 'var(--tm-text-3)', fontSize: '0.8rem', margin: '0.25rem 0 0' }}>{t('orgflow.subtitle')}</p>
      </div>
      )}

      <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '0.5rem', padding: '0.5rem 0', borderBottom: '1px solid var(--tm-border)' }}>
        <OrgUnitSelect id="orgflow-unit" compact value={unit} onChange={setUnit} org={org} />
      <label className={`lens-toggle ${scope === 'subtree' ? 'on' : ''}`} title={t('orgops.scope_subtree')}>
        <input type="checkbox" checked={scope === 'subtree'} onChange={(e) => setScope(e.target.checked ? 'subtree' : 'unit')} />
        {t('orgops.include_children')}
      </label>
        <label className="toolbar-select">
          {t('orgflow.filter_status')}
          <select value={status} onChange={(e) => setStatus(e.target.value)}>
            <option value="">{t('orgops.filter_all')}</option>
            {STATUSES.map((s) => <option key={s} value={s}>{t(STATUS_KEYS[s])}</option>)}
          </select>
        </label>
        <label className="toolbar-select">
          {t('orgflow.filter_decision')}
          <select value={decision} onChange={(e) => setDecision(e.target.value)}>
            <option value="">{t('orgops.filter_all')}</option>
            {DECISIONS.map((d) => <option key={d} value={d}>{t(DECISION_KEYS[d])}</option>)}
          </select>
        </label>
        <input className="toolbar-search" placeholder={t('orgflow.filter_target_run')} value={targetRun} onChange={(e) => setTargetRun(e.target.value)} />
        <input className="toolbar-search" placeholder={t('orgflow.filter_source_run')} value={sourceRun} onChange={(e) => setSourceRun(e.target.value)} />
        <input className="toolbar-search" placeholder={t('orgflow.filter_batch')} value={batchId} onChange={(e) => setBatchId(e.target.value)} />
        <label className="toolbar-select">
          {t('orgops.period_from')}
          <input type="datetime-local" className="toolbar-search" style={{ width: 185 }} value={from} onChange={(e) => setFrom(e.target.value)} />
        </label>
        <label className="toolbar-select">
          {t('orgops.period_to')}
          <input type="datetime-local" className="toolbar-search" style={{ width: 185 }} value={to} onChange={(e) => setTo(e.target.value)} />
        </label>
        <label className="toolbar-select">
          {t('orgflow.details_level')}
          <select value={detailsLevel} onChange={(e) => setDetailsLevel(e.target.value as OrgDetailLevel)}>
            {LEVELS.map((level) => <option key={level} value={level}>{t(LEVEL_KEYS[level])}</option>)}
          </select>
        </label>
        <button className="toolbar-btn" onClick={() => void load()} disabled={loading} title={t('common.refresh')}>↻</button>
        <button className="toolbar-btn" onClick={clearFilters}>{t('common.clear_filters')}</button>
        <span style={{ marginLeft: 'auto', fontSize: '0.7rem', color: 'var(--tm-text-3)' }}>
          {count > 0 && pluralT(t, locale, 'orgflow.deliveries_n', count)}
        </span>
      </div>

      {loading ? <PageLoading /> : (
        <>
          {rows.length === 0 ? (
            <div style={{ color: 'var(--tm-text-3)', fontSize: '0.8rem', padding: '1.5rem 0', textAlign: 'center' }}>
              {t('orgflow.empty')}
            </div>
          ) : (
            <div style={{ marginTop: '0.75rem' }}>
              {rows.map((row) => (
                <ChainRow
                  key={row.delivery.id}
                  row={row}
                  busy={busyId === row.delivery.id}
                  onDismiss={(r) => void dismiss(r)}
                  onAction={(r) => { setActionRef(''); setActionError(''); setActionFor(r) }}
                  onDetails={(r) => void requestDetails(r)}
                />
              ))}
            </div>
          )}

          {canMore && (
            <div style={{ display: 'flex', justifyContent: 'center', marginTop: '0.75rem' }}>
              <Button
                size="sm"
                kind="ghost"
                onClick={() => {
                  const next = offset + PAGE_SIZE
                  setOffset(next)
                  void load({ offset: next, append: true })
                }}
              >
                {t('orgops.load_more')}
              </Button>
            </div>
          )}
        </>
      )}

      <BatchesSection unit={unit} org={org} />

      {/* Register action: the run did something with the delivered context —
          record what, so the pipeline learns the delivery landed. */}
      {actionFor && (
        <AppModal onClose={() => { setActionFor(null); setActionError('') }} panelStyle={{ width: '26rem' }}>
          <h3 style={{ marginTop: 0, fontSize: '1rem' }}>{t('orgflow.action_title')}</h3>
          <p style={{ fontSize: '0.74rem', color: 'var(--tm-text-3)', display: 'flex', gap: '0.4rem', flexWrap: 'wrap' }}>
            <span className="mono" title={actionFor.delivery.target_run_id}>{shortId(actionFor.delivery.target_run_id)}</span>
            {actionFor.delivery.source_run_id && (
              <span className="mono" title={actionFor.delivery.source_run_id}>← {shortId(actionFor.delivery.source_run_id)}</span>
            )}
          </p>
          <label style={{ display: 'grid', gap: '0.25rem', fontSize: '0.72rem', color: 'var(--tm-text-2)' }}>
            {t('orgflow.action_ref_label')}
            <input
              className="toolbar-search"
              style={{ width: '100%' }}
              value={actionRef}
              autoFocus
              placeholder={t('orgflow.action_ref_placeholder')}
              onChange={(e) => setActionRef(e.target.value)}
            />
          </label>
          {actionError && (
            <InlineNotification kind="error" title={t('common.error')} subtitle={actionError} lowContrast style={{ marginTop: '0.6rem' }} />
          )}
          <div style={{ display: 'flex', justifyContent: 'flex-end', gap: '0.5rem', marginTop: '0.8rem' }}>
            <Button size="sm" kind="ghost" onClick={() => { setActionFor(null); setActionError('') }}>{t('orgflow.action_cancel')}</Button>
            <Button size="sm" kind="primary" disabled={!actionRef.trim() || actionBusy} onClick={() => void submitAction()}>
              {actionBusy ? '…' : t('orgflow.action_confirm')}
            </Button>
          </div>
        </AppModal>
      )}

      {/* Requested details: the source run's context, pretty-printed. */}
      {detailsFor && (
        <AppModal onClose={() => setDetailsFor(null)} panelStyle={{ width: 'min(46rem, 92vw)' }}>
          <h3 style={{ marginTop: 0, fontSize: '1rem' }}>{t('orgflow.details_title')}</h3>
          <p style={{ fontSize: '0.74rem', color: 'var(--tm-text-3)', display: 'flex', gap: '0.4rem', alignItems: 'baseline', flexWrap: 'wrap' }}>
            <span className="mono" title={detailsFor.data.run_id ?? detailsFor.row.delivery.source_run_id}>
              {detailsFor.data.run_id ?? detailsFor.row.delivery.source_run_id}
            </span>
            <Tag type="purple" size="sm">{t(LEVEL_KEYS[detailsFor.data.level ?? detailsLevel] ?? detailsFor.data.level ?? detailsLevel)}</Tag>
          </p>
          <pre style={{ maxHeight: '55vh', overflow: 'auto', fontSize: '0.7rem', background: 'var(--tm-surface-2)', padding: '0.6rem', borderRadius: 'var(--tm-radius-sm)', whiteSpace: 'pre-wrap', wordBreak: 'break-word', margin: 0 }}>
            {JSON.stringify(detailsFor.data.details ?? detailsFor.data, null, 2)}
          </pre>
          <div style={{ display: 'flex', justifyContent: 'flex-end', marginTop: '0.6rem' }}>
            <Button size="sm" onClick={() => setDetailsFor(null)}>{t('action.close')}</Button>
          </div>
        </AppModal>
      )}
    </div>
  )
}

// ── Batches: the machinery under the flow ───────────────────────────────────

function BatchesSection({ unit, org }: { unit: string; org: OrgUnitsState }) {
  const { t } = useI18n()
  const [open, setOpen] = useState(false)
  const [batches, setBatches] = useState<OrgActivityBatch[]>([])
  const [overrides, setOverrides] = useState<Record<string, OrgActivityBatch>>({})
  const [decisions, setDecisions] = useState<Record<string, OrgBatchDecision[]>>({})
  const [expanded, setExpanded] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)
  const [creating, setCreating] = useState(false)
  const [busyBatch, setBusyBatch] = useState('')
  const [error, setError] = useState('')
  const [note, setNote] = useState('')

  const load = useCallback(async () => {
    setLoading(true)
    setError('')
    try {
      const page = await orgApi.activityBatches({ org_node: unit || undefined, limit: 20 })
      setBatches(page.batches ?? [])
    } catch (failure) {
      setError(message(failure))
    } finally {
      setLoading(false)
    }
  }, [unit])

  useEffect(() => {
    if (open) void load()
  }, [open, load])

  const create = async () => {
    if (!unit) return
    setCreating(true)
    setError('')
    try {
      const result = await orgApi.createActivityBatch(unit)
      setNote(result.batch_id ?? '')
      await load()
    } catch (failure) {
      setError(message(failure))
    } finally {
      setCreating(false)
    }
  }

  const toggleBatch = async (batchID: string) => {
    if (expanded === batchID) { setExpanded(null); return }
    setExpanded(batchID)
    if (decisions[batchID]) return
    setBusyBatch(batchID)
    setError('')
    try {
      // Refresh the batch itself (its counters move while it processes) and
      // fetch the decisions it produced.
      const [single, page] = await Promise.all([
        orgApi.activityBatch(batchID).catch(() => null),
        orgApi.activityBatchDecisions(batchID),
      ])
      if (single) setOverrides((prev) => ({ ...prev, [batchID]: single }))
      setDecisions((prev) => ({ ...prev, [batchID]: page.decisions ?? [] }))
    } catch (failure) {
      setError(message(failure))
    } finally {
      setBusyBatch('')
    }
  }

  return (
    <div style={{ marginTop: '1.25rem', borderTop: '1px solid var(--tm-border)', paddingTop: '0.6rem' }}>
      <div style={{ display: 'flex', alignItems: 'baseline', gap: '0.6rem', flexWrap: 'wrap' }}>
        <button
          type="button"
          onClick={() => setOpen((v) => !v)}
          style={{ background: 'none', border: 'none', padding: 0, cursor: 'pointer', fontSize: '0.8rem', color: 'var(--tm-teal)', display: 'flex', alignItems: 'baseline', gap: '0.4rem' }}
        >
          <span>{open ? '▾' : '▸'}</span>
          <span>{t('orgflow.batches_title')}</span>
        </button>
        <span style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)' }}>{t('orgflow.batches_hint')}</span>
        <span style={{ marginLeft: 'auto' }} title={unit ? undefined : t('orgflow.batch_need_unit')}>
          <Button size="sm" kind="primary" disabled={!unit || creating} onClick={() => void create()}>
            {creating ? '…' : t('orgflow.batch_create')}
          </Button>
        </span>
      </div>

      {note && (
        <InlineNotification kind="success" lowContrast title={t('orgflow.batch_created', { id: note })} onClose={() => setNote('')} style={{ marginTop: '0.5rem' }} />
      )}
      {open && error && (
        <InlineNotification kind="error" lowContrast title={t('common.error')} subtitle={error} onClose={() => setError('')} style={{ marginTop: '0.5rem' }} />
      )}

      {open && (
        loading && batches.length === 0 ? <PageLoading /> : (
          <>
            {batches.length === 0 ? (
              <div style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem', padding: '0.5rem 0' }}>{t('orgflow.batches_empty')}</div>
            ) : (
              <div style={{ marginTop: '0.5rem', display: 'flex', flexDirection: 'column', gap: '0.4rem' }}>
                {batches.map((listBatch) => {
                  const batch = overrides[listBatch.id] ?? listBatch
                  const isOpen = expanded === batch.id
                  const batchDecisions = decisions[batch.id]
                  return (
                    <div key={batch.id} style={{ border: '1px solid var(--tm-border)', borderRadius: 'var(--tm-radius-sm)', background: 'var(--tm-surface)' }}>
                      <button
                        type="button"
                        onClick={() => void toggleBatch(batch.id)}
                        style={{ width: '100%', display: 'flex', gap: '0.5rem', alignItems: 'baseline', flexWrap: 'wrap', background: 'none', border: 'none', padding: '0.45rem 0.6rem', cursor: 'pointer', fontSize: '0.76rem', color: 'var(--tm-text)', textAlign: 'left' }}
                      >
                        <span style={{ color: 'var(--tm-text-3)' }}>{isOpen ? '▾' : '▸'}</span>
                        <span className="mono" style={{ fontSize: '0.7rem' }} title={batch.id}>{shortId(batch.id)}</span>
                        {batch.status && <Tag type={batch.empty ? 'warm-gray' : 'blue'} size="sm">{batch.status}</Tag>}
                        {batch.trigger && <span style={{ color: 'var(--tm-text-2)' }}>{batch.trigger}</span>}
                        {batch.org_node_id && <span style={{ color: 'var(--tm-text-3)', fontSize: '0.7rem' }}>{org.nameOf(batch.org_node_id)}</span>}
                        <span style={{ color: 'var(--tm-text-3)', fontSize: '0.68rem' }}>
                          {batch.event_count ?? 0} → {batch.decision_count ?? 0} → {batch.delivery_count ?? 0}
                        </span>
                        {batch.created_at && (
                          <TimeHint value={batch.created_at} style={{ color: 'var(--tm-text-3)', fontSize: '0.68rem', marginLeft: 'auto' }}>
                            {briefDateTime(batch.created_at)}
                          </TimeHint>
                        )}
                      </button>
                      {isOpen && (
                        <div style={{ borderTop: '1px solid var(--tm-border)', padding: '0.4rem 0.6rem' }}>
                          {busyBatch === batch.id && !batchDecisions ? (
                            <PageLoading />
                          ) : (batchDecisions ?? []).length === 0 ? (
                            <div style={{ color: 'var(--tm-text-3)', fontSize: '0.74rem' }}>{t('orgflow.batch_decisions_empty')}</div>
                          ) : (
                            <div style={{ display: 'flex', flexDirection: 'column', gap: '0.25rem' }}>
                              {batchDecisions!.map((dec, i) => (
                                <div key={dec.id ?? `${batch.id}-${i}`} style={{ display: 'flex', gap: '0.5rem', alignItems: 'baseline', flexWrap: 'wrap', fontSize: '0.74rem' }}>
                                  <Tag type={DECISION_TAGS[dec.decision ?? ''] ?? 'gray'} size="sm">{t(DECISION_KEYS[dec.decision ?? ''] ?? dec.decision ?? '—')}</Tag>
                                  {dec.target_run_id && <span className="mono" style={{ fontSize: '0.68rem' }} title={dec.target_run_id}>{shortId(dec.target_run_id)}</span>}
                                  {(dec.relevance !== undefined) && <span style={{ fontSize: '0.66rem', color: 'var(--tm-text-3)' }}>r {dec.relevance}</span>}
                                  {(dec.urgency !== undefined) && <span style={{ fontSize: '0.66rem', color: 'var(--tm-text-3)' }}>u {dec.urgency}</span>}
                                  {dec.reason && <span style={{ color: 'var(--tm-text-2)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={dec.reason}>{dec.reason}</span>}
                                  {dec.delivery_id && <span className="mono" style={{ fontSize: '0.64rem', color: 'var(--tm-text-3)' }} title={dec.delivery_id}>d: {shortId(dec.delivery_id)}</span>}
                                </div>
                              ))}
                            </div>
                          )}
                        </div>
                      )}
                    </div>
                  )
                })}
              </div>
            )}
          </>
        )
      )}
    </div>
  )
}
