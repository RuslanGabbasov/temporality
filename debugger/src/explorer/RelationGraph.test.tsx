import { describe, expect, it } from 'vitest'
import { buildGraph, relationColor, relationLabel, RELATION_TYPES, type GraphNode } from './RelationGraph'
import type { ExploreKnowledgeCard, ExploreRelation } from '../observationApi'

const node = (id: string, depth: number): GraphNode => ({ id, label: `prop ${id}`, state: 'proposed', depth })

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
