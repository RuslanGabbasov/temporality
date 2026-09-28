import { type FormEvent, useMemo, useState } from 'react'
import { API_BASE } from './api'
import { observationApi, type KnowledgeItem, type ObservationEvent, type ObservationHint } from './observationApi'
import {
  Button,
  TextInput,
  TextArea,
  InlineNotification,
  Loading,
  Tag,
  Tile,
  Grid,
  Column,
  Stack,
  Section,
  Heading,
} from '@carbon/react'

function errorMessage(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }
function json(value: unknown) { return JSON.stringify(value, null, 2) }
function knowledgeID(event: ObservationEvent) {
  const value = event.data?.knowledge_id
  return typeof value === 'string' ? value : ''
}
function utcValue(local: string) { return local ? new Date(local).toISOString() : undefined }

const STATE_COLORS: Record<string, 'blue' | 'green' | 'warm-gray' | 'gray' | 'red'> = {
  proposed: 'blue',
  confirmed: 'green',
  challenged: 'warm-gray',
  corrected: 'warm-gray',
  superseded: 'gray',
  invalidated: 'red',
}

export default function Observability({ project }: { project: string }) {
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
  const [projection, setProjection] = useState<any>(null)
  const [showProjection, setShowProjection] = useState(false)
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

  return (
    <div style={{ padding: '1rem' }}>
      {error && <InlineNotification kind="error" title="Error" subtitle={error} onClose={() => setError('')} lowContrast style={{ marginBottom: '1rem' }} />}
      {operationError && <InlineNotification kind="error" title="Operation error" subtitle={operationError} onClose={() => setOperationError('')} lowContrast style={{ marginBottom: '1rem' }} />}

      <Grid>
        <Column sm={4} md={8} lg={16}>
          <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'flex-end', marginBottom: '1rem', flexWrap: 'wrap' }}>
            <div style={{ minWidth: '150px' }}>
              <TextInput id="as-of" labelText="As of" type="datetime-local" value={asOf} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setAsOf(e.target.value)} />
            </div>
            <div style={{ minWidth: '150px' }}>
              <TextInput id="compare" labelText="Compare from" type="datetime-local" value={compareAsOf} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setCompareAsOf(e.target.value)} />
            </div>
            <div style={{ minWidth: '150px' }}>
              <TextInput id="known-at" labelText="Known by" type="datetime-local" value={knownAt} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setKnownAt(e.target.value)} />
            </div>
            <Button onClick={load} disabled={loading}>Open project</Button>
          </div>
        </Column>
      </Grid>

      {knowledgeChanges && (
        <InlineNotification
          kind="info"
          title={`Change since ${new Date(utcValue(compareAsOf)!).toLocaleString()}`}
          subtitle={`+${knowledgeChanges.added.length} knowledge items · ${knowledgeChanges.changed.length} changed · ${knowledgeChanges.previousCount} at comparison point`}
          lowContrast
          style={{ marginBottom: '1rem' }}
        />
      )}

      <Grid>
        {/* Event stream */}
        <Column sm={4} md={3} lg={4}>
          <Section level={3}>
            <Heading style={{ fontSize: '0.875rem', marginBottom: '0.5rem' }}>Event Stream ({events.length})</Heading>
            <Stack gap={1}>
              {events.length === 0 && <Tile><p>Open a project to inspect events</p></Tile>}
              {events.map((event) => (
                <div key={`${event.source.id}:${event.event_id}`} style={{ fontSize: '0.75rem', padding: '0.25rem 0' }}>
                  <time style={{ color: '#7e8a9c' }}>{new Date(event.occurred_at).toLocaleString()}</time>
                  <br />
                  <span
                    style={{ cursor: 'pointer', color: '#57d7e8' }}
                    onClick={() => { const id = knowledgeID(event); if (id) setSelectedID(id) }}
                  >
                    {event.type}
                  </span>
                  <span style={{ color: '#7e8a9c', marginLeft: '0.5rem' }}>
                    {knowledgeID(event) || event.context?.task || event.context?.run || ''}
                  </span>
                </div>
              ))}
              {nextCursor && (
                <Button size="sm" kind="ghost" onClick={() => void loadMore()} disabled={loading}>
                  Load more
                </Button>
              )}
            </Stack>
          </Section>
        </Column>

        {/* Knowledge list */}
        <Column sm={4} md={5} lg={5}>
          <Section level={3}>
            <Heading style={{ fontSize: '0.875rem', marginBottom: '0.5rem' }}>Knowledge ({knowledge.length})</Heading>
            {clusters.length > 0 && (
              <div style={{ display: 'flex', gap: '0.25rem', flexWrap: 'wrap', marginBottom: '0.5rem' }}>
                <Tag
                  type={clusterFilter === 'all' ? 'blue' : 'gray'}
                  size="sm"
                  onClick={() => setClusterFilter('all')}
                  style={{ cursor: 'pointer' }}
                >
                  All ({knowledge.length})
                </Tag>
                {clusters.map(([name, items]) => (
                  <Tag
                    key={name}
                    type={clusterFilter === name ? 'blue' : 'gray'}
                    size="sm"
                    onClick={() => { setClusterFilter(name); setSelectedID(items[0]?.id ?? '') }}
                    style={{ cursor: 'pointer' }}
                  >
                    {name} ({items.length})
                  </Tag>
                ))}
              </div>
            )}
            <Stack gap={1}>
              {visibleKnowledge.map((item) => (
                <Tile
                  key={item.id}
                  onClick={() => setSelectedID(item.id)}
                  className={`workspace-tile ${selectedID === item.id ? 'selected' : ''}`}
                  style={{ padding: '0.5rem' }}
                >
                  <Tag type={STATE_COLORS[item.state] || 'gray'} size="sm">{item.at_risk ? 'at risk' : item.state}</Tag>
                  <strong style={{ fontSize: '0.875rem' }}>{item.proposition}</strong>
                  <br />
                  <small style={{ color: '#7e8a9c' }}>
                    {item.history.length} events · reused {item.reuse_count} · hints {item.hint_uses}/{item.hint_offers}
                  </small>
                </Tile>
              ))}
            </Stack>
          </Section>
        </Column>

        {/* Detail */}
        <Column sm={4} md={8} lg={7}>
          <Section level={3}>
            <Heading style={{ fontSize: '1rem', marginBottom: '0.75rem' }}>Provenance / Activation</Heading>
            {!selected ? (
              <Tile><p>Select a knowledge item to inspect</p></Tile>
            ) : (
              <Stack gap={3}>
                <Tile>
                  <Tag type={STATE_COLORS[selected.state] || 'gray'}>{selected.state}</Tag>
                  <p style={{ fontSize: '0.875rem', fontWeight: 600, marginTop: '0.5rem', lineHeight: 1.4 }}>{selected.proposition}</p>
                  <small style={{ color: '#7e8a9c' }}>ID: {selected.id}</small>
                  <p style={{ fontSize: '0.75rem', color: '#7e8a9c', marginTop: '0.25rem' }}>Created: {new Date(selected.created_at).toLocaleString()} · Updated: {new Date(selected.updated_at).toLocaleString()}</p>
                  <p style={{ fontSize: '0.75rem', color: '#7e8a9c' }}>Reuse: {selected.reuse_count} · Hints: {selected.hint_uses}/{selected.hint_offers} · Helpful: {selected.helpful_outcomes} · Harmful: {selected.harmful_outcomes}</p>
                  {selected.at_risk && <InlineNotification kind="warning" title="At risk" subtitle={`Depends on: ${selected.risk_sources?.join(', ')}`} lowContrast />}
                  {selected.relationships?.length ? (
                    <>
                      <h5 style={{ fontSize: "0.75rem", fontWeight: 600, color: "#7e8a9c", textTransform: "uppercase", letterSpacing: "0.05em", marginBottom: "0.5rem" }}>Relations</h5>
                      {selected.relationships.map((r) => <div key={r.event_id}>{r.type} → <code>{r.target_id}</code></div>)}
                    </>
                  ) : null}
                  {selected.evidence?.length ? (
                    <>
                      <h5 style={{ fontSize: "0.75rem", fontWeight: 600, color: "#7e8a9c", textTransform: "uppercase", letterSpacing: "0.05em", marginBottom: "0.5rem" }}>Evidence</h5>
                      {selected.evidence.map((e) => <div key={e.ref}><code>{e.ref}</code> ({e.type})</div>)}
                    </>
                  ) : <p style={{ color: '#7e8a9c' }}>No evidence attached</p>}
                </Tile>

                <Tile>
                  <h5 style={{ fontSize: "0.75rem", fontWeight: 600, color: "#7e8a9c", textTransform: "uppercase", letterSpacing: "0.05em", marginBottom: "0.5rem" }}>Lifecycle</h5>
                  <Stack gap={1}>
                    {selected.history.map((entry) => (
                      <div key={entry.event_id} style={{ display: 'flex', gap: '0.5rem', alignItems: 'flex-start', fontSize: '0.75rem' }}>
                        <small style={{ color: '#7e8a9c', minWidth: '8rem' }}>{new Date(entry.at).toLocaleString()}</small>
                        <Tag type="gray" size="sm">{entry.type}</Tag>
                        <span>{entry.state}{entry.rule ? ` · ${entry.rule}` : ''}</span>
                        {entry.reason && <small style={{ color: '#7e8a9c' }}>{entry.reason}</small>}
                      </div>
                    ))}
                  </Stack>
                </Tile>

                {selected.state !== 'invalidated' && selected.state !== 'corrected' && selected.state !== 'superseded' && (
                  <Tile>
                    <h5 style={{ fontSize: "0.75rem", fontWeight: 600, color: "#7e8a9c", textTransform: "uppercase", letterSpacing: "0.05em", marginBottom: "0.5rem" }}>Manual Invalidation</h5>
                    <Stack gap={2}>
                      <TextInput id="actor" labelText="Actor" value={actor} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setActor(e.target.value)} />
                      <TextArea id="reason" labelText="Reason" value={reason} onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => setReason(e.target.value)} rows={3} />
                      <Button kind="danger" onClick={invalidate} disabled={!reason.trim()}>Invalidate</Button>
                    </Stack>
                  </Tile>
                )}

                <Tile>
                  <h5 style={{ fontSize: "0.75rem", fontWeight: 600, color: "#7e8a9c", textTransform: "uppercase", letterSpacing: "0.05em", marginBottom: "0.5rem" }}>Memory Activation</h5>
                  <Stack gap={2}>
                    <TextArea id="query" labelText="Query" value={query} onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => setQuery(e.target.value)} rows={2} placeholder="What is the agent trying to do?" />
                    <TextInput id="tool" labelText="Tool" value={tool} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setTool(e.target.value)} placeholder="test-runner" />
                    <TextArea id="tool-result" labelText="Tool result" value={toolResult} onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => setToolResult(e.target.value)} rows={3} placeholder="Relevant excerpt" />
                    <Button onClick={retrieveHints} disabled={hintBusy || (!query.trim() && !toolResult.trim())}>
                      {hintBusy ? 'Searching…' : 'Retrieve hints'}
                    </Button>
                    {hintError && <InlineNotification kind="error" title="Error" subtitle={hintError} lowContrast />}
                    {hints.map((hint) => (
                      <Tile key={hint.hint_id} style={{ padding: '0.5rem' }}>
                        <Tag type="gray" size="sm">{hint.state}</Tag>
                        <strong>{hint.proposition}</strong>
                        {hint.caution && <small style={{ color: '#e6b85c', display: 'block' }}>{hint.caution}</small>}
                        <small style={{ color: '#7e8a9c', display: 'block' }}>Matched by: {hint.matched_by.join(', ')}</small>
                      </Tile>
                    ))}
                  </Stack>
                </Tile>
              </Stack>
            )}
          </Section>
        </Column>
      </Grid>

      {loading && <Loading withOverlay={false} />}
    </div>
  )
}