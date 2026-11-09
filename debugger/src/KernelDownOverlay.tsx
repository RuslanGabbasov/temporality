import { useEffect, useState } from 'react'
import { useT } from './i18n'
import { isKernelDown, probeKernelLiveness, reportKernelReachable, subscribeKernelAvailability } from './kernelAvailability'

/** The calm «kernel is restarting» overlay. While the agent-kernel container
 *  is down (deploy/restart), every kernel-api call fails; pages used to spray
 *  raw 502 banners and users read a routine restart as a breakage. This
 *  overlay covers the app chrome with a quiet explanation + a live poll that
 *  lifts itself the instant the kernel answers /livez again — including the
 *  boot phase: /livez is served by the early listener from second one, so
 *  the overlay disappears while migrations still run, and the page data
 *  fetches underneath retry on their own schedules.
 *
 *  Rendered at the App root, above every page. */
export default function KernelDownOverlay() {
  const t = useT()
  const [down, setDown] = useState(isKernelDown())
  const [seconds, setSeconds] = useState(0)

  useEffect(() => subscribeKernelAvailability(setDown), [])

  // While down: tick the elapsed counter and poll /livez every 3s. Any HTTP
  // answer — 200 from the boot listener included — means the process is
  // alive; the overlay lifts and the pages' own fetch retries take over.
  useEffect(() => {
    if (!down) { setSeconds(0); return }
    const tick = window.setInterval(() => setSeconds((s) => s + 1), 1000)
    const poll = window.setInterval(() => {
      void probeKernelLiveness().then((alive) => { if (alive) reportKernelReachable() })
    }, 3000)
    // An immediate probe too — fast restarts (a few seconds) shouldn't sit
    // through a full poll interval.
    void probeKernelLiveness().then((alive) => { if (alive) reportKernelReachable() })
    return () => { window.clearInterval(tick); window.clearInterval(poll) }
  }, [down])

  if (!down) return null

  return (
    <div
      role="alert"
      aria-live="polite"
      style={{
        position: 'fixed', inset: 0, zIndex: 10000,
        display: 'grid', placeItems: 'center',
        background: 'color-mix(in srgb, var(--tm-bg) 78%, transparent)',
        backdropFilter: 'blur(3px)',
      }}
    >
      <div
        style={{
          maxWidth: '26rem', width: 'calc(100% - 3rem)',
          background: 'var(--tm-elevated, var(--tm-surface))',
          border: '1px solid var(--tm-border)',
          borderRadius: '12px',
          padding: '1.5rem 1.6rem',
          textAlign: 'center',
          boxShadow: '0 12px 40px rgba(0,0,0,0.35)',
        }}
      >
        <div style={{ fontSize: '2rem', marginBottom: '0.4rem' }}>⏳</div>
        <div style={{ fontSize: '1rem', fontWeight: 600, marginBottom: '0.45rem' }}>
          {t('kernel_down.title') ?? 'The agent kernel is restarting'}
        </div>
        <p style={{ fontSize: '0.82rem', color: 'var(--tm-text-2)', margin: '0 0 0.9rem', lineHeight: 1.5 }}>
          {t('kernel_down.body') ?? 'This is routine — a deploy or restart is in progress. Your data is safe; running agents continue in the background. The page will come back by itself.'}
        </p>
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', gap: '0.5rem', fontSize: '0.75rem', color: 'var(--tm-text-3)' }}>
          <span className="spinner spinner-sm" />
          {t('kernel_down.waiting', { seconds: String(seconds) }) ?? `waiting for the kernel… ${seconds}s`}
        </div>
      </div>
    </div>
  )
}
