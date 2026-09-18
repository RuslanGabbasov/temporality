import { useMemo } from 'react'
import type { FrpEvent } from './types'
import { buildTrajectory, claimKnowledgeAt, memoryDelta } from './trajectory'

/** Pivot Этап 5: investigation history views for the Cognitive Debugger.
 * Trajectory = focus moves (with triggers) and claim lifecycles over time.
 * Memory Delta = what the step that produced the selected frame changed.
 * Both derive purely from the episode event list the debugger already has. */

const stateGlyph: Record<string, string> = { candidate: '○', supported: '●', refuted: '✕', superseded: '→', focus: '➔' }

export function TrajectoryCard({ events }: { events: FrpEvent[] }) {
  const lanes = useMemo(() => buildTrajectory(events), [events])
  if (!lanes.length) return null
  return <article className="data-card trajectory-card" data-testid="trajectory">
    <h3><span>⇢</span>Investigation trajectory</h3>
    <p className="card-hint">Focus moves with their triggers, and the lifecycle of every hypothesis (○ born · ● confirmed · ✕ refuted · → superseded).</p>
    <div className="trajectory-lanes">
      {lanes.map((lane) => <div className={`trajectory-lane ${lane.kind}`} key={lane.id}>
        <div className="lane-label" title={lane.label}><span className="lane-kind">{lane.kind === 'focus' ? '◎ focus' : '◇ claim'}</span><strong>{lane.label}</strong></div>
        <ol className="lane-nodes">
          {lane.nodes.map((node) => <li key={node.eventId} className={`node ${node.kind}`} title={node.at}>
            <span className="node-glyph">{stateGlyph[node.kind] ?? '·'}</span>
            <span className="node-body">
              <span className="node-kind">{node.kind}{node.trigger ? ` · ${node.trigger}` : ''}</span>
              {node.kind === 'focus'
                ? <span className="node-label">{node.label}</span>
                : <span className="node-label">{node.label}</span>}
              {node.evidence?.length ? <span className="node-evidence">evidence: {node.evidence.join(', ')}</span> : null}
              {node.frameId ? <span className="node-frame">frame {node.frameId.slice(0, 8)}</span> : null}
            </span>
          </li>)}
        </ol>
      </div>)}
    </div>
  </article>
}

export function MemoryDeltaCard({ events, frameId }: { events: FrpEvent[]; frameId?: string }) {
  const delta = useMemo(() => memoryDelta(events, frameId), [events, frameId])
  const knowledge = useMemo(() => claimKnowledgeAt(events, frameId), [events, frameId])
  const empty = !delta.added.length && !delta.confirmed.length && !delta.refuted.length && !delta.superseded.length && !delta.focus
  return <article className="data-card delta-card" data-testid="memory-delta">
    <h3><span>Δ</span>Memory delta {frameId ? `· ${frameId.slice(0, 8)}` : ''}</h3>
    {empty && <p className="card-hint">This step changed no knowledge{frameId ? '' : ' — select a frame'}. The agent held {knowledge.length} claim{knowledge.length === 1 ? '' : 's'} at this point.</p>}
    {delta.focus && <p className="delta-focus"><span className={`trigger ${delta.focus.trigger}`}>{delta.focus.trigger ?? 'deliberate'}</span> {delta.focus.from} ➔ <strong>{delta.focus.to}</strong>{delta.focus.evidence?.length ? <small> · evidence: {delta.focus.evidence.join(', ')}</small> : null}</p>}
    <div className="delta-groups">
      {([['added', delta.added], ['confirmed', delta.confirmed], ['refuted', delta.refuted], ['superseded', delta.superseded]] as const).map(([kind, items]) => items.length > 0 && <div className={`delta-group ${kind}`} key={kind}>
        <span className="delta-kind">{kind}</span>
        <ul>{items.map((claim) => <li key={`${kind}-${claim.claimId}`}>{stateGlyph[claim.state]} {claim.proposition}{typeof claim.confidence === 'number' ? <small> · {Math.round(claim.confidence * 100)}%</small> : null}</li>)}</ul>
      </div>)}
    </div>
    {knowledge.length > 0 && <details className="known-at"><summary>What the agent knew here ({knowledge.length})</summary>
      <ul>{knowledge.map((claim) => <li key={`k-${claim.claimId}`}><span className={`state ${claim.state}`}>{stateGlyph[claim.state]} {claim.state}</span> {claim.proposition}</li>)}</ul>
    </details>}
  </article>
}
