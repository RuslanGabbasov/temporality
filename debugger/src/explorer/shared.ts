/** Shared knowledge presentation helpers for the explorer family. */

export function stateLabel(state: string, t: (key: string) => string): string {
  const key = `knowledge.state_${state}`
  const text = t(key)
  return text !== key ? text : state
}

export function kindLabel(kind: string | undefined, t: (key: string) => string): string {
  if (kind === 'observation') return t('explorer.kind_observation') ?? 'Observation'
  return t('explorer.kind_claim') ?? 'Claim'
}
