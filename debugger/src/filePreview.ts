import { workspaceApi } from './workspaceApi'
import type { MediaViewerPayload } from './MediaViewer'

// Which /workspace/... documents the browser can show inline: images open as
// images, markdown as a markdown render, other text-like documents as a
// highlighted pane; everything else (pdf, zip, office) keeps the download
// path — the browser handles those itself.
const IMAGE_EXTS = new Set(['png', 'jpg', 'jpeg', 'gif', 'webp', 'svg', 'bmp'])
// Same set the storage preview treats as markdown (Storage.tsx MARKDOWN_EXT).
const MARKDOWN_EXTS = new Set(['md', 'markdown'])
const TEXT_EXTS = new Set([
  'txt', 'log', 'json', 'yaml', 'yml', 'toml', 'ini',
  'csv', 'tsv', 'xml', 'html', 'htm', 'css', 'scss', 'js', 'jsx', 'ts',
  'tsx', 'mjs', 'cjs', 'mts', 'cts', 'go', 'py', 'rb', 'java', 'kt', 'rs',
  'sh', 'bash', 'zsh', 'sql', 'env', 'conf', 'cfg', 'gitignore',
  'dockerfile', 'makefile',
])

/** Preview mode for a workspace path, or null when the file should just be
 *  downloaded (the browser's own viewer handles pdf/zip/office). */
export function previewKindFor(path: string): 'image' | 'text' | 'markdown' | null {
  const name = (path.split('/').pop() ?? path).toLowerCase()
  const ext = name.includes('.') ? (name.split('.').pop() ?? '') : name
  if (IMAGE_EXTS.has(ext)) return 'image'
  if (MARKDOWN_EXTS.has(ext)) return 'markdown'
  if (TEXT_EXTS.has(ext) || name === 'dockerfile' || name === 'makefile') return 'text'
  return null
}

/** A workspace link whose path ends with a slash marks a DIRECTORY: clicking
 *  it must open the storage browser at that folder, not a download (the files
 *  API serves files, not listings). */
export function isWorkspaceDirectoryPath(path: string): boolean {
  return path.endsWith('/')
}

/** The storage page URL browsing a workspace directory: strips the marker
 *  slash — the browser tree works with plain relative paths. */
export function storageBrowseUrl(project: string, path: string): string {
  const dir = path.replace(/\/+$/, '')
  return `/storage?project=${encodeURIComponent(project)}&path=${encodeURIComponent(dir)}`
}

/** Fetch a workspace file and shape it for MediaViewer. Returns null when the
 *  file is not previewable (caller falls back to download); throws on fetch
 *  failure so the caller can report it. */
export async function loadWorkspaceFilePreview(project: string, path: string): Promise<MediaViewerPayload | null> {
  const kind = previewKindFor(path)
  if (!kind) return null
  const response = await workspaceApi.fetchFile(project, path)
  if (!response.ok) {
    throw new Error(`HTTP ${response.status}`)
  }
  	if (kind === 'image') {
  		return { kind: 'image', src: URL.createObjectURL(await response.blob()), title: path }
  	}
  	const text = await response.text()
  	if (kind === 'markdown') {
  		// The same size cap the storage preview applies before parsing.
  		return { kind: 'markdown', src: text.length > 256 << 10 ? text.slice(0, 256 << 10) : text, title: path }
  	}
  	return { kind: 'text', src: text, title: path }
  }
