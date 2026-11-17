import { useEffect, useMemo, useState } from 'react'
import { storageFileContent, storageFileURL } from './kernelApi'

/** In-app references (`/workspace/<rel>`, `/cache/<rel>`) point at files
 *  behind the kernel storage API: the bare URL 404s on the debugger origin
 *  and the endpoint wants the auth header, so <img>/<a> cannot use it
 *  directly. This pure mapper classifies a src — http(s) URLs and plain
 *  origin paths pass through untouched — and resolves storage references to
 *  the project/area/path triple plus the storage URL (content fetches still
 *  need the auth header; see storageFileContent in kernelApi.ts). */
export interface ResolvedImageRef {
  /** <img src> value for pass-through references, visible fallback otherwise. */
  src: string
  storage?: { project: string; area: 'workspace' | 'cache'; path: string; url: string }
}

export const IN_APP_STORAGE_PATH = /^\/(workspace|cache)\/(.+)$/

// The kernel files-API URL — the exact form linkifyWorkspacePaths and the
// chat render for every workspace path. Agents paste it into artifact bodies
// verbatim (`![](​/kernel-api/v1/workspace/projects/<id>/files?path=…)`), so
// it classifies as a storage reference: the project id rides in the URL
// itself, no selection context needed.
const FILES_API_URL_RE = /^\/(?:kernel-api)?\/v1\/workspace\/projects\/([A-Za-z0-9][A-Za-z0-9._-]*)\/files\?(.+)$/

/** Bare relative paths ("design/x.png") and the sandbox-absolute form
 *  without the leading slash ("workspace/design/x.png") resolve to the
 *  in-app "/workspace/<rel>" reference; scheme-relative, absolute and URL
 *  forms pass through. The old naive prepend produced
 *  /workspace/workspace/... for the second form — a silent 404. */
export function normalizeImageSrc(src: string): string {
  if (/^\/?workspace\//i.test(src)) return '/workspace/' + src.replace(/^\/?workspace\//i, '')
  if (/^(?:[a-z][a-z0-9+.-]*:|\/\/|\/)/i.test(src)) return src
  return '/workspace/' + src
}

export function resolveImageSource(raw: string, project?: string): ResolvedImageRef {
  const filesApi = FILES_API_URL_RE.exec(raw)
  if (filesApi) {
    const params = new URLSearchParams(filesApi[2])
    const path = params.get('path') ?? ''
    if (path) {
      const area = params.get('area') === 'cache' ? 'cache' : 'workspace'
      // The URL names the owning project — the current selection is irrelevant.
      return { src: raw, storage: { project: filesApi[1], area, path, url: raw } }
    }
  }
  const match = IN_APP_STORAGE_PATH.exec(raw)
  if (match && project) {
    const area = match[1] as 'workspace' | 'cache'
    const path = match[2]
    return { src: raw, storage: { project, area, path, url: storageFileURL(project, area, path) } }
  }
  return { src: raw }
}

// Blobs are cached per src+project so repeated renders of the same artifact
// never refetch. Blobs (not object URLs) are what gets cached because object
// URLs are revoked on unmount — every mount creates and revokes its own.
const imageBlobCache = new Map<string, Promise<Blob>>()

export interface ResolvedImage {
  /** <img src> once resolved: a blob object URL for storage references, the
   *  raw src for pass-through ones — and the raw src again on failure (a
   *  visible fallback, never silent). */
  src?: string
  /** Human-readable fetch failure (status + the kernel's error text): the
   *  fallback link carries it as the tooltip, so «why is this a link» has an
   *  answer on hover. */
  error?: string
}

/** Resolve an image src for <img>: http(s) passes through untouched; in-app
 *  storage paths become blob object URLs (fetched with the auth header,
 *  revoked on unmount). Undefined while a storage image is in flight. */
export function useResolvedImage(src: string | undefined, project?: string): ResolvedImage {
  const ref = useMemo(() => (src ? resolveImageSource(src, project) : undefined), [src, project])
  const [state, setState] = useState<ResolvedImage>({})

  useEffect(() => {
    const storage = ref?.storage
    if (!storage) return
    setState({})
    const key = `${storage.project}|${ref.src}`
    let entry = imageBlobCache.get(key)
    if (!entry) {
      entry = storageFileContent(storage.project, storage.area, storage.path).then((response) => response.blob())
      imageBlobCache.set(key, entry)
      // A failed fetch must not poison the cache — the next mount retries.
      entry.catch(() => { imageBlobCache.delete(key) })
    }
    let cancelled = false
    let objectUrl: string | undefined
    entry
      .then((blob) => {
        objectUrl = URL.createObjectURL(blob)
        if (cancelled) URL.revokeObjectURL(objectUrl)
        else setState({ src: objectUrl })
      })
      .catch((failure) => { if (!cancelled) setState({ src: ref.src, error: failure instanceof Error ? failure.message : String(failure) }) })
    return () => {
      cancelled = true
      if (objectUrl) URL.revokeObjectURL(objectUrl)
    }
  }, [ref])

  if (!ref) return {}
  if (!ref.storage) return { src: ref.src }
  return state
}
