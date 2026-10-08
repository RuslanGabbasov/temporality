import { useEffect, useMemo, useRef, useState } from 'react'
import { observationApi, type ObservationEvent } from './observationApi'
import { detectMisleads, foldExperience, type MisleadSignal } from './experience'
import { shortTime } from './eventSummary'
import { useT } from './i18n'

/** Knowledge lifecycle event types that can start a row (proposed) or end it
 * (challenged/disproved/corrected/invalidated/superseded) — the only inputs
 * dispute detection needs, so the banner queries just these per run. */
const DISPUTE_EVENT_TYPES = [
  'knowledge.proposed',
  'knowledge.challenged',
  'knowledge.disproved',
  'knowledge.corrected',
  'knowledge.invalidated',
  'knowledge.superseded',
] as const

/** The chain of a run: walk up to its root, then take the whole subtree.
 * `rows` is the run tree built from run.started events; runs whose parent is
 * outside the loaded window stay their own root. */
export function chainRunIds(rows: { id: string; parent: string | null }[], selected: string): string[] {
  const byId = new Map(rows.map((row) => [row.id, row]))
  const childrenOf = new Map<string, string[]>()
  for (const row of rows) {
    if (!row.parent) continue
    const list = childrenOf.get(row.parent) ?? []
    list.push(row.id)
    childrenOf.set(row.parent, list)
  }
  let root = selected
  const seen = new Set<string>([selected])
  // Guard against parent cycles: stop when a parent repeats.
  while (true) {
    const parent = byId.get(root)?.parent
    if (!parent || !byId.has(parent) || seen.has(parent)) break
    root = parent
    seen.add(parent)
  }
  const ids: string[] = []
  const walk = (id: string) => {
    if (ids.includes(id)) return
    ids.push(id)
    for (const child of childrenOf.get(id) ?? []) walk(child)
  }
  walk(root)
  return ids
}

/** Load the knowledge lifecycle events of every run in the chain and fold
 * them together: a dispute is knowledge proposed by one role and killed by a
 * different role, so both halves must come from one merged stream. */
export async function fetchChainDisputes(project: string, runIds: string[]): Promise<MisleadSignal[]> {
  const pages = await Promise.all(runIds.flatMap((runId) => DISPUTE_EVENT_TYPES.map((type) =>
    observationApi.events(project, undefined, undefined, undefined, { run: runId, type, limit: 100 })
      .catch(() => ({ events: [] as ObservationEvent[], count: 0 }))
  )))
  const model = foldExperience(pages.flatMap((page) => page.events))
  return detectMisleads(model.rows)
}

function clip(text: string, max = 140): string {
  const clean = text.replace(/\s+/g, ' ').trim()
  return clean.length > max ? `${clean.slice(0, max - 1)}…` : clean
}

/** Orange banner in the run panel: cross-agent knowledge disputes found
 * anywhere in the selected run's delegation chain ("agent A misled agent B").
 * Renders nothing while loading and when the chain has no disputes. */
export default function RunDisputes({ project, selected, rows, onOpenRun }: {
  project: string
  selected: string
  rows: { id: string; parent: string | null }[]
  onOpenRun: (runId: string) => void
}) {
  const t = useT()
  const [signals, setSignals] = useState<MisleadSignal[] | null>(null)
  const [open, setOpen] = useState(false)

  const chainIds = useMemo(() => chainRunIds(rows, selected), [rows, selected])
  const chainKey = chainIds.join('\n')
  // The list silently reloads every few seconds, which recreates `rows` and
  // with it `chainIds`; only a real chain change (user picked another run,
  // chain grew) may blank the banner — a refresh keeps it on screen.
  const loadedKeyRef = useRef<string | null>(null)

  useEffect(() => {
    if (loadedKeyRef.current !== chainKey) {
      loadedKeyRef.current = chainKey
      setOpen(false)
      setSignals(null)
    }
    let cancelled = false
    void fetchChainDisputes(project, chainIds).then((found) => {
      if (!cancelled) setSignals(found)
    })
    return () => { cancelled = true }
  }, [project, chainIds, chainKey])

  if (!signals || signals.length === 0) return null

  return (
    <div className="run-disputes">
      <button type="button" className="run-disputes-toggle" onClick={() => setOpen((prev) => !prev)}>
        <span className="run-disputes-icon">⚠</span>
        <strong>{t('runs.disputes.title')}</strong>
        <span className="run-disputes-count">{t('runs.disputes.count_chain', { count: String(signals.length) })}</span>
        <span className="run-disputes-arrow">{open ? '▾' : '▸'}</span>
      </button>
      {open && (
        <div>
          {signals.map((signal) => (
            <div key={`${signal.knowledgeId}:${signal.at}`} className="run-disputes-item">
              <div className="run-disputes-item-head">
                <span className="run-disputes-item-text" title={`${signal.proposedBy ?? '?'} → ${signal.challengedBy ?? '?'}`}>
                  {t('timeline.mislead', { challenged: signal.challengedBy ?? '?', proposed: signal.proposedBy ?? '?' })}
                </span>
                <span className="run-disputes-item-kind">{t(`timeline.kind.${signal.kind}`) || signal.kind}</span>
                <span className="run-disputes-item-time">{shortTime(signal.at)}</span>
                {signal.run && (
                  <button
                    type="button"
                    className="run-disputes-open"
                    title={`${t('runs.disputes.open_run')} ${signal.run}`}
                    onClick={() => onOpenRun(signal.run!)}
                  >↗</button>
                )}
              </div>
              <div className="run-disputes-item-proposition" title={signal.proposition}>«{clip(signal.proposition)}»</div>
              {signal.reason && (
                <div className="run-disputes-item-reason" title={signal.reason}>{clip(signal.reason, 200)}</div>
              )}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
