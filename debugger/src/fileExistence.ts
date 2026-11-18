import { storageListDir } from './kernelApi'

// Workspace-path links in rendered markdown (chat answers, artifact bodies)
// point into the project's workspace area — the SAME area-relative namespace
// the storage tree lists. So one directory listing answers existence for
// every link into that directory, and the listing is cached as a promise:
// concurrent links and re-renders share one API call.

// path (area-relative, trailing slash stripped) → is it a directory.
const dirListings = new Map<string, { at: number; entries: Promise<Map<string, boolean>> }>()

// Negative results go stale fast while agents write files mid-answer; the
// TTL lets later bubbles see a file that appeared after an earlier bubble's
// listing. Positive results rarely flip, but one shared TTL keeps it simple.
const LISTING_TTL_MS = 30_000

function listingKey(project: string, dir: string): string {
  return `${project}\n${dir}`
}

function loadDirEntries(project: string, dir: string): Promise<Map<string, boolean>> {
  const key = listingKey(project, dir)
  const cached = dirListings.get(key)
  if (cached && Date.now() - cached.at < LISTING_TTL_MS) return cached.entries
  const entries = storageListDir(project, 'workspace', dir)
    .then((data) => {
      const map = new Map<string, boolean>()
      for (const entry of data.entries ?? []) map.set(entry.path, entry.dir)
      return map
    })
    .catch(() => {
      // A failed listing must not poison the cache: drop it so the next link
      // retries instead of inheriting the failure.
      dirListings.delete(key)
      return new Map<string, boolean>()
    })
  dirListings.set(key, { at: Date.now(), entries })
  return entries
}

export type FileExistence = 'unknown' | 'exists' | 'missing'

/** 'exists' / 'missing' verdicts come from a directory listing; 'unknown'
 *  covers the cannot-decide cases (listing failed, empty directory) — the
 *  caller fail-opens on those, hiding real files over a transient error is
 *  worse than an extra click into the download fallback. */
export async function workspaceFileExists(project: string, path: string): Promise<FileExistence> {
  const clean = path.replace(/\/+$/, '')
  const slash = clean.lastIndexOf('/')
  const dir = slash === -1 ? '' : clean.slice(0, slash)
  const entries = await loadDirEntries(project, dir)
  if (entries.size === 0) return 'unknown'
  return entries.has(clean) ? 'exists' : 'missing'
}
