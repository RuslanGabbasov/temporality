/** Offline-aware reconnect helpers (docs/resilience-and-scale.md): a laptop
 *  that slept through a network switch wakes with a graveyard of dead
 *  sockets. Retrying immediately (and every backoff tick) while
 *  navigator.onLine is false burns hundreds of pointless requests and buries
 *  the real failures in the network log. waitOnline() resolves right away
 *  when the browser reports connectivity, otherwise as soon as the 'online'
 *  event fires (plus one tick of grace: the event can race the actual
 *  socket). */

export function isOffline(): boolean {
  return typeof navigator !== 'undefined' && navigator.onLine === false
}

/** Resolve when the browser reports the network is back (immediately when it
 *  already is). The signal aborts the wait. */
export function waitOnline(signal?: AbortSignal): Promise<void> {
  if (!isOffline()) return Promise.resolve()
  return new Promise((resolve) => {
    const done = () => {
      window.removeEventListener('online', done)
      signal?.removeEventListener('abort', done)
      // One beat of grace: 'online' can fire while the socket is still coming
      // up; the next request failing simply re-enters the wait.
      window.setTimeout(resolve, 300)
    }
    window.addEventListener('online', done)
    signal?.addEventListener('abort', done, { once: true })
  })
}
