import { useEffect, useState } from 'react'
import { useT } from './i18n'
import { storageFileContent } from './kernelApi'
import MediaViewer, { type MediaViewerPayload } from './MediaViewer'

/** Inline trace previews (the «what did the agent actually see» layer):
 *  - image tools (view_image / inspect_image): a workspace file thumbnail
 *    right in the expanded row; click → the fullscreen zoom/pan viewer.
 *  - file tools (write_file / edit_file): the content fragment as a compact
 *    monospace block; click → the fullscreen text viewer.
 *
 *  The image fetch rides the storage file API (area=workspace, the same
 *  /workspace the tool's path addresses) with header auth → object URL. */
function TracePreviewInner({ project, kind, path, content }: {
  project: string
  kind: 'image' | 'file'
  path: string
  content?: string
}) {
  const t = useT()
  const [url, setUrl] = useState<string | null>(null)
  const [error, setError] = useState('')
  const [viewer, setViewer] = useState<MediaViewerPayload | null>(null)

  useEffect(() => {
    if (kind !== 'image') return
    let objectURL: string | null = null
    let alive = true
    storageFileContent(project, 'workspace', path)
      .then((response) => response.blob())
      .then((blob) => {
        if (!alive) return
        objectURL = URL.createObjectURL(blob)
        setUrl(objectURL)
      })
      .catch((e) => { if (alive) setError(e instanceof Error ? e.message : String(e)) })
    return () => { alive = false; if (objectURL) URL.revokeObjectURL(objectURL) }
  }, [project, kind, path])

  if (kind === 'image') {
    return (
      <div style={{ marginTop: '0.35rem' }}>
        {url ? (
          <img
            src={url}
            alt={path}
            onClick={() => setViewer({ kind: 'image', src: url, title: path })}
            style={{ maxHeight: '160px', maxWidth: '100%', borderRadius: '4px', border: '1px solid var(--tm-border)', cursor: 'zoom-in', display: 'block' }}
          />
        ) : error ? (
          <span style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)' }}>
            {t('trace.preview_image_failed', { error }) ?? `image unavailable (${error})`}
          </span>
        ) : (
          <span style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)' }}>{t('trace.preview_loading') ?? 'loading…'}</span>
        )}
        {viewer && <MediaViewer payload={viewer} onClose={() => setViewer(null)} />}
      </div>
    )
  }

  // File fragment: a bounded monospace block; the click opens the full text.
  const preview = content ?? ''
  if (!preview.trim()) return null
  return (
    <div style={{ marginTop: '0.35rem' }}>
      <pre
        onClick={() => setViewer({ kind: 'text', src: preview, title: path })}
        style={{ margin: 0, padding: '0.45rem 0.55rem', background: 'var(--tm-raised)', border: '1px solid var(--tm-border)', borderRadius: '4px', color: 'var(--tm-text)', fontSize: '0.68rem', lineHeight: 1.45, fontFamily: '"SFMono-Regular", Consolas, monospace', maxHeight: '140px', overflow: 'hidden', cursor: 'zoom-in', whiteSpace: 'pre-wrap', overflowWrap: 'anywhere', position: 'relative' }}
      >
        {preview.length > 600 ? `${preview.slice(0, 600)}\n…` : preview}
      </pre>
      {preview.length > 600 && (
        <button onClick={() => setViewer({ kind: 'text', src: preview, title: path })} style={{ background: 'none', border: 'none', color: 'var(--tm-teal)', cursor: 'pointer', fontSize: '0.68rem', padding: '0.15rem 0' }}>
          {t('trace.preview_open_full') ?? 'open fully…'}
        </button>
      )}
      {viewer && <MediaViewer payload={viewer} onClose={() => setViewer(null)} />}
    </div>
  )
}

/** Maps a tool.started/tool.completed event onto a preview request: image
 *  tools by their path argument, file tools by their content argument.
 *  Returns null when the event carries nothing previewable. */
export function tracePreviewOf(event: { type: string; data?: Record<string, unknown> }): { kind: 'image' | 'file'; path: string; content?: string } | null {
  if (event.type !== 'tool.started' && event.type !== 'tool.completed') return null
  const tool = typeof event.data?.tool === 'string' ? event.data.tool : ''
  let args: Record<string, unknown> | null = null
  if (typeof event.data?.arguments === 'string' && event.data.arguments.trim().startsWith('{')) {
    try { args = JSON.parse(event.data.arguments) } catch { /* bounded fragment — not parseable */ }
  } else if (event.data?.arguments && typeof event.data.arguments === 'object') {
    args = event.data.arguments as Record<string, unknown>
  }
  if (!args) return null
  if (tool === 'view_image' || tool === 'inspect_image') {
    const path = typeof args.path === 'string' ? args.path.trim() : ''
    if (!path) return null
    return { kind: 'image', path: path.replace(/^\/workspace\//, '').replace(/^\//, '') }
  }
  if (tool === 'write_file' || tool === 'edit_file') {
    const path = typeof args.path === 'string' ? args.path.trim() : ''
    const content = typeof args.content === 'string' ? args.content
      : typeof args.new_string === 'string' ? args.new_string
        : ''
    if (!content) return null
    return { kind: 'file', path: path || 'untitled', content }
  }
  return null
}

export default function TracePreview({ project, request }: { project: string; request: { kind: 'image' | 'file'; path: string; content?: string } }) {
  // Remount per request identity: blob URLs and error states belong to one path.
  return <TracePreviewInner key={`${request.kind}:${request.path}`} project={project} kind={request.kind} path={request.path} content={request.content} />
}
