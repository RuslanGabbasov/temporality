import type { Locale } from './locales'
import { eventInfo, type EventMeta } from './eventCatalog'
import { explainKernelError } from './eventSummary'
import type { ObservationEvent } from './observationApi'

/**
 * Declarative presentation catalog for run-trace events (T2, the three-layer
 * model of v2-consolidated.md §4–§5).
 *
 * The kernel journal is an open set of dotted event types; this module maps
 * every type to what the three layers show:
 *  - timeline row:  semantic category icon/color, a human title (NO event-type
 *    name — §4 principle), one key fact, status and duration markers;
 *  - details:       human-form arguments, result, failure cause (via
 *    explainKernelError), links, console slot for run_command;
 *  - raw layer:     the event envelope as JSON, collapsed by default — the
 *    only place technical payloads are allowed (§4.3, K-1).
 *
 * Invariant P-2 (§7, DoD-2): secret.* values never render on any layer —
 * only name / status / denial reason; secret.value_set → «значение задано».
 *
 * Streaming types (model.text_delta / model.reasoning) are kind='stream':
 * they never render as timeline strings — the live model row and the parent
 * model call's details are fed from them elsewhere (AgentRuns trace memo).
 */

export type TranslateFn = (key: string, vars?: Record<string, string>) => string

/** Semantic icon+color of a timeline row: the catalog group's meaning, not
 *  the event type (§4.1). */
export interface CategoryLook {
  icon: string
  color: string
}

/** Semantic category of an event — its catalog group id. */
export type EventCategory =
  | 'runs' | 'model' | 'tools' | 'delegation' | 'teams' | 'approvals'
  | 'knowledge' | 'artifacts' | 'secrets' | 'mcp' | 'triggers' | 'intake'
  | 'quality' | 'sagas' | 'ops'

/** The presentation classes every event type maps into (§4–§5). */
export type PresentationKind = 'normal' | 'minor' | 'noise' | 'stream'

/** Human-form of one event for the timeline layer (§4.1). */
export interface TimelineRow {
  /** Type the row was built from — for keying and the §5 folding tests. */
  type: string
  /** Semantic category (catalog group id; 'ops' fallback for unknown types). */
  category: EventCategory
  /** Semantic icon+color — never the event-type name (§4 principle). */
  icon: string
  color: string
  /** Human title, ≤120 chars, never the raw type name. */
  title: string
  /** One key fact: command preview, token count, child agent, verdict… */
  fact: string
  /** Duration marker («12s», «4m 30s») when the payload carries latency. */
  duration: string
  /** Row status indicator (§4.1). */
  status: 'ok' | 'error' | 'cancelled' | 'running' | 'none'
  /** Presentation class: folded minor, ops noise or a streaming feed. */
  kind: PresentationKind
  /** Whether the details layer is reachable for this event. */
  hasDetails: boolean
}

/** Payload fields whose presence marks the event as a failure, so generic
 *  payloads do not swallow a real failure into a neutral row. */
const FAILURE_FIELDS = ['error', 'error_type', 'denied_reason', 'fail_reason'] as const

/** §5.7: ops noise — folded/filterable by default, never standalone rows. */
const NOISE_TYPES = new Set(['storage.snapshot', 'operation.reconciled', 'notification.sent', 'memory.read.failed'])

/** §5.2: streaming feeds — never rendered as timeline strings. */
const STREAM_TYPES = new Set(['model.text_delta', 'model.reasoning'])

/** §5.1: turn boundaries fold into the «Ход N» group. */
const MINOR_TURN_RE = /^turn\.(started|completed)$/

/** §5.4: minor plan-task transitions fold into «изменения плана». */
const MINOR_PLAN_RE = /^plan\.task\.(skipped|invalidated|reopened|incomplete)$/

/** Collapse whitespace so multi-line payloads stay one timeline line. */
const squash = (value: unknown): string => String(value ?? '').replace(/\s+/g, ' ').trim()

/** Human string of a payload value: objects JSON-ify, strings pass through. */
const textOf = (value: unknown): string => {
  if (value === undefined || value === null || value === '') return ''
  if (typeof value === 'string') return value.trim()
  if (typeof value === 'number' || typeof value === 'boolean') return String(value)
  return JSON.stringify(value)
}

/** Human byte size: 512 B / 244.1 KB. */
export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return ''
  if (bytes < 1024) return `${Math.round(bytes)} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}

/** Compact handle for ids nobody could resolve to text: a bare guid says
 *  nothing, the first 8 chars are enough to eyeball-match rows. */
export function shortHandle(id: unknown): string {
  const s = String(id ?? '')
  return s.length <= 16 ? s : `${s.slice(0, 8)}…`
}

/** Localized run status: the raw journal values (running/completed/…) never
 *  reach the user — callers keep their own tag colors, only the label is
 *  shared. Unknown values fall back to the raw string. */
export function runStatusLabel(status: unknown, t: TranslateFn): string {
  const value = String(status ?? '')
  if (!value) return ''
  const key = value === 'cancelled' ? 'status.cancelled'
    : value === 'completed' ? 'chat.delegation.completed'
      : value === 'failed' ? 'chat.delegation.failed'
        : value === 'running' ? 'chat.delegation.running'
          : ''
  if (!key) return value
  const label = t(key)
  return label === key ? value : label
}

/** Try to parse JSON-encoded tool arguments; tool payloads arrive as JSON
 *  strings (`{"command":["go","test"]}`), the human preview reads fields.
 *  Exported: the background-ops panel (AgentRuns) reads the background flag
 *  and the command of a detached run_command the same way. */
export function parseArguments(raw: unknown): Record<string, unknown> | null {
  if (typeof raw !== 'string' || !raw) return null
  try {
    const parsed: unknown = JSON.parse(raw)
    return parsed && typeof parsed === 'object' && !Array.isArray(parsed)
      ? (parsed as Record<string, unknown>)
      : null
  } catch {
    return null
  }
}

/** One key fact of a tool payload: the command, path or target that tells
 *  what the call touched. Mirrors eventSummary's preview heuristics. */
function toolFact(d: Record<string, unknown>): string {
  const args = parseArguments(d.arguments)
  if (args) {
    if (Array.isArray(args.command) && args.command.length > 0) return squash(args.command.join(' '))
    if (Array.isArray(args.items) && args.items.length > 0) {
      // remember_many: the first proposition IS the fact (what it decided to
      // keep), the count tells the batch size — readable without a click.
      const first = args.items[0] as Record<string, unknown> | string
      const proposition = squash(first && typeof first === 'object' ? first.proposition : first)
      const rest = args.items.length - 1
      return proposition.slice(0, 90) + (rest > 0 ? ` +${rest}` : '')
    }
    for (const key of ['path', 'file', 'query', 'url', 'proposition', 'prompt', 'skill_id', 'saga_id', 'server', 'tool']) {
      const value = textOf(args[key])
      if (value) return squash(value)
    }
    if (args.agent_id) {
      const gist = squash(args.prompt)
      return `→ ${String(args.agent_id)}${gist ? ` · ${gist}` : ''}`
    }
  }
  return squash(d.output ?? d.path ?? d.query ?? d.title ?? d.tool)
}

/** A duration marker from the latency fields the kernel actually journals. */
function durationOf(d: Record<string, unknown>): string {
  for (const key of ['latency_ms', 'duration_ms', 'wait_ms']) {
    const ms = Number(d[key])
    if (Number.isFinite(ms) && ms > 0) {
      if (ms < 1000) return `${Math.round(ms)}ms`
      if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`
      const minutes = Math.floor(ms / 60_000)
      const seconds = Math.round((ms % 60_000) / 1000)
      return seconds > 0 ? `${minutes}m ${seconds}s` : `${minutes}m`
    }
  }
  return ''
}

/** Turn count as a duration-like marker for delegation payloads. */
function turnsOf(d: Record<string, unknown>): string {
  return d.turns !== undefined ? `${squash(d.turns)} turns` : ''
}

/** Human title of a tool call: real tool ids stay verbatim (they name the
 *  capability the reader looks for); the product concepts "team"/"plan" run
 *  as pseudo tools and get localized titles. */
function toolTitle(d: Record<string, unknown>, t: TranslateFn): string {
  const tool = typeof d.tool === 'string' && d.tool ? d.tool : ''
  if (!tool) return ''
  if (tool === 'team') return t('runs.event.team')
  if (tool === 'plan') return t('runs.event.plan')
  return tool
}

/** The tool/action an approval names: operation.tool, action or reason. */
function approvalActionOf(d: Record<string, unknown>): string {
  const operation = d.operation as Record<string, unknown> | undefined
  return squash(operation?.tool ?? d.action)
}

/** The secret's name only — P-2 caps what a secret row may carry. */
const secretName = (d: Record<string, unknown>): string => squash(d.secret ?? d.name)

/** P-2 sanctioned fact of a denial: the reason only. */
const denialReason = (d: Record<string, unknown>): string => squash(d.reason ?? d.denied_reason)

/** Human cause: peel Temporal envelopes so the row shows the real cause. */
function causeOf(d: Record<string, unknown>): string {
  const raw = String(d.error ?? '')
  if (!raw) return ''
  return explainKernelError(raw).cause.slice(0, 80)
}

const versionOf = (d: Record<string, unknown>): string => {
  const version = d.version
  return version !== undefined && version !== null && String(version) !== '' ? `v${squash(version)}` : ''
}

const bytesOf = (raw: unknown): string => {
  const bytes = Number(raw)
  return Number.isFinite(bytes) && bytes > 0 ? formatBytes(bytes) : ''
}

const mcpTitle = (d: Record<string, unknown>): string => squash(d.tool) || 'MCP'

const skillName = (d: Record<string, unknown>): string => squash(d.skill_name ?? d.skill_id)

const gateFact = (d: Record<string, unknown>): string => {
  const run = d.evaluation_run_id != null && String(d.evaluation_run_id) !== '' ? shortHandle(String(d.evaluation_run_id)) : ''
  return [squash(d.agent_id), d.proposal_id != null ? `#${squash(d.proposal_id)}` : '', squash(d.cause), run]
    .filter(Boolean)
    .join(' · ')
}

/** Context the overrides may read: the translate fn and the locale. */
interface OverrideContext {
  t: TranslateFn
  locale: Locale
}

/** Localized human title of an event type (§4 principle: meaning, not type). */
function titleOf(meta: EventMeta | undefined, locale: Locale): string {
  return meta ? meta[locale].title : ''
}

/** Per-type declarative overrides: the timeline aspects the generic catalog
 *  derivation cannot express. Types outside this map derive their row from
 *  the catalog group + payload (DoD-1: full coverage via the generic row). */
const OVERRIDES: Record<string, (d: Record<string, unknown>, ctx: OverrideContext) => Partial<TimelineRow>> = {
  // ── §5.1 runs ──
  // The run's birth marker: a non-expandable row — the initiating request
  // renders as its own overview tile, and the payload has nothing else a
  // reader would act on.
  'run.started': (d, { t }) => ({ title: t('runs.event.run_started'), fact: squash(d.request ?? d.model), status: 'running', hasDetails: false }),
  'run.completed': (d, { t }) => ({ title: t('runs.event.run_completed'), fact: `${squash(d.turns ?? '?')} ${t('runs.turns')}`, status: 'ok' }),
  'run.failed': (d, { t }) => ({ title: t('runs.event.run_failed'), status: 'error', fact: causeOf(d) }),
  'run.cancelled': () => ({ status: 'cancelled' }),
  'run.user_note': (d, { t }) => ({ title: t('runs.event.user_note'), fact: t('runs.event.user_note_detail', { turn: squash(d.turn ?? '?') }) }),
  'run.note_dropped': (d, { t }) => ({ title: `⚠ ${t('runs.event.note_dropped')}`, fact: t('runs.event.note_dropped_detail'), status: 'error' }),
  'agent.summary': (d, { t }) => ({ title: t('runs.event.summary'), fact: squash(d.answer).slice(0, 80) || squash(d.kind) }),
  // ── §5.2 model ──
  'model.started': (d, { t }) => ({ title: t('runs.event.model_call'), fact: squash(d.model), status: 'running' }),
  'model.completed': (d, { t }) => ({
    title: t('runs.event.model_response'),
    fact: [
      d.total_tokens ? `${squash(d.total_tokens)} tok` : '',
      d.latency_ms ? `${(Number(d.latency_ms) / 1000).toFixed(1)}s` : '',
      d.tool_call_count ? `${squash(d.tool_call_count)} tools` : '',
    ].filter(Boolean).join(' · '),
    status: 'ok',
  }),
  'model.failed': (d) => ({ status: 'error', fact: causeOf(d) }),
  'model.attempt': (d, { t }) => {
    // reason=routed is the routing decision (which provider served the call),
    // not a retry — no attempt number, no error styling.
    if (d.reason === 'routed') {
      return { title: t('runs.event.model_route'), fact: squash(d.route ?? ''), status: 'ok' }
    }
    return {
      title: t('runs.event.model_attempt'),
      fact: [
        d.reason === 'stream_fallback' ? t('runs.event.model_attempt_fallback')
          : d.reason === 'stream_timeout' ? t('runs.event.model_attempt_timeout')
            : t('runs.event.model_attempt_retry'),
        t('runs.event.model_attempt_n', { n: squash(d.attempt ?? '?') }),
      ].filter(Boolean).join(' · '),
      status: 'error',
    }
  },
  'model.nudged': (d, { t }) => ({ title: t('runs.event.model_nudged'), fact: t('runs.event.model_nudged_detail', { turn: squash(d.turn ?? '?') }) }),
  // ── §5.3 tools ──
  'tool.started': (d, { t }) => ({ title: toolTitle(d, t), fact: toolFact(d) }),
  'tool.completed': (d, { t }) => {
    const failed = d.exit_code !== undefined && d.exit_code !== 0
    const parts = [
      toolFact({ ...d, output: '' }),
      d.exit_code !== undefined ? `exit ${squash(d.exit_code)}` : '',
      typeof d.output === 'string' ? squash(d.output).slice(0, 60) : '',
    ].filter(Boolean).join(' · ')
    return { title: toolTitle(d, t), fact: parts, status: failed ? 'error' : 'ok' }
  },
  'tool.failed': (d, { t }) => ({ title: toolTitle(d, t), status: 'error', fact: causeOf(d) || toolFact(d) }),
  'tool.blocked': (d, { t }) => ({ title: `🚫 ${t('runs.event.blocked')}`, status: 'error', fact: squash(d.reason) }),
  // ── §5.4 delegation / plans ──
  'delegation.started': (d, { t }) => ({ title: t('runs.event.delegation'), fact: `→ ${squash(d.agent_id ?? '?')}`, status: 'running' }),
  'delegation.completed': (d, { t }) => ({ title: t('runs.event.delegation_completed'), fact: squash(d.agent_id), status: 'ok' }),
  'delegation.failed': (d, { t }) => ({ title: t('runs.event.delegation_failed'), fact: causeOf(d), status: 'error' }),
  'plan.started': (d, { t }) => ({ title: t('runs.event.plan_started'), fact: Array.isArray(d.tasks) ? `${d.tasks.length} ${t('runs.event.plan_tasks')}` : '' }),
  'plan.completed': (d, { t }) => ({ title: t('runs.event.plan_completed'), status: 'ok' }),
  'plan.task.started': (d, { t }) => ({ title: t('runs.event.plan_task_started'), fact: [squash(d.task_id), squash(d.agent_id)].filter(Boolean).join(' · ') }),
  'plan.task.completed': (d, { t }) => ({ title: t('runs.event.plan_task_completed'), fact: squash(d.task_id), status: 'ok' }),
  'plan.task.failed': (d, { t }) => ({ title: t('runs.event.plan_task_failed'), fact: [squash(d.task_id), causeOf(d)].filter(Boolean).join(' · '), status: 'error' }),
  'plan.task.rejected': (d, { t }) => ({ title: `↺ ${t('runs.event.plan_task_rejected')}`, fact: [squash(d.task_id), squash(d.feedback)].filter(Boolean).join(' · '), status: 'error' }),
  'plan.task.skipped': (d, { t }) => ({ title: t('runs.event.plan_task_skipped'), fact: squash(d.task_id), status: 'none' }),
  'plan.task.invalidated': (d, { t }) => ({ title: t('runs.event.plan_task_invalidated'), fact: squash(d.task_id), status: 'none' }),
  'plan.task.reopened': (d, { t }) => ({ title: t('runs.event.plan_task_reopened'), fact: squash(d.task_id) }),
  'plan.task.incomplete': (d, { t }) => ({ title: t('runs.event.plan_task_incomplete'), fact: squash(d.task_id), status: 'none' }),
  // ── §5.5 approvals / human ──
  'approval.requested': (d, { t }) => ({ title: t('runs.event.approval_needed'), fact: approvalActionOf(d), status: 'running' }),
  'approval.granted': (d, { t }) => ({ title: t('runs.event.approved'), fact: [squash(d.approver), squash(d.reason)].filter(Boolean).join(' · '), status: 'ok' }),
  'approval.auto_granted': (d, { t }) => {
    const operation = d.operation as Record<string, unknown> | undefined
    const tool = typeof operation?.tool === 'string' ? operation.tool : squash(d.action)
    const args = operation?.arguments
    const command = args && typeof args === 'object' && Array.isArray((args as Record<string, unknown>).command)
      ? ((args as Record<string, unknown>).command as unknown[]).join(' ')
      : ''
    return {
      title: `${t('runs.event.auto_approved')} · ${tool}`,
      fact: command.slice(0, 120),
      status: 'ok',
      // The approved operation's arguments render in the details layer —
      // the reader must see WHAT was auto-approved without leaving the trace.
      hasDetails: true,
    }
  },
  'approval.rejected': (d, { t }) => ({ title: t('runs.event.rejected'), fact: squash(d.reason), status: 'error' }),
  'human.requested': (d, { t }) => ({ title: t('runs.event.human_requested'), fact: squash(d.question).slice(0, 100) }),
  'human.answered': (d, { t }) => ({ title: t('runs.event.human_answered'), fact: squash(d.response).slice(0, 80), status: 'ok' }),
  'human.rejected': (d, { t }) => ({ title: t('runs.event.rejected'), fact: squash(d.reason ?? d.response), status: 'error' }),
  'human.reasked': (d, { t }) => ({ title: t('runs.event.human_requested'), fact: squash(d.question).slice(0, 100) }),
  'human.cancelled': () => ({ status: 'cancelled' }),
  'human.timed_out': () => ({ status: 'error' }),
  'hitl.verdict.recorded': (d, { t }) => ({
    title: t('runs.event.hitl_verdict'),
    fact: [
      d.type === 'accepted' ? t('runs.event.hitl_verdict_accepted')
        : d.type === 'corrected' ? t('runs.event.hitl_verdict_corrected')
          : d.type === 'rejected' ? t('runs.event.hitl_verdict_rejected')
            : squash(d.type),
      squash(d.response).slice(0, 80),
      squash(d.answered_by),
    ].filter(Boolean).join(' · '),
    status: d.type === 'rejected' ? 'error' : 'ok',
    // The response text renders in the details layer — the folded row only
    // previews it; the reader must be able to open the decision.
    hasDetails: true,
  }),
  // ── §5.6 knowledge / hints (rows stay minimal — §5 folds the bulk into
  // the hints aggregate; a row still renders when the group is unfolded) ──
  'knowledge.proposed': (d, { t }) => ({ title: t('runs.event.learned'), fact: squash(d.proposition).slice(0, 90) }),
  'knowledge.recalled': (d, { t }) => ({ title: t('runs.event.recalled'), fact: squash(d.proposition).slice(0, 90) }),
  'knowledge.used': (d, { t }) => ({ title: t('runs.event.knowledge_used'), fact: squash(d.proposition).slice(0, 90) }),
  'knowledge.confirmed': (d, { t }) => ({ title: t('runs.event.knowledge_confirmed'), fact: squash(d.proposition).slice(0, 90), status: 'ok' }),
  'knowledge.challenged': (d, { t }) => ({ title: t('runs.event.knowledge_challenged'), fact: (squash(d.proposition) || squash(d.reason)).slice(0, 90), status: 'error' }),
  'knowledge.corrected': (d, { t }) => ({ title: t('runs.event.knowledge_corrected'), fact: squash(d.reason).slice(0, 90) }),
  'knowledge.disproved': (d, { t }) => ({ title: t('runs.event.knowledge_disproved'), fact: squash(d.reason).slice(0, 90), status: 'error' }),
  'knowledge.invalidated': (d, { t }) => ({ title: t('runs.event.knowledge_invalidated'), fact: squash(d.reason).slice(0, 90), status: 'error' }),
  'knowledge.superseded': (d, { t }) => ({ title: t('runs.event.knowledge_superseded'), fact: squash(d.reason).slice(0, 90) }),
  'knowledge.promoted': (d, { t }) => ({ title: t('runs.event.knowledge_promoted'), fact: squash(d.proposition).slice(0, 90) }),
  'knowledge.linked': (d, { t }) => ({ title: t('runs.event.knowledge_linked'), fact: squash(d.relation) }),
  'knowledge.relation_confirmed': (d, { t }) => ({ title: t('runs.event.relation_confirmed'), fact: [squash(d.reason), squash(d.method)].filter(Boolean).join(' · '), status: 'ok' }),
  'knowledge.relation_dismissed': (d, { t }) => ({ title: t('runs.event.relation_dismissed'), fact: squash(d.reason) }),
  'knowledge.extraction.started': (d, { t }) => ({ title: t('runs.event.extraction_started') }),
  'knowledge.extraction.completed': (d, { t }) => ({ title: t('runs.event.extraction_completed'), fact: t('runs.extraction_candidates', { count: squash(d.candidates_count ?? 0) }), status: 'ok' }),
  'knowledge.extraction.failed': (d, { t }) => ({ title: t('runs.event.extraction_failed'), fact: causeOf(d), status: 'error' }),
  'hint.query': (d, { t }) => ({ title: t('runs.event.memory_lookup'), fact: t('runs.candidates', { count: squash(d.candidate_count ?? 0) }) }),
  'memory.read': (d, { t }) => ({
    title: t('runs.event.memory_read'),
    fact: t('runs.event.memory_read_detail', {
      hints: squash(d.hint_count ?? 0),
      primed: squash(d.primed_hints ?? 0),
    }),
    hasDetails: false,
  }),
  'hint.offered': (d, { t }) => ({ title: t('runs.event.hint_offered'), fact: squash(d.proposition).slice(0, 90) }),
  'hint.used': (d, { t }) => ({ title: t('runs.event.hint_used'), fact: squash(d.proposition).slice(0, 90) }),
  'hint.ignored': (d, { t }) => ({ title: t('runs.event.hint_ignored'), fact: squash(d.proposition).slice(0, 90) }),
  'hint.outcome': (d, { t }) => ({
    title: t('runs.event.hint_outcome'),
    fact: d.outcome === 'helpful' ? t('runs.event.hint_outcome_helpful')
      : d.outcome === 'harmful' ? t('runs.event.hint_outcome_harmful')
        : squash(d.outcome),
  }),
  // ── §5.7 artifacts ──
  'artifact.created': (d, { t }) => ({ title: t('runs.layers.artifact.created'), fact: [squash(d.artifact_id), versionOf(d)].filter(Boolean).join(' · ') }),
  'artifact.updated': (d, { t }) => ({ title: t('runs.layers.artifact.updated'), fact: [squash(d.artifact_id), versionOf(d)].filter(Boolean).join(' · ') }),
  'artifact.deleted': (d, { t }) => ({ title: t('runs.layers.artifact.deleted'), fact: squash(d.artifact_id) }),
  'artifact.authored': (d, { t }) => ({ title: t('runs.layers.artifact.authored'), fact: [squash(d.artifact_id), versionOf(d)].filter(Boolean).join(' · ') }),
  'artifact.read': (d, { t }) => ({ title: t('runs.event.artifact_read'), fact: [squash(d.title ?? d.artifact_id), bytesOf(d.bytes)].filter(Boolean).join(' · ') }),
  'artifact.proposed': (d, { t }) => ({ title: t('runs.layers.artifact.proposed'), fact: [squash(d.artifact_id), versionOf(d)].filter(Boolean).join(' · ') }),
  'artifact.submitted': (d, { t }) => ({ title: t('runs.layers.artifact.submitted'), fact: [squash(d.artifact_id), versionOf(d)].filter(Boolean).join(' · ') }),
  'artifact.review.requested': (d, { t }) => ({ title: t('runs.layers.artifact.review_requested'), fact: [squash(d.artifact_id), versionOf(d)].filter(Boolean).join(' · ') }),
  'artifact.review.approved': (d, { t }) => ({ title: t('runs.layers.artifact.review_approved'), fact: [squash(d.artifact_id), versionOf(d)].filter(Boolean).join(' · '), status: 'ok' }),
  'artifact.review.rejected': (d, { t }) => ({ title: t('runs.layers.artifact.review_rejected'), fact: squash(d.artifact_id), status: 'error' }),
  'artifact.review.reminded': (d, { t }) => ({ title: t('runs.layers.artifact.review_reminded', { hours: String(d.age_hours ?? 0) }), fact: [squash(d.artifact_id), versionOf(d)].filter(Boolean).join(' · ') }),
  'artifact.review.escalated': (d, { t }) => ({ title: t('runs.layers.artifact.review_escalated', { hours: String(d.age_hours ?? 0) }), fact: [squash(d.artifact_id), versionOf(d)].filter(Boolean).join(' · '), status: 'error' }),
  // ── §5.7 secrets — P-2: name/status/reason only, values never. The payload
  // never leaks into fact/title; the details layer stays closed for them. ──
  'secret.created': (d, { t }) => ({ title: t('runs.layers.secret.created', { name: secretName(d) }), fact: '', hasDetails: false }),
  'secret.updated': (d, { t }) => ({ title: t('runs.layers.secret.updated', { name: secretName(d) }), fact: '', hasDetails: false }),
  'secret.deleted': (d, { t }) => ({ title: t('runs.layers.secret.deleted', { name: secretName(d) }), fact: '', hasDetails: false }),
  'secret.value_set': (d, { t }) => ({ title: t('runs.layers.secret.without_value', { name: secretName(d) }), fact: '', hasDetails: false }),
  'secret.issued': (d, { t }) => ({ title: t('runs.layers.secret.issued', { name: secretName(d) }), fact: '', hasDetails: false }),
  'secret.denied': (d, { t }) => ({ title: t('runs.layers.secret.denied', { name: secretName(d) }), fact: denialReason(d), status: 'error', hasDetails: false }),
  // ── §5.7 mcp ──
  'mcp.call.started': (d) => ({ title: mcpTitle(d), fact: `→ ${squash(d.server)}`, status: 'running' }),
  'mcp.call.completed': (d) => ({ title: mcpTitle(d), fact: bytesOf(d.result_bytes), status: 'ok' }),
  'mcp.call.failed': (d) => ({ title: mcpTitle(d), fact: squash(d.error_type ?? 'failed'), status: 'error' }),
  'mcp.call.blocked': (d) => ({ title: mcpTitle(d), fact: squash(d.reason ?? d.error_type), status: 'error' }),
  // ── §5.7 triggers / intake ──
  'trigger.received': (d, { t }) => ({ title: t('runs.event.trigger_received'), fact: squash(d.trigger_name ?? d.source) }),
  'trigger.accepted': (d, { t }) => ({ title: t('runs.event.trigger_accepted'), fact: squash(d.trigger_name), status: 'ok' }),
  'trigger.rejected': (d) => ({ status: 'error', fact: squash(d.reason ?? d.error_type) }),
  'trigger.created': (d) => ({ fact: squash(d.trigger_name ?? d.id) }),
  'trigger.updated': (d) => ({ fact: squash(d.trigger_name ?? d.id) }),
  'trigger.deleted': (d) => ({ fact: squash(d.trigger_name ?? d.id) }),
  'intake.received': (d) => ({ fact: squash(d.channel ?? d.source) }),
  'intake.classified': (d) => ({ fact: squash(d.project ?? d.intent) }),
  'intake.routed': (d) => ({ fact: squash(d.agent ?? d.queue ?? d.route) }),
  'intake.rejected': (d) => ({ status: 'error', fact: squash(d.reason) }),
  'intake.cancelled': () => ({ status: 'cancelled' }),
  // ── §5.7 quality (evolution, gates, skills) ──
  'agent.definition.proposed': (d, { t }) => ({ title: t('runs.layers.quality.proposed'), fact: squash(d.agent_id) }),
  'agent.definition.applied': (d, { t }) => ({ title: t('runs.layers.quality.applied'), fact: squash(d.agent_id), status: 'ok' }),
  'agent.definition.rejected': (d, { t }) => ({ title: t('runs.layers.quality.rejected'), fact: squash(d.agent_id), status: 'error' }),
  'agent.evaluation.completed': (d, { t }) => ({
    title: t('runs.event.evaluation_completed'),
    fact: [
      squash(d.agent_name ?? d.agent_id),
      d.version != null ? `v${squash(d.version)}` : '',
      d.failed != null ? t('runs.event.evaluation_failed_n', { count: squash(d.failed) }) : '',
    ].filter(Boolean).join(' · '),
    status: Number(d.failed ?? 0) > 0 ? 'error' : 'ok',
  }),
  'agent.evaluation.gate.passed': (d, { t }) => ({ title: t('runs.event.eval_gate_passed'), fact: gateFact(d), status: 'ok' }),
  'agent.evaluation.gate.blocked': (d, { t }) => ({ title: t('runs.event.eval_gate_blocked'), fact: gateFact(d), status: 'error' }),
  'agent.evaluation.gate.overridden': (d, { t }) => ({
    title: t('runs.event.eval_gate_overridden'),
    fact: [gateFact(d), squash(d.override_justification).slice(0, 60)].filter(Boolean).join(' · '),
    status: 'error',
  }),
  'agent.evaluation.judge.calibrated': (d, { t }) => ({
    title: t('runs.event.judge_calibrated'),
    fact: [
      squash(d.agent_id),
      squash(d.judge_model),
      d.agreement_rate != null ? `${Math.round(Number(d.agreement_rate) * 100)}%` : '',
      d.degraded ? t('runs.event.judge_degraded') : '',
    ].filter(Boolean).join(' · '),
    status: d.degraded ? 'error' : 'ok',
  }),
  'skill.proposed': (d, { t }) => ({ title: t('runs.event.skill_proposed'), fact: [skillName(d), squash(d.change_summary).slice(0, 60)].filter(Boolean).join(' · ') }),
  'skill.applied': (d, { t }) => ({ title: t('runs.event.skill_applied'), fact: [skillName(d), squash(d.change_summary).slice(0, 60)].filter(Boolean).join(' · '), status: 'ok' }),
  'skill.rejected': (d, { t }) => ({ title: t('runs.layers.quality.skill_rejected'), fact: skillName(d), status: 'error' }),
  'skill.evaluation.completed': (d, { t }) => ({ title: t('runs.layers.quality.skill_evaluated'), fact: skillName(d), status: 'ok' }),
  'eval.case_candidate.created': (d, { t }) => ({ title: t('runs.event.eval_case_created'), fact: [squash(d.agent_id), squash(d.source)].filter(Boolean).join(' · ') }),
  'eval.case_candidate.decided': (d, { t }) => ({
    title: t('runs.event.eval_case_decided'),
    fact: [
      d.verdict === 'accepted' ? t('runs.event.eval_verdict_accepted')
        : d.verdict === 'rejected' ? t('runs.event.eval_verdict_rejected')
          : squash(d.verdict),
      squash(d.reviewed_by),
    ].filter(Boolean).join(' · '),
    status: d.verdict === 'rejected' ? 'error' : 'ok',
  }),
  // ── §5.7 sagas — one row per saga lifecycle step ──
  'saga.registered': (d, { t }) => ({ title: t('runs.event.saga_registered'), fact: squash(d.title).slice(0, 80) }),
  'saga.attached': (d, { t }) => ({ title: t('runs.event.saga_attached'), fact: [squash(d.task_id), squash(d.note).slice(0, 60)].filter(Boolean).join(' · ') }),
  'saga.renamed': (d, { t }) => ({ title: t('runs.event.saga_renamed'), fact: squash(d.title).slice(0, 80) }),
  'saga.merged': (d, { t }) => ({ title: t('runs.event.saga_merged'), fact: Array.isArray(d.source_saga_ids) ? `+${d.source_saga_ids.length}` : '', status: 'ok' }),
  'saga.detached': (d, { t }) => ({ title: t('runs.event.saga_detached'), fact: squash(d.task_id), status: 'none' }),
}

/** Semantic icon+color per catalog group (§4.1: category, not type). */
const CATEGORY_LOOKS: Record<EventCategory, CategoryLook> = {
  runs: { icon: '▶', color: 'var(--tm-teal)' },
  model: { icon: '🧠', color: '#bb9af7' },
  tools: { icon: '🔧', color: '#e0af68' },
  delegation: { icon: '⎇', color: '#7aa2f7' },
  teams: { icon: '👥', color: 'var(--tm-teal)' },
  approvals: { icon: '✋', color: '#e6b85c' },
  knowledge: { icon: '📚', color: '#9d7cd8' },
  artifacts: { icon: '📄', color: 'var(--tm-teal)' },
  secrets: { icon: '🔑', color: '#9ece6a' },
  mcp: { icon: '🔌', color: '#bb9af7' },
  triggers: { icon: '⚡', color: '#7aa2f7' },
  intake: { icon: '📥', color: '#7aa2f7' },
  quality: { icon: '🎓', color: '#7aa2f7' },
  sagas: { icon: '🧭', color: '#73daca' },
  ops: { icon: '⚙', color: 'var(--tm-text-3)' },
}

/** Public accessor: the semantic look of a category. Unknown categories fall
 *  back to the neutral dot. */
export function categoryLook(category: EventCategory): CategoryLook {
  return CATEGORY_LOOKS[category] ?? { icon: '•', color: 'var(--tm-text-3)' }
}

/** The default fact for catalog-typed events without an override: the first
 *  human-meaningful payload value. */
function defaultFactOf(d: Record<string, unknown>): string {
  return squash(d.proposition ?? d.title ?? d.reason ?? d.note ?? d.task_id ?? d.artifact_id ?? d.name)
}

/** The presentation class of a type (§5): streaming feed, ops noise, folded
 *  minor transition, or a normal row. */
function kindOf(type: string, category: EventCategory, known: boolean): PresentationKind {
  if (STREAM_TYPES.has(type)) return 'stream'
  // Only catalogued ops types are noise: an unknown type (no catalog meta)
  // stays a normal row — it must never vanish into the folded noise group.
  if (NOISE_TYPES.has(type) || (known && category === 'ops')) return 'noise'
  if (MINOR_TURN_RE.test(type) || MINOR_PLAN_RE.test(type)) return 'minor'
  return 'normal'
}

/** Timeline row of one event (§4.1). Unknown types derive a generic row from
 *  the catalog groups («Событие: …») — never a raw JSON dump, never the type
 *  name as the title (DoD-1). */
export function timelineRow(event: ObservationEvent, locale: Locale, t: TranslateFn): TimelineRow {
  const type = event.type
  const d = event.data ?? {}
  const meta = eventInfo(type)
  const category = (meta?.group ?? 'ops') as EventCategory
  const override = OVERRIDES[type]
  const overrideRow = override ? override(d, { t, locale }) : {}
  const look = categoryLook(category)
  const failure = failureOf(d)
  const status: TimelineRow['status'] = overrideRow.status ?? (failure ? 'error' : 'none')
  const kind = kindOf(type, category, Boolean(meta))
  const baseTitle = (overrideRow.title?.trim() || titleOf(meta, locale) || '').trim()
  const fact = squash(overrideRow.fact ?? defaultFactOf(d))
  // Unknown types (outside the catalog and the overrides): the generic
  // template carries the short human text in the title itself.
  const title = baseTitle || (fact ? t('runs.layers.generic', { text: fact.slice(0, 60) }) : t('runs.layers.generic', { text: '…' }))
  const row: TimelineRow = {
    type,
    category,
    icon: look.icon,
    color: look.color,
    title,
    fact: baseTitle ? fact : '',
    duration: durationOf(d) || turnsOf(d),
    status,
    kind,
    hasDetails: overrideRow.hasDetails ?? kind !== 'stream',
  }
  return capTitle(row)
}

/** First non-empty failure field of the payload, if any. */
function failureOf(d: Record<string, unknown>): string {
  for (const field of FAILURE_FIELDS) {
    const raw = d[field]
    if (raw !== undefined && raw !== null && String(raw).trim() !== '') return String(raw)
  }
  return ''
}

/** §4.1: the timeline text caps at ~120 chars. */
function capTitle(row: TimelineRow): TimelineRow {
  if (row.title.length <= 120) return row
  return { ...row, title: `${row.title.slice(0, 119)}…` }
}

/** A detail link the details layer renders: child runs, artifacts, knowledge
 *  dossiers — resolvable from the payload (§4.2). */
export interface DetailLink {
  kind: 'child_run' | 'artifact' | 'knowledge'
  label: string
  url: string
}

/** The event's operation id, when the payload carries one (console slot,
 *  reconcile marks). */
export function operationIdOf(event: { data?: Record<string, unknown> }): string {
  const opId = (event.data ?? {}).operation_id
  return typeof opId === 'string' ? opId : ''
}

/** Details-layer model (§4.2): human-form arguments, the result, the failure
 *  cause, links, the console slot and the raw envelope (§4.3). */
export interface EventDetails {
  /** What ran: tool name / model name / agent — the row's actor. */
  actor: string
  /** Human-form arguments, one line per field (tool calls, MCP calls). */
  argumentLines: Array<{ key: string; value: string }>
  /** The result in human form (tool output, verdict, answer preview). */
  result: string
  /** Failure cause in human form (Temporal envelopes already peeled). */
  failureCause: string
  /** The cause looks like a model/provider timeout. */
  timeout: boolean
  /** Links: child run, artifact registry, knowledge dossier. */
  links: DetailLink[]
  /** run_command console slot: the operation id addressing its stream —
   *  console://<operation_id> (§6). Empty — no console for this event. */
  consoleOperationId: string
  /** Streaming event? It feeds the live row / parent details instead. */
  stream: boolean
  /** This event's timeline row is error classed. */
  failed: boolean
}

/** Build the details-layer model of an event (§4.2). Pure: no fetches, no
 *  React — AgentRuns renders it (console data itself is T4's ConsoleOutput). */
export function eventDetails(event: ObservationEvent, t: TranslateFn): EventDetails {
  const d = event.data ?? {}
  const explained = explainKernelError(String(d.error ?? ''))
  const rawError = typeof d.error === 'string' && d.error.trim() ? d.error : ''
  const operation = d.operation as Record<string, unknown> | undefined
  const args = parseArguments(d.arguments) ?? parseArguments(operation?.arguments)
  const tool = typeof d.tool === 'string' && d.tool ? d.tool : (typeof operation?.tool === 'string' ? operation.tool : '')
  const argumentLines: Array<{ key: string; value: string }> = []
  if (args && tool === 'remember_many') {
    // Memory writes are the reader's «what did it decide to keep?» moment: a
    // raw JSON fragment of the items array says nothing — one line per
    // proposition instead (the kernel fragment carries up to ~500 chars, so
    // the propositions here are already a bounded preview).
    const items = Array.isArray(args.items) ? (args.items as unknown[]) : []
    items.forEach((item, index) => {
      const proposition = squash(item && typeof item === 'object' ? (item as Record<string, unknown>).proposition : item)
      if (proposition) argumentLines.push({ key: `#${index + 1}`, value: proposition.slice(0, 200) + (proposition.length > 200 ? '…' : '') })
    })
    if (argumentLines.length === 0) argumentLines.push({ key: 'items', value: t('runs.event.memory_empty') ?? '(no items)' })
  } else if (args && (tool === 'write_file' || tool === 'edit_file')) {
    // File writes: the path leads, the content fragment follows — the reader
    // wants WHAT was written, not a JSON envelope.
    const path = textOf(args.path)
    if (path) argumentLines.push({ key: 'path', value: path })
    const content = textOf(args.content ?? args.new_string ?? args.old_string)
    if (content) argumentLines.push({ key: 'content', value: content.slice(0, 400) + (content.length > 400 ? '…' : '') })
  } else if (args) {
    for (const [key, value] of Object.entries(args)) {
      const text = textOf(value)
      if (!text) continue
      argumentLines.push({ key, value: text.length > 300 ? `${text.slice(0, 299)}…` : text })
    }
  }
  const project = typeof event.context?.project === 'string' ? event.context.project : ''
  const links: DetailLink[] = []
  const child = typeof d.child_run_id === 'string' && d.child_run_id ? d.child_run_id : ''
  if (child) {
    links.push({ kind: 'child_run', label: child, url: `/agents?project=${encodeURIComponent(project)}&run=${encodeURIComponent(child)}` })
  }
  const artifact = textOf(d.artifact_id)
  if (artifact) {
    links.push({ kind: 'artifact', label: artifact, url: `/artifacts?project=${encodeURIComponent(project)}&id=${encodeURIComponent(artifact)}` })
  }
  const knowledgeID = textOf(d.knowledge_id)
  if (knowledgeID) {
    // Lead with the statement itself when the event carries it — a hash id
    // tells the reader nothing. The id stays in the dossier header.
    const statement = squash(d.proposition ?? d.reason).slice(0, 70)
    links.push({ kind: 'knowledge', label: statement || knowledgeID, url: `/knowledge?project=${encodeURIComponent(project)}&dossier=${encodeURIComponent(knowledgeID)}` })
  }
  const exitCode = d.exit_code
  const exitFailed = typeof exitCode === 'number' && exitCode !== 0
  const resultValue = d.output ?? d.result ?? d.answer ?? d.response
  return {
    actor: tool || squash(d.model) || squash(d.agent_id) || '',
    argumentLines,
    result: (typeof resultValue === 'string' ? resultValue.trim() : textOf(resultValue)).slice(0, 400),
    failureCause: rawError ? explained.cause.slice(0, 200) : '',
    timeout: rawError ? explained.timeout : false,
    links,
    consoleOperationId: tool === 'run_command' && operationIdOf(event) ? operationIdOf(event) : '',
    stream: STREAM_TYPES.has(event.type),
    failed: rawError !== '' || exitFailed,
  }
}

/** The raw layer payload (§4.3): the event envelope — the ONLY layer allowed
 *  to show technical data (K-1). P-2: secret events expose the name only,
 *  never the payload. */
export function rawEnvelope(event: { type: string; data?: Record<string, unknown>; event_id?: string; occurred_at?: string }): string {
  if (event.type.startsWith('secret.')) {
    return JSON.stringify({
      type: event.type,
      event_id: event.event_id ?? '',
      occurred_at: event.occurred_at ?? '',
      // P-2 (DoD-2): the data of a secret event never renders.
      data: { name: secretName(event.data ?? {}) },
    }, null, 2)
  }
  return JSON.stringify(event, null, 2)
}

/** Test seam: the declarative tables the coverage test walks (DoD-1). */
export const PRESENTATION_TABLES = { OVERRIDES, STREAM_TYPES, NOISE_TYPES, MINOR_TURN_RE, MINOR_PLAN_RE } as const

/** One live detached command for the pinned trace panel
 *  (docs/agent-async-operations.md): its operation id (the console:// address)
 *  and the human command line. */
export interface BackgroundOp {
  operationId: string
  command: string
}

/** Running background run_command operations of one run (AgentRuns trace
 *  panel): the live console mounts only on a tool.started row, and the
 *  bounded trace window sheds that row while the model keeps working — the
 *  stream would vanish for the reader though the command still runs. This
 *  scan walks the FULL timeline and drops:
 *  - operations already completed (completedOps, over the full timeline —
 *    the completed row carries the output); the started row folds away;
 *  - operations whose tool.started row is still inside the render window
 *    (windowEvents — the inline console already shows them; no duplicate
 *    subscription surface);
 *  - everything that is not a detached run_command (no operation id, other
 *    tool, background flag not true, foreign run).
 *  Pure, unit-tested: the panel itself stays a thin renderer. */
export function runningBackgroundOps(events: ObservationEvent[], windowEvents: ObservationEvent[], completedOps: Set<string>, selectedRun: string): BackgroundOp[] {
  const windowOps = new Set<string>()
  for (const event of windowEvents) {
    if (event.type !== 'tool.started') continue
    const id = operationIdOf(event)
    if (id) windowOps.add(id)
  }
  const ops: BackgroundOp[] = []
  const seen = new Set<string>()
  for (const event of events) {
    if (event.type !== 'tool.started') continue
    if ((event.context?.run ?? '') !== selectedRun) continue
    const opId = operationIdOf(event)
    if (!opId || seen.has(opId) || windowOps.has(opId)) continue
    if (completedOps.has(operationKeyOf(event, opId))) continue
    const data = event.data ?? {}
    if (data.tool !== 'run_command') continue
    const args = parseArguments(data.arguments)
    if (!args || args.background !== true) continue
    seen.add(opId)
    const command = Array.isArray(args.command) ? (args.command as unknown[]).map(String).join(' ') : ''
    ops.push({ operationId: opId, command })
  }
  return ops
}

/** operationKey for an operation id that is already known — matches
 *  traceModel.operationKey (run-agnostic: the id alone carries the identity;
 *  the registry is keyed by (project, operation_id)). Importing traceModel
 *  here would cycle, so the composition stays local. */
function operationKeyOf(event: ObservationEvent, operationId: string): string {
  return operationId
}
