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

/** Runs the heavy semantic-map pipeline off the main thread. Falls back to
 *  synchronous computation when workers are unavailable (very old browsers,
 *  some test environments) — the map must render, just slower. */
export function useSemanticLayout(items: MapItem[] | null): SemanticMapResult | null {
  const [result, setResult] = useState<SemanticMapResult | null>(null)
  const workerRef = useRef<Worker | null>(null)

  useEffect(() => {
    if (!items) { setResult(null); return }
    if (items.length === 0) { setResult(null); return }

    let cancelled = false
    let worker: Worker | null = null
    try {
      worker = new SemanticWorker()
      workerRef.current = worker
      worker.onmessage = (event: MessageEvent) => {
        const message = event.data as { kind: string; nodes?: SemanticMapResult['nodes'] } & Partial<SemanticMapResult>
        if (message.kind !== 'done' || cancelled) return
        setResult({
          nodes: message.nodes ?? [],
          edges: message.edges ?? [],
          level1: message.level1 ?? [],
          level2: message.level2 ?? [],
          width: message.width ?? 1400,
          height: message.height ?? 900,
        })
      }
      worker.onerror = () => {
        // Worker boot failed (CSP, old engine): synchronous fallback.
        if (cancelled) return
        workerRef.current = null
        const layout = projectSemanticMap(items, { width: 1400, height: 900 })
        const graph = semanticGraph(items)
        const tree = buildClusterTree(items, layout.nodes)
        setResult({ ...layout, edges: graph.edges, level1: tree.level1, level2: tree.level2 })
      }
      worker.postMessage({ items, width: 1400, height: 900 })
    } catch {
      // Workers unavailable: same synchronous fallback.
      const layout = projectSemanticMap(items, { width: 1400, height: 900 })
      const graph = semanticGraph(items)
      const tree = buildClusterTree(items, layout.nodes)
      setResult({ ...layout, edges: graph.edges, level1: tree.level1, level2: tree.level2 })
    }

    return () => {
      cancelled = true
      worker?.terminate()
      workerRef.current = null
    }
  }, [items])

  return result
}
