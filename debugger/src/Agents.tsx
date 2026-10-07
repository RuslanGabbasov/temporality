import { useEffect, useState, useCallback } from 'react'
import {
  Button,
  TextInput,
  TextArea,
  Select,
  SelectItem,
  InlineNotification,
  Loading,
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
import { Add, Edit, TrashCan, Copy, Star, Renew } from '@carbon/icons-react'
import GeneratingState from './GeneratingState'
import ListFilter, { matchesFilter } from './ListFilter'
import {
  workspaceApi,
  type Agent,
  type AgentCapabilities,
  type AgentDefinition,
  type AgentDraft,
  type AgentVersion,
  type BuiltinAgentSpec,
  type Provider,
  type Skill,
  type MCPServer,
  type MCPServerTools,
  type MCPToolInfo,
  type Run,
} from './workspaceApi'
import { useT } from './i18n'
import { useOrgUnits, OrgUnitSelect, OrgBadge } from './orgUnits'

function message(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }

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
  for (const key of [...CAP_KEYS, 'delegation'] as const) {
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

function sameList(a?: string[], b?: string[]) {
  const x = cleanList(a), y = cleanList(b)
  return x.length === y.length && x.every((v, i) => v === y[i])
}

export default function Agents({ project, defaultAgentId, refreshProjects }: { project: string; defaultAgentId?: string; refreshProjects?: () => void }) {
  const t = useT()
  const org = useOrgUnits()
  const [agents, setAgents] = useState<Agent[]>([])
  const [providers, setProviders] = useState<Provider[]>([])
  const [builtins, setBuiltins] = useState<BuiltinAgentSpec[]>([])
  const [availableSkills, setAvailableSkills] = useState<Skill[]>([])
  const [mcpServers, setMcpServers] = useState<MCPServer[]>([])
  const [mcpTools, setMcpTools] = useState<MCPServerTools[]>([])
  const [builtinTools, setBuiltinTools] = useState<MCPToolInfo[]>([])
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

  // Evolution (§15): versions × runs grouped by definition version.
  const [versions, setVersions] = useState<AgentVersion[]>([])
  const [agentRuns, setAgentRuns] = useState<Run[]>([])
  const [evoLoadedFor, setEvoLoadedFor] = useState('')

  // Prompt tab preview (reflects the saved agent).
  const [promptPreview, setPromptPreview] = useState<{ source: string; prompt: string } | null>(null)

  // Form state
  const [form, setForm] = useState<Partial<Agent>>({})

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const [agentsData, providersData, skillsData, mcpData, toolsData, builtinsData] = await Promise.all([
        workspaceApi.listAllAgents(),
        workspaceApi.listProviders(),
        workspaceApi.listSkills(),
        workspaceApi.listMCPServers(),
        workspaceApi.listMCPTools(),
        workspaceApi.listBuiltinAgents(),
      ])
      setAgents(agentsData.agents ?? [])
      setProviders(providersData.providers ?? [])
      setAvailableSkills(skillsData.skills ?? [])
      setMcpServers(mcpData.servers ?? [])
      setMcpTools(Object.values(toolsData.servers ?? {}))
      setBuiltinTools(toolsData.builtins ?? [])
      setBuiltins(builtinsData.builtins ?? [])
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }, [project])

  useEffect(() => { void load() }, [load])

  const touch = (section: SectionKey) =>
    setProvenance((prev) => (prev[section] === 'user' ? prev : { ...prev, [section]: 'user' }))

  const setDef = (part: Partial<AgentDefinition>) =>
    setForm((prev) => ({ ...prev, definition: { ...prev.definition, ...part } }))

  const setCap = (key: CapKey | 'delegation', value: boolean | undefined) =>
    setForm((prev) => ({
      ...prev,
      definition: {
        ...prev.definition,
        capabilities: { ...prev.definition?.capabilities, [key]: value },
      },
    }))

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
    setFormTab('general')
    setFromWizard(false)
    setProvenance({})
    setDraftQuestions([])
    setPromptPreview(null)
    setForm({ name: '', model: '', skills: [], mcp_servers: [], tools: [], definition: {} })
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
      sandbox_profile: draft.suggested?.sandbox || '',
      network_access: draft.suggested?.network,
      skills: [],
      mcp_servers: [],
      tools: [],
    })
    setFromWizard(true)
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
    setFormTab('general')
    setFromWizard(false)
    setProvenance({})
    setDraftQuestions([])
    setPromptPreview(null)
    setForm({ ...agent, definition: agent.definition ? { ...agent.definition } : {} })
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
      const payload: Partial<Agent> & { prompt_source?: string } = { ...form, definition }
      if (fromWizard && !editing) payload.prompt_source = 'ai'
      if (editing) {
        await workspaceApi.updateAgent(editing.id, payload)
        // PUT payloads ignore org bindings: re-scoping goes through the admin
        // binding endpoint (docs/org-structure.md §39).
        if (org.isAdmin && (form.org_unit_id ?? '') !== (editing.org_unit_id ?? '')) {
          await workspaceApi.setResourceBinding('agent', editing.id, form.org_unit_id ?? '')
        }
      } else {
        await workspaceApi.createAgent(payload as Agent & { name: string })
      }
      setShowForm(false); setEditing(null); setFromWizard(false)
      void load()
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }

  const deleteAgent = async (agent: Agent) => {
    if (!confirm(t('agents.delete_confirm', { name: agent.name }) ?? `Delete agent "${agent.name}"?`)) return
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

  // Every project gets its own copy of the builtin agents on creation, so a
  // global list shows one duplicate per extra project. The panel is scoped
  // to the current project; global agents (no project) are always visible.
  const visibleAgents = agents.filter((a) => !a.project_id || a.project_id === project)

  // Client-side filter over the visible set (name/id).
  const [filter, setFilter] = useState('')
  const filteredAgents = visibleAgents.filter((a) => matchesFilter(filter, a.name, a.id, a.description))

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

  useEffect(() => {
    if (detailTab !== 'evolution' || !selectedId || evoLoadedFor === selectedId) return
    setEvoLoadedFor(selectedId)
    setVersions([])
    setAgentRuns([])
    void (async () => {
      try {
        const [vData, rData] = await Promise.all([
          workspaceApi.listAgentVersions(selectedId),
          workspaceApi.listAgentRuns(selectedId),
        ])
        setVersions(vData.versions ?? [])
        setAgentRuns(rData.runs ?? [])
      } catch (f) { setError(message(f)) }
    })()
  }, [detailTab, selectedId, evoLoadedFor])

  const runsByVersion = new Map<number, { total: number; completed: number; failed: number }>()
  for (const run of agentRuns) {
    const key = run.agent_version ?? 0
    const entry = runsByVersion.get(key) ?? { total: 0, completed: 0, failed: 0 }
    entry.total++
    if (run.status === 'completed') entry.completed++
    if (run.status === 'failed' || run.status === 'cancelled') entry.failed++
    runsByVersion.set(key, entry)
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
      sandbox_profile: spec.sandbox_profile,
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
      sandbox_profile: regenDraft.suggested?.sandbox || prev.sandbox_profile,
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

  /** Human summary of what changed between two definition versions. */
  const versionChanges = (prev?: AgentVersion, next?: AgentVersion): string[] => {
    if (!prev || !next) return []
    const changes: string[] = []
    if (prev.description !== next.description) changes.push(t('agents.diff_purpose') ?? 'purpose')
    for (const key of CAP_KEYS) {
      const before = prev.definition?.capabilities?.[key]
      const after = next.definition?.capabilities?.[key]
      if (before !== after) changes.push(t('agents.diff_cap', { name: capLabel(key) }) ?? `capability: ${capLabel(key)}`)
    }
    if (!sameList(prev.definition?.constraints, next.definition?.constraints)) changes.push(t('agents.diff_constraints') ?? 'constraints')
    if (!sameList(prev.definition?.completion, next.definition?.completion)) changes.push(t('agents.diff_completion') ?? 'completion checks')
    if ((prev.definition?.prompt_override ?? '') !== (next.definition?.prompt_override ?? '')) changes.push(t('agents.diff_override') ?? 'manual prompt override')
    return changes
  }

  const promptSourceLabel = (source: string) =>
    source === 'override' ? (t('agents.source_override') ?? 'manual override')
      : source === 'definition' ? (t('agents.source_definition') ?? 'compiled from definition')
        : source === 'legacy' ? (t('agents.source_legacy') ?? 'manual prompt (legacy)')
          : (t('agents.source_role') ?? 'base contract')

  return (
    <div style={{ padding: '1rem' }}>
      {error && <InlineNotification kind="error" title={t('action.error') ?? 'Error'} subtitle={error} onClose={() => setError('')} lowContrast style={{ marginBottom: '1rem' }} />}

      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1rem' }}>
        <Heading>{t('agents.title') ?? 'Agents'}</Heading>
        <Stack orientation="horizontal" gap={2}>
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
              </div>
              <ListFilter value={filter} onChange={setFilter} placeholder={t('common.filter_agents') ?? 'Filter agents…'} />
            </div>
            <Stack gap={1}>
              {filteredAgents.length === 0 && <Tile style={{ color: 'var(--tm-text-3)', textAlign: 'center' }}>{t('agents.none_created') ?? 'No agents yet'}</Tile>}
              {filteredAgents.map((a) => (
                <Tile
                  key={a.id}
                  onClick={() => setSelectedId(a.id)}
                  className={`workspace-tile ${selectedId === a.id ? 'selected' : ''}`}
                  style={{ padding: '0.5rem 0.75rem', cursor: 'pointer' }}
                >
                  <div style={{ display: 'flex', alignItems: 'center', gap: '0.4rem' }}>
                    <span style={{ fontWeight: 500, fontSize: '0.8rem', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', flex: 1, minWidth: 0 }}>{a.name}</span>
                    <OrgBadge orgUnitID={a.org_unit_id} org={org} />
                    {a.id === defaultAgentId && <Tag type="green" size="sm">{t('agents.default') ?? 'default'}</Tag>}
                  </div>
                  <div style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)', display: 'flex', gap: '0.25rem', flexWrap: 'wrap', marginTop: '0.15rem' }}>
                    <span>{a.model || (t('agents.no_model') ?? 'no model')}</span>
                    {a.provider && <span>· {providers.find((p) => p.id === a.provider)?.name ?? a.provider}</span>}
                    {a.labels?.builtin === 'true' && <span>· {t('agents.builtin_tag') ?? 'builtin'}</span>}
                    {!!a.definition_version && <span>· v{a.definition_version}</span>}
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

              {detailTab === 'evolution' && (
                <div>
                  {versions.length === 0 && (
                    <p style={{ color: 'var(--tm-text-3)' }}>
                      {t('agents.evolution_empty') ?? 'No definition versions yet. Versions appear when you edit the agent’s definition.'}
                    </p>
                  )}
                  {[...versions].sort((a, b) => b.version - a.version).map((v, idx) => {
                    const prev = [...versions].sort((a, b) => b.version - a.version)[idx + 1]
                    const stats = runsByVersion.get(v.version)
                    const changes = versionChanges(prev, v)
                    const isCurrent = v.version === selected.definition_version
                    return (
                      <div key={v.version} style={{ borderLeft: '2px solid var(--tm-border)', paddingLeft: '0.75rem', marginBottom: '1rem' }}>
                        <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', flexWrap: 'wrap' }}>
                          <strong>v{v.version}</strong>
                          {isCurrent && <Tag type="green" size="sm">{t('agents.version_current') ?? 'current'}</Tag>}
                          <Tag type="gray" size="sm">{v.prompt_source}</Tag>
                          {v.generator_model && <Tag type="blue" size="sm">{v.generator_model}</Tag>}
                          <span style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem' }}>
                            {new Date(v.created_at).toLocaleString()}{v.author ? ` · ${v.author}` : ''}
                          </span>
                        </div>
                        {changes.length > 0 ? (
                          <p style={{ margin: '0.25rem 0', fontSize: '0.8rem' }}>
                            {t('agents.changed') ?? 'Changed'}: {changes.join(', ')}
                          </p>
                        ) : (
                          <p style={{ margin: '0.25rem 0', fontSize: '0.8rem', color: 'var(--tm-text-3)' }}>
                            {prev ? (t('agents.no_changes') ?? 'no semantic changes') : (t('agents.initial_version') ?? 'initial definition')}
                          </p>
                        )}
                        <p style={{ margin: 0, fontSize: '0.8rem', color: 'var(--tm-text-2)' }}>
                          {t('agents.runs_total') ?? 'Runs'}: <strong>{stats?.total ?? 0}</strong>
                          {stats && <> · {t('agents.runs_completed') ?? 'completed'}: {stats.completed} · {t('agents.runs_failed') ?? 'failed'}: {stats.failed}</>}
                        </p>
                      </div>
                    )
                  })}
                </div>
              )}
            </Stack>
          )}
        </Column>
      </Grid>

      {/* Wizard: describe the specialist in natural language */}
      {wizardOpen && (
        <div className="modal-overlay">
          <div className="modal-panel" style={{ width: '560px' }}>
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
          </div>
        </div>
      )}

      {/* Edit/Create modal */}
      {showForm && (
        <div className="modal-overlay">
          <div className="modal-panel tabbed" style={{ width: '760px', maxWidth: 'calc(100vw - 2rem)', maxHeight: '85vh', minHeight: '480px' }}>
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
                  <TextInput id="agent-model" labelText={t('agents.model') ?? 'Model'} value={form.model ?? ''} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, model: e.target.value })} placeholder="mimo-v2.6-pro…" />
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
                <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr 1fr', gap: '0.5rem' }}>
                  <NumberInput id="agent-temp" label={t('agents.temperature') ?? 'Temperature'} value={form.temperature ?? 0} onChange={(_, { value }) => setForm({ ...form, temperature: typeof value === 'number' ? value : 0 })} min={0} max={2} step={0.1} />
                  <NumberInput id="agent-tokens" label={t('agents.max_tokens') ?? 'Max tokens'} value={form.max_tokens ?? 16384} onChange={(_, { value }) => setForm({ ...form, max_tokens: typeof value === 'number' ? value : 16384 })} min={64} max={1000000} />
                  <NumberInput id="agent-turns" label={t('agents.max_turns') ?? 'Max turns'} value={form.max_turns ?? 20} onChange={(_, { value }) => setForm({ ...form, max_turns: typeof value === 'number' ? value : 20 })} min={1} max={100} />
                </div>
                <Select id="agent-sandbox" labelText={t('agents.sandbox_profile') ?? 'Sandbox profile'} value={form.sandbox_profile ?? ''} onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setForm({ ...form, sandbox_profile: e.target.value })}>
                  <SelectItem value="" text={t('agents.default_label') ?? 'Default'} />
                  <SelectItem value="restricted" text={t('agents.restricted') ?? 'Restricted'} />
                  <SelectItem value="standard" text={t('agents.standard') ?? 'Standard'} />
                  <SelectItem value="privileged" text={t('agents.privileged') ?? 'Privileged'} />
                </Select>
                <div style={{ display: 'flex', gap: '1rem' }}>
                  <Toggle id="agent-network" labelText={t('agents.network') ?? 'Network'} toggled={form.network_access ?? false} onToggle={(checked: boolean) => setForm({ ...form, network_access: checked })} />
                  <Toggle id="agent-readonly" labelText={t('agents.read_only') ?? 'Read-only'} toggled={form.read_only ?? false} onToggle={(checked: boolean) => setForm({ ...form, read_only: checked })} />
                </div>
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
              <Button kind="secondary" onClick={() => { setShowForm(false); setEditing(null); setPromptPreview(null) }}>{t('action.cancel') ?? 'Cancel'}</Button>
              <Button onClick={() => void saveAgent()}>{editing ? (t('action.save') ?? 'Save') : (t('action.create') ?? 'Create')}</Button>
            </div>
            </>
            )}
          </div>
        </div>
      )}

      {/* Rebuild diff (§13): user edits are never silently replaced */}
      {regenDraft && (
        <div className="modal-overlay">
          <div className="modal-panel" style={{ width: '640px', maxHeight: '85vh', display: 'flex', flexDirection: 'column' }}>
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
          </div>
        </div>
      )}

      {loading && <Loading withOverlay={false} />}

      {/* Template selection modal: curated builtin definitions from the server */}
      {showTemplates && (
        <div className="modal-overlay">
          <div className="modal-panel">
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
                    sandbox_profile: tpl.sandbox_profile,
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
                    <Tag type="gray" size="sm">{tpl.sandbox_profile}</Tag>
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
          </div>
        </div>
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
        <div style={{ maxHeight: '12rem', overflow: 'auto', padding: '0.5rem 0.75rem', border: '1px solid var(--tm-border)', borderRadius: '6px' }}>
          <p className="cds--label" style={{ fontSize: '0.7rem', margin: '0 0 0.25rem' }}>{t('agents.tools_builtin') ?? 'Built-in'}</p>
          {builtinDefs.map((tool) => row(tool, `b-${tool.name}`))}
          {servers.map((server) => (
            <div key={server.id}>
              <p className="cds--label" style={{ fontSize: '0.7rem', margin: '0.5rem 0 0.25rem' }}>{server.name}</p>
              {server.tools.length === 0 && (
                <p style={{ color: 'var(--tm-text-3)', fontSize: '0.7rem', margin: 0 }}>{t('agents.tools_no_tools') ?? 'no tools discovered'}</p>
              )}
              {server.tools.map((tool) => row(tool, `s-${server.id}-${tool.name}`))}
            </div>
          ))}
        </div>
      </div>
    )
  }
