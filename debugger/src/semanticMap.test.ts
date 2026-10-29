import { describe, expect, it } from 'vitest'
import { buildClusterTree, buildSemanticMap, clusterAssign, knnEdges, knnEdgesFlat, projectSemanticMap, type MapItem } from './semanticMap'

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

describe('buildClusterTree', () => {
  const items: MapItem[] = Array.from({ length: 40 }, (_, i) => ({
    id: `k${i}`,
    project: 'p',
    proposition: `statement ${i}`,
    state: 'confirmed',
    topics: [i % 2 === 0 ? 'frontend' : 'backend'],
    // Two tight blobs far apart: even indices near (10,10), odd near (90,90).
    vector: i % 2 === 0 ? [1, 0.05, 0] : [0, 0.05, 1],
  }))
  const { nodes } = projectSemanticMap(items, { width: 800, height: 600 })

  it('builds two pyramid levels with captions and counts', () => {
    const tree = buildClusterTree(items, nodes)
    expect(tree.level1.length).toBeGreaterThanOrEqual(2)
    expect(tree.level2.length).toBeGreaterThanOrEqual(tree.level1.length)
    const total = tree.level1.reduce((acc, node) => acc + node.count, 0)
    expect(total).toBe(items.length)
    for (const node of tree.level1) {
      if (node.count >= 2) expect(typeof node.label).toBe('string')
    }
  })

  it('covers every point exactly once at level 2', () => {
    const tree = buildClusterTree(items, nodes)
    const seen = new Set<number>()
    for (const child of tree.level2) {
      for (const member of child.members ?? []) {
        expect(seen.has(member)).toBe(false)
        seen.add(member)
      }
    }
    expect(seen.size).toBe(items.length)
  })

  it('stamps every point with its region index so a region paints one hue', () => {
    const tree = buildClusterTree(items, nodes)
    // Every point's cluster points at an existing region, and that region
    // carries its own color — the renderer paints a dot in the same shade as
    // its bubble and legend entry.
    for (const [index, node] of nodes.entries()) {
      const region = tree.level1[node.cluster]
      expect(region).toBeDefined()
      expect(region.color).toMatch(/^#/)
      expect(node.item.id).toBe(items[index].id)
    }
  })
})

describe('knnEdgesFlat', () => {
  it('matches knnEdges on the same vectors', () => {
    const vectors = [[1, 0], [0.9, 0.1], [0.95, 0.05], [0, 1], [0.05, 0.95]]
    const n = vectors.length, dim = 2
    const flat = new Float32Array(n * dim)
    vectors.forEach((v, i) => { flat.set(v, i * dim) })
    const fast = knnEdgesFlat(flat, n, dim, 2)
    const reference = knnEdges(vectors, 2)
    // Same source nodes and neighbors, order-insensitive per node.
    const key = (e: [number, number, number]) => `${Math.min(e[0], e[1])}->${Math.max(e[0], e[1])}`
    const fastSet = new Set(fast.map(key))
    for (const e of reference) expect(fastSet.has(key(e))).toBe(true)
  })

  it('caps each source at k edges', () => {
    const vectors = [[1, 0], [0.9, 0.1], [0.8, 0.2], [0.7, 0.3], [0, 1]]
    const flat = new Float32Array(vectors.flat())
    const edges = knnEdgesFlat(flat, vectors.length, 2, 2)
    const out = new Map<number, number>()
    for (const [a] of edges) out.set(a, (out.get(a) ?? 0) + 1)
    for (let i = 0; i < vectors.length; i++) expect(out.get(i)).toBe(2)
  })
})

describe('buildSemanticMap', () => {
  const items: MapItem[] = Array.from({ length: 30 }, (_, i) => ({
    id: `k${i}`, project: 'p', proposition: `statement ${i}`, state: 'confirmed',
    topics: [i % 2 === 0 ? 'frontend' : 'backend'],
    vector: i % 2 === 0 ? [1, 0.05, 0] : [0, 0.05, 1],
  }))

  it('produces a consistent pyramid and edges in one pass', () => {
    const result = buildSemanticMap(items, { width: 800, height: 600 })
    expect(result.nodes).toHaveLength(items.length)
    expect(result.level1.length).toBeGreaterThanOrEqual(2)
    expect(result.level2.length).toBeGreaterThanOrEqual(result.level1.length)
    // Every edge references valid node indices.
    for (const [a, b] of result.edges) {
      expect(a).toBeLessThan(items.length)
      expect(b).toBeLessThan(items.length)
    }
  })

  it('is deterministic', () => {
    const first = buildSemanticMap(items, { width: 800, height: 600 })
    const second = buildSemanticMap(items, { width: 800, height: 600 })
    expect(first.nodes.map((n) => [Math.round(n.x), Math.round(n.y)])).toEqual(
      second.nodes.map((n) => [Math.round(n.x), Math.round(n.y)]))
  })

  it('incremental build keeps placed items near where they were', () => {
    const first = buildSemanticMap(items, { width: 800, height: 600 })
    const positions = new Map(first.nodes.map((n) => [n.id, { x: n.x, y: n.y }]))
    const next = buildSemanticMap(items, { width: 800, height: 600, previous: { positions, basis: first.basis } })
    let maxShift = 0
    for (const node of next.nodes) {
      const prev = positions.get(node.id)!
      maxShift = Math.max(maxShift, Math.hypot(node.x - prev.x, node.y - prev.y))
    }
    // The anchor pass keeps placed items within a local adjustment — not the
    // whole-plane migration a fresh re-fit produces.
    expect(maxShift).toBeLessThan(60)
  })

  it('incremental build seats newcomers next to their placed neighbors', () => {
    const first = buildSemanticMap(items, { width: 800, height: 600 })
    const positions = new Map(first.nodes.map((n) => [n.id, { x: n.x, y: n.y }]))
    const newcomer: MapItem = { id: 'new', project: 'p', proposition: 'new statement', state: 'confirmed', topics: ['frontend'], vector: [0.98, 0.1, 0] }
    const next = buildSemanticMap([newcomer, ...items], { width: 800, height: 600, previous: { positions, basis: first.basis } })
    const seeded = next.nodes.find((n) => n.id === 'new')!
    // Its nearest placed item is the frontend cluster; the newcomer must
    // settle inside that half of the canvas, not at a fresh PCA seed.
    const placedX = [...positions.values()].reduce((sum, p) => sum + p.x, 0) / positions.size
    const frontend = first.nodes.filter((n) => n.item.topics![0] === 'frontend')
    const clusterX = frontend.reduce((sum, n) => sum + n.x, 0) / frontend.length
    expect(Math.abs(seeded.x - clusterX)).toBeLessThan(Math.abs(placedX - clusterX) + 200)
  })

  it('a different corpus ignores the previous layout (project flip)', () => {
    const other: MapItem[] = Array.from({ length: 30 }, (_, i) => ({
      id: `other${i}`, project: 'q', proposition: `other ${i}`, state: 'confirmed',
      topics: ['x'], vector: [Math.cos(i), Math.sin(i), 0.1],
    }))
    const first = buildSemanticMap(items, { width: 800, height: 600 })
    const positions = new Map(first.nodes.map((n) => [n.id, { x: n.x, y: n.y }]))
    // Nothing matches: must fall back to a fresh build without throwing.
    const next = buildSemanticMap(other, { width: 800, height: 600, previous: { positions, basis: first.basis } })
    expect(next.nodes).toHaveLength(30)
    expect(next.basis).toBeDefined()
  })
})
