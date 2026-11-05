import { describe, expect, it } from 'vitest'
import { buildChatPrompt, chatStamp, CHAT_HISTORY_WINDOW, type PromptMessage } from './chatPrompt'

const msg = (role: 'user' | 'assistant', content: string, at: string, attachments?: { path: string }[]): PromptMessage =>
  ({ role, content, timestamp: new Date(at), attachments })

const HOUR = 60 * 60 * 1000

/** A follow-up sent long after the run finished must arrive framed: dated
 *  messages, «this is a continuation», and «the last message is the new
 *  request» — otherwise the model reads the transcript as a fresh compound
 *  task and barely recognizes its own earlier dialogue. */
describe('buildChatPrompt', () => {
  it('keeps the historic plain format for a dialogue’s first message', () => {
    const prompt = buildChatPrompt([msg('user', 'сделай отчёт', '2026-11-05T09:00:00', [{ path: '/scratch/a.csv' }])])
    expect(prompt).toBe('User: сделай отчёт\n[Attached file: /scratch/a.csv]')
  })

  it('frames a follow-up: header, dated lines, the new request last', () => {
    const prompt = buildChatPrompt([
      msg('user', 'первый вопрос', '2026-11-04T09:00:00'),
      msg('assistant', 'первый ответ', '2026-11-04T09:01:00'),
      msg('user', 'а теперь продолжим', '2026-11-05T11:30:00'),
    ])
    expect(prompt).toContain('[Continuation of an existing conversation]')
    expect(prompt).toContain(`[${chatStamp(new Date('2026-11-04T09:00:00'))}] User: первый вопрос`)
    expect(prompt).toContain(`[${chatStamp(new Date('2026-11-04T09:01:00'))}] Assistant: первый ответ`)
    // The resumption note rides the header: a day passed between the answer
    // and the new message.
    expect(prompt).toContain('returning to this conversation after a pause')
    // The new request is the last line and is NOT called history.
    const lastLine = prompt.split('\n\n').pop() ?? ''
    expect(lastLine).toContain('User: а теперь продолжим')
  })

  it('adds no pause note for a quick follow-up', () => {
    const prompt = buildChatPrompt([
      msg('user', 'вопрос', '2026-11-05T09:00:00'),
      msg('assistant', 'ответ', '2026-11-05T09:00:30'),
      msg('user', 'сразу уточню', '2026-11-05T09:01:00'),
    ])
    expect(prompt).not.toContain('after a pause')
  })

  it('bounds the history window and notes the omitted tail', () => {
    const messages: PromptMessage[] = []
    for (let i = 0; i < CHAT_HISTORY_WINDOW + 5; i++) {
      messages.push(msg('user', `вопрос ${i}`, new Date(Date.UTC(2026, 10, 1, 8, i)).toISOString()))
      messages.push(msg('assistant', `ответ ${i}`, new Date(Date.UTC(2026, 10, 1, 8, i, 30)).toISOString()))
    }
    messages.push(msg('user', 'новое сообщение', '2026-11-05T09:00:00'))
    const prompt = buildChatPrompt(messages)
    // 17 pairs = 34 history messages, the window keeps the last 12 → 22 omitted.
    expect(prompt).toContain('(22 earlier messages omitted)')
    expect(prompt).not.toContain('[2026-11-01 08:00] User: вопрос 0')
    // The newest history message stays in the window.
    expect(prompt).toContain('ответ 16')
  })

  it('skips empty assistant placeholders instead of blank lines', () => {
    const prompt = buildChatPrompt([
      msg('user', 'вопрос', '2026-11-05T09:00:00'),
      msg('assistant', '', '2026-11-05T09:00:30'),
      msg('user', 'ещё вопрос', '2026-11-05T09:05:00'),
    ])
    expect(prompt).not.toContain('[] Assistant')
    // header block, the surviving history line, the new request — three blocks.
    expect(prompt.split('\n\n')).toHaveLength(3)
  })
})
