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
import { captionLines, dominantTopic, topicColor } from './knowledgeTaxonomy'

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
  /** Topic palette index after clusterAssign; after buildClusterTree — the
   *  index into the returned level1 regions the point belongs to, so the
   *  renderer can paint a dot in its region's exact shade. */
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
export interface ProjectionBasis {
  mean: number[]
  v1: number[]
  v2: number[]
}

/** Compute the projection axes for a corpus. Called ONCE per stream (first
 *  batch) and then reused: re-fitting the axes per batch would rotate the
 *  whole plane between batches and every dot would migrate with it. */
export function projectionBasis(vectors: number[][]): ProjectionBasis {
  const n = vectors.length
  const dim = n > 0 ? vectors[0].length : 0
  const mean = new Array<number>(dim).fill(0)
  for (const vector of vectors) {
    for (let d = 0; d < dim; d++) mean[d] += vector[d] / n
  }
  const centered = vectors.map((vector) => vector.map((value, d) => value - mean[d]))
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
  return { mean, v1, v2 }
}

/** Project vectors onto a FIXED basis (centered by its own mean). */
function projectWith(vectors: number[][], basis: ProjectionBasis): { x: number[]; y: number[] } {
  const { mean, v1, v2 } = basis
  const x: number[] = []
  const y: number[] = []
  for (const vector of vectors) {
    let dx = 0, dy = 0
    for (let d = 0; d < mean.length; d++) {
      const centered = vector[d] - mean[d]
      dx += centered * v1[d]
      dy += centered * v2[d]
    }
    x.push(dx)
    y.push(dy)
  }
  return { x, y }
}

function pca2(vectors: number[][]): { x: number[]; y: number[] } {
  const n = vectors.length
  const dim = n > 0 ? vectors[0].length : 0
  if (n === 0 || dim === 0) return { x: [], y: [] }
  const basis = projectionBasis(vectors)
  return projectWith(vectors, basis)
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

/** k-means-lite over 2D layout coordinates: cheap (points are already
 *  projected), deterministic with a fixed seed, good enough to carve the
 *  plane into themed regions the map can render per zoom tier. */
function kmeans2d(points: Array<{ x: number; y: number }>, k: number, iterations = 10): number[] {
  const random = rng(points.length * 7919 + k)
  // Seed centers by the k-means++ idea: spread the first picks out.
  const centers: Array<{ x: number; y: number }> = []
  let minDist = new Array<number>(points.length).fill(Number.POSITIVE_INFINITY)
  for (let c = 0; c < k; c++) {
    let pick: { x: number; y: number }
    if (c === 0) {
      pick = points[Math.floor(random() * points.length)]
    } else {
      let total = minDist.reduce((acc, value) => acc + value, 0)
      let roll = random() * total
      pick = points[points.length - 1]
      for (let i = 0; i < points.length; i++) {
        roll -= minDist[i]
        if (roll <= 0) { pick = points[i]; break }
      }
    }
    centers.push({ x: pick.x, y: pick.y })
    for (let i = 0; i < points.length; i++) {
      const dx = points[i].x - pick.x, dy = points[i].y - pick.y
      minDist[i] = Math.min(minDist[i], dx * dx + dy * dy)
    }
  }
  const assign = new Array<number>(points.length).fill(0)
  for (let iter = 0; iter < iterations; iter++) {
    for (let i = 0; i < points.length; i++) {
      let best = 0, bestDist = Number.POSITIVE_INFINITY
      for (let c = 0; c < centers.length; c++) {
        const dx = points[i].x - centers[c].x, dy = points[i].y - centers[c].y
        const dist = dx * dx + dy * dy
        if (dist < bestDist) { bestDist = dist; best = c }
      }
      assign[i] = best
    }
    for (let c = 0; c < centers.length; c++) {
      let sx = 0, sy = 0, count = 0
      for (let i = 0; i < points.length; i++) {
        if (assign[i] !== c) continue
        sx += points[i].x; sy += points[i].y; count++
      }
      if (count > 0) { centers[c] = { x: sx / count, y: sy / count } }
    }
  }
  return assign
}

export interface ClusterTreeNode {
  label: string
  /** Up to two caption lines (two most distinctive topics); the canvas
   *  renders the count per line. Falls back to [label]. */
  labelLines: string[]
  count: number
  /** Centroid and covering radius in layout coordinates. */
  cx: number
  cy: number
  radius: number
  color: string
  /** Level-2 nodes only: their points. */
  members?: number[]
  children?: ClusterTreeNode[]
}

function captionFor(items: MapItem[], memberIndices: number[]): string {
  return dominantTopic(memberIndices.map((index) => items[index].topics))
}

function captionLinesFor(items: MapItem[], memberIndices: number[]): string[] {
  return captionLines(memberIndices.map((index) => items[index].topics))
}

/** The map's zoom pyramid (docs/human-knowledge-explorer.md §12):
 *  level 1 — a few dozen broad regions, level 2 — themed sub-clusters inside
 *  each region, level 3 — the individual statements. Each node carries its
 *  centroid, covering radius, caption (most frequent topic) and palette
 *  color, so every zoom tier renders aggregates instead of raw dots until
 *  there is room for them. */
export function buildClusterTree(items: MapItem[], nodes: LayoutNode[]): {
  level1: ClusterTreeNode[]
  level2: ClusterTreeNode[]
} {
  const n = nodes.length
  if (n === 0) return { level1: [], level2: [] }
  const k1 = Math.max(4, Math.min(28, Math.round(Math.sqrt(n) / 1.6)))
  const assign1 = kmeans2d(nodes, k1)

  const groups1 = new Map<number, number[]>()
  for (let i = 0; i < n; i++) {
    const g = groups1.get(assign1[i])
    if (g) g.push(i)
    else groups1.set(assign1[i], [i])
  }

  const level1: ClusterTreeNode[] = []
  const level2: ClusterTreeNode[] = []
  for (const memberIndices of [...groups1.values()].sort((a, b) => b.length - a.length)) {
    // Region color = the color of its dominant TOPIC (stable hash), the
    // same color the treemap and every other surface show for this theme.
    const color = topicColor(captionFor(items, memberIndices) || `region-${level1.length}`)
    // Every member's cluster becomes the region's index into level1: the
    // renderer paints each dot in its region's exact shade — dots inside one
    // bubble share one hue, the same the bubble fill and legend entry show.
    const regionIndex = level1.length
    for (const index of memberIndices) { nodes[index].cluster = regionIndex }
    let cx = 0, cy = 0
    for (const index of memberIndices) { cx += nodes[index].x; cy += nodes[index].y }
    cx /= memberIndices.length; cy /= memberIndices.length
    let radius = 0
    for (const index of memberIndices) {
      radius = Math.max(radius, Math.hypot(nodes[index].x - cx, nodes[index].y - cy))
    }
    const node1: ClusterTreeNode = {
      label: captionFor(items, memberIndices),
      labelLines: captionLinesFor(items, memberIndices),
      count: memberIndices.length,
      cx, cy, radius: radius + 14, color,
    }
    // Level 2: subdivide big regions only — small ones are their own detail.
    if (memberIndices.length >= 12) {
      const sub = memberIndices.map((index) => nodes[index])
      const k2 = Math.max(2, Math.min(10, Math.round(Math.sqrt(memberIndices.length) / 1.4)))
      const assign2 = kmeans2d(sub, k2)
      const groups2 = new Map<number, number[]>()
      memberIndices.forEach((pointIndex, i) => {
        const g = groups2.get(assign2[i])
        if (g) g.push(pointIndex)
        else groups2.set(assign2[i], [pointIndex])
      })
      const children: ClusterTreeNode[] = []
      for (const subMembers of [...groups2.values()].sort((a, b) => b.length - a.length)) {
        let scx = 0, scy = 0
        for (const index of subMembers) { scx += nodes[index].x; scy += nodes[index].y }
        scx /= subMembers.length; scy /= subMembers.length
        let sRadius = 0
        for (const index of subMembers) {
          sRadius = Math.max(sRadius, Math.hypot(nodes[index].x - scx, nodes[index].y - scy))
        }
        const child: ClusterTreeNode = {
          label: captionFor(items, subMembers),
          labelLines: captionLinesFor(items, subMembers),
          count: subMembers.length,
          cx: scx, cy: scy, radius: sRadius + 8, color,
          members: subMembers,
        }
        children.push(child)
        level2.push(child)
      }
      node1.children = children
    } else {
      node1.members = memberIndices
      // A small region is its own level-2 detail: every point must be
      // reachable at the sub-cluster tier, small themes included.
      level2.push({
        label: node1.label, labelLines: node1.labelLines, count: node1.count, cx, cy, radius: node1.radius, color,
        members: memberIndices,
      })
    }
    level1.push(node1)
  }
  return { level1, level2 }
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

// ── Fused fast pipeline (worker-ready) ──────────────────────────────────────
// projectSemanticMap + semanticGraph + buildClusterTree each normalized the
// vectors and each rebuilt the kNN graph — three O(n²·dim) passes where one
// suffices. buildSemanticMap does the whole job in one pass and powers the
// worker; the older composable functions stay for tests and small datasets.

/** kNN over a flat normalized matrix with bounded top-k selection: O(n²·dim)
 *  dot products on a Float32Array and an insertion-kept top-k (no per-node
 *  sort of n elements). Returns [i, j, similarity] triples, ≤ k per source. */
export function knnEdgesFlat(flat: Float32Array, n: number, dim: number, k = 6): Array<[number, number, number]> {
  const edges: Array<[number, number, number]> = []
  const bestIdx = new Int32Array(k)
  const bestSim = new Float32Array(k)
  for (let i = 0; i < n; i++) {
    const base = i * dim
    let count = 0
    let worst = -2
    let worstAt = 0
    for (let j = 0; j < n; j++) {
      if (i === j) continue
      const other = j * dim
      let dot = 0
      for (let d = 0; d < dim; d++) dot += flat[base + d] * flat[other + d]
      if (count < k) {
        bestIdx[count] = j
        bestSim[count] = dot
        if (dot < worst || count === 0) { worst = dot; worstAt = count }
        count++
      } else if (dot > worst) {
        // Replace the current worst and recompute the new worst lazily.
        bestIdx[worstAt] = j
        bestSim[worstAt] = dot
        worst = 2
        for (let e = 0; e < k; e++) {
          if (bestSim[e] < worst) { worst = bestSim[e]; worstAt = e }
        }
      }
    }
    for (let e = 0; e < count; e++) edges.push([i, bestIdx[e], bestSim[e]])
  }
  return edges
}

export interface SemanticMapResult {
  nodes: LayoutNode[]
  edges: Array<[number, number, number]>
  level1: ClusterTreeNode[]
  level2: ClusterTreeNode[]
  width: number
  height: number
  /** The fixed projection axes of this build — feed back into the next
   *  incremental build so the coordinate frame never rotates mid-stream. */
  basis?: ProjectionBasis
}

/** Previous-layout state for incremental builds: where items ended up last
 *  time and the projection axes the corpus lives in. */
export interface PreviousLayout {
  positions: Map<string, { x: number; y: number }>
  basis?: ProjectionBasis
}

/** One-pass build: normalize once, kNN once, force layout, cluster pyramid.
 *  Deterministic; heavy enough (a couple of seconds at a few thousand items)
 *  to run inside a worker — the UI module imports this through one.
 *
 *  Incremental mode (previous given and covering most of the corpus): the
 *  coordinate frame stays the previous basis, already-placed items start (and
 *  predominantly stay) at their previous spots — only newcomers settle, next
 *  to whichever placed neighbor their kNN edges name. The map grows like a
 *  crystal instead of the whole herd migrating after every batch. */
export function buildSemanticMap(
  items: MapItem[],
  options?: { k?: number; width?: number; height?: number; ticks?: number; previous?: PreviousLayout },
): SemanticMapResult {
  const width = options?.width ?? WORLD_DEFAULT_W
  const height = options?.height ?? WORLD_DEFAULT_H
  const k = options?.k ?? 8
  const ticks = options?.ticks ?? (items.length > 1200 ? 160 : 240)

  const dim = items.length ? items[0].vector.length : 0
  const flat = new Float32Array(items.length * dim)
  items.forEach((item, i) => {
    const normalized = normalize(item.vector)
    for (let d = 0; d < dim; d++) flat[i * dim + d] = normalized[d] ?? 0
  })

  const edges = knnEdgesFlat(flat, items.length, dim, k)

  // Incremental frame: reuse the previous basis only when it actually covers
  // this corpus (a project flip changes nearly every id — rebuild fresh).
  const previous = options?.previous
  let matched = 0
  if (previous) {
    for (const item of items) if (previous.positions.has(item.id)) matched++
  }
  const incremental = !!previous && matched >= items.length * 0.3 && matched > 0
  const basis = incremental && previous!.basis ? previous!.basis : projectionBasis(items.map((item) => item.vector))
  const pca = incremental && previous!.basis ? projectWith(items.map((item) => item.vector), basis) : pca2(items.map((item) => item.vector))
  const prevPositions = incremental ? previous!.positions : null
  const random = rng(hashStrings(items.map((item) => item.id)))

  const nodes: LayoutNode[] = items.map((item, i) => ({
    id: item.id,
    item,
    cluster: 0,
    x: width / 2 + (pca.x[i] ?? 0) * 8 + (random() - 0.5) * 4,
    y: height / 2 + (pca.y[i] ?? 0) * 8 + (random() - 0.5) * 4,
  }))
  clusterAssign(items).forEach((cluster, i) => { nodes[i].cluster = cluster })

  // Newcomers first: seed each at its strongest already-placed kNN neighbor,
  // so a fresh dot is born inside the cluster it belongs to instead of
  // migrating across the plane.
  if (prevPositions) {
    const indexById = new Map(nodes.map((n, i) => [n.id, i]))
    const placed = nodes.map((n) => prevPositions.has(n.id))
    const seedFrom = new Map<number, number>()
    for (const [a, b] of edges) {
      for (const [self, other] of [[a, b], [b, a]] as const) {
        if (placed[other] && !placed[self] && !seedFrom.has(self)) seedFrom.set(self, other)
      }
    }
    for (const [self, other] of seedFrom) {
      const prev = prevPositions.get(nodes[other].id)
      if (prev) {
        nodes[self].x = prev.x + (random() - 0.5) * 14
        nodes[self].y = prev.y + (random() - 0.5) * 14
      }
    }
    // Already-placed items start exactly where they were.
    for (let i = 0; i < nodes.length; i++) {
      if (placed[i]) {
        const prev = prevPositions.get(nodes[i].id)
        if (prev) { nodes[i].x = prev.x; nodes[i].y = prev.y }
      }
    }
  }

  const links = edges.map(([source, target, similarity]) => ({
    source, target,
    distance: 18 + (1 - Math.max(0, Math.min(1, similarity))) * 112,
  }))
  const simTicks = prevPositions ? Math.min(ticks, 90) : ticks
  const simulation = forceSimulation(nodes)
    .force('link', forceLink(links).distance((link: { distance: number }) => link.distance).strength(0.6))
    .force('charge', forceManyBody().strength(-90))
    .force('x', forceX(width / 2).strength(0.03))
    .force('y', forceY(height / 2).strength(0.03))
    .stop()
  for (let i = 0; i < simTicks; i++) simulation.tick()

  // The anchor pass: placed items keep 85% of where they were — the force
  // pass only bends them locally, so a batch adjusts neighborhoods instead of
  // relocating the whole plane.
  if (prevPositions) {
    for (const node of nodes) {
      const prev = prevPositions.get(node.id)
      if (prev) {
        node.x = prev.x + (node.x - prev.x) * 0.15
        node.y = prev.y + (node.y - prev.y) * 0.15
      }
    }
  }

  const tree = buildClusterTree(items, nodes)
  return { nodes, edges, level1: tree.level1, level2: tree.level2, width, height, basis }
}

const WORLD_DEFAULT_W = 1400
const WORLD_DEFAULT_H = 900
