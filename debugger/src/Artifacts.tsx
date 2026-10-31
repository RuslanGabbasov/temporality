import { useEffect, useMemo, useRef, useState, useCallback, type CSSProperties } from 'react'
import {
  Button,
  TextInput,
  TextArea,
  InlineNotification,
  Tag,
  Tile,
  Stack,
  Heading,
  Select,
  SelectItem,
} from '@carbon/react'
import PageLoading from './PageLoading'
import { Add, Edit, TrashCan, CheckmarkOutline, CloseOutline, Close } from '@carbon/icons-react'
import { workspaceApi, type Artifact, type ArtifactInboxEntry, type ArtifactReview, type ArtifactVersion } from './workspaceApi'
import { whoami, canReconcile } from './kernelApi'
import { useT } from './i18n'
import { shortTime } from './eventSummary'
import ListFilter from './ListFilter'
import AppModal from './Modal'
import { useConfirm } from './ConfirmDialog'
import { useOrgUnits, OrgUnitSelect, OrgBadge } from './orgUnits'
import { ReviewerPicker } from './ReviewerPicker'
import type { User } from './workspaceApi'
import Markdown from './Markdown'

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
  // Tens of thousands of artifacts: the list is a server-side search. q is
  // debounced; tags narrow with AND; «Show more» pages offset.
  const [serverQuery, setServerQuery] = useState('')
  const [tagFilter, setTagFilter] = useState<string[]>([])
  const [tagOptions, setTagOptions] = useState<Array<{ tag: string; count: number }>>([])
  // Creator and age filters: «my artifacts» and the new / this-week /
  // this-month presets — a 10k registry is unnavigable without them.
  // Author filter pick: a person (author=<id>) or an org unit
  // (author_unit=<unit id> — every active member counts as author).
  const [authorPick, setAuthorPick] = useState<{ kind: 'user' | 'unit'; id: string; name: string } | null>(null)
  const [searchFocused, setSearchFocused] = useState(false)
  const [ageFilter, setAgeFilter] = useState<'' | 'week' | 'month' | 'quarter'>('')
  // Approvals inbox: open review rounds addressed to the caller, one tab.
  const [inboxMode, setInboxMode] = useState(false)
  const [inbox, setInbox] = useState<ArtifactInboxEntry[] | null>(null)
  const [inboxBusy, setInboxBusy] = useState(false)
  // Tag editor state + the caller's role (tags drive shared filters, so only
  // operators re-tag; viewers see the tags read-only).
  const [tagDraft, setTagDraft] = useState('')
  const [tagsBusy, setTagsBusy] = useState(false)
  const [operator, setOperator] = useState(false)
  useEffect(() => { void whoami().then((identity) => setOperator(canReconcile(identity.role))).catch(() => setOperator(false)) }, [])
  const saveTags = async (artifact: Artifact, tags: string[]) => {
    setTagsBusy(true)
    try {
      const page = await workspaceApi.setArtifactTags(artifact.id, tags)
      setArtifacts((current) => current.map((a) => (a.id === artifact.id ? { ...page.artifact, content: a.content } : a)))
      setTagDraft('')
      void workspaceApi.artifactTags().then((data) => setTagOptions(data.tags ?? [])).catch(() => { /* dropdown refresh is best-effort */ })
    } catch (f) { setError(message(f)) }
    finally { setTagsBusy(false) }
  }
  const [limit, setLimit] = useState(200)
  const [totalKnown, setTotalKnown] = useState<number | null>(null)
  const searchGen = useRef(0)
  // Two-column layout (docs/artifact-registry.md §8): the left column is the
  // filterable list, the right column is the selected artifact — body first,
  // so an approver reads the document where the decision is made.
  const [selectedId, setSelectedId] = useState('')
  const [selectedBody, setSelectedBody] = useState('')
  const [bodyTruncated, setBodyTruncated] = useState(false)
  const [reviews, setReviews] = useState<ArtifactReview[]>([])
  const [reviewsOpen, setReviewsOpen] = useState(false)
  const [submitOpen, setSubmitOpen] = useState(false)
  // Reviewers as tokens (the exact ask_human syntax underneath); the picker
  // resolves them to chips. The directory loads lazily — only when someone
  // opens the submit dialog.
  const [submitReviewerTokens, setSubmitReviewerTokens] = useState<string[]>([])
  const [directory, setDirectory] = useState<User[]>([])
  // The directory feeds the unified search's author suggestions; it is small
  // (a company roster), one load on mount is fine.
  useEffect(() => { workspaceApi.listUsers().then((data) => setDirectory(data.users ?? [])).catch(() => setDirectory([])) }, [])
  const userNameOf = useCallback((id: string | undefined) => {
    if (!id) return ''
    const user = directory.find((u) => u.id === id || u.name === id)
    return user ? (user.name || user.id) : id
  }, [directory])
  const [submitReviewers, setSubmitReviewers] = useState('')
  const [submitNote, setSubmitNote] = useState('')
  const [decisionComment, setDecisionComment] = useState('')
  const [busy, setBusy] = useState(false)
  // The open revision of the selected artifact (docs/artifact-lifecycle.md
  // §5): while it exists the approved version stays live and the card shows
  // the revision state — edit/review actions target the revision.
  const [revision, setRevision] = useState<ArtifactVersion | null>(null)

  // Deep link (/artifacts?project=…&id=…): the linked artifact opens even
  // when the server page does not include it. Applied once, after the first
  // list lands; afterwards the selection syncs back into the URL so any
  // opened artifact is shareable.
  const deepLinkRef = useRef(new URLSearchParams(window.location.search).get('id') ?? '')
  const applyDeepLink = useCallback(async () => {
    const id = deepLinkRef.current
    deepLinkRef.current = ''
    if (!id) return
    setSelectedId(id)
    if (artifacts.some((a) => a.id === id)) return
    try {
      const page = await workspaceApi.getArtifact(id)
      const full = page.artifact
      setArtifacts((current) => (current.some((a) => a.id === id) ? current : [{ ...full, content: '' }, ...current]))
    } catch { /* gone or inaccessible — the empty selection pane says so */ }
  }, [artifacts])

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const tag = tagFilter.length > 0 ? tagFilter.join(',') : undefined
      const now = new Date()
      const createdAfter = ageFilter === 'week' ? new Date(now.getTime() - 7 * 864e5)
        : ageFilter === 'month' ? new Date(now.getFullYear(), now.getMonth() - 1, now.getDate())
        : ageFilter === 'quarter' ? new Date(now.getFullYear(), now.getMonth() - 3, now.getDate())
        : undefined
      const [list, refs] = await Promise.all([
        workspaceApi.listArtifacts({
          status: statusFilter || undefined,
          provider: providerFilter || undefined,
          org: orgFilter || undefined,
          q: serverQuery.trim() || undefined,
          tag,
          author: authorPick?.kind === 'user' ? authorPick.id : undefined,
          author_unit: authorPick?.kind === 'unit' ? authorPick.id : undefined,
          created_after: createdAfter?.toISOString(),
          limit,
        }),
        workspaceApi.artifactReferences().catch(() => ({ references: {} as Record<string, string[]> })),
      ])
      setArtifacts(list.artifacts ?? [])
      setReferences(refs.references ?? {})
      // count == limit hints there is more; the endpoint pages with offset.
      setTotalKnown(list.count < limit ? (limit >= 200 ? list.count : null) : null)
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }, [statusFilter, providerFilter, orgFilter, serverQuery, tagFilter, authorPick, ageFilter, limit])

  useEffect(() => { void load() }, [load])

  // Approvals inbox: loaded on tab open and refreshed with the list — the
  // decisions made elsewhere (chat/agent) must unstick the rows here. A
  // failure lands in the shared error banner: an empty inbox must be
  // distinguishable from a broken one (the ambiguous-column SQL bug read as
  // «you have nothing to review» for weeks).
  const loadInbox = useCallback(async () => {
    setInboxBusy(true)
    try {
      const page = await workspaceApi.artifactInbox()
      setInbox(page.entries ?? [])
      setError('')
    } catch (f) { setInbox([]); setError(message(f)) }
    finally { setInboxBusy(false) }
  }, [])
  useEffect(() => { if (inboxMode) void loadInbox() }, [inboxMode, loadInbox])

  const firstLoadRef = useRef(true)
  useEffect(() => {
    if (loading || firstLoadRef.current === false) return
    firstLoadRef.current = false
    void applyDeepLink()
  }, [loading, applyDeepLink])

  // Selection → URL: replaceState keeps the back button on the list page.
  useEffect(() => {
    if (!selectedId) return
    const params = new URLSearchParams(window.location.search)
    if (params.get('id') === selectedId) return
    params.set('id', selectedId)
    window.history.replaceState(null, '', `${window.location.pathname}?${params.toString()}`)
  }, [selectedId])

  // Distinct tags for the filter chips, most-used first. Reloaded when the
  // org context changes; failures leave the row empty (filtering is
  // optional, the search box works without it).
  useEffect(() => {
    let cancelled = false
    void workspaceApi.artifactTags()
      .then((data) => { if (!cancelled) setTagOptions(data.tags ?? []) })
      .catch(() => { if (!cancelled) setTagOptions([]) })
    return () => { cancelled = true }
  }, [])

  // Debounced server search: keystrokes land 300ms apart, not per keypress.
  const [searchDraft, setSearchDraft] = useState('')

  // Unified search suggestions: typing a name offers matching authors to pin
  // as a filter (distinct origin_user values of the currently visible
  // artifacts + the user directory's names/emails). The query itself keeps
  // searching titles — the suggestion only appears while it matches someone.
  // One field, two pick kinds: matching directory users AND matching org
  // units (the unit's active members become the author filter server-side).
  const authorSuggest = useMemo(() => {
    const needle = searchDraft.trim().toLowerCase()
    if (needle.length < 2 || authorPick) return []
    const people: Array<{ kind: 'user' | 'unit'; id: string; name: string }> = []
    for (const u of directory ?? []) {
      if (u.active === false) continue
      const haystack = `${u.name} ${u.email}`.toLowerCase()
      if (needle && !haystack.includes(needle)) continue
      people.push({ kind: 'user', id: u.id, name: u.name || u.id })
      if (people.length >= 5) return people
    }
    for (const unit of org.units ?? []) {
      const haystack = `${unit.name} ${unit.id}`.toLowerCase()
      if (needle && !haystack.includes(needle)) continue
      people.push({ kind: 'unit', id: unit.id, name: unit.name || unit.id })
      if (people.length >= 7) break
    }
    return people
  }, [searchDraft, directory, org.units, authorPick])
  useEffect(() => {
    const timer = setTimeout(() => setServerQuery(searchDraft), 300)
    return () => clearTimeout(timer)
  }, [searchDraft])

  const selected = artifacts.find((a) => a.id === selectedId) ?? null

  // The body loads on selection: an approver must read the document in the
  // same view where they decide. url-provider bodies live outside the
  // registry — the reader follows the external reference instead.
  useEffect(() => {
    setSelectedBody('')
    setBodyTruncated(false)
    setReviews([])
    setReviewsOpen(false)
    setDecisionComment('')
    setRevision(null)
    if (!selected) return
    if (selected.provider === 'url') return
    let stale = false
    workspaceApi.getArtifact(selected.id)
      .then((page) => {
        if (stale) return
        setSelectedBody(page.artifact.content ?? '')
        setBodyTruncated(Boolean(page.truncated))
      })
      .catch((f) => { if (!stale) setError(message(f)) })
    if (selected.status === 'approved') {
      workspaceApi.getArtifactRevision(selected.id)
        .then((page) => { if (!stale) setRevision(page.revision ?? null) })
        .catch(() => { if (!stale) setRevision(null) })
    }
    return () => { stale = true }
  }, [selected?.id, selected?.version, selected?.provider]) // eslint-disable-line react-hooks/exhaustive-deps

  // Lifecycle counters drive the status filter chips: how many artifacts sit in
  // each state. Pending reviews first — that is the human queue.
  const statusCounts = artifacts.reduce<Record<string, number>>((acc, a) => {
    const s = a.status ?? 'approved'
    acc[s] = (acc[s] ?? 0) + 1
    return acc
  }, {})
  const orderedStatuses = ['in_review', 'draft', 'rejected', 'approved'].filter((s) => statusCounts[s] > 0)
  // Filtering happens server-side (tens of thousands of rows); the page
  // only sorts the served page the way the registry reads: lifecycle first,
  // then title.
  const visibleArtifacts = [...artifacts]
    .sort((x, y) => (STATUS_ORDER[x.status ?? 'approved'] - STATUS_ORDER[y.status ?? 'approved']) || x.title.localeCompare(y.title))
  const hasMore = totalKnown === null && artifacts.length >= limit

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

  const rowStyle = (active: boolean): CSSProperties => ({
    border: `1px solid ${active ? 'var(--tm-teal)' : 'var(--tm-border)'}`,
    borderRadius: '8px',
    padding: '0.55rem 0.7rem',
    cursor: 'pointer',
    background: active ? 'var(--tm-elevated)' : 'transparent',
    marginBottom: '0.4rem',
  })

  const startCreate = () => {
    setForm({ id: '', title: '', description: '', provider: 'builtin', content: '', mime_type: '', kind: 'reference' })
    setEditing(null)
    setShowForm(true)
  }

  // The single edit action's label follows the state: an approved artifact is
  // edited by opening a revision, a draft/rejected one edits in place, and
  // an open revision continues its draft.
  const editLabel = (status?: string, revisionEditable?: boolean) => {
    if (status === 'approved' && revisionEditable) return t('artifacts.edit_revision_btn') ?? 'Edit revision'
    if (status === 'approved') return t('artifacts.new_revision_btn') ?? 'New revision'
    return t('artifacts.edit_btn') ?? 'Edit'
  }

  const startEdit = async (a: Artifact) => {
    let content = a.content ?? ''
    let form: ArtifactForm = { ...a, content }
    if (a.provider !== 'url') {
      try {
        const page = await workspaceApi.getArtifact(a.id)
        content = page.artifact.content ?? ''
        form = { ...a, content }
      } catch { /* the form still opens with what the listing carries */ }
    }
    // An open revision is the editing surface: the approved body the live
    // row carries is NOT what the revision continues from.
    if (a.status === 'approved') {
      try {
        const page = await workspaceApi.getArtifactRevision(a.id)
        if (page.revision) {
          form = {
            ...a,
            title: page.revision.title,
            description: page.revision.description ?? '',
            provider: (page.revision.provider as Artifact['provider']) ?? a.provider,
            external_ref: page.revision.external_ref ?? a.external_ref,
            content: page.revision.content ?? '',
            mime_type: page.revision.mime_type ?? a.mime_type,
            kind: (page.revision.kind as Artifact['kind']) ?? a.kind,
            saga_id: page.revision.saga_id ?? a.saga_id,
            content_hash: page.revision.content_hash ?? undefined,
            size_bytes: page.revision.size_bytes,
          }
        }
      } catch { /* fall back to the live body */ }
    }
    setForm(form)
    setEditing(a)
    setShowForm(true)
  }

  const save = async () => {
    setError('')
    setBusy(true)
    try {
      if (editing) {
        await workspaceApi.updateArtifact(editing.id, {
          title: form.title,
          description: form.description,
          provider: form.provider,
          external_ref: form.external_ref,
          content: form.content,
          mime_type: form.mime_type,
          kind: form.kind,
          saga_id: form.saga_id,
          org_unit_id: form.org_unit_id,
        })
      } else {
        await workspaceApi.createArtifact({
          id: form.id?.trim() ?? '',
          title: form.title ?? '',
          description: form.description,
          provider: form.provider,
          external_ref: form.external_ref,
          content: form.content,
          mime_type: form.mime_type,
          org_unit_id: form.org_unit_id,
          kind: form.kind,
          saga_id: form.saga_id,
        })
      }
      setShowForm(false)
      setEditing(null)
      await load()
    } catch (f) { setError(message(f)) }
    finally { setBusy(false) }
  }

  const remove = async (a: Artifact) => {
    const ok = await confirmDialog({
      title: t('artifacts.delete_confirm', { name: a.title }),
      body: t('action.delete_warning'),
      danger: true,
      confirmLabel: t('action.delete'),
    })
    if (!ok) return
    setError('')
    try {
      await workspaceApi.deleteArtifact(a.id)
      if (selectedId === a.id) setSelectedId('')
      await load()
    } catch (f) { setError(message(f)) }
  }

  const openReviews = async (a: Artifact) => {
    try {
      const page = await workspaceApi.listArtifactReviews(a.id)
      setReviews(page.reviews ?? [])
      setReviewsOpen(true)
    } catch (f) { setError(message(f)) }
  }

  const submit = async () => {
    if (!selected) return
    setBusy(true)
    try {
      await workspaceApi.submitArtifact(selected.id, { reviewers: submitReviewerTokens, note: submitNote })
      setSubmitOpen(false)
      setSubmitReviewerTokens([])
      setSubmitNote('')
      await load()
    } catch (f) { setError(message(f)) }
    finally { setBusy(false) }
  }

  const decide = async (r: ArtifactReview, approve: boolean) => {
    if (!selected) return
    setBusy(true)
    try {
      await workspaceApi.resolveArtifactReview(selected.id, r.id, approve, decisionComment)
      setDecisionComment('')
      const page = await workspaceApi.listArtifactReviews(selected.id)
      setReviews(page.reviews ?? [])
      await load()
    } catch (f) { setError(message(f)) }
    finally { setBusy(false) }
  }

  const fmtTime = (v?: string) => (v ? new Date(v).toLocaleString() : '')

  return (
    <div style={{ padding: '1rem' }}>
      {confirmDialogNode}
      {error && <InlineNotification kind="error" title={t('action.error') ?? 'Error'} subtitle={error} onCloseButtonClick={() => setError('')} lowContrast style={{ marginBottom: '0.75rem' }} />}

      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '0.75rem' }}>
        <Heading>{t('artifacts.title') ?? 'Reference Artifacts'}</Heading>
        <Button renderIcon={Add} onClick={startCreate}>{t('artifacts.new') ?? 'New Artifact'}</Button>
      </div>

      <div style={{ display: 'grid', gridTemplateColumns: 'minmax(280px, 360px) 1fr', gap: '1rem', alignItems: 'start' }}>
        {/* Left column: filters and the list. Rows carry no buttons — every
            action lives in the detail pane, so nothing ever overflows.
            Same sticky pattern as the runs page: the column is the scroller
            and the filter block pins to its top. */}
        <div style={{ position: 'sticky', top: '3rem', maxHeight: 'calc(100vh - 4rem)', overflowY: 'auto' }}>
          {/* Two views: the whole catalog vs my approvals inbox (rounds
              addressed to me) — a 10k registry buries your two rounds deep. */}
          <div style={{ display: 'flex', gap: '0.375rem', marginBottom: '0.5rem' }}>
            <button type="button" onClick={() => setInboxMode(false)} style={chipStyle(!inboxMode)}>
              {t('artifacts.tab_catalog') ?? 'Каталог'}
            </button>
            <button type="button" onClick={() => setInboxMode(true)} style={chipStyle(inboxMode)}>
              {t('artifacts.tab_inbox') ?? 'На моём согласовании'}{(inbox?.length ?? 0) > 0 && ` · ${inbox!.length}`}
            </button>
          </div>
          {inboxMode ? (
            <>
              {inboxBusy && <Tile style={{ color: 'var(--tm-text-3)' }}>{t('common.loading') ?? 'Loading…'}</Tile>}
              {!inboxBusy && (inbox ?? []).length === 0 && (
                <Tile style={{ color: 'var(--tm-text-3)' }}>{t('artifacts.inbox_empty') ?? 'Открытых согласований на тебя нет.'}</Tile>
              )}
              {(inbox ?? []).map((entry) => (
                <div
                  key={entry.review.id}
                  style={{ ...rowStyle(selectedId === entry.artifact_id), borderLeft: '3px solid var(--tm-amber, #e0af68)' }}
                  onClick={() => { setSelectedId(entry.artifact_id); setInboxMode(false) }}
                >
                  <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: '0.5rem' }}>
                    <strong style={{ fontSize: '0.85rem' }}>{entry.artifact_title}</strong>
                    <Tag type="purple" size="sm">{t('artifacts.inbox_review') ?? 'согласование'}</Tag>
                  </div>
                  <div style={{ fontSize: '0.72rem', color: 'var(--tm-text-3)', marginTop: '0.2rem', display: 'flex', gap: '0.35rem', flexWrap: 'wrap' }}>
                    {entry.review.mode === 'all' ? (t('artifacts.inbox_mode_all') ?? 'все рецензенты') : (t('artifacts.inbox_mode_any') ?? 'любой рецензент')}
                    {' · '}{shortTime(entry.review.requested_at)}
                    {entry.origin_user ? ` · ${entry.origin_user}` : ''}
                  </div>
                  {entry.matched.length > 0 && (
                    <div style={{ fontSize: '0.7rem', color: 'var(--tm-text-2)', marginTop: '0.2rem' }}>
                      {t('artifacts.inbox_matched') ?? 'ты рецензент как'}: {entry.matched.join(', ')}
                    </div>
                  )}
                </div>
              ))}
            </>
          ) : (
            <>
              <div style={{ padding: '0.5rem 0 0', borderBottom: '1px solid var(--tm-border)', position: 'sticky', top: 0, background: 'var(--tm-bg)', zIndex: 1 }}>
                {/* Two rows, the whole filter surface: (1) unified search with
                    suggestions — a query searches title/id/description, and
                    typing offers the matching authors to pin as a filter;
                    (2) status / age / tags combos. Provider and org moved out:
                    where the bytes live is a machine detail, and the org scope
                    is already enforced by visibility. */}
                <div style={{ position: 'relative', marginBottom: '0.4rem' }}>
                  <input
                    value={searchDraft}
                    onChange={(e) => setSearchDraft(e.target.value)}
                    onFocus={() => setSearchFocused(true)}
                    onBlur={() => setTimeout(() => setSearchFocused(false), 150)}
                    placeholder={t('artifacts.search_placeholder') ?? 'Search by name, id, author…'}
                    style={{ width: '100%', border: '1px solid var(--tm-border)', borderRadius: '6px', background: 'var(--tm-bg)', color: 'var(--tm-text)', fontSize: '0.8rem', padding: '0.35rem 0.6rem', outline: 'none' }}
                  />
                  {searchFocused && authorSuggest.length > 0 && (
                    <div style={{ position: 'absolute', left: 0, right: 0, top: '100%', background: 'var(--tm-elevated)', border: '1px solid var(--tm-border)', borderRadius: '6px', zIndex: 3, overflow: 'hidden' }}>
                      {authorSuggest.map((pick) => (
                        <button
                          key={`${pick.kind}:${pick.id}`}
                          type="button"
                          onClick={() => { setAuthorPick(pick); setSearchDraft('') }}
                          style={{ display: 'flex', gap: '0.4rem', alignItems: 'center', width: '100%', padding: '0.35rem 0.6rem', background: 'none', border: 'none', cursor: 'pointer', color: 'var(--tm-text)', fontSize: '0.78rem', textAlign: 'left' }}
                        >
                          {pick.kind === 'user' ? '👤' : '🏢'} {pick.name}
                          <span style={{ color: 'var(--tm-text-3)', fontSize: '0.68rem', marginLeft: 'auto' }}>{pick.kind === 'user' ? (t('artifacts.suggest_author') ?? 'author') : (t('artifacts.suggest_unit') ?? 'org unit')}</span>
                        </button>
                      ))}
                    </div>
                  )}
                  {authorPick && (
                    <button
                      type="button"
                      onClick={() => setAuthorPick(null)}
                      title={t('artifacts.filter_author_label') ?? 'Author'}
                      style={{ position: 'absolute', right: '0.4rem', top: '50%', transform: 'translateY(-50%)', background: 'var(--tm-surface-3, rgba(127,127,127,.15))', border: 'none', borderRadius: 999, color: 'var(--tm-text)', cursor: 'pointer', fontSize: '0.7rem', padding: '0.05rem 0.45rem' }}
                    >{authorPick.kind === 'user' ? '👤' : '🏢'} {authorPick.kind === 'user' ? (userNameOf(authorPick.id) || authorPick.name) : authorPick.name} ✕</button>
                  )}
                </div>
                <div style={{ display: 'flex', gap: '0.4rem', marginBottom: '0.5rem' }}>
                  <label className="toolbar-select" title={t('artifacts.filter_status_label') ?? 'Status'} style={{ flex: 1, minWidth: 0 }}>
                    <select value={statusFilter} onChange={(e) => setStatusFilter(e.target.value)} style={{ width: '100%' }}>
                      <option value="">{t('artifacts.filter_status_all') ?? `Status — all (${artifacts.length})`}</option>
                      {orderedStatuses.map((s) => (
                        <option key={s} value={s}>{statusLabel(s)} ({statusCounts[s] ?? 0})</option>
                      ))}
                    </select>
                  </label>
                  <label className="toolbar-select" title={t('artifacts.filter_age_label') ?? 'Age'} style={{ flex: 1, minWidth: 0 }}>
                    <select value={ageFilter} onChange={(e) => setAgeFilter(e.target.value as typeof ageFilter)} style={{ width: '100%' }}>
                      <option value="">{t('artifacts.filter_age_all') ?? 'Any time'}</option>
                      <option value="week">{t('artifacts.filter_age_week') ?? 'This week'}</option>
                      <option value="month">{t('artifacts.filter_age_month') ?? 'This month'}</option>
                      <option value="quarter">{t('artifacts.filter_age_quarter') ?? 'Last 3 months'}</option>
                    </select>
                  </label>
                  <label className="toolbar-select" title={t('artifacts.filter_tags_label') ?? 'Tags'} style={{ flex: 1.4, minWidth: 0 }}>
                    <select
                      value=""
                      onChange={(e) => {
                        if (e.target.value && !tagFilter.includes(e.target.value)) setTagFilter([...tagFilter, e.target.value])
                      }}
                      style={{ width: '100%' }}
                    >
                      <option value="">{tagFilter.length ? `${t('artifacts.filter_tags_label') ?? 'Tags'}: +${tagFilter.length}` : (t('artifacts.filter_tags_all') ?? 'Tags')}</option>
                      {tagOptions.filter(({ tag }) => !tagFilter.includes(tag)).map(({ tag, count }) => (
                        <option key={tag} value={tag}>#{tag} ({count})</option>
                      ))}
                    </select>
                  </label>
                </div>
                {tagFilter.length > 0 && (
                  <div style={{ display: 'flex', gap: '0.375rem', flexWrap: 'wrap', paddingBottom: '0.5rem' }}>
                    {tagFilter.map((tag) => (
                      <button key={tag} type="button" onClick={() => setTagFilter(tagFilter.filter((x) => x !== tag))} style={chipStyle(true)}>
                        #{tag} ✕
                      </button>
                    ))}
                  </div>
                )}
              </div>
              <div style={{ paddingRight: '0.15rem' }}>
                {visibleArtifacts.map((a) => (
                  <div key={a.id} style={rowStyle(selectedId === a.id)} onClick={() => setSelectedId(a.id)}>
                    <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: '0.5rem' }}>
                      <strong style={{ fontSize: '0.85rem' }}>{a.title}</strong>
                      <Tag type={statusTagType(a.status)} size="sm">{statusLabel(a.status)}</Tag>
                    </div>
                    <div style={{ fontFamily: 'var(--tm-font-mono, monospace)', fontSize: '0.7rem', color: 'var(--tm-text-3)', marginTop: '0.15rem', display: 'flex', gap: '0.5rem', flexWrap: 'wrap' }}>
                      <span>{a.id}{a.version && a.version > 1 ? ` · v${a.version}` : ''}</span>
                      <span>{humanArtifactSize(a.size_bytes)}</span>
                      {(references[a.id] ?? []).length > 0 && <span title={(references[a.id] ?? []).join(', ')}>🔗 {(references[a.id] ?? []).length}</span>}
                    </div>
                    {(a.tags ?? []).length > 0 && (
                      <div style={{ display: 'flex', gap: '0.25rem', flexWrap: 'wrap', marginTop: '0.25rem' }}>
                        {(a.tags ?? []).slice(0, 4).map((tag) => (
                          <span key={tag} style={{ fontSize: '0.66rem', padding: '0.05rem 0.35rem', borderRadius: 999, background: 'var(--tm-surface-3, rgba(127,127,127,.14))', color: 'var(--tm-text-3)' }}>#{tag}</span>
                        ))}
                        {(a.tags ?? []).length > 4 && <span style={{ fontSize: '0.66rem', color: 'var(--tm-text-3)' }}>+{(a.tags ?? []).length - 4}</span>}
                      </div>
                    )}
                  </div>
                ))}
                {visibleArtifacts.length === 0 && !loading && (
                  <p style={{ color: 'var(--tm-text-3)', fontSize: '0.8rem', padding: '0.5rem 0.25rem' }}>
                    {artifacts.length === 0
                      ? (t('artifacts.search_empty') ?? 'Nothing found. Try a shorter query or drop the filters.')
                      : (t('artifacts.filter_empty') ?? 'Nothing matches the filters.')}
                  </p>
                )}
                {hasMore && (
                  <Button size="sm" kind="ghost" style={{ marginTop: '0.35rem' }} onClick={() => setLimit((n) => n + 200)}>
                    {t('artifacts.show_more') ?? 'Show more'}
                  </Button>
                )}
              </div>
            </>
          )}
        </div>

        {/* Right column: the selected artifact — header, actions, the readable
            body and the review rounds in one place, so approving never needs
            a second window. */}
        <div style={{ minWidth: 0 }}>
          {!selected && !loading && (
            <Tile style={{ textAlign: 'center', padding: '3rem', color: 'var(--tm-text-3)' }}>
              {artifacts.length === 0 ? (
                <>
                  <p>{t('artifacts.empty') ?? 'No reference artifacts yet.'}</p>
                  <p style={{ fontSize: '0.8rem', marginTop: '0.5rem' }}>
                    {t('artifacts.empty_hint') ?? 'Regulations, approved instructions and checklists live here. Skills reference them from the manifest; agents load the body on demand instead of keeping it in the prompt.'}
                  </p>
                </>
              ) : (
                <p>{t('artifacts.select_hint') ?? 'Select an artifact on the left to read it.'}</p>
              )}
            </Tile>
          )}

          {selected && (() => {
            const usedBy = references[selected.id] ?? []
            const reviewable = selected.status === 'draft' || selected.status === 'rejected'
            const openReview = (selected.status ?? '') === 'in_review'
            // An open revision keeps the approved version live: edit/review
            // actions target the revision, the card keeps showing approved.
            const revisionOpen = selected.status === 'approved' && revision !== null
            const revisionEditable = revision?.status === 'draft' || revision?.status === 'rejected'
            return (
              <Tile style={{ padding: '1rem 1.1rem' }}>
                <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', gap: '0.75rem', flexWrap: 'wrap' }}>
                  <div style={{ minWidth: 0 }}>
                    <div style={{ display: 'flex', alignItems: 'center', gap: '0.4rem', flexWrap: 'wrap' }}>
                      <Heading style={{ margin: 0, fontSize: '1.05rem' }}>{selected.title}</Heading>
                      <OrgBadge orgUnitID={selected.org_unit_id} org={org} />
                    </div>
                    <p style={{ fontFamily: 'var(--tm-font-mono, monospace)', fontSize: '0.72rem', color: 'var(--tm-text-3)', marginTop: '0.25rem' }}>
                      {selected.id}{selected.version && selected.version > 1 ? ` · v${selected.version}` : ''}
                    </p>
                  </div>
                  				<Stack orientation="horizontal" gap={2}>
                  					{(reviewable || (revisionOpen && revisionEditable)) && (
                  						<Button size="sm" onClick={() => {
                  							setSubmitReviewerTokens([])
                  							setSubmitNote('')
                  							setSubmitOpen(true)
                  							void workspaceApi.listUsers().then((data) => setDirectory(data.users ?? [])).catch(() => setDirectory([]))
                  						}}>
                  							{t('artifacts.submit_review') ?? 'Send to review'}
                  						</Button>
                  					)}
                  					{/* ONE edit action for every state: in_review is locked; approved
                  					 opens a new revision draft (the dialog header says so); draft/
                  					 rejected edits in place. The two pencil buttons that used to sit
                  					 here said different things for the same gesture — the dialog
                  					 now carries the state-specific wording instead. */}
                  					<Button
                  						size="sm"
                  						kind="secondary"
                  						renderIcon={Edit}
                  						disabled={(selected.status ?? '') === 'in_review'}
                  						title={(selected.status ?? '') === 'in_review' ? (t('artifacts.edit_locked_review') ?? 'Locked while a review is open') : undefined}
                  						onClick={() => void startEdit(selected)}
					>{editLabel(selected.status, revisionOpen && revisionEditable)}</Button>
					<Button size="sm" kind="danger--ghost" hasIconOnly renderIcon={TrashCan} iconDescription={t('action.delete') ?? 'Delete'} onClick={() => void remove(selected)} />
                  				</Stack>
                </div>

                <div style={{ display: 'flex', gap: '0.25rem', flexWrap: 'wrap', marginTop: '0.5rem', alignItems: 'center' }}>
                  <Tag type={statusTagType(selected.status)} size="sm" title={`status: ${selected.status ?? 'approved'}`}>{statusLabel(selected.status)}</Tag>
                  <Tag type={selected.provider === 'builtin' ? 'blue' : 'gray'} size="sm" title={selected.provider}>{providerLabel(selected.provider)}</Tag>
                  {selected.kind === 'work' && <Tag type="cool-gray" size="sm">{t('artifacts.kind_work') ?? 'work product'}</Tag>}
                  <Tag type="cool-gray" size="sm">{humanArtifactSize(selected.size_bytes)}</Tag>
                  {usedBy.length > 0
                    ? <Tag type="green" size="sm" title={usedBy.join(', ')}>{t('artifacts.used_by', { n: String(usedBy.length) }) ?? `used by ${usedBy.length} skill(s)`}</Tag>
                    : <Tag size="sm">{t('artifacts.not_used') ?? 'not referenced'}</Tag>}
                </div>

                {/* Tag editor: live on the card, lifecycle-independent — tags
                    drive the catalog filters, locking them behind the review
                    state just breaks other people's navigation. */}
                <div style={{ display: 'flex', flexWrap: 'wrap', gap: '0.3rem', alignItems: 'center', marginTop: '0.5rem' }}>
                  {(selected.tags ?? []).map((tag) => (
                    <span key={tag} style={{ display: 'inline-flex', alignItems: 'center', gap: '0.15rem', padding: '0.1rem 0.3rem 0.1rem 0.45rem', borderRadius: 999, background: 'var(--tm-surface-3, rgba(127,127,127,.15))', fontSize: '0.75rem' }}>
                      #{tag}
                      {operator && (
                        <button
                          type="button"
                          aria-label={`remove tag ${tag}`}
                          disabled={tagsBusy}
                          onClick={() => void saveTags(selected, (selected.tags ?? []).filter((x) => x !== tag))}
                          style={{ background: 'none', border: 0, cursor: 'pointer', color: 'inherit', opacity: 0.7, display: 'inline-flex', padding: 0 }}
                        >✕</button>
                      )}
                    </span>
                  ))}
                  {operator && (
                    <input
                      id="artifact-tag-editor"
                      value={tagDraft}
                      onChange={(e: React.ChangeEvent<HTMLInputElement>) => setTagDraft(e.target.value)}
                      onKeyDown={(e: React.KeyboardEvent<HTMLInputElement>) => {
                        if (e.key === 'Enter' && tagDraft.trim()) {
                          e.preventDefault()
                          void saveTags(selected, [...(selected.tags ?? []), tagDraft])
                        }
                      }}
                      list="artifact-tag-options"
                      placeholder={t('artifacts.tag_add') ?? '+ tag'}
                      style={{ border: '1px dashed var(--tm-border)', borderRadius: 999, background: 'transparent', color: 'var(--tm-text-2)', fontSize: '0.72rem', padding: '0.1rem 0.45rem', width: '7rem', outline: 'none' }}
                    />
                  )}
                  <datalist id="artifact-tag-options">
                    {tagOptions.map(({ tag }) => <option key={tag} value={tag} />)}
                  </datalist>
                </div>
                {/* Everyone who put a hand on this: creator, the run's agent,
                    editors, reviewers — the author filter searches the same
                    set, so a name from here is one click to pin. */}
                {(selected.contributors ?? []).length > 0 && (
                  <div style={{ fontSize: '0.72rem', color: 'var(--tm-text-3)', marginTop: '0.45rem', display: 'flex', gap: '0.3rem', flexWrap: 'wrap', alignItems: 'center' }}>
                    <span>{t('artifacts.contributors') ?? 'Прикладывали руку'}:</span>
                    {(selected.contributors ?? []).map((person) => (
                      <button
                        key={person}
                        type="button"
                        title={t('artifacts.filter_author_label') ?? 'Author'}
                        onClick={() => setAuthorPick({ kind: 'user', id: person, name: userNameOf(person) || person })}
                        style={{ background: 'none', border: 'none', padding: 0, color: 'var(--tm-text-2)', cursor: 'pointer', fontSize: '0.72rem', textDecoration: 'underline dotted' }}
                      >{userNameOf(person) || person}</button>
                    ))}
                  </div>
                )}

                {selected.description && (
                  <p style={{ color: 'var(--tm-text-2)', fontSize: '0.8rem', marginTop: '0.6rem', wordBreak: 'break-word' }}>{selected.description}</p>
                )}
                {revisionOpen && revision && (
                  <div style={{ border: '1px solid var(--tm-border)', borderLeft: '3px solid var(--tm-amber)', borderRadius: '6px', padding: '0.5rem 0.7rem', marginTop: '0.6rem', fontSize: '0.78rem' }}>
                    <strong>{t('artifacts.revision_open_title', { version: String(revision.version) }) ?? `Revision v${revision.version} is open`}</strong>
                    {' · '}{revision.status === 'in_review'
                      ? (t('artifacts.revision_in_review') ?? 'on review — the approved version keeps serving until the round resolves')
                      : (t('artifacts.revision_draft') ?? 'draft — the approved version keeps serving until the revision is approved')}
                  </div>
                )}
                <p style={{ fontSize: '0.7rem', marginTop: '0.4rem', color: 'var(--tm-text-3)' }}>
                  {t('artifacts.origin_prefix') ?? 'created by'}: {originLabel(selected)}
                  {selected.saga_id ? ` · ${t('artifacts.saga_prefix') ?? 'saga'}: ${selected.saga_id}` : ''}
                </p>

                {(openReview || selected.status === 'rejected' || reviewsOpen) && (
                  <div style={{ marginTop: '0.75rem' }}>
                    <Button size="sm" kind="secondary" onClick={() => (reviewsOpen ? setReviewsOpen(false) : void openReviews(selected))}>
                      {openReview
                        ? (t('artifacts.open_reviews') ?? 'Review queue')
                        : (t('artifacts.review_history') ?? 'Review history')}
                    </Button>
                  </div>
                )}

                {reviewsOpen && (
                  <div style={{ marginTop: '0.6rem', borderTop: '1px solid var(--tm-border)', paddingTop: '0.6rem' }}>
                    <Stack gap={3}>
                      {reviews.length === 0 && (
                        <p style={{ color: 'var(--tm-text-3)', fontSize: '0.8rem' }}>{t('artifacts.reviews_empty') ?? 'No review rounds yet.'}</p>
                      )}
                      {reviews.map((r) => (
                        <div key={r.id} style={{ border: '1px solid var(--tm-border)', borderRadius: '8px', padding: '0.6rem 0.75rem' }}>
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
                        </div>
                      ))}
                      {reviews.some((r) => r.status === 'open') && (
                        <TextInput
                          id="artifact-decision-comment"
                          labelText={t('artifacts.decision_comment_label') ?? 'Decision comment (optional)'}
                          value={decisionComment}
                          onChange={(e: React.ChangeEvent<HTMLInputElement>) => setDecisionComment(e.target.value)}
                          placeholder={t('artifacts.decision_comment_placeholder') ?? 'Shown to the requesting agent in the resolution event'}
                        />
                      )}
                    </Stack>
                  </div>
                )}

                {/* The body: the document itself, rendered for reading. */}
                <div style={{ marginTop: '0.75rem', borderTop: '1px solid var(--tm-border)', paddingTop: '0.75rem' }}>
                  {selected.provider === 'url' ? (
                    selected.external_ref
                      ? <p style={{ fontSize: '0.85rem', wordBreak: 'break-all' }}>
                          {t('artifacts.external_body_hint') ?? 'The body lives in the external system:'}{' '}
                          <a href={selected.external_ref} target="_blank" rel="noreferrer">{selected.external_ref}</a>
                        </p>
                      : <p style={{ color: 'var(--tm-text-3)', fontSize: '0.8rem' }}>{t('artifacts.external_body_missing') ?? 'No external reference set.'}</p>
                  ) : selectedBody ? (
                    <>
                      <div className="artifact-body">
                        <Markdown content={selectedBody} />
                      </div>
                      {bodyTruncated && (
                        <p style={{ color: 'var(--tm-text-3)', fontSize: '0.72rem', marginTop: '0.5rem' }}>
                          {t('artifacts.body_truncated') ?? 'The body is shown partially; agents read it in 32 KiB chunks.'}
                        </p>
                      )}
                    </>
                  ) : (
                    <p style={{ color: 'var(--tm-text-3)', fontSize: '0.8rem' }}>{t('artifacts.body_empty') ?? 'No stored body.'}</p>
                  )}
                </div>

                {/* Version history (docs/artifact-lifecycle.md §12): every
                    approved revision is archived when a new revision opens.
                    Bodies are viewable — the point of history is reading what
                    the old version actually said. */}
                <VersionHistory artifactID={selected.id} />
              </Tile>
            )
          })()}
        </div>
      </div>

      {showForm && (
        <AppModal onClose={() => { setShowForm(false); setEditing(null) }} panelStyle={{ width: '640px' }}>
          <Heading>{editing
            ? (editing.status === 'approved' ? (t('artifacts.new_revision_title', { version: String((editing.version ?? 1) + 1) }) ?? `New revision (v${(editing.version ?? 1) + 1})`)
              : (t('artifacts.edit') ?? 'Edit Artifact'))
            : (t('artifacts.new') ?? 'New Artifact')}</Heading>
          {editing?.status === 'approved' && (
            <InlineNotification kind="info" lowContrast hideCloseButton
              title={t('artifacts.new_revision_banner_title') ?? 'New revision'}
              subtitle={t('artifacts.new_revision_banner') ?? 'The edit is saved as draft revision; the approved version keeps serving agents and skills until the revision is approved.'}
              style={{ marginBottom: '0.75rem' }} />
          )}
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
            <div>
              <div style={{ fontSize: '0.78rem', color: 'var(--tm-text-3)', marginBottom: '0.3rem' }}>{t('artifacts.tags_label') ?? 'Tags'}</div>
              <div style={{
                display: 'flex', flexWrap: 'wrap', gap: '0.3rem', alignItems: 'center',
                border: '1px solid var(--tm-border)', borderRadius: '6px', padding: '0.35rem 0.5rem', minHeight: '2.2rem',
              }}>
                {(form.tags ?? []).map((tag) => (
                  <span key={tag} style={{ display: 'inline-flex', alignItems: 'center', gap: '0.2rem', padding: '0.1rem 0.3rem 0.1rem 0.45rem', borderRadius: 999, background: 'var(--tm-surface-3, rgba(127,127,127,.15))', fontSize: '0.78rem' }}>
                    #{tag}
                    <button type="button" aria-label={`remove ${tag}`} onClick={() => setForm({ ...form, tags: (form.tags ?? []).filter((x) => x !== tag) })} style={{ background: 'none', border: 0, cursor: 'pointer', color: 'inherit', opacity: 0.7, display: 'inline-flex', padding: 0 }}>
                      <Close size={12} />
                    </button>
                  </span>
                ))}
                <input
                  id="artifact-tags"
                  placeholder={t('artifacts.tags_placeholder') ?? 'Add a tag and press Enter…'}
                  onKeyDown={(e: React.KeyboardEvent<HTMLInputElement>) => {
                    if (e.key !== 'Enter') return
                    e.preventDefault()
                    const raw = e.currentTarget.value.trim().toLowerCase()
                    if (!raw) return
                    if (!(form.tags ?? []).includes(raw)) setForm({ ...form, tags: [...(form.tags ?? []), raw] })
                    e.currentTarget.value = ''
                  }}
                  style={{ flex: 1, minWidth: '9rem', border: 0, outline: 0, background: 'transparent', color: 'inherit', fontSize: '0.85rem', padding: '0.2rem 0' }}
                />
              </div>
              <p style={{ fontSize: '0.72rem', color: 'var(--tm-text-3)', marginTop: '0.3rem' }}>{t('artifacts.tags_hint') ?? 'Catalog labels: topic, product, team. The registry filters by them with AND.'}</p>
            </div>
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
              <Button onClick={() => void save()} disabled={busy || !form.title?.trim() || (!editing && !form.id?.trim()) || ((form.provider ?? 'builtin') === 'url' ? !form.external_ref?.trim() : !form.content?.trim())}>{t('action.save') ?? 'Save'}</Button>
              <Button kind="secondary" onClick={() => { setShowForm(false); setEditing(null) }}>{t('action.cancel') ?? 'Cancel'}</Button>
            </div>
          </Stack>
        </AppModal>
      )}

      {submitOpen && selected && (
        <AppModal onClose={() => setSubmitOpen(false)} panelStyle={{ width: '560px' }}>
          <Heading>{t('artifacts.submit_title') ?? 'Send to review'}</Heading>
          <Stack gap={3}>
            <p style={{ color: 'var(--tm-text-3)', fontSize: '0.85rem' }}>
              {selected.title} · v{selected.version ?? 1}
            </p>
            <div>
              <div style={{ fontSize: '0.78rem', color: 'var(--tm-text-3)', marginBottom: '0.3rem' }}>{t('artifacts.submit_reviewers_label') ?? 'Reviewers'}</div>
              <ReviewerPicker
                value={submitReviewerTokens}
                onChange={setSubmitReviewerTokens}
                users={directory}
                org={org}
              />
              <p style={{ fontSize: '0.72rem', color: 'var(--tm-text-3)', marginTop: '0.3rem' }}>{t('artifacts.submit_reviewers_helper') ?? 'People by name or title, org units, roles — or type a raw id.'}</p>
            </div>
            <TextArea
              id="artifact-submit-note"
              labelText={t('artifacts.submit_note_label') ?? 'Note'}
              value={submitNote}
              onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => setSubmitNote(e.target.value)}
              rows={2}
              placeholder={t('artifacts.submit_note_placeholder') ?? 'Why this deserves approval now'}
            />
            <div className="form-actions">
              <Button disabled={busy || submitReviewerTokens.length === 0} onClick={() => void submit()}>{t('artifacts.submit_review') ?? 'Send to review'}</Button>
              <Button kind="secondary" onClick={() => setSubmitOpen(false)}>{t('action.cancel') ?? 'Cancel'}</Button>
            </div>
          </Stack>
        </AppModal>
      )}

      {loading && <PageLoading />}
    </div>
  )
}

/** Version history of one artifact (docs/artifact-lifecycle.md §12): archived
 *  approved revisions, newest first. The discloser stays collapsed — history
 *  matters after the first «Новая редакция», not before. A version row
 *  expands into its stored body. */
function VersionHistory({ artifactID }: { artifactID: string }) {
  const t = useT()
  const [open, setOpen] = useState(false)
  const [versions, setVersions] = useState<ArtifactVersion[] | null>(null)
  const [expanded, setExpanded] = useState(0)
  const [error, setError] = useState('')

  useEffect(() => {
    setOpen(false)
    setVersions(null)
    setExpanded(0)
    setError('')
  }, [artifactID])

  useEffect(() => {
    if (!open || versions !== null) return
    let cancelled = false
    ;(async () => {
      try {
        const page = await workspaceApi.listArtifactVersions(artifactID)
        if (!cancelled) setVersions(page.versions ?? [])
      } catch (e) {
        if (!cancelled) setError(e instanceof Error ? e.message : 'Request failed')
      }
    })()
    return () => { cancelled = true }
  }, [open, versions, artifactID])

  return (
    <div style={{ marginTop: '0.75rem', borderTop: '1px solid var(--tm-border)', paddingTop: '0.75rem' }}>
      <button
        onClick={() => setOpen((v) => !v)}
        style={{ background: 'none', border: 'none', color: 'var(--tm-teal)', cursor: 'pointer', fontSize: '0.78rem', padding: 0, display: 'inline-flex', alignItems: 'center', gap: '0.3rem' }}
      >
        <span className="chevron-rotate" style={{ display: 'inline-flex', ...(open ? { transform: 'rotate(90deg)' } : {}) }}>›</span>
        {t('artifacts.versions_title') ?? 'Version history'}
      </button>
      {open && (
        <div style={{ marginTop: '0.4rem' }}>
          {error && <p style={{ color: 'var(--tm-danger)', fontSize: '0.75rem' }}>{error}</p>}
          {versions !== null && versions.length === 0 && (
            <p style={{ color: 'var(--tm-muted)', fontSize: '0.75rem' }}>{t('artifacts.versions_empty') ?? 'No archived revisions yet — history appears when a new revision of an approved artifact opens.'}</p>
          )}
          {versions !== null && versions.map((v) => (
            <div key={v.version} style={{ marginBottom: '0.4rem', border: '1px solid var(--tm-border)', borderRadius: '6px' }}>
              <div
                role="button" tabIndex={0}
                onClick={() => setExpanded(expanded === v.version ? 0 : v.version)}
                style={{ padding: '0.4rem 0.55rem', cursor: 'pointer', display: 'flex', gap: '0.5rem', alignItems: 'baseline', flexWrap: 'wrap', fontSize: '0.75rem' }}
              >
                <strong>v{v.version}</strong>
                <Tag type="green" size="sm">{v.status}</Tag>
                <span style={{ color: 'var(--tm-text-2)' }}>{v.title}</span>
                <span style={{ color: 'var(--tm-muted)', marginLeft: 'auto', fontSize: '0.7rem' }}>
                  {t('artifacts.versions_superseded') ?? 'superseded'} {v.superseded_at ? new Date(v.superseded_at).toLocaleString() : ''}
                </span>
              </div>
              {expanded === v.version && (
                <div style={{ padding: '0 0.55rem 0.55rem' }}>
                  <div className="artifact-body" style={{ border: '1px dashed var(--tm-border)', borderRadius: '6px', padding: '0.5rem', maxHeight: '22rem', overflowY: 'auto' }}>
                    <Markdown content={v.content || ''} />
                  </div>
                </div>
              )}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
