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

/** Format an event into a human-readable summary line. */
export function eventSummary(event: ObservationEvent, t: TranslateFn): EventSummaryInfo {
  const d = event.data ?? {}
  switch (event.type) {
    case 'run.started':
      return { icon: '▶', label: t('runs.event.run_started'), detail: d.model ? t('runs.model', { name: String(d.model) }) : '', color: 'var(--tm-teal)' }
    case 'run.completed':
      return { icon: '✓', label: t('runs.event.run_completed'), detail: `${d.turns ?? '?'} ${t('runs.turns')}`, color: '#9ece6a' }
    case 'run.failed':
      return { icon: '✗', label: t('runs.event.run_failed'), detail: explainKernelError(String(d.error ?? '')).cause.slice(0, 80), color: '#f7768e' }
    case 'turn.started':
      return { icon: '→', label: t('runs.turn', { turn: String(d.turn ?? '?') }), detail: t('runs.event.started'), color: 'var(--tm-text-3)' }
    case 'turn.completed':
      return { icon: '←', label: t('runs.turn', { turn: String(d.turn ?? '?') }), detail: d.tool_calls ? t('runs.tool_calls_n', { count: String(d.tool_calls) }) : t('runs.event.completed'), color: 'var(--tm-text-3)' }
    case 'model.started':
      return { icon: '⏳', label: t('runs.event.model_call'), detail: String(d.model ?? t('runs.event.started')), color: 'var(--tm-text-3)' }
    case 'model.completed': {
      const tokens = d.total_tokens ? `${d.total_tokens} tok` : ''
      const latency = d.latency_ms ? `${(Number(d.latency_ms) / 1000).toFixed(1)}s` : ''
      const calls = d.tool_call_count ? `${d.tool_call_count} tools` : ''
      const parts = [tokens, latency, calls].filter(Boolean).join(' · ')
      return { icon: '🧠', label: t('runs.event.model_response'), detail: parts, color: '#bb9af7' }
    }
    case 'model.failed': {
      const info = explainKernelError(String(d.error ?? ''))
      const suffix = info.timeout ? ` · ${t('errors.timeout_short')}` : ''
      return { icon: '🧠', label: t('runs.event.model_failed'), detail: (info.cause.slice(0, 60) + suffix).trim(), color: '#f7768e' }
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
          else preview = args.slice(0, 60)
        } catch { preview = String(args).slice(0, 60) }
      }
      return { icon: '🔧', label: String(d.tool ?? 'tool'), detail: preview || t('runs.event.started'), color: '#e0af68' }
    }
    case 'tool.completed': {
      const exit = d.exit_code !== undefined ? `exit ${d.exit_code}` : ''
      const ms = d.latency_ms ? `${(Number(d.latency_ms) / 1000).toFixed(1)}s` : ''
      const output = typeof d.output === 'string' ? d.output.slice(0, 80).replace(/\n/g, ' ') : ''
      return { icon: d.exit_code === 0 ? '✓' : '✗', label: String(d.tool ?? 'tool'), detail: [exit, ms, output].filter(Boolean).join(' · '), color: d.exit_code === 0 ? '#9ece6a' : '#f7768e' }
    }
    case 'tool.failed': {
      // A failed delegate tool is the parent's view of a crashed child run —
      // say so instead of a bare tool name; the child link lives in the detail.
      if (d.error_type === 'delegated_run_failed') {
        const child = typeof d.child_run_id === 'string' ? d.child_run_id.split('/').pop() ?? '' : ''
        return { icon: '↗', label: t('runs.event.delegation_failed'), detail: child ? `→ ${child}` : '', color: '#f7768e' }
      }
      const errDetail = d.error ? explainKernelError(String(d.error)).cause.slice(0, 60) : 'failed'
      const args = d.arguments
      let preview = ''
      if (typeof args === 'string') {
        try {
          const parsed = JSON.parse(args)
          if (Array.isArray(parsed.command)) preview = parsed.command.join(' ')
          else if (parsed.path) preview = parsed.path
        } catch { /* ignore */ }
      }
      return { icon: '✗', label: String(d.tool ?? 'tool'), detail: preview ? `${preview} → ${errDetail}` : errDetail, color: '#f7768e' }
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
    case 'hint.query':
      return { icon: '🔍', label: t('runs.event.memory_lookup'), detail: t('runs.candidates', { count: String(d.candidate_count ?? 0) }), color: 'var(--tm-text-3)' }
    case 'memory.read':
      return { icon: '📖', label: t('runs.event.memory_read'), detail: t('runs.hints_loaded', { count: String(d.hint_count ?? 0) }), color: 'var(--tm-text-3)' }
    case 'mcp.call.started':
      return { icon: '🔌', label: String(d.tool ?? 'MCP'), detail: `→ ${d.server ?? ''}`, color: '#bb9af7' }
    case 'mcp.call.completed':
      return { icon: '🔌', label: String(d.tool ?? 'MCP'), detail: t('runs.event.completed'), color: '#9ece6a' }
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
    case 'agent.summary':
      return { icon: '📋', label: t('runs.event.summary'), detail: String(d.kind ?? ''), color: 'var(--tm-teal)' }
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
    case 'delegation.started':
      return { icon: '↗', label: t('runs.event.delegation'), detail: `→ ${d.child_run_id ?? '?'}`, color: '#7aa2f7' }
    case 'delegation.completed':
      return { icon: '↗', label: t('runs.event.delegation_completed'), detail: d.turns != null ? `${d.turns} ${t('runs.turns')}` : '', color: '#9ece6a' }
    case 'delegation.failed': {
      const info = explainKernelError(String(d.error ?? ''))
      const suffix = info.timeout ? ` · ${t('errors.timeout_short')}` : ''
      return { icon: '↗', label: t('runs.event.delegation_failed'), detail: (info.cause.slice(0, 80) + suffix).trim(), color: '#f7768e' }
    }
    default:
      return { icon: '•', label: event.type, detail: '', color: 'var(--tm-text-3)' }
  }
}
