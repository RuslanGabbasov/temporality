import { useEffect, useMemo, useState } from 'react'
import { Button, Heading, InlineNotification, Select, SelectItem, TextArea, TextInput, Toggle } from '@carbon/react'
import { workspaceApi, type FilesystemRules, type OrgUnit, type Policy } from './workspaceApi'
import { authHeaders } from './api'
import { flattenUnits } from './orgUnits'
import { useT } from './i18n'
import SuggestChips from './SuggestChips'
import { normalizeFSPath, pathVerdict, validateFilesystemMask, type FilesystemForm, type MaskErrorKind } from './fsGlob'

type Tab = 'general' | 'models' | 'mcp' | 'limits' | 'network' | 'files'

const TAB_KEYS: Tab[] = ['general', 'models', 'mcp', 'limits', 'network', 'files']

const emptyFS = (): FilesystemForm => ({ read: [], write: [], deny: [] })

const fsOf = (p: Policy | null, initialUnitId: string) => ({
  name: p?.name ?? '',
  org_unit_id: p?.org_unit_id ?? initialUnitId ?? '',
  enabled: p?.enabled ?? true,
  allowedModels: p?.allowed_models ?? [],
  allowedMcp: p?.allowed_mcp ?? [],
  egress: (p?.egress_allowlist ?? []).join('\n'),
  max_tokens: p?.max_tokens != null ? String(p.max_tokens) : '',
  max_budget_usd: p?.max_budget_usd != null ? String(p.max_budget_usd) : '',
  timeout_seconds: p?.timeout_seconds != null ? String(p.timeout_seconds) : '',
  max_concurrent_runs: p?.max_concurrent_runs != null ? String(p.max_concurrent_runs) : '',
  memory_budget_mb: p?.memory_budget_mb != null ? String(p.memory_budget_mb) : '',
  network_mode: p?.network_mode ?? '',
  sandbox_mode: p?.sandbox_mode ?? '',
  approval_mode: p?.approval_mode ?? '',
  intake_mode: p?.intake_mode ?? '',
  fs: { read: [...(p?.filesystem?.read ?? [])], write: [...(p?.filesystem?.write ?? [])], deny: [...(p?.filesystem?.deny ?? [])] },
})

const optionalNumber = (s: string): number | null => {
  const n = Number(s.trim())
  return s.trim() && Number.isFinite(n) ? n : null
}

/** One filesystem dimension (read / write / deny): masks as removable chips,
 *  an add-input validating each mask the way the kernel does, and preset
 *  buttons for the canonical workspace areas. */
function MaskList({ label, hint, dimension, masks, onChange, presets, touched }: {
  label: string
  hint: string
  dimension: 'read' | 'write' | 'deny'
  masks: string[]
  onChange: (next: string[]) => void
  presets: { mask: string; label: string }[]
  touched: boolean
}) {
  const t = useT()
  const [draft, setDraft] = useState('')
  const [error, setError] = useState<MaskErrorKind | null>(null)
  const color = dimension === 'deny' ? 'var(--tm-danger)' : dimension === 'write' ? 'var(--tm-teal)' : 'var(--tm-text-2)'

  const add = (mask: string) => {
    const kind = validateFilesystemMask(mask)
    if (kind) { setError(kind); return }
    setError(null)
    if (!masks.includes(mask)) onChange([...masks, mask])
    setDraft('')
  }

  return (
    <div style={{ border: '1px solid var(--tm-border)', borderRadius: '8px', padding: '0.6rem 0.7rem' }}>
      <div style={{ display: 'flex', alignItems: 'baseline', gap: '0.5rem' }}>
        <strong style={{ fontSize: '0.8rem', color }}>{label}</strong>
        <span style={{ fontSize: '0.68rem', color: 'var(--tm-text-3)' }}>{hint}</span>
      </div>
      {masks.length > 0 && (
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: '0.3rem', marginTop: '0.45rem' }}>
          {masks.map((mask) => (
            <span key={mask} style={{ display: 'inline-flex', alignItems: 'center', gap: '0.3rem', background: 'var(--tm-raised)', border: '1px solid var(--tm-border)', borderRadius: '6px', padding: '0.08rem 0.5rem', fontFamily: 'var(--tm-font-mono)', fontSize: '0.72rem' }}>
              {mask}
              <button
                type="button"
                aria-label="×"
                onClick={() => onChange(masks.filter((m) => m !== mask))}
                style={{ background: 'none', border: 0, cursor: 'pointer', color: 'var(--tm-text-3)', padding: 0, lineHeight: 1 }}
              >×</button>
            </span>
          ))}
        </div>
      )}
      <div style={{ display: 'flex', gap: '0.4rem', marginTop: '0.45rem', alignItems: 'flex-start' }}>
        <div style={{ flex: 1, minWidth: 0 }}>
          <TextInput
            id={`policy-fs-${dimension}`}
            size="sm"
            hideLabel
            labelText="mask"
            value={draft}
            invalid={touched && error !== null}
            placeholder={t('org.policy.fs_add_placeholder') ?? 'mask, e.g. projects/app/src/**'}
            onChange={(e: React.ChangeEvent<HTMLInputElement>) => { setDraft(e.target.value); setError(null) }}
            onKeyDown={(e: React.KeyboardEvent<HTMLInputElement>) => { if (e.key === 'Enter') { e.preventDefault(); add(draft) } }}
            onBlur={() => { if (draft.trim()) add(draft) }}
          />
        </div>
        <Button size="sm" kind="secondary" onClick={() => add(draft)} disabled={!draft.trim()}>{t('action.add') ?? 'Add'}</Button>
      </div>
      {touched && error !== null && (
        <div style={{ color: 'var(--tm-danger)', fontSize: '0.7rem', marginTop: '0.25rem' }}>
          {t(`org.policy.fs_err_${error}`) ?? 'Invalid mask'}
        </div>
      )}
      {presets.length > 0 && (
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: '0.3rem', alignItems: 'center', marginTop: '0.45rem' }}>
          <span style={{ fontSize: '0.68rem', color: 'var(--tm-text-3)' }}>{t('org.policy.fs_presets') ?? 'Presets:'}</span>
          {presets.filter((p) => !masks.includes(p.mask)).map((p) => (
            <button
              key={p.mask}
              type="button"
              title={p.mask}
              onClick={() => add(p.mask)}
              style={{ background: 'none', border: '1px dashed var(--tm-border)', borderRadius: '999px', cursor: 'pointer', color: 'var(--tm-text-2)', fontSize: '0.68rem', padding: '0.08rem 0.5rem' }}
            >+ {p.label}</button>
          ))}
        </div>
      )}
    </div>
  )
}

/**
 * Policy create/edit dialog (docs/org-structure.md §24, docs/filesystem-policy.md §3).
 * Tabbed: the old single-column form crammed a dozen unrelated dimensions into
 * one scroll and made you type model ids and masks from memory. Models and MCP
 * pick from the real catalog; the filesystem dimension gets a structured
 * editor (presets, per-mask validation, a path tester) instead of a raw mask
 * textarea. The enable switch is the same bottom-left toggle as the other
 * dialogs.
 */
export default function PolicyDialog({ editing, initialUnitId, units, onClose, onSaved }: {
  editing: Policy | null
  initialUnitId: string
  units: OrgUnit[]
  onClose: () => void
  onSaved: () => void
}) {
  const t = useT()
  const [tab, setTab] = useState<Tab>('general')
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const [form, setForm] = useState(() => fsOf(editing, initialUnitId))
  const [fsTouched, setFsTouched] = useState(false)
  // Catalogs for the suggestions (best-effort: a failed load leaves the
  // free-typing path, which always worked).
  const [models, setModels] = useState<string[]>([])
  // model id → provider names serving it: the dropdown annotates each
  // suggestion with its source, so «one provider's models» is visible at a
  // glance instead of a mystery (catalogs of OpenAI-compatible mirrors
  // overlap and dedupe to one list).
  const [modelSources, setModelSources] = useState<Map<string, string>>(new Map())
  const [mcpServers, setMcpServers] = useState<{ id: string; name: string }[]>([])
  const [projects, setProjects] = useState<{ id: string; name: string }[]>([])
  const [testerPath, setTesterPath] = useState('')
  const [testerProject, setTesterProject] = useState('')
  // Host memory-budget gate state (the third, unit-independent quota
  // dimension): loaded when the Limits tab opens — a run queued on
  // memory_budget while the unit quota reads ∞MB otherwise reads as phantom
  // queueing. Best-effort: older kernels without the endpoint show nothing.
  const [gateStats, setGateStats] = useState<{ used_mb: number; budget_mb: number; admitted: number; max_runs: number; holders: string[]; queued: string[] } | null>(null)
  useEffect(() => {
    if (tab !== 'limits' || gateStats) return
    let alive = true
    fetch('/kernel-api/v1/agent/gate', { headers: { Accept: 'application/json', ...authHeaders() } })
      .then((resp) => (resp.ok ? resp.json() : Promise.reject(new Error(String(resp.status)))))
      .then((data) => { if (alive) setGateStats(data) })
      .catch(() => { /* older kernel — the panel just omits the line */ })
    return () => { alive = false }
  }, [tab, gateStats])

  useEffect(() => {
    let alive = true
    void workspaceApi.listProviders().then((page) => {
      if (!alive) return
      const sources = new Map<string, string[]>()
      for (const provider of page.providers ?? []) {
        for (const model of provider.models ?? []) {
          const list = sources.get(model)
          if (list) list.push(provider.name)
          else sources.set(model, [provider.name])
        }
      }
      setModels([...sources.keys()].sort())
      setModelSources(new Map([...sources.entries()].map(([m, names]) => [m, names.join(', ')])))
    }).catch(() => { /* the free-typing path still works */ })
    void workspaceApi.listMCPServers().then((page) => {
      if (!alive) return
      setMcpServers((page.servers ?? []).map((s) => ({ id: s.id, name: s.name })))
    }).catch(() => { /* ditto */ })
    void workspaceApi.listProjects().then((page) => {
      if (!alive) return
      setProjects((page.projects ?? []).map((p) => ({ id: p.id, name: p.name })))
    }).catch(() => { /* ditto */ })
    return () => { alive = false }
  }, [])

  const flat = useMemo(() => flattenUnits(units), [units])
  const mcpLabels = useMemo(() => new Map(mcpServers.map((s) => [s.id, s.name])), [mcpServers])
  const projectPresets = useMemo(() => {
    const id = testerProject
    if (!id) return []
    return [
      { mask: `projects/${id}/**`, label: t('org.policy.fs_project_all') ?? 'whole project' },
      { mask: `projects/${id}/src/**`, label: 'src/**' },
      { mask: `projects/${id}/tests/**`, label: 'tests/**' },
      { mask: `projects/${id}/docs/**`, label: 'docs/**' },
    ]
  }, [testerProject, t])
  const projectDenyPresets = useMemo(() => (
    testerProject ? [{ mask: `projects/${testerProject}/secrets/**`, label: 'secrets/**' }] : []
  ), [testerProject])

  const setFS = (dimension: 'read' | 'write' | 'deny', masks: string[]) => {
    setForm((prev) => ({ ...prev, fs: { ...prev.fs, [dimension]: masks } }))
  }

  const fsMaskCount = form.fs.read.length + form.fs.write.length + form.fs.deny.length
  const verdict = useMemo(() => {
    const clean = normalizeFSPath(testerPath)
    if (!clean) return null
    return { path: clean, v: pathVerdict(form.fs, clean) }
  }, [testerPath, form.fs])

  const fsValid = form.fs.read.every((m) => validateFilesystemMask(m) === null)
    && form.fs.write.every((m) => validateFilesystemMask(m) === null)
    && form.fs.deny.every((m) => validateFilesystemMask(m) === null)

  const save = async () => {
    if (!form.name.trim() || saving) return
    if (!fsValid) { setFsTouched(true); setTab('files'); return }
    setSaving(true)
    setError('')
    try {
      // The store replaces the whole row (workspace/policy.go UpdatePolicy):
      // every dimension is sent as-is, empty lists normalize to nil (§3.5).
      const filesystem: FilesystemRules = { read: form.fs.read, write: form.fs.write, deny: form.fs.deny }
      const payload = {
        name: form.name.trim(),
        org_unit_id: form.org_unit_id || '',
        allowed_models: form.allowedModels,
        allowed_mcp: form.allowedMcp,
        egress_allowlist: form.egress.split('\n').map((line) => line.trim()).filter(Boolean),
        max_tokens: optionalNumber(form.max_tokens),
        max_budget_usd: optionalNumber(form.max_budget_usd),
        timeout_seconds: optionalNumber(form.timeout_seconds),
        max_concurrent_runs: optionalNumber(form.max_concurrent_runs) ?? 0,
        memory_budget_mb: optionalNumber(form.memory_budget_mb) ?? 0,
        network_mode: form.network_mode,
        sandbox_mode: form.sandbox_mode,
        approval_mode: form.approval_mode,
        intake_mode: form.intake_mode,
        filesystem,
        enabled: form.enabled,
      }
      if (editing) await workspaceApi.updatePolicy(editing.id, payload)
      else await workspaceApi.createPolicy(payload)
      onSaved()
      onClose()
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : 'Request failed')
    } finally {
      setSaving(false)
    }
  }

  const tabs: { key: Tab; label: string; count?: number }[] = [
    { key: 'general', label: t('org.policy.tab_general') ?? 'General' },
    { key: 'models', label: t('org.policy.tab_models') ?? 'Models', count: form.allowedModels.length },
    { key: 'mcp', label: t('org.policy.tab_mcp') ?? 'MCP', count: form.allowedMcp.length },
    { key: 'limits', label: t('org.policy.tab_limits') ?? 'Limits & modes' },
    { key: 'network', label: t('org.policy.tab_network') ?? 'Network' },
    { key: 'files', label: t('org.policy.tab_files') ?? 'Files', count: fsMaskCount },
  ]

  return (
    <div style={{ width: 'min(760px, calc(100vw - 3rem))' }}>
      <Heading style={{ fontSize: '1.1rem', marginBottom: '0.5rem' }}>
        {editing ? (t('org.policy_edit') ?? 'Edit policy') : (t('org.policy_create') ?? 'Create policy')}
      </Heading>
      {error && (
        <InlineNotification kind="error" title={t('common.error') ?? 'Error'} subtitle={error} lowContrast onClose={() => setError('')} style={{ marginBottom: '0.5rem' }} />
      )}

      <div className="skill-tabs" style={{ margin: '0 0 0.75rem', padding: 0 }}>
        {tabs.map((item) => (
          <button key={item.key} type="button" className={`skill-tab ${tab === item.key ? 'active' : ''}`} onClick={() => setTab(item.key)}>
            {item.label}
            {item.count != null && item.count > 0 && <span className="skill-tab-count">{item.count}</span>}
          </button>
        ))}
      </div>

      <div style={{ minHeight: '16rem' }}>
        {tab === 'general' && (
          <div style={{ display: 'grid', gap: '0.75rem' }}>
            <TextInput
              id="policy-name"
              labelText={t('org.name_label') ?? 'Name'}
              value={form.name}
              onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, name: e.target.value })}
              placeholder="Tier-1 production limits"
              autoFocus
            />
            <Select
              id="policy-scope"
              labelText={t('org.policy_scope_label') ?? 'Applies to'}
              value={form.org_unit_id}
              onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setForm({ ...form, org_unit_id: e.target.value })}
            >
              <SelectItem value="" text={t('org.policy_scope_installation') ?? 'Whole installation'} />
              {flat.map(({ unit, depth }) => (
                <SelectItem key={unit.id} value={unit.id} text={`${'　'.repeat(depth)}${unit.name}`} />
              ))}
            </Select>
            <p style={{ fontSize: '0.72rem', color: 'var(--tm-text-3)', margin: 0 }}>
              {t('org.policy_general_hint') ?? 'Policies of a unit and its ancestors merge restrictively for every run in scope; an unset dimension restricts nothing.'}
            </p>
          </div>
        )}

        {tab === 'models' && (
          <div style={{ display: 'grid', gap: '0.5rem' }}>
            <span style={{ fontSize: '0.78rem', color: 'var(--tm-text-2)' }}>{t('org.policy_models_label') ?? 'Allowed models'}</span>
            <SuggestChips
              id="policy-models"
              value={form.allowedModels}
              onChange={(next) => setForm({ ...form, allowedModels: next })}
              suggestions={models}
              suggestionLabels={modelSources}
              annotateChips={false}
              placeholder={t('org.policy.chip_placeholder') ?? 'Type or pick, Enter to add…'}
            />
            <p style={{ fontSize: '0.72rem', color: 'var(--tm-text-3)', margin: 0 }}>
              {t('org.policy_models_hint') ?? 'Empty — any model. Suggestions come from the provider catalogs; free values are allowed.'}
            </p>
          </div>
        )}

        {tab === 'mcp' && (
          <div style={{ display: 'grid', gap: '0.5rem' }}>
            <span style={{ fontSize: '0.78rem', color: 'var(--tm-text-2)' }}>{t('org.policy_mcp_label') ?? 'Allowed MCP servers'}</span>
            <SuggestChips
              id="policy-mcp"
              value={form.allowedMcp}
              onChange={(next) => setForm({ ...form, allowedMcp: next })}
              suggestions={mcpServers.map((s) => s.id)}
              suggestionLabels={mcpLabels}
              placeholder={t('org.policy.chip_placeholder') ?? 'Type or pick, Enter to add…'}
            />
            <p style={{ fontSize: '0.72rem', color: 'var(--tm-text-3)', margin: 0 }}>
              {t('org.policy_mcp_hint') ?? 'Empty — any server. The policy stores server ids; the chip shows the human name next to it.'}
            </p>
          </div>
        )}

        {tab === 'limits' && (
          <div style={{ display: 'grid', gap: '0.75rem' }}>
            <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr 1fr', gap: '0.5rem' }}>
              <TextInput id="policy-tokens" labelText={t('org.policy_max_tokens_label') ?? 'Max tokens per run'} value={form.max_tokens} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, max_tokens: e.target.value })} placeholder="200000" />
              <TextInput id="policy-budget" labelText={t('org.policy_budget_label') ?? 'Max cost, USD'} value={form.max_budget_usd} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, max_budget_usd: e.target.value })} placeholder="5" />
              <TextInput id="policy-timeout" labelText={t('org.policy_timeout_label') ?? 'Max time, s'} value={form.timeout_seconds} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, timeout_seconds: e.target.value })} placeholder="1800" />
            </div>
            <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '0.5rem' }}>
              <TextInput id="policy-max-runs" labelText={t('org.policy_max_runs_label') ?? 'Max concurrent runs (unit)'} value={form.max_concurrent_runs} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, max_concurrent_runs: e.target.value })} placeholder="10" />
              <TextInput id="policy-mem-budget" labelText={t('org.policy_mem_budget_label') ?? 'Memory budget, MB (unit)'} value={form.memory_budget_mb} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, memory_budget_mb: e.target.value })} placeholder="4096" />
            </div>
            <p style={{ fontSize: '0.72rem', color: 'var(--tm-text-3)', margin: 0 }}>
              {t('org.policy_quota_hint') ?? 'Memory quota for this unit: when the running requests sum up to the memory budget, new ones wait in a queue and start automatically as slots free up. 0 or empty = no restriction.'}
            </p>
            {/* The HOST budget — a third dimension beside the unit quotas: the
                whole-server sandbox memory ledger. When it is full, runs queue
                with reason memory_budget EVEN IF every unit quota is ∞ — the
                line makes that visible right where the quotas are edited. */}
            {gateStats && (
              <div style={{ border: '1px solid var(--tm-border)', borderRadius: '6px', padding: '0.5rem 0.75rem', fontSize: '0.75rem', display: 'flex', flexDirection: 'column', gap: '0.2rem' }}>
                <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'baseline', flexWrap: 'wrap' }}>
                  <strong style={{ color: 'var(--tm-text)' }}>{t('org.gate_host_title') ?? 'Server sandbox memory'}</strong>
                  <span style={{ color: gateStats.used_mb >= gateStats.budget_mb ? 'var(--tm-danger)' : 'var(--tm-text-2)' }}>
                    {(gateStats.used_mb / 1024).toFixed(1)} / {(gateStats.budget_mb / 1024).toFixed(1)} GB
                  </span>
                  <span style={{ color: 'var(--tm-text-3)' }}>
                    · {t('org.gate_runs', { admitted: String(gateStats.admitted) }) ?? `${gateStats.admitted} runs`}
                    {gateStats.queued?.length ? t('org.gate_queued', { count: String(gateStats.queued.length) }) ?? `, ${gateStats.queued.length} queued` : ''}
                  </span>
                </div>
                {gateStats.holders?.length ? (
                  <div style={{ color: 'var(--tm-text-3)', fontSize: '0.68rem', display: 'flex', flexDirection: 'column', gap: '0.1rem', maxHeight: '7rem', overflowY: 'auto' }}>
                    {gateStats.holders.slice(0, 12).map((h) => {
                      const idx = h.lastIndexOf(':')
                      const id = idx > 0 ? h.slice(0, idx) : h
                      const mb = idx > 0 ? h.slice(idx + 1) : ''
                      return <span key={h} style={{ whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{mb ? `${(Number(mb.replace('MB', '')) / 1024).toFixed(1)} GB` : ''} — {id.length > 46 ? `…${id.slice(-46)}` : id}</span>
                    })}
                  </div>
                ) : null}
              </div>
            )}
            <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '0.5rem' }}>
              <Select id="policy-sandbox" labelText={t('org.policy_sandbox_label') ?? 'Sandbox'} value={form.sandbox_mode} onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setForm({ ...form, sandbox_mode: e.target.value })}>
                <SelectItem value="" text={t('org.policy_sandbox.default') ?? 'standard'} />
                <SelectItem value="read_only" text={t('org.policy_sandbox.read_only') ?? 'read-only'} />
              </Select>
              <Select id="policy-approval" labelText={t('org.policy_approval_label') ?? 'Consequential tools'} value={form.approval_mode} onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setForm({ ...form, approval_mode: e.target.value })}>
                <SelectItem value="" text={t('org.policy_approval.default') ?? 'no approval'} />
                <SelectItem value="tools" text={t('org.policy_approval.tools') ?? 'require approval'} />
              </Select>
              <Select id="policy-intake" labelText={t('org.policy_intake_label') ?? 'Channel intake'} value={form.intake_mode} onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setForm({ ...form, intake_mode: e.target.value })}>
                <SelectItem value="" text={t('org.policy_intake.default') ?? 'governed by projects'} />
                <SelectItem value="deny" text={t('org.policy_intake.deny') ?? 'denied'} />
              </Select>
            </div>
          </div>
        )}

        {tab === 'network' && (
          <div style={{ display: 'grid', gap: '0.75rem' }}>
            <Select id="policy-network" labelText={t('org.policy_network_label') ?? 'Network'} value={form.network_mode} onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setForm({ ...form, network_mode: e.target.value })}>
              <SelectItem value="" text={t('org.policy_network.default') ?? 'not restricted'} />
              <SelectItem value="deny" text={t('org.policy_network.deny') ?? 'denied'} />
            </Select>
            <TextArea
              id="policy-egress"
              labelText={t('org.policy_egress_label') ?? 'Egress baseline (centralized)'}
              value={form.egress}
              onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => setForm({ ...form, egress: e.target.value })}
              rows={5}
              placeholder={t('org.policy_egress_placeholder') ?? 'One entry per line: a domain, a glob (*.example.com), a CIDR. Every sandboxed agent in scope gets these for free.'}
            />
          </div>
        )}

        {tab === 'files' && (
          <div style={{ display: 'grid', gap: '0.6rem' }}>
            <p style={{ fontSize: '0.72rem', color: 'var(--tm-text-3)', margin: 0 }}>
              {t('org.policy.fs_hint') ?? 'Workspace-relative glob masks (paths as the file tools see them, without the /workspace prefix): ** spans segments, * and ? stay in one. An unset dimension restricts nothing; deny always wins; write never widens beyond read.'}
            </p>
            <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'center', flexWrap: 'wrap' }}>
              <span style={{ fontSize: '0.72rem', color: 'var(--tm-text-2)' }}>{t('org.policy.fs_project_label') ?? 'Quick rules for project'}:</span>
              {/* Carbon's .cds--select wrapper is width:100% and ignores the
                  style prop — constrain it with a sized container. */}
              <div style={{ width: '18rem', maxWidth: '100%' }}>
                <Select
                  id="policy-fs-project"
                  size="sm"
                  hideLabel
                  labelText="project"
                  value={testerProject}
                  onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setTesterProject(e.target.value)}
                >
                  <SelectItem value="" text="—" />
                  {projects.map((p) => <SelectItem key={p.id} value={p.id} text={p.name || p.id} />)}
                </Select>
              </div>
            </div>
            <MaskList
              label={t('org.policy.fs_read') ?? 'Read'}
              hint={t('org.policy.fs_read_hint') ?? 'empty = read everything'}
              dimension="read"
              masks={form.fs.read}
              onChange={(next) => setFS('read', next)}
              touched={fsTouched}
              presets={[
                { mask: '**', label: t('org.policy.fs_preset_all') ?? 'whole workspace' },
                { mask: 'projects/**', label: 'projects/**' },
                ...projectPresets,
              ]}
            />
            <MaskList
              label={t('org.policy.fs_write') ?? 'Write'}
              hint={t('org.policy.fs_write_hint') ?? 'empty = follows read (write ⊆ read)'}
              dimension="write"
              masks={form.fs.write}
              onChange={(next) => setFS('write', next)}
              touched={fsTouched}
              presets={projectPresets}
            />
            <MaskList
              label={t('org.policy.fs_deny') ?? 'Deny'}
              hint={t('org.policy.fs_deny_hint') ?? 'absolute, inherited by children'}
              dimension="deny"
              masks={form.fs.deny}
              onChange={(next) => setFS('deny', next)}
              touched={fsTouched}
              presets={[
                { mask: 'secrets/**', label: 'secrets/**' },
                { mask: '**/.env', label: '.env files' },
                { mask: '**/*.pem', label: '*.pem keys' },
                ...projectDenyPresets,
              ]}
            />
            <div style={{ border: '1px dashed var(--tm-border)', borderRadius: '8px', padding: '0.6rem 0.7rem' }}>
              <div style={{ fontSize: '0.78rem', color: 'var(--tm-text-2)', marginBottom: '0.35rem' }}>{t('org.policy.fs_tester_title') ?? 'Check a path'}</div>
              <TextInput
                id="policy-fs-tester"
                size="sm"
                hideLabel
                labelText="path"
                value={testerPath}
                placeholder={t('org.policy.fs_tester_placeholder') ?? '/workspace/projects/app/src/main.go'}
                onChange={(e: React.ChangeEvent<HTMLInputElement>) => setTesterPath(e.target.value)}
              />
              {verdict && (
                <div style={{ display: 'flex', gap: '0.4rem', marginTop: '0.45rem', flexWrap: 'wrap', alignItems: 'center' }}>
                  <code style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)' }}>{verdict.path}</code>
                  <span style={{ fontSize: '0.72rem', padding: '0.05rem 0.5rem', borderRadius: '999px', border: `1px solid ${verdict.v.denied ? 'var(--tm-danger)' : verdict.v.readable ? 'var(--tm-success)' : 'var(--tm-border)'}`, color: verdict.v.denied ? 'var(--tm-danger)' : verdict.v.readable ? 'var(--tm-success)' : 'var(--tm-text-3)' }}>
                    {verdict.v.denied ? `🚫 ${t('org.policy.fs_verdict_denied') ?? 'denied'}` : verdict.v.readable ? `✓ ${t('org.policy.fs_verdict_read') ?? 'readable'}` : `✗ ${t('org.policy.fs_verdict_read_no') ?? 'not readable'}`}
                  </span>
                  <span style={{ fontSize: '0.72rem', padding: '0.05rem 0.5rem', borderRadius: '999px', border: `1px solid ${verdict.v.writable ? 'var(--tm-success)' : 'var(--tm-border)'}`, color: verdict.v.writable ? 'var(--tm-success)' : 'var(--tm-text-3)' }}>
                    {verdict.v.writable ? `✓ ${t('org.policy.fs_verdict_write') ?? 'writable'}` : `✗ ${t('org.policy.fs_verdict_write_no') ?? 'not writable'}`}
                  </span>
                </div>
              )}
              <p style={{ fontSize: '0.66rem', color: 'var(--tm-text-3)', margin: '0.35rem 0 0' }}>
                {t('org.policy.fs_tester_note') ?? 'This row only — the effective policy also inherits restrictions from ancestors.'}
              </p>
            </div>
          </div>
        )}
      </div>

      <div className="form-actions" style={{ marginTop: '0.75rem' }}>
        <div style={{ marginRight: 'auto', display: 'flex', alignItems: 'center' }} title={t('org.policy_enabled_hint') ?? 'Disabled policies do not constrain runs'}>
          <Toggle
            id="policy-enabled"
            labelText=""
            hideLabel
            aria-label={t('org.policy_enabled_label') ?? 'Policy enabled'}
            labelA={t('common.off') ?? 'Off'}
            labelB={t('common.on') ?? 'On'}
            toggled={form.enabled}
            onToggle={(v: boolean) => setForm({ ...form, enabled: v })}
            size="sm"
          />
        </div>
        <Button kind="secondary" onClick={onClose}>{t('action.cancel') ?? 'Cancel'}</Button>
        <Button onClick={() => void save()} disabled={!form.name.trim() || saving}>{editing ? (t('action.save') ?? 'Save') : (t('action.create') ?? 'Create')}</Button>
      </div>
    </div>
  )
}
