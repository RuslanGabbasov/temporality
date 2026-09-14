import { extractClaims, extractModelAnswer, type CompactClaim } from './answer'
import type { ModelStepResponse } from './types'

export interface ConversationMessage {
  id: string
  role: 'user' | 'assistant'
  text: string
  label?: string
  frameId?: string
  duration?: string
  claims?: CompactClaim[]
}

export interface ConversationState {
  messages: ConversationMessage[]
  unavailableHistory: boolean
}

export type ConversationAction =
  | { type: 'start'; user: ConversationMessage }
  | { type: 'switchEpisode' }
  | { type: 'ensureUser'; message: ConversationMessage }
  | { type: 'appendAssistant'; message: ConversationMessage }

export const initialConversationState: ConversationState = { messages: [], unavailableHistory: false }

export function conversationReducer(state: ConversationState, action: ConversationAction): ConversationState {
  if (action.type === 'switchEpisode') return { messages: [], unavailableHistory: true }
  if (action.type === 'start') return { messages: [action.user], unavailableHistory: false }
  if (state.messages.some((message) => message.id === action.message.id)) return state
  return { ...state, messages: [...state.messages, action.message] }
}

export function userMessage(id: string, text: string, frameId?: string): ConversationMessage {
  return { id, role: 'user', text: text.trim(), frameId }
}

function durationOf(response: ModelStepResponse): string | undefined {
  const summary = response.debugger_summary
  if (!summary || typeof summary !== 'object' || Array.isArray(summary)) return undefined
  const duration = (summary as Record<string, unknown>).duration
  return typeof duration === 'string' ? duration : undefined
}

export function assistantMessage(id: string, response: ModelStepResponse, frameId?: string): ConversationMessage {
  const answer = extractModelAnswer(response)
  return {
    id,
    role: 'assistant',
    text: answer.text || 'No model answer was returned.',
    label: answer.label,
    frameId,
    duration: durationOf(response),
    claims: extractClaims(response),
  }
}
