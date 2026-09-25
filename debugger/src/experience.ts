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

export interface ClusterMember {
  knowledgeId: string
  proposition: string
  state: string
  firstAt: string
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
  toolCalls: number
  modelCalls: number
  knowledgeEvents: number
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

export interface ExperienceModel {
  runs: RunInfo[]
  clusters: ExperienceCluster[]
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
        runs.set(id, { id, role, parentRun: stringOf(event.data?.parent_run_id) || undefined, startedAt: event.occurred_at, toolCalls: 0, modelCalls: 0, knowledgeEvents: 0 })
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
        })
      }
    }

    const member = members.get(knowledgeId)
    if (!member) continue

    member.points.push({ at: event.occurred_at, kind, origin: event.type, knowledgeId, run, role, eventId: event.event_id, hintId: stringOf(event.data?.hint_id) || undefined, rule: stringOf(event.data?.rule) || undefined })

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
    links: links.filter((link) => link.clusterId),
    lineage,
    bounds: { from: first, to: last },
    totals: { events: events.length, knowledge: members.size },
  }
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
