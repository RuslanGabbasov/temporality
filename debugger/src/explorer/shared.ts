/** Shared knowledge presentation helpers for the explorer family. */

export function stateLabel(state: string, t: (key: string) => string): string {
  const key = `knowledge.state_${state}`
  const text = t(key)
  return text !== key ? text : state
}

/** Facet scope values are raw API identifiers (project/org_unit/organization);
 * `knowledge.scope_organization` means "the whole organization" in run-event
 * texts, so organization maps to `knowledge.scope_org` explicitly. */
const SCOPE_KEYS: Record<string, string> = {
  project: 'knowledge.scope_project',
  org_unit: 'knowledge.scope_org_unit',
  organization: 'knowledge.scope_org',
}

export function scopeLabel(scope: string, t: (key: string) => string): string {
  const key = SCOPE_KEYS[scope]
  return key ? (t(key) ?? scope) : scope
}

export function kindLabel(kind: string | undefined, t: (key: string) => string): string {
  if (kind === 'observation') return t('explorer.kind_observation') ?? 'Observation'
  return t('explorer.kind_claim') ?? 'Claim'
}
