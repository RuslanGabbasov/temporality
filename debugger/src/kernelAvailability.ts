/** Kernel availability tracking (the «kernel is restarting» overlay).
 *
 * When the agent-kernel container restarts (a deploy, a compose up), nginx
 * answers /kernel-api/ with 502 and every page's data fetch fails with a
 * scary raw error. Users saw "502 Bad Gateway" walls and thought everything
 * broke. This module is the single source of truth for "the kernel is
 * DOWN right now": API layers report transport-level failures here, the App
 * subscribes and renders a calm overlay with a liveness poll that lifts it
 * the moment the kernel answers /livez again.
 *
 * Two different stories share the overlay, and the user deserves the right
 * one: a 502/503/504 from the proxy means the web layer is UP and the kernel
 * is not (kind 'gateway' — a routine restart); a fetch that never got an
 * HTTP answer means the browser could not reach the server at all (kind
 * 'unreachable' — down network, dropped VPN, powered-off host), which is
 * not a restart and must not read as one.
 *
 * Application-level 4xx/5xx from a HEALTHY kernel are none of this module's
 * business — those are real errors with real causes. */

export type KernelDownKind = 'gateway' | 'unreachable'

export interface KernelAvailabilityState {
  down: boolean
  /** Why we are down, as of the most recent transport evidence. Stays set
   *  after recovery (harmless — only read while down). */
  kind: KernelDownKind
}

export type KernelAvailabilityListener = (state: KernelAvailabilityState) => void

const listeners = new Set<KernelAvailabilityListener>()
let down = false
let kind: KernelDownKind = 'gateway'

export function isKernelDown(): boolean {
  return down
}

/** The current failure story — meaningful while down, stale after recovery. */
export function kernelDownKind(): KernelDownKind {
  return kind
}

export function subscribeKernelAvailability(listener: KernelAvailabilityListener): () => void {
  listeners.add(listener)
  listener({ down, kind })
  return () => { listeners.delete(listener) }
}

function notify(): void {
  const state = { down, kind }
  listeners.forEach((l) => { try { l(state) } catch { /* listener errors are not ours */ } })
}

/** An API transport failure. kind 'gateway' — the proxy answered 502/503/504
 *  (kernel process not there, restart in progress); 'unreachable' — no HTTP
 *  answer at all (network refusal, dropped VPN). Called by the request
 *  layers; while down, a CHANGED kind re-notifies so the overlay's copy
 *  follows the freshest evidence. */
export function reportKernelTransportFailure(failureKind: KernelDownKind): void {
  if (down && kind === failureKind) return
  down = true
  kind = failureKind
  notify()
  // The poll (started by whoever subscribes) reports recovery.
}

/** The kernel answered something transport-level sane — lift the overlay.
 * Called by the liveness poll; also safe to call opportunistically from any
 * successful response path (cheap no-op when already up). */
export function reportKernelReachable(): void {
  if (!down) return
  down = false
  notify()
}

/** What a single liveness probe learned:
 *  - 'up'          — any kernel answer (200 from the boot listener, 401 auth
 *                    wall, even 404: the process is speaking HTTP);
 *  - 'gateway'     — the WEB layer answered 502/503/504: the proxy is alive,
 *                    the kernel is not (a restart, most likely);
 *  - 'unreachable' — fetch rejected: nothing answered, the browser could not
 *                    reach the server at all. */
export type KernelProbe = 'up' | 'gateway' | 'unreachable'

export async function probeKernelLiveness(): Promise<KernelProbe> {
  try {
    const response = await fetch('/kernel-api/livez', { cache: 'no-store' })
    if (response.status === 502 || response.status === 503 || response.status === 504) return 'gateway'
    return 'up'
  } catch {
    return 'unreachable'
  }
}
