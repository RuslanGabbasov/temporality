import { Fragment, useEffect, useState, useCallback, useRef, type ChangeEvent, type CSSProperties, type ReactNode } from 'react'
import { Button, Checkbox, Heading, InlineNotification, Tag } from '@carbon/react'
import { Copy, Download, Renew, Settings, TrashCan, Upload, ChevronRight, ChevronDown, Checkbox as CheckboxIcon } from '@carbon/icons-react'
import ProjectRequired from './ProjectRequired'
import Markdown from './Markdown'
import { useConfirm } from './ConfirmDialog'
import AppModal from './Modal'
import { useT } from './i18n'
import { useDebouncedValue } from './useDebouncedValue'
import {
  storageListDir,
  storageFileContent,
  storageDownloadFile,
  storageDeleteFile,
  storageUploadFile,
  storageSearch,
  storageOverview,
  storageSweep,
  storageGetPolicies,
  storagePutPolicies,
  storageFileHistory,
  storageRunFiles,
  type StorageFileEntry,
  type StorageFileChange,
  type StorageRunFile,
  type StorageSearchHit,
  type StorageSearchResponse,
  type StorageSweepBody,
  type StorageSweepResult,
  type StoragePolicies,
} from './kernelApi'
import { fmtBytes } from './format'
import { highlightCode, languageForFile } from './syntax'

type AreaName = 'workspace' | 'cache'
type TreeSort = 'name' | 'size' | 'date'
type SweepAge = '7d' | '30d' | 'any'

const TEXT_EXT = /\.(md|txt|log|json|ya?ml|toml|go|ts|tsx|js|py|sh|sql|css|html|csv|env|conf|ini|xml|svg)$/i
const MARKDOWN_EXT = /\.(md|markdown)$/i
const IMAGE_EXT = /\.(png|jpe?g|gif|webp|svg)$/i

/** The search endpoint refuses shorter queries — the UI mirrors that. */
const SEARCH_MIN_CHARS = 2
/** How many plan paths the sweep dialog lists before the "…and N more" tail. */
const SWEEP_PLAN_SHOWN = 20
const AGE_HOURS: Record<SweepAge, number | undefined> = { '7d': 168, '30d': 720, any: undefined }

interface BatchItem { area: AreaName; path: string; name: string; bytes: number; dir: boolean }

interface SearchState {
  status: 'idle' | 'loading' | 'ready' | 'error'
  res?: StorageSearchResponse
  error?: string
}

// ── Attribution (file.changed): the card's change history and the run's
// file list — both degrade to nothing when the journal endpoint is missing
// on an older deployment, never to a page-level error. ──
interface HistoryState {
  status: 'idle' | 'loading' | 'ready' | 'error'
  changes: StorageFileChange[]
}

interface RunFilesState {
  run: string
  status: 'loading' | 'ready' | 'error'
  files: StorageRunFile[]
  error?: string
}

interface SweepState {
  step: 'form' | 'plan' | 'done'
  areas: Record<AreaName, boolean>
  age: SweepAge
  plan: StorageSweepResult | null
  result: StorageSweepResult | null
  busy: boolean
  err: string
}

interface PoliciesState {
  loading: boolean
  form: { enabled: boolean; cache_ttl_hours: string; workspace_stale_hours: string; quota_bytes: string } | null
  saved: boolean
  busy: boolean
  err: string
}

function message(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }

function initialSweep(): SweepState {
  return { step: 'form', areas: { cache: true, workspace: false }, age: '30d', plan: null, result: null, busy: false, err: '' }
}

/** Client-side ordering for the lazily loaded tree. Dirs always come first:
 *  the dir API exposes only entry counts, so «size» orders folders by that. */
function sortEntries(list: StorageFileEntry[], sort: TreeSort): StorageFileEntry[] {
  const copy = [...list]
  const dirsFirst = (a: StorageFileEntry, b: StorageFileEntry) => (b.dir ? 1 : 0) - (a.dir ? 1 : 0)
  if (sort === 'size') {
    copy.sort((a, b) => dirsFirst(a, b) || (b.dir ? (b.entries ?? 0) - (a.entries ?? 0) : b.bytes - a.bytes))
  } else if (sort === 'date') {
    copy.sort((a, b) => dirsFirst(a, b) || (Date.parse(b.mod_at ?? '') || 0) - (Date.parse(a.mod_at ?? '') || 0))
  } else {
    copy.sort((a, b) => dirsFirst(a, b) || a.name.localeCompare(b.name))
  }
  return copy
}

/** Quota fill colour: green below 70 %, yellow below 90 %, red above. */
function quotaColor(ratio: number): string {
  if (ratio < 0.7) return 'var(--tm-success)'
  if (ratio < 0.9) return 'var(--tm-warning)'
  return 'var(--tm-danger)'
}

/** Disk fill colour (inverted thresholds on free space): healthy ≥30 % free,
 *  warning ≥10 %, critical below. */
function diskColor(freeRatio: number): string {
  if (freeRatio >= 0.3) return 'var(--tm-success)'
  if (freeRatio >= 0.1) return 'var(--tm-warning)'
  return 'var(--tm-danger)'
}

function entryFromHit(hit: StorageSearchHit): StorageFileEntry {
  return {
    name: hit.path.split('/').pop() ?? hit.path,
    path: hit.path,
    dir: hit.dir,
    bytes: hit.bytes,
    mod_at: hit.mod_at,
  }
}

/** Carbon tag colour per recorded mutation — delete reads red, write blue. */
function opTagType(op: string): 'gray' | 'blue' | 'purple' | 'teal' | 'red' {
  switch (op) {
    case 'write': return 'blue'
    case 'edit': return 'purple'
    case 'upload': return 'teal'
    case 'delete': return 'red'
    default: return 'gray'
  }
}

/** Case-insensitive substring highlight for search results. */
function highlightMatch(text: string, query: string): ReactNode {
  const idx = text.toLowerCase().indexOf(query.toLowerCase())
  if (idx < 0 || !query) return text
  return (
    <>
      {text.slice(0, idx)}
      <mark style={{ background: 'var(--tm-chart-3)', color: '#161616', padding: 0 }}>{text.slice(idx, idx + query.length)}</mark>
      {text.slice(idx + query.length)}
    </>
  )
}

const inputStyle = {
  padding: '0.3rem 0.45rem',
  border: '1px solid var(--tm-border)',
  borderRadius: '4px',
  background: 'var(--tm-surface)',
  color: 'var(--tm-text-1)',
  fontSize: '0.78rem',
  width: '100%',
  boxSizing: 'border-box',
} as const

const fieldLabelStyle = { fontSize: '0.7rem', textTransform: 'uppercase', letterSpacing: '0.05em', color: 'var(--tm-text-3)', marginBottom: '0.3rem' } as const

const dialogTitleStyle = { margin: '0 0 0.75rem', fontSize: '1rem', fontWeight: 600 } as const

const meterTrackStyle = { height: '6px', borderRadius: '3px', background: 'var(--tm-border)', overflow: 'hidden', flex: '1 1 90px', minWidth: '70px' } as const
const meterFillStyle = { height: '100%', borderRadius: '3px' } as const

/** Storage page (docs/sandbox-storage.md §7.2): inventory dashboard on top,
 *  a lazy Finder-style tree or a flat debounced search below, and bulk
 *  operations (select → delete/download, sweep with dry-run, retention
 *  policies). Reads need reader rights; sweep/policies are operator-gated
 *  server-side — permission errors surface as plain notifications here. */
export default function Storage({ project }: { project: string }) {
  const t = useT()
  const { confirmDialog, dialog: confirmDialogNode } = useConfirm()

  // ── Tree ──
  const [areas, setAreas] = useState<Record<AreaName, StorageFileEntry[] | null>>({ workspace: null, cache: null })
  const [children, setChildren] = useState<Record<string, StorageFileEntry[]>>({})
  const [expanded, setExpanded] = useState<Set<string>>(new Set())
  const [loadingDir, setLoadingDir] = useState<string | null>(null)
  const [treeSort, setTreeSort] = useState<TreeSort>('name')
  const [error, setError] = useState('')

  // ── Preview / selection ──
  const [selected, setSelected] = useState<{ area: AreaName; entry: StorageFileEntry } | null>(null)
  const [preview, setPreview] = useState<{ kind: 'text' | 'markdown' | 'image'; content: string; html?: string } | null>(null)
  const [previewError, setPreviewError] = useState('')
  const [busy, setBusy] = useState(false)
  const [copied, setCopied] = useState(false)
  const copiedTimer = useRef<number | null>(null)
  const uploadTarget = useRef<{ area: AreaName; dir: string }>({ area: 'workspace', dir: '' })
  const uploadInput = useRef<HTMLInputElement | null>(null)

  // ── Search ──
  const [query, setQuery] = useState('')
  const searchQ = useDebouncedValue(query)
  const [search, setSearch] = useState<SearchState>({ status: 'idle' })
  const searchSeq = useRef(0)

  // ── Attribution ──
  const [history, setHistory] = useState<HistoryState>({ status: 'idle', changes: [] })
  const historySeq = useRef(0)
  const [runFiles, setRunFiles] = useState<RunFilesState | null>(null)
  const runFilesSeq = useRef(0)

  // ── Bulk selection ──
  const [selectMode, setSelectMode] = useState(false)
  const [batch, setBatch] = useState<Record<string, BatchItem>>({})
  const [deleteProgress, setDeleteProgress] = useState<{ done: number; total: number } | null>(null)
  const [batchNote, setBatchNote] = useState('')

  // Shared refresh trigger: refreshAll bumps it and the debounced search
  // re-fetches its current query.
  const [refreshTick, setRefreshTick] = useState(0)

  // ── Area header stats: the workspace total + free disk, shown in the
  // workspace row where the header has room. Read-only numbers from the
  // hourly snapshot; a failed fetch hides them instead of failing the page. ──
  const [areaStats, setAreaStats] = useState<{ wsBytes?: number; free?: number } | null>(null)

  // ── Dialogs ──
  const [sweep, setSweep] = useState<SweepState | null>(null)
  const [policies, setPolicies] = useState<PoliciesState | null>(null)

  const key = (area: AreaName, path: string) => `${area}:${path}`
  const searchMode = query.trim().length >= SEARCH_MIN_CHARS
  const areaLabel = (area: AreaName) => (area === 'workspace' ? t('storage.area_workspace') : t('storage.area_cache'))

  const fetchRoots = useCallback(async (): Promise<Record<AreaName, StorageFileEntry[]>> => {
    const [ws, cache] = await Promise.all([
      storageListDir(project.trim(), 'workspace', ''),
      storageListDir(project.trim(), 'cache', ''),
    ])
    return { workspace: ws.entries, cache: cache.entries }
  }, [project])

  const load = useCallback(async () => {
    if (!project.trim()) return
    setError('')
    try {
      const roots = await fetchRoots()
      setAreas(roots)
      setChildren({})
      setExpanded(new Set())
      setSelected(null)
      setPreview(null)
      setPreviewError('')
      setBatch({})
      setBatchNote('')
    } catch (f) { setError(message(f)) }
  }, [project, fetchRoots])

  useEffect(() => {
    setQuery('')
    setSelectMode(false)
    void load()
  }, [load])

  // Refresh = full tree reload + re-fetch of search results and overview.
  const refreshAll = useCallback(async () => {
    await load()
    setRefreshTick((n) => n + 1)
  }, [load])

  // ── Area header stats: workspace total + free disk from the hourly
  // snapshot; a failed fetch hides the numbers, never the page. ──
  useEffect(() => {
    const pid = project.trim()
    if (!pid) { setAreaStats(null); return }
    let alive = true
    storageOverview()
      .then((snap) => {
        if (!alive) return
        const row = snap.projects.find((p) => p.project === pid)
        setAreaStats({ wsBytes: row?.workspace_bytes, free: snap.disk?.free_bytes })
      })
      .catch(() => { if (alive) setAreaStats(null) })
    return () => { alive = false }
  }, [project, refreshTick])

  // ── Search: debounced (300 ms) flat query; only the latest request wins. ──
  useEffect(() => {
    const q = searchQ.trim()
    if (q.length < SEARCH_MIN_CHARS) { setSearch({ status: 'idle' }); return }
    const seq = ++searchSeq.current
    let alive = true
    setSearch({ status: 'loading' })
    storageSearch(project.trim(), q)
      .then((res) => { if (alive && seq === searchSeq.current) setSearch({ status: 'ready', res }) })
      .catch((f) => { if (alive && seq === searchSeq.current) setSearch({ status: 'error', error: message(f) }) })
    return () => { alive = false }
  }, [searchQ, project, refreshTick])

  const loadChildren = async (area: AreaName, dirPath: string) => {
    const k = key(area, dirPath)
    setLoadingDir(k)
    setError('')
    try {
      const page = await storageListDir(project.trim(), area, dirPath)
      setChildren((current) => ({ ...current, [k]: page.entries }))
    } catch (f) { setError(message(f)) }
    finally { setLoadingDir(null) }
  }

  const toggleDir = (area: AreaName, entry: StorageFileEntry) => {
    setSelected({ area, entry })
    setPreview(null)
    setPreviewError('')
    const k = key(area, entry.path)
    setExpanded((current) => {
      const next = new Set(current)
      if (next.has(k)) next.delete(k)
      else {
        next.add(k)
        if (!children[k]) void loadChildren(area, entry.path)
      }
      return next
    })
  }

  // loadHistory fetches the file's change attribution; a journal without the
  // endpoint (older deployment) leaves the card without the section instead
  // of failing the preview.
  const loadHistory = async (area: AreaName, path: string) => {
    const seq = ++historySeq.current
    setHistory({ status: 'loading', changes: [] })
    try {
      const res = await storageFileHistory(project.trim(), area, path, 10)
      if (seq !== historySeq.current) return
      setHistory({ status: 'ready', changes: res.changes ?? [] })
    } catch (f) {
      if (seq !== historySeq.current) return
      setHistory({ status: 'error', changes: [] })
      void f
    }
  }

  // openRunFiles switches the left pane to «files of this run» — the other
  // half of attribution: from one changed file to everything the run touched.
  const openRunFiles = async (run: string) => {
    const seq = ++runFilesSeq.current
    setRunFiles({ run, status: 'loading', files: [] })
    try {
      const res = await storageRunFiles(project.trim(), run, 100)
      if (seq !== runFilesSeq.current) return
      setRunFiles({ run, status: 'ready', files: res.files ?? [] })
    } catch (f) {
      if (seq !== runFilesSeq.current) return
      setRunFiles({ run, status: 'error', files: [], error: message(f) })
    }
  }

  const openFile = async (area: AreaName, entry: StorageFileEntry) => {
    setSelected({ area, entry })
    setPreview(null)
    setPreviewError('')
    if (entry.dir) return
    void loadHistory(area, entry.path)
    if (entry.bytes > 2 << 20) { setPreviewError(t('storage.files_too_big')); return }
    try {
      const response = await storageFileContent(project.trim(), area, entry.path)
      if (IMAGE_EXT.test(entry.name)) {
        const blob = await response.blob()
        setPreview({ kind: 'image', content: URL.createObjectURL(blob) })
        return
      }
      const text = await response.text()
      // Binary payload smuggled as text shows garbage — a NUL byte in the
      // first chunk is the cheap detector.
      if (text.slice(0, 2048).includes('\u0000')) {
        setPreviewError(t('storage.files_binary'))
        return
      }
      const kind = MARKDOWN_EXT.test(entry.name) ? 'markdown' : 'text'
      const trimmed = text.length > 256 << 10 ? text.slice(0, 256 << 10) : text
      // Syntax highlighting for known code extensions; everything else stays
      // escaped plain text.
      const language = kind === 'text' ? languageForFile(entry.name) : ''
      setPreview({ kind, content: trimmed, html: highlightCode(trimmed, language) })
    } catch (f) { setPreviewError(message(f)) }
  }

  const download = async (area: AreaName, entry: StorageFileEntry) => {
    try { await storageDownloadFile(project.trim(), area, entry.path) } catch (f) { setError(message(f)) }
  }

  const remove = async (area: AreaName, entry: StorageFileEntry) => {
    const ok = await confirmDialog({
      title: t('storage.files_delete_title', { name: entry.name }),
      body: t('storage.files_delete_body'),
      danger: true,
    })
    if (!ok) return
    setBusy(true); setError('')
    try {
      await storageDeleteFile(project.trim(), area, entry.path)
      const parent = parentOf(entry.path)
      if (parent === '') await load()
      else await loadChildren(area, parent)
      setSelected(null); setPreview(null)
      setRefreshTick((n) => n + 1)
    } catch (f) { setError(message(f)) }
    finally { setBusy(false) }
  }

  const startUpload = (area: AreaName, dirPath: string) => {
    uploadTarget.current = { area, dir: dirPath }
    uploadInput.current?.click()
  }

  const onUploadPicked = async (event: ChangeEvent<HTMLInputElement>) => {
    const files = event.target.files
    const target = uploadTarget.current
    event.target.value = ''
    if (!files || files.length === 0) return
    setBusy(true); setError('')
    try {
      for (const file of Array.from(files)) {
        const path = target.dir ? `${target.dir}/${file.name}` : file.name
        await storageUploadFile(project.trim(), target.area, path, file)
      }
      if (target.dir) await loadChildren(target.area, target.dir)
      else await load()
      setRefreshTick((n) => n + 1)
    } catch (f) { setError(message(f)) }
    finally { setBusy(false) }
  }

  // ── Navigation: expand every ancestor of a path so a top-path or a
  // breadcrumb click lands on it inside the lazy tree. ──
  const revealInTree = async (area: AreaName, path: string, isDir: boolean) => {
    const parts = path.split('/').filter(Boolean)
    const dirParts = isDir ? parts : parts.slice(0, -1)
    const missing: string[] = []
    let acc = ''
    for (const part of dirParts) {
      acc = acc ? `${acc}/${part}` : part
      if (!children[key(area, acc)]) missing.push(acc)
    }
    setError('')
    try {
      const fetched = await Promise.all(missing.map(async (p) => ({ p, page: await storageListDir(project.trim(), area, p) })))
      setChildren((prev) => {
        const next = { ...prev }
        for (const { p, page } of fetched) next[key(area, p)] = page.entries
        return next
      })
      setExpanded((prev) => {
        const next = new Set(prev)
        let a = ''
        for (const part of dirParts) { a = a ? `${a}/${part}` : part; next.add(key(area, a)) }
        return next
      })
    } catch (f) { setError(message(f)) }
  }

  /** Breadcrumb / top-path entry point: leave the search view, reveal the
   *  path in the tree. */
  const revealFrom = (area: AreaName, path: string, isDir: boolean) => {
    setQuery('')
    void revealInTree(area, path, isDir)
  }

  const refreshRoot = async (area: AreaName) => {
    setError('')
    try {
      const page = await storageListDir(project.trim(), area, '')
      setAreas((prev) => ({ ...prev, [area]: page.entries }))
    } catch (f) { setError(message(f)) }
  }

  // ── Bulk selection ──
  const batchItems = Object.values(batch)
  const batchBytes = batchItems.reduce((sum, item) => sum + (item.dir ? 0 : item.bytes), 0)

  const toggleBatch = (item: BatchItem) => {
    setBatchNote('')
    setBatch((prev) => {
      const next = { ...prev }
      const k = key(item.area, item.path)
      if (next[k]) delete next[k]
      else next[k] = item
      return next
    })
  }

  const enterSelect = () => { setSelectMode(true); setBatch({}); setBatchNote('') }
  const exitSelect = () => { setSelectMode(false); setBatch({}); setBatchNote(''); setDeleteProgress(null) }

  /** After a bulk delete, reload exactly the touched listings (roots and
   *  distinct parents) instead of resetting the whole tree. */
  const reloadAfterDelete = async (items: BatchItem[]) => {
    const deleted = new Set(items.map((item) => key(item.area, item.path)))
    const rootAreas = new Set<AreaName>()
    const parents: { area: AreaName; path: string }[] = []
    for (const item of items) {
      const parent = parentOf(item.path)
      if (parent === '') rootAreas.add(item.area)
      else if (!deleted.has(key(item.area, parent))) parents.push({ area: item.area, path: parent })
    }
    const fetched = await Promise.all(parents.map(async (p) => ({ ...p, entries: (await storageListDir(project.trim(), p.area, p.path)).entries })))
    setChildren((prev) => {
      const next = { ...prev }
      for (const f of fetched) next[key(f.area, f.path)] = f.entries
      return next
    })
    if (rootAreas.size > 0) {
      const roots = await Promise.all([...rootAreas].map(async (a) => ({ area: a, entries: (await storageListDir(project.trim(), a, '')).entries })))
      setAreas((prev) => {
        const next = { ...prev }
        for (const r of roots) next[r.area] = r.entries
        return next
      })
    }
  }

  const deleteSelected = async () => {
    const items = batchItems
    if (items.length === 0) return
    const deleted = new Set(items.map((item) => key(item.area, item.path)))
    const ok = await confirmDialog({
      title: t('storage.bulk_delete_title', { count: String(items.length) }),
      body: t('storage.bulk_delete_body', { count: String(items.length), size: fmtBytes(batchBytes) }),
      danger: true,
    })
    if (!ok) return
    setBusy(true); setError(''); setBatchNote('')
    setDeleteProgress({ done: 0, total: items.length })
    let removed = 0
    let freed = 0
    let firstError = ''
    try {
      for (const item of items) {
        try {
          await storageDeleteFile(project.trim(), item.area, item.path)
          removed++
          if (!item.dir) freed += item.bytes
        } catch (f) { if (!firstError) firstError = message(f) }
        setDeleteProgress((p) => (p ? { ...p, done: p.done + 1 } : p))
      }
      try { await reloadAfterDelete(items) } catch (f) { if (!firstError) firstError = message(f) }
      if (selected) {
        const selectedKey = key(selected.area, selected.entry.path)
        if (deleted.has(selectedKey)) { setSelected(null); setPreview(null); setPreviewError('') }
      }
      setBatch({})
      setBatchNote(t('storage.bulk_deleted', { count: String(removed), size: fmtBytes(freed) }))
      setRefreshTick((n) => n + 1)
      if (firstError) setError(firstError)
    } finally {
      setDeleteProgress(null)
      setBusy(false)
    }
  }

  const downloadSelected = async () => {
    const items = batchItems.filter((item) => !item.dir)
    if (items.length === 0) return
    setBusy(true); setError('')
    try {
      for (const item of items) {
        try { await storageDownloadFile(project.trim(), item.area, item.path) }
        catch (f) { setError(message(f)); break }
      }
    } finally { setBusy(false) }
  }

  const copyPath = async () => {
    if (!selected) return
    try {
      await navigator.clipboard.writeText(`${selected.area}/${selected.entry.path}`)
      setCopied(true)
      if (copiedTimer.current !== null) window.clearTimeout(copiedTimer.current)
      copiedTimer.current = window.setTimeout(() => setCopied(false), 1500)
    } catch (f) { setError(message(f)) }
  }

  useEffect(() => () => { if (copiedTimer.current !== null) window.clearTimeout(copiedTimer.current) }, [])

  // ── Sweep dialog (dry-run plan → confirmed run → result) ──
  const runSweep = async (dryRun: boolean) => {
    const st = sweep
    if (!st) return
    const areasPicked = (['cache', 'workspace'] as AreaName[]).filter((a) => st.areas[a])
    if (areasPicked.length === 0) { setSweep({ ...st, err: t('storage.sweep_need_area') }); return }
    const hours = AGE_HOURS[st.age]
    const body: StorageSweepBody = hours === undefined
      ? { dry_run: dryRun, areas: areasPicked }
      : { dry_run: dryRun, areas: areasPicked, older_than_hours: hours }
    setSweep({ ...st, busy: true, err: '' })
    try {
      const res = await storageSweep(project.trim(), body)
      setSweep((prev) => (prev ? {
        ...prev,
        busy: false,
        err: '',
        step: dryRun ? 'plan' : 'done',
        plan: dryRun ? res : prev.plan,
        result: dryRun ? prev.result : res,
      } : prev))
      if (!dryRun) void refreshAll()
    } catch (f) {
      setSweep((prev) => (prev ? { ...prev, busy: false, err: message(f) } : prev))
    }
  }

  // ── Policies dialog (GET on open, PUT on save) ──
  const openPolicies = async () => {
    setPolicies({ loading: true, form: null, saved: false, busy: false, err: '' })
    try {
      const p: StoragePolicies = await storageGetPolicies()
      setPolicies({
        loading: false,
        form: {
          enabled: p.enabled,
          cache_ttl_hours: String(p.cache_ttl_hours),
          workspace_stale_hours: String(p.workspace_stale_hours),
          quota_bytes: String(p.quota_bytes),
        },
        saved: false, busy: false, err: '',
      })
    } catch (f) {
      setPolicies({ loading: false, form: null, saved: false, busy: false, err: message(f) })
    }
  }

  const savePolicies = async () => {
    const st = policies
    if (!st?.form) return
    const cacheTtl = Number(st.form.cache_ttl_hours)
    const stale = Number(st.form.workspace_stale_hours)
    const quota = Number(st.form.quota_bytes)
    if (!Number.isInteger(cacheTtl) || cacheTtl < 0 || !Number.isInteger(stale) || stale < 0 || !Number.isInteger(quota) || quota < 0) {
      setPolicies({ ...st, saved: false, err: t('storage.policies_invalid') })
      return
    }
    const body: StoragePolicies = { enabled: st.form.enabled, cache_ttl_hours: cacheTtl, workspace_stale_hours: stale, quota_bytes: quota }
    setPolicies({ ...st, busy: true, saved: false, err: '' })
    try {
      await storagePutPolicies(body)
      setPolicies((prev) => (prev ? { ...prev, busy: false, saved: true } : prev))
    } catch (f) {
      setPolicies((prev) => (prev ? { ...prev, busy: false, err: message(f) } : prev))
    }
  }

  // ── Renderers ──
  const row = (area: AreaName, entry: StorageFileEntry, depth: number): ReactNode => {
    const k = key(area, entry.path)
    const open = expanded.has(k)
    const selectedHere = selected?.area === area && selected.entry.path === entry.path
    const batched = Boolean(batch[k])
    const item: BatchItem = { area, path: entry.path, name: entry.name, bytes: entry.bytes, dir: entry.dir }
    const activate = () => {
      if (selectMode) { toggleBatch(item); return }
      if (entry.dir) toggleDir(area, entry)
      else void openFile(area, entry)
    }
    return (
      <div key={k}>
        <div
          role="button" tabIndex={0}
          onClick={activate}
          onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); activate() } }}
          title={`${area}/${entry.path}${entry.mod_at ? ` · ${new Date(entry.mod_at).toLocaleString()}` : ''}`}
          style={{
            display: 'flex', alignItems: 'center', gap: '0.35rem', padding: '0.16rem 0.3rem', cursor: 'pointer', borderRadius: '4px',
            fontSize: '0.78rem', paddingLeft: `${0.3 + depth * 0.9}rem`,
            background: selectedHere || batched ? 'var(--tm-elevated)' : 'transparent',
          }}
        >
          {selectMode && (
            <input
              type="checkbox" checked={batched} aria-label={entry.name}
              onClick={(e) => e.stopPropagation()}
              onChange={() => toggleBatch(item)}
              style={{ flexShrink: 0, cursor: 'pointer' }}
            />
          )}
          <span
            className="chevron-hit"
            onClick={(e) => { if (entry.dir) { e.stopPropagation(); toggleDir(area, entry) } }}
          >
            {entry.dir && (loadingDir === k ? <span style={{ fontSize: '0.7rem' }}>…</span> : open ? <ChevronDown size={14} /> : <ChevronRight size={14} />)}
          </span>
          <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', flex: 1 }}>{entry.name}</span>
          <span style={{ color: 'var(--tm-text-3)', fontSize: '0.66rem', whiteSpace: 'nowrap', flexShrink: 0 }}>
            {entry.dir ? (entry.bytes > 0 ? fmtBytes(entry.bytes) : `${entry.entries ?? 0}`) : fmtBytes(entry.bytes)}
          </span>
        </div>
        {entry.dir && open && sortEntries(children[k] ?? [], treeSort).map((child) => row(area, child, depth + 1))}
      </div>
    )
  }

  const areaSection = (area: AreaName) => {
    const entries = areas[area]
    const label = areaLabel(area)
    return (
      <div style={{ marginBottom: '1rem' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: '0.4rem', marginBottom: '0.25rem' }}>
          <span style={{ fontSize: '0.7rem', textTransform: 'uppercase', letterSpacing: '0.05em', color: 'var(--tm-text-3)' }}>{label}</span>
          {area === 'workspace' && (areaStats?.wsBytes !== undefined || areaStats?.free !== undefined) && (
            <span style={{ marginLeft: 'auto', fontSize: '0.66rem', color: 'var(--tm-text-3)', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>
              {t('storage.area_workspace_stats', {
                size: areaStats?.wsBytes !== undefined ? fmtBytes(areaStats.wsBytes) : '—',
                free: areaStats?.free !== undefined ? fmtBytes(areaStats.free) : '—',
              })}
            </span>
          )}
          <button
            title={t('storage.files_upload')}
            onClick={() => startUpload(area, '')}
            style={{ background: 'none', border: 'none', color: 'var(--tm-text-3)', cursor: 'pointer', fontSize: '0.8rem', padding: '0 0.25rem', lineHeight: 1 }}
          >＋</button>
        </div>
        {entries === null ? (
          <div style={{ fontSize: '0.72rem', color: 'var(--tm-muted)', padding: '0.2rem 0' }}>…</div>
        ) : entries.length === 0 ? (
          <div style={{ fontSize: '0.72rem', color: 'var(--tm-muted)', padding: '0.2rem 0' }}>{t('storage.files_empty')}</div>
        ) : (
          sortEntries(entries, treeSort).map((entry) => row(area, entry, 0))
        )}
      </div>
    )
  }

  const searchRow = (hit: StorageSearchHit): ReactNode => {
    const k = key(hit.area, hit.path)
    const batched = Boolean(batch[k])
    const selectedHere = selected?.area === hit.area && selected.entry.path === hit.path
    const item: BatchItem = { area: hit.area, path: hit.path, name: hit.path.split('/').pop() ?? hit.path, bytes: hit.bytes, dir: hit.dir }
    const activate = () => {
      if (selectMode) { toggleBatch(item); return }
      void openFile(hit.area, entryFromHit(hit))
    }
    return (
      <div
        key={k}
        role="button" tabIndex={0}
        onClick={activate}
        onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); activate() } }}
        title={`${hit.area}/${hit.path}`}
        style={{
          display: 'flex', alignItems: 'center', gap: '0.4rem', padding: '0.18rem 0.3rem', cursor: 'pointer', borderRadius: '4px',
          fontSize: '0.78rem', background: selectedHere || batched ? 'var(--tm-elevated)' : 'transparent',
        }}
      >
        {selectMode && (
          <input
            type="checkbox" checked={batched} aria-label={hit.path}
            onClick={(e) => e.stopPropagation()}
            onChange={() => toggleBatch(item)}
            style={{ flexShrink: 0, cursor: 'pointer' }}
          />
        )}
        <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', flex: 1 }}>
          {highlightMatch(hit.path, searchQ.trim())}
        </span>
        <Tag type={hit.area === 'workspace' ? 'blue' : 'gray'} size="sm">{areaLabel(hit.area)}</Tag>
        <span style={{ color: 'var(--tm-text-3)', fontSize: '0.66rem', whiteSpace: 'nowrap', flexShrink: 0 }}>
          {hit.dir ? t('storage.files_dir') : fmtBytes(hit.bytes)}
        </span>
        <span style={{ color: 'var(--tm-text-3)', fontSize: '0.66rem', whiteSpace: 'nowrap', flexShrink: 0 }}>
          {hit.mod_at ? new Date(hit.mod_at).toLocaleDateString() : ''}
        </span>
      </div>
    )
  }

  const searchPane = (): ReactNode => {
    if (search.status === 'error') {
      return <div style={{ fontSize: '0.75rem', color: 'var(--tm-danger)' }}>{t('storage.search_failed', { error: search.error ?? '' })}</div>
    }
    if (search.status === 'ready' && search.res) {
      const { results, truncated, elapsed_ms } = search.res
      return (
        <>
          <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', flexWrap: 'wrap', marginBottom: '0.4rem' }}>
            <span style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)' }}>
              {t('storage.search_count', { n: results.length.toLocaleString() })}
              {' · '}
              {t('storage.search_elapsed', { ms: String(elapsed_ms) })}
              {truncated ? ` · ${t('storage.search_truncated')}` : ''}
            </span>
            <span style={{ marginLeft: 'auto' }}>
              <Button size="sm" kind="ghost" onClick={() => setQuery('')}>{t('storage.search_back')}</Button>
            </span>
          </div>
          {results.length === 0 ? (
            <div style={{ fontSize: '0.75rem', color: 'var(--tm-muted)', padding: '0.2rem 0' }}>{t('storage.search_none')}</div>
          ) : (
            results.map((hit) => searchRow(hit))
          )}
        </>
      )
    }
    // idle (debounce still catching up) and loading share the "…" state.
    return <div style={{ fontSize: '0.75rem', color: 'var(--tm-muted)', padding: '0.2rem 0' }}>…</div>
  }

  // runFilesPane is the attribution counterpart of the search pane: every
  // file one run touched, click-to-preview like the search rows.
  const runFilesPane = (): ReactNode => {
    if (!runFiles) return null
    const header = (
      <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', marginBottom: '0.4rem' }}>
        <span style={{ fontSize: '0.72rem', color: 'var(--tm-text-2)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
          {t('storage.run_files_title', { id: runFiles.run })}
        </span>
        <span style={{ marginLeft: 'auto' }}>
          <Button size="sm" kind="ghost" onClick={() => setRunFiles(null)}>{t('storage.search_back')}</Button>
        </span>
      </div>
    )
    if (runFiles.status === 'loading') {
      return <>{header}<div style={{ fontSize: '0.75rem', color: 'var(--tm-muted)', padding: '0.2rem 0' }}>…</div></>
    }
    if (runFiles.status === 'error') {
      return <>{header}<div style={{ fontSize: '0.75rem', color: 'var(--tm-danger)' }}>{t('storage.run_files_failed', { error: runFiles.error ?? '' })}</div></>
    }
    if (runFiles.files.length === 0) {
      return <>{header}<div style={{ fontSize: '0.75rem', color: 'var(--tm-muted)', padding: '0.2rem 0' }}>{t('storage.run_files_empty')}</div></>
    }
    return (
      <>
        {header}
        {runFiles.files.map((file) => {
          const k = key(file.area as AreaName, file.path)
          const batched = Boolean(batch[k])
          const item: BatchItem = { area: file.area as AreaName, path: file.path, name: file.path.split('/').pop() ?? file.path, bytes: 0, dir: false }
          const activate = () => {
            if (selectMode) { toggleBatch(item); return }
            void openFile(item.area, { name: item.name, path: file.path, dir: false, bytes: 0, mod_at: file.last_at })
          }
          return (
            <div
              key={k}
              role="button" tabIndex={0}
              onClick={activate}
              onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); activate() } }}
              title={`${file.area}/${file.path}`}
              style={{
                display: 'flex', alignItems: 'center', gap: '0.4rem', padding: '0.18rem 0.3rem', cursor: 'pointer', borderRadius: '4px',
                fontSize: '0.78rem', background: batched ? 'var(--tm-elevated)' : 'transparent',
              }}
            >
              {selectMode && (
                <input
                  type="checkbox" checked={batched} aria-label={file.path}
                  onClick={(e) => e.stopPropagation()}
                  onChange={() => toggleBatch(item)}
                  style={{ flexShrink: 0, cursor: 'pointer' }}
                />
              )}
              <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', flex: 1 }}>{file.path}</span>
              <Tag type={file.area === 'workspace' ? 'blue' : 'gray'} size="sm">
                {file.area === 'workspace' || file.area === 'cache' ? areaLabel(file.area as AreaName) : file.area}
              </Tag>
              <span style={{ color: 'var(--tm-text-3)', fontSize: '0.66rem', whiteSpace: 'nowrap', flexShrink: 0 }}>
                {t('storage.run_files_count', { n: String(file.changes) })}
              </span>
              <span style={{ color: 'var(--tm-text-3)', fontSize: '0.66rem', whiteSpace: 'nowrap', flexShrink: 0 }}>
                {file.last_at ? new Date(file.last_at).toLocaleDateString() : ''}
              </span>
            </div>
          )
        })}
      </>
    )
  }

  const breadcrumb = selected && (() => {
    const parts = selected.entry.path.split('/').filter(Boolean)
    const isDir = selected.entry.dir
    const dirs = isDir ? parts : parts.slice(0, -1)
    const current = parts[parts.length - 1] ?? ''
    return (
      <nav
        aria-label={t('storage.title')}
        style={{ display: 'flex', alignItems: 'center', gap: '0.25rem', flexWrap: 'wrap', fontSize: '0.72rem', color: 'var(--tm-text-3)', marginBottom: '0.5rem', overflowWrap: 'anywhere' }}
      >
        <button
          onClick={() => { setQuery(''); void refreshRoot(selected.area) }}
          style={{ background: 'none', border: 'none', padding: 0, font: 'inherit', color: 'var(--tm-text-2)', cursor: 'pointer' }}
        >{areaLabel(selected.area)}</button>
        {dirs.map((seg, i) => {
          const prefix = dirs.slice(0, i + 1).join('/')
          const isCurrent = i === dirs.length - 1 && isDir
          return (
            <Fragment key={prefix}>
              <span> / </span>
              <button
                onClick={() => revealFrom(selected.area, prefix, true)}
                style={{
                  background: 'none', border: 'none', padding: 0, font: 'inherit', cursor: 'pointer',
                  color: isCurrent ? 'var(--tm-text-1)' : 'var(--tm-text-2)',
                  fontWeight: isCurrent ? 600 : 400,
                }}
              >{seg}</button>
            </Fragment>
          )
        })}
        {!isDir && (
          <>
            <span> / </span>
            <span style={{ color: 'var(--tm-text-1)', fontWeight: 600 }}>{current}</span>
          </>
        )}
      </nav>
    )
  })()

  const selectBar = selectMode && (
    <div style={{ display: 'flex', alignItems: 'center', gap: '0.6rem', flexWrap: 'wrap', marginBottom: '0.5rem', padding: '0.4rem 0.6rem', border: '1px solid var(--tm-border)', borderRadius: '6px', background: 'var(--tm-elevated)', flexShrink: 0 }}>
      <span style={{ fontSize: '0.72rem', color: 'var(--tm-text-3)' }}>{t('storage.select_hint')}</span>
      <span style={{ fontSize: '0.72rem', color: 'var(--tm-text-1)' }}>
        {t('storage.select_count', { n: String(batchItems.length) })} · {fmtBytes(batchBytes)}
      </span>
      {deleteProgress && (
        <span style={{ fontSize: '0.72rem', color: 'var(--tm-warning)' }}>
          {t('storage.bulk_progress', { done: String(deleteProgress.done), total: String(deleteProgress.total) })}
        </span>
      )}
      {batchNote && <span style={{ fontSize: '0.72rem', color: 'var(--tm-success)' }}>{batchNote}</span>}
      <span style={{ marginLeft: 'auto', display: 'flex', gap: '0.4rem', flexShrink: 0 }}>
        <Button
          size="sm" kind="danger--ghost" renderIcon={TrashCan}
          disabled={busy || deleteProgress !== null || batchItems.length === 0}
          onClick={() => void deleteSelected()}
        >{t('storage.bulk_delete')}</Button>
        <Button
          size="sm" kind="ghost" renderIcon={Download}
          disabled={busy || batchItems.length === 0}
          onClick={() => void downloadSelected()}
        >{t('storage.bulk_download')}</Button>
        <Button size="sm" kind="secondary" disabled={busy} onClick={exitSelect}>{t('storage.select_exit')}</Button>
      </span>
    </div>
  )

  const sweepDialog = sweep && (
    <AppModal onClose={() => setSweep(null)} panelStyle={{ width: '540px' }}>
      <h2 style={dialogTitleStyle}>{t('storage.sweep_title')}</h2>
      {sweep.step === 'form' && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: '0.7rem' }}>
          <div>
            <div style={fieldLabelStyle}>{t('storage.sweep_areas')}</div>
            <div style={{ display: 'flex', gap: '1.25rem' }}>
              <Checkbox
                id="storage-sweep-cache" labelText={t('storage.area_cache')}
                checked={sweep.areas.cache}
                onChange={(_, { checked }) => setSweep({ ...sweep, areas: { ...sweep.areas, cache: checked }, err: '' })}
              />
              <Checkbox
                id="storage-sweep-ws" labelText={t('storage.area_workspace')}
                checked={sweep.areas.workspace}
                onChange={(_, { checked }) => setSweep({ ...sweep, areas: { ...sweep.areas, workspace: checked }, err: '' })}
              />
            </div>
          </div>
          <div>
            <div style={fieldLabelStyle}>{t('storage.sweep_older')}</div>
            <div style={{ display: 'flex', gap: '0.4rem' }}>
              {(['7d', '30d', 'any'] as SweepAge[]).map((age) => (
                <Button
                  key={age} size="sm" kind={sweep.age === age ? 'primary' : 'tertiary'}
                  onClick={() => setSweep({ ...sweep, age, err: '' })}
                >
                  {age === '7d' ? t('storage.sweep_age_7d') : age === '30d' ? t('storage.sweep_age_30d') : t('storage.sweep_age_any')}
                </Button>
              ))}
            </div>
          </div>
          {sweep.err && <div style={{ fontSize: '0.75rem', color: 'var(--tm-danger)' }}>{sweep.err}</div>}
          <div className="form-actions" style={{ marginTop: 0 }}>
            <Button size="sm" kind="secondary" onClick={() => setSweep(null)}>{t('action.cancel')}</Button>
            <Button size="sm" kind="primary" disabled={sweep.busy} onClick={() => void runSweep(true)}>
              {sweep.busy ? '…' : t('storage.sweep_plan_btn')}
            </Button>
          </div>
        </div>
      )}
      {sweep.step === 'plan' && sweep.plan && (() => {
        const plan = sweep.plan!
        const planPaths = plan.plan ?? []
        const shown = Math.min(planPaths.length, SWEEP_PLAN_SHOWN)
        const total = Math.max(plan.removed, planPaths.length)
        return (
          <div style={{ display: 'flex', flexDirection: 'column', gap: '0.6rem' }}>
            <div style={fieldLabelStyle}>{t('storage.sweep_plan_title')}</div>
            {plan.removed === 0 ? (
              <div style={{ fontSize: '0.78rem', color: 'var(--tm-muted)' }}>{t('storage.sweep_nothing')}</div>
            ) : (
              <>
                <div style={{ fontSize: '0.8rem' }}>{t('storage.sweep_objects', { n: plan.removed.toLocaleString() })}</div>
                <div style={{ fontSize: '0.8rem' }}>{t('storage.sweep_freed', { size: fmtBytes(plan.freed_bytes) })}</div>
                <div style={{ maxHeight: '200px', overflowY: 'auto', border: '1px solid var(--tm-border)', borderRadius: '4px', padding: '0.4rem 0.5rem', fontSize: '0.72rem', background: 'var(--tm-surface)' }}>
                  {planPaths.slice(0, shown).map((p) => (
                    <div key={p.path} style={{ display: 'flex', justifyContent: 'space-between', gap: '0.6rem' }}>
                      <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{p.path}</span>
                      <span style={{ color: 'var(--tm-text-3)', flexShrink: 0 }}>{fmtBytes(p.bytes)}</span>
                    </div>
                  ))}
                </div>
                {total > shown && (
                  <div style={{ fontSize: '0.72rem', color: 'var(--tm-text-3)' }}>{t('storage.sweep_more', { n: (total - shown).toLocaleString() })}</div>
                )}
              </>
            )}
            {sweep.err && <div style={{ fontSize: '0.75rem', color: 'var(--tm-danger)' }}>{sweep.err}</div>}
            <div className="form-actions" style={{ marginTop: 0 }}>
              <Button size="sm" kind="secondary" disabled={sweep.busy} onClick={() => setSweep({ ...sweep, step: 'form', plan: null, err: '' })}>
                {t('storage.sweep_back')}
              </Button>
              <Button size="sm" kind="danger" disabled={sweep.busy || plan.removed === 0} onClick={() => void runSweep(false)}>
                {sweep.busy ? '…' : t('storage.sweep_apply')}
              </Button>
              <Button size="sm" kind="secondary" disabled={sweep.busy} onClick={() => setSweep(null)}>{t('action.close')}</Button>
            </div>
          </div>
        )
      })()}
      {sweep.step === 'done' && sweep.result && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: '0.5rem' }}>
          <div style={{ fontSize: '0.8rem', color: 'var(--tm-success)' }}>{t('storage.sweep_done_title')}</div>
          <div style={{ fontSize: '0.8rem' }}>{t('storage.sweep_removed', { n: sweep.result.removed.toLocaleString() })}</div>
          <div style={{ fontSize: '0.8rem' }}>{t('storage.sweep_freed_actual', { size: fmtBytes(sweep.result.freed_bytes) })}</div>
          <div className="form-actions" style={{ marginTop: 0 }}>
            <Button size="sm" kind="primary" onClick={() => setSweep(null)}>{t('action.close')}</Button>
          </div>
        </div>
      )}
    </AppModal>
  )

  const policiesDialog = policies && (
    <AppModal onClose={() => setPolicies(null)} panelStyle={{ width: '460px' }}>
      <h2 style={dialogTitleStyle}>{t('storage.policies_title')}</h2>
      {policies.loading ? (
        <div style={{ fontSize: '0.78rem', color: 'var(--tm-muted)' }}>…</div>
      ) : !policies.form ? (
        <div style={{ display: 'flex', flexDirection: 'column', gap: '0.6rem' }}>
          <div style={{ fontSize: '0.78rem', color: 'var(--tm-danger)' }}>{policies.err || t('storage.overview_unavailable')}</div>
          <div className="form-actions" style={{ marginTop: 0 }}>
            <Button size="sm" kind="secondary" onClick={() => setPolicies(null)}>{t('action.close')}</Button>
            <Button size="sm" kind="primary" onClick={() => void openPolicies()}>{t('action.retry')}</Button>
          </div>
        </div>
      ) : (() => {
        const form = policies.form!
        const quotaPreview = Number(form.quota_bytes)
        return (
          <div style={{ display: 'flex', flexDirection: 'column', gap: '0.7rem' }}>
            <Checkbox
              id="storage-policies-enabled" labelText={t('storage.policies_enabled')}
              checked={form.enabled}
              onChange={(_, { checked }) => setPolicies({ ...policies, form: { ...form, enabled: checked }, saved: false, err: '' })}
            />
            <label style={{ display: 'flex', flexDirection: 'column', gap: '0.2rem', fontSize: '0.75rem', color: 'var(--tm-text-2)' }}>
              {t('storage.policies_cache_ttl')}
              <input
                type="number" min={0} step={1} value={form.cache_ttl_hours} style={inputStyle}
                onChange={(e) => setPolicies({ ...policies, form: { ...form, cache_ttl_hours: e.target.value }, saved: false, err: '' })}
              />
            </label>
            <label style={{ display: 'flex', flexDirection: 'column', gap: '0.2rem', fontSize: '0.75rem', color: 'var(--tm-text-2)' }}>
              {t('storage.policies_stale')}
              <input
                type="number" min={0} step={1} value={form.workspace_stale_hours} style={inputStyle}
                onChange={(e) => setPolicies({ ...policies, form: { ...form, workspace_stale_hours: e.target.value }, saved: false, err: '' })}
              />
            </label>
            <label style={{ display: 'flex', flexDirection: 'column', gap: '0.2rem', fontSize: '0.75rem', color: 'var(--tm-text-2)' }}>
              {t('storage.policies_quota')}
              <input
                type="number" min={0} step={1} value={form.quota_bytes} style={inputStyle}
                onChange={(e) => setPolicies({ ...policies, form: { ...form, quota_bytes: e.target.value }, saved: false, err: '' })}
              />
              <span style={{ fontSize: '0.68rem', color: 'var(--tm-text-3)' }}>
                {Number.isFinite(quotaPreview) && quotaPreview >= 0 ? fmtBytes(quotaPreview) : '—'}
              </span>
            </label>
            {policies.err && <div style={{ fontSize: '0.75rem', color: 'var(--tm-danger)' }}>{policies.err}</div>}
            {policies.saved && <div style={{ fontSize: '0.75rem', color: 'var(--tm-success)' }}>{t('storage.policies_saved')}</div>}
            <div className="form-actions" style={{ marginTop: 0 }}>
              <Button size="sm" kind="secondary" onClick={() => setPolicies(null)}>{t('action.cancel')}</Button>
              <Button size="sm" kind="primary" disabled={policies.busy} onClick={() => void savePolicies()}>
                {policies.busy ? t('action.saving') : t('action.save')}
              </Button>
            </div>
          </div>
        )
      })()}
    </AppModal>
  )

  if (!project.trim()) return <ProjectRequired what={t('storage.title')} />

  return (
    <div style={{ padding: '1rem', height: 'calc(100vh - 3rem)', display: 'flex', flexDirection: 'column', boxSizing: 'border-box' }}>
      {confirmDialogNode}
      {sweepDialog}
      {policiesDialog}
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '0.75rem', flexShrink: 0, gap: '0.5rem', flexWrap: 'wrap' }}>
        <div>
          <Heading style={{ fontSize: '1.1rem' }}>{t('storage.title')}</Heading>
          <p style={{ color: 'var(--tm-text-3)', fontSize: '0.8rem', marginTop: '0.25rem' }}>{t('storage.subtitle')}</p>
        </div>
        <div style={{ display: 'flex', gap: '0.3rem', flexShrink: 0, flexWrap: 'wrap' }}>
          <Button size="sm" kind="ghost" renderIcon={Renew} disabled={loadingDir !== null || busy} onClick={() => void refreshAll()}>
            {t('common.refresh')}
          </Button>
          <Button size="sm" kind="ghost" renderIcon={TrashCan} disabled={busy} onClick={() => setSweep(initialSweep())}>
            {t('storage.sweep_open')}
          </Button>
          <Button
            size="sm" kind="ghost" hasIconOnly renderIcon={Settings}
            iconDescription={t('storage.policies_open')} disabled={busy}
            onClick={() => void openPolicies()}
          />
        </div>
      </div>
      <input ref={uploadInput} type="file" multiple hidden onChange={(e) => void onUploadPicked(e)} />
      {error && <InlineNotification kind="error" lowContrast title={t('action.error')} subtitle={error} onCloseButtonClick={() => setError('')} style={{ marginBottom: '0.75rem' }} />}
      {breadcrumb}
      {selectBar}
      <div style={{ display: 'grid', gridTemplateColumns: 'minmax(280px, 380px) 1fr', gap: '1rem', flex: 1, minHeight: 0 }}>
        {/* Left: search input + «select» toggle, then results or the lazy tree */}
        <div style={{ border: '1px solid var(--tm-border)', borderRadius: '6px', padding: '0.6rem 0.75rem', overflowY: 'auto', minHeight: 0 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: '0.4rem', marginBottom: '0.4rem' }}>
            <div style={{ minWidth: 0, flex: 1 }}>
              <input
                className="toolbar-search"
                type="search"
                style={{ width: '100%' }}
                placeholder={t('storage.search_placeholder')}
                aria-label={t('storage.search_placeholder')}
                value={query}
                onChange={(e: ChangeEvent<HTMLInputElement>) => setQuery(e.target.value)}
              />
            </div>
            <Button
              size="sm" kind={selectMode ? 'secondary' : 'ghost'} hasIconOnly
              renderIcon={CheckboxIcon}
              iconDescription={selectMode ? t('storage.select_exit') : t('storage.select_mode')}
              tooltipPosition="bottom"
              style={{ flexShrink: 0 }}
              onClick={() => (selectMode ? exitSelect() : enterSelect())}
            />
          </div>
          {runFiles ? runFilesPane() : searchMode ? searchPane() : (
            <>
              <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'flex-end', marginBottom: '0.3rem' }}>
                <label style={{ display: 'flex', alignItems: 'center', gap: '0.35rem', fontSize: '0.7rem', color: 'var(--tm-text-3)' }}>
                  {t('storage.sort_label')}
                  <select
                    value={treeSort}
                    onChange={(e) => setTreeSort(e.target.value as TreeSort)}
                    style={{ ...inputStyle, width: 'auto', padding: '0.15rem 0.3rem', fontSize: '0.72rem' }}
                  >
                    <option value="name">{t('storage.sort_name')}</option>
                    <option value="size">{t('storage.sort_size')}</option>
                    <option value="date">{t('storage.sort_date')}</option>
                  </select>
                </label>
              </div>
              {areaSection('workspace')}
              {areaSection('cache')}
            </>
          )}
        </div>
        {/* Right: details / preview */}
        <div style={{ border: '1px solid var(--tm-border)', borderRadius: '6px', padding: '0.6rem 0.75rem', overflowY: 'auto', minHeight: 0 }}>
          {!selected ? (
            <div style={{ color: 'var(--tm-muted)', fontSize: '0.78rem', padding: '1rem 0' }}>{t('storage.files_pick')}</div>
          ) : (
            <>
              <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', flexWrap: 'wrap', marginBottom: '0.5rem' }}>
                <strong style={{ fontSize: '0.85rem', overflowWrap: 'anywhere' }}>{selected.entry.name}</strong>
                {selected.entry.dir
                  ? <Tag type="blue" size="sm">{t('storage.files_dir')}</Tag>
                  : <Tag type="gray" size="sm">{fmtBytes(selected.entry.bytes)}</Tag>}
                {selected.entry.mod_at && <span style={{ color: 'var(--tm-text-3)', fontSize: '0.7rem' }}>{new Date(selected.entry.mod_at).toLocaleString()}</span>}
                <span style={{ marginLeft: 'auto', display: 'flex', gap: '0.4rem', flexShrink: 0 }}>
                  <Button size="sm" kind="ghost" hasIconOnly renderIcon={Upload} iconDescription={t('storage.files_upload')} tooltipPosition="bottom" onClick={() => startUpload(selected.area, selected.entry.dir ? selected.entry.path : parentOf(selected.entry.path))} />
                  {!selected.entry.dir && (
                    <Button size="sm" kind="ghost" hasIconOnly renderIcon={Download} iconDescription={t('storage.files_download')} tooltipPosition="bottom" onClick={() => void download(selected.area, selected.entry)} />
                  )}
                  <Button size="sm" kind="ghost" hasIconOnly renderIcon={Copy} iconDescription={copied ? t('storage.copied') : t('storage.copy_path')} tooltipPosition="bottom" disabled={busy} onClick={() => void copyPath()} />
                  <Button size="sm" kind="danger--ghost" hasIconOnly renderIcon={TrashCan} iconDescription={t('action.delete')} tooltipPosition="bottom" disabled={busy} onClick={() => void remove(selected.area, selected.entry)} />
                </span>
              </div>
              {!selected.entry.dir && history.status === 'ready' && history.changes.length > 0 && (
                <div style={{ marginBottom: '0.6rem' }}>
                  {history.changes.map((change, i) => (
                    <div
                      key={`${change.occurred_at}-${i}`}
                      style={{
                        display: 'flex', alignItems: 'center', gap: '0.5rem', fontSize: '0.72rem', padding: '0.15rem 0', minWidth: 0,
                        borderBottom: i < history.changes.length - 1 ? '1px solid var(--tm-border)' : 'none',
                      }}
                    >
                      <span style={{ color: 'var(--tm-text-3)', whiteSpace: 'nowrap' }}>{new Date(change.occurred_at).toLocaleString()}</span>
                      <Tag type={opTagType(change.op)} size="sm">
                        {change.op === 'write' || change.op === 'edit' || change.op === 'upload' || change.op === 'delete'
                          ? t(`storage.op_${change.op}`) : change.op}
                      </Tag>
                      {change.bytes > 0 && <span style={{ color: 'var(--tm-text-3)', whiteSpace: 'nowrap' }}>{fmtBytes(change.bytes)}</span>}
                      {change.run && (
                        <button
                          type="button"
                          onClick={() => void openRunFiles(change.run)}
                          title={t('storage.history_run_hint', { run: change.run })}
                          style={{
                            marginLeft: 'auto', background: 'none', border: 'none', padding: 0, font: 'inherit',
                            color: 'var(--tm-chart-3)', cursor: 'pointer', fontSize: '0.72rem',
                            overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', maxWidth: '9rem', flexShrink: 1,
                          }}
                        >{t('storage.history_run', { id: change.run.slice(-8) })}</button>
                      )}
                    </div>
                  ))}
                </div>
              )}
              {selected.entry.dir ? (
                <div style={{ color: 'var(--tm-muted)', fontSize: '0.78rem' }}>
                  {children[key(selected.area, selected.entry.path)]?.length ?? selected.entry.entries ?? 0} {t('storage.files_items')} — {t('storage.files_expand')}
                </div>
              ) : previewError ? (
                <div style={{ color: 'var(--tm-muted)', fontSize: '0.78rem' }}>{previewError}</div>
              ) : !preview ? (
                <div style={{ color: 'var(--tm-muted)', fontSize: '0.78rem' }}>…</div>
              ) : preview.kind === 'image' ? (
                <img src={preview.content} alt={selected.entry.name} style={{ maxWidth: '100%', borderRadius: '4px', border: '1px solid var(--tm-border)' }} />
              ) : preview.kind === 'markdown' ? (
                <div style={{ border: '1px solid var(--tm-border)', borderRadius: '4px', padding: '0.75rem', background: 'var(--tm-surface)' }}>
                  <Markdown content={preview.content} />
                </div>
              ) : (
                <pre
                  className="code-preview hljs"
                  style={{ margin: 0, padding: '0.5rem', background: 'var(--tm-surface)', border: '1px solid var(--tm-border)', borderRadius: '4px', fontSize: '0.72rem', lineHeight: 1.45, whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}
                  dangerouslySetInnerHTML={{ __html: preview.html ?? '' }}
                />
              )}
            </>
          )}
        </div>
      </div>
    </div>
  )
}

function parentOf(path: string): string {
  return path.includes('/') ? path.slice(0, path.lastIndexOf('/')) : ''
}
