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

export function resolveImageSource(raw: string, project?: string): ResolvedImageRef {
  const match = IN_APP_STORAGE_PATH.exec(raw)
  if (!match || !project) return { src: raw }
  const area = match[1] as 'workspace' | 'cache'
  const path = match[2]
  return { src: raw, storage: { project, area, path, url: storageFileURL(project, area, path) } }
}

// Blobs are cached per src+project so repeated renders of the same artifact
// never refetch. Blobs (not object URLs) are what gets cached because object
// URLs are revoked on unmount — every mount creates and revokes its own.
const imageBlobCache = new Map<string, Promise<Blob>>()

/** Resolve an image src for <img>: http(s) passes through untouched; in-app
 *  storage paths become blob object URLs (fetched with the auth header,
 *  revoked on unmount). Undefined while a storage image is in flight; on
 *  failure the raw path stays as the src — a visible fallback, never silent. */
export function useResolvedImage(src: string | undefined, project?: string): string | undefined {
  const ref = useMemo(() => (src ? resolveImageSource(src, project) : undefined), [src, project])
  const [resolved, setResolved] = useState<string | undefined>(undefined)

  useEffect(() => {
    const storage = ref?.storage
    if (!storage) return
    setResolved(undefined)
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
        else setResolved(objectUrl)
      })
      .catch(() => { if (!cancelled) setResolved(ref.src) })
    return () => {
      cancelled = true
      if (objectUrl) URL.revokeObjectURL(objectUrl)
    }
  }, [ref])

  return ref ? (ref.storage ? resolved : ref.src) : undefined
}
