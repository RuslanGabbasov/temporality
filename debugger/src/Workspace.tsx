import { useCallback, useEffect, useState, useRef } from 'react'
import {
  Button,
  TextInput,
  TextArea,
  Select,
  SelectItem,
  InlineNotification,
  Tag,
  Heading,
} from '@carbon/react'
import { Add, ArrowDown, Attachment, Send, TrashCan } from '@carbon/icons-react'
import { workspaceApi, type Agent, type UploadedFile, ApiError } from './workspaceApi'
import { runCost, taskCost, KERNEL_API, whoami, type Whoami } from './kernelApi'
import { authHeaders } from './api'
import Markdown, { linkifyWorkspacePaths } from './Markdown'
import { CallReportView } from './AgentRuns'
import { observationApi, type ObservationEvent } from './observationApi'
import HumanForm, { normalizeFormValues, validateFormValues, type FormSchema } from './HumanForm'
import { useRunTokenMetrics } from './tokenStream'
import DelegationTree from './DelegationTree'
import PlanGraph from './PlanGraph'
import ListFilter, { matchesFilter } from './ListFilter'
import AppModal from './Modal'
import ProjectRequired from './ProjectRequired'
import { useI18n, pluralT } from './i18n'
import { pseudoToolLabel, formatDuration } from './eventSummary'
import { randomUUID } from './uuid'

function message(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }

/** The runs-tab trace view of one chat run — the «Трейс» link next to Stop.
 *  Same URL contract AgentRuns' initializers read (project/run/view), so the
 *  page opens straight on the trace tab. */
export function runTraceUrl(project: string, runId: string): string {
  return `/agents?project=${encodeURIComponent(project)}&run=${encodeURIComponent(runId)}&view=trace`
}

function formatSize(bytes: number) {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`
}
function shortTime(iso: string) {
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? '' : d.toLocaleTimeString()
}

const STATUS_COLORS: Record<string, 'blue' | 'green' | 'warm-gray' | 'gray' | 'red'> = {
  completed: 'green', failed: 'red', running: 'blue', turn_limit: 'warm-gray', pending: 'gray',
  started: 'blue', cancelled: 'warm-gray', canceled: 'warm-gray',
  time_limit: 'warm-gray', token_limit: 'warm-gray', budget_limit: 'warm-gray',
}

/** Normalize a run status from the backend into the canonical vocabulary.
 * Temporal-derived statuses used to arrive as American "canceled" while the
 * whole UI (terminal lists, tags, colors) speaks British "cancelled". */
export function normalizeRunStatus(status: string | undefined | null): string {
  return status === 'canceled' ? 'cancelled' : (status ?? '')
}

// One ask_human question bound to a run (docs/interactive-HITL.md §5): the
// chat renders it as an interactive card; the answer goes through the same
// approval endpoint the runs page uses. State follows the journaled events:
// human.requested → answered | cancelled | timed_out | rejected.
export interface ChatQuestion {
  operationId: string
  argumentsHash: string
  question: string
  context?: string
  options: string[]
  recipient?: string
  /** Optional form definition (docs/interactive-HITL.md §7): when present
   *  the card renders a form instead of the free-text answer. */
  schema?: FormSchema
  state: 'pending' | 'answered' | 'cancelled' | 'timed_out' | 'rejected'
  response?: string
  actor?: string
  busy?: boolean
  error?: string
}

interface ChatMessage {
  role: 'user' | 'assistant'
  content: string
  reasoning?: string
  timestamp: Date
  runId?: string
  status?: string
  streamLines?: string[]
  attachments?: UploadedFile[]
  totals?: string
  /** Whole-task budget (all follow-up runs + delegation tree), docs: cost accounting. */
  taskTotals?: string
  /** Tool the model is streaming arguments for right now (no answer text this
   * turn) — transient, cleared when the model moves on to text. */
  liveTool?: string
  /** Files the run delivered with send_file — download chips under the
   * answer. Sourced from the journaled tool.completed events. */
  files?: { path: string; name: string }[]
  /** User addenda sent into a RUNNING run: they augment this original task
   *  message (reply-style, timestamped) instead of dangling at the chat
   *  bottom after the model's answer. */
  addenda?: { text: string; at: Date; attachments?: UploadedFile[] }[]
  /** Open (or last resolved) ask_human question of this run — the interactive
   *  HITL card. A newer human.requested replaces it. */
  question?: ChatQuestion
}

interface Conversation {
  id: string
  title: string
  messages: ChatMessage[]
  agentId: string
  taskId: string
  createdAt: Date
}

const STORAGE_KEY = 'temporality_chats'

function loadConversations(): Conversation[] {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (!raw) return []
    return JSON.parse(raw).map((c: any) => ({
      ...c,
      taskId: c.taskId ?? '',
      agentId: c.agentId ?? '',
      createdAt: new Date(c.createdAt),
      messages: c.messages.map((m: any) => ({
        ...m,
        timestamp: new Date(m.timestamp),
        // Mid-run addenda ride the same JSON round-trip: their Date comes
        // back as an ISO string and must be revived before rendering.
        addenda: (m.addenda ?? []).map((a: any) => ({ ...a, at: new Date(a.at) })),
      })),
    }))
  } catch { return [] }
}

function saveConversations(convs: Conversation[]) {
  localStorage.setItem(STORAGE_KEY, JSON.stringify(convs))
}

type Translate = (key: string, vars?: Record<string, string>) => string

/** Compact token count for trace lines: 980 stays, 1248 → 1.2k, 10000 → 10k. */
function compactTokens(n: number): string {
	if (n < 1000) return String(n)
	const k = Math.round(n / 100) / 10
	return `${k}k`
}

/** Pull a human-meaningful detail out of a tool call's arguments — the what
 * it is doing right now line. Works on complete JSON and on a partial args
 * stream (the model writes arguments token by token): falls back to a regex
 * over the raw text. */
export function toolArgDetail(rawArgs: string): string {
	const raw = (rawArgs || '').trim()
	if (!raw) return ''
	let detail = ''
	try {
		const parsed = JSON.parse(raw) as Record<string, unknown>
		for (const key of ['command', 'path', 'file_path', 'url', 'query', 'title', 'note', 'skill_id', 'artifact_id', 'pattern']) {
			const value = parsed[key]
			if (typeof value === 'string' && value.trim()) { detail = value.trim(); break }
			if (Array.isArray(value) && value.length && typeof value[0] === 'string') { detail = value.join(' '); break }
		}
	} catch {
		// Partial stream: regex the known keys out of the raw prefix. Built via
		// new RegExp — three adjacent regex literals in one array trip esbuild's
		// literal scanner.
		const patterns = [
			'"command"\\s*:\\s*\\[\\s*"((?:[^"\\\\]|\\\\.)*)',
			'"(?:path|file_path|url|query|title|note|skill_id|artifact_id|pattern)"\\s*:\\s*"((?:[^"\\\\]|\\\\.)*)',
			'"command"\\s*:\\s*"((?:[^"\\\\]|\\\\.)*)',
		]
		for (const pattern of patterns) {
			const m = raw.match(new RegExp(pattern))
			if (m?.[1]) { detail = m[1]; break }
		}
		if (!detail && raw.length > 2) detail = raw.slice(-30)
	}
	detail = detail.replace(/\s+/g, ' ').trim()
	return detail.length > 64 ? detail.slice(0, 61) + '…' : detail
}

/** Format a stream event into a human-readable status line. */
export function formatStreamEvent(type: string, outer: any, t: Translate, locale: string): string | null {
	const d = outer.data ?? outer
	// Timeline prefix: seconds since run start, when the caller knows it.
	const elapsed = Number(d.elapsed_s)
	const prefix = Number.isFinite(elapsed) && elapsed >= 5 ? `+${formatDuration(elapsed * 1000)} ` : ''
	switch (type) {
		case 'run.started':
			return t('chat.stream.run_started') + (d.model ? t('chat.stream.model_suffix', { model: String(d.model) }) : '')
		case 'model.completed': {
			// One compact line per turn: thinking time (when the live stream saw
			// reasoning deltas), tokens, provider latency.
			const tokens = d.total_tokens ? t('chat.stream.tokens_suffix', { tokens: compactTokens(Number(d.total_tokens)) }) : ''
			const thinking = d.thinking_s ? t('chat.stream.thinking_suffix', { seconds: Number(d.thinking_s).toFixed(1) }) : ''
			const latency = d.latency_ms ? t('chat.stream.latency_suffix', { seconds: (d.latency_ms / 1000).toFixed(1) }) : ''
			return prefix + t('chat.stream.model_turn', { turn: String(d.turn ?? '') }) + thinking + tokens + latency
		}
		case 'model.attempt': {
			// Silent-recovery markers: without a line the user sees the chat freeze
			// for minutes while the kernel quietly retries a stalled provider.
			// The routing marker (reason=routed) is NOT a retry: it just names the
			// provider that served the call — show it as one quiet line, and skip
			// the line entirely for the default routing (no news is no noise).
			if (d.reason === 'routed') {
				const route = String(d.route ?? '')
				if (!route || route === 'default') return ''
				return prefix + t('chat.stream.model_route', { route })
			}
			const attempt = String(d.attempt ?? '?')
			if (d.reason === 'stream_timeout') return t('chat.stream.model_attempt_timeout', { attempt })
			if (d.reason === 'stream_fallback') return t('chat.stream.model_attempt_fallback', { attempt })
			return t('chat.stream.model_attempt_retry', { attempt })
		}
		case 'knowledge.proposed':
			return t('chat.stream.learned', { proposition: String(d.proposition ?? '').slice(0, 60) })
		case 'run.completed':
			return t('chat.stream.completed', { turns: pluralT(t, locale, 'chat.stream.turns_n', Number(d.turns ?? 0)) })
		case 'run.user_note':
			// Confirmation that a mid-run chat message was drained into the model's
			// conversation (turn = the turn that will consume it). When the lead was
			// blocked on a delegation, the note was also steered into the running
			// children's mailboxes.
			return t('chat.stream.note_accepted', { turn: String(d.turn ?? '') }) + (d.forwarded ? t('chat.stream.note_forwarded') : '')
		case 'run.note_dropped':
			return t('chat.stream.note_dropped', { turn: String(d.turn ?? '') })
		case 'run.failed':
			return t('chat.stream.failed', { error: String(d.error ?? 'unknown') })
		case 'human.requested':
			return `❓ ${squashQuestion(String(d.question ?? ''))}`
		case 'human.reasked':
			return t('chat.stream.question_reasked', { policy: String(d.policy ?? '') })
		case 'human.answered':
			return t('chat.stream.question_answered', { response: String(d.response ?? '').slice(0, 80) })
		case 'human.cancelled':
			return t('chat.stream.question_cancelled')
		case 'human.timed_out':
			return t('chat.stream.question_timed_out')
		case 'human.rejected':
			return t('chat.stream.question_rejected')
		case 'tool.started': {
			const tool = pseudoToolLabel(d.tool ?? d.name ?? '', t)
			const detail = toolArgDetail(String(d.arguments ?? d.args ?? ''))
			return prefix + t('chat.stream.tool_started', { tool, detail })
		}
		case 'tool.failed': {
			const tool = pseudoToolLabel(d.tool ?? d.name ?? '', t)
			return prefix + t('chat.stream.tool_failed', { tool, error: String(d.error ?? '').slice(0, 60) })
		}
		default:
			// tool.completed and turn.completed stay silent: the started line told
			// the story, counts are noise in a compact trace.
			return null
	}
}

/** Extract the download chip from a send_file tool.completed event: the
 * arguments JSON carries the workspace path. Null for any other tool or an
 * unparseable shape — pure, unit-testable. */
export function sendFileChip(d: { tool?: unknown; arguments?: unknown }): { path: string; name: string } | null {
  if (d.tool !== 'send_file') return null
  try {
    const args = typeof d.arguments === 'string' ? JSON.parse(d.arguments) : {}
    const path = String(args.path ?? '')
    if (!path) return null
    const name = path.split('/').filter(Boolean).pop() || path
    return { path, name }
  } catch {
    return null
  }
}

/** One-line question preview for the activity feed — newlines squashed, so a
 * long ask stays a single status line like the other stream events. */
function squashQuestion(text: string): string {
  const squashed = text.replace(/\s+/g, ' ').trim()
  return squashed.length > 100 ? squashed.slice(0, 97) + '…' : squashed
}

/** Live-activity feed with capped height. Auto-scrolls to the newest line
 * unless the user has scrolled up to read earlier activity. */
function ChatStreamLines({ lines }: { lines: string[] }) {
  const ref = useRef<HTMLDivElement>(null)
  const mounted = useRef(false)
  useEffect(() => {
    const el = ref.current
    if (!el) return
    if (!mounted.current || el.scrollHeight - el.scrollTop - el.clientHeight < 48) {
      el.scrollTop = el.scrollHeight
    }
    mounted.current = true
  }, [lines.length])
  return (
    <div ref={ref} className="chat-activity-scroll">
      {lines.map((line, li) => (
        <div key={li} className="chat-stream-line">
          <span className="chat-stream-prefix">›</span>{line}
        </div>
      ))}
    </div>
  )
}

/** Interactive HITL card (docs/interactive-HITL.md §5): pending questions
 * render options + free-form answer + cancel; resolved questions collapse to
 * a one-line outcome. With a form schema the card renders the shared form
 * instead of the free-text input. Local input state keeps typing from
 * re-rendering the whole conversation. */
function ChatQuestionCard({ question, busy, onAnswer }: {
  question: ChatQuestion
  busy: boolean
  onAnswer: (response: string, approved: boolean, values?: Record<string, unknown>) => void
}) {
  const { t } = useI18n()
  const [answer, setAnswer] = useState('')
  if (question.state !== 'pending') {
    const outcome = t(`chat.question.${question.state}`) ?? question.state
    return (
      <div className="chat-question chat-question-resolved">
        <span style={{ fontSize: '0.72rem', color: 'var(--tm-muted)' }}>
          {outcome}{question.response ? `: ${question.response.slice(0, 120)}` : ''}
        </span>
      </div>
    )
  }
  return (
    <div className="chat-question">
      <div style={{ fontSize: '0.72rem', fontWeight: 600, color: 'var(--tm-amber)', marginBottom: '0.25rem' }}>
        ❓ {t('runs.human_question') ?? 'Agent asks'}
      </div>
      <div className="chat-question-body" style={{ fontSize: '0.85rem' }}>
        <Markdown content={question.question} />
      </div>
      {question.context && (
        <div className="chat-question-context" style={{ fontSize: '0.78rem', color: 'var(--tm-text-2)', marginTop: '0.25rem' }}>
          <Markdown content={question.context} />
        </div>
      )}
      {question.schema && Object.keys(question.schema.properties ?? {}).length > 0 ? (
        <HumanForm
          schema={question.schema}
          disabled={busy}
          submitLabel={t('runs.send_answer') ?? 'Send'}
          onValidSubmit={(values) => onAnswer('', true, values)}
        />
      ) : (
        <>
          {question.options.length > 0 && (
            <div style={{ display: 'flex', gap: '0.35rem', flexWrap: 'wrap', marginTop: '0.5rem' }}>
              {question.options.map((option) => (
                <button
                  key={option}
                  disabled={busy}
                  onClick={() => onAnswer(option, true)}
                  style={{ background: 'var(--tm-elevated)', border: '1px solid var(--tm-teal)', borderRadius: '4px', color: 'var(--tm-teal)', cursor: 'pointer', fontSize: '0.75rem', padding: '0.2rem 0.6rem', whiteSpace: 'nowrap' }}
                  title={option}
                >{option}</button>
              ))}
            </div>
          )}
          <div style={{ display: 'flex', gap: '0.4rem', alignItems: 'center', marginTop: '0.5rem' }}>
            <input
              value={answer}
              disabled={busy}
              onChange={(e) => setAnswer(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); if (answer.trim()) onAnswer(answer.trim(), true) }
              }}
              placeholder={t('runs.answer_placeholder') ?? 'Free-form answer…'}
              aria-label={t('runs.answer_label') ?? 'Your answer'}
              style={{ flex: 1, minWidth: 0, background: 'var(--tm-raised)', border: '1px solid var(--tm-border)', borderRadius: '4px', color: 'var(--tm-text)', fontSize: '0.8rem', padding: '0.3rem 0.5rem' }}
            />
            <button
              disabled={busy || !answer.trim()}
              onClick={() => onAnswer(answer.trim(), true)}
              style={{ background: 'var(--tm-teal)', border: 'none', borderRadius: '4px', color: '#fff', cursor: 'pointer', fontSize: '0.72rem', padding: '0.3rem 0.6rem' }}
            >{t('runs.send_answer') ?? 'Send'}</button>
            {busy && <span className="spinner" />}
          </div>
        </>
      )}
      <div style={{ display: 'flex', gap: '0.4rem', alignItems: 'center', marginTop: '0.4rem' }}>
        <button
          disabled={busy}
          onClick={() => onAnswer('', false)}
          style={{ background: 'none', border: '1px solid var(--tm-border)', borderRadius: '4px', color: 'var(--tm-muted)', cursor: 'pointer', fontSize: '0.72rem', padding: '0.25rem 0.5rem' }}
        >{t('runs.cancel_question') ?? 'Cancel question'}</button>
      </div>
      {question.error && (
        <div style={{ fontSize: '0.72rem', color: 'var(--tm-danger)', marginTop: '0.3rem' }}>
          {t('chat.question.error', { error: question.error }) ?? `Answer failed: ${question.error}`}
        </div>
      )}
    </div>
  )
}

export default function Workspace({ project, defaultAgentId, defaultModel }: { project: string; defaultAgentId?: string; defaultModel?: string }) {
  const { locale, t } = useI18n()
  const [allAgents, setAllAgents] = useState<Agent[]>([])
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)
  // Answering HITL questions needs an actor id. Kernels without /whoami (or
  // anonymous access) fall back to a stable label — the approval endpoint
  // overrides it with the authenticated principal when one exists.
  const [identity, setIdentity] = useState<Whoami | null>(null)
  useEffect(() => { void whoami().then(setIdentity).catch(() => setIdentity(null)) }, [])
  const humanActorID = identity?.subject && identity.subject !== 'anonymous' ? identity.subject : 'human-chat'

  // Chat state
  const [conversations, setConversations] = useState<Conversation[]>(() => loadConversations())
  const [activeConvId, setActiveConvId] = useState<string | null>(null)
  const [inputValue, setInputValue] = useState('')
  const [showNewChat, setShowNewChat] = useState(false)
  const [newChatAgentId, setNewChatAgentId] = useState('')
  const [attachments, setAttachments] = useState<UploadedFile[]>([])
  const [uploading, setUploading] = useState(false)
  const fileInputRef = useRef<HTMLInputElement>(null)
  // The chat input grows with its content — up to a third of the screen —
  // then scrolls internally. The composer sits at the column's bottom edge,
  // so the extra height eats into the message pane: the field grows upward.
  const chatInputRef = useRef<HTMLTextAreaElement | null>(null)
  const resizeChatInput = useCallback(() => {
    const el = chatInputRef.current
    if (!el) return
    const cap = Math.floor(window.innerHeight / 3)
    el.style.height = 'auto'
    el.style.height = `${Math.min(el.scrollHeight, cap)}px`
    el.style.overflowY = el.scrollHeight > cap ? 'auto' : 'hidden'
  }, [])
  useEffect(() => {
    resizeChatInput()
    window.addEventListener('resize', resizeChatInput)
    return () => window.removeEventListener('resize', resizeChatInput)
  }, [inputValue, resizeChatInput])
  // Auto-scroll stickiness: while the user reads near the bottom the chat
  // follows new content; once they scroll up into history it stops yanking
  // them down and counts unread messages on a floating jump button instead.
  const messagesRef = useRef<HTMLDivElement>(null)
  const stickRef = useRef(true)
  const jumpingRef = useRef(false)
  const lastConvIdRef = useRef<string | null>(null)
  const prevLenRef = useRef(0)
  const [atBottom, setAtBottom] = useState(true)
  const [unread, setUnread] = useState(0)
  const streamRef = useRef<Map<string, EventSource>>(new Map())
  // Live token streams (ephemeral /runs/{id}/tokens SSE), one per conversation.
  const tokenStreamRef = useRef<Map<string, EventSource>>(new Map())

  const activeConv = conversations.find((c) => c.id === activeConvId) ?? null

  // The live step line (what-is-it-doing-right-now) rides the shared token
  // metrics hook — the same source the runs-page trace uses: elapsed tokens,
  // rate, liveness (alive/stalled/reconnecting) and the pending tool's args
  // tail. Only the active conversation's running run is watched: the user can
  // only look at one chat at a time.
  // The LAST running message, not the first: a conversation that survived a
  // dead run (crashed provider, forced stop) keeps the stale bubble's status
  // 'running' forever — the first-match lookup would point the token channel
  // at that dead run while the fresh run streams in the newer bubble.
  const activeRunningMessages = activeConv?.messages.filter((m) => m.status === 'running' && m.runId) ?? []
  const activeRunningRunId = activeRunningMessages.length > 0 ? activeRunningMessages[activeRunningMessages.length - 1].runId : undefined
  const tokenMetrics = useRunTokenMetrics(activeRunningRunId ?? '', !!activeRunningRunId)

  // Pending attachments belong to the active conversation — never leak them
  // into another chat when the user switches.
  useEffect(() => { setAttachments([]) }, [activeConvId])

  const onFilesPicked = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const files = Array.from(e.target.files ?? [])
    e.target.value = '' // allow picking the same file twice
    if (!files.length) return
    setUploading(true)
    setError('')
    try {
      const uploaded: UploadedFile[] = []
      for (const f of files) {
        uploaded.push(await workspaceApi.uploadFile(project, f))
      }
      setAttachments((prev) => [...prev, ...uploaded])
    } catch (f) {
      setError(message(f))
    } finally {
      setUploading(false)
    }
  }

  // Assistant messages reference agent-produced files by /workspace/... path;
  // clicking such a link downloads the file from the project sandbox.
  const onFileLink = async (path: string) => {
    try {
      await workspaceApi.downloadFile(project, path)
    } catch (f) {
      setError(t('chat.download_failed') ?? 'Failed to download file')
      console.error(f)
    }
  }

  useEffect(() => { saveConversations(conversations) }, [conversations])

  // Server-side chat sync (docs/resilience-and-scale.md §4): the durable
  // copy lives in the workspace DB — localStorage stays as the offline
  // cache, but a browser wipe (or a second device) restores from here.
  // Push is debounced: conversations change on every streamed token batch.
  const chatSyncTimer = useRef<number | null>(null)
  useEffect(() => {
    if (!project || conversations.length === 0) return
    if (chatSyncTimer.current) window.clearTimeout(chatSyncTimer.current)
    chatSyncTimer.current = window.setTimeout(() => {
      for (const c of conversations) {
        const transcript = c.messages.map((m) => ({
          role: m.role, content: m.content, at: m.timestamp,
          addenda: m.addenda ?? [], attachments: m.attachments ?? [],
        }))
        workspaceApi.putServerChat(c.id, {
          project_id: project, agent_id: c.agentId ?? '',
          title: c.title || 'Untitled', transcript,
        }).catch(() => { /* server sync is best-effort; localStorage stays the cache */ })
      }
    }, 2000)
    return () => { if (chatSyncTimer.current) window.clearTimeout(chatSyncTimer.current) }
  }, [conversations, project])

  // Restore on mount: server chats missing locally (other device, wiped
  // browser) are appended; locally newer copies win (by message count).
  useEffect(() => {
    if (!project) return
    let alive = true
    workspaceApi.listServerChats(project).then((page) => {
      if (!alive || !page.chats?.length) return
      setConversations((prev) => {
        const known = new Map(prev.map((c) => [c.id, c]))
        const next = [...prev]
        for (const chat of page.chats) {
          if (known.has(chat.id)) continue // local copy wins: it may be newer
          // Older kernels marshaled Chat without json tags ("Transcript") —
          // such rows carry no readable transcript; skip instead of crashing.
          if (!Array.isArray(chat.transcript)) continue
          const messages = (chat.transcript as any[]).map((m) => ({
            role: m.role, content: m.content ?? '', timestamp: new Date(m.at ?? chat.updated_at),
            addenda: m.addenda ?? [], attachments: m.attachments ?? [],
          }))
          if (messages.length === 0) continue
          next.push({
            id: chat.id, title: chat.title || 'Chat', messages,
            agentId: chat.agent_id ?? '', taskId: '', createdAt: new Date(chat.updated_at),
          })
        }
        return next
      })
    }).catch(() => { /* server chats are a durability feature, not a dependency */ })
    return () => { alive = false }
  }, [project])
  useEffect(() => () => {
    streamRef.current.forEach((es) => es.close()); streamRef.current.clear()
    tokenStreamRef.current.forEach((es) => es.close()); tokenStreamRef.current.clear()
  }, [])

  // Reconnect SSE streams for in-progress messages on mount (e.g. after
  // navigating away and back to the Workspace tab). tail=1: resume from the
  // journal tail instead of replaying the run's whole history — returning to
  // a long-running chat must show the live state, not storm through dozens of
  // already-seen steps. Also poll for stale running messages as a safety net.
  useEffect(() => {
    for (const conv of conversations) {
      for (const msg of conv.messages) {
        if (msg.status === 'running' && msg.runId && !streamRef.current.has(conv.id)) {
          streamRun(msg.runId, conv.id, { tail: true })
        }
      }
    }
    // Poll for stale running messages every 30s
    const timer = window.setInterval(() => {
      setConversations((prev) => {
        for (const conv of prev) {
          for (const msg of conv.messages) {
            if (msg.status === 'running' && msg.runId && !streamRef.current.has(conv.id)) {
              void fetchFinalAnswer(msg.runId, conv.id)
            }
          }
        }
        return prev
      })
    }, 30000)
    return () => window.clearInterval(timer)
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  // Wake-from-sleep heal (docs/resilience-and-scale.md): a laptop that slept
  // mid-run wakes with dead SSE sockets that STILL sit in streamRef — the
  // plain mount-reconnect sees has(conv.id) and skips, the run ended hours
  // ago, and the bubble stays «running» next to stale retry lines forever.
  // On every return to visibility, rebuild the streams for running messages
  // (tail=1 re-attaches cheaply; a finished run's server closes right after
  // the replay and fetchFinalAnswer lands the terminal status) and re-run the
  // terminal check.
  useEffect(() => {
    const heal = () => {
      if (document.visibilityState !== 'visible') return
      for (const conv of conversations) {
        for (const msg of conv.messages) {
          if (msg.status === 'running' && msg.runId) {
            // Re-subscribe from the tail: a finished run's server closes right
            // after the replay and fetchFinalAnswer lands the terminal
            // status; an actually-live run just continues streaming.
            const old = streamRef.current.get(conv.id)
            if (old) { old.close(); streamRef.current.delete(conv.id) }
            streamRun(msg.runId, conv.id, { tail: true })
            void fetchFinalAnswer(msg.runId, conv.id)
          }
        }
      }
    }
    document.addEventListener('visibilitychange', heal)
    return () => document.removeEventListener('visibilitychange', heal)
  }, [conversations]) // heal uses the fresh conversation list on every change

  const loadAllAgents = useCallback(async () => {
    try {
      const data = await workspaceApi.listAllAgents()
      setAllAgents(data.agents ?? [])
    } catch { /* ignore */ }
  }, [])

  useEffect(() => { void loadAllAgents() }, [loadAllAgents])

  // "At bottom" with a tolerance: minor sub-pixel drift or a shrinking
  // last message must not count as reading history.
  const BOTTOM_TOLERANCE = 80

  const onMessagesScroll = () => {
    const el = messagesRef.current
    if (!el) return
    const nearBottom = el.scrollHeight - el.scrollTop - el.clientHeight < BOTTOM_TOLERANCE
    if (jumpingRef.current) {
      // Programmatic smooth jump: ignore intermediate frames, settle once arrived.
      if (nearBottom) {
        jumpingRef.current = false
        stickRef.current = true
        setAtBottom(true)
        setUnread(0)
      }
      return
    }
    if (nearBottom === stickRef.current) return
    stickRef.current = nearBottom
    setAtBottom(nearBottom)
    if (nearBottom) setUnread(0)
  }

  const jumpToBottom = () => {
    const el = messagesRef.current
    if (!el) return
    jumpingRef.current = true
    stickRef.current = true
    el.scrollTo({ top: el.scrollHeight, behavior: 'smooth' })
    // Safety net: user interaction cancels the smooth animation and no
    // settling scroll event may arrive — never stay in jumping mode forever.
    window.setTimeout(() => { jumpingRef.current = false }, 800)
  }

  // Follow new content only while stuck to the bottom; otherwise count
  // unread messages and let the jump button bring the user back.
  useEffect(() => {
    const convId = activeConv?.id ?? null
    const msgs = activeConv?.messages ?? []
    const switched = lastConvIdRef.current !== convId
    lastConvIdRef.current = convId
    const prevLen = prevLenRef.current
    prevLenRef.current = msgs.length
    if (switched) {
      stickRef.current = true
      setAtBottom(true)
      setUnread(0)
      const el = messagesRef.current
      if (el) el.scrollTop = el.scrollHeight
      return
    }
    if (stickRef.current) {
      const el = messagesRef.current
      if (el) el.scrollTop = el.scrollHeight
      return
    }
    const grew = msgs.length - prevLen
    if (grew > 0) setUnread((u) => u + grew)
  }, [activeConv?.messages, activeConv?.id]) // eslint-disable-line react-hooks/exhaustive-deps

  const agentName = (id: string) => allAgents.find((a) => a.id === id)?.name ?? (t('chat.default_agent') ?? 'Default')

  // Live step line for the running turn of the active chat — the compact-trace
  // counterpart of the runs-page liveModelParts: elapsed, tool+args tail,
  // token rate and liveness (alive/stalled/reconnecting). Ticks once a second
  // so the elapsed part breathes while a thinking model is silent.
  const [nowTick, setNowTick] = useState(0)
  useEffect(() => {
    if (!activeRunningRunId) return
    const timer = window.setInterval(() => setNowTick((v) => v + 1), 1000)
    return () => window.clearInterval(timer)
  }, [activeRunningRunId])
  const liveStepParts = (() => {
    if (!tokenMetrics) return ['…']
    const parts: string[] = []
    if (!tokenMetrics.connected) {
      parts.push(tokenMetrics.reconnects > 0
        ? t('runs.live.sse_reconnecting', { count: String(tokenMetrics.reconnects) })
        : t('runs.live.sse_down'))
      return parts
    }
    if (tokenMetrics.tool) parts.push(`⚙ ${tokenMetrics.tool}`)
    if (tokenMetrics.approxTokens > 0) parts.push(`~${tokenMetrics.approxTokens} tok`)
    if (tokenMetrics.tps > 0) parts.push(`${tokenMetrics.tps} tok/s`)
    const stalled = tokenMetrics.lastChunkAgoMs > 15_000
    if (tokenMetrics.streaming) {
      parts.push(stalled
        ? t('runs.live.stalled', { seconds: String(Math.floor(tokenMetrics.lastChunkAgoMs / 1000)) })
        : t('runs.live.alive'))
    } else if (tokenMetrics.approxTokens === 0) {
      parts.push(t('runs.live.waiting_first_token'))
    }
    return parts
  })()
  void nowTick

  // Client-side filter for the conversation list sidebar.
  const [convFilter, setConvFilter] = useState('')
  const visibleConversations = conversations.filter((conv) => matchesFilter(convFilter, conv.title, conv.agentId ? agentName(conv.agentId) : ''))

  const createConversation = () => {
    const conv: Conversation = {
      id: randomUUID(),
      title: t('new.conversation') ?? 'New conversation',
      messages: [],
      agentId: newChatAgentId,
      taskId: '',
      createdAt: new Date(),
    }
    setConversations((prev) => [conv, ...prev])
    setActiveConvId(conv.id)
    setShowNewChat(false)
    setNewChatAgentId('')
  }

  const deleteConversation = (id: string) => {
    const es = streamRef.current.get(id)
    if (es) { es.close(); streamRef.current.delete(id) }
    const tes = tokenStreamRef.current.get(id)
    if (tes) { tes.close(); tokenStreamRef.current.delete(id) }
    setConversations((prev) => prev.filter((c) => c.id !== id))
    if (activeConvId === id) setActiveConvId(null)
  }

  const branchConversation = (convId: string, messageIndex: number) => {
    const conv = conversations.find((c) => c.id === convId)
    if (!conv) return
    // Create a new conversation with messages up to (and including) the clicked message
    const branchMessages = conv.messages.slice(0, messageIndex + 1)
    const branch: Conversation = {
      id: randomUUID(),
      title: conv.title + ' (branch)',
      messages: branchMessages,
      agentId: conv.agentId,
      taskId: '',
      createdAt: new Date(),
    }
    setConversations((prev) => [branch, ...prev])
    setActiveConvId(branch.id)
  }

  const updateMsg = (convId: string, runId: string, patch: Partial<ChatMessage>) => {
    setConversations((prev) => prev.map((c) => {
      if (c.id !== convId) return c
      return { ...c, messages: c.messages.map((m) => m.runId === runId ? { ...m, ...patch } : m) }
    }))
  }

  /** Append one line to a running message's activity feed — the immediate
   * confirmation for a mid-run note (the journaled run.user_note event lands
   * only at the next turn boundary, minutes later). */
  const appendStreamLine = (convId: string, runId: string, line: string) => {
    setConversations((prev) => prev.map((c) => {
      if (c.id !== convId) return c
      return { ...c, messages: c.messages.map((m) => m.runId === runId ? { ...m, streamLines: [...(m.streamLines ?? []), line] } : m) }
    }))
  }

  const streamRun = (runId: string, convId: string, opts?: { tail?: boolean }) => {
    const token = localStorage.getItem('temporality_token') ?? ''
    // tail=1 asks the kernel to skip the journal backlog and replay only the
    // recent past + live events: reconnecting to a long in-flight run must not
    // storm the chat with hundreds of stale updates before catching up.
    const streamParams = new URLSearchParams()
    if (token) streamParams.set('token', token)
    if (opts?.tail) streamParams.set('tail', '1')
    const streamQS = streamParams.toString()
    const es = new EventSource(`/kernel-api/v1/workspace/runs/${encodeURIComponent(runId)}/stream${streamQS ? `?${streamQS}` : ''}`)
    streamRef.current.set(convId, es)
    const lines: string[] = []

    // Coalesce stream-line updates: a reconnect burst (or a busy delegation)
    // lands dozens of events at once, and one setState per event turns the
    // replay into a render storm. Flush at most once per short interval.
    let linesTimer: number | undefined
    const stopLinesTimer = () => {
      if (linesTimer !== undefined) { window.clearTimeout(linesTimer); linesTimer = undefined }
    }
    const flushLines = () => {
      linesTimer = undefined
      updateMsg(convId, runId, { streamLines: [...lines] })
    }
    const pushLine = (line: string) => {
      lines.push(line)
      if (linesTimer === undefined) linesTimer = window.setTimeout(flushLines, 120)
    }

    // Compact-trace bookkeeping: per-turn stats (thinking measured from the
    // live reasoning→text delta gap), the run's start time for +elapsed
    // prefixes, and each turn's text fragment — flushed as an intermediate-
    // answer line only when the turn turns out to have tools after it.
    const runStartAt: { at?: number } = {}
    const turnStats: Record<number, { reasoningAt?: number; textAt?: number }> = {}
    const fragments: Record<number, string> = {}
    const elapsedFor = (outer: any): number | undefined => {
      const at = Date.parse(outer?.occurred_at ?? '')
      if (!runStartAt.at || Number.isNaN(at)) return undefined
      return Math.max(0, (at - runStartAt.at) / 1000)
    }

    // Live token stream (ephemeral SSE, never journaled): preview the model's
    // answer and reasoning while it is being generated. The journal's
    // model.text_delta / model.reasoning events remain the authoritative
    // per-turn text and replace the live preview once the turn completes.
    const closeTokenStream = () => {
      const tes = tokenStreamRef.current.get(convId)
      if (tes) { tes.close(); tokenStreamRef.current.delete(convId) }
    }
    const tes = new EventSource(`/kernel-api/v1/workspace/runs/${encodeURIComponent(runId)}/tokens${token ? `?token=${token}` : ''}`)
    tokenStreamRef.current.set(convId, tes)
    let liveTurn = -1
    let liveText = ''
    let liveReasoning = ''
    let liveTool = ''
    // Fragment of the last finished turn. It stays on screen while the next
    // turn runs its tools — the bubble never blanks mid-run, so intermediate
    // results remain readable until the next substantive text (or the final
    // answer) replaces them. settledTurn guards against a stale live copy
    // overwriting the journal-authoritative fragment.
    let settledTurn = -1
    let settledText = ''
    let settledReasoning = ''
    const applyLive = (data: { type?: string; turn?: number; text?: string; reasoning?: string; tool?: string; args?: string }) => {
      if (data.type === 'done') { closeTokenStream(); return }
      const turn = Number(data.turn ?? 0)
      if (turn !== liveTurn) {
        if (liveText && liveTurn > settledTurn) {
          settledText = liveText
          settledReasoning = liveReasoning
          settledTurn = liveTurn
        }
        liveTurn = turn
        liveText = ''
        liveReasoning = ''
        liveTool = ''
      }
      // Thinking measurement for the compact trace: the gap between the first
      // reasoning delta and the first text delta of the turn. Live-only — a
      // tail replay shows the line without the 💭 part.
      if (data.reasoning && !turnStats[turn]?.reasoningAt) {
        turnStats[turn] = { ...turnStats[turn], reasoningAt: Date.now() }
      }
      if (data.text && !turnStats[turn]?.textAt) {
        turnStats[turn] = { ...turnStats[turn], textAt: Date.now() }
      }
      if (data.reasoning) liveReasoning += data.reasoning
      if (data.text) liveText += data.text
      // Tool args are never rendered as content — they only say which tool the
      // model is preparing, so the bubble shows life on tool-only turns. The
      // live detail/metrics line comes from the shared useRunTokenMetrics hook
      // (the same source the runs-page trace uses).
      if (data.tool) liveTool = data.tool
      else if (data.text) liveTool = ''
      updateMsg(convId, runId, { content: liveText || settledText, reasoning: liveReasoning || settledReasoning, liveTool: liveTool || undefined })
    }
    tes.addEventListener('snapshot', (e) => {
      try { applyLive(JSON.parse((e as MessageEvent).data)) } catch { /* ignore */ }
    })
    tes.addEventListener('delta', (e) => {
      try { applyLive(JSON.parse((e as MessageEvent).data)) } catch { /* ignore */ }
    })
    tes.addEventListener('done', closeTokenStream)
    tes.onerror = closeTokenStream

    es.onmessage = () => {}

    const handleEvent = (type: string) => (e: MessageEvent) => {
      try {
        const outer = JSON.parse(e.data)
        const line = formatStreamEvent(type, outer, t, locale)
        if (line) pushLine(line)
      } catch { /* ignore */ }
    }

    // Compact trace: run start anchors the +elapsed prefixes; model.completed
    // is enriched with the live-measured thinking time; a turn's text fragment
    // becomes an intermediate-answer line only when tools follow it.
    es.addEventListener('run.started', (e) => {
      try {
        const outer = JSON.parse(e.data)
        runStartAt.at = Date.parse(outer.occurred_at ?? '')
        if (Number.isNaN(runStartAt.at)) runStartAt.at = Date.now()
        const line = formatStreamEvent('run.started', outer, t, locale)
        if (line) pushLine(line)
      } catch { /* ignore */ }
    })
    es.addEventListener('model.started', () => { /* pacing marker; the completed line carries the numbers */ })
    // send_file starts attach the file chip to the bubble immediately: the
    // journaled tool.started carries the workspace path (tool.completed does
    // NOT — it ships only the delivery summary). The run workspace serves the
    // download, so the chat gets the file even when the model's reply forgets
    // to mention the path.
    es.addEventListener('tool.started', (e) => {
      try {
        const outer = JSON.parse(e.data)
        const chip = sendFileChip(outer.data ?? outer)
        if (!chip) return
        const { path, name } = chip
        setConversations((prev) => prev.map((c) => c.id !== convId ? c : ({
          ...c,
          messages: c.messages.map((m) => m.runId !== runId ? m : ({
            ...m,
            files: [...(m.files ?? []).filter((f) => f.path !== path), { path, name }],
          })),
        })))
      } catch { /* ignore */ }
    })

    es.addEventListener('model.completed', (e) => {
      try {
        const outer = JSON.parse(e.data)
        const d = outer.data ?? outer
        const stats = turnStats[Number(d.turn ?? 0)] ?? {}
        const thinking = stats.reasoningAt && stats.textAt ? (stats.textAt - stats.reasoningAt) / 1000 : undefined
        const line = formatStreamEvent('model.completed', { ...outer, data: { ...d, thinking_s: thinking, elapsed_s: elapsedFor(outer) } }, t, locale)
        if (line) pushLine(line)
      } catch { /* ignore */ }
    })
    es.addEventListener('tool.started', (e) => {
      try {
        const outer = JSON.parse(e.data)
        const line = formatStreamEvent('tool.started', { ...outer, data: { ...(outer.data ?? outer), elapsed_s: elapsedFor(outer) } }, t, locale)
        if (line) pushLine(line)
      } catch { /* ignore */ }
    })
    es.addEventListener('tool.failed', (e) => {
      try {
        const outer = JSON.parse(e.data)
        const line = formatStreamEvent('tool.failed', { ...outer, data: { ...(outer.data ?? outer), elapsed_s: elapsedFor(outer) } }, t, locale)
        if (line) pushLine(line)
      } catch { /* ignore */ }
    })
    es.addEventListener('turn.completed', (e) => {
      try {
        const outer = JSON.parse(e.data)
        const d = outer.data ?? outer
        const turn = Number(d.turn ?? 0)
        const fragment = fragments[turn]
        // Text followed by tools is intermediate commentary — the breadcrumbs
        // worth keeping; a turn with no tools was the final answer, which the
        // bubble already shows.
        if (fragment && Number(d.tool_calls ?? 0) > 0) {
          const elapsed = elapsedFor(outer)
          const prefix = elapsed !== undefined && elapsed >= 5 ? `+${formatDuration(elapsed * 1000)} ` : ''
          pushLine(prefix + t('chat.stream.fragment', { text: squashQuestion(fragment) }))
        }
        delete fragments[turn]
      } catch { /* ignore */ }
    })
    es.addEventListener('model.attempt', handleEvent('model.attempt'))
    es.addEventListener('knowledge.proposed', handleEvent('knowledge.proposed'))

    // model.text_delta (journal) carries the authoritative full text of the
    // finished turn — it corrects the live preview and becomes the fragment
    // that stays visible through the next turn's tool work.
    es.addEventListener('model.text_delta', (e) => {
      try {
        const outer = JSON.parse(e.data)
        const d = outer.data ?? outer
        const text = d.text ?? ''
        const turn = Number(d.turn ?? 0)
        if (!text || turn < liveTurn) return // stale: a newer turn already streams live
        fragments[turn] = text
        settledText = text
        settledTurn = turn
        setConversations((prev) => prev.map((c) => {
          if (c.id !== convId) return c
          return { ...c, messages: c.messages.map((m) => {
            if (m.runId !== runId) return m
            // Only update if we don't already have a final answer
            if (m.status && m.status !== 'running') return m
            return { ...m, content: text }
          }) }
        }))
      } catch { /* ignore */ }
    })

    // model.reasoning (journal) carries the full thinking text of the finished
    // turn — same fragment policy as model.text_delta above.
    es.addEventListener('model.reasoning', (e) => {
      try {
        const outer = JSON.parse(e.data)
        const d = outer.data ?? outer
        const text = d.text ?? ''
        const turn = Number(d.turn ?? 0)
        if (!text || turn < liveTurn) return
        settledReasoning = text
        setConversations((prev) => prev.map((c) => {
          if (c.id !== convId) return c
          return { ...c, messages: c.messages.map((m) => {
            if (m.runId !== runId) return m
            if (m.status && m.status !== 'running') return m
            return { ...m, reasoning: text }
          }) }
        }))
      } catch { /* ignore */ }
    })

    // agent.summary arrives via SSE but the answer in observation_events
    // loses newlines (JSONB storage). Always fetch the real answer from REST.
    es.addEventListener('agent.summary', (e) => {
      try {
        const outer = JSON.parse(e.data)
        const d = outer.data ?? outer
        if (d.answer) {
          pushLine(t('chat.answer_received') ?? 'Answer received')
          void fetchFinalAnswer(runId, convId)
        }
      } catch { /* ignore */ }
    })

    // Approval events — show in stream lines
    es.addEventListener('approval.requested', (e) => {
      try {
        const outer = JSON.parse(e.data)
        const d = outer.data ?? outer
        const action = d.action ?? d.operation?.tool ?? 'action'
        pushLine(t('chat.stream.approval_needed', { action: String(action) }))
      } catch { /* ignore */ }
    })
    es.addEventListener('approval.granted', () => {
      pushLine(t('chat.stream.approval_granted'))
    })
    es.addEventListener('approval.rejected', () => {
      pushLine(t('chat.stream.approval_rejected'))
    })
    es.addEventListener('approval.auto_granted', (e) => {
      try {
        const outer = JSON.parse(e.data)
        const d = outer.data ?? outer
        const tool = d.operation?.tool ?? d.policy_id ?? 'tool'
        pushLine(t('chat.stream.auto_approved', { tool: String(tool) }))
      } catch { /* ignore */ }
    })

    // HITL questions (docs/interactive-HITL.md §5): human.requested attaches
    // the interactive card to this run's bubble; the terminal events close it.
    // All of them also land in the activity feed via formatStreamEvent. The
    // tail replay on reconnect re-attaches an open question, so a page reload
    // during a paused run restores the card.
    es.addEventListener('human.requested', (e) => {
      try {
        const outer = JSON.parse(e.data)
        const d = outer.data ?? outer
        const line = formatStreamEvent('human.requested', outer, t, locale)
        if (line) pushLine(line)
        updateMsg(convId, runId, {
          question: {
            operationId: String(d.operation_id ?? ''),
            argumentsHash: String(d.arguments_hash ?? ''),
            question: String(d.question ?? ''),
            context: d.context ? String(d.context) : undefined,
            options: Array.isArray(d.options) ? d.options.map((o: unknown) => String(o)) : [],
            recipient: d.recipient ? String(d.recipient) : undefined,
            schema: (d.schema && typeof d.schema === 'object' && !Array.isArray(d.schema)) ? (d.schema as FormSchema) : undefined,
            state: 'pending',
          },
        })
      } catch { /* ignore */ }
    })
    // Terminal transitions resolve the card only when they belong to the
    // question it shows — an older ask's events must not close the newer one.
    es.addEventListener('human.answered', (e) => {
      try {
        const outer = JSON.parse(e.data)
        const d = outer.data ?? outer
        const line = formatStreamEvent('human.answered', outer, t, locale)
        if (line) pushLine(line)
        setConversations((prev) => prev.map((c) => c.id !== convId ? c : ({
          ...c,
          messages: c.messages.map((m) => m.runId !== runId || m.question?.operationId !== String(d.operation_id ?? '') ? m : ({
            ...m,
            question: { ...m.question!, state: 'answered', response: d.response ? String(d.response) : undefined, actor: d.actor ? String(d.actor) : undefined },
          })),
        })))
      } catch { /* ignore */ }
    })
    es.addEventListener('human.cancelled', (e) => {
      try {
        const outer = JSON.parse(e.data)
        const d = outer.data ?? outer
        const line = formatStreamEvent('human.cancelled', outer, t, locale)
        if (line) pushLine(line)
        setConversations((prev) => prev.map((c) => c.id !== convId ? c : ({
          ...c,
          messages: c.messages.map((m) => m.runId !== runId || m.question?.operationId !== String(d.operation_id ?? '') ? m : ({
            ...m,
            question: { ...m.question!, state: 'cancelled', actor: d.actor ? String(d.actor) : undefined },
          })),
        })))
      } catch { /* ignore */ }
    })
    es.addEventListener('human.timed_out', (e) => {
      try {
        const outer = JSON.parse(e.data)
        const d = outer.data ?? outer
        const line = formatStreamEvent('human.timed_out', outer, t, locale)
        if (line) pushLine(line)
        setConversations((prev) => prev.map((c) => c.id !== convId ? c : ({
          ...c,
          messages: c.messages.map((m) => m.runId !== runId || m.question?.operationId !== String(d.operation_id ?? '') ? m : ({
            ...m,
            question: { ...m.question!, state: 'timed_out' },
          })),
        })))
      } catch { /* ignore */ }
    })
    es.addEventListener('human.rejected', (e) => {
      try {
        const outer = JSON.parse(e.data)
        const d = outer.data ?? outer
        const line = formatStreamEvent('human.rejected', outer, t, locale)
        if (line) pushLine(line)
        setConversations((prev) => prev.map((c) => c.id !== convId ? c : ({
          ...c,
          messages: c.messages.map((m) => m.runId !== runId || m.question?.operationId !== String(d.operation_id ?? '') ? m : ({
            ...m,
            question: { ...m.question!, state: 'rejected' },
          })),
        })))
      } catch { /* ignore */ }
    })
    es.addEventListener('human.reasked', handleEvent('human.reasked'))

    es.addEventListener('run.completed', handleEvent('run.completed'))

    // Mid-run note acceptance: the workflow drained a user note into the
    // conversation at this turn's boundary (the chat already showed an
    // optimistic "sent" line when the signal was posted).
    es.addEventListener('run.user_note', handleEvent('run.user_note'))
    es.addEventListener('run.note_dropped', handleEvent('run.note_dropped'))

    // A cancelled run never emits run.completed/run.failed — without this the
    // message spinner runs forever (the server stream also used to never send
    // `done` for cancelled runs).
    es.addEventListener('run.cancelled', () => {
      es.close(); streamRef.current.delete(convId)
      closeTokenStream()
      stopLinesTimer()
      void fetchFinalAnswer(runId, convId)
    })

    // Delegation events — surface child agent activity in the stream
    const delegationAgentName = (id: unknown) => {
      const s = typeof id === 'string' ? id : ''
      return allAgents.find((a) => a.id === s)?.name ?? s ?? 'agent'
    }
    es.addEventListener('delegation.started', (e) => {
      try {
        const outer = JSON.parse(e.data)
        const d = outer.data ?? outer
        const name = delegationAgentName(d.agent_id)
        pushLine(t('chat.delegation.stream_started', { name }) ?? `Delegated to ${name}`)
      } catch { /* ignore */ }
    })
    es.addEventListener('delegation.completed', (e) => {
      try {
        const outer = JSON.parse(e.data)
        const d = outer.data ?? outer
        const name = delegationAgentName(d.agent_id)
        const turns = d.turns != null ? String(d.turns) : ''
        const line = turns
          ? (t('chat.delegation.stream_completed', { name, count: turns }))
          : `${name} · ${t('chat.delegation.completed')}`
        pushLine(line)
      } catch { /* ignore */ }
    })
    es.addEventListener('delegation.failed', (e) => {
      try {
        const outer = JSON.parse(e.data)
        const d = outer.data ?? outer
        const name = delegationAgentName(d.agent_id)
        pushLine(t('chat.delegation.stream_failed', { name }) ?? `${name} · failed`)
      } catch { /* ignore */ }
    })
    // Plan events — DAG task progress of plan calls in the stream
    es.addEventListener('plan.started', (e) => {
      try {
        const outer = JSON.parse(e.data)
        const d = outer.data ?? outer
        const goal = typeof d.goal === 'string' && d.goal.trim() ? d.goal : t('chat.plan.untitled')
        pushLine(t('chat.plan.stream_started', { goal }) ?? `Plan started: ${goal}`)
      } catch { /* ignore */ }
    })
    es.addEventListener('plan.task.started', (e) => {
      try {
        const outer = JSON.parse(e.data)
        const d = outer.data ?? outer
        const round = Number(d.round ?? 1)
        const roundSuffix = round > 1 ? ` ×${round}` : ''
        const gate = Array.isArray(d.review_of) && d.review_of.length ? ` ⌾ ${d.review_of.join(', ')}` : ''
        pushLine((t('chat.plan.stream_task_started', { task: String(d.task_id ?? '') + roundSuffix + gate, name: delegationAgentName(d.agent_id) })) ?? `Plan · ${d.task_id}`)
      } catch { /* ignore */ }
    })
    es.addEventListener('plan.task.completed', (e) => {
      try {
        const outer = JSON.parse(e.data)
        const d = outer.data ?? outer
        const round = Number(d.round ?? 1)
        const roundSuffix = round > 1 ? ` ×${round}` : ''
        const verdict = String(d.verdict ?? '')
        const verdictSuffix = verdict === 'accept' ? ' ✓' : verdict === 'rework' ? ' ↺' : ''
        pushLine((t('chat.plan.stream_task_completed', { task: String(d.task_id ?? '') + roundSuffix + verdictSuffix, name: delegationAgentName(d.agent_id) })) ?? `Plan · ${d.task_id} · completed`)
      } catch { /* ignore */ }
    })
    es.addEventListener('plan.task.failed', (e) => {
      try {
        const outer = JSON.parse(e.data)
        const d = outer.data ?? outer
        pushLine(t('chat.plan.stream_task_failed', { task: String(d.task_id ?? ''), name: delegationAgentName(d.agent_id) }) ?? `Plan · ${d.task_id} · failed`)
      } catch { /* ignore */ }
    })
    es.addEventListener('plan.task.incomplete', (e) => {
      try {
        const outer = JSON.parse(e.data)
        const d = outer.data ?? outer
        pushLine(t('chat.plan.stream_task_incomplete', { task: String(d.task_id ?? ''), name: delegationAgentName(d.agent_id) }) ?? `Plan · ${d.task_id} · incomplete`)
      } catch { /* ignore */ }
    })
    es.addEventListener('plan.task.rejected', (e) => {
      try {
        const outer = JSON.parse(e.data)
        const d = outer.data ?? outer
        pushLine(t('chat.plan.stream_task_rejected', { task: String(d.task_id ?? ''), by: delegationAgentName(d.rejected_by), feedback: String(d.feedback ?? '') }) ?? `Plan · ${d.task_id} · rework`)
      } catch { /* ignore */ }
    })
    es.addEventListener('plan.task.reopened', (e) => {
      try {
        const outer = JSON.parse(e.data)
        const d = outer.data ?? outer
        const round = Number(d.round ?? 0)
        pushLine(t('chat.plan.stream_task_reopened', { task: String(d.task_id ?? ''), round: String(round + 1) }) ?? `Plan · ${d.task_id} · reopened`)
      } catch { /* ignore */ }
    })
    es.addEventListener('plan.task.invalidated', (e) => {
      try {
        const outer = JSON.parse(e.data)
        const d = outer.data ?? outer
        pushLine(t('chat.plan.stream_task_invalidated', { task: String(d.task_id ?? '') }) ?? `Plan · ${d.task_id} · discarded`)
      } catch { /* ignore */ }
    })
    es.addEventListener('plan.task.skipped', (e) => {
      try {
        const outer = JSON.parse(e.data)
        const d = outer.data ?? outer
        pushLine(t('chat.plan.stream_task_skipped', { task: String(d.task_id ?? '') }) ?? `Plan · ${d.task_id} · skipped`)
      } catch { /* ignore */ }
    })
    es.addEventListener('plan.completed', (e) => {
      try {
        const outer = JSON.parse(e.data)
        const d = outer.data ?? outer
        const statuses = (d.statuses ?? {}) as Record<string, unknown>
        const counts = new Map<string, number>()
        for (const value of Object.values(statuses)) counts.set(String(value), (counts.get(String(value)) ?? 0) + 1)
        const summary = ['completed', 'incomplete', 'failed', 'skipped', 'invalidated']
          .map((status) => counts.get(status) ? `${counts.get(status)} ${t(`chat.plan.status_${status}`)}` : '')
          .filter(Boolean).join(' · ')
        pushLine(t('chat.plan.stream_completed', { summary }) ?? `Plan finished: ${summary}`)
        stopLinesTimer()
        flushLines()
      } catch { /* ignore */ }
    })

    es.addEventListener('run.failed', (e) => {
      try {
        const outer = JSON.parse(e.data)
        const d = outer.data ?? outer
        const line = formatStreamEvent('run.failed', outer, t, locale)
        if (line) lines.push(line)
        stopLinesTimer()
        updateMsg(convId, runId, { content: t('chat.stream.failed', { error: String(d.error ?? 'unknown') }), status: 'failed', streamLines: [...lines] })
      } catch {
        stopLinesTimer()
        updateMsg(convId, runId, { content: t('chat.run_failed') ?? 'Run failed', status: 'failed', streamLines: [...lines] })
      }
      es.close(); streamRef.current.delete(convId)
      closeTokenStream()
    })

    es.addEventListener('done', () => {
      es.close(); streamRef.current.delete(convId)
      closeTokenStream()
      stopLinesTimer()
      // The final answer replaces the kept fragment — the REST copy also
      // fills the terminal status and totals. Skipping the fetch when a
      // fragment was on screen left policy-limited runs spinning forever.
      void fetchFinalAnswer(runId, convId)
    })

    es.onerror = () => {
      es.close(); streamRef.current.delete(convId)
      closeTokenStream()
      stopLinesTimer()
      void fetchFinalAnswer(runId, convId)
    }
  }

  const fetchFinalAnswer = async (runId: string, convId: string) => {
    // run.completed lands in the journal while knowledge extraction (another
    // model call, up to minutes) still keeps the Temporal workflow running —
    // the run row reaches its terminal status noticeably later than the
    // event. A single check here loses that race and the chat spinner never
    // stops; poll with a backoff until the run is actually terminal.
    const terminal = ['completed', 'failed', 'turn_limit', 'cancelled', 'time_limit', 'token_limit', 'budget_limit']
    // ~5 minutes total: extraction StartToClose is 3m, cover its retries too.
    const delays = [...Array(10).fill(2000), ...Array(55).fill(5000)]
    for (const delay of delays) {
      try {
        const run = await workspaceApi.getRun(runId)
        const status = normalizeRunStatus(run.status)
        if (terminal.includes(status)) {
          const answer = run.answer || run.error || 'No answer received'
          // Best-effort token/cost totals for the finished run; pricing may be
          // unconfigured, and this must never block the answer itself.
          let totals: string | undefined
          // Whole-task budget: every run sharing this task (follow-ups and
          // delegated children) so a long conversation totals its full spend.
          let taskTotals: string | undefined
          const [runCostResult, taskCostResult] = await Promise.all([
            runCost(project, runId).catch(() => null),
            run.task_id ? taskCost(project, run.task_id).catch(() => null) : Promise.resolve(null),
          ])
          try {
            const cost = runCostResult
            if (cost) {
              const parts: string[] = []
              const tokens = (cost.total_prompt_tokens ?? 0) + (cost.total_completion_tokens ?? 0)
              if (tokens > 0) parts.push(t('chat.totals.tokens', { count: tokens.toLocaleString(locale) }))
              if ((cost.total_cached_tokens ?? 0) > 0) parts.push(t('chat.totals.cached', { count: (cost.total_cached_tokens ?? 0).toLocaleString(locale) }))
              if (cost.total_cost_usd > 0) parts.push(`$${cost.total_cost_usd.toFixed(4)}`)
              if (parts.length) totals = parts.join(' · ')
            }
            const task = taskCostResult
            if (task && task.run_count > 1) {
              const parts: string[] = [pluralT(t, locale, 'chat.task_totals.runs_n', task.run_count), pluralT(t, locale, 'chat.task_totals.agents_n', task.agents.length)]
              if (task.elapsed_ms > 0) parts.push(`⏱ ${formatDuration(task.elapsed_ms)}`)
              if (task.total_tokens > 0) parts.push(t('chat.totals.tokens', { count: task.total_tokens.toLocaleString(locale) }))
              if ((task.total_cached_tokens ?? 0) > 0) parts.push(t('chat.totals.cached', { count: (task.total_cached_tokens ?? 0).toLocaleString(locale) }))
              if (task.total_cost_usd > 0) parts.push(`$${task.total_cost_usd.toFixed(4)}`)
              taskTotals = parts.join(' · ')
            }
          } catch { /* totals stay hidden */ }
          updateMsg(convId, runId, { content: answer, status, totals, taskTotals })
          return
        }
      } catch { /* transient — keep polling */ }
      await new Promise((resolve) => setTimeout(resolve, delay))
    }
  }

  // The rejected provider call behind a failed chat run: the «запрос → ответ»
  // button fetches the run's model.call_failed journal event on demand and
  // opens the same pair the provider test dialog shows.
  const [callReport, setCallReport] = useState<{ runId: string; event: ObservationEvent } | null>(null)

  const cancelRun = async (runId: string) => {
    try {
      await workspaceApi.cancelRun(runId)
      if (activeConv) {
        const es = streamRef.current.get(activeConv.id)
        if (es) { es.close(); streamRef.current.delete(activeConv.id) }
        const tes = tokenStreamRef.current.get(activeConv.id)
        if (tes) { tes.close(); tokenStreamRef.current.delete(activeConv.id) }
        updateMsg(activeConv.id, runId, { status: 'cancelled', content: t('chat.run_cancelled') ?? 'Run cancelled by user', streamLines: [] })
      }
    } catch (f) { setError(message(f)) }
  }

  // Answer one open ask_human question from the chat (docs/interactive-HITL.md
  // §5): same approval endpoint the runs page uses — one signal path, one
  // authorization model. Values carry the structured form answer; the server
  // validates them against the row's schema before resuming the run. The
  // journaled human.answered/human.cancelled event is the authoritative card
  // state; the optimistic update below only keeps the UI honest while the
  // event travels (and on flaky SSE reconnects).
  const answerQuestion = async (convId: string, runId: string, q: ChatQuestion, response: string, approved: boolean, values?: Record<string, unknown>) => {
    updateMsg(convId, runId, { question: { ...q, busy: true, error: undefined } })
    try {
      const resp = await fetch(`${KERNEL_API}/v1/agent/runs/${encodeURIComponent(runId)}/approval?project=${encodeURIComponent(project)}`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', ...authHeaders() },
        body: JSON.stringify({ operation_id: q.operationId, arguments_hash: q.argumentsHash, approved, actor_id: humanActorID, response, values }),
      })
      if (!resp.ok) throw new Error(`${resp.status} ${await resp.text()}`)
      updateMsg(convId, runId, { question: { ...q, state: approved ? 'answered' : 'cancelled', response: response || undefined, busy: false } })
    } catch (f) {
      updateMsg(convId, runId, { question: { ...q, busy: false, error: message(f) } })
    }
  }

  const sendMessage = async () => {
    if ((!inputValue.trim() && attachments.length === 0) || loading || uploading || !activeConv) return
    const content = inputValue.trim()
    const sentAttachments = attachments
    setAttachments([])
    setInputValue('')
    setLoading(true)
    setError('')

    const sendAttachments = sentAttachments.length ? sentAttachments : undefined

    // A message sent while this conversation's run is still executing is an
    // addendum to THAT run (kernel.user_note signal lands at the nearest turn
    // boundary), not a new parallel run. It renders as a timestamped
    // reply under the ORIGINAL task message — not as a dangling message at
    // the bottom, below the model's answer.
    const runningMsg = activeConv.messages.filter((m) => m.role === 'assistant' && m.status === 'running' && m.runId).pop()
    if (runningMsg?.runId) {
      const noteParts: string[] = []
      if (content) noteParts.push(content)
      // Files travel as sandbox paths only — same convention as the prompt.
      if (sentAttachments.length) noteParts.push(sentAttachments.map((a) => `[Attached file: ${a.path}]`).join('\n'))
      try {
        await workspaceApi.appendRunNote(runningMsg.runId, noteParts.join('\n\n'), project)
        // Attach the addendum to the last user message (the task source).
        const lastUserIdx = activeConv.messages.map((m) => m.role).lastIndexOf('user')
        if (lastUserIdx >= 0) {
          setConversations((prev) => prev.map((c) => {
            if (c.id !== activeConv.id) return c
            const messages = c.messages.map((m, idx) => idx === lastUserIdx ? {
              ...m,
              addenda: [...(m.addenda ?? []), { text: content, at: new Date(), attachments: sendAttachments }],
            } : m)
            return { ...c, messages }
          }))
        } else {
          // No user message to attach to (defensive) — keep the old marker.
          appendStreamLine(activeConv.id, runningMsg.runId, t('chat.note.sent') ?? 'Sent to the current run')
        }
        setLoading(false)
        return
      } catch (noteErr) {
        if (!(noteErr instanceof ApiError && noteErr.status === 409)) {
          setError(message(noteErr))
          setLoading(false)
          return
        }
        // 409: the run reached its terminal state between the UI update and
        // the signal — fall through and start a fresh run over the full
        // history, which already includes this message.
      }
    }

    const userMsg: ChatMessage = { role: 'user', content, timestamp: new Date(), attachments: sendAttachments }
    // Sending implies reading the reply: always follow it from the bottom,
    // even if the user was browsing history a moment ago.
    stickRef.current = true
    setAtBottom(true)
    setUnread(0)
    const updatedConv = { ...activeConv, messages: [...activeConv.messages, userMsg] }
    const firstLine = content || sentAttachments[0]?.name || ''
    if (updatedConv.messages.length === 1) {
      updatedConv.title = firstLine.slice(0, 60) + (firstLine.length > 60 ? '…' : '')
    }
    setConversations((prev) => prev.map((c) => c.id === updatedConv.id ? updatedConv : c))

    try {
      // Build full conversation history as the prompt
      const historyLines: string[] = []
      for (const msg of updatedConv.messages) {
        if (msg.role === 'user') {
          let line = `User: ${msg.content}`
          // Files travel as sandbox paths only — never inlined into the prompt.
          if (msg.attachments?.length) {
            line += '\n' + msg.attachments.map((a) => `[Attached file: ${a.path}]`).join('\n')
          }
          historyLines.push(line)
        } else if (msg.role === 'assistant' && msg.content) {
          historyLines.push(`Assistant: ${msg.content}`)
        }
      }
      const fullPrompt = historyLines.join('\n\n')

      // Reuse existing task for follow-up messages, create new one for first message
      let taskId = activeConv.taskId
      if (!taskId) {
        const task = await workspaceApi.createTask({
          project_id: project,
          agent_id: activeConv.agentId || undefined,
          title: firstLine.slice(0, 80),
          prompt: fullPrompt,
        })
        taskId = task.id
        // Store taskId on the conversation
        setConversations((prev) => prev.map((c) => c.id === activeConv.id ? { ...c, taskId } : c))
      } else {
        // Update the existing task's prompt with the full conversation. The
        // title is intentionally omitted: it was set from the first message
        // and the store keeps the stored one when the field is empty.
        await workspaceApi.updateTask(taskId, { prompt: fullPrompt })
      }

      const result = await workspaceApi.startRun(taskId, { agent_id: activeConv.agentId || undefined })
      const runId = result.run_id

      const assistantMsg: ChatMessage = { role: 'assistant', content: '', timestamp: new Date(), status: 'running', runId, streamLines: [`${t('chat.run_started') ?? 'Run started'} · ${t('chat.streaming') ?? 'streaming…'}`] }
      const withAssistant = { ...updatedConv, messages: [...updatedConv.messages, assistantMsg] }
      setConversations((prev) => prev.map((c) => c.id === withAssistant.id ? withAssistant : c))

      streamRun(runId, activeConv.id)
    } catch (f) {
      setError(message(f))
      const errMsg: ChatMessage = { role: 'assistant', content: `Error: ${message(f)}`, timestamp: new Date(), status: 'failed' }
      const withErr = { ...updatedConv, messages: [...updatedConv.messages, errMsg] }
      setConversations((prev) => prev.map((c) => c.id === withErr.id ? withErr : c))
    } finally { setLoading(false) }
  }

  // Chats, files and the project inbox are anchored to one project
  // (docs/activity-scoping.md §7): the global filter has nothing to open.
  if (!project.trim()) return <ProjectRequired what={t('workspace.title') ?? 'Workspace'} />

  return (
    <div className="workspace-root">
      {/* Left panel: conversations */}
      <div className="workspace-sidebar">
        <div style={{ padding: '0.75rem', borderBottom: '1px solid var(--tm-border)' }}>
          <Button renderIcon={Add} size="sm" onClick={() => { setNewChatAgentId(defaultAgentId ?? allAgents.find((a) => !a.project_id || a.project_id === project)?.id ?? ''); setShowNewChat(true) }} style={{ width: '100%' }}>{t('new.chat') ?? 'New chat'}</Button>
          <div style={{ marginTop: '0.5rem' }}>
            <ListFilter value={convFilter} onChange={setConvFilter} placeholder={t('common.filter_chats') ?? 'Filter chats…'} />
          </div>
        </div>
        <div style={{ flex: 1, overflowY: 'auto', padding: '0.5rem' }}>
          {conversations.length === 0 && (
            <p style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem', padding: '1rem', textAlign: 'center' }}>No conversations yet</p>
          )}
          {conversations.length > 0 && visibleConversations.length === 0 && (
            <p style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem', padding: '1rem', textAlign: 'center' }}>{t('common.no_matches') ?? 'Nothing matches the filter'}</p>
          )}
          {visibleConversations.map((conv) => (
            <div
              key={conv.id}
              onClick={() => setActiveConvId(conv.id)}
              style={{
                padding: '0.75rem',
                marginBottom: '0.25rem',
                borderRadius: '6px',
                cursor: 'pointer',
                background: conv.id === activeConvId ? 'var(--tm-elevated)' : 'transparent',
                border: conv.id === activeConvId ? '1px solid var(--tm-border)' : '1px solid transparent',
              }}
            >
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start' }}>
                <div style={{ flex: 1, minWidth: 0 }}>
                  <div style={{ fontSize: '0.875rem', fontWeight: 500, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{conv.title}</div>
                  <div style={{ fontSize: '0.75rem', color: 'var(--tm-text-3)', marginTop: '0.25rem' }}>
                    {conv.agentId ? agentName(conv.agentId) : (defaultAgentId ? agentName(defaultAgentId) : (t('chat.no_agent') ?? 'No agent'))}{defaultModel ? ` · ${defaultModel}` : ''} · {conv.messages.length} {t('chat.msgs') ?? 'msgs'}
                  </div>
                </div>
                <button
                  onClick={(e) => { e.stopPropagation(); deleteConversation(conv.id) }}
                  style={{ background: 'none', border: 'none', color: 'var(--tm-text-3)', cursor: 'pointer', padding: '0.25rem' }}
                >
                  <TrashCan size={16} />
                </button>
              </div>
            </div>
          ))}
        </div>
      </div>

      {/* Right panel: active chat */}
      <div className="workspace-main">
        {activeConv ? (
          <>
            {/* Chat header */}
            <div style={{ padding: '0.5rem 1rem', borderBottom: '1px solid var(--tm-border)', background: 'var(--tm-surface)', display: 'flex', alignItems: 'center', gap: '0.75rem', flexShrink: 0 }}>
              <strong style={{ fontSize: '0.875rem' }}>{activeConv.title}</strong>
              {activeConv.agentId && <Tag type="blue" size="sm">{agentName(activeConv.agentId)}</Tag>}
            </div>

            {/* Chat messages */}
            <div className="workspace-messages-wrap">
              <div className="workspace-messages" ref={messagesRef} onScroll={onMessagesScroll}>
              {activeConv.messages.length === 0 && (
                <div style={{ textAlign: 'center', color: 'var(--tm-text-3)', padding: '3rem 1rem' }}>
                  <Heading>Temporality Agent</Heading>
                  <p>Send a message to start the conversation.</p>
                </div>
              )}
              {activeConv.messages.map((msg, i) => (
                <div key={i} style={{ marginBottom: '0.75rem', display: 'flex', justifyContent: msg.role === 'user' ? 'flex-end' : 'flex-start' }}>
                  <div className={msg.role === 'user' ? 'chat-bubble-user' : 'chat-bubble-assistant'}>
                    {/* Reasoning/thinking block — collapsible */}
                    {msg.reasoning && (
                      <details className="chat-thinking">
                        <summary>
                          💭 {t('chat.thinking') ?? 'Thinking'}
                        </summary>
                        <div className="chat-thinking-content">
                          {msg.reasoning}
                        </div>
                      </details>
                    )}

                    {msg.role === 'user' && msg.attachments && msg.attachments.length > 0 && (
                      <div className="chat-attachments chat-attachments-sent">
                        {msg.attachments.map((a) => (
                          <span key={a.path} className="chat-attachment-chip" title={a.path}>
                            <span className="chip-name">📎 {a.name}</span>
                            <span className="chip-size">{formatSize(a.size)}</span>
                          </span>
                        ))}
                      </div>
                    )}

                    {msg.content && msg.role === 'assistant' ? (
                      <Markdown content={linkifyWorkspacePaths(msg.content, project)} onFile={(path) => void onFileLink(path)} />
                    ) : msg.content ? (
                      <div style={{ whiteSpace: 'pre-wrap' }}>{msg.content}</div>
                    ) : null}

                    {/* send_file chips: the run delivered a file — download it
                        straight from the chat bubble. */}
                    {msg.role === 'assistant' && (msg.files ?? []).length > 0 && (
                      <div style={{ display: 'flex', gap: '0.35rem', flexWrap: 'wrap', marginTop: '0.4rem' }}>
                        {(msg.files ?? []).map((f) => (
                          <button
                            key={f.path}
                            className="chat-attachment-chip"
                            onClick={() => void onFileLink(f.path)}
                            title={f.path}
                            style={{ cursor: 'pointer', border: '1px solid var(--tm-teal)', background: 'var(--tm-elevated)', borderRadius: '6px', color: 'var(--tm-teal)', fontSize: '0.72rem', padding: '0.25rem 0.6rem' }}
                          >📎 {f.name}</button>
                        ))}
                      </div>
                    )}

                    {/* Mid-run addenda trail the original message (reply-style):
                        the first line is the text, everything amended later
                        stacks below with its timestamp. */}
                    {msg.role === 'user' && (msg.addenda ?? []).length > 0 && (
                      <div style={{ marginTop: '0.5rem', display: 'flex', flexDirection: 'column', gap: '0.35rem' }}>
                        {(msg.addenda ?? []).map((addendum, addendumIndex) => (
                          <div key={addendumIndex} style={{ borderLeft: '2px solid var(--tm-border-strong)', paddingLeft: '0.6rem' }}>
                            <div style={{ fontSize: '0.68rem', color: 'var(--tm-text-3)', display: 'flex', alignItems: 'center', gap: '0.3rem' }}>
                              <span>⤳</span>
                              <span>{t('chat.addendum_at', { time: new Date(addendum.at).toLocaleTimeString() }) ?? `Amended at ${new Date(addendum.at).toLocaleTimeString()}`}</span>
                            </div>
                            {addendum.text && <div style={{ fontSize: '0.82rem', whiteSpace: 'pre-wrap' }}>{addendum.text}</div>}
                            {addendum.attachments && addendum.attachments.length > 0 && (
                              <div style={{ display: 'flex', gap: '0.3rem', flexWrap: 'wrap', marginTop: '0.25rem' }}>
                                {addendum.attachments.map((a) => (
                                  <span key={a.path} className="chat-attachment-chip" title={a.path}>
                                    <span className="chip-name">📎 {a.name}</span>
                                  </span>
                                ))}
                              </div>
                            )}
                          </div>
                        ))}
                      </div>
                    )}

                    {msg.role === 'assistant' && msg.status === 'running' && msg.runId && msg.runId === activeRunningRunId && tokenMetrics && (
                      <div style={{ color: 'var(--tm-muted)', fontSize: '0.72rem', display: 'flex', alignItems: 'center', gap: '0.35rem', marginTop: '0.3rem', minWidth: 0 }}>
                        <span className='spinner' />
                        <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', minWidth: 0 }}>
                          {liveStepParts.join(' · ')}
                        </span>
                      </div>
                    )}

                    {msg.role === 'assistant' && msg.runId && msg.status && (
                      <>
                        <DelegationTree project={project} runId={msg.runId} agents={allAgents} live={msg.status === 'running'} />
                        <PlanGraph project={project} runId={msg.runId} agents={allAgents} live={msg.status === 'running'} />
                      </>
                    )}

                    {/* Interactive HITL card: the agent's question and the
                        answer controls ride the same bubble as the run. */}
                    {msg.role === 'assistant' && msg.question && msg.runId && activeConv && (
                      <div style={{ marginTop: msg.content || (msg.streamLines?.length ?? 0) > 0 ? '0.5rem' : 0 }}>
                        <ChatQuestionCard
                          question={msg.question}
                          busy={!!msg.question.busy}
                          onAnswer={(response, approved, values) => { void answerQuestion(activeConv.id, msg.runId!, msg.question!, response, approved, values) }}
                        />
                      </div>
                    )}

                    {msg.status === 'running' && msg.streamLines && msg.streamLines.length > 0 && (
                      <div style={{ marginTop: msg.content ? '0.5rem' : 0 }}>
                        <ChatStreamLines lines={msg.streamLines} />
                        <div style={{ display: 'flex', alignItems: 'center', gap: '0.4rem', marginTop: '0.25rem' }}>
                          <span className="spinner" />
                          <span className="chat-streaming">{t('chat.streaming') ?? 'streaming…'}</span>
                          {msg.runId && (
                            <button
                              onClick={() => cancelRun(msg.runId!)}
                              style={{ background: 'none', border: '1px solid var(--tm-danger)', borderRadius: '4px', color: 'var(--tm-danger)', cursor: 'pointer', fontSize: '0.65rem', padding: '0.1rem 0.4rem', marginLeft: 'auto' }}
                              title={t('chat.cancel_run') ?? 'Cancel this run'}
                            >{t('chat.stop') ?? 'Stop'}</button>
                          )}
                          {msg.runId && (
                            /* New tab on purpose: the run is still streaming
                               here — the chat keeps its live view while the
                               trace opens next to it. */
                            <a
                              href={runTraceUrl(project, msg.runId)}
                              target="_blank"
                              rel="noopener"
                              style={{ background: 'none', border: '1px solid var(--tm-border)', borderRadius: '4px', color: 'var(--tm-teal)', textDecoration: 'none', cursor: 'pointer', fontSize: '0.65rem', padding: '0.1rem 0.4rem' }}
                              title={t('chat.trace_title') ?? 'Open the run trace'}
                            >{t('chat.trace') ?? 'Trace'}</a>
                          )}
                        </div>
                      </div>
                    )}

                    {msg.runId && msg.status && msg.status !== 'running' && (
                      <div className={msg.role === 'user' ? 'chat-status chat-status-user' : 'chat-status'}>
                        <Tag type={STATUS_COLORS[msg.status] || 'gray'} size="sm">{msg.status}</Tag>
                        {msg.role === 'assistant' && msg.totals && (
                          <span style={{ color: 'var(--tm-muted)', fontSize: '0.7rem' }}>{msg.totals}</span>
                        )}
                        {msg.role === 'assistant' && msg.taskTotals && (
                          <span style={{ color: 'var(--tm-muted)', fontSize: '0.7rem' }} title={t('chat.task_totals.title') ?? 'Whole-task budget: every run of this task (follow-ups and delegated agents)'}>Σ {msg.taskTotals}</span>
                        )}
                        {activeConv && (
                          <button
                            onClick={() => branchConversation(activeConv.id, i)}
                            style={{ background: 'none', border: '1px solid var(--tm-border)', borderRadius: '4px', color: 'var(--tm-teal)', cursor: 'pointer', fontSize: '0.7rem', padding: '0.15rem 0.5rem', marginLeft: 'auto' }}
                            title={t('chat.branch_title') ?? 'Branch conversation from this point'}
                          >{t('chat.branch') ?? 'Branch'}</button>
                        )}
                        {msg.status === 'failed' && msg.runId && activeConv && (
                          <CallReportButton runId={msg.runId} project={project} onOpen={(event) => setCallReport({ runId: msg.runId!, event })} />
                        )}
                      </div>
                    )}
                  </div>
                </div>
              ))}
              </div>
              {!atBottom && (
                <button
                  className="chat-jump-btn"
                  onClick={jumpToBottom}
                  title={t('chat.scroll_bottom') ?? 'Scroll to bottom'}
                  aria-label={t('chat.scroll_bottom') ?? 'Scroll to bottom'}
                >
                  <ArrowDown size={16} />
                  {unread > 0 && <span className="chat-jump-badge">{unread > 99 ? '99+' : unread}</span>}
                </button>
              )}
            </div>

            {/* Input area */}
            <div className="workspace-input-bar">
              {error && <InlineNotification kind="error" title="Error" subtitle={error} onClose={() => setError('')} lowContrast style={{ marginBottom: '0.5rem' }} />}
              {(attachments.length > 0 || uploading) && (
                <div className="chat-attachments">
                  {attachments.map((a, i) => (
                    <span key={a.path} className="chat-attachment-chip" title={a.path}>
                      <span className="chip-name">{a.name}</span>
                      <span className="chip-size">{formatSize(a.size)}</span>
                      <button
                        onClick={() => setAttachments((prev) => prev.filter((_, j) => j !== i))}
                        title={t('chat.remove_attachment') ?? 'Remove attachment'}
                        aria-label={t('chat.remove_attachment') ?? 'Remove attachment'}
                      >×</button>
                    </span>
                  ))}
                  {uploading && (
                    <span className="chat-attachment-chip chat-attachment-uploading">
                      <span className="spinner" /><span className="chip-name">{t('chat.uploading') ?? 'Uploading…'}</span>
                    </span>
                  )}
                </div>
              )}
              {/* Mid-run messages route into the running run as addenda — the
               * input's placeholder says so (was a separate notice line). */}
              <div style={{ display: 'flex', gap: '0.75rem', alignItems: 'flex-end' }}>
                <input ref={fileInputRef} type="file" multiple hidden onChange={(e) => void onFilesPicked(e)} />
                <Button
                  kind="ghost"
                  size="md"
                  renderIcon={Attachment}
                  iconDescription={t('chat.attach_file') ?? 'Attach files'}
                  tooltipPosition="top"
                  hasIconOnly
                  onClick={() => fileInputRef.current?.click()}
                  disabled={loading || uploading}
                  style={{ marginBottom: '2px', flexShrink: 0 }}
                />
                <div style={{ flex: 1 }}>
                  <TextArea
                    id="chat-input"
                    labelText=""
                    hideLabel
                    value={inputValue}
                    onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => setInputValue(e.target.value)}
                    placeholder={
                      activeConv.messages.some((m) => m.role === 'assistant' && m.status === 'running' && m.runId)
                        ? (t('chat.placeholder_running') ?? 'Добавится к выполняющемуся запуску как дополнение к задаче…')
                        : (t('chat.placeholder') ?? 'Введите сообщение… (Shift+Enter для новой строки)')
                    }
                    rows={3}
                    ref={chatInputRef}
                    onKeyDown={(e: React.KeyboardEvent<HTMLTextAreaElement>) => {
                      if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); void sendMessage() }
                    }}
                    style={{ resize: 'none', minHeight: '72px', maxHeight: '33vh', overflowY: 'auto' }}
                  />
                </div>
                {loading && <span className="spinner" title={t('chat.sending') ?? 'Sending…'} style={{ alignSelf: 'center', flexShrink: 0 }} />}
                <Button renderIcon={Send} onClick={() => void sendMessage()} disabled={loading || uploading || (!inputValue.trim() && attachments.length === 0)} style={{ marginBottom: '2px' }}>{t('chat.send') ?? 'Send'}</Button>
              </div>
            </div>
          </>
        ) : (
          <div style={{ flex: 1, display: 'flex', alignItems: 'center', justifyContent: 'center', color: 'var(--tm-text-3)' }}>
            <div style={{ textAlign: 'center' }}>
              <Heading>{t('chat.title') ?? 'Temporality Agent'}</Heading>
              <p style={{ marginTop: '0.5rem' }}>{t('chat.select_conversation') ?? 'Select a conversation or start a new one.'}</p>
              <Button renderIcon={Add} onClick={() => { setNewChatAgentId(defaultAgentId ?? allAgents.find((a) => !a.project_id || a.project_id === project)?.id ?? ''); setShowNewChat(true) }} style={{ marginTop: '1rem' }}>{t('new.chat') ?? 'New chat'}</Button>
            </div>
          </div>
        )}
      </div>

      {/* New chat modal */}
      {showNewChat && (
        <AppModal onClose={() => setShowNewChat(false)} panelStyle={{ width: '420px' }}>
            <Heading style={{ fontSize: '1.1rem', marginBottom: '0.75rem' }}>{t('new.conversation') ?? 'New conversation'}</Heading>
            <Select
              id="new-chat-agent"
              labelText={t('chat.agent') ?? 'Agent'}
              value={newChatAgentId}
              onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setNewChatAgentId(e.target.value)}
            >
              {defaultAgentId && (
                <SelectItem value="" text={`${t('chat.project_default') ?? 'Project default'} (${allAgents.find((a) => a.id === defaultAgentId)?.name ?? defaultAgentId})`} />
              )}
              {allAgents.filter((a) => a.id !== defaultAgentId && (!a.project_id || a.project_id === project)).map((a) => <SelectItem key={a.id} value={a.id} text={`${a.name}${a.model ? ` (${a.model})` : ''}`} />)}
            </Select>
            {!defaultAgentId && (
              <p style={{ fontSize: '0.75rem', color: 'var(--tm-text-3)', margin: '0.5rem 0 0' }}>{t('chat.no_default_hint') ?? 'This project has no default agent yet — set one on the Agents page.'}</p>
            )}
            <div className="form-actions">
              <Button kind="secondary" onClick={() => setShowNewChat(false)}>{t('action.cancel') ?? 'Cancel'}</Button>
              <Button onClick={createConversation}>{t('action.create') ?? 'Create'}</Button>
            </div>
        </AppModal>
      )}

      {callReport && (
        <AppModal onClose={() => setCallReport(null)} panelStyle={{ width: '640px' }}>
          <CallReportView event={callReport.event} t={t} />
          <div style={{ display: 'flex', justifyContent: 'flex-end', marginTop: '0.75rem' }}>
            <Button kind="secondary" onClick={() => setCallReport(null)}>{t('action.close') ?? 'Close'}</Button>
          </div>
        </AppModal>
      )}
    </div>
  )
}

/** The «Запрос → Ответ» button for a failed chat run. Self-checking: it
 *  fetches the run's model.call_failed event once and renders only when the
 *  run actually carries one — failures that died of other causes (kernel
 *  restart removed the workflow, turn limits) show no dead button. */
function CallReportButton({ runId, project, onOpen }: {
  runId: string
  project: string
  onOpen: (event: ObservationEvent) => void
}) {
  const { t } = useI18n()
  const [event, setEvent] = useState<ObservationEvent | null>(null)
  const [checked, setChecked] = useState(false)

  useEffect(() => {
    let cancelled = false
    void observationApi.events(project || undefined, undefined, undefined, undefined, { run: runId, type: 'model.call_failed', limit: 5 })
      .then((page) => {
        if (cancelled) return
        const last = page.events[page.events.length - 1]
        if (last) setEvent(last)
        setChecked(true)
      })
      .catch(() => { if (!cancelled) setChecked(true) })
    return () => { cancelled = true }
  }, [runId, project])

  if (!checked || !event) return null
  return (
    <button
      onClick={() => onOpen(event)}
      style={{ background: 'none', border: '1px solid var(--tm-danger)', borderRadius: '4px', color: 'var(--tm-danger)', cursor: 'pointer', fontSize: '0.7rem', padding: '0.15rem 0.5rem' }}
      title={t('runs.call_report_hint') ?? 'The rejected provider call: where and with which token'}
    >{t('runs.call_report') ?? 'Запрос → Ответ'}</button>
  )
}
