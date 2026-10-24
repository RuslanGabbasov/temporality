import { useEffect, useState, useCallback, useMemo, useRef } from 'react'
import { Button, Tag } from '@carbon/react'
import PageLoading from './PageLoading'
import { observationApi, type ObservationEvent } from './observationApi'
import { workspaceApi } from './workspaceApi'
import { useT } from './i18n'

const navigate = (path: string) => { window.history.pushState(null, '', path); window.dispatchEvent(new PopStateEvent('popstate')) }

// Supervisor page (docs/supervisor-model.md §3): the installation's problem
// queue. Verdicts (wave A1) collapse into clusters of same-root failures
// (wave A2, keyed by cluster_key); insights (wave A3) explain health
// anomalies in plain language above them. All content is journaled — the
// page is a read-only view over supervisor.* events.

const CLUSTER_WINDOW_MS = 7 * 24 * 60 * 60 * 1000

function message(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }

// generalizeID derives a stable knowledge id from the generalized text —
// identical re-proposals strengthen the same node instead of duplicating it
// (the projection's idempotent proposal semantics).
function generalizeID(text: string) {
  let hash = 5381
  for (let i = 0; i < text.length; i++) hash = ((hash << 5) + hash + text.charCodeAt(i)) | 0
  return 'gen-' + (hash >>> 0).toString(36)
}

const WORD_RE = /[\p{L}\p{N}]+/gu
function words(text: string): Set<string> {
  const set = new Set<string>()
  for (const match of text.toLowerCase().matchAll(WORD_RE)) set.add(match[0])
  return set
}

/** CompareText renders a proposition with a lightweight semantic-diff
 *  treatment: words the OTHER side does not have are highlighted, shared
 *  words stay plain. Lexical overlap is an honest proxy — the score line
 *  nearby quotes the actual cosine. */
function CompareText({ text, other }: { text: string; other: string }) {
  if (!text) return <span style={{ color: 'var(--tm-text-3)' }}>—</span>
  if (!other) return <>{text}</>
  const mine = words(text)
  const theirs = words(other)
  const parts = text.split(/(\s+)/)
  return (
    <>
      {parts.map((part, i) => {
        const clean = part.toLowerCase().replace(/^[^\p{L}\p{N}]+|[^\p{L}\p{N}]+$/gu, '')
        if (!clean || !mine.has(clean) || theirs.has(clean)) return <span key={i}>{part}</span>
        return <span key={i} style={{ background: 'color-mix(in srgb, var(--tm-amber) 22%, transparent)', borderRadius: 2, padding: '0 1px' }}>{part}</span>
      })}
    </>
  )
}

interface ClusterRow {
  key: string
  cause: string
  pattern: string
  caseID: string
  subject: string
  summary: string
  recommendation: string
  project: string
  count: number
  runs: { run: string; at: string }[]
  eventIDs: string[]
  lastAt: string
  // knowledge_conflict case payload: the pair to compare and act on.
  conflict?: { newID: string; newProp: string; newProject: string; oldID: string; oldProp: string; oldProject: string; score: number }
  // C4 skill draft (name/description/steps from the verdict).
  skill?: { name: string; description: string; steps: string[] }
}

// causeTag picks the tag color for a cause class.
function causeTag(cause: string): 'red' | 'warm-gray' | 'blue' {
  if (cause === 'harness_bug' || cause === 'model_error') return 'red'
  if (cause === 'agent_prompt' || cause === 'user_error') return 'warm-gray'
  return 'blue'
}

/** causeLabel localizes a triage cause class for display; raw id stays as
 *  fallback for classes the locale does not know yet. */
function causeLabel(cause: string, t: (key: string) => string): string {
  const key = 'supervisor.cause_' + cause
  const label = t(key)
  return label === key ? cause : label
}

function clusterVerdicts(events: ObservationEvent[]): ClusterRow[] {
  const byKey = new Map<string, ClusterRow>()
  const loose: ClusterRow[] = []
  for (const event of events) {
    const run = String(event.data?.run_id ?? '')
    const cause = String(event.data?.cause_class ?? 'unknown')
    const key = String(event.data?.cluster_key ?? '')
    const row: ClusterRow = {
      key: key || `solo:${event.event_id}`,
      cause,
      pattern: String(event.data?.pattern ?? ''),
      caseID: String(event.data?.case ?? ''),
      subject: String(event.data?.subject ?? ''),
      summary: String(event.data?.summary ?? ''),
      recommendation: String(event.data?.recommendation ?? ''),
      project: event.context?.project ?? '',
      count: 1,
      runs: run ? [{ run, at: event.occurred_at }] : [],
      eventIDs: [event.event_id],
      lastAt: event.occurred_at,
    }
    if (event.data?.case === 'knowledge_conflict' && event.data?.knowledge_id && event.data?.neighbor_id) {
      row.conflict = {
        newID: String(event.data.knowledge_id),
        newProp: String(event.data.new_proposition ?? ''),
        newProject: String(event.data.new_project ?? event.context?.project ?? ''),
        oldID: String(event.data.neighbor_id),
        oldProp: String(event.data.old_proposition ?? ''),
        oldProject: String(event.data.old_project ?? event.context?.project ?? ''),
        score: Number(event.data.score ?? 0),
      }
    }
    // B1 knowledge_contradiction: a challenged item vs its contradictor — the
    // same compare-and-act surface as C5. Layout contract: 'new' is the
    // contradicting observation (contradicted_by), 'existing' is the
    // challenged subject.
    if (event.data?.case === 'knowledge_contradiction' && event.data?.subject && event.data?.contradicted_by) {
      row.conflict = {
        newID: String(event.data.contradicted_by),
        newProp: '',
        newProject: String(event.context?.project ?? ''),
        oldID: String(event.data.subject),
        oldProp: '',
        oldProject: String(event.context?.project ?? ''),
        score: 0,
      }
    }
    if (event.data?.case === 'skill_candidate' && event.data?.skill_name) {
      row.skill = {
        name: String(event.data.skill_name),
        description: String(event.data.description ?? ''),
        steps: Array.isArray(event.data.steps) ? event.data.steps.map(String) : [],
      }
    }
    if (!key) {
      // Legacy verdicts without a cluster key stay standalone rows.
      loose.push(row)
      continue
    }
    const existing = byKey.get(key)
    if (!existing) {
      byKey.set(key, row)
      continue
    }
    existing.count++
    existing.runs.push(...row.runs)
    existing.eventIDs.push(...row.eventIDs)
    if (event.occurred_at > existing.lastAt) {
      existing.lastAt = event.occurred_at
      // The newest verdict speaks for the cluster.
      existing.summary = row.summary
      existing.recommendation = row.recommendation
      existing.pattern = row.pattern || existing.pattern
      existing.project = row.project || existing.project
      if (row.conflict) existing.conflict = row.conflict
      if (row.skill) existing.skill = row.skill
    }
  }
  const clustered = [...byKey.values()]
  clustered.sort((a, b) => (b.count - a.count) || (a.lastAt < b.lastAt ? 1 : -1))
  return [...clustered, ...loose.sort((a, b) => (a.lastAt < b.lastAt ? 1 : -1))]
}

// SupervisorPanels is the whole supervisor surface without the page chrome —
// the /supervisor page and the dashboard's supervisor tab render the same
// panels (docs/supervisor-model.md §7).
export function SupervisorPanels({ onPendingChange, setProject }: { onPendingChange?: (pending: number) => void; setProject?: (project: string) => void } = {}) {
  const t = useT()
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [verdicts, setVerdicts] = useState<ObservationEvent[]>([])
  const [insights, setInsights] = useState<ObservationEvent[]>([])
  const [expanded, setExpanded] = useState<string | null>(null)
  // Queue filters (docs/supervisor-model.md §7): case family, project and
  // acknowledged visibility — the queue becomes navigable once waves B/C
  // produce many rows.
  const [caseFilter, setCaseFilter] = useState('')
  const [projectFilter, setProjectFilter] = useState('')
  const [showResolved, setShowResolved] = useState(false)
  const [acknowledged, setAcknowledged] = useState<Set<string>>(new Set())
  const [busyAck, setBusyAck] = useState('')
  const [busyCase, setBusyCase] = useState('')
  // Bulk mode (§7): the queue fills up, one-row buttons stop scaling.
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [bulkBusy, setBulkBusy] = useState(false)
  // Propositions behind conflict verdicts that predate new_proposition /
  // old_proposition in the verdict payload: fetched once from the knowledge
  // projection, keyed "project/id".
  // Propositions behind conflict verdicts that predate new_proposition /
  // old_proposition in the verdict payload: fetched once from the knowledge
  // projection, keyed "project/id" (and "/id" for project-less verdicts);
  // project records where the item was found.
  const [propFallback, setPropFallback] = useState<Record<string, { text: string; project?: string }>>({})
  // Inline generalization editor: which conflict row, and the draft text.
  const [generalize, setGeneralize] = useState<{ rowKey: string; text: string } | null>(null)
  // Semantic-layer health (§6.5): read-only monitoring. Configuration lives
  // in Agents → quality settings.
  const [embeddings, setEmbeddings] = useState<{ model: string; ready: boolean; error?: string } | null>(null)
  const [tiers, setTiers] = useState<{ tier: string; helpful: number; harmful: number; helpful_rate_pct: number }[] | null>(null)
  // Collapsible per-case queue sections: 50+ rows in one flat list stopped
  // being readable — the header toggle folds what has been read.
  const [collapsedCases, setCollapsedCases] = useState<Set<string>>(new Set())

  // silent: refresh the data WITHOUT the spinner — acknowledge/skip keep the
  // list mounted, so the scroll position survives on long queues (52+ rows).
  const load = useCallback(async (silent = false) => {
    if (!silent) setLoading(true)
    setError('')
    try {
      const [verdictPage, insightPage, ackPage, settings, effect] = await Promise.all([
        observationApi.events(undefined, undefined, undefined, undefined, { type: 'supervisor.verdict', limit: 300 }),
        observationApi.events(undefined, undefined, undefined, undefined, { type: 'supervisor.insight', limit: 50 }),
        observationApi.events(undefined, undefined, undefined, undefined, { type: 'supervisor.verdict.acknowledged', limit: 300 }),
        workspaceApi.getSupervisorSettings().catch(() => null),
        observationApi.hintEffectiveness().catch(() => null),
      ])
      setVerdicts(verdictPage.events ?? [])
      setInsights(insightPage.events ?? [])
      const ackSet = new Set<string>()
      for (const event of ackPage.events ?? []) {
        const id = String(event.data?.verdict_event_id ?? '')
        if (id) ackSet.add(id)
      }
      // MERGE, never replace: acknowledgements ride the async outbox (the POST
      // 200s before the journal has the event), so a fresh reload can be
      // missing the just-clicked ids. Replacing resurrected the row and made
      // dismiss take 2–3 clicks; an acknowledgement is irreversible, so the
      // union of the server truth and everything clicked this session is
      // always safe.
      setAcknowledged((prev) => {
        const next = new Set(prev)
        for (const id of ackSet) next.add(id)
        return next
      })
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      setEmbeddings((settings as any)?.embeddings ?? null)
      setTiers(effect?.tiers ?? [])
    } catch (failure) {
      setError(message(failure))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => { void load() }, [load])

  const acknowledge = async (eventID: string) => {
    setBusyAck(eventID)
    try {
      await workspaceApi.acknowledgeSupervisorVerdict(eventID)
      setAcknowledged((prev) => new Set(prev).add(eventID))
    } catch (failure) {
      setError(message(failure))
    } finally {
      setBusyAck('')
    }
  }

  // One-click resolutions for a knowledge_conflict verdict: invalidate the
  // losing knowledge through the knowledge API, then acknowledge the verdict
  // (it leaves the queue). The keep-both path only acknowledges.
  // One-click resolutions per case family (docs/supervisor-model.md §3):
  // C4 skill candidates open the Skills form pre-filled from the verdict's
  // draft; C1 friction rows jump to the runs list. No dead-end buttons: an
  // action either navigates somewhere useful or is not shown.
  const openSkillFromCase = (row: ClusterRow) => {
    const skill = row.skill
    if (!skill) return
    const md = [
      '---', `name: ${skill.name}`, 'description: >-',
      ...skill.description.split('\n').map((l) => '  ' + l),
      '---', '',
      `# ${skill.name}`, '',
      skill.description, '',
      '## Steps', ...skill.steps.map((s, i) => `${i + 1}. ${s}`), '',
      `Evidence: supervisor case ${row.key} (${row.count} runs)`,
    ].join('\n')
    try {
      sessionStorage.setItem('skill_prefill', JSON.stringify({
        name: skill.name, description: skill.description, markdown: md,
      }))
    } catch { /* storage full or private mode: the form opens unfilled */ }
    navigate('/skills')
  }
  const openFrictionRuns = (row: ClusterRow) => {
    navigate(row.project ? `/agents?project=${encodeURIComponent(row.project)}` : '/agents')
  }

  /** Generalize: replace BOTH sides with one broader statement. Creates the
   *  generalized knowledge (knowledge.proposed through the ingest endpoint),
   *  invalidates both originals with replacement_id — the projection turns
   *  each invalidation into a knowledge.superseded correction — and
   *  acknowledges the verdict. Projects come from the card's resolved state
   *  (verdict's own, or the fallback's found-project): empty projects would
   *  422 on every call, and the fallback already knows where the items live. */
  const generalizeConflict = async (row: ClusterRow, newProject: string, oldProject: string) => {
    const conflict = row.conflict!
    if (!generalize || generalize.rowKey !== row.key) return
    const text = generalize.text.trim()
    if (!text) return
    if (!newProject && !oldProject) {
      setError(t('supervisor.generalize_no_project') ?? 'Could not resolve the projects of these knowledge items — they may be removed or invisible to your account.')
      return
    }
    setBusyCase(row.key)
    try {
      const id = generalizeID(text)
      const anchor = oldProject || newProject
      await observationApi.proposeKnowledge({
        knowledge_id: id, proposition: text, project: anchor,
        actor: { id: 'supervisor-queue', type: 'human' },
      })
      for (const target of [
        { id: conflict.newID, project: newProject },
        { id: conflict.oldID, project: oldProject },
      ]) {
        // A side whose project could not be resolved is skipped: its item is
        // likely gone — the generalized statement still replaces the pair.
        if (!target.project || !target.id) continue
        await observationApi.invalidate({
          knowledge_id: target.id, project: target.project,
          actor: { id: 'supervisor-queue', type: 'human' },
          replacement_id: id,
          reason: t('supervisor.action_generalized_reason', { id }),
        })
      }
      // The helper adds each id to the session ack-set optimistically: the
      // acks ride the async outbox, so the reload below cannot see them yet —
      // without the optimistic add the row resurrected and looked broken.
      for (const eventID of row.eventIDs) {
        await acknowledge(eventID)
      }
      setGeneralize(null)
      await load(true)
    } catch (failure) {
      setError(message(failure))
    } finally {
      setBusyCase('')
    }
  }

  // Resolve the proposition texts behind conflict verdicts that were published
  // without them (older payloads, or verdicts whose kernel-side meta scan
  // missed the item): one exact-item lookup per missing side. The listing
  // requires a project — when the verdict does not name one (or the item is
  // not there), walk the account's visible projects. Results are stored under
  // "project/id" AND the project-less "/id" key the render reads when the
  // verdict names no project; project carries where the item was FOUND, so
  // the dossier link opens the right project.
  const propFallbackRef = useRef<Record<string, { text: string; project?: string }>>({})
  useEffect(() => {
    const missing: { project: string; id: string }[] = []
    for (const row of clusterVerdicts(verdicts)) {
      const cf = row.conflict
      if (!cf) continue
      if (!cf.newProp) {
        const project = cf.newProject || row.project
        if (!(`${project}/${cf.newID}` in propFallbackRef.current) && !(`/${cf.newID}` in propFallbackRef.current)) missing.push({ project, id: cf.newID })
      }
      if (!cf.oldProp) {
        const project = cf.oldProject || row.project
        if (!(`${project}/${cf.oldID}` in propFallbackRef.current) && !(`/${cf.oldID}` in propFallbackRef.current)) missing.push({ project, id: cf.oldID })
      }
    }
    if (missing.length === 0) return
    let cancelled = false
    void (async () => {
      let candidates: string[] = []
      try {
        const { projects } = await workspaceApi.listProjects()
        candidates = projects.map((p) => p.id)
      } catch { /* no project list — only the verdict's own project is tried */ }
      const next: Record<string, { text: string; project?: string }> = {}
      let fetched = 0
      const lookup = async (project: string, id: string): Promise<string> => {
        if (fetched >= 40) return ''
        fetched++
        try {
          const page = await observationApi.knowledge(project, undefined, undefined, { knowledge_id: id, limit: 1 })
          return page.knowledge?.[0]?.proposition ?? ''
        } catch {
          return '' // not visible from this account
        }
      }
      for (const item of missing) {
        // Verdict's project first (the common case, one lookup), then the
        // visible projects for items born elsewhere or project-less verdicts.
        const order = [...new Set([item.project, ...candidates].filter(Boolean))]
        let found: { text: string; project?: string } | null = null
        for (const project of order) {
          const text = await lookup(project, item.id)
          if (text) { found = { text, project }; break }
        }
        // Store under the found project and the project-less render key; the
        // bare entry also stops the effect from re-fetching on every render.
        if (found) {
          next[`${found.project}/${item.id}`] = found
          next[`/${item.id}`] = found
        } else {
          next[`${item.project}/${item.id}`] = { text: '' }
          next[`/${item.id}`] = { text: '' }
        }
      }
      if (cancelled) return
      propFallbackRef.current = { ...propFallbackRef.current, ...next }
      setPropFallback(propFallbackRef.current)
    })()
    return () => { cancelled = true }
  }, [verdicts])

  const resolveConflict = async (row: ClusterRow, kind: 'invalidate_new' | 'invalidate_old' | 'invalidate_both' | 'keep_both') => {
    const conflict = row.conflict!
    setBusyCase(row.key)
    try {
      if (kind !== 'keep_both') {
        const targets = kind === 'invalidate_both'
          ? [
              { id: conflict.newID, project: conflict.newProject },
              { id: conflict.oldID, project: conflict.oldProject },
            ]
          : [kind === 'invalidate_new'
              ? { id: conflict.newID, project: conflict.newProject }
              : { id: conflict.oldID, project: conflict.oldProject }]
        for (const target of targets) {
          await observationApi.invalidate({
            knowledge_id: target.id,
            project: target.project,
            actor: { id: 'supervisor-queue', type: 'human' },
            reason: t('supervisor.action_invalidated_reason', { score: (conflict.score || 0).toFixed(2) }),
          })
        }
      }
      // Same optimistic ack-set add: invalidate → acknowledge → refresh must
      // hide the row on the first click, not after the next manual reload.
      for (const eventID of row.eventIDs) {
        await acknowledge(eventID)
      }
      await load(true)
    } catch (failure) {
      setError(message(failure))
    } finally {
      setBusyCase('')
    }
  }

  const clusters = clusterVerdicts(verdicts)
  const projectOptions = [...new Set(clusters.map((row) => row.project).filter(Boolean))]
  const caseOptions = [...new Set(clusters.map((row) => row.caseID).filter(Boolean))]
  const resolved = (row: ClusterRow) => row.eventIDs.every((id) => acknowledged.has(id))
  const pendingCount = clusters.filter((row) => !resolved(row)).length

  // The tab badge (§7): the administrator must see the queue depth before
  // opening the tab. The Dashboard renders the badge from this report.
  useEffect(() => {
    onPendingChange?.(pendingCount)
  }, [pendingCount, onPendingChange])

  const visibleClusters = clusters.filter((row) => {
    if (caseFilter && row.caseID !== caseFilter) return false
    if (projectFilter && row.project !== projectFilter) return false
    if (!showResolved && resolved(row)) return false
    return true
  })

  // Case-family sections: 50+ rows in one flat list stopped being readable.
  // Groups keep queue order inside, sorted by unresolved count — the busiest
  // family first. Collapsed state is per case family.
  const caseGroups = (() => {
    const byCase = new Map<string, ClusterRow[]>()
    for (const row of visibleClusters) {
      const key = row.caseID || 'other'
      const list = byCase.get(key)
      if (list) list.push(row); else byCase.set(key, [row])
    }
    return [...byCase.entries()]
      .map(([caseID, rows]) => ({
        caseID,
        rows,
        unresolved: rows.filter((row) => !resolved(row)).length,
      }))
      .sort((a, b) => b.unresolved - a.unresolved || a.caseID.localeCompare(b.caseID))
  })()
  const toggleCase = (caseID: string) => {
    setCollapsedCases((prev) => {
      const next = new Set(prev)
      if (next.has(caseID)) next.delete(caseID); else next.add(caseID)
      return next
    })
  }

  // Bulk selection works on VISIBLE rows only: a stale key from another
  // filter would otherwise act on rows the administrator cannot see.
  const toggleRow = (key: string) => {
    setSelected((prev) => {
      const next = new Set(prev)
      if (next.has(key)) next.delete(key); else next.add(key)
      return next
    })
  }
  const selectedRows = visibleClusters.filter((row) => selected.has(row.key) && !resolved(row))
  const allVisibleSelected = visibleClusters.length > 0 && visibleClusters.every((row) => selected.has(row.key))
  const toggleAll = () => {
    setSelected(allVisibleSelected ? new Set() : new Set(visibleClusters.map((row) => row.key)))
  }
  /** Runs one row action after another across the selection. Conflicts are
   *  only touched when every selected row carries the same resolution kind —
   *  mixed selections degrade to acknowledge-only for safety. */
  const bulkApply = async (kind: 'acknowledge' | 'invalidate_new' | 'invalidate_old' | 'invalidate_both') => {
    if (selectedRows.length === 0) return
    setBulkBusy(true)
    setError('')
    try {
      for (const row of selectedRows) {
        if (kind === 'acknowledge' || !row.conflict) {
          for (const id of row.eventIDs) {
            try { await workspaceApi.acknowledgeSupervisorVerdict(id) } catch { /* best-effort */ }
          }
          continue
        }
        await resolveConflictQuiet(row, kind)
      }
      setSelected(new Set())
      await load(true)
    } catch (failure) {
      setError(message(failure))
    } finally {
      setBulkBusy(false)
    }
  }
  /** resolveConflict without its per-row load()/busy churn — the bulk loop
   *  reloads once at the end. */
  async function resolveConflictQuiet(row: ClusterRow, kind: 'invalidate_new' | 'invalidate_old' | 'invalidate_both' | 'keep_both') {
    const conflict = row.conflict!
    if (kind !== 'keep_both') {
      const targets = kind === 'invalidate_both'
        ? [
            { id: conflict.newID, project: conflict.newProject },
            { id: conflict.oldID, project: conflict.oldProject },
          ]
        : [kind === 'invalidate_new'
            ? { id: conflict.newID, project: conflict.newProject }
            : { id: conflict.oldID, project: conflict.oldProject }]
      for (const target of targets) {
        try {
          await observationApi.invalidate({
            knowledge_id: target.id, project: target.project,
            actor: { id: 'supervisor-queue', type: 'human' },
            reason: t('supervisor.action_invalidated_reason', { score: (conflict.score || 0).toFixed(2) }),
          })
        } catch { /* one failure must not sink the batch; the reload shows survivors */ }
      }
    }
    for (const eventID of row.eventIDs) {
      try { await workspaceApi.acknowledgeSupervisorVerdict(eventID) } catch { /* best-effort */ }
    }
  }

  return (
    <>
      {error && <p style={{ color: 'var(--tm-danger)', fontSize: '0.8rem' }}>{error}</p>}

      {loading ? <PageLoading /> : (
        <>
          {/* Monitoring only (§6.5): semantic-layer health and hint-tier
              effectiveness. Configuration (models, HAB coefficients) lives in
              Agents → quality settings — a monitoring tab must not be a
              second editing surface for the same values. Compact: the whole
              widget is one inline strip — status, model, tiers. */}
          <div style={{ border: '1px solid var(--tm-border)', borderRadius: '6px', padding: '0.4rem 0.75rem', fontSize: '0.78rem', marginBottom: '1rem', display: 'flex', gap: '0.6rem', alignItems: 'baseline', flexWrap: 'wrap' }}>
            <span style={{ fontWeight: 600 }}>{t('supervisor.semantic_title') ?? 'Semantic layer'}</span>
            {embeddings && (
              <span style={{ display: 'inline-flex', gap: '0.35rem', alignItems: 'baseline' }} title={embeddings.error ?? undefined}>
                <Tag type={embeddings.ready ? 'green' : 'red'} size="sm">{embeddings.ready ? t('supervisor.semantic_ready') ?? 'ready' : t('supervisor.semantic_degraded') ?? 'degraded'}</Tag>
                <span style={{ color: 'var(--tm-text-3)', fontSize: '0.72rem' }}>{embeddings.model}</span>
              </span>
            )}
            {tiers && tiers.length > 0 ? (
              <span style={{ display: 'inline-flex', gap: '0.75rem', flexWrap: 'wrap', color: 'var(--tm-text-2)' }} title={t('supervisor.tiers_hint') ?? undefined}>
                {tiers.map((row) => (
                  <span key={row.tier} style={{ whiteSpace: 'nowrap' }}>
                    <span style={{ color: row.tier === 'semantic' ? 'var(--tm-teal)' : 'var(--tm-text-3)' }}>{t(`supervisor.tier_${row.tier}`) ?? row.tier}</span>
                    {' '}👍{row.helpful} 👎{row.harmful} · {row.helpful_rate_pct}%
                  </span>
                ))}
              </span>
            ) : (
              <span style={{ color: 'var(--tm-text-3)', fontSize: '0.72rem' }}>{t('supervisor.tiers_empty') ?? 'No hint outcomes in the window yet.'}</span>
            )}
          </div>

          {/* Wave A3: health anomaly explanations, newest first. One row per
              metric — the pass re-publishes the same anomaly each cycle, so
              older entries of a metric are hidden behind the newest. A read
              insight can be skipped like a queue item. */}
          <section style={{ marginBottom: '1.5rem' }}>
            <h3 style={{ fontSize: '0.85rem', margin: '0 0 0.4rem' }}>{t('supervisor.insights_title')}</h3>
            {(() => {
              const ackedInsights = insights.filter((event) => acknowledged.has(event.event_id))
              const visible = insights.filter((event) => !acknowledged.has(event.event_id))
              // Dedup by metric: only the newest insight per metric stays.
              const latestByMetric = new Map<string, ObservationEvent>()
              for (const event of visible) {
                const metric = String(event.data?.metric ?? event.event_id)
                if (!latestByMetric.has(metric)) latestByMetric.set(metric, event)
              }
              const rows = [...latestByMetric.values()]
              if (insights.length === 0) {
                return <p style={{ color: 'var(--tm-text-3)', fontSize: '0.78rem', margin: 0 }}>{t('supervisor.insights_empty')}</p>
              }
              return (
                <>
                  {(ackedInsights.length > 0 || visible.length !== insights.length) && (
                    <p style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)', margin: '0 0 0.35rem' }}>
                      {t('supervisor.insights_hidden_n', { count: String(insights.length - rows.length) })}
                    </p>
                  )}
                  <div style={{ border: '1px solid var(--tm-border)', borderRadius: '6px', overflow: 'hidden' }}>
                    {rows.map((event) => {
                      const severity = String(event.data?.severity ?? 'info')
                      const title = String(event.data?.title ?? '')
                      const explanation = String(event.data?.explanation ?? '')
                      const recommendation = String(event.data?.recommendation ?? '')
                      const current = String(event.data?.current ?? '')
                      const baseline = String(event.data?.baseline ?? '')
                      const busyKey = 'insight:' + event.event_id
                      return (
                        <div key={event.event_id} style={{ padding: '0.55rem 0.75rem', borderTop: '1px solid var(--tm-border)', fontSize: '0.8rem' }}>
                          <div style={{ display: 'flex', alignItems: 'center', gap: '0.45rem', flexWrap: 'wrap' }}>
                            <Tag type={severity === 'warning' ? 'red' : 'blue'} size="sm">{severity === 'warning' ? t('supervisor.severity_warning') : t('supervisor.severity_info')}</Tag>
                            <strong>{title}</strong>
                            <span style={{ marginLeft: 'auto', fontSize: '0.68rem', color: 'var(--tm-text-3)', flexShrink: 0 }}>{new Date(event.occurred_at).toLocaleString()}</span>
                          </div>
                          <div style={{ marginTop: '0.3rem', lineHeight: 1.5 }}>{explanation}</div>
                          {(current || baseline) && (
                            <div style={{ marginTop: '0.2rem', fontSize: '0.7rem', color: 'var(--tm-text-3)' }}>
                              {current}{baseline ? ` · ${t('supervisor.vs_baseline')} ${baseline}` : ''}
                            </div>
                          )}
                          {recommendation && (
                            <div style={{ marginTop: '0.2rem', fontSize: '0.72rem', color: 'var(--tm-text-2)' }}>
                              <span style={{ color: 'var(--tm-amber)' }}>→</span> {recommendation}
                            </div>
                          )}
                          <div style={{ marginTop: '0.35rem' }}>
                            <Button
                              size="sm"
                              kind="ghost"
                              disabled={busyAck === busyKey}
                              onClick={async () => {
                                setBusyAck(busyKey)
                                try {
                                  await workspaceApi.acknowledgeSupervisorVerdict(event.event_id)
                                  await load(true)
                                } catch (failure) {
                                  setError(message(failure))
                                } finally {
                                  setBusyAck('')
                                }
                              }}
                            >
                              {busyAck === busyKey ? '…' : t('supervisor.action_skip') ?? 'Skip'}
                            </Button>
                          </div>
                        </div>
                      )
                    })}
                  </div>
                </>
              )
            })()}
          </section>

          {/* Waves A1+A2: triage verdicts collapsed into same-root clusters. */}
          <section>
            <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', flexWrap: 'wrap', marginBottom: '0.4rem' }}>
              <h3 style={{ fontSize: '0.85rem', margin: 0 }}>{t('supervisor.problems_title')}</h3>
              <label className="toolbar-select">
                {t('supervisor.filter_case') ?? 'Case'}
                <select value={caseFilter} onChange={(e) => setCaseFilter(e.target.value)}>
                  <option value="">{t('supervisor.filter_all_cases') ?? 'All cases'}</option>
                  {caseOptions.map((caseID) => <option key={caseID} value={caseID}>{t(`supervisor.case_${caseID}`) ?? caseID}</option>)}
                </select>
              </label>
              {projectOptions.length > 0 && (
                <label className="toolbar-select">
                  {t('supervisor.filter_project') ?? 'Project'}
                  <select value={projectFilter} onChange={(e) => setProjectFilter(e.target.value)}>
                    <option value="">{t('supervisor.filter_all_projects') ?? 'All projects'}</option>
                    {projectOptions.map((p) => <option key={p} value={p}>{p}</option>)}
                  </select>
                </label>
              )}
              <label style={{ fontSize: '0.74rem', color: 'var(--tm-text-3)', display: 'flex', alignItems: 'center', gap: '0.3rem', cursor: 'pointer' }}>
                <input type="checkbox" checked={showResolved} onChange={(e) => setShowResolved(e.target.checked)} />
                {t('supervisor.show_resolved') ?? 'Show resolved'}
              </label>
            </div>
            {visibleClusters.length > 0 && (
              <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', flexWrap: 'wrap', marginBottom: '0.4rem', fontSize: '0.74rem' }}>
                <label style={{ display: 'flex', alignItems: 'center', gap: '0.3rem', cursor: 'pointer', color: 'var(--tm-text-3)' }}>
                  <input type="checkbox" checked={allVisibleSelected} onChange={toggleAll} />
                  {t('supervisor.bulk_select_all') ?? 'Select all'}
                </label>
                {selectedRows.length > 0 && (
                  <>
                    <span style={{ color: 'var(--tm-text-2)' }}>{t('supervisor.bulk_selected_n', { count: String(selectedRows.length) })}</span>
                    <Button size="sm" kind="ghost" disabled={bulkBusy} onClick={() => void bulkApply('acknowledge')}>
                      {t('supervisor.bulk_acknowledge') ?? 'Skip selected'}
                    </Button>
                    {selectedRows.every((row) => row.conflict) && (
                      <>
                        <Button size="sm" kind="danger" disabled={bulkBusy} onClick={() => void bulkApply('invalidate_new')}>
                          {bulkBusy ? '…' : t('supervisor.bulk_invalidate_new') ?? 'Invalidate new'}
                        </Button>
                        <Button size="sm" kind="danger" disabled={bulkBusy} onClick={() => void bulkApply('invalidate_old')}>
                          {t('supervisor.bulk_invalidate_old') ?? 'Invalidate existing'}
                        </Button>
                        <Button size="sm" kind="danger" disabled={bulkBusy} onClick={() => void bulkApply('invalidate_both')} title={t('supervisor.bulk_invalidate_both_hint') ?? undefined}>
                          {t('supervisor.bulk_invalidate_both') ?? 'Invalidate both'}
                        </Button>
                      </>
                    )}
                    <Button size="sm" kind="ghost" disabled={bulkBusy} onClick={() => setSelected(new Set())}>
                      {t('supervisor.bulk_clear') ?? 'Clear'}
                    </Button>
                  </>
                )}
              </div>
            )}
            {visibleClusters.length === 0 ? (
              <p style={{ color: 'var(--tm-text-3)', fontSize: '0.78rem', margin: 0 }}>{t('supervisor.problems_empty')}</p>
            ) : (
              <div style={{ display: 'grid', gap: '0.75rem' }}>
                {caseGroups.map((group) => {
                  const collapsed = collapsedCases.has(group.caseID)
                  return (
                    <div key={group.caseID}>
                      <button
                        type="button"
                        onClick={() => toggleCase(group.caseID)}
                        style={{ display: 'flex', alignItems: 'center', gap: '0.45rem', width: '100%', background: 'none', border: 'none', padding: '0.3rem 0.15rem', cursor: 'pointer', textAlign: 'left' }}
                      >
                        <span style={{ color: 'var(--tm-text-3)', fontSize: '0.68rem', width: '0.8rem' }}>{collapsed ? '▸' : '▾'}</span>
                        <strong style={{ fontSize: '0.8rem' }}>{t(`supervisor.case_${group.caseID}`) ?? group.caseID}</strong>
                        {group.unresolved > 0 && <Tag type={group.unresolved > 5 ? 'red' : 'blue'} size="sm">{group.unresolved}</Tag>}
                        <span style={{ fontSize: '0.68rem', color: 'var(--tm-text-3)' }}>/ {group.rows.length}</span>
                      </button>
                      {!collapsed && (
                        <div style={{ border: '1px solid var(--tm-border)', borderRadius: '6px', overflow: 'hidden' }}>
                          {group.rows.map((row) => {
                            const open = expanded === row.key
                            const isResolved = resolved(row)
                            return (
                              <div key={row.key} style={{ padding: '0.55rem 0.75rem', borderTop: '1px solid var(--tm-border)', fontSize: '0.78rem', opacity: isResolved ? 0.55 : 1 }}>
                      <div style={{ display: 'flex', alignItems: 'center', gap: '0.45rem', flexWrap: 'wrap' }}>
                        {!isResolved && (
                          <input
                            type="checkbox"
                            checked={selected.has(row.key)}
                            onChange={() => toggleRow(row.key)}
                            aria-label={t('supervisor.bulk_select_row') ?? 'Select row'}
                          />
                        )}
                        <Tag type={causeTag(row.cause)} size="sm">{causeLabel(row.cause, t)}</Tag>
                        {row.pattern && <span style={{ fontSize: '0.74rem', color: 'var(--tm-text-2)' }}>{row.pattern}</span>}
                        {row.caseID && <Tag type="purple" size="sm">{t(`supervisor.case_${row.caseID}`) ?? row.caseID}</Tag>}
                        {row.project && <span style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)' }}>{row.project}</span>}
                        {row.count > 1 && <Tag type="cool-gray" size="sm">×{row.count}</Tag>}
                        {isResolved && <Tag type="green" size="sm">{t('supervisor.resolved') ?? 'resolved'}</Tag>}
                        <span style={{ marginLeft: 'auto', fontSize: '0.68rem', color: 'var(--tm-text-3)', flexShrink: 0 }}>{new Date(row.lastAt).toLocaleString()}</span>
                      </div>
                      <div style={{ marginTop: '0.3rem', lineHeight: 1.5 }}>{row.summary}</div>
                      {row.recommendation && (
                        <div style={{ marginTop: '0.2rem', fontSize: '0.72rem', color: 'var(--tm-text-2)' }}>
                          <span style={{ color: 'var(--tm-amber)' }}>→</span> {row.recommendation}
                        </div>
                      )}
                      {row.conflict && (() => {
                      const cf = row.conflict
                      const newProject = cf.newProject || row.project
                      const oldProject = cf.oldProject || row.project
                      // Verdict text first, then the fallback lookup — which
                      // also knows the project where the item was FOUND, so
                      // links open the right dossier even cross-project.
                      const fallbackNew = cf.newProp ? null : (propFallback[`${newProject}/${cf.newID}`] ?? propFallback[`/${cf.newID}`])
                      const fallbackOld = cf.oldProp ? null : (propFallback[`${oldProject}/${cf.oldID}`] ?? propFallback[`/${cf.oldID}`])
                      const newProp = cf.newProp || fallbackNew?.text || ''
                      const oldProp = cf.oldProp || fallbackOld?.text || ''
                      const newLinkProject = fallbackNew?.project || newProject
                      const oldLinkProject = fallbackOld?.project || oldProject
                      const openDossier = (id: string, project: string) => (e: React.MouseEvent) => {
                        e.preventDefault()
                        // The knowledge page takes its project from the app
                        // context, not the URL — switch context first or the
                        // dossier opens against the wrong project and 404s.
                        setProject?.(project)
                        navigate(`/knowledge?project=${encodeURIComponent(project)}&dossier=${encodeURIComponent(id)}`)
                      }
                      const dossierLink = (id: string, project: string) => (
                        <a
                          href={`/knowledge?project=${encodeURIComponent(project)}&dossier=${encodeURIComponent(id)}`}
                          onClick={openDossier(id, project)}
                          style={{ color: 'var(--tm-teal)', fontSize: '0.68rem' }}
                          title={id}
                        >{id}</a>
                      )
                      return (
                      <div style={{ marginTop: '0.4rem', border: '1px solid var(--tm-border)', borderRadius: '4px', padding: '0.4rem 0.6rem', fontSize: '0.75rem' }}>
                          <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '0.5rem' }}>
                            <div style={{ borderLeft: '2px solid var(--tm-teal)', paddingLeft: '0.45rem', display: 'grid', gap: '0.3rem' }}>
                              <div><Tag type="blue" size="sm">{t('supervisor.compare_new') ?? 'new'}</Tag>{' '}{dossierLink(cf.newID, newLinkProject)}</div>
                              <div style={{ lineHeight: 1.45 }}>
                                {newProp
                                  ? <CompareText text={newProp} other={oldProp} />
                                  : fallbackNew && fallbackNew.text === ''
                                    ? <span style={{ color: 'var(--tm-text-3)', fontStyle: 'italic' }}>{t('supervisor.compare_missing') ?? 'knowledge not found (removed or not visible to your account)'}</span>
                                    : <span style={{ color: 'var(--tm-text-3)' }}>…</span>}
                              </div>
                              <div>
                                <Button size="sm" kind="danger--ghost" disabled={busyCase === row.key} onClick={() => void resolveConflict(row, 'invalidate_new')} title={t('supervisor.action_invalidate_hint_new') ?? undefined}>
                                  {busyCase === row.key ? '…' : t('supervisor.action_invalidate') ?? 'Invalidate'}
                                </Button>
                              </div>
                            </div>
                            <div style={{ borderLeft: '2px solid var(--tm-border)', paddingLeft: '0.45rem', display: 'grid', gap: '0.3rem' }}>
                              <div><Tag type="warm-gray" size="sm">{t('supervisor.compare_old') ?? 'existing'}</Tag>{' '}{dossierLink(cf.oldID, oldLinkProject)}</div>
                              <div style={{ lineHeight: 1.45 }}>
                                {oldProp
                                  ? <CompareText text={oldProp} other={newProp} />
                                  : fallbackOld && fallbackOld.text === ''
                                    ? <span style={{ color: 'var(--tm-text-3)', fontStyle: 'italic' }}>{t('supervisor.compare_missing') ?? 'knowledge not found (removed or not visible to your account)'}</span>
                                    : <span style={{ color: 'var(--tm-text-3)' }}>…</span>}
                              </div>
                              <div>
                                <Button size="sm" kind="danger--ghost" disabled={busyCase === row.key} onClick={() => void resolveConflict(row, 'invalidate_old')} title={t('supervisor.action_invalidate_hint_old') ?? undefined}>
                                  {busyCase === row.key ? '…' : t('supervisor.action_invalidate') ?? 'Invalidate'}
                                </Button>
                              </div>
                            </div>
                          </div>
                          {(newProp || oldProp) && (
                            <div style={{ marginTop: '0.3rem', fontSize: '0.66rem', color: 'var(--tm-text-3)' }}>
                              {t('supervisor.compare_hint') ?? 'Highlighted — words absent on the other side'}
                              {cf.score > 0 && ` · ${t('supervisor.compare_score', { score: cf.score.toFixed(2) })}`}
                            </div>
                          )}
                          {generalize?.rowKey === row.key ? (
                            <div style={{ marginTop: '0.35rem', display: 'grid', gap: '0.3rem' }}>
                              <textarea
                                value={generalize.text}
                                onChange={(e) => setGeneralize({ rowKey: row.key, text: e.target.value })}
                                rows={3}
                                placeholder={t('supervisor.generalize_placeholder') ?? 'One generalized statement covering both…'}
                                style={{ width: '100%', fontSize: '0.75rem', padding: '0.35rem 0.45rem', border: '1px solid var(--tm-border)', borderRadius: '4px', background: 'var(--tm-bg-1, transparent)', color: 'var(--tm-text)', resize: 'vertical' }}
                              />
                              <div style={{ display: 'flex', gap: '0.4rem', flexWrap: 'wrap' }}>
                                <Button size="sm" kind="primary" disabled={busyCase === row.key || !generalize.text.trim()} onClick={() => void generalizeConflict(row, newLinkProject, oldLinkProject)}>
                                  {busyCase === row.key ? '…' : t('supervisor.generalize_confirm') ?? 'Replace both'}
                                </Button>
                                <Button size="sm" kind="ghost" disabled={busyCase === row.key} onClick={() => setGeneralize(null)}>
                                  {t('supervisor.generalize_cancel') ?? 'Cancel'}
                                </Button>
                              </div>
                            </div>
                          ) : (
                            <div style={{ display: 'flex', gap: '0.4rem', flexWrap: 'wrap', marginTop: '0.4rem' }}>
                              <Button size="sm" kind="secondary" disabled={busyCase === row.key} onClick={() => void resolveConflict(row, 'keep_both')} title={t('supervisor.action_skip_hint') ?? undefined}>
                                {busyCase === row.key ? '…' : t('supervisor.action_skip') ?? 'Skip'}
                              </Button>
                              {/* Both formulations outdated: retire the whole pair
                                  in one click — two invalidations + ack. */}
                              <Button size="sm" kind="danger--ghost" disabled={busyCase === row.key} onClick={() => void resolveConflict(row, 'invalidate_both')} title={t('supervisor.action_invalidate_both_hint') ?? undefined}>
                                {busyCase === row.key ? '…' : t('supervisor.action_invalidate_both') ?? 'Invalidate both'}
                              </Button>
                              <Button
                                size="sm"
                                kind="primary"
                                disabled={busyCase === row.key}
                                onClick={() => setGeneralize({ rowKey: row.key, text: newProp.length >= oldProp.length ? newProp : oldProp })}
                              >
                                {t('supervisor.action_generalize') ?? 'Generalize…'}
                              </Button>
                            </div>
                          )}
                        </div>
                      )
                      })()}
                      <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'center', marginTop: '0.35rem', flexWrap: 'wrap' }}>
                        {isResolved ? null : (
                          <Button
                            size="sm"
                            kind="ghost"
                            disabled={busyAck === row.key}
                            onClick={async () => {
                              setBusyAck(row.key)
                              try {
                                // Await every acknowledgement before the
                                // refresh: the queue hides on the optimistic
                                // set, and the reload must not race the
                                // outbox delivery of these acks.
                                for (const id of row.eventIDs) {
                                  await acknowledge(id)
                                }
                                await load(true)
                              } finally {
                                setBusyAck('')
                              }
                            }}
                          >
                            {t('supervisor.action_skip') ?? 'Skip'}
                          </Button>
                        )}
                        {row.caseID === 'skill_candidate' && row.skill && (
                          <Button size="sm" kind="primary" onClick={() => openSkillFromCase(row)}>
                            {t('supervisor.action_create_skill') ?? 'Create skill'}
                          </Button>
                        )}
                        {row.caseID === 'user_friction' && (
                          <Button size="sm" kind="ghost" onClick={() => openFrictionRuns(row)}>
                            {t('supervisor.action_show_runs') ?? 'Show these runs'}
                          </Button>
                        )}
                        {row.runs.length > 0 && (
                          <button
                            type="button"
                            onClick={() => setExpanded(open ? null : row.key)}
                            style={{ background: 'none', border: 'none', color: 'var(--tm-teal)', cursor: 'pointer', fontSize: '0.72rem', padding: 0 }}
                          >
                            {open ? t('supervisor.hide_runs') : t('supervisor.show_runs', { count: String(row.runs.length) })}
                          </button>
                        )}
                      </div>
                      {open && row.runs.length > 0 && (
                        <div style={{ marginTop: '0.3rem', display: 'flex', flexDirection: 'column', gap: '0.2rem' }}>
                              {row.runs.slice(0, 20).map(({ run, at }) => (
                                <div key={run + at} style={{ display: 'flex', gap: '0.5rem', alignItems: 'baseline' }}>
                                  <a
                                    href={`/agents?project=${encodeURIComponent(row.project)}&run=${encodeURIComponent(run)}`}
                                    onClick={(e) => { e.preventDefault(); if (row.project) setProject?.(row.project); navigate(`/agents?project=${encodeURIComponent(row.project)}&run=${encodeURIComponent(run)}`) }}
                                    style={{ fontSize: '0.72rem', color: 'var(--tm-teal)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}
                                    title={run}
                                  >{run}</a>
                                  <span style={{ fontSize: '0.66rem', color: 'var(--tm-text-3)', flexShrink: 0 }}>{new Date(at).toLocaleString()}</span>
                                </div>
                              ))}
                        </div>
                      )}
                    </div>
                  )
                })}
                        </div>
                      )}
                    </div>
                  )
                })}
              </div>
            )}
          </section>
        </>
      )}
    </>
  )
}

export default function Supervisor({ setProject }: { setProject?: (project: string) => void }) {
  const t = useT()
  return (
    <div style={{ padding: '1rem', maxWidth: '60rem', margin: '0 auto' }}>
      <div style={{ marginBottom: '0.75rem' }}>
        <h2 style={{ margin: 0 }}>{t('supervisor.title')}</h2>
        <p style={{ color: 'var(--tm-text-3)', fontSize: '0.8rem', margin: '0.25rem 0 0' }}>{t('supervisor.subtitle')}</p>
      </div>
      <SupervisorPanels setProject={setProject} />
    </div>
  )
}
