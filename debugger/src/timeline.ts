import type { FrpEvent } from './types'

export function unwrapEvents(value: FrpEvent[] | { events: FrpEvent[] }): FrpEvent[] {
  return Array.isArray(value) ? value : value.events ?? []
}

export function frameIdOf(event: FrpEvent): string | undefined {
  const id = event.payload?.frame_id
  return typeof id === 'string' && id ? id : undefined
}

export function executionIdOf(event: FrpEvent): string | undefined {
  const direct = event.execution_id
  const nested = event.payload?.execution_id
  if (typeof direct === 'string' && direct) return direct
  return typeof nested === 'string' && nested ? nested : undefined
}

export function eventTime(event: FrpEvent): string {
  return event.timestamp ?? event.created_at ?? ''
}

export function eventLabel(event: FrpEvent): string {
  return event.type ?? event.event_type ?? 'event'
}

export function eventKey(event: FrpEvent, index: number): string {
  return event.id ?? event.event_id ?? `${eventTime(event)}-${eventLabel(event)}-${index}`
}

/** Newest-first view order for the timeline: readers watch the live end of the
 * episode, so fresh events belong at the top. Falls back to the given order
 * when timestamps are missing; ties keep their original relative order. */
export function newestFirst(events: FrpEvent[]): FrpEvent[] {
  const stamp = (event: FrpEvent) => { const parsed = Date.parse(eventTime(event)); return Number.isNaN(parsed) ? 0 : parsed }
  return events.map((event, index) => ({ event, index })).sort((left, right) => stamp(right.event) - stamp(left.event) || left.index - right.index).map(({ event }) => event)
}
