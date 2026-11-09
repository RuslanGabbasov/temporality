import { describe, expect, it, vi, beforeEach } from 'vitest'

/** The kernel-availability tracker: transport failures flip it down, any
 *  successful call flips it back, and listeners hear both transitions. The
 *  overlay's whole behavior hangs off this — raw 502s must never reach the
 *  user as page-level error walls again. */

describe('kernelAvailability', () => {
  beforeEach(async () => {
    vi.resetModules()
  })

  async function fresh() {
    const mod = await import('./kernelAvailability')
    return mod
  }

  it('starts up; a transport failure flips it down; a success flips it back', async () => {
    const mod = await fresh()
    expect(mod.isKernelDown()).toBe(false)
    const seen: boolean[] = []
    mod.subscribeKernelAvailability((down) => seen.push(down))
    // The subscribe callback fires immediately with the current state.
    expect(seen).toEqual([false])

    mod.reportKernelTransportFailure()
    expect(mod.isKernelDown()).toBe(true)
    mod.reportKernelTransportFailure() // idempotent while down
    expect(mod.isKernelDown()).toBe(true)

    mod.reportKernelReachable()
    expect(mod.isKernelDown()).toBe(false)
    mod.reportKernelReachable() // no-op while up
    expect(mod.isKernelDown()).toBe(false)
    expect(seen).toEqual([false, true, false])
  })

  it('unsubscribe stops deliveries', async () => {
    const mod = await fresh()
    const seen: boolean[] = []
    const off = mod.subscribeKernelAvailability((down) => seen.push(down))
    off()
    mod.reportKernelTransportFailure()
    expect(seen).toEqual([false])
  })
})
