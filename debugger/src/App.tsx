import { type FormEvent, type ReactNode, useCallback, useEffect, useState } from 'react'
import { api, API_BASE } from './api'
import { eventKey, eventLabel, eventTime, executionIdOf, frameIdOf, unwrapEvents } from './timeline'
import type { Branch, Execution, Frame, FrameSection, FrpEvent, Json, RenderResponse } from './types'

const SECTIONS: FrameSection[] = ['focus', 'map', 'periphery', 'procedures', 'recent']

function JsonView({ value, empty = 'No data' }: { value: unknown; empty?: string }) {
  if (value === undefined || value === null) return <p className="muted empty">{empty}</p>
  return <pre>{typeof value === 'string' ? value : JSON.stringify(value, null, 2)}</pre>
}

function Status({ loading, error, children }: { loading: boolean; error?: string; children: ReactNode }) {
  if (loading) return <div className="status" role="status"><span className="spinner" /> Loading…</div>
  if (error) return <div className="status error" role="alert">{error}</div>
  return <>{children}</>
}

function App() {
  const [episodeInput, setEpisodeInput] = useState('')
  const [episodeId, setEpisodeId] = useState('')
  const [limit, setLimit] = useState(100)
  const [events, setEvents] = useState<FrpEvent[]>([])
  const [eventsBusy, setEventsBusy] = useState(false)
  const [eventsError, setEventsError] = useState('')
  const [frame, setFrame] = useState<Frame | null>(null)
  const [frameBusy, setFrameBusy] = useState(false)
  const [frameError, setFrameError] = useState('')
  const [selectedFrameId, setSelectedFrameId] = useState('')
  const [execution, setExecution] = useState<Execution | null>(null)
  const [executionBusy, setExecutionBusy] = useState(false)
  const [executionError, setExecutionError] = useState('')
  const [rendered, setRendered] = useState<RenderResponse | null>(null)
  const [actionBusy, setActionBusy] = useState('')
  const [actionError, setActionError] = useState('')
  const [replay, setReplay] = useState<unknown>(null)
  const [blameQuery, setBlameQuery] = useState('')
  const [blame, setBlame] = useState<unknown>(null)
  const [branches, setBranches] = useState<Branch[]>([])

  const loadEvents = useCallback(async (id: string) => {
    if (!id.trim()) return
    setEventsBusy(true); setEventsError('')
    try { setEvents(unwrapEvents(await api.events(id.trim(), limit))) }
    catch (error) { setEventsError(error instanceof Error ? error.message : 'Unable to load timeline') }
    finally { setEventsBusy(false) }
  }, [limit])

  useEffect(() => {
    if (!episodeId) return
    const timer = window.setInterval(() => void loadEvents(episodeId), 5000)
    return () => window.clearInterval(timer)
  }, [episodeId, loadEvents])

  async function openFrame(id: string) {
    setSelectedFrameId(id); setFrameBusy(true); setFrameError(''); setFrame(null)
    setRendered(null); setReplay(null); setBlame(null); setBranches([]); setActionError('')
    try { setFrame(await api.frame(id)) }
    catch (error) { setFrameError(error instanceof Error ? error.message : 'Unable to load frame') }
    finally { setFrameBusy(false) }
  }

  async function openExecution(id: string) {
    setExecutionBusy(true); setExecutionError(''); setExecution(null)
    try { setExecution(await api.execution(id)) }
    catch (error) { setExecutionError(error instanceof Error ? error.message : 'Unable to load execution') }
    finally { setExecutionBusy(false) }
  }

  async function runAction(name: string, operation: () => Promise<void>) {
    setActionBusy(name); setActionError('')
    try { await operation() }
    catch (error) { setActionError(error instanceof Error ? error.message : `Unable to ${name}`) }
    finally { setActionBusy('') }
  }

  const objectiveId = frame?.objective_id
  const frameId = frame?.frame_id ?? frame?.id ?? selectedFrameId

  function submitEpisode(event: FormEvent) {
    event.preventDefault(); const id = episodeInput.trim(); setEpisodeId(id); void loadEvents(id)
  }

  function submitBlame(event: FormEvent) {
    event.preventDefault()
    if (!frameId || !blameQuery.trim()) return
    void runAction('blame', async () => setBlame(await api.blame(frameId, blameQuery.trim())))
  }

  function fork() {
    if (!frameId) return
    void runAction('fork', async () => {
      const definitions = ['counterfactual-a', 'counterfactual-b'].map((label) => ({ branch_id: crypto.randomUUID(), label }))
      const responses = await Promise.all(definitions.map((branch) => api.fork(frameId, branch.branch_id, branch.label)))
      setBranches(definitions.map((branch, index) => ({ ...branch, response: responses[index] })))
    })
  }

  return <div className="app-shell">
    <header className="topbar">
      <div><span className="eyebrow">TEMPORALITY / FRP</span><h1>Cognitive Debugger</h1></div>
      <div className="connection"><span className="pulse" /> API <code>{API_BASE}</code></div>
    </header>

    <main className="workspace">
      <aside className="timeline panel" aria-label="Episode timeline">
        <div className="panel-heading"><div><span className="eyebrow">EPISODE</span><h2>Timeline</h2></div>{episodeId && <button className="icon-button" onClick={() => void loadEvents(episodeId)} aria-label="Refresh timeline">↻</button>}</div>
        <form className="episode-form" onSubmit={submitEpisode}>
          <label htmlFor="episode">Episode ID</label>
          <div className="input-row"><input id="episode" value={episodeInput} onChange={(e) => setEpisodeInput(e.target.value)} placeholder="episode_…" required /><button type="submit">Trace</button></div>
          <label htmlFor="limit">Event limit <output>{limit}</output></label>
          <input id="limit" type="range" min="10" max="500" step="10" value={limit} onChange={(e) => setLimit(Number(e.target.value))} />
        </form>
        <Status loading={eventsBusy} error={eventsError}>
          <ol className="event-list">
            {events.map((item, index) => {
              const fid = frameIdOf(item); const xid = executionIdOf(item)
              return <li key={eventKey(item, index)} className={fid === selectedFrameId ? 'selected' : ''}>
                <div className="event-rail"><span className="event-dot" /><span /></div>
                <div className="event-card">
                  <span className="event-time">{eventTime(item) ? new Date(eventTime(item)).toLocaleString() : `#${index + 1}`}</span>
                  <strong>{eventLabel(item)}</strong>
                  <div className="event-links">{fid && <button onClick={() => void openFrame(fid)}>Frame {fid}</button>}{xid && <button onClick={() => void openExecution(xid)}>Execution {xid}</button>}</div>
                </div>
              </li>
            })}
            {!events.length && !eventsBusy && <li className="empty-state">Enter an episode ID to inspect its causal trace.</li>}
          </ol>
        </Status>
      </aside>

      <section className="inspector panel" aria-label="Frame inspector">
        <div className="panel-heading inspector-heading"><div><span className="eyebrow">SELECTED FRAME</span><h2>{selectedFrameId || 'No frame selected'}</h2></div>{objectiveId && <div className="objective"><span>Objective</span><code>{objectiveId}</code></div>}</div>
        <Status loading={frameBusy} error={frameError}>
          {!frame ? <div className="hero-empty"><div className="frame-glyph">⌗</div><h3>Select a frame event</h3><p>Inspect cognition boundaries, evidence, token use, and causal provenance.</p></div> : <>
            <div className="actionbar" aria-label="Frame actions">
              <button onClick={() => void runAction('render', async () => setRendered(await api.render(frameId, objectiveId)))} disabled={!!actionBusy}>Render</button>
              <button onClick={() => void runAction('replay', async () => setReplay(await api.replay(frameId)))} disabled={!!actionBusy}>↶ Replay from here</button>
              <button onClick={fork} disabled={!!actionBusy}>⑂ Fork ×2</button>
              {actionBusy && <span className="muted" role="status"><span className="spinner" /> {actionBusy}…</span>}
            </div>
            {actionError && <div className="status error" role="alert">{actionError}</div>}
            <div className="section-grid">
              {SECTIONS.map((section) => <article className={`data-card ${section === 'focus' ? 'featured' : ''}`} key={section}><h3><span>{section === 'focus' ? '◎' : '◇'}</span>{section}</h3><JsonView value={frame.sections?.[section] ?? frame[section]} /></article>)}
            </div>
            <div className="evidence-grid">
              <article className="data-card warning"><h3><span>↗</span>outside_frame</h3><JsonView value={frame.outside_frame} /></article>
              <article className="data-card"><h3><span>⌁</span>provenance</h3><JsonView value={frame.provenance} /></article>
              <article className="data-card"><h3><span>◴</span>token usage</h3><JsonView value={frame.token_usage ?? frame.usage} /></article>
            </div>
            {(rendered || replay) && <div className="result-grid">{rendered && <article className="result-card"><h3>Rendered cognition</h3><JsonView value={rendered.rendered ?? rendered.output ?? rendered.content ?? rendered} /></article>}{replay !== null && <article className="result-card"><h3>Replay result</h3><JsonView value={replay} /></article>}</div>}
            <article className="tool-card"><div><span className="eyebrow">CAUSAL QUERY</span><h3>Blame analysis</h3><p>Ask why a value, choice, or omission occurred in this frame.</p></div><form onSubmit={submitBlame}><textarea value={blameQuery} onChange={(e) => setBlameQuery(e.target.value)} placeholder="Why was this procedure selected?" aria-label="Blame question" required /><button disabled={!!actionBusy}>Analyze blame</button></form>{blame !== null && <JsonView value={blame} />}</article>
            {branches.length > 0 && <article className="tool-card"><span className="eyebrow">COUNTERFACTUALS</span><h3>Forked branches</h3><div className="branch-grid">{branches.map((branch) => <div className="branch" key={branch.branch_id}><strong>{branch.label}</strong><code>{branch.branch_id}</code><JsonView value={branch.response} /></div>)}</div></article>}
          </>}
        </Status>
      </section>

      <aside className={`execution-drawer panel ${execution || executionBusy || executionError ? 'open' : ''}`} aria-label="Execution details">
        <div className="panel-heading"><div><span className="eyebrow">EXECUTION</span><h2>Details</h2></div><button className="icon-button" onClick={() => { setExecution(null); setExecutionError('') }} aria-label="Close execution details">×</button></div>
        <Status loading={executionBusy} error={executionError}><JsonView value={execution} /></Status>
      </aside>
    </main>
  </div>
}

export default App
