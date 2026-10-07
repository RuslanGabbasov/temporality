import type { ObservationEvent } from './observationApi'

/** The Experience Timeline folds the immutable observation event stream into
 * two layers over one time axis:
 *
 *  - trajectory: what the agent actually did (runs, model calls, tool calls);
 *  - experience: how memory items appeared, were recalled/injected/reused,
 *    validated, contradicted and archived across runs.
 *
 * Everything here is a pure function of the event list, so the view stays
 * derived data — the event stream remains the only source of truth. */

export type LifecycleKind =
  | 'appeared'
  | 'recalled'
  | 'injected'
  | 'reused'
  | 'validated'
  | 'contradicted'
  | 'weakened'
  | 'archived'

export const LIFECYCLE_KINDS: LifecycleKind[] = ['appeared', 'recalled', 'injected', 'reused', 'validated', 'contradicted', 'weakened', 'archived']

/** One memory-lifecycle moment on the time axis. */
export interface ExperiencePoint {
  at: string
  kind: LifecycleKind
  /** The observation event type the point was derived from. */
  origin: string
  knowledgeId: string
  run?: string
  role?: string
  actor?: string
  eventId: string
  hintId?: string
  rule?: string
}

/** A concrete execution or observation backing a cluster member. */
export interface EpisodeRef {
  knowledgeId: string
  run: string
  role?: string
  ref: string
  kind: string
  at: string
}

/** One approved operation executed inside a run (approval.* payload). */
export interface RunCommand {
  command: string
  policy?: string
  at: string
  eventId: string
}

/** A weakening or terminal transition with its cause: who/what/why. */
export interface DeathRecord {
  kind: LifecycleKind
  at: string
  run?: string
  role?: string
  actor?: string
  reason?: string
  eventId: string
}

export interface ClusterMember {
  knowledgeId: string
  proposition: string
  state: string
  firstAt: string
}

/** Primary visual object after Experiment 5: one knowledge item as a lane row
 * with its lifespan (appeared → terminal), lifecycle points and scopes. */
export interface KnowledgeRow {
  knowledgeId: string
  proposition: string
  state: string
  firstAt: string
  lastAt: string
  /** First archived point — the death of the experience, if it died. */
  terminal?: { kind: LifecycleKind; at: string }
  scopes: { primary: string; secondary: string[] }
  command?: string
  commandClass?: string
  policy?: string
  points: ExperiencePoint[]
  episodes: EpisodeRef[]
  deaths: DeathRecord[]
  relatedIds: string[]
  runs: string[]
  roles: string[]
  strength: number
}

/** Semantic top-level grouping: Scope → Knowledge, orthogonal to time. */
export interface ScopeLane {
  id: string
  title: string
  kind: 'execution' | 'claim'
  rows: KnowledgeRow[]
  runs: string[]
  roles: string[]
  firstAt: string
  lastAt: string
}

/** A semantic group of related knowledge items (one verification command
 * family, one recurring claim type). The primary visual object. */
export interface ExperienceCluster {
  id: string
  title: string
  scope: string
  kind: 'execution' | 'claim'
  command?: string
  commands: string[]
  members: ClusterMember[]
  points: ExperiencePoint[]
  episodes: EpisodeRef[]
  runs: string[]
  roles: string[]
  firstAt: string
  lastAt: string
  state: string
  /** Derived 0..1 strength: lifecycle state + reuse telemetry. Not a model output. */
  strength: number
}

export interface RunInfo {
  id: string
  role?: string
  parentRun?: string
  startedAt: string
  endedAt?: string
  status?: string
  /** Run initiator (context.actor): a user id, trigger id or agent id. */
  actor?: string
  /** Generated human title attached to run.started (runtitle.go). */
  title?: string
  agentId?: string
  toolCalls: number
  modelCalls: number
  knowledgeEvents: number
  commands: RunCommand[]
}

/** A delegation chain: the root run plus every run it spawned (directly or
 * transitively). The collapsed unit of the timeline run section — one lane
 * per chain, expandable to per-run lanes. */
export interface RunChain {
  root: RunInfo
  runs: RunInfo[]
  /** Deepest failure inside the chain, if any run failed. */
  failed: boolean
  endedAt?: string
  lastAt: string
}

/** Group runs into delegation chains by parent_run_id. Runs whose parent is
 * missing from the stream stay attached to their own root (the parent events
 * may live outside the loaded window). */
export function buildRunChains(runs: RunInfo[]): { chains: RunChain[]; rootOf: Map<string, string> } {
  const byId = new Map(runs.map((run) => [run.id, run]))
  const rootOf = new Map<string, string>()
  const resolveRoot = (run: RunInfo): string => {
    const cached = rootOf.get(run.id)
    if (cached !== undefined) return cached
    // Guard against parent cycles: mark before descending.
    rootOf.set(run.id, run.id)
    const parent = run.parentRun ? byId.get(run.parentRun) : undefined
    const root = parent ? resolveRoot(parent) : run.id
    rootOf.set(run.id, root)
    return root
  }
  const byRoot = new Map<string, RunInfo[]>()
  for (const run of runs) {
    const root = resolveRoot(run)
    const list = byRoot.get(root) ?? []
    list.push(run)
    byRoot.set(root, list)
  }
  const chains: RunChain[] = []
  for (const [rootId, members] of byRoot) {
    const root = byId.get(rootId)!
    const sorted = [...members].sort((a, b) => (a.startedAt < b.startedAt ? -1 : a.startedAt > b.startedAt ? 1 : a.id < b.id ? -1 : 1))
    let lastAt = root.startedAt
    let endedAt: string | undefined
    let failed = false
    for (const run of sorted) {
      if (!lastAt || run.startedAt > lastAt) lastAt = run.startedAt
      const end = run.endedAt ?? run.startedAt
      if (!endedAt || end > endedAt) endedAt = end
      if (run.status === 'failed') failed = true
    }
    chains.push({ root, runs: sorted, failed, endedAt: endedAt ?? root.startedAt, lastAt })
  }
  chains.sort((a, b) => (a.root.startedAt < b.root.startedAt ? 1 : a.root.startedAt > b.root.startedAt ? -1 : a.root.id < b.root.id ? 1 : -1))
  return { chains, rootOf }
}

/** Cross-agent disagreement: one agent proposed knowledge, a different agent
 * (different role) later contradicted, weakened or archived it. This is the
 * "where agents misled each other" signal for the run timeline. */
export interface MisleadSignal {
  knowledgeId: string
  proposition: string
  kind: LifecycleKind
  at: string
  /** Run where the contradiction landed. */
  run?: string
  proposedBy?: string
  challengedBy?: string
  reason?: string
}

export function detectMisleads(rows: KnowledgeRow[]): MisleadSignal[] {
  const out: MisleadSignal[] = []
  for (const row of rows) {
    const appeared = row.points.find((point) => point.kind === 'appeared')
    const proposedBy = appeared?.role ?? ''
    if (!proposedBy) continue
    for (const death of row.deaths) {
      const challengedBy = death.role ?? ''
      // Self-correction inside one agent's own work is normal verification,
      // not a dispute.
      if (!challengedBy || challengedBy === proposedBy) continue
      out.push({
        knowledgeId: row.knowledgeId,
        proposition: row.proposition,
        kind: death.kind,
        at: death.at,
        run: death.run,
        proposedBy,
        challengedBy,
        reason: death.reason,
      })
    }
  }
  return out.sort((a, b) => (a.at < b.at ? 1 : a.at > b.at ? -1 : 0))
}

/** recall → injection pairing: which run pulled which memory item when. */
export interface ActivationLink {
  hintId: string
  knowledgeId: string
  clusterId: string
  offeredAt: string
  offeredRun?: string
  usedAt?: string
  usedRun?: string
  /** Final status of the run that consumed the hint (joined post-fold):
   * distinguishes “injected → reused” from “injected → run.failed”. */
  usedRunStatus?: string
}

/** Old knowledge → replacing knowledge edge. Explicit when carried by a
 * corrected/superseded event (replacement_id); inferred when an invalidation
 * reason references another knowledge item of the same project. */
export interface KnowledgeLineage {
  fromId: string
  toId: string
  reason?: string
  eventId: string
  at: string
  inferred: boolean
}

/** Forensic reconstruction (phase plan §20): one knowledge item's causal
 * chain built from the same folded stream — formation evidence, activations
 * and death. Pure derived data, no extra state. */
export interface ForensicCommand { command: string; policy?: string; at: string; eventId: string }
export interface ForensicEvidence {
  ref: string
  type: string
  /** When and in which run the evidence was captured — lets the UI focus the
   * timeline on the producing operation and attribute it to a run/role. */
  at: string
  run?: string
  role?: string
}
export interface ForensicFormed {
  at: string
  run?: string
  role?: string
  actor?: string
  evidence: ForensicEvidence[]
  commands: ForensicCommand[]
}
export interface ForensicActivation {
  offeredAt: string
  offeredRun?: string
  usedAt?: string
  usedRun?: string
  usedRunStatus?: string
  commands: ForensicCommand[]
}
export interface ForensicDeath extends DeathRecord { supersededBy: string[] }
export interface ForensicRecord {
  formed?: ForensicFormed
  activations: ForensicActivation[]
  deaths: ForensicDeath[]
}

export interface ExperienceModel {
  runs: RunInfo[]
  clusters: ExperienceCluster[]
  rows: KnowledgeRow[]
  scopes: ScopeLane[]
  links: ActivationLink[]
  lineage: KnowledgeLineage[]
  bounds: { from: string; to: string }
  totals: { events: number; knowledge: number }
}

export function lifecycleKindOf(event: ObservationEvent): LifecycleKind | undefined {
  switch (event.type) {
    case 'knowledge.proposed':
      return 'appeared'
    case 'knowledge.confirmed':
      return 'validated'
    case 'knowledge.challenged':
    case 'knowledge.disproved':
      return 'contradicted'
    case 'knowledge.corrected':
      return 'weakened'
    case 'knowledge.invalidated':
    case 'knowledge.superseded':
      return 'archived'
    case 'knowledge.used':
      return stringOf(event.data?.hint_id) ? 'injected' : 'reused'
    case 'hint.offered':
      return 'recalled'
    default:
      return undefined
  }
}

/** Canonical form used for clustering: flag tokens are dropped so
 * `go test ./...` and `go test -count=1 -v ./...` share one experience. */
export function canonicalCommand(command: string): string {
  const tokens = command.split(/\s+/).filter((token) => token && !token.startsWith('-'))
  return tokens.join(' ')
}

export function scopeOfCommand(command: string): string {
  const tokens = command.split(/\s+/).filter(Boolean)
  const head = tokens[0] ?? ''
  const sub = tokens[1] ?? ''
  if (head === 'go') {
    if (sub === 'test') return 'test'
    if (sub === 'vet') return 'vet'
    if (sub === 'build') return 'build'
    if (sub === 'fmt') return 'fmt'
    return sub || 'go'
  }
  if (head === 'gofmt') return 'fmt'
  return head || 'exec'
}

// ——— Experience scope derivation ———
// Visual grouping is deliberately NOT the retrieval matcher (lexical claim
// clustering over-merges: see docs/experiment5-long-horizon.md). A claim
// lands in `auth` because the agent actually operated `go run . auth` in
// this project — the mechanism vocabulary is built from subcommand-position
// tokens of executed commands, never from proposition overlap.

const SHELL_BINARIES = new Set(['mkdir', 'rm', 'cp', 'mv', 'cat', 'ls', 'echo', 'cd', 'touch', 'chmod', 'chown', 'curl', 'wget', 'grep', 'sed', 'awk', 'head', 'tail', 'sort', 'uniq', 'find', 'xargs', 'tee', 'env', 'export', 'sudo', 'pwd', 'which', 'whoami', 'date', 'sleep', 'true', 'false', 'printf', 'less', 'more', 'vim', 'nano', 'git', 'docker', 'python', 'python3', 'pip', 'pip3', 'node', 'npm', 'npx', 'yarn', 'pnpm', 'cargo', 'rustc', 'make', 'cmake', 'java', 'gradle', 'mvn', 'dotnet', 'sh', 'bash', 'zsh', 'set', 'command', 'type', 'source', 'alias', 'trap', 'wait', 'read', 'test', 'exit', 'unset', 'return', 'shift', 'break', 'continue'])
const COMMAND_WRAPPERS = new Set(['env', 'sudo', 'nohup', 'time', 'timeout', 'exec'])
const SHELL_OPERATORS = /^[;&|>]+$/ // any run of operator chars: &&, ||, ;, |, &, >, >&
const WORD_TOKEN = /^[a-zA-Z][a-zA-Z0-9_-]{1,}$/
const ENV_ASSIGNMENT = /^[A-Za-z_][A-Za-z0-9_]*=/
// `env -u SIGN_KEY SIGNING_KEY …` leaves bare variable names in argv; they
// are environment names, never mechanisms, and would otherwise leak into the
// vocabulary (and from there into every claim quoting them).
const ENV_NAME = /^[A-Z][A-Z0-9_]{1,}$/

/** Split a tokenized command on shell operators into simple segments. */
export function commandSegments(tokens: string[]): string[][] {
  const segments: string[][] = []
  let current: string[] = []
  for (const token of tokens) {
    if (SHELL_OPERATORS.test(token)) {
      if (current.length) segments.push(current)
      current = []
    } else current.push(token)
  }
  if (current.length) segments.push(current)
  return segments
}

/** The project mechanism a command segment operates on: the subcommand of
 * an interpreter/project binary (`go run . auth` → auth, `nb report` →
 * report, `go test` → test). Plain shell utilities own no mechanism. */
export function segmentSubcommand(tokens: string[]): string | undefined {
  const meaningful = tokens.filter((token) => !ENV_ASSIGNMENT.test(token) && !ENV_NAME.test(token) && !COMMAND_WRAPPERS.has(token) && !/^\d+$/.test(token) && !/^-\D/.test(token))
  if (!meaningful.length) return undefined
  const head = meaningful[0]
  // `go` is a toolchain head, not a mechanism: it only carries meaning in
  // head position (`go build`, `go run . auth`) and is noise in the tail of
  // broken argv like ["head;", "go", "build"] where the head failed to parse.
  const wordTail = () => meaningful.slice(1).find((token) => WORD_TOKEN.test(token) && token !== 'go' && !SHELL_BINARIES.has(token) && !COMMAND_WRAPPERS.has(token))?.toLowerCase()
  if (head === 'go') {
    if (meaningful[1] === 'run') return meaningful.slice(2).find((token) => WORD_TOKEN.test(token) && !SHELL_BINARIES.has(token))?.toLowerCase()
    return WORD_TOKEN.test(meaningful[1] ?? '') ? meaningful[1].toLowerCase() : undefined
  }
  if (head.startsWith('./') || head.startsWith('.') || head.startsWith('/') || !WORD_TOKEN.test(head)) return wordTail()
  if (SHELL_BINARIES.has(head)) return undefined
  return wordTail()
}

/** Mechanisms a proposition talks about, matched against the project's
 * executed-command vocabulary. Backtick code spans are the authoritative
 * signal — prose like "In this relay CLI build" must not read as the `build`
 * mechanism — with a whole-text fallback for claims that never quote a
 * command (e.g. nightly K82 mentions `nb auth` only in prose). */
export function propositionMechanisms(proposition: string, vocabulary: Set<string>): string[] {
  if (!vocabulary.size || !proposition) return []
  const hit = (text: string) => {
    const tokens = new Set(text.toLowerCase().split(/[^a-z0-9_-]+/i).filter(Boolean))
    return [...vocabulary].filter((mechanism) => tokens.has(mechanism)).sort()
  }
  const spans = [...proposition.matchAll(/`([^`]*)`/g)].map((match) => match[1])
  if (spans.length) {
    const quoted = hit(spans.join(' '))
    if (quoted.length) return quoted
  }
  return hit(proposition)
}

/** True when argv invokes a shell with `-c` and a single script argument. */
function shellScriptArgv(tokens: string[]): string | undefined {
  if (tokens.length < 3) return undefined
  if (!['sh', 'bash', 'zsh', 'dash', 'ash'].includes(tokens[0])) return undefined
  return tokens.slice(1, -1).includes('-c') ? tokens[tokens.length - 1] : undefined
}

/** Primary/secondary scope of a claim. Beyond the mechanism set, this uses
 * two signals the flat list cannot express: PIPELINE INTENT (an explicit step
 * enumeration or the words “end to end” mark a claim that genuinely spans
 * mechanisms, however many it happens to quote) and SUBJECT FREQUENCY — a
 * thorough claim enumerates related behaviours (“related v1 behaviors: sign
 * requires…, publish requires…”), so the subject is the mechanism the claim
 * talks about MOST, not the first one it happens to quote (`go build`
 * verification prose routinely precedes the `forge pack` finding). */
export function propositionScope(proposition: string, vocabulary: Set<string>): { primary: string; secondary: string[] } {
  if (!vocabulary.size || !proposition) return { primary: '', secondary: [] }
  // Mechanisms the claim mentions, in first-appearance order — code spans
  // first (a claim's subject is usually among its quoted commands), whole
  // text second (nightly K82 mentions `nb auth` only in prose).
  const seen: string[] = []
  const push = (text: string) => {
    for (const token of text.toLowerCase().split(/[^a-z0-9_-]+/i).filter(Boolean)) {
      if (vocabulary.has(token) && !seen.includes(token)) seen.push(token)
    }
  }
  for (const match of proposition.matchAll(/`([^`]*)`/g)) push(match[1])
  push(proposition)
  if (!seen.length) return { primary: '', secondary: [] }
  if (seen.length >= 2 && (/\(\d+\)/.test(proposition) || /end.?to.?end/i.test(proposition))) {
    return { primary: 'end-to-end', secondary: seen }
  }
  const counts = new Map<string, number>()
  for (const token of proposition.toLowerCase().split(/[^a-z0-9_-]+/i).filter(Boolean)) {
    if (vocabulary.has(token)) counts.set(token, (counts.get(token) ?? 0) + 1)
  }
  const subject = [...seen].sort((a, b) => (counts.get(b) ?? 0) - (counts.get(a) ?? 0) || seen.indexOf(a) - seen.indexOf(b))[0]
  return { primary: subject, secondary: seen.filter((mechanism) => mechanism !== subject) }
}

/** Memory population: how many experiences are alive at a moment. */
export function aliveRowAt(rows: KnowledgeRow[], at: string): number {
  return rows.filter((row) => row.firstAt <= at && (!row.terminal || row.terminal.at > at)).length
}

export type MemoryBucket = 'active' | 'stale' | 'invalidated' | 'archived'

export function stateBucket(state: string): MemoryBucket {
  if (state === 'invalidated') return 'invalidated'
  if (state === 'superseded') return 'archived'
  if (state === 'challenged' || state === 'corrected') return 'stale'
  return 'active'
}

/** Compact row label: `…/knowledge/82` → K82, auto hashes → head…tail. */
export function shortKnowledge(id: string): string {
  const tail = id.split('/knowledge/')[1] ?? id
  if (/^\d+$/.test(tail)) return `K${tail}`
  // Lane-label budget before row-state (x=58) is ~6 mono chars; longer
  // auto tails keep only their recognizable ending.
  return tail.length > 6 ? `…${tail.slice(-5)}` : tail
}

/** Child run ids look like `<root>/<role>`; root team runs have no slash. */
export function roleOfRun(run: string | undefined): string | undefined {
  if (!run) return undefined
  const parts = run.split('/')
  if (parts.length < 2) return undefined
  const role = parts[parts.length - 1]
  return role || undefined
}

function stringOf(value: unknown): string {
  return typeof value === 'string' ? value : ''
}

function sortedEvents(events: ObservationEvent[]): ObservationEvent[] {
  return [...events].sort((left, right) =>
    left.occurred_at.localeCompare(right.occurred_at) ||
    left.received_at.localeCompare(right.received_at) ||
    left.event_id.localeCompare(right.event_id),
  )
}

function distinct(values: (string | undefined)[]): string[] {
  return [...new Set(values.filter((value): value is string => Boolean(value)))].sort()
}

/** Terminal-ish states dominate the aggregate: a contradicted member makes
 * the whole cluster contradicted even if other members are confirmed. */
function aggregateState(states: string[]): string {
  const rank: Record<string, number> = { invalidated: 0, superseded: 1, corrected: 2, challenged: 3, proposed: 4, confirmed: 5 }
  let best = ''
  let bestRank = Number.POSITIVE_INFINITY
  for (const state of states) {
    const value = rank[state] ?? 4
    if (value < bestRank) {
      best = state
      bestRank = value
    }
  }
  return best || 'proposed'
}

function derivedStrength(state: string, useCount: number): number {
  const base: Record<string, number> = { confirmed: 0.7, proposed: 0.5, challenged: 0.35, corrected: 0.2, superseded: 0.15, invalidated: 0.1 }
  return Math.min(1, Math.round(((base[state] ?? 0.5) + Math.min(0.25, 0.05 * useCount)) * 100) / 100)
}

interface MemberFold {
  knowledgeId: string
  proposition: string
  state: string
  firstAt: string
  command?: string
  commandClass?: string
  policy?: string
  points: ExperiencePoint[]
  episodes: EpisodeRef[]
  deaths: DeathRecord[]
}

/** Fold the raw event stream into the Experience Timeline model. */
export function foldExperience(events: ObservationEvent[]): ExperienceModel {
  const ordered = sortedEvents(events)
  const runs = new Map<string, RunInfo>()
  const members = new Map<string, MemberFold>()
  const links: ActivationLink[] = []
  const offered = new Map<string, ActivationLink>()
  const clusterOfKnowledge = new Map<string, string>()
  const lineage: KnowledgeLineage[] = []
  // Invalidations whose reason may reference the replacing knowledge; resolved
  // after the pass because the referenced item can appear later in the stream.
  const pendingInferences: { fromId: string; refs: string[]; reason?: string; eventId: string; at: string }[] = []
  // Executed commands (approval payloads) feed the mechanism vocabulary.
  const executedCommands: string[][] = []
  let first = ''
  let last = ''

  const touchTime = (at: string) => {
    if (!first || at < first) first = at
    if (!last || at > last) last = at
  }

  for (const event of ordered) {
    touchTime(event.occurred_at)
    const run = event.context?.run
    const role = stringOf(event.data?.role) || roleOfRun(run)

    if (event.type === 'run.started') {
      const id = run ?? event.event_id
      if (!runs.has(id)) {
        runs.set(id, { id, role, parentRun: stringOf(event.data?.parent_run_id) || undefined, startedAt: event.occurred_at, status: undefined, actor: event.context?.actor?.id || undefined, title: stringOf(event.data?.title) || undefined, agentId: stringOf(event.data?.agent_id) || undefined, toolCalls: 0, modelCalls: 0, knowledgeEvents: 0, commands: [] })
      }
      continue
    }
    const runInfo = run ? runs.get(run) : undefined
    if (runInfo) {
      if (event.type === 'tool.started') runInfo.toolCalls++
      if (event.type === 'model.started') runInfo.modelCalls++
      if (event.type === 'run.completed' || event.type === 'run.failed' || event.type === 'run.cancelled') {
        runInfo.endedAt = event.occurred_at
        runInfo.status = stringOf(event.data?.status) || event.type.slice('run.'.length)
      }
    }

    if (event.type.startsWith('approval.')) {
      const operation = event.data?.operation as { arguments?: { command?: unknown } } | undefined
      const command = operation?.arguments?.command
      if (Array.isArray(command)) {
        const tokens = command.map((token) => String(token))
        executedCommands.push(tokens)
        if (runInfo) runInfo.commands.push({ command: tokens.join(' '), policy: stringOf(event.data?.policy_id) || undefined, at: event.occurred_at, eventId: event.event_id })
      }
    }

    const kind = lifecycleKindOf(event)
    if (!kind) continue
    if (runInfo) runInfo.knowledgeEvents++

    if (event.type === 'hint.offered') {
      const hintId = stringOf(event.data?.hint_id)
      const knowledgeId = stringOf(event.data?.knowledge_id)
      if (hintId && knowledgeId) {
        const link: ActivationLink = { hintId, knowledgeId, clusterId: '', offeredAt: event.occurred_at, offeredRun: run }
        offered.set(hintId, link)
        links.push(link)
      }
    }

    const knowledgeId = stringOf(event.data?.knowledge_id)
    if (!knowledgeId) continue

    if (event.type === 'knowledge.corrected' || event.type === 'knowledge.superseded') {
      const replacement = stringOf(event.data?.replacement_id)
      if (replacement) {
        lineage.push({ fromId: knowledgeId, toId: replacement, reason: stringOf(event.data?.reason) || undefined, eventId: event.event_id, at: event.occurred_at, inferred: false })
      }
    }
    if (event.type === 'knowledge.invalidated') {
      const reason = stringOf(event.data?.reason)
      const refs = reason ? [...reason.matchAll(/([A-Za-z0-9_-]+\/knowledge\/[A-Za-z0-9_-]+)/g)].map((match) => match[1]).filter((ref) => ref !== knowledgeId) : []
      if (refs.length) pendingInferences.push({ fromId: knowledgeId, refs, reason: reason || undefined, eventId: event.event_id, at: event.occurred_at })
    }

    if (event.type === 'knowledge.proposed') {
      const existing = members.get(knowledgeId)
      if (!existing) {
        const proposition = stringOf(event.data?.proposition)
        const command = stringOf(event.data?.command)
        members.set(knowledgeId, {
          knowledgeId,
          proposition,
          state: 'proposed',
          firstAt: event.occurred_at,
          command: command || undefined,
          commandClass: stringOf(event.data?.command_class) || undefined,
          policy: stringOf(event.data?.policy_id) || undefined,
          points: [],
          episodes: [],
          deaths: [],
        })
      }
    }

    const member = members.get(knowledgeId)
    if (!member) continue

    member.points.push({ at: event.occurred_at, kind, origin: event.type, knowledgeId, run, role, eventId: event.event_id, hintId: stringOf(event.data?.hint_id) || undefined, rule: stringOf(event.data?.rule) || undefined, actor: stringOf(event.context?.actor?.id) || undefined })

    if (kind === 'contradicted' || kind === 'weakened' || kind === 'archived') {
      member.deaths.push({ kind, at: event.occurred_at, run, role, actor: stringOf(event.context?.actor?.id) || undefined, reason: stringOf(event.data?.reason) || undefined, eventId: event.event_id })
    }

    if (kind === 'injected') {
      const hintId = stringOf(event.data?.hint_id)
      const link = hintId ? offered.get(hintId) : undefined
      if (link) {
        link.usedAt = event.occurred_at
        link.usedRun = run
      }
    }

    switch (event.type) {
      case 'knowledge.confirmed':
        member.state = 'confirmed'
        break
      case 'knowledge.challenged':
        member.state = 'challenged'
        break
      case 'knowledge.disproved':
      case 'knowledge.invalidated':
        member.state = 'invalidated'
        break
      case 'knowledge.corrected':
        member.state = 'corrected'
        break
      case 'knowledge.superseded':
        member.state = 'superseded'
        break
      case 'knowledge.proposed':
        break
      default:
        break
    }
    if (event.type === 'knowledge.proposed') {
      for (const evidence of event.evidence ?? []) {
        const ref = stringOf(evidence.ref)
        if (!ref) continue
        const executionRun = evidence.type === 'execution' ? runOfEvidenceRef(ref) : undefined
        member.episodes.push({ knowledgeId, run: executionRun?.run ?? run ?? '', role: executionRun?.role ?? role, ref, kind: evidence.type ?? 'artifact', at: event.occurred_at })
      }
    }
  }

  // Runs that never recorded a terminal event still need an end for the lane.
  for (const run of runs.values()) {
    if (!run.endedAt) run.endedAt = last
  }

  // Group members into clusters. Execution observations cluster by canonical
  // command; free-form claims cluster by shared content tokens so that one
  // semantic experience (one problem, one mechanism) spans runs and roles as
  // a single visual object.
  const clusters = new Map<string, ExperienceCluster>()
  const attach = (cluster: ExperienceCluster, member: MemberFold) => {
    cluster.members.push({ knowledgeId: member.knowledgeId, proposition: member.proposition, state: member.state, firstAt: member.firstAt })
    cluster.points.push(...member.points)
    cluster.episodes.push(...member.episodes)
    if (member.command && !cluster.commands.includes(member.command)) cluster.commands.push(member.command)
    clusterOfKnowledge.set(member.knowledgeId, cluster.id)
  }
  const claimMembers: MemberFold[] = []
  for (const member of members.values()) {
    if (member.policy && member.command) {
      const key = execClusterKeyOf(member)
      let cluster = clusters.get(key.id)
      if (!cluster) {
        cluster = { id: key.id, title: key.title, scope: key.scope, kind: key.kind, command: key.command, commands: [], members: [], points: [], episodes: [], runs: [], roles: [], firstAt: member.firstAt, lastAt: member.firstAt, state: 'proposed', strength: 0.5 }
        clusters.set(key.id, cluster)
      }
      attach(cluster, member)
    } else {
      claimMembers.push(member)
    }
  }
  for (const group of groupClaims(claimMembers)) {
    const key = claimClusterKeyOf(group)
    let cluster = clusters.get(key.id)
    if (!cluster) {
      cluster = { id: key.id, title: key.title, scope: key.scope, kind: 'claim', commands: [], members: [], points: [], episodes: [], runs: [], roles: [], firstAt: group[0].firstAt, lastAt: group[0].firstAt, state: 'proposed', strength: 0.5 }
      clusters.set(key.id, cluster)
    }
    for (const member of group) attach(cluster, member)
  }
  for (const link of links) link.clusterId = clusterOfKnowledge.get(link.knowledgeId) ?? ''

  // Inferred lineage: an invalidation reason may name the knowledge item
  // that replaced the invalidated one. Only refs that exist as members
  // become edges; unknown references are ignored.
  for (const pending of pendingInferences) {
    for (const ref of pending.refs) {
      if (!members.has(ref)) continue
      lineage.push({ fromId: pending.fromId, toId: ref, reason: pending.reason, eventId: pending.eventId, at: pending.at, inferred: true })
    }
  }
  // Join the consuming run's final status so the UI can distinguish
  // “injected → reused” from “injected → run.failed”.
  for (const link of links) {
    if (link.usedRun) link.usedRunStatus = runs.get(link.usedRun)?.status
  }

  // ——— Experience scopes ———
  const vocabulary = new Set<string>()
  const feed = (tokens: string[], depth = 0) => {
    // Shell-wrapped commands (`sh -c "cd /work && forge login"`) are the norm
    // once models learn argv has no shell; the script body carries the real
    // mechanisms. Script text is tokenized so shell operators become standalone
    // tokens (commandSegments then splits on them) — glued forms like `true;
    // test` or newlines would otherwise smuggle noise into the vocabulary.
    const script = depth === 0 ? shellScriptArgv(tokens) : undefined
    if (script) feed(script.replace(/&&|\|\||[;|>&]+/g, (operator) => ` ${operator} `).split(/\s+/).filter(Boolean), 1)
    for (const segment of commandSegments(tokens)) {
      const sub = segmentSubcommand(segment)
      if (sub) vocabulary.add(sub)
    }
  }
  for (const tokens of executedCommands) feed(tokens)
  for (const member of members.values()) if (member.command) feed(member.command.split(/\s+/))

  // Lineage reversed: for replacing knowledge, its predecessor is the
  // earliest invalidated item pointing at it (K82→K73 means K73 supersedes K82).
  const predecessorOf = new Map<string, { fromId: string; at: string }>()
  for (const edge of lineage) {
    const current = predecessorOf.get(edge.toId)
    if (!current || edge.at < current.at) predecessorOf.set(edge.toId, { fromId: edge.fromId, at: edge.at })
  }

  const primaryOf = new Map<string, string>()
  const secondaryOf = new Map<string, string[]>()
  // Priority: execution command scope > lineage inheritance (a corrected
  // experience stays in its predecessor's scope) > single mechanism >
  // multi-mechanism “end-to-end” > lexical claim-cluster fallback.
  const assignScope = (id: string, seen: Set<string>): string => {
    const known = primaryOf.get(id)
    if (known) return known
    if (seen.has(id)) return ''
    seen.add(id)
    const member = members.get(id)
    if (!member) return ''
    let primary = ''
    let secondary: string[] = []
    if (member.policy && member.command) {
      primary = scopeOfCommand(member.command)
    } else {
      const predecessor = predecessorOf.get(id)
      if (predecessor && members.has(predecessor.fromId)) primary = assignScope(predecessor.fromId, seen)
      const scope = propositionScope(member.proposition, vocabulary)
      if (!primary) primary = scope.primary
      if (!primary) primary = clusters.get(clusterOfKnowledge.get(id) ?? '')?.scope ?? 'misc'
      secondary = scope.secondary
    }
    primaryOf.set(id, primary)
    secondaryOf.set(id, secondary)
    return primary
  }
  for (const id of members.keys()) assignScope(id, new Set())

  const relatedOf = new Map<string, string[]>()
  for (const cluster of clusters.values()) {
    const ids = cluster.members.map((member) => member.knowledgeId)
    for (const id of ids) relatedOf.set(id, ids.filter((other) => other !== id))
  }

  const rows: KnowledgeRow[] = [...members.values()].map((member) => {
    const points = [...member.points].sort((left, right) => left.at.localeCompare(right.at))
    const terminal = points.find((point) => point.kind === 'archived')
    const useCount = points.filter((point) => point.kind === 'injected' || point.kind === 'reused').length
    return {
      knowledgeId: member.knowledgeId,
      proposition: member.proposition,
      state: member.state,
      firstAt: member.firstAt,
      lastAt: points[points.length - 1]?.at ?? member.firstAt,
      terminal: terminal ? { kind: terminal.kind, at: terminal.at } : undefined,
      scopes: { primary: primaryOf.get(member.knowledgeId) ?? 'misc', secondary: secondaryOf.get(member.knowledgeId) ?? [] },
      command: member.command,
      commandClass: member.commandClass,
      policy: member.policy,
      points,
      episodes: member.episodes,
      deaths: member.deaths,
      relatedIds: relatedOf.get(member.knowledgeId) ?? [],
      runs: distinct(points.map((point) => point.run)),
      roles: distinct(points.map((point) => point.role)),
      strength: derivedStrength(member.state, useCount),
    }
  })
  rows.sort((left, right) => left.firstAt.localeCompare(right.firstAt) || left.knowledgeId.localeCompare(right.knowledgeId))

  const scopeMap = new Map<string, ScopeLane>()
  for (const row of rows) {
    let lane = scopeMap.get(row.scopes.primary)
    if (!lane) {
      lane = { id: row.scopes.primary, title: row.scopes.primary.charAt(0).toUpperCase() + row.scopes.primary.slice(1), kind: row.policy ? 'execution' : 'claim', rows: [], runs: [], roles: [], firstAt: row.firstAt, lastAt: row.lastAt }
      scopeMap.set(row.scopes.primary, lane)
    }
    lane.rows.push(row)
    lane.runs = distinct([...lane.runs, ...row.runs])
    lane.roles = distinct([...lane.roles, ...row.roles])
    if (row.lastAt > lane.lastAt) lane.lastAt = row.lastAt
  }
  const scopeList = [...scopeMap.values()].sort((left, right) => left.firstAt.localeCompare(right.firstAt) || left.id.localeCompare(right.id))

  const clusterList = [...clusters.values()].map((cluster) => {
    // Stable sort by time only: ties keep stream order (received_at), which
    // already places a hint offer before the injection that consumed it.
    cluster.points.sort((left, right) => left.at.localeCompare(right.at))
    cluster.firstAt = cluster.points[0]?.at ?? cluster.firstAt
    cluster.lastAt = cluster.points[cluster.points.length - 1]?.at ?? cluster.lastAt
    cluster.runs = distinct(cluster.points.map((point) => point.run))
    cluster.roles = distinct(cluster.points.map((point) => point.role))
    cluster.state = aggregateState(cluster.members.map((member) => member.state))
    const useCount = cluster.points.filter((point) => point.kind === 'injected' || point.kind === 'reused').length
    cluster.strength = derivedStrength(cluster.state, useCount)
    return cluster
  })
  clusterList.sort((left, right) => left.firstAt.localeCompare(right.firstAt) || left.id.localeCompare(right.id))

  const runList = [...runs.values()].sort((left, right) => left.startedAt.localeCompare(right.startedAt) || left.id.localeCompare(right.id))

  return {
    runs: runList,
    clusters: clusterList,
    rows,
    scopes: scopeList,
    links: links.filter((link) => link.clusterId),
    lineage,
    bounds: { from: first, to: last },
    totals: { events: events.length, knowledge: members.size },
  }
}

/** Reconstruct the §20 forensic chain for one knowledge item: formation
 * (run + evidence + prior executed commands), activations (recall →
 * injection → outcome of the consuming run) and death (who/what/why plus
 * the successor via lineage). Derived from the fold, no extra state. */
export function forensicOf(row: KnowledgeRow, model: ExperienceModel): ForensicRecord {
  const commandsOf = (runId: string | undefined, after?: string, before?: string): ForensicCommand[] => {
    const run = model.runs.find((item) => item.id === runId)
    if (!run) return []
    return run.commands
      .filter((item) => (!after || item.at >= after) && (!before || item.at <= before))
      .map(({ command, policy, at, eventId }) => ({ command, policy, at, eventId }))
  }
  const appeared = row.points.find((point) => point.kind === 'appeared')
  const formed: ForensicFormed | undefined = appeared ? {
    at: appeared.at,
    run: appeared.run,
    role: appeared.role,
    actor: appeared.actor,
    evidence: row.episodes
      .filter((episode) => episode.at === appeared.at)
      .map((episode) => ({ ref: episode.ref, type: episode.kind, at: episode.at, run: episode.run || undefined, role: episode.role })),
    commands: commandsOf(appeared.run, undefined, appeared.at),
  } : undefined
  const activations: ForensicActivation[] = model.links
    .filter((link) => link.knowledgeId === row.knowledgeId)
    .map((link) => ({
      offeredAt: link.offeredAt,
      offeredRun: link.offeredRun,
      usedAt: link.usedAt,
      usedRun: link.usedRun,
      usedRunStatus: link.usedRunStatus,
      commands: commandsOf(link.usedRun, link.usedAt),
    }))
  const deaths: ForensicDeath[] = row.deaths.map((death) => ({
    ...death,
    supersededBy: model.lineage.filter((edge) => edge.fromId === row.knowledgeId && edge.at === death.at).map((edge) => edge.toId),
  }))
  return { formed, activations, deaths }
}

/** Time window centered on a lifecycle moment, clamped to the project bounds:
 * what the forensic navigation uses to bring an event into view. */
export function windowAround(at: string, bounds: { from: string; to: string }, minSpan = 30_000): { t0: number; t1: number } {
  const target = new Date(at).getTime()
  const from = new Date(bounds.from).getTime()
  const to = new Date(bounds.to).getTime()
  const span = Math.max(minSpan, (to - from) * 0.04)
  let t0 = target - span / 2
  let t1 = target + span / 2
  if (t0 < from) { t1 += from - t0; t0 = from }
  if (t1 > to) { t0 -= t1 - to; t1 = to }
  return { t0: Math.max(from, t0), t1: Math.min(to, t1) }
}

function execClusterKeyOf(member: MemberFold): { id: string; title: string; scope: string; kind: 'execution'; command: string } {
  const canonical = canonicalCommand(member.command!)
  // Scope comes from the command itself (test/vet/build/fmt): the kernel's
  // command_class is a coarse verification/build split that would fold `go
  // vet` into `test` and hide real scope boundaries in the UI.
  return { id: `exec:${canonical}`, title: canonical, scope: scopeOfCommand(canonical), kind: 'execution', command: canonical }
}

/** Content tokens used for claim clustering: lowercase alphanumeric runs,
 * mirroring the runtime matcher's tokenization (AUTH_TOKEN → auth, token). */
export function contentTokens(text: string): string[] {
  const raw = text.toLowerCase().match(/[a-z0-9]+/g) ?? []
  return raw.filter((token) => token.length >= 2 && !CLAIM_STOPWORDS.has(token))
}

const CLAIM_STOPWORDS = new Set([
  'the', 'a', 'an', 'and', 'or', 'of', 'to', 'in', 'on', 'at', 'by', 'is', 'are', 'was', 'were', 'be', 'been', 'being',
  'it', 'its', 'this', 'that', 'these', 'those', 'with', 'without', 'for', 'from', 'as', 'not', 'no', 'nor', 'but',
  'does', 'do', 'did', 'done', 'so', 'if', 'then', 'than', 'when', 'which', 'who', 'whom', 'whose', 'into', 'via',
  'only', 'after', 'before', 'must', 'should', 'will', 'would', 'can', 'could', 'may', 'might', 'shall', 'has',
  'have', 'had', 'am', 'we', 'you', 'they', 'he', 'she', 'our', 'their', 'your', 'my', 'me', 'us', 'them',
])

/** Merges claim members whose propositions share at least two content
 * tokens — the same overlap threshold the runtime hint matcher uses, so
 * what clusters together in the view is what can recall together. */
function groupClaims(claimMembers: MemberFold[]): MemberFold[][] {
  const tokens = new Map(claimMembers.map((member) => [member.knowledgeId, new Set(contentTokens(member.proposition))]))
  const parent = new Map(claimMembers.map((member) => [member.knowledgeId, member.knowledgeId]))
  const find = (id: string): string => {
    let root = id
    while (parent.get(root) !== root) root = parent.get(root)!
    while (parent.get(id) !== root) {
      const next = parent.get(id)!
      parent.set(id, root)
      id = next
    }
    return root
  }
  for (let i = 0; i < claimMembers.length; i++) {
    for (let j = i + 1; j < claimMembers.length; j++) {
      const left = tokens.get(claimMembers[i].knowledgeId)!
      const right = tokens.get(claimMembers[j].knowledgeId)!
      let shared = 0
      for (const token of left) if (right.has(token)) shared++
      if (shared >= 2) parent.set(find(claimMembers[i].knowledgeId), find(claimMembers[j].knowledgeId))
    }
  }
  const groups = new Map<string, MemberFold[]>()
  for (const member of claimMembers) {
    const root = find(member.knowledgeId)
    const group = groups.get(root) ?? []
    group.push(member)
    groups.set(root, group)
  }
  return [...groups.values()]
}

/** Claim cluster identity comes from the group's most frequent content
 * tokens (frequency, then first appearance in the stream). */
function claimClusterKeyOf(group: MemberFold[]): { id: string; title: string; scope: string } {
  const freq = new Map<string, number>()
  const order = new Map<string, number>()
  let seen = 0
  for (const member of group) {
    for (const token of new Set(contentTokens(member.proposition))) {
      freq.set(token, (freq.get(token) ?? 0) + 1)
      if (!order.has(token)) order.set(token, seen++)
    }
  }
  const top = [...freq.entries()]
    .sort((left, right) => right[1] - left[1] || (order.get(left[0]) ?? 0) - (order.get(right[0]) ?? 0))
    .slice(0, 2)
    .map(([token]) => token)
  if (top.length === 0) {
    const role = roleOfKnowledgeId(group[0].knowledgeId) ?? 'agent'
    return { id: `claim:${role}`, title: `${role} claims`, scope: role }
  }
  return { id: `claim:${top.join(' ')}`, title: top.join(' '), scope: top[0] }
}

/** Knowledge ids from agent runs embed the originating role:
 * `<run>/<role>/knowledge/N`. Heuristic ids (`auto/...`) have none. */
export function roleOfKnowledgeId(knowledgeId: string): string | undefined {
  const parts = knowledgeId.split('/')
  if (parts.length >= 4 && parts[parts.length - 2] === 'knowledge') return parts[parts.length - 3]
  return undefined
}

/** Execution evidence refs look like `<root-run>/<role>/turn/<n>/call_<id>`. */
export function runOfEvidenceRef(ref: string): { run: string; role?: string } | undefined {
  const parts = ref.split('/')
  if (parts.length >= 5 && parts[parts.length - 3] === 'turn') {
    const role = parts[parts.length - 4]
    const root = parts.slice(0, parts.length - 4).join('/')
    return { run: root ? `${root}/${role}` : role, role }
  }
  return undefined
}
