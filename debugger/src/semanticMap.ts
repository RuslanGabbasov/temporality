// Semantic projection for the knowledge map (docs/human-knowledge-explorer.md
// §12): raw embedding vectors in, 2D layout out. The pipeline is the classic
// "poor man's UMAP": PCA to 2D for a deterministic starting layout, then a
// d3-force refinement over the k-nearest-neighbor graph built on cosine
// similarity — attraction only along "these mean the same thing" edges, so
// proximity on the map reads as semantic closeness, and topics tend to paint
// visible clusters without any clustering pass.
//
// Everything runs client-side: the journal ships raw vectors once, the
// browser does the math. Bounded at ~1500 items — cosine pairs and the force
// pass stay in the tens of milliseconds to low seconds range.

import { forceSimulation, forceLink, forceManyBody, forceX, forceY } from 'd3-force'

export interface MapItem {
  id: string
  project: string
  proposition: string
  state: string
  kind?: string
  topics?: string[]
  scope_kind?: string
  scope_id?: string
  updated_at?: string
  vector: number[]
}

export interface LayoutNode {
  id: string
  item: MapItem
  x: number
  y: number
  /** Index into the cluster palette: the item's dominant topic cluster. */
  cluster: number
}

export interface MapLayout {
  nodes: LayoutNode[]
  /** Deterministic width/height the nodes were laid out in. */
  width: number
  height: number
}

/** Cluster colors (text-friendly on dark and light). Index by cluster number;
 *  the palette is fixed so the same topic keeps its color across renders. */
export const CLUSTER_COLORS = [
  '#4f9cf9', // blue
  '#73daca', // teal
  '#e0af68', // amber
  '#bb9af7', // violet
  '#f7768e', // red
  '#9ece6a', // green
  '#e8c07d', // sand
  '#7aa2f7', // indigo
  '#ff9e64', // orange
  '#b4f9f8', // light cyan
]

export const STATE_COLORS: Record<string, string> = {
  proposed: '#7aa2f7',
  confirmed: '#9ece6a',
  challenged: '#e0af68',
  corrected: '#7dcfff',
  superseded: '#565f89',
  invalidated: '#f7768e',
  disproved: '#f7768e',
}

/** Deterministic PRNG (mulberry32): the same corpus always lays out the same
 *  way, so the map does not shuffle between visits. */
function rng(seed: number): () => number {
  let a = seed >>> 0
  return () => {
    a |= 0; a = (a + 0x6d2b79f5) | 0
    let t = Math.imul(a ^ (a >>> 15), 1 | a)
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296
  }
}

function hashStrings(values: string[]): number {
  let h = 2166136261
  for (const value of values) {
    for (let i = 0; i < value.length; i++) {
      h ^= value.charCodeAt(i)
      h = Math.imul(h, 16777619)
    }
  }
  return h >>> 0
}

/** Unit-normalize one vector; zero vectors stay zero (they get no kNN edges
 *  and settle by repulsion only). */
function normalize(vector: number[]): number[] {
  let sum = 0
  for (const value of vector) sum += value * value
  const norm = Math.sqrt(sum)
  if (norm === 0) return vector.map(() => 0)
  return vector.map((value) => value / norm)
}

/** PCA to 2D via two power iterations on the centered covariance — a far
 *  better starting layout than random, so the force pass converges quickly
 *  and deterministically. */
function pca2(vectors: number[][]): { x: number[]; y: number[] } {
  const n = vectors.length
  const dim = n > 0 ? vectors[0].length : 0
  const mean = new Array<number>(dim).fill(0)
  for (const vector of vectors) {
    for (let d = 0; d < dim; d++) mean[d] += vector[d] / n
  }
  const centered = vectors.map((vector) => vector.map((value, d) => value - mean[d]))
  const power = (): { x: number[]; y: number[] } => {
    // First component: repeated vector×matrix products.
    let v1 = new Array<number>(dim).fill(0).map((_, i) => Math.sin(i + 1))
    for (let iter = 0; iter < 8; iter++) {
      const next = new Array<number>(dim).fill(0)
      for (const row of centered) {
        const dot = row.reduce((acc, value, d) => acc + value * v1[d], 0)
        for (let d = 0; d < dim; d++) next[d] += dot * row[d]
      }
      const norm = Math.sqrt(next.reduce((acc, value) => acc + value * value, 0)) || 1
      v1 = next.map((value) => value / norm)
    }
    // Second component orthogonal to the first.
    let v2 = new Array<number>(dim).fill(0).map((_, i) => Math.cos(i + 2))
    for (let iter = 0; iter < 8; iter++) {
      const next = new Array<number>(dim).fill(0)
      for (const row of centered) {
        const dot = row.reduce((acc, value, d) => acc + value * v2[d], 0)
        for (let d = 0; d < dim; d++) next[d] += dot * row[d]
      }
      const proj = v2.reduce((acc, value, d) => acc + value * v1[d], 0)
      for (let d = 0; d < dim; d++) v2[d] -= proj * v1[d]
      const norm = Math.sqrt(v2.reduce((acc, value) => acc + value * value, 0)) || 1
      v2 = v2.map((value) => value / norm)
    }
    return {
      x: centered.map((row) => row.reduce((acc, value, d) => acc + value * v1[d], 0)),
      y: centered.map((row) => row.reduce((acc, value, d) => acc + value * v2[d], 0)),
    }
  }
  if (n === 0 || dim === 0) return { x: [], y: [] }
  return power()
}

/** k-nearest-neighbor edges over cosine similarity (vectors must be
 *  normalized). Returns [i, j, similarity] triples, each node ≤ k edges. */
export function knnEdges(vectors: number[][], k = 6): Array<[number, number, number]> {
  const edges: Array<[number, number, number]> = []
  const n = vectors.length
  for (let i = 0; i < n; i++) {
    const sims: Array<[number, number]> = []
    for (let j = 0; j < n; j++) {
      if (i === j) continue
      let dot = 0
      for (let d = 0; d < vectors[i].length; d++) dot += vectors[i][d] * vectors[j][d]
      sims.push([j, dot])
    }
    sims.sort((a, b) => b[1] - a[1])
    for (let e = 0; e < Math.min(k, sims.length); e++) {
      edges.push([i, sims[e][0], sims[e][1]])
    }
  }
  return edges
}

export interface SemanticCluster {
  /** Indices into the items array. */
  members: number[]
  /** The cluster's caption: its most frequent topic ('' when topic-less). */
  label: string
  count: number
}

/** The map's skeleton: kNN edges plus super-clusters — union-find over the
 *  strong edges (cosine ≥ strong). Clusters are what the far-zoom plane
 *  renders instead of hundreds of dots: one bubble per theme, a caption per
 *  bubble, detail appears as the user zooms in. */
export function semanticGraph(items: MapItem[], k = 8, strong = 0.6): {
  edges: Array<[number, number, number]>
  clusters: SemanticCluster[]
} {
  const normalized = items.map((item) => normalize(item.vector))
  const edges = knnEdges(normalized, k)
  const parent = items.map((_, i) => i)
  const find = (a: number): number => {
    while (parent[a] !== a) {
      parent[a] = parent[parent[a]]
      a = parent[a]
    }
    return a
  }
  for (const [a, b, sim] of edges) {
    if (sim < strong) continue
    const ra = find(a), rb = find(b)
    if (ra !== rb) parent[ra] = rb
  }
  const groups = new Map<number, number[]>()
  for (let i = 0; i < items.length; i++) {
    const root = find(i)
    const group = groups.get(root)
    if (group) group.push(i)
    else groups.set(root, [i])
  }
  const clusters: SemanticCluster[] = []
  for (const members of groups.values()) {
    const frequency = new Map<string, number>()
    for (const index of members) {
      for (const topic of items[index].topics ?? []) {
        const key = topic.trim()
        if (key) frequency.set(key, (frequency.get(key) ?? 0) + 1)
      }
    }
    let label = ''
    let bestCount = 1 // a caption needs at least two members sharing a topic
    for (const [topic, count] of frequency) {
      if (count > bestCount) {
        bestCount = count
        label = topic
      }
    }
    clusters.push({ members, label, count: members.length })
  }
  return { edges, clusters }
}

/** Topic clusters: every topic gets a stable palette index (by hash), an item
 *  takes the index of its rarest topic (rare topics group tighter than
 *  «общие» ones). Items without topics share the neutral cluster 0. */
export function clusterAssign(items: MapItem[]): number[] {
  const frequency = new Map<string, number>()
  for (const item of items) {
    for (const topic of item.topics ?? []) {
      const key = topic.trim().toLowerCase()
      if (key) frequency.set(key, (frequency.get(key) ?? 0) + 1)
    }
  }
  return items.map((item) => {
    let best = -1
    let bestCount = Number.POSITIVE_INFINITY
    for (const topic of item.topics ?? []) {
      const key = topic.trim().toLowerCase()
      if (!key) continue
      const count = frequency.get(key) ?? 0
      if (count < bestCount) {
        bestCount = count
        best = Math.abs(hashStrings([key])) % CLUSTER_COLORS.length
      }
    }
    return best >= 0 ? best : 0
  })
}

/** Build the full layout: normalize → PCA init → force refinement over the
 *  kNN similarity graph. `onTick` receives intermediate layouts so the UI can
 *  animate the settling instead of freezing. */
export function projectSemanticMap(
  items: MapItem[],
  options?: { k?: number; onTick?: (nodes: LayoutNode[]) => void; width?: number; height?: number },
): MapLayout {
  const width = options?.width ?? 1200
  const height = options?.height ?? 800
  const k = options?.k ?? 6
  const normalized = items.map((item) => normalize(item.vector))
  const pca = pca2(normalized)
  const random = rng(hashStrings(items.map((item) => item.id)))

  const nodes: LayoutNode[] = items.map((item, i) => ({
    id: item.id,
    item,
    cluster: 0,
    // PCA scatter scaled to the canvas; degenerate projections (all-equal
    // vectors collapse to a point) fall back to a deterministic jitter.
    x: width / 2 + (pca.x[i] ?? 0) * 8 + (random() - 0.5) * 4,
    y: height / 2 + (pca.y[i] ?? 0) * 8 + (random() - 0.5) * 4,
  }))
  const clusters = clusterAssign(items)
  clusters.forEach((cluster, i) => { nodes[i].cluster = cluster })

  const edges = knnEdges(normalized, k)
  const links = edges.map(([source, target, similarity]) => ({
    source, target,
    // Closer in meaning → shorter link. Cosine is 0..1 on normalized
    // non-negative embeddings; map the whole 0..1 band onto 18..130 px.
    distance: 18 + (1 - Math.max(0, Math.min(1, similarity))) * 112,
  }))

  const simulation = forceSimulation(nodes)
    .force('link', forceLink(links).distance((link: { distance: number }) => link.distance).strength(0.6))
    .force('charge', forceManyBody().strength(-90))
    .force('x', forceX(width / 2).strength(0.03))
    .force('y', forceY(height / 2).strength(0.03))
    .stop()

  // Deterministic pre-run: 180 ticks synchronously (fast for ≤1500 nodes),
  // then hand the result to the caller. An animated variant can pass onTick
  // and drive manual ticks from rAF instead.
  if (options?.onTick) {
    simulation.alpha(1).on('tick', () => options.onTick?.(nodes))
    for (let i = 0; i < 240; i++) simulation.tick()
    simulation.stop()
  } else {
    for (let i = 0; i < 240; i++) simulation.tick()
  }
  return { nodes, width, height }
}
