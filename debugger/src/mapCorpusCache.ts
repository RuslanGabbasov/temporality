// IndexedDB corpus cache for the semantic map (docs/human-knowledge-explorer.md
// §12): per-project point sets with their vectors, so a cold mount paints
// instantly and only the delta crosses the network. localStorage holds the
// tiny version stamp per project (count, max updated_at, state counts — a few
// hundred bytes); the points live here (structured-clone Float32Array
// vectors — no base64, no JSON float bloat).
//
// Size math: a point is ~vector (768×4B = 3 KB) + a few hundred bytes of
// meta. 10k points ≈ 35 MB, 100k ≈ 350 MB — both inside IndexedDB quotas
// (hundreds of MB to GB per origin), far beyond localStorage's 5–10 MB.

import type { MapItem } from './semanticMap'

const DB_NAME = 'temporality-map'
const DB_VERSION = 1
const STORE = 'points'
const META_KEY = 'temporality_map_meta'

export interface ProjectMeta {
  count: number
  maxUpdatedAt: string
  states: Record<string, number>
  knowledgeTotal: number
  bytes: number
  at: number
}

interface CachedPoint {
  pid: string
  project: string
  item: Omit<MapItem, 'vector'>
  vector: Float32Array
}

function metaAll(): Record<string, ProjectMeta> {
  try {
    return JSON.parse(localStorage.getItem(META_KEY) ?? '{}') as Record<string, ProjectMeta>
  } catch {
    return {}
  }
}

function saveMeta(all: Record<string, ProjectMeta>) {
  try {
    localStorage.setItem(META_KEY, JSON.stringify(all))
  } catch { /* quota — the IDB cache still works, only instant-hydrate is lost */ }
}

export function projectMeta(project: string): ProjectMeta | null {
  return metaAll()[project] ?? null
}

function setProjectMeta(project: string, meta: ProjectMeta | null) {
  const all = metaAll()
  if (meta) all[project] = meta
  else delete all[project]
  saveMeta(all)
}

function open(): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    const req = indexedDB.open(DB_NAME, DB_VERSION)
    req.onupgradeneeded = () => {
      const db = req.result
      if (!db.objectStoreNames.contains(STORE)) {
        const store = db.createObjectStore(STORE, { keyPath: 'pid' })
        store.createIndex('byProject', 'project')
      }
    }
    req.onsuccess = () => resolve(req.result)
    req.onerror = () => reject(req.error)
  })
}

function requestAs<T>(req: IDBRequest<T>): Promise<T> {
  return new Promise((resolve, reject) => {
    req.onsuccess = () => resolve(req.result)
    req.onerror = () => reject(req.error)
  })
}

/** Estimated per-project bytes: vector bytes + a prose/meta allowance. */
function estimateBytes(dim: number, count: number): number {
  return count * (dim * 4 + 400)
}

/** Whole-cache budget: 200 MB. Over it, least-recently-used projects are
 *  dropped (the current project is always kept). */
const CACHE_BUDGET_BYTES = 200 * 1024 * 1024

async function evictToBudget(keepProject: string) {
  const all = metaAll()
  let total = Object.values(all).reduce((sum, m) => sum + m.bytes, 0)
  if (total <= CACHE_BUDGET_BYTES) return
  const lru = Object.entries(all)
    .filter(([project]) => project !== keepProject)
    .sort((a, b) => a[1].at - b[1].at)
  for (const [project, meta] of lru) {
    await dropProject(project)
    total -= meta.bytes
    if (total <= CACHE_BUDGET_BYTES) return
  }
}

let dbPromise: Promise<IDBDatabase> | null = null
function db(): Promise<IDBDatabase> {
  if (!dbPromise) dbPromise = open()
  return dbPromise
}

const idbAvailable = typeof indexedDB !== 'undefined'

/** All cached points of one project, newest-meta first. Null = no usable
 *  cache (missing, empty, or IndexedDB unavailable). */
export async function loadProject(project: string): Promise<{ items: MapItem[]; meta: ProjectMeta } | null> {
  if (!idbAvailable) return null
  try {
    const database = await db()
    const store = database.transaction(STORE, 'readonly').objectStore(STORE)
    const index = store.index('byProject')
    const rows = await requestAs(index.getAll(project) as IDBRequest<CachedPoint[]>)
    if (!rows.length) return null
    const items = rows.map((row) => ({ ...row.item, vector: Array.from(row.vector) }))
    return { items, meta: projectMeta(project) ?? { count: rows.length, maxUpdatedAt: '', states: {}, knowledgeTotal: rows.length, bytes: 0, at: 0 } }
  } catch {
    return null
  }
}

/** Replace a project's cached corpus wholesale and stamp the meta. */
export async function saveProject(project: string, items: MapItem[], extra: { states: Record<string, number>; knowledgeTotal: number }) {
  if (!idbAvailable || items.length === 0) return
  try {
    const database = await db()
    const dim = items[0]?.vector.length ?? 0
    await new Promise<void>((resolve, reject) => {
      const tx = database.transaction(STORE, 'readwrite')
      const store = tx.objectStore(STORE)
      const index = store.index('byProject')
      const cursorReq = index.openCursor(IDBKeyRange.only(project))
      cursorReq.onsuccess = () => {
        const cursor = cursorReq.result
        if (cursor) { cursor.delete(); cursor.continue() }
      }
      tx.oncomplete = () => resolve()
      tx.onerror = () => reject(tx.error)
      void store
    })
    await new Promise<void>((resolve, reject) => {
      const tx = database.transaction(STORE, 'readwrite')
      const store = tx.objectStore(STORE)
      for (const item of items) {
        const { vector, ...rest } = item
        store.put({ pid: `${project}\u001f${item.id}`, project, item: rest, vector: Float32Array.from(vector) } satisfies CachedPoint)
      }
      tx.oncomplete = () => resolve()
      tx.onerror = () => reject(tx.error)
    })
    const maxUpdatedAt = items.reduce((max, item) => (item.updated_at && item.updated_at > max ? item.updated_at : max), '')
    setProjectMeta(project, {
      count: items.length,
      maxUpdatedAt,
      states: extra.states,
      knowledgeTotal: extra.knowledgeTotal,
      bytes: estimateBytes(dim, items.length),
      at: Date.now(),
    })
    await evictToBudget(project)
  } catch { /* cache write failures are silent — the network path still works */ }
}

export async function dropProject(project: string) {
  if (!idbAvailable) return
  try {
    const database = await db()
    await new Promise<void>((resolve, reject) => {
      const tx = database.transaction(STORE, 'readwrite')
      const index = tx.objectStore(STORE).index('byProject')
      const req = index.openCursor(IDBKeyRange.only(project))
      req.onsuccess = () => {
        const cursor = req.result
        if (cursor) { cursor.delete(); cursor.continue() }
      }
      tx.oncomplete = () => resolve()
      tx.onerror = () => reject(tx.error)
    })
    setProjectMeta(project, null)
  } catch { /* best-effort */ }
}

/** Ask the browser to keep the cache across storage pressure. Best-effort —
 *  denied persistence just means the browser may evict under pressure. */
export function requestPersistence() {
  try { void navigator.storage?.persist?.() } catch { /* unsupported */ }
}
