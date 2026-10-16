/** Session payload v1 (docs/human-knowledge-explorer.md §11): the research
 *  position — question and filters — plus the researcher's notes and
 *  conclusion. Every field is a REFERENCE into the current projection; the
 *  explorer re-resolves it on open, so a saved session never snapshots
 *  knowledge and honors revoked rights (§11.3).
 *
 *  The filter shape mirrors the explorer's Filters type (state/scope/topic/
 *  entity/origin/kind); it is declared structurally here so the module stays
 *  import-cycle-free. */

export const FILTER_KEYS = ['state', 'scope', 'topic', 'entity', 'origin', 'kind'] as const

export interface SessionFilters extends Record<(typeof FILTER_KEYS)[number], string> {}

export interface ResearchSessionPayload {
  version: 1
  question: string
  searchInput: string
  filters: SessionFilters
  areaKey: string
  view: string
  dossierID: string
  notes: string
  conclusion: string
}

/** What the explorer needs to hand over for a snapshot: its own live state. */
export interface SessionSnapshot {
  question: string
  searchInput: string
  filters: SessionFilters
  areaKey: string
  view: string
  dossierID: string
  notes: string
  conclusion: string
}

function emptyFilters(): SessionFilters {
  return { state: '', scope: '', topic: '', entity: '', origin: '', kind: '' }
}

/** Serialize the current research position. */
export function sessionSnapshot(state: SessionSnapshot): ResearchSessionPayload {
  return {
    version: 1,
    question: state.question,
    searchInput: state.searchInput,
    filters: { ...state.filters },
    areaKey: state.areaKey,
    view: state.view,
    dossierID: state.dossierID,
    notes: state.notes,
    conclusion: state.conclusion,
  }
}

function str(value: unknown): string {
  return typeof value === 'string' ? value : ''
}

/** Keeps only the known single-value dimensions; foreign or non-string
 *  entries are dropped rather than crashing the restore. */
function parseFilters(value: unknown): SessionFilters {
  const result = emptyFilters()
  if (!value || typeof value !== 'object' || Array.isArray(value)) return result
  const raw = value as Record<string, unknown>
  for (const key of FILTER_KEYS) {
    if (typeof raw[key] === 'string') result[key] = raw[key] as string
  }
  return result
}

/** Deserialize with tolerance: a payload written by an older or newer client
 *  (missing or foreign fields) restores what it can instead of throwing —
 *  sessions outlive UI refactors (§11.4). Returns null when the value is not
 *  an object at all. The historical `nav` path list is no longer restored:
 *  the explorer dropped it. */
export function applySessionPayload(payload: unknown): SessionSnapshot | null {
  if (!payload || typeof payload !== 'object' || Array.isArray(payload)) return null
  const raw = payload as Record<string, unknown>
  return {
    question: str(raw.question),
    searchInput: str(raw.searchInput) || str(raw.question),
    filters: parseFilters(raw.filters),
    areaKey: str(raw.areaKey),
    view: raw.view === 'conflicts' || raw.view === 'agent' ? raw.view : 'search',
    dossierID: str(raw.dossierID),
    notes: str(raw.notes),
    conclusion: str(raw.conclusion),
  }
}
