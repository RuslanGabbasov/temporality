import { workspaceApi } from './workspaceApi'
import type { MediaViewerPayload } from './MediaViewer'

// Which /workspace/... documents the browser can show inline: images open as
// images, text-like documents as a highlighted pane; everything else (pdf,
// zip, office) keeps the download path — the browser handles those itself.
const IMAGE_EXTS = new Set(['png', 'jpg', 'jpeg', 'gif', 'webp', 'svg', 'bmp'])
const TEXT_EXTS = new Set([
  'md', 'markdown', 'txt', 'log', 'json', 'yaml', 'yml', 'toml', 'ini',
  'csv', 'tsv', 'xml', 'html', 'htm', 'css', 'scss', 'js', 'jsx', 'ts',
  'tsx', 'go', 'py', 'rb', 'java', 'kt', 'rs', 'sh', 'bash', 'zsh', 'sql',
  'env', 'conf', 'cfg', 'gitignore', 'dockerfile', 'makefile',
])

/** Preview mode for a workspace path, or null when the file should just be
 *  downloaded (the browser's own viewer handles pdf/zip/office). */
export function previewKindFor(path: string): 'image' | 'text' | null {
  const name = (path.split('/').pop() ?? path).toLowerCase()
  const ext = name.includes('.') ? (name.split('.').pop() ?? '') : name
  if (IMAGE_EXTS.has(ext)) return 'image'
  if (TEXT_EXTS.has(ext) || name === 'dockerfile' || name === 'makefile') return 'text'
  return null
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
  return { kind: 'text', src: await response.text(), title: path }
}
