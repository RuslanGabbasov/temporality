import type { ObservationEvent } from './observationApi'

export type TranslateFn = (key: string, vars?: Record<string, string>) => string

export interface EventSummaryInfo {
  icon: string
  label: string
  detail: string
  color: string
}

export interface KernelErrorInfo {
  /** Activity/workflow types of the peeled envelopes, outermost first
   * (e.g. ['AgentRun', 'kernel.call_model']). */
  chain: string[]
  /** Deepest human-readable message — the actual cause. */
  cause: string
  /** The cause looks like a model/provider timeout. */
  timeout: boolean
}

const TEMPORAL_ENVELOPE = /^(?:child workflow execution|activity) error \(type: ([A-Za-z0-9_.]+),[^)]*\): (.+)$/s

/** Peel Temporal error envelopes ("child workflow execution error (type: X, …): …",
 * "activity error (type: Y, …): …") so the UI can show the actual cause instead
 * of workflow plumbing. */
export function explainKernelError(raw: string): KernelErrorInfo {
  let rest = (raw ?? '').trim()
  const chain: string[] = []
  for (;;) {
    const match = TEMPORAL_ENVELOPE.exec(rest)
    if (!match) break
    chain.push(match[1])
    rest = match[2].trim()
  }
  const timeout = /context deadline exceeded|Client\.Timeout|context cancellation/i.test(rest)
  return { chain, cause: rest || (raw ?? '').trim(), timeout }
}

export function shortTime(iso: string) {
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? '' : d.toLocaleTimeString()
}

/** Compact human duration for budget tiles: 2h 5m / 4m 30s / 12s / 900ms. */
export function formatDuration(ms: number): string {
	if (!Number.isFinite(ms) || ms <= 0) return '0s'
	if (ms < 1000) return `${Math.round(ms)}ms`
	const totalSeconds = Math.floor(ms / 1000)
	const h = Math.floor(totalSeconds / 3600)
	const m = Math.floor((totalSeconds % 3600) / 60)
	const s = totalSeconds % 60
	if (h > 0) return m > 0 ? `${h}h ${m}m` : `${h}h`
	if (m > 0) return s > 0 ? `${m}m ${s}s` : `${m}m`
	return `${s}s`
}

/** Human-approval budget tooltip: the full breakdown behind a compact cell.
 * Structural on purpose — works for ApprovalStats from any rollup shape.
 * human (persistent HAB/HWT, docs/supervisor-model.md §5) appends the
 * attention/wait line when the rollup carries it. */
export function approvalHint(t: TranslateFn, ap: { requested: number; granted: number; rejected: number; timed_out: number; pending?: number; wait_ms: number }, human?: { decisions: number; answers: number; hab_seconds: number; hwt_seconds: number; flow_efficiency: number }): string {
	const base = t('runs.approvals_hint', {
		requested: String(ap.requested),
		granted: String(ap.granted),
		rejected: String(ap.rejected),
		timed_out: String(ap.timed_out),
		pending: String(ap.pending ?? 0),
		wait: formatDuration(ap.wait_ms),
	})
	if (!human || (human.hab_seconds <= 0 && human.hwt_seconds <= 0)) return base
	return base + '\n' + t('runs.human_hint', {
		hab: formatDuration(human.hab_seconds * 1000),
		hwt: formatDuration(human.hwt_seconds * 1000),
		flow: String(human.flow_efficiency),
	})
}

/** MCP tool results arrive as JSON-encoded strings, sometimes doubly so
 * (a JSON string wrapping a JSON string). Unwrap the string layers for
 * display: `"{\"connected\":true}"` → `{"connected":true}`. Plain JSON
 * objects and non-JSON text are returned unchanged. */
export function unwrapJsonString(value: string): string {
  let out = value
  for (let depth = 0; depth < 3 && out.length > 1 && out.startsWith('"') && out.endsWith('"'); depth++) {
    try {
      const parsed: unknown = JSON.parse(out)
      if (typeof parsed === 'string') {
        out = parsed
        continue
      }
      return JSON.stringify(parsed)
    } catch {
      break
    }
  }
  return out
}

/* Historical agent.summary events carry newline-collapsed answers: the
 * journal's redaction pipeline used to join all whitespace into single
 * spaces, flattening markdown to one line. The journal is append-only, so
 * old events keep that shape forever. This heuristic conservatively
 * re-inserts line breaks before block-level markdown tokens — but only for
 * texts that have no line structure at all. */
const HAS_BLOCK_STRUCTURE = /\| \||(?:^|\s)#{1,6} |(?:^|\s)(?:[-*+]|\d{1,3}[.)])\s\S|(?:^|\s)---+/

/** Restore plausible markdown line structure in a newline-collapsed text.
 * Texts that already contain newlines are returned unchanged. */
export function restoreMarkdownLines(text: string): string {
  if (!text || text.includes('\n')) return text
  if (!HAS_BLOCK_STRUCTURE.test(text)) return text
  let out = text
    // Table row boundary: "| |" between rows (cells inside a row stay glued)
    .replace(/\| \|/g, '|\n|')
    // ATX headings: "## Отчёт" starts a new line
    .replace(/\s(#{1,6})\s/g, '\n$1 ')
    // Horizontal rule: a standalone run of 3+ dashes
    .replace(/\s(---+)(?=\s|$)/g, '\n$1')
    // List items: "- ", "* ", "+ ", "1. ", "1) " followed by content.
    // The preceding character must be real prose — not a heading marker,
    // which keeps "### 1. Title" in one piece.
    .replace(/([^#\s])\s(?=(?:[-*+]|\d{1,3}[.)])\s\S)/g, '$1\n')
  // A header row glued to preceding prose ("### 1. Что сделано | Шаг | …")
  // would swallow the whole table: GFM needs the header row on its own line
  // directly above the delimiter row. If the next line is a delimiter row,
  // move any pipe-free prefix to its own line.
  const lines = out.split('\n')
  for (let i = 0; i < lines.length - 1; i++) {
    const next = lines[i + 1].trim()
    if (!/^\|(?:[-: ]*\|)+$/.test(next) || !next.includes('-')) continue
    const at = lines[i].indexOf(' | ')
    if (at > 0 && !lines[i].slice(0, at).includes('|')) {
      lines[i] = `${lines[i].slice(0, at)}\n${lines[i].slice(at + 1)}`
    }
  }
  return lines.join('\n')
}

/** Strip matcher prefixes ("term:foo" → "foo") from matched_by markers. */
function cleanMatchTerms(matched: unknown): string[] {
  const list = Array.isArray(matched) ? matched : typeof matched === 'string' && matched ? [matched] : []
  return list.map((m) => String(m).replace(/^(term|entity|topic):/, '')).filter(Boolean)
}

/** Compact form of an id that no resolver could turn into text: a bare guid
 * says nothing, the first 8 chars are enough to eyeball-match other rows. */
function shortId(id: unknown): string {
  const s = String(id ?? '')
  return s.length <= 16 ? s : `${s.slice(0, 8)}…`
}

/** Human byte size for MCP result markers: 512 B / 1.2 KB / 3.4 MB. */
function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return ''
  if (bytes < 1024) return `${Math.round(bytes)} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}

/** The team runtime executes the compiled program as a pseudo tool call named
 * "team", and DAG execution reports the "plan" tool — those two are product
 * concepts, not real tools, so they get localized labels. Real tool ids
 * (run_command, skill_*, mcp__server__tool) stay verbatim on purpose. */
export function pseudoToolLabel(tool: unknown, t: TranslateFn): string {
  const name = typeof tool === 'string' && tool ? tool : 'tool'
  if (name === 'team') return t('runs.event.team')
  if (name === 'plan') return t('runs.event.plan')
  return name
}

/** Gate events share one payload shape (agent, proposal, cause, run); the
 * agent answers "whose gate", the proposal and run tie the row to the apply
 * it decided. Long uuids collapse to a short handle. */
function evalGateDetail(d: Record<string, unknown>): string {
  const run = d.evaluation_run_id != null && String(d.evaluation_run_id) !== '' ? shortId(d.evaluation_run_id) : ''
  return [String(d.agent_id ?? ''), d.proposal_id != null ? `#${String(d.proposal_id)}` : '', String(d.cause ?? ''), run]
    .filter(Boolean)
    .join(' · ')
}

/** Format an event into a human-readable summary line. `propositionOf` resolves
 * knowledge ids to their text for events recorded before propositions were
 * embedded (historical runs still carry bare ids). */
export function eventSummary(event: ObservationEvent, t: TranslateFn, propositionOf?: (knowledgeID: string) => string | undefined): EventSummaryInfo {
  const d = event.data ?? {}
  // Prefer the proposition embedded in the event; fall back to resolving the
  // knowledge id; bare ids are the last resort for unresolvable history.
  const knowledgeText = () => {
    const embedded = String(d.proposition ?? '')
    if (embedded) return embedded
    const id = String(d.knowledge_id ?? '')
    return id ? (propositionOf?.(id) ?? id) : ''
  }
  switch (event.type) {
    case 'run.started':
      return { icon: '▶', label: t('runs.event.run_started'), detail: d.model ? t('runs.model', { name: String(d.model) }) : '', color: 'var(--tm-teal)' }
    case 'run.completed':
      return { icon: '✓', label: t('runs.event.run_completed'), detail: `${d.turns ?? '?'} ${t('runs.turns')}`, color: '#9ece6a' }
    case 'run.failed':
      return { icon: '✗', label: t('runs.event.run_failed'), detail: explainKernelError(String(d.error ?? '')).cause.slice(0, 80), color: '#f7768e' }
    case 'run.user_note':
      // Mid-run chat addendum drained into the conversation (kernel.user_note).
      return { icon: '⤳', label: t('runs.event.user_note'), detail: t('runs.event.user_note_detail', { turn: String(d.turn ?? '?') }), color: 'var(--tm-teal)' }
    case 'run.note_dropped':
      return { icon: '⚠', label: t('runs.event.note_dropped'), detail: t('runs.event.note_dropped_detail'), color: '#e0af68' }
    case 'turn.started':
      return { icon: '→', label: t('runs.turn', { turn: String(d.turn ?? '?') }), detail: t('runs.event.started'), color: 'var(--tm-text-3)' }
    case 'turn.completed':
      return { icon: '←', label: t('runs.turn', { turn: String(d.turn ?? '?') }), detail: d.tool_calls ? t('runs.tool_calls_n', { count: String(d.tool_calls) }) : t('runs.event.completed'), color: 'var(--tm-text-3)' }
    case 'model.started':
      return { icon: '⏳', label: t('runs.event.model_call'), detail: String(d.model ?? t('runs.event.started')), color: 'var(--tm-text-3)' }
    case 'model.completed': {
      const tokens = d.total_tokens ? `${d.total_tokens} tok` : ''
      const cached = d.cached_tokens ? `↻ ${d.cached_tokens}` : ''
      const latency = d.latency_ms ? `${(Number(d.latency_ms) / 1000).toFixed(1)}s` : ''
      const calls = d.tool_call_count ? `${d.tool_call_count} tools` : ''
      // The stream died mid-flight and the blocking fallback regenerated the
      // answer — the latency already includes both generations.
      const regen = d.stream_failed ? `↻ ${t('runs.event.stream_regen')}` : ''
      const parts = [tokens, cached, latency, calls, regen].filter(Boolean).join(' · ')
      return { icon: '🧠', label: t('runs.event.model_response'), detail: parts, color: '#bb9af7' }
    }
    case 'model.failed': {
      const info = explainKernelError(String(d.error ?? ''))
      const suffix = info.timeout ? ` · ${t('errors.timeout_short')}` : ''
      return { icon: '🧠', label: t('runs.event.model_failed'), detail: (info.cause.slice(0, 60) + suffix).trim(), color: '#f7768e' }
    }
    case 'model.nudged':
      // Manual unstick: a human cancelled a hanging provider call and the
      // kernel retried the turn (see kernel.nudge / POST .../nudge).
      return { icon: '↻', label: t('runs.event.model_nudged'), detail: t('runs.event.model_nudged_detail', { turn: String(d.turn ?? '?') }), color: 'var(--tm-teal)' }
    case 'model.attempt': {
      // Internal recovery marker journaled by the activity itself, outside the
      // deterministic workflow sequence: either Temporal retried a dead model
      // call (activity_retry), the stream died mid-flight and the blocking
      // API salvaged the turn (stream_fallback), or the first-token watchdog
      // caught a queueing provider and failed the attempt so the Temporal
      // retry opens a fresh connection (stream_timeout). Without it a hanging
      // provider looks like a silent multi-minute gap inside one
      // model.started…completed.
      const reasonKey = d.reason === 'stream_fallback'
        ? 'runs.event.model_attempt_fallback'
        : d.reason === 'stream_timeout'
          ? 'runs.event.model_attempt_timeout'
          : 'runs.event.model_attempt_retry'
      const attempt = t('runs.event.model_attempt_n', { n: String(d.attempt ?? '?') })
      const ms = d.stream_ms ? `${(Number(d.stream_ms) / 1000).toFixed(1)}s` : ''
      const err = d.error ? String(d.error).slice(0, 60) : ''
      return { icon: '↻', label: t('runs.event.model_attempt'), detail: [t(reasonKey), attempt, ms, err].filter(Boolean).join(' · '), color: '#e0af68' }
    }
    case 'tool.started': {
      const args = d.arguments
      let preview = ''
      if (typeof args === 'string') {
        try {
          const parsed = JSON.parse(args)
          if (Array.isArray(parsed.command)) preview = parsed.command.join(' ')
          else if (parsed.path) preview = parsed.path
          else if (parsed.query) preview = String(parsed.query).slice(0, 60)
          else if (parsed.proposition) preview = String(parsed.proposition).slice(0, 60)
          // delegate: who got the subtask and its gist — the prompt hash says
          // nothing about intent.
          else if (parsed.agent_id) preview = `→ ${parsed.agent_id}${parsed.prompt ? ` · ${String(parsed.prompt).replace(/\s+/g, ' ').slice(0, 60)}` : ''}`
          else preview = args.slice(0, 60)
        } catch { preview = String(args).slice(0, 60) }
      }
      return { icon: '🔧', label: pseudoToolLabel(d.tool, t), detail: preview || t('runs.event.started'), color: '#e0af68' }
    }
    case 'tool.completed': {
      // tool.completed is only emitted when the call itself succeeded; only
      // run_command reports an exit code, and a non-zero one means the command
      // exited with an error. MCP and kernel tools (skill_*, mcp__*) carry no
      // exit code — their failures land in tool.failed instead — so a missing
      // exit code is a success, not an error.
      const failed = d.exit_code !== undefined && d.exit_code !== 0
      const exit = d.exit_code !== undefined ? `exit ${d.exit_code}` : ''
      const ms = d.latency_ms ? `${(Number(d.latency_ms) / 1000).toFixed(1)}s` : ''
      const output = typeof d.output === 'string' ? unwrapJsonString(d.output).slice(0, 80).replace(/\n/g, ' ') : ''
      return { icon: failed ? '✗' : '✓', label: pseudoToolLabel(d.tool, t), detail: [exit, ms, output].filter(Boolean).join(' · '), color: failed ? '#f7768e' : '#9ece6a' }
    }
    case 'tool.failed': {
      // A failed delegate tool is the parent's view of a crashed child run —
      // say so instead of a bare tool name; the child link lives in the detail.
      if (d.error_type === 'delegated_run_failed') {
        const child = typeof d.child_run_id === 'string' ? d.child_run_id.split('/').pop() ?? '' : ''
        return { icon: '↗', label: t('runs.event.delegation_failed'), detail: child ? `→ ${child}` : '', color: '#f7768e' }
      }
      // A rejected plan never started anything — surface the validation detail.
      if (d.tool === 'plan' && (d.error_type === 'plan_invalid' || d.error_type === 'agent_resolution_failed' || d.error_type === 'delegation_depth_exceeded')) {
        return { icon: '⎇', label: t('runs.event.plan'), detail: String(d.detail ?? d.error_type ?? '').slice(0, 80), color: '#f7768e' }
      }
      const errDetail = d.error ? explainKernelError(String(d.error)).cause.slice(0, 60) : 'failed'
      const args = d.arguments
      let preview = ''
      if (typeof args === 'string') {
        try {
          const parsed = JSON.parse(args)
          if (Array.isArray(parsed.command)) preview = parsed.command.join(' ')
          else if (parsed.path) preview = parsed.path
          else if (parsed.agent_id) preview = `→ ${parsed.agent_id}`
        } catch { /* ignore */ }
      }
      return { icon: '✗', label: pseudoToolLabel(d.tool, t), detail: preview ? `${preview} → ${errDetail}` : errDetail, color: '#f7768e' }
    }
    case 'knowledge.proposed':
      return { icon: '💡', label: t('runs.event.learned'), detail: String(d.proposition ?? '').slice(0, 60), color: '#73daca' }
    case 'knowledge.extraction.started':
      return { icon: '🧪', label: t('runs.event.extraction_started'), detail: String(d.extractor_version ?? ''), color: '#73daca' }
    case 'knowledge.extraction.completed': {
      const skipped = d.skipped ? ` · ${t('runs.extraction_skipped')}` : ''
      return { icon: '🧪', label: t('runs.event.extraction_completed'), detail: t('runs.extraction_candidates', { count: String(d.candidates_count ?? 0) }) + skipped, color: '#73daca' }
    }
    case 'knowledge.extraction.failed':
      return { icon: '⚠', label: t('runs.event.extraction_failed'), detail: String(d.error ?? '').slice(0, 80), color: '#e6b85c' }
    case 'knowledge.recalled':
      return { icon: '📚', label: t('runs.event.recalled'), detail: String(d.proposition ?? '').slice(0, 60), color: '#9d7cd8' }
    case 'knowledge.used':
      return { icon: '📚', label: t('runs.event.knowledge_used'), detail: knowledgeText().slice(0, 90), color: '#9d7cd8' }
    case 'knowledge.confirmed':
      return { icon: '✓', label: t('runs.event.knowledge_confirmed'), detail: knowledgeText().slice(0, 90), color: '#73daca' }
    case 'knowledge.challenged': {
      // Challenged events carry the reason the item is doubted; it matters more
      // than a bare id when no proposition/resolver text is available.
      const text = String(d.proposition ?? '') || propositionOf?.(String(d.knowledge_id ?? '')) || String(d.reason ?? '')
      return { icon: '⚠', label: t('runs.event.knowledge_challenged'), detail: text.slice(0, 90), color: '#e6b85c' }
    }
    case 'knowledge.corrected':
    case 'knowledge.superseded': {
      // Terminal lifecycle events carry ids only; resolve both sides to
      // propositions so the row reads as a rule, not as two guids. The
      // replacement is the part the reader usually wants (what to use now).
      // Unlike knowledgeText(), no bare-id fallback — a guid gets shortId'd.
      const resolveText = (id: unknown) => {
        const s = String(id ?? '')
        if (s && s === String(d.knowledge_id ?? '') && d.proposition) return String(d.proposition)
        return s ? propositionOf?.(s) ?? '' : ''
      }
      const replacementId = String(d.replacement_id ?? '')
      const replacement = resolveText(d.replacement_id).slice(0, 60) || (replacementId ? shortId(replacementId) : '')
      const self = resolveText(d.knowledge_id).slice(0, 60) || shortId(d.knowledge_id)
      return {
        icon: event.type === 'knowledge.corrected' ? '✎' : '⇄',
        label: event.type === 'knowledge.corrected' ? t('runs.event.knowledge_corrected') : t('runs.event.knowledge_superseded'),
        detail: [self, replacement ? `→ ${replacement}` : ''].filter(Boolean).join(' '),
        color: '#e6b85c',
      }
    }
    case 'knowledge.promoted': {
      const scope = d.scope_kind === 'organization' ? t('runs.event.scope_organization') : String(d.scope_id ?? d.scope_kind ?? '')
      const self = (String(d.proposition ?? '') || (d.knowledge_id != null ? propositionOf?.(String(d.knowledge_id)) ?? '' : '')).slice(0, 60) || shortId(d.knowledge_id)
      return { icon: '⤴', label: t('runs.event.knowledge_promoted'), detail: [self, scope ? `→ ${scope}` : ''].filter(Boolean).join(' '), color: '#9d7cd8' }
    }
    case 'knowledge.invalidated':
      return { icon: '✗', label: t('runs.event.knowledge_invalidated'), detail: String(d.reason ?? shortId(d.knowledge_id)).slice(0, 80), color: '#f7768e' }
    case 'knowledge.disproved':
      return { icon: '✗', label: t('runs.event.knowledge_disproved'), detail: String(d.reason ?? '').slice(0, 80), color: '#f7768e' }
    case 'knowledge.linked': {
      // Resolve both endpoints through the resolver: a relation between two
      // readable rules beats a relation between two guids. Each side is capped
      // so one long proposition cannot swallow the other.
      const from = (propositionOf?.(String(d.knowledge_id ?? '')) ?? '').slice(0, 40) || shortId(d.knowledge_id)
      const to = (propositionOf?.(String(d.target_id ?? '')) ?? '').slice(0, 40) || shortId(d.target_id)
      return { icon: '🔗', label: t('runs.event.knowledge_linked'), detail: [String(d.relation ?? ''), `${from} → ${to}`].filter(Boolean).join(' · '), color: 'var(--tm-text-3)' }
    }
    case 'hint.offered': {
      const proposition = String(d.proposition ?? '') || propositionOf?.(String(d.knowledge_id ?? '')) || ''
      const terms = cleanMatchTerms(d.matched_by)
      const detail = proposition
        ? proposition.slice(0, 90)
        : terms.length ? `${t('runs.matched_by')}: ${terms.join(' · ')}` : ''
      return { icon: '💡', label: t('runs.event.hint_offered'), detail, color: '#9d7cd8' }
    }
    case 'hint.used':
      return { icon: '✓', label: t('runs.event.hint_used'), detail: knowledgeText().slice(0, 90), color: 'var(--tm-text-3)' }
    case 'hint.ignored':
      return { icon: '○', label: t('runs.event.hint_ignored'), detail: knowledgeText().slice(0, 90), color: 'var(--tm-text-3)' }
    case 'hint.outcome': {
      // The outcome alone says "something was judged"; the proposition says
      // what was judged — without it the row is uninterpretable.
      const outcome = d.outcome === 'helpful' ? t('runs.event.hint_outcome_helpful') : d.outcome === 'harmful' ? t('runs.event.hint_outcome_harmful') : String(d.outcome ?? '')
      const proposition = propositionOf?.(String(d.knowledge_id ?? '')) ?? ''
      return { icon: '⌾', label: t('runs.event.hint_outcome'), detail: [outcome, proposition ? proposition.slice(0, 60) : ''].filter(Boolean).join(' · '), color: 'var(--tm-text-3)' }
    }
    case 'skill.proposed':
      return { icon: '🎯', label: t('runs.event.skill_proposed'), detail: [d.version ? `${d.skill_name ?? d.skill_id ?? ''} v${d.version}` : String(d.skill_name ?? d.skill_id ?? ''), String(d.change_summary ?? '').slice(0, 60)].filter(Boolean).join(' · '), color: '#7aa2f7' }
    case 'skill.applied':
      return { icon: '🎯', label: t('runs.event.skill_applied'), detail: [d.version ? `${d.skill_name ?? d.skill_id ?? ''} v${d.version}` : String(d.skill_name ?? d.skill_id ?? ''), String(d.change_summary ?? '').slice(0, 60)].filter(Boolean).join(' · '), color: '#9ece6a' }
    case 'hint.query':
      return { icon: '🔍', label: t('runs.event.memory_lookup'), detail: t('runs.candidates', { count: String(d.candidate_count ?? 0) }), color: 'var(--tm-text-3)' }
    case 'memory.read':
      return { icon: '📖', label: t('runs.event.memory_read'), detail: t('runs.hints_loaded', { count: String(d.hint_count ?? 0) }), color: 'var(--tm-text-3)' }
    case 'mcp.call.started':
      return { icon: '🔌', label: String(d.tool ?? 'MCP'), detail: `→ ${d.server ?? ''}`, color: '#bb9af7' }
    case 'mcp.call.completed': {
      // The result is content-addressed (result_ref) with only its size in the
      // event; the byte count tells the reader whether the answer is a one-liner
      // or a payload worth opening in the operation log.
      const bytes = d.result_bytes != null ? formatBytes(Number(d.result_bytes)) : ''
      return { icon: '🔌', label: String(d.tool ?? 'MCP'), detail: [bytes, t('runs.event.completed')].filter(Boolean).join(' · '), color: '#9ece6a' }
    }
    case 'mcp.call.failed':
      return { icon: '🔌', label: String(d.tool ?? 'MCP'), detail: String(d.error_type ?? 'failed'), color: '#f7768e' }
    case 'approval.requested': {
      const op = d.operation as Record<string, unknown> | undefined
      return { icon: '⚠', label: t('runs.event.approval_needed'), detail: String(d.action ?? op?.tool ?? ''), color: '#e6b85c' }
    }
    case 'approval.granted':
      return { icon: '✓', label: t('runs.event.approved'), detail: d.approver ? `by ${String(d.approver)}` : '', color: '#9ece6a' }
    case 'approval.auto_granted': {
      const op2 = d.operation as Record<string, unknown> | undefined
      return { icon: '✓', label: t('runs.event.auto_approved'), detail: String(op2?.tool ?? d.policy_id ?? ''), color: '#9ece6a' }
    }
    case 'tool.blocked':
      return { icon: '🚫', label: t('runs.event.blocked'), detail: String(d.reason ?? ''), color: '#f7768e' }
    case 'agent.summary': {
      // The answer preview is the row's whole value; a bare "narrative" kind
      // tag carries no information.
      const answer = String(d.answer ?? '').replace(/\s+/g, ' ').trim()
      return { icon: '📋', label: t('runs.event.summary'), detail: answer ? answer.slice(0, 80) : String(d.kind ?? ''), color: 'var(--tm-teal)' }
    }
    case 'approval.rejected':
      return { icon: '✗', label: t('runs.event.rejected'), detail: String(d.reason ?? ''), color: '#f7768e' }
    case 'human.requested':
      return { icon: '❓', label: t('runs.event.human_requested'), detail: String(d.question ?? ''), color: '#e6b85c' }
    case 'human.answered':
      return { icon: '✓', label: t('runs.event.human_answered'), detail: String(d.response ?? ''), color: '#9ece6a' }
    case 'human.timed_out':
      return { icon: '⏱', label: t('runs.event.human_timed_out'), detail: '', color: '#f7768e' }
    case 'human.cancelled':
      return { icon: '✗', label: t('runs.event.human_cancelled'), detail: String(d.reason ?? ''), color: '#f7768e' }
    case 'trigger.received':
      return { icon: '⚡', label: t('runs.event.trigger_received'), detail: String(d.trigger_name ?? d.source ?? ''), color: '#7aa2f7' }
    case 'trigger.accepted':
      return { icon: '⚡', label: t('runs.event.trigger_accepted'), detail: String(d.trigger_name ?? ''), color: '#7aa2f7' }
    case 'notification.sent':
      return { icon: '🔔', label: t('runs.event.notification_sent'), detail: `${d.channel ?? 'web'} · ${d.status ?? ''}${d.recipient ? ' · ' + d.recipient : ''}`, color: '#bb9af7' }
    case 'delegation.started': {
      // The raw child_run_id is a long path; the agent id answers "who was
      // spawned", the last two path segments answer "which delegation".
      const child = typeof d.child_run_id === 'string' ? d.child_run_id.split('/').slice(-2).join('/') : ''
      return { icon: '↗', label: t('runs.event.delegation'), detail: ['→ ' + String(d.agent_id ?? '?'), child].filter(Boolean).join(' · '), color: '#7aa2f7' }
    }
    case 'delegation.completed': {
      // agent_id ties the completion to its started row; child_status matters
      // only when it differs from the happy path.
      const status = String(d.child_status ?? '')
      const parts = [String(d.agent_id ?? ''), d.turns != null ? `${d.turns} ${t('runs.turns')}` : '', status && status !== 'completed' ? status : ''].filter(Boolean)
      return { icon: '↗', label: t('runs.event.delegation_completed'), detail: parts.join(' · '), color: '#9ece6a' }
    }
    case 'delegation.failed': {
      const info = explainKernelError(String(d.error ?? ''))
      const suffix = info.timeout ? ` · ${t('errors.timeout_short')}` : ''
      return { icon: '↗', label: t('runs.event.delegation_failed'), detail: (info.cause.slice(0, 80) + suffix).trim(), color: '#f7768e' }
    }
    case 'team.started': {
      const name = String(d.team_name ?? d.team_id ?? '')
      const parts = [name, String(d.goal ?? '').slice(0, 60), d.version ? `v${d.version}` : ''].filter(Boolean)
      return { icon: '👥', label: t('runs.event.team_started'), detail: parts.join(' · '), color: 'var(--tm-teal)' }
    }
    case 'slot.bound': {
      const binding = d.slot_id != null || d.agent_id != null ? `${d.slot_id ?? '?'} → ${d.agent_id ?? '?'}` : ''
      const mode = d.mode === 'matched' ? t('runs.event.slot_mode_matched') : d.mode === 'fixed' ? t('runs.event.slot_mode_fixed') : ''
      return { icon: '⚓', label: t('runs.event.slot_bound'), detail: [binding, mode].filter(Boolean).join(' · '), color: '#7aa2f7' }
    }
    case 'team.completed': {
      const tokens = d.total_tokens ? `${d.total_tokens} tok` : ''
      const cost = d.cost_usd ? `$${Number(d.cost_usd).toFixed(4)}` : ''
      const rework = d.rework_rounds ? t('runs.event.team_rework_rounds', { count: String(d.rework_rounds) }) : ''
      return { icon: '👥', label: t('runs.event.team_completed'), detail: [tokens, cost, rework].filter(Boolean).join(' · '), color: '#9ece6a' }
    }
    case 'team.failed': {
      const info = explainKernelError(String(d.error ?? ''))
      const child = typeof d.child_run_id === 'string' ? d.child_run_id.split('/').pop() ?? '' : ''
      const parts = [String(d.step ?? ''), info.cause.slice(0, 60), child ? `→ ${child}` : ''].filter(Boolean)
      return { icon: '👥', label: t('runs.event.team_failed'), detail: parts.join(' · '), color: '#f7768e' }
    }
    case 'plan.started': {
      const tasks = Array.isArray(d.tasks) ? d.tasks.length : '?'
      return { icon: '⎇', label: t('runs.event.plan_started'), detail: [String(d.goal ?? ''), `${tasks} ${t('runs.event.plan_tasks')}`].filter(Boolean).join(' · '), color: '#7aa2f7' }
    }
    case 'plan.task.started': {
      const round = Number(d.round ?? 1)
      const parts = [String(d.task_id ?? ''), String(d.agent_id ?? ''), round > 1 ? `×${round}` : '', Array.isArray(d.review_of) && d.review_of.length ? `⌾ ${d.review_of.join(', ')}` : ''].filter(Boolean)
      return { icon: '⎇', label: t('runs.event.plan_task_started'), detail: parts.join(' · '), color: '#7aa2f7' }
    }
    case 'plan.task.completed': {
      const verdict = String(d.verdict ?? '')
      const round = Number(d.round ?? 1)
      const parts = [String(d.task_id ?? ''), round > 1 ? `×${round}` : '', verdict === 'accept' ? '✓' : verdict === 'rework' ? '↺' : '', d.turns != null ? `${d.turns} ${t('runs.turns')}` : ''].filter(Boolean)
      return { icon: '⎇', label: t('runs.event.plan_task_completed'), detail: parts.join(' · '), color: '#9ece6a' }
    }
    case 'plan.task.failed': {
      const info = explainKernelError(String(d.error ?? ''))
      const round = Number(d.round ?? 0)
      const parts = [String(d.task_id ?? ''), d.error_type === 'rework_exhausted' && round ? t('runs.event.rework_exhausted', { round: String(round) }) : '', info.cause.slice(0, 60)].filter(Boolean)
      return { icon: '⎇', label: t('runs.event.plan_task_failed'), detail: parts.join(' · '), color: '#f7768e' }
    }
    case 'plan.task.incomplete': {
      const round = Number(d.round ?? 1)
      const status = String(d.child_status ?? '')
      const cause = status === 'turn_limit' || status === 'time_limit' || status === 'token_limit' || status === 'budget_limit'
        ? t(`chat.plan.child_status.${status}`)
        : status
      const parts = [String(d.task_id ?? ''), round > 1 ? `×${round}` : '', cause].filter(Boolean)
      return { icon: '⎇', label: t('runs.event.plan_task_incomplete'), detail: parts.join(' · '), color: '#ff9e64' }
    }
    case 'plan.task.rejected':
      return { icon: '↺', label: t('runs.event.plan_task_rejected'), detail: [String(d.task_id ?? ''), String(d.rejected_by ?? ''), String(d.feedback ?? '').slice(0, 80)].filter(Boolean).join(' · '), color: '#e6b85c' }
    case 'plan.task.reopened':
      return { icon: '↺', label: t('runs.event.plan_task_reopened'), detail: [String(d.task_id ?? ''), d.round != null ? `×${Number(d.round) + 1}` : ''].filter(Boolean).join(' · '), color: '#7aa2f7' }
    case 'plan.task.invalidated':
      return { icon: '⎇', label: t('runs.event.plan_task_invalidated'), detail: [String(d.task_id ?? ''), d.reason === 'upstream_rework_exhausted' ? t('chat.plan.upstream_rework_exhausted') : t('chat.plan.upstream_rework')].filter(Boolean).join(' · '), color: 'var(--tm-text-3)' }
    case 'plan.task.skipped': {
      const reason = d.reason === 'run_time_limit' ? t('chat.plan.run_time_limit') : d.reason === 'execution_budget' ? t('chat.plan.execution_budget') : d.reason === 'upstream_incomplete' ? t('chat.plan.upstream_incomplete') : t('chat.plan.upstream_failed')
      return { icon: '⎇', label: t('runs.event.plan_task_skipped'), detail: [String(d.task_id ?? ''), reason, d.blocked_by ? `← ${d.blocked_by}` : ''].filter(Boolean).join(' · '), color: 'var(--tm-text-3)' }
    }
    case 'plan.completed': {
      const statuses = (d.statuses ?? {}) as Record<string, unknown>
      const counts = new Map<string, number>()
      for (const value of Object.values(statuses)) counts.set(String(value), (counts.get(String(value)) ?? 0) + 1)
      const detail = ['completed', 'incomplete', 'failed', 'skipped', 'invalidated']
        .map((status) => counts.get(status) ? `${counts.get(status)} ${t(`chat.plan.status_${status}`)}` : '')
        .filter(Boolean).join(' · ')
      return { icon: '⎇', label: t('runs.event.plan_completed'), detail, color: '#9ece6a' }
    }
    case 'secret.issued':
      return { icon: '🔑', label: t('runs.event.secret_issued'), detail: String(d.secret ?? ''), color: '#9ece6a' }
    case 'secret.denied':
      return { icon: '🚫', label: t('runs.event.secret_denied'), detail: [String(d.secret ?? ''), String(d.reason ?? '')].filter(Boolean).join(' · '), color: '#f7768e' }
    case 'artifact.read': {
      // The doc title plus the chunk size tell the reader which reference
      // document informed the run and how much of it the agent consumed.
      const bytes = Number(d.bytes ?? 0)
      const size = bytes >= 1024 ? `${(bytes / 1024).toFixed(1)} KiB` : `${bytes} B`
      return { icon: '📄', label: t('runs.event.artifact_read'), detail: [String(d.title ?? ''), String(d.artifact_id ?? ''), size].filter(Boolean).join(' · '), color: 'var(--tm-teal)' }
    }
    case 'saga.registered': {
      // The title is what the reader recognises; the generated saga id only
      // matters as a short handle for saga_attach.
      return { icon: '🧭', label: t('runs.event.saga_registered'), detail: [String(d.title ?? ''), shortId(d.saga_id)].filter(Boolean).join(' · '), color: 'var(--tm-teal)' }
    }
    case 'saga.attached': {
      // The task answers "which run joined the saga"; the note (optional)
      // says why the binding moved.
      return { icon: '🧭', label: t('runs.event.saga_attached'), detail: [String(d.task_id ?? ''), String(d.note ?? '').slice(0, 60)].filter(Boolean).join(' · '), color: 'var(--tm-teal)' }
    }
    case 'saga.renamed': {
      // The new title is the row; the old one lives in the event history.
      return { icon: '🧭', label: t('runs.event.saga_renamed'), detail: [String(d.title ?? ''), String(d.goal ?? '').slice(0, 60)].filter(Boolean).join(' · '), color: 'var(--tm-teal)' }
    }
    case 'saga.merged': {
      // Sources fold into the target: name the target and how many came in.
      const sources = Array.isArray(d.source_saga_ids) ? d.source_saga_ids.length : 0
      return { icon: '🧭', label: t('runs.event.saga_merged'), detail: [shortId(d.saga_id), sources > 0 ? `+${sources}` : ''].filter(Boolean).join(' · '), color: 'var(--tm-teal)' }
    }
    case 'saga.detached': {
      // Back to the unbound pool: name the task that left its saga.
      return { icon: '🧭', label: t('runs.event.saga_detached'), detail: String(d.task_id ?? ''), color: 'var(--tm-text-3)' }
    }
    case 'operation.reconciled': {
      // The effect is the whole point of the row: did the unconfirmed tool
      // call actually change the outside world or not.
      const effect = d.effect === 'occurred' ? t('runs.event.reconcile_effect_occurred')
        : d.effect === 'none' ? t('runs.event.reconcile_effect_none')
          : d.effect === 'unknown' ? t('runs.event.reconcile_effect_unknown')
            : String(d.effect ?? '')
      return { icon: '⚖', label: t('runs.event.operation_reconciled'), detail: [effect, String(d.note ?? '').slice(0, 60)].filter(Boolean).join(' · '), color: 'var(--tm-text-3)' }
    }
    case 'hitl.verdict.recorded': {
      // Verdict types come from workspace.Verdict: accepted | corrected | rejected.
      const kind = String(d.type ?? '')
      const verdict = kind === 'accepted' ? t('runs.event.hitl_verdict_accepted')
        : kind === 'corrected' ? t('runs.event.hitl_verdict_corrected')
          : kind === 'rejected' ? t('runs.event.hitl_verdict_rejected')
            : kind
      const color = kind === 'rejected' ? '#f7768e' : kind === 'corrected' ? '#e6b85c' : '#9ece6a'
      return { icon: '⚖', label: t('runs.event.hitl_verdict'), detail: [verdict, String(d.answered_by ?? '')].filter(Boolean).join(' · '), color }
    }
    case 'agent.evaluation.completed': {
      // passed/failed counts are the verdict; the agent and version tie the
      // row to the evolution proposal it will gate.
      const who = String(d.agent_name ?? d.agent_id ?? '')
      const passed = d.passed != null ? t('runs.event.evaluation_passed_n', { count: String(d.passed) }) : ''
      const failed = d.failed != null ? t('runs.event.evaluation_failed_n', { count: String(d.failed) }) : ''
      const version = d.version != null ? `v${d.version}` : ''
      return { icon: '🎓', label: t('runs.event.evaluation_completed'), detail: [who, version, passed, failed].filter(Boolean).join(' · '), color: Number(d.failed ?? 0) > 0 ? '#f7768e' : '#9ece6a' }
    }
    case 'agent.evaluation.gate.passed':
      return { icon: '🚦', label: t('runs.event.eval_gate_passed'), detail: evalGateDetail(d), color: '#9ece6a' }
    case 'agent.evaluation.gate.blocked':
      return { icon: '🚦', label: t('runs.event.eval_gate_blocked'), detail: evalGateDetail(d), color: '#f7768e' }
    case 'agent.evaluation.gate.overridden': {
      // The justification is the audit trail of a promoted-red gate — the
      // one thing a reviewer wants to read on this row.
      const justification = String(d.override_justification ?? '').slice(0, 60)
      return { icon: '🚦', label: t('runs.event.eval_gate_overridden'), detail: [evalGateDetail(d), justification].filter(Boolean).join(' · '), color: '#e6b85c' }
    }
    case 'agent.evaluation.gate.skipped':
      return { icon: '🚦', label: t('runs.event.eval_gate_skipped'), detail: evalGateDetail(d), color: 'var(--tm-text-3)' }
    case 'agent.evaluation.judge.configured': {
      const model = String(d.model ?? '')
      const state = d.configured ? t('runs.event.judge_enabled') : t('runs.event.judge_disabled')
      return { icon: '⚖', label: t('runs.event.judge_configured'), detail: [model, state].filter(Boolean).join(' · '), color: '#7aa2f7' }
    }
    case 'agent.evaluation.judge.calibrated': {
      // Agreement between the judge and human verdicts; degraded means the
      // floor was missed and the judge cannot gate applies.
      const agreement = d.agreement_rate != null ? `${Math.round(Number(d.agreement_rate) * 100)}%` : ''
      const degraded = d.degraded ? t('runs.event.judge_degraded') : ''
      return { icon: '⚖', label: t('runs.event.judge_calibrated'), detail: [String(d.agent_id ?? ''), String(d.judge_model ?? ''), agreement, degraded].filter(Boolean).join(' · '), color: d.degraded ? '#e6b85c' : '#9ece6a' }
    }
    case 'agent.evaluation.schedule.changed':
      return { icon: '📅', label: t('runs.event.quality_schedule_changed'), detail: d.interval_hours != null ? t('runs.event.interval_hours', { count: String(d.interval_hours) }) : '', color: '#7aa2f7' }
    case 'eval.case_candidate.created': {
      const redaction = d.redaction_applied ? t('runs.event.redaction_applied') : ''
      return { icon: '🧪', label: t('runs.event.eval_case_created'), detail: [String(d.agent_id ?? ''), String(d.source ?? ''), redaction].filter(Boolean).join(' · '), color: '#73daca' }
    }
    case 'eval.case_candidate.decided': {
      const verdict = d.verdict === 'accepted' ? t('runs.event.eval_verdict_accepted')
        : d.verdict === 'rejected' ? t('runs.event.eval_verdict_rejected')
          : String(d.verdict ?? '')
      return { icon: '🧪', label: t('runs.event.eval_case_decided'), detail: [verdict, String(d.reviewed_by ?? '')].filter(Boolean).join(' · '), color: d.verdict === 'rejected' ? '#f7768e' : '#9ece6a' }
    }
    default:
      return { icon: '•', label: event.type, detail: '', color: 'var(--tm-text-3)' }
  }
}
