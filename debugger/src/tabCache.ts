// Session tab cache: a module-level map keyed by surface+scope, mirrored
// into sessionStorage so a reload (F5) keeps the last view too.
//
// Why: pages remount on every tab switch (Workspace ↔ Runs, Dashboard tabs)
// and each mount started from a blank screen — a 3–5s fetch for data the
// user saw seconds ago. Surfaces hydrate from here on mount and refresh
// over the network in the background: the previous state paints instantly,
// the fetch only updates numbers in place. This is display state, not a
// source of truth — nothing reads the cache for actions, only for painting.
//
// sessionStorage, not localStorage: the cached slices are volatile
// operational state (run lists, dashboard rollups, saga budgets), stale
// after a kernel restart anyway — per-tab lifetime matches that, and a
// browser wipe clears it for free. Writes are best-effort: a quota error
// disables only the persistence mirror, the in-memory map keeps working.

const PREFIX = 'temporality-tab-cache'

const memory = new Map<string, { at: number; value: unknown }>()

function storageKey(key: string) { return `${PREFIX}:${key}` }

/** Last cached value for `key`, or null. Reads hit the in-memory map after
 *  the first lookup; sessionStorage is only touched on a cold module. */
export function cacheGet<T>(key: string): T | null {
  const hit = memory.get(key)
  if (hit) return hit.value as T
  try {
    const raw = sessionStorage.getItem(storageKey(key))
    if (!raw) return null
    const parsed = JSON.parse(raw) as { at: number; value: unknown }
    memory.set(key, parsed)
    return parsed.value as T
  } catch { return null }
}

/** Stamp `key` with `value` and the current time. Never throws. */
export function cacheSet(key: string, value: unknown) {
  const entry = { at: Date.now(), value }
  memory.set(key, entry)
  try { sessionStorage.setItem(storageKey(key), JSON.stringify(entry)) } catch { /* quota: the memory map still serves this tab */ }
}
