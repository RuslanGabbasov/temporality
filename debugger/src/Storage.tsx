import { useEffect, useState, useCallback, useRef, type ChangeEvent, type ReactNode } from 'react'
import { Button, Heading, InlineNotification, Tag, Tile } from '@carbon/react'
import { Download, Renew, TrashCan, Upload } from '@carbon/icons-react'
import PageLoading from './PageLoading'
import ProjectRequired from './ProjectRequired'
import Markdown from './Markdown'
import { useConfirm } from './ConfirmDialog'
import { useT } from './i18n'
import {
  storageListDir,
  storageFileContent,
  storageDownloadFile,
  storageDeleteFile,
  storageUploadFile,
  type StorageFileEntry,
} from './kernelApi'
import { fmtBytes } from './format'

type AreaName = 'workspace' | 'cache'

const TEXT_EXT = /\.(md|txt|log|json|ya?ml|toml|go|ts|tsx|js|py|sh|sql|css|html|csv|env|conf|ini|xml|svg)$/i
const MARKDOWN_EXT = /\.(md|markdown)$/i
const IMAGE_EXT = /\.(png|jpe?g|gif|webp|svg)$/i

function message(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }

/** Storage page (docs/sandbox-storage.md §7.1): a Finder-style two-pane
 *  browser over a project's persistent sandbox areas. Left — a lazy-loaded
 *  tree (workspace + cache as independent roots); right — the selected entry
 *  with preview (text/image), download, upload and delete. Reads need reader
 *  rights; mutations are writer-gated server-side and confirmed here. */
export default function Storage({ project }: { project: string }) {
  const t = useT()
  const { confirmDialog, dialog: confirmDialogNode } = useConfirm()
  const [areas, setAreas] = useState<Record<AreaName, StorageFileEntry[] | null>>({ workspace: null, cache: null })
  const [children, setChildren] = useState<Record<string, StorageFileEntry[]>>({})
  const [expanded, setExpanded] = useState<Set<string>>(new Set())
  const [loadingDir, setLoadingDir] = useState<string | null>(null)
  const [error, setError] = useState('')
  const [selected, setSelected] = useState<{ area: AreaName; entry: StorageFileEntry } | null>(null)
  const [preview, setPreview] = useState<{ kind: 'text' | 'markdown' | 'image'; content: string } | null>(null)
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
      setChildren({})
      setExpanded(new Set())
      setSelected(null)
      setPreview(null)
    } catch (f) { setError(message(f)) }
  }, [project])

  useEffect(() => { void load() }, [load])

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
    if (entry.bytes > 2 << 20) { setPreviewError(t('storage.files_too_big') ?? 'File is too big to preview — download it.'); return }
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
        setPreviewError(t('storage.files_binary') ?? 'Binary file — download to view.')
        return
      }
      const kind = MARKDOWN_EXT.test(entry.name) ? 'markdown' : 'text'
      setPreview({ kind, content: text.length > 256 << 10 ? text.slice(0, 256 << 10) : text })
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
    } catch (f) { setError(message(f)) }
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
    const label = area === 'workspace' ? (t('storage.area_workspace') ?? 'Workspace') : (t('storage.area_cache') ?? 'Cache')
    return (
      <div style={{ marginBottom: '1rem' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: '0.4rem', marginBottom: '0.25rem' }}>
          <span style={{ fontSize: '0.7rem', textTransform: 'uppercase', letterSpacing: '0.05em', color: 'var(--tm-text-3)' }}>{label}</span>
          <button
            title={t('storage.files_upload') ?? 'Upload files here'}
            onClick={() => startUpload(area, '')}
            style={{ background: 'none', border: 'none', color: 'var(--tm-text-3)', cursor: 'pointer', fontSize: '0.8rem', padding: '0 0.25rem', lineHeight: 1 }}
          >＋</button>
        </div>
        {entries === null ? (
          <div style={{ fontSize: '0.72rem', color: 'var(--tm-muted)', padding: '0.2rem 0' }}>…</div>
        ) : entries.length === 0 ? (
          <div style={{ fontSize: '0.72rem', color: 'var(--tm-muted)', padding: '0.2rem 0' }}>{t('storage.files_empty') ?? 'Empty.'}</div>
        ) : (
          entries.map((entry) => row(area, entry, 0))
        )}
      </div>
    )
  }

  if (!project.trim()) return <ProjectRequired what={t('storage.title') ?? 'Storage'} />

  return (
    <div style={{ padding: '1rem', height: 'calc(100vh - 3rem)', display: 'flex', flexDirection: 'column', boxSizing: 'border-box' }}>
      {confirmDialogNode}
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '0.75rem', flexShrink: 0 }}>
        <div>
          <Heading style={{ fontSize: '1.1rem' }}>{t('storage.title') ?? 'Storage'}</Heading>
          <p style={{ color: 'var(--tm-text-3)', fontSize: '0.8rem', marginTop: '0.25rem' }}>{t('storage.subtitle')}</p>
        </div>
        <Button size="sm" kind="ghost" renderIcon={Renew} disabled={loadingDir !== null || busy} onClick={() => void load()}>{t('common.refresh') ?? 'Refresh'}</Button>
      </div>
      <input ref={uploadInput} type="file" multiple hidden onChange={(e) => void onUploadPicked(e)} />
      {error && <InlineNotification kind="error" lowContrast title={t('action.error') ?? 'Error'} subtitle={error} onCloseButtonClick={() => setError('')} style={{ marginBottom: '0.75rem' }} />}
      <div style={{ display: 'grid', gridTemplateColumns: 'minmax(280px, 380px) 1fr', gap: '1rem', flex: 1, minHeight: 0 }}>
        {/* Left: the tree */}
        <div style={{ border: '1px solid var(--tm-border)', borderRadius: '6px', padding: '0.6rem 0.75rem', overflowY: 'auto', minHeight: 0 }}>
          {areaSection('workspace')}
          {areaSection('cache')}
        </div>
        {/* Right: details / preview */}
        <div style={{ border: '1px solid var(--tm-border)', borderRadius: '6px', padding: '0.6rem 0.75rem', overflowY: 'auto', minHeight: 0 }}>
          {!selected ? (
            <div style={{ color: 'var(--tm-muted)', fontSize: '0.78rem', padding: '1rem 0' }}>{t('storage.files_pick') ?? 'Pick a file to preview it.'}</div>
          ) : (
            <>
              <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', flexWrap: 'wrap', marginBottom: '0.5rem' }}>
                <strong style={{ fontSize: '0.85rem', overflowWrap: 'anywhere' }}>{selected.entry.name}</strong>
                {selected.entry.dir
                  ? <Tag type="blue" size="sm">{t('storage.files_dir') ?? 'folder'}</Tag>
                  : <Tag type="gray" size="sm">{fmtBytes(selected.entry.bytes)}</Tag>}
                {selected.entry.mod_at && <span style={{ color: 'var(--tm-text-3)', fontSize: '0.7rem' }}>{new Date(selected.entry.mod_at).toLocaleString()}</span>}
                <span style={{ marginLeft: 'auto', display: 'flex', gap: '0.4rem', flexShrink: 0 }}>
                  <Button size="sm" kind="ghost" renderIcon={Upload} onClick={() => startUpload(selected.area, selected.entry.dir ? selected.entry.path : parentOf(selected.entry.path))}>{t('storage.files_upload') ?? 'Upload'}</Button>
                  {!selected.entry.dir && (
                    <Button size="sm" kind="ghost" renderIcon={Download} onClick={() => void download(selected.area, selected.entry)}>{t('storage.files_download') ?? 'Download'}</Button>
                  )}
                  <Button size="sm" kind="danger--ghost" renderIcon={TrashCan} disabled={busy} onClick={() => void remove(selected.area, selected.entry)}>{t('action.delete') ?? 'Delete'}</Button>
                </span>
              </div>
              <div style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)', marginBottom: '0.5rem', overflowWrap: 'anywhere' }}>
                {selected.area}/{selected.entry.path}
              </div>
              {selected.entry.dir ? (
                <div style={{ color: 'var(--tm-muted)', fontSize: '0.78rem' }}>
                  {children[key(selected.area, selected.entry.path)]?.length ?? selected.entry.entries ?? 0} {t('storage.files_items') ?? 'items'} — {t('storage.files_expand') ?? 'expand the folder in the tree on the left.'}
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
                <pre style={{ margin: 0, padding: '0.5rem', background: 'var(--tm-surface)', border: '1px solid var(--tm-border)', borderRadius: '4px', fontSize: '0.72rem', lineHeight: 1.45, whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{preview.content}</pre>
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
