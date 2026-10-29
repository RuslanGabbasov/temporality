import { useEffect, useRef, useState } from 'react'
import {
  buildClusterTree,
  projectSemanticMap,
  semanticGraph,
  buildSemanticMap,
  type MapItem,
  type ProjectionBasis,
  type SemanticMapResult,
} from './semanticMap'
import SemanticWorker from './semanticWorker?worker'

/** Module-level worker + result cache: the map unmounts on every dossier
 *  open/close, and re-spawning a worker plus recomputing the whole layout
 *  per mount (then re-fetching) made every navigation cost seconds and a
 *  child-process spawn each time. One worker for the session, results keyed
 *  by corpus hash. */
let sharedWorker: Worker | null = null
let requestSeq = 0
const resultCache = new Map<string, SemanticMapResult>()

/** Incremental state: where the last answered build placed each item, and the
 *  projection axes that corpus lives in. Fed into the next build so the map
 *  grows in place instead of re-fitting the whole plane per batch. */
let incremental: { positions: Array<[string, number, number]>; basis?: ProjectionBasis } | null = null

/** Worker pipeline discipline: batches stream faster than the worker
 *  answers, and a build started WITHOUT the previous layout re-fits PCA and
 *  rotates the whole plane (the migrating-herd bug). So at most one build is
 *  in flight; a build request arriving meanwhile is queued and posted only
 *  when the worker answers — already carrying the fresh incremental state.
 *  Waiters are keyed by the hook effect's seq: only the LATEST waiter is
 *  delivered (older corpora are superseded), the result cache still serves
 *  repeated mounts. */
let inFlight = false
let queued: { items: MapItem[]; width: number; height: number; seq: number } | null = null
const waiters = new Map<number, (value: SemanticMapResult) => void>()

function corpusKey(items: MapItem[]): string {
  return `${items.length}:${items[0]?.id ?? ''}:${items[items.length - 1]?.id ?? ''}`
}

function handleAnswer(message: { kind: string } & Partial<SemanticMapResult>) {
  if (message.kind !== 'done') return
  inFlight = false
  incremental = {
    positions: (message.nodes ?? []).map((n) => [n.id, n.x, n.y]),
    basis: message.basis,
  }
  const result: SemanticMapResult = {
    nodes: message.nodes ?? [],
    edges: message.edges ?? [],
    level1: message.level1 ?? [],
    level2: message.level2 ?? [],
    width: message.width ?? 1400,
    height: message.height ?? 900,
    basis: message.basis,
  }
  // Deliver the latest waiter only — superseded seqs are stale corpora.
  const latest = Math.max(...waiters.keys())
  const deliver = waiters.get(latest)
  waiters.delete(latest)
  waiters.clear()
  deliver?.(result)
  // The queued batch builds on TOP of the answer we just got.
  if (queued) {
    const next = queued
    queued = null
    startBuild(next.items, next.width, next.height, next.seq)
  }
}

function getWorker(): Worker | null {
  if (sharedWorker) return sharedWorker
  try {
    sharedWorker = new SemanticWorker()
    sharedWorker.addEventListener('message', (event) => handleAnswer(event.data as { kind: string } & Partial<SemanticMapResult>))
    sharedWorker.addEventListener('error', () => {
      // Worker died: fail every waiter over to the sync fallback.
      inFlight = false
      const pending = [...waiters.values()]
      waiters.clear()
      for (const deliver of pending) {
        // The items are not at hand here; the waiter keeps them.
        deliver?.(FALLBACK_FAILED)
      }
      void queued
      queued = null
    })
  } catch {
    sharedWorker = null
  }
  return sharedWorker
}

/** Sentinel for the worker-error path: waiters resolve through their own
 *  fallback (computeSync), so this value never reaches the UI. */
const FALLBACK_FAILED = { nodes: [], edges: [], level1: [], level2: [], width: 0, height: 0 } as unknown as SemanticMapResult

function startBuild(items: MapItem[], width: number, height: number, seq: number) {
  const worker = getWorker()
  if (!worker) {
    // Workers unavailable: synchronous fallback (still cached via install).
    waiters.get(seq)?.(computeSync(items))
    waiters.delete(seq)
    return
  }
  inFlight = true
  worker.postMessage({ items, width, height, previous: incremental ?? undefined })
}

function computeSync(items: MapItem[]): SemanticMapResult {
  const layout = projectSemanticMap(items, { width: 1400, height: 900 })
  const graph = semanticGraph(items)
  const tree = buildClusterTree(items, layout.nodes)
  return { ...layout, edges: graph.edges, level1: tree.level1, level2: tree.level2 }
}

/** Runs the heavy semantic-map pipeline (worker first, sync fallback), and
 *  caches: the same corpus resolves instantly across tab switches and
 *  dossier open/close. */
export function useSemanticLayout(items: MapItem[] | null): SemanticMapResult | null {
  const [result, setResult] = useState<SemanticMapResult | null>(null)
  const [pending, setPending] = useState(false)
  const seqRef = useRef(0)

  useEffect(() => {
    if (!items) { setResult(null); setPending(false); return }
    if (items.length === 0) { setResult(null); setPending(false); return }

    const key = corpusKey(items)
    const cached = resultCache.get(key)
    if (cached) { setResult(cached); setPending(false); return }

    const mySeq = ++requestSeq
    setPending(true)
    const install = (value: SemanticMapResult) => {
      if (cancelled || mySeq !== requestSeq) return
      resultCache.set(key, value)
      // Keep the cache small: streaming produces a fresh corpus key per
      // batch, and stale intermediate layouts are dead weight.
      while (resultCache.size > 3) {
        const oldest = resultCache.keys().next().value
        if (oldest === undefined) break
        resultCache.delete(oldest)
      }
      setResult(value)
      setPending(false)
    }
    let cancelled = false

    if (inFlight) {
      // A build is running; queue this corpus. It supersedes anything queued
      // (corpora only grow during a stream) and will be posted — with the
      // incremental state of the in-flight answer — as soon as it lands.
      queued = { items, width: 1400, height: 900, seq: mySeq }
      waiters.set(mySeq, install)
    } else {
      waiters.set(mySeq, install)
      startBuild(items, 1400, 900, mySeq)
    }
    return () => { cancelled = true }
  }, [items])

  return { result, pending }.result
}

export interface SemanticLayoutState {
  result: SemanticMapResult | null
  pending: boolean
}

/** Overload keeping the pending flag available to callers that show it. */
export function useSemanticLayoutState(items: MapItem[] | null): { result: SemanticMapResult | null; pending: boolean } {
  const result = useSemanticLayout(items)
  return { result, pending: result === null }
}
