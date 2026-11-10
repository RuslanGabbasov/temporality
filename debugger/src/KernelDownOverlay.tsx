import { useEffect, useState } from 'react'
import { useT } from './i18n'
import {
  isKernelDown, kernelDownKind, probeKernelLiveness,
  reportKernelReachable, reportKernelTransportFailure,
  subscribeKernelAvailability, type KernelDownKind,
} from './kernelAvailability'

/** The calm «kernel is restarting» overlay. While the agent-kernel container
 *  is down (deploy/restart), every kernel-api call fails; pages used to spray
 *  raw 502 banners and users read a routine restart as a breakage. This
 *  overlay covers the app chrome with a quiet explanation + a live poll that
 *  lifts itself the instant the kernel answers /livez again — including the
 *  boot phase: /livez is served by the early listener from second one, so
 *  the overlay disappears while migrations still run, and the page data
 *  fetches underneath retry on their own schedules.
 *
 *  Two stories, honestly told: proxy 502/503/504 → the kernel is restarting
 *  (routine, waits itself out); fetch that got no HTTP answer at all → the
 *  server is simply unreachable (dropped VPN, dead network) — «нет связи»,
 *  not a restart. The copy follows the freshest transport evidence.
 *
 *  Rendered at the App root, above every page. */
export default function KernelDownOverlay() {
  const t = useT()
  const [down, setDown] = useState(isKernelDown())
  const [kind, setKind] = useState<KernelDownKind>(kernelDownKind())
  const [seconds, setSeconds] = useState(0)

  useEffect(() => subscribeKernelAvailability((state) => {
    setDown(state.down)
    setKind(state.kind)
  }), [])

  // While down: tick the elapsed counter and poll /livez every 3s.
  //  - 'up' — ANY kernel answer (200 from the boot listener, even 404) means
  //    the process is alive: lift the overlay, the pages' own fetch retries
  //    take over.
  //  - 'gateway' — the proxy answered 502/503/504: still down, restart copy.
  //  - 'unreachable' — nothing answered: still down, no-connection copy.
  useEffect(() => {
    if (!down) { setSeconds(0); return }
    const tick = window.setInterval(() => setSeconds((s) => s + 1), 1000)
    const probe = () => {
      void probeKernelLiveness().then((result) => {
        if (result === 'up') reportKernelReachable()
        else reportKernelTransportFailure(result)
      })
    }
    const poll = window.setInterval(probe, 3000)
    // An immediate probe too — fast restarts (a few seconds) shouldn't sit
    // through a full poll interval.
    probe()
    return () => { window.clearInterval(tick); window.clearInterval(poll) }
  }, [down])

  if (!down) return null

  const unreachable = kind === 'unreachable'
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
        <div style={{ fontSize: '2rem', marginBottom: '0.4rem' }}>{unreachable ? '📡' : '⏳'}</div>
        <div style={{ fontSize: '1rem', fontWeight: 600, marginBottom: '0.45rem' }}>
          {unreachable
            ? (t('kernel_down.unreachable_title') ?? 'No connection to the server')
            : (t('kernel_down.title') ?? 'The agent kernel is restarting')}
        </div>
        <p style={{ fontSize: '0.82rem', color: 'var(--tm-text-2)', margin: '0 0 0.9rem', lineHeight: 1.5 }}>
          {unreachable
            ? (t('kernel_down.unreachable_body') ?? 'The page cannot reach the server — check your network or VPN. Nothing is lost; everything continues by itself once the connection is back.')
            : (t('kernel_down.body') ?? 'This is routine — a deploy or restart is in progress. Your data is safe; running agents continue in the background. The page will come back by itself.')}
        </p>
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', gap: '0.5rem', fontSize: '0.75rem', color: 'var(--tm-text-3)' }}>
          <span className="spinner spinner-sm" />
          {t('kernel_down.waiting', { seconds: String(seconds) }) ?? `waiting… ${seconds}s`}
        </div>
      </div>
    </div>
  )
}
