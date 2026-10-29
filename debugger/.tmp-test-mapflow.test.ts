import { describe, it } from 'vitest'
import { buildSemanticMap, type MapItem } from './src/semanticMap'

function makeItems(n: number, seed: number): MapItem[] {
  const out: MapItem[] = []
  for (let i = 0; i < n; i++) {
    const id = `k-${seed}-${i}`
    out.push({
      id,
      project: 'proj',
      proposition: `Knowledge statement number ${i} about topic ${i % 12} with seed ${seed}`,
      state: i % 17 === 0 ? 'challenged' : 'active',
      topics: [`topic-${i % 12}`],
      vector: Array.from({ length: 64 }, (_, d) => Math.sin(i * 0.1 + d) + (seed * 0.01)),
      updated_at: new Date(2026, 0, 1 + (i % 28)).toISOString(),
    } as MapItem)
  }
  return out
}

it('progressive batches through buildSemanticMap without throwing', () => {
  const acc: MapItem[] = []
  const seen = new Set<string>()
  let total: number | null = null
  for (let offset = 0; (total === null || offset < total); offset += 400) {
    const page = makeItems(Math.min(400, 965 - offset), offset / 400 + 1)
    total = 965
    const fresh = page.filter((x) => !seen.has(x.id))
    for (const x of page) seen.add(x.id)
    acc.push(...fresh)
    if ((page ?? []).length === 0) break
    const result = buildSemanticMap(acc, { width: 1400, height: 900 })
    if (!result || !result.nodes || result.nodes.length === 0) throw new Error('empty layout at ' + acc.length)
    console.log('batch ok, items:', acc.length, 'nodes:', result.nodes.length)
    if (acc.length >= 965) break
  }
}, 120_000)
