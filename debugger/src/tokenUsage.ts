import type { RenderPacket, RenderSection, TokenUsage } from './types'

export interface TokenUsageView {
  estimated?: number
  budget?: number
  percent?: number
  remaining?: number
  warning: boolean
}

function finiteNonNegative(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0 ? value : undefined
}

export function tokenUsageView(usage?: TokenUsage | null): TokenUsageView {
  const estimated = finiteNonNegative(usage?.estimated)
  const budget = finiteNonNegative(usage?.budget)
  const percent = estimated !== undefined && budget !== undefined && budget > 0 ? (estimated / budget) * 100 : undefined
  return {
    estimated,
    budget,
    percent,
    remaining: estimated !== undefined && budget !== undefined ? Math.max(0, budget - estimated) : undefined,
    warning: percent !== undefined && percent > 85,
  }
}

export function renderSection(packet: RenderPacket | null | undefined, kind: RenderSection['kind']): RenderSection | undefined {
  return packet?.sections.find((section) => section.kind === kind)
}
