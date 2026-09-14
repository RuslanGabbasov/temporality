import type { ModelStepResponse } from './types'

export type ModelAnswer =
  | { kind: 'answer'; label: 'Model answer'; text: string }
  | { kind: 'completion'; label: 'Model answer'; text: string }
  | { kind: 'cognition'; label: 'Model cognition (no explicit answer)'; text: string }
  | { kind: 'empty'; label: 'Model answer'; text: '' }

function record(value: unknown): Record<string, unknown> | undefined {
  return typeof value === 'object' && value !== null && !Array.isArray(value) ? value as Record<string, unknown> : undefined
}

function nonEmptyText(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value : undefined
}

export function extractModelAnswer(response: ModelStepResponse): ModelAnswer {
  const emission = record(response.emission)
  const reasoning = Array.isArray(emission?.reasoning) ? emission.reasoning : []
  const items = reasoning.map(record).filter((item): item is Record<string, unknown> => item !== undefined)

  const explicit = items.find((item) =>
    typeof item.kind === 'string' && item.kind.trim().toLowerCase() === 'answer' && nonEmptyText(item.text),
  )
  const explicitText = explicit && nonEmptyText(explicit.text)
  if (explicitText) return { kind: 'answer', label: 'Model answer', text: explicitText }

  const completion = nonEmptyText(emission?.completion)
  if (completion) return { kind: 'completion', label: 'Model answer', text: completion }

  const cognition = items.map((item) => nonEmptyText(item.text)).filter((text): text is string => text !== undefined).join('\n\n')
  if (cognition) return { kind: 'cognition', label: 'Model cognition (no explicit answer)', text: cognition }

  return { kind: 'empty', label: 'Model answer', text: '' }
}

export interface CompactClaim {
  proposition: string
  confidence?: number
  status?: string
}

export function extractClaims(response: ModelStepResponse): CompactClaim[] {
  const emission = record(response.emission)
  if (!Array.isArray(emission?.claims)) return []
  return emission.claims.flatMap((value) => {
    const claim = record(value)
    const proposition = nonEmptyText(claim?.proposition)
    if (!proposition) return []
    return [{
      proposition,
      confidence: typeof claim?.confidence === 'number' ? claim.confidence : undefined,
      status: nonEmptyText(claim?.status),
    }]
  })
}
