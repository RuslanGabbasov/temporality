import { useCallback, useEffect, useMemo, useState } from 'react'
import { API_BASE, authHeaders } from './api'
import { observationApi, type ObservationEvent } from './observationApi'
import Markdown from './Markdown'
import Token from './Token'
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
import { Time, Play } from '@carbon/icons-react'

const KERNEL_API = '/kernel-api'
function message(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }
function json(value: unknown) { return JSON.stringify(value, null, 2) }
function runID(event: ObservationEvent) { return event.context?.run ?? '' }
function pendingApprovals(events: ObservationEvent[]) {
  const ended = new Set(events.filter((event) => ['approval.granted', 'approval.rejected', 'approval.timed_out'].includes(event.type)).map((event) => event.data?.operation_id))
  return events.filter((event) => event.type === 'approval.requested' && !ended.has(event.data?.operation_id))
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
      if (!response.ok) { const detail = await response.text(); throw new Error(`${response.status} ${detail}`) }
      setResult(await response.json() as Record<string, unknown>)
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
        <InlineNotification
          kind="error"
          title="Error"
          subtitle={error}
          onClose={() => setError('')}
          lowContrast
          style={{ marginBottom: '1rem' }}
        />
      )}

      <Grid>
        <Column sm={4} md={8} lg={16}>
          <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'flex-end', marginBottom: '1rem' }}>
            <Button onClick={() => { setSelected(''); setTimeline([]); void loadRuns() }} disabled={busy}>
              Load runs
            </Button>
            <Token />
          </div>
        </Column>
      </Grid>

      <Grid>
        {/* Run list */}
        <Column sm={4} md={3} lg={4}>
          <Section level={3}>
            <Heading>Run History ({runs.length})</Heading>
            <Stack gap={2}>
              {runs.length === 0 && <Tile><p>No runs for this project</p></Tile>}
              {runs.map((event) => {
                const id = runID(event)
                return (
                  <Tile
                    key={event.event_id}
                    onClick={() => setSelected(id)}
                    className={`workspace-tile ${selected === id ? 'selected' : ''}`}
                  >
                    <strong>{id}</strong>
                    <br />
                    <Tag type="gray" size="sm">{String(event.data?.role ?? 'agent')}</Tag>
                    <small style={{ marginLeft: '0.5rem', color: '#7e8a9c' }}>
                      {new Date(event.occurred_at).toLocaleString()}
                    </small>
                  </Tile>
                )
              })}
            </Stack>
          </Section>
        </Column>

        {/* Run detail */}
        <Column sm={4} md={5} lg={12}>
          <Section level={3}>
            <Heading>
              {String(result?.status ?? (selected ? 'Loading…' : 'Select a run'))}
            </Heading>

            {!selected ? (
              <Tile><p>Select a run from the list</p></Tile>
            ) : (
              <Stack gap={3}>
                {typeof answer === 'string' && (
                  <Tile>
                    <Heading>Agent Answer</Heading>
                    <Markdown content={answer} />
                  </Tile>
                )}

                {Array.isArray(stages) && stages.map((stage, index) => {
                  const item = stage as Record<string, unknown>
                  return (
                    <Tile key={`${String(item.run_id ?? item.role ?? index)}`}>
                      <Heading>{String(item.role ?? `Stage ${index + 1}`)}</Heading>
                      <Tag type="gray" size="sm">{String(item.status ?? '')}</Tag>
                      {typeof item.answer === 'string' && <Markdown content={item.answer} />}
                    </Tile>
                  )
                })}

                {result?.result !== undefined && !answer && (
                  <Tile>
                    <Heading>Run Result</Heading>
                    <pre style={{ background: '#121823', padding: '1rem', fontSize: '0.75rem', overflow: 'auto' }}>
                      {json(result.result)}
                    </pre>
                  </Tile>
                )}

                {pending.map((event) => {
                  const details = event.data?.details as Record<string, unknown> | undefined
                  const operation = event.data?.operation as Record<string, unknown> | undefined
                  const operationArgs = operation?.arguments as Record<string, unknown> | undefined
                  const command = operationArgs?.command
                  const risk = event.data?.risk as Record<string, unknown> | undefined
                  const redaction = event.data?.redaction as Record<string, unknown> | undefined
                  return (
                    <Tile key={event.event_id} style={{ borderLeft: '3px solid #e6b85c' }}>
                      <Heading>Approval Required</Heading>
                      <p>{String(operation?.summary ?? event.data?.reason ?? event.data?.action ?? 'Agent requested approval')}</p>
                      {details && (
                        <>
                          <Tag type="warm-gray" size="sm">{String(details.tool)}</Tag>
                          {details.workspace_read_only && <Tag type="gray" size="sm">read only</Tag>}
                          <Tag type="gray" size="sm">risk {String(risk?.level ?? 'unknown')}</Tag>
                          <pre style={{ background: '#121823', padding: '0.5rem', fontSize: '0.75rem', marginTop: '0.5rem' }}>
                            {Array.isArray(command) ? command.join(' ') : json(operation?.arguments ?? details)}
                          </pre>
                          <small style={{ color: '#7e8a9c' }}>
                            Workspace: {String(details.workspace ?? 'not specified')} · arguments {String(operation?.arguments_hash ?? 'hash unavailable')} · redacted {String(redaction?.applied ?? false)}
                          </small>
                        </>
                      )}
                      <div style={{ marginTop: '1rem' }}>
                        <TextInput
                          id="decision-note"
                          labelText="Decision note"
                          value={reason}
                          onChange={(e: React.ChangeEvent<HTMLInputElement>) => setReason(e.target.value)}
                        />
                      </div>
                      <Stack orientation="horizontal" gap={2} style={{ marginTop: '0.75rem' }}>
                        <Button onClick={() => void decide(event, true)} disabled={busy}>Approve</Button>
                        <Button kind="secondary" onClick={() => void decide(event, false)} disabled={busy}>Reject</Button>
                      </Stack>
                    </Tile>
                  )
                })}

                <Tile>
                  <Heading>Timeline</Heading>
                  <Stack gap={1}>
                    {[...timeline].reverse().map((event) => (
                      <div key={`${event.source.id}:${event.event_id}`} style={{ display: 'flex', gap: '0.5rem', fontSize: '0.75rem' }}>
                        <Time size={16} style={{ flexShrink: 0 }} />
                        <small style={{ color: '#7e8a9c', minWidth: '8rem' }}>{new Date(event.occurred_at).toLocaleString()}</small>
                        <strong>{event.type}</strong>
                      </div>
                    ))}
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