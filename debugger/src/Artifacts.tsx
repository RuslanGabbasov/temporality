import { useEffect, useState, useCallback, type CSSProperties } from 'react'
import {
  Button,
  TextInput,
  TextArea,
  InlineNotification,
  Tag,
  Tile,
  Grid,
  Column,
  Stack,
  Heading,
  Select,
  SelectItem,
} from '@carbon/react'
import PageLoading from './PageLoading'
import { Add, Edit, TrashCan, CheckmarkOutline, CloseOutline } from '@carbon/icons-react'
import { workspaceApi, type Artifact, type ArtifactReview } from './workspaceApi'
import { useT } from './i18n'
import ListFilter, { matchesFilter } from './ListFilter'
import AppModal from './Modal'
import { useConfirm } from './ConfirmDialog'
import { useOrgUnits, OrgUnitSelect, OrgBadge } from './orgUnits'

function message(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }

export function humanArtifactSize(bytes: number): string {
  if (!bytes) return '—'
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KiB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MiB`
}

type ArtifactForm = Partial<Artifact>

const STATUS_ORDER: Record<string, number> = { in_review: 0, draft: 1, rejected: 2, approved: 3 }

function statusTagType(status?: string): 'red' | 'warm-gray' | 'green' | 'purple' {
  switch (status) {
    case 'in_review': return 'purple'
    case 'draft': return 'warm-gray'
    case 'rejected': return 'red'
    default: return 'green' // approved
  }
}

export default function Artifacts() {
  const t = useT()
  const { confirmDialog, dialog: confirmDialogNode } = useConfirm()
  const org = useOrgUnits()
  const [artifacts, setArtifacts] = useState<Artifact[]>([])
  const [references, setReferences] = useState<Record<string, string[]>>({})
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [showForm, setShowForm] = useState(false)
  const [editing, setEditing] = useState<Artifact | null>(null)
  const [form, setForm] = useState<ArtifactForm>({})
  const [filter, setFilter] = useState('')
  const [providerFilter, setProviderFilter] = useState('')
  const [statusFilter, setStatusFilter] = useState('')
  const [orgFilter, setOrgFilter] = useState('')
  const [reviewArtifact, setReviewArtifact] = useState<Artifact | null>(null)
  const [reviews, setReviews] = useState<ArtifactReview[]>([])
  const [submitOpen, setSubmitOpen] = useState(false)
  const [submitReviewers, setSubmitReviewers] = useState('')
  const [submitNote, setSubmitNote] = useState('')
  const [decisionComment, setDecisionComment] = useState('')
  const [busy, setBusy] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const [list, refs] = await Promise.all([
        workspaceApi.listArtifacts(),
        workspaceApi.artifactReferences().catch(() => ({ references: {} as Record<string, string[]> })),
      ])
      setArtifacts(list.artifacts ?? [])
      setReferences(refs.references ?? {})
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }, [])

  useEffect(() => { void load() }, [load])

  // Lifecycle counters drive the status filter chips: how many artifacts sit in
  // each state. Pending reviews first — that is the human queue.
  const statusCounts = artifacts.reduce<Record<string, number>>((acc, a) => {
    const s = a.status ?? 'approved'
    acc[s] = (acc[s] ?? 0) + 1
    return acc
  }, {})
  const orderedStatuses = ['in_review', 'draft', 'rejected', 'approved'].filter((s) => statusCounts[s] > 0)
  const visibleArtifacts = artifacts
    .filter((a) => {
      if (providerFilter && a.provider !== providerFilter) return false
      if (statusFilter && (a.status ?? 'approved') !== statusFilter) return false
      if (orgFilter && (a.org_unit_id ?? '') !== orgFilter) return false
      return matchesFilter(filter, a.title, a.id, a.description)
    })
    .sort((x, y) => (STATUS_ORDER[x.status ?? 'approved'] - STATUS_ORDER[y.status ?? 'approved']) || x.title.localeCompare(y.title))

  const providerLabel = (p: string) =>
    p === 'builtin' ? (t('artifacts.provider_short_builtin') ?? 'In registry')
      : p === 'url' ? (t('artifacts.provider_short_url') ?? 'External (URL)')
        : p

  const statusLabel = (s?: string) => {
    switch (s) {
      case 'in_review': return t('artifacts.status_in_review') ?? 'In review'
      case 'draft': return t('artifacts.status_draft') ?? 'Draft'
      case 'rejected': return t('artifacts.status_rejected') ?? 'Rejected'
      default: return t('artifacts.status_approved') ?? 'Approved'
    }
  }

  const originLabel = (a: Artifact) =>
    a.origin_type === 'agent'
      ? (t('artifacts.origin_agent', { run: a.origin_run ? ` · ${a.origin_run}` : '' }) ?? `agent${a.origin_run ? ` · ${a.origin_run}` : ''}`)
      : (t('artifacts.origin_human', { user: a.origin_user ? ` · ${a.origin_user}` : '' }) ?? `human${a.origin_user ? ` · ${a.origin_user}` : ''}`)

  const chipStyle = (active: boolean): CSSProperties => ({
    background: active ? 'var(--tm-teal)' : 'transparent',
    border: '1px solid var(--tm-border)',
    borderRadius: '999px',
    padding: '0.2rem 0.75rem',
    cursor: 'pointer',
    color: active ? '#FFFFFF' : 'var(--tm-text-2)',
    fontSize: '0.75rem',
  })

  const startCreate = () => {
    setEditing(null)
    setForm({ id: '', title: '', description: '', provider: 'builtin', content: '', mime_type: 'text/markdown', kind: 'reference' })
    setShowForm(true)
  }

  const startEdit = async (a: Artifact) => {
    setEditing(a)
    setForm({ ...a, content: '' })
    setShowForm(true)
    // List rows ship no bodies: pull the document itself for the editor.
    try {
      const page = await workspaceApi.getArtifact(a.id)
      setForm((prev) => ({ ...prev, content: page.artifact.content ?? '' }))
    } catch (f) { setError(message(f)) }
  }

  const save = async () => {
    if (!form.title?.trim()) return
    setLoading(true); setError('')
    try {
      if (editing) {
        await workspaceApi.updateArtifact(editing.id, {
          title: form.title, description: form.description, provider: form.provider,
          external_ref: form.external_ref, content: form.content, mime_type: form.mime_type,
          kind: form.kind, saga_id: form.saga_id,
        })
        // PUT payloads keep the stored org binding: re-scoping goes through
        // the admin binding endpoint, same contract as secrets and skills.
        if (org.isAdmin && (form.org_unit_id ?? '') !== (editing.org_unit_id ?? '')) {
          await workspaceApi.setResourceBinding('artifact', editing.id, form.org_unit_id ?? '')
        }
      } else {
        await workspaceApi.createArtifact({
          id: form.id ?? '', title: form.title, description: form.description,
          provider: form.provider ?? 'builtin', external_ref: form.external_ref,
          content: form.content, mime_type: form.mime_type, org_unit_id: form.org_unit_id,
          kind: form.kind ?? 'reference', saga_id: form.saga_id,
        })
      }
      setShowForm(false); setEditing(null)
      void load()
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }

  const remove = async (a: Artifact) => {
    if (!(await confirmDialog({ title: t('artifacts.delete_confirm', { name: a.title }) ?? `Delete artifact "${a.title}"?`, body: t('action.delete_warning') ?? 'This cannot be undone.', danger: true, confirmLabel: t('action.delete') ?? 'Delete' }))) return
    setLoading(true); setError('')
    try {
      await workspaceApi.deleteArtifact(a.id)
      void load()
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }

  const openReviews = async (a: Artifact) => {
    setReviewArtifact(a)
    setDecisionComment('')
    try {
      const page = await workspaceApi.listArtifactReviews(a.id)
      setReviews(page.reviews ?? [])
    } catch (f) {
      setError(message(f))
      setReviews([])
    }
  }

  const submit = async () => {
    if (!reviewArtifact) return
    const reviewers = submitReviewers.split(',').map((s) => s.trim()).filter(Boolean)
    if (reviewers.length === 0) return
    setBusy(true); setError('')
    try {
      await workspaceApi.submitArtifact(reviewArtifact.id, { reviewers, note: submitNote })
      setSubmitOpen(false); setSubmitReviewers(''); setSubmitNote('')
      await openReviews(reviewArtifact)
      void load()
    } catch (f) { setError(message(f)) }
    finally { setBusy(false) }
  }

  const decide = async (review: ArtifactReview, approve: boolean) => {
    if (!reviewArtifact) return
    setBusy(true); setError('')
    try {
      await workspaceApi.resolveArtifactReview(reviewArtifact.id, review.id, approve, decisionComment)
      setDecisionComment('')
      await openReviews(reviewArtifact)
      void load()
    } catch (f) { setError(message(f)) }
    finally { setBusy(false) }
  }

  const fmtTime = (iso?: string) => {
    if (!iso) return ''
    try { return new Date(iso).toLocaleString() } catch { return iso }
  }

  return (
    <div style={{ padding: '1rem' }}>
      {confirmDialogNode}
      {error && <InlineNotification kind="error" title={t('action.error') ?? 'Error'} subtitle={error} onClose={() => setError('')} lowContrast style={{ marginBottom: '1rem' }} />}

      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '0.5rem' }}>
        <Heading>{t('artifacts.title') ?? 'Reference Artifacts'}</Heading>
        <Button renderIcon={Add} onClick={startCreate}>{t('artifacts.new') ?? 'New Artifact'}</Button>
      </div>
      <div style={{ display: 'flex', gap: '0.5rem', flexWrap: 'wrap', alignItems: 'flex-end', position: 'sticky', top: '3rem', background: 'var(--tm-bg)', zIndex: 1, paddingBottom: '0.5rem', marginBottom: '0.5rem' }}>
        <div style={{ maxWidth: '420px', flex: '1 1 260px' }}>
          <ListFilter value={filter} onChange={setFilter} placeholder={t('artifacts.filter_placeholder') ?? 'Filter artifacts…'} />
        </div>
        {/* Compact toolbar selects in the same style as the knowledge and
            timeline toolbars (.toolbar-select); the Carbon Select used in
            dialogs is too heavy here and its absolutely-positioned chevron
            breaks loose in a flex row. */}
        <label className="toolbar-select">{t('artifacts.filter_provider_label') ?? 'Source'}
          <select value={providerFilter} onChange={(e) => setProviderFilter(e.target.value)}>
            <option value="">{t('artifacts.filter_all_providers') ?? 'All providers'}</option>
            <option value="builtin">{providerLabel('builtin')}</option>
            <option value="url">{providerLabel('url')}</option>
          </select>
        </label>
        {org.units.length > 0 && (
          <label className="toolbar-select">{t('artifacts.filter_org_label') ?? 'Unit'}
            <select value={orgFilter} onChange={(e) => setOrgFilter(e.target.value)}>
              <option value="">{t('artifacts.filter_all_orgs') ?? 'All units'}</option>
              {org.units.map((u) => <option key={u.id} value={u.id}>{u.name}</option>)}
            </select>
          </label>
        )}
      </div>

      {/* Status chips behave like the inbox filters: a click narrows the grid,
          an active chip is filled. Open reviews float to the top of the queue. */}
      {orderedStatuses.length > 1 && (
        <div style={{ display: 'flex', gap: '0.375rem', flexWrap: 'wrap', marginBottom: '0.75rem', marginTop: '0.25rem' }}>
          <button type="button" onClick={() => setStatusFilter('')} style={chipStyle(statusFilter === '')}>
            {t('inbound.filter_all') ?? 'All'} · {artifacts.length}
          </button>
          {orderedStatuses.map((s) => (
            <button key={s} type="button" onClick={() => setStatusFilter(statusFilter === s ? '' : s)} style={chipStyle(statusFilter === s)}>
              {statusLabel(s)} · {statusCounts[s]}
            </button>
          ))}
        </div>
      )}

      {artifacts.length === 0 && !loading && (
        <Tile style={{ textAlign: 'center', padding: '3rem', color: 'var(--tm-text-3)' }}>
          <p>{t('artifacts.empty') ?? 'No reference artifacts yet.'}</p>
          <p style={{ fontSize: '0.8rem', marginTop: '0.5rem' }}>
            {t('artifacts.empty_hint') ?? 'Regulations, approved instructions and checklists live here. Skills reference them from the manifest; agents load the body on demand instead of keeping it in the prompt.'}
          </p>
        </Tile>
      )}

      <Grid>
        {visibleArtifacts.map((a) => {
          const usedBy = references[a.id] ?? []
          const openReview = (a.status ?? '') === 'in_review'
          return (
            <Column key={a.id} sm={4} md={4} lg={4}>
              <Tile style={{ marginBottom: '0.75rem' }}>
                <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start' }}>
                  <div>
                    <div style={{ display: 'flex', alignItems: 'center', gap: '0.3rem', flexWrap: 'wrap' }}>
                      <strong>{a.title}</strong>
                      <OrgBadge orgUnitID={a.org_unit_id} org={org} />
                    </div>
                    <p style={{ fontFamily: 'var(--tm-font-mono, monospace)', fontSize: '0.72rem', color: 'var(--tm-text-3)', marginTop: '0.15rem' }}>
                      {a.id}{a.version && a.version > 1 ? ` · v${a.version}` : ''}
                    </p>
                    {a.description && (
                      <p style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem', marginTop: '0.25rem', wordBreak: 'break-word' }}>{a.description}</p>
                    )}
                    <div style={{ display: 'flex', gap: '0.25rem', flexWrap: 'wrap', marginTop: '0.5rem', alignItems: 'center' }}>
                      <Tag type={statusTagType(a.status)} size="sm" title={`status: ${a.status ?? 'approved'}`}>{statusLabel(a.status)}</Tag>
                      <Tag type={a.provider === 'builtin' ? 'blue' : 'gray'} size="sm" title={a.provider}>{providerLabel(a.provider)}</Tag>
                      {a.kind === 'work' && <Tag type="cool-gray" size="sm">{t('artifacts.kind_work') ?? 'work product'}</Tag>}
                      <Tag type="cool-gray" size="sm">{humanArtifactSize(a.size_bytes)}</Tag>
                      {usedBy.length > 0
                        ? <Tag type="green" size="sm" title={usedBy.join(', ')}>{t('artifacts.used_by', { n: String(usedBy.length) }) ?? `used by ${usedBy.length} skill(s)`}</Tag>
                        : <Tag size="sm">{t('artifacts.not_used') ?? 'not referenced'}</Tag>}
                    </div>
                    <p style={{ fontSize: '0.7rem', marginTop: '0.4rem', color: 'var(--tm-text-3)' }}>
                      {t('artifacts.origin_prefix') ?? 'created by'}: {originLabel(a)}
                      {a.saga_id ? ` · ${t('artifacts.saga_prefix') ?? 'saga'}: ${a.saga_id}` : ''}
                    </p>
                    {a.provider === 'url' && a.external_ref && (
                      <p style={{ fontSize: '0.7rem', marginTop: '0.5rem', wordBreak: 'break-all', color: 'var(--tm-text-3)' }}>{a.external_ref}</p>
                    )}
                  </div>
                  <Stack orientation="horizontal" gap={1}>
                    {(a.status === 'draft' || a.status === 'rejected') && (
                      <Button size="sm" kind="ghost" onClick={() => { setReviewArtifact(a); setSubmitReviewers(''); setSubmitNote(''); setSubmitOpen(true) }}>
                        {t('artifacts.submit_review') ?? 'Send to review'}
                      </Button>
                    )}
                    <Button size="sm" kind="ghost" hasIconOnly renderIcon={Edit} iconDescription={t('action.edit') ?? 'Edit'}
                      disabled={a.status === 'approved' || a.status === 'in_review'}
                      title={a.status === 'approved' ? (t('artifacts.edit_locked_approved') ?? 'The approved body is immutable') : a.status === 'in_review' ? (t('artifacts.edit_locked_review') ?? 'Locked while a review is open') : undefined}
                      onClick={() => void startEdit(a)} />
                    <Button size="sm" kind="danger--ghost" hasIconOnly renderIcon={TrashCan} iconDescription="Delete" onClick={() => void remove(a)} />
                  </Stack>
                </div>
                {(openReview || a.status === 'rejected') && (
                  <div style={{ marginTop: '0.5rem' }}>
                    <Button size="sm" kind="secondary" onClick={() => void openReviews(a)}>
                      {openReview
                        ? (t('artifacts.open_reviews') ?? 'Review queue')
                        : (t('artifacts.review_history') ?? 'Review history')}
                    </Button>
                  </div>
                )}
              </Tile>
            </Column>
          )
        })}
      </Grid>

      {showForm && (
        <AppModal onClose={() => { setShowForm(false); setEditing(null) }} panelStyle={{ width: '640px' }}>
          <Heading>{editing ? (t('artifacts.edit') ?? 'Edit Artifact') : (t('artifacts.new') ?? 'New Artifact')}</Heading>
          <Stack gap={3}>
            <TextInput
              id="artifact-id"
              labelText={t('artifacts.id_label') ?? 'ID'}
              value={form.id ?? ''}
              disabled={!!editing}
              onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, id: e.target.value })}
              placeholder="coding-standard"
              helperText={t('artifacts.id_helper') ?? 'Lowercase letters, digits and dashes; skills reference the artifact by this id'}
              autoFocus
            />
            <TextInput
              id="artifact-title"
              labelText={t('artifacts.title_label') ?? 'Title'}
              value={form.title ?? ''}
              onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, title: e.target.value })}
              placeholder={t('artifacts.title_placeholder') ?? 'Code quality standard'}
            />
            <TextArea
              id="artifact-description"
              labelText={t('artifacts.description_label') ?? 'Applicability (when to read this)'}
              value={form.description ?? ''}
              onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => setForm({ ...form, description: e.target.value })}
              rows={2}
              placeholder={t('artifacts.description_placeholder') ?? 'Read before changing any Go code in this repository'}
              helperText={t('artifacts.description_helper') ?? 'The agent matches the task against this condition — it decides whether artifact_read is worth the tokens'}
            />
            <Select
              id="artifact-kind"
              labelText={t('artifacts.kind_label') ?? 'Kind'}
              value={form.kind ?? 'reference'}
              onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setForm({ ...form, kind: e.target.value as Artifact['kind'] })}
            >
              <SelectItem value="reference" text={t('artifacts.kind_reference') ?? 'reference — regulation, instruction, checklist'} />
              <SelectItem value="work" text={t('artifacts.kind_work_option') ?? 'work — saga work product (requirement, spec, report)'} />
            </Select>
            <Select
              id="artifact-provider"
              labelText={t('artifacts.provider_label') ?? 'Provider'}
              value={form.provider ?? 'builtin'}
              onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setForm({ ...form, provider: e.target.value as Artifact['provider'] })}
            >
              <SelectItem value="builtin" text={t('artifacts.provider_builtin') ?? 'builtin — body stored in the registry (≤ 2 MiB)'} />
              <SelectItem value="url" text={t('artifacts.provider_url') ?? 'url — body lives in an external system; the kernel fetches and caches it'} />
            </Select>
            {(form.provider ?? 'builtin') === 'url' ? (
              <TextInput
                id="artifact-external-ref"
                labelText={t('artifacts.external_ref_label') ?? 'External reference'}
                value={form.external_ref ?? ''}
                onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, external_ref: e.target.value })}
                placeholder="https://wiki.internal.example.com/deploy-runbook"
                helperText={t('artifacts.external_ref_helper') ?? 'Absolute http(s) URL; the sandbox never dials out — the kernel proxies the fetch'}
              />
            ) : (
              <TextArea
                id="artifact-content"
                labelText={t('artifacts.content_label') ?? 'Document body'}
                value={form.content ?? ''}
                onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => setForm({ ...form, content: e.target.value })}
                rows={12}
                placeholder="# Standard…"
                helperText={t('artifacts.content_helper') ?? 'Markdown; served to agents in 32 KiB chunks, never inlined into the prompt'}
                style={{ fontFamily: 'var(--tm-font-mono, monospace)', fontSize: '0.8rem' }}
              />
            )}
            <TextInput
              id="artifact-mime"
              labelText={t('artifacts.mime_label') ?? 'MIME type'}
              value={form.mime_type ?? ''}
              onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, mime_type: e.target.value })}
              placeholder="text/markdown"
            />
            <TextInput
              id="artifact-saga"
              labelText={t('artifacts.saga_label') ?? 'Saga'}
              value={form.saga_id ?? ''}
              onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, saga_id: e.target.value })}
              placeholder="saga-…"
              helperText={editing
                ? (t('artifacts.saga_helper_edit') ?? 'Saga this work product belongs to; clear the field to detach — regulations usually carry none')
                : (t('artifacts.saga_helper') ?? 'Optional: the saga this work product belongs to; company/department regulations do not need one')}
            />
            {org.units.length > 0 && (
              <OrgUnitSelect
                id="artifact-org"
                label={t('artifacts.org_label') ?? 'Availability'}
                org={org}
                value={form.org_unit_id ?? ''}
                onChange={(v: string) => setForm({ ...form, org_unit_id: v })}
              />
            )}
            <div className="form-actions">
              <Button onClick={() => void save()} disabled={!form.title?.trim() || (!editing && !form.id?.trim()) || ((form.provider ?? 'builtin') === 'url' ? !form.external_ref?.trim() : !form.content?.trim())}>{t('action.save') ?? 'Save'}</Button>
              <Button kind="secondary" onClick={() => { setShowForm(false); setEditing(null) }}>{t('action.cancel') ?? 'Cancel'}</Button>
            </div>
          </Stack>
        </AppModal>
      )}

      {reviewArtifact && (
        <AppModal onClose={() => { setReviewArtifact(null); setReviews([]) }} panelStyle={{ width: '680px' }}>
          <Heading>{t('artifacts.reviews_title', { name: reviewArtifact.title }) ?? `Reviews — ${reviewArtifact.title}`}</Heading>
          <p style={{ color: 'var(--tm-text-3)', fontSize: '0.8rem', margin: '0.25rem 0 0.75rem' }}>
            {t('artifacts.reviews_hint') ?? 'Agents create drafts and request review; the decision is always human. mode=all needs every reviewer; mode=any closes on the first decision.'}
          </p>
          <Stack gap={4}>
            {reviews.length === 0 && (
              <p style={{ color: 'var(--tm-text-3)' }}>{t('artifacts.reviews_empty') ?? 'No review rounds yet.'}</p>
            )}
            {reviews.map((r) => (
              <Tile key={r.id}>
                <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: '0.5rem', flexWrap: 'wrap' }}>
                  <div>
                    <Tag type={r.status === 'open' ? 'purple' : r.status === 'approved' ? 'green' : 'red'} size="sm">
                      {r.status === 'open'
                        ? (t('artifacts.review_open') ?? 'open')
                        : r.status === 'approved'
                          ? (t('artifacts.status_approved') ?? 'approved')
                          : (t('artifacts.status_rejected') ?? 'rejected')}
                    </Tag>
                    <span style={{ fontSize: '0.78rem', color: 'var(--tm-text-2)', marginLeft: '0.5rem' }}>
                      v{r.artifact_version} · {t('artifacts.review_mode_label') ?? 'mode'}: {r.mode} · {fmtTime(r.requested_at)}
                    </span>
                  </div>
                  {r.status === 'open' && (
                    <Stack orientation="horizontal" gap={2}>
                      <Button size="sm" renderIcon={CheckmarkOutline} disabled={busy} onClick={() => void decide(r, true)}>{t('artifacts.approve') ?? 'Approve'}</Button>
                      <Button size="sm" kind="danger" renderIcon={CloseOutline} disabled={busy} onClick={() => void decide(r, false)}>{t('artifacts.reject') ?? 'Reject'}</Button>
                    </Stack>
                  )}
                </div>
                <p style={{ fontSize: '0.78rem', color: 'var(--tm-text-2)', marginTop: '0.4rem' }}>
                  {t('artifacts.reviewers_label') ?? 'Reviewers'}: {r.reviewers.join(', ')}
                </p>
                {r.note && <p style={{ fontSize: '0.78rem', color: 'var(--tm-text-3)', marginTop: '0.25rem' }}>{r.note}</p>}
                {Object.entries(r.decisions ?? {}).map(([who, d]) => (
                  <p key={who} style={{ fontSize: '0.75rem', color: 'var(--tm-text-3)', marginTop: '0.25rem' }}>
                    <strong>{who}{d.decided_by && d.decided_by !== who ? ` (${d.decided_by})` : ''}</strong>: {d.decision}{d.comment ? ` — ${d.comment}` : ''} ({fmtTime(d.at)})
                  </p>
                ))}
              </Tile>
            ))}
            {r0OpenReviews(reviews) > 0 && (
              <TextInput
                id="artifact-decision-comment"
                labelText={t('artifacts.decision_comment_label') ?? 'Decision comment (optional)'}
                value={decisionComment}
                onChange={(e: React.ChangeEvent<HTMLInputElement>) => setDecisionComment(e.target.value)}
                placeholder={t('artifacts.decision_comment_placeholder') ?? 'Shown to the requesting agent in the resolution event'}
              />
            )}
          </Stack>
        </AppModal>
      )}

      {submitOpen && reviewArtifact && (
        <AppModal onClose={() => setSubmitOpen(false)} panelStyle={{ width: '560px' }}>
          <Heading>{t('artifacts.submit_title') ?? 'Send to review'}</Heading>
          <Stack gap={3}>
            <p style={{ color: 'var(--tm-text-3)', fontSize: '0.85rem' }}>
              {reviewArtifact.title} · v{reviewArtifact.version ?? 1}
            </p>
            <TextInput
              id="artifact-submit-reviewers"
              labelText={t('artifacts.submit_reviewers_label') ?? 'Reviewers'}
              value={submitReviewers}
              onChange={(e: React.ChangeEvent<HTMLInputElement>) => setSubmitReviewers(e.target.value)}
              placeholder="user:ivanov, project_owner"
              helperText={t('artifacts.submit_reviewers_helper') ?? 'Comma-separated; same recipient syntax as ask_human: user:<id>, org:<unit>, role:<role>, project_owner'}
            />
            <TextArea
              id="artifact-submit-note"
              labelText={t('artifacts.submit_note_label') ?? 'Note'}
              value={submitNote}
              onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => setSubmitNote(e.target.value)}
              rows={2}
              placeholder={t('artifacts.submit_note_placeholder') ?? 'Why this deserves approval now'}
            />
            <div className="form-actions">
              <Button disabled={busy || submitReviewers.trim() === ''} onClick={() => void submit()}>{t('artifacts.submit_review') ?? 'Send to review'}</Button>
              <Button kind="secondary" onClick={() => setSubmitOpen(false)}>{t('action.cancel') ?? 'Cancel'}</Button>
            </div>
          </Stack>
        </AppModal>
      )}

      {loading && <PageLoading />}
    </div>
  )
}

function r0OpenReviews(reviews: ArtifactReview[]): number {
  return reviews.filter((r) => r.status === 'open').length
}
