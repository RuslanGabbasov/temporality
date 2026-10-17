import { useState } from 'react'
import { observationApi, type ObservationEvent } from '../observationApi'
import { useT } from '../i18n'

const navigate = (path: string) => { window.history.pushState(null, '', path); window.dispatchEvent(new PopStateEvent('popstate')) }

/** EvidenceLink (ТЗ §8.2→§8.3, «переход от выводов к исходным данным»): one
 *  evidence ref of a knowledge item. Clicking resolves the journal event and
 *  jumps to the run whose trajectory produced it — the source data behind the
 *  conclusion. Unresolvable refs stay plain text: honest degradation. */
export default function EvidenceLink({ refID, project }: { refID: string; project: string }) {
  const t = useT()
  const [run, setRun] = useState<string | null>(null)
  const [failed, setFailed] = useState(false)
  const [busy, setBusy] = useState(false)

  if (failed || !refID) {
    return <span style={{ marginRight: '0.5rem' }}>{refID}</span>
  }

  const resolve = async () => {
    setBusy(true)
    try {
      const event: ObservationEvent = await observationApi.event('', refID)
      const runID = event.context?.run ?? ''
      if (runID) {
        setRun(runID)
        navigate(`/agents?project=${encodeURIComponent(event.context?.project || project)}&run=${encodeURIComponent(runID)}`)
      } else {
        setFailed(true)
      }
    } catch {
      setFailed(true)
    } finally {
      setBusy(false)
    }
  }

  if (run) {
    // Already resolved once: render the direct link (covers re-renders).
    return (
      <a
        href={`/agents?project=${encodeURIComponent(project)}&run=${encodeURIComponent(run)}`}
        onClick={(e) => { e.preventDefault(); navigate(`/agents?project=${encodeURIComponent(project)}&run=${encodeURIComponent(run)}`) }}
        title={t('knowledge.evidence_open') ?? 'Open the source run'}
        style={{ marginRight: '0.5rem', color: 'var(--tm-teal)', cursor: 'pointer' }}
      >{refID}</a>
    )
  }

  return (
    <a
      href="#"
      onClick={(e) => { e.preventDefault(); if (!busy) void resolve() }}
      title={t('knowledge.evidence_open') ?? 'Open the source run'}
      style={{ marginRight: '0.5rem', color: 'var(--tm-teal)', cursor: 'pointer', textDecoration: 'underline dotted' }}
    >{busy ? '…' : refID}</a>
  )
}
