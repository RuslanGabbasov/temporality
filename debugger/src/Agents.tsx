import { useEffect, useState, useCallback, useRef, useMemo } from 'react'
import {
  Button,
  TextInput,
  TextArea,
  Select,
  SelectItem,
  InlineNotification,
  Tag,
  Tile,
  Grid,
  Column,
  Stack,
  Heading,
  Toggle,
  NumberInput,
  Checkbox,

} from '@carbon/react'
import PageLoading from './PageLoading'
import { Add, Edit, TrashCan, Copy, Star, Renew, Settings } from '@carbon/icons-react'
import GeneratingState from './GeneratingState'
import {
  listEvolutionVersions,
  listEvolutionProposals,
  evolutionVersionDiff,
  rollbackEvolutionVersion,
  applyEvolutionProposal,
  rejectEvolutionProposal,
  getObservationSettings,
  setObservationSettings,
  evolutionVersionMetrics,
  listAgentCheckCases,
  addAgentCheckCase,
  deleteAgentCheckCase,
  runAgentCheckCases,
  type EvolutionProposal,
  type EvolutionVersion,
  type ObservationSettings,
  type VersionMetrics,
  type VersionDiff,
  type CheckCase,
  type CheckRunResult,
} from './kernelApi'
import ListFilter, { matchesFilter } from './ListFilter'
import AppModal from './Modal'
import { useConfirm } from './ConfirmDialog'
import {
  workspaceApi,
  type Agent,
  type AgentCapabilities,
  type AgentDefinition,
  type AgentDraft,
  type BuiltinAgentSpec,
  type Provider,
  type Skill,
  type MCPServer,
  type MCPServerTools,
  type MCPToolInfo,
  type Secret,
  type SandboxProfile,
  type SupervisorSettings,
  type TransferReport,
  type TransferResultItem,
} from './workspaceApi'
import {
  isCostTimeMetric,
  isLowerBetter,
  isSignalMark,
  metricDeltaText,
  metricValueText,
  verdictTagType,
  versionKindLabelKey,
  versionKindTagType,
} from './agentEvolution'
import { ImproveAgentDialog, ProposalDiffModal, DefinitionDiffView } from './AgentProposalDialogs'
import { useT } from './i18n'
import { useOrgUnits, OrgUnitSelect, OrgBadge } from './orgUnits'
import { formatDuration } from './eventSummary'

function message(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }

// Numeric count fields default to 0: clicking must SELECT the value, or
// typing "4" appends and shows "04". One handler for every number input.
const selectOnFocus = (e: React.FocusEvent<HTMLInputElement>) => e.target.select()


const CAP_KEYS = ['read_files', 'modify_files', 'run_commands', 'network', 'skills', 'knowledge'] as const
type CapKey = (typeof CAP_KEYS)[number]

/** Where a form section's value came from (same visual language as skills). */
type Prov = 'agent' | 'user' | 'todo'
type SectionKey = 'identity' | 'capabilities' | 'constraints' | 'completion'

function ProvBadge({ kind }: { kind: Prov }) {
  const t = useT()
  const label = kind === 'agent'
    ? (t('skills.prov.agent') ?? 'from agent')
    : kind === 'todo'
      ? (t('skills.prov.todo') ?? 'needs clarification')
      : (t('skills.prov.user') ?? 'verified')
  return <span className={`prov-badge prov-${kind}`}>{label}</span>
}

/** Sections the builder asks questions about map onto form sections. */
function sectionOfField(field: string): SectionKey {
  const head = field.split('.')[0].split('[')[0].trim()
  if (head === 'constraints') return 'constraints'
  if (head === 'completion') return 'completion'
  if (head === 'capabilities' || head === 'name' || head === 'description') return 'identity'
  return 'capabilities'
}

const cleanList = (values?: string[]) =>
  (values ?? []).map((v) => v.trim()).filter(Boolean)

const listToText = (values?: string[]) => (values ?? []).join('\n')

/** Capabilities actually set (undefined = allowed, matches server semantics). */
function setCaps(caps?: AgentCapabilities): AgentCapabilities {
  const result: AgentCapabilities = {}
  // Delegation is serialized with the rest: the backend default is denied,
  // so dropping it here would silently revoke a granted toggle on save.
  for (const key of [...CAP_KEYS, 'delegation', 'triggers', 'vision', 'vision_describe'] as const) {
    const value = caps?.[key]
    if (typeof value === 'boolean') result[key] = value
  }
  return result
}

function disabledCaps(caps?: AgentCapabilities): CapKey[] {
  return CAP_KEYS.filter((key) => caps?.[key] === false)
}

function enabledCaps(caps?: AgentCapabilities): CapKey[] {
  return CAP_KEYS.filter((key) => caps?.[key] === true)
}

export default function Agents({ project, defaultAgentId, refreshProjects }: { project: string; defaultAgentId?: string; refreshProjects?: () => void }) {
  const t = useT()
  const { confirmDialog, dialog: confirmDialogNode } = useConfirm()
  const org = useOrgUnits()
  const [agents, setAgents] = useState<Agent[]>([])
  // Bulk configuration: checkbox mode over the agent list, then one dialog —
  // provider+model, sandbox profile, secrets — applied to every selected
  // agent (per-agent GET+PUT, so each agent keeps its own version history).
  const [bulkMode, setBulkMode] = useState(false)
  const [bulkSelected, setBulkSelected] = useState<Set<string>>(new Set())
  const [bulkOpen, setBulkOpen] = useState(false)
  // The edit dialog's copy of the uniform switch: the config PUT never
  // carries enabled, so a changed toggle lands through the /enabled endpoint
  // right after the save.
  const [formEnabled, setFormEnabled] = useState(true)
  const [providers, setProviders] = useState<Provider[]>([])
  const [builtins, setBuiltins] = useState<BuiltinAgentSpec[]>([])
  const [availableSkills, setAvailableSkills] = useState<Skill[]>([])
  const [mcpServers, setMcpServers] = useState<MCPServer[]>([])
  const [mcpTools, setMcpTools] = useState<MCPServerTools[]>([])
  const [secretsList, setSecretsList] = useState<Secret[]>([])
  const [sandboxProfiles, setSandboxProfiles] = useState<SandboxProfile[]>([])
  const [builtinTools, setBuiltinTools] = useState<MCPToolInfo[]>([])
  // Supervisor model settings (docs/supervisor-model.md): the vision model
  // and the human-attention estimate coefficients. Collapsed by default — a
  // rarely used operator surface that must not push the agent list down.
  const [qualityOpen, setQualityOpen] = useState(false)
  // Stand-to-stand transfer (temporality.transfer/v1): agents export/import.
  const transferFileRef = useRef<HTMLInputElement | null>(null)
  const [transferBusy, setTransferBusy] = useState(false)
  const [transferReport, setTransferReport] = useState<TransferReport | null>(null)

  const runTransferExport = async () => {
    setTransferBusy(true)
    setError('')
    try {
      const blob = await workspaceApi.exportTransfer('agents')
      const url = URL.createObjectURL(blob)
      const link = document.createElement('a')
      link.href = url
      link.download = `temporality-agents-${new Date().toISOString().slice(0, 10)}.json`
      link.click()
      URL.revokeObjectURL(url)
    } catch (f) { setError(message(f)) }
    finally { setTransferBusy(false) }
  }

  const runTransferImport = async (file: File) => {
    setTransferBusy(true)
    setError('')
    try {
      setTransferReport(await workspaceApi.importTransfer(file, 'merge'))
      await load()
    } catch (f) { setError(message(f)) }
    finally { setTransferBusy(false) }
  }
  // Supervisor panel drafts: vision model override and the human-attention
  // estimate coefficients (docs/supervisor-model.md).
  const [supervisor, setSupervisor] = useState<SupervisorSettings | null>(null)
  const [visionDraft, setVisionDraft] = useState('')
  const [compactionDraft, setCompactionDraft] = useState('')
  const [titleDraft, setTitleDraft] = useState('')
  const [extractionDraft, setExtractionDraft] = useState('')
  const [habDraft, setHabDraft] = useState({ approval: 45, answerBase: 20, readingWpm: 180 })
  const [qualityBusy, setQualityBusy] = useState(false)
  const [qualityError, setQualityError] = useState('')
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [showForm, setShowForm] = useState(false)
  const [editing, setEditing] = useState<Agent | null>(null)
  const [formTab, setFormTab] = useState<'general' | 'capabilities' | 'rules' | 'params' | 'bindings' | 'tools' | 'prompt'>('general')
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [detailTab, setDetailTab] = useState<'overview' | 'evolution'>('overview')
  const [promptFor, setPromptFor] = useState<Record<string, { source: string; prompt: string; version: number }>>({})
  const [showTemplates, setShowTemplates] = useState(false)
  const [fromWizard, setFromWizard] = useState(false)

  // Wizard: natural language → agent-built definition draft.
  const [wizardOpen, setWizardOpen] = useState(false)
  const [wizardText, setWizardText] = useState('')
  const [wizardBusy, setWizardBusy] = useState(false)
  const [wizardError, setWizardError] = useState('')
  const [draftQuestions, setDraftQuestions] = useState<{ field: string; question: string }[]>([])
  const [answers, setAnswers] = useState<Record<string, string>>({})
  const [provenance, setProvenance] = useState<Record<string, Prov>>({})

  // Rebuild (§13): draft diff dialog for an existing agent.
  const [regenDraft, setRegenDraft] = useState<AgentDraft | null>(null)
  const [regenBusy, setRegenBusy] = useState(false)

  // Evolution v2 (docs/agent-evolution.md §6): the rewritten «Эволюция» tab —
  // health of the current version, proposals inbox, version timeline,
  // observation window and control check-cases. No gate machinery at all.
  const [versions, setVersions] = useState<EvolutionVersion[]>([])
  const [evoLoadedFor, setEvoLoadedFor] = useState('')
  // Health card of the current version (§6.1): observation state + SLI table.
  const [metrics, setMetrics] = useState<VersionMetrics | null>(null)
  // Observation window settings (§6.5) — also scopes the metrics lookback.
  const [observation, setObservation] = useState<ObservationSettings | null>(null)
  const [obsDraft, setObsDraft] = useState({ days: '7', minRuns: '10' })
  // Control check-cases (§6.6) and the last smoke run result.
  const [checkCases, setCheckCases] = useState<CheckCase[]>([])
  const [newCase, setNewCase] = useState({ input: '', must: '', mustNot: '' })
  const [checkBusy, setCheckBusy] = useState(false)
  const [checkResult, setCheckResult] = useState<CheckRunResult | null>(null)
  // «Предложить улучшение» dialog (§6.3) and the proposal detail modal (§6.2).
  const [improveOpen, setImproveOpen] = useState(false)
  const [detailProposal, setDetailProposal] = useState<EvolutionProposal | null>(null)
  // Version compare modal (§6.4): «Сравнить» fetches the versions diff.
  const [compareDiff, setCompareDiff] = useState<{ from: number; to: number; diff: VersionDiff } | null>(null)
  const [compareBusy, setCompareBusy] = useState(false)
  // Degraded marker for the agent list rows (§6, degradation): filled from
  // every metrics load — the list API carries no verdict of its own.
  const [degradedAgents, setDegradedAgents] = useState<Set<string>>(new Set())

  // Evolution proposals (§2.2): source-tagged definition changes; apply is
  // one button and always succeeds (§1).
  const [proposals, setProposals] = useState<EvolutionProposal[]>([])
  // Pending count per agent id (agents-list badge): comes with the agents
  // list from the server and is re-synced from every per-agent proposals
  // reload so decisions update it immediately.
  const [pendingProposals, setPendingProposals] = useState<Record<string, number>>({})
  const [proposalBusy, setProposalBusy] = useState(false)
  const [proposalNotice, setProposalNotice] = useState<{ kind: 'info' | 'success' | 'error'; text: string } | null>(null)

  // Prompt tab preview (reflects the saved agent).
  const [promptPreview, setPromptPreview] = useState<{ source: string; prompt: string } | null>(null)

  // Form state
  const [form, setForm] = useState<Partial<Agent>>({})

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const [agentsData, providersData, skillsData, mcpData, toolsData, builtinsData, secretsData, profilesData] = await Promise.all([
        workspaceApi.listAllAgents(),
        workspaceApi.listProviders(),
        workspaceApi.listSkills(),
        workspaceApi.listMCPServers(),
        workspaceApi.listMCPTools(),
        workspaceApi.listBuiltinAgents(),
        workspaceApi.listSecrets(),
        workspaceApi.listSandboxProfiles(),
      ])
      setAgents(agentsData.agents ?? [])
      setPendingProposals(agentsData.pending_proposals ?? {})
      setProviders(providersData.providers ?? [])
      setAvailableSkills(skillsData.skills ?? [])
      setMcpServers(mcpData.servers ?? [])
      setMcpTools(Object.values(toolsData.servers ?? {}))
      setBuiltinTools(toolsData.builtins ?? [])
      setBuiltins(builtinsData.builtins ?? [])
      setSecretsList(secretsData.secrets ?? [])
      setSandboxProfiles(profilesData.profiles ?? [])
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }, [project])

  useEffect(() => { void load() }, [load])

  // The uniform switch (the trigger pattern): flip first, roll back the tile
  // on failure — the list never waits for a reload to reflect the toggle.
  const toggleAgentEnabled = async (a: Agent) => {
    const next = a.enabled === false
    setAgents((prev) => prev.map((x) => (x.id === a.id ? { ...x, enabled: next } : x)))
    try {
      const updated = await workspaceApi.setAgentEnabled(a.id, next)
      setAgents((prev) => prev.map((x) => (x.id === a.id ? updated : x)))
    } catch (f) {
      setAgents((prev) => prev.map((x) => (x.id === a.id ? { ...x, enabled: a.enabled } : x)))
      setError(message(f))
    }
  }

  // Quality settings are independent of the agent list: a failure here must
  // not break the page, and the panel just shows its own error.
  // Supervisor settings are independent of the agent list: a failure here must
  // not break the page, and the panel just shows its own error.
  const loadQualitySettings = useCallback(async () => {
    try {
      const sup = await workspaceApi.getSupervisorSettings().catch(() => null)
      if (sup) {
        setSupervisor(sup)
        setVisionDraft(sup.vision_model ?? '')
        setCompactionDraft(sup.compaction_model ?? '')
        setTitleDraft(sup.title_model ?? '')
        setExtractionDraft(sup.extraction_model ?? '')
        setHabDraft({
          approval: sup.hab.approval_seconds,
          answerBase: sup.hab.answer_base_seconds,
          readingWpm: sup.hab.reading_wpm,
        })
      }
    } catch { /* panel shows its own empty state */ }
  }, [])
  useEffect(() => { void loadQualitySettings() }, [loadQualitySettings])

  // One Save for the supervisor panel: the vision model and the HAB
  // coefficients change together in practice.
  const saveQualitySettings = async () => {
    setQualityBusy(true); setQualityError('')
    try {
      const sup = await workspaceApi.updateSupervisorSettings({
        judge_model: supervisor?.judge_model ?? '',
        vision_model: visionDraft.trim(),
        compaction_model: compactionDraft.trim(),
        title_model: titleDraft.trim(),
        extraction_model: extractionDraft.trim(),
        hab: {
          approval_seconds: Number(habDraft.approval) || 45,
          answer_base_seconds: Number(habDraft.answerBase) || 20,
          reading_wpm: Number(habDraft.readingWpm) || 180,
        },
      })
      setSupervisor(sup)
      setQualityOpen(false)
    } catch (e) { setQualityError(message(e)) }
    finally { setQualityBusy(false) }
  }

  const touch = (section: SectionKey) =>
    setProvenance((prev) => (prev[section] === 'user' ? prev : { ...prev, [section]: 'user' }))

  const setDef = (part: Partial<AgentDefinition>) =>
    setForm((prev) => ({ ...prev, definition: { ...prev.definition, ...part } }))

  const setCap = (key: CapKey | 'delegation' | 'triggers' | 'vision' | 'vision_describe', value: boolean | undefined) =>
    setForm((prev) => ({
      ...prev,
      definition: {
        ...prev.definition,
        capabilities: { ...prev.definition?.capabilities, [key]: value },
      },
    }))

  // Vision mode (docs/harness-settings.md): the 3-way image-handling switch
  // mapped onto the capability flags — off is vision=false, describe-only is
  // vision_describe=true, full is the default.
  const visionMode: 'full' | 'describe' = form.definition?.capabilities?.vision_describe === true ? 'describe' : 'full'
  const setVisionMode = (mode: 'full' | 'describe') => {
    touch('capabilities')
    setCap('vision', undefined)
    setCap('vision_describe', mode === 'describe' ? true : undefined)
  }

  // ── Create / edit flows ────────────────────────────────────────────────

  const openWizard = () => {
    setWizardText('')
    setWizardError('')
    setDraftQuestions([])
    setAnswers({})
    setWizardOpen(true)
  }

  const startManual = () => {
    setEditing(null)
    setFormEnabled(true)
    setFormTab('general')
    setFromWizard(false)
    setProvenance({})
    setDraftQuestions([])
    setPromptPreview(null)
    setForm({ name: '', model: '', skills: [], mcp_servers: [], secrets: [], tools: [], definition: {} })
    setShowForm(true)
    setWizardOpen(false)
  }

  const applyDraft = (draft: AgentDraft, questions: { field: string; question: string }[]) => {
    const prov: Record<string, Prov> = {}
    if (draft.name || draft.description) prov.identity = 'agent'
    if (Object.keys(setCaps(draft.capabilities)).length > 0) prov.capabilities = 'agent'
    if (cleanList(draft.constraints).length > 0) prov.constraints = 'agent'
    if (cleanList(draft.completion).length > 0) prov.completion = 'agent'
    for (const q of questions) {
      const section = sectionOfField(q.field)
      if (!prov[section]) prov[section] = 'todo'
    }
    setProvenance(prov)
    setDraftQuestions(questions)
    setAnswers({})
    setPromptPreview(null)
    setForm({
      name: draft.name || '',
      description: draft.description || '',
      model: draft.suggested?.model || '',
      definition: {
        capabilities: setCaps(draft.capabilities),
        constraints: cleanList(draft.constraints),
        completion: cleanList(draft.completion),
      },
      sandbox_profile: '',
      network_access: draft.suggested?.network,
      skills: [],
      mcp_servers: [],
      tools: [],
    })
    setFromWizard(true)
    setEgressLevelOverride(null)
    setFormTab('general')
    setShowForm(true)
    setWizardOpen(false)
  }

  const generate = async (extraAnswers?: Record<string, string>) => {
    if (!wizardText.trim()) return
    setWizardBusy(true); setWizardError('')
    try {
      let description = wizardText.trim()
      const filled = Object.entries(extraAnswers ?? answers).filter(([, a]) => a.trim())
      if (filled.length) {
        description += '\n\nClarifications:\n' + filled.map(([i, a]) => {
          const q = draftQuestions[Number(i)]?.question ?? ''
          return `- ${q}\n  Answer: ${a.trim()}`
        }).join('\n')
      }
      // Follow-up round: questions were already asked once — this call must
      // not generate new ones (single clarification round by design).
      const draft = await workspaceApi.draftAgent(description, draftQuestions.length > 0)
      applyDraft(draft, draft.questions ?? [])
    } catch (f) {
      setWizardError(message(f))
    } finally {
      setWizardBusy(false)
    }
  }

  const startEdit = (agent: Agent) => {
    setEditing(agent)
    setFormEnabled(agent.enabled !== false)
    setFormTab('general')
    setFromWizard(false)
    setProvenance({})
    setDraftQuestions([])
    setPromptPreview(null)
    setForm({ ...agent, definition: agent.definition ? { ...agent.definition } : {} })
    setEgressLevelOverride(null)
    syncEgressDraft(agent.egress_allowlist)
    setShowForm(true)
  }

  const saveAgent = async () => {
    if (!form.name?.trim()) return
    setLoading(true); setError('')
    try {
      const caps = setCaps(form.definition?.capabilities)
      const constraints = cleanList(form.definition?.constraints)
      const completion = cleanList(form.definition?.completion)
      const override = (form.definition?.prompt_override ?? '').trim()
      const hasDefinition = Object.keys(caps).length > 0 || constraints.length > 0 || completion.length > 0 || override !== ''
      const definition = hasDefinition
        ? {
            ...(Object.keys(caps).length > 0 ? { capabilities: caps } : {}),
            ...(constraints.length > 0 ? { constraints } : {}),
            ...(completion.length > 0 ? { completion } : {}),
            ...(override !== '' ? { prompt_override: override } : {}),
          }
        : undefined
      // The egress textarea lives in a draft: fold the parsed lines into the
      // payload (empty lines and blanks already dropped by the parser).
      const withEgress = { ...form, egress_allowlist: parseEgressDraft() }
      const payload: Partial<Agent> & { prompt_source?: string } = { ...withEgress, definition }
      if (fromWizard && !editing) payload.prompt_source = 'ai'
      if (editing) {
        await workspaceApi.updateAgent(editing.id, payload)
        // PUT payloads ignore org bindings: re-scoping goes through the admin
        // binding endpoint (docs/org-structure.md §39).
        if (org.isAdmin && (form.org_unit_id ?? '') !== (editing.org_unit_id ?? '')) {
          await workspaceApi.setResourceBinding('agent', editing.id, form.org_unit_id ?? '')
        }
        if (formEnabled !== (editing.enabled !== false)) {
          await workspaceApi.setAgentEnabled(editing.id, formEnabled)
        }
      } else {
        const created = await workspaceApi.createAgent(payload as Agent & { name: string })
        if (!formEnabled) {
          await workspaceApi.setAgentEnabled(created.id, false)
        }
      }
      setShowForm(false); setEditing(null); setFromWizard(false)
      void load()
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }

  const deleteAgent = async (agent: Agent) => {
    if (!(await confirmDialog({ title: t('agents.delete_confirm', { name: agent.name }) ?? `Delete agent "${agent.name}"?`, body: t('action.delete_warning') ?? 'This cannot be undone.', danger: true, confirmLabel: t('action.delete') ?? 'Delete' }))) return
    setLoading(true); setError('')
    try {
      await workspaceApi.deleteAgent(agent.id)
      void load()
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }

  const duplicateAgent = (agent: Agent) => {
    setEditing(null)
    setFormTab('general')
    setFromWizard(false)
    setProvenance({})
    setPromptPreview(null)
    // Agents are cross-functional (docs/evaluable-agent.md): no project
    // binding on copies either — project context is attached per run.
    setForm({ ...agent, id: undefined, name: agent.name + (t('agents.copy_suffix') ?? ' (copy)'), definition: agent.definition ? { ...agent.definition } : {} })
    setShowForm(true)
  }

  const makeDefault = async (agent: Agent) => {
    if (!project) return
    setLoading(true); setError('')
    try {
      // PUT replaces the whole project, so re-send every editable field.
      const proj = await workspaceApi.getProject(project)
      await workspaceApi.updateProject(project, {
        name: proj.name,
        description: proj.description,
        default_agent_id: agent.id,
        default_model: proj.default_model,
      })
      refreshProjects?.()
    } catch (f) {
      const msg = message(f)
      if (/\bHTTP 404\b|not found/i.test(msg)) {
        // Stale project in the URL: the project no longer exists.
        setError(t('agents.project_missing', { project }) ?? `Project "${project}" was not found — it may have been deleted. The project list has been refreshed.`)
        refreshProjects?.()
      } else if (/\bHTTP 401\b|bearer token/i.test(msg)) {
        setError(t('agents.token_invalid') ?? 'Your token was rejected. Please sign in again.')
      } else {
        setError(msg)
      }
    }
    finally { setLoading(false) }
  }

  // ── Prompt preview / evolution ─────────────────────────────────────────

  const loadPromptFor = async (agentId: string) => {
    try {
      const data = await workspaceApi.getAgentPrompt(agentId)
      setPromptFor((prev) => ({ ...prev, [agentId]: { source: data.source, prompt: data.prompt, version: data.version } }))
    } catch { /* preview is best-effort */ }
  }

  useEffect(() => {
    if (formTab === 'prompt' && editing?.id && !promptPreview) {
      void workspaceApi.getAgentPrompt(editing.id).then((data) => setPromptPreview({ source: data.source, prompt: data.prompt })).catch(() => setPromptPreview(null))
    }
  }, [formTab, editing, promptPreview])

  // ── Master-detail: selection + lazy tab data ───────────────────────

  const selected = agents.find((a) => a.id === selectedId) ?? null
  // Pending evolution proposals for the selected agent (tab badge); falls
  // back to 0 when the server reported nothing.
  const selectedPending = selectedId ? (pendingProposals[selectedId] ?? 0) : 0

  // Every project gets its own copy of the builtin agents on creation, so a
  // global list shows one duplicate per extra project. The panel is scoped
  // to the current project; global agents (no project) are always visible.
  const visibleAgents = agents.filter((a) => !a.project_id || a.project_id === project)

  // Client-side filter over the visible set (name/id).
  const [filter, setFilter] = useState('')
  const filteredAgents = visibleAgents.filter((a) => matchesFilter(filter, a.name, a.id, a.description))
  const toggleBulk = useCallback((id: string) => {
    setBulkSelected((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }, [])

  // Skills and MCP servers come from the global registry. Ids bound earlier
  // that no longer exist stay listed with a marker so they can be reviewed
  // and unbound.
  const missing = t('agents.binding_missing') ?? 'not found'
  const skillOptions = [
    ...availableSkills.map((s) => ({ id: s.id, label: s.name })),
    ...(form.skills ?? [])
      .filter((id) => !availableSkills.some((s) => s.id === id))
      .map((id) => ({ id, label: `${id} — ${missing}` })),
  ]
  const mcpOptions = [
    ...mcpServers.map((s) => ({ id: s.id, label: s.name })),
    ...(form.mcp_servers ?? [])
      .filter((id) => !mcpServers.some((s) => s.id === id))
      .map((id) => ({ id, label: `${id} — ${missing}` })),
  ]

  const secretOptions = [
    ...secretsList.map((s) => ({ id: s.name, label: s.description ? `${s.name} — ${s.description}` : s.name, description: s.description ?? '' })),
    ...(form.secrets ?? [])
      .filter((name) => !secretsList.some((s) => s.name === name))
      .map((name) => ({ id: name, label: `${name} — ${missing}`, description: '' })),
  ]

  // Auto-select the first visible agent once the list is loaded.
  useEffect(() => {
    if (!selectedId && visibleAgents.length > 0) setSelectedId(visibleAgents[0].id)
    if (selectedId && !visibleAgents.some((a) => a.id === selectedId) && visibleAgents.length > 0) setSelectedId(visibleAgents[0].id)
  }, [visibleAgents, selectedId])

  // Overview needs the compiled prompt; evolution needs versions × runs.
  useEffect(() => {
    if (!selectedId) return
    if (!promptFor[selectedId]) void loadPromptFor(selectedId)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selectedId])

  // Health of the current version (§6.1): metrics come from the version
  // metrics endpoint scoped to the observation window; the current version is
  // the newest snapshot (the server pins it on the agent).
  const loadMetrics = useCallback(async (agentId: string, version: number, days?: number) => {
    if (!version) { setMetrics(null); return }
    try {
      const data = await evolutionVersionMetrics(agentId, version, days)
      setMetrics(data)
      setDegradedAgents((prev) => {
        const next = new Set(prev)
        if (data.observation?.verdict === 'degraded') next.add(agentId)
        else next.delete(agentId)
        return next
      })
    } catch { setMetrics(null) }
  }, [])

  const currentVersionOf = (list: EvolutionVersion[]) =>
    list.reduce((max, v) => Math.max(max, v.version), 0)

  useEffect(() => {
    if (detailTab !== 'evolution' || !selectedId || evoLoadedFor === selectedId) return
    setEvoLoadedFor(selectedId)
    setVersions([])
    setProposals([])
    setMetrics(null)
    setObservation(null)
    setCheckCases([])
    setCheckResult(null)
    setProposalNotice(null)
    void (async () => {
      try {
        const [vData, pData, obs, cases] = await Promise.all([
          listEvolutionVersions(selectedId),
          listEvolutionProposals(selectedId),
          getObservationSettings(selectedId).catch(() => null),
          listAgentCheckCases(selectedId).catch(() => ({ cases: [] })),
        ])
        const loaded = vData.versions ?? []
        setVersions(loaded)
        applyProposals(selectedId, pData.proposals ?? [])
        if (obs) {
          setObservation(obs)
          setObsDraft({ days: String(obs.days), minRuns: String(obs.min_runs) })
        }
        setCheckCases(cases.cases ?? [])
        await loadMetrics(selectedId, currentVersionOf(loaded), obs?.days)
      } catch (f) { setError(message(f)) }
    })()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [detailTab, selectedId, evoLoadedFor])

  // Evolution proposals (§2.2): apply is one button and always succeeds (§1);
  // afterwards reload versions, proposals and metrics so the timeline and the
  // health card reflect the new definition version.
  const applyProposals = (agentId: string, list: EvolutionProposal[]) => {
    setProposals(list)
    // The fetched list is the authoritative pending count — keep the list
    // badge in sync so apply/reject/create update it without a full reload.
    setPendingProposals((prev) => ({ ...prev, [agentId]: list.filter((p) => p.status === 'pending').length }))
  }

  const refreshEvolution = async (agentId: string) => {
    const [vData, pData] = await Promise.all([
      listEvolutionVersions(agentId),
      listEvolutionProposals(agentId),
    ])
    const loaded = vData.versions ?? []
    setVersions(loaded)
    applyProposals(agentId, pData.proposals ?? [])
    await loadMetrics(agentId, currentVersionOf(loaded), observation?.days)
  }

  // Rollback: bring a previous definition snapshot back as a NEW version
  // (§1: history is append-only and the rollback itself is revertible).
  const rollbackToVersion = async (version: number) => {
    if (!selectedId) return
    setProposalBusy(true); setError('')
    try {
      await rollbackEvolutionVersion(selectedId, version)
      setProposalNotice({ kind: 'success', text: t('agents.rollback_done', { version: String(version) }) ?? `Rolled back to v${version} as a new version.` })
      await refreshEvolution(selectedId)
      await load()
    } catch (f) { setError(message(f)) }
    finally { setProposalBusy(false) }
  }

  // Apply always succeeds (§1) — no gate, no override, no justification. The
  // confirm dialog is the only ceremony; the response names the new version.
  const applyProposal = async (proposal: EvolutionProposal) => {
    if (!selectedId) return
    setProposalBusy(true); setError(''); setProposalNotice(null)
    try {
      const receipt = await applyEvolutionProposal(selectedId, proposal.id)
      setProposalNotice({
        kind: 'success',
        text: t('agents.proposal_applied_version', { version: String(receipt.version) }) ?? `The definition is now v${receipt.version}.`,
      })
      await refreshEvolution(selectedId)
      await load()
    } catch (f) {
      setProposalNotice({ kind: 'error', text: message(f) })
    } finally { setProposalBusy(false) }
  }

  const rejectProposal = async (proposal: EvolutionProposal) => {
    if (!selectedId) return
    setProposalBusy(true); setError('')
    try {
      await rejectEvolutionProposal(selectedId, proposal.id)
      const pData = await listEvolutionProposals(selectedId)
      applyProposals(selectedId, pData.proposals ?? [])
    } catch (f) { setError(message(f)) }
    finally { setProposalBusy(false) }
  }

  // «Сравнить» (§6.4): structural + prompt diff of a version against its
  // baseline — the version it replaced.
  const compareVersion = async (version: EvolutionVersion) => {
    if (!selectedId) return
    const from = version.baseline_version ?? version.version - 1
    if (from < 1 || from === version.version) return
    setCompareBusy(true); setError('')
    try {
      const diff = await evolutionVersionDiff(selectedId, from, version.version)
      setCompareDiff({ from, to: version.version, diff })
    } catch (f) { setError(message(f)) }
    finally { setCompareBusy(false) }
  }

  // Control check-cases (§6.6): an optional synchronous smoke run against a
  // version or a pending proposal's draft — an informational signal, never a
  // condition of applying.
  const runCheckCases = async (target: number | { proposal: number }) => {
    if (!selectedId) return
    setCheckBusy(true); setError('')
    try {
      const result = await runAgentCheckCases(selectedId, target)
      setCheckResult(result)
      setProposalNotice({
        kind: result.failed > 0 ? 'error' : 'success',
        text: t('agents.check_result', { passed: String(result.passed), failed: String(result.failed) }) ?? `Passed ${result.passed} · failed ${result.failed}.`,
      })
    } catch (f) { setError(message(f)) }
    finally { setCheckBusy(false) }
  }

  const saveCheckCase = async () => {
    if (!selectedId || !newCase.input.trim()) return
    setCheckBusy(true); setError('')
    try {
      await addAgentCheckCase(selectedId, {
        input: newCase.input.trim(),
        must_contain: newCase.must.split(',').map((s) => s.trim()).filter(Boolean),
        must_not_contain: newCase.mustNot.split(',').map((s) => s.trim()).filter(Boolean),
      })
      setNewCase({ input: '', must: '', mustNot: '' })
      const page = await listAgentCheckCases(selectedId)
      setCheckCases(page.cases ?? [])
    } catch (f) { setError(message(f)) }
    finally { setCheckBusy(false) }
  }

  const removeCheckCase = async (checkCase: CheckCase) => {
    if (!selectedId) return
    setCheckBusy(true); setError('')
    try {
      await deleteAgentCheckCase(selectedId, checkCase.id)
      setCheckCases((prev) => prev.filter((c) => c.id !== checkCase.id))
    } catch (f) { setError(message(f)) }
    finally { setCheckBusy(false) }
  }

  const saveObservation = async () => {
    if (!selectedId) return
    const days = Math.min(90, Math.max(1, Math.round(Number(obsDraft.days) || 0)))
    const minRuns = Math.min(100, Math.max(1, Math.round(Number(obsDraft.minRuns) || 0)))
    setProposalBusy(true); setError('')
    try {
      const saved = await setObservationSettings(selectedId, { days, min_runs: minRuns })
      setObservation(saved)
      setObsDraft({ days: String(saved.days), minRuns: String(saved.min_runs) })
      await loadMetrics(selectedId, currentVersionOf(versions), saved.days)
    } catch (f) { setError(message(f)) }
    finally { setProposalBusy(false) }
  }

  // Rebuild (§13): regenerate from the current purpose, show the diff first.
  const rebuild = async () => {
    if (!form.description?.trim()) return
    setRegenBusy(true); setError('')
    try {
      // Rebuild is always a final round: make assumptions, no new questions.
      const draft = await workspaceApi.draftAgent(form.description.trim(), true)
      setRegenDraft(draft)
    } catch (f) { setError(message(f)) }
    finally { setRegenBusy(false) }
  }

  const builtinFor = (agent?: Partial<Agent>) =>
    agent?.name ? builtins.find((b) => b.name === agent.name) : undefined

  const applyBuiltin = (spec: BuiltinAgentSpec) => {
    setForm((prev) => ({
      ...prev,
      description: spec.description,
      definition: { ...spec.definition },
      network_access: spec.network_access,
      read_only: spec.read_only,
    }))
    setProvenance((prev) => ({ ...prev, identity: 'agent', capabilities: 'agent', constraints: 'agent', completion: 'agent' }))
  }

  const capLabel = (key: CapKey) => t(`agents.cap.${key}`) ?? key

  // Capabilities diff for the regen proposal: disabled caps plus delegation
  // (opt-in, so it is only listed when granted).
  const capSummary = (caps?: AgentCapabilities) => {
    const parts = disabledCaps(caps).map(capLabel)
    if (caps?.delegation === true) parts.push(t('agents.cap.delegation') ?? 'Delegate to other agents')
    return parts.join(', ') || (t('agents.all_caps') ?? 'all allowed')
  }

  const regenRows = regenDraft
    ? [
        { section: 'identity' as SectionKey, label: t('agents.name') ?? 'Name', current: form.name ?? '', proposed: regenDraft.name },
        { section: 'identity' as SectionKey, label: t('agents.purpose') ?? 'Purpose', current: form.description ?? '', proposed: regenDraft.description },
        {
          section: 'capabilities' as SectionKey,
          label: t('agents.capabilities') ?? 'Capabilities',
          current: capSummary(form.definition?.capabilities),
          proposed: capSummary(regenDraft.capabilities),
        },
        {
          section: 'constraints' as SectionKey,
          label: t('agents.constraints') ?? 'Constraints',
          current: cleanList(form.definition?.constraints).join('\n'),
          proposed: cleanList(regenDraft.constraints).join('\n'),
        },
        {
          section: 'completion' as SectionKey,
          label: t('agents.completion') ?? 'Completion',
          current: cleanList(form.definition?.completion).join('\n'),
          proposed: cleanList(regenDraft.completion).join('\n'),
        },
      ].filter((row) => row.current !== row.proposed)
    : []

  const acceptRegen = () => {
    if (!regenDraft) return
    setForm((prev) => ({
      ...prev,
      name: regenDraft.name || prev.name,
      description: regenDraft.description || prev.description,
      definition: {
        ...prev.definition,
        capabilities: setCaps(regenDraft.capabilities),
        constraints: cleanList(regenDraft.constraints),
        completion: cleanList(regenDraft.completion),
      },
      model: regenDraft.suggested?.model || prev.model,
    }))
    setProvenance((prev) => ({
      ...prev,
      identity: 'agent',
      capabilities: 'agent',
      constraints: 'agent',
      completion: 'agent',
    }))
    setRegenDraft(null)
  }

  const promptSourceLabel = (source: string) =>
    source === 'override' ? (t('agents.source_override') ?? 'manual override')
      : source === 'definition' ? (t('agents.source_definition') ?? 'compiled from definition')
        : source === 'legacy' ? (t('agents.source_legacy') ?? 'manual prompt (legacy)')
          : (t('agents.source_role') ?? 'base contract')

  // The human network-access level (docs/agent-behavior-monitoring.md §7):
  // derived from the stored pair so the radio tiles reflect any agent —
  // including ones configured through the API before this UI existed. The
  // override exists because "allowlist with an empty list" derives back to
  // "off", which would make the level unselectable exactly when the user
  // needs to start typing entries into it. Reset whenever a fresh form loads.
  const [egressLevelOverride, setEgressLevelOverride] = useState<string | null>(null)
  // The allowlist textarea keeps its own DRAFT text: filtering empty lines on
  // every keystroke fought the Enter key (a new empty line was removed before
  // the user could type into it). The draft parses back into the form on blur
  // and on save.
  const [egressDraft, setEgressDraft] = useState('')
  const syncEgressDraft = (list: string[] | undefined) => setEgressDraft((list ?? []).join('\n'))
  const parseEgressDraft = (): string[] =>
    egressDraft.split('\n').map((line) => line.trim()).filter(Boolean)
  const egressLevel = egressLevelOverride
    ?? (form.network_access ? 'public' : (form.egress_allowlist ?? []).length > 0 ? 'allowlist' : 'off')

  return (
    <div style={{ padding: '1rem' }}>
      {confirmDialogNode}
      {bulkOpen && (
        <BulkAgentDialog
          agentIds={[...bulkSelected]}
          providers={providers}
          sandboxProfiles={sandboxProfiles}
          secrets={secretsList.filter((s) => s.enabled !== false)}
          mcpServers={mcpServers}
          onClose={() => setBulkOpen(false)}
          onApplied={() => {
            setBulkOpen(false)
            setBulkMode(false)
            setBulkSelected(new Set())
            void load()
          }}
          t={t}
        />
      )}
      {transferReport && (
        <AppModal onClose={() => setTransferReport(null)} panelStyle={{ width: '560px' }}>
          <Heading style={{ fontSize: '1.05rem', marginBottom: '0.75rem' }}>{t('skills.transfer_report_title') ?? 'Transfer report'}</Heading>
          <div style={{ display: 'grid', gap: '0.75rem', fontSize: '0.8rem' }}>
            {([['created', transferReport.created, 'green'], ['updated', transferReport.updated, 'blue'], ['skipped', transferReport.skipped, 'gray'], ['errors', transferReport.errors, 'red']] as const).map(([label, items, tagType]) => (
              items.length > 0 && (
                <div key={label}>
                  <div style={{ marginBottom: '0.25rem' }}><Tag size="sm" type={tagType}>{t(`skills.transfer_${label}`) ?? label} · {items.length}</Tag></div>
                  {items.map((item: TransferResultItem) => (
                    <div key={item.id + (item.detail ?? '')} style={{ color: 'var(--tm-text-2)', padding: '0.1rem 0' }}>
                      <strong>{item.name || item.id}</strong>{item.detail && <span style={{ color: 'var(--tm-text-3)' }}> — {item.detail}</span>}
                    </div>
                  ))}
                </div>
              )
            ))}
          </div>
          <div style={{ display: 'flex', justifyContent: 'flex-end', marginTop: '1rem' }}>
            <Button onClick={() => setTransferReport(null)}>{t('action.close') ?? 'Close'}</Button>
          </div>
        </AppModal>
      )}
      {error && <InlineNotification kind="error" title={t('action.error') ?? 'Error'} subtitle={error} onClose={() => setError('')} lowContrast style={{ marginBottom: '1rem' }} />}

      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1rem' }}>
        <Heading>{t('agents.title') ?? 'Agents'}</Heading>
        <Stack orientation="horizontal" gap={2}>
          {/* Stand-to-stand transfer: agents export/import (admin endpoints —
              readers see an error bubble from the 403). */}
          <input
            ref={transferFileRef}
            type="file"
            accept=".json,application/json"
            style={{ display: 'none' }}
            onChange={(e) => {
              const file = e.target.files?.[0]
              e.target.value = ''
              if (file) void runTransferImport(file)
            }}
          />
          <Button kind="ghost" disabled={transferBusy} onClick={() => void runTransferExport()}>{t('agents.transfer_export') ?? 'Export…'}</Button>
          <Button kind="ghost" disabled={transferBusy} onClick={() => transferFileRef.current?.click()}>{t('agents.transfer_import') ?? 'Import…'}</Button>
          <Button kind="ghost" renderIcon={Settings} iconDescription={t('quality.show_settings') ?? 'Quality settings'} onClick={() => setQualityOpen(true)}>{t('quality.show_settings') ?? 'Quality settings'}</Button>
          <Button kind="secondary" onClick={() => setShowTemplates(true)}>{t('agents.from_template') ?? 'From template'}</Button>
          <Button renderIcon={Add} onClick={openWizard}>{t('agents.new_agent') ?? 'New Agent'}</Button>
        </Stack>
      </div>

      {/* Master-detail: agent list on the left, full profile with tabs on the right */}
      <Grid style={{ rowGap: '1.5rem' }}>
        {/* Agent list */}
        <Column sm={4} md={3} lg={4}>
          <div style={{ position: 'sticky', top: '3rem', maxHeight: 'calc(100vh - 4rem)', overflowY: 'auto' }}>
            <div style={{ padding: '0.5rem 0 0', borderBottom: '1px solid var(--tm-border)', position: 'sticky', top: 0, background: 'var(--tm-bg)', zIndex: 1 }}>
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '0.25rem' }}>
                <Heading style={{ fontSize: '1rem' }}>{t('agents.count', { count: String(filteredAgents.length) }) ?? `Agents (${filteredAgents.length})`}</Heading>
                <button
                  type="button"
                  onClick={() => { setBulkMode(!bulkMode); setBulkSelected(new Set()) }}
                  style={{ background: 'none', border: '1px solid var(--tm-border)', borderRadius: '4px', color: bulkMode ? 'var(--tm-amber)' : 'var(--tm-text-2)', cursor: 'pointer', fontSize: '0.7rem', padding: '0.15rem 0.5rem' }}
                  title={t('agents.bulk_hint') ?? 'Select agents and apply provider/model, sandbox or secrets to all of them at once'}
                >{t('agents.bulk_mode') ?? 'Массовая настройка'}</button>
              </div>
              <ListFilter value={filter} onChange={setFilter} placeholder={t('common.filter_agents') ?? 'Filter agents…'} />
            </div>
            <Stack gap={1}>
              {filteredAgents.length === 0 && <Tile style={{ color: 'var(--tm-text-3)', textAlign: 'center' }}>{t('agents.none_created') ?? 'No agents yet'}</Tile>}
              {bulkMode && bulkSelected.size > 0 && (
                <Button size="sm" style={{ marginTop: '0.5rem' }} onClick={() => setBulkOpen(true)}>
                  {t('agents.bulk_configure', { count: String(bulkSelected.size) }) ?? `Настроить выбранные (${bulkSelected.size})`}
                </Button>
              )}
              {filteredAgents.map((a) => (
                <Tile
                  key={a.id}
                  onClick={() => (bulkMode ? toggleBulk(a.id) : setSelectedId(a.id))}
                  className={`workspace-tile ${selectedId === a.id ? 'selected' : ''} ${bulkMode && bulkSelected.has(a.id) ? 'bulk-selected' : ''}`}
                  style={{ padding: '0.5rem 0.75rem', cursor: 'pointer', opacity: a.enabled === false ? 0.5 : 1, borderLeft: bulkMode && bulkSelected.has(a.id) ? '3px solid var(--tm-amber)' : undefined }}
                >
                  <div style={{ display: 'flex', alignItems: 'center', gap: '0.4rem' }}>
                    {bulkMode && (
                      <input
                        type="checkbox"
                        readOnly
                        checked={bulkSelected.has(a.id)}
                        onClick={(e) => { e.stopPropagation(); toggleBulk(a.id) }}
                        style={{ cursor: 'pointer', accentColor: 'var(--tm-amber)' }}
                      />
                    )}
                    <span style={{ fontWeight: 500, fontSize: '0.8rem', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', flex: 1, minWidth: 0 }}>{a.name}</span>
                    {(pendingProposals[a.id] ?? 0) > 0 && (
                      <span title={t('agents.pending_proposals_hint', { count: String(pendingProposals[a.id]) })}>
                        <Tag type="purple" size="sm">⚡ {pendingProposals[a.id]}</Tag>
                      </span>
                    )}
                    {degradedAgents.has(a.id) && (
                      <span
                        title={t('agents.degraded_hint') ?? 'The current version is degrading — see the Evolution tab'}
                        style={{ width: '8px', height: '8px', borderRadius: '50%', background: 'var(--tm-danger, #f7768e)', flexShrink: 0 }}
                      />
                    )}
                    <OrgBadge orgUnitID={a.org_unit_id} org={org} />
                    {a.id === defaultAgentId && <Tag type="green" size="sm">{t('agents.default') ?? 'default'}</Tag>}
                  </div>
                  <div style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)', display: 'flex', gap: '0.25rem', flexWrap: 'wrap', marginTop: '0.15rem' }}>
                    <span>{a.model || (t('agents.no_model') ?? 'no model')}</span>
                    {a.provider && <span>· {providers.find((p) => p.id === a.provider)?.name ?? a.provider}</span>}
                    {!a.provider && (
                      <span style={{ color: 'var(--tm-danger)' }} title={t('agents.no_provider_hint') ?? 'Без провайдера агент не запускается — выберите провайдера в карточке'}>
                        · ⚠ {t('agents.no_provider') ?? 'без провайдера'}
                      </span>
                    )}
                    {a.labels?.builtin === 'true' && <span>· {t('agents.builtin_tag') ?? 'builtin'}</span>}
                    {!!a.definition_version && <span>· v{a.definition_version}</span>}
                  </div>
                  {/* The uniform switch, bottom-right of the mini-card (the
                      trigger pattern): a disabled agent starts no new runs. */}
                  <div style={{ display: 'flex', justifyContent: 'flex-end', marginTop: '0.3rem' }} title={t('agents.enabled_hint') ?? 'Disabled agents cannot start new runs'} onClick={(e) => e.stopPropagation()}>
                    <Toggle
                      id={`agent-enabled-${a.id}`}
                      labelText=""
                      hideLabel
                      aria-label={t('agents.enabled_hint') ?? 'Toggle agent'}
                      toggled={a.enabled !== false}
                      onToggle={() => void toggleAgentEnabled(a)}
                      size="sm"
                    />
                  </div>
                </Tile>
              ))}
            </Stack>
          </div>
        </Column>

        {/* Detail pane */}
        <Column sm={4} md={5} lg={12}>
          {!selected ? (
            <div style={{ textAlign: 'center', color: 'var(--tm-text-3)', padding: '3rem 1rem' }}>
              <Heading>{t('agents.select') ?? 'Select an agent'}</Heading>
              <p style={{ marginTop: '0.5rem' }}>{t('agents.select_hint') ?? 'Choose an agent on the left to see its definition, compiled prompt and evolution.'}</p>
            </div>
          ) : (
            <Stack gap={3}>
              {/* Profile header */}
              <div style={{ display: 'flex', alignItems: 'flex-start', gap: '0.75rem', flexWrap: 'wrap' }}>
                <div style={{ flex: 1, minWidth: 0 }}>
                  <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', flexWrap: 'wrap' }}>
                    <Heading style={{ fontSize: '1.1rem' }}>{selected.name}</Heading>
                    <OrgBadge orgUnitID={selected.org_unit_id} org={org} />
                    {selected.id === defaultAgentId && <Tag type="green" size="sm">{t('agents.project_default') ?? 'project default'}</Tag>}
                    {selected.labels?.builtin === 'true' && <Tag type="warm-gray" size="sm">{t('agents.builtin_tag') ?? 'builtin'}</Tag>}
                    {!!selected.definition_version && <Tag type="purple" size="sm">v{selected.definition_version}</Tag>}
                  </div>
                  {selected.description && <p style={{ color: 'var(--tm-text-2)', margin: '0.35rem 0 0', fontSize: '0.85rem' }}>{selected.description}</p>}
                </div>
                <Stack orientation="horizontal" gap={1} style={{ flexShrink: 0 }}>
                  {project && selected.id !== defaultAgentId && (
                    <Button size="sm" kind="ghost" hasIconOnly renderIcon={Star} iconDescription={t('agents.make_default') ?? 'Make default'} title={t('agents.make_default') ?? 'Make default'} onClick={() => void makeDefault(selected)} />
                  )}
                  <Button size="sm" kind="ghost" hasIconOnly renderIcon={Edit} iconDescription={t('action.edit') ?? 'Edit'} onClick={() => startEdit(selected)} />
                  <Button size="sm" kind="ghost" hasIconOnly renderIcon={Copy} iconDescription={t('action.duplicate') ?? 'Duplicate'} onClick={() => duplicateAgent(selected)} />
                  <Button size="sm" kind="danger--ghost" hasIconOnly renderIcon={TrashCan} iconDescription={t('action.delete') ?? 'Delete'} onClick={() => void deleteAgent(selected)} />
                </Stack>
              </div>

              {/* Detail tabs */}
              <div className="skill-tabs" role="tablist" style={{ padding: 0 }}>
                <button role="tab" aria-selected={detailTab === 'overview'} className={`skill-tab ${detailTab === 'overview' ? 'active' : ''}`} onClick={() => setDetailTab('overview')}>
                  {t('agents.tab_overview') ?? 'Overview'}
                </button>
                <button role="tab" aria-selected={detailTab === 'evolution'} className={`skill-tab ${detailTab === 'evolution' ? 'active' : ''}`} onClick={() => setDetailTab('evolution')}>
                  {t('agents.evolution') ?? 'Evolution'}
                  {selectedPending > 0 && <span className="tune-badge" style={{ marginLeft: '0.35rem' }}>{selectedPending}</span>}
                </button>
              </div>

              {detailTab === 'overview' && (
                <Stack gap={4}>
                  <div>
                    <p className="cds--label" style={{ marginBottom: '0.25rem' }}>{t('agents.purpose') ?? 'Purpose'}</p>
                    <p style={{ margin: 0 }}>{selected.description || <span style={{ color: 'var(--tm-text-3)' }}>{t('agents.none') ?? '(none)'}</span>}</p>
                  </div>
                  <div>
                    <p className="cds--label" style={{ marginBottom: '0.25rem' }}>{t('agents.can') ?? 'Can'}</p>
                    <div style={{ display: 'flex', gap: '0.25rem', flexWrap: 'wrap' }}>
                      {enabledCaps(selected.definition?.capabilities).map((key) => <Tag key={key} type="green" size="sm">{capLabel(key)}</Tag>)}
                      {selected.definition?.capabilities?.delegation === true && <Tag type="green" size="sm">{t('agents.cap.delegation') ?? 'Delegate to other agents'}</Tag>}
                      {selected.definition?.capabilities?.triggers === true && <Tag type="green" size="sm">{t('agents.cap.triggers') ?? 'Create triggers'}</Tag>}
                      {enabledCaps(selected.definition?.capabilities).length === 0 && disabledCaps(selected.definition?.capabilities).length === 0 && (
                        <span style={{ color: 'var(--tm-text-3)' }}>{t('agents.all_caps') ?? 'All capabilities allowed'}</span>
                      )}
                    </div>
                  </div>
                  {(disabledCaps(selected.definition?.capabilities).length > 0 || cleanList(selected.definition?.constraints).length > 0) && (
                    <div>
                      <p className="cds--label" style={{ marginBottom: '0.25rem' }}>{t('agents.cannot') ?? 'Cannot'}</p>
                      <div style={{ display: 'flex', gap: '0.25rem', flexWrap: 'wrap', marginBottom: cleanList(selected.definition?.constraints).length ? '0.25rem' : 0 }}>
                        {disabledCaps(selected.definition?.capabilities).map((key) => <Tag key={key} type="red" size="sm">{capLabel(key)}</Tag>)}
                      </div>
                      <ul style={{ margin: '0.25rem 0 0', paddingLeft: '1rem', color: 'var(--tm-text-2)' }}>
                        {cleanList(selected.definition?.constraints).map((c, i) => <li key={i}>{c}</li>)}
                      </ul>
                    </div>
                  )}
                  {cleanList(selected.definition?.completion).length > 0 && (
                    <div>
                      <p className="cds--label" style={{ marginBottom: '0.25rem' }}>{t('agents.before_done') ?? 'Before declaring done'}</p>
                      <ul style={{ margin: 0, paddingLeft: '1rem', color: 'var(--tm-text-2)' }}>
                        {cleanList(selected.definition?.completion).map((c, i) => <li key={i}>{c}</li>)}
                      </ul>
                    </div>
                  )}
                  <div>
                    <p className="cds--label" style={{ marginBottom: '0.25rem' }}>{t('agents.system_prompt') ?? 'System prompt'}</p>
                    {promptFor[selected.id] ? (
                      <>
                        <Tag type="gray" size="sm" style={{ marginBottom: '0.25rem' }}>{promptSourceLabel(promptFor[selected.id].source)}</Tag>
                        <pre style={{ background: 'var(--tm-elevated)', border: '1px solid var(--tm-border)', borderRadius: '6px', padding: '0.5rem 0.75rem', fontFamily: 'var(--tm-mono, monospace)', fontSize: '0.72rem', whiteSpace: 'pre-wrap', wordBreak: 'break-word', maxHeight: '14rem', overflow: 'auto', margin: 0 }}>
                          {promptFor[selected.id].prompt || (t('agents.source_role') ?? 'base contract')}
                        </pre>
                      </>
                    ) : (
                      <p style={{ color: 'var(--tm-text-3)' }}>{t('agents.none') ?? '(none)'}</p>
                    )}
                  </div>
                </Stack>
              )}

              {detailTab === 'evolution' && (() => {
                const fmtMs = (ms: number) => formatDuration(ms, t)
                const currentVersion = currentVersionOf(versions)
                const sliLabel = (metric: string) => {
                  const key = `agents.sli.${metric}`
                  const label = t(key)
                  return label === key ? metric : label
                }
                const verdictLabel = (verdict: string | null | undefined) => {
                  const key = `agents.verdict_${verdict}`
                  const label = verdict ? t(key) : (t('agents.verdict_pending') ?? 'observing')
                  return verdict && label === key ? verdict : label
                }
                const sourceLabel = (source: string) => {
                  const key = `agents.source_${source}`
                  const label = t(key)
                  return label === key ? source : label
                }
                // The «сейчас / база / Δ» table (§6.1) renders the delta rows;
                // when the rollup leaves cost/time out of delta[] (§5 carries
                // them as per-window averages) the informational rows fall
                // back to the window summaries.
                const deltaRows = metrics?.delta ?? []
                const tableRows: typeof deltaRows = [...deltaRows]
                if (!deltaRows.some((row) => isCostTimeMetric(row.metric)) && metrics?.current) {
                  const cur = metrics.current
                  const base = metrics.baseline
                  tableRows.push(
                    { metric: 'tokens_avg', current: cur.tokens_avg ?? 0, baseline: base?.tokens_avg ?? 0, delta: (cur.tokens_avg ?? 0) - (base?.tokens_avg ?? 0), signal: 'noise' },
                    { metric: 'cost_usd_avg', current: cur.cost_usd_avg ?? 0, baseline: base?.cost_usd_avg ?? 0, delta: (cur.cost_usd_avg ?? 0) - (base?.cost_usd_avg ?? 0), signal: 'noise' },
                    { metric: 'wall_ms_avg', current: cur.wall_ms_avg ?? 0, baseline: base?.wall_ms_avg ?? 0, delta: (cur.wall_ms_avg ?? 0) - (base?.wall_ms_avg ?? 0), signal: 'noise' },
                  )
                }
                return (
                <div>
                  {/* Degradation alert (§3.2, §6): the current version loses
                      ground against its baseline — rollback is one click in
                      the timeline below. */}
                  {metrics?.observation?.verdict === 'degraded' && (
                    <InlineNotification
                      kind="error"
                      title={verdictLabel('degraded')}
                      subtitle={t('agents.degraded_banner') ?? 'The current version is degrading against its baseline on real runs. Review the SLIs and consider a rollback.'}
                      lowContrast
                      style={{ marginBottom: '1rem' }}
                    />
                  )}
                  {proposalNotice && (
                    <InlineNotification
                      kind={proposalNotice.kind === 'error' ? 'error' : proposalNotice.kind === 'success' ? 'success' : 'info'}
                      title={proposalNotice.kind === 'error' ? (t('action.error') ?? 'Error') : proposalNotice.kind === 'success' ? (t('action.done') ?? 'Done') : (t('action.info') ?? 'Info')}
                      subtitle={proposalNotice.text}
                      onClose={() => setProposalNotice(null)}
                      lowContrast
                      style={{ marginBottom: '0.75rem' }}
                    />
                  )}

                  {/* Health card of the current version (§6.1): observation
                      progress, verdict and the SLI table against the baseline. */}
                  <div style={{ marginBottom: '1.5rem', borderBottom: '1px solid var(--tm-border)', paddingBottom: '1rem' }}>
                    <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '0.5rem', flexWrap: 'wrap', gap: '0.5rem' }}>
                      <div className="skill-subheading" style={{ margin: 0 }}>
                        {t('agents.health_title') ?? 'Current version health'}
                        {metrics && (
                          <Tag size="sm" type={verdictTagType(metrics.observation?.verdict)} style={{ marginLeft: '0.5rem' }}>
                            {verdictLabel(metrics.observation?.verdict)}
                          </Tag>
                        )}
                      </div>
                      <Button size="sm" kind="secondary" onClick={() => setImproveOpen(true)}>
                        {t('agents.propose_improvement') ?? 'Suggest an improvement'}
                      </Button>
                    </div>
                    {!metrics ? (
                      <p style={{ color: 'var(--tm-text-3)', fontSize: '0.8rem', margin: 0 }}>
                        {t('agents.metrics_empty') ?? 'No metrics yet — the current version needs runs under observation.'}
                      </p>
                    ) : (
                      <>
                        <p style={{ margin: '0 0 0.5rem', fontSize: '0.8rem', color: 'var(--tm-text-2)' }}>
                          {t('agents.obs_progress', {
                            elapsed: String(metrics.observation?.elapsed_days ?? 0),
                            days: String(metrics.observation?.days ?? observation?.days ?? 0),
                            runs: String(metrics.observation?.runs ?? 0),
                            min: String(metrics.observation?.min_runs ?? observation?.min_runs ?? 0),
                          }) ?? 'observation in progress'}
                        </p>
                        {tableRows.length === 0 ? (
                          <p style={{ color: 'var(--tm-text-3)', fontSize: '0.8rem', margin: 0 }}>
                            {t('agents.metrics_empty') ?? 'No metrics yet — the current version needs runs under observation.'}
                          </p>
                        ) : (
                          <div style={{ overflowX: 'auto' }}>
                            <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: '0.78rem' }}>
                              <thead>
                                <tr style={{ color: 'var(--tm-text-3)', textAlign: 'left' }}>
                                  <th style={{ padding: '0.2rem 0.5rem 0.2rem 0', fontWeight: 500 }}>SLI</th>
                                  <th style={{ padding: '0.2rem 0.5rem', fontWeight: 500 }}>{t('agents.sli_current') ?? 'now'}</th>
                                  <th style={{ padding: '0.2rem 0.5rem', fontWeight: 500 }}>{t('agents.sli_baseline') ?? 'baseline'}</th>
                                  <th style={{ padding: '0.2rem 0.5rem', fontWeight: 500 }}>{t('agents.sli_delta') ?? 'Δ'}</th>
                                  <th style={{ padding: '0.2rem 0 0.2rem 0.5rem', fontWeight: 500 }}>{t('agents.sli_mark') ?? 'mark'}</th>
                                </tr>
                              </thead>
                              <tbody>
                                {tableRows.map((row) => {
                                  const informational = isCostTimeMetric(row.metric)
                                  const bad = isLowerBetter(row.metric) ? row.delta > 0 : row.delta < 0
                                  return (
                                    <tr key={row.metric} style={{ borderTop: '1px solid var(--tm-border)' }}>
                                      <td style={{ padding: '0.3rem 0.5rem 0.3rem 0' }}>{sliLabel(row.metric)}</td>
                                      <td style={{ padding: '0.3rem 0.5rem', whiteSpace: 'nowrap' }}>{metricValueText(row.metric, row.current, fmtMs)}</td>
                                      <td style={{ padding: '0.3rem 0.5rem', whiteSpace: 'nowrap', color: 'var(--tm-text-2)' }}>{metricValueText(row.metric, row.baseline, fmtMs)}</td>
                                      <td style={{ padding: '0.3rem 0.5rem', whiteSpace: 'nowrap', color: row.delta === 0 ? 'var(--tm-text-3)' : bad ? 'var(--tm-danger, #f7768e)' : 'var(--tm-success, #9ece6a)' }}>
                                        {metricDeltaText(row.metric, row.delta, fmtMs)}
                                      </td>
                                      <td style={{ padding: '0.3rem 0 0.3rem 0.5rem' }}>
                                        {informational ? (
                                          <Tag size="sm" type="warm-gray">{t('agents.sli_mark_info') ?? 'info'}</Tag>
                                        ) : (
                                          <Tag size="sm" type={isSignalMark(row.signal) ? 'red' : 'gray'}>
                                            {isSignalMark(row.signal) ? (t('agents.sli_mark_signal') ?? 'signal') : (t('agents.sli_mark_noise') ?? 'noise')}
                                          </Tag>
                                        )}
                                      </td>
                                    </tr>
                                  )
                                })}
                              </tbody>
                            </table>
                          </div>
                        )}
                      </>
                    )}
                  </div>

                  {/* Proposals inbox (§6.2): source-tagged drafts with the
                      behavioral summary; apply is one button, always green. */}
                  <div style={{ marginBottom: '1.5rem', borderBottom: '1px solid var(--tm-border)', paddingBottom: '1rem' }}>
                    <div className="skill-subheading" style={{ margin: '0 0 0.5rem' }}>{t('agents.proposals') ?? 'Proposals'}</div>
                    {proposals.length === 0 ? (
                      <p style={{ color: 'var(--tm-text-3)', fontSize: '0.8rem', margin: 0 }}>
                        {t('agents.proposals_empty') ?? 'No proposals yet. Agents file them with the agent_propose tool; changes go live only after a human applies them.'}
                      </p>
                    ) : (
                      <div style={{ display: 'flex', flexDirection: 'column', gap: '0.5rem' }}>
                        {proposals.map((p) => {
                          const pending = p.status === 'pending'
                          return (
                            <div key={p.id} style={{ border: '1px solid var(--tm-border)', borderRadius: '6px', padding: '0.6rem 0.75rem', opacity: pending ? 1 : 0.65 }}>
                              <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', flexWrap: 'wrap' }}>
                                <Tag size="sm" type={p.status === 'applied' ? 'green' : p.status === 'rejected' ? 'red' : 'purple'}>{t(`agents.proposal_${p.status}`) ?? p.status}</Tag>
                                <Tag size="sm" type="cyan">{sourceLabel(String(p.source ?? ''))}</Tag>
                                <span style={{ fontSize: '0.75rem', color: 'var(--tm-text-3)' }}>
                                  {t('agents.proposal_base', { version: String(p.base_version) }) ?? `based on v${p.base_version}`} · {new Date(p.created_at).toLocaleString()}{p.author ? ` · ${p.author}` : ''}
                                </span>
                              </div>
                              <button
                                type="button"
                                onClick={() => setDetailProposal(p)}
                                title={t('agents.proposal_details_hint') ?? 'Open the change summary and the diff'}
                                style={{ display: 'block', textAlign: 'left', background: 'none', border: 'none', padding: '0.35rem 0 0', color: 'inherit', font: 'inherit', cursor: 'pointer', textDecoration: 'underline', textDecorationColor: 'var(--tm-border)', textUnderlineOffset: '3px' }}
                              >
                                {p.change_summary || (t('agents.none') ?? '(none)')}
                              </button>
                              {p.context?.run_id && (
                                <p style={{ margin: '0.25rem 0 0', fontSize: '0.75rem', color: 'var(--tm-text-3)' }}>
                                  {t('agents.proposal_from_run', { run: p.context.run_id }) ?? `from run ${p.context.run_id}`}
                                </p>
                              )}
                              {p.status === 'applied' && !!p.applied_version && (
                                <p style={{ margin: '0.25rem 0 0', fontSize: '0.75rem', color: 'var(--tm-text-3)' }}>
                                  {t('agents.proposal_applied_in', { version: String(p.applied_version) }) ?? `applied in v${p.applied_version}`}
                                </p>
                              )}
                              {pending ? (
                                <div style={{ display: 'flex', gap: '0.5rem', marginTop: '0.5rem', flexWrap: 'wrap' }}>
                                  <Button
                                    size="sm"
                                    disabled={proposalBusy}
                                    onClick={() => {
                                      void (async () => {
                                        if (await confirmDialog({ title: t('agents.proposal_apply_confirm_title') ?? 'Apply proposal?', body: t('agents.proposal_apply_confirm') ?? 'The definition version will increase and a snapshot will be stored.', confirmLabel: t('agents.proposal_apply') ?? 'Apply' })) void applyProposal(p)
                                      })()
                                    }}
                                  >
                                    {t('agents.proposal_apply') ?? 'Apply'}
                                  </Button>
                                  <Button
                                    size="sm"
                                    kind="secondary"
                                    disabled={proposalBusy}
                                    onClick={() => {
                                      void (async () => {
                                        if (await confirmDialog({ title: t('agents.proposal_reject_confirm_title') ?? 'Reject proposal?', body: t('agents.proposal_reject_confirm') ?? 'The proposal stays in history and can never be applied.', danger: true, confirmLabel: t('agents.proposal_reject') ?? 'Reject' })) void rejectProposal(p)
                                      })()
                                    }}
                                  >
                                    {t('agents.proposal_reject') ?? 'Reject'}
                                  </Button>
                                  <Button
                                    size="sm"
                                    kind="ghost"
                                    disabled={checkBusy}
                                    onClick={() => void runCheckCases({ proposal: p.id })}
                                  >
                                    {t('agents.check_run') ?? 'Check on examples'}
                                  </Button>
                                </div>
                              ) : (
                                p.decided_by && (
                                  <p style={{ margin: '0.3rem 0 0', fontSize: '0.75rem', color: 'var(--tm-text-3)' }}>
                                    {t('agents.proposal_decided_by') ?? 'decided by'} {p.decided_by}{p.decided_at ? ` · ${new Date(p.decided_at).toLocaleString()}` : ''}
                                  </p>
                                )
                              )}
                            </div>
                          )
                        })}
                      </div>
                    )}
                  </div>

                  {/* Version timeline (§6.4): how every version came to be and
                      what its observation window concluded. */}
                  <div style={{ marginBottom: '1.5rem', borderBottom: '1px solid var(--tm-border)', paddingBottom: '1rem' }}>
                    <div className="skill-subheading" style={{ margin: '0 0 0.5rem' }}>{t('agents.versions_title') ?? 'Versions'}</div>
                    {versions.length === 0 ? (
                      <p style={{ color: 'var(--tm-text-3)', fontSize: '0.8rem', margin: 0 }}>
                        {t('agents.evolution_empty') ?? 'No definition versions yet. Versions appear when you edit the agent’s definition.'}
                      </p>
                    ) : (
                      [...versions].sort((a, b) => b.version - a.version).map((v) => {
                        const isCurrent = v.version === currentVersion
                        const kindKey = versionKindLabelKey(v.kind ?? '')
                        return (
                          <div key={v.version} style={{ borderLeft: '2px solid var(--tm-border)', paddingLeft: '0.75rem', marginBottom: '1rem' }}>
                            <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', flexWrap: 'wrap' }}>
                              <strong>v{v.version}</strong>
                              {isCurrent && <Tag type="green" size="sm">{t('agents.version_current') ?? 'current'}</Tag>}
                              <Tag type={versionKindTagType(v.kind ?? '')} size="sm">{kindKey ? t(kindKey) : v.kind}</Tag>
                              {!!v.verdict && <Tag type={verdictTagType(v.verdict)} size="sm">{verdictLabel(v.verdict)}</Tag>}
                              <span style={{ fontSize: '0.75rem', color: 'var(--tm-text-3)' }}>
                                {new Date(v.created_at).toLocaleString()}{v.author ? ` · ${v.author}` : ''}
                              </span>
                            </div>
                            {v.change_summary && (
                              <p style={{ margin: '0.35rem 0 0', fontSize: '0.8rem' }}>{v.change_summary}</p>
                            )}
                            {v.kind === 'rollback' && !!v.rollback_of && (
                              <p style={{ margin: '0.25rem 0 0', fontSize: '0.75rem', color: 'var(--tm-text-3)' }}>
                                {t('agents.rollback_of', { version: String(v.rollback_of) }) ?? `rollback of v${v.rollback_of}`}
                              </p>
                            )}
                            <div style={{ display: 'flex', gap: '0.5rem', marginTop: '0.4rem', flexWrap: 'wrap' }}>
                              <Button size="sm" kind="secondary" disabled={compareBusy} onClick={() => void compareVersion(v)}>
                                {t('agents.compare') ?? 'Compare'}
                              </Button>
                              {!isCurrent && (
                                <Button
                                  size="sm"
                                  kind="secondary"
                                  disabled={proposalBusy}
                                  onClick={() => {
                                    void (async () => {
                                      if (await confirmDialog({
                                        title: t('agents.rollback_title', { version: String(v.version) }) ?? `Roll back to v${v.version}?`,
                                        body: t('agents.rollback_body', { version: String(v.version) }) ?? 'The snapshot\'s definition and purpose return as a NEW version — history stays append-only and the rollback itself is revertible.',
                                        confirmLabel: t('agents.rollback_action') ?? 'Roll back',
                                      })) void rollbackToVersion(v.version)
                                    })()
                                  }}
                                >
                                  {t('agents.rollback_action') ?? 'Roll back to this version'}
                                </Button>
                              )}
                            </div>
                          </div>
                        )
                      })
                    )}
                  </div>

                  {/* Observation window (§6.5): both bounds must be reached
                      before the window closes and the verdict lands. */}
                  <div style={{ marginBottom: '1.5rem', borderBottom: '1px solid var(--tm-border)', paddingBottom: '1rem' }}>
                    <div className="skill-subheading" style={{ margin: '0 0 0.35rem' }}>{t('agents.obs_settings') ?? 'Observation window'}</div>
                    <p style={{ fontSize: '0.75rem', color: 'var(--tm-text-3)', margin: '0 0 0.5rem' }}>
                      {t('agents.obs_settings_hint') ?? 'The window closes when both bounds are reached: the day count and the minimum number of runs of the version. A quiet agent gets a longer window and fewer runs.'}
                    </p>
                    <div style={{ display: 'flex', gap: '0.75rem', alignItems: 'flex-end', flexWrap: 'wrap' }}>
                      <label style={{ display: 'block', width: '8rem' }}>
                        <span style={{ display: 'block', fontSize: '0.72rem', color: 'var(--tm-text-3)', margin: '0 0 0.25rem' }}>{t('agents.obs_days') ?? 'Days'}</span>
                        <input
                          type="number"
                          onFocus={selectOnFocus}
                          min={1}
                          max={90}
                          value={obsDraft.days}
                          onChange={(e) => setObsDraft({ ...obsDraft, days: e.target.value })}
                          style={{ width: '100%', border: '1px solid var(--tm-border)', borderRadius: '6px', background: 'var(--tm-bg)', color: 'var(--tm-text)', fontSize: '0.8rem', padding: '0.4rem 0.6rem', outline: 'none' }}
                        />
                      </label>
                      <label style={{ display: 'block', width: '8rem' }}>
                        <span style={{ display: 'block', fontSize: '0.72rem', color: 'var(--tm-text-3)', margin: '0 0 0.25rem' }}>{t('agents.obs_min_runs') ?? 'Min runs'}</span>
                        <input
                          type="number"
                          onFocus={selectOnFocus}
                          min={1}
                          max={100}
                          value={obsDraft.minRuns}
                          onChange={(e) => setObsDraft({ ...obsDraft, minRuns: e.target.value })}
                          style={{ width: '100%', border: '1px solid var(--tm-border)', borderRadius: '6px', background: 'var(--tm-bg)', color: 'var(--tm-text)', fontSize: '0.8rem', padding: '0.4rem 0.6rem', outline: 'none' }}
                        />
                      </label>
                      <Button size="sm" disabled={proposalBusy} onClick={() => void saveObservation()}>{t('action.save') ?? 'Save'}</Button>
                    </div>
                  </div>

                  {/* Control examples (§6.6): optional smoke checks, and an
                      informational run signal — never a gate. */}
                  <div>
                    <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '0.5rem', flexWrap: 'wrap', gap: '0.5rem' }}>
                      <div className="skill-subheading" style={{ margin: 0 }}>{t('agents.check_cases') ?? 'Control examples'}</div>
                      <Button size="sm" kind="secondary" disabled={checkBusy || checkCases.length === 0} onClick={() => void runCheckCases(currentVersion)}>
                        {checkBusy ? (t('agents.check_running') ?? 'Running the examples…') : (t('agents.check_run') ?? 'Check on examples')}
                      </Button>
                    </div>
                    {checkResult && (
                      <div style={{ border: `1px solid ${checkResult.failed > 0 ? 'var(--tm-danger, #f7768e)' : 'var(--tm-success, #9ece6a)'}`, borderRadius: '6px', padding: '0.5rem 0.75rem', marginBottom: '0.75rem' }}>
                        <p style={{ margin: '0 0 0.35rem', fontSize: '0.8rem' }}>
                          {t('agents.check_result', { passed: String(checkResult.passed), failed: String(checkResult.failed) }) ?? `Passed ${checkResult.passed} · failed ${checkResult.failed}`}
                        </p>
                        {checkResult.results.map((r, i) => (
                          <div key={i} style={{ fontSize: '0.75rem', padding: '0.15rem 0', color: 'var(--tm-text-2)' }}>
                            <span style={{ color: r.pass ? 'var(--tm-success, #9ece6a)' : 'var(--tm-danger, #f7768e)' }}>{r.pass ? '✓' : '✗'}</span>{' '}
                            {r.input.slice(0, 120)}{r.input.length > 120 ? '…' : ''}
                            {r.detail && <span style={{ color: 'var(--tm-text-3)' }}> — {r.detail}</span>}
                          </div>
                        ))}
                      </div>
                    )}
                    <div style={{ border: '1px solid var(--tm-border)', borderRadius: '6px', padding: '0.6rem 0.75rem', marginBottom: '0.75rem' }}>
                      <div className="skill-subheading" style={{ margin: '0 0 0.35rem' }}>{t('agents.check_case_add') ?? 'Add example'}</div>
                      <TextArea
                        id="agent-check-case-input"
                        hideLabel
                        labelText=""
                        rows={2}
                        placeholder={t('agents.save_example_input_ph') ?? 'Task for the agent…'}
                        value={newCase.input}
                        onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => setNewCase({ ...newCase, input: e.target.value })}
                      />
                      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '0.5rem', marginTop: '0.4rem' }}>
                        <TextInput
                          id="agent-check-case-must"
                          hideLabel
                          labelText=""
                          placeholder={t('agents.save_example_must_ph') ?? 'Answer must contain (comma-separated)'}
                          value={newCase.must}
                          onChange={(e: React.ChangeEvent<HTMLInputElement>) => setNewCase({ ...newCase, must: e.target.value })}
                        />
                        <TextInput
                          id="agent-check-case-must-not"
                          hideLabel
                          labelText=""
                          placeholder={t('agents.save_example_must_not_ph') ?? 'Answer must NOT contain (comma-separated)'}
                          value={newCase.mustNot}
                          onChange={(e: React.ChangeEvent<HTMLInputElement>) => setNewCase({ ...newCase, mustNot: e.target.value })}
                        />
                      </div>
                      <Button size="sm" style={{ marginTop: '0.5rem' }} disabled={checkBusy || !newCase.input.trim()} onClick={() => void saveCheckCase()}>
                        {t('agents.check_case_save') ?? 'Add example'}
                      </Button>
                    </div>
                    {checkCases.length === 0 ? (
                      <p style={{ color: 'var(--tm-text-3)', fontSize: '0.8rem', margin: 0 }}>
                        {t('agents.check_cases_empty') ?? 'No examples yet. Save one from a run or add it manually.'}
                      </p>
                    ) : (
                      <div style={{ display: 'flex', flexDirection: 'column', gap: '0.5rem' }}>
                        {checkCases.map((c) => (
                          <div key={c.id} style={{ border: '1px solid var(--tm-border)', borderRadius: '6px', padding: '0.5rem 0.75rem' }}>
                            <div style={{ display: 'flex', justifyContent: 'space-between', gap: '0.5rem', alignItems: 'flex-start' }}>
                              <div style={{ minWidth: 0, fontSize: '0.8rem' }}>
                                {c.input.slice(0, 220)}{c.input.length > 220 ? '…' : ''}
                              </div>
                              <Button
                                size="sm"
                                kind="danger--ghost"
                                hasIconOnly
                                renderIcon={TrashCan}
                                iconDescription={t('action.delete') ?? 'Delete'}
                                disabled={checkBusy}
                                onClick={() => {
                                  void (async () => {
                                    if (await confirmDialog({ title: t('agents.check_case_delete_title') ?? 'Delete this example?', body: t('action.delete_warning') ?? 'This cannot be undone.', danger: true, confirmLabel: t('action.delete') ?? 'Delete' })) void removeCheckCase(c)
                                  })()
                                }}
                              />
                            </div>
                            <div style={{ display: 'flex', gap: '0.25rem', flexWrap: 'wrap', marginTop: '0.35rem' }}>
                              {(c.must_contain ?? []).map((m, i) => <Tag key={`y${i}`} size="sm" type="green">{m}</Tag>)}
                              {(c.must_not_contain ?? []).map((m, i) => <Tag key={`n${i}`} size="sm" type="red">{m}</Tag>)}
                              {c.from_run_id && (
                                <Tag size="sm" type="gray">
                                  {t('agents.check_case_from_run', { run: c.from_run_id }) ?? `from run ${c.from_run_id}`}
                                </Tag>
                              )}
                            </div>
                          </div>
                        ))}
                      </div>
                    )}
                  </div>
                </div>
                )
              })()}
            </Stack>
          )}
        </Column>
      </Grid>

      {/* Wizard: describe the specialist in natural language */}
      {wizardOpen && (
        <AppModal onClose={() => setWizardOpen(false)} panelStyle={{ width: '560px' }}>
            <Heading>{t('agents.wizard_title') ?? 'Create agent'}</Heading>
            {wizardBusy ? (
              <GeneratingState
                title={t('agents.generating_title') ?? 'Building agent definition…'}
                hint={t('agents.generating_hint') ?? 'The builder agent analyses the description and your clarifications. This usually takes less than a minute.'}
              />
            ) : (
              <>
                <p style={{ color: 'var(--tm-text-3)', fontSize: '0.8rem', margin: '0.25rem 0 0.75rem' }}>
                  {t('agents.wizard_hint') ?? 'Describe the specialist in plain words — the agent will turn it into a structured definition you can adjust afterwards.'}
                </p>
                <TextArea
                  id="agent-wizard-description"
                  hideLabel
                  labelText={t('agents.wizard_label') ?? 'Describe what this agent should do'}
                  rows={7}
                  value={wizardText}
                  onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => setWizardText(e.target.value)}
                  placeholder={t('agents.wizard_placeholder') ?? 'Need an agent for backend development in C#. It can change code and run tests, but must not touch infrastructure…'}
                  autoFocus
                />
                {wizardError && <InlineNotification kind="error" title={t('action.error') ?? 'Error'} subtitle={wizardError} onClose={() => setWizardError('')} lowContrast style={{ marginTop: '0.5rem' }} />}
                <div className="form-actions">
                  <Button kind="secondary" onClick={startManual}>{t('agents.manual') ?? 'Fill manually'}</Button>
                  <Button disabled={wizardBusy || !wizardText.trim()} onClick={() => void generate()}>
                    {draftQuestions.length ? (t('skills.send_answers') ?? 'Send answers') : (t('agents.build') ?? 'Build agent')}
                  </Button>
                </div>
              </>
            )}
        </AppModal>
      )}

      {/* «Предложить улучшение» (docs/agent-evolution.md §6.3): free-text
          problem → from-text draft → diff review. */}
      {improveOpen && selected && (
        <ImproveAgentDialog
          agentId={selected.id}
          onClose={() => setImproveOpen(false)}
          onChanged={() => {
            void refreshEvolution(selected.id)
            void load()
          }}
        />
      )}

      {/* Proposal detail (§6.2): change_summary + structural and prompt diff,
          with the gate-free decisions. */}
      {detailProposal && (
        <ProposalDiffModal
          agentId={detailProposal.agent_id}
          proposalId={detailProposal.id}
          changeSummary={detailProposal.change_summary}
          canDecide={detailProposal.status === 'pending'}
          onClose={() => setDetailProposal(null)}
          onDecided={(outcome) => {
            setProposalNotice({
              kind: 'success',
              text: outcome.kind === 'applied'
                ? (t('agents.proposal_applied_version', { version: String(outcome.version ?? '') }) ?? `The definition is now v${outcome.version ?? ''}.`)
                : (t('agents.proposal_rejected_ok') ?? 'Proposal rejected.'),
            })
            if (selectedId) {
              void refreshEvolution(selectedId)
              void load()
            }
          }}
        />
      )}

      {/* «Сравнить» (§6.4): structural + prompt diff of one version against
          the version it replaced. */}
      {compareDiff && (
        <AppModal onClose={() => setCompareDiff(null)} panelStyle={{ width: '640px', maxHeight: '85vh', display: 'flex', flexDirection: 'column' }}>
          <Heading style={{ fontSize: '1.05rem' }}>
            {t('agents.compare_title', { from: String(compareDiff.from), to: String(compareDiff.to) }) ?? `Compare v${compareDiff.from} → v${compareDiff.to}`}
          </Heading>
          <div className="modal-scroll" style={{ marginTop: '0.75rem' }}>
            <DefinitionDiffView diff={compareDiff.diff} />
          </div>
          <div className="form-actions" style={{ marginTop: '0.75rem' }}>
            <Button onClick={() => setCompareDiff(null)}>{t('action.close') ?? 'Close'}</Button>
          </div>
        </AppModal>
      )}

      {/* Edit/Create modal */}
      {showForm && (
        <AppModal onClose={() => { setShowForm(false); setEditing(null); setPromptPreview(null) }} panelClassName="tabbed" panelStyle={{ width: '760px', maxWidth: 'calc(100vw - 2rem)', maxHeight: '85vh', minHeight: '480px' }}>
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: '0.5rem' }}>
              <Heading>{editing ? (t('agents.edit_agent') ?? 'Edit Agent') : (t('agents.new_agent') ?? 'New Agent')}</Heading>
              {editing?.definition_version ? <Tag type="purple" size="sm">{t('agents.def_version', { count: String(editing.definition_version) }) ?? `Definition v${editing.definition_version}`}</Tag> : null}
            </div>
            {(wizardBusy || regenBusy) ? (
              <GeneratingState
                title={t('agents.generating_title') ?? 'Building agent definition…'}
                hint={t('agents.generating_hint') ?? 'The builder agent analyses the description and your clarifications. This usually takes less than a minute.'}
              />
            ) : (
            <>
            <div className="skill-tabs modal-tabs" role="tablist">
              {(['general', 'capabilities', 'rules', 'params', 'bindings', 'tools', 'prompt'] as const).map((key) => (
                <button
                  key={key}
                  role="tab"
                  aria-selected={formTab === key}
                  className={`skill-tab ${formTab === key ? 'active' : ''}`}
                  onClick={() => setFormTab(key)}
                >
                  {t(`agents.ftab_${key}`) ?? key}
                </button>
              ))}
            </div>
            <div className="modal-scroll">
            {formTab === 'general' && (
              <Stack gap={3}>
                <div className="manifest-section-head">
                  <div style={{ minWidth: 0, flex: 1 }}>
                    <TextInput id="agent-name" labelText={t('agents.name') ?? 'Name'} value={form.name ?? ''} onChange={(e: React.ChangeEvent<HTMLInputElement>) => { touch('identity'); setForm({ ...form, name: e.target.value }) }} placeholder="coder" autoFocus />
                  </div>
                  {provenance.identity && <ProvBadge kind={provenance.identity} />}
                </div>
                <div>
                  <div className="manifest-section-head">
                    <p className="cds--label" style={{ margin: 0 }}>{t('agents.purpose') ?? 'Purpose'}</p>
                    {provenance.identity && <ProvBadge kind={provenance.identity} />}
                  </div>
                  <TextArea
                    id="agent-desc"
                    hideLabel
                    labelText={t('agents.purpose') ?? 'Purpose'}
                    value={form.description ?? ''}
                    onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => { touch('identity'); setForm({ ...form, description: e.target.value }) }}
                    rows={3}
                    placeholder={t('agents.purpose_placeholder') ?? 'For which tasks do you need this agent? E.g. implements code changes, fixes bugs and verifies the result.'}
                    style={{ marginTop: '0.25rem' }}
                  />
                </div>
                <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '0.5rem' }}>
                  <Select id="agent-provider" labelText={t('agents.provider') ?? 'Provider'} value={form.provider ?? ''} onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setForm({ ...form, provider: e.target.value })}>
                    <SelectItem value="" text={t('agents.default_label') ?? 'Default'} />
                    {providers.map((p) => <SelectItem key={p.id} value={p.id} text={p.name} />)}
                  </Select>
                  {(() => {
                    // The model dropdown follows the provider selection: a
                    // concrete provider narrows the list to its catalog; the
                    // default routes across all providers and labels each
                    // model with its owner. A value outside the catalog (wizard
                    // suggestion, legacy config) stays selectable; with no
                    // catalog at all we degrade to free text.
                    const scoped = form.provider ? providers.filter((p) => p.id === form.provider) : providers
                    const options = scoped.flatMap((p) => (p.models ?? []).map((m) => ({ model: m, label: form.provider ? m : `${m} · ${p.name}`, key: `${p.id}/${m}` })))
                    if (options.length === 0) {
                      return (
                        <TextInput id="agent-model" labelText={t('agents.model') ?? 'Model'} value={form.model ?? ''} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, model: e.target.value })} placeholder="mimo-v2.6-pro…" />
                      )
                    }
                    const inCatalog = options.some((o) => o.model === form.model)
                    return (
                      <Select id="agent-model" labelText={t('agents.model') ?? 'Model'} value={form.model ?? ''} onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setForm({ ...form, model: e.target.value })}>
                        <SelectItem value="" text={t('agents.default_label') ?? 'Default'} />
                        {form.model && !inCatalog && <SelectItem value={form.model} text={`${form.model} · ${t('agents.model_not_in_catalog') ?? 'not in catalog'}`} />}
                        {options.map((o) => <SelectItem key={o.key} value={o.model} text={o.label} />)}
                      </Select>
                    )
                  })()}
                </div>
                <div style={{ display: 'flex', gap: '0.5rem', flexWrap: 'wrap' }}>
                  {editing && (
                    <Button size="sm" kind="secondary" renderIcon={Renew} disabled={regenBusy || !form.description?.trim()} onClick={() => void rebuild()}>
                      {regenBusy ? (t('action.building') ?? 'Building…') : (t('agents.regenerate') ?? 'Rebuild')}
                    </Button>
                  )}
                  {editing && builtinFor(form) && (
                    <Button size="sm" kind="ghost" onClick={() => applyBuiltin(builtinFor(form)!)}>{t('agents.restore_builtin') ?? 'Restore builtin definition'}</Button>
                  )}
                </div>
                {draftQuestions.length > 0 && (
                  <div className="wizard-questions">
                    <div className="skill-subheading">{t('skills.agent_questions') ?? 'The agent asks for clarification'}</div>
                    <p style={{ fontSize: '0.75rem', color: 'var(--tm-text-3)', margin: '0 0 0.5rem' }}>{t('skills.agent_questions_hint')}</p>
                    {draftQuestions.map((q, i) => (
                      <div key={i} style={{ marginBottom: '0.5rem' }}>
                        <p style={{ margin: '0 0 0.25rem', fontSize: '0.8rem', color: 'var(--tm-text-2)' }}>{q.question}</p>
                        <TextInput
                          id={`agent-answer-${i}`}
                          hideLabel
                          labelText=""
                          value={answers[String(i)] ?? ''}
                          placeholder={t('skills.answer_placeholder') ?? 'Your answer'}
                          onChange={(e: React.ChangeEvent<HTMLInputElement>) => setAnswers((prev) => ({ ...prev, [String(i)]: e.target.value }))}
                        />
                      </div>
                    ))}
                    <Button size="sm" kind="secondary" disabled={wizardBusy} onClick={() => void generate(answers)}>
                      {t('agents.answer_and_rebuild') ?? 'Answer & rebuild'}
                    </Button>
                  </div>
                )}
              </Stack>
            )}
            {formTab === 'capabilities' && (
              <Stack gap={4}>
                <div className="manifest-section-head">
                  <div>
                    <p className="cds--label" style={{ margin: 0 }}>{t('agents.capabilities') ?? 'Capabilities'}</p>
                    <p style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem', margin: '0.25rem 0 0' }}>
                      {t('agents.cap_hint') ?? 'Disabled capabilities are enforced: the corresponding tools and access are removed at run time, not just discouraged.'}
                    </p>
                  </div>
                  {provenance.capabilities && <ProvBadge kind={provenance.capabilities} />}
                </div>
                {/* Two columns: seven toggles in one stack read as an endless
                    list; a 2-col grid halves the height and groups cleanly. */}
                <div style={{ display: 'grid', gridTemplateColumns: 'repeat(2, minmax(0, 1fr))', gap: '0.25rem 1.5rem' }}>
                  {CAP_KEYS.map((key) => (
                    <Toggle
                      key={key}
                      id={`agent-cap-${key}`}
                      labelText={capLabel(key)}
                      toggled={form.definition?.capabilities?.[key] !== false}
                      onToggle={(checked: boolean) => { touch('capabilities'); setCap(key, checked ? undefined : false) }}
                    />
                  ))}
                  <Toggle
                    id="agent-cap-delegation"
                    labelText={t('agents.cap.delegation') ?? 'Delegate to other agents'}
                    toggled={form.definition?.capabilities?.delegation === true}
                    onToggle={(checked: boolean) => { touch('capabilities'); setCap('delegation', checked ? true : undefined) }}
                  />
                  <Toggle
                    id="agent-cap-triggers"
                    labelText={t('agents.cap.triggers') ?? 'Create triggers'}
                    toggled={form.definition?.capabilities?.triggers === true}
                    onToggle={(checked: boolean) => { touch('capabilities'); setCap('triggers', checked ? true : undefined) }}
                  />
                </div>
                {/* Image handling (docs/harness-settings.md): the master
                    switch and the screenshot context window in one row —
                    toggle left, limit right. Off denies the vision tools and
                    keeps pixels out of the conversation. */}
                <div style={{ borderTop: '1px solid var(--tm-border)', paddingTop: '0.8rem', marginTop: '0.5rem' }}>
                  {/* Bottom-aligned row: the toggle and the inputs share one
                      baseline — their bottom edges line up (the inputs' box
                      sets the row height). */}
                  <div style={{ display: 'flex', alignItems: 'flex-end', justifyContent: 'space-between', gap: '1rem', flexWrap: 'wrap' }}>
                    <Toggle
                      id="agent-cap-vision"
                      labelText={t('agents.cap.vision') ?? 'Работа с изображениями'}
                      toggled={form.definition?.capabilities?.vision !== false}
                      onToggle={(checked: boolean) => { touch('capabilities'); setCap('vision', checked ? undefined : false) }}
                    />
                    {form.definition?.capabilities?.vision !== false && (
                      <div style={{ display: 'flex', alignItems: 'center', gap: '0.8rem', flexWrap: 'wrap' }}>
                        <label style={{ display: 'flex', alignItems: 'center', gap: '0.45rem', whiteSpace: 'nowrap' }}>
                          <span style={{ fontSize: '0.72rem', color: 'var(--tm-text-3)' }}>{t('agents.vision_mode_label') ?? 'Изображения:'}</span>
                          <select
                            value={visionMode}
                            onChange={(e) => setVisionMode(e.target.value as 'full' | 'describe')}
                            style={{ ...paramInputStyle, width: 'auto', padding: '0.3rem 0.5rem' }}
                          >
                            <option value="full">{t('agents.vision_mode_full') ?? 'Пиксели в контексте'}</option>
                            <option value="describe">{t('agents.vision_mode_describe') ?? 'Только описания'}</option>
                          </select>
                        </label>
                        {visionMode === 'full' && (
                          <label style={{ display: 'flex', alignItems: 'center', gap: '0.45rem', whiteSpace: 'nowrap' }}>
                            <span style={{ fontSize: '0.72rem', color: 'var(--tm-text-3)' }} title={t('agents.vision_limit_hint') ?? 'Сколько свежих сообщений со скриншотами держать в контексте; старые vision-блоки вытесняются по мере появления новых (экономит запросы, историю и prompt cache). 0 = без лимита.'}>{t('agents.vision_limit_short') ?? '× лимит изображений в контексте'}</span>
                            <input
                              type="number"
                              onFocus={selectOnFocus}
                              value={form.vision_keep_images ?? 0}
                              min={0}
                              max={20}
                              step={1}
                              onChange={(e) => setForm({ ...form, vision_keep_images: Math.min(20, Math.max(0, Math.floor(Number(e.target.value) || 0))) })}
                              style={{ ...paramInputStyle, width: '64px', textAlign: 'center' }}
                            />
                          </label>
                        )}
                      </div>
                    )}
                  </div>
                  <p style={{ color: 'var(--tm-text-3)', fontSize: '0.72rem', margin: '0.35rem 0 0' }}>{t('agents.cap.vision_hint') ?? 'Выключено — view_image / fetch_image / inspect_image недоступны, скриншоты не попадают в контекст. «Только описания» — тулы вместо пикселей сразу возвращают текстовое описание (vision-модель), base64 в контекст не попадает никогда. «Пиксели в контексте» — vision-блоки живут в диалоге (с лимитом).'}</p>
                </div>
              </Stack>
            )}
            {formTab === 'rules' && (
              <Stack gap={4}>
                <div>
                  <div className="manifest-section-head">
                    <div>
                      <p className="cds--label" style={{ margin: 0 }}>{t('agents.constraints') ?? 'Constraints'}</p>
                      <p style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem', margin: '0.25rem 0 0' }}>
                        {t('agents.constraints_hint') ?? 'What the agent must never do. One item per line.'}
                      </p>
                    </div>
                    {provenance.constraints && <ProvBadge kind={provenance.constraints} />}
                  </div>
                  <TextArea
                    id="agent-constraints"
                    hideLabel
                    labelText={t('agents.constraints') ?? 'Constraints'}
                    rows={5}
                    value={listToText(form.definition?.constraints)}
                    onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => { touch('constraints'); setDef({ constraints: e.target.value.split('\n') }) }}
                    placeholder={t('agents.constraints_placeholder') ?? 'Never modify infrastructure configuration.\nNever delete files.'}
                    style={{ marginTop: '0.25rem' }}
                  />
                </div>
                <div>
                  <div className="manifest-section-head">
                    <div>
                      <p className="cds--label" style={{ margin: 0 }}>{t('agents.completion') ?? 'Completion checks'}</p>
                      <p style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem', margin: '0.25rem 0 0' }}>
                        {t('agents.completion_hint') ?? 'What the agent must verify before declaring the work done. One item per line.'}
                      </p>
                    </div>
                    {provenance.completion && <ProvBadge kind={provenance.completion} />}
                  </div>
                  <TextArea
                    id="agent-completion"
                    hideLabel
                    labelText={t('agents.completion') ?? 'Completion checks'}
                    rows={5}
                    value={listToText(form.definition?.completion)}
                    onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => { touch('completion'); setDef({ completion: e.target.value.split('\n') }) }}
                    placeholder={t('agents.completion_placeholder') ?? 'Run the relevant tests.\nMake sure the build passes.'}
                    style={{ marginTop: '0.25rem' }}
                  />
                </div>
              </Stack>
            )}
            {formTab === 'params' && (
              <Stack gap={3}>
                {/* One compact row of four numeric fields (the dialog is a
                    settings table, not a control showroom): temperature is a
                    plain 0..1 text box (no slider, no steppers), and the
                    auto-continuation count sits next to the turn budget it
                    extends — the long hint lives in the title tooltip. */}
                <div style={{ display: 'grid', gridTemplateColumns: 'repeat(4, minmax(0, 1fr))', gap: '0.5rem' }}>
                  <label style={{ display: 'block', minWidth: 0 }}>
                    <span style={{ display: 'block', fontSize: '0.72rem', color: 'var(--tm-text-3)', margin: '0 0 0.25rem' }}>{t('agents.temperature') ?? 'Temperature'}</span>
                    <input
                      type="number"
                      onFocus={selectOnFocus}
                      value={form.temperature ?? 0}
                      min={0}
                      max={1}
                      step={0.05}
                      onChange={(e) => {
                        const parsed = Number(e.target.value)
                        setForm({ ...form, temperature: Number.isFinite(parsed) ? Math.min(1, Math.max(0, parsed)) : 0 })
                      }}
                      style={paramInputStyle}
                    />
                  </label>
                  <label style={{ display: 'block', minWidth: 0 }}>
                    <span style={{ display: 'block', fontSize: '0.72rem', color: 'var(--tm-text-3)', margin: '0 0 0.25rem' }}>{t('agents.max_tokens') ?? 'Max tokens'}</span>
                    <input
                      type="number"
                      onFocus={selectOnFocus}
                      value={form.max_tokens ?? 16384}
                      min={64}
                      max={1000000}
                      step={64}
                      onChange={(e) => setForm({ ...form, max_tokens: Math.max(64, Math.floor(Number(e.target.value) || 16384)) })}
                      style={paramInputStyle}
                    />
                  </label>
                  <label style={{ display: 'block', minWidth: 0 }}>
                    <span style={{ display: 'block', fontSize: '0.72rem', color: 'var(--tm-text-3)', margin: '0 0 0.25rem' }}>{t('agents.max_turns') ?? 'Max turns'}</span>
                    <input
                      type="number"
                      onFocus={selectOnFocus}
                      value={form.max_turns ?? 20}
                      min={1}
                      max={100}
                      step={1}
                      onChange={(e) => setForm({ ...form, max_turns: Math.min(100, Math.max(1, Math.floor(Number(e.target.value) || 20))) })}
                      style={paramInputStyle}
                    />
                  </label>
                  <label style={{ display: 'block', minWidth: 0 }}>
                    <span style={{ display: 'block', fontSize: '0.72rem', color: 'var(--tm-text-3)', margin: '0 0 0.25rem' }} title={t('agents.turn_restarts_hint') ?? 'Если ран завершился только из-за лимита ходов, он автоматически продолжится новым раном с полным бюджетом ходов (в трейсе — run.restarted). 0 = выключено.'}>{t('agents.turn_restarts_short') ?? '× продолжений'}</span>
                    <input
                      type="number"
                      onFocus={selectOnFocus}
                      value={form.turn_limit_restarts ?? 0}
                      min={0}
                      max={5}
                      step={1}
                      onChange={(e) => setForm({ ...form, turn_limit_restarts: Math.min(5, Math.max(0, Math.floor(Number(e.target.value) || 0))) })}
                      style={paramInputStyle}
                    />
                  </label>
                </div>
                <Select id="agent-sandbox" labelText={t('agents.sandbox_profile') ?? 'Sandbox profile'} value={form.sandbox_profile ?? ''} onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setForm({ ...form, sandbox_profile: e.target.value })}>
                  <SelectItem value="" text={t('agents.sandbox_default') ?? 'Default (installation image)'} />
                  {sandboxProfiles.map((p) => (
                    <SelectItem key={p.id} value={p.id} text={`${p.name}${(p.toolchains ?? []).length > 0 ? ' · ' + (p.toolchains ?? []).join(', ') : ''}${p.available ? '' : ' — ' + (t('agents.sandbox_not_built') ?? 'not built')}`} />
                  ))}
                </Select>
                {/* Read-only is NOT a separate switch when the structured
                    definition exists: «Возможности → Изменять файлы» is the
                    single source — unticking it makes the workspace mount
                    read-only AND removes the file-writing tools (the backend
                    has done this coordination all along). The manual toggle
                    survives only for legacy prompt-only agents. */}
                {(!form.definition || Object.keys(form.definition).length === 0) && (
                  <div style={{ display: 'flex', gap: '1rem' }}>
                    <Toggle id="agent-readonly" labelText={t('agents.read_only') ?? 'Read-only workspace'} toggled={form.read_only ?? false} onToggle={(checked: boolean) => setForm({ ...form, read_only: checked })} />
                    <p style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)', alignSelf: 'center', margin: 0, maxWidth: '420px' }}>{t('agents.read_only_legacy_hint') ?? 'This agent has no structured definition (the Capabilities panel does not apply to it) — so its file access switches here. Example: the system intake agent.'}</p>
                  </div>
                )}
                {/* Network access as one horizontal block: the level on the
                    left, the destination list (when the level uses it) on
                    the right. */}
                <div style={{ display: 'flex', gap: '1rem', alignItems: 'flex-start' }}>
                  <div style={{ width: '45%', minWidth: 0 }}>
                    <Select
                      id="agent-egress-level"
                      labelText={t('agents.egress_level') ?? 'Network access'}
                      value={egressLevel}
                      onChange={(e: React.ChangeEvent<HTMLSelectElement>) => {
                        const level = e.target.value
                        setEgressLevelOverride(level)
                        if (level === 'off') { setForm({ ...form, network_access: false, egress_allowlist: [] }); syncEgressDraft([]) }
                        else if (level === 'allowlist') { setForm({ ...form, network_access: false, egress_allowlist: form.egress_allowlist && form.egress_allowlist.length > 0 ? form.egress_allowlist : [] }); syncEgressDraft(form.egress_allowlist) }
                        else setForm({ ...form, network_access: true })
                      }}
                      helperText={egressLevel === 'off'
                        ? (t('agents.egress_off_hint') ?? 'The agent works only inside its workspace.')
                        : egressLevel === 'allowlist'
                          ? (t('agents.egress_allowlist_only_hint') ?? 'Exactly the addresses listed on the right — nothing else.')
                          : (t('agents.egress_public_hint') ?? 'Public internet is reachable; your LAN stays closed.')}
                    >
                      <SelectItem value="off" text={t('agents.egress_off') ?? 'No network'} />
                      <SelectItem value="allowlist" text={t('agents.egress_allowlist_only') ?? 'Only allowed addresses'} />
                      <SelectItem value="public" text={t('agents.egress_public') ?? 'Open internet (LAN closed)'} />
                    </Select>
                  </div>
                  {egressLevel !== 'off' && (
                    <div style={{ flex: 1, minWidth: 0 }}>
                      <TextArea
                        id="agent-egress"
                        labelText={t('agents.egress_allowlist') ?? 'Allowed addresses'}
                        value={egressDraft}
                        onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => setEgressDraft(e.target.value)}
                        onBlur={() => setForm({ ...form, egress_allowlist: parseEgressDraft() })}
                        rows={3}
                        placeholder={t('agents.egress_allowlist_hint') ?? 'One entry per line: a domain (api.github.com), a glob (*.example.com), a CIDR (10.20.30.0/24) or host:port.'}
                      />
                    </div>
                  )}
                </div>
                <Select
                  id="agent-approval"
                  labelText={t('agents.approval_mode') ?? 'Control level'}
                  value={form.approval_mode ?? ''}
                  onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setForm({ ...form, approval_mode: e.target.value })}
                  helperText={t('agents.approval_mode_hint') ?? 'Autonomous: workspace and web run without confirmation. Monitored: the same, plus a judge reviews every completed turn and flags suspicious behavior. Strict: every consequential tool asks a human first.'}
                >
                  <SelectItem value="monitored" text={t('agents.approval_monitored') ?? 'Monitored (recommended)'} />
                  <SelectItem value="autonomous" text={t('agents.approval_autonomous') ?? 'Autonomous (no oversight)'} />
                  <SelectItem value="strict" text={t('agents.approval_strict') ?? 'Strict (confirm every tool)'} />
                </Select>
              </Stack>
            )}
            {formTab === 'bindings' && (
              <Stack gap={4}>
                {org.units.length > 0 && (
                  <div>
                    <OrgUnitSelect
                      id="agent-org"
                      value={form.org_unit_id ?? ''}
                      onChange={(orgUnitID) => setForm({ ...form, org_unit_id: orgUnitID })}
                      org={org}
                      disabled={!!editing && !org.isAdmin}
                    />
                    {!!editing && !org.isAdmin && (
                      <p style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)', margin: '0.25rem 0 0' }}>{t('org.binding_admin_only') ?? 'Only an administrator can change the availability of an existing resource.'}</p>
                    )}
                  </div>
                )}
                {skillOptions.length > 0 && (
                  <div>
                    <p className="cds--label" style={{ marginBottom: '0.5rem' }}>{t('agents.skills') ?? 'Skills'}</p>
                    <p style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem', margin: '0 0 0.5rem' }}>{t('agents.skills_hint') ?? 'Selected skills are injected into the system prompt at run time.'}</p>
                    <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '0.25rem 1rem', maxHeight: '14rem', overflow: 'auto', padding: '0.5rem 0.75rem', border: '1px solid var(--tm-border)', borderRadius: '6px' }}>
                      {skillOptions.map((s) => (
                        <Checkbox
                          key={s.id}
                          id={`agent-skill-${s.id}`}
                          labelText={s.label}
                          title={s.id}
                          checked={form.skills?.includes(s.id) ?? false}
                          onChange={(_: React.ChangeEvent<HTMLInputElement>, { checked }: { checked: boolean }) =>
                            setForm({ ...form, skills: checked ? [...(form.skills ?? []), s.id] : (form.skills ?? []).filter((x) => x !== s.id) })}
                        />
                      ))}
                    </div>
                  </div>
                )}
                {mcpOptions.length > 0 && (
                  <div>
                    <p className="cds--label" style={{ marginBottom: '0.5rem' }}>{t('agents.mcp_servers') ?? 'MCP servers'}</p>
                    <p style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem', margin: '0 0 0.5rem' }}>{t('agents.mcp_hint') ?? 'Selected servers add their tools to this agent. Bind servers on the MCP tab.'}</p>
                    <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '0.25rem 1rem', maxHeight: '10rem', overflow: 'auto', padding: '0.5rem 0.75rem', border: '1px solid var(--tm-border)', borderRadius: '6px' }}>
                      {mcpOptions.map((s) => (
                        <Checkbox
                          key={s.id}
                          id={`agent-mcp-${s.id}`}
                          labelText={s.label}
                          title={s.id}
                          checked={form.mcp_servers?.includes(s.id) ?? false}
                          onChange={(_: React.ChangeEvent<HTMLInputElement>, { checked }: { checked: boolean }) =>
                            setForm({ ...form, mcp_servers: checked ? [...(form.mcp_servers ?? []), s.id] : (form.mcp_servers ?? []).filter((x) => x !== s.id) })}
                        />
                      ))}
                    </div>
                  </div>
                )}
                {secretOptions.length > 0 && (
                  <div>
                    <p className="cds--label" style={{ marginBottom: '0.5rem' }}>{t('agents.secrets') ?? 'Secrets'}</p>
                    <p style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem', margin: '0 0 0.5rem' }}>{t('agents.secrets_hint') ?? 'Granted secrets are injected as SECRET_<NAME> environment variables into run_command. The model never sees the values.'}</p>
                    <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '0.25rem 1rem', maxHeight: '10rem', overflow: 'auto', padding: '0.5rem 0.75rem', border: '1px solid var(--tm-border)', borderRadius: '6px' }}>
                      {secretOptions.map((s) => (
                        <Checkbox
                          key={s.id}
                          id={`agent-secret-${s.id}`}
                          labelText={s.label}
                          title={s.description || s.id}
                          checked={form.secrets?.includes(s.id) ?? false}
                          onChange={(_: React.ChangeEvent<HTMLInputElement>, { checked }: { checked: boolean }) =>
                            setForm({ ...form, secrets: checked ? [...(form.secrets ?? []), s.id] : (form.secrets ?? []).filter((x) => x !== s.id) })}
                        />
                      ))}
                    </div>
                  </div>
                )}
                {skillOptions.length === 0 && mcpOptions.length === 0 && (
                  <p style={{ color: 'var(--tm-text-3)', fontSize: '0.8rem' }}>{t('agents.no_bindings') ?? 'No skills or MCP servers configured in this project yet.'}</p>
                )}
              </Stack>
            )}
            {formTab === 'tools' && (
              <ToolsPanel
                builtins={builtinTools}
                servers={mcpTools.filter((s) => form.mcp_servers?.includes(s.id))}
                allSelected={form.mcp_servers ?? []}
                selected={form.tools ?? []}
                onChange={(tools) => setForm({ ...form, tools })}
              />
            )}
            {formTab === 'prompt' && (
              <Stack gap={4}>
                <div>
                  <p className="cds--label" style={{ marginBottom: '0.25rem' }}>{t('agents.prompt_preview') ?? 'Compiled prompt'}</p>
                  {editing ? (
                    <>
                      <p style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem', margin: '0 0 0.5rem' }}>
                        {t('agents.prompt_saved_hint') ?? 'Reflects the saved definition; save your changes to update it.'}
                      </p>
                      {promptPreview ? (
                        <>
                          <Tag type="gray" size="sm" style={{ marginBottom: '0.25rem' }}>{promptSourceLabel(promptPreview.source)}</Tag>
                          <pre style={{ background: 'var(--tm-elevated)', border: '1px solid var(--tm-border)', borderRadius: '6px', padding: '0.5rem 0.75rem', fontFamily: 'var(--tm-mono, monospace)', fontSize: '0.72rem', whiteSpace: 'pre-wrap', wordBreak: 'break-word', maxHeight: '16rem', overflow: 'auto', margin: 0 }}>
                            {promptPreview.prompt || (t('agents.source_role') ?? 'base contract')}
                          </pre>
                        </>
                      ) : (
                        <p style={{ color: 'var(--tm-text-3)' }}>{t('action.loading') ?? 'Loading…'}</p>
                      )}
                    </>
                  ) : (
                    <p style={{ color: 'var(--tm-text-3)', fontSize: '0.8rem' }}>
                      {t('agents.prompt_unsaved_hint') ?? 'The prompt is compiled from the definition when the agent runs. Save the agent to preview it here.'}
                    </p>
                  )}
                </div>
                <div>
                  <p className="cds--label" style={{ marginBottom: '0.25rem' }}>{t('agents.prompt_override') ?? 'Manual override'}</p>
                  <p style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem', margin: '0 0 0.5rem' }}>
                    {t('agents.prompt_override_hint') ?? 'Optional. Replaces the compiled prompt entirely — capabilities enforcement still applies.'}
                  </p>
                  <TextArea
                    id="agent-prompt-override"
                    hideLabel
                    labelText={t('agents.prompt_override') ?? 'Manual override'}
                    rows={8}
                    value={form.definition?.prompt_override ?? ''}
                    onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => setDef({ prompt_override: e.target.value })}
                    placeholder={t('agents.prompt_override_placeholder') ?? 'Leave empty to use the compiled prompt.'}
                  />
                </div>
              </Stack>
            )}
            </div>
            <div className="form-actions" style={{ marginTop: 0 }}>
              {/* The uniform switch lives at the bottom-left of the button
                  block: visible without hunting, hard to hit by accident. */}
              <div style={{ marginRight: 'auto', display: 'flex', alignItems: 'center' }} title={t('agents.enabled_hint') ?? 'Disabled agents cannot start new runs'}>
                <Toggle id="agent-form-enabled" labelText="" hideLabel aria-label={t('agents.enabled_hint') ?? 'Enabled'} labelA={t('common.off') ?? 'Off'} labelB={t('common.on') ?? 'On'} toggled={formEnabled} onToggle={(v: boolean) => setFormEnabled(v)} size="sm" />
              </div>
              <Button kind="secondary" onClick={() => { setShowForm(false); setEditing(null); setPromptPreview(null) }}>{t('action.cancel') ?? 'Cancel'}</Button>
              <Button onClick={() => void saveAgent()}>{editing ? (t('action.save') ?? 'Save') : (t('action.create') ?? 'Create')}</Button>
            </div>
            </>
            )}
        </AppModal>
      )}

      {/* Rebuild diff (§13): user edits are never silently replaced */}
      {regenDraft && (
        <AppModal onClose={() => setRegenDraft(null)} panelStyle={{ width: '640px', maxHeight: '85vh', display: 'flex', flexDirection: 'column' }}>
            <Heading>{t('agents.regen_title') ?? 'Apply rebuilt definition?'}</Heading>
            <p style={{ color: 'var(--tm-text-3)', fontSize: '0.8rem', margin: '0.25rem 0 0.75rem' }}>
              {t('agents.regen_hint') ?? 'The agent rebuilt the definition from the current purpose. Review the changes — fields you edited manually are marked.'}
            </p>
            <div className="modal-scroll">
              {regenRows.length === 0 && <p style={{ color: 'var(--tm-text-3)' }}>{t('agents.regen_no_changes') ?? 'No changes against the current form.'}</p>}
              {regenRows.map((row) => (
                <div key={row.label} style={{ marginBottom: '0.75rem', borderBottom: '1px solid var(--tm-border)', paddingBottom: '0.75rem' }}>
                  <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', marginBottom: '0.25rem' }}>
                    <strong>{row.label}</strong>
                    {provenance[row.section] === 'user' && <Tag type="warm-gray" size="sm">{t('agents.regen_user_edit') ?? 'your edit will be replaced'}</Tag>}
                  </div>
                  <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '0.5rem', fontSize: '0.8rem' }}>
                    <div>
                      <p className="cds--label" style={{ fontSize: '0.65rem', margin: 0 }}>{t('agents.regen_current') ?? 'Current'}</p>
                      <p style={{ margin: 0, whiteSpace: 'pre-wrap', color: 'var(--tm-text-2)' }}>{row.current || '—'}</p>
                    </div>
                    <div>
                      <p className="cds--label" style={{ fontSize: '0.65rem', margin: 0 }}>{t('agents.regen_proposed') ?? 'Proposed'}</p>
                      <p style={{ margin: 0, whiteSpace: 'pre-wrap' }}>{row.proposed || '—'}</p>
                    </div>
                  </div>
                </div>
              ))}
            </div>
            <div className="form-actions">
              <Button kind="secondary" onClick={() => setRegenDraft(null)}>{t('action.cancel') ?? 'Cancel'}</Button>
              <Button onClick={acceptRegen}>{t('agents.accept') ?? 'Accept'}</Button>
            </div>
        </AppModal>
      )}

      {loading && <PageLoading />}

      {/* Template selection modal: curated builtin definitions from the server */}
      {showTemplates && (
        <AppModal onClose={() => setShowTemplates(false)}>
            <Heading>{t('agents.choose_template') ?? 'Choose a template'}</Heading>
            <p style={{ color: 'var(--tm-text-3)', fontSize: '0.8rem', marginBottom: '1rem' }}>{t('agents.template_hint') ?? 'Start with a curated agent and customize as needed.'}</p>
            <Stack gap={2}>
              {builtins.map((tpl) => (
                <Tile key={tpl.slug} style={{ cursor: 'pointer' }} onClick={() => {
                  setEditing(null)
                  setFormTab('general')
                  setFromWizard(false)
                  setProvenance({})
                  setPromptPreview(null)
                  setForm({
                    name: tpl.name,
                    description: tpl.description,
                    model: '',
                    definition: { ...tpl.definition },
                    sandbox_profile: '',
                    network_access: tpl.network_access,
                    read_only: tpl.read_only,
                    skills: [],
                    mcp_servers: [],
                    tools: [],
                  })
                  setShowForm(true)
                  setShowTemplates(false)
                }}>
                  <strong>{tpl.name}</strong>
                  <p style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem', marginTop: '0.25rem' }}>{tpl.description}</p>
                  <div style={{ display: 'flex', gap: '0.25rem', marginTop: '0.25rem', flexWrap: 'wrap' }}>
                    {tpl.network_access ? <Tag type="green" size="sm">{t('agents.network_label') ?? 'network'}</Tag> : <Tag type="red" size="sm">{t('agents.no_network') ?? 'no network'}</Tag>}
                    {disabledCaps(tpl.definition?.capabilities).map((key) => <Tag key={key} type="warm-gray" size="sm">{capLabel(key)} ✕</Tag>)}
                    {cleanList(tpl.definition?.completion).length > 0 && <Tag type="blue" size="sm">{t('agents.before_done') ?? 'completion checks'}</Tag>}
                  </div>
                </Tile>
              ))}
            </Stack>
            <div className="form-actions">
              <Button kind="secondary" onClick={() => setShowTemplates(false)}>{t('action.cancel') ?? 'Cancel'}</Button>
            </div>
        </AppModal>
      )}

      {/* Installation-level supervisor settings (judge + vision models,
          re-run schedule, HAB coefficients): one modal, one Save.
          The button lives in the page header — an operator surface, not part
          of the everyday agent browsing flow. */}
      {qualityOpen && (
        <AppModal onClose={() => setQualityOpen(false)} panelStyle={{ width: '560px', maxHeight: '85vh', overflowY: 'auto' }}>
          <Heading>{t('quality.settings_title') ?? 'Quality settings'}</Heading>
          <p style={{ color: 'var(--tm-text-3)', fontSize: '0.78rem', margin: '0.25rem 0 1rem' }}>{t('quality.settings_hint') ?? ''}</p>
          {qualityError && <InlineNotification kind="error" title={t('action.error') ?? 'Error'} subtitle={qualityError} onClose={() => setQualityError('')} lowContrast style={{ marginBottom: '0.75rem' }} />}
          <div style={{ marginTop: '1rem' }}>
            <Select id="supervisor-vision-model" labelText={t('quality.vision_model') ?? 'Vision model (image fallback)'} value={visionDraft} onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setVisionDraft(e.target.value)}>
              <SelectItem value="" text={t('quality.model_off') ?? '— main model —'} />
              {providers.flatMap((p) => (p.models ?? []).map((m) => (
                <SelectItem key={`v/${p.id}/${m}`} value={m} text={`${m} · ${p.name}`} />
              )))}
            </Select>
            <p style={{ fontSize: '0.75rem', color: 'var(--tm-text-3)', margin: '0.25rem 0 0' }}>{t('quality.vision_model_hint') ?? ''}</p>
          </div>
          {/* Harness model roles (docs/harness-settings.md): the models that
              serve the harness's own background work. Empty = the role's
              cascade fallback (judge model, then the default model). */}
          <div style={{ marginTop: '1.25rem' }}>
            <div style={{ fontSize: '0.8rem', fontWeight: 600, marginBottom: '0.35rem' }}>{t('quality.roles_title') ?? 'Harness models'}</div>
            <p style={{ fontSize: '0.72rem', color: 'var(--tm-text-3)', margin: '0 0 0.5rem' }}>{t('quality.roles_hint') ?? ''}</p>
            <Select id="harness-compaction-model" labelText={t('quality.compaction_model') ?? 'Compaction model (state card)'} value={compactionDraft} onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setCompactionDraft(e.target.value)}>
              <SelectItem value="" text={t('quality.model_cascade') ?? '— cascade fallback —'} />
              {providers.flatMap((p) => (p.models ?? []).map((m) => (
                <SelectItem key={`c/${p.id}/${m}`} value={m} text={`${m} · ${p.name}`} />
              )))}
            </Select>
            <Select id="harness-title-model" labelText={t('quality.title_model') ?? 'Run title model'} value={titleDraft} onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setTitleDraft(e.target.value)} style={{ marginTop: '0.6rem' }}>
              <SelectItem value="" text={t('quality.model_cascade') ?? '— cascade fallback —'} />
              {providers.flatMap((p) => (p.models ?? []).map((m) => (
                <SelectItem key={`t/${p.id}/${m}`} value={m} text={`${m} · ${p.name}`} />
              )))}
            </Select>
            <Select id="harness-extraction-model" labelText={t('quality.extraction_model') ?? 'Knowledge extraction model'} value={extractionDraft} onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setExtractionDraft(e.target.value)} style={{ marginTop: '0.6rem' }}>
              <SelectItem value="" text={t('quality.model_cascade') ?? '— cascade fallback —'} />
              {providers.flatMap((p) => (p.models ?? []).map((m) => (
                <SelectItem key={`e/${p.id}/${m}`} value={m} text={`${m} · ${p.name}`} />
              )))}
            </Select>
          </div>
          {/* Effective harness knobs (env-resolved, read-only): one place to
              see what the kernel actually runs with. */}
          {supervisor?.harness && (
            <div style={{ marginTop: '1.25rem' }}>
              <div style={{ fontSize: '0.8rem', fontWeight: 600, marginBottom: '0.35rem' }}>{t('quality.harness_title') ?? 'Effective harness settings'}</div>
              <p style={{ fontSize: '0.72rem', color: 'var(--tm-text-3)', margin: '0 0 0.5rem' }}>{t('quality.harness_hint') ?? ''}</p>
              <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(180px, 1fr))', gap: '0.25rem 0.8rem', fontSize: '0.72rem', color: 'var(--tm-text-2)' }}>
                {Object.entries(supervisor.harness.budgets ?? {}).map(([key, value]) => (
                  <div key={key} style={{ display: 'flex', justifyContent: 'space-between', gap: '0.5rem' }}>
                    <code style={{ fontFamily: 'var(--tm-font-mono)', fontSize: '0.68rem' }}>{key}</code>
                    <span>{value}</span>
                  </div>
                ))}
              </div>
            </div>
          )}
          <div style={{ marginTop: '1.25rem' }}>
            <div style={{ fontSize: '0.8rem', fontWeight: 600, marginBottom: '0.35rem' }}>{t('quality.hab_title') ?? 'Human Attention Budget (estimates)'}</div>
            <p style={{ fontSize: '0.72rem', color: 'var(--tm-text-3)', margin: '0 0 0.5rem' }}>{t('quality.hab_hint') ?? ''}</p>
            <div style={{ display: 'grid', gridTemplateColumns: 'repeat(3, minmax(0, 1fr))', gap: '0.6rem' }}>
              <TextInput id="hab-approval" labelText={t('quality.hab_approval') ?? 'Approval, sec'} value={String(habDraft.approval)} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setHabDraft({ ...habDraft, approval: Number(e.target.value) })} />
              <TextInput id="hab-answer" labelText={t('quality.hab_answer') ?? 'Answer base, sec'} value={String(habDraft.answerBase)} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setHabDraft({ ...habDraft, answerBase: Number(e.target.value) })} />
              <TextInput id="hab-wpm" labelText={t('quality.hab_wpm') ?? 'Reading, wpm'} value={String(habDraft.readingWpm)} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setHabDraft({ ...habDraft, readingWpm: Number(e.target.value) })} />
            </div>
          </div>
          <div className="form-actions">
            <Button kind="secondary" disabled={qualityBusy} onClick={() => setQualityOpen(false)}>{t('action.cancel') ?? 'Cancel'}</Button>
            <Button disabled={qualityBusy} onClick={() => void saveQualitySettings()}>{qualityBusy ? '…' : (t('action.save') ?? 'Save')}</Button>
          </div>
        </AppModal>
      )}
    </div>
  )
}

  function ToolsPanel({
    builtins: builtinDefs,
    servers,
    selected,
    onChange,
  }: {
    builtins: MCPToolInfo[]
    servers: MCPServerTools[]
    allSelected: string[]
    selected: string[]
    onChange: (tools: string[]) => void
  }) {
    const t = useT()
    // Filter: tons of tools once MCP servers join — match against tool name
    // AND description, case-insensitive.
    const [toolFilter, setToolFilter] = useState('')
    const matches = (tool: MCPToolInfo) => {
      const q = toolFilter.trim().toLowerCase()
      if (!q) return true
      return (tool.name ?? '').toLowerCase().includes(q)
        || (tool.model_name ?? '').toLowerCase().includes(q)
        || (tool.description ?? '').toLowerCase().includes(q)
    }
    const visibleBuiltins = builtinDefs.filter(matches)
    const visibleServers = servers
      .map((s) => ({ ...s, tools: s.tools.filter(matches) }))
      .filter((s) => s.tools.length > 0)
    const filterEmpty = toolFilter.trim() !== '' && visibleBuiltins.length === 0 && visibleServers.length === 0

    if (builtinDefs.length === 0 && servers.length === 0) return null
    // Empty selection = all tools allowed; show every checkbox checked.
    const allMode = selected.length === 0
    const total = builtinDefs.length + servers.reduce((sum, s) => sum + s.tools.length, 0)

    const toggle = (name: string, checked: boolean) => {
      if (allMode && checked) return // cannot check further when everything is on
      let next = checked ? [...selected, name] : selected.filter((x) => x !== name)
      if (next.length === total) next = [] // everything selected = allow all
      onChange(next)
    }

    const row = (tool: MCPToolInfo, key: string) => (
      <div key={key} style={{ padding: '0.2rem 0', minWidth: 0, overflowWrap: 'anywhere' }}>
        <Checkbox
          id={`agent-tool-${key}`}
          labelText={tool.name}
          title={tool.description || tool.name}
          checked={allMode || selected.includes(tool.model_name || tool.name)}
          onChange={(_: React.ChangeEvent<HTMLInputElement>, { checked }: { checked: boolean }) => toggle(tool.model_name || tool.name, checked)}
        />
        {tool.description && (
          <div
            style={{
              color: 'var(--tm-text-3)',
              fontSize: '0.7rem',
              marginTop: '-0.15rem',
              paddingLeft: '1.5rem',
              maxWidth: '100%',
              display: '-webkit-box',
              WebkitLineClamp: 2,
              WebkitBoxOrient: 'vertical',
              overflow: 'hidden',
            }}
            title={tool.description}
          >
            {tool.description}
          </div>
        )}
      </div>
    )

    return (
      <div>
        <p className="cds--label" style={{ marginBottom: '0.25rem' }}>{t('agents.tools') ?? 'Tools'}</p>
        <p style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem', margin: '0 0 0.5rem' }}>
          {allMode
            ? (t('agents.tools_all_hint') ?? 'All tools are allowed. Uncheck one to restrict the agent.')
            : (t('agents.tools_restricted_hint', { count: String(selected.length) }) ?? `${selected.length} tools allowed.`)}
        </p>
        <div style={{ marginBottom: '0.5rem' }}>
          <ListFilter value={toolFilter} onChange={setToolFilter} placeholder={t('agents.tools_filter') ?? 'Filter tools…'} />
        </div>
        <div style={{ maxHeight: '22rem', overflow: 'auto', padding: '0.5rem 0.75rem', border: '1px solid var(--tm-border)', borderRadius: '6px' }}>
          {filterEmpty ? (
            <p style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem', margin: 0 }}>{t('agents.tools_filter_empty') ?? 'Nothing matches the filter.'}</p>
          ) : (
            <>
              <p className="cds--label" style={{ fontSize: '0.7rem', margin: '0 0 0.25rem' }}>{t('agents.tools_builtin') ?? 'Built-in'}</p>
              {visibleBuiltins.map((tool) => row(tool, `b-${tool.name}`))}
              {visibleServers.map((server) => (
                <div key={server.id}>
                  <p className="cds--label" style={{ fontSize: '0.7rem', margin: '0.5rem 0 0.25rem' }}>{server.name}</p>
                  {server.tools.length === 0 && (
                    <p style={{ color: 'var(--tm-text-3)', fontSize: '0.7rem', margin: 0 }}>{t('agents.tools_no_tools') ?? 'no tools discovered'}</p>
                  )}
                  {server.tools.map((tool) => row(tool, `s-${server.id}-${tool.name}`))}
                </div>
              ))}
            </>
          )}
        </div>
      </div>
    )
  }

/** Bulk configuration dialog: apply one provider+model, sandbox profile,
 *  secrets change or MCP servers to MANY agents at once — «переключить всех
 *  с Xiaomi на z.ai» в два клика вместо шести обходов карточек. Only the
 *  checked sections are applied; every selected agent is read (GET) and
 *  written (PUT) whole, so skills/prompts/versions stay untouched and each
 *  agent keeps its own version history. Collections (secrets, MCP servers)
 *  apply in add or remove mode — «добавить всем» never wipes what an agent
 *  already had. */
const bulkInputStyle = { width: '100%', border: '1px solid var(--tm-border)', borderRadius: '6px', background: 'var(--tm-bg)', color: 'var(--tm-text)', fontSize: '0.8rem', padding: '0.4rem 0.6rem', outline: 'none' } as const

// The agent dialog's four numeric params (temperature / tokens / turns /
// continuations) share one compact style — same shape as the bulk editor's.
const paramInputStyle = bulkInputStyle

function BulkAgentDialog({ agentIds, providers, sandboxProfiles, secrets, mcpServers, onClose, onApplied, t }: {
  agentIds: string[]
  providers: Provider[]
  sandboxProfiles: SandboxProfile[]
  secrets: Secret[]
  mcpServers: MCPServer[]
  onClose: () => void
  onApplied: () => void
  t: (key: string, vars?: Record<string, string>) => string
}) {
  const [applyModel, setApplyModel] = useState(false)
  const [providerId, setProviderId] = useState('')
  const [model, setModel] = useState('')
  const [applySandbox, setApplySandbox] = useState(false)
  const [sandboxProfile, setSandboxProfile] = useState('')
  const [applySecrets, setApplySecrets] = useState(false)
  const [secretSet, setSecretSet] = useState<string[]>([])
  const [applyMcp, setApplyMcp] = useState(false)
  const [mcpAdd, setMcpAdd] = useState<string[]>([])
  const [mcpRemove, setMcpRemove] = useState<string[]>([])
  const [applyParams, setApplyParams] = useState(false)
  const [bulkTemp, setBulkTemp] = useState(0.7)
  const [bulkMaxTokens, setBulkMaxTokens] = useState(16384)
  const [bulkMaxTurns, setBulkMaxTurns] = useState(20)
  const [bulkTurnRestarts, setBulkTurnRestarts] = useState(0)
  const [progress, setProgress] = useState<{ done: number; total: number } | null>(null)
  const [failures, setFailures] = useState<string[]>([])
  const [done, setDone] = useState(0)

  const providerModels = providers.find((p) => p.id === providerId)?.models ?? []

  const apply = async () => {
    setProgress({ done: 0, total: agentIds.length })
    const failed: string[] = []
    let ok = 0
    for (const id of agentIds) {
      try {
        const full = await workspaceApi.getAgent(id)
        const patch: Partial<Agent> = { ...full }
        if (applyModel) { patch.provider = providerId; patch.model = model }
        if (applySandbox) patch.sandbox_profile = sandboxProfile
        if (applySecrets) patch.secrets = secretSet
        if (applyMcp) {
          const current = full.mcp_servers ?? []
          // add: append servers the agent does not have yet; remove: strip the
          // chosen ones. The two lists are exclusive by construction (the UI
          // moves a server between them).
          const next = [...current.filter((s) => !mcpRemove.includes(s)), ...mcpAdd.filter((s) => !current.includes(s))]
          patch.mcp_servers = next
        }
        if (applyParams) {
          patch.temperature = bulkTemp
          patch.max_tokens = bulkMaxTokens
          patch.max_turns = bulkMaxTurns
          patch.turn_limit_restarts = bulkTurnRestarts
        }
        await workspaceApi.updateAgent(id, patch)
        ok++
      } catch {
        failed.push(id)
      }
      setProgress((p) => (p ? { ...p, done: p.done + 1 } : p))
    }
    setDone(ok)
    setFailures(failed)
    onApplied()
  }

  const section = (checked: boolean, onCheck: () => void, label: string, content: React.ReactNode) => (
    <div style={{ padding: '0.6rem 0', borderTop: '1px solid var(--tm-border)' }}>
      <label style={{ display: 'flex', gap: '0.45rem', alignItems: 'center', fontSize: '0.82rem', fontWeight: 600, cursor: 'pointer' }}>
        <input type="checkbox" checked={checked} onChange={onCheck} style={{ accentColor: 'var(--tm-amber)' }} />
        {label}
      </label>
      {checked && <div style={{ marginTop: '0.45rem' }}>{content}</div>}
    </div>
  )

  // A collection editor in add/remove mode: chips of every available server,
  // three states per chip — stays / added (+) / removed (−).
  const collectionEditor = (add: string[], remove: string[], onToggleAdd: (id: string) => void, onToggleRemove: (id: string) => void) => (
    <div style={{ display: 'flex', flexWrap: 'wrap', gap: '0.3rem' }}>
      {mcpServers.map((server) => {
        const added = add.includes(server.id)
        const removed = remove.includes(server.id)
        const had = false // per-agent state differs; the apply-time merge is what matters
        const state: 'add' | 'remove' | 'keep' = added ? 'add' : removed ? 'remove' : 'keep'
        const label = added ? `+ ${server.name}` : removed ? `− ${server.name}` : server.name
        return (
          <button
            key={server.id}
            type="button"
            title={had ? (t('agents.bulk_present') ?? 'уже у агента') : (t('agents.bulk_absent') ?? 'у агента нет')}
            onClick={() => {
              // A chip flips add → remove → keep in one cycle; had-items start
              // at keep, others at absent-keep too.
              if (added) { onToggleAdd(server.id); onToggleRemove(server.id) }
              else if (removed) { onToggleRemove(server.id) }
              else { onToggleAdd(server.id) }
            }}
            style={{
              background: added ? 'var(--tm-success, #5e887a)' : removed ? 'var(--tm-danger)' : 'transparent',
              border: `1px solid ${added ? 'var(--tm-success, #5e887a)' : removed ? 'var(--tm-danger)' : 'var(--tm-border)'}`,
              borderRadius: 999,
              color: added || removed ? '#fff' : 'var(--tm-text-2)',
              cursor: 'pointer', fontSize: '0.72rem', padding: '0.15rem 0.5rem',
              opacity: had && !added && !removed ? 0.85 : 1,
            }}
          >
            {label}
          </button>
        )
      })}
    </div>
  )

  return (
    <AppModal onClose={onClose} panelStyle={{ width: '560px', maxHeight: '85vh', overflowY: 'auto' }}>
      <Heading>{t('agents.bulk_title', { count: String(agentIds.length) }) ?? `Массовая настройка: ${agentIds.length} агентов`}</Heading>
      <p style={{ color: 'var(--tm-text-3)', fontSize: '0.8rem', margin: '0.35rem 0 0.75rem' }}>
        {t('agents.bulk_hint_dialog') ?? 'Применяется только отмеченное. Прочие настройки агентов (промпты, навыки, права) не трогаются.'}
      </p>

      {section(applyModel, () => setApplyModel(!applyModel), t('agents.bulk_model') ?? 'Провайдер и модель', (
        <div style={{ display: 'flex', gap: '0.4rem' }}>
          <select value={providerId} onChange={(e) => { setProviderId(e.target.value); setModel('') }} style={{ ...bulkInputStyle, flex: 1 }}>
            <option value="">{t('agents.bulk_pick_provider') ?? 'Провайдер…'}</option>
            {providers.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
          </select>
          <select value={model} onChange={(e) => setModel(e.target.value)} disabled={!providerId} style={{ ...bulkInputStyle, flex: 1 }}>
            <option value="">{t('agents.bulk_pick_model') ?? 'Модель…'}</option>
            {providerModels.map((m) => <option key={m} value={m}>{m}</option>)}
          </select>
        </div>
      ))}

      {section(applySandbox, () => setApplySandbox(!applySandbox), t('agents.bulk_sandbox') ?? 'Профиль песочницы', (
        <select value={sandboxProfile} onChange={(e) => setSandboxProfile(e.target.value)} style={bulkInputStyle}>
          <option value="">{t('agents.bulk_pick_sandbox') ?? 'Профиль…'}</option>
          {sandboxProfiles.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
        </select>
      ))}

      {section(applySecrets, () => setApplySecrets(!applySecrets), t('agents.bulk_secrets_add') ?? 'Секреты: добавить выбранные всем', (
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: '0.3rem' }}>
          {secrets.map((s) => {
            const on = secretSet.includes(s.name)
            return (
              <button
                key={s.id}
                type="button"
                onClick={() => setSecretSet(on ? secretSet.filter((x) => x !== s.name) : [...secretSet, s.name])}
                style={{ background: on ? 'var(--tm-amber)' : 'transparent', border: `1px solid ${on ? 'var(--tm-amber)' : 'var(--tm-border)'}`, borderRadius: 999, color: on ? '#17211e' : 'var(--tm-text-2)', cursor: 'pointer', fontSize: '0.72rem', padding: '0.15rem 0.5rem' }}
              >
                {s.name}
              </button>
            )
          })}
        </div>
      ))}

      {section(applyMcp, () => setApplyMcp(!applyMcp), t('agents.bulk_mcp') ?? 'MCP-серверы: добавить/убрать', (
        <div>
          <p style={{ fontSize: '0.72rem', color: 'var(--tm-text-3)', margin: '0 0 0.35rem' }}>
            {t('agents.bulk_mcp_hint') ?? '+ добавит сервер всем, − уберёт у всех, без пометки — не тронет.'}
          </p>
          {collectionEditor(mcpAdd, mcpRemove,
            (id) => { setMcpAdd((prev) => prev.includes(id) ? prev.filter((x) => x !== id) : [...prev, id]); setMcpRemove((prev) => prev.filter((x) => x !== id)) },
            (id) => { setMcpRemove((prev) => prev.includes(id) ? prev.filter((x) => x !== id) : [...prev, id]); setMcpAdd((prev) => prev.filter((x) => x !== id)) })}
        </div>
      ) )}

      {section(applyParams, () => setApplyParams(!applyParams), t('agents.bulk_params') ?? 'Параметры генерации: температура, лимит токенов и ходов', (
        <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '0.6rem' }}>
          <label style={{ display: 'block', minWidth: 0 }}>
            <span style={{ display: 'block', fontSize: '0.72rem', color: 'var(--tm-text-3)', margin: '0 0 0.25rem' }}>{t('agents.temperature') ?? 'Temperature'}</span>
            <input
              type="number"
              onFocus={selectOnFocus}
              value={bulkTemp}
              min={0}
              max={1}
              step={0.05}
              onChange={(e) => {
                const parsed = Number(e.target.value)
                setBulkTemp(Number.isFinite(parsed) ? Math.min(1, Math.max(0, parsed)) : 0)
              }}
              style={bulkInputStyle}
            />
          </label>
          <div style={{ minWidth: 0 }}>
            <label style={{ display: 'block', fontSize: '0.72rem', color: 'var(--tm-text-3)', margin: '0 0 0.25rem' }}>{t('agents.max_tokens') ?? 'Max tokens'}</label>
            <input
              type="number"
              onFocus={selectOnFocus}
              value={bulkMaxTokens}
              min={64}
              max={1000000}
              step={1}
              onChange={(e) => setBulkMaxTokens(Math.max(64, Math.floor(Number(e.target.value) || 64)))}
              style={bulkInputStyle}
            />
          </div>
          <div style={{ minWidth: 0 }}>
            <label style={{ display: 'block', fontSize: '0.72rem', color: 'var(--tm-text-3)', margin: '0 0 0.25rem' }}>{t('agents.max_turns') ?? 'Max turns'}</label>
            <input
              type="number"
              onFocus={selectOnFocus}
              value={bulkMaxTurns}
              min={1}
              max={100}
              step={1}
              onChange={(e) => setBulkMaxTurns(Math.max(1, Math.floor(Number(e.target.value) || 1)))}
              style={bulkInputStyle}
            />
          </div>
          <div style={{ minWidth: 0 }}>
            <label style={{ display: 'block', fontSize: '0.72rem', color: 'var(--tm-text-3)', margin: '0 0 0.25rem' }} title={t('agents.turn_restarts_hint') ?? 'Если ран завершился только из-за лимита ходов, он автоматически продолжится новым раном с полным бюджетом ходов (в трейсе — run.restarted). 0 = выключено.'}>{t('agents.turn_restarts_short') ?? '× продолжений'}</label>
            <input
              type="number"
              onFocus={selectOnFocus}
              value={bulkTurnRestarts}
              min={0}
              max={5}
              step={1}
              onChange={(e) => setBulkTurnRestarts(Math.min(5, Math.max(0, Math.floor(Number(e.target.value) || 0))))}
              style={bulkInputStyle}
            />
          </div>
          <p style={{ flex: '1 1 100%', fontSize: '0.72rem', color: 'var(--tm-text-3)', margin: 0 }}>
            {t('agents.bulk_params_hint') ?? 'Значения применяются всем выбранным агентам как есть (текущие значения агентов не учитываются).'}
          </p>
        </div>
      ) )}

      {progress && (
        <p style={{ fontSize: '0.8rem', color: 'var(--tm-text-2)' }}>
          {t('agents.bulk_progress', { done: String(progress.done), total: String(progress.total) }) ?? `Применено: ${progress.done} из ${progress.total}…`}
        </p>
      )}
      {failures.length > 0 && (
        <p style={{ fontSize: '0.78rem', color: 'var(--tm-danger)' }}>
          {t('agents.bulk_failures', { count: String(failures.length) }) ?? `Не удалось применить к ${failures.length}`} — {failures.join(', ')}
        </p>
      )}
      {done > 0 && failures.length === 0 && (
        <p style={{ fontSize: '0.8rem', color: 'var(--tm-success, #5e887a)' }}>
          {t('agents.bulk_done', { count: String(done) }) ?? `Готово: применено к ${done} агентам.`}
        </p>
      )}

      <div className="form-actions" style={{ display: 'flex', justifyContent: 'flex-end', gap: '0.5rem', marginTop: '1rem' }}>
        <Button kind="secondary" onClick={onClose} disabled={!!progress && progress.done < progress.total}>
          {done > 0 ? (t('action.close') ?? 'Close') : (t('action.cancel') ?? 'Cancel')}
        </Button>
        <Button
          onClick={() => void apply()}
          disabled={(!applyModel && !applySandbox && !applySecrets && !applyMcp && !applyParams) || (!!progress && progress.done < progress.total)}
        >
          {progress && progress.done < progress.total
            ? (t('agents.bulk_applying') ?? 'Применяю…')
            : t('agents.bulk_apply', { count: String(agentIds.length) }) ?? `Применить к ${agentIds.length}`}
        </Button>
      </div>
    </AppModal>
  )
}
