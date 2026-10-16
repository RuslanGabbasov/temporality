import { useEffect, useMemo, useRef, useState } from 'react'
import { Button, InlineNotification, Tag } from '@carbon/react'
import { authHeaders } from './api'
import {
  observationApi,
  type ExploreConflict,
  type ExploreHistory,
  type ExploreRelation,
  type ObservationEvent,
} from './observationApi'
import { useT } from './i18n'
import HistoryTimeline from './explorer/HistoryTimeline'
import RelationGraph from './explorer/RelationGraph'
import ConflictPanel from './explorer/ConflictPanel'
import LifecycleBar from './explorer/LifecycleBar'
import { stateLabel } from './explorer/shared'

/** Dossier of one knowledge item (ТЗ §8): content, lifecycle, applicability,
 *  provenance (primary sources, activation chain), relations and conflicts —
 *  each block only when data exists. Data comes from the engine's trace /
 *  expand / compare operations plus the activation chain endpoint; nothing is
 *  invented client-side. */

type Tab = 'overview' | 'history' | 'relations' | 'conflicts'

interface ActivationChainInfo {
  total_runs?: number
  is_alive?: boolean
  formed_in?: string
  formed_at?: string
  activations?: { run_id?: string; outcome?: string; recalled_at?: string }[]
}

const TAB_KEYS: Record<Tab, string> = {
  overview: 'explorer.dossier_tab_overview',
  history: 'explorer.dossier_tab_history',
  relations: 'explorer.dossier_tab_relations',
  conflicts: 'explorer.dossier_tab_conflicts',
}

export default function Dossier({ project, knowledgeID, revision = 0, onBack, onOpenDossier, onQuestion, onRefresh }: {
  project: string
  knowledgeID: string
  revision?: number
  onBack: () => void
  onOpenDossier: (id: string) => void
  onQuestion: (question: string) => void
  onRefresh: () => void
}) {
  const t = useT()
  const [tab, setTab] = useState<Tab>('overview')
  const [trace, setTrace] = useState<ExploreHistory | null>(null)
  const [asOf, setAsOf] = useState('')
  const [knownAt, setKnownAt] = useState('')
  const [traceLoading, setTraceLoading] = useState(false)
  const [traceError, setTraceError] = useState('')
  const [contradictions, setContradictions] = useState<ExploreRelation[]>([])
  const [sources, setSources] = useState<Map<string, ObservationEvent>>(new Map())
  const [sourcesOpen, setSourcesOpen] = useState(false)
  const [chain, setChain] = useState<ActivationChainInfo | null>(null)
  const [expandedSource, setExpandedSource] = useState('')
  const traceSeq = useRef(0)

  // Trace (with bitemporal cuts) is the dossier backbone (ТЗ §4.4).
  useEffect(() => {
    if (!project.trim() || !knowledgeID) return
    const current = ++traceSeq.current
    setTraceLoading(true); setTraceError('')
    ;(async () => {
      try {
        const res = await observationApi.explore({
          project: project.trim(),
          operation: 'trace',
          targets: [knowledgeID],
          as_of: asOf.trim() || undefined,
          known_at: knownAt.trim() || undefined,
        })
        if (current !== traceSeq.current) return
        setTrace(res.traces?.[0] ?? null)
      } catch (e) {
        if (current === traceSeq.current) setTraceError(e instanceof Error ? e.message : 'Request failed')
      } finally {
        if (current === traceSeq.current) setTraceLoading(false)
      }
    })()
    return () => { traceSeq.current++ }
  }, [project, knowledgeID, asOf, knownAt, revision])

  // Contradiction edges around the item feed the Conflicts tab.
  useEffect(() => {
    if (!project.trim() || !knowledgeID) { setContradictions([]); return }
    let cancelled = false
    ;(async () => {
      try {
        const res = await observationApi.explore({
          project: project.trim(), operation: 'expand', targets: [knowledgeID], max_depth: 1, max_items: 30,
        })
        if (cancelled) return
        setContradictions((res.relations ?? []).filter((rel) => rel.type === 'contradicts'))
      } catch { if (!cancelled) setContradictions([]) }
    })()
    return () => { cancelled = true }
  }, [project, knowledgeID, revision])

  // Primary sources: resolve evidence refs to journal events (bounded at 8 —
  // the engine bound the evidence list the same way).
  const evidenceRefs = useMemo(() => (trace?.evidence ?? []).map((e) => e.ref).filter(Boolean).slice(0, 8), [trace])
  useEffect(() => {
    if (!sourcesOpen || evidenceRefs.length === 0) return
    let cancelled = false
    ;(async () => {
      const found: ObservationEvent[] = []
      for (let i = 0; i < evidenceRefs.length; i += 4) {
        if (cancelled) return
        const chunk = await Promise.all(evidenceRefs.slice(i, i + 4).map(async (ref) => {
          try { return await observationApi.eventById(ref, { project: project.trim() }) } catch { return undefined }
        }))
        for (const item of chunk) if (item?.event) found.push(item.event)
      }
      if (!cancelled) setSources(new Map(found.map((event) => [event.event_id, event])))
    })()
    return () => { cancelled = true }
  }, [sourcesOpen, evidenceRefs, project])

  // Activation chain: runs where the knowledge was recalled/used.
  useEffect(() => {
    if (!knowledgeID || !project.trim()) { setChain(null); return }
    let cancelled = false
    ;(async () => {
      try {
        const resp = await fetch(`/kernel-api/v1/workspace/knowledge/${encodeURIComponent(knowledgeID)}/chain?project=${encodeURIComponent(project.trim())}`, { headers: { ...authHeaders() } })
        if (resp.ok && !cancelled) setChain(await resp.json())
        else if (!cancelled) setChain(null)
      } catch { if (!cancelled) setChain(null) }
    })()
    return () => { cancelled = true }
  }, [knowledgeID, project, revision])

  const conflictObjects: ExploreConflict[] | undefined = useMemo(() => {
    if (contradictions.length === 0) return undefined
    // Build proper conflict records by comparing the item with its
    // contradictors — compare carries context differences and resolution
    // status the raw edges lack.
    const others = contradictions.flatMap((rel) => [rel.from_id, rel.to_id]).filter((id) => id !== knowledgeID)
    return others.map((id) => ({
      knowledge_ids: [knowledgeID, id].sort(),
      basis: 'explicit:contradicts',
      detail: t('explorer.conflict_edge_detail') ?? 'linked by an explicit contradicts relation',
      resolved: false,
    }))
  }, [contradictions, knowledgeID, t])

  const cutActive = asOf.trim() !== '' || knownAt.trim() !== ''
  const tabRow = (key: Tab, label: string, badge?: number) => (
    <button
      key={key}
      onClick={() => setTab(key)}
      style={{
        padding: '0.4rem 0.8rem', background: tab === key ? 'var(--tm-elevated)' : 'transparent',
        border: 'none', borderBottom: tab === key ? '2px solid var(--tm-teal)' : '2px solid transparent',
        color: tab === key ? 'var(--tm-teal)' : 'var(--tm-text-3)', cursor: 'pointer', fontSize: '0.78rem', fontWeight: 600,
      }}
    >{label}{badge !== undefined && badge > 0 ? ` (${badge})` : ''}</button>
  )

  return (
    <div style={{ border: '1px solid var(--tm-border)', borderRadius: 'var(--tm-radius-md)', padding: '0.75rem' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', marginBottom: '0.5rem' }}>
        <Button size="sm" kind="ghost" onClick={onBack}>← {t('explorer.dossier_back') ?? 'Back to results'}</Button>
        <div style={{ fontSize: '0.8rem', fontWeight: 600, flex: 1 }}>{t('explorer.dossier') ?? 'Dossier'}</div>
        <code style={{ fontSize: '0.65rem', color: 'var(--tm-muted)', fontFamily: 'var(--tm-font-mono)' }}>{knowledgeID}</code>
      </div>

      {traceError && <InlineNotification kind="error" title="Error" subtitle={traceError} onClose={() => setTraceError('')} lowContrast style={{ marginBottom: '0.6rem' }} />}
      {traceLoading && <div style={{ fontSize: '0.75rem', color: 'var(--tm-muted)', marginBottom: '0.5rem' }}>…</div>}

      {trace && (
        <>
          <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'flex-start', flexWrap: 'wrap', marginBottom: '0.6rem' }}>
            <Tag size="sm" type={trace.state === 'invalidated' ? 'red' : trace.state === 'challenged' ? 'warm-gray' : trace.state === 'confirmed' ? 'green' : 'blue'}>{stateLabel(trace.state, t)}</Tag>
            {trace.at_risk && <Tag type="red" size="sm">{t('knowledge.at_risk') ?? 'at risk'}</Tag>}
            <div style={{ flex: 1, fontSize: '0.9rem', lineHeight: 1.5 }}>{trace.proposition}</div>
          </div>

          <div style={{ display: 'flex', gap: '0.4rem', borderBottom: '1px solid var(--tm-border)', marginBottom: '0.75rem', flexWrap: 'wrap' }}>
            {tabRow('overview', t(TAB_KEYS.overview) ?? 'Overview')}
            {tabRow('history', t(TAB_KEYS.history) ?? 'History')}
            {tabRow('relations', t(TAB_KEYS.relations) ?? 'Relations')}
            {tabRow('conflicts', t(TAB_KEYS.conflicts) ?? 'Conflicts', contradictions.length)}
          </div>

          {tab === 'overview' && (
            <div>
              {/* Provenance + applicability (ТЗ §8.2). Blocks with no data are
                  omitted rather than shown empty. */}
              <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(160px, 1fr))', gap: '0.4rem', fontSize: '0.72rem', color: 'var(--tm-text-2)', marginBottom: '0.7rem' }}>
                <div>{t('explorer.dossier_created') ?? 'Created'}: {new Date(trace.created_at).toLocaleString()}</div>
                {trace.created_by && <div>{t('explorer.dossier_created_by') ?? 'by'}: {trace.created_by}</div>}
                {chain?.formed_in && <div>{t('explorer.dossier_formed_in') ?? 'Formed in run'}: {chain.formed_in}</div>}
                {chain?.total_runs !== undefined && <div>{t('explorer.dossier_usage_runs') ?? 'Used in runs'}: {chain.total_runs}</div>}
                <div>{t('explorer.offers') ?? 'offers'}: {trace.usage.hint_offers} · {t('explorer.uses') ?? 'uses'}: {trace.usage.hint_uses} · 👍 {trace.usage.helpful_outcomes} / 👎 {trace.usage.harmful_outcomes}</div>
              </div>

              {trace.replacement_id && (
                <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'center', marginBottom: '0.6rem', fontSize: '0.75rem' }}>
                  <span style={{ color: 'var(--tm-text-2)' }}>{t('explorer.dossier_replacement') ?? 'Replaced by'}:</span>
                  <code style={{ fontFamily: 'var(--tm-font-mono)', fontSize: '0.68rem' }}>{trace.replacement_id}</code>
                  <Button size="sm" kind="ghost" onClick={() => onOpenDossier(trace.replacement_id!)}>{t('explorer.dossier_open') ?? 'Open'}</Button>
                </div>
              )}

              {/* Primary sources (ТЗ §8.3 "открыть первоисточник"). */}
              {evidenceRefs.length > 0 && (
                <div style={{ marginBottom: '0.7rem' }}>
                  <button onClick={() => setSourcesOpen((v) => !v)} style={{ background: 'none', border: 'none', color: 'var(--tm-teal)', cursor: 'pointer', fontSize: '0.78rem', padding: 0 }}>
                    {sourcesOpen ? '▾' : '▸'} {t('explorer.dossier_sources') ?? 'Primary sources'} ({evidenceRefs.length})
                  </button>
                  {sourcesOpen && (
                    <div style={{ marginTop: '0.4rem', display: 'flex', flexDirection: 'column', gap: '0.35rem' }}>
                      {evidenceRefs.map((ref) => {
                        const event = sources.get(ref)
                        return (
                          <div key={ref} style={{ border: '1px solid var(--tm-border)', borderRadius: '6px', padding: '0.4rem 0.5rem', fontSize: '0.72rem' }}>
                            <div role="button" tabIndex={0} style={{ cursor: 'pointer', display: 'flex', gap: '0.5rem', alignItems: 'baseline', flexWrap: 'wrap' }} onClick={() => setExpandedSource(expandedSource === ref ? '' : ref)}>
                              <span style={{ fontWeight: 600 }}>{event?.type ?? 'event'}</span>
                              <span style={{ color: 'var(--tm-muted)' }}>{event ? new Date(event.occurred_at).toLocaleString() : ref}</span>
                              {event?.context?.run && <span style={{ color: 'var(--tm-text-3)' }}>run: {event.context.run}</span>}
                            </div>
                            {expandedSource === ref && (
                              <pre style={{ margin: '0.4rem 0 0', fontSize: '0.65rem', fontFamily: 'var(--tm-font-mono)', color: 'var(--tm-text-2)', whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}>
                                {JSON.stringify(event?.data ?? { ref }, null, 2)}
                              </pre>
                            )}
                          </div>
                        )
                      })}
                    </div>
                  )}
                </div>
              )}

              {/* Activation chain summary (ТЗ §8.2 "Использование"). */}
              {chain && (chain.activations?.length ?? 0) > 0 && (
                <div style={{ marginBottom: '0.7rem' }}>
                  <div style={{ fontSize: '0.75rem', fontWeight: 600, marginBottom: '0.3rem' }}>
                    {t('knowledge.activation_chain') ?? 'Activation chain'} · {chain.total_runs} {t('knowledge.runs') ?? 'runs'} · {chain.is_alive ? (t('knowledge.alive') ?? 'alive') : (t('knowledge.dead') ?? 'retired')}
                  </div>
                  <div style={{ display: 'flex', flexDirection: 'column', gap: '0.2rem' }}>
                    {(chain.activations ?? []).slice(0, 8).map((activation, i) => (
                      <div key={i} style={{ fontSize: '0.7rem', color: 'var(--tm-text-2)', display: 'flex', gap: '0.5rem', flexWrap: 'wrap' }}>
                        <span style={{ fontFamily: 'var(--tm-font-mono)' }}>{activation.run_id}</span>
                        {activation.recalled_at && <span style={{ color: 'var(--tm-muted)' }}>{new Date(activation.recalled_at).toLocaleString()}</span>}
                        {activation.outcome && <span>{activation.outcome === 'success' ? '✓' : activation.outcome === 'failure' ? '✕' : '?'} {activation.outcome}</span>}
                      </div>
                    ))}
                  </div>
                </div>
              )}

              {/* Navigation cross-links before the lifecycle block: research
                  and comparison are reads, the lifecycle section below is the
                  only place that mutates the knowledge. */}
              <div style={{ display: 'flex', gap: '0.5rem', flexWrap: 'wrap', marginBottom: '0.75rem', paddingBottom: '0.6rem', borderBottom: '1px solid var(--tm-border)' }}>
                <Button size="sm" kind="ghost" onClick={() => onQuestion(trace.proposition)}>{t('explorer.dossier_research') ?? 'Research from this statement'}</Button>
                {contradictions.length > 0 && <Button size="sm" kind="ghost" onClick={() => setTab('conflicts')}>{t('explorer.dossier_compare') ?? 'Compare with counterparts'}</Button>}
              </div>

              <LifecycleBar project={project} knowledgeID={knowledgeID} onDone={onRefresh} onOpenDossier={onOpenDossier} />
            </div>
          )}

          {tab === 'history' && (
            <HistoryTimeline
              trace={trace}
              asOf={asOf}
              knownAt={knownAt}
              onCutChange={(cut) => {
                if (cut.asOf !== undefined) setAsOf(cut.asOf)
                if (cut.knownAt !== undefined) setKnownAt(cut.knownAt)
              }}
            />
          )}

          {tab === 'relations' && (
            <RelationGraph project={project} rootID={knowledgeID} revision={revision} onOpenDossier={onOpenDossier} />
          )}

          {tab === 'conflicts' && (
            conflictObjects
              ? <ConflictPanel project={project} conflicts={conflictObjects} revision={revision} onChanged={onRefresh} onOpenDossier={onOpenDossier} />
              : <div style={{ color: 'var(--tm-muted)', fontSize: '0.8rem' }}>{t('explorer.conflicts_none_for_item') ?? 'No contradictions reference this knowledge.'}</div>
          )}
        </>
      )}

      {!trace && !traceLoading && !traceError && (
        <div style={{ color: 'var(--tm-muted)', fontSize: '0.8rem' }}>{t('explorer.dossier_not_found') ?? 'Knowledge was not found in the visible base.'}</div>
      )}
      {trace && cutActive && tab !== 'history' && (
        <div style={{ marginTop: '0.6rem', fontSize: '0.7rem', color: 'var(--tm-amber)' }}>
          {t('explorer.dossier_cut_active') ?? 'A bitemporal cut is active — the History tab explains it.'}
        </div>
      )}
    </div>
  )
}
