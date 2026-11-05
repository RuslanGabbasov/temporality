/**
 * Chat follow-up prompt building. A reply sent into a chat whose run already
 * finished starts a NEW run whose prompt is the whole dialogue — but a bare
 * «User: … / Assistant: …» transcript reads to the model as one compound
 * task: it cannot tell a resumed conversation (hours or days later) from a
 * fresh request, and it re-does work its own earlier answers describe.
 *
 * The builder frames the history instead:
 *  - every message is stamped with its local date and time, so pauses are
 *    visible at a glance;
 *  - a header says the history is the model's own earlier dialogue and the
 *    LAST message is the new request to answer;
 *  - a pause hint is added when the new message arrives well after the last
 *    one («the user is returning to this conversation»);
 *  - the window is bounded (recent messages win; the omitted count is noted).
 */

export interface PromptMessage {
  role: 'user' | 'assistant'
  content: string
  timestamp: Date
  attachments?: { path: string }[]
}

/** How many recent messages ride the prompt. Long dialogues stay affordable;
 *  older turns are summarized by omission count only. */
export const CHAT_HISTORY_WINDOW = 12

/** A gap beyond this says «the user went away and came back», not «we are
 *  mid-dialogue». The header gains an explicit resumption note. */
export const CHAT_RESUME_PAUSE_MS = 10 * 60 * 1000

const pad = (n: number) => String(n).padStart(2, '0')

/** Local `YYYY-MM-DD HH:MM` — sortable, unambiguous, no seconds noise. */
export function chatStamp(d: Date): string {
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

/** Build the run prompt for the dialogue. The last message must be the user's
 *  new one; a single-message dialogue keeps the historic plain format. */
export function buildChatPrompt(messages: PromptMessage[]): string {
  const line = (msg: PromptMessage): string => {
    const stamp = chatStamp(msg.timestamp instanceof Date ? msg.timestamp : new Date(msg.timestamp))
    if (msg.role === 'user') {
      let text = `[${stamp}] User: ${msg.content}`
      // Files travel as sandbox paths only — never inlined into the prompt.
      if (msg.attachments?.length) {
        text += '\n' + msg.attachments.map((a) => `[Attached file: ${a.path}]`).join('\n')
      }
      return text
    }
    return msg.content ? `[${stamp}] Assistant: ${msg.content}` : ''
  }

  if (messages.length <= 1) {
    // First message of a dialogue: the historic single-turn format.
    const only = messages[0]
    if (!only) return ''
    let text = `User: ${only.content}`
    if (only.attachments?.length) {
      text += '\n' + only.attachments.map((a) => `[Attached file: ${a.path}]`).join('\n')
    }
    return text
  }

  const history = messages.slice(0, -1)
  const newMsg = messages[messages.length - 1]
  const windowed = history.slice(-CHAT_HISTORY_WINDOW)
  const omitted = history.length - windowed.length

  const header = [
    '[Continuation of an existing conversation]',
    'The messages below are the recent history of your dialogue with the user (oldest first, each stamped with its local date and time). The Assistant messages are your own earlier answers in this dialogue — treat them as context and do not redo the work they describe. The LAST message is the user\'s NEW request: answer it, using the history as context.',
  ]
  if (omitted > 0) {
    header.push(`(${omitted} earlier message${omitted === 1 ? '' : 's'} omitted)`)
  }
  const lastAt = new Date(history[history.length - 1].timestamp).getTime()
  const nowAt = new Date(newMsg.timestamp).getTime()
  if (Number.isFinite(lastAt) && Number.isFinite(nowAt) && nowAt - lastAt >= CHAT_RESUME_PAUSE_MS) {
    header.push('Note the timestamps: the user is returning to this conversation after a pause — the new message likely refers to the earlier discussion.')
  }

  return header.join('\n') + '\n\n' + [...windowed, newMsg].map(line).filter(Boolean).join('\n\n')
}
