import { useCallback, useEffect, useRef, useState } from 'react'
import { Button, InlineNotification, Tag, TextInput } from '@carbon/react'
import { observationApi, runEvents, type ExploreResult } from '../observationApi'
import { extractExploreCalls, parseExploreArguments, type AgentExploreCall } from './agentExploreCalls'
import { stateLabel } from './shared'
import { useT } from '../i18n'

/** Agent research results (ТЗ §12): the knowledge_explore calls of one run,
 *  replayed against the current projection. The replay is honest about being
 *  a replay — the agent saw the base as it was then; the result shown here is
 *  the base as it is now (§12.2-12.3). */
export default function AgentResearch({ project, onOpenDossier }: { project: string; onOpenDossier: (id: string) => void }) {
  const t = useT()
  const [runInput, setRunInput] = useState('')
  const [researchRuns, setResearchRuns] = useState<{ id: string; at: string; calls: AgentExploreCall[] }[]>([])
  const [scanLoading, setScanLoading] = useState(false)
  const [calls, setCalls] = useState<AgentExploreCall[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [loadedRun, setLoadedRun] = useState('')
  const [replays, setReplays] = useState<Map<string, ExploreResult>>(new Map())
  const [replayBusy, setReplayBusy] = useState('')
  const [replayErrors, setReplayErrors] = useState<Map<string, string>>(new Map())
  const seq = useRef(0)

  // The tab opens on WHAT THE AGENTS ACTUALLY RESEARCHED: scan the recent
  // tool.started events for knowledge_explore calls and group them by run —
  // a picker over all runs forced the user to guess which of the thousands
  // ever looked at knowledge.
  const loadResearchRuns = useCallback(async () => {
    if (!project.trim()) { setResearchRuns([]); return }
    setScanLoading(true)
    try {
      const page = await observationApi.events(project.trim(), undefined, undefined, undefined, { type: 'tool.started', limit: 500 })
      const byRun = new Map<string, { id: string; at: string; calls: AgentExploreCall[] }>()
      for (const event of page.events ?? []) {
        const data = event.data as Record<string, unknown> | undefined
        if (!data || data.tool !== 'knowledge_explore') continue
        const call = parseExploreArguments(data)
        if (!call) continue
        call.eventID = event.event_id
        call.at = event.occurred_at
        const run = event.context?.run ?? ''
        if (!run) continue
        const entry = byRun.get(run)
        if (entry) entry.calls.push(call)
        else byRun.set(run, { id: run, at: event.occurred_at, calls: [call] })
      }
      const runs = [...byRun.values()]
        .map((entry) => ({ ...entry, calls: [...entry.calls].sort((a, b) => a.at.localeCompare(b.at)) }))
        .sort((a, b) => b.at.localeCompare(a.at))
        .slice(0, 50)
      setResearchRuns(runs)
    } catch { setResearchRuns([]) } finally { setScanLoading(false) }
  }, [project])

  useEffect(() => { void loadResearchRuns() }, [loadResearchRuns])

  async function load(runID?: string) {
    // Explicit id first: the run list calls load(entry.id) — reading runInput
    // here would see the stale (pre-setState) value and silently no-op on the
    // first click.
    const run = (runID ?? runInput).trim()
    if (!run) return
    const current = ++seq.current
    setLoading(true); setError(''); setCalls([]); setReplays(new Map()); setReplayErrors(new Map())
    setLoadedRun(run)
    try {
      const events = await runEvents(run, project.trim() || undefined)
      if (seq.current !== current) return
      setCalls(extractExploreCalls(events))
    } catch (e) {
      if (seq.current === current) setError(e instanceof Error ? e.message : 'Request failed')
    } finally {
      if (seq.current === current) setLoading(false)
    }
  }

  async function replay(call: AgentExploreCall) {
    setReplayBusy(call.eventID)
    setReplayErrors((current) => { const next = new Map(current); next.delete(call.eventID); return next })
    try {
      const result = await observationApi.explore({
        project: project.trim(),
        operation: call.operation as 'orient' | 'search' | 'expand' | 'trace' | 'compare' | 'synthesize',
        question: call.question || undefined,
        targets: call.targets.length ? call.targets : undefined,
        relations: call.relations.length ? call.relations : undefined,
        max_depth: call.maxDepth || undefined,
        max_items: call.maxItems || undefined,
        include_candidates: call.includeCandidates || undefined,
      })
      setReplays((current) => { const next = new Map(current); next.set(call.eventID, result); return next })
    } catch (e) {
      setReplayErrors((current) => {
        const next = new Map(current)
        next.set(call.eventID, e instanceof Error ? e.message : 'Request failed')
        return next
      })
    } finally {
      setReplayBusy('')
    }
  }

  const opLabel = useCallback((operation: string) =>
    t(`explorer.op_${operation}`) !== `explorer.op_${operation}` ? t(`explorer.op_${operation}`) : operation, [t])

  return (
    <div>
      <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'baseline', marginBottom: '0.4rem', flexWrap: 'wrap' }}>
        <div style={{ fontSize: '0.8rem', fontWeight: 600 }}>{t('explorer.agent_title') ?? 'Agent research'}</div>
        <div style={{ fontSize: '0.7rem', color: 'var(--tm-muted)' }}>{t('explorer.agent_hint') ?? ''}</div>
      </div>

      {/* The research-run list: what the agents actually explored, newest
          first. Click a run — its calls expand in place with replay buttons.
          The manual run-id entry is a secondary path below the list. */}
      <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', marginBottom: '0.4rem' }}>
        <div style={{ fontSize: '0.75rem', fontWeight: 600 }}>{t('explorer.agent_researched_runs') ?? 'Runs that explored knowledge'}</div>
        <Button size="sm" kind="ghost" onClick={() => void loadResearchRuns()} disabled={scanLoading}>↻</Button>
      </div>

      {error && <InlineNotification kind="error" title="Error" subtitle={error} onClose={() => setError('')} lowContrast style={{ marginBottom: '0.75rem' }} />}

      {scanLoading && <div style={{ color: 'var(--tm-muted)', fontSize: '0.78rem', marginBottom: '0.5rem' }}>…</div>}
      {!scanLoading && researchRuns.length === 0 && !loadedRun && (
        <div style={{ border: '1px dashed var(--tm-border)', borderRadius: 'var(--tm-radius-md)', padding: '1rem', color: 'var(--tm-muted)', fontSize: '0.8rem' }}>
          {t('explorer.agent_no_research_runs') ?? 'В последних прогонах агент ничего не исследовал через knowledge_explore. Укажите id рана вручную.'}
        </div>
      )}

      {researchRuns.length > 0 && (
        <div style={{ border: '1px solid var(--tm-border)', borderRadius: 'var(--tm-radius-md)', overflow: 'hidden', marginBottom: '0.75rem', maxHeight: '40vh', overflowY: 'auto' }}>
          {researchRuns.map((entry, index) => {
            const expanded = loadedRun === entry.id
            const lastQuestion = entry.calls[entry.calls.length - 1]?.question || entry.calls[0]?.operation || ''
            return (
              <div key={entry.id} style={{ borderTop: index > 0 ? '1px solid var(--tm-border)' : 'none' }}>
                <div
                  role="button" tabIndex={0}
                  onClick={() => { if (expanded) { setLoadedRun(''); setCalls([]) } else void load(entry.id) }}
                  onKeyDown={(e) => { if (e.key === 'Enter') void load(entry.id) }}
                  style={{ padding: '0.45rem 0.6rem', cursor: 'pointer', background: expanded ? 'var(--tm-elevated)' : 'transparent', display: 'flex', gap: '0.5rem', alignItems: 'baseline', flexWrap: 'wrap' }}
                >
                  <Tag size="sm" type="cool-gray">{entry.calls.length} {t('explorer.agent_calls') ?? 'calls'}</Tag>
                  <span style={{ fontFamily: 'var(--tm-font-mono)', fontSize: '0.72rem', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', maxWidth: '40%' }}>{entry.id}</span>
                  <span style={{ fontSize: '0.72rem', color: 'var(--tm-text-2)', flex: 1, minWidth: '10rem', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{lastQuestion}</span>
                  <small style={{ color: 'var(--tm-muted)', fontSize: '0.68rem', marginLeft: 'auto', flexShrink: 0 }}>{new Date(entry.at).toLocaleString()}</small>
                </div>
                {expanded && (
                  <div style={{ padding: '0 0.6rem 0.6rem', background: 'var(--tm-elevated)' }}>
                    {calls.length === 0 && loading && <div style={{ color: 'var(--tm-muted)', fontSize: '0.75rem' }}>…</div>}
                    {calls.map((call) => <CallCard key={call.eventID} call={call} replays={replays} replayBusy={replayBusy} replayErrors={replayErrors} onReplay={replay} onOpenDossier={onOpenDossier} t={t} opLabel={opLabel} />)}
                  </div>
                )}
              </div>
            )
          })}
        </div>
      )}

      {/* Secondary path: a specific run by id (the list covers the recent 50). */}
      <div style={{ display: 'flex', gap: '0.4rem', alignItems: 'center' }}>
        <TextInput
          id="agent-research-run"
          size="sm" labelText="" hideLabel
          style={{ width: '240px' }}
          placeholder={t('explorer.agent_run_placeholder') ?? 'Run id…'}
          value={runInput}
          onChange={(e) => setRunInput(e.target.value)}
          onKeyDown={(e) => { if (e.key === 'Enter') void load() }}
        />
        <Button size="sm" kind="ghost" onClick={() => void load()} disabled={loading || !runInput.trim()}>
          {loading ? '…' : (t('explorer.agent_load') ?? 'Load')}
        </Button>
      </div>

      {loadedRun && researchRuns.every((entry) => entry.id !== loadedRun) && calls.length === 0 && !loading && (
        <div style={{ border: '1px dashed var(--tm-border)', borderRadius: 'var(--tm-radius-md)', padding: '1rem', color: 'var(--tm-muted)', fontSize: '0.8rem' }}>
          {t('explorer.agent_no_calls') ?? ''}
        </div>
      )}

      {loadedRun && calls.length > 0 && researchRuns.every((entry) => entry.id !== loadedRun) && calls.map((call) => (
        <CallCard key={call.eventID} call={call} replays={replays} replayBusy={replayBusy} replayErrors={replayErrors} onReplay={replay} onOpenDossier={onOpenDossier} t={t} opLabel={opLabel} />
      ))}
    </div>
  )
}

/** One knowledge_explore call: arguments as the agent sent them + replay. */
function CallCard({ call, replays, replayBusy, replayErrors, onReplay, onOpenDossier, t, opLabel }: {
  call: AgentExploreCall
  replays: Map<string, ExploreResult>
  replayBusy: string
  replayErrors: Map<string, string>
  onReplay: (call: AgentExploreCall) => void
  onOpenDossier: (id: string) => void
  t: (key: string, vars?: Record<string, string>) => string
  opLabel: (operation: string) => string
}) {
  const replayResult = replays.get(call.eventID)
  const replayError = replayErrors.get(call.eventID)
  return (
    <div style={{ border: '1px solid var(--tm-border)', borderRadius: 'var(--tm-radius-md)', padding: '0.6rem 0.75rem', marginBottom: '0.5rem', background: 'var(--tm-bg)' }}>
      <div style={{ display: 'flex', alignItems: 'flex-start', gap: '0.5rem', flexWrap: 'wrap' }}>
        <Tag type="cyan" size="sm">{opLabel(call.operation)}</Tag>
        <div style={{ flex: 1, fontSize: '0.82rem', lineHeight: 1.45, minWidth: '200px' }}>
          {call.question || call.targets.join(', ') || '—'}
        </div>
        <small style={{ color: 'var(--tm-muted)', fontSize: '0.7rem' }}>
          {new Date(call.at).toLocaleString()}{call.turn ? ` · ${t('knowledge.turn') ?? 'turn'} ${call.turn}` : ''}
        </small>
      </div>
      {(call.targets.length > 0 || call.relations.length > 0 || call.maxDepth || call.maxItems || call.includeCandidates) && (
        <div style={{ display: 'flex', gap: '0.3rem', flexWrap: 'wrap', marginTop: '0.3rem' }}>
          {call.targets.length > 0 && <Tag size="sm" type="cool-gray">targets: {call.targets.length}</Tag>}
          {call.relations.length > 0 && <Tag size="sm" type="cool-gray">{call.relations.join('+')}</Tag>}
          {call.maxDepth > 0 && <Tag size="sm" type="cool-gray">depth {call.maxDepth}</Tag>}
          {call.maxItems > 0 && <Tag size="sm" type="cool-gray">max {call.maxItems}</Tag>}
          {call.includeCandidates && <Tag size="sm" type="cool-gray">candidates</Tag>}
        </div>
      )}
      <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'center', marginTop: '0.4rem' }}>
        <Button size="sm" kind="secondary" onClick={() => onReplay(call)} disabled={replayBusy === call.eventID}>
          {replayBusy === call.eventID ? '…' : (t('explorer.agent_replay') ?? 'Replay now')}
        </Button>
        {replayResult && (
          <small style={{ color: 'var(--tm-muted)', fontSize: '0.7rem' }}>
            {t('explorer.agent_replayed') ?? 'replayed'} · {new Date().toLocaleTimeString()} · {replayResult.completeness}
          </small>
        )}
      </div>
      {replayError && <InlineNotification kind="error" title="Replay" subtitle={replayError} lowContrast style={{ marginTop: '0.4rem' }} />}
      {replayResult && <ReplayResult result={replayResult} onOpenDossier={onOpenDossier} t={t} />}
    </div>
  )
}

const STATE_TAG: Record<string, 'blue' | 'green' | 'warm-gray' | 'gray' | 'red'> = {
  proposed: 'blue', confirmed: 'green', challenged: 'warm-gray',
  corrected: 'warm-gray', superseded: 'gray', invalidated: 'red',
}

function cardLine(card: { knowledge_id: string; proposition: string; state: string; matched_by?: string[]; score?: number }, t: (key: string, vars?: Record<string, string>) => string, onOpenDossier: (id: string) => void) {
  return (
    <div key={card.knowledge_id} style={{ display: 'flex', alignItems: 'flex-start', gap: '0.4rem', padding: '0.3rem 0', fontSize: '0.8rem' }}>
      <Tag type={STATE_TAG[card.state] ?? 'gray'} size="sm">{stateLabel(card.state, t)}</Tag>
      <div style={{ flex: 1, minWidth: 0 }}>
        {card.proposition}
        {(card.matched_by ?? []).length > 0 && (
          <small style={{ display: 'block', color: 'var(--tm-muted)', fontSize: '0.68rem' }}>
            {(card.matched_by ?? []).join(' · ')}{card.score !== undefined && card.score > 0 ? ` · ${card.score.toFixed(2)}` : ''}
          </small>
        )}
      </div>
      <button onClick={() => onOpenDossier(card.knowledge_id)} style={{ background: 'none', border: 'none', color: 'var(--tm-teal)', cursor: 'pointer', fontSize: '0.7rem', padding: 0, flexShrink: 0 }}>
        {t('explorer.dossier_open') ?? 'Dossier'}
      </button>
    </div>
  )
}

function section(title: string, count: number, children: React.ReactNode) {
  if (count === 0) return null
  return (
    <div style={{ marginTop: '0.5rem' }}>
      <div style={{ fontSize: '0.65rem', textTransform: 'uppercase', letterSpacing: '0.05em', color: 'var(--tm-muted)', marginBottom: '0.25rem' }}>{title} ({count})</div>
      {children}
    </div>
  )
}

function ReplayResult({ result, onOpenDossier, t }: { result: ExploreResult; onOpenDossier: (id: string) => void; t: (key: string, vars?: Record<string, string>) => string }) {
  const synthesis = result.synthesis
  return (
    <div style={{ borderTop: '1px solid var(--tm-border)', marginTop: '0.5rem', paddingTop: '0.5rem' }}>
      {section(t('explorer.agent_areas') ?? 'Areas', (result.areas ?? []).length, (
        <div style={{ display: 'flex', gap: '0.3rem', flexWrap: 'wrap', fontSize: '0.75rem' }}>
          {(result.areas ?? []).slice(0, 20).map((area) => (
            <span key={area.name} style={{ padding: '0.1rem 0.45rem', border: '1px solid var(--tm-border)', borderRadius: '999px', fontSize: '0.7rem' }}>
              {area.name} · {area.knowledge_count}
            </span>
          ))}
        </div>
      ))}

      {section(t('explorer.results') ?? 'Results', (result.results ?? []).length, (
        <div>{(result.results ?? []).map((card) => cardLine(card, t, onOpenDossier))}</div>
      ))}

      {section(t('explorer.relations_title') ?? 'Relations', (result.relations ?? []).length, (
        <div style={{ fontSize: '0.78rem' }}>
          {(result.relations ?? []).map((relation, index) => (
            <div key={index} style={{ padding: '0.2rem 0' }}>
              <button onClick={() => onOpenDossier(relation.from_id)} style={{ background: 'none', border: 'none', color: 'var(--tm-teal)', cursor: 'pointer', fontSize: '0.72rem', padding: 0 }}>…{relation.from_id.slice(-6)}</button>
              {' '}{relation.type}{' '}
              <button onClick={() => onOpenDossier(relation.to_id)} style={{ background: 'none', border: 'none', color: 'var(--tm-teal)', cursor: 'pointer', fontSize: '0.72rem', padding: 0 }}>…{relation.to_id.slice(-6)}</button>
              <small style={{ color: 'var(--tm-muted)' }}> · {relation.basis}</small>
            </div>
          ))}
        </div>
      ))}

      {section(t('explorer.dossier_tab_history') ?? 'Traces', (result.traces ?? []).length, (
        <div>{(result.traces ?? []).map((trace) => cardLine({ knowledge_id: trace.knowledge_id, proposition: trace.proposition, state: trace.state, matched_by: trace.steps ? [`${trace.steps.length} steps`] : [] }, t, onOpenDossier))}</div>
      ))}

      {section(t('explorer.tab_conflicts') ?? 'Conflicts', (result.conflicts ?? []).length, (
        <div style={{ fontSize: '0.78rem' }}>
          {(result.conflicts ?? []).map((conflict, index) => (
            <div key={index} style={{ padding: '0.25rem 0', borderBottom: '1px solid var(--tm-border)' }}>
              <div style={{ display: 'flex', gap: '0.3rem', alignItems: 'center', flexWrap: 'wrap' }}>
                <Tag size="sm" type={conflict.resolved ? 'green' : 'warm-gray'}>{conflict.basis}</Tag>
                {(conflict.knowledge_ids ?? []).map((id) => (
                  <button key={id} onClick={() => onOpenDossier(id)} style={{ background: 'none', border: 'none', color: 'var(--tm-teal)', cursor: 'pointer', fontSize: '0.72rem', padding: 0 }}>…{id.slice(-6)}</button>
                ))}
              </div>
              <small style={{ color: 'var(--tm-text-2)' }}>{conflict.detail}</small>
            </div>
          ))}
        </div>
      ))}

      {synthesis && (
        <>
          <InlineNotification
            kind="info" lowContrast hideCloseButton
            title={t('explorer.agent_interpretation') ?? 'Interpretation'}
            subtitle={t('explorer.agent_interpretation_hint') ?? ''}
            style={{ marginTop: '0.5rem' }}
          />
          {section(t('explorer.syn_established') ?? 'Established', (synthesis.established ?? []).length, (
            <div>{(synthesis.established ?? []).map((card) => cardLine(card, t, onOpenDossier))}</div>
          ))}
          {section(t('explorer.syn_disputed') ?? 'Disputed', (synthesis.disputed ?? []).length, (
            <div>{(synthesis.disputed ?? []).map((card) => cardLine(card, t, onOpenDossier))}</div>
          ))}
          {section(t('explorer.syn_retired') ?? 'Retired', (synthesis.retired ?? []).length, (
            <div>{(synthesis.retired ?? []).map((card) => cardLine(card, t, onOpenDossier))}</div>
          ))}
          {section(t('explorer.syn_gaps') ?? 'Gaps', (synthesis.gaps ?? []).length, (
            <ul style={{ margin: 0, paddingLeft: '1.1rem', fontSize: '0.78rem', color: 'var(--tm-text-2)' }}>
              {(synthesis.gaps ?? []).map((gap, index) => <li key={index}>{gap}</li>)}
            </ul>
          ))}
          {section(t('explorer.syn_next') ?? 'Next actions', (synthesis.next_actions ?? []).length, (
            <ul style={{ margin: 0, paddingLeft: '1.1rem', fontSize: '0.78rem', color: 'var(--tm-text-2)' }}>
              {(synthesis.next_actions ?? []).map((action, index) => <li key={index}>{action}</li>)}
            </ul>
          ))}
          {section(t('explorer.syn_references') ?? 'References', (synthesis.references ?? []).length, (
            <small style={{ fontSize: '0.7rem', color: 'var(--tm-muted)', fontFamily: 'var(--tm-font-mono)', wordBreak: 'break-all' }}>
              {(synthesis.references ?? []).join('  ')}
            </small>
          ))}
        </>
      )}

      {(result.notes ?? []).length > 0 && (
        <div style={{ marginTop: '0.5rem', fontSize: '0.72rem', color: 'var(--tm-muted)' }}>
          {result.notes!.map((note, index) => <div key={index}>· {note}</div>)}
        </div>
      )}
      {result.limits?.truncated && (
        <small style={{ display: 'block', marginTop: '0.4rem', color: 'var(--tm-warning)', fontSize: '0.7rem' }}>
          {t('explorer.agent_truncated') ?? 'Result truncated by engine limits.'}
        </small>
      )}
    </div>
  )
}
