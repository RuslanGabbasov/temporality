import { useEffect, useState, type CSSProperties } from 'react'
import { Button, InlineNotification, Tag } from '@carbon/react'
import { Renew } from '@carbon/icons-react'
import { workspaceApi, type InboundMessage } from './workspaceApi'
import { useT, useI18n } from './i18n'
import { INTAKE_PROJECT_ID } from './kernelApi'

function message(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }

const STATUS_ORDER = ['received', 'clarifying', 'classified', 'routed', 'rejected', 'expired'] as const

const STATUS_TAG: Record<string, 'blue' | 'green' | 'warm-gray' | 'red' | 'purple' | 'magenta'> = {
  received: 'blue',
  clarifying: 'purple',
  classified: 'blue',
  routed: 'green',
  rejected: 'red',
  expired: 'warm-gray',
}

// The list pages in instead of rendering the whole wall of rows; the filter
// chips carry the full per-status counters, so the scale stays visible.
const PAGE_SIZE = 50

/**
 * Intake inbox (docs/inbound-messages.md §11): channel-initiative messages and
 * where the intake pipeline took them — classification, clarification and the
 * routed task/run. The row is the durable state machine; statuses and classes
 * render as badges, links jump into Runs for the actual trajectories.
 */
export default function InboundMessages() {
  const t = useT()
  const { locale } = useI18n()
  const [rows, setRows] = useState<InboundMessage[]>([])
  const [counts, setCounts] = useState<Record<string, number>>({})
  const [userNames, setUserNames] = useState<Record<string, string>>({})
  const [status, setStatus] = useState('')
  const [limit, setLimit] = useState(PAGE_SIZE)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')

  const load = async (withStatus = status, withLimit = limit) => {
    setLoading(true)
    setError('')
    try {
      const data = await workspaceApi.listInboundMessages({
        ...(withStatus ? { status: withStatus } : {}),
        limit: withLimit,
      })
      setRows(data.messages ?? [])
      setCounts(data.counts ?? {})
    } catch (e) {
      setError(message(e))
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => { void load('', PAGE_SIZE) }, [])
  // Sender logins → display names via the user directory (closed world); a
  // failure degrades to plain logins rather than breaking the inbox.
  useEffect(() => {
    void workspaceApi.listUsers()
      .then((data) => setUserNames(Object.fromEntries((data.users ?? []).map((u) => [u.id, u.name]))))
      .catch(() => setUserNames({}))
  }, [])

  const countFor = (s: string) => (s ? (counts[s] ?? 0) : Object.values(counts).reduce((sum, n) => sum + n, 0))
  const remaining = countFor(status) - rows.length

  const selectStatus = (s: string) => {
    setStatus(s)
    setLimit(PAGE_SIZE)
    void load(s, PAGE_SIZE)
  }

  const showMore = () => {
    const next = limit + PAGE_SIZE
    setLimit(next)
    void load(status, next)
  }

  const time = (iso: string) => {
    try { return new Date(iso).toLocaleString(locale === 'ru' ? 'ru-RU' : 'en-GB', { day: '2-digit', month: '2-digit', hour: '2-digit', minute: '2-digit' }) } catch { return iso }
  }

  const statusLabel = (s: string) => t(`inbound.status_${s}`) ?? s
  const classLabel = (c: string) => t(`inbound.class_${c}`) ?? c

  const chipStyle = (active: boolean): CSSProperties => ({
    background: active ? 'var(--tm-teal)' : 'transparent',
    border: '1px solid var(--tm-border)',
    borderRadius: '999px',
    padding: '0.2rem 0.75rem',
    cursor: 'pointer',
    color: active ? '#FFFFFF' : 'var(--tm-text-2)',
    fontSize: '0.75rem',
  })

  const openRun = (project: string, run: string) => {
    const query = new URLSearchParams({ project, run })
    window.history.pushState(null, '', `/agents?${query}`)
    window.dispatchEvent(new PopStateEvent('popstate'))
  }

  return (
    <div className="tm-page">
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', gap: '1rem', flexWrap: 'wrap' }}>
        <div>
          <h2 style={{ marginTop: 0 }}>{t('inbound.title') ?? 'Inbox'}</h2>
          <p style={{ color: 'var(--tm-text-3)', maxWidth: '46rem', margin: 0 }}>
            {t('inbound.intro') ?? 'Initiative messages from communication channels and where the intake pipeline routed them.'}
          </p>
        </div>
        <Button size="sm" kind="secondary" renderIcon={Renew} onClick={() => void load()}>
          {t('action.refresh') ?? 'Refresh'}
        </Button>
      </div>

      <div style={{ display: 'flex', gap: '0.375rem', flexWrap: 'wrap', margin: '1rem 0' }}>
        <button type="button" data-active={status === '' ? 'true' : 'false'} onClick={() => selectStatus('')} style={chipStyle(status === '')}>
          {t('inbound.filter_all') ?? 'All'}{countFor('') > 0 ? ` · ${countFor('')}` : ''}
        </button>
        {STATUS_ORDER.map((s) => (
          <button key={s} type="button" onClick={() => selectStatus(s)} style={chipStyle(status === s)}>
            {statusLabel(s)}{counts[s] ? ` · ${counts[s]}` : ''}
          </button>
        ))}
      </div>

      {error && <InlineNotification kind="error" title={t('common.error') ?? 'Error'} subtitle={error} lowContrast style={{ marginBottom: '1rem' }} />}
      {loading && <p style={{ color: 'var(--tm-text-3)' }}>{t('common.loading') ?? 'Loading…'}</p>}
      {!loading && rows.length === 0 && !error && (
        <p style={{ color: 'var(--tm-text-3)' }}>{t('inbound.empty') ?? 'No messages yet. Messages sent by users in registered channels appear here.'}</p>
      )}

      <div style={{ display: 'flex', flexDirection: 'column', gap: '0.5rem' }}>
        {rows.map((row) => (
          <div key={row.id} style={{ border: '1px solid var(--tm-border)', borderRadius: '6px', padding: '0.6rem 0.85rem', display: 'flex', flexDirection: 'column', gap: '0.35rem' }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', flexWrap: 'wrap' }}>
              <span style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem', fontVariantNumeric: 'tabular-nums' }}>{time(row.created_at)}</span>
              <Tag size="sm" type="gray">{row.channel}</Tag>
              <span style={{ fontSize: '0.8rem' }}>
                {row.sender_user_id
                  ? <strong title={row.sender_user_id}>{userNames[row.sender_user_id] || row.sender_user_id}</strong>
                  : <span title={row.sender_raw} style={{ color: 'var(--tm-text-3)' }}>{row.sender_raw || '—'}</span>}
              </span>
              {row.classification && <Tag size="sm" type="cool-gray">{classLabel(row.classification)}</Tag>}
              <Tag size="sm" type={STATUS_TAG[row.status] ?? 'gray'}>{statusLabel(row.status)}</Tag>
              <span style={{ flex: 1 }} />
              {row.project_id && row.target_run_id && (
                <a
                  href={`/agents?project=${encodeURIComponent(row.project_id)}&run=${encodeURIComponent(row.target_run_id)}`}
                  onClick={(e) => { e.preventDefault(); openRun(row.project_id!, row.target_run_id!) }}
                  style={{ fontSize: '0.75rem' }}
                >
                  {t('inbound.open_task') ?? 'Task run'} →
                </a>
              )}
              {row.intake_run_id && (
                <a
                  href={`/agents?project=${INTAKE_PROJECT_ID}&run=${encodeURIComponent(row.intake_run_id)}`}
                  onClick={(e) => { e.preventDefault(); openRun(INTAKE_PROJECT_ID, row.intake_run_id!) }}
                  style={{ fontSize: '0.75rem', color: 'var(--tm-text-3)' }}
                >
                  intake →
                </a>
              )}
            </div>
            {row.retired_at ? (
              <div style={{ fontSize: '0.8rem', color: 'var(--tm-text-3)', fontStyle: 'italic' }}>
                {t('inbound.retired') ?? 'Message body removed by retention policy'}
              </div>
            ) : (
              <div style={{ fontSize: '0.85rem', whiteSpace: 'pre-wrap', wordBreak: 'break-word' }}>
                {row.body.length > 500 ? row.body.slice(0, 500) + '…' : row.body}
              </div>
            )}
            {(row.attachments?.length ?? 0) > 0 && (
              <div style={{ display: 'flex', gap: '0.25rem', flexWrap: 'wrap' }}>
                {row.attachments!.map((path) => <Tag key={path} size="sm" type="outline">{path.split('/').pop()}</Tag>)}
              </div>
            )}
          </div>
        ))}
      </div>

      {!loading && remaining > 0 && (
        <div style={{ display: 'flex', justifyContent: 'center', marginTop: '0.75rem' }}>
          <Button size="sm" kind="secondary" onClick={showMore}>
            {t('inbound.show_more') ?? 'Show more'} ({remaining})
          </Button>
        </div>
      )}
    </div>
  )
}
