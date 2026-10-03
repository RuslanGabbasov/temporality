import { useEffect, useState, useCallback } from 'react'
import {
  Button,
  TextInput,
  TextArea,
  InlineNotification,
  Loading,
  Tag,
  Tile,
  Grid,
  Column,
  Stack,
  Heading,
  Select,
  SelectItem,
  Checkbox,
  Toggle,
} from '@carbon/react'
import { Add, Edit, TrashCan } from '@carbon/icons-react'
import { workspaceApi, type MCPServer, type MCPToolInfo } from './workspaceApi'
import { useT } from './i18n'
import ListFilter, { matchesFilter } from './ListFilter'

function message(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }

type ServerForm = Partial<MCPServer> & { envText?: string; headersText?: string }

export default function Mcp() {
  const t = useT()
  const [servers, setServers] = useState<MCPServer[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [showForm, setShowForm] = useState(false)
  const [editing, setEditing] = useState<MCPServer | null>(null)
  const [form, setForm] = useState<ServerForm>({})
  const [discovered, setDiscovered] = useState<MCPToolInfo[]>([])
  const [discovering, setDiscovering] = useState(false)
  const [discoverError, setDiscoverError] = useState('')
  const [filter, setFilter] = useState('')

  const visibleServers = servers.filter((s) => matchesFilter(filter, s.name, s.id, s.command, s.url))

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const data = await workspaceApi.listMCPServers()
      setServers(data.servers ?? [])
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }, [])

  useEffect(() => { void load() }, [load])

  const startCreate = () => {
    setEditing(null)
    setDiscovered([])
    setDiscoverError('')
    setForm({ name: '', type: 'stdio', command: '', args: [], env: [], headers: {}, allowed_tools: [], approval_tools: [], enabled: true, envText: '', headersText: '' })
    setShowForm(true)
  }

  const startEdit = (s: MCPServer) => {
    setEditing(s)
    setDiscovered([])
    setDiscoverError('')
    setForm({ ...s, envText: (s.env ?? []).join('\n'), headersText: Object.entries(s.headers ?? {}).map(([k, v]) => `${k}: ${v}`).join('\n') })
    setShowForm(true)
  }

  const toPayload = (): Partial<MCPServer> & { name: string } => {
    const env = (form.envText ?? '').split('\n').map((l) => l.trim()).filter(Boolean)
    const headers: Record<string, string> = {}
    for (const line of (form.headersText ?? '').split('\n')) {
      const idx = line.indexOf(':')
      if (idx > 0) headers[line.slice(0, idx).trim()] = line.slice(idx + 1).trim()
    }
    const args = form.args ?? []
    return {
      id: form.id,
      name: form.name ?? '',
      type: form.type ?? 'stdio',
      url: form.url ?? '',
      command: form.command ?? '',
      args,
      env,
      headers,
      allowed_tools: form.allowed_tools ?? [],
      approval_tools: form.approval_tools ?? [],
      enabled: form.enabled ?? true,
    }
  }

  const discover = async () => {
    setDiscovering(true); setDiscoverError(''); setDiscovered([])
    try {
      const data = await workspaceApi.discoverMCPServer(toPayload())
      setDiscovered(data.tools ?? [])
    } catch (f) {
      setDiscoverError(message(f))
    } finally { setDiscovering(false) }
  }

  const save = async () => {
    if (!form.name?.trim()) return
    setLoading(true); setError('')
    try {
      if (editing) {
        await workspaceApi.updateMCPServer(editing.id, toPayload())
      } else {
        await workspaceApi.createMCPServer(toPayload())
      }
      setShowForm(false); setEditing(null)
      void load()
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }

  const remove = async (s: MCPServer) => {
    if (!confirm(t('mcp.delete_confirm', { name: s.name }) ?? `Delete MCP server "${s.name}"?`)) return
    setLoading(true); setError('')
    try {
      await workspaceApi.deleteMCPServer(s.id)
      void load()
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }

  const toggleTool = (toolName: string, checked: boolean) => {
    const allowed = form.allowed_tools ? [...form.allowed_tools] : []
    if (checked) allowed.push(toolName)
    else {
      const idx = allowed.indexOf(toolName)
      if (idx >= 0) allowed.splice(idx, 1)
      // approval-only tools must stay in the allowlist
      const approval = (form.approval_tools ?? []).filter((x) => x !== toolName)
      setForm({ ...form, allowed_tools: allowed, approval_tools: approval })
      return
    }
    setForm({ ...form, allowed_tools: allowed })
  }

  const toggleApproval = (toolName: string, checked: boolean) => {
    const allowed = form.allowed_tools ? [...form.allowed_tools] : []
    const approval = form.approval_tools ? [...form.approval_tools] : []
    if (checked) {
      if (!allowed.includes(toolName)) allowed.push(toolName)
      approval.push(toolName)
    } else {
      const idx = approval.indexOf(toolName)
      if (idx >= 0) approval.splice(idx, 1)
    }
    setForm({ ...form, allowed_tools: allowed, approval_tools: approval })
  }

  const type = form.type ?? 'stdio'

  return (
    <div style={{ padding: '1rem' }}>
      {error && <InlineNotification kind="error" title={t('action.error') ?? 'Error'} subtitle={error} onClose={() => setError('')} lowContrast style={{ marginBottom: '1rem' }} />}

      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '0.5rem' }}>
        <Heading>{t('mcp.title') ?? 'MCP Servers'}</Heading>
        <Button renderIcon={Add} onClick={startCreate}>{t('mcp.new_server') ?? 'New Server'}</Button>
      </div>
      <div style={{ position: 'sticky', top: '3rem', background: 'var(--tm-bg)', zIndex: 1, paddingBottom: '0.5rem', marginBottom: '0.5rem', maxWidth: '420px' }}>
        <ListFilter value={filter} onChange={setFilter} placeholder={t('common.filter_mcp') ?? 'Filter servers…'} />
      </div>

      {servers.length === 0 && !loading && (
        <Tile style={{ textAlign: 'center', padding: '3rem', color: 'var(--tm-text-3)' }}>
          <p>{t('mcp.no_servers') ?? 'No MCP servers yet.'}</p>
          <p style={{ fontSize: '0.8rem', marginTop: '0.5rem' }}>{t('mcp.no_servers_hint') ?? 'Connect an MCP server over stdio, SSE or HTTP to give agents access to external tools.'}</p>
        </Tile>
      )}

      <Grid>
        {visibleServers.map((s) => (
          <Column key={s.id} sm={4} md={4} lg={4}>
            <Tile style={{ marginBottom: '0.75rem' }}>
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start' }}>
                <div>
                  <strong>{s.name}</strong>
                  <p style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem', marginTop: '0.25rem', wordBreak: 'break-all' }}>
                    {s.type === 'stdio' ? (s.command + (s.args?.length ? ' ' + s.args.join(' ') : '')) : s.url}
                  </p>
                  <div style={{ display: 'flex', gap: '0.25rem', flexWrap: 'wrap', marginTop: '0.5rem', alignItems: 'center' }}>
                    <Tag type={s.type === 'stdio' ? 'gray' : 'blue'} size="sm">{s.type}</Tag>
                    {!s.enabled && <Tag type="red" size="sm">{t('mcp.disabled') ?? 'disabled'}</Tag>}
                    {s.allowed_tools?.length
                      ? <Tag type="green" size="sm">{t('mcp.tools_count', { count: String(s.allowed_tools.length) }) ?? `${s.allowed_tools.length} tools`}</Tag>
                      : <Tag type="green" size="sm">{t('mcp.all_tools') ?? 'all tools'}</Tag>}
                    {s.approval_tools?.length ? <Tag type="purple" size="sm">{t('mcp.approval_count', { count: String(s.approval_tools.length) }) ?? `${s.approval_tools.length} approval`}</Tag> : null}
                  </div>
                </div>
                <Stack orientation="horizontal" gap={1}>
                  <Button size="sm" kind="ghost" hasIconOnly renderIcon={Edit} iconDescription="Edit" onClick={() => startEdit(s)} />
                  <Button size="sm" kind="danger--ghost" hasIconOnly renderIcon={TrashCan} iconDescription="Delete" onClick={() => void remove(s)} />
                </Stack>
              </div>
            </Tile>
          </Column>
        ))}
      </Grid>

      {showForm && (
        <div className="modal-overlay">
          <div className="modal-panel" style={{ width: '640px' }}>
            <Heading>{editing ? (t('mcp.edit_server') ?? 'Edit MCP Server') : (t('mcp.new_server') ?? 'New MCP Server')}</Heading>
            <Stack gap={3}>
              <TextInput id="mcp-name" labelText={t('mcp.name_label') ?? 'Name'} value={form.name ?? ''} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, name: e.target.value })} placeholder="github" autoFocus />
              <Select id="mcp-type" labelText={t('mcp.type_label') ?? 'Transport'} value={type} onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setForm({ ...form, type: e.target.value as MCPServer['type'] })}>
                <SelectItem value="stdio" text={t('mcp.type_stdio') ?? 'stdio — local process'} />
                <SelectItem value="sse" text={t('mcp.type_sse') ?? 'SSE — server-sent events'} />
                <SelectItem value="http" text={t('mcp.type_http') ?? 'HTTP — streamable HTTP'} />
              </Select>

              {type === 'stdio' ? (
                <>
                  <TextInput id="mcp-command" labelText={t('mcp.command_label') ?? 'Command'} value={form.command ?? ''} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, command: e.target.value })} placeholder="npx -y @modelcontextprotocol/server-github" />
                  <TextInput id="mcp-args" labelText={t('mcp.args_label') ?? 'Arguments'} value={(form.args ?? []).join(' ')} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, args: e.target.value.split(/\s+/).filter(Boolean) })} placeholder="--port 3000" helperText={t('mcp.args_helper') ?? 'Space-separated'} />
                  <TextArea id="mcp-env" labelText={t('mcp.env_label') ?? 'Environment (one KEY=VALUE per line)'} value={form.envText ?? ''} onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => setForm({ ...form, envText: e.target.value })} rows={3} placeholder={'GITHUB_TOKEN=ghp_...\nNODE_ENV=production'} />
                </>
              ) : (
                <>
                  <TextInput id="mcp-url" labelText={t('mcp.url_label') ?? 'URL'} value={form.url ?? ''} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, url: e.target.value })} placeholder="https://mcp.example.com/sse" />
                  <TextArea id="mcp-headers" labelText={t('mcp.headers_label') ?? 'Headers (one "Name: value" per line)'} value={form.headersText ?? ''} onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => setForm({ ...form, headersText: e.target.value })} rows={3} placeholder={'Authorization: Bearer ...'} />
                </>
              )}

              <div style={{ display: 'flex', gap: '0.75rem', alignItems: 'center' }}>
                <Button size="sm" kind="secondary" onClick={() => void discover()} disabled={discovering}>
                  {discovering ? (t('mcp.discovering') ?? 'Connecting…') : (t('mcp.discover') ?? 'Discover tools')}
                </Button>
                {discovered.length > 0 && (
                  <span style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem' }}>
                    {t('mcp.discovered_count', { count: String(discovered.length) }) ?? `${discovered.length} tools found`}
                  </span>
                )}
              </div>

              {discoverError && <InlineNotification kind="error" title={t('action.error') ?? 'Error'} subtitle={discoverError} onClose={() => setDiscoverError('')} lowContrast />}

              {discovered.length > 0 && (
                <div>
                  <p className="cds--label" style={{ marginBottom: '0.25rem' }}>{t('mcp.tools_label') ?? 'Tools'}</p>
                  <p style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem', margin: '0 0 0.5rem' }}>
                    {t('mcp.tools_hint') ?? 'Unchecked tools are hidden from agents. "Approval" sends the tool through the human approval flow.'}
                  </p>
                  <div style={{ maxHeight: '14rem', overflow: 'auto', padding: '0.5rem 0.75rem', border: '1px solid var(--tm-border)', borderRadius: '6px' }}>
                    {discovered.map((tool) => {
                      const allowed = (form.allowed_tools ?? []).includes(tool.name)
                      return (
                        <div key={tool.name} style={{ padding: '0.25rem 0', borderBottom: '1px solid var(--tm-border)', minWidth: 0, overflowWrap: 'anywhere' }}>
                          <div style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', flexWrap: 'wrap' }}>
                            <Checkbox
                              id={`mcp-tool-${tool.name}`}
                              labelText={tool.name}
                              title={tool.description || tool.name}
                              checked={allowed}
                              onChange={(_: React.ChangeEvent<HTMLInputElement>, { checked }: { checked: boolean }) => toggleTool(tool.name, checked)}
                            />
                            {allowed && (
                              <Checkbox
                                id={`mcp-approval-${tool.name}`}
                                labelText={t('mcp.approval') ?? 'Approval'}
                                checked={(form.approval_tools ?? []).includes(tool.name)}
                                onChange={(_: React.ChangeEvent<HTMLInputElement>, { checked }: { checked: boolean }) => toggleApproval(tool.name, checked)}
                              />
                            )}
                          </div>
                          {tool.description && (
                            <div
                              style={{
                                color: 'var(--tm-text-3)',
                                fontSize: '0.7rem',
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
                    })}
                  </div>
                </div>
              )}

              <Toggle id="mcp-enabled" labelText={t('mcp.enabled') ?? 'Enabled'} toggled={form.enabled ?? true} onToggle={(checked: boolean) => setForm({ ...form, enabled: checked })} />

              <div className="form-actions">
                <Button kind="secondary" onClick={() => { setShowForm(false); setEditing(null) }}>{t('action.cancel') ?? 'Cancel'}</Button>
                <Button onClick={() => void save()}>{editing ? (t('action.save') ?? 'Save') : (t('action.create') ?? 'Create')}</Button>
              </div>
            </Stack>
          </div>
        </div>
      )}

      {loading && <Loading withOverlay={false} />}
    </div>
  )
}