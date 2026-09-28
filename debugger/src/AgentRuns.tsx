import { useCallback, useEffect, useMemo, useState } from 'react'
import { API_BASE, authHeaders } from './api'
import { observationApi, type ObservationEvent } from './observationApi'
import Markdown from './Markdown'
import {
  Button,
  TextInput,
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
import { Time, Play, ChevronDown, ChevronRight } from '@carbon/icons-react'

const KERNEL_API = '/kernel-api'
function message(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }
function json(value: unknown) { return JSON.stringify(value, null, 2) }
function runID(event: ObservationEvent) { return event.context?.run ?? '' }
function pendingApprovals(events: ObservationEvent[]) {
  const ended = new Set(events.filter((event) => ['approval.granted', 'approval.rejected', 'approval.timed_out'].includes(event.type)).map((event) => event.data?.operation_id))
  return events.filter((event) => event.type === 'approval.requested' && !ended.has(event.data?.operation_id))
}

function shortTime(iso: string) {
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? '' : d.toLocaleTimeString()
}

/** Format an event into a human-readable summary line. */
function eventSummary(event: ObservationEvent): { icon: string; label: string; detail: string; color: string } {
  const d = event.data ?? {}
  switch (event.type) {
    case 'run.started':
      return { icon: '▶', label: 'Run started', detail: d.model ? `model ${d.model}` : '', color: '#57d7e8' }
    case 'run.completed':
      return { icon: '✓', label: 'Run completed', detail: `${d.turns ?? '?'} turns`, color: '#9ece6a' }
    case 'run.failed':
      return { icon: '✗', label: 'Run failed', detail: d.error ? String(d.error).slice(0, 80) : '', color: '#f7768e' }
    case 'turn.started':
      return { icon: '→', label: `Turn ${d.turn ?? '?'}`, detail: 'started', color: '#7e8a9c' }
    case 'turn.completed':
      return { icon: '←', label: `Turn ${d.turn ?? '?'}`, detail: d.tool_calls ? `${d.tool_calls} tool calls` : 'completed', color: '#7e8a9c' }
    case 'model.started':
      return { icon: '⏳', label: 'Model call', detail: String(d.model ?? "started"), color: '#7e8a9c' }
    case 'model.completed': {
      const tokens = d.total_tokens ? `${d.total_tokens} tok` : ''
      const latency = d.latency_ms ? `${(Number(d.latency_ms) / 1000).toFixed(1)}s` : ''
      const calls = d.tool_call_count ? `${d.tool_call_count} tools` : ''
      const parts = [tokens, latency, calls].filter(Boolean).join(' · ')
      return { icon: '🧠', label: 'Model response', detail: parts, color: '#bb9af7' }
    }
    case 'tool.started':
      return { icon: '🔧', label: String(d.tool ?? 'tool'), detail: 'started', color: '#e0af68' }
    case 'tool.completed': {
      const exit = d.exit_code !== undefined ? `exit ${d.exit_code}` : ''
      const ms = d.latency_ms ? `${(Number(d.latency_ms) / 1000).toFixed(1)}s` : ''
      return { icon: d.exit_code === 0 ? '✓' : '✗', label: String(d.tool ?? 'tool'), detail: [exit, ms].filter(Boolean).join(' · '), color: d.exit_code === 0 ? '#9ece6a' : '#f7768e' }
    }
    case 'tool.failed':
      return { icon: '✗', label: String(d.tool ?? "tool"), detail: d.error ? String(d.error).slice(0, 60) : 'failed', color: '#f7768e' }
    case 'knowledge.proposed':
      return { icon: '💡', label: 'Learned', detail: String(d.proposition ?? '').slice(0, 60), color: '#73daca' }
    case 'knowledge.recalled':
      return { icon: '📚', label: 'Recalled', detail: String(d.proposition ?? '').slice(0, 60), color: '#9d7cd8' }
    case 'hint.query':
      return { icon: '🔍', label: 'Memory lookup', detail: `${d.candidate_count ?? 0} candidates`, color: '#7e8a9c' }
    case 'memory.read':
      return { icon: '📖', label: 'Memory read', detail: `${d.hint_count ?? 0} hints loaded`, color: '#7e8a9c' }
    case 'agent.summary':
      return { icon: '📋', label: 'Summary', detail: String(d.kind ?? ''), color: '#57d7e8' }
    case 'approval.requested':
      return { icon: '⚠', label: 'Approval needed', detail: String(d.action ?? d.reason ?? ''), color: '#e6b85c' }
    case 'approval.granted':
      return { icon: '✓', label: 'Approved', detail: '', color: '#9ece6a' }
    case 'approval.rejected':
      return { icon: '✗', label: 'Rejected', detail: '', color: '#f7768e' }
    case 'delegation.started':
      return { icon: '↗', label: 'Delegation', detail: `→ ${d.child_run_id ?? '?'}`, color: '#7aa2f7' }
    default:
      return { icon: '•', label: event.type, detail: '', color: '#7e8a9c' }
  }
}

/** Expandable event detail showing raw data. */
function EventDetail({ event }: { event: ObservationEvent }) {
  const [expanded, setExpanded] = useState(false)
  const d = event.data ?? {}

  // Show useful fields first, skip noise
  const skipFields = new Set(['caused_by', 'frame_id', 'parent_frame_id', 'sequence'])
  const importantFields: [string, unknown][] = []
  const otherFields: [string, unknown][] = []
  for (const [k, v] of Object.entries(d)) {
    if (skipFields.has(k)) continue
    if (v === '' || v === null || v === undefined) continue
    if (['tool', 'turn', 'total_tokens', 'latency_ms', 'exit_code', 'tool_call_count', 'finish_reason', 'model', 'provider', 'error', 'proposition', 'answer'].includes(k)) {
      importantFields.push([k, v])
    } else {
      otherFields.push([k, v])
    }
  }

  return (
    <div>
      <button
        onClick={() => setExpanded(!expanded)}
        style={{ background: 'none', border: 'none', color: '#7e8a9c', cursor: 'pointer', fontSize: '0.7rem', padding: '0.15rem 0', display: 'flex', alignItems: 'center', gap: '0.25rem' }}
      >
        {expanded ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
        {expanded ? 'hide details' : 'show details'}
      </button>
      {expanded && (
        <div style={{ marginTop: '0.35rem', padding: '0.5rem', background: '#0b1016', borderRadius: '4px', fontSize: '0.7rem', fontFamily: '"SFMono-Regular", Consolas, monospace', maxHeight: '300px', overflowY: 'auto' }}>
          {/* Show answer/content if present */}
          {typeof d.answer === 'string' && (
            <div style={{ marginBottom: '0.5rem' }}>
              <div style={{ color: '#7e8a9c', marginBottom: '0.25rem' }}>answer:</div>
              <div style={{ whiteSpace: 'pre-wrap', color: '#e5e9f0', background: '#121823', padding: '0.5rem', borderRadius: '3px', maxHeight: '150px', overflowY: 'auto' }}>{d.answer.slice(0, 2000)}</div>
            </div>
          )}
          {/* Show other data */}
          {[...importantFields, ...otherFields].map(([k, v]) => (
            <div key={k} style={{ marginBottom: '0.2rem' }}>
              <span style={{ color: '#7e8a9c' }}>{k}: </span>
              <span style={{ color: '#e5e9f0' }}>
                {typeof v === 'object' ? JSON.stringify(v).slice(0, 200) : String(v).slice(0, 200)}
              </span>
            </div>
          ))}
          {/* Event metadata */}
          <div style={{ marginTop: '0.35rem', borderTop: '1px solid #202a38', paddingTop: '0.35rem', color: '#565f89' }}>
            event_id: {event.event_id}
          </div>
        </div>
      )}
    </div>
  )
}

export default function AgentRuns({ project }: { project: string }) {
  const [runs, setRuns] = useState<ObservationEvent[]>([])
  const [selected, setSelected] = useState('')
  const [timeline, setTimeline] = useState<ObservationEvent[]>([])
  const [result, setResult] = useState<Record<string, unknown> | null>(null)
  const [reason, setReason] = useState('Reviewed in Agent Runs UI')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const pending = useMemo(() => pendingApprovals(timeline), [timeline])

  const loadRuns = useCallback(async () => {
    if (!project.trim()) return
    setBusy(true); setError('')
    try {
      const page = await observationApi.events(project.trim(), undefined, undefined, undefined, { type: 'run.started', limit: 500 })
      const rootRuns = page.events.filter((event) => !event.data?.parent_run_id)
      setRuns(rootRuns.reverse())
      if (!selected && rootRuns.length) {
        for (const event of rootRuns) {
          const id = runID(event)
          if (!id) continue
          const query = new URLSearchParams({ project: project.trim(), source_id: event.source.id })
          let response = await fetch(`${KERNEL_API}/v1/agent/runs/${encodeURIComponent(id)}?${query}`, { headers: authHeaders() })
          if (!response.ok && event.data?.workflow === 'LeadCoderReviewerQA') {
            response = await fetch(`${KERNEL_API}/v1/agent/examples/lead-coder-reviewer-qa/runs/${encodeURIComponent(id)}?${query}`, { headers: authHeaders() })
          }
          if (response.ok) { setSelected(id); break }
        }
        if (!selected) setSelected(runID(rootRuns[0]) || '')
      }
    } catch (failure) { setError(message(failure)) }
    finally { setBusy(false) }
  }, [project])

  const loadRun = useCallback(async (run: string, silent = false) => {
    if (!project.trim() || !run) return
    if (!silent) setBusy(true)
    setError('')
    try {
      const page = await observationApi.events(project.trim(), undefined, undefined, undefined, { run, limit: 500 })
      const root = page.events.find((event) => event.type === 'run.started')
      const teamRun = root?.data?.workflow === 'LeadCoderReviewerQA'
      let allEvents = page.events
      if (teamRun) {
        const childIDs = page.events.filter((event) => event.type === 'delegation.started').map((event) => String(event.data?.child_run_id ?? '')).filter(Boolean)
        const childPages = await Promise.all(childIDs.map((child) => observationApi.events(project.trim(), undefined, undefined, undefined, { run: child, limit: 500 })))
        allEvents = [...page.events, ...childPages.flatMap((childPage) => childPage.events)].sort((left, right) => left.occurred_at.localeCompare(right.occurred_at))
      }
      setTimeline(allEvents)
      const resultPath = teamRun ? `/v1/agent/examples/lead-coder-reviewer-qa/runs/${encodeURIComponent(run)}` : `/v1/agent/runs/${encodeURIComponent(run)}`
      const sourceQuery = new URLSearchParams({ project: project.trim(), source_id: root?.source.id ?? '' })
      let response = await fetch(`${KERNEL_API}${resultPath}?${sourceQuery}`, { headers: authHeaders() })
      if (!response.ok && teamRun) response = await fetch(`${KERNEL_API}/v1/agent/runs/${encodeURIComponent(run)}?${sourceQuery}`, { headers: authHeaders() })
      if (response.ok) {
        setResult(await response.json() as Record<string, unknown>)
      } else if (response.status === 404) {
        setResult({ status: 'workflow_expired', error: 'Workflow no longer available in Temporal (events preserved in journal)' })
      } else {
        const detail = await response.text()
        throw new Error(`${response.status} ${detail}`)
      }
      const query = new URLSearchParams({ project: project.trim(), run })
      window.history.replaceState(null, '', `/agents?${query}`)
    } catch (failure) { setError(message(failure)) }
    finally { if (!silent) setBusy(false) }
  }, [project])

  useEffect(() => { void loadRuns() }, [loadRuns])
  useEffect(() => { if (selected) void loadRun(selected) }, [selected, loadRun])
  useEffect(() => {
    if (!selected) return
    const timer = window.setInterval(() => { void loadRuns(); void loadRun(selected, true) }, 4000)
    return () => window.clearInterval(timer)
  }, [selected, loadRuns, loadRun])

  async function decide(event: ObservationEvent, approved: boolean) {
    const operationID = String(event.data?.operation_id ?? '')
    setBusy(true); setError('')
    try {
      const root = timeline.find((item) => item.type === 'run.started' && runID(item) === selected)
      const operation = event.data?.operation as Record<string, unknown> | undefined
      const approvalQuery = new URLSearchParams({ project: project.trim(), source_id: root?.source.id ?? '' })
      const response = await fetch(`${KERNEL_API}/v1/agent/runs/${encodeURIComponent(selected)}/approval?${approvalQuery}`, {
        method: 'POST', headers: { 'Content-Type': 'application/json', ...authHeaders() },
        body: JSON.stringify({ operation_id: operationID, arguments_hash: operation?.arguments_hash ?? '', approved, actor_id: 'human-ui', reason: reason.trim() }),
      })
      if (!response.ok) throw new Error(`${response.status} ${await response.text()}`)
      await loadRun(selected, true)
    } catch (failure) { setError(message(failure)) }
    finally { setBusy(false) }
  }

  const answer = (result?.result as Record<string, unknown> | undefined)?.answer
  const stages = (result?.result as Record<string, unknown> | undefined)?.stages
  return (
    <div style={{ padding: '1rem' }}>
      {error && (
        <InlineNotification kind="error" title="Error" subtitle={error} onClose={() => setError('')} lowContrast style={{ marginBottom: '1rem' }} />
      )}

      <Grid>
        <Column sm={4} md={8} lg={16}>
          <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'flex-end', marginBottom: '1rem' }}>
            <Button onClick={() => { setSelected(''); setTimeline([]); void loadRuns() }} disabled={busy}>
              Refresh
            </Button>
            {selected && <Tag type="gray" size="sm">{selected}</Tag>}
          </div>
        </Column>
      </Grid>

      <Grid>
        {/* Run list */}
        <Column sm={4} md={3} lg={4}>
          <Section level={3}>
            <Heading>Runs ({runs.length})</Heading>
            <Stack gap={1}>
              {runs.length === 0 && <Tile style={{ color: '#7e8a9c', textAlign: 'center' }}>No runs for this project</Tile>}
              {runs.map((event) => {
                const id = runID(event)
                return (
                  <Tile
                    key={event.event_id}
                    onClick={() => setSelected(id)}
                    className={`workspace-tile ${selected === id ? 'selected' : ''}`}
                    style={{ padding: '0.5rem 0.75rem', cursor: 'pointer' }}
                  >
                    <div style={{ fontWeight: 500, fontSize: '0.8rem', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{id}</div>
                    <div style={{ fontSize: '0.7rem', color: '#7e8a9c' }}>
                      {String(event.data?.role ?? 'agent')} · {shortTime(event.occurred_at)}
                    </div>
                  </Tile>
                )
              })}
            </Stack>
          </Section>
        </Column>

        {/* Run detail */}
        <Column sm={4} md={5} lg={12}>
          <Section level={3}>
            {!selected ? (
              <div style={{ textAlign: 'center', color: '#7e8a9c', padding: '3rem 1rem' }}>
                <Heading>Select a run</Heading>
                <p style={{ marginTop: '0.5rem' }}>Click a run on the left to see its trace.</p>
              </div>
            ) : (
              <Stack gap={3}>
                {/* Answer */}
                {typeof answer === 'string' && (
                  <Tile>
                    <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', marginBottom: '0.5rem' }}>
                      <Heading style={{ fontSize: '0.875rem' }}>Agent Answer</Heading>
                      <Tag type={result?.status === 'completed' ? 'green' : 'red'} size="sm">{String(result?.status ?? '')}</Tag>
                    </div>
                    <Markdown content={answer} />
                  </Tile>
                )}

                {/* Multi-stage answer */}
                {Array.isArray(stages) && stages.map((stage, index) => {
                  const item = stage as Record<string, unknown>
                  return (
                    <Tile key={`${String(item.run_id ?? item.role ?? index)}`}>
                      <Heading style={{ fontSize: '0.875rem' }}>{String(item.role ?? `Stage ${index + 1}`)}</Heading>
                      <Tag type="gray" size="sm">{String(item.status ?? '')}</Tag>
                      {typeof item.answer === 'string' && <Markdown content={item.answer} />}
                    </Tile>
                  )
                })}

                {/* Raw result if no answer */}
                {result?.result !== undefined && !answer && (
                  <Tile>
                    <Heading style={{ fontSize: '0.875rem' }}>Run Result</Heading>
                    <pre style={{ background: '#0b1016', padding: '0.75rem', fontSize: '0.75rem', overflow: 'auto', borderRadius: '4px', maxHeight: '300px' }}>
                      {json(result.result)}
                    </pre>
                  </Tile>
                )}

                {/* Pending approvals */}
                {pending.map((event) => {
                  const details = event.data?.details as Record<string, unknown> | undefined
                  const operation = event.data?.operation as Record<string, unknown> | undefined
                  const operationArgs = operation?.arguments as Record<string, unknown> | undefined
                  const command = operationArgs?.command
                  const risk = event.data?.risk as Record<string, unknown> | undefined
                  return (
                    <Tile key={event.event_id} style={{ borderLeft: '3px solid #e6b85c' }}>
                      <Heading style={{ fontSize: '0.875rem' }}>⚠ Approval Required</Heading>
                      <p style={{ fontSize: '0.875rem', margin: '0.5rem 0' }}>{String(operation?.summary ?? event.data?.reason ?? event.data?.action ?? 'Agent requested approval')}</p>
                      {details && (
                        <>
                          <Tag type="warm-gray" size="sm">{String(details.tool)}</Tag>
                          <Tag type="gray" size="sm">risk {String(risk?.level ?? 'unknown')}</Tag>
                          <pre style={{ background: '#0b1016', padding: '0.5rem', fontSize: '0.75rem', marginTop: '0.5rem', borderRadius: '4px' }}>
                            {Array.isArray(command) ? command.join(' ') : json(operation?.arguments ?? details)}
                          </pre>
                        </>
                      )}
                      <div style={{ marginTop: '0.75rem' }}>
                        <TextInput id="decision-note" labelText="Decision note" value={reason} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setReason(e.target.value)} />
                      </div>
                      <Stack orientation="horizontal" gap={2} style={{ marginTop: '0.5rem' }}>
                        <Button onClick={() => void decide(event, true)} disabled={busy}>Approve</Button>
                        <Button kind="secondary" onClick={() => void decide(event, false)} disabled={busy}>Reject</Button>
                      </Stack>
                    </Tile>
                  )
                })}

                {/* Timeline — the main trace view */}
                <Tile>
                  <Heading style={{ fontSize: '0.875rem', marginBottom: '0.5rem' }}>Trace ({timeline.length} events)</Heading>
                  <Stack gap={0}>
                    {[...timeline].reverse().map((event) => {
                      const info = eventSummary(event)
                      return (
                        <div
                          key={`${event.source.id}:${event.event_id}`}
                          style={{
                            padding: '0.4rem 0.5rem',
                            borderLeft: `3px solid ${info.color}`,
                            marginBottom: '0.15rem',
                            background: 'rgba(255,255,255,0.02)',
                            borderRadius: '0 4px 4px 0',
                          }}
                        >
                          <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', fontSize: '0.8rem' }}>
                            <span style={{ color: info.color, width: '1.2rem', textAlign: 'center', flexShrink: 0 }}>{info.icon}</span>
                            <strong style={{ color: info.color, minWidth: '100px' }}>{info.label}</strong>
                            <span style={{ color: '#7e8a9c', flex: 1, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{info.detail}</span>
                            <span style={{ color: '#565f89', fontSize: '0.7rem', flexShrink: 0 }}>{shortTime(event.occurred_at)}</span>
                          </div>
                          <div style={{ marginLeft: '1.7rem', marginTop: '0.15rem' }}>
                            <EventDetail event={event} />
                          </div>
                        </div>
                      )
                    })}
                  </Stack>
                </Tile>
              </Stack>
            )}
          </Section>
        </Column>
      </Grid>

      {busy && <Loading withOverlay={false} />}
    </div>
  )
}
