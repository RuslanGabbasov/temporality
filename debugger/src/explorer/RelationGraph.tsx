import { useEffect, useMemo, useRef, useState } from 'react'
import { Button, TextInput } from '@carbon/react'
import { observationApi, type ExploreKnowledgeCard, type ExploreRelation } from '../observationApi'
import { useT } from '../i18n'
import { queryMatchesItem } from '../knowledgeQuery'
import { STATE_FILL } from './AreaMap'
import { stateLabel } from './shared'
import RelationMapCanvas from './RelationMapCanvas'

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
  const needle = nodeFilter.trim()
  const source = needle
    ? cards.filter((c) => queryMatchesItem(needle, { id: c.knowledge_id, proposition: c.proposition }))
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
  // List-first (§7 rework): the graph is for shapes, the list is for reading.
  // A dense ring of unreadable dots answered nothing; the grouped list with
  // proposition snippets answers "what is related and why" at a glance.
  const [view, setView] = useState<'list' | 'graph'>('list')

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
        <span style={{ display: 'inline-flex', border: '1px solid var(--tm-border)', borderRadius: '6px', overflow: 'hidden' }}>
          <button style={{ ...chip(view === 'list'), border: 'none' }} onClick={() => setView('list')}>{t('explorer.relations_view_list') ?? 'List'}</button>
          <button style={{ ...chip(view === 'graph'), border: 'none' }} onClick={() => setView('graph')}>{t('explorer.relations_view_graph') ?? 'Graph'}</button>
        </span>
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

      {/* Selected node detail: explanation of its edges + navigation (ТЗ §7.4).
          Rendered ABOVE the graph/list: a panel below the fold was invisible —
          the reader clicked a dot and nothing seemed to happen. */}
      {selected && selectedCard && (
        <div style={{ border: '1px solid var(--tm-teal)', borderRadius: 'var(--tm-radius-md)', padding: '0.6rem 0.75rem', marginBottom: '0.6rem' }}>
          <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'flex-start' }}>
            <div style={{ flex: 1, fontSize: '0.8rem', lineHeight: 1.45 }}>{selectedCard.proposition}</div>
            <div style={{ display: 'flex', gap: '0.4rem', flexShrink: 0 }}>
              {selected !== center && <Button size="sm" kind="secondary" onClick={() => { setCenter(selected); setSelected('') }}>{t('explorer.relations_center') ?? 'Center here'}</Button>}
              <Button size="sm" kind="primary" onClick={() => onOpenDossier(selected)}>{t('explorer.dossier_open') ?? 'Open dossier'}</Button>
            </div>
          </div>
          <div style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)', marginTop: '0.3rem' }}>
            {stateLabel(selectedCard.state, t)}{selectedCard.via ? ` · via ${selectedCard.via}` : ''}{selectedCard.basis ? ` · ${selectedCard.basis}` : ''} · {selectedEdges.length} {t('explorer.relations_edges') ?? 'edges'}
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

      {view === 'graph' && graph.nodes.length > 0 && (
        <>
          {/* The map's visual language on canvas (halo dots, verdict shapes):
              the old concentric SVG rings read as a debug artifact. */}
          <RelationMapCanvas
            positioned={positioned}
            edges={graph.edges}
            selected={selected}
            onSelect={setSelected}
          />
          {/* Legend: two independent channels — edge style/color for relation
              type, node fill for lifecycle state (ТЗ §7.3). */}
          <div style={{ display: 'flex', gap: '1rem', flexWrap: 'wrap', marginTop: '0.4rem', fontSize: '0.68rem', color: 'var(--tm-muted)' }}>
            <span><span style={{ display: 'inline-block', width: '1.1rem', borderTop: '2px solid var(--tm-text-2)', verticalAlign: 'middle', marginRight: '0.3rem' }} />{t('explorer.relations_legend_explicit') ?? 'explicit'}</span>
            <span><span style={{ display: 'inline-block', width: '1.1rem', borderTop: '2px dashed var(--tm-text-2)', verticalAlign: 'middle', marginRight: '0.3rem' }} />{t('explorer.relations_legend_candidate') ?? 'candidate (heuristic)'}</span>
          </div>
        </>
      )}

      {view === 'list' && graph.nodes.length > 0 && (
        <RelationList
          project={project}
          center={center}
          cards={cards}
          edges={graph.edges}
          selected={selected}
          onSelect={setSelected}
          onCenter={(id) => { setCenter(id); setSelected('') }}
          onOpenDossier={onOpenDossier}
        />
      )}

      {graph.hiddenByFilter > 0 && view === 'graph' && (
        <div style={{ fontSize: '0.7rem', color: 'var(--tm-muted)', marginTop: '0.3rem' }}>
          {t('explorer.relations_hidden', { count: String(graph.hiddenByFilter) }) ?? `${graph.hiddenByFilter} edges hidden by the node filter`}
        </div>
      )}
      {notes.length > 0 && (
        <div style={{ fontSize: '0.7rem', color: 'var(--tm-muted)', marginTop: '0.3rem' }}>{notes.join(' · ')}</div>
      )}
    </div>
  )
}

/** The readable view of the neighborhood: every edge as one row — relation
 *  chip, the other statement's text, candidate marker, actions. Grouped by
 *  relation type, explicit edges before candidates, so a reader can triage
 *  without decoding a hairball of lines. */
function RelationList({ project, center, cards, edges, selected, onSelect, onCenter, onOpenDossier }: {
  project: string
  center: string
  cards: ExploreKnowledgeCard[]
  edges: ExploreRelation[]
  selected: string
  onSelect: (id: string) => void
  onCenter: (id: string) => void
  onOpenDossier: (id: string) => void
}) {
  const t = useT()
  const cardOf = useMemo(() => new Map(cards.map((c) => [c.knowledge_id, c])), [cards])
  const groups = useMemo(() => {
    const byType = new Map<string, ExploreRelation[]>()
    for (const edge of edges) {
      const list = byType.get(edge.type) ?? []
      list.push(edge)
      byType.set(edge.type, list)
    }
    // Explicit edges first inside each group; candidates after, both sorted
    // by the neighbor id so the order is stable across reloads.
    for (const [type, list] of byType) {
      list.sort((a, b) => {
        const ea = a.basis === 'candidate' ? 1 : 0
        const eb = b.basis === 'candidate' ? 1 : 0
        if (ea !== eb) return ea - eb
        const oa = a.from_id === center ? a.to_id : a.from_id
        const ob = b.from_id === center ? b.to_id : b.from_id
        return oa.localeCompare(ob)
      })
      byType.set(type, list)
    }
    return [...byType.entries()].sort((a, b) => b[1].length - a[1].length)
  }, [edges, center])

  if (edges.length === 0) {
    return (
      <div style={{ border: '1px dashed var(--tm-border)', borderRadius: 'var(--tm-radius-md)', padding: '0.75rem', color: 'var(--tm-muted)', fontSize: '0.8rem' }}>
        {t('explorer.relations_empty') ?? 'No relations recorded for this knowledge yet.'}
      </div>
    )
  }

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: '0.6rem' }}>
      {groups.map(([type, list]) => (
        <div key={type}>
          <div style={{ display: 'flex', alignItems: 'baseline', gap: '0.4rem', marginBottom: '0.25rem' }}>
            <span style={{ display: 'inline-block', width: '0.9rem', borderTop: `2px solid ${relationColor(type)}` }} />
            <span style={{ fontSize: '0.72rem', fontWeight: 600, color: relationColor(type) }}>{relationLabel(type, t)}</span>
            <span style={{ fontSize: '0.68rem', color: 'var(--tm-muted)' }}>{list.length}</span>
          </div>
          <div style={{ display: 'flex', flexDirection: 'column' }}>
            {list.map((edge, index) => {
              const other = edge.from_id === center ? edge.to_id : edge.from_id
              const card = cardOf.get(other)
              const candidate = edge.basis === 'candidate'
              const active = selected === other
              return (
                <div
                  key={`${edge.from_id}-${edge.to_id}-${index}`}
                  style={{
                    display: 'flex', gap: '0.5rem', alignItems: 'baseline', flexWrap: 'wrap',
                    padding: '0.3rem 0.45rem', borderTop: '1px solid var(--tm-border)',
                    background: active ? 'var(--tm-elevated)' : 'transparent', cursor: 'pointer',
                  }}
                  onClick={() => onSelect(active ? '' : other)}
                >
                  <span style={{ fontSize: '0.76rem', flex: 1, minWidth: '14rem' }}>
                    {card ? card.proposition : other}
                    {card?.state && card.state !== 'proposed' && card.state !== 'confirmed' && (
                      <span style={{ color: 'var(--tm-muted)', fontSize: '0.68rem' }}> · {stateLabel(card.state, t)}</span>
                    )}
                  </span>
                  {candidate
                    ? <span style={{ fontSize: '0.66rem', color: 'var(--tm-muted)', flexShrink: 0 }} title={edge.note || undefined}>◇ {edge.note || (t('explorer.relations_legend_candidate') ?? 'candidate')}</span>
                    : <span style={{ fontSize: '0.66rem', color: 'var(--tm-success)', flexShrink: 0 }}>◆ {t('explorer.relations_legend_explicit') ?? 'explicit'}</span>}
                  <span style={{ display: 'inline-flex', gap: '0.3rem', flexShrink: 0 }}>
                    <button
                      onClick={(e) => { e.stopPropagation(); onCenter(other) }}
                      title={t('explorer.relations_center') ?? 'Center here'}
                      style={{ background: 'none', border: '1px solid var(--tm-border)', borderRadius: '4px', cursor: 'pointer', fontSize: '0.66rem', color: 'var(--tm-text-2)', padding: '0.05rem 0.35rem' }}
                    >◎</button>
                    <button
                      onClick={(e) => { e.stopPropagation(); onOpenDossier(other) }}
                      title={t('explorer.dossier_open') ?? 'Open dossier'}
                      style={{ background: 'none', border: '1px solid var(--tm-border)', borderRadius: '4px', cursor: 'pointer', fontSize: '0.66rem', color: 'var(--tm-text-2)', padding: '0.05rem 0.35rem' }}
                    >→</button>
                  </span>
                </div>
              )
            })}
          </div>
        </div>
      ))}
    </div>
  )
}
