import { useMemo } from 'react'
import { TextInput, Button } from '@carbon/react'
import type { ExploreHistory, ExploreHistoryStep } from '../observationApi'
import { useI18n, useT } from '../i18n'
import { eventInfo } from '../eventCatalog'

/** Temporal history of one knowledge item (ТЗ §9): the trace steps as a
 *  vertical timeline, plus bitemporal cuts (as_of / known_at). The cut is
 *  applied server-side on refetch — the historical state is never
 *  substituted with the current record state. */

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

export default function HistoryTimeline({ trace, asOf, knownAt, onCutChange }: {
  trace: ExploreHistory
  asOf: string
  knownAt: string
  onCutChange: (cut: { asOf?: string; knownAt?: string }) => void
}) {
  const t = useT()
  const { locale } = useI18n()
  const steps = useMemo(() => [...(trace.steps ?? [])].sort((a, b) => Date.parse(a.at) - Date.parse(b.at)), [trace.steps])

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
              {stateAtCut ? <strong>{stateAtCut.state}</strong> : <span style={{ color: 'var(--tm-muted)' }}>{t('explorer.history_before_creation') ?? 'the knowledge did not exist yet'}</span>}
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
          {steps.map((step, index) => (
            <div key={`${step.event_id}-${index}`} style={{ position: 'relative', paddingBottom: '0.65rem' }}>
              <div style={{ position: 'absolute', left: '-1.05rem', top: '0.1rem', fontSize: '0.8rem', background: 'var(--tm-bg)', borderRadius: '50%', width: '1.2rem', textAlign: 'center' }}>{stepIcon(step.type)}</div>
              <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'baseline', flexWrap: 'wrap' }}>
                <span style={{ fontSize: '0.8rem', fontWeight: 600 }}>{stepLabel(step.type, locale)}</span>
                {step.state && <span style={{ fontSize: '0.7rem', color: 'var(--tm-teal)' }}>→ {step.state}</span>}
                <span style={{ fontSize: '0.7rem', color: 'var(--tm-muted)' }}>{new Date(step.at).toLocaleString()}</span>
                {step.actor_id && <span style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)' }}>{step.actor_id}</span>}
              </div>
              {step.reason && <div style={{ fontSize: '0.75rem', color: 'var(--tm-text-2)', marginTop: '0.1rem' }}>{step.reason}</div>}
              {step.rule && <div style={{ fontSize: '0.68rem', color: 'var(--tm-muted)', fontFamily: 'var(--tm-font-mono)' }}>{step.rule}</div>}
              {step.event_id && <div style={{ fontSize: '0.65rem', color: 'var(--tm-muted)', fontFamily: 'var(--tm-font-mono)', wordBreak: 'break-all' }}>{step.event_id}</div>}
            </div>
          ))}
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
