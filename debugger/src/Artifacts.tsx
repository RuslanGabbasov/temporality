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
import { Add, Edit, TrashCan, CheckmarkOutline, CloseOutline, Close, Download, Chat, Checkmark, WarningFilled, Restart } from '@carbon/icons-react'
import { workspaceApi, exportArtifactMarkdown, type Artifact, type ArtifactAnnotation, type ArtifactInboxEntry, type ArtifactReview, type ArtifactVersion } from './workspaceApi'
import { MarkdownWithPresentations } from './PresentationView'
import { whoami, canReconcile } from './kernelApi'
import { useT, uiLocale } from './i18n'
import { shortTime } from './eventSummary'
import { TimeHint, fullDateTime, briefDateTime } from './timeHint'
import ListFilter from './ListFilter'
import AppModal from './Modal'
import { useConfirm } from './ConfirmDialog'
import { useOrgUnits, OrgUnitSelect, OrgBadge } from './orgUnits'
import { ReviewerPicker } from './ReviewerPicker'
import { SagaComboBox } from './ProjectOverview'
import RichDiff from './RichDiff'
import { ArtifactEditor } from './ArtifactEditor'
import type { User } from './workspaceApi'
import Markdown from './Markdown'
import { useFragmentNoteMenu } from './useFragmentNoteMenu'
import { projectBudget, type SagaRollup } from './kernelApi'

function message(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }

export function humanArtifactSize(bytes: number): string {
  if (!bytes) return '—'
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KiB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MiB`
}

// ── Fragment review highlights (docs/artifact-lifecycle.md §7) ─────────────
// Annotated ranges in the rendered body carry a persistent <mark> tinted by
// verdict kind: the document itself shows what reviewers flagged, the rail
// below is the index. The anchors are text offsets of the RENDERED document
// (the same coordinates the kernel stores at capture time), so the marks are
// re-wrapped after every render — markdown re-renders discard foreign DOM.
function stripAnnotationHighlights(root: HTMLElement | null) {
  root?.querySelectorAll('mark.artifact-ann').forEach((mark) => {
    const parent = mark.parentNode
    if (!parent) return
    while (mark.firstChild) parent.insertBefore(mark.firstChild, mark)
    mark.remove()
    parent.normalize()
  })
}

// The rail groups by verdict weight: rejects first — they block the round —
// then change requests, comments, approvals last.
function annotationKindPriority(kind: string): number {
  switch (kind) {
    case 'reject': return 0
    case 'change': return 1
    case 'comment': return 2
    default: return 3 // approve
  }
}

function highlightAnnotationRange(root: HTMLElement, item: { id: string; kind: string; start_offset: number; end_offset: number; comment?: string; replacement?: string }) {
  if (item.start_offset < 0 || item.end_offset < 0) return
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT)
  let node: Node | null
  let cursor = 0
  while ((node = walker.nextNode())) {
    const text = node.textContent ?? ''
    const nodeEnd = cursor + text.length
    if (nodeEnd <= item.start_offset) { cursor = nodeEnd; continue }
    if (cursor >= item.end_offset) break
    const from = Math.max(item.start_offset, cursor) - cursor
    const to = Math.min(item.end_offset, nodeEnd) - cursor
    if (to > from) {
      const textNode = node as Text
      const wrap = document.createElement('mark')
      wrap.className = 'artifact-ann'
      wrap.dataset.kind = item.kind
      wrap.dataset.annId = item.id
      // Hover hint right on the fragment: the note's substance without a
      // scroll to the rail (kind + replacement/comment, capped).
      const hint = [item.comment, item.replacement ? `→ ${item.replacement}` : ''].filter(Boolean).join(' · ')
      if (hint) wrap.title = hint.slice(0, 300)
      const sub = document.createRange()
      sub.setStart(textNode, from)
      sub.setEnd(textNode, to)
      try { sub.surroundContents(wrap) } catch { /* cross-node boundary — the segment stays unwrapped */ }
    }
    cursor = nodeEnd
  }
}

type ArtifactForm = Partial<Artifact>

const STATUS_ORDER: Record<string, number> = { in_review: 0, draft: 1, rejected: 2, approved: 3, deprecated: 4 }

function statusTagType(status?: string): 'red' | 'warm-gray' | 'green' | 'purple' {
  switch (status) {
    case 'in_review': return 'purple'
    case 'draft': return 'warm-gray'
    case 'rejected': return 'red'
    case 'deprecated': return 'warm-gray'
    default: return 'green' // approved
  }
}

export default function Artifacts({ project }: { project?: string }) {
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
  // It OPENS by default when there is anything to decide (the reader came
  // for their queue; the catalog is the browsing surface) — if the mount
  // probe finds the queue empty (or broken), the default falls back to the
  // catalog instead of opening on a dead end. A tab picked by hand always
  // stays put: the empty state is information, not a bounce.
  const [inboxMode, setInboxMode] = useState(true)
  const [inbox, setInbox] = useState<ArtifactInboxEntry[] | null>(null)
  const [inboxBusy, setInboxBusy] = useState(false)
  // Tag editor state + the caller's role (tags drive shared filters, so only
  // operators re-tag; viewers see the tags read-only).
  const [tagDraft, setTagDraft] = useState('')
  const [tagsBusy, setTagsBusy] = useState(false)
  const [operator, setOperator] = useState(false)
  // Fragment reviews are written by anyone who can edit documents (writer+):
  // the review conversation is not an admin duty.
  const [canAnnotate, setCanAnnotate] = useState(false)
  const [myUserID, setMyUserID] = useState('')
  useEffect(() => {
    void whoami().then((identity) => {
      setOperator(canReconcile(identity.role))
      setCanAnnotate(['writer', 'operator', 'admin'].includes(identity.role))
      setMyUserID(identity.user_id || identity.subject || '')
    }).catch(() => { setOperator(false); setCanAnnotate(false); setMyUserID('') })
  }, [])
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
  const [annotations, setAnnotations] = useState<ArtifactAnnotation[]>([])
  const artifactBodyRef = useRef<HTMLDivElement>(null)
  const [reviewsOpen, setReviewsOpen] = useState(false)
  const [submitOpen, setSubmitOpen] = useState(false)
  // Reviewers as tokens (the exact ask_human syntax underneath); the picker
  // resolves them to chips. The directory loads lazily — only when someone
  // opens the submit dialog.
  const [submitReviewerTokens, setSubmitReviewerTokens] = useState<string[]>([])
  const [directory, setDirectory] = useState<User[]>([])
  const [revisionAgentID, setRevisionAgentID] = useState('')
  const [revisionAgents, setRevisionAgents] = useState<Array<{ id: string; name: string }>>([])
  const [revisionAgentOpen, setRevisionAgentOpen] = useState(false)
  // The directory feeds the unified search's author suggestions; it is small
  // (a company roster), one load on mount is fine.
  useEffect(() => { workspaceApi.listUsers().then((data) => setDirectory(data.users ?? [])).catch(() => setDirectory([])) }, [])
  const userNameOf = useCallback((id: string | undefined) => {
    if (!id) return ''
    const user = directory.find((u) => u.id === id || u.name === id)
    return user ? (user.name || user.id) : id
  }, [directory])

  // Reviewer tokens follow the ask_human recipient syntax (user:<id>,
  // org:<unit>, role:<role>, project_owner — plus whatever the submitter
  // typed by hand, e.g. a bare "user-…" id or a bare name). The review card
  // shows people, not envelopes: every token resolves to its human label,
  // falling back to the raw token when the directory does not know it.
  const reviewerLabel = useCallback((raw: string): string => {
    if (raw === 'project_owner') return t('reviewers.owner_label') ?? 'Project owner (admins)'
    if (raw.startsWith('org:')) return org.nameOf(raw.slice(4)) || raw.slice(4)
    if (raw.startsWith('role:')) {
      const role = raw.slice(5)
      return t(`reviewers.role_${role}`) ?? role
    }
    if (raw.startsWith('user:')) return userNameOf(raw.slice(5)) || raw
    return userNameOf(raw) || raw
  }, [t, org, userNameOf])
  const [submitReviewers, setSubmitReviewers] = useState('')
  const [submitNote, setSubmitNote] = useState('')
  const [decisionComment, setDecisionComment] = useState('')
  const [busy, setBusy] = useState(false)
  // The open revision of the selected artifact (docs/artifact-lifecycle.md
  // §5): while it exists the approved version stays live and the card shows
  // the revision state — edit/review actions target the revision.
  const [revision, setRevision] = useState<ArtifactVersion | null>(null)
  // Which version the body pane reads: the live approved one or the open
  // revision. A revision under review IS the document being decided — the
  // pane defaults to it, with the approved text one click away.
  const [showRevisionBody, setShowRevisionBody] = useState(false)
  // Rich diff mode (docs/artifact-lifecycle.md §5): semantic markdown
  // comparison against any chosen version — by default the previous one, so
  // an approver sees WHAT changed, not a wall of raw markup.
  const [diffMode, setDiffMode] = useState(false)
  const [diffBase, setDiffBase] = useState<number | null>(null)
  const [archivedVersions, setArchivedVersions] = useState<ArtifactVersion[] | null>(null)
  // The detail card is tabbed: the document (read + the big in-place editor
  // + fragment reviews), the version archive, the review rounds and the
  // compact entity settings live in separate tabs — the old single-page card
  // mixed version-bound (body, mime) and version-free (title, tags, saga,
  // storage) attributes in one giant dialog, which made editing a large
  // document impossible.
  type DetailTab = 'document' | 'versions' | 'review' | 'settings'
  const [detailTab, setDetailTab] = useState<DetailTab>('document')
  // In-place editing state: enter loads the current text into the editor,
  // save lands it via the same endpoints as before (draft edits in place,
  // approved opens a draft revision), cancel just leaves.
  const [bodyEditing, setBodyEditing] = useState(false)
  const [editDraft, setEditDraft] = useState('')
  const [editBusy, setEditBusy] = useState(false)
  // Which review note the text highlight and the rail pin to (one at a time).
  const [activeAnnotation, setActiveAnnotation] = useState('')
  // Saga suggest: the project's registered sagas (the same source the
  // Dashboard sagas tab uses), loaded when the settings tab opens.
  const [sagas, setSagas] = useState<SagaRollup[]>([])
  const [settingsBusy, setSettingsBusy] = useState(false)
  const [settingsDraft, setSettingsDraft] = useState<ArtifactForm>({})
  const [sagaTouched, setSagaTouched] = useState(false)

  const openSettings = useCallback((a: Artifact) => {
    setSettingsDraft({
      title: a.title,
      description: a.description ?? '',
      kind: a.kind ?? 'reference',
      provider: a.provider ?? 'builtin',
      external_ref: a.external_ref ?? '',
      mime_type: a.mime_type ?? '',
      tags: [...(a.tags ?? [])],
      saga_id: a.saga_id ?? '',
    })
    setSagaTouched(false)
    if (project) {
      void projectBudget(project).then((budget) => setSagas(budget.sagas ?? [])).catch(() => setSagas([]))
    }
  }, [project])

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
  const inboxDefaultRef = useRef(true)
  const loadInbox = useCallback(async () => {
    // The auto-flip below belongs to the default-tab probe only: a tab the
    // user picked by hand always stays put, empty queue or not.
    const defaultProbe = inboxDefaultRef.current
    inboxDefaultRef.current = false
    setInboxBusy(true)
    try {
      const page = await workspaceApi.artifactInbox()
      setInbox(page.entries ?? [])
      setError('')
      // The inbox is the default tab ONLY while there is something to decide:
      // an empty queue falls back to the catalog instead of opening there.
      if (defaultProbe && (page.entries ?? []).length === 0) setInboxMode(false)
    } catch (f) {
      setInbox([])
      setError(message(f))
      // A broken inbox must not hold the default tab hostage either.
      if (defaultProbe) setInboxMode(false)
    } finally { setInboxBusy(false) }
  }, [])
  // Load on mount (the default tab is the inbox) and on every manual switch
  // to it — decisions made elsewhere (chat/agent) must unstick the rows.
  useEffect(() => { void loadInbox() }, [loadInbox, inboxMode])

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
    setAnnotations([])
    setReviewsOpen(false)
    setDecisionComment('')
    setRevision(null)
    setShowRevisionBody(false)
    setDiffMode(false)
    setDiffBase(null)
    setArchivedVersions(null)
    setBodyEditing(false)
    setEditDraft('')
    setActiveAnnotation('')
    setDetailTab('document')
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
    workspaceApi.listArtifactAnnotations(selected.id, selected.version ?? 1)
      .then((page) => { if (!stale) setAnnotations(page.annotations ?? []) })
      .catch(() => {})
    if (selected.status === 'approved') {
      workspaceApi.getArtifactRevision(selected.id)
        .then((page) => {
          if (stale) return
          setRevision(page.revision ?? null)
          if (page.revision) {
            void workspaceApi.listArtifactAnnotations(selected.id, page.revision.version)
              .then((annotationPage) => { if (!stale) setAnnotations((items) => [...items, ...(annotationPage.annotations ?? [])]) })
              .catch(() => {})
          }
          // The pending revision is what the decision is about — read it by
          // default, and surface the review panel without a hunt for buttons.
          if (page.revision?.status === 'in_review') {
            setShowRevisionBody(true)
            void workspaceApi.listArtifactReviews(selected.id)
              .then((rPage) => { if (!stale) { setReviews(rPage.reviews ?? []); setReviewsOpen(true) } })
              .catch(() => {})
          }
        })
        .catch(() => { if (!stale) setRevision(null) })
    }
    return () => { stale = true }
  }, [selected?.id, selected?.version, selected?.provider]) // eslint-disable-line react-hooks/exhaustive-deps

  // Compare mode loads the version archive once per artifact: the retired
  // versions' bodies live there and feed the base-version picker.
  useEffect(() => {
    if (!diffMode || !selectedId || archivedVersions !== null) return
    let stale = false
    workspaceApi.listArtifactVersions(selectedId)
      .then((page) => { if (!stale) setArchivedVersions(page.versions ?? []) })
      .catch(() => { if (!stale) setArchivedVersions([]) })
    return () => { stale = true }
  }, [diffMode, selectedId, archivedVersions])

  // Review highlights repaint after the markdown render settles. Mermaid and
  // presentation blocks hydrate asynchronously — the second pass catches the
  // layout they shift. The read view only: the editor shows its own text.
  const displayedVersion = showRevisionBody && revision ? revision.version : (selected?.version ?? 1)
  const bodyText = showRevisionBody && revision ? (revision.content ?? '') : selectedBody
  const annotationsForVersion = useMemo(
    () => annotations.filter((item) => item.version === displayedVersion),
    [annotations, displayedVersion],
  )
  useEffect(() => {
    const root = artifactBodyRef.current
    if (!root || !selected || selected.provider === 'url' || diffMode || bodyEditing) return
    const apply = () => {
      stripAnnotationHighlights(root)
      for (const item of annotationsForVersion) highlightAnnotationRange(root, item)
      root.querySelectorAll('mark.artifact-ann').forEach((mark) => {
        const html = mark as HTMLElement
        html.classList.toggle('artifact-ann-active', Boolean(activeAnnotation) && html.dataset.annId === activeAnnotation)
      })
    }
    const first = window.setTimeout(apply, 60)
    const second = window.setTimeout(apply, 700)
    return () => { window.clearTimeout(first); window.clearTimeout(second); stripAnnotationHighlights(root) }
  }, [selected, displayedVersion, bodyText, annotationsForVersion, diffMode, bodyEditing, activeAnnotation])

  // Clicking a highlight in the text opens the note popover right at the
  // fragment (kind, quote, comment, replacement, author — and edit/retract
  // actions); the rail stays as the index and still focuses a fragment when
  // its note is clicked there.
  const [annPopoverEdit, setAnnPopoverEdit] = useState(false)
  const [annPopoverAnchor, setAnnPopoverAnchor] = useState<{ left: number; top: number; placement: 'below' | 'above' } | null>(null)
  const annPopoverRef = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const root = artifactBodyRef.current
    if (!root) return
    const onClick = (event: Event) => {
      const target = (event.target as HTMLElement).closest('mark.artifact-ann') as HTMLElement | null
      if (!target?.dataset.annId) return
      event.preventDefault()
      setAnnPopoverEdit(false)
      setActiveAnnotation((current) => {
        const next = current === target.dataset.annId ? '' : (target.dataset.annId ?? '')
        if (!next) setAnnPopoverAnchor(null)
        return next
      })
    }
    root.addEventListener('click', onClick)
    return () => root.removeEventListener('click', onClick)
  }, [selected, bodyEditing, diffMode])

  // Anchor the popover to the active fragment's viewport rect (position:
  // fixed on the popover — document-space coordinates would shift by every
  // positioned ancestor, landing the popover far from the fragment): below
  // the mark when there is room, above when not; follow scroll/resize so it
  // stays glued to the text.
  const activeItem = activeAnnotation ? annotationsForVersion.find((item) => item.id === activeAnnotation) : undefined
  useEffect(() => {
    if (!activeAnnotation || !activeItem) { setAnnPopoverAnchor(null); return }
    const anchor = () => {
      const mark = artifactBodyRef.current?.querySelector(`mark.artifact-ann[data-ann-id='${CSS.escape(activeAnnotation)}']`) as HTMLElement | null
      if (!mark) { setAnnPopoverAnchor(null); return }
      const rect = mark.getBoundingClientRect()
      const below = window.innerHeight - rect.bottom
      const openUp = below < 190 && below <= rect.top
      setAnnPopoverAnchor({
        left: Math.min(Math.max(12, rect.left), window.innerWidth - 372),
        top: openUp ? Math.max(12, rect.top - 6) : rect.bottom + 6,
        placement: openUp ? 'above' : 'below',
      })
    }
    anchor()
    const onMove = () => anchor()
    window.addEventListener('scroll', onMove, true)
    window.addEventListener('resize', onMove)
    return () => {
      window.removeEventListener('scroll', onMove, true)
      window.removeEventListener('resize', onMove)
    }
  }, [activeAnnotation, activeItem, displayedVersion, bodyText])

  // Clicks outside the popover (and outside its mark) release the pin — the
  // same outside-dismiss the selection menu already follows.
  useEffect(() => {
    if (!activeAnnotation) return
    const onDown = (event: MouseEvent) => {
      const target = event.target as HTMLElement
      if (target.closest('.artifact-note-popover')) return
      if (target.closest('mark.artifact-ann')) return
      setActiveAnnotation('')
      setAnnPopoverEdit(false)
    }
    document.addEventListener('mousedown', onDown)
    return () => document.removeEventListener('mousedown', onDown)
  }, [activeAnnotation])

  // ── Fragment-note popup, the Notion model (docs/artifact-lifecycle.md §7) ──
  // The menu lives on `selectionchange`, not on mouseup: it tracks the live
  // selection wherever it came from (mouse or keyboard), disappears the
  // moment the selection collapses or leaves the document, and stays glued
  // to its anchor rect on scroll while a note draft is open. A bare menu
  // hides on scroll instead — the anchor is gone from view, following it is
  // noise. Escape always closes.
  // The fragment-note popup (docs/artifact-lifecycle.md §7) lives in a hook:
  // the Notion selection model — open on selectionchange, discard on
  // collapse/outside/Escape, hide on scroll unless a draft is typing.
  const artifactNoteMenu = useFragmentNoteMenu({ enabled: Boolean(selected && selected.provider !== 'url' && !bodyEditing && !diffMode && canAnnotate), rootRef: artifactBodyRef })
  const selectionMenu = artifactNoteMenu.menu
  const annotationDraft = artifactNoteMenu.draft
  const setAnnotationDraft = artifactNoteMenu.setDraft
  const closeSelectionMenu = artifactNoteMenu.close

  // Lifecycle counters drive the status filter chips: how many artifacts sit in
  // each state. Pending reviews first — that is the human queue.
  const statusCounts = artifacts.reduce<Record<string, number>>((acc, a) => {
    const s = a.status ?? 'approved'
    acc[s] = (acc[s] ?? 0) + 1
    return acc
  }, {})
  const orderedStatuses = ['in_review', 'draft', 'rejected', 'approved', 'deprecated'].filter((s) => statusCounts[s] > 0)
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
      case 'deprecated': return t('artifacts.status_deprecated') ?? 'Устарел'
      default: return t('artifacts.status_approved') ?? 'Approved'
    }
  }

  const originLabel = (a: Artifact) =>
    a.origin_type === 'agent'
      ? (t('artifacts.origin_agent', { run: a.origin_run ? ` · ${a.origin_run}` : '' }) ?? `agent${a.origin_run ? ` · ${a.origin_run}` : ''}`)
      : (t('artifacts.origin_human', { user: a.origin_user ? ` · ${userNameOf(a.origin_user)}` : '' }) ?? `human${a.origin_user ? ` · ${userNameOf(a.origin_user)}` : ''}`)

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

  // The list row's modification time, whether it rides the mono meta line
  // (no tags) or the tag-chip row below it: same muted mono 0.7rem look in
  // both spots, and no internal wrapping — a tight row wraps the whole date
  // to its own line, never splits «10 нояб.» from «14:03».
  const listRowDateStyle: CSSProperties = {
    marginLeft: 'auto', flexShrink: 0, whiteSpace: 'nowrap',
    fontFamily: 'var(--tm-font-mono, monospace)', fontSize: '0.7rem', color: 'var(--tm-text-3)',
  }

  const startCreate = () => {
    setForm({ id: '', title: '', description: '', provider: 'builtin', content: '', mime_type: '', kind: 'reference', tags: [] })
    setEditing(null)
    setShowForm(true)
    if (project) void projectBudget(project).then((budget) => setSagas(budget.sagas ?? [])).catch(() => setSagas([]))
  }

  // The single edit action's label follows the state: an approved artifact is
  // edited by opening a revision, a draft/rejected one edits in place, and
  // an open revision continues its draft.
  const editLabel = (status?: string, revisionEditable?: boolean) => {
    if (status === 'approved' && revisionEditable) return t('artifacts.edit_revision_btn') ?? 'Edit revision'
    if (status === 'approved') return t('artifacts.new_revision_btn') ?? 'New revision'
    return t('artifacts.edit_btn') ?? 'Edit'
  }

  // Create-only save: the body is optional — a big document is written in
  // the in-place editor right after the shell lands. mime stays empty → the
  // server default text/markdown.
  const save = async () => {
    setError('')
    setBusy(true)
    try {
      await workspaceApi.createArtifact({
        id: form.id?.trim() ?? '',
        title: form.title ?? '',
        description: form.description,
        provider: form.provider,
        external_ref: form.external_ref,
        content: form.content,
        org_unit_id: form.org_unit_id,
        kind: form.kind,
        saga_id: form.saga_id,
        tags: form.tags,
      })
      setShowForm(false)
      setEditing(null)
      await load()
    } catch (f) { setError(message(f)) }
    finally { setBusy(false) }
  }

  // Entering the in-place editor: the body pane itself becomes the editing
  // surface (a large document never fits the old modal), pre-filled with the
  // version currently displayed — the live body, or the open revision's text
  // when the approved row is being continued.
  const startEdit = async (a: Artifact) => {
    if (a.provider === 'url') return
    setDetailTab('document')
    setEditBusy(true)
    try {
      let content = selectedBody
      if (showRevisionBody && revision) {
        content = revision.content ?? ''
      } else if (!content) {
        const page = await workspaceApi.getArtifact(a.id)
        content = page.artifact.content ?? ''
        if (!showRevisionBody) setSelectedBody(content)
      }
      setEditDraft(content)
      setBodyEditing(true)
    } finally {
      setEditBusy(false)
    }
  }

  // Save lands the edited markdown: a draft/rejected row updates in place
  // (same version), an approved row opens/updates the draft revision — the
  // server decides by status, the omitted fields (title, tags, saga…) keep
  // their stored values. Only the BODY changes here; the metadata lives in
  // the Settings tab.
  const saveEdit = async () => {
    if (!selected) return
    setError('')
    setEditBusy(true)
    try {
      await workspaceApi.updateArtifact(selected.id, { content: editDraft })
      setBodyEditing(false)
      // The row changed: a draft edit bumps updated_at, an approved one
      // opened a draft revision. Reload the row and the body.
      const [page, full] = await Promise.all([
        workspaceApi.getArtifact(selected.id).catch(() => null),
        load(),
      ])
      if (page) {
        setSelectedBody(page.artifact.content ?? '')
        setBodyTruncated(Boolean(page.truncated))
        if (selected.status === 'approved') {
          const rPage = await workspaceApi.getArtifactRevision(selected.id).catch(() => ({ revision: null }))
          setRevision(rPage.revision ?? null)
          setShowRevisionBody(false)
        }
      }
      void workspaceApi.listArtifactAnnotations(selected.id, selected.version ?? 1).then((p) => setAnnotations(p.annotations ?? [])).catch(() => {})
    } catch (f) { setError(message(f)) }
    finally { setEditBusy(false) }
  }

  // Settings save carries the VERSION-FREE attributes only: title,
  // description, kind, provider/reference, saga link and tags. The body and
  // mime stay untouched — the backend backfills omitted fields, and an
  // explicit saga_id="" detaches (nil keeps it).
  const saveSettings = async () => {
    if (!selected) return
    setError('')
    setSettingsBusy(true)
    try {
      const payload: Record<string, unknown> = {
        title: (settingsDraft.title ?? '').trim(),
        description: settingsDraft.description ?? '',
        provider: settingsDraft.provider ?? 'builtin',
        external_ref: settingsDraft.external_ref ?? '',
        kind: settingsDraft.kind ?? 'reference',
        tags: settingsDraft.tags ?? [],
      }
      if (sagaTouched) payload.saga_id = settingsDraft.saga_id ?? ''
      if ((settingsDraft.provider ?? 'builtin') === 'builtin') payload.mime_type = settingsDraft.mime_type ?? ''
      const updated = await workspaceApi.updateArtifact(selected.id, payload)
      setArtifacts((current) => current.map((a) => (a.id === selected.id ? { ...updated, content: a.content } : a)))
    } catch (f) { setError(message(f)) }
    finally { setSettingsBusy(false) }
  }

  // Retract one review note: own notes for any writer, anything for an
  // operator (the backend enforces the same rule).
  // Retract one review note: own notes for any writer, anything for an
  // operator (the backend enforces the same rule).
  const canDeleteAnnotation = useCallback((item: ArtifactAnnotation) => {
    if (!canAnnotate) return false
    return item.created_by === myUserID || operator
  }, [canAnnotate, myUserID, operator])

  const removeAnnotation = async (item: ArtifactAnnotation) => {
    if (!selected) return
    const ok = await confirmDialog({
      title: t('artifacts.annotation_delete_confirm') ?? 'Удалить заметку?',
      body: item.quote.slice(0, 120),
      danger: true,
      confirmLabel: t('action.delete'),
    })
    if (!ok) return
    try {
      await workspaceApi.deleteArtifactAnnotation(selected.id, item.id)
      setAnnotations((items) => items.filter((x) => x.id !== item.id))
      if (activeAnnotation === item.id) setActiveAnnotation('')
      setAnnPopoverEdit(false)
    } catch (f) { setError(message(f)) }
  }

  // In-place note edit: the API has no update — retract + re-add at the same
  // text offsets (kind/quote preserved, comment/replacement replaced). The
  // backend enforces the same can-delete rule before the retract.
  const [annEditDraft, setAnnEditDraft] = useState<{ comment: string; replacement: string }>({ comment: '', replacement: '' })
  const canEditAnnotation = canDeleteAnnotation
  const saveAnnotationEdit = async (item: ArtifactAnnotation) => {
    if (!selected) return
    setBusy(true)
    try {
      await workspaceApi.deleteArtifactAnnotation(selected.id, item.id)
      const created = await workspaceApi.addArtifactAnnotation(selected.id, {
        version: item.version,
        kind: item.kind,
        quote: item.quote,
        replacement: annEditDraft.replacement,
        comment: annEditDraft.comment,
        start_offset: item.start_offset,
        end_offset: item.end_offset,
      })
      setAnnotations((items) => items.map((x) => x.id === item.id ? created : x))
      setActiveAnnotation(created.id)
      setAnnPopoverEdit(false)
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

  // Soft retirement (§lifecycle): an approved artifact drops out of the
  // default listing; skill manifests still referencing it block the move —
  // the 409 names them and the error banner shows it verbatim.
  const deprecate = async (a: Artifact) => {
    const ok = await confirmDialog({
      title: t('artifacts.deprecate_confirm', { name: a.title }) ?? `Устарел: ${a.title}?`,
      body: t('artifacts.deprecate_hint') ?? 'Документ останется доступным по id, но исчезнет из списка по умолчанию. Если на него ссылается навык — перевод блокируется.',
      danger: true,
      confirmLabel: t('artifacts.status_deprecated') ?? 'Устарел',
    })
    if (!ok) return
    setError('')
    try {
      const updated = await workspaceApi.deprecateArtifact(a.id)
      setArtifacts((current) => current.map((x) => (x.id === a.id ? { ...updated, content: x.content } : x)))
    } catch (f) { setError(message(f)) }
  }

  const revive = async (a: Artifact) => {
    setError('')
    try {
      const updated = await workspaceApi.reviveArtifact(a.id)
      setArtifacts((current) => current.map((x) => (x.id === a.id ? { ...updated, content: x.content } : x)))
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

  // Round management (add reviewers / delegate / mode flip): one busy flag,
  // the handlers reload the rounds list after each mutation so the cards and
  // the «Согласование» tab badge stay truthful. A mode flip can CLOSE the
  // round retroactively (any + an approve already on the table) — the reload
  // also refreshes the row's status.
  const [roundManage, setRoundManage] = useState<{ reviewID: string; kind: 'add' | 'delegate' } | null>(null)
  const [roundTokens, setRoundTokens] = useState<string[]>([])
  const [delegateTarget, setDelegateTarget] = useState('')
  const [roundBusy, setRoundBusy] = useState(false)
  const reloadReviews = async (artifactID: string) => {
    const page = await workspaceApi.listArtifactReviews(artifactID)
    setReviews(page.reviews ?? [])
  }
  const addReviewers = async (r: ArtifactReview) => {
    if (roundTokens.length === 0) return
    setRoundBusy(true)
    try {
      await workspaceApi.addArtifactReviewers(selected!.id, r.id, roundTokens)
      await reloadReviews(selected!.id)
      setRoundManage(null); setRoundTokens([])
    } catch (f) { setError(message(f)) }
    finally { setRoundBusy(false) }
  }
  const delegateRound = async (r: ArtifactReview) => {
    if (!delegateTarget.trim()) return
    setRoundBusy(true)
    try {
      await workspaceApi.delegateArtifactReview(selected!.id, r.id, delegateTarget.trim())
      await reloadReviews(selected!.id)
      setRoundManage(null); setDelegateTarget('')
    } catch (f) { setError(message(f)) }
    finally { setRoundBusy(false) }
  }
  const flipRoundMode = async (r: ArtifactReview, mode: 'all' | 'any') => {
    if (r.mode === mode) return
    setRoundBusy(true)
    try {
      await workspaceApi.setArtifactReviewMode(selected!.id, r.id, mode)
      await reloadReviews(selected!.id)
      await load()
    } catch (f) { setError(message(f)) }
    finally { setRoundBusy(false) }
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

  // The document-tab decision strip: settle the open round right where the
  // approver finished reading. The round loads lazily — the panel only
  // renders when one is open, so the fetch happens at most once.
  const decideOpenRound = async (approve: boolean) => {
    if (!selected) return
    setBusy(true)
    try {
      let open = reviews.find((r) => r.status === 'open')
      if (!open) {
        const page = await workspaceApi.listArtifactReviews(selected.id)
        open = (page.reviews ?? []).find((r) => r.status === 'open')
        if (page.reviews) setReviews(page.reviews)
      }
      if (!open) return
      await workspaceApi.resolveArtifactReview(selected.id, open.id, approve, decisionComment)
      setDecisionComment('')
      const page = await workspaceApi.listArtifactReviews(selected.id)
      setReviews(page.reviews ?? [])
      await load()
    } catch (f) { setError(message(f)) }
    finally { setBusy(false) }
  }

  const fmtTime = (v?: string) => (v ? new Date(v).toLocaleString([uiLocale()]) : '')

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
          <div style={{ display: 'flex', gap: '0.375rem', marginBottom: '0.5rem', alignItems: 'center' }}>
            <button type="button" onClick={() => setInboxMode(false)} style={chipStyle(!inboxMode)}>
              {t('artifacts.tab_catalog') ?? 'Каталог'}
            </button>
            <button type="button" onClick={() => setInboxMode(true)} style={chipStyle(inboxMode)}>
              {t('artifacts.tab_inbox') ?? 'На моём согласовании'}
              {(inbox?.length ?? 0) > 0 && (
                // The attention badge (runs-tab style): pending decisions are
                // the work the user came for — it must be SEEN, not a gray
                // suffix lost in the tab label.
                <span style={{ marginLeft: '0.375rem', background: '#e0af68', color: '#1a1a1a', borderRadius: 999, fontSize: '0.68rem', fontWeight: 600, padding: '0.05rem 0.45rem', lineHeight: 1.4 }}>
                  {inbox!.length}
                </span>
              )}
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
                    {' · '}<TimeHint value={entry.review.requested_at}>{shortTime(entry.review.requested_at)}</TimeHint>
                    {entry.origin_user ? ` · ${userNameOf(entry.origin_user)}` : ''}
                  </div>
                  {entry.matched.length > 0 && (
                    <div style={{ fontSize: '0.7rem', color: 'var(--tm-text-2)', marginTop: '0.2rem' }}>
                      {t('artifacts.inbox_matched') ?? 'ты рецензент как'}: {entry.matched.map((matched) => reviewerLabel(matched)).join(', ')}
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
                      {/* Deprecated drops out of the default listing server-side,
                          so its count never shows up in statusCounts — the option
                          stays reachable to un-hide the retired documents. */}
                      {!orderedStatuses.includes('deprecated') && (
                        <option value="deprecated">{statusLabel('deprecated')}</option>
                      )}
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
                      {(a.tags ?? []).length === 0 && (
                        // No tag row below → the modification time rides at the
                        // right end of this line, still the row's bottom corner.
                        <TimeHint value={a.updated_at} style={{ ...listRowDateStyle }}>{briefDateTime(a.updated_at)}</TimeHint>
                      )}
                    </div>
                    {(a.tags ?? []).length > 0 && (
                      <div style={{ display: 'flex', gap: '0.25rem', flexWrap: 'wrap', marginTop: '0.25rem', alignItems: 'center' }}>
                        {(a.tags ?? []).slice(0, 4).map((tag) => (
                          <span key={tag} style={{ fontSize: '0.66rem', padding: '0.05rem 0.35rem', borderRadius: 999, background: 'var(--tm-surface-3, rgba(127,127,127,.14))', color: 'var(--tm-text-3)' }}>#{tag}</span>
                        ))}
                        {(a.tags ?? []).length > 4 && <span style={{ fontSize: '0.66rem', color: 'var(--tm-text-3)' }}>+{(a.tags ?? []).length - 4}</span>}
                        {/* Same look as the meta line's date: inheriting the card font made it render larger and brighter than the chips it shares the row with. */}
                        <TimeHint value={a.updated_at} style={{ ...listRowDateStyle }}>{briefDateTime(a.updated_at)}</TimeHint>
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
            // The review surface follows the VERSION under review: an open
            // revision keeps the approved row alive, and its review round must
            // stay reachable — the decision is about the pending version, not
            // the status of the live row.
            const revisionInReview = selected.status === 'approved' && revision?.status === 'in_review'
            const openReview = (selected.status ?? '') === 'in_review' || revisionInReview
            // An open revision keeps the approved version live: edit/review
            // actions target the revision, the card keeps showing approved.
            const revisionOpen = selected.status === 'approved' && revision !== null
            const revisionEditable = revision?.status === 'draft' || revision?.status === 'rejected'
            // The body pane reads the version being decided: a pending
            // revision is what an approver must actually read.
            // Compare mode: the universe of readable versions (live, open
            // revision, archived) and the base — by default the previous
            // version of the one being read.
            const compareOptions = (() => {
              const opts: Array<{ version: number; label: string; content: string }> = [
                { version: selected.version ?? 1, label: t('artifacts.diff_current') ?? 'current', content: selectedBody },
              ]
              if (revision) opts.push({ version: revision.version, label: t('artifacts.diff_revision') ?? 'revision', content: revision.content ?? '' })
              for (const v of archivedVersions ?? []) {
                if (!opts.some((o) => o.version === v.version)) opts.push({ version: v.version, label: v.status, content: v.content ?? '' })
              }
              return opts.sort((a, b) => b.version - a.version)
            })()
            const saveAnnotation = async () => {
              if (!annotationDraft) return
              const anchor = selectionMenu
              if (!anchor) return
              setBusy(true)
              try {
                const created = await workspaceApi.addArtifactAnnotation(selected.id, { version: displayedVersion, ...annotationDraft, start_offset: anchor.start, end_offset: anchor.end })
                setAnnotations((items) => [...items, created])
                window.getSelection()?.removeAllRanges()
                closeSelectionMenu()
              } catch (f) { setError(message(f)) }
              finally { setBusy(false) }
            }
            const focusAnnotation = (item: ArtifactAnnotation) => {
              const root = artifactBodyRef.current
              if (!root) return
              setActiveAnnotation(item.id)
              // The persistent <mark> is the anchor: when it exists (the render
              // settled), scroll to it — no native selection needed.
              const mark = root.querySelector(`mark.artifact-ann[data-ann-id='${CSS.escape(item.id)}']`)
              if (mark) {
                mark.scrollIntoView({ behavior: 'smooth', block: 'center' })
                return
              }
              const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT)
              let node: Node | null
              let cursor = 0
              let startNode: Node | null = null, endNode: Node | null = null, startLocal = 0, endLocal = 0
              while ((node = walker.nextNode())) {
                const text = node.textContent ?? ''
                const length = text.length
                if (item.start_offset >= 0 && !startNode && item.start_offset >= cursor && item.start_offset <= cursor + length) { startNode = node; startLocal = item.start_offset - cursor }
                if (item.end_offset >= 0 && item.end_offset <= cursor + length && item.end_offset >= cursor) { endNode = node; endLocal = item.end_offset - cursor; break }
                cursor += length
              }
              if (!startNode || !endNode) {
                const exactQuote = item.quote
                const allText = root.textContent ?? ''
                let occurrence = item.start_offset >= 0 && allText.slice(item.start_offset, item.start_offset + exactQuote.length) === exactQuote ? item.start_offset : -1
                if (occurrence < 0) occurrence = allText.indexOf(exactQuote)
                if (occurrence < 0 || (occurrence !== item.start_offset && allText.indexOf(exactQuote, occurrence + 1) >= 0)) return
                item = { ...item, start_offset: occurrence, end_offset: occurrence + exactQuote.length }
                cursor = 0
                const fallbackWalker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT)
                while ((node = fallbackWalker.nextNode())) {
                  const length = node.textContent?.length ?? 0
                  if (!startNode && item.start_offset >= cursor && item.start_offset <= cursor + length) { startNode = node; startLocal = item.start_offset - cursor }
                  if (item.end_offset >= cursor && item.end_offset <= cursor + length) { endNode = node; endLocal = item.end_offset - cursor; break }
                  cursor += length
                }
              }
              if (!startNode || !endNode) return
              const range = document.createRange()
              range.setStart(startNode, startLocal)
              range.setEnd(endNode, endLocal)
              const rect = range.getBoundingClientRect()
              window.scrollTo({ top: window.scrollY + rect.top - 120, behavior: 'smooth' })
              const selection = window.getSelection()
              selection?.removeAllRanges()
              selection?.addRange(range)
            }
            const defaultBase = compareOptions.filter((o) => o.version < displayedVersion)[0] ?? compareOptions.find((o) => o.version !== displayedVersion)
            const baseOption = compareOptions.find((o) => o.version === (diffBase ?? defaultBase?.version))
            return (
              <Tile style={{ padding: '1rem 1.1rem' }}>
                				{/* Header — the Agents-card pattern: the title block shrinks
                				    (heading ellipsizes, the meta line wraps internally) and the
                				    submit/delete buttons stay on the SAME line, pinned right —
                				    no wrap, no wasted row. */}
                				<div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', gap: '0.75rem' }}>
                					<div style={{ minWidth: 0 }}>
                						<div style={{ display: 'flex', alignItems: 'center', gap: '0.4rem', minWidth: 0 }}>
                							<Heading style={{ margin: 0, fontSize: '1.05rem', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis', minWidth: 0 }}>{selected.title}</Heading>
                							<OrgBadge orgUnitID={selected.org_unit_id} org={org} />
                						</div>
                						<p style={{ fontFamily: 'var(--tm-font-mono, monospace)', fontSize: '0.72rem', color: 'var(--tm-text-3)', marginTop: '0.25rem', wordBreak: 'break-all' }}>
                							{selected.id}{selected.version && selected.version > 1 ? ` · v${selected.version}` : ''}
                							{' · '}{t('artifacts.updated_prefix') ?? 'Изменён'}: {fullDateTime(selected.updated_at)}
                							{' · '}{originLabel(selected)}
                							{selected.saga_id ? ` · ${t('artifacts.saga_prefix') ?? 'saga'}: ${selected.saga_id}` : ''}
                						</p>
                					</div>
                					<Stack orientation="horizontal" gap={2} style={{ flexShrink: 0 }}>
                						{(reviewable || (revisionOpen && revisionEditable)) && (
                							<Button size="sm" onClick={() => {
                								setSubmitReviewerTokens([])
                								setSubmitNote('')
                								setSubmitOpen(true)
                								void workspaceApi.listUsers().then((data) => setDirectory(data.users ?? [])).catch(() => setDirectory([]))
                							}}>
                								{t('artifacts.submit_review') ?? 'На согласование'}
                							</Button>
                						)}
                						<Button size="sm" kind="danger--ghost" hasIconOnly renderIcon={TrashCan} iconDescription={t('action.delete') ?? 'Delete'} onClick={() => void remove(selected)} />
                					</Stack>
                				</div>

                {/* One semantic row: lifecycle badges and catalog tags are the
                    same class of metadata — before they ate two full rows. */}
                <div style={{ display: 'flex', gap: '0.3rem', flexWrap: 'wrap', marginTop: '0.5rem', alignItems: 'center' }}>
                  <Tag type={statusTagType(selected.status)} size="sm" title={`status: ${selected.status ?? 'approved'}`}>{statusLabel(selected.status)}</Tag>
                  <Tag type={selected.provider === 'builtin' ? 'blue' : 'gray'} size="sm" title={selected.provider}>{providerLabel(selected.provider)}</Tag>
                  {selected.kind === 'work' && <Tag type="cool-gray" size="sm">{t('artifacts.kind_work') ?? 'work product'}</Tag>}
                  <Tag type="cool-gray" size="sm">{humanArtifactSize(selected.size_bytes)}</Tag>
                  {usedBy.length > 0
                    ? <Tag type="green" size="sm" title={usedBy.join(', ')}>{t('artifacts.used_by', { n: String(usedBy.length) }) ?? `used by ${usedBy.length} skill(s)`}</Tag>
                    : <Tag size="sm">{t('artifacts.not_used') ?? 'not referenced'}</Tag>}
                  {(selected.tags ?? []).length > 0 && <span style={{ width: '1px', height: '1.1rem', background: 'var(--tm-border)' }} />}
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
                    <TagEditorInput
                      draft={tagDraft}
                      onDraft={setTagDraft}
                      onCommit={(tag) => void saveTags(selected, [...(selected.tags ?? []), tag])}
                      options={tagOptions}
                      exclude={selected.tags ?? []}
                    />
                  )}
                </div>
                {/* Everyone who put a hand on this: creator, the run's agent,
                    editors, reviewers — the author filter searches the same
                    set, so a name from here is one click to pin. */}
                {(selected.contributors ?? []).length > 0 && (
                  <div style={{ fontSize: '0.72rem', color: 'var(--tm-text-3)', marginTop: '0.45rem', display: 'flex', gap: '0.3rem', flexWrap: 'wrap', alignItems: 'center' }}>
                    <span>{t('artifacts.contributors') ?? 'Принимали участие в разработке'}:</span>
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

                {/* The detail card is tabbed: the document (read + edit +
                    fragment reviews), the version archive, the review rounds
                    and the compact entity settings — version-bound attributes
                    (body, mime) live on the document tab, version-free ones
                    (title, tags, saga, storage) in settings. */}
                <div role="tablist" aria-label={t('artifacts.title') ?? 'Reference Artifacts'} style={{ display: 'flex', gap: '0.25rem', flexWrap: 'wrap', marginTop: '0.75rem', borderBottom: '1px solid var(--tm-border)', paddingBottom: 0 }}>
                  {([
                    ['document', t('artifacts.tab_document') ?? 'Документ'],
                    ['review', openReview ? `${t('artifacts.open_reviews') ?? 'Согласование'} (${(reviews.length > 0 ? reviews : [{ status: 'open' }]).filter((r) => r.status === 'open').length})` : (t('artifacts.review_history') ?? 'Согласование')],
                    ['versions', t('artifacts.versions_title') ?? 'Версии'],
                    ['settings', t('artifacts.tab_settings') ?? 'Настройки'],
                  ] as Array<[DetailTab, string]>).map(([tab, label]) => (
                    <button
                      key={tab}
                      role="tab"
                      aria-selected={detailTab === tab}
                      onClick={() => { setDetailTab(tab); if (tab === 'review' && reviews.length === 0) void openReviews(selected); if (tab === 'settings') openSettings(selected) }}
                      style={{ background: detailTab === tab ? 'var(--tm-elevated)' : 'none', border: '1px solid var(--tm-border)', borderBottomColor: detailTab === tab ? 'var(--tm-elevated)' : 'var(--tm-border)', borderRadius: '6px 6px 0 0', padding: '0.3rem 0.75rem', cursor: 'pointer', color: 'var(--tm-text)', fontSize: '0.78rem' }}
                    >{label}</button>
                  ))}
                </div>

                {detailTab === 'review' && (
                  <div style={{ marginTop: '0.6rem', paddingTop: '0.6rem' }}>
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
                                v{r.artifact_version} · {fmtTime(r.requested_at)}
                              </span>
                            </div>
                            {r.status === 'open' && (
                              <Stack orientation="horizontal" gap={2}>
                                <Button size="sm" renderIcon={CheckmarkOutline} disabled={busy} onClick={() => void decide(r, true)}>{t('artifacts.approve') ?? 'Approve'}</Button>
                                <Button size="sm" kind="danger" renderIcon={CloseOutline} disabled={busy} onClick={() => void decide(r, false)}>{t('artifacts.reject') ?? 'Reject'}</Button>
                              </Stack>
                            )}
                          </div>
                          {/* The closing rule is a live knob of an open round:
                              switching can close it retroactively (any + an
                              approve already cast). */}
                          {r.status === 'open' ? (
                            <div style={{ display: 'flex', alignItems: 'center', gap: '0.4rem', flexWrap: 'wrap', marginTop: '0.4rem' }}>
                              <span style={{ fontSize: '0.75rem', color: 'var(--tm-text-3)' }}>{t('artifacts.review_mode_label') ?? 'Режим'}:</span>
                              {(['all', 'any'] as const).map((mode) => (
                                <button
                                  key={mode}
                                  type="button"
                                  disabled={roundBusy}
                                  onClick={() => void flipRoundMode(r, mode)}
                                  style={{ background: r.mode === mode ? 'var(--tm-teal)' : 'transparent', border: '1px solid var(--tm-border)', borderRadius: 999, padding: '0.12rem 0.6rem', cursor: r.mode === mode ? 'default' : 'pointer', color: r.mode === mode ? '#fff' : 'var(--tm-text-2)', fontSize: '0.72rem' }}
                                >{mode === 'all' ? (t('artifacts.inbox_mode_all') ?? 'все рецензенты') : (t('artifacts.inbox_mode_any') ?? 'любой рецензент')}</button>
                              ))}
                            </div>
                          ) : (
                            <p style={{ fontSize: '0.78rem', color: 'var(--tm-text-2)', marginTop: '0.4rem' }}>
                              {t('artifacts.review_mode_label') ?? 'Режим'}: {r.mode === 'all' ? (t('artifacts.inbox_mode_all') ?? 'все рецензенты') : (t('artifacts.inbox_mode_any') ?? 'любой рецензент')}
                            </p>
                          )}
                          <p style={{ fontSize: '0.78rem', color: 'var(--tm-text-2)', marginTop: '0.4rem' }}>
                            {t('artifacts.reviewers_label') ?? 'Reviewers'}: {r.reviewers.map((reviewer) => reviewerLabel(reviewer)).join(', ')}
                          </p>
                          {r.status === 'open' && (
                            <div style={{ marginTop: '0.5rem' }}>
                              {roundManage?.reviewID === r.id ? (
                                <div style={{ border: '1px solid var(--tm-border)', borderRadius: '8px', padding: '0.5rem 0.6rem' }}>
                                  {roundManage.kind === 'add' ? (
                                    <>
                                      <ReviewerPicker value={roundTokens} onChange={setRoundTokens} users={directory} org={org} />
                                      <Stack orientation="horizontal" gap={2} style={{ marginTop: '0.4rem' }}>
                                        <Button size="sm" disabled={roundBusy || roundTokens.length === 0} onClick={() => void addReviewers(r)}>{t('artifacts.round_add_apply') ?? 'Добавить'}</Button>
                                        <Button size="sm" kind="ghost" disabled={roundBusy} onClick={() => { setRoundManage(null); setRoundTokens([]) }}>{t('action.cancel') ?? 'Cancel'}</Button>
                                      </Stack>
                                    </>
                                  ) : (
                                    <>
                                      <div style={{ display: 'flex', gap: '0.4rem', alignItems: 'center', flexWrap: 'wrap' }}>
                                        <select
                                          value={delegateTarget}
                                          onChange={(e) => setDelegateTarget(e.target.value)}
                                          style={{ border: '1px solid var(--tm-border)', borderRadius: '6px', background: 'var(--tm-bg)', color: 'var(--tm-text)', fontSize: '0.78rem', padding: '0.3rem 0.5rem', minWidth: '12rem' }}
                                        >
                                          <option value="">{t('artifacts.round_delegate_pick') ?? 'Кому передать…'}</option>
                                          {directory.filter((u) => u.active).map((u) => (
                                            <option key={u.id} value={`user:${u.id}`}>{u.name || u.id}</option>
                                          ))}
                                        </select>
                                      </div>
                                      <Stack orientation="horizontal" gap={2} style={{ marginTop: '0.4rem' }}>
                                        <Button size="sm" disabled={roundBusy || !delegateTarget} onClick={() => void delegateRound(r)}>{t('artifacts.round_delegate_apply') ?? 'Делегировать'}</Button>
                                        <Button size="sm" kind="ghost" disabled={roundBusy} onClick={() => { setRoundManage(null); setDelegateTarget('') }}>{t('action.cancel') ?? 'Cancel'}</Button>
                                      </Stack>
                                      <p style={{ fontSize: '0.72rem', color: 'var(--tm-text-3)', marginTop: '0.3rem' }}>{t('artifacts.round_delegate_hint') ?? 'Ваш персональный голос (если вы ещё не голосовали) перейдёт выбранному человеку; групповые места (роль, подразделение) остаются.'}</p>
                                    </>
                                  )}
                                </div>
                              ) : (
                                <div style={{ display: 'flex', gap: '0.4rem', flexWrap: 'wrap' }}>
                                  <Button size="sm" kind="ghost" onClick={() => { setRoundManage({ reviewID: r.id, kind: 'add' }); setRoundTokens([]); void workspaceApi.listUsers().then((data) => setDirectory(data.users ?? [])).catch(() => setDirectory([])) }}>
                                    {t('artifacts.round_add') ?? '+ согласующий'}
                                  </Button>
                                  <Button size="sm" kind="ghost" onClick={() => { setRoundManage({ reviewID: r.id, kind: 'delegate' }); setDelegateTarget(''); void workspaceApi.listUsers().then((data) => setDirectory(data.users ?? [])).catch(() => setDirectory([])) }}>
                                    {t('artifacts.round_delegate') ?? 'Делегировать'}
                                  </Button>
                                </div>
                              )}
                            </div>
                          )}
                          {r.note && <p style={{ fontSize: '0.78rem', color: 'var(--tm-text-3)', marginTop: '0.25rem' }}>{r.note}</p>}
                          {Object.entries(r.decisions ?? {}).map(([who, d]) => (
                            <p key={who} style={{ fontSize: '0.75rem', color: 'var(--tm-text-3)', marginTop: '0.25rem' }}>
                              <strong>{reviewerLabel(who)}{d.decided_by && d.decided_by !== who ? ` (${reviewerLabel(d.decided_by)})` : ''}</strong>: {d.decision}{d.comment ? ` — ${d.comment}` : ''} ({fmtTime(d.at)})
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
                      {annotationsForVersion.length > 0 && selected.status !== 'in_review' && (
                        <div>
                          <Button size="sm" kind="secondary" disabled={busy} onClick={() => setRevisionAgentOpen(true)}>
                            {selected.status === 'approved' ? (t('artifacts.annotation_ai_revision') ?? 'Apply notes and submit a new version') : (t('artifacts.annotation_ai_revision') ?? 'Send notes for rework')}
                          </Button>
                          <p style={{ fontSize: '0.72rem', color: 'var(--tm-text-3)', marginTop: '0.3rem' }}>
                            {t('artifacts.annotation_ai_hint', { count: String(annotationsForVersion.length) }) ?? `The agent receives all ${annotationsForVersion.length} notes of this version`}
                          </p>
                        </div>
                      )}
                    </Stack>
                  </div>
                )}

                {detailTab === 'versions' && (
                  <div style={{ marginTop: '0.75rem', paddingTop: '0.75rem' }}>
                    {selected.provider !== 'url' && compareOptions.length > 1 && (
                      <div style={{ display: 'flex', gap: '0.45rem', alignItems: 'center', flexWrap: 'wrap', marginBottom: '0.65rem' }}>
                        <Button size="sm" kind="ghost" onClick={() => setDiffMode((v) => !v)}>
                          {diffMode ? (t('artifacts.diff_close') ?? 'Close diff') : (t('artifacts.diff_open') ?? 'Compare versions')}
                        </Button>
                        {diffMode && (
                          <>
                            <span style={{ fontSize: '0.75rem', color: 'var(--tm-text-2)' }}>{t('artifacts.diff_compare_with') ?? 'Compare with:'}</span>
                            <select
                              value={String(diffBase ?? defaultBase?.version ?? '')}
                              onChange={(e) => setDiffBase(Number(e.target.value))}
                              style={{ border: '1px solid var(--tm-border)', borderRadius: '6px', background: 'var(--tm-bg)', color: 'var(--tm-text)', fontSize: '0.78rem', padding: '0.3rem 0.5rem' }}
                            >
                              {compareOptions.filter((o) => o.version !== displayedVersion).map((o) => (
                                <option key={o.version} value={o.version}>{`v${o.version} · ${o.label}`}</option>
                              ))}
                            </select>
                          </>
                        )}
                      </div>
                    )}
                    {diffMode && compareOptions.length > 1 && baseOption ? (
                      <RichDiff
                        oldText={baseOption.content}
                        newText={bodyText}
                        oldLabel={`v${baseOption.version}`}
                        newLabel={`v${displayedVersion}`}
                      />
                    ) : diffMode ? (
                      <p style={{ color: 'var(--tm-text-3)', fontSize: '0.8rem' }}>{t('artifacts.diff_no_base') ?? 'No other version to compare with yet.'}</p>
                    ) : (
                      <VersionHistory artifactID={selected.id} project={project} defaultOpen />
                    )}
                  </div>
                )}

                {detailTab === 'settings' && (
                  <div style={{ marginTop: '0.75rem', paddingTop: '0.75rem' }}>
                    {/* Version-free attributes only: the body and its mime belong
                        to the document tab (the editor owns the body). org_unit
                        is create-time — shown read-only here. */}
                    <Stack gap={3}>
                      <TextInput
                        id="artifact-settings-title"
                        labelText={t('artifacts.title_label') ?? 'Title'}
                        value={settingsDraft.title ?? ''}
                        onChange={(e: React.ChangeEvent<HTMLInputElement>) => setSettingsDraft({ ...settingsDraft, title: e.target.value })}
                      />
                      <TextArea
                        id="artifact-settings-description"
                        labelText={t('artifacts.description_label') ?? 'Applicability (when to read this)'}
                        value={settingsDraft.description ?? ''}
                        onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => setSettingsDraft({ ...settingsDraft, description: e.target.value })}
                        rows={2}
                        helperText={t('artifacts.description_helper') ?? 'The agent matches the task against this condition — it decides whether artifact_read is worth the tokens'}
                      />
                      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '0.75rem' }}>
                        <Select
                          id="artifact-settings-kind"
                          labelText={t('artifacts.kind_label') ?? 'Kind'}
                          value={settingsDraft.kind ?? 'reference'}
                          onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setSettingsDraft({ ...settingsDraft, kind: e.target.value as Artifact['kind'] })}
                        >
                          <SelectItem value="reference" text={t('artifacts.kind_reference') ?? 'reference — regulation, instruction, checklist'} />
                          <SelectItem value="work" text={t('artifacts.kind_work_option') ?? 'work — saga work product (requirement, spec, report)'} />
                        </Select>
                        <Select
                          id="artifact-settings-provider"
                          labelText={t('artifacts.provider_label') ?? 'Storage'}
          
                          value={settingsDraft.provider ?? 'builtin'}
                          onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setSettingsDraft({ ...settingsDraft, provider: e.target.value as Artifact['provider'] })}
                        >
                          <SelectItem value="builtin" text={t('artifacts.provider_builtin') ?? 'builtin — body stored in the registry (≤ 2 MiB)'} />
                          <SelectItem value="url" text={t('artifacts.provider_url') ?? 'url — body lives in an external system'} />
                        </Select>
                      </div>
                      {(settingsDraft.provider ?? 'builtin') === 'url' && (
                        <TextInput
                          id="artifact-settings-external-ref"
                          labelText={t('artifacts.external_ref_label') ?? 'External reference'}
                          value={settingsDraft.external_ref ?? ''}
                          onChange={(e: React.ChangeEvent<HTMLInputElement>) => setSettingsDraft({ ...settingsDraft, external_ref: e.target.value })}
                          placeholder="https://wiki.internal.example.com/deploy-runbook"
                        />
                      )}
                      <div>
                        <SagaComboBox
                          id="artifact-settings-saga"
                          labelText={t('artifacts.saga_label') ?? 'Saga'}
                          sagas={sagas}
                          value={settingsDraft.saga_id ?? ''}
                          onChange={(sagaID) => { setSettingsDraft({ ...settingsDraft, saga_id: sagaID }); setSagaTouched(true) }}
                        />
                        <p style={{ fontSize: '0.72rem', color: 'var(--tm-text-3)', marginTop: '0.3rem' }}>
                          {sagaTouched
                            ? (t('artifacts.saga_helper_edit') ?? 'An empty field detaches the saga; the change lands on save')
                            : (t('artifacts.saga_current', { saga: settingsDraft.saga_id ?? '' }) ?? `Linked saga: ${settingsDraft.saga_id || '—'}`)}
                        </p>
                      </div>
                      {(settingsDraft.provider ?? 'builtin') === 'builtin' && (
                        <TextInput
                          id="artifact-settings-mime"
                          labelText={t('artifacts.mime_label') ?? 'MIME type'}
                          value={settingsDraft.mime_type ?? ''}
                          onChange={(e: React.ChangeEvent<HTMLInputElement>) => setSettingsDraft({ ...settingsDraft, mime_type: e.target.value })}
                          placeholder="text/markdown"
                          helperText={t('artifacts.mime_helper') ?? 'Leave empty for markdown (the default); change only when the body is not markdown'}
                        />
                      )}
                      <div>
                        <div style={{ fontSize: '0.78rem', color: 'var(--tm-text-3)', marginBottom: '0.3rem' }}>{t('artifacts.tags_label') ?? 'Tags'}</div>
                        <div style={{ display: 'flex', flexWrap: 'wrap', gap: '0.3rem', alignItems: 'center', border: '1px solid var(--tm-border)', borderRadius: '6px', padding: '0.35rem 0.5rem', minHeight: '2.2rem' }}>
                          {(settingsDraft.tags ?? []).map((tag) => (
                            <span key={tag} style={{ display: 'inline-flex', alignItems: 'center', gap: '0.2rem', padding: '0.1rem 0.3rem 0.1rem 0.45rem', borderRadius: 999, background: 'var(--tm-surface-3, rgba(127,127,127,.15))', fontSize: '0.78rem' }}>
                              #{tag}
                              <button type="button" aria-label={`remove ${tag}`} onClick={() => setSettingsDraft({ ...settingsDraft, tags: (settingsDraft.tags ?? []).filter((x) => x !== tag) })} style={{ background: 'none', border: 0, cursor: 'pointer', color: 'inherit', opacity: 0.7, display: 'inline-flex', padding: 0 }}>
                                <Close size={12} />
                              </button>
                            </span>
                          ))}
                          <TagEditorInput
                            draft={tagDraft}
                            onDraft={setTagDraft}
                            onCommit={(tag) => { setSettingsDraft({ ...settingsDraft, tags: [...new Set([...(settingsDraft.tags ?? []), tag.toLowerCase()])] }) }}
                            options={tagOptions}
                            exclude={settingsDraft.tags ?? []}
                          />
                        </div>
                      </div>
                      {selected.org_unit_id && (
                        <p style={{ fontSize: '0.75rem', color: 'var(--tm-text-3)' }}>
                          {t('artifacts.org_label') ?? 'Availability'}: <OrgBadge orgUnitID={selected.org_unit_id} org={org} />
                          {' · '}{t('artifacts.org_readonly_hint') ?? 'The org scope is set at creation and changes only through admins'}
                        </p>
                      )}
                      <div className="form-actions">
                        <Button disabled={settingsBusy || !(settingsDraft.title ?? '').trim()} onClick={() => void saveSettings()}>{t('action.save') ?? 'Save'}</Button>
                        <Button kind="secondary" disabled={settingsBusy} onClick={() => { openSettings(selected); setTagDraft('') }}>{t('action.cancel') ?? 'Reset'}</Button>
                      </div>
                    </Stack>
                  </div>
                )}

                {detailTab === 'document' && (
                <div style={{ marginTop: '0.75rem', paddingTop: '0.75rem' }}>
                  {/* Version-text toolbar: the version switch and the two
                      actions that belong to THIS version's text (download,
                      edit) — they used to sit in the card header and ate a
                      full row of it. */}
                  <div style={{ display: 'flex', gap: '0.45rem', alignItems: 'center', flexWrap: 'wrap', marginBottom: '0.65rem' }}>
                    {revisionOpen && revision && !bodyEditing && (
                      <>
                        <Button size="sm" kind={showRevisionBody ? 'ghost' : 'primary'} onClick={() => { setShowRevisionBody(false); setBodyEditing(false) }}>
                          {t('artifacts.body_current_version', { version: String(selected.version ?? 1) }) ?? `v${selected.version ?? 1} · current`}
                        </Button>
                        <Button size="sm" kind={showRevisionBody ? 'primary' : 'ghost'} onClick={() => { setShowRevisionBody(true); setBodyEditing(false) }}>
                          {t('artifacts.body_revision', { version: String(revision.version) }) ?? `v${revision.version} · revision`}{revision.status === 'in_review' ? ` — ${t('artifacts.status_in_review') ?? 'in review'}` : ''}
                        </Button>
                        <span style={{ width: '1px', height: '1.4rem', background: 'var(--tm-border)', margin: '0 0.2rem' }} />
                      </>
                    )}
                    					{!bodyEditing && (
                    						<Button
                    							size="sm"
                    							kind="secondary"
                    							renderIcon={Edit}
                    							disabled={selected.provider === 'url' || (selected.status ?? '') === 'in_review' || revisionInReview || selected.status === 'deprecated'}
                    							title={selected.provider === 'url' ? (t('artifacts.edit_locked_url') ?? 'The body lives in the external system') : ((selected.status ?? '') === 'in_review' || revisionInReview) ? (t('artifacts.edit_locked_review') ?? 'Locked while a review is open') : selected.status === 'deprecated' ? (t('artifacts.deprecated_locked') ?? 'Устаревший документ — верните в работу, чтобы править') : undefined}
                    							onClick={() => void startEdit(selected)}
						>{editLabel(selected.status, revisionOpen && revisionEditable)}</Button>
					)}
					{/* Retirement rides the version toolbar: it is a statement
					    about the current text. Deprecated flips back to draft. */}
                    					{!bodyEditing && selected.status === 'approved' && (
                    						<Button
                    							size="sm"
                    							kind="ghost"
                    							hasIconOnly
                    							renderIcon={WarningFilled}
                    							iconDescription={t('artifacts.deprecate') ?? 'Устарел'}
                    							tooltipPosition="bottom"
                    							onClick={() => void deprecate(selected)}
                    						/>
                    					)}
					                    {!bodyEditing && selected.status === 'deprecated' && (
					                      <Button size="sm" kind="ghost" renderIcon={Restart} onClick={() => void revive(selected)}>{t('artifacts.revive') ?? 'Вернуть в работу'}</Button>
					                    )}
					                    {selected.provider !== 'url' && (
                    						<Button
                    							size="sm"
                    							kind="ghost"
                    							hasIconOnly
                    							renderIcon={Download}
                    							iconDescription={t('artifacts.download_md') ?? 'Download .md'}
                    							tooltipPosition="bottom"
                    							style={{ marginLeft: 'auto' }}
                    							onClick={() => {
                    								setError('')
                    								void exportArtifactMarkdown(selected.id, showRevisionBody && revision ? revision.version : undefined).catch((f) => setError(String((f as Error).message ?? f)))
                    							}}
                    						/>
                    					)}
                    					{bodyEditing && <span style={{ fontSize: '0.78rem', color: 'var(--tm-text-2)' }}>{t('artifacts.editing_now') ?? 'Редактирование'}</span>}
                  </div>
                  {selected.provider === 'url' ? (
                    selected.external_ref
                      ? <p style={{ fontSize: '0.85rem', wordBreak: 'break-all' }}>
                          {t('artifacts.external_body_hint') ?? 'The body lives in the external system:'}{' '}
                          <a href={selected.external_ref} target="_blank" rel="noreferrer">{selected.external_ref}</a>
                        </p>
                      : <p style={{ color: 'var(--tm-text-3)', fontSize: '0.8rem' }}>{t('artifacts.external_body_missing') ?? 'No external reference set.'}</p>
                  ) : bodyEditing ? (
                    <>
                      {selected.status === 'approved' && (
                        <InlineNotification kind="info" lowContrast hideCloseButton
                          title={t('artifacts.new_revision_banner_title') ?? 'New revision'}
                          subtitle={t('artifacts.new_revision_banner') ?? 'The edit is saved as draft revision; the approved version keeps serving agents and skills until the revision is approved.'}
                          style={{ marginBottom: '0.6rem' }} />
                      )}
                      <ArtifactEditor
                        key={`${selected.id}:${displayedVersion}`}
                        markdown={editDraft}
                        onChange={setEditDraft}
                      />
                      <div style={{ display: 'flex', gap: '0.45rem', marginTop: '0.6rem', alignItems: 'center' }}>
                        <Button disabled={editBusy || editDraft === (showRevisionBody && revision ? (revision.content ?? '') : selectedBody)} onClick={() => void saveEdit()}>{t('action.save') ?? 'Save'}</Button>
                        <Button kind="secondary" disabled={editBusy} onClick={() => { setBodyEditing(false); setEditDraft('') }}>{t('action.cancel') ?? 'Cancel'}</Button>
                        {editBusy && <span style={{ fontSize: '0.75rem', color: 'var(--tm-text-3)' }}>{t('common.loading') ?? 'Working…'}</span>}
                      </div>
                    </>
                  ) : bodyText ? (
                    <>
                      <div className="artifact-body artifact-review-body" ref={artifactBodyRef}>
                        <MarkdownWithPresentations content={bodyText} project={project} />
                      </div>
                      {/* Clicking inside either popup must NOT collapse the
                          selection — the menu survives until an explicit
                          dismiss (outside click, Escape, scroll). Inputs are
                          exempt from the hold: the caret belongs to them. */}
                      {selectionMenu && !annotationDraft && (
                        <div
                          className={`artifact-selection-menu${selectionMenu.below ? ' below' : ''}`}
                          style={{ left: selectionMenu.x, top: selectionMenu.y }}
                          onMouseDown={(event) => { if (!(event.target as HTMLElement).closest('input, textarea')) event.preventDefault() }}
                        >
                          {/* Verdict icons, colored and large: the dark-on-dark
                              text row was unreadable, and the verdicts read
                              instantly as green-check / red-cross. */}
                          {([
                            ['comment', Chat, 'var(--tm-amber)'],
                            ['change', Edit, 'var(--tm-teal)'],
                            ['approve', Checkmark, 'var(--tm-success)'],
                            ['reject', Close, 'var(--tm-danger)'],
                          ] as const).map(([kind, Icon, color]) => (
                            <button
                              key={kind}
                              type="button"
                              className={`artifact-ann-pick artifact-ann-pick-${kind}`}
                              title={t(`artifacts.annotation_${kind}`) ?? kind}
                              aria-label={t(`artifacts.annotation_${kind}`) ?? kind}
                              onClick={() => setAnnotationDraft({ quote: selectionMenu.quote, kind, comment: '', replacement: '' })}
                            >
                              <Icon size={20} style={{ color }} />
                            </button>
                          							))}
                          							<span className="artifact-ann-pick-divider" />
                          							{/* No close button: any click outside, Escape and a collapsed
                          							    selection already dismiss the menu (selectionchange). */}
                          						</div>
                          					)}
                      {selectionMenu && annotationDraft && (
                        <div
                          className={`artifact-annotation-editor${selectionMenu.below ? ' below' : ''}`}
                          style={{ left: selectionMenu.x, top: selectionMenu.y }}
                          onMouseDown={(event) => { if (!(event.target as HTMLElement).closest('input, textarea')) event.preventDefault() }}
                        >
                          <div className="artifact-annotation-editor-title">{t(`artifacts.annotation_${annotationDraft.kind}`) ?? annotationDraft.kind}</div>
                          <blockquote>{annotationDraft.quote}</blockquote>
                          {annotationDraft.kind === 'change' && <TextArea id="artifact-annotation-replacement" labelText={t('artifacts.annotation_replacement') ?? 'Suggested replacement'} value={annotationDraft.replacement} onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => setAnnotationDraft({ ...annotationDraft, replacement: e.target.value })} rows={3} autoFocus />}
                          {annotationDraft.kind !== 'approve' && annotationDraft.kind !== 'reject' && <TextArea id="artifact-annotation-comment" labelText={t('artifacts.annotation_comment') ?? 'Comment'} value={annotationDraft.comment} onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => setAnnotationDraft({ ...annotationDraft, comment: e.target.value })} rows={2} autoFocus />}
                          							<Stack orientation="horizontal" gap={2}>
                          								<Button size="sm" disabled={busy || (annotationDraft.kind === 'change' && !annotationDraft.replacement.trim())} onClick={() => void saveAnnotation()}>{t('action.save') ?? 'Save'}</Button>
                          								<Button size="sm" kind="ghost" onClick={closeSelectionMenu}>{t('action.cancel') ?? 'Cancel'}</Button>
                          							</Stack>
                          						</div>
                          					)}
					{/* The active fragment's note, pinned to the text: kind, quote,
					    replacement, comment, author — with edit/retract right here,
					    so reading a remark never means scrolling to the rail. Hidden
					    while the selection draft editor is up — one overlay at a
					    time, or the two collide over its Save button. */}
					{activeItem && annPopoverAnchor && !annotationDraft && !selectionMenu && (
                          						<div
                          							ref={annPopoverRef}
                          							className={`artifact-note-popover${annPopoverAnchor.placement === 'above' ? ' above' : ''}`}
                          							style={{ left: annPopoverAnchor.left, top: annPopoverAnchor.top }}
                          							onMouseDown={(event) => event.preventDefault()}
                          						>
                          							<div className="artifact-note-popover-head">
                          								<strong style={{ fontSize: '0.75rem' }}>{t(`artifacts.annotation_${activeItem.kind}`) ?? activeItem.kind}</strong>
                          								<span style={{ fontSize: '0.68rem', color: 'var(--tm-text-3)' }}>{userNameOf(activeItem.created_by)} · {fmtTime(activeItem.created_at)}</span>
                          							</div>
                          							<blockquote className="artifact-note-popover-quote">{activeItem.quote}</blockquote>
                          							{!annPopoverEdit && (
                          								<>
                          									{activeItem.replacement && <p className="artifact-note-popover-line">→ {activeItem.replacement}</p>}
                          									{activeItem.comment && <p className="artifact-note-popover-line">{activeItem.comment}</p>}
                          								</>
                          							)}
							{annPopoverEdit && (
								<div style={{ display: 'grid', gap: '0.4rem', marginTop: '0.35rem' }}>
									{activeItem.kind === 'change' && <TextArea id="artifact-note-edit-replacement" labelText={t('artifacts.annotation_replacement') ?? 'Suggested replacement'} value={annEditDraft.replacement} onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => setAnnEditDraft({ ...annEditDraft, replacement: e.target.value })} rows={3} autoFocus />}
									{activeItem.kind !== 'approve' && activeItem.kind !== 'reject' && <TextArea id="artifact-note-edit-comment" labelText={t('artifacts.annotation_comment') ?? 'Comment'} value={annEditDraft.comment} onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => setAnnEditDraft({ ...annEditDraft, comment: e.target.value })} rows={2} />}
								</div>
							)}
							{/* One action line in both modes: save/cancel while editing,
							    edit while reading; the retract is icon-only so the row
							    never wraps (full text on the hint). */}
							<div style={{ display: 'flex', gap: '0.4rem', marginTop: '0.45rem', alignItems: 'center' }}>
								{annPopoverEdit ? (
									<>
										<Button size="sm" disabled={busy || (activeItem.kind === 'change' && !annEditDraft.replacement.trim())} onClick={() => void saveAnnotationEdit(activeItem)}>{t('action.save') ?? 'Save'}</Button>
										<Button size="sm" kind="ghost" onClick={() => setAnnPopoverEdit(false)}>{t('action.cancel') ?? 'Cancel'}</Button>
									</>
								) : (
									canEditAnnotation(activeItem) && (
										<Button size="sm" kind="ghost" renderIcon={Edit} onClick={() => { setAnnEditDraft({ comment: activeItem.comment ?? '', replacement: activeItem.replacement ?? '' }); setAnnPopoverEdit(true) }}>{t('action.edit') ?? 'Edit'}</Button>
									)
								)}
								{canDeleteAnnotation(activeItem) && (
									<Button size="sm" kind="danger--ghost" hasIconOnly renderIcon={TrashCan} iconDescription={t('artifacts.annotation_delete') ?? 'Retract note'} tooltipPosition="top" style={{ marginLeft: 'auto' }} onClick={() => void removeAnnotation(activeItem)} />
								)}
							</div>
                          						</div>
                          					)}
                      {annotationsForVersion.length > 0 && (
                        <div className="artifact-review-rail">
                          <div className="artifact-review-rail-header">
                            <strong>{t('artifacts.annotations_title') ?? 'Review notes'} · {annotationsForVersion.length}</strong>
                          </div>
                          {[...annotationsForVersion].sort((a, b) => annotationKindPriority(a.kind) - annotationKindPriority(b.kind)).map((item) => (
                            <div key={item.id} data-note-id={item.id}>
                              <button
                                className={`artifact-review-note${activeAnnotation === item.id ? ' artifact-review-note-active' : ''}`}
                                type="button"
                                onClick={() => focusAnnotation(item)}
                                style={{ borderLeftColor: item.kind === 'reject' ? 'var(--tm-danger)' : item.kind === 'approve' ? 'var(--tm-success)' : 'var(--tm-amber)', width: 'calc(100% - 2rem)', display: 'inline-block' }}
                              >
                                <strong style={{ fontSize: '0.75rem' }}>{t(`artifacts.annotation_${item.kind}`) ?? item.kind}</strong>
                                <blockquote style={{ margin: '0.25rem 0', fontSize: '0.78rem' }}>{item.quote}</blockquote>
                                {item.replacement && <p style={{ fontSize: '0.78rem' }}>→ {item.replacement}</p>}
                                {item.comment && <p style={{ fontSize: '0.78rem' }}>{item.comment}</p>}
                                <span style={{ fontSize: '0.68rem', color: 'var(--tm-text-3)' }}>{userNameOf(item.created_by)} · {fmtTime(item.created_at)}</span>
                              </button>
                              {canDeleteAnnotation(item) && (
                                <button
                                  type="button"
                                  aria-label={t('action.delete') ?? 'Delete'}
                                  title={t('artifacts.annotation_delete') ?? 'Retract note'}
                                  onClick={() => void removeAnnotation(item)}
                                  style={{ background: 'none', border: 0, cursor: 'pointer', color: 'var(--tm-text-3)', fontSize: '0.72rem', padding: '0 0.3rem', verticalAlign: 'top' }}
                                >✕</button>
                              )}
                            </div>
                          ))}
                        </div>
                      )}
                      {bodyTruncated && !showRevisionBody && (
                        <p style={{ color: 'var(--tm-text-3)', fontSize: '0.72rem', marginTop: '0.5rem' }}>
                          {t('artifacts.body_truncated') ?? 'The body is shown partially; agents read it in 32 KiB chunks.'}
                        </p>
                      )}
                      {/* The decision rides the document: an approver reads the
                          text and settles the round without leaving the tab —
                          the full round cards live in the review tab. */}
                      {openReview && (
                        <div style={{ marginTop: '0.75rem', borderTop: '1px solid var(--tm-border)', paddingTop: '0.6rem', display: 'flex', gap: '0.45rem', alignItems: 'center', flexWrap: 'wrap' }}>
                          <strong style={{ fontSize: '0.8rem' }}>{t('artifacts.decision_here') ?? 'Ваше решение по этой версии:'}</strong>
                          <Button size="sm" renderIcon={CheckmarkOutline} disabled={busy} onClick={() => void decideOpenRound(true)}>{t('artifacts.approve') ?? 'Approve'}</Button>
                          <Button size="sm" kind="danger" renderIcon={CloseOutline} disabled={busy} onClick={() => void decideOpenRound(false)}>{t('artifacts.reject') ?? 'Reject'}</Button>
                          <span style={{ fontSize: '0.72rem', color: 'var(--tm-text-3)' }}>{t('artifacts.decision_in_review_tab') ?? 'раунды и замечания — в табе «Согласование»'}</span>
                        </div>
                      )}
                    </>
                  ) : (
                    <p style={{ color: 'var(--tm-text-3)', fontSize: '0.8rem' }}>{t('artifacts.body_empty') ?? 'No stored body.'}</p>
                  )}
                </div>
                )}
              </Tile>
            )
          })()}
        </div>
      </div>

      {showForm && (
        <AppModal onClose={() => { setShowForm(false); setEditing(null) }} panelStyle={{ width: '560px' }}>
          <Heading>{t('artifacts.new') ?? 'New Artifact'}</Heading>
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
            <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '0.75rem' }}>
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
                labelText={t('artifacts.provider_label') ?? 'Storage'}
                value={form.provider ?? 'builtin'}
                onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setForm({ ...form, provider: e.target.value as Artifact['provider'] })}
              >
                <SelectItem value="builtin" text={t('artifacts.provider_builtin') ?? 'builtin — body stored in the registry (≤ 2 MiB)'} />
                <SelectItem value="url" text={t('artifacts.provider_url') ?? 'url — body lives in an external system'} />
              </Select>
            </div>
            {(form.provider ?? 'builtin') === 'url' && (
              <TextInput
                id="artifact-external-ref"
                labelText={t('artifacts.external_ref_label') ?? 'External reference'}
                value={form.external_ref ?? ''}
                onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, external_ref: e.target.value })}
                placeholder="https://wiki.internal.example.com/deploy-runbook"
                helperText={t('artifacts.external_ref_helper') ?? 'Absolute http(s) URL; the sandbox never dials out — the kernel proxies the fetch'}
              />
            )}
            <div>
              <div style={{ fontSize: '0.78rem', color: 'var(--tm-text-3)', marginBottom: '0.3rem' }}>{t('artifacts.saga_label') ?? 'Saga'}</div>
              <SagaComboBox
                id="artifact-saga"
                labelText={t('artifacts.saga_label') ?? 'Saga'}
                sagas={sagas}
                value={form.saga_id ?? ''}
                onChange={(sagaID) => setForm({ ...form, saga_id: sagaID })}
              />
              <p style={{ fontSize: '0.72rem', color: 'var(--tm-text-3)', marginTop: '0.3rem' }}>{t('artifacts.saga_helper') ?? 'Optional: the saga this work product belongs to; company/department regulations do not need one'}</p>
            </div>
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
            {(form.provider ?? 'builtin') === 'builtin' && (
              <TextArea
                id="artifact-content"
                labelText={t('artifacts.content_label') ?? 'Initial text (optional)'}
                value={form.content ?? ''}
                onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => setForm({ ...form, content: e.target.value })}
                rows={4}
                placeholder="# Standard…"
                helperText={t('artifacts.content_optional_helper') ?? 'Paste a draft or leave empty — the document is written in the big editor after creating'}
              />
            )}
            <div className="form-actions">
              <Button onClick={() => void save()} disabled={busy || !form.title?.trim() || (!editing && !form.id?.trim()) || ((form.provider ?? 'builtin') === 'url' && !form.external_ref?.trim())}>{t('action.save') ?? 'Save'}</Button>
              <Button kind="secondary" onClick={() => { setShowForm(false); setEditing(null) }}>{t('action.cancel') ?? 'Cancel'}</Button>
            </div>
          </Stack>
        </AppModal>
      )}

      {revisionAgentOpen && selected && project && (
        <AppModal onClose={() => setRevisionAgentOpen(false)} panelStyle={{ width: '520px' }}>
          <Heading>{selected.status === 'approved' ? (t('artifacts.annotation_ai_revision') ?? 'Apply notes and submit a new version') : (t('artifacts.annotation_ai_revision') ?? 'Send notes for rework')}</Heading>
          <Stack gap={3}>
            <p style={{ color: 'var(--tm-text-3)', fontSize: '0.82rem' }}>{selected.title}</p>
            {revisionAgents.length > 0 ? (
              <Select id="artifact-revision-agent" labelText={t('artifacts.annotation_agent') ?? 'Agent'} value={revisionAgentID} onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setRevisionAgentID(e.target.value)}>
                {revisionAgents.map((agent) => <SelectItem key={agent.id} value={agent.id} text={agent.name} />)}
              </Select>
            ) : <InlineNotification kind="warning" lowContrast title={t('artifacts.annotation_no_agent') ?? 'No agents are available in this project'} />}
            <Button disabled={busy || !revisionAgentID} onClick={async () => {
              setBusy(true)
              try {
                const version = showRevisionBody && revision ? revision.version : (selected.version ?? 1)
                const baseVersion = selected.status === 'approved' ? version : (revision?.version ?? version)
                const prompt = `Revise artifact ${selected.id} (${selected.title}) in response to the human's fragment review. First call artifact_feedback with artifact_id=${selected.id} and version=${version} to fetch the complete notes from the workspace, then call artifact_read. Apply suggested replacements, preserve approved fragments, address rejected fragments, and resolve comments/questions. For an approved source artifact call artifact_update with new_version=true; for an editable draft update it in place. Use exact-fragment patches for large documents. After editing, call artifact_submit for artifact ${selected.id}, reviewers ["user:<initiating human>"] (the run's actor_id), with a note that this version addresses the fragment feedback. Do not stop after editing: the task is complete only when artifact_submit succeeds. Report the submitted version and any unresolved questions. Source version: ${baseVersion}.`
                const task = await workspaceApi.createTask({ project_id: project, agent_id: revisionAgentID, title: `Revise artifact: ${selected.title}`, prompt })
                await workspaceApi.startRun(task.id, { agent_id: revisionAgentID })
                setRevisionAgentOpen(false)
              } catch (f) { setError(message(f)) }
              finally { setBusy(false) }
            }}>{selected.status === 'approved' ? (t('artifacts.annotation_start_agent_approved') ?? 'Apply notes and submit for approval') : (t('artifacts.annotation_start_agent_rework') ?? 'Send for rework')}</Button>
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
function VersionHistory({ artifactID, project, defaultOpen }: { artifactID: string; project?: string; defaultOpen?: boolean }) {
  const t = useT()
  const [open, setOpen] = useState(Boolean(defaultOpen))
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
                  {t('artifacts.versions_superseded') ?? 'superseded'} {v.superseded_at ? new Date(v.superseded_at).toLocaleString([uiLocale()]) : ''}
                </span>
              </div>
              {expanded === v.version && (
                <div style={{ padding: '0 0.55rem 0.55rem' }}>
                  <div className="artifact-body" style={{ border: '1px dashed var(--tm-border)', borderRadius: '6px', padding: '0.5rem', maxHeight: '22rem', overflowY: 'auto' }}>
                    <MarkdownWithPresentations content={v.content || ''} project={project} />
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

/** The tag editor's suggest field: a styled dropdown instead of the native
 *  datalist (browser chrome looked alien and only prefix-matched). Focus
 *  shows the most-used tags first — the «last used on click» affordance;
 *  typing narrows by substring, sorted by usage count. Enter commits the
 *  typed tag, a click commits the suggestion; already-present tags never
 *  suggest. */
function TagEditorInput({ draft, onDraft, onCommit, options, exclude }: {
  draft: string
  onDraft: (value: string) => void
  onCommit: (tag: string) => void
  options: Array<{ tag: string; count: number }>
  exclude: string[]
}) {
  const [focused, setFocused] = useState(false)
  const [highlighted, setHighlighted] = useState(-1)
  const suggestions = useMemo(() => {
    const needle = draft.trim().toLowerCase()
    return options
      .filter(({ tag }) => !exclude.includes(tag) && (!needle || tag.toLowerCase().includes(needle)))
      .slice(0, 8)
  }, [options, exclude, draft])
  const commit = (tag: string) => {
    onDraft('')
    setHighlighted(-1)
    setFocused(false)
    onCommit(tag)
  }
  return (
    <span style={{ position: 'relative', display: 'inline-flex', minWidth: 0 }}>
      <input
        value={draft}
        onChange={(e) => { onDraft(e.target.value); setHighlighted(-1); setFocused(true) }}
        onFocus={() => setFocused(true)}
        onBlur={() => setTimeout(() => setFocused(false), 150)}
        onKeyDown={(e: React.KeyboardEvent<HTMLInputElement>) => {
          if (e.key === 'Enter') {
            e.preventDefault()
            const pick = highlighted >= 0 ? suggestions[highlighted]?.tag : draft.trim()
            if (pick) commit(pick)
          } else if (e.key === 'Escape') {
            setFocused(false)
            setHighlighted(-1)
          } else if (e.key === 'ArrowDown' && suggestions.length > 0) {
            e.preventDefault()
            setFocused(true)
            setHighlighted((v) => Math.min(v + 1, suggestions.length - 1))
          } else if (e.key === 'ArrowUp' && suggestions.length > 0) {
            e.preventDefault()
            setHighlighted((v) => Math.max(v - 1, -1))
          }
        }}
        list={undefined}
        placeholder="+ tag"
        style={{ border: '1px dashed var(--tm-border)', borderRadius: 999, background: 'transparent', color: 'var(--tm-text-2)', fontSize: '0.72rem', padding: '0.1rem 0.45rem', width: '7rem', outline: 'none' }}
      />
      {focused && suggestions.length > 0 && (
        <span style={{ position: 'absolute', left: 0, top: '100%', marginTop: '0.2rem', background: 'var(--tm-elevated)', border: '1px solid var(--tm-border)', borderRadius: '8px', zIndex: 5, display: 'flex', flexDirection: 'column', overflow: 'hidden', minWidth: '10rem', boxShadow: '0 4px 16px rgba(0,0,0,0.25)' }}>
          {suggestions.map(({ tag, count }, index) => (
            <button
              key={tag}
              type="button"
              onMouseDown={(e) => { e.preventDefault(); commit(tag) }}
              onMouseEnter={() => setHighlighted(index)}
              style={{ display: 'flex', alignItems: 'center', gap: '0.4rem', width: '100%', padding: '0.3rem 0.6rem', background: index === highlighted ? 'var(--tm-raised)' : 'none', border: 'none', cursor: 'pointer', color: 'var(--tm-text)', fontSize: '0.72rem', textAlign: 'left' }}
            >
              <span style={{ color: 'var(--tm-teal)' }}>#{tag}</span>
              <span style={{ color: 'var(--tm-text-3)', fontSize: '0.65rem', marginLeft: 'auto' }}>×{count}</span>
            </button>
          ))}
        </span>
      )}
    </span>
  )
}
