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
  Section,
  Heading,
  Toggle,
  NumberInput,
  Checkbox,
} from '@carbon/react'
import { Add, Edit, TrashCan, Copy } from '@carbon/icons-react'
import { workspaceApi, type Agent, type Provider, type Skill } from './workspaceApi'
import { useT } from './i18n'

function message(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }

export default function Agents({ defaultAgentId }: { defaultAgentId?: string }) {
  const t = useT()
  const [agents, setAgents] = useState<Agent[]>([])
  const [providers, setProviders] = useState<Provider[]>([])
  const [availableSkills, setAvailableSkills] = useState<Skill[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [showForm, setShowForm] = useState(false)
  const [editing, setEditing] = useState<Agent | null>(null)
  const [expandedId, setExpandedId] = useState<string | null>(null)
  const [showTemplates, setShowTemplates] = useState(false)

  // Form state
  const [form, setForm] = useState<Partial<Agent>>({})

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const [agentsData, providersData, skillsData] = await Promise.all([
        workspaceApi.listAllAgents(),
        workspaceApi.listProviders(),
        workspaceApi.listSkills(''),
      ])
      setAgents(agentsData.agents ?? [])
      setProviders(providersData.providers ?? [])
      setAvailableSkills(skillsData.skills ?? [])
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }, [])

  useEffect(() => { void load() }, [load])

  const TEMPLATES = [
  {
    name: 'Coder',
    description: 'General-purpose coding agent',
    model: '',
    system_prompt: 'You are a careful developer. Read code before modifying it. Run tests after changes. Write clean, minimal code.',
    sandbox_profile: 'standard',
    network_access: true,
    max_turns: 30,
  },
  {
    name: 'Reviewer',
    description: 'Code review and analysis (read-only)',
    model: '',
    system_prompt: 'You are a code reviewer. Read the codebase, identify issues, and report findings. Do not modify files.',
    sandbox_profile: 'restricted',
    network_access: false,
    read_only: true,
    max_turns: 15,
  },
  {
    name: 'Researcher',
    description: 'Web research and documentation',
    model: '',
    system_prompt: 'You are a researcher. Search the web, read documentation, and compile findings into a clear report.',
    sandbox_profile: 'standard',
    network_access: true,
    max_turns: 20,
  },
  {
    name: 'DevOps',
    description: 'Infrastructure and deployment tasks',
    model: '',
    system_prompt: 'You are a DevOps engineer. Work with Docker, CI/CD, infrastructure config. Always verify changes before applying.',
    sandbox_profile: 'standard',
    network_access: true,
    max_turns: 25,
  },
]

const startCreate = () => {
    setEditing(null)
    setForm({ name: '', model: '', system_prompt: '', skills: [], mcp_servers: [] })
    setShowForm(true)
  }

  const startEdit = (agent: Agent) => {
    setEditing(agent)
    setForm({ ...agent })
    setShowForm(true)
  }

  const saveAgent = async () => {
    if (!form.name?.trim()) return
    setLoading(true); setError('')
    try {
      if (editing) {
        await workspaceApi.updateAgent(editing.id, form)
      } else {
        await workspaceApi.createAgent(form as Agent & { name: string })
      }
      setShowForm(false); setEditing(null)
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
    setForm({ ...agent, id: undefined, name: agent.name + (t('agents.copy_suffix') ?? ' (copy)') })
    setShowForm(true)
  }

  return (
    <div style={{ padding: '1rem' }}>
      {error && <InlineNotification kind="error" title={t('action.error') ?? 'Error'} subtitle={error} onClose={() => setError('')} lowContrast style={{ marginBottom: '1rem' }} />}

      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1rem' }}>
        <Heading>{t('agents.title') ?? 'Agents'}</Heading>
        <Stack orientation="horizontal" gap={2}>
          <Button kind="secondary" onClick={() => setShowTemplates(true)}>{t('agents.from_template') ?? 'From template'}</Button>
          <Button renderIcon={Add} onClick={startCreate}>{t('agents.new_agent') ?? 'New Agent'}</Button>
        </Stack>
      </div>

      <Grid>
        {agents.map((a) => (
          <Column key={a.id} sm={4} md={4} lg={4}>
            <Tile style={{ marginBottom: '0.75rem' }}>
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start' }}>
                <div>
                  <strong>{a.name}</strong>
                  {a.id === defaultAgentId && <Tag type="green" size="sm" style={{ marginLeft: '0.5rem' }}>{t('agents.project_default') ?? 'project default'}</Tag>}
                  {a.description && <p style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem', marginTop: '0.25rem' }}>{a.description}</p>}
                  <div style={{ display: 'flex', gap: '0.25rem', flexWrap: 'wrap', marginTop: '0.5rem' }}>
                    <Tag type="blue" size="sm">{a.model || (t('agents.no_model') ?? 'no model')}</Tag>
                    {a.provider && <Tag type="cyan" size="sm">{a.provider}</Tag>}
                    <Tag type="gray" size="sm">{a.sandbox_profile || (t('agents.default') ?? 'default')}</Tag>
                    {a.project_id && <Tag type="warm-gray" size="sm">{a.project_id}</Tag>}
                  </div>
                </div>
                <Stack orientation="horizontal" gap={1}>
                  <Button size="sm" kind="ghost" hasIconOnly renderIcon={Edit} iconDescription="Edit" onClick={() => startEdit(a)} />
                  <Button size="sm" kind="ghost" hasIconOnly renderIcon={Copy} iconDescription="Duplicate" onClick={() => duplicateAgent(a)} />
                  <Button size="sm" kind="danger--ghost" hasIconOnly renderIcon={TrashCan} iconDescription="Delete" onClick={() => void deleteAgent(a)} />
                </Stack>
              </div>

              <Button size="sm" kind="ghost" onClick={() => setExpandedId(expandedId === a.id ? null : a.id)} style={{ marginTop: '0.5rem' }}>
                {expandedId === a.id ? (t('agents.hide_details') ?? 'Hide details') : (t('agents.show_details') ?? 'Show details')}
              </Button>

              {expandedId === a.id && (
                <div style={{ marginTop: '0.75rem', fontSize: '0.75rem' }}>
                  <dl>
                    <dt style={{ color: 'var(--tm-text-3)' }}>{t('agents.system_prompt') ?? 'System prompt'}</dt>
                    <dd style={{ marginBottom: '0.5rem' }}>{a.system_prompt || (t('agents.none') ?? '(none)')}</dd>
                    <dt style={{ color: 'var(--tm-text-3)' }}>{t('agents.temperature') ?? 'Temperature'}</dt>
                    <dd style={{ marginBottom: '0.5rem' }}>{a.temperature ?? (t('agents.default') ?? 'default')}</dd>
                    <dt style={{ color: 'var(--tm-text-3)' }}>{t('agents.max_tokens') ?? 'Max tokens'}</dt>
                    <dd style={{ marginBottom: '0.5rem' }}>{a.max_tokens ?? (t('agents.default') ?? 'default')}</dd>
                    <dt style={{ color: 'var(--tm-text-3)' }}>{t('agents.max_turns') ?? 'Max turns'}</dt>
                    <dd style={{ marginBottom: '0.5rem' }}>{a.max_turns ?? (t('agents.unlimited') ?? 'unlimited')}</dd>
                    <dt style={{ color: 'var(--tm-text-3)' }}>{t('agents.skills') ?? 'Skills'}</dt>
                    <dd style={{ marginBottom: '0.5rem' }}>{a.skills?.length ? a.skills.join(', ') : (t('agents.none') ?? '(none)')}</dd>
                    <dt style={{ color: 'var(--tm-text-3)' }}>{t('agents.mcp_servers') ?? 'MCP servers'}</dt>
                    <dd style={{ marginBottom: '0.5rem' }}>{a.mcp_servers?.length ? a.mcp_servers.join(', ') : (t('agents.none') ?? '(none)')}</dd>
                    <dt style={{ color: 'var(--tm-text-3)' }}>{t('agents.network') ?? 'Network'}</dt>
                    <dd style={{ marginBottom: '0.5rem' }}>{a.network_access ? (t('agents.enabled') ?? 'enabled') : (t('agents.blocked') ?? 'blocked')}</dd>
                    <dt style={{ color: 'var(--tm-text-3)' }}>{t('agents.read_only') ?? 'Read-only'}</dt>
                    <dd>{a.read_only ? (t('agents.yes') ?? 'yes') : (t('agents.no') ?? 'no')}</dd>
                  </dl>
                </div>
              )}
            </Tile>
          </Column>
        ))}
      </Grid>

      {/* Edit/Create modal */}
      {showForm && (
        <div className="modal-overlay">
          <div className="modal-panel" style={{ width: '600px' }}>
            <Heading>{editing ? (t('agents.edit_agent') ?? 'Edit Agent') : (t('agents.new_agent') ?? 'New Agent')}</Heading>
            <Stack gap={3}>
              <TextInput id="agent-name" labelText={t('agents.name') ?? 'Name'} value={form.name ?? ''} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, name: e.target.value })} placeholder="coder" autoFocus />
              <TextInput id="agent-desc" labelText={t('agents.description') ?? 'Description'} value={form.description ?? ''} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, description: e.target.value })} placeholder="A careful developer agent" />
              <Select id="agent-provider" labelText={t('agents.provider') ?? 'Provider'} value={form.provider ?? ''} onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setForm({ ...form, provider: e.target.value })}>
                <SelectItem value="" text={t('agents.default_label') ?? 'Default'} />
                {providers.map((p) => <SelectItem key={p.id} value={p.id} text={p.name} />)}
              </Select>
              <TextInput id="agent-model" labelText={t('agents.model') ?? 'Model'} value={form.model ?? ''} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, model: e.target.value })} placeholder="gpt-4o, claude-3-sonnet, etc." />
              <TextArea id="agent-prompt" labelText={t('agents.system_prompt') ?? 'System prompt'} value={form.system_prompt ?? ''} onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => setForm({ ...form, system_prompt: e.target.value })} rows={6} placeholder="You are a careful developer..." />
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
              {availableSkills.length > 0 && (
                <div>
                  <p className="cds--label" style={{ marginBottom: '0.5rem' }}>{t('agents.skills') ?? 'Skills'}</p>
                  <p style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem', margin: '0 0 0.5rem' }}>{t('agents.skills_hint') ?? 'Selected skills are injected into the system prompt at run time.'}</p>
                  <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '0.25rem 1rem', maxHeight: '10rem', overflow: 'auto', padding: '0.5rem 0.75rem', border: '1px solid var(--tm-border)', borderRadius: '6px' }}>
                    {availableSkills.map((s) => (
                      <Checkbox
                        key={s.id}
                        id={`agent-skill-${s.id}`}
                        labelText={s.name}
                        title={s.id}
                        checked={form.skills?.includes(s.id) ?? false}
                        onChange={(_: React.ChangeEvent<HTMLInputElement>, { checked }: { checked: boolean }) =>
                          setForm({ ...form, skills: checked ? [...(form.skills ?? []), s.id] : (form.skills ?? []).filter((x) => x !== s.id) })}
                      />
                    ))}
                  </div>
                </div>
              )}
              <div className="form-actions">
                <Button kind="secondary" onClick={() => { setShowForm(false); setEditing(null) }}>{t('action.cancel') ?? 'Cancel'}</Button>
                <Button onClick={() => void saveAgent()}>{editing ? (t('action.save') ?? 'Save') : (t('action.create') ?? 'Create')}</Button>
              </div>
            </Stack>
          </div>
        </div>
      )}

      {loading && <Loading withOverlay={false} />}

      {/* Template selection modal */}
      {showTemplates && (
        <div className="modal-overlay">
          <div className="modal-panel">
            <Heading>{t('agents.choose_template') ?? 'Choose a template'}</Heading>
            <p style={{ color: 'var(--tm-text-3)', fontSize: '0.8rem', marginBottom: '1rem' }}>{t('agents.template_hint') ?? 'Start with a pre-configured agent and customize as needed.'}</p>
            <Stack gap={2}>
              {TEMPLATES.map((tpl) => (
                <Tile key={tpl.name} style={{ cursor: 'pointer' }} onClick={() => {
                  setEditing(null)
                  setForm({ name: tpl.name, model: tpl.model, system_prompt: tpl.system_prompt, skills: [], mcp_servers: [], sandbox_profile: tpl.sandbox_profile, network_access: tpl.network_access, read_only: tpl.read_only, max_turns: tpl.max_turns })
                  setShowForm(true)
                  setShowTemplates(false)
                }}>
                  <strong>{tpl.name}</strong>
                  <p style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem', marginTop: '0.25rem' }}>{tpl.description}</p>
                  <div style={{ display: 'flex', gap: '0.25rem', marginTop: '0.25rem' }}>
                    <Tag type="gray" size="sm">{tpl.sandbox_profile}</Tag>
                    {tpl.network_access ? <Tag type="green" size="sm">{t('agents.network_label') ?? 'network'}</Tag> : <Tag type="red" size="sm">{t('agents.no_network') ?? 'no network'}</Tag>}
                    {tpl.read_only && <Tag type="warm-gray" size="sm">{t('agents.read_only_tag') ?? 'read-only'}</Tag>}
                    <Tag type="blue" size="sm">{t('agents.turns_count', { count: String(tpl.max_turns) })}</Tag>
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