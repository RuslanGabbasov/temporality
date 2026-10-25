import { describe, expect, it } from 'vitest'
import { clusterAssign, knnEdges, projectSemanticMap, type MapItem } from './semanticMap'

function item(id: string, vector: number[], topics: string[] = []): MapItem {
  return { id, project: 'p', proposition: `statement ${id}`, state: 'confirmed', topics, vector }
}

describe('knnEdges', () => {
  it('links each point to its nearest neighbors by cosine similarity', () => {
    const vectors = [
      [1, 0], [0.9, 0.1], [0.95, 0.05], // one tight trio
      [0, 1], [0.05, 0.95],             // another pair
    ]
    const edges = knnEdges(vectors, 2)
    // Each node emits exactly k edges (out-degree); incoming edges may add
    // more, so only the source count is contractual.
    const outDegree = new Map<number, number>()
    for (const [a] of edges) outDegree.set(a, (outDegree.get(a) ?? 0) + 1)
    for (let i = 0; i < vectors.length; i++) {
      expect(outDegree.get(i)).toBe(2)
    }
    // 0 and 2 must be connected (near-identical directions).
    expect(edges.some(([a, b]) => (a === 0 && b === 2) || (a === 2 && b === 0))).toBe(true)
    // The unrelated pair (0 vs 3) must not be anyone's nearest choice.
    expect(edges.some(([a, b]) => (a === 0 && b === 3) || (a === 3 && b === 0))).toBe(false)
  })
})

describe('clusterAssign', () => {
  it('prefers the rarest topic so common words do not paint everything', () => {
    const items = [
      item('a', [1, 0], ['deploy', 'общее']),
      item('b', [1, 0], ['deploy', 'общее']),
      item('c', [1, 0], ['deploy']),
    ]
    const clusters = clusterAssign(items)
    // «deploy» is rare (1 use) vs «общее» (3): items carrying it take its
    // cluster, so a and b share one, c — another.
    expect(clusters[0]).toBe(clusters[1])
    expect(clusters[2]).not.toBe(clusters[0])
  })

  it('topic-less items fall into the neutral cluster', () => {
    expect(clusterAssign([item('a', [1]), item('b', [0, 1])])).toEqual([0, 0])
  })
})

describe('projectSemanticMap', () => {
  it('is deterministic for the same corpus', () => {
    const items = [
      item('a', [1, 0.1, 0]),
      item('b', [0.95, 0.15, 0]),
      item('c', [0, 1, 0.1]),
      item('d', [0.05, 0.9, 0.05]),
    ]
    const first = projectSemanticMap(items, { width: 800, height: 600 })
    const second = projectSemanticMap(items, { width: 800, height: 600 })
    expect(first.nodes.map((node) => [Math.round(node.x), Math.round(node.y)])).toEqual(
      second.nodes.map((node) => [Math.round(node.x), Math.round(node.y)]))
  })

  it('puts semantically close items closer than distant ones', () => {
    const items = [
      item('near-a', [1, 0.05, 0]),
      item('near-b', [0.98, 0.1, 0]),
      item('far', [0, 0.05, 1]),
    ]
    const { nodes } = projectSemanticMap(items, { width: 800, height: 600 })
    const byId = new Map(nodes.map((node) => [node.id, node]))
    const dist = (a: string, b: string) => {
      const pa = byId.get(a)!, pb = byId.get(b)!
      return Math.hypot(pa.x - pb.x, pa.y - pb.y)
    }
    expect(dist('near-a', 'near-b')).toBeLessThan(dist('near-a', 'far'))
  })

  it('keeps zero vectors on the map without breaking the layout', () => {
    const items = [item('zero', [0, 0, 0]), item('real', [1, 0, 0])]
    const { nodes } = projectSemanticMap(items, { width: 600, height: 400 })
    expect(nodes).toHaveLength(2)
    expect(nodes.every((node) => Number.isFinite(node.x) && Number.isFinite(node.y))).toBe(true)
  })
})
