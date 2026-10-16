import { useCallback, useEffect, useMemo, useState } from 'react'
import {
  Button,
  TextInput,
  TextArea,
  Select,
  SelectItem,
  InlineNotification,
  Tag,
  Tile,
  Stack,
  Heading,
  Checkbox,
} from '@carbon/react'
import PageLoading from './PageLoading'
import { Add, Edit, TrashCan, Renew, Play, MagicWand } from '@carbon/icons-react'
import ListFilter, { matchesFilter } from './ListFilter'
import AppModal from './Modal'
import GeneratingState from './GeneratingState'
import { useConfirm } from './ConfirmDialog'
import {
  workspaceApi,
  ApiError,
  TEAM_CAPABILITIES,
  TEAM_PROTOCOL_KINDS,
  type Team,
  type TeamManifest,
  type TeamProtocolKind,
  type TeamSlot,
  type TeamStep,
  type TeamVersion,
  type TeamResolvePreview,
  type TeamDraft,
  type SkillDraftQuestion,
  type Agent,
  type Skill,
  type Project,
} from './workspaceApi'
import { observationApi, type ObservationEvent } from './observationApi'
import { teamRunsFromEvents, type TeamRunRow } from './teamRuns'
import { useT } from './i18n'
import { useOrgUnits, OrgUnitSelect, OrgBadge } from './orgUnits'
import { INTAKE_PROJECT_ID } from './kernelApi'

function message(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }

/** Locale function as passed around components (matches useT). */
type Translate = (key: string, vars?: Record<string, string>) => string

type TeamTab = 'main' | 'slots' | 'runs' | 'evolution'

/** Slot in the edit form: text fields stay strings until the manifest is
 *  assembled, capabilities are checkboxes, skills arrive as a comma-separated
 *  list (the registry of ids is free-form, unlike the fixed capability set). */
interface TeamSlotForm {
  id: string
  title: string
  responsibility: string
  contract: string
  mode: 'fixed' | 'role'
  agent_id: string
  preferred_agent_id: string
  capabilities: string[]
  skillsText: string
}

interface TeamFormState {
  id: string
  name: string
  description: string
  version: string
  org_unit_id: string
  protocol: TeamProtocolKind
  maxRework: string
  maxTurns: string
  budgetUSD: string
  parallelLimit: string
  steps: TeamStep[]
  slots: TeamSlotForm[]
}

const splitList = (text: string) => text.split(',').map((v) => v.trim()).filter(Boolean)

function slotToForm(slot: TeamSlot): TeamSlotForm {
  return {
    id: slot.id,
    title: slot.title,
    responsibility: slot.responsibility ?? '',
    contract: slot.contract ?? '',
    mode: slot.binding?.mode === 'fixed' ? 'fixed' : 'role',
    agent_id: slot.binding?.agent_id ?? '',
    preferred_agent_id: slot.binding?.preferred_agent_id ?? '',
    capabilities: [...(slot.requirements?.capabilities ?? [])],
    skillsText: (slot.requirements?.skills ?? []).join(', '),
  }
}

/** Wizard seed templates (wave E): suggested starting points for the builder,
 *  not pre-created entities — the user always creates their own team. */
const TEAM_TEMPLATES = ['code_delivery', 'research_fanout', 'delivery_qa_gate'] as const

function emptySlotForm(n: number): TeamSlotForm {
  return {
    id: `worker-${n}`,
    title: '',
    responsibility: '',
    contract: '',
    mode: 'role',
    agent_id: '',
    preferred_agent_id: '',
    capabilities: [],
    skillsText: '',
  }
}

type ManifestFormPart = Pick<TeamFormState, 'protocol' | 'maxRework' | 'maxTurns' | 'budgetUSD' | 'parallelLimit' | 'steps' | 'slots'>

function manifestToForm(manifest: TeamManifest | undefined): ManifestFormPart {
  const m = manifest ?? { protocol: { kind: 'pipeline' as TeamProtocolKind }, slots: [] }
  return {
    protocol: TEAM_PROTOCOL_KINDS.includes(m.protocol?.kind) ? m.protocol.kind : 'pipeline',
    maxRework: m.protocol?.max_rework ? String(m.protocol.max_rework) : '',
    maxTurns: m.defaults?.max_turns ? String(m.defaults.max_turns) : '',
    budgetUSD: m.defaults?.budget_usd ? String(m.defaults.budget_usd) : '',
    parallelLimit: m.defaults?.parallel_limit ? String(m.defaults.parallel_limit) : '',
    steps: (m.protocol?.steps ?? []).map((step) => ({ ...step, depends_on: [...(step.depends_on ?? [])], review_of: [...(step.review_of ?? [])] })),
    slots: (m.slots ?? []).map(slotToForm),
  }
}

const num = (text: string) => {
  const value = Number(text.trim())
  return text.trim() !== '' && Number.isFinite(value) ? value : undefined
}

/** Assemble the structured manifest from the form. Undefined optional fields
 *  drop out of the JSON — the server validates the result (422 is human
 *  readable and shown inline in the form). */
function formToManifest(form: TeamFormState): TeamManifest {
  const slots: TeamSlot[] = form.slots.map((slot) => {
    const capabilities = slot.mode === 'role' ? slot.capabilities : []
    const skills = slot.mode === 'role' ? splitList(slot.skillsText) : []
    return {
      id: slot.id.trim(),
      title: slot.title.trim() || slot.id.trim(),
      responsibility: slot.responsibility.trim() || undefined,
      requirements: capabilities.length > 0 || skills.length > 0 ? { capabilities, skills } : undefined,
      contract: slot.contract.trim() || undefined,
      binding: slot.mode === 'fixed'
        ? { mode: 'fixed', agent_id: slot.agent_id }
        : { mode: 'role', preferred_agent_id: slot.preferred_agent_id.trim() || undefined },
    }
  })
  const manifest: TeamManifest = {
    protocol: { kind: form.protocol },
    slots,
  }
  const maxRework = num(form.maxRework)
  if (maxRework !== undefined) manifest.protocol.max_rework = maxRework
  if (form.protocol === 'dag') manifest.protocol.steps = form.steps
  const defaults: typeof manifest.defaults = {}
  const maxTurns = num(form.maxTurns)
  const budget = num(form.budgetUSD)
  const parallel = num(form.parallelLimit)
  if (maxTurns !== undefined) defaults.max_turns = maxTurns
  if (budget !== undefined) defaults.budget_usd = budget
  if (parallel !== undefined) defaults.parallel_limit = parallel
  if (Object.keys(defaults).length > 0) manifest.defaults = defaults
  return manifest
}

function protocolLabel(kind: string, t: Translate): string {
  return t(`teams.protocol_${kind}`) ?? kind
}

function originLabel(origin: string | undefined, t: Translate): string {
  if (origin === 'initial') return t('teams.origin_initial') ?? 'initial'
  if (origin === 'human_edit') return t('teams.origin_human_edit') ?? 'human edit'
  if (origin === 'agent_proposal') return t('teams.origin_agent_proposal') ?? 'agent proposal'
  return origin || '—'
}

function statusTag(status: string, t: Translate) {
  if (status === 'active') return <Tag type="green" size="sm">{t('teams.version_active') ?? 'active'}</Tag>
  if (status === 'draft') return <Tag type="purple" size="sm">{t('teams.version_draft') ?? 'draft'}</Tag>
  if (status === 'rejected') return <Tag type="red" size="sm">{t('teams.version_rejected') ?? 'rejected'}</Tag>
  return <Tag type="gray" size="sm">{status || '—'}</Tag>
}

export default function Teams({ project }: { project: string }) {
  const t = useT()
  const { confirmDialog, dialog: confirmDialogNode } = useConfirm()
  const org = useOrgUnits()
  const [teams, setTeams] = useState<Team[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [selected, setSelected] = useState<Team | null>(null)
  const [tab, setTab] = useState<TeamTab>('main')
  const [filter, setFilter] = useState('')
  const [versions, setVersions] = useState<TeamVersion[]>([])
  const [runs, setRuns] = useState<TeamRunRow[]>([])
  // Launch dialog + edit form data (agents, skills, projects) are shared.
  const [agents, setAgents] = useState<Agent[]>([])
  const [skills, setSkills] = useState<Skill[]>([])
  const [projects, setProjects] = useState<Project[]>([])
  const [showForm, setShowForm] = useState(false)
  const [editing, setEditing] = useState<Team | null>(null)
  const [form, setForm] = useState<TeamFormState | null>(null)
  const [formError, setFormError] = useState('')
  const [saving, setSaving] = useState(false)
  const [launchFor, setLaunchFor] = useState<Team | null>(null)
  // Wizard (wave E): description → agent-built draft → prefilled form.
  // One clarification round by design, mirroring the skill builder.
  const [wizardOpen, setWizardOpen] = useState(false)
  const [wizardText, setWizardText] = useState('')
  const [wizardBusy, setWizardBusy] = useState(false)
  const [wizardError, setWizardError] = useState('')
  const [draftQuestions, setDraftQuestions] = useState<SkillDraftQuestion[]>([])
  const [answers, setAnswers] = useState<Record<string, string>>({})
  const [builderProvenance, setBuilderProvenance] = useState<Partial<Record<'protocol' | 'defaults' | 'slots', boolean>>>({})
  // Snapshot of the form exactly as the builder produced it: a difference
  // against the live form means manual edits exist and regeneration must ask.
  const [lastDraftForm, setLastDraftForm] = useState<TeamFormState | null>(null)

  const load = useCallback(async () => {
    setLoading(true); setError('')
    try {
      const data = await workspaceApi.listTeams()
      setTeams(data.teams ?? [])
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }, [])

  useEffect(() => { void load() }, [load])

  // Reference data for the form selects and the launch dialog roster.
  useEffect(() => {
    let cancelled = false
    void (async () => {
      try {
        const [agentData, skillData, projectData] = await Promise.all([
          workspaceApi.listAllAgents().catch(() => ({ agents: [] as Agent[] })),
          workspaceApi.listSkills().catch(() => ({ skills: [] as Skill[] })),
          workspaceApi.listProjects().catch(() => ({ projects: [] as Project[] })),
        ])
        if (cancelled) return
        setAgents(agentData.agents ?? [])
        setSkills(skillData.skills ?? [])
        setProjects((projectData.projects ?? []).filter((p) => p.id !== INTAKE_PROJECT_ID))
      } catch { /* reference data is optional decoration */ }
    })()
    return () => { cancelled = true }
  }, [])

  const agentName = useCallback((id: string | undefined) => agents.find((a) => a.id === id)?.name ?? id ?? '', [agents])

  // Opening a team pulls its version history and run history in parallel.
  const openTeam = useCallback(async (team: Team) => {
    setSelected(team)
    setTab('main')
    setRuns([])
    setVersions([])
    try {
      const [versionData, startedPage, completedPage, failedPage, cancelledPage] = await Promise.all([
        workspaceApi.listTeamVersions(team.id).catch(() => ({ versions: [] as TeamVersion[] })),
        observationApi.events(undefined, undefined, undefined, undefined, { type: 'team.started', limit: 500 }).catch(() => ({ events: [] as ObservationEvent[] })),
        observationApi.events(undefined, undefined, undefined, undefined, { type: 'team.completed', limit: 500 }).catch(() => ({ events: [] as ObservationEvent[] })),
        observationApi.events(undefined, undefined, undefined, undefined, { type: 'team.failed', limit: 500 }).catch(() => ({ events: [] as ObservationEvent[] })),
        observationApi.events(undefined, undefined, undefined, undefined, { type: 'run.cancelled', limit: 500 }).catch(() => ({ events: [] as ObservationEvent[] })),
      ])
      setVersions(versionData.versions ?? [])
      setRuns(teamRunsFromEvents(team.id, startedPage.events, completedPage.events, failedPage.events, cancelledPage.events))
    } catch { /* per-team panels degrade to empty, the main list shows errors */ }
  }, [])

  const visibleTeams = useMemo(
    () => teams.filter((team) => matchesFilter(filter, team.name, team.id, team.description)),
    [teams, filter],
  )

  // ── Wizard: natural language → agent-built draft ──────────────────────

  const startWizard = () => {
    setWizardText('')
    setWizardError('')
    setDraftQuestions([])
    setAnswers({})
    setWizardOpen(true)
  }

  const applyDraft = (draft: TeamDraft) => {
    const prefilled: TeamFormState = {
      id: '',
      name: draft.name || '',
      description: draft.description || '',
      version: '1.0.0',
      org_unit_id: '',
      ...manifestToForm(draft.manifest),
    }
    setDraftQuestions(draft.questions ?? [])
    setAnswers({})
    setBuilderProvenance({ protocol: true, defaults: true, slots: true })
    setLastDraftForm(prefilled)
    setForm(prefilled)
    setEditing(null)
    setFormError('')
    setShowForm(true)
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
      const draft = await workspaceApi.draftTeam(description, draftQuestions.length > 0)
      if ((draft.questions?.length ?? 0) > 0) {
        // Questions live in the wizard: keep it open, collect the answers and
        // run the one final round.
        setDraftQuestions(draft.questions ?? [])
        setAnswers({})
        return
      }
      // Regenerating while the form holds manual edits replaces them — ask
      // first instead of losing the work silently.
      if (showForm && form && lastDraftForm && JSON.stringify(form) !== JSON.stringify(lastDraftForm)) {
        if (!(await confirmDialog({
          title: t('teams.regenerate_confirm_title') ?? 'Rebuild the draft?',
          body: t('teams.regenerate_confirm') ?? 'The form holds manual edits — rebuilding replaces them with a fresh draft.',
          confirmLabel: t('teams.regenerate') ?? 'Rebuild',
        }))) return
      }
      applyDraft(draft)
      setWizardOpen(false)
    } catch (f) {
      setWizardError(message(f))
    } finally {
      setWizardBusy(false)
    }
  }

  // "Rebuild" from the form: reopen the wizard with the original description
  // so the user can adjust it before regenerating.
  const reopenWizard = () => {
    setDraftQuestions([])
    setAnswers({})
    setWizardOpen(true)
  }

  const startManual = () => {
    setWizardOpen(false)
    startCreate()
  }

  const startCreate = () => {
    setEditing(null)
    setFormError('')
    setBuilderProvenance({})
    setLastDraftForm(null)
    setForm({
      id: '', name: '', description: '', version: '1.0.0', org_unit_id: '',
      protocol: 'pipeline', maxRework: '', maxTurns: '', budgetUSD: '', parallelLimit: '',
      steps: [], slots: [emptySlotForm(1), emptySlotForm(2)],
    })
    setShowForm(true)
  }

  const startEdit = (team: Team) => {
    setEditing(team)
    setFormError('')
    setBuilderProvenance({})
    setLastDraftForm(null)
    setForm({
      id: team.id,
      name: team.name,
      description: team.description,
      version: team.version,
      org_unit_id: team.org_unit_id ?? '',
      ...manifestToForm(team.manifest),
    })
    setShowForm(true)
  }

  const save = async () => {
    if (!form) return
    if (!form.name.trim()) { setFormError(t('teams.error_name') ?? 'Name is required'); return }
    if (form.slots.some((slot) => !slot.id.trim())) { setFormError(t('teams.error_slot_id') ?? 'Every slot needs an id'); return }
    const ids = form.slots.map((slot) => slot.id.trim())
    if (new Set(ids).size !== ids.length) { setFormError(t('teams.error_slot_dup') ?? 'Slot ids must be unique'); return }
    if (form.slots.some((slot) => slot.mode === 'fixed' && !slot.agent_id)) { setFormError(t('teams.error_fixed_agent') ?? 'A fixed slot needs an agent'); return }
    setSaving(true); setFormError('')
    const manifest = formToManifest(form)
    try {
      if (editing) {
        const updated = await workspaceApi.updateTeam(editing.id, {
          name: form.name.trim(), description: form.description, version: form.version.trim() || editing.version, manifest,
        })
        setShowForm(false)
        await openTeam(updated)
      } else {
        const created = await workspaceApi.createTeam({
          id: form.id.trim() || undefined, name: form.name.trim(), description: form.description,
          version: form.version.trim() || undefined, manifest, org_unit_id: form.org_unit_id || undefined,
        })
        setShowForm(false)
        await openTeam(created)
      }
      void load()
    } catch (f) { setFormError(message(f)) }
    finally { setSaving(false) }
  }

  const remove = async (team: Team) => {
    if (!(await confirmDialog({
      title: t('teams.delete_confirm', { name: team.name }) ?? `Delete team "${team.name}"?`,
      body: t('action.delete_warning') ?? 'This cannot be undone.',
      danger: true, confirmLabel: t('action.delete') ?? 'Delete',
    }))) return
    setLoading(true); setError('')
    try {
      await workspaceApi.deleteTeam(team.id)
      if (selected?.id === team.id) setSelected(null)
      void load()
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }

  // Applying a draft is the explicit human approval that makes it the current
  // revision — drafts never launch. Rejecting dismisses the draft forever.
  const applyVersion = async (team: Team, version: string) => {
    setLoading(true); setError('')
    try {
      const updated = await workspaceApi.applyTeamVersion(team.id, version)
      await openTeam(updated)
      void load()
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }

  const rejectVersion = async (team: Team, version: string) => {
    setLoading(true); setError('')
    try {
      await workspaceApi.rejectTeamVersion(team.id, version)
      const fresh = await workspaceApi.getTeam(team.id)
      await openTeam(fresh)
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }

  const isDraft = selected?.version_status === 'draft'

  return (
    <div style={{ padding: '1rem' }}>
      {confirmDialogNode}
      {error && <InlineNotification kind="error" title={t('action.error') ?? 'Error'} subtitle={error} onClose={() => setError('')} lowContrast style={{ marginBottom: '1rem' }} />}

      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1rem' }}>
        <Heading>{t('teams.title') ?? 'Teams'}</Heading>
        <Stack orientation="horizontal" gap={3}>
          <Button kind="ghost" hasIconOnly renderIcon={Renew} iconDescription={t('action.refresh') ?? 'Refresh'} onClick={() => void load()} />
          <Button kind="secondary" renderIcon={MagicWand} onClick={startWizard}>{t('teams.wizard_create') ?? 'Build a team'}</Button>
          <Button renderIcon={Add} onClick={startCreate}>{t('teams.new_team') ?? 'New Team'}</Button>
        </Stack>
      </div>

      {loading && <PageLoading />}

      {teams.length === 0 && !loading && (
        <Tile style={{ textAlign: 'center', padding: '3rem', color: 'var(--tm-text-3)' }}>
          <p>{t('teams.no_teams') ?? 'No teams yet.'}</p>
          <p style={{ fontSize: '0.8rem', marginTop: '0.5rem' }}>{t('teams.no_teams_hint') ?? 'A team is a versioned multi-agent composition: role slots plus a collaboration protocol.'}</p>
        </Tile>
      )}

      {teams.length > 0 && (
        <div style={{ display: 'grid', gridTemplateColumns: 'minmax(220px, 300px) 1fr', gap: '1rem', alignItems: 'start' }}>
          <div style={{ position: 'sticky', top: '3rem', maxHeight: 'calc(100vh - 4rem)', overflowY: 'auto' }}>
            <div style={{ position: 'sticky', top: 0, background: 'var(--tm-bg)', zIndex: 1, paddingBottom: '0.25rem' }}>
              <ListFilter value={filter} onChange={setFilter} placeholder={t('common.filter_teams') ?? 'Filter teams…'} />
            </div>
            {visibleTeams.map((team) => (
              <Tile
                key={team.id}
                style={{
                  marginBottom: '0.5rem', cursor: 'pointer', padding: '0.75rem',
                  outline: selected?.id === team.id ? '1px solid var(--tm-amber)' : 'none',
                }}
                onClick={() => { if (selected?.id !== team.id) void openTeam(team) }}
              >
                <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: '0.5rem' }}>
                  <strong style={{ overflow: 'hidden', textOverflow: 'ellipsis' }}>{team.name}</strong>
                  <span style={{ display: 'inline-flex', alignItems: 'center', gap: '0.25rem', flexShrink: 0 }}>
                    <OrgBadge orgUnitID={team.org_unit_id} org={org} />
                    {team.version_status === 'draft' && <Tag size="sm" type="purple">{t('teams.version_draft') ?? 'draft'}</Tag>}
                    <Tag size="sm">{team.version}</Tag>
                  </span>
                </div>
                <div style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)' }}>
                  {protocolLabel(team.manifest?.protocol?.kind ?? '', t)} · {t('teams.slots_count', { count: String(team.manifest?.slots?.length ?? 0) }) ?? `${team.manifest?.slots?.length ?? 0} slots`}
                </div>
              </Tile>
            ))}
          </div>

          {selected ? (
            <div className="skill-card">
              <div className="skill-card-header">
                <div>
                  <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', flexWrap: 'wrap' }}>
                    <strong style={{ fontSize: '1.1rem' }}>{selected.name}</strong>
                    <Tag size="sm">{selected.version}</Tag>
                    {selected.version_status === 'draft' && <Tag size="sm" type="purple">{t('teams.version_draft') ?? 'draft'}</Tag>}
                    <OrgBadge orgUnitID={selected.org_unit_id} org={org} />
                    <code style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)' }}>{selected.id}</code>
                  </div>
                  {selected.description && <p style={{ color: 'var(--tm-text-2)', marginTop: '0.5rem' }}>{selected.description}</p>}
                </div>
                <Stack orientation="horizontal" gap={1}>
                  <Button size="sm" renderIcon={Play} disabled={isDraft} onClick={() => setLaunchFor(selected)}>{t('teams.launch') ?? 'Launch'}</Button>
                  <Button size="sm" kind="ghost" hasIconOnly renderIcon={Edit} iconDescription={t('action.edit') ?? 'Edit'} onClick={() => startEdit(selected)} />
                  <Button size="sm" kind="danger--ghost" hasIconOnly renderIcon={TrashCan} iconDescription={t('action.delete') ?? 'Delete'} onClick={() => void remove(selected)} />
                </Stack>
              </div>

              {isDraft && (
                <div style={{ padding: '0 1.25rem' }}>
                  <InlineNotification
                    kind="warning"
                    lowContrast
                    title={t('teams.draft_notice_title') ?? 'Draft version'}
                    subtitle={t('teams.draft_notice') ?? 'This version is a draft — apply it on the Evolution tab before launching.'}
                  />
                </div>
              )}

              <div className="skill-tabs" role="tablist">
                {(['main', 'slots', 'runs', 'evolution'] as TeamTab[]).map((key) => (
                  <button
                    key={key}
                    role="tab"
                    aria-selected={tab === key}
                    className={`skill-tab ${tab === key ? 'active' : ''}`}
                    onClick={() => setTab(key)}
                  >
                    {t(`teams.tab_${key}`) ?? key}
                    {key === 'runs' && runs.length > 0 && <span className="skill-tab-count">{runs.length}</span>}
                    {key === 'evolution' && versions.length > 0 && <span className="skill-tab-count">{versions.length}</span>}
                  </button>
                ))}
              </div>

              <div className="skill-card-body">
                {tab === 'main' && <TeamMainTab team={selected} versions={versions.length} runs={runs.length} t={t} />}
                {tab === 'slots' && <TeamSlotsTab team={selected} agentName={agentName} t={t} />}
                {tab === 'runs' && <TeamRunsTab runs={runs} t={t} />}
                {tab === 'evolution' && (
                  <TeamEvolutionTab
                    team={selected}
                    versions={versions}
                    t={t}
                    onApply={(version) => void applyVersion(selected, version)}
                    onReject={(version) => void rejectVersion(selected, version)}
                  />
                )}
              </div>
            </div>
          ) : (
            <Tile style={{ textAlign: 'center', padding: '3rem', color: 'var(--tm-text-3)' }}>
              {t('teams.select_hint') ?? 'Select a team to see its composition and runs.'}
            </Tile>
          )}
        </div>
      )}

      {showForm && form && (
        <TeamForm
          form={form}
          setForm={setForm}
          editing={editing}
          agents={agents}
          skills={skills}
          org={org}
          error={formError}
          saving={saving}
          provenance={builderProvenance}
          canRegenerate={Boolean(lastDraftForm) && wizardText.trim().length > 0}
          onRegenerate={reopenWizard}
          onSave={() => void save()}
          onClose={() => setShowForm(false)}
        />
      )}

      {/* Wizard: describe the team in natural language */}
      {wizardOpen && (
        <AppModal onClose={() => setWizardOpen(false)} panelStyle={{ width: '640px' }}>
          <Heading>{t('teams.wizard_title') ?? 'Build a team'}</Heading>
          {wizardBusy ? (
            <GeneratingState
              title={t('teams.generating_title') ?? 'Building team draft…'}
              hint={t('teams.generating_hint') ?? 'The builder agent turns your description into a protocol with role slots. This usually takes less than a minute.'}
            />
          ) : (
            <>
              <p style={{ color: 'var(--tm-text-3)', fontSize: '0.8rem', margin: '0.25rem 0 0.75rem' }}>
                {t('teams.wizard_hint') ?? 'Describe the collaboration — the agent turns it into a structured team draft you can adjust afterwards.'}
              </p>
              {draftQuestions.length === 0 && (
                <div style={{ marginBottom: '0.75rem' }}>
                  <div className="skill-subheading">{t('teams.wizard_templates') ?? 'Templates'}</div>
                  <div style={{ display: 'flex', gap: '0.5rem', flexWrap: 'wrap' }}>
                    {TEAM_TEMPLATES.map((key) => (
                      <Button key={key} size="sm" kind="secondary" onClick={() => setWizardText(t(`teams.template_${key}`) ?? '')}>
                        {t(`teams.template_${key}_name`) ?? key}
                      </Button>
                    ))}
                  </div>
                </div>
              )}
              <TextArea
                id="team-wizard-description"
                hideLabel
                labelText={t('teams.wizard_label') ?? 'Describe what the team should do'}
                rows={7}
                value={wizardText}
                onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => setWizardText(e.target.value)}
                placeholder={t('teams.wizard_placeholder') ?? 'A developer implements the task in the repository, a reviewer checks the change and sends it back…'}
                autoFocus
              />
              {wizardError && <InlineNotification kind="error" title={t('teams.wizard_failed') ?? 'Generation failed'} subtitle={wizardError} lowContrast hideCloseButton style={{ marginTop: '0.5rem' }} />}
              {draftQuestions.length > 0 && (
                <div className="wizard-questions">
                  <div className="skill-subheading">{t('teams.agent_questions') ?? 'The agent asks for clarification'}</div>
                  <p style={{ fontSize: '0.75rem', color: 'var(--tm-text-3)', margin: '0 0 0.5rem' }}>
                    {t('teams.agent_questions_hint') ?? 'Answer and send — the draft will be rebuilt once with your answers. No more questions follow: anything ambiguous gets sensible defaults.'}
                  </p>
                  {draftQuestions.map((q, i) => (
                    <div key={i} style={{ marginBottom: '0.5rem' }}>
                      <p style={{ margin: '0 0 0.25rem', fontSize: '0.8rem', color: 'var(--tm-text-2)' }}>{q.question}</p>
                      <TextInput
                        id={`team-answer-${i}`}
                        hideLabel
                        labelText=""
                        value={answers[String(i)] ?? ''}
                        onChange={(e: React.ChangeEvent<HTMLInputElement>) => setAnswers({ ...answers, [String(i)]: e.target.value })}
                        placeholder={t('skills.answer_placeholder') ?? 'Your answer'}
                      />
                    </div>
                  ))}
                </div>
              )}
              <div className="form-actions">
                <Button kind="secondary" onClick={() => setWizardOpen(false)}>{t('action.cancel') ?? 'Cancel'}</Button>
                <Button kind="secondary" onClick={startManual}>{t('skills.wizard_manual') ?? 'Fill manually'}</Button>
                <Button onClick={() => void generate()} disabled={wizardBusy || !wizardText.trim()}>
                  {draftQuestions.length ? (t('skills.send_answers') ?? 'Send answers') : (t('teams.wizard_generate') ?? 'Build team')}
                </Button>
              </div>
            </>
          )}
        </AppModal>
      )}

      {launchFor && (
        <LaunchDialog
          team={launchFor}
          agents={agents}
          projects={projects}
          currentProject={project}
          onClose={() => setLaunchFor(null)}
        />
      )}
    </div>
  )
}

function TeamMainTab({ team, versions, runs, t }: { team: Team; versions: number; runs: number; t: Translate }) {
  const manifest = team.manifest ?? { protocol: { kind: 'pipeline' as TeamProtocolKind }, slots: [] }
  return (
    <Stack gap={4}>
      <div>
        <div className="skill-subheading">{t('teams.protocol') ?? 'Protocol'}</div>
        <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'center', flexWrap: 'wrap' }}>
          <Tag type="blue">{protocolLabel(manifest.protocol?.kind ?? '', t)}</Tag>
          {manifest.protocol?.max_rework ? <Tag type="gray">{t('teams.max_rework', { count: String(manifest.protocol.max_rework) }) ?? `rework ≤ ${manifest.protocol.max_rework}`}</Tag> : null}
        </div>
        <p style={{ fontSize: '0.75rem', color: 'var(--tm-text-3)', marginTop: '0.4rem' }}>{t(`teams.protocol_hint_${manifest.protocol?.kind ?? ''}`) ?? ''}</p>
      </div>

      {manifest.protocol?.kind === 'dag' && (manifest.protocol.steps?.length ?? 0) > 0 && (
        <div>
          <div className="skill-subheading">{t('teams.steps') ?? 'Steps'}</div>
          <Stack gap={2}>
            {manifest.protocol.steps!.map((step) => (
              <div key={step.id} style={{ display: 'flex', gap: '0.5rem', alignItems: 'center', flexWrap: 'wrap', fontSize: '0.8rem' }}>
                <strong>{step.id}</strong>
                <Tag size="sm">{step.slot_id}</Tag>
                {(step.depends_on ?? []).map((dep) => <Tag key={dep} size="sm" type="cool-gray">← {dep}</Tag>)}
                {(step.review_of ?? []).map((rev) => <Tag key={rev} size="sm" type="purple">⎇ {rev}</Tag>)}
              </div>
            ))}
          </Stack>
        </div>
      )}

      <div>
        <div className="skill-subheading">{t('teams.defaults') ?? 'Defaults'}</div>
        <div className="skill-stats">
          <div className="skill-stat"><span className="skill-stat-value">{manifest.slots?.length ?? 0}</span><span className="skill-stat-label">{t('teams.stat_slots') ?? 'Slots'}</span></div>
          <div className="skill-stat"><span className="skill-stat-value">{manifest.defaults?.max_turns ?? '—'}</span><span className="skill-stat-label">{t('teams.max_turns') ?? 'Max turns'}</span></div>
          <div className="skill-stat"><span className="skill-stat-value">{manifest.defaults?.budget_usd ? `$${manifest.defaults.budget_usd}` : '—'}</span><span className="skill-stat-label">{t('teams.budget_usd') ?? 'Budget'}</span></div>
          <div className="skill-stat"><span className="skill-stat-value">{manifest.defaults?.parallel_limit ?? '—'}</span><span className="skill-stat-label">{t('teams.parallel_limit') ?? 'Parallel limit'}</span></div>
          <div className="skill-stat"><span className="skill-stat-value">{versions}</span><span className="skill-stat-label">{t('teams.stat_versions') ?? 'Versions'}</span></div>
          <div className="skill-stat"><span className="skill-stat-value">{runs}</span><span className="skill-stat-label">{t('teams.stat_runs') ?? 'Runs'}</span></div>
        </div>
      </div>
    </Stack>
  )
}

function TeamSlotsTab({ team, agentName, t }: { team: Team; agentName: (id: string | undefined) => string; t: Translate }) {
  const slots = team.manifest?.slots ?? []
  if (slots.length === 0) return <p style={{ color: 'var(--tm-text-3)' }}>{t('teams.no_slots') ?? 'No slots.'}</p>
  return (
    <Stack gap={3}>
      {slots.map((slot) => (
        <div key={slot.id} style={{ padding: '0.75rem', background: 'var(--tm-raised)', border: '1px solid var(--tm-border)', borderRadius: '6px' }}>
          <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'center', flexWrap: 'wrap' }}>
            <strong>{slot.title || slot.id}</strong>
            <code style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)' }}>{slot.id}</code>
            {slot.binding?.mode === 'fixed'
              ? <Tag type="cyan" size="sm">{t('teams.slot_binding_fixed') ?? 'fixed'} → {agentName(slot.binding.agent_id)}</Tag>
              : <Tag type="teal" size="sm">{t('teams.slot_binding_role') ?? 'role'}</Tag>}
          </div>
          {slot.responsibility && <p style={{ margin: '0.4rem 0 0', fontSize: '0.8rem', color: 'var(--tm-text-2)' }}>{slot.responsibility}</p>}
          {(slot.requirements?.capabilities?.length ?? 0) > 0 && (
            <div style={{ display: 'flex', gap: '0.25rem', flexWrap: 'wrap', marginTop: '0.4rem' }}>
              {slot.requirements!.capabilities!.map((cap) => <Tag key={cap} type="green" size="sm">{cap}</Tag>)}
            </div>
          )}
          {(slot.requirements?.skills?.length ?? 0) > 0 && (
            <div style={{ display: 'flex', gap: '0.25rem', flexWrap: 'wrap', marginTop: '0.4rem' }}>
              {slot.requirements!.skills!.map((skill) => <Tag key={skill} type="blue" size="sm">{skill}</Tag>)}
            </div>
          )}
          {slot.binding?.mode === 'role' && slot.binding.preferred_agent_id && (
            <div style={{ fontSize: '0.75rem', color: 'var(--tm-text-3)', marginTop: '0.4rem' }}>
              {t('teams.slot_preferred') ?? 'preferred'}: {agentName(slot.binding.preferred_agent_id)}
            </div>
          )}
          {slot.contract && (
            <div style={{ marginTop: '0.5rem', fontSize: '0.75rem', color: 'var(--tm-text-2)', borderLeft: '2px solid var(--tm-border)', paddingLeft: '0.5rem' }}>
              <span style={{ color: 'var(--tm-text-3)' }}>{t('teams.slot_contract') ?? 'Contract'}: </span>{slot.contract}
            </div>
          )}
        </div>
      ))}
    </Stack>
  )
}

function TeamRunsTab({ runs, t }: { runs: TeamRunRow[]; t: Translate }) {
  if (runs.length === 0) return <p style={{ color: 'var(--tm-text-3)' }}>{t('teams.no_runs') ?? 'This team has not run yet.'}</p>
  return (
    <Stack gap={2}>
      {runs.map((run) => (
        <div key={run.run_id} style={{ padding: '0.6rem 0.75rem', background: 'var(--tm-raised)', border: '1px solid var(--tm-border)', borderRadius: '6px' }}>
          <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'center', flexWrap: 'wrap', fontSize: '0.8rem' }}>
            {run.status === 'completed' && <Tag type="green" size="sm">{t('chat.delegation.completed') ?? 'completed'}</Tag>}
            {run.status === 'failed' && <Tag type="red" size="sm">{t('chat.delegation.failed') ?? 'failed'}</Tag>}
            {run.status === 'running' && <Tag type="blue" size="sm">{t('chat.delegation.running') ?? 'running'}</Tag>}
            {run.status === 'cancelled' && <Tag type="warm-gray" size="sm">{t('status.cancelled') ?? 'cancelled'}</Tag>}
            {run.version && <Tag size="sm">v{run.version}</Tag>}
            <a href={`/agents?project=${encodeURIComponent(run.project)}&run=${encodeURIComponent(run.run_id)}`} style={{ color: 'var(--tm-teal)', fontFamily: 'monospace', fontSize: '0.72rem' }}>{run.run_id}</a>
            <span style={{ color: 'var(--tm-text-3)', marginLeft: 'auto', fontSize: '0.72rem' }}>{new Date(run.started_at).toLocaleString()}</span>
          </div>
          {run.goal && <div style={{ fontSize: '0.8rem', color: 'var(--tm-text-2)', marginTop: '0.25rem', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{run.goal}</div>}
          {(run.status === 'completed' || run.status === 'failed') && (
            <div style={{ display: 'flex', gap: '0.5rem', flexWrap: 'wrap', marginTop: '0.3rem', fontSize: '0.72rem', color: 'var(--tm-text-3)' }}>
              {run.total_tokens !== undefined && <span>{run.total_tokens} tok</span>}
              {run.cost_usd !== undefined && <span>${run.cost_usd.toFixed(4)}</span>}
              {run.rework_rounds !== undefined && <span>{t('teams.rework_rounds', { count: String(run.rework_rounds) }) ?? `rework ×${run.rework_rounds}`}</span>}
              {run.failed_step && <span style={{ color: '#f7768e' }}>{t('teams.failed_step') ?? 'step'}: {run.failed_step}</span>}
            </div>
          )}
        </div>
      ))}
    </Stack>
  )
}

function TeamEvolutionTab({ team, versions, t, onApply, onReject }: {
  team: Team
  versions: TeamVersion[]
  t: Translate
  onApply: (version: string) => void
  onReject: (version: string) => void
}) {
  if (versions.length === 0) return <p style={{ color: 'var(--tm-text-3)' }}>{t('teams.no_versions') ?? 'No versions recorded.'}</p>
  return (
    <div className="skill-evolution-timeline">
      {[...versions].reverse().map((version) => {
        const isDraft = version.status === 'draft'
        const isCurrent = version.version === team.version
        return (
          <div key={version.version} style={{ padding: '0.6rem 0.75rem', background: 'var(--tm-raised)', border: '1px solid var(--tm-border)', borderRadius: '6px', marginBottom: '0.5rem', opacity: isCurrent || isDraft ? 1 : 0.6 }}>
            <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'center', flexWrap: 'wrap' }}>
              <strong>v{version.version}</strong>
              {statusTag(version.status, t)}
              {isCurrent && <Tag type="cool-gray" size="sm">{t('teams.version_current') ?? 'current'}</Tag>}
              <Tag type="gray" size="sm">{originLabel(version.origin, t)}</Tag>
              <span style={{ color: 'var(--tm-text-3)', marginLeft: 'auto', fontSize: '0.72rem' }}>{new Date(version.created_at).toLocaleString()}</span>
            </div>
            {version.change_summary && <p style={{ margin: '0.35rem 0 0', fontSize: '0.8rem', color: 'var(--tm-text-2)' }}>{version.change_summary}</p>}
            {version.observed_problem && <p style={{ margin: '0.25rem 0 0', fontSize: '0.75rem', color: 'var(--tm-text-3)' }}>{t('teams.observed_problem') ?? 'Observed problem'}: {version.observed_problem}</p>}
            {version.proposed_change && <p style={{ margin: '0.25rem 0 0', fontSize: '0.75rem', color: 'var(--tm-text-3)' }}>{t('teams.proposed_change') ?? 'Proposed change'}: {version.proposed_change}</p>}
            {version.expected_effect && <p style={{ margin: '0.25rem 0 0', fontSize: '0.75rem', color: 'var(--tm-text-3)' }}>{t('teams.expected_effect') ?? 'Expected effect'}: {version.expected_effect}</p>}
            {(version.source_runs?.length ?? 0) > 0 && (
              <div style={{ marginTop: '0.3rem', display: 'flex', gap: '0.25rem', flexWrap: 'wrap' }}>
                {version.source_runs!.map((run) => (
                  <a key={run} href={`/agents?run=${encodeURIComponent(run)}`} style={{ fontSize: '0.7rem', color: 'var(--tm-teal)', fontFamily: 'monospace' }}>{run}</a>
                ))}
              </div>
            )}
            {isDraft && (
              <Stack orientation="horizontal" gap={2} style={{ marginTop: '0.5rem' }}>
                <Button size="sm" onClick={() => onApply(version.version)}>{t('teams.apply_version') ?? 'Apply'}</Button>
                <Button size="sm" kind="danger--ghost" onClick={() => onReject(version.version)}>{t('teams.reject_version') ?? 'Reject'}</Button>
              </Stack>
            )}
          </div>
        )
      })}
    </div>
  )
}

/** Structured team editor — protocol select, slot cards, requirements as
 *  capability checkboxes + free-form skill ids, bindings as selects. */
function TeamForm({ form, setForm, editing, agents, skills, org, error, saving, provenance, canRegenerate, onRegenerate, onSave, onClose }: {
  form: TeamFormState
  setForm: (form: TeamFormState) => void
  editing: Team | null
  agents: Agent[]
  skills: Skill[]
  org: ReturnType<typeof useOrgUnits>
  error: string
  saving: boolean
  provenance: Partial<Record<'protocol' | 'defaults' | 'slots', boolean>>
  canRegenerate: boolean
  onRegenerate: () => void
  onSave: () => Promise<void> | void
  onClose: () => void
}) {
  const t = useT()
  const builderTag = (section: 'protocol' | 'defaults' | 'slots') =>
    provenance[section] ? <Tag size="sm" type="purple" title={t('teams.from_builder_hint') ?? ''}>{t('teams.from_builder') ?? 'builder'}</Tag> : null
  const patch = (partial: Partial<TeamFormState>) => setForm({ ...form, ...partial })
  const patchSlot = (index: number, partial: Partial<TeamSlotForm>) => {
    const slots = form.slots.map((slot, i) => (i === index ? { ...slot, ...partial } : slot))
    patch({ slots })
  }
  const patchStep = (index: number, partial: Partial<TeamStep>) => {
    const steps = form.steps.map((step, i) => (i === index ? { ...step, ...partial } : step))
    patch({ steps })
  }

  return (
    <AppModal onClose={onClose} panelStyle={{ width: '720px', maxHeight: '85vh', overflowY: 'auto' }}>
      <Heading style={{ fontSize: '1.1rem', marginBottom: '0.75rem' }}>
        {editing ? (t('teams.edit_team') ?? 'Edit team') : (t('teams.new_team') ?? 'New team')}
      </Heading>
      {error && <InlineNotification kind="error" title={t('action.error') ?? 'Error'} subtitle={error} lowContrast style={{ marginBottom: '0.75rem' }} />}

      <Stack gap={4}>
        <Stack orientation="horizontal" gap={3}>
          {!editing && (
            <div style={{ flex: 1 }}>
              <TextInput id="team-id" labelText={t('teams.id') ?? 'ID'} placeholder="code-delivery" value={form.id} onChange={(e) => patch({ id: e.target.value })} />
            </div>
          )}
          <div style={{ flex: 1 }}>
            <TextInput id="team-name" labelText={t('teams.name') ?? 'Name'} value={form.name} onChange={(e) => patch({ name: e.target.value })} />
          </div>
          <div style={{ width: '110px' }}>
            <TextInput id="team-version" labelText={t('teams.version') ?? 'Version'} value={form.version} onChange={(e) => patch({ version: e.target.value })} />
          </div>
        </Stack>
        <TextArea id="team-description" labelText={t('teams.description') ?? 'Description'} rows={2} value={form.description} onChange={(e) => patch({ description: e.target.value })} />
        {!editing && (
          <OrgUnitSelect id="team-org" value={form.org_unit_id} onChange={(orgUnitID) => patch({ org_unit_id: orgUnitID })} org={org} label={t('teams.availability') ?? 'Availability'} />
        )}

        <div>
          <div className="skill-subheading" style={{ display: 'flex', alignItems: 'center', gap: '0.4rem' }}>
            {t('teams.protocol') ?? 'Protocol'}
            {builderTag('protocol')}
          </div>
          <Stack orientation="horizontal" gap={3}>
            <div style={{ flex: 1 }}>
              <Select id="team-protocol" hideLabel labelText="" value={form.protocol} onChange={(e) => patch({ protocol: e.target.value as TeamProtocolKind })}>
                {TEAM_PROTOCOL_KINDS.map((kind) => <SelectItem key={kind} value={kind} text={protocolLabel(kind, t)} />)}
              </Select>
            </div>
            <div style={{ width: '160px' }}>
              <TextInput id="team-max-rework" labelText={t('teams.max_rework_label') ?? 'Max rework rounds'} placeholder="0" value={form.maxRework} onChange={(e) => patch({ maxRework: e.target.value })} />
            </div>
          </Stack>
          <p style={{ fontSize: '0.72rem', color: 'var(--tm-text-3)', marginTop: '0.35rem' }}>{t(`teams.protocol_hint_${form.protocol}`) ?? ''}</p>
        </div>

        <div>
          <div className="skill-subheading" style={{ display: 'flex', alignItems: 'center', gap: '0.4rem' }}>
            {t('teams.defaults') ?? 'Defaults'}
            {builderTag('defaults')}
          </div>
          <Stack orientation="horizontal" gap={3}>
            <div style={{ flex: 1 }}>
              <TextInput id="team-max-turns" labelText={t('teams.max_turns') ?? 'Max turns'} placeholder="—" value={form.maxTurns} onChange={(e) => patch({ maxTurns: e.target.value })} />
            </div>
            <div style={{ flex: 1 }}>
              <TextInput id="team-budget" labelText={t('teams.budget_usd') ?? 'Budget, $'} placeholder="—" value={form.budgetUSD} onChange={(e) => patch({ budgetUSD: e.target.value })} />
            </div>
            <div style={{ flex: 1 }}>
              <TextInput id="team-parallel" labelText={t('teams.parallel_limit') ?? 'Parallel limit'} placeholder="—" value={form.parallelLimit} onChange={(e) => patch({ parallelLimit: e.target.value })} />
            </div>
          </Stack>
        </div>

        {form.protocol === 'dag' && (
          <div>
            <div className="skill-subheading">{t('teams.steps') ?? 'Steps'}</div>
            <Stack gap={2}>
              {form.steps.map((step, index) => (
                <div key={index} style={{ display: 'grid', gridTemplateColumns: '1fr 1fr 1.4fr 1.4fr auto', gap: '0.5rem', alignItems: 'end' }}>
                  <TextInput id={`step-id-${index}`} labelText={t('teams.step_id') ?? 'Step id'} value={step.id} onChange={(e) => patchStep(index, { id: e.target.value })} />
                  <Select id={`step-slot-${index}`} labelText={t('teams.step_slot') ?? 'Slot'} value={step.slot_id} onChange={(e) => patchStep(index, { slot_id: e.target.value })}>
                    <SelectItem value="" text="—" />
                    {form.slots.map((slot) => <SelectItem key={slot.id} value={slot.id} text={slot.id} />)}
                  </Select>
                  <TextInput id={`step-deps-${index}`} labelText={t('teams.step_depends') ?? 'Depends on (csv)'} value={(step.depends_on ?? []).join(', ')} onChange={(e) => patchStep(index, { depends_on: splitList(e.target.value) })} />
                  <TextInput id={`step-review-${index}`} labelText={t('teams.step_reviews') ?? 'Reviews (csv)'} value={(step.review_of ?? []).join(', ')} onChange={(e) => patchStep(index, { review_of: splitList(e.target.value) })} />
                  <Button kind="ghost" hasIconOnly renderIcon={TrashCan} iconDescription={t('action.delete') ?? 'Delete'} onClick={() => patch({ steps: form.steps.filter((_, i) => i !== index) })} />
                </div>
              ))}
            </Stack>
            <Button size="sm" kind="ghost" renderIcon={Add} style={{ marginTop: '0.5rem' }} onClick={() => patch({ steps: [...form.steps, { id: `step-${form.steps.length + 1}`, slot_id: '', depends_on: [], review_of: [] }] })}>
              {t('teams.add_step') ?? 'Add step'}
            </Button>
          </div>
        )}

        <div>
          <div className="skill-subheading" style={{ display: 'flex', alignItems: 'center', gap: '0.4rem' }}>
            {t('teams.slots') ?? 'Slots'}
            {builderTag('slots')}
          </div>
          <Stack gap={3}>
            {form.slots.map((slot, index) => (
              <div key={index} style={{ padding: '0.75rem', border: '1px solid var(--tm-border)', borderRadius: '6px', background: 'var(--tm-raised)' }}>
                <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'end', flexWrap: 'wrap' }}>
                  <div style={{ flex: 1, minWidth: '120px' }}>
                    <TextInput id={`slot-id-${index}`} labelText={t('teams.slot_id') ?? 'Slot id'} value={slot.id} onChange={(e) => patchSlot(index, { id: e.target.value })} />
                  </div>
                  <div style={{ flex: 1, minWidth: '140px' }}>
                    <TextInput id={`slot-title-${index}`} labelText={t('teams.slot_title') ?? 'Role title'} value={slot.title} onChange={(e) => patchSlot(index, { title: e.target.value })} />
                  </div>
                  <Select id={`slot-mode-${index}`} labelText={t('teams.slot_binding') ?? 'Binding'} value={slot.mode} onChange={(e) => patchSlot(index, { mode: e.target.value as 'fixed' | 'role' })}>
                    <SelectItem value="fixed" text={t('teams.slot_binding_fixed') ?? 'Fixed agent'} />
                    <SelectItem value="role" text={t('teams.slot_binding_role') ?? 'Role (matched at launch)'} />
                  </Select>
                  <Button kind="ghost" hasIconOnly renderIcon={TrashCan} iconDescription={t('teams.remove_slot') ?? 'Remove slot'} onClick={() => patch({ slots: form.slots.filter((_, i) => i !== index) })} />
                </div>

                {slot.mode === 'fixed' ? (
                  <div style={{ marginTop: '0.75rem', maxWidth: '360px' }}>
                    <Select id={`slot-agent-${index}`} labelText={t('teams.slot_agent') ?? 'Agent'} value={slot.agent_id} onChange={(e) => patchSlot(index, { agent_id: e.target.value })}>
                      <SelectItem value="" text="—" />
                      {agents.map((agent) => <SelectItem key={agent.id} value={agent.id} text={`${agent.name} (${agent.id})`} />)}
                    </Select>
                  </div>
                ) : (
                  <Stack gap={3} style={{ marginTop: '0.75rem' }}>
                    <div>
                      <div style={{ fontSize: '0.75rem', color: 'var(--tm-text-2)', marginBottom: '0.35rem' }}>{t('teams.slot_capabilities') ?? 'Required capabilities'}</div>
                      <div style={{ display: 'flex', gap: '0.75rem 1.25rem', flexWrap: 'wrap' }}>
                        {TEAM_CAPABILITIES.map((cap) => (
                          <Checkbox
                            key={cap}
                            id={`slot-${index}-cap-${cap}`}
                            labelText={cap}
                            checked={slot.capabilities.includes(cap)}
                            onChange={(_, checked) => patchSlot(index, {
                              capabilities: checked ? [...slot.capabilities, cap] : slot.capabilities.filter((c) => c !== cap),
                            })}
                          />
                        ))}
                      </div>
                    </div>
                    <div style={{ display: 'flex', gap: '0.5rem', flexWrap: 'wrap' }}>
                      <div style={{ flex: 2, minWidth: '220px' }}>
                        <TextInput id={`slot-skills-${index}`} labelText={t('teams.slot_skills') ?? 'Required skills (comma-separated ids)'} placeholder={skills.slice(0, 2).map((s) => s.id).join(', ')} value={slot.skillsText} onChange={(e) => patchSlot(index, { skillsText: e.target.value })} />
                      </div>
                      <div style={{ flex: 2, minWidth: '220px' }}>
                        <Select id={`slot-preferred-${index}`} labelText={t('teams.slot_preferred') ?? 'Preferred agent'} value={slot.preferred_agent_id} onChange={(e) => patchSlot(index, { preferred_agent_id: e.target.value })}>
                          <SelectItem value="" text="—" />
                          {agents.map((agent) => <SelectItem key={agent.id} value={agent.id} text={`${agent.name} (${agent.id})`} />)}
                        </Select>
                      </div>
                    </div>
                  </Stack>
                )}

                <Stack gap={3} style={{ marginTop: '0.75rem' }}>
                  <TextInput id={`slot-responsibility-${index}`} labelText={t('teams.slot_responsibility') ?? 'Responsibility'} value={slot.responsibility} onChange={(e) => patchSlot(index, { responsibility: e.target.value })} />
                  <TextArea id={`slot-contract-${index}`} labelText={t('teams.slot_contract') ?? 'Contract (expected output the next stage relies on)'} rows={2} value={slot.contract} onChange={(e) => patchSlot(index, { contract: e.target.value })} />
                </Stack>
              </div>
            ))}
          </Stack>
          <Button size="sm" kind="ghost" renderIcon={Add} style={{ marginTop: '0.5rem' }} onClick={() => patch({ slots: [...form.slots, emptySlotForm(form.slots.length + 1)] })}>
            {t('teams.add_slot') ?? 'Add slot'}
          </Button>
        </div>
      </Stack>

      <div style={{ display: 'flex', justifyContent: 'flex-end', gap: '0.5rem', marginTop: '1.25rem' }}>
        {canRegenerate && (
          <Button kind="secondary" renderIcon={MagicWand} style={{ marginRight: 'auto' }} onClick={onRegenerate}>
            {t('teams.regenerate') ?? 'Rebuild'}
          </Button>
        )}
        <Button kind="secondary" onClick={onClose}>{t('action.cancel') ?? 'Cancel'}</Button>
        <Button onClick={onSave} disabled={saving}>{saving ? (t('action.saving') ?? 'Saving…') : (t('action.save') ?? 'Save')}</Button>
      </div>
    </AppModal>
  )
}

/** Launch dialog: project + goal + live binding preview (wave C resolve).
 *  Ambiguous slots (several matching agents) and unresolvable slots (none)
 *  are handled per slot — an explicit pick becomes a launch-time binding. */
function LaunchDialog({ team, agents, projects, currentProject, onClose }: {
  team: Team
  agents: Agent[]
  projects: Project[]
  currentProject: string
  onClose: () => void
}) {
  const t = useT()
  const initialProject = currentProject.trim() && currentProject.trim() !== INTAKE_PROJECT_ID ? currentProject.trim() : (projects[0]?.id ?? '')
  const [project, setProject] = useState(initialProject)
  const [goal, setGoal] = useState('')
  const [preview, setPreview] = useState<TeamResolvePreview | null>(null)
  const [previewLoading, setPreviewLoading] = useState(false)
  const [previewError, setPreviewError] = useState('')
  const [overrides, setOverrides] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  // The roster mirrors the server's launch scope: agents with no project pin
  // plus those pinned to the selected project.
  const roster = useMemo(
    () => agents.filter((a) => !a.project_id || a.project_id === project),
    [agents, project],
  )
  const agentName = useCallback((id: string) => agents.find((a) => a.id === id)?.name ?? id, [agents])

  useEffect(() => {
    if (!project) return
    let cancelled = false
    setPreviewLoading(true); setPreviewError('')
    void workspaceApi.resolveTeam(team.id, project)
      .then((data) => { if (!cancelled) setPreview(data) })
      .catch((f) => { if (!cancelled) setPreviewError(message(f)) })
      .finally(() => { if (!cancelled) setPreviewLoading(false) })
    return () => { cancelled = true }
  }, [team.id, project])

  const start = async () => {
    if (!project || !goal.trim()) return
    setBusy(true); setError('')
    try {
      const result = await workspaceApi.startTeamRun(team.id, {
        project, goal: goal.trim(),
        bindings: Object.keys(overrides).length > 0 ? overrides : undefined,
      })
      // Open the freshly started run in Runs — the team card's history
      // follows on the next visit.
      window.history.pushState(null, '', `/agents?project=${encodeURIComponent(result.project)}&run=${encodeURIComponent(result.run_id)}`)
      window.dispatchEvent(new PopStateEvent('popstate'))
      onClose()
    } catch (f) {
      const detail = f instanceof ApiError ? f.body : {}
      const candidates = Array.isArray(detail.candidates) ? (detail.candidates as string[]).map(agentName).join(', ') : ''
      const requirements = Array.isArray(detail.requirements) ? (detail.requirements as string[]).join(', ') : ''
      setError([message(f), candidates ? `${t('teams.launch_candidates') ?? 'candidates'}: ${candidates}` : '', requirements ? `${t('teams.launch_requirements') ?? 'requirements'}: ${requirements}` : ''].filter(Boolean).join(' — '))
    } finally { setBusy(false) }
  }

  return (
    <AppModal onClose={onClose} panelStyle={{ width: '640px', maxHeight: '85vh', overflowY: 'auto' }}>
      <Heading style={{ fontSize: '1.1rem', marginBottom: '0.75rem' }}>{t('teams.launch_title', { name: team.name }) ?? `Launch ${team.name}`}</Heading>
      {error && <InlineNotification kind="error" title={t('action.error') ?? 'Error'} subtitle={error} onClose={() => setError('')} lowContrast style={{ marginBottom: '0.75rem' }} />}

      <Stack gap={4}>
        <Select id="launch-project" labelText={t('teams.launch_project') ?? 'Project'} value={project} onChange={(e) => { setProject(e.target.value); setOverrides({}) }}>
          {projects.map((p) => <SelectItem key={p.id} value={p.id} text={p.name ?? p.id} />)}
        </Select>
        <TextArea id="launch-goal" labelText={t('teams.launch_goal') ?? 'Goal'} rows={3} value={goal} onChange={(e) => setGoal(e.target.value)} placeholder={t('teams.launch_goal_hint') ?? 'What should this team deliver?'} />

        <div>
          <div className="skill-subheading">{t('teams.preview') ?? 'Slot bindings'}</div>
          {previewLoading && <p style={{ color: 'var(--tm-text-3)', fontSize: '0.8rem' }}>{t('teams.preview_loading') ?? 'Resolving slots…'}</p>}
          {previewError && <InlineNotification kind="error" lowContrast title={t('action.error') ?? 'Error'} subtitle={previewError} />}
          {preview && (
            <Stack gap={2}>
              {preview.slots.map((choice) => {
                const slot = team.manifest?.slots?.find((s) => s.id === choice.slot_id)
                const chosen = overrides[choice.slot_id]
                return (
                  <div key={choice.slot_id} style={{ padding: '0.5rem 0.75rem', border: '1px solid var(--tm-border)', borderRadius: '6px', background: 'var(--tm-raised)' }}>
                    <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'center', flexWrap: 'wrap', fontSize: '0.8rem' }}>
                      <strong>{slot?.title || choice.slot_id}</strong>
                      <code style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)' }}>{choice.slot_id}</code>
                      {chosen ? (
                        <Tag type="cyan" size="sm">→ {agentName(chosen)}</Tag>
                      ) : choice.agent_id ? (
                        <Tag type="green" size="sm">→ {agentName(choice.agent_id)}</Tag>
                      ) : choice.candidates && choice.candidates.length > 0 ? (
                        <Tag type="warm-gray" size="sm">{t('teams.slot_ambiguous') ?? 'several candidates — pick one'}</Tag>
                      ) : (
                        <Tag type="red" size="sm">{t('teams.slot_unresolvable') ?? 'no matching agent'}</Tag>
                      )}
                      {choice.mode && !chosen && <Tag size="sm" type="cool-gray">{choice.mode}</Tag>}
                    </div>
                    {choice.reason && <div style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)', marginTop: '0.25rem' }}>{choice.reason}</div>}
                    {(choice.requirements?.length ?? 0) > 0 && (
                      <div style={{ display: 'flex', gap: '0.25rem', flexWrap: 'wrap', marginTop: '0.3rem' }}>
                        {choice.requirements!.map((req) => <Tag key={req} size="sm" type="red">{req}</Tag>)}
                      </div>
                    )}
                    {(!choice.agent_id || chosen) && (
                      <div style={{ marginTop: '0.5rem', maxWidth: '360px' }}>
                        <Select
                          id={`override-${choice.slot_id}`}
                          labelText={t('teams.override_binding') ?? 'Fill the slot with'}
                          value={chosen ?? ''}
                          onChange={(e) => {
                            const next = { ...overrides }
                            if (e.target.value) next[choice.slot_id] = e.target.value
                            else delete next[choice.slot_id]
                            setOverrides(next)
                          }}
                        >
                          <SelectItem value="" text="—" />
                          {((choice.candidates?.length ?? 0) > 0 ? choice.candidates! : roster.map((a) => a.id)).map((id) => (
                            <SelectItem key={id} value={id} text={`${agentName(id)} (${id})`} />
                          ))}
                        </Select>
                      </div>
                    )}
                  </div>
                )
              })}
              {!preview.resolvable && preview.reason && (
                <InlineNotification kind="warning" lowContrast title={t('teams.preview_incomplete') ?? 'Not resolvable yet'} subtitle={preview.reason} />
              )}
            </Stack>
          )}
        </div>
      </Stack>

      <div style={{ display: 'flex', justifyContent: 'flex-end', gap: '0.5rem', marginTop: '1.25rem' }}>
        <Button kind="secondary" onClick={onClose}>{t('action.cancel') ?? 'Cancel'}</Button>
        <Button disabled={busy || !project || !goal.trim()} onClick={() => void start()}>{busy ? (t('teams.starting') ?? 'Starting…') : (t('teams.start_run') ?? 'Start run')}</Button>
      </div>
    </AppModal>
  )
}
