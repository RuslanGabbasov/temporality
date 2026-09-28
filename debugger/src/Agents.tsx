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
} from '@carbon/react'
import { Add, Edit, TrashCan, Copy } from '@carbon/icons-react'
import { workspaceApi, type Agent, type Provider } from './workspaceApi'

function message(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }

export default function Agents() {
  const [agents, setAgents] = useState<Agent[]>([])
  const [providers, setProviders] = useState<Provider[]>([])
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
      const [agentsData, providersData] = await Promise.all([
        workspaceApi.listAllAgents(),
        workspaceApi.listProviders(),
      ])
      setAgents(agentsData.agents ?? [])
      setProviders(providersData.providers ?? [])
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
    if (!confirm(`Delete agent "${agent.name}"?`)) return
    setLoading(true); setError('')
    try {
      await workspaceApi.deleteAgent(agent.id)
      void load()
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }

  const duplicateAgent = (agent: Agent) => {
    setEditing(null)
    setForm({ ...agent, id: undefined, name: agent.name + ' (copy)' })
    setShowForm(true)
  }

  return (
    <div style={{ padding: '1rem' }}>
      {error && <InlineNotification kind="error" title="Error" subtitle={error} onClose={() => setError('')} lowContrast style={{ marginBottom: '1rem' }} />}

      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1rem' }}>
        <Heading>Agents</Heading>
        <Stack orientation="horizontal" gap={2}>
          <Button kind="secondary" onClick={() => setShowTemplates(true)}>From template</Button>
          <Button renderIcon={Add} onClick={startCreate}>New Agent</Button>
        </Stack>
      </div>

      <Grid>
        {agents.map((a) => (
          <Column key={a.id} sm={4} md={4} lg={4}>
            <Tile style={{ marginBottom: '0.75rem' }}>
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start' }}>
                <div>
                  <strong>{a.name}</strong>
                  {a.description && <p style={{ color: '#7e8a9c', fontSize: '0.75rem', marginTop: '0.25rem' }}>{a.description}</p>}
                  <div style={{ display: 'flex', gap: '0.25rem', flexWrap: 'wrap', marginTop: '0.5rem' }}>
                    <Tag type="blue" size="sm">{a.model || 'no model'}</Tag>
                    {a.provider && <Tag type="cyan" size="sm">{a.provider}</Tag>}
                    <Tag type="gray" size="sm">{a.sandbox_profile || 'default'}</Tag>
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
                {expandedId === a.id ? 'Hide details' : 'Show details'}
              </Button>

              {expandedId === a.id && (
                <div style={{ marginTop: '0.75rem', fontSize: '0.75rem' }}>
                  <dl>
                    <dt style={{ color: '#7e8a9c' }}>System prompt</dt>
                    <dd style={{ marginBottom: '0.5rem' }}>{a.system_prompt || '(none)'}</dd>
                    <dt style={{ color: '#7e8a9c' }}>Temperature</dt>
                    <dd style={{ marginBottom: '0.5rem' }}>{a.temperature ?? 'default'}</dd>
                    <dt style={{ color: '#7e8a9c' }}>Max tokens</dt>
                    <dd style={{ marginBottom: '0.5rem' }}>{a.max_tokens ?? 'default'}</dd>
                    <dt style={{ color: '#7e8a9c' }}>Max turns</dt>
                    <dd style={{ marginBottom: '0.5rem' }}>{a.max_turns ?? 'unlimited'}</dd>
                    <dt style={{ color: '#7e8a9c' }}>Skills</dt>
                    <dd style={{ marginBottom: '0.5rem' }}>{a.skills?.length ? a.skills.join(', ') : '(none)'}</dd>
                    <dt style={{ color: '#7e8a9c' }}>MCP servers</dt>
                    <dd style={{ marginBottom: '0.5rem' }}>{a.mcp_servers?.length ? a.mcp_servers.join(', ') : '(none)'}</dd>
                    <dt style={{ color: '#7e8a9c' }}>Network</dt>
                    <dd style={{ marginBottom: '0.5rem' }}>{a.network_access ? 'enabled' : 'blocked'}</dd>
                    <dt style={{ color: '#7e8a9c' }}>Read-only</dt>
                    <dd>{a.read_only ? 'yes' : 'no'}</dd>
                  </dl>
                </div>
              )}
            </Tile>
          </Column>
        ))}
      </Grid>

      {/* Edit/Create modal */}
      {showForm && (
        <div style={{ position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.6)', zIndex: 1000, display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
          <div style={{ background: '#0d1118', border: '1px solid #344258', borderRadius: '8px', padding: '1.5rem', width: '600px', maxHeight: '90vh', overflow: 'auto' }}>
            <Heading>{editing ? 'Edit Agent' : 'New Agent'}</Heading>
            <Stack gap={3}>
              <TextInput id="agent-name" labelText="Name" value={form.name ?? ''} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, name: e.target.value })} placeholder="coder" autoFocus />
              <TextInput id="agent-desc" labelText="Description" value={form.description ?? ''} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, description: e.target.value })} placeholder="A careful developer agent" />
              <Select id="agent-provider" labelText="Provider" value={form.provider ?? ''} onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setForm({ ...form, provider: e.target.value })}>
                <SelectItem value="" text="Default" />
                {providers.map((p) => <SelectItem key={p.id} value={p.id} text={p.name} />)}
              </Select>
              <TextInput id="agent-model" labelText="Model" value={form.model ?? ''} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, model: e.target.value })} placeholder="gpt-4o, claude-3-sonnet, etc." />
              <TextArea id="agent-prompt" labelText="System prompt" value={form.system_prompt ?? ''} onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => setForm({ ...form, system_prompt: e.target.value })} rows={6} placeholder="You are a careful developer..." />
              <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr 1fr', gap: '0.5rem' }}>
                <NumberInput id="agent-temp" label="Temperature" value={form.temperature ?? 0} onChange={(_, { value }) => setForm({ ...form, temperature: typeof value === 'number' ? value : 0 })} min={0} max={2} step={0.1} />
                <NumberInput id="agent-tokens" label="Max tokens" value={form.max_tokens ?? 1024} onChange={(_, { value }) => setForm({ ...form, max_tokens: typeof value === 'number' ? value : 1024 })} min={64} max={1000000} />
                <NumberInput id="agent-turns" label="Max turns" value={form.max_turns ?? 20} onChange={(_, { value }) => setForm({ ...form, max_turns: typeof value === 'number' ? value : 20 })} min={1} max={100} />
              </div>
              <Select id="agent-sandbox" labelText="Sandbox profile" value={form.sandbox_profile ?? ''} onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setForm({ ...form, sandbox_profile: e.target.value })}>
                <SelectItem value="" text="Default" />
                <SelectItem value="restricted" text="Restricted" />
                <SelectItem value="standard" text="Standard" />
                <SelectItem value="privileged" text="Privileged" />
              </Select>
              <div style={{ display: 'flex', gap: '1rem' }}>
                <Toggle id="agent-network" labelText="Network" toggled={form.network_access ?? false} onToggle={(checked: boolean) => setForm({ ...form, network_access: checked })} />
                <Toggle id="agent-readonly" labelText="Read-only" toggled={form.read_only ?? false} onToggle={(checked: boolean) => setForm({ ...form, read_only: checked })} />
              </div>
              <Stack orientation="horizontal" gap={2}>
                <Button onClick={() => void saveAgent()}>{editing ? 'Save' : 'Create'}</Button>
                <Button kind="secondary" onClick={() => { setShowForm(false); setEditing(null) }}>Cancel</Button>
              </Stack>
            </Stack>
          </div>
        </div>
      )}

      {loading && <Loading withOverlay={false} />}

      {/* Template selection modal */}
      {showTemplates && (
        <div style={{ position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.6)', zIndex: 1000, display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
          <div style={{ background: '#0d1118', border: '1px solid #344258', borderRadius: '8px', padding: '1.5rem', width: '500px', maxHeight: '80vh', overflow: 'auto' }}>
            <Heading>Choose a template</Heading>
            <p style={{ color: '#7e8a9c', fontSize: '0.8rem', marginBottom: '1rem' }}>Start with a pre-configured agent and customize as needed.</p>
            <Stack gap={2}>
              {TEMPLATES.map((t) => (
                <Tile key={t.name} style={{ cursor: 'pointer' }} onClick={() => {
                  setEditing(null)
                  setForm({ name: t.name, model: t.model, system_prompt: t.system_prompt, skills: [], mcp_servers: [], sandbox_profile: t.sandbox_profile, network_access: t.network_access, read_only: t.read_only, max_turns: t.max_turns })
                  setShowForm(true)
                  setShowTemplates(false)
                }}>
                  <strong>{t.name}</strong>
                  <p style={{ color: '#7e8a9c', fontSize: '0.75rem', marginTop: '0.25rem' }}>{t.description}</p>
                  <div style={{ display: 'flex', gap: '0.25rem', marginTop: '0.25rem' }}>
                    <Tag type="gray" size="sm">{t.sandbox_profile}</Tag>
                    {t.network_access ? <Tag type="green" size="sm">network</Tag> : <Tag type="red" size="sm">no network</Tag>}
                    {t.read_only && <Tag type="warm-gray" size="sm">read-only</Tag>}
                    <Tag type="blue" size="sm">{t.max_turns} turns</Tag>
                  </div>
                </Tile>
              ))}
            </Stack>
            <Button kind="secondary" onClick={() => setShowTemplates(false)} style={{ marginTop: '1rem' }}>Cancel</Button>
          </div>
        </div>
      )}
    </div>
  )
}