import { describe, expect, it } from 'vitest'
import { buildGraph, layoutGraph, relationColor, relationLabel, RELATION_TYPES, type GraphNode } from './RelationGraph'
import type { ExploreKnowledgeCard, ExploreRelation } from '../observationApi'

const node = (id: string, depth: number): GraphNode => ({ id, label: `prop ${id}`, state: 'proposed', depth })

describe('layoutGraph', () => {
  it('places depth-0 roots in the center and deeper nodes on outer rings', () => {
    const { positioned, height } = layoutGraph([node('root', 0), node('a', 1), node('b', 1), node('c', 2)])
    const center = positioned.find((p) => p.node.id === 'root')!
    const ring1 = positioned.filter((p) => p.node.depth === 1)
    const ring2 = positioned.filter((p) => p.node.depth === 2)
    const cx = 320, cy = height / 2
    expect(Math.hypot(center.x - cx, center.y - cy)).toBeLessThan(1e-6)
    for (const p of ring1) {
      const distance = Math.hypot(p.x - cx, p.y - cy)
      expect(distance).toBeGreaterThan(0)
      expect(distance).toBeLessThan(Math.hypot(ring2[0].x - cx, ring2[0].y - cy))
    }
    for (const p of ring2) {
      expect(Math.hypot(p.x - cx, p.y - cy)).toBeGreaterThan(Math.hypot(ring1[0].x - cx, ring1[0].y - cy))
    }
  })

  it('is deterministic regardless of input order', () => {
    const input = [node('b', 1), node('a', 1), node('root', 0), node('c', 2)]
    const first = layoutGraph(input).positioned
    const second = layoutGraph([...input].reverse()).positioned
    expect(second.map((p) => p.node.id)).toEqual(first.map((p) => p.node.id))
    expect(second.map((p) => [p.x, p.y])).toEqual(first.map((p) => [p.x, p.y]))
  })

  it('keeps every node inside the canvas', () => {
    const many: GraphNode[] = [node('root', 0)]
    for (let i = 0; i < 20; i++) many.push(node(`n${i}`, 1 + (i % 3)))
    const { positioned, width, height } = layoutGraph(many)
    for (const p of positioned) {
      expect(p.x + p.r).toBeLessThanOrEqual(width + 1e-6)
      expect(p.x - p.r).toBeGreaterThanOrEqual(-1e-6)
      expect(p.y + p.r).toBeLessThanOrEqual(height + 1e-6)
      expect(p.y - p.r).toBeGreaterThanOrEqual(-1e-6)
    }
  })

  it('widens the canvas so deep rings never clip', () => {
    const shallow = layoutGraph([node('root', 0), node('a', 1)])
    const deep = layoutGraph([node('root', 0), node('a', 3)])
    expect(shallow.width).toBe(640)
    expect(deep.width).toBeGreaterThan(640)
  })
})

describe('relation colors and labels', () => {
  it('gives every vocabulary type a color', () => {
    for (const type of RELATION_TYPES) expect(relationColor(type), type).toBeTruthy()
    expect(relationColor('unknown')).toBe('var(--tm-text-2)')
  })

  it('labels known types through the locale, unknown ones verbatim', () => {
    const t = (key: string) => (key === 'explorer.relation_supports' ? 'подтверждает' : key)
    expect(relationLabel('supports', t)).toBe('подтверждает')
    expect(relationLabel('mystery', t)).toBe('mystery')
  })
})

describe('buildGraph', () => {
  const cards: ExploreKnowledgeCard[] = [
    { knowledge_id: 'k1', proposition: 'api client must be regenerated', state: 'confirmed', depth: 0 },
    { knowledge_id: 'k2', proposition: 'integration tests need docker', state: 'proposed', depth: 1 },
    { knowledge_id: 'k3', proposition: 'db changes need a migration', state: 'proposed', depth: 1 },
  ]
  const relations: ExploreRelation[] = [
    { from_id: 'k1', to_id: 'k2', type: 'depends_on', basis: 'explicit' },
    { from_id: 'k1', to_id: 'k3', type: 'related_to', basis: 'candidate', note: 'shared topic: api' },
  ]

  it('keeps edges only between present nodes', () => {
    const graph = buildGraph(cards, relations, '')
    expect(graph.nodes.map((n) => n.id)).toEqual(['k1', 'k2', 'k3'])
    expect(graph.edges).toHaveLength(2)
    expect(graph.hiddenByFilter).toBe(0)
  })

  it('drops edges whose endpoints were filtered out and reports the cut', () => {
    const graph = buildGraph(cards, relations, 'docker')
    expect(graph.nodes.map((n) => n.id)).toEqual(['k2'])
    expect(graph.edges).toHaveLength(0)
    expect(graph.hiddenByFilter).toBe(2)
  })
})
