/** Kernel availability tracking (the «kernel is restarting» overlay).
 *
 * When the agent-kernel container restarts (a deploy, a compose up), nginx
 * answers /kernel-api/ with 502 and every page's data fetch fails with a
 * scary raw error. Users saw "502 Bad Gateway" walls and thought everything
 * broke. This module is the single source of truth for "the kernel is
 * DOWN right now": API layers report transport-level failures (502/503/504,
 * network refusal) here, the App subscribes and renders a calm overlay with
 * a liveness poll that lifts it the moment the kernel answers /livez again.
 *
 * Application-level 4xx/5xx from a HEALTHY kernel are none of this module's
 * business — those are real errors with real causes. */

export type KernelAvailabilityListener = (down: boolean) => void

const listeners = new Set<KernelAvailabilityListener>()
let down = false

export function isKernelDown(): boolean {
  return down
}

export function subscribeKernelAvailability(listener: KernelAvailabilityListener): () => void {
  listeners.add(listener)
  listener(down)
  return () => { listeners.delete(listener) }
}

/** An API transport failure that smells like "the kernel process is not
 * there": nginx 502/503/504 from the proxy, or the browser's network-level
 * refusal. Called by the request layers; idempotent while down. */
export function reportKernelTransportFailure(): void {
  if (down) return
  down = true
  listeners.forEach((l) => { try { l(true) } catch { /* listener errors are not ours */ } })
  // The poll below (started by whoever subscribes) reports recovery.
}

/** The kernel answered something transport-level sane — lift the overlay.
 * Called by the liveness poll; also safe to call opportunistically from any
 * successful response path (cheap no-op when already up). */
export function reportKernelReachable(): void {
  if (!down) return
  down = false
  listeners.forEach((l) => { try { l(false) } catch { /* ditto */ } })
}

/** One liveness probe: fetch /kernel-api/v1/… anything cheap — /livez needs
 * no auth (the boot listener answers it from second one). Resolves true when
 * ANY HTTP status came back (even 404 means the kernel process is up and
 * speaking HTTP); false only on network refusal. */
export async function probeKernelLiveness(): Promise<boolean> {
  try {
    await fetch('/kernel-api/livez', { cache: 'no-store' })
    return true
  } catch {
    return false
  }
}
