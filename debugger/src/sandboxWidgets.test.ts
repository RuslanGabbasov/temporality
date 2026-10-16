import { describe, expect, it } from 'vitest'
import { sandboxLoad, maxSandboxNet, sandboxDiameter, sandboxColor, topSandboxes, sandboxShortName } from './sandboxWidgets'
import type { SandboxContainerInfo } from './kernelApi'

const container = (overrides: Partial<SandboxContainerInfo>): SandboxContainerInfo => ({
  name: 'temporality-sandbox-x',
  image: 'temporality-sandbox:latest',
  cpu_percent: 0,
  mem_used_bytes: 0,
  mem_limit_bytes: 1 << 30,
  mem_percent: 0,
  net_in_bytes: 0,
  net_out_bytes: 0,
  ...overrides,
})

describe('sandboxLoad', () => {
  it('weights cpu/memory double against relative network', () => {
    // CPU + memory saturated, no network: 4/5 — already "hog" territory.
    const busy = container({ cpu_percent: 100, mem_percent: 100 })
    expect(sandboxLoad(busy, 0)).toBe(0.8)
    // Network: half of the max total of the set contributes 0.5/5.
    const netOnly = container({ net_in_bytes: 500, net_out_bytes: 0 })
    expect(sandboxLoad(netOnly, 1000)).toBeCloseTo(0.1, 5)
    const idle = container({})
    expect(sandboxLoad(idle, 1000)).toBe(0)
  })

  it('clamps cpu percentages above 100 (multi-core bursts)', () => {
    // (2 * clamp(250%) ) / 5 — saturation alone cannot exceed its weight.
    expect(sandboxLoad(container({ cpu_percent: 250 }), 0)).toBe(0.4)
  })
})

describe('topSandboxes', () => {
  it('sorts by load descending and folds the tail into rest', () => {
    const list = [
      container({ name: 'a' }),
      container({ name: 'b', cpu_percent: 80 }),
      container({ name: 'c', cpu_percent: 20 }),
      ...Array.from({ length: 10 }, (_, i) => container({ name: `t${i}`, mem_percent: 1 })),
    ]
    const { top, rest } = topSandboxes(list, 3)
    expect(top.map((c) => c.name)).toEqual(['b', 'c', ...['t0', 't1'].slice(0, 1)])
    expect(top).toHaveLength(3)
    expect(rest).toHaveLength(list.length - 3)
    expect(sandboxLoad(top[0], maxSandboxNet(list))).toBeGreaterThanOrEqual(sandboxLoad(top[1], maxSandboxNet(list)))
  })

  it('keeps deterministic order for equal loads', () => {
    const list = [container({ name: 'x' }), container({ name: 'y' })]
    const first = topSandboxes(list, 2).top
    const second = topSandboxes([...list].reverse(), 2).top
    // Equal loads tie-break by name, so the dashboard must not reshuffle
    // bubbles between refreshes when docker ps reports a different order.
    expect(second.map((c) => c.name)).toEqual(first.map((c) => c.name))
    expect(first.map((c) => c.name)).toEqual(['x', 'y'])
  })
})

describe('presentation helpers', () => {
  it('maps the score to size and severity color', () => {
    expect(sandboxDiameter(0)).toBe(30)
    expect(sandboxDiameter(1)).toBe(72)
    expect(sandboxDiameter(0.25)).toBeLessThan(sandboxDiameter(0.75))
    expect(sandboxColor(0.9)).toBe('var(--tm-danger)')
    expect(sandboxColor(0.5)).toBe('var(--tm-amber)')
    expect(sandboxColor(0.1)).toBe('var(--tm-teal)')
  })

  it('strips the shared container-name prefix', () => {
    expect(sandboxShortName('temporality-sandbox-abc123')).toBe('abc123')
    expect(sandboxShortName('other')).toBe('other')
  })
})
