import type { FrpEvent, Json } from './types'
import { eventLabel } from './timeline'

export interface EventSummary {
  /** What happened, in human terms: "read_file · file_content cmd/app/main.go". */
  title: string
  /** Secondary line: observed content preview, entry names, sizes. */
  detail?: string
  /** Tertiary provenance line: confidence, extractor, world version. */
  meta?: string
}

/** Bookkeeping event types attention emits about its own operation. */
export const ATTENTION_HINT_TYPES = ['attention.suggested', 'attention.selected']

export function isAttentionHint(event: FrpEvent): boolean {
  return ATTENTION_HINT_TYPES.includes(eventLabel(event))
}

export function describeEvent(event: FrpEvent): EventSummary {
  const payload = (event.payload ?? {}) as Record<string, Json>
  switch (eventLabel(event)) {
    case 'world.observation':
      return describeObservation(payload)
    case 'claim.candidate':
      return describeClaim(payload)
    case 'attention.suggested':
      return { title: `hint → ${compactRef(String(payload.ref ?? ''))}`, meta: fromEmission(payload) }
    case 'attention.selected': {
      const target = (payload.target ?? {}) as Record<string, Json>
      const focus = typeof target.text === 'string' && target.text ? `"${preview(target.text, 60)}"` : compactRef(`${target.type ?? ''}:${target.id ?? ''}`)
      return { title: `attend ${focus}`, meta: fromEmission(payload) }
    }
    case 'execution.created':
      return { title: `execution ${shortId(stringOf(payload.execution_id))} registered`, meta: fromEmission(payload) }
    case 'execution.started':
      return { title: `execution ${shortId(stringOf(payload.execution_id))} started` }
    case 'execution.completed':
      return { title: `execution ${shortId(stringOf(payload.execution_id))} completed` }
    case 'execution.failed':
      return { title: `execution ${shortId(stringOf(payload.execution_id))} failed`, detail: preview(stringOf(payload.error), 140) }
    case 'affordance.requested':
      return { title: `action request ${shortId(stringOf(payload.request_id))} (pending validation)`, meta: fromEmission(payload) }
    case 'frame.created':
      return { title: 'frame created — first render of this branch', meta: agentMeta(payload) }
    case 'frame.transitioned': {
      const parent = shortId(stringOf(payload.parent_frame_id))
      const next = shortId(stringOf(payload.frame_id))
      return { title: `frame ${parent} → ${next} (emission committed)`, meta: fromEmission(payload) }
    }
    case 'episode.started': {
      const world = stringOf(payload.world_id)
      const meta: string[] = []
      if (payload.bootstrap === true) meta.push('bootstrap discovery')
      if (stringOf(payload.objective_id)) meta.push(`objective ${shortId(stringOf(payload.objective_id))}`)
      return { title: world ? `episode started in world ${world}@v${payload.world_version ?? 1}` : 'episode started', meta: meta.join(' · ') || undefined }
    }
    default: {
      const compact = compactJson(event.payload)
      return { title: compact ? compact : 'no payload' }
    }
  }
}

function describeObservation(payload: Record<string, Json>): EventSummary {
  const kind = stringOf(payload.observation_type) || 'observation'
  const actor = stringOf(payload.affordance_id) || (payload.source_resource_id ? `ingest:${stringOf(payload.source_resource_id)}` : 'observation')
  const inner = (payload.payload ?? {}) as Record<string, Json>
  const title = `${actor} · ${kind} ${relativePath(stringOf(inner.path), stringOf(payload.resource))}`.trim()
  const meta = [payload.world_id ? `${stringOf(payload.world_id)}@v${payload.world_version ?? 1}` : '', payload.truncated ? 'payload truncated' : ''].filter(Boolean).join(' · ')
  switch (kind) {
    case 'file_content':
      return { title, detail: `${preview(stringOf(inner.content), 160)} (${numberOf(inner.size)} B)`, meta }
    case 'directory_listing': {
      const items = Array.isArray(inner.items) ? inner.items : []
      const names = items.slice(0, 5).map((item) => stringOf((item as Record<string, Json>)?.name)).filter(Boolean)
      const rest = items.length - names.length
      return { title, detail: [`${numberOf(inner.entries)} entries`, names.length ? `: ${names.join(', ')}` : '', rest > 0 ? ` +${rest} more` : ''].join(''), meta }
    }
    case 'stat':
      return { title, detail: `${inner.directory ? 'directory' : 'file'} · ${inner.exists === false ? 'missing' : `${numberOf(inner.size)} B`}`, meta }
    default:
      return { title, detail: compactJson(inner) || undefined, meta }
  }
}

function describeClaim(payload: Record<string, Json>): EventSummary {
  const proposition = stringOf(payload.proposition)
  const confidence = payload.confidence
  const source = payload.extractor ? `extractor ${stringOf(payload.extractor)}` : fromEmission(payload)
  const meta = [typeof confidence === 'number' ? `confidence ${Math.round(confidence * 100)}%` : '', source].filter(Boolean).join(' · ')
  if (proposition) {
    const triple = [payload.subject, payload.predicate, payload.object].map((part) => stringOf(part)).filter(Boolean).join(' ')
    return { title: proposition, meta: [meta, triple].filter(Boolean).join(' · ') }
  }
  // Legacy cognitive-step events stored only a claim pointer; the proposition
  // lives in the claims table, not in the event payload.
  return { title: `claim ${shortId(stringOf(payload.claim_id))} (proposition not stored in event)`, meta }
}

function fromEmission(payload: Record<string, Json>): string {
  const emission = stringOf(payload.emission_id)
  return emission ? `emission ${shortId(emission)}` : ''
}

function agentMeta(payload: Record<string, Json>): string {
  const agent = stringOf(payload.agent_id)
  return agent ? `agent ${shortId(agent)}` : ''
}

function relativePath(path: string, resource: string): string {
  if (!path) return ''
  const root = resource.startsWith('filesystem:') ? resource.slice('filesystem:'.length) : ''
  if (root && path.startsWith(root)) return path.slice(root.length) || '/'
  return path
}

function compactRef(ref: string): string {
  const [kind, id] = ref.split(':')
  if (!id) return ref || '?'
  return `${kind}:${shortId(id)}`
}

function shortId(value: string): string {
  if (!value) return '?'
  return value.slice(0, 8)
}

function stringOf(value: Json | undefined): string {
  return typeof value === 'string' ? value : ''
}

function numberOf(value: Json | undefined): string {
  return typeof value === 'number' ? String(value) : '—'
}

function preview(value: string, width: number): string {
  const line = value.replace(/\s+/g, ' ').trim()
  return line.length <= width ? line : `${line.slice(0, width - 1)}…`
}

function compactJson(value: unknown): string {
  if (value === undefined || value === null) return ''
  try {
    const text = JSON.stringify(value)
    return preview(text, 160)
  } catch {
    return ''
  }
}
