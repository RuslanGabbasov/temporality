import { describe, expect, it, vi, beforeEach } from 'vitest'

/** The kernel-availability tracker: transport failures flip it down, any
 *  successful call flips it back, and listeners hear both transitions. The
 *  overlay's whole behavior hangs off this — raw 502s must never reach the
 *  user as page-level error walls again.
 *
 *  The tracker also remembers WHY it is down: a proxy 502/503/504 is a
 *  restart ('gateway'), a fetch with no HTTP answer is a dead connection
 *  ('unreachable' — dropped VPN). The overlay's copy follows the kind. */

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
    const seen: Array<{ down: boolean; kind: string }> = []
    mod.subscribeKernelAvailability((state) => seen.push({ ...state }))
    // The subscribe callback fires immediately with the current state.
    expect(seen).toEqual([{ down: false, kind: 'gateway' }])

    mod.reportKernelTransportFailure('gateway')
    expect(mod.isKernelDown()).toBe(true)
    expect(mod.kernelDownKind()).toBe('gateway')
    mod.reportKernelTransportFailure('gateway') // idempotent while down, same kind
    expect(mod.isKernelDown()).toBe(true)
    expect(seen).toHaveLength(2) // no duplicate notification

    mod.reportKernelReachable()
    expect(mod.isKernelDown()).toBe(false)
    mod.reportKernelReachable() // no-op while up
    expect(mod.isKernelDown()).toBe(false)
    expect(seen.map((s) => s.down)).toEqual([false, true, false])
  })

  it('a kind change while down re-notifies — the overlay copy follows the freshest evidence', async () => {
    const mod = await fresh()
    const seen: Array<{ down: boolean; kind: string }> = []
    mod.subscribeKernelAvailability((state) => seen.push({ ...state }))

    // VPN dropped: fetches get no HTTP answer at all.
    mod.reportKernelTransportFailure('unreachable')
    expect(mod.kernelDownKind()).toBe('unreachable')
    // VPN is back but the kernel is mid-restart: 502s arrive.
    mod.reportKernelTransportFailure('gateway')
    expect(mod.kernelDownKind()).toBe('gateway')
    expect(mod.isKernelDown()).toBe(true)
    // Same kind again — no duplicate notification.
    mod.reportKernelTransportFailure('gateway')
    expect(seen).toEqual([
      { down: false, kind: 'gateway' },
      { down: true, kind: 'unreachable' },
      { down: true, kind: 'gateway' },
    ])
  })

  it('unsubscribe stops deliveries', async () => {
    const mod = await fresh()
    const seen: Array<{ down: boolean; kind: string }> = []
    const off = mod.subscribeKernelAvailability((state) => seen.push({ ...state }))
    off()
    mod.reportKernelTransportFailure('gateway')
    expect(seen).toEqual([{ down: false, kind: 'gateway' }])
  })

  it('probeKernelLiveness reads a fetch stub: up / gateway / unreachable', async () => {
    const mod = await fresh()

    // Kernel speaking HTTP (even a 404 auth-less probe) — up.
    vi.stubGlobal('fetch', vi.fn(async () => ({ status: 404 })))
    await expect(mod.probeKernelLiveness()).resolves.toBe('up')

    // Proxy answered, kernel absent — a restart, not a recovery.
    vi.stubGlobal('fetch', vi.fn(async () => ({ status: 502 })))
    await expect(mod.probeKernelLiveness()).resolves.toBe('gateway')

    // No HTTP answer at all — the server is unreachable.
    vi.stubGlobal('fetch', vi.fn(async () => { throw new TypeError('Failed to fetch') }))
    await expect(mod.probeKernelLiveness()).resolves.toBe('unreachable')

    vi.unstubAllGlobals()
  })
})
