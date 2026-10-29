// Semantic map worker: the layout pipeline (normalize → kNN → force →
// cluster pyramid) is CPU-bound for seconds at a few thousand items — it
// runs here so the map tab never freezes. One message in, one message out;
// a progress ping goes out per pipeline stage so the UI can show what is
// happening instead of a dead spinner.
import { buildSemanticMap, type MapItem, type ProjectionBasis, type SemanticMapResult } from './semanticMap'

export interface SemanticMapRequest {
  items: MapItem[]
  width: number
  height: number
  /** Incremental mode: positions from the previous build + the projection
   *  axes they live in. Structured-clone friendly (arrays, not Maps). */
  previous?: { positions: Array<[string, number, number]>; basis?: ProjectionBasis }
}

export type SemanticMapResponse =
  | { kind: 'progress'; stage: string }
  | ({ kind: 'done' } & SemanticMapResult)

self.addEventListener('message', (event: MessageEvent<SemanticMapRequest>) => {
  const { items, width, height, previous } = event.data
  const post = (message: SemanticMapResponse) => (self as unknown as Worker).postMessage(message)
  post({ kind: 'progress', stage: 'knn' })
  const result: SemanticMapResult = buildSemanticMap(items, {
    width, height,
    previous: previous
      ? { positions: new Map(previous.positions.map(([id, x, y]) => [id, { x, y }])), basis: previous.basis }
      : undefined,
  })
  post({ kind: 'progress', stage: 'layout' })
  post({ kind: 'done', ...result })
})
