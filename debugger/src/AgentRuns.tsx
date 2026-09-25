import { useCallback, useEffect, useMemo, useState } from 'react'
import { API_BASE } from './api'
import { observationApi, type ObservationEvent } from './observationApi'
import Markdown from './Markdown'

const KERNEL_API = '/kernel-api'
function message(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }
function json(value: unknown) { return JSON.stringify(value, null, 2) }
function runID(event: ObservationEvent) { return event.context?.run ?? '' }
function pendingApprovals(events: ObservationEvent[]) {
  const ended = new Set(events.filter((event) => ['approval.granted', 'approval.rejected', 'approval.timed_out'].includes(event.type)).map((event) => event.data?.operation_id))
  return events.filter((event) => event.type === 'approval.requested' && !ended.has(event.data?.operation_id))
}

export default function AgentRuns() {
  const params = new URLSearchParams(window.location.search)
  const [project, setProject] = useState(params.get('project') ?? 'temporality-live-verification')
  const [runs, setRuns] = useState<ObservationEvent[]>([])
  const [selected, setSelected] = useState(params.get('run') ?? '')
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
          let response = await fetch(`${KERNEL_API}/v1/agent/runs/${encodeURIComponent(id)}?${query}`)
          if (!response.ok && event.data?.workflow === 'LeadCoderReviewerQA') {
            response = await fetch(`${KERNEL_API}/v1/agent/examples/lead-coder-reviewer-qa/runs/${encodeURIComponent(id)}?${query}`)
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
      let response = await fetch(`${KERNEL_API}${resultPath}?${sourceQuery}`)
      if (!response.ok && teamRun) response = await fetch(`${KERNEL_API}/v1/agent/runs/${encodeURIComponent(run)}?${sourceQuery}`)
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
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ operation_id: operationID, arguments_hash: operation?.arguments_hash ?? '', approved, actor_id: 'human-ui', reason: reason.trim() }),
      })
      if (!response.ok) throw new Error(`${response.status} ${await response.text()}`)
      await loadRun(selected, true)
    } catch (failure) { setError(message(failure)) }
    finally { setBusy(false) }
  }

  const answer = (result?.result as Record<string, unknown> | undefined)?.answer
  const stages = (result?.result as Record<string, unknown> | undefined)?.stages
  return <div className="observability-shell agent-runs-shell">
    <header className="topbar obs-topbar"><div><span className="eyebrow">TEMPORALITY / AGENT KERNEL</span><h1>Agent runs</h1></div><div className="header-actions"><span className="connection">Kernel <code>:8090</code> · Events <code>{API_BASE}</code></span><a className="obs-link" href="/experience">Experience</a><a className="obs-link" href="/observability">Knowledge</a></div></header>
    <form className="obs-controls agent-controls" onSubmit={(event) => { event.preventDefault(); setSelected(''); setTimeline([]); void loadRuns() }}><label>Project ID<input value={project} onChange={(event) => setProject(event.target.value)} placeholder="temporality-live-verification" required /></label><button className="primary" disabled={busy}>{busy ? 'Loading…' : 'Load runs'}</button></form>
    {error && <div className="obs-error" role="alert">{error}<small>Проверьте, что Agent Kernel доступен на localhost:8090, а Runtime — на localhost:8080.</small></div>}
    <main className="agent-grid">
      <aside className="obs-panel agent-list"><header><span className="eyebrow">RUN HISTORY</span><strong>{runs.length} runs</strong></header>
        {!runs.length ? <p className="obs-empty">Нет запусков для этого проекта. Сначала отправьте run через ваш harness/Agent Kernel, затем нажмите Load runs.</p> : runs.map((event) => { const id = runID(event); return <button key={event.event_id} className={`agent-run ${selected === id ? 'selected' : ''}`} onClick={() => setSelected(id)}><time>{new Date(event.occurred_at).toLocaleString()}</time><strong>{id}</strong><span>{String(event.data?.role ?? 'agent')} · {String(event.data?.task_id ?? '')}</span></button> })}
      </aside>
      <section className="obs-panel agent-detail"><header><span className="eyebrow">RESULT & TRACE</span><strong>{String(result?.status ?? (selected ? 'loading' : 'select a run'))}</strong></header>
        {!selected ? <p className="obs-empty">Выберите запуск слева. Здесь появятся ответ агента, события и запросы на подтверждение.</p> : <>
          {typeof answer === 'string' && <article className="agent-answer"><h2>Agent answer</h2><Markdown content={answer} /></article>}
          {Array.isArray(stages) && stages.map((stage, index) => {
            const item = stage as Record<string, unknown>
            return <article className="agent-stage" key={`${String(item.run_id ?? item.role ?? index)}`}><h3>{String(item.role ?? `Stage ${index + 1}`)} <span>{String(item.status ?? '')}</span></h3>{typeof item.answer === 'string' && <Markdown content={item.answer} />}</article>
          })}
          {result?.result !== undefined && !answer && <details open><summary>Run result</summary><pre>{json(result.result)}</pre></details>}
          {pending.map((event) => { const details = event.data?.details as Record<string, unknown> | undefined; const operation = event.data?.operation as Record<string, unknown> | undefined; const operationArgs = operation?.arguments as Record<string, unknown> | undefined; const command = operationArgs?.command; const risk = event.data?.risk as Record<string, unknown> | undefined; const redaction = event.data?.redaction as Record<string, unknown> | undefined; return <article className="agent-approval" key={event.event_id}><h2>Approval required</h2><p>{String(operation?.summary ?? event.data?.reason ?? event.data?.action ?? 'Agent requested approval')}</p>{details && <><strong>{String(details.tool)}{details.workspace_read_only ? ' · read only' : ''} · risk {String(risk?.level ?? 'unknown')}</strong><pre>{Array.isArray(command) ? command.join(' ') : json(operation?.arguments ?? details)}</pre><small>Workspace: {String(details.workspace ?? 'not specified')} · arguments {String(operation?.arguments_hash ?? 'hash unavailable')} · redacted {String(redaction?.applied ?? false)}{redaction?.truncated ? ' · preview truncated' : ''}</small></>}<label>Decision note<input value={reason} onChange={(change) => setReason(change.target.value)} /></label><div className="agent-actions"><button className="primary" disabled={busy} onClick={() => void decide(event, true)}>Approve and run</button><button disabled={busy} onClick={() => void decide(event, false)}>Reject</button></div></article> })}
          <ol className="agent-timeline">{[...timeline].reverse().map((event) => <li key={`${event.source.id}:${event.event_id}`}><time>{new Date(event.occurred_at).toLocaleString()}</time><strong>{event.type}</strong><details><summary>event data</summary><pre>{json(event)}</pre></details></li>)}</ol>
        </>}
      </section>
    </main>
  </div>
}
