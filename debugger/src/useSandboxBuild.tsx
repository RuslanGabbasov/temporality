import { useCallback, useEffect, useRef, useState } from 'react'
import { ProgressBar } from '@carbon/react'
import { buildSandboxStatus, type SandboxBuildStatus } from './kernelApi'
import { useT } from './i18n'

const POLL_MS = 2000

/** Tracks one sandbox image build: the caller fires the (now immediate)
 *  build POST through start(), the hook polls the kernel's build status
 *  until the slot reports done/failed and exposes the progress snapshot —
 *  phase, dockerfile step counter, log tail, elapsed clock. The build itself
 *  survives page reloads and client timeouts: the status endpoint is the
 *  source of truth, not the POST response. */
export function useSandboxBuild() {
  const [status, setStatus] = useState<SandboxBuildStatus | null>(null)
  const [tracking, setTracking] = useState(false)
  const timer = useRef<number | null>(null)

  const stop = useCallback(() => {
    if (timer.current !== null) {
      window.clearInterval(timer.current)
      timer.current = null
    }
    setTracking(false)
  }, [])

  const poll = useCallback(async (): Promise<boolean> => {
    try {
      const snapshot = await buildSandboxStatus()
      setStatus(snapshot)
      // done/failed close the tracking; a slot gone idle without a done
      // marker (kernel restart mid-build) ends it too — nothing to wait for.
      if (!snapshot.running && (snapshot.done || snapshot.failed)) return false
      if (!snapshot.running && !snapshot.done && !snapshot.failed) return false
      return true
    } catch {
      return true // transport hiccups keep the poll alive
    }
  }, [])

  const start = useCallback((initiate: () => Promise<unknown>) => {
    setTracking(true)
    void initiate().catch(() => { /* the status endpoint carries the failure */ })
    void poll()
    if (timer.current !== null) window.clearInterval(timer.current)
    timer.current = window.setInterval(() => { void poll().then((keep) => { if (!keep) stop() }) }, POLL_MS)
  }, [poll, stop])

  // A kernel restart mid-build leaves the slot idle: one immediate poll on
  // mount re-syncs the panel to reality (a running build keeps tracking).
  useEffect(() => {
    void poll().then((keep) => { if (keep) setTracking(true) })
    return () => { if (timer.current !== null) window.clearInterval(timer.current) }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const busy = tracking && !(status?.done || status?.failed)
  return { status, busy, start, stop }
}

/** The progress panel: phase label, a determinate bar while the dockerfile
 *  step counter is known, the last build line and the elapsed clock. */
export function SandboxBuildProgress({ status }: { status: SandboxBuildStatus | null }) {
  const t = useT()
  if (!status) return null
  const phaseKey = status.phase === 'pull' ? 'sandbox.progress_pull'
    : status.phase === 'export' ? 'sandbox.progress_export'
      : 'sandbox.progress_build'
  const phaseLabel = t(phaseKey)
    .replace('{{step}}', String(status.step ?? 0))
    .replace('{{total}}', String(status.total ?? 0))
  const elapsed = status.elapsed_ms ? Math.round(status.elapsed_ms / 1000) : 0
  const clock = elapsed >= 60 ? `${Math.floor(elapsed / 60)}м ${elapsed % 60}с` : `${elapsed}с`
  const determinate = !!status.total && (status.total ?? 0) > 0
  return (
    <div style={{ margin: '0.4rem 0' }}>
      <ProgressBar
        label={phaseLabel}
        value={determinate ? (status.step ?? 0) : undefined}
        max={determinate ? (status.total ?? 1) : undefined}
        size="small"
      />
      <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'baseline', marginTop: '0.3rem', fontSize: '0.7rem', color: 'var(--tm-text-3)' }}>
        <span>{phaseLabel}</span>
        {elapsed > 0 && <span style={{ marginLeft: 'auto', flexShrink: 0 }}>{clock}</span>}
      </div>
      {status.last_line && (
        <div className="mono" style={{ marginTop: '0.2rem', fontSize: '0.64rem', color: 'var(--tm-text-3)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={status.last_line}>
          {status.last_line}
        </div>
      )}
      {status.failed && status.error && (
        <div style={{ marginTop: '0.2rem', fontSize: '0.7rem', color: 'var(--tm-danger)', overflowWrap: 'anywhere' }}>{status.error}</div>
      )}
    </div>
  )
}
