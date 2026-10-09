import { useCallback, useEffect, useState } from 'react'
import {
  Button,
  TextInput,
  TextArea,
  InlineNotification,
  Loading,
  Tag,
  Tile,
  Stack,
  Heading,
} from '@carbon/react'
import { Add, Edit, TrashCan, Renew } from '@carbon/icons-react'
import GeneratingState from './GeneratingState'
import { workspaceApi, type Skill, type SkillVersion, type SkillExecution, type SkillMemoryItem, type SkillValidationIssue, type SkillDraftQuestion, type SkillEvaluationCase, type SkillEvaluationRun } from './workspaceApi'
import { useT } from './i18n'
import ListFilter, { matchesFilter } from './ListFilter'
import AppModal from './Modal'
import { useOrgUnits, OrgUnitSelect, OrgBadge, type OrgUnitsState } from './orgUnits'
import Markdown from './Markdown'
import SkillManifestEditor, { manifestToYaml, type SkillManifest, type ManifestSuggestions, type SectionProvenance } from './SkillManifestEditor'

function message(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }

const STARTER_MARKDOWN = `# Skill name

## Purpose

What this capability does.

## When to use

When the agent should apply this skill.

## Procedure

1. First step.
2. Second step.

## Constraints

- What must never happen.
`

const STARTER_MANIFEST: SkillManifest = {
  capabilities: [],
  tools: [],
  runtime: { sandbox: 'optional' },
}

type SkillTab = 'main' | 'executions' | 'evolution' | 'evals'

interface SkillFormState {
  id: string
  name: string
  description: string
  version: string
  markdown: string
  manifest: SkillManifest
  org_unit_id?: string
}

/** Sections the agent asks questions about map onto editor sections. */
function sectionOfField(field: string): string {
  const head = field.split('.')[0].split('[')[0].trim()
  return head || '*'
}

interface ManifestDiffEntry {
  field: string
  from: string
  to: string
}

/** Field-level manifest diff mirroring skills/diff.go (capabilities, tools,
 * runtime, conditions, evidence, evaluation suite reference). */
function diffManifests(from: SkillManifest, to: SkillManifest): ManifestDiffEntry[] {
  const entries: ManifestDiffEntry[] = []
  const diffSets = (field: string, a: string[] = [], b: string[] = []) => {
    const setA = new Set(a)
    const setB = new Set(b)
    const added = b.filter((v) => !setA.has(v))
    const removed = a.filter((v) => !setB.has(v))
    if (added.length) entries.push({ field, from: '∅', to: '+ ' + added.join(', + ') })
    if (removed.length) entries.push({ field, from: '− ' + removed.join(', − '), to: '∅' })
  }
  if ((from.description ?? '') !== (to.description ?? '')) entries.push({ field: 'description', from: from.description ?? '', to: to.description ?? '' })
  diffSets('capabilities', from.capabilities, to.capabilities)
  diffSets('tools', from.tools, to.tools)
  if ((from.runtime?.sandbox ?? '') !== (to.runtime?.sandbox ?? '')) entries.push({ field: 'runtime.sandbox', from: from.runtime?.sandbox ?? '', to: to.runtime?.sandbox ?? '' })
  if ((from.runtime?.network ?? '') !== (to.runtime?.network ?? '')) entries.push({ field: 'runtime.network', from: from.runtime?.network ?? '', to: to.runtime?.network ?? '' })
  diffSets('preconditions', from.preconditions, to.preconditions)
  diffSets('postconditions', from.postconditions, to.postconditions)
  diffSets('evidence.required', from.evidence?.required, to.evidence?.required)
  return entries
}

/** Added/removed SKILL.md line counts (bag semantics, mirrors DiffMarkdown). */
function diffMarkdownCounts(from: string, to: string): [number, number] {
  const counts = (text: string) => {
    const map = new Map<string, number>()
    for (const line of text.split('\n')) map.set(line, (map.get(line) ?? 0) + 1)
    return map
  }
  const a = counts(from)
  const b = counts(to)
  let added = 0
  let removed = 0
  for (const [line, count] of b) added += Math.max(0, count - (a.get(line) ?? 0))
  for (const [line, count] of a) removed += Math.max(0, count - (b.get(line) ?? 0))
  return [added, removed]
}

export default function Skills() {
  const t = useT()
  const org = useOrgUnits()
  const [skills, setSkills] = useState<Skill[]>([])
  const [selected, setSelected] = useState<Skill | null>(null)
  const [tab, setTab] = useState<SkillTab>('main')
  const [versions, setVersions] = useState<SkillVersion[]>([])
  const [executions, setExecutions] = useState<SkillExecution[]>([])
  const [memory, setMemory] = useState<SkillMemoryItem[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [showForm, setShowForm] = useState(false)
  const [editing, setEditing] = useState<Skill | null>(null)
  const [form, setForm] = useState<SkillFormState>({ id: '', name: '', description: '', version: '1.0.0', markdown: STARTER_MARKDOWN, manifest: STARTER_MANIFEST })
  const [validation, setValidation] = useState<SkillValidationIssue[] | null>(null)
  const [openVersion, setOpenVersion] = useState<string | null>(null)
  const [suggestions, setSuggestions] = useState<ManifestSuggestions>({})
  const [wizardOpen, setWizardOpen] = useState(false)
  const [wizardText, setWizardText] = useState('')
  const [wizardBusy, setWizardBusy] = useState(false)
  const [wizardError, setWizardError] = useState('')
  const [draftQuestions, setDraftQuestions] = useState<SkillDraftQuestion[]>([])
  const [answers, setAnswers] = useState<Record<string, string>>({})
  const [provenance, setProvenance] = useState<Record<string, SectionProvenance>>({})
  const [filter, setFilter] = useState('')

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const data = await workspaceApi.listSkills()
      setSkills(data.skills ?? [])
      setSelected((current) => current && data.skills?.some((s) => s.id === current.id) ? data.skills.find((s) => s.id === current.id)! : (data.skills?.[0] ?? null))
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }, [])

  useEffect(() => { void load() }, [load])

  const visibleSkills = skills.filter((s) => matchesFilter(filter, s.name, s.id, s.description))

  // Autosuggest values for the manifest editor: tools from builtins + MCP,
  // capabilities collected from existing skills, mcp from server names.
  useEffect(() => {
    let cancelled = false
    void (async () => {
      try {
        const [toolsData, serversData] = await Promise.all([
          workspaceApi.listMCPTools(),
          workspaceApi.listMCPServers(),
        ])
        if (cancelled) return
        const tools = new Set<string>()
        for (const tool of toolsData.builtins ?? []) tools.add(tool.model_name || tool.name)
        for (const server of Object.values(toolsData.servers ?? {})) {
          for (const tool of server.tools ?? []) tools.add(tool.model_name || tool.name)
        }
        setSuggestions({
          tools: [...tools].sort(),
          mcp: (serversData.servers ?? []).map((s) => s.name).sort(),
        })
      } catch {
        // suggestions are optional — ignore load errors
      }
    })()
    return () => { cancelled = true }
  }, [])

  // Capabilities come from the already-loaded skills list.
  const capabilitySuggestions = [...new Set(skills.flatMap((s) => Array.isArray(s.manifest?.capabilities) ? s.manifest.capabilities.map(String) : []))].sort()

  const openSkill = useCallback(async (skill: Skill) => {
    setSelected(skill)
    setTab('main')
    setOpenVersion(null)
    try {
      const [v, e, m] = await Promise.all([
        workspaceApi.listSkillVersions(skill.id),
        workspaceApi.listSkillExecutions(skill.id),
        workspaceApi.listSkillMemory(skill.id),
      ])
      setVersions(v.versions ?? [])
      setExecutions(e.executions ?? [])
      setMemory(m.memory ?? [])
    } catch (f) { setError(message(f)) }
  }, [])

  useEffect(() => { if (selected) void openSkill(selected) }, [selected, openSkill])

  // ── Wizard: natural language → agent-built draft ──────────────────────

  const startWizard = () => {
    setWizardText('')
    setWizardError('')
    setWizardOpen(true)
  }

  const applyDraft = (draft: { name: string; description: string; markdown: string; manifest: Record<string, unknown> }, questions: SkillDraftQuestion[]) => {
    const manifest = (draft.manifest ?? {}) as Record<string, unknown>
    const prov: Record<string, SectionProvenance> = {}
    for (const key of ['inputs', 'outputs', 'capabilities', 'tools', 'runtime', 'preconditions', 'postconditions', 'evidence', 'evaluation']) {
      const value = manifest[key]
      if (value && (Array.isArray(value) ? value.length : Object.keys(value as object).length) > 0) prov[key] = 'agent'
    }
    for (const q of questions) {
      const section = sectionOfField(q.field)
      prov[section] = prov[section] === 'agent' ? 'agent' : 'todo'
    }
    setProvenance(prov)
    setDraftQuestions(questions)
    setAnswers({})
    setValidation(null)
    setForm({
      id: '',
      name: draft.name || '',
      description: draft.description || '',
      version: '1.0.0',
      markdown: draft.markdown || STARTER_MARKDOWN,
      manifest,
      org_unit_id: '',
    })
    setEditing(null)
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
      const draft = await workspaceApi.draftSkill(description, draftQuestions.length > 0)
      applyDraft(draft, draft.questions ?? [])
      setWizardOpen(false)
    } catch (f) {
      setWizardError(message(f))
    } finally {
      setWizardBusy(false)
    }
  }

  const startManual = () => {
    setEditing(null)
    setValidation(null)
    setDraftQuestions([])
    setProvenance({})
    setForm({ id: '', name: '', description: '', version: '1.0.0', markdown: STARTER_MARKDOWN, manifest: STARTER_MANIFEST, org_unit_id: '' })
    setShowForm(true)
  }

  // ── CRUD ──────────────────────────────────────────────────────────────

  const manifestYaml = () => manifestToYaml({
    ...form.manifest,
    id: editing?.id ?? form.id.trim(),
    name: form.name,
    description: form.description,
    version: form.version,
  })

  const validate = async () => {
    try {
      const result = await workspaceApi.validateSkill(editing?.id ?? 'draft', { markdown: form.markdown, manifest_yaml: manifestYaml() })
      setValidation(result.issues ?? [])
    } catch (f) { setError(message(f)) }
  }

  const save = async () => {
    if (!form.name.trim()) return
    setLoading(true); setError('')
    try {
      if (editing) {
        const updated = await workspaceApi.updateSkill(editing.id, {
          name: form.name, description: form.description, version: form.version,
          markdown: form.markdown, manifest_yaml: manifestYaml(),
        })
        // PUT payloads ignore org bindings: re-scoping goes through the admin
        // binding endpoint (docs/org-structure.md §39).
        if (org.isAdmin && (form.org_unit_id ?? '') !== (editing.org_unit_id ?? '')) {
          await workspaceApi.setResourceBinding('skill', editing.id, form.org_unit_id ?? '')
        }
        setShowForm(false); setEditing(null)
        void openSkill(updated)
      } else {
        const created = await workspaceApi.createSkill({
          id: form.id.trim() || form.name.trim().toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, ''),
          name: form.name, description: form.description, version: form.version,
          markdown: form.markdown, manifest_yaml: manifestYaml(),
          org_unit_id: form.org_unit_id || undefined,
        })
        setShowForm(false); setEditing(null)
        void openSkill(created)
      }
      void load()
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }

  const remove = async (skill: Skill) => {
    if (!confirm(t('skills.delete_confirm', { name: skill.name }) ?? `Delete skill "${skill.name}"?`)) return
    setLoading(true); setError('')
    try {
      await workspaceApi.deleteSkill(skill.id)
      setSelected(null)
      void load()
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }

  // Applying is the explicit human approval that makes a draft (or an older
  // version, as a rollback) the current revision — agents never see drafts.
  const applyVersion = async (skill: Skill, version: string) => {
    setLoading(true); setError('')
    try {
      const updated = await workspaceApi.applySkillVersion(skill.id, version)
      void openSkill(updated)
      void load()
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }

  // Rejecting dismisses a draft proposal forever: the row stays in history as
  // rejected and can no longer be applied (docs/living-skills.md §23).
  const rejectVersion = async (skill: Skill, version: string) => {
    setLoading(true); setError('')
    try {
      await workspaceApi.rejectSkillVersion(skill.id, version)
      void openSkill(skill)
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }

  // ── Evaluations: stored suite + run history ─────────────────────────
  const [evalSuite, setEvalSuite] = useState<SkillEvaluationCase[]>([])
  const [evalRuns, setEvalRuns] = useState<SkillEvaluationRun[]>([])
  const [evalBusy, setEvalBusy] = useState(false)
  const [openEvalRun, setOpenEvalRun] = useState<number | null>(null)

  const loadEvaluations = useCallback(async (skillId: string) => {
    try {
      const [suite, runs] = await Promise.all([
        workspaceApi.getSkillEvaluationSuite(skillId),
        workspaceApi.listSkillEvaluationRuns(skillId),
      ])
      setEvalSuite(suite.cases ?? [])
      setEvalRuns(runs.runs ?? [])
    } catch (f) { setError(message(f)) }
  }, [])

  useEffect(() => { if (selected && tab === 'evals') void loadEvaluations(selected.id) }, [selected, tab, loadEvaluations])

  const saveEvalSuite = async () => {
    if (!selected) return
    setEvalBusy(true); setError('')
    try {
      const saved = await workspaceApi.saveSkillEvaluationSuite(selected.id, evalSuite)
      setEvalSuite(saved.cases ?? [])
    } catch (f) { setError(message(f)) }
    finally { setEvalBusy(false) }
  }

  const runEvaluations = async (version?: string) => {
    if (!selected) return
    setEvalBusy(true); setError('')
    try {
      await workspaceApi.runSkillEvaluation(selected.id, version)
      await loadEvaluations(selected.id)
    } catch (f) { setError(message(f)) }
    finally { setEvalBusy(false) }
  }

  const touchSection = (section: string) => {
    setProvenance((current) => {
      if (section === '*') {
        // raw YAML edit touches everything
        const next: Record<string, SectionProvenance> = {}
        for (const key of Object.keys(current)) next[key] = 'user'
        return next
      }
      if (current[section] === 'user') return current
      return { ...current, [section]: 'user' }
    })
  }

  return (
    <div style={{ padding: '1rem' }}>
      {error && <InlineNotification kind="error" title={t('action.error') ?? 'Error'} subtitle={error} onClose={() => setError('')} lowContrast style={{ marginBottom: '1rem' }} />}

      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1rem' }}>
        <Heading>{t('skills.title') ?? 'Skills'}</Heading>
        <Stack orientation="horizontal" gap={3}>
          <Button kind="ghost" hasIconOnly renderIcon={Renew} iconDescription={t('action.refresh') ?? 'Refresh'} onClick={() => void load()} />
          <Button renderIcon={Add} onClick={startWizard}>{t('skills.new_skill') ?? 'New Skill'}</Button>
        </Stack>
      </div>

      {skills.length === 0 && !loading && (
        <Tile style={{ textAlign: 'center', padding: '3rem', color: 'var(--tm-text-3)' }}>
          <p>{t('skills.no_skills') ?? 'No skills yet.'}</p>
          <p style={{ fontSize: '0.8rem', marginTop: '0.5rem' }}>{t('skills.no_skills_hint') ?? 'Create a skill to give agents a reusable, versioned capability.'}</p>
        </Tile>
      )}

      {skills.length > 0 && (
        <div style={{ display: 'grid', gridTemplateColumns: 'minmax(220px, 300px) 1fr', gap: '1rem', alignItems: 'start' }}>
          {/* Skills list — sticks below the fixed app header like Runs/Operations */}
          <div style={{ position: 'sticky', top: '3rem', maxHeight: 'calc(100vh - 4rem)', overflowY: 'auto' }}>
            <div style={{ position: 'sticky', top: 0, background: 'var(--tm-bg)', zIndex: 1, paddingBottom: '0.25rem' }}>
              <ListFilter value={filter} onChange={setFilter} placeholder={t('common.filter_skills') ?? 'Filter skills…'} />
            </div>
            {visibleSkills.map((skill) => (
              <Tile
                key={skill.id}
                style={{
                  marginBottom: '0.5rem', cursor: 'pointer', padding: '0.75rem',
                  outline: selected?.id === skill.id ? '1px solid var(--tm-amber)' : 'none',
                }}
                onClick={() => setSelected(skill)}
              >
                <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: '0.5rem' }}>
                  <strong style={{ overflow: 'hidden', textOverflow: 'ellipsis' }}>{skill.name}</strong>
                  <span style={{ display: 'inline-flex', alignItems: 'center', gap: '0.25rem', flexShrink: 0 }}>
                    <OrgBadge orgUnitID={skill.org_unit_id} org={org} />
                    {skill.version_status === 'draft' && <Tag size="sm" type="purple">{t('skills.draft') ?? 'draft'}</Tag>}
                    <Tag size="sm">{skill.version}</Tag>
                  </span>
                </div>
                <div style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)' }}>{skill.id}</div>
              </Tile>
            ))}
          </div>

          {/* Detail card with tabs */}
          {selected && (
            <div className="skill-card">
              <div className="skill-card-header">
                <div>
                  <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', flexWrap: 'wrap' }}>
                    <strong style={{ fontSize: '1.1rem' }}>{selected.name}</strong>
                    <Tag size="sm">{selected.version}</Tag>
                    {selected.version_status === 'draft' && <Tag size="sm" type="purple">{t('skills.draft') ?? 'draft'}</Tag>}
                    <OrgBadge orgUnitID={selected.org_unit_id} org={org} />
                    <code style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)' }}>{selected.id}</code>
                  </div>
                  {selected.description && <p style={{ color: 'var(--tm-text-2)', marginTop: '0.5rem' }}>{selected.description}</p>}
                </div>
                <Stack orientation="horizontal" gap={1}>
                  <Button size="sm" kind="ghost" hasIconOnly renderIcon={Edit} iconDescription={t('action.edit') ?? 'Edit'} onClick={() => {
                    setEditing(selected)
                    setValidation(null)
                    setDraftQuestions([])
                    setProvenance({})
                    let manifest: SkillManifest = {}
                    try { manifest = JSON.parse(JSON.stringify(selected.manifest ?? {})) as SkillManifest } catch { manifest = {} }
                    setForm({ id: selected.id, name: selected.name, description: selected.description, version: selected.version, markdown: selected.markdown, manifest, org_unit_id: selected.org_unit_id ?? '' })
                    setShowForm(true)
                  }} />
                  <Button size="sm" kind="danger--ghost" hasIconOnly renderIcon={TrashCan} iconDescription={t('action.delete') ?? 'Delete'} onClick={() => void remove(selected)} />
                </Stack>
              </div>

              <div className="skill-tabs" role="tablist">
                {(['main', 'executions', 'evolution', 'evals'] as SkillTab[]).map((key) => (
                  <button
                    key={key}
                    role="tab"
                    aria-selected={tab === key}
                    className={`skill-tab ${tab === key ? 'active' : ''}`}
                    onClick={() => setTab(key)}
                  >
                    {t(`skills.tab_${key}`) ?? key}
                    {key === 'executions' && executions.length > 0 && <span className="skill-tab-count">{executions.length}</span>}
                    {key === 'evolution' && versions.length > 0 && <span className="skill-tab-count">{versions.length}</span>}
                  </button>
                ))}
              </div>

              <div className="skill-card-body">
                {tab === 'main' && (
                  <Stack gap={4}>
                    <div>
                      <div className="manifest-section-head"><div className="skill-subheading">{t('skills.instructions') ?? 'Instructions (SKILL.md)'}</div></div>
                      <div className="skill-markdown"><Markdown content={selected.markdown} /></div>
                    </div>
                    <div>
                      <div className="manifest-section-head"><div className="skill-subheading">{t('skills.editor.contract') ?? 'Contract'}</div></div>
                      <pre className="skill-manifest-preview">{manifestToYaml(selected.manifest as SkillManifest)}</pre>
                    </div>
                    {Array.isArray((selected.manifest as SkillManifest)?.capabilities) && (selected.manifest as SkillManifest).capabilities!.length > 0 && (
                      <div style={{ display: 'flex', gap: '0.25rem', flexWrap: 'wrap' }}>
                        {(selected.manifest as SkillManifest).capabilities!.map((c) => <Tag key={c} type="green" size="sm">{c}</Tag>)}
                      </div>
                    )}
                    {Array.isArray((selected.manifest as SkillManifest)?.tools) && (selected.manifest as SkillManifest).tools!.length > 0 && (
                      <div style={{ display: 'flex', gap: '0.25rem', flexWrap: 'wrap' }}>
                        {(selected.manifest as SkillManifest).tools!.map((tool) => <Tag key={tool} type="blue" size="sm">{tool}</Tag>)}
                      </div>
                    )}
                  </Stack>
                )}

                {tab === 'executions' && (
                  <div>
                    {executions.length === 0 && <p style={{ color: 'var(--tm-text-3)' }}>{t('skills.no_executions') ?? 'No runs have used this skill yet.'}</p>}
                    {executions.map((execution) => (
                      <div key={execution.id} className="skill-execution-row">
                        <Tag size="sm">{execution.skill_version}</Tag>
                        <a href={`/agents?run=${execution.run_id}`} style={{ color: 'var(--tm-teal)' }}>{execution.run_id}</a>
                        <span style={{ color: 'var(--tm-text-3)' }}>{execution.agent_id}</span>
                        <span style={{ color: 'var(--tm-text-3)', marginLeft: 'auto' }}>{new Date(execution.started_at).toLocaleString()}</span>
                      </div>
                    ))}
                    <div style={{ marginTop: '1rem' }}>
                      <div className="skill-subheading">{t('skills.memory_section', { count: String(memory.length) }) ?? `Memory (${memory.length})`}</div>
                      <p style={{ fontSize: '0.75rem', color: 'var(--tm-text-3)' }}>{t('skills.memory_hint') ?? ''}</p>
                      {memory.map((item) => (
                        <div key={item.knowledge_id} style={{ padding: '0.5rem 0', borderTop: '1px solid var(--tm-border)' }}>
                          <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'center', flexWrap: 'wrap' }}>
                            <Tag size="sm" type={item.state === 'confirmed' ? 'green' : item.state === 'invalidated' || item.state === 'superseded' ? 'red' : 'gray'}>{item.state}</Tag>
                            {item.capability && <Tag size="sm" type="blue">{item.capability}</Tag>}
                            <code style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)' }}>{item.knowledge_id}</code>
                          </div>
                          <p style={{ margin: '0.25rem 0 0', color: 'var(--tm-text-2)' }}>{item.proposition}</p>
                          <div style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)' }}>{item.run_id} · {new Date(item.occurred_at).toLocaleString()}</div>
                        </div>
                      ))}
                    </div>
                  </div>
                )}

                {tab === 'evolution' && (
                  <div>
                    <div className="skill-subheading">{t('skills.stability') ?? 'Skill stability'}</div>
                    <div className="skill-stats">
                      <div className="skill-stat"><span className="skill-stat-value">{executions.length}</span><span className="skill-stat-label">{t('skills.stat_executions') ?? 'Executions'}</span></div>
                      <div className="skill-stat"><span className="skill-stat-value">{memory.length}</span><span className="skill-stat-label">{t('skills.stat_knowledge') ?? 'Knowledge'}</span></div>
                      <div className="skill-stat"><span className="skill-stat-value">{versions.length}</span><span className="skill-stat-label">{t('skills.stat_versions') ?? 'Versions'}</span></div>
                      <div className="skill-stat"><span className="skill-stat-value">{selected.version}</span><span className="skill-stat-label">{t('skills.stat_current') ?? 'Current'}</span></div>
                    </div>

                    <div className="skill-evolution-timeline">
                      {[...versions].reverse().map((version) => {
                        const isDraft = version.status === 'draft'
                        const isRejected = version.status === 'rejected'
                        const isCurrent = version.version === selected.version
                        // Diff against the current revision for proposals.
                        const currentRow = versions.find((v) => v.version === selected.version)
                        const manifestDiff = currentRow && version.version !== selected.version ? diffManifests(currentRow.manifest as SkillManifest, version.manifest as SkillManifest) : []
                        const [mdAdded, mdRemoved] = currentRow && version.version !== selected.version ? diffMarkdownCounts(currentRow.markdown, version.markdown) : [0, 0]
                        return (
                        <div key={version.version} className="skill-version" style={isRejected ? { opacity: 0.55 } : undefined}>
                          <button className="skill-version-head" onClick={() => setOpenVersion(openVersion === version.version ? null : version.version)}>
                            <span className="skill-version-dot" />
                            <Tag size="sm">{version.version}</Tag>
                            {isCurrent && <Tag size="sm" type="green">{t('skills.current') ?? 'current'}</Tag>}
                            {isDraft && <Tag size="sm" type="purple">{t('skills.draft') ?? 'draft'}</Tag>}
                            {isRejected && <Tag size="sm" type="red">{t('skills.rejected') ?? 'rejected'}</Tag>}
                            {!isDraft && !isRejected && version.origin === 'agent-proposal' && <Tag size="sm" type="blue">{t('skills.origin_agent') ?? 'agent proposal'}</Tag>}
                            <span style={{ fontSize: '0.75rem', color: 'var(--tm-text-3)' }}>
                              {isDraft
                                ? (t('skills.proposed') ?? 'proposed')
                                : isRejected
                                  ? (t('skills.rejected_at') ?? 'rejected')
                                  : (t('skills.published') ?? 'published')} {new Date(version.created_at).toLocaleDateString()}
                            </span>
                          </button>
                          {openVersion === version.version && (
                            <div className="skill-version-body">
                              {(version.observed_problem || version.proposed_change || version.expected_effect) && (
                                <div style={{ marginBottom: '0.75rem', padding: '0.5rem 0.75rem', border: '1px solid var(--tm-border)', borderRadius: '6px', fontSize: '0.8rem' }}>
                                  {version.observed_problem && (
                                    <div style={{ marginBottom: '0.25rem' }}>
                                      <span style={{ color: 'var(--tm-text-3)' }}>{t('skills.observed_problem') ?? 'Observed problem'}: </span>
                                      <span style={{ color: 'var(--tm-text-2)' }}>{version.observed_problem}</span>
                                    </div>
                                  )}
                                  {version.proposed_change && (
                                    <div style={{ marginBottom: '0.25rem' }}>
                                      <span style={{ color: 'var(--tm-text-3)' }}>{t('skills.proposed_change') ?? 'Proposed change'}: </span>
                                      <span style={{ color: 'var(--tm-text-2)' }}>{version.proposed_change}</span>
                                    </div>
                                  )}
                                  {version.expected_effect && (
                                    <div>
                                      <span style={{ color: 'var(--tm-text-3)' }}>{t('skills.expected_effect') ?? 'Expected effect'}: </span>
                                      <span style={{ color: 'var(--tm-text-2)' }}>{version.expected_effect}</span>
                                    </div>
                                  )}
                                </div>
                              )}
                              {(version.change_summary || (version.source_runs?.length ?? 0) > 0 || (version.knowledge_ids?.length ?? 0) > 0) && (
                                <div style={{ marginBottom: '0.5rem', fontSize: '0.8rem' }}>
                                  {version.change_summary && (
                                    <div style={{ color: 'var(--tm-text-2)' }}>
                                      <span style={{ color: 'var(--tm-text-3)' }}>{t('skills.change_summary') ?? 'What changed'}: </span>
                                      {version.change_summary}
                                    </div>
                                  )}
                                  {(version.source_runs?.length ?? 0) > 0 && (
                                    <div style={{ color: 'var(--tm-text-3)', marginTop: '0.15rem' }}>
                                      {t('skills.source_runs') ?? 'Source runs'}: {version.source_runs!.join(', ')}
                                    </div>
                                  )}
                                  {(version.knowledge_ids?.length ?? 0) > 0 && (
                                    <div style={{ color: 'var(--tm-text-3)', marginTop: '0.15rem' }}>
                                      {t('skills.knowledge_ids') ?? 'Knowledge'}: {version.knowledge_ids!.join(', ')}
                                    </div>
                                  )}
                                </div>
                              )}
                              {!isCurrent && !isRejected && manifestDiff.length > 0 && (
                                <div style={{ marginBottom: '0.75rem', fontSize: '0.78rem', padding: '0.5rem 0.75rem', background: 'var(--tm-surface-2)', borderRadius: '6px' }}>
                                  <div style={{ color: 'var(--tm-text-3)', marginBottom: '0.25rem' }}>{t('skills.diff_vs_current') ?? 'Changes vs current revision'} ({selected.version} → {version.version}):</div>
                                  {manifestDiff.map((d, i) => (
                                    <div key={i} style={{ fontFamily: 'var(--tm-mono, monospace)', color: 'var(--tm-text-2)' }}>
                                      {d.field}: <span style={{ color: 'var(--tm-text-3)' }}>{d.from || '∅'}</span> → <span>{d.to || '∅'}</span>
                                    </div>
                                  ))}
                                  {(mdAdded > 0 || mdRemoved > 0) && (
                                    <div style={{ marginTop: '0.25rem', color: 'var(--tm-text-3)' }}>SKILL.md: +{mdAdded} −{mdRemoved}</div>
                                  )}
                                </div>
                              )}
                              {!isCurrent && !isRejected && (
                                <div style={{ marginBottom: '0.75rem', display: 'flex', gap: '0.5rem' }}>
                                  {isDraft ? (
                                    <>
                                      <Button size="sm" onClick={() => {
                                        if (confirm(t('skills.apply_confirm', { version: version.version }) ?? `Apply draft ${version.version}? It becomes the current revision for all agents.`)) void applyVersion(selected, version.version)
                                      }}>{t('skills.apply') ?? 'Apply'}</Button>
                                      <Button size="sm" kind="danger" onClick={() => {
                                        if (confirm(t('skills.reject_confirm', { version: version.version }) ?? `Reject draft ${version.version}? It stays in history and can no longer be applied.`)) void rejectVersion(selected, version.version)
                                      }}>{t('skills.reject') ?? 'Reject'}</Button>
                                    </>
                                  ) : (
                                    <Button size="sm" kind="secondary" onClick={() => {
                                      if (confirm(t('skills.rollback_confirm', { version: version.version }) ?? `Make version ${version.version} the current revision?`)) void applyVersion(selected, version.version)
                                    }}>{t('skills.rollback') ?? 'Roll back to this version'}</Button>
                                  )}
                                </div>
                              )}
                              <div className="skill-subheading" style={{ fontSize: '0.7rem' }}>SKILL.md</div>
                              <pre className="skill-manifest-preview">{version.markdown}</pre>
                              <div className="skill-subheading" style={{ fontSize: '0.7rem', marginTop: '0.5rem' }}>skill.yaml</div>
                              <pre className="skill-manifest-preview">{manifestToYaml(version.manifest as SkillManifest)}</pre>
                            </div>
                          )}
                        </div>
                        )
                      })}
                    </div>
                  </div>
                )}

                {tab === 'evals' && (
                  <div>
                    <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '0.5rem', flexWrap: 'wrap', gap: '0.5rem' }}>
                      <div className="skill-subheading" style={{ margin: 0 }}>{t('skills.evals_suite') ?? 'Evaluation suite'}</div>
                      <div style={{ display: 'flex', gap: '0.5rem' }}>
                        <Button size="sm" kind="secondary" disabled={evalBusy} onClick={() => {
                          setEvalSuite([...evalSuite, { name: '', input: '', must_contain: [], must_not_contain: [] }])
                        }}>{t('skills.evals_add_case') ?? 'Add case'}</Button>
                        <Button size="sm" kind="secondary" disabled={evalBusy} onClick={() => void saveEvalSuite()}>{t('skills.evals_save') ?? 'Save suite'}</Button>
                        <Button size="sm" disabled={evalBusy || evalSuite.length === 0} onClick={() => void runEvaluations()}>{t('skills.evals_run') ?? 'Run evaluations'}</Button>
                      </div>
                    </div>
                    {evalSuite.length === 0 ? (
                      <div style={{ padding: '1.5rem 0', textAlign: 'center', color: 'var(--tm-text-3)' }}>
                        <p>{t('skills.evals_empty') ?? 'Evaluation suite is not configured yet.'}</p>
                        <p style={{ fontSize: '0.8rem', marginTop: '0.5rem' }}>{t('skills.evals_empty_hint') ?? 'Add cases: a task input plus expected phrases the answer must (or must not) contain. Each run checks the current skill revision against every case.'}</p>
                      </div>
                    ) : (
                      <div style={{ display: 'flex', flexDirection: 'column', gap: '0.5rem', marginBottom: '1.5rem' }}>
                        {evalSuite.map((c, i) => (
                          <div key={i} style={{ border: '1px solid var(--tm-border)', borderRadius: '6px', padding: '0.6rem 0.75rem' }}>
                            <div style={{ display: 'flex', gap: '0.5rem', marginBottom: '0.4rem' }}>
                              <TextInput
                                id={`eval-case-name-${i}`}
                                hideLabel
                                labelText=""
                                placeholder={t('skills.evals_case_name') ?? 'Case name'}
                                value={c.name}
                                onChange={(e: React.ChangeEvent<HTMLInputElement>) => setEvalSuite(evalSuite.map((x, j) => j === i ? { ...x, name: e.target.value } : x))}
                              />
                              <Button size="sm" kind="ghost" hasIconOnly iconDescription={t('action.delete') ?? 'Delete'} disabled={evalBusy} onClick={() => setEvalSuite(evalSuite.filter((_, j) => j !== i))}>
                                <TrashCan size={16} />
                              </Button>
                            </div>
                            <TextArea
                              id={`eval-case-input-${i}`}
                              hideLabel
                              labelText=""
                              rows={2}
                              placeholder={t('skills.evals_case_input') ?? 'Task for the agent executing this skill…'}
                              value={c.input}
                              onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => setEvalSuite(evalSuite.map((x, j) => j === i ? { ...x, input: e.target.value } : x))}
                            />
                            <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '0.5rem', marginTop: '0.4rem' }}>
                              <TextInput
                                id={`eval-case-must-${i}`}
                                hideLabel
                                labelText=""
                                placeholder={t('skills.evals_must_contain') ?? 'Answer must contain (comma-separated)'}
                                value={(c.must_contain ?? []).join(', ')}
                                onChange={(e: React.ChangeEvent<HTMLInputElement>) => setEvalSuite(evalSuite.map((x, j) => j === i ? { ...x, must_contain: e.target.value.split(',').map((s) => s.trim()).filter(Boolean) } : x))}
                              />
                              <TextInput
                                id={`eval-case-must-not-${i}`}
                                hideLabel
                                labelText=""
                                placeholder={t('skills.evals_must_not_contain') ?? 'Answer must NOT contain'}
                                value={(c.must_not_contain ?? []).join(', ')}
                                onChange={(e: React.ChangeEvent<HTMLInputElement>) => setEvalSuite(evalSuite.map((x, j) => j === i ? { ...x, must_not_contain: e.target.value.split(',').map((s) => s.trim()).filter(Boolean) } : x))}
                              />
                            </div>
                          </div>
                        ))}
                      </div>
                    )}

                    <div className="skill-subheading">{t('skills.evals_history') ?? 'Run history'}</div>
                    {evalBusy && <p style={{ color: 'var(--tm-text-3)', fontSize: '0.8rem' }}>{t('skills.evals_running') ?? 'Running evaluations…'}</p>}
                    {evalRuns.length === 0 && !evalBusy ? (
                      <p style={{ color: 'var(--tm-text-3)', fontSize: '0.8rem' }}>{t('skills.evals_no_runs') ?? 'No evaluation runs yet.'}</p>
                    ) : (
                      <div className="skill-evolution-timeline">
                        {evalRuns.map((run) => {
                          const ok = run.failed === 0
                          return (
                            <div key={run.id} className="skill-version">
                              <button className="skill-version-head" onClick={() => setOpenEvalRun(openEvalRun === run.id ? null : run.id)}>
                                <span className="skill-version-dot" />
                                <Tag size="sm" type={ok ? 'green' : 'red'}>{run.passed}/{run.passed + run.failed}</Tag>
                                <Tag size="sm">v{run.skill_version}</Tag>
                                <span style={{ fontSize: '0.75rem', color: 'var(--tm-text-3)' }}>{new Date(run.created_at).toLocaleString()}</span>
                              </button>
                              {openEvalRun === run.id && (
                                <div className="skill-version-body">
                                  {(run.cases ?? []).map((c, i) => (
                                    <div key={i} style={{ padding: '0.4rem 0', borderTop: i === 0 ? 'none' : '1px solid var(--tm-border)' }}>
                                      <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'center' }}>
                                        <span style={{ color: c.passed ? 'var(--tm-success, green)' : 'var(--tm-danger, red)' }}>{c.passed ? '✓' : '✗'}</span>
                                        <strong style={{ fontSize: '0.8rem' }}>{c.name}</strong>
                                        {(c.missed?.length ?? 0) > 0 && <Tag size="sm" type="red">{t('skills.evals_missed') ?? 'missed'}: {c.missed!.join(', ')}</Tag>}
                                        {(c.unexpected?.length ?? 0) > 0 && <Tag size="sm" type="red">{t('skills.evals_unexpected') ?? 'unexpected'}: {c.unexpected!.join(', ')}</Tag>}
                                      </div>
                                      {c.answer && <details style={{ marginTop: '0.25rem' }}><summary style={{ fontSize: '0.75rem', color: 'var(--tm-text-3)', cursor: 'pointer' }}>{t('skills.evals_answer') ?? 'Answer'}</summary><pre className="skill-manifest-preview">{c.answer}</pre></details>}
                                    </div>
                                  ))}
                                </div>
                              )}
                            </div>
                          )
                        })}
                      </div>
                    )}
                  </div>
                )}
              </div>
            </div>
          )}
        </div>
      )}

      {/* Wizard: describe the skill in natural language */}
      {wizardOpen && (
        <AppModal onClose={() => setWizardOpen(false)} panelStyle={{ width: '560px' }}>
            <Heading>{t('skills.wizard_title') ?? 'Create skill'}</Heading>
            {wizardBusy ? (
              <GeneratingState
                title={t('skills.generating_title') ?? 'Building skill draft…'}
                hint={t('skills.generating_hint') ?? 'The builder agent turns your description into a structured draft. This usually takes less than a minute.'}
              />
            ) : (
              <>
            <p style={{ color: 'var(--tm-text-3)', fontSize: '0.8rem', margin: '0.25rem 0 0.75rem' }}>
              {t('skills.wizard_hint') ?? 'The agent will turn your description into a structured skill draft — you can adjust everything afterwards.'}
            </p>
            <TextArea
              id="wizard-description"
              hideLabel
              labelText={t('skills.wizard_label') ?? 'Describe how the skill should work'}
              rows={7}
              value={wizardText}
              onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => setWizardText(e.target.value)}
              placeholder={t('skills.wizard_placeholder') ?? 'Check pull requests: analyze changes, find problems, verify tests and leave comments…'}
              autoFocus
            />
            {wizardError && <InlineNotification kind="error" title={t('skills.wizard_failed') ?? 'Generation failed'} subtitle={wizardError} lowContrast hideCloseButton style={{ marginTop: '0.5rem' }} />}
            {draftQuestions.length > 0 && (
              <div className="wizard-questions">
                <div className="skill-subheading">{t('skills.agent_questions') ?? 'The agent asks for clarification'}</div>
                {draftQuestions.map((q, i) => (
                  <div key={i} style={{ marginBottom: '0.5rem' }}>
                    <p style={{ margin: '0 0 0.25rem', fontSize: '0.8rem', color: 'var(--tm-text-2)' }}>{q.question}</p>
                    <TextInput
                      id={`answer-${i}`}
                      hideLabel
                      labelText=""
                      value={answers[String(i)] ?? ''}
                      placeholder={t('skills.answer_placeholder') ?? 'Your answer'}
                      onChange={(e: React.ChangeEvent<HTMLInputElement>) => setAnswers({ ...answers, [String(i)]: e.target.value })}
                    />
                  </div>
                ))}
              </div>
            )}
            <div className="form-actions">
              <Button kind="secondary" onClick={() => setWizardOpen(false)}>{t('action.cancel') ?? 'Cancel'}</Button>
              <Button kind="secondary" onClick={startManual}>{t('skills.wizard_manual') ?? 'Fill manually'}</Button>
              <Button onClick={() => void generate()} disabled={wizardBusy || !wizardText.trim()}>
                {draftQuestions.length ? (t('skills.send_answers') ?? 'Send answers') : (t('skills.wizard_generate') ?? 'Generate skill')}
              </Button>
            </div>
              </>
            )}
        </AppModal>
      )}

      {/* Create/Edit form (prefilled by the agent after the wizard) */}
      {showForm && (
        <AppModal onClose={() => { setShowForm(false); setEditing(null) }} panelClassName="tabbed" panelStyle={{ width: '760px', maxHeight: '85vh', minHeight: '480px' }}>
            <SkillFormFields
              form={form}
              setForm={setForm}
              editing={editing}
              org={org}
              provenance={provenance}
              onTouch={touchSection}
              suggestions={{ ...suggestions, capabilities: capabilitySuggestions }}
              validation={validation}
              onValidate={() => void validate()}
              onSave={() => void save()}
              onCancel={() => { setShowForm(false); setEditing(null) }}
              questions={draftQuestions}
              answers={answers}
              setAnswers={setAnswers}
              onSendAnswers={() => { setShowForm(false); setWizardOpen(true); void generate(answers) }}
              saving={loading}
            />
        </AppModal>
      )}

      {loading && <Loading withOverlay={false} />}
    </div>
  )
}

type SkillFormTab = 'general' | 'instructions' | 'contract' | 'runtime'

/** Shared CRUD fields: used by the create/edit modal (and available for inline editing). */
function SkillFormFields({
  form, setForm, editing, org, provenance, onTouch, suggestions, validation, onValidate, onSave, onCancel,
  questions, answers, setAnswers, onSendAnswers, saving,
}: {
  form: SkillFormState
  setForm: React.Dispatch<React.SetStateAction<SkillFormState>>
  editing: Skill | null
  org?: OrgUnitsState
  provenance?: Record<string, SectionProvenance>
  onTouch: (section: string) => void
  suggestions?: ManifestSuggestions
  validation: SkillValidationIssue[] | null
  onValidate: () => void
  onSave: () => void
  onCancel: () => void
  questions: SkillDraftQuestion[]
  answers: Record<string, string>
  setAnswers: React.Dispatch<React.SetStateAction<Record<string, string>>>
  onSendAnswers: () => void
  saving: boolean
}) {
  const t = useT()
  const [tab, setTab] = useState<SkillFormTab>('general')
  const tabs: Array<{ key: SkillFormTab; label: string }> = [
    { key: 'general', label: t('skills.ftab_general') ?? 'General' },
    { key: 'instructions', label: t('skills.ftab_instructions') ?? 'Instructions' },
    { key: 'contract', label: t('skills.ftab_contract') ?? 'Contract' },
    { key: 'runtime', label: t('skills.ftab_runtime') ?? 'Runtime' },
  ]
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: '0.75rem', minHeight: 0, flex: 1 }}>
      <Heading>{editing ? (t('skills.edit_skill') ?? 'Edit Skill') : (t('skills.new_skill') ?? 'New Skill')}</Heading>

      {questions.length > 0 && (
        <div className="wizard-questions">
          <div className="skill-subheading">{t('skills.agent_questions') ?? 'The agent asks for clarification'}</div>
          <p style={{ fontSize: '0.75rem', color: 'var(--tm-text-3)', margin: '0 0 0.5rem' }}>{t('skills.agent_questions_hint') ?? ''}</p>
          {questions.map((q, i) => (
            <div key={i} style={{ marginBottom: '0.5rem' }}>
              <p style={{ margin: '0 0 0.25rem', fontSize: '0.8rem', color: 'var(--tm-text-2)' }}>{q.question}</p>
              <TextInput
                id={`form-answer-${i}`}
                hideLabel
                labelText=""
                value={answers[String(i)] ?? ''}
                placeholder={t('skills.answer_placeholder') ?? 'Your answer'}
                onChange={(e: React.ChangeEvent<HTMLInputElement>) => setAnswers({ ...answers, [String(i)]: e.target.value })}
              />
            </div>
          ))}
          <Button size="sm" kind="secondary" onClick={onSendAnswers}>{t('skills.send_answers') ?? 'Send answers'}</Button>
        </div>
      )}

      <div className="skill-tabs modal-tabs" role="tablist">
        {tabs.map(({ key, label }) => (
          <button
            key={key}
            role="tab"
            aria-selected={tab === key}
            className={`skill-tab ${tab === key ? 'active' : ''}`}
            onClick={() => setTab(key)}
          >
            {label}
          </button>
        ))}
      </div>

      <div className="modal-scroll">
        {tab === 'general' && (
          <>
            {!editing && (
              <TextInput id="skill-id" labelText={t('skills.id') ?? 'ID'} value={form.id} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, id: e.target.value })} placeholder="deploy-service" helperText={t('skills.id_helper') ?? 'Lowercase with dashes; auto-generated from name if empty'} />
            )}
            <TextInput id="skill-name" labelText={t('skills.name') ?? 'Name'} value={form.name} onChange={(e: React.ChangeEvent<HTMLInputElement>) => { onTouch('name'); setForm({ ...form, name: e.target.value }) }} placeholder="Deploy service" />
            <TextInput id="skill-description" labelText={t('skills.description') ?? 'Description'} value={form.description} onChange={(e: React.ChangeEvent<HTMLInputElement>) => { onTouch('description'); setForm({ ...form, description: e.target.value }) }} />
            <TextInput id="skill-version" labelText={t('skills.version') ?? 'Version'} value={form.version} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, version: e.target.value })} helperText={editing ? (t('skills.version_helper') ?? 'Increase the version to record a new immutable version') : '1.0.0'} />
            {org && org.units.length > 0 && (
              <>
                <OrgUnitSelect
                  id="skill-org"
                  value={form.org_unit_id ?? ''}
                  onChange={(orgUnitID) => setForm({ ...form, org_unit_id: orgUnitID })}
                  org={org}
                  disabled={!!editing && !org.isAdmin}
                />
                {!!editing && !org.isAdmin && (
                  <p style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)', margin: 0 }}>{t('org.binding_admin_only') ?? 'Only an administrator can change the availability of an existing resource.'}</p>
                )}
              </>
            )}
          </>
        )}
        {tab === 'instructions' && (
          <TextArea id="skill-markdown" labelText={t('skills.instructions') ?? 'Instructions (SKILL.md)'} rows={18} value={form.markdown} onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => { onTouch('markdown'); setForm({ ...form, markdown: e.target.value }) }} />
        )}
        {tab === 'contract' && (
          <SkillManifestEditor manifest={form.manifest} onChange={(manifest) => setForm({ ...form, manifest })} suggestions={suggestions} provenance={provenance} onTouch={onTouch} variant="contract" />
        )}
        {tab === 'runtime' && (
          <SkillManifestEditor manifest={form.manifest} onChange={(manifest) => setForm({ ...form, manifest })} suggestions={suggestions} provenance={provenance} onTouch={onTouch} variant="runtime" />
        )}
      </div>

      {validation && (
        <div style={{ margin: 0 }}>
          {validation.length === 0
            ? <InlineNotification kind="success" title={t('skills.valid') ?? 'Valid'} subtitle={t('skills.valid_hint') ?? ''} lowContrast hideCloseButton />
            : <InlineNotification kind="warning" title={t('skills.invalid') ?? 'Issues found'} subtitle={validation.map((i) => `${i.field}: ${i.message}`).join(' · ')} lowContrast hideCloseButton />}
        </div>
      )}
      <div className="form-actions" style={{ marginTop: 0 }}>
        <Button kind="secondary" onClick={onValidate}>{t('skills.validate') ?? 'Validate'}</Button>
        <Button kind="secondary" onClick={onCancel}>{t('action.cancel') ?? 'Cancel'}</Button>
        <Button onClick={onSave} disabled={saving}>{editing ? (t('action.save') ?? 'Save') : (t('action.create') ?? 'Create')}</Button>
      </div>
    </div>
  )
}
