import { useEffect, useMemo, useState } from 'react'
import { authHeaders } from './api'
import { observationApi, type KnowledgeItem, type ObservationEvent, type ObservationHint } from './observationApi'
import {
  Button,
  TextInput,
  TextArea,
  InlineNotification,
  Loading,
  Tag,
  Tile,
  Stack,
  Heading,
} from '@carbon/react'

function errorMessage(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }
function utcValue(local: string) { return local ? new Date(local).toISOString() : undefined }

const STATE_COLORS: Record<string, 'blue' | 'green' | 'warm-gray' | 'gray' | 'red'> = {
  proposed: 'blue', confirmed: 'green', challenged: 'warm-gray',
  corrected: 'warm-gray', superseded: 'gray', invalidated: 'red',
}

type Tab = 'overview' | 'knowledge' | 'patterns'

export default function Observability({ project }: { project: string }) {
  const initParams = new URLSearchParams(window.location.search)
  const [tab, setTab] = useState<Tab>((['overview', 'knowledge', 'patterns'].includes(initParams.get('tab') as Tab) ? initParams.get('tab') : 'knowledge') as Tab)
  const [asOf, setAsOf] = useState(initParams.get('as_of') ?? '')
  const [compareAsOf, setCompareAsOf] = useState(initParams.get('compare') ?? '')
  const [knownAt, setKnownAt] = useState(initParams.get('known_at') ?? '')
  const [knowledge, setKnowledge] = useState<KnowledgeItem[]>([])
  const [comparisonKnowledge, setComparisonKnowledge] = useState<KnowledgeItem[] | null>(null)
  const [selectedID, setSelectedID] = useState(initParams.get('selected') ?? '')
  const [clusterFilter, setClusterFilter] = useState(initParams.get('cluster') ?? 'all')
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [chain, setChain] = useState<any>(null)
  const [actor, setActor] = useState('human')
  const [reason, setReason] = useState('')
  const [query, setQuery] = useState('')
  const [tool, setTool] = useState('')
  const [toolResult, setToolResult] = useState('')
  const [hints, setHints] = useState<ObservationHint[]>([])
  const [hintBusy, setHintBusy] = useState(false)
  const [hintError, setHintError] = useState('')
  const [projection, setProjection] = useState<any>(null)
  const [projectionLoading, setProjectionLoading] = useState(false)

  const selected = useMemo(() => knowledge.find((item) => item.id === selectedID), [knowledge, selectedID])
  const clusters = useMemo(() => {
    const groups = new Map<string, KnowledgeItem[]>()
    for (const item of knowledge) {
      const name = item.topics?.[0] || item.entities?.[0] || 'Unscoped'
      groups.set(name, [...(groups.get(name) ?? []), item])
    }
    return [...groups.entries()].sort((a, b) => b[1].length - a[1].length)
  }, [knowledge])
  const visibleKnowledge = clusterFilter === 'all' ? knowledge : knowledge.filter((item) => (item.topics?.[0] || item.entities?.[0] || 'Unscoped') === clusterFilter)

  // Health summary
  const health = useMemo(() => {
    const counts = { alive: 0, stale: 0, invalidated: 0, total: knowledge.length }
    for (const k of knowledge) {
      if (k.state === 'invalidated' || k.state === 'superseded' || k.state === 'corrected') counts.invalidated++
      else if (k.at_risk) counts.stale++
      else counts.alive++
    }
    return counts
  }, [knowledge])

  // Knowledge changes vs comparison
  const knowledgeChanges = useMemo(() => {
    if (!comparisonKnowledge) return null
    const oldByID = new Map(comparisonKnowledge.map((k) => [k.id, k]))
    const added = knowledge.filter((k) => !oldByID.has(k.id))
    const changed = knowledge.filter((k) => {
      const old = oldByID.get(k.id)
      return old && old.state !== k.state
    })
    return { added, changed, previousCount: comparisonKnowledge.length }
  }, [knowledge, comparisonKnowledge])

  // URL sync
  useEffect(() => {
    const p = new URLSearchParams(window.location.search)
    p.set('project', project)
    p.set('tab', tab)
    if (asOf) p.set('as_of', asOf); else p.delete('as_of')
    if (compareAsOf) p.set('compare', compareAsOf); else p.delete('compare')
    if (knownAt) p.set('known_at', knownAt); else p.delete('known_at')
    if (selectedID) p.set('selected', selectedID); else p.delete('selected')
    if (clusterFilter !== 'all') p.set('cluster', clusterFilter); else p.delete('cluster')
    window.history.replaceState(null, '', `${window.location.pathname}?${p.toString()}`)
  }, [project, tab, asOf, compareAsOf, knownAt, selectedID, clusterFilter])

  // Load knowledge
  async function load() {
    if (!project.trim()) { setError('Enter a project ID.'); return }
    setLoading(true); setError('')
    try {
      const [history, comparison] = await Promise.all([
        observationApi.knowledge(project.trim(), utcValue(asOf), utcValue(knownAt)),
        compareAsOf ? observationApi.knowledge(project.trim(), utcValue(compareAsOf), utcValue(knownAt)) : Promise.resolve(null),
      ])
      setKnowledge(history.knowledge)
      setComparisonKnowledge(comparison?.knowledge ?? null)
      setSelectedID((current) => history.knowledge.some((item) => item.id === current) ? current : history.knowledge[0]?.id ?? '')
    } catch (reason) { setError(errorMessage(reason)) }
    finally { setLoading(false) }
  }

  // Load projection
  async function loadProjection() {
    if (!project.trim() || projection) return
    setProjectionLoading(true)
    try {
      const resp = await fetch(`/kernel-api/v1/workspace/projects/${encodeURIComponent(project.trim())}/projection`, { headers: { ...authHeaders() } })
      if (resp.ok) setProjection(await resp.json())
    } catch { /* ignore */ }
    finally { setProjectionLoading(false) }
  }

  useEffect(() => { void load() }, [project, asOf, compareAsOf, knownAt])
  useEffect(() => { if (tab === 'patterns') void loadProjection() }, [tab])

  // Fetch activation chain
  useEffect(() => {
    if (!selectedID || !project.trim()) { setChain(null); return }
    const fetchChain = async () => {
      try {
        const resp = await fetch(`/kernel-api/v1/workspace/knowledge/${encodeURIComponent(selectedID)}/chain?project=${encodeURIComponent(project.trim())}`, { headers: { ...authHeaders() } })
        if (resp.ok) setChain(await resp.json())
        else setChain(null)
      } catch { setChain(null) }
    }
    void fetchChain()
  }, [selectedID, project])

  // Invalidation
  async function invalidate() {
    if (!selected || !reason.trim()) return
    try {
      await observationApi.invalidate({
        knowledge_id: selected.id, project,
        actor: { id: actor || 'human', type: 'human' },
        reason: reason.trim(),
      })
      setReason('')
      await load()
    } catch (e) { setError(errorMessage(e)) }
  }

  // Hints
  async function retrieveHints() {
    if (!query.trim() && !toolResult.trim()) return
    setHintBusy(true); setHintError('')
    try {
      const response = await observationApi.hints({ project, query, tool_result: toolResult, limit: 7 })
      setHints(response.hints ?? [])
    } catch (e) { setHintError(errorMessage(e)) }
    finally { setHintBusy(false) }
  }

  const TABS: { key: Tab; label: string }[] = [
    { key: 'overview', label: 'Overview' },
    { key: 'knowledge', label: 'Knowledge' },
    { key: 'patterns', label: 'Patterns' },
  ]

  return (
    <div style={{ padding: '1rem' }}>
      {error && <InlineNotification kind="error" title="Error" subtitle={error} onClose={() => setError('')} lowContrast style={{ marginBottom: '1rem' }} />}

      {/* Top bar: project + filters */}
      <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'flex-end', marginBottom: '0.75rem', flexWrap: 'wrap' }}>
        <div style={{ minWidth: '130px' }}>
          <TextInput id="as-of" labelText="As of" type="datetime-local" value={asOf} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setAsOf(e.target.value)} size="sm" />
        </div>
        <div style={{ minWidth: '130px' }}>
          <TextInput id="compare" labelText="Compare" type="datetime-local" value={compareAsOf} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setCompareAsOf(e.target.value)} size="sm" />
        </div>
        <div style={{ minWidth: '130px' }}>
          <TextInput id="known-at" labelText="Known by" type="datetime-local" value={knownAt} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setKnownAt(e.target.value)} size="sm" />
        </div>
        <Button size="sm" onClick={load} disabled={loading}>Load</Button>
      </div>

      {/* Tab bar */}
      <div style={{ display: 'flex', gap: '0', borderBottom: '1px solid #202a38', marginBottom: '1rem' }}>
        {TABS.map((t) => (
          <button
            key={t.key}
            onClick={() => setTab(t.key)}
            style={{
              padding: '0.5rem 1rem', background: tab === t.key ? '#121823' : 'transparent',
              border: 'none', borderBottom: tab === t.key ? '2px solid #57d7e8' : '2px solid transparent',
              color: tab === t.key ? '#57d7e8' : '#7e8a9c', cursor: 'pointer', fontSize: '0.8rem', fontWeight: 600,
            }}
          >{t.label}</button>
        ))}
      </div>

      {loading && <Loading withOverlay={false} />}

      {/* ═══════════ OVERVIEW TAB ═══════════ */}
      {tab === 'overview' && !loading && (
        <div>
          {/* Health summary */}
          <div style={{ display: 'flex', gap: '1rem', marginBottom: '1.5rem', flexWrap: 'wrap' }}>
            {[
              { label: 'Total', count: health.total, color: '#e5e9f0' },
              { label: 'Alive', count: health.alive, color: '#9ece6a' },
              { label: 'Stale', count: health.stale, color: '#e0af68' },
              { label: 'Invalidated', count: health.invalidated, color: '#f7768e' },
            ].map((s) => (
              <Tile key={s.label} style={{ padding: '0.75rem 1.25rem', minWidth: '100px', textAlign: 'center' }}>
                <div style={{ fontSize: '1.5rem', fontWeight: 700, color: s.color }}>{s.count}</div>
                <div style={{ fontSize: '0.7rem', color: '#7e8a9c', textTransform: 'uppercase', letterSpacing: '0.05em' }}>{s.label}</div>
              </Tile>
            ))}
          </div>

          {/* Changes vs comparison */}
          {knowledgeChanges && (
            <InlineNotification
              kind="info"
              title={`Change since ${new Date(utcValue(compareAsOf)!).toLocaleString()}`}
              subtitle={`+${knowledgeChanges.added.length} new · ${knowledgeChanges.changed.length} changed · ${knowledgeChanges.previousCount} at comparison point`}
              lowContrast style={{ marginBottom: '1rem' }}
            />
          )}

          {/* Cluster summary */}
          {clusters.length > 0 && (
            <Tile style={{ marginBottom: '1rem' }}>
              <Heading style={{ fontSize: '0.875rem', marginBottom: '0.5rem' }}>Knowledge by scope</Heading>
              <div style={{ display: 'flex', gap: '0.5rem', flexWrap: 'wrap' }}>
                {clusters.map(([name, items]) => {
                  const alive = items.filter((k) => k.state !== 'invalidated' && k.state !== 'superseded' && !k.at_risk).length
                  return (
                    <div key={name} style={{ padding: '0.5rem 0.75rem', background: '#0b1016', borderRadius: '6px', border: '1px solid #202a38', minWidth: '120px' }}>
                      <div style={{ fontSize: '0.8rem', fontWeight: 600, marginBottom: '0.25rem' }}>{name}</div>
                      <div style={{ fontSize: '0.7rem', color: '#7e8a9c' }}>{items.length} items · {alive} alive</div>
                    </div>
                  )
                })}
              </div>
            </Tile>
          )}

          {knowledge.length === 0 && !loading && (
            <div style={{ textAlign: 'center', color: '#7e8a9c', padding: '3rem 1rem' }}>
              <p>No knowledge found for this project. Load a project to see agent knowledge.</p>
            </div>
          )}
        </div>
      )}

      {/* ═══════════ KNOWLEDGE TAB ═══════════ */}
      {tab === 'knowledge' && !loading && (
        <div style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 1fr) 380px', gap: '1rem' }}>
          {/* Left: knowledge list */}
          <div style={{ minWidth: 0 }}>
            {/* Cluster filter chips */}
            {clusters.length > 1 && (
              <div style={{ display: 'flex', gap: '0.35rem', flexWrap: 'wrap', marginBottom: '0.75rem' }}>
                <Tag type={clusterFilter === 'all' ? 'blue' : 'gray'} size="sm" onClick={() => { setClusterFilter('all'); setSelectedID(knowledge[0]?.id ?? '') }} style={{ cursor: 'pointer' }}>All ({knowledge.length})</Tag>
                {clusters.map(([name, items]) => (
                  <Tag key={name} type={clusterFilter === name ? 'blue' : 'gray'} size="sm" onClick={() => { setClusterFilter(name); setSelectedID(items[0]?.id ?? '') }} style={{ cursor: 'pointer' }}>
                    {name} ({items.length})
                  </Tag>
                ))}
              </div>
            )}

            {/* Knowledge items */}
            <Stack gap={1}>
              {visibleKnowledge.map((item) => (
                <Tile
                  key={item.id}
                  onClick={() => setSelectedID(item.id)}
                  className={`workspace-tile ${selectedID === item.id ? 'selected' : ''}`}
                  style={{ padding: '0.5rem', cursor: 'pointer' }}
                >
                  <Tag type={STATE_COLORS[item.state] || 'gray'} size="sm">{item.at_risk ? 'at risk' : item.state}</Tag>
                  <strong style={{ fontSize: '0.875rem', marginLeft: '0.35rem' }}>{item.proposition}</strong>
                  <br />
                  <small style={{ color: '#7e8a9c' }}>
                    {item.history.length} events · reused {item.reuse_count} · hints {item.hint_uses}/{item.hint_offers}
                  </small>
                </Tile>
              ))}
            </Stack>
          </div>

          {/* Right: detail panel */}
          <div style={{ minWidth: 0 }}>
            {!selected ? (
              <Tile style={{ textAlign: 'center', color: '#7e8a9c', padding: '2rem' }}>
                <p>Select a knowledge item to inspect</p>
              </Tile>
            ) : (
              <Stack gap={2}>
                <Tile>
                  <Tag type={STATE_COLORS[selected.state] || 'gray'}>{selected.state}</Tag>
                  <p style={{ fontSize: '0.875rem', fontWeight: 600, marginTop: '0.5rem', lineHeight: 1.4 }}>{selected.proposition}</p>
                  <small style={{ color: '#7e8a9c' }}>ID: {selected.id}</small>
                  <p style={{ fontSize: '0.75rem', color: '#7e8a9c', marginTop: '0.25rem' }}>
                    Created: {new Date(selected.created_at).toLocaleString()} · Updated: {new Date(selected.updated_at).toLocaleString()}
                  </p>
                  <p style={{ fontSize: '0.75rem', color: '#7e8a9c' }}>
                    Reuse: {selected.reuse_count} · Hints: {selected.hint_uses}/{selected.hint_offers} · Helpful: {selected.helpful_outcomes} · Harmful: {selected.harmful_outcomes}
                  </p>
                  {selected.at_risk && <InlineNotification kind="warning" title="At risk" subtitle={`Depends on: ${selected.risk_sources?.join(', ')}`} lowContrast />}
                  {selected.relationships?.length ? (
                    <>
                      <h5 style={{ fontSize: '0.75rem', fontWeight: 600, color: '#7e8a9c', textTransform: 'uppercase', letterSpacing: '0.05em', marginBottom: '0.5rem', marginTop: '0.75rem' }}>Relations</h5>
                      {selected.relationships.map((r) => <div key={r.event_id} style={{ fontSize: '0.8rem' }}>{r.type} → <code>{r.target_id}</code></div>)}
                    </>
                  ) : null}
                  {selected.evidence?.length ? (
                    <>
                      <h5 style={{ fontSize: '0.75rem', fontWeight: 600, color: '#7e8a9c', textTransform: 'uppercase', letterSpacing: '0.05em', marginBottom: '0.5rem', marginTop: '0.75rem' }}>Evidence</h5>
                      {selected.evidence.map((e) => <div key={e.ref} style={{ fontSize: '0.8rem' }}><code>{e.ref}</code> ({e.type})</div>)}
                    </>
                  ) : <p style={{ color: '#7e8a9c', fontSize: '0.8rem' }}>No evidence attached</p>}
                </Tile>

                {/* Lifecycle */}
                <Tile>
                  <h5 style={{ fontSize: '0.75rem', fontWeight: 600, color: '#7e8a9c', textTransform: 'uppercase', letterSpacing: '0.05em', marginBottom: '0.5rem' }}>Lifecycle</h5>
                  <Stack gap={1}>
                    {selected.history.map((entry) => (
                      <div key={entry.event_id} style={{ display: 'flex', gap: '0.5rem', alignItems: 'flex-start', fontSize: '0.75rem' }}>
                        <small style={{ color: '#7e8a9c', minWidth: '7rem' }}>{new Date(entry.at).toLocaleString()}</small>
                        <Tag type="gray" size="sm">{entry.type}</Tag>
                        <span>{entry.state}{entry.rule ? ` · ${entry.rule}` : ''}</span>
                        {entry.reason && <small style={{ color: '#7e8a9c' }}>{entry.reason}</small>}
                      </div>
                    ))}
                  </Stack>
                </Tile>

                {/* Activation chain */}
                {chain && chain.activations?.length > 0 && (
                  <Tile>
                    <h5 style={{ fontSize: '0.75rem', fontWeight: 600, color: '#7e8a9c', textTransform: 'uppercase', letterSpacing: '0.05em', marginBottom: '0.5rem' }}>
                      Activation Chain — {chain.total_runs} runs · {chain.activations.length} activations · {chain.is_alive ? 'alive' : 'dead'}
                    </h5>
                    {chain.formation_evidence?.length > 0 && (
                      <div style={{ marginBottom: '0.5rem', padding: '0.4rem 0.5rem', borderLeft: '3px solid #57d7e8', background: 'rgba(87,215,232,0.05)', borderRadius: '0 4px 4px 0' }}>
                        <div style={{ fontSize: '0.75rem', color: '#57d7e8', fontWeight: 600, marginBottom: '0.25rem' }}>Formed in {chain.formed_in} turn {chain.formed_turn}</div>
                        <div style={{ display: 'flex', gap: '0.25rem', flexWrap: 'wrap' }}>
                          {chain.formation_evidence.map((t: any, i: number) => (
                            <Tag key={i} type={t.success ? 'green' : 'red'} size="sm">{t.tool}{t.exit_code !== undefined && t.exit_code !== 0 ? ` (${t.exit_code})` : ''}</Tag>
                          ))}
                        </div>
                      </div>
                    )}
                    {chain.activations.map((a: any, i: number) => (
                      <div key={i} style={{ padding: '0.4rem 0.5rem', borderLeft: `3px solid ${a.outcome === 'success' ? '#9ece6a' : a.outcome === 'failure' ? '#f7768e' : '#7e8a9c'}`, marginBottom: '0.25rem', background: 'rgba(255,255,255,0.02)', borderRadius: '0 4px 4px 0', fontSize: '0.8rem' }}>
                        <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
                          <strong>Turn {a.turn}</strong>
                          <span style={{ color: '#7e8a9c' }}>{a.run_id}</span>
                          {a.tool_name && <Tag type={a.tool_success ? 'green' : 'red'} size="sm">{a.tool_name}</Tag>}
                          {a.outcome && <Tag type={a.outcome === 'success' ? 'green' : 'red'} size="sm">{a.outcome}</Tag>}
                        </div>
                        <div style={{ fontSize: '0.7rem', color: '#7e8a9c', marginTop: '0.15rem' }}>
                          recalled {new Date(a.recalled_at).toLocaleString()}
                          {a.injected_at && ` → injected ${new Date(a.injected_at).toLocaleString()}`}
                        </div>
                      </div>
                    ))}
                    {chain.invalidation && (
                      <div style={{ padding: '0.4rem 0.5rem', borderLeft: '3px solid #f7768e', marginTop: '0.25rem', background: 'rgba(247,118,142,0.05)', borderRadius: '0 4px 4px 0', fontSize: '0.8rem' }}>
                        <strong style={{ color: '#f7768e' }}>{chain.invalidation.kind}</strong>
                        <span style={{ color: '#7e8a9c', marginLeft: '0.5rem' }}>{chain.invalidation.run_id} · {new Date(chain.invalidation.at).toLocaleString()}</span>
                      </div>
                    )}
                  </Tile>
                )}

                {/* Manual invalidation */}
                {selected.state !== 'invalidated' && selected.state !== 'corrected' && selected.state !== 'superseded' && (
                  <Tile>
                    <h5 style={{ fontSize: '0.75rem', fontWeight: 600, color: '#7e8a9c', textTransform: 'uppercase', letterSpacing: '0.05em', marginBottom: '0.5rem' }}>Manual Invalidation</h5>
                    <Stack gap={2}>
                      <TextInput id="actor" labelText="Actor" value={actor} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setActor(e.target.value)} size="sm" />
                      <TextArea id="reason" labelText="Reason" value={reason} onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => setReason(e.target.value)} rows={2} />
                      <Button kind="danger" size="sm" onClick={invalidate} disabled={!reason.trim()}>Invalidate</Button>
                    </Stack>
                  </Tile>
                )}

                {/* Memory hints */}
                <Tile>
                  <h5 style={{ fontSize: '0.75rem', fontWeight: 600, color: '#7e8a9c', textTransform: 'uppercase', letterSpacing: '0.05em', marginBottom: '0.5rem' }}>Memory Lookup</h5>
                  <Stack gap={2}>
                    <TextArea id="query" labelText="Query" value={query} onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => setQuery(e.target.value)} rows={2} placeholder="What is the agent trying to do?" />
                    <TextInput id="tool" labelText="Tool" value={tool} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setTool(e.target.value)} placeholder="test-runner" size="sm" />
                    <TextArea id="tool-result" labelText="Tool result" value={toolResult} onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => setToolResult(e.target.value)} rows={2} placeholder="Relevant excerpt" />
                    <Button size="sm" onClick={retrieveHints} disabled={hintBusy || (!query.trim() && !toolResult.trim())}>
                      {hintBusy ? 'Searching…' : 'Retrieve hints'}
                    </Button>
                    {hintError && <InlineNotification kind="error" title="Error" subtitle={hintError} lowContrast />}
                    {hints.map((hint) => (
                      <Tile key={hint.hint_id} style={{ padding: '0.5rem' }}>
                        <Tag type="gray" size="sm">{hint.state}</Tag>
                        <strong style={{ fontSize: '0.85rem' }}>{hint.proposition}</strong>
                        {hint.caution && <small style={{ color: '#e6b85c', display: 'block', marginTop: '0.25rem' }}>{hint.caution}</small>}
                        <small style={{ color: '#7e8a9c', display: 'block' }}>Matched by: {hint.matched_by.join(', ')}</small>
                      </Tile>
                    ))}
                  </Stack>
                </Tile>
              </Stack>
            )}
          </div>
        </div>
      )}

      {/* ═══════════ PATTERNS TAB ═══════════ */}
      {tab === 'patterns' && (
        <div>
          {projectionLoading && <Loading withOverlay={false} />}
          {!projection && !projectionLoading && (
            <div style={{ textAlign: 'center', color: '#7e8a9c', padding: '3rem 1rem' }}>
              <p>No projection data yet. Click Load to fetch experience patterns.</p>
              <Button onClick={loadProjection} style={{ marginTop: '1rem' }}>Load patterns</Button>
            </div>
          )}
          {projection && (
            <Stack gap={3}>
              {/* Summary */}
              <div style={{ display: 'flex', gap: '1rem', flexWrap: 'wrap' }}>
                {[
                  { label: 'Runs', count: projection.run_count, color: '#57d7e8' },
                  { label: 'Tool patterns', count: projection.summary?.total_tool_patterns ?? 0, color: '#9ece6a' },
                  { label: 'Knowledge', count: projection.summary?.total_knowledge ?? 0, color: '#bb9af7' },
                  { label: 'Alive', count: projection.summary?.alive_knowledge ?? 0, color: '#9ece6a' },
                  { label: 'Dead', count: projection.summary?.dead_knowledge ?? 0, color: '#f7768e' },
                ].map((s) => (
                  <Tile key={s.label} style={{ padding: '0.5rem 1rem', minWidth: '80px', textAlign: 'center' }}>
                    <div style={{ fontSize: '1.25rem', fontWeight: 700, color: s.color }}>{s.count}</div>
                    <div style={{ fontSize: '0.65rem', color: '#7e8a9c', textTransform: 'uppercase', letterSpacing: '0.05em' }}>{s.label}</div>
                  </Tile>
                ))}
                {projection.summary?.avg_success_rate > 0 && (
                  <Tile style={{ padding: '0.5rem 1rem', textAlign: 'center' }}>
                    <div style={{ fontSize: '1.25rem', fontWeight: 700, color: '#73daca' }}>{(projection.summary.avg_success_rate * 100).toFixed(0)}%</div>
                    <div style={{ fontSize: '0.65rem', color: '#7e8a9c', textTransform: 'uppercase', letterSpacing: '0.05em' }}>Success rate</div>
                  </Tile>
                )}
              </div>

              {/* Top tools */}
              {projection.summary?.most_used_tools?.length > 0 && (
                <Tile>
                  <h5 style={{ fontSize: '0.75rem', fontWeight: 600, color: '#7e8a9c', textTransform: 'uppercase', letterSpacing: '0.05em', marginBottom: '0.5rem' }}>Most used tools</h5>
                  <div style={{ display: 'flex', gap: '0.35rem', flexWrap: 'wrap' }}>
                    {projection.summary.most_used_tools.map((t: string) => <Tag key={t} type="blue" size="sm">{t}</Tag>)}
                  </div>
                </Tile>
              )}

              {/* Tool patterns */}
              {projection.tool_patterns?.length > 0 && (
                <Tile>
                  <h5 style={{ fontSize: '0.75rem', fontWeight: 600, color: '#7e8a9c', textTransform: 'uppercase', letterSpacing: '0.05em', marginBottom: '0.5rem' }}>Tool Patterns</h5>
                  <Stack gap={1}>
                    {projection.tool_patterns.map((p: any, i: number) => (
                      <div key={i} style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', fontSize: '0.8rem', padding: '0.3rem 0', borderBottom: '1px solid #1a2332' }}>
                        <Tag type="cyan" size="sm">{p.count}x</Tag>
                        <code style={{ color: '#9e8cff', fontSize: '0.78rem' }}>{p.signature}</code>
                        <span style={{ color: '#7e8a9c', marginLeft: 'auto' }}>{p.run_count} runs · {(p.success_rate * 100).toFixed(0)}% success</span>
                      </div>
                    ))}
                  </Stack>
                </Tile>
              )}

              {/* Knowledge lifecycle */}
              {projection.knowledge_lifecycle?.length > 0 && (
                <Tile>
                  <h5 style={{ fontSize: '0.75rem', fontWeight: 600, color: '#7e8a9c', textTransform: 'uppercase', letterSpacing: '0.05em', marginBottom: '0.5rem' }}>Knowledge Lifecycle</h5>
                  <Stack gap={1}>
                    {projection.knowledge_lifecycle.map((k: any, i: number) => (
                      <div key={i} style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', fontSize: '0.8rem', padding: '0.3rem 0', borderBottom: '1px solid #1a2332' }}>
                        <Tag type={k.is_alive ? 'green' : 'red'} size="sm">{k.current_state}</Tag>
                        <span style={{ flex: 1, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{k.proposition || k.knowledge_id}</span>
                        <span style={{ color: '#7e8a9c', flexShrink: 0 }}>{k.run_count} runs</span>
                      </div>
                    ))}
                  </Stack>
                </Tile>
              )}

              {/* Scopes */}
              {projection.scopes?.length > 0 && (
                <Tile>
                  <h5 style={{ fontSize: '0.75rem', fontWeight: 600, color: '#7e8a9c', textTransform: 'uppercase', letterSpacing: '0.05em', marginBottom: '0.5rem' }}>Scopes</h5>
                  <div style={{ display: 'flex', gap: '0.5rem', flexWrap: 'wrap' }}>
                    {projection.scopes.map((s: any, i: number) => (
                      <div key={i} style={{ padding: '0.4rem 0.6rem', background: '#0b1016', borderRadius: '4px', border: '1px solid #202a38', fontSize: '0.8rem' }}>
                        <strong>{s.scope}</strong>
                        <span style={{ color: '#7e8a9c', marginLeft: '0.5rem' }}>{s.tool_count} tools · {s.run_ids?.length ?? 0} runs</span>
                      </div>
                    ))}
                  </div>
                </Tile>
              )}
            </Stack>
          )}
        </div>
      )}
    </div>
  )
}