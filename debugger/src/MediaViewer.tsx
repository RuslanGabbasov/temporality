import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useT } from './i18n'
import { highlightCode, languageForFile } from './syntax'
import Markdown from './Markdown'

/** Fullscreen media viewer for trace previews: an image with wheel-zoom and
 *  drag-pan, or a document pane (markdown renders as markdown — identical to
 *  the storage preview — everything else as highlighted code). Esc/click-out
 *  closes. One component, three payloads — trace rows open it for image tools
 *  (view_image screenshots), file tools (write_file content) and workspace
 *  file links in artifacts/versions. */
export interface MediaViewerPayload {
  kind: 'image' | 'text' | 'markdown'
  /** image: an object URL; text/markdown: the raw content. */
  src: string
  title: string
}

export default function MediaViewer({ payload, onClose, project }: { payload: MediaViewerPayload; onClose: () => void; project?: string }) {
  const t = useT()
  const [zoom, setZoom] = useState(1)
  const [pan, setPan] = useState({ x: 0, y: 0 })
  const dragRef = useRef<{ x: number; y: number; px: number; py: number } | null>(null)

  const close = useCallback(() => onClose(), [onClose])
  // Same deterministic highlighting as the storage preview: language by file
  // extension, no auto-detection; unknown extensions stay plain text.
  const textLanguage = payload.kind === 'text' ? languageForFile(payload.title) : ''
  const html = useMemo(() => (payload.kind === 'text' ? highlightCode(payload.src, textLanguage) : ''), [payload, textLanguage])
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') close() }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [close])

  if (payload.kind === 'text' || payload.kind === 'markdown') {
    return (
      <div role="dialog" aria-modal onClick={close} style={{ position: 'fixed', inset: 0, zIndex: 10001, background: 'rgba(0,0,0,0.75)', display: 'flex', flexDirection: 'column' }}>
        <div onClick={(e) => e.stopPropagation()} style={{ flex: 1, display: 'flex', flexDirection: 'column', margin: '2rem', background: 'var(--tm-surface, #142522)', border: '1px solid var(--tm-border)', borderRadius: '10px', overflow: 'hidden' }}>
          <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', padding: '0.5rem 0.9rem', borderBottom: '1px solid var(--tm-border)', fontSize: '0.78rem', color: 'var(--tm-text-2)' }}>
            <span style={{ fontFamily: 'monospace', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{payload.title}{textLanguage ? ` · ${textLanguage}` : ''}</span>
            <button onClick={close} style={{ background: 'none', border: 'none', color: 'var(--tm-text-2)', cursor: 'pointer', fontSize: '1rem', lineHeight: 1 }}>✕</button>
          </div>
          {payload.kind === 'markdown' ? (
            // Markdown renders as markdown — the SAME surface the storage
            // preview uses (same component, same styles), not a code pane.
            <div style={{ flex: 1, overflowY: 'auto', padding: '0.9rem 1.1rem' }}>
              <div className="artifact-body"><Markdown content={payload.src} project={project} /></div>
            </div>
          ) : (
            <pre className="code-preview hljs" style={{ flex: 1, margin: 0, padding: '0.9rem', overflow: 'auto', color: 'var(--tm-text)', fontSize: '0.75rem', lineHeight: 1.5, fontFamily: '"SFMono-Regular", Consolas, monospace', whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }} dangerouslySetInnerHTML={{ __html: html }} />
          )}
        </div>
      </div>
    )
  }

  return (
    <div
      role="dialog"
      aria-modal
      onClick={close}
      onWheel={(e) => { e.preventDefault(); setZoom((z) => Math.min(8, Math.max(0.2, z * Math.exp(-e.deltaY * 0.0015)))) }}
      style={{ position: 'fixed', inset: 0, zIndex: 10001, background: 'rgba(0,0,0,0.85)', display: 'grid', placeItems: 'center', overflow: 'hidden', cursor: dragRef.current ? 'grabbing' : 'grab' }}
    >
      <div
        onClick={(e) => e.stopPropagation()}
        style={{ position: 'absolute', top: '0.6rem', right: '0.9rem', display: 'flex', gap: '0.6rem', alignItems: 'center', fontSize: '0.75rem', color: 'var(--tm-text-2)', zIndex: 1 }}
      >
        <span style={{ fontFamily: 'monospace' }}>{payload.title} · {Math.round(zoom * 100)}%</span>
        <button onClick={close} style={{ background: 'none', border: 'none', color: '#fff', cursor: 'pointer', fontSize: '1rem', lineHeight: 1 }}>✕</button>
      </div>
      <img
        src={payload.src}
        alt={payload.title}
        draggable={false}
        onClick={(e) => e.stopPropagation()}
        onMouseDown={(e) => { dragRef.current = { x: e.clientX, y: e.clientY, px: pan.x, py: pan.y } }}
        onMouseMove={(e) => {
          if (!dragRef.current) return
          setPan({ x: dragRef.current.px + (e.clientX - dragRef.current.x), y: dragRef.current.py + (e.clientY - dragRef.current.y) })
        }}
        onMouseUp={() => { dragRef.current = null }}
        onMouseLeave={() => { dragRef.current = null }}
        onDoubleClick={(e) => { e.stopPropagation(); setZoom(1); setPan({ x: 0, y: 0 }) }}
        style={{ maxWidth: '92vw', maxHeight: '88vh', transform: `translate(${pan.x}px, ${pan.y}px) scale(${zoom})`, transition: dragRef.current ? 'none' : 'transform 0.08s', userSelect: 'none' }}
      />
      <div onClick={(e) => e.stopPropagation()} style={{ position: 'absolute', bottom: '0.8rem', fontSize: '0.68rem', color: 'rgba(255,255,255,0.55)' }}>
        {t('media.hint') ?? 'wheel — zoom · drag — pan · double click — reset · Esc — close'}
      </div>
    </div>
  )
}
