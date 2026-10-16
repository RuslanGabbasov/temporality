import { useEffect, useState, useCallback, useRef, type ChangeEvent, type ReactNode } from 'react'
import {
  Button,
  TextInput,
  TextArea,
  InlineNotification,
  Tag,
  Tile,
  Stack,
  Heading,
} from '@carbon/react'
import PageLoading from './PageLoading'
import { Add, TrashCan, Renew, Edit, Download, Upload } from '@carbon/icons-react'
import { workspaceApi, type SandboxProfile } from './workspaceApi'
import {
  storageListDir,
  storageFileContent,
  storageDownloadFile,
  storageDeleteFile,
  storageUploadFile,
  type StorageFileEntry,
} from './kernelApi'
import { fmtBytes } from './format'
import { useT } from './i18n'
import ListFilter, { matchesFilter } from './ListFilter'
import AppModal from './Modal'
import { useConfirm } from './ConfirmDialog'

function message(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }

type ProfileForm = {
  id: string
  name: string
  description: string
  image: string
  toolchains: string
  memory: string
  cpus: string
  scratch_size: string
  tmp_size: string
  cache_env: string
}

const emptyForm: ProfileForm = { id: '', name: '', description: '', image: '', toolchains: '', memory: '', cpus: '', scratch_size: '', tmp_size: '', cache_env: '' }

// Sandbox profile registry (docs/sandbox-profiles.md §8): the catalog of
// execution environments, builtin recipes with a Build action and custom
// profiles pointing at external digest-pinned images.
export default function Sandbox({ project }: { project?: string }) {
  const t = useT()
  const { confirmDialog, dialog: confirmDialogNode } = useConfirm()
  const [profiles, setProfiles] = useState<SandboxProfile[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [showForm, setShowForm] = useState(false)
  const [form, setForm] = useState<ProfileForm>(emptyForm)
  const [filter, setFilter] = useState('')
  const [building, setBuilding] = useState('')
  const [editing, setEditing] = useState<SandboxProfile | null>(null)

  const visible = profiles.filter((p) => matchesFilter(filter, p.name, p.id, p.description, ...(p.toolchains ?? [])))

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const data = await workspaceApi.listSandboxProfiles()
      setProfiles(data.profiles ?? [])
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }, [])

  useEffect(() => { void load() }, [load])

  const build = async (p: SandboxProfile) => {
    setBuilding(p.id); setError(''); setNotice('')
    try {
      await workspaceApi.buildSandboxProfile(p.id)
      setNotice(t('sandbox.built_notice', { name: p.name }))
      await load()
    } catch (f) { setError(message(f)) }
    finally { setBuilding('') }
  }

  const remove = async (p: SandboxProfile) => {
    const ok = await confirmDialog({
      title: t('sandbox.delete_confirm', { name: p.name }),
      body: t('action.delete_warning'),
      danger: true,
      confirmLabel: t('action.delete'),
    })
    if (!ok) return
    setError('')
    try {
      await workspaceApi.deleteSandboxProfile(p.id)
      await load()
    } catch (f) { setError(message(f)) }
  }

  const startEdit = (p: SandboxProfile) => {
    setEditing(p)
    setForm({
      id: p.id,
      name: p.name,
      description: p.description ?? '',
      image: p.image ?? '',
      toolchains: (p.toolchains ?? []).join(', '),
      memory: p.memory ?? '',
      cpus: p.cpus ?? '',
      scratch_size: p.scratch_size ?? '',
      tmp_size: p.tmp_size ?? '',
      cache_env: (p.cache_env ?? []).join('\n'),
    })
    setShowForm(true)
  }

  const saveCustom = async () => {
    setError('')
    const payload = {
      id: editing ? editing.id : form.id.trim(),
      name: form.name.trim(),
      description: form.description,
      image: form.image.trim(),
      toolchains: form.toolchains.split(',').map((s) => s.trim()).filter(Boolean),
      memory: form.memory.trim(),
      cpus: form.cpus.trim(),
      scratch_size: form.scratch_size.trim(),
      tmp_size: form.tmp_size.trim(),
      cache_env: form.cache_env.split('\n').map((s) => s.trim()).filter(Boolean),
    }
    try {
      if (editing) {
        await workspaceApi.updateSandboxProfile(editing.id, payload)
        setEditing(null)
      } else {
        await workspaceApi.createSandboxProfile(payload)
      }
      setShowForm(false)
      setForm(emptyForm)
      await load()
    } catch (f) { setError(message(f)) }
  }

  return (
    <div style={{ padding: '1rem' }}>
      {confirmDialogNode}
      {error && <InlineNotification kind="error" lowContrast title={t('action.error')} subtitle={error} onCloseButtonClick={() => setError('')} style={{ marginBottom: '1rem' }} />}
      {notice && <InlineNotification kind="success" lowContrast title="" subtitle={notice} onCloseButtonClick={() => setNotice('')} style={{ marginBottom: '1rem' }} />}
      {building && <InlineNotification kind="info" lowContrast hideCloseButton title={t('sandbox.building')} subtitle={t('sandbox.building_hint')} style={{ marginBottom: '1rem' }} />}

      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '0.5rem' }}>
        <div>
          <Heading>{t('sandbox.title')}</Heading>
          <p style={{ color: 'var(--tm-text-3)', fontSize: '0.8rem', marginTop: '0.25rem' }}>{t('sandbox.subtitle')}</p>
        </div>
        <Button size="sm" onClick={() => setShowForm(true)} renderIcon={Add}>{t('sandbox.add_custom')}</Button>
      </div>
      <div style={{ position: 'sticky', top: '3rem', background: 'var(--tm-bg)', zIndex: 1, paddingBottom: '0.5rem', marginBottom: '0.5rem', maxWidth: '420px' }}>
        <ListFilter value={filter} onChange={setFilter} placeholder={t('sandbox.filter')} />
      </div>

      {loading ? <PageLoading /> : (
        <Stack gap={4}>
          {visible.map((p) => (
            <Tile key={p.id}>
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', gap: '1rem' }}>
                <div style={{ opacity: p.image ? 1 : 0.55 }}>
                  <div style={{ display: 'flex', alignItems: 'center', gap: '0.3rem', flexWrap: 'wrap' }}>
                    <strong>{p.name}</strong>
                    <span style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem' }}>{p.id}{p.builtin ? '' : ' · custom'}</span>
                  </div>
                  <p style={{ color: 'var(--tm-text-2)', fontSize: '0.8rem', marginTop: '0.25rem' }}>{p.description}</p>
                </div>
                <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'center', flexShrink: 0 }}>
                  <Button size="sm" kind="ghost" renderIcon={Edit} onClick={() => startEdit(p)}>{t('sandbox.edit')}</Button>
                  {p.builtin && (
                    <Button size="sm" kind="secondary" renderIcon={Renew} disabled={building === p.id} onClick={() => void build(p)}>
                      {building === p.id ? t('sandbox.building') : p.image ? t('sandbox.rebuild') : t('sandbox.build')}
                    </Button>
                  )}
                  {!p.builtin && (
                    <Button size="sm" kind="danger--ghost" renderIcon={TrashCan} onClick={() => void remove(p)}>{t('action.delete')}</Button>
                  )}
                </div>
              </div>
              <div style={{ display: 'flex', gap: '0.25rem', marginTop: '0.5rem', flexWrap: 'wrap', alignItems: 'center', opacity: p.image ? 1 : 0.55 }}>
                {(p.toolchains ?? []).map((tc) => <Tag key={tc} type="blue" size="sm">{tc}</Tag>)}
                {p.image
                  ? (p.available
                      ? <Tag type="green" size="sm">{t('sandbox.built')}</Tag>
                      : <Tag type="red" size="sm">{t('sandbox.missing_image')}</Tag>)
                  : <Tag type="gray" size="sm">{t('sandbox.not_built')}</Tag>}
                {p.memory && <Tag type="gray" size="sm">mem {p.memory}</Tag>}
                {p.scratch_size && <Tag type="gray" size="sm">scratch {p.scratch_size}</Tag>}
              </div>
              {p.image && <p style={{ color: 'var(--tm-text-3)', fontSize: '0.7rem', marginTop: '0.35rem', wordBreak: 'break-all' }}>{p.image}</p>}
            </Tile>
          ))}
          {visible.length === 0 && (
            <Tile style={{ textAlign: 'center', padding: '3rem', color: 'var(--tm-text-3)' }}>
              <p>{t('sandbox.empty')}</p>
            </Tile>
          )}
        </Stack>
      )}

      <StorageBrowser project={project ?? ''} />

      {showForm && (
        <AppModal onClose={() => { setShowForm(false); setEditing(null) }} panelStyle={{ width: '640px' }}>
          <Heading>{editing ? t('sandbox.edit_heading') : t('sandbox.custom_heading')}</Heading>
          <Stack gap={3}>
            {error && <InlineNotification kind="error" lowContrast title={t('action.error')} subtitle={error} />}
            <TextInput id="sp-id" labelText={t('sandbox.field_id')} value={form.id} disabled={!!editing} onChange={(e) => setForm({ ...form, id: e.target.value })} placeholder="rust" />
            <TextInput id="sp-name" labelText={t('sandbox.field_name')} value={form.name} disabled={!!editing?.builtin} onChange={(e) => setForm({ ...form, name: e.target.value })} placeholder="Rust toolchain" />
            <TextArea id="sp-desc" labelText={t('sandbox.field_description')} rows={2} value={form.description} disabled={!!editing?.builtin} onChange={(e) => setForm({ ...form, description: e.target.value })} />
            <TextInput id="sp-image" labelText={t('sandbox.field_image')} value={form.image} disabled={!!editing?.builtin} onChange={(e) => setForm({ ...form, image: e.target.value })} placeholder="registry.local/toolbox@sha256:…" />
            <TextInput id="sp-toolchains" labelText={t('sandbox.field_toolchains')} value={form.toolchains} disabled={!!editing?.builtin} onChange={(e) => setForm({ ...form, toolchains: e.target.value })} placeholder="rust, wasm" />
            <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr 1fr 1fr', gap: '0.5rem' }}>
              <TextInput id="sp-memory" labelText={t('sandbox.field_memory')} value={form.memory} onChange={(e) => setForm({ ...form, memory: e.target.value })} placeholder="2g" />
              <TextInput id="sp-cpus" labelText={t('sandbox.field_cpus')} value={form.cpus} onChange={(e) => setForm({ ...form, cpus: e.target.value })} placeholder="2" />
              <TextInput id="sp-scratch" labelText={t('sandbox.field_scratch')} value={form.scratch_size} onChange={(e) => setForm({ ...form, scratch_size: e.target.value })} placeholder="1g" />
              <TextInput id="sp-tmp" labelText={t('sandbox.field_tmp')} value={form.tmp_size} onChange={(e) => setForm({ ...form, tmp_size: e.target.value })} placeholder="512m" />
            </div>
            <TextArea id="sp-cache" labelText={t('sandbox.field_cache')} rows={2} value={form.cache_env} onChange={(e) => setForm({ ...form, cache_env: e.target.value })} placeholder={'CARGO_HOME=/cache/cargo'} />
            <p style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem' }}>{editing?.builtin ? t('sandbox.edit_hint') : t('sandbox.custom_hint')}</p>
            <div style={{ display: 'flex', justifyContent: 'flex-end', gap: '0.5rem' }}>
              <Button kind="secondary" onClick={() => { setShowForm(false); setEditing(null) }}>{t('action.cancel')}</Button>
              <Button onClick={() => void saveCustom()} disabled={!form.id.trim() || !form.name.trim()}>{editing ? t('action.save') : t('action.create')}</Button>
            </div>
          </Stack>
        </AppModal>
      )}
    </div>
  )
}

// ── Persistent storage browser (docs/sandbox-storage.md §7.1) ────────────────
// Finder-style two-pane browser over a project's persistent sandbox areas:
// left — a lazy-loaded tree (workspace + cache as independent roots), right —
// the selected entry with preview (text/image), download, upload and delete.
// Read actions need reader rights; mutations are writer-gated server-side and
// confirmed here.

type AreaName = 'workspace' | 'cache'

const TEXT_EXT = /\.(md|txt|log|json|ya?ml|toml|go|ts|tsx|js|py|sh|sql|css|html|csv|env|conf|ini|xml|svg)$/i
const IMAGE_EXT = /\.(png|jpe?g|gif|webp|svg)$/i

function StorageBrowser({ project }: { project: string }) {
  const t = useT()
  const { confirmDialog, dialog: confirmDialogNode } = useConfirm()
  const [areas, setAreas] = useState<Record<AreaName, StorageFileEntry[] | null>>({ workspace: null, cache: null })
  const [loaded, setLoaded] = useState<Record<AreaName, boolean>>({ workspace: false, cache: false })
  const [children, setChildren] = useState<Record<string, StorageFileEntry[]>>({})
  const [expanded, setExpanded] = useState<Set<string>>(new Set())
  const [loadingDir, setLoadingDir] = useState<string | null>(null)
  const [error, setError] = useState('')
  const [selected, setSelected] = useState<{ area: AreaName; entry: StorageFileEntry } | null>(null)
  const [preview, setPreview] = useState<{ kind: 'text' | 'image'; content: string } | null>(null)
  const [previewError, setPreviewError] = useState('')
  const [busy, setBusy] = useState(false)
  const uploadTarget = useRef<{ area: AreaName; dir: string }>({ area: 'workspace', dir: '' })
  const uploadInput = useRef<HTMLInputElement | null>(null)

  const key = (area: AreaName, path: string) => `${area}:${path}`

  const load = useCallback(async () => {
    if (!project.trim()) return
    setError('')
    try {
      const [ws, cache] = await Promise.all([
        storageListDir(project.trim(), 'workspace', ''),
        storageListDir(project.trim(), 'cache', ''),
      ])
      setAreas({ workspace: ws.entries, cache: cache.entries })
      setLoaded({ workspace: true, cache: true })
      setChildren({})
      setExpanded(new Set())
      setSelected(null)
      setPreview(null)
    } catch (f) { setError(f instanceof Error ? f.message : 'Request failed') }
  }, [project])

  useEffect(() => { void load() }, [load])

  const loadChildren = async (area: AreaName, dirPath: string) => {
    const k = key(area, dirPath)
    setLoadingDir(k)
    setError('')
    try {
      const page = await storageListDir(project.trim(), area, dirPath)
      setChildren((current) => ({ ...current, [k]: page.entries }))
    } catch (f) { setError(f instanceof Error ? f.message : 'Request failed') }
    finally { setLoadingDir(null) }
  }

  const toggleDir = (area: AreaName, entry: StorageFileEntry) => {
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

  const openFile = async (area: AreaName, entry: StorageFileEntry) => {
    setSelected({ area, entry })
    setPreview(null)
    setPreviewError('')
    if (entry.bytes > 2 << 20) return // 2 MiB preview cap — download instead
    try {
      const response = await storageFileContent(project.trim(), area, entry.path)
      if (IMAGE_EXT.test(entry.name)) {
        const blob = await response.blob()
        setPreview({ kind: 'image', content: URL.createObjectURL(blob) })
      } else if (TEXT_EXT.test(entry.name) || entry.bytes < 256 << 10) {
        const text = await response.text()
        // Binary payload smuggled as text shows garbage — a NUL byte in the
        // first chunk is the cheap detector.
        if (text.slice(0, 2048).includes('\u0000')) setPreviewError(t('sandbox.files_binary') ?? 'Binary file — download to view.')
        else setPreview({ kind: 'text', content: text.length > 256 << 10 ? text.slice(0, 256 << 10) : text })
      } else {
        setPreviewError(t('sandbox.files_binary') ?? 'Binary file — download to view.')
      }
    } catch (f) { setPreviewError(f instanceof Error ? f.message : 'Request failed') }
  }

  const download = async (area: AreaName, entry: StorageFileEntry) => {
    try { await storageDownloadFile(project.trim(), area, entry.path) } catch (f) { setError(f instanceof Error ? f.message : 'Request failed') }
  }

  const remove = async (area: AreaName, entry: StorageFileEntry) => {
    const ok = await confirmDialog({
      title: t('sandbox.files_delete_title', { name: entry.name }),
      body: t('sandbox.files_delete_body'),
      danger: true,
    })
    if (!ok) return
    setBusy(true); setError('')
    try {
      await storageDeleteFile(project.trim(), area, entry.path)
      // Refresh the parent listing (or the area root when deleting there).
      const parentPath = entry.path.includes('/') ? entry.path.slice(0, entry.path.lastIndexOf('/')) : ''
      if (parentPath === '') await load()
      else await loadChildren(area, parentPath)
      setSelected(null); setPreview(null)
    } catch (f) { setError(f instanceof Error ? f.message : 'Request failed') }
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
    } catch (f) { setError(f instanceof Error ? f.message : 'Request failed') }
    finally { setBusy(false) }
  }

  const row = (area: AreaName, entry: StorageFileEntry, depth: number): ReactNode => {
    const k = key(area, entry.path)
    const open = expanded.has(k)
    const selectedHere = selected?.area === area && selected.entry.path === entry.path
    return (
      <div key={k}>
        <div
          role="button" tabIndex={0}
          onClick={() => (entry.dir ? toggleDir(area, entry) : void openFile(area, entry))}
          onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); entry.dir ? toggleDir(area, entry) : void openFile(area, entry) } }}
          style={{
            display: 'flex', alignItems: 'center', gap: '0.35rem', padding: '0.16rem 0.3rem', cursor: 'pointer', borderRadius: '4px',
            fontSize: '0.78rem', paddingLeft: `${0.3 + depth * 0.9}rem`,
            background: selectedHere ? 'var(--tm-elevated)' : 'transparent',
          }}
        >
          <span style={{ width: '0.8rem', color: 'var(--tm-text-3)', flexShrink: 0, fontSize: '0.66rem' }}>
            {entry.dir ? (loadingDir === k ? '…' : open ? '▾' : '▸') : ''}
          </span>
          <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', flex: 1 }}>{entry.name}</span>
          <span style={{ color: 'var(--tm-text-3)', fontSize: '0.66rem', whiteSpace: 'nowrap', flexShrink: 0 }}>
            {entry.dir ? `${entry.entries ?? 0}` : fmtBytes(entry.bytes)}
          </span>
        </div>
        {entry.dir && open && (children[k] ?? []).map((child) => row(area, child, depth + 1))}
      </div>
    )
  }

  const areaSection = (area: AreaName) => {
    const entries = areas[area]
    const label = area === 'workspace' ? (t('sandbox.storage_workspace') ?? 'Workspace') : (t('sandbox.storage_cache') ?? 'Cache')
    return (
      <div style={{ marginBottom: '1rem' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: '0.4rem', marginBottom: '0.25rem' }}>
          <span style={{ fontSize: '0.7rem', textTransform: 'uppercase', letterSpacing: '0.05em', color: 'var(--tm-text-3)' }}>{label}</span>
          <button
            title={t('sandbox.files_upload') ?? 'Upload files here'}
            onClick={() => startUpload(area, '')}
            style={{ background: 'none', border: 'none', color: 'var(--tm-text-3)', cursor: 'pointer', fontSize: '0.8rem', padding: '0 0.25rem', lineHeight: 1 }}
          >＋</button>
        </div>
        {entries === null ? (
          <div style={{ fontSize: '0.72rem', color: 'var(--tm-muted)', padding: '0.2rem 0' }}>{t('sandbox.storage_no_project') ?? 'Pick a project.'}</div>
        ) : entries.length === 0 ? (
          <div style={{ fontSize: '0.72rem', color: 'var(--tm-muted)', padding: '0.2rem 0' }}>{t('sandbox.files_empty') ?? 'Empty.'}</div>
        ) : (
          entries.map((entry) => row(area, entry, 0))
        )}
      </div>
    )
  }

  const selectedIsText = selected ? (TEXT_EXT.test(selected.entry.name) || !IMAGE_EXT.test(selected.entry.name)) : false

  return (
    <div style={{ marginTop: '2rem' }}>
      {confirmDialogNode}
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '0.5rem' }}>
        <div>
          <Heading style={{ fontSize: '1rem' }}>{t('sandbox.storage_title')}</Heading>
          <p style={{ color: 'var(--tm-text-3)', fontSize: '0.78rem', marginTop: '0.25rem' }}>{t('sandbox.storage_subtitle')}</p>
        </div>
        <Button size="sm" kind="ghost" renderIcon={Renew} disabled={loadingDir !== null || busy} onClick={() => void load()}>{t('common.refresh')}</Button>
      </div>
      <input ref={uploadInput} type="file" multiple hidden onChange={(e) => void onUploadPicked(e)} />
      {error && <InlineNotification kind="error" lowContrast title={t('action.error')} subtitle={error} onCloseButtonClick={() => setError('')} style={{ marginBottom: '0.75rem' }} />}
      {!project.trim() ? (
        <Tile style={{ color: 'var(--tm-text-3)' }}>{t('sandbox.storage_no_project')}</Tile>
      ) : (
        <div style={{ display: 'grid', gridTemplateColumns: 'minmax(280px, 380px) 1fr', gap: '1rem', alignItems: 'start' }}>
          {/* Left: the tree */}
          <div style={{ border: '1px solid var(--tm-border)', borderRadius: '6px', padding: '0.6rem 0.75rem', maxHeight: '60vh', overflowY: 'auto' }}>
            {areaSection('workspace')}
            {areaSection('cache')}
          </div>
          {/* Right: details / preview */}
          <div style={{ border: '1px solid var(--tm-border)', borderRadius: '6px', padding: '0.6rem 0.75rem', maxHeight: '60vh', overflowY: 'auto', minHeight: '12rem' }}>
            {!selected ? (
              <div style={{ color: 'var(--tm-muted)', fontSize: '0.78rem', padding: '1rem 0' }}>{t('sandbox.files_pick') ?? 'Pick a file to preview it.'}</div>
            ) : (
              <>
                <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', flexWrap: 'wrap', marginBottom: '0.5rem' }}>
                  <strong style={{ fontSize: '0.85rem', overflowWrap: 'anywhere' }}>{selected.entry.name}</strong>
                  {selected.entry.dir
                    ? <Tag type="blue" size="sm">{t('sandbox.files_dir') ?? 'folder'}</Tag>
                    : <Tag type="gray" size="sm">{fmtBytes(selected.entry.bytes)}</Tag>}
                  {selected.entry.mod_at && <span style={{ color: 'var(--tm-text-3)', fontSize: '0.7rem' }}>{new Date(selected.entry.mod_at).toLocaleString()}</span>}
                  <span style={{ marginLeft: 'auto', display: 'flex', gap: '0.4rem', flexShrink: 0 }}>
                    <Button size="sm" kind="ghost" renderIcon={Upload} onClick={() => startUpload(selected.area, selected.entry.dir ? selected.entry.path : parentOf(selected.entry.path))}>{t('sandbox.files_upload') ?? 'Upload'}</Button>
                    {!selected.entry.dir && (
                      <Button size="sm" kind="ghost" renderIcon={Download} onClick={() => void download(selected.area, selected.entry)}>{t('sandbox.files_download') ?? 'Download'}</Button>
                    )}
                    <Button size="sm" kind="danger--ghost" renderIcon={TrashCan} disabled={busy} onClick={() => void remove(selected.area, selected.entry)}>{t('action.delete')}</Button>
                  </span>
                </div>
                <div style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)', marginBottom: '0.5rem', overflowWrap: 'anywhere' }}>
                  {selected.area === 'workspace' ? (t('sandbox.storage_workspace') ?? 'Workspace') : (t('sandbox.storage_cache') ?? 'Cache')}/{selected.entry.path}
                </div>
                {selected.entry.dir ? (
                  <div style={{ color: 'var(--tm-muted)', fontSize: '0.78rem' }}>
                    {children[key(selected.area, selected.entry.path)]?.length ?? selected.entry.entries ?? 0} {t('sandbox.files_items') ?? 'items'} — раскройте папку в дереве слева.
                  </div>
                ) : previewError ? (
                  <div style={{ color: 'var(--tm-muted)', fontSize: '0.78rem' }}>{previewError}</div>
                ) : !preview ? (
                  <div style={{ color: 'var(--tm-muted)', fontSize: '0.78rem' }}>…</div>
                ) : preview.kind === 'image' ? (
                  <img src={preview.content} alt={selected.entry.name} style={{ maxWidth: '100%', maxHeight: '45vh', borderRadius: '4px', border: '1px solid var(--tm-border)' }} />
                ) : selectedIsText || preview.content ? (
                  <pre style={{ margin: 0, padding: '0.5rem', background: 'var(--tm-surface)', border: '1px solid var(--tm-border)', borderRadius: '4px', fontSize: '0.72rem', lineHeight: 1.45, overflow: 'auto', maxHeight: '45vh', whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{preview.content}</pre>
                ) : null}
              </>
            )}
          </div>
        </div>
      )}
    </div>
  )
}

function parentOf(path: string): string {
  return path.includes('/') ? path.slice(0, path.lastIndexOf('/')) : ''
}
