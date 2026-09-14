import { describe, expect, it } from 'vitest'
import { assistantMessage, conversationReducer, initialConversationState, userMessage } from './conversation'

const response = { emission: { completion: 'Done' } }

describe('conversationReducer', () => {
  it('starts an initial conversation and appends its answer', () => {
    let state = conversationReducer(initialConversationState, { type: 'start', user: userMessage('turn-1', 'Original prompt') })
    state = conversationReducer(state, { type: 'appendAssistant', message: assistantMessage('turn-1:assistant', response) })
    expect(state.messages.map(({ role, text }) => ({ role, text }))).toEqual([
      { role: 'user', text: 'Original prompt' },
      { role: 'assistant', text: 'Done' },
    ])
  })

  it('appends a follow-up turn', () => {
    let state = conversationReducer(initialConversationState, { type: 'ensureUser', message: userMessage('follow-up', 'More detail') })
    state = conversationReducer(state, { type: 'appendAssistant', message: assistantMessage('follow-up:assistant', response) })
    expect(state.messages).toHaveLength(2)
  })

  it('does not duplicate either side when a saved turn is retried', () => {
    const user = userMessage('retry', 'Try this')
    const assistant = assistantMessage('retry:assistant', response)
    let state = conversationReducer(initialConversationState, { type: 'ensureUser', message: user })
    state = conversationReducer(state, { type: 'ensureUser', message: user })
    state = conversationReducer(state, { type: 'appendAssistant', message: assistant })
    state = conversationReducer(state, { type: 'appendAssistant', message: assistant })
    expect(state.messages).toHaveLength(2)
  })

  it('clears stale messages when an existing episode is opened', () => {
    const state = conversationReducer({ messages: [userMessage('old', 'Old episode')], unavailableHistory: false }, { type: 'switchEpisode' })
    expect(state).toEqual({ messages: [], unavailableHistory: true })
  })

  it('uses a readable fallback when the response has no answer', () => {
    expect(assistantMessage('empty', {}).text).toBe('No model answer was returned.')
  })
})
