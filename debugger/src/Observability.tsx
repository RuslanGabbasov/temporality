import { type FormEvent, useMemo, useState } from 'react'
import { API_BASE } from './api'
import { observationApi, type KnowledgeItem, type ObservationEvent, type ObservationHint } from './observationApi'

function errorMessage(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }
function json(value: unknown) { return JSON.stringify(value, null, 2) }
function knowledgeID(event: ObservationEvent) {
  const value = event.data?.knowledge_id
  return typeof value === 'string' ? value : ''
}
function utcValue(local: string) { return local ? new Date(local).toISOString() : undefined }

export default function Observability() {
  const [project, setProject] = useState(new URLSearchParams(window.location.search).get('project') ?? '')
  const [asOf, setAsOf] = useState('')
  const [compareAsOf, setCompareAsOf] = useState('')
  const [knownAt, setKnownAt] = useState('')
  const [events, setEvents] = useState<ObservationEvent[]>([])
  const [nextCursor, setNextCursor] = useState('')
  const [knowledge, setKnowledge] = useState<KnowledgeItem[]>([])
  const [comparisonKnowledge, setComparisonKnowledge] = useState<KnowledgeItem[] | null>(null)
  const [selectedID, setSelectedID] = useState('')
  const [clusterFilter, setClusterFilter] = useState('all')
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [actor, setActor] = useState('human')
  const [reason, setReason] = useState('')
  const [operationError, setOperationError] = useState('')
  const [query, setQuery] = useState('')
  const [tool, setTool] = useState('')
  const [toolResult, setToolResult] = useState('')
  const [hints, setHints] = useState<ObservationHint[]>([])
  const [sourceEvent, setSourceEvent] = useState<ObservationEvent | null>(null)
  const [hintBusy, setHintBusy] = useState(false)
  const [hintError, setHintError] = useState('')
  const selected = useMemo(() => knowledge.find((item) => item.id === selectedID), [knowledge, selectedID])
  const clusters = useMemo(() => {
    const groups = new Map<string, KnowledgeItem[]>()
    for (const item of knowledge) {
      const name = item.topics?.[0] || item.entities?.[0] || 'Unscoped knowledge'
      groups.set(name, [...(groups.get(name) ?? []), item])
    }
    return [...groups.entries()].sort((left, right) => right[1].length - left[1].length || left[0].localeCompare(right[0]))
  }, [knowledge])
  const visibleKnowledge = clusterFilter === 'all' ? knowledge : knowledge.filter((item) => (item.topics?.[0] || item.entities?.[0] || 'Unscoped knowledge') === clusterFilter)
  const knowledgeChanges = useMemo(() => {
    if (!comparisonKnowledge) return null
    const oldByID = new Map(comparisonKnowledge.map((item) => [item.id, item]))
    const added = knowledge.filter((item) => !oldByID.has(item.id))
    const changed = knowledge.flatMap((item) => {
      const previous = oldByID.get(item.id)
      if (!previous) return []
      if (previous.state === item.state && previous.reuse_count === item.reuse_count && previous.at_risk === item.at_risk) return []
      return [{ item, previous }]
    })
    return { added, changed, previousCount: comparisonKnowledge.length }
  }, [knowledge, comparisonKnowledge])

  async function load(event?: FormEvent) {
    event?.preventDefault()
    if (!project.trim()) { setError('Enter a project ID.'); return }
    setLoading(true); setError(''); setOperationError('')
    try {
      const [history, timeline, comparison] = await Promise.all([
        observationApi.knowledge(project.trim(), utcValue(asOf), utcValue(knownAt)),
        observationApi.events(project.trim()),
        compareAsOf ? observationApi.knowledge(project.trim(), utcValue(compareAsOf), utcValue(knownAt)) : Promise.resolve(null),
      ])
      setKnowledge(history.knowledge)
      setClusterFilter('all')
      setEvents(timeline.events)
      setNextCursor(timeline.next_cursor ?? '')
      setComparisonKnowledge(comparison?.knowledge ?? null)
      setSelectedID((current) => history.knowledge.some((item) => item.id === current) ? current : history.knowledge[0]?.id ?? '')
      const params = new URLSearchParams(window.location.search)
      params.set('project', project.trim())
      window.history.replaceState(null, '', `${window.location.pathname}?${params.toString()}`)
    } catch (reason) { setError(errorMessage(reason)) }
    finally { setLoading(false) }
  }

  async function loadMore() {
    if (!project || !nextCursor) return
    setLoading(true); setError('')
    try {
      const page = await observationApi.events(project, nextCursor)
      setEvents((current) => [...current, ...page.events])
      setNextCursor(page.next_cursor ?? '')
    } catch (reason) { setError(errorMessage(reason)) }
    finally { setLoading(false) }
  }

  async function invalidate(event: FormEvent) {
    event.preventDefault()
    if (!selected || !reason.trim()) return
    setOperationError('')
    try {
      await observationApi.invalidate({ knowledge_id: selected.id, project, actor: { id: actor || 'human', type: 'human' }, reason: reason.trim() })
      setReason('')
      setAsOf(''); setKnownAt('')
      await load()
    } catch (failure) { setOperationError(errorMessage(failure)) }
  }

  async function retrieveHints(event: FormEvent) {
    event.preventDefault()
    setHintBusy(true); setHintError(''); setHints([])
    try {
      const response = await observationApi.hints({ project, query, tool, tool_result: toolResult, limit: 8 })
      setHints(response.hints)
    } catch (failure) { setHintError(errorMessage(failure)) }
    finally { setHintBusy(false) }
  }

  async function openSourceEvent(sourceID: string, eventID: string) {
    setOperationError('')
    try { setSourceEvent(await observationApi.event(sourceID, eventID)) }
    catch (failure) { setOperationError(errorMessage(failure)) }
  }

  return <div className="observability-shell">
    <header className="topbar obs-topbar"><div><span className="eyebrow">TEMPORALITY / OBSERVATIONS</span><h1>Knowledge observability</h1></div><div className="header-actions"><span className="connection">API <code>{API_BASE}</code></span><a className="obs-link" href="/">FRP debugger</a></div></header>
    <form className="obs-controls" onSubmit={load}>
      <label>Project ID<input value={project} onChange={(event) => setProject(event.target.value)} placeholder="repo-a" required /></label>
      <label>Observed as of<input type="datetime-local" value={asOf} onChange={(event) => setAsOf(event.target.value)} /></label>
      <label>Compare from<input type="datetime-local" value={compareAsOf} onChange={(event) => setCompareAsOf(event.target.value)} /></label>
      <label>Known by<input type="datetime-local" value={knownAt} onChange={(event) => setKnownAt(event.target.value)} /></label>
      <button className="primary" disabled={loading}>{loading ? 'Loading…' : 'Open project'}</button>
    </form>
    {error && <div className="obs-error" role="alert">{error}</div>}
    <main className="obs-grid">
      <aside className="obs-panel obs-timeline">
        <header><span className="eyebrow">EVENT STREAM</span><strong>{events.length} loaded</strong></header>
        {!events.length ? <p className="obs-empty">Open a project to inspect its observation history.</p> : <ol>{events.map((event) => <li key={`${event.source.id}:${event.event_id}`}>
          <time>{new Date(event.occurred_at).toLocaleString()}</time>
          <button className="obs-event" onClick={() => { const id = knowledgeID(event); if (id) setSelectedID(id) }}>
            <strong>{event.type}</strong><span>{knowledgeID(event) || event.context?.task || event.context?.run || event.source.integration}</span>
          </button>
          <details><summary>event payload</summary><pre>{json(event)}</pre></details>
        </li>)}</ol>}
        {nextCursor && <button className="obs-more" onClick={() => void loadMore()} disabled={loading}>{loading ? 'Loading…' : 'Load next events'}</button>}
      </aside>

      <section className="obs-panel obs-knowledge">
        <header><span className="eyebrow">COLLECTIVE KNOWLEDGE</span><strong>{knowledge.length} items</strong></header>
        {knowledgeChanges && <section className="obs-diff"><strong>Change since {new Date(utcValue(compareAsOf)!).toLocaleString()}</strong><span>+{knowledgeChanges.added.length} knowledge items · {knowledgeChanges.changed.length} changed · {knowledgeChanges.previousCount} at comparison point</span>{knowledgeChanges.changed.slice(0, 6).map(({ item, previous }) => <small key={item.id}>{item.proposition}: {previous.state} → {item.state} · reuse {previous.reuse_count} → {item.reuse_count}{item.at_risk && ' · at risk'}</small>)}</section>}
        {clusters.length > 0 && <nav className="obs-cloud" aria-label="Knowledge clusters"><button className={clusterFilter === 'all' ? 'active' : ''} onClick={() => setClusterFilter('all')}>All · {knowledge.length}</button>{clusters.map(([name, items]) => <button key={name} className={clusterFilter === name ? 'active' : ''} onClick={() => { setClusterFilter(name); setSelectedID(items[0]?.id ?? '') }}><strong>{name}</strong><span>{items.length} items · {items.filter((item) => item.state === 'confirmed').length} confirmed</span></button>)}</nav>}
        {!visibleKnowledge.length ? <p className="obs-empty">No knowledge in this cluster.</p> : <div className="obs-knowledge-list">{visibleKnowledge.map((item) => <button key={item.id} className={`obs-knowledge-item ${selectedID === item.id ? 'selected' : ''}`} onClick={() => setSelectedID(item.id)}>
          <span className={`obs-state ${item.state}`}>{item.at_risk ? 'at risk' : item.state}</span><strong>{item.proposition}</strong><small>{item.history.length} events · reused {item.reuse_count} · hints used {item.hint_uses}/{item.hint_offers}</small>
        </button>)}</div>}
      </section>

      <aside className="obs-panel obs-detail">
        <header><span className="eyebrow">PROVENANCE / ACTIVATION</span></header>
        {selected ? <>
          <article className="obs-selected"><span className={`obs-state ${selected.state}`}>{selected.state}</span><h2>{selected.proposition}</h2><small>Knowledge ID <code>{selected.id}</code></small>
            <p>Created {new Date(selected.created_at).toLocaleString()} · Updated {new Date(selected.updated_at).toLocaleString()}</p>
            <p>Reuse {selected.reuse_count} · hints offered {selected.hint_offers} · used {selected.hint_uses} · ignored {selected.hint_ignores} · helpful {selected.helpful_outcomes} · harmful {selected.harmful_outcomes}</p>
            {selected.at_risk && <p className="obs-risk">Depends on retired knowledge: {selected.risk_sources?.join(', ')}</p>}
            {selected.relationships?.length ? <><h3>Knowledge relations</h3><ul>{selected.relationships.map((relation) => <li key={relation.event_id}>{relation.type} → <code>{relation.target_id}</code></li>)}</ul></> : null}
            {selected.evidence?.length ? <><h3>Evidence</h3><ul>{selected.evidence.map((item) => <li key={item.ref}><code>{item.ref}</code> {item.type}</li>)}</ul></> : <p className="obs-muted">No evidence attached</p>}
            <h3>Lifecycle</h3><ol className="obs-history">{selected.history.map((entry) => <li key={entry.event_id}><time>{new Date(entry.at).toLocaleString()}</time><strong>{entry.type}</strong><span>{entry.state}{entry.rule ? ` · ${entry.rule}` : ''}</span>{entry.reason && <p>{entry.reason}</p>}{entry.evidence?.map((item) => <code key={item.ref}>{item.ref}</code>)}<button onClick={() => void openSourceEvent(entry.source_id, entry.event_id)}>Open source event</button></li>)}</ol>
            {sourceEvent && <details className="obs-source-event" open><summary>Source event {sourceEvent.source.id}:{sourceEvent.event_id}</summary><pre>{json(sourceEvent)}</pre></details>}
          </article>
          {selected.state !== 'invalidated' && selected.state !== 'corrected' && selected.state !== 'superseded' && <form className="obs-form" onSubmit={invalidate}><h3>Manual invalidation</h3><label>Actor<input value={actor} onChange={(event) => setActor(event.target.value)} required /></label><label>Reason<textarea value={reason} onChange={(event) => setReason(event.target.value)} required rows={3} /></label><button disabled={!reason.trim()}>Invalidate knowledge</button>{operationError && <p className="obs-error" role="alert">{operationError}</p>}</form>}
        </> : <p className="obs-empty">Select a knowledge item to inspect its origin and lifecycle.</p>}
        <form className="obs-form obs-hint-form" onSubmit={retrieveHints}><h3>Try memory activation</h3><label>Current task / query<textarea value={query} onChange={(event) => setQuery(event.target.value)} rows={2} placeholder="What is the agent trying to do?" /></label><label>Tool name<input value={tool} onChange={(event) => setTool(event.target.value)} placeholder="test-runner" /></label><label>Tool result<textarea value={toolResult} onChange={(event) => setToolResult(event.target.value)} rows={3} placeholder="Relevant excerpt from the latest tool result" /></label><button disabled={hintBusy || (!query.trim() && !toolResult.trim())}>{hintBusy ? 'Searching…' : 'Retrieve hints'}</button>{hintError && <p className="obs-error" role="alert">{hintError}</p>}
          {hints.length > 0 && <ul className="obs-hints">{hints.map((hint) => <li key={hint.hint_id}><span className={`obs-state ${hint.state}`}>{hint.state}</span><strong>{hint.proposition}</strong>{hint.caution && <small>{hint.caution}</small>}<small>Matched by {hint.matched_by.join(', ')}</small><small>Hint ID <code>{hint.hint_id}</code></small></li>)}</ul>}
        </form>
      </aside>
    </main>
  </div>
}
