// Semantic map worker: the layout pipeline (normalize → kNN → force →
// cluster pyramid) is CPU-bound for seconds at a few thousand items — it
// runs here so the map tab never freezes. One message in, one message out;
// a progress ping goes out per pipeline stage so the UI can show what is
// happening instead of a dead spinner.
import { buildSemanticMap, type MapItem, type SemanticMapResult } from './semanticMap'

export interface SemanticMapRequest {
  items: MapItem[]
  width: number
  height: number
}

export type SemanticMapResponse =
  | { kind: 'progress'; stage: string }
  | ({ kind: 'done' } & SemanticMapResult)

self.addEventListener('message', (event: MessageEvent<SemanticMapRequest>) => {
  const { items, width, height } = event.data
  const post = (message: SemanticMapResponse) => (self as unknown as Worker).postMessage(message)
  post({ kind: 'progress', stage: 'knn' })
  const result: SemanticMapResult = buildSemanticMap(items, {
    width, height,
  })
  post({ kind: 'progress', stage: 'layout' })
  post({ kind: 'done', ...result })
})
