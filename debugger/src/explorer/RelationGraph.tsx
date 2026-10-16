import { useEffect, useMemo, useRef, useState } from 'react'
import { Button, TextInput } from '@carbon/react'
import { observationApi, type ExploreKnowledgeCard, type ExploreRelation } from '../observationApi'
import { useT } from '../i18n'
import { STATE_FILL } from './AreaMap'

/** Relation neighborhood of one knowledge item (ТЗ §7). The engine's expand
 *  walk already bounds the graph: explicit edges first, heuristic candidates
 *  only on request and always marked (dashed) so they never read as facts. */

export interface GraphNode {
  id: string
  label: string
  state: string
  depth: number
  basis?: string
  via?: string
}

export interface PositionedNode {
  node: GraphNode
  x: number
  y: number
  r: number
}

/** Edge color encodes the relation type only; node color encodes lifecycle
 *  state (STATE_FILL) — two independent channels, never one (ТЗ §7.3). */
export const RELATION_COLORS: Record<string, string> = {
  supports: 'var(--tm-success)',
  contradicts: 'var(--tm-danger)',
  supersedes: 'var(--tm-text-3)',
  derived_from: 'var(--tm-info)',
  depends_on: 'var(--tm-amber)',
  related_to: 'var(--tm-text-2)',
}

export const RELATION_TYPES = Object.keys(RELATION_COLORS)

export function relationColor(type: string): string {
  return RELATION_COLORS[type] ?? 'var(--tm-text-2)'
}

export function relationLabel(type: string, t: (key: string) => string): string {
  const key = `explorer.relation_${type}`
  const text = t(key)
  return text !== key ? text : type
}

/** Deterministic concentric layout: depth-0 roots sit in the center, every
 *  next depth forms an outer ring. Within a ring nodes are ordered by id and
 *  spread evenly, so the same expand result always draws the same graph. */
export function layoutGraph(nodes: GraphNode[], ringGap = 104): { positioned: PositionedNode[]; width: number; height: number } {
  const byDepth = new Map<number, GraphNode[]>()
  for (const node of nodes) {
    const ring = byDepth.get(node.depth) ?? []
    ring.push(node)
    byDepth.set(node.depth, ring)
  }
  for (const ring of byDepth.values()) ring.sort((a, b) => a.id.localeCompare(b.id))

  const maxDepth = nodes.length ? Math.max(...nodes.map((n) => n.depth)) : 0
  const outer = 46 + ringGap * maxDepth
  const width = Math.max(640, outer * 2 + 40)
  const height = Math.max(outer * 2 + 40, 160)
  const centerX = width / 2
  const centerY = height / 2
  const radiusFor = (depth: number, count: number) => {
    if (depth === 0) return count > 1 ? 46 : 0
    return 46 + ringGap * depth
  }
  const nodeR = (depth: number) => (depth === 0 ? 18 : depth === 1 ? 14 : 11)

  const positioned: PositionedNode[] = []
  for (const [depth, ring] of [...byDepth.entries()].sort((a, b) => a[0] - b[0])) {
    const ringR = radiusFor(depth, ring.length)
    ring.forEach((node, index) => {
      const angle = ring.length === 1 && ringR === 0 ? 0 : (2 * Math.PI * index) / ring.length - Math.PI / 2
      positioned.push({
        node,
        x: centerX + ringR * Math.cos(angle),
        y: centerY + ringR * Math.sin(angle),
        r: nodeR(depth),
      })
    })
  }
  return { positioned, width, height }
}

/** Cards → graph nodes; relations → edges between present nodes only. */
export function buildGraph(cards: ExploreKnowledgeCard[], relations: ExploreRelation[], nodeFilter: string): {
  nodes: GraphNode[]
  edges: ExploreRelation[]
  hiddenByFilter: number
} {
  const needle = nodeFilter.trim().toLowerCase()
  const source = needle
    ? cards.filter((c) => c.proposition.toLowerCase().includes(needle) || c.knowledge_id.toLowerCase().includes(needle))
    : cards
  const keep = new Set(source.map((c) => c.knowledge_id))
  // A filtered-out node keeps its neighbors' edges only when both endpoints
  // survive; hiddenByFilter reports how much was cut.
  const edges = relations.filter((rel) => keep.has(rel.from_id) && keep.has(rel.to_id))
  return { nodes: source.map(toNode), edges, hiddenByFilter: relations.length - edges.length }
}

function toNode(card: ExploreKnowledgeCard): GraphNode {
  return {
    id: card.knowledge_id,
    label: card.proposition,
    state: card.state,
    depth: card.depth ?? 1,
    basis: card.basis,
    via: card.via,
  }
}

function truncate(text: string, max: number): string {
  return text.length > max ? `${text.slice(0, max - 1)}…` : text
}

export default function RelationGraph({ project, rootID, revision, onOpenDossier }: {
  project: string
  rootID: string
  revision?: number
  onOpenDossier: (id: string) => void
}) {
  const t = useT()
  const [center, setCenter] = useState(rootID)
  const [depth, setDepth] = useState(2)
  const [includeCandidates, setIncludeCandidates] = useState(true)
  const [relationTypes, setRelationTypes] = useState<string[]>([])
  const [nodeFilter, setNodeFilter] = useState('')
  const [selected, setSelected] = useState('')

  const [cards, setCards] = useState<ExploreKnowledgeCard[]>([])
  const [relations, setRelations] = useState<ExploreRelation[]>([])
  const [notes, setNotes] = useState<string[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const seq = useRef(0)

  useEffect(() => { setCenter(rootID) }, [rootID])

  useEffect(() => {
    if (!project.trim() || !center) return
    const current = ++seq.current
    setLoading(true); setError('')
    ;(async () => {
      try {
        const res = await observationApi.explore({
          project: project.trim(),
          operation: 'expand',
          targets: [center],
          max_depth: depth,
          max_items: 60,
          include_candidates: includeCandidates,
          relations: relationTypes.length ? relationTypes : undefined,
        })
        if (current !== seq.current) return
        setCards(res.results ?? [])
        setRelations(res.relations ?? [])
        setNotes(res.notes ?? [])
      } catch (e) {
        if (current === seq.current) setError(e instanceof Error ? e.message : 'Request failed')
      } finally {
        if (current === seq.current) setLoading(false)
      }
    })()
    return () => { seq.current++ }
  }, [project, center, depth, includeCandidates, relationTypes, revision])

  const graph = useMemo(() => buildGraph(cards, relations, nodeFilter), [cards, relations, nodeFilter])
  const { positioned, width, height } = useMemo(() => layoutGraph(graph.nodes), [graph.nodes])
  const byID = useMemo(() => new Map(positioned.map((p) => [p.node.id, p])), [positioned])
  const selectedEdges = useMemo(
    () => selected ? graph.edges.filter((e) => e.from_id === selected || e.to_id === selected) : [],
    [graph.edges, selected],
  )
  const selectedCard = useMemo(() => cards.find((c) => c.knowledge_id === selected), [cards, selected])

  const chip = (active: boolean) => ({
    padding: '0.1rem 0.5rem', borderRadius: '999px', border: `1px solid ${active ? 'var(--tm-teal)' : 'var(--tm-border)'}`,
    background: active ? 'var(--tm-elevated)' : 'transparent',
    color: active ? 'var(--tm-teal)' : 'var(--tm-text-2)', cursor: 'pointer', fontSize: '0.68rem', whiteSpace: 'nowrap',
  })

  const toggleType = (type: string) => {
    setRelationTypes((current) => current.includes(type) ? current.filter((x) => x !== type) : [...current, type])
  }

  return (
    <div>
      <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', flexWrap: 'wrap', marginBottom: '0.5rem' }}>
        <span style={{ fontSize: '0.7rem', color: 'var(--tm-muted)' }}>{t('explorer.relations_depth') ?? 'Depth'}:</span>
        {[1, 2, 3].map((d) => (
          <button key={d} style={chip(depth === d)} onClick={() => setDepth(d)}>{d}</button>
        ))}
        <button style={chip(includeCandidates)} onClick={() => setIncludeCandidates((v) => !v)}>
          {t('explorer.relations_candidates') ?? 'Candidates'}
        </button>
        <TextInput
          id="explorer-node-filter"
          size="sm" labelText="" hideLabel style={{ width: '220px' }}
          placeholder={t('explorer.relations_filter_nodes') ?? 'Filter nodes…'}
          value={nodeFilter} onChange={(e) => setNodeFilter(e.target.value)}
        />
        {center !== rootID && (
          <Button size="sm" kind="ghost" onClick={() => setCenter(rootID)}>{t('explorer.relations_reset_center') ?? 'Back to root'}</Button>
        )}
        {loading && <span style={{ fontSize: '0.72rem', color: 'var(--tm-muted)' }}>…</span>}
      </div>

      <div style={{ display: 'flex', alignItems: 'center', gap: '0.4rem', flexWrap: 'wrap', marginBottom: '0.5rem' }}>
        <span style={{ fontSize: '0.7rem', color: 'var(--tm-muted)' }}>{t('explorer.relations_types') ?? 'Relations'}:</span>
        {RELATION_TYPES.map((type) => (
          <button
            key={type}
            style={{ ...chip(relationTypes.includes(type)), borderColor: relationTypes.includes(type) ? relationColor(type) : undefined }}
            onClick={() => toggleType(type)}
          >
            <span style={{ display: 'inline-block', width: '0.7rem', height: 0, borderTop: `2px solid ${relationColor(type)}`, marginRight: '0.3rem', verticalAlign: 'middle' }} />
            {relationLabel(type, t)}
          </button>
        ))}
      </div>

      {error && <div style={{ color: 'var(--tm-danger)', fontSize: '0.75rem', marginBottom: '0.5rem' }}>{error}</div>}

      {graph.nodes.length === 0 && !loading && (
        <div style={{ border: '1px dashed var(--tm-border)', borderRadius: 'var(--tm-radius-md)', padding: '0.75rem', color: 'var(--tm-muted)', fontSize: '0.8rem' }}>
          {t('explorer.relations_empty') ?? 'No relations recorded for this knowledge yet.'}
        </div>
      )}

      {graph.nodes.length > 0 && (
        <svg role="group" aria-label={t('explorer.relations_title') ?? 'Relation graph'} width="100%" viewBox={`0 0 ${width} ${height}`} style={{ display: 'block', border: '1px solid var(--tm-border)', borderRadius: 'var(--tm-radius-md)', background: 'var(--tm-bg)' }}>
          {graph.edges.map((edge, index) => {
            const from = byID.get(edge.from_id)
            const to = byID.get(edge.to_id)
            if (!from || !to) return null
            const candidate = edge.basis === 'candidate'
            const highlight = selected === edge.from_id || selected === edge.to_id
            return (
              <line
                key={`${edge.from_id}-${edge.to_id}-${edge.type}-${index}`}
                x1={from.x} y1={from.y} x2={to.x} y2={to.y}
                stroke={relationColor(edge.type)}
                strokeWidth={highlight ? 2.2 : 1.2}
                strokeOpacity={highlight ? 0.95 : 0.5}
                strokeDasharray={candidate ? '5 4' : undefined}
              >
                <title>{candidate
                  ? `${relationLabel(edge.type, t)} · ${t('explorer.relations_candidate_note') ?? 'candidate'}${edge.note ? ` · ${edge.note}` : ''}`
                  : `${relationLabel(edge.type, t)}${edge.note ? ` · ${edge.note}` : ''}${edge.event_id ? ` · ${edge.event_id}` : ''}`}</title>
              </line>
            )
          })}
          {positioned.map(({ node, x, y, r }) => {
            const isRoot = node.depth === 0
            const active = node.id === selected
            return (
              <g
                key={node.id}
                role="button" tabIndex={0}
                aria-label={truncate(node.label, 80)}
                onClick={() => setSelected(active ? '' : node.id)}
                onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); setSelected(active ? '' : node.id) } }}
                style={{ cursor: 'pointer' }}
              >
                <title>{`${node.label} [${node.state}]${node.via ? ` · via ${node.via}` : ''}${node.basis === 'candidate' ? ' · candidate' : ''}`}</title>
                <circle
                  cx={x} cy={y} r={r}
                  fill={STATE_FILL[node.state] ?? 'var(--tm-info)'}
                  fillOpacity={active ? 0.6 : isRoot ? 0.45 : 0.3}
                  stroke={active ? 'var(--tm-teal)' : isRoot ? 'var(--tm-text)' : 'var(--tm-border)'}
                  strokeWidth={active ? 2.5 : isRoot ? 1.6 : 1}
                />
                {node.basis === 'candidate' && (
                  <circle cx={x} cy={y} r={r + 3.5} fill="none" stroke="var(--tm-text-3)" strokeWidth={0.8} strokeDasharray="3 3" />
                )}
                <text x={x} y={y + r + 11} textAnchor="middle" fill={active ? 'var(--tm-text)' : 'var(--tm-text-3)'} fontSize={10} style={{ pointerEvents: 'none', userSelect: 'none' }}>
                  {truncate(node.label, isRoot ? 34 : 22)}
                </text>
              </g>
            )
          })}
        </svg>
      )}

      {/* Legend: two independent channels — edge style/color for relation
          type, node fill for lifecycle state (ТЗ §7.3). */}
      <div style={{ display: 'flex', gap: '1rem', flexWrap: 'wrap', marginTop: '0.4rem', fontSize: '0.68rem', color: 'var(--tm-muted)' }}>
        <span><span style={{ display: 'inline-block', width: '1.1rem', borderTop: '2px solid var(--tm-text-2)', verticalAlign: 'middle', marginRight: '0.3rem' }} />{t('explorer.relations_legend_explicit') ?? 'explicit'}</span>
        <span><span style={{ display: 'inline-block', width: '1.1rem', borderTop: '2px dashed var(--tm-text-2)', verticalAlign: 'middle', marginRight: '0.3rem' }} />{t('explorer.relations_legend_candidate') ?? 'candidate (heuristic)'}</span>
      </div>
      {graph.hiddenByFilter > 0 && (
        <div style={{ fontSize: '0.7rem', color: 'var(--tm-muted)', marginTop: '0.3rem' }}>
          {t('explorer.relations_hidden', { count: String(graph.hiddenByFilter) }) ?? `${graph.hiddenByFilter} edges hidden by the node filter`}
        </div>
      )}
      {notes.length > 0 && (
        <div style={{ fontSize: '0.7rem', color: 'var(--tm-muted)', marginTop: '0.3rem' }}>{notes.join(' · ')}</div>
      )}

      {/* Selected node detail: explanation of its edges + navigation (ТЗ §7.4). */}
      {selected && selectedCard && (
        <div style={{ border: '1px solid var(--tm-border)', borderRadius: 'var(--tm-radius-md)', padding: '0.6rem 0.75rem', marginTop: '0.6rem' }}>
          <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'flex-start' }}>
            <div style={{ flex: 1, fontSize: '0.8rem', lineHeight: 1.45 }}>{selectedCard.proposition}</div>
            <div style={{ display: 'flex', gap: '0.4rem', flexShrink: 0 }}>
              {selected !== center && <Button size="sm" kind="secondary" onClick={() => { setCenter(selected); setSelected('') }}>{t('explorer.relations_center') ?? 'Center here'}</Button>}
              <Button size="sm" kind="primary" onClick={() => onOpenDossier(selected)}>{t('explorer.dossier_open') ?? 'Open dossier'}</Button>
            </div>
          </div>
          <div style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)', marginTop: '0.3rem' }}>
            {selectedCard.state}{selectedCard.via ? ` · via ${selectedCard.via}` : ''}{selectedCard.basis ? ` · ${selectedCard.basis}` : ''} · {selectedEdges.length} {t('explorer.relations_edges') ?? 'edges'}
          </div>
          {selectedEdges.length > 0 && (
            <div style={{ marginTop: '0.4rem', display: 'flex', flexDirection: 'column', gap: '0.25rem' }}>
              {selectedEdges.slice(0, 12).map((edge, index) => {
                const other = edge.from_id === selected ? edge.to_id : edge.from_id
                const otherCard = cards.find((c) => c.knowledge_id === other)
                return (
                  <div key={index} style={{ fontSize: '0.72rem', color: 'var(--tm-text-2)', display: 'flex', gap: '0.4rem', alignItems: 'baseline' }}>
                    <span style={{ color: relationColor(edge.type), flexShrink: 0, minWidth: '4.5rem' }}>{edge.from_id === selected ? '→' : '←'} {relationLabel(edge.type, t)}</span>
                    <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{truncate(otherCard?.proposition ?? other, 90)}</span>
                    {edge.basis === 'candidate' && <span style={{ color: 'var(--tm-muted)', flexShrink: 0 }}>({t('explorer.relations_legend_candidate') ?? 'candidate'})</span>}
                  </div>
                )
              })}
            </div>
          )}
        </div>
      )}
    </div>
  )
}
