import { useEffect, useMemo, useRef, useState } from 'react'
import { TextInput, Button } from '@carbon/react'
import type { ExploreHistory, ExploreHistoryStep, ObservationEvent } from '../observationApi'
import { observationApi } from '../observationApi'
import { useI18n, useT } from '../i18n'
import { eventInfo } from '../eventCatalog'
import { stateLabel } from './shared'

/** Temporal history of one knowledge item (ТЗ §9): the trace steps as a
 *  vertical timeline, plus bitemporal cuts (as_of / known_at). The cut is
 *  applied server-side on refetch — the historical state is never
 *  substituted with the current record state.
 *
 *  Steps are enriched with the underlying journal event (bounded fetch):
 *  the projection step carries only ids, while the event knows the run,
 *  the task and who the actor was — the difference between "user-a516…"
 *  and "агент, в ране «починить egress» (clickable)". */

/** State the item carried at moment `timeISO`: the latest step at or before
 *  the cut. Steps without a state inherit the previous known state; before
 *  the first step the item did not exist yet (undefined). */
export function stateAsOf(steps: ExploreHistoryStep[], timeISO: string): { state: string; step: ExploreHistoryStep } | undefined {
  const time = Date.parse(timeISO)
  if (Number.isNaN(time)) return undefined
  let current: { state: string; step: ExploreHistoryStep } | undefined
  for (const step of [...steps].sort((a, b) => Date.parse(a.at) - Date.parse(b.at))) {
    if (Date.parse(step.at) > time) break
    if (step.state) current = { state: step.state, step }
    else if (current) current = { ...current, step }
  }
  return current
}

/** Icon per lifecycle event — visual anchors match the event catalog groups. */
export function stepIcon(type: string): string {
  if (type === 'knowledge.proposed') return '🌱'
  if (type === 'knowledge.confirmed') return '✓'
  if (type === 'knowledge.challenged' || type === 'knowledge.disproved') return '⚠'
  if (type === 'knowledge.corrected' || type === 'knowledge.superseded') return '⇄'
  if (type === 'knowledge.invalidated') return '✕'
  if (type === 'knowledge.promoted') return '↑'
  if (type === 'knowledge.linked') return '🔗'
  return '•'
}

export function stepLabel(type: string, locale: 'ru' | 'en'): string {
  return eventInfo(type)?.[locale].title ?? type
}

/** The state each lifecycle type normally produces: a step whose state equals
 *  this default reads as a duplicate ("Знание предложено → Предложено") and
 *  is suppressed. Only genuine transitions ("→ Инвалидировано") show. */
const DEFAULT_STATE: Record<string, string> = {
  'knowledge.proposed': 'proposed',
  'knowledge.confirmed': 'confirmed',
  'knowledge.challenged': 'challenged',
  'knowledge.corrected': 'corrected',
  'knowledge.superseded': 'superseded',
  'knowledge.invalidated': 'invalidated',
  'knowledge.disproved': 'disproved',
}

/** Bounded enrichment: resolve the journal events behind the trace steps so
 *  each line can name its run and actor kind. Failures degrade to plain ids. */
function useStepEvents(project: string, steps: ExploreHistoryStep[], revision?: number) {
  const [events, setEvents] = useState<Map<string, ObservationEvent>>(new Map())
  const seq = useRef(0)
  useEffect(() => {
    const ids = [...new Set(steps.map((s) => s.event_id).filter(Boolean))].slice(0, 24)
    if (!project.trim() || ids.length === 0) { setEvents(new Map()); return }
    const current = ++seq.current
    let cancelled = false
    ;(async () => {
      const found: ObservationEvent[] = []
      for (let i = 0; i < ids.length; i += 4) {
        if (cancelled) return
        const chunk = await Promise.all(ids.slice(i, i + 4).map(async (id) => {
          try { return (await observationApi.eventById(id, { project })).event } catch { return undefined }
        }))
        for (const item of chunk) if (item) found.push(item)
      }
      if (!cancelled) setEvents(new Map(found.map((event) => [event.event_id, event])))
    })()
    return () => { cancelled = true }
  }, [project, steps, revision])
  return events
}

export default function HistoryTimeline({ project, trace, asOf, knownAt, onCutChange, revision }: {
  project: string
  trace: ExploreHistory
  asOf: string
  knownAt: string
  onCutChange: (cut: { asOf?: string; knownAt?: string }) => void
  revision?: number
}) {
  const t = useT()
  const { locale } = useI18n()
  const steps = useMemo(() => [...(trace.steps ?? [])].sort((a, b) => Date.parse(a.at) - Date.parse(b.at)), [trace.steps])
  const events = useStepEvents(project, steps, revision)

  const cutActive = asOf.trim() !== '' || knownAt.trim() !== ''
  const stateAtCut = asOf.trim() ? stateAsOf(steps, asOf.trim()) : undefined

  const setCut = (next: { asOf?: string; knownAt?: string }) => onCutChange(next)

  return (
    <div>
      {/* Bitemporal cut (ТЗ §9.3): the fields explain which aspect each
          value slices — as_of what was true, known_at what the system knew. */}
      <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'center', flexWrap: 'wrap', marginBottom: '0.75rem' }}>
        <TextInput
          id="explorer-history-asof" size="sm" labelText="" hideLabel type="datetime-local"
          style={{ width: '200px' }} title={t('explorer.history_as_of_hint') ?? ''}
          value={asOf ? asExLocal(asOf) : ''} onChange={(e) => setCut({ asOf: e.target.value ? asUTC(e.target.value) : '' })}
          placeholder={t('explorer.history_as_of') ?? 'as of'}
        />
        <span style={{ fontSize: '0.72rem', color: 'var(--tm-muted)' }}>{t('explorer.history_as_of') ?? 'as of'}</span>
        <TextInput
          id="explorer-history-knownat" size="sm" labelText="" hideLabel type="datetime-local"
          style={{ width: '200px' }} title={t('explorer.history_known_at_hint') ?? ''}
          value={knownAt ? asExLocal(knownAt) : ''} onChange={(e) => setCut({ knownAt: e.target.value ? asUTC(e.target.value) : '' })}
          placeholder={t('explorer.history_known_at') ?? 'known at'}
        />
        <span style={{ fontSize: '0.72rem', color: 'var(--tm-muted)' }}>{t('explorer.history_known_at') ?? 'known at'}</span>
        {cutActive && (
          <Button size="sm" kind="ghost" onClick={() => setCut({ asOf: '', knownAt: '' })}>{t('explorer.history_clear_cut') ?? 'Now'}</Button>
        )}
      </div>

      {cutActive && (
        <div style={{ border: '1px dashed var(--tm-border)', borderRadius: 'var(--tm-radius-md)', padding: '0.5rem 0.75rem', marginBottom: '0.75rem', fontSize: '0.75rem', color: 'var(--tm-text-2)' }}>
          {asOf.trim() && (
            <div>
              {t('explorer.history_state_at') ?? 'State at the cut'}:{' '}
              {stateAtCut ? <strong>{stateLabel(stateAtCut.state, t)}</strong> : <span style={{ color: 'var(--tm-muted)' }}>{t('explorer.history_before_creation') ?? 'the knowledge did not exist yet'}</span>}
            </div>
          )}
          <div style={{ color: 'var(--tm-muted)', marginTop: '0.15rem' }}>{t('explorer.history_cut_hint') ?? ''}</div>
        </div>
      )}

      {steps.length === 0 && (
        <div style={{ color: 'var(--tm-muted)', fontSize: '0.8rem' }}>{t('explorer.history_empty') ?? 'No lifecycle events recorded.'}</div>
      )}

      {steps.length > 0 && (
        <div style={{ position: 'relative', paddingLeft: '1.4rem' }}>
          <div style={{ position: 'absolute', left: '0.55rem', top: '0.3rem', bottom: '0.3rem', width: '1px', background: 'var(--tm-border)' }} />
          {steps.map((step, index) => {
            const event = events.get(step.event_id ?? '')
            const run = event?.context?.run ?? ''
            const actorType = event?.context?.actor?.type ?? ''
            const actorID = event?.context?.actor?.id || step.actor_id
            const defaultState = DEFAULT_STATE[step.type]
            const showState = step.state && step.state !== defaultState
            return (
              <div key={`${step.event_id}-${index}`} style={{ position: 'relative', paddingBottom: '0.65rem' }}>
                <div style={{ position: 'absolute', left: '-1.05rem', top: '0.1rem', fontSize: '0.8rem', background: 'var(--tm-bg)', borderRadius: '50%', width: '1.2rem', textAlign: 'center' }}>{stepIcon(step.type)}</div>
                <div style={{ display: 'flex', gap: '0.45rem', alignItems: 'baseline', flexWrap: 'wrap' }}>
                  <span style={{ fontSize: '0.8rem', fontWeight: 600 }}>{stepLabel(step.type, locale)}</span>
                  {showState && step.state && <span style={{ fontSize: '0.7rem', color: 'var(--tm-teal)' }}>→ {stateLabel(step.state, t)}</span>}
                  <span style={{ fontSize: '0.7rem', color: 'var(--tm-muted)' }}>{new Date(step.at).toLocaleString()}</span>
                  {/* Who: the actor kind in words (the raw id is a hash nobody
                      can read), with the full id as the tooltip. */}
                  {actorType && (
                    <span style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)' }} title={actorID}>
                      {actorType === 'agent' ? (t('explorer.history_by_agent') ?? 'агент')
                        : actorType === 'human' ? (t('explorer.history_by_human') ?? 'человек')
                          : actorType === 'system' ? (t('explorer.history_by_system') ?? 'система')
                            : actorType}
                    </span>
                  )}
                  {!actorType && step.actor_id && <span style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)' }} title={step.actor_id}>{step.actor_id}</span>}
                  {/* Where: the run that caused the step, clickable into the
                      runs page — "в рамках чего это произошло" is the question
                      the raw event id never answered. */}
                  {run && (
                    <a
                      href={`/agents?project=${encodeURIComponent(project)}&run=${encodeURIComponent(run)}`}
                      onClick={(e) => { e.preventDefault(); window.history.pushState(null, '', `/agents?project=${encodeURIComponent(project)}&run=${encodeURIComponent(run)}`); window.dispatchEvent(new PopStateEvent('popstate')) }}
                      style={{ fontSize: '0.7rem', color: 'var(--tm-teal)', cursor: 'pointer', overflowWrap: 'anywhere' }}
                      title={t('explorer.history_open_run') ?? ''}
                    >🏃 {run}</a>
                  )}
                </div>
                {step.reason && <div style={{ fontSize: '0.75rem', color: 'var(--tm-text-2)', marginTop: '0.1rem' }}>{step.reason}</div>}
                {/* Technical identity (frame rule, event id) stays as the
                    quiet tooltip it always was. */}
                {(step.rule || step.event_id) && (
                  <div style={{ fontSize: '0.65rem', color: 'var(--tm-muted)', fontFamily: 'var(--tm-font-mono)', wordBreak: 'break-all' }} title={step.event_id}>
                    {step.rule}
                  </div>
                )}
              </div>
            )
          })}
        </div>
      )}

      {/* Usage telemetry (ТЗ §8.2 "Использование"): what the system recorded
          about offers, uses and outcomes. */}
      <div style={{ borderTop: '1px solid var(--tm-border)', marginTop: '0.5rem', paddingTop: '0.5rem', display: 'flex', gap: '1rem', flexWrap: 'wrap', fontSize: '0.72rem', color: 'var(--tm-text-2)' }}>
        <span>{t('explorer.offers') ?? 'offers'}: {trace.usage.hint_offers}</span>
        <span>{t('explorer.uses') ?? 'uses'}: {trace.usage.hint_uses}</span>
        <span>👍 {trace.usage.helpful_outcomes} / 👎 {trace.usage.harmful_outcomes}</span>
        <span>♻ {trace.usage.reuse_count}</span>
      </div>
    </div>
  )
}

/** datetime-local works in local time; the API needs an instant (UTC). */
export function asUTC(localDateTime: string): string {
  return new Date(localDateTime).toISOString()
}

export function asExLocal(utc: string): string {
  const date = new Date(utc)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`
}
