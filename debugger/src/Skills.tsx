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
import { workspaceApi, type Skill, type SkillVersion, type SkillExecution, type SkillMemoryItem, type SkillValidationIssue } from './workspaceApi'
import { useT } from './i18n'
import Markdown from './Markdown'
import SkillManifestEditor, { manifestToYaml, type SkillManifest, type ManifestSuggestions } from './SkillManifestEditor'

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

function manifestList(manifest: Record<string, unknown>, key: string): string[] {
  const value = manifest?.[key]
  return Array.isArray(value) ? value.map(String) : []
}

export default function Skills({ project }: { project: string }) {
  const t = useT()
  const [skills, setSkills] = useState<Skill[]>([])
  const [selected, setSelected] = useState<Skill | null>(null)
  const [versions, setVersions] = useState<SkillVersion[]>([])
  const [executions, setExecutions] = useState<SkillExecution[]>([])
  const [memory, setMemory] = useState<SkillMemoryItem[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [showForm, setShowForm] = useState(false)
  const [editing, setEditing] = useState<Skill | null>(null)
  const [form, setForm] = useState<{ id: string; name: string; description: string; version: string; markdown: string; manifest: SkillManifest }>({ id: '', name: '', description: '', version: '1.0.0', markdown: STARTER_MARKDOWN, manifest: STARTER_MANIFEST })
  const [validation, setValidation] = useState<SkillValidationIssue[] | null>(null)
  const [openVersion, setOpenVersion] = useState<string | null>(null)
  const [suggestions, setSuggestions] = useState<ManifestSuggestions>({})

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

  const load = useCallback(async () => {
    if (!project.trim()) return
    setLoading(true)
    try {
      const data = await workspaceApi.listSkills(project)
      setSkills(data.skills ?? [])
      setSelected((current) => current && data.skills?.some((s) => s.id === current.id) ? data.skills.find((s) => s.id === current.id)! : (data.skills?.[0] ?? null))
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }, [project])

  useEffect(() => { void load() }, [load])

  const openSkill = useCallback(async (skill: Skill) => {
    setSelected(skill)
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

  const startCreate = () => {
    setEditing(null)
    setValidation(null)
    setForm({ id: '', name: '', description: '', version: '1.0.0', markdown: STARTER_MARKDOWN, manifest: STARTER_MANIFEST })
    setShowForm(true)
  }

  const startEdit = (skill: Skill) => {
    setEditing(skill)
    setValidation(null)
    let manifest: SkillManifest = {}
    try { manifest = JSON.parse(JSON.stringify(skill.manifest ?? {})) as SkillManifest } catch { manifest = {} }
    setForm({
      id: skill.id,
      name: skill.name,
      description: skill.description,
      version: skill.version,
      markdown: skill.markdown,
      manifest,
    })
    setShowForm(true)
  }

  // Manifest yaml is derived from the structured form so name/description/version
  // stay in sync with the top-level fields instead of being edited twice.
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
        await workspaceApi.updateSkill(editing.id, {
          name: form.name, description: form.description, version: form.version,
          markdown: form.markdown, manifest_yaml: manifestYaml(),
        })
      } else {
        await workspaceApi.createSkill({
          id: form.id.trim() || form.name.trim().toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, ''),
          project_id: project, name: form.name, description: form.description, version: form.version,
          markdown: form.markdown, manifest_yaml: manifestYaml(),
        })
      }
      setShowForm(false); setEditing(null)
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

  return (
    <div style={{ padding: '1rem' }}>
      {error && <InlineNotification kind="error" title={t('action.error') ?? 'Error'} subtitle={error} onClose={() => setError('')} lowContrast style={{ marginBottom: '1rem' }} />}

      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1rem' }}>
        <Heading>{t('skills.title') ?? 'Skills'}</Heading>
        <Stack orientation="horizontal" gap={3}>
          <Button kind="ghost" hasIconOnly renderIcon={Renew} iconDescription={t('action.refresh') ?? 'Refresh'} onClick={() => void load()} />
          <Button renderIcon={Add} onClick={startCreate}>{t('skills.new_skill') ?? 'New Skill'}</Button>
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
          {/* Skills list */}
          <div style={{ position: 'sticky', top: '1rem' }}>
            {skills.map((skill) => (
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
                  <Tag size="sm">{skill.version}</Tag>
                </div>
                <div style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)' }}>{skill.id}</div>
              </Tile>
            ))}
          </div>

          {/* Detail panel */}
          {selected && (
            <div>
              <Tile style={{ marginBottom: '0.75rem' }}>
                <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start' }}>
                  <div>
                    <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', flexWrap: 'wrap' }}>
                      <strong>{selected.name}</strong>
                      <Tag size="sm">{selected.version}</Tag>
                      <code style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)' }}>{selected.id}</code>
                    </div>
                    {selected.description && <p style={{ color: 'var(--tm-text-2)', marginTop: '0.5rem' }}>{selected.description}</p>}
                    {manifestList(selected.manifest as Record<string, unknown>, 'capabilities').length > 0 && (
                      <div style={{ display: 'flex', gap: '0.25rem', flexWrap: 'wrap', marginTop: '0.5rem' }}>
                        {manifestList(selected.manifest as Record<string, unknown>, 'capabilities').map((capability) => (
                          <Tag key={capability} type="green" size="sm">{capability}</Tag>
                        ))}
                      </div>
                    )}
                    {manifestList(selected.manifest as Record<string, unknown>, 'tools').length > 0 && (
                      <div style={{ display: 'flex', gap: '0.25rem', flexWrap: 'wrap', marginTop: '0.25rem' }}>
                        {manifestList(selected.manifest as Record<string, unknown>, 'tools').map((tool) => (
                          <Tag key={tool} type="blue" size="sm">{tool}</Tag>
                        ))}
                      </div>
                    )}
                  </div>
                  <Stack orientation="horizontal" gap={1}>
                    <Button size="sm" kind="ghost" hasIconOnly renderIcon={Edit} iconDescription="Edit" onClick={() => startEdit(selected)} />
                    <Button size="sm" kind="danger--ghost" hasIconOnly renderIcon={TrashCan} iconDescription="Delete" onClick={() => void remove(selected)} />
                  </Stack>
                </div>
              </Tile>

              <Tile style={{ marginBottom: '0.75rem' }}>
                <Heading style={{ fontSize: '0.9rem' }}>{t('skills.markdown_section') ?? 'SKILL.md'}</Heading>
                <div style={{ color: 'var(--tm-text-2)' }}><Markdown content={selected.markdown} /></div>
              </Tile>

              <Tile style={{ marginBottom: '0.75rem' }}>
                <Heading style={{ fontSize: '0.9rem' }}>{t('skills.manifest_section') ?? 'Manifest (skill.yaml)'}</Heading>
                <pre style={{ fontSize: '0.75rem', whiteSpace: 'pre-wrap', margin: 0 }}>{manifestToYaml(selected.manifest as SkillManifest)}</pre>
              </Tile>

              <Tile style={{ marginBottom: '0.75rem' }}>
                <Heading style={{ fontSize: '0.9rem' }}>{t('skills.versions_section', { count: String(versions.length) }) ?? `Versions (${versions.length})`}</Heading>
                {versions.map((version) => (
                  <div key={version.version} style={{ padding: '0.5rem 0' }}>
                    <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'center', cursor: 'pointer' }} onClick={() => setOpenVersion(openVersion === version.version ? null : version.version)}>
                      <Tag size="sm">{version.version}</Tag>
                      <span style={{ fontSize: '0.75rem', color: 'var(--tm-text-3)' }}>{new Date(version.created_at).toLocaleString()}</span>
                      {version.version === selected.version && <Tag size="sm" type="green">{t('skills.current') ?? 'current'}</Tag>}
                    </div>
                    {openVersion === version.version && (
                      <pre style={{ fontSize: '0.7rem', whiteSpace: 'pre-wrap', margin: '0.5rem 0 0', color: 'var(--tm-text-2)' }}>{version.markdown}</pre>
                    )}
                  </div>
                ))}
              </Tile>

              <Tile style={{ marginBottom: '0.75rem' }}>
                <Heading style={{ fontSize: '0.9rem' }}>{t('skills.executions_section', { count: String(executions.length) }) ?? `Executions (${executions.length})`}</Heading>
                {executions.length === 0 && <p style={{ color: 'var(--tm-text-3)' }}>{t('skills.no_executions') ?? 'No runs used this skill yet.'}</p>}
                {executions.map((execution) => (
                  <div key={execution.id} style={{ display: 'flex', gap: '0.5rem', alignItems: 'center', padding: '0.35rem 0', fontSize: '0.8rem' }}>
                    <Tag size="sm">{execution.skill_version}</Tag>
                    <a href={`/agents?run=${execution.run_id}`} style={{ color: 'var(--tm-teal)' }}>{execution.run_id}</a>
                    <span style={{ color: 'var(--tm-text-3)' }}>{execution.agent_id}</span>
                    <span style={{ color: 'var(--tm-text-3)', marginLeft: 'auto' }}>{new Date(execution.started_at).toLocaleString()}</span>
                  </div>
                ))}
              </Tile>

              <Tile>
                <Heading style={{ fontSize: '0.9rem' }}>{t('skills.memory_section', { count: String(memory.length) }) ?? `Memory (${memory.length})`}</Heading>
                <p style={{ fontSize: '0.75rem', color: 'var(--tm-text-3)' }}>{t('skills.memory_hint') ?? 'Knowledge recorded from this skill — a separate temporal layer, never part of the skill file.'}</p>
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
              </Tile>
            </div>
          )}
        </div>
      )}

      {loading && <Loading withOverlay={false} />}

      {/* Create/Edit modal */}
      {showForm && (
        <div className="modal-overlay">
          <div className="modal-panel" style={{ width: '760px', maxHeight: '85vh', overflow: 'auto' }}>
            <Heading>{editing ? (t('skills.edit_skill') ?? 'Edit Skill') : (t('skills.new_skill') ?? 'New Skill')}</Heading>
            {!editing && (
              <TextInput id="skill-id" labelText={t('skills.id') ?? 'ID'} value={form.id} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, id: e.target.value })} placeholder="deploy-service" helperText={t('skills.id_helper') ?? 'Lowercase with dashes; auto-generated from name if empty'} />
            )}
            <TextInput id="skill-name" labelText={t('skills.name') ?? 'Name'} value={form.name} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, name: e.target.value })} placeholder="Deploy service" />
            <TextInput id="skill-description" labelText={t('skills.description') ?? 'Description'} value={form.description} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, description: e.target.value })} />
            <TextInput id="skill-version" labelText={t('skills.version') ?? 'Version'} value={form.version} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, version: e.target.value })} helperText={editing ? (t('skills.version_helper') ?? 'Increase the version to record a new immutable version') : '1.0.0'} />
            <TextArea id="skill-markdown" labelText="SKILL.md" rows={10} value={form.markdown} onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => setForm({ ...form, markdown: e.target.value })} style={{ fontFamily: 'monospace', fontSize: '0.8rem' }} />
            <SkillManifestEditor manifest={form.manifest} onChange={(manifest) => setForm({ ...form, manifest })} suggestions={{ ...suggestions, capabilities: capabilitySuggestions }} />
            {validation && (
              <div style={{ margin: '0.5rem 0' }}>
                {validation.length === 0
                  ? <InlineNotification kind="success" title={t('skills.valid') ?? 'Valid'} subtitle={t('skills.valid_hint') ?? 'Manifest passes contract validation'} lowContrast hideCloseButton />
                  : <InlineNotification kind="warning" title={t('skills.invalid') ?? 'Issues found'} subtitle={validation.map((i) => `${i.field}: ${i.message}`).join(' · ')} lowContrast hideCloseButton />}
              </div>
            )}
            <div className="form-actions">
              <Button kind="secondary" onClick={() => void validate()}>{t('skills.validate') ?? 'Validate'}</Button>
              <Button kind="secondary" onClick={() => { setShowForm(false); setEditing(null) }}>{t('action.cancel') ?? 'Cancel'}</Button>
              <Button onClick={() => void save()}>{editing ? (t('action.save') ?? 'Save') : (t('action.create') ?? 'Create')}</Button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
