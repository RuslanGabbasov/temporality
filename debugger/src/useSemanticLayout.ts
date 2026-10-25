import { useEffect, useRef, useState } from 'react'
import {
  buildClusterTree,
  projectSemanticMap,
  semanticGraph,
  buildSemanticMap,
  type MapItem,
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

function corpusKey(items: MapItem[]): string {
  return `${items.length}:${items[0]?.id ?? ''}:${items[items.length - 1]?.id ?? ''}`
}

function getWorker(): Worker | null {
  if (sharedWorker) return sharedWorker
  try {
    sharedWorker = new SemanticWorker()
  } catch {
    sharedWorker = null
  }
  return sharedWorker
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

    let cancelled = false
    const mySeq = ++requestSeq
    setPending(true)

    const install = (value: SemanticMapResult) => {
      if (cancelled || mySeq !== requestSeq) return
      resultCache.set(key, value)
      setResult(value)
      setPending(false)
    }

    const worker = getWorker()
    if (worker) {
      const onMessage = (event: MessageEvent) => {
        const message = event.data as { kind: string } & Partial<SemanticMapResult>
        if (message.kind !== 'done') return
        worker.removeEventListener('message', onMessage)
        install({
          nodes: message.nodes ?? [],
          edges: message.edges ?? [],
          level1: message.level1 ?? [],
          level2: message.level2 ?? [],
          width: message.width ?? 1400,
          height: message.height ?? 900,
        })
      }
      const onError = () => {
        worker.removeEventListener('error', onError)
        install(computeSync(items))
      }
      worker.addEventListener('message', onMessage)
      worker.addEventListener('error', onError)
      worker.postMessage({ items, width: 1400, height: 900 })
      return () => { cancelled = true; worker.removeEventListener('message', onMessage); worker.removeEventListener('error', onError) }
    }
    // Workers unavailable: synchronous fallback (still cached).
    install(computeSync(items))
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
