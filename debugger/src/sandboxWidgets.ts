/** Sandbox bubble widgets (dashboard): size and color encode the integral
 *  resource load (CPU + memory + network) so the host-hogging run is visible
 *  at a glance. Pure functions, unit-tested. */

import type { SandboxContainerInfo } from './kernelApi'

export const SANDBOX_TOP_N = 8

/** Integral load score 0..1: CPU and memory are absolute (percent of the
 *  container's own limits — docker reports both against one core / the cgroup
 *  cap) and carry double weight — a host hog usually saturates compute, not
 *  network. Network is relative to the busiest container in the current set,
 *  since docker only exposes cumulative counters and a bare byte count means
 *  nothing without a peer to compare with. */
export function sandboxLoad(container: SandboxContainerInfo, maxNetBytes: number): number {
  const cpu = Math.min(1, container.cpu_percent / 100)
  const mem = Math.min(1, container.mem_percent / 100)
  const net = maxNetBytes > 0 ? Math.min(1, (container.net_in_bytes + container.net_out_bytes) / maxNetBytes) : 0
  return (cpu * 2 + mem * 2 + net) / 5
}

/** Max total network bytes across the set (the relative-net denominator). */
export function maxSandboxNet(containers: SandboxContainerInfo[]): number {
  return containers.reduce((max, c) => Math.max(max, c.net_in_bytes + c.net_out_bytes), 0)
}

/** Bubble diameter in px: 30 (idle) to 72 (saturating), area grows with load
 *  so "twice the load" reads as visibly bigger without dwarfing the row. */
export function sandboxDiameter(score: number): number {
  return Math.round(30 + Math.sqrt(Math.max(0, Math.min(1, score))) * 42)
}

/** Severity color: teal while quiet, amber as the container starts competing
 *  for the host, red when it clearly hogs it. */
export function sandboxColor(score: number): string {
  if (score >= 0.7) return 'var(--tm-danger)'
  if (score >= 0.35) return 'var(--tm-amber)'
  return 'var(--tm-teal)'
}

/** Top-N by load for bubbles; the rest collapse into one "+N" chip. Ties
 *  break by name so bubbles do not reshuffle between refreshes when docker ps
 *  reports containers in a different order. */
export function topSandboxes(containers: SandboxContainerInfo[], n = SANDBOX_TOP_N): { top: SandboxContainerInfo[]; rest: SandboxContainerInfo[] } {
  const maxNet = maxSandboxNet(containers)
  const sorted = [...containers].sort((a, b) =>
    sandboxLoad(b, maxNet) - sandboxLoad(a, maxNet) || a.name.localeCompare(b.name))
  return { top: sorted.slice(0, n), rest: sorted.slice(n) }
}

/** Short display name: strip the shared prefix so the caption fits the bubble. */
export function sandboxShortName(name: string): string {
  return name.startsWith('temporality-sandbox-') ? name.slice('temporality-sandbox-'.length) : name
}
