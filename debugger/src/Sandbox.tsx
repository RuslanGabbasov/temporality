import { useEffect, useState, useCallback, useRef } from 'react'
import {
  Button,
  Checkbox,
  TextInput,
  TextArea,
  InlineNotification,
  RadioButton,
  RadioButtonGroup,
  Select,
  SelectItem,
  Tag,
  Tile,
  Stack,
  Heading,
  Toggle,
} from '@carbon/react'
import PageLoading from './PageLoading'
import { Add, TrashCan, Renew, Edit, Download, MagicWand } from '@carbon/icons-react'
import { workspaceApi, type SandboxProfile } from './workspaceApi'
import {
  emptySandboxProfileForm as emptyForm,
  prefilledDockerfile,
  buildSandboxProfilePayload,
  recipeModeFor,
  sandboxSaveBlocked,
  type DockerfileCheck,
  type SandboxProfileForm as ProfileForm,
  type SandboxRecipeMode,
} from './sandboxProfileForm'
import { observationApi } from './observationApi'
import { useT } from './i18n'
import ListFilter, { matchesFilter } from './ListFilter'
import AppModal from './Modal'
import { useConfirm } from './ConfirmDialog'

function message(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }

// Sandbox profile registry (docs/sandbox-profiles.md §8): the catalog of
// execution environments, builtin recipes with a Build action and custom
// profiles pointing at external digest-pinned images.
export default function Sandbox({ project }: { project?: string }) {
  const t = useT()
  const { confirmDialog, dialog: confirmDialogNode } = useConfirm()
  const [profiles, setProfiles] = useState<SandboxProfile[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [showForm, setShowForm] = useState(false)
  const [form, setForm] = useState<ProfileForm>(emptyForm)
  // The edit dialog's copy of the uniform switch: applied through the
  // /enabled endpoint after the save (the profile PUT never carries it).
  const [formEnabled, setFormEnabled] = useState(true)
  const [filter, setFilter] = useState('')
  const [building, setBuilding] = useState('')
  const [editing, setEditing] = useState<SandboxProfile | null>(null)
  const [wizardOpen, setWizardOpen] = useState(false)
  const [wizardText, setWizardText] = useState('')
  const [wizardBusy, setWizardBusy] = useState(false)
  const [wizardError, setWizardError] = useState('')
  const [draftRationale, setDraftRationale] = useState('')
  // Layer-builder preview (§5.1 phase 2): the composed Dockerfile for the
  // current base+packages, refreshed as the operator edits.
  const [recipePreview, setRecipePreview] = useState('')
  // Recipe source of the open form (D6): base[+packages] vs the operator's
  // own Dockerfile text; exactly one is ever sent (D2.1 XOR).
  const [recipeMode, setRecipeMode] = useState<SandboxRecipeMode>('base')
  // Live check of the operator text against the preview endpoint (spec §4):
  // 'valid' is the only state the save button accepts (AC-8).
  const [dockerfileCheck, setDockerfileCheck] = useState<DockerfileCheck>('empty')
  const [dockerfileError, setDockerfileError] = useState('')
  // Monotonic sequence so a slow in-flight check never overwrites the state
  // of newer text.
  const checkSeq = useRef(0)
  // Build history (§5.1 phase 2): journal events per opened profile.
  const [historyFor, setHistoryFor] = useState('')
  const [history, setHistory] = useState<{ at: string; image: string; pulled?: boolean }[]>([])
  const [pullRef, setPullRef] = useState<{ id: string; reference: string } | null>(null)

  const visible = profiles.filter((p) => matchesFilter(filter, p.name, p.id, p.description, ...(p.toolchains ?? [])))

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const data = await workspaceApi.listSandboxProfiles()
      setProfiles(data.profiles ?? [])
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }, [])

  useEffect(() => { void load() }, [load])

  // The uniform switch (the trigger pattern): a disabled profile fails runs
  // that reference it with an explicit error. Optimistic flip, rollback.
  const toggleSandboxEnabled = async (p: SandboxProfile) => {
    const next = p.enabled === false
    setProfiles((prev) => prev.map((x) => (x.id === p.id ? { ...x, enabled: next } : x)))
    try {
      const updated = await workspaceApi.setSandboxProfileEnabled(p.id, next)
      setProfiles((prev) => prev.map((x) => (x.id === p.id ? updated : x)))
    } catch (f) {
      setProfiles((prev) => prev.map((x) => (x.id === p.id ? { ...x, enabled: p.enabled } : x)))
      setError(message(f))
    }
  }

  const build = async (p: SandboxProfile) => {
    setBuilding(p.id); setError(''); setNotice('')
    try {
      await workspaceApi.buildSandboxProfile(p.id)
      setNotice(t('sandbox.built_notice', { name: p.name }))
      await load()
    } catch (f) { setError(message(f)) }
    finally { setBuilding('') }
  }

  // Pull an external image and pin the resolved digest into the profile.
  const pull = async (p: SandboxProfile, reference: string) => {
    setBuilding(p.id); setError(''); setNotice('')
    try {
      const updated = await workspaceApi.pullSandboxProfile(p.id, reference)
      setProfiles((prev) => prev.map((x) => (x.id === p.id ? { ...x, image: updated.image, available: false } : x)))
      setNotice(t('sandbox.pulled_notice', { name: p.name }))
    } catch (f) { setError(message(f)) }
    finally { setBuilding('') }
  }

  // Build history: the journal keeps every sandbox.profile.built for the
  // profile; the newest ones render in the tile.
  const openHistory = async (p: SandboxProfile) => {
    if (historyFor === p.id) { setHistoryFor(''); return }
    setHistoryFor(p.id); setHistory([])
    try {
      const page = await observationApi.events(undefined, undefined, undefined, undefined, { type: 'sandbox.profile.built', limit: 100 })
      const rows = (page.events ?? [])
        .filter((e) => String(e.data?.profile ?? '') === p.id)
        .slice(0, 8)
        .map((e) => ({ at: e.occurred_at, image: String(e.data?.image ?? ''), pulled: Boolean(e.data?.pulled) }))
      setHistory(rows)
    } catch { /* history is best-effort */ }
  }

  // Compose the Dockerfile preview whenever the recipe inputs change.
  useEffect(() => {
    if (showForm && !editing?.builtin && form.base) {
      const packages = form.packages.split(',').map((s) => s.trim()).filter(Boolean)
      const timer = setTimeout(() => {
        workspaceApi.previewSandboxRecipe(form.base, packages)
          .then((r) => setRecipePreview(r.dockerfile))
          .catch(() => setRecipePreview(''))
      }, 300)
      return () => clearTimeout(timer)
    }
    setRecipePreview('')
    return undefined
  }, [showForm, editing, form.base, form.packages])

  // Live validation of the operator's Dockerfile: the text is checked through
  // the same preview endpoint a build would use (spec §4) — 422 carries the
  // line-numbered violations, which are shown verbatim and block saving.
  useEffect(() => {
    const seq = ++checkSeq.current
    if (!showForm || recipeMode !== 'dockerfile' || editing?.builtin) {
      setDockerfileError('')
      return undefined
    }
    const text = form.dockerfile
    if (!text.trim()) {
      setDockerfileCheck('empty')
      setDockerfileError('')
      return undefined
    }
    setDockerfileCheck('checking')
    const timer = setTimeout(() => {
      workspaceApi.previewSandboxDockerfile(text)
        .then(() => {
          if (checkSeq.current === seq) { setDockerfileCheck('valid'); setDockerfileError('') }
        })
        .catch((f) => {
          if (checkSeq.current === seq) { setDockerfileCheck('invalid'); setDockerfileError(message(f)) }
        })
    }, 300)
    return () => clearTimeout(timer)
  }, [showForm, recipeMode, editing, form.dockerfile])

  // Mode switch (D6): entering "own Dockerfile" prefills the textarea with
  // the currently composed base+packages recipe — typed operator text is
  // never overwritten.
  const switchRecipeMode = (mode: SandboxRecipeMode) => {
    if (mode === recipeMode) return
    setRecipeMode(mode)
    setDockerfileError('')
    if (mode !== 'dockerfile') return
    const composed = prefilledDockerfile(form.dockerfile, recipePreview)
    if (composed !== form.dockerfile) {
      setForm((f) => ({ ...f, dockerfile: composed }))
      return
    }
    if (!composed.trim() && form.base) {
      // The debounced base preview may not have landed yet — compose now so
      // the textarea still opens with the current recipe.
      const packages = form.packages.split(',').map((s) => s.trim()).filter(Boolean)
      workspaceApi.previewSandboxRecipe(form.base, packages)
        .then((r) => setForm((f) => (f.dockerfile.trim() ? f : { ...f, dockerfile: r.dockerfile })))
        .catch(() => { /* no composed text available — start empty */ })
    }
  }

  const remove = async (p: SandboxProfile) => {
    const ok = await confirmDialog({
      title: t('sandbox.delete_confirm', { name: p.name }),
      body: t('action.delete_warning'),
      danger: true,
      confirmLabel: t('action.delete'),
    })
    if (!ok) return
    setError('')
    try {
      await workspaceApi.deleteSandboxProfile(p.id)
      await load()
    } catch (f) { setError(message(f)) }
  }

  const startEdit = (p: SandboxProfile) => {
    setEditing(p)
    setFormEnabled(p.enabled !== false)
    const mode = recipeModeFor(p)
    setRecipeMode(mode)
    setDockerfileError('')
    setDockerfileCheck(p.dockerfile_override?.trim() ? 'checking' : 'empty')
    setForm({
      id: p.id,
      name: p.name,
      description: p.description ?? '',
      image: p.image ?? '',
      base: p.base ?? '',
      packages: (p.packages ?? []).join(', '),
      dockerfile: p.dockerfile_override ?? '',
      toolchains: (p.toolchains ?? []).join(', '),
      memory: p.memory ?? '',
      cpus: p.cpus ?? '',
      scratch_size: p.scratch_size ?? '',
      tmp_size: p.tmp_size ?? '',
      cache_env: (p.cache_env ?? []).join('\n'),
      docker_socket: Boolean(p.docker_socket),
    })
    setShowForm(true)
  }

  const saveCustom = async () => {
    if (sandboxSaveBlocked(form, recipeMode, dockerfileCheck, editing)) return
    setError('')
    const payload = {
      ...buildSandboxProfilePayload(form, recipeMode),
      id: editing ? editing.id : form.id.trim(),
      name: form.name.trim(),
    }
    try {
      if (editing) {
        await workspaceApi.updateSandboxProfile(editing.id, payload)
        if (formEnabled !== (editing.enabled !== false)) {
          await workspaceApi.setSandboxProfileEnabled(editing.id, formEnabled)
        }
        setEditing(null)
      } else {
        const created = await workspaceApi.createSandboxProfile(payload)
        if (!formEnabled) {
          await workspaceApi.setSandboxProfileEnabled(created.id, false)
        }
      }
      setFormEnabled(true)
      setShowForm(false)
      setForm(emptyForm)
      setRecipeMode('base')
      setDockerfileCheck('empty')
      setDockerfileError('')
      setDraftRationale('')
      await load()
    } catch (f) { setError(message(f)) }
  }

  // The wizard turns a plain-language task description into a profile draft
  // via the supervisor-scope builder endpoint; the draft lands in the regular
  // form for review and editing.
  const runWizard = async () => {
    setWizardBusy(true); setWizardError('')
    try {
      const draft = await workspaceApi.sandboxBuilder(wizardText)
      // The builder never proposes an override (D2.2) — the draft always
      // lands in base mode for the operator to edit.
      setRecipeMode('base')
      setDockerfileCheck('empty')
      setDockerfileError('')
      setForm({
        id: draft.id,
        name: draft.name,
        description: draft.description ?? '',
        image: '',
        base: draft.base ?? '',
        packages: (draft.packages ?? []).join(', '),
        dockerfile: '',
        toolchains: (draft.toolchains ?? []).join(', '),
        memory: draft.memory ?? '',
        cpus: draft.cpus ?? '',
        scratch_size: draft.scratch_size ?? '',
        tmp_size: draft.tmp_size ?? '',
        cache_env: '',
        // The builder never proposes the socket passthrough: it is an
        // operator-only decision made in the profile form.
        docker_socket: false,
      })
      setDraftRationale(draft.rationale ?? '')
      setEditing(null)
      setWizardOpen(false)
      setShowForm(true)
    } catch (f) { setWizardError(message(f)) }
    finally { setWizardBusy(false) }
  }

  return (
    <div style={{ padding: '1rem' }}>
      {confirmDialogNode}
      {error && <InlineNotification kind="error" lowContrast title={t('action.error')} subtitle={error} onCloseButtonClick={() => setError('')} style={{ marginBottom: '1rem' }} />}
      {notice && <InlineNotification kind="success" lowContrast title="" subtitle={notice} onCloseButtonClick={() => setNotice('')} style={{ marginBottom: '1rem' }} />}
      {building && <InlineNotification kind="info" lowContrast hideCloseButton title={t('sandbox.building')} subtitle={t('sandbox.building_hint')} style={{ marginBottom: '1rem' }} />}

      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '0.5rem' }}>
        <div>
          <Heading>{t('sandbox.title')}</Heading>
          <p style={{ color: 'var(--tm-text-3)', fontSize: '0.8rem', marginTop: '0.25rem' }}>{t('sandbox.subtitle')}</p>
        </div>
        <Button size="sm" kind="secondary" renderIcon={MagicWand} onClick={() => { setWizardText(''); setWizardError(''); setWizardOpen(true) }}>{t('sandbox.wizard_button') ?? 'From description'}</Button>
        <Button size="sm" onClick={() => { setRecipeMode('base'); setDockerfileCheck('empty'); setDockerfileError(''); setShowForm(true) }} renderIcon={Add}>{t('sandbox.add_custom')}</Button>
      </div>
      <div style={{ position: 'sticky', top: '3rem', background: 'var(--tm-bg)', zIndex: 1, paddingBottom: '0.5rem', marginBottom: '0.5rem', maxWidth: '420px' }}>
        <ListFilter value={filter} onChange={setFilter} placeholder={t('sandbox.filter')} />
      </div>

      {loading ? <PageLoading /> : (
        <Stack gap={4}>
          {visible.map((p) => (
            <Tile key={p.id} style={{ opacity: p.enabled === false ? 0.5 : 1, display: 'flex', flexDirection: 'column' }}>
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', gap: '1rem' }}>
                <div style={{ opacity: p.image ? 1 : 0.55 }}>
                  <div style={{ display: 'flex', alignItems: 'center', gap: '0.3rem', flexWrap: 'wrap' }}>
                    <strong>{p.name}</strong>
                    <span style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem' }}>{p.id}{p.builtin ? '' : ' · custom'}</span>
                  </div>
                  <p style={{ color: 'var(--tm-text-2)', fontSize: '0.8rem', marginTop: '0.25rem' }}>{p.description}</p>
                </div>
                <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'center', flexShrink: 0 }}>
                  <Button size="sm" kind="ghost" renderIcon={Edit} onClick={() => startEdit(p)}>{t('sandbox.edit')}</Button>
                  {(p.builtin || p.base || p.dockerfile_override) && (
                    <Button size="sm" kind="secondary" renderIcon={Renew} disabled={building === p.id} onClick={() => void build(p)}>
                      {building === p.id ? t('sandbox.building') : p.image ? t('sandbox.rebuild') : t('sandbox.build')}
                    </Button>
                  )}
                  {!p.builtin && !p.base && !p.dockerfile_override && (
                    <Button
                      size="sm"
                      kind="secondary"
                      renderIcon={Download}
                      disabled={building === p.id}
                      iconDescription={t('sandbox.pull_hint') ?? 'Pull a reference and pin its digest'}
                      onClick={() => setPullRef({ id: p.id, reference: '' })}
                    >
                      {building === p.id ? t('sandbox.building') : t('sandbox.pull') ?? 'Pull'}
                    </Button>
                  )}
                  {!p.builtin && !p.base && (
                    <Button size="sm" kind="danger--ghost" renderIcon={TrashCan} onClick={() => void remove(p)}>{t('action.delete')}</Button>
                  )}
                  <Button size="sm" kind="ghost" onClick={() => void openHistory(p)}>{t('sandbox.history') ?? 'History'}</Button>
                </div>
              </div>
              <div style={{ display: 'flex', gap: '0.25rem', marginTop: '0.5rem', flexWrap: 'wrap', alignItems: 'center', opacity: p.image ? 1 : 0.55 }}>
                {(p.toolchains ?? []).map((tc) => <Tag key={tc} type="blue" size="sm">{tc}</Tag>)}
                {p.base && <Tag type="purple" size="sm">{t('sandbox.base_tag', { base: p.base })}</Tag>}
                {p.dockerfile_override?.trim() && <Tag type="teal" size="sm">{t('sandbox.override_tag')}</Tag>}
                {(p.packages ?? []).map((pkg) => <Tag key={pkg} type="cool-gray" size="sm">{pkg}</Tag>)}
                {p.docker_socket && <Tag type="red" size="sm">{t('sandbox.socket_tag') ?? 'docker socket'}</Tag>}
                {p.image
                  ? (p.available
                      ? <Tag type="green" size="sm">{t('sandbox.built')}</Tag>
                      : <Tag type="red" size="sm">{t('sandbox.missing_image')}</Tag>)
                  : <Tag type="gray" size="sm">{t('sandbox.not_built')}</Tag>}
                {p.memory && <Tag type="gray" size="sm">mem {p.memory}</Tag>}
                {p.scratch_size && <Tag type="gray" size="sm">scratch {p.scratch_size}</Tag>}
                {/* The uniform switch rides at the right end of the tag row:
                    bottom-right of the card, clear of the action buttons. */}
                <span style={{ marginLeft: 'auto' }} title={t('sandbox.enabled_hint') ?? 'Disabled profiles fail runs that reference them'}>
                  <Toggle id={`sandbox-enabled-${p.id}`} labelText="" hideLabel aria-label={t('sandbox.enabled_hint') ?? 'Toggle profile'} toggled={p.enabled !== false} onToggle={() => void toggleSandboxEnabled(p)} size="sm" />
                </span>
              </div>
              {p.image && <p style={{ color: 'var(--tm-text-3)', fontSize: '0.7rem', marginTop: '0.35rem', wordBreak: 'break-all' }}>{p.image}</p>}
              {historyFor === p.id && (
                <div style={{ marginTop: '0.4rem', borderTop: '1px solid var(--tm-border)', paddingTop: '0.4rem', fontSize: '0.72rem' }}>
                  <div style={{ color: 'var(--tm-text-3)', marginBottom: '0.2rem' }}>{t('sandbox.history_heading') ?? 'Build history'}</div>
                  {history.length === 0 ? (
                    <p style={{ color: 'var(--tm-text-3)', margin: 0 }}>{t('sandbox.history_empty') ?? 'No builds journaled yet.'}</p>
                  ) : (
                    history.map((h, hi) => (
                      <div key={hi} style={{ display: 'flex', gap: '0.5rem', alignItems: 'baseline', flexWrap: 'wrap' }}>
                        <span style={{ color: 'var(--tm-text-3)', flexShrink: 0 }}>{new Date(h.at).toLocaleString()}</span>
                        {h.pulled && <Tag type="cool-gray" size="sm">{t('sandbox.pulled') ?? 'pulled'}</Tag>}
                        <span style={{ fontFamily: 'var(--tm-font-mono)', fontSize: '0.66rem', wordBreak: 'break-all' }}>{h.image}</span>
                      </div>
                    ))
                  )}
                </div>
              )}
            </Tile>
          ))}
          {visible.length === 0 && (
            <Tile style={{ textAlign: 'center', padding: '3rem', color: 'var(--tm-text-3)' }}>
              <p>{t('sandbox.empty')}</p>
            </Tile>
          )}
        </Stack>
      )}

      {showForm && (
        <AppModal onClose={() => { setShowForm(false); setEditing(null); setDraftRationale('') }} panelStyle={{ width: '640px' }}>
          <Heading>{editing ? t('sandbox.edit_heading') : t('sandbox.custom_heading')}</Heading>
          <Stack gap={3}>
            {draftRationale && <InlineNotification kind="info" lowContrast title={t('sandbox.wizard_rationale')} subtitle={draftRationale} onCloseButtonClick={() => setDraftRationale('')} />}
            {error && <InlineNotification kind="error" lowContrast title={t('action.error')} subtitle={error} />}
            <TextInput id="sp-id" labelText={t('sandbox.field_id')} value={form.id} disabled={!!editing} onChange={(e) => setForm({ ...form, id: e.target.value })} placeholder="rust" />
            <TextInput id="sp-name" labelText={t('sandbox.field_name')} value={form.name} disabled={!!editing?.builtin} onChange={(e) => setForm({ ...form, name: e.target.value })} placeholder="Rust toolchain" />
            <TextArea id="sp-desc" labelText={t('sandbox.field_description')} rows={2} value={form.description} disabled={!!editing?.builtin} onChange={(e) => setForm({ ...form, description: e.target.value })} />
            {!editing?.builtin && (
              <RadioButtonGroup
                legendText={t('sandbox.recipe_mode_legend')}
                name="sp-recipe-mode"
                orientation="horizontal"
                valueSelected={recipeMode}
                onChange={(selection) => switchRecipeMode(selection as SandboxRecipeMode)}
              >
                <RadioButton value="base" labelText={t('sandbox.mode_base')} />
                <RadioButton value="dockerfile" labelText={t('sandbox.mode_dockerfile')} />
              </RadioButtonGroup>
            )}
            {recipeMode === 'base' && !editing?.builtin && (
              <Select
                id="sp-base"
                labelText={t('sandbox.field_base')}
                value={form.base}
                onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setForm({ ...form, base: e.target.value })}
              >
                <SelectItem value="" text={t('sandbox.base_external')} />
                <SelectItem value="base" text={t('sandbox.base_minimal')} />
                <SelectItem value="node" text={t('sandbox.base_node')} />
                <SelectItem value="python" text={t('sandbox.base_python')} />
                <SelectItem value="browser-e2e" text={t('sandbox.base_browser')} />
              </Select>
            )}
            {recipeMode === 'base' && !editing?.builtin && form.base && (
              <TextInput id="sp-packages" labelText={t('sandbox.field_packages')} value={form.packages} onChange={(e) => setForm({ ...form, packages: e.target.value })} placeholder="gcc, musl-dev" />
            )}
            {recipeMode === 'base' && !editing?.builtin && form.base && recipePreview && (
              <div>
                <div style={{ fontSize: '0.75rem', fontWeight: 600, color: 'var(--tm-text-3)', marginBottom: '0.25rem' }}>{t('sandbox.recipe_preview') ?? 'Dockerfile preview'}</div>
                <pre style={{ background: 'var(--tm-surface-2)', borderRadius: '4px', padding: '0.5rem', fontSize: '0.7rem', overflowX: 'auto', margin: 0, whiteSpace: 'pre-wrap' }}>{recipePreview}</pre>
              </div>
            )}
            {recipeMode === 'base' && !editing?.builtin && !form.base && (
              <TextInput id="sp-image" labelText={t('sandbox.field_image')} value={form.image} onChange={(e) => setForm({ ...form, image: e.target.value })} placeholder="registry.local/toolbox@sha256:…" />
            )}
            {recipeMode === 'dockerfile' && !editing?.builtin && (
              <div>
                <TextArea
                  id="sp-dockerfile"
                  labelText={t('sandbox.field_dockerfile')}
                  rows={14}
                  value={form.dockerfile}
                  onChange={(e) => setForm({ ...form, dockerfile: e.target.value })}
                  placeholder={'FROM python:3.12-alpine\nRUN apk add --no-cache gcc'}
                  style={{ fontFamily: 'var(--tm-font-mono)', fontSize: '0.75rem' }}
                />
                <p style={{ color: 'var(--tm-text-3)', fontSize: '0.72rem', marginTop: '0.25rem' }}>{t('sandbox.dockerfile_hint')}</p>
              </div>
            )}
            {recipeMode === 'dockerfile' && !editing?.builtin && dockerfileCheck === 'checking' && (
              <InlineNotification
                kind="info"
                lowContrast
                hideCloseButton
                title={t('sandbox.dockerfile_checking')}
                subtitle=""
                style={{ marginBottom: 0 }}
              />
            )}
            {recipeMode === 'dockerfile' && !editing?.builtin && dockerfileCheck === 'valid' && (
              <InlineNotification
                kind="success"
                lowContrast
                hideCloseButton
                title={t('sandbox.dockerfile_valid')}
                subtitle=""
                style={{ marginBottom: 0 }}
              />
            )}
            {recipeMode === 'dockerfile' && !editing?.builtin && dockerfileCheck === 'invalid' && dockerfileError && (
              <InlineNotification
                kind="error"
                lowContrast
                hideCloseButton
                title={t('sandbox.dockerfile_invalid')}
                subtitle={dockerfileError}
                style={{ marginBottom: 0 }}
              />
            )}
            <TextInput id="sp-toolchains" labelText={t('sandbox.field_toolchains')} value={form.toolchains} disabled={!!editing?.builtin} onChange={(e) => setForm({ ...form, toolchains: e.target.value })} placeholder="rust, wasm" />
            <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr 1fr 1fr', gap: '0.5rem' }}>
              <TextInput id="sp-memory" labelText={t('sandbox.field_memory')} value={form.memory} onChange={(e) => setForm({ ...form, memory: e.target.value })} placeholder="2g" />
              <TextInput id="sp-cpus" labelText={t('sandbox.field_cpus')} value={form.cpus} onChange={(e) => setForm({ ...form, cpus: e.target.value })} placeholder="2" />
              <TextInput id="sp-scratch" labelText={t('sandbox.field_scratch')} value={form.scratch_size} onChange={(e) => setForm({ ...form, scratch_size: e.target.value })} placeholder="1g" />
              <TextInput id="sp-tmp" labelText={t('sandbox.field_tmp')} value={form.tmp_size} onChange={(e) => setForm({ ...form, tmp_size: e.target.value })} placeholder="512m" />
            </div>
            <TextArea id="sp-cache" labelText={t('sandbox.field_cache')} rows={2} value={form.cache_env} onChange={(e) => setForm({ ...form, cache_env: e.target.value })} placeholder={'CARGO_HOME=/cache/cargo'} />
            {!editing?.builtin && (
              <div>
                <Checkbox
                  id="sp-docker-socket"
                  labelText={t('sandbox.field_docker_socket') ?? 'Mount the host docker socket'}
                  checked={form.docker_socket}
                  onChange={(_, { checked }) => setForm({ ...form, docker_socket: checked })}
                />
                <p style={{ color: 'var(--tm-text-3)', fontSize: '0.72rem', margin: 0 }}>{t('sandbox.docker_socket_hint')}</p>
              </div>
            )}
            <p style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem' }}>
              {editing?.builtin
                ? t('sandbox.edit_hint')
                : recipeMode === 'dockerfile'
                  ? t('sandbox.dockerfile_save_hint')
                  : form.base
                    ? t('sandbox.recipe_hint')
                    : t('sandbox.custom_hint')}
            </p>
            <div style={{ display: 'flex', justifyContent: 'flex-end', alignItems: 'center', gap: '0.5rem' }}>
              {/* The uniform switch at the bottom-left of the button block. */}
              <span style={{ marginRight: 'auto' }} title={t('sandbox.enabled_hint') ?? 'Disabled profiles fail runs that reference them'}>
                <Toggle id="sandbox-form-enabled" labelText={t('sandbox.enabled_hint') ?? 'Enabled'} labelA={t('common.off') ?? 'Off'} labelB={t('common.on') ?? 'On'} toggled={formEnabled} onToggle={(v: boolean) => setFormEnabled(v)} size="sm" />
              </span>
              <Button kind="secondary" onClick={() => { setShowForm(false); setEditing(null); setDraftRationale('') }}>{t('action.cancel')}</Button>
              <Button
                onClick={() => void saveCustom()}
                disabled={sandboxSaveBlocked(form, recipeMode, dockerfileCheck, editing)}
              >{editing ? t('action.save') : t('action.create')}</Button>
            </div>
          </Stack>
        </AppModal>
      )}

      {/* Pull dialog (§5.1 phase 2): resolve an external reference to a
          digest and pin it into the profile. */}
      {pullRef && (
        <AppModal onClose={() => setPullRef(null)} panelStyle={{ width: '520px' }}>
          <Heading>{t('sandbox.pull_title') ?? 'Pull external image'}</Heading>
          <p style={{ color: 'var(--tm-text-3)', fontSize: '0.8rem', margin: '0.25rem 0 0.75rem' }}>{t('sandbox.pull_hint') ?? 'The reference is pulled now and pinned by digest; runs stay reproducible until the next explicit pull.'}</p>
          <TextInput
            id="sp-pull-ref"
            labelText={t('sandbox.pull_reference') ?? 'Image reference'}
            value={pullRef.reference}
            onChange={(e) => setPullRef({ ...pullRef, reference: e.target.value })}
            placeholder="registry.local/toolbox:1.4"
            autoFocus
          />
          <div className="form-actions">
            <Button kind="secondary" onClick={() => setPullRef(null)}>{t('action.cancel')}</Button>
            <Button
              disabled={!pullRef.reference.trim() || building === pullRef.id}
              onClick={() => {
                const target = profiles.find((p) => p.id === pullRef.id)
                setPullRef(null)
                if (target) void pull(target, pullRef.reference.trim())
              }}
            >{building ? t('sandbox.building') : t('sandbox.pull') ?? 'Pull'}</Button>
          </div>
        </AppModal>
      )}

      {/* Wizard: describe the agent's tasks, the supervisor proposes the profile */}
      {wizardOpen && (
        <AppModal onClose={() => setWizardOpen(false)} panelStyle={{ width: '560px' }}>
          <Heading>{t('sandbox.wizard_title')}</Heading>
          {wizardBusy ? (
            <p style={{ color: 'var(--tm-text-2)', fontSize: '0.85rem', margin: '1rem 0' }}>{t('sandbox.wizard_generating')}</p>
          ) : (
            <>
              <p style={{ color: 'var(--tm-text-3)', fontSize: '0.8rem', margin: '0.25rem 0 0.75rem' }}>
                {t('sandbox.wizard_hint')}
              </p>
              <TextArea
                id="sandbox-wizard-description"
                hideLabel
                labelText=""
                rows={5}
                value={wizardText}
                onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => setWizardText(e.target.value)}
                placeholder={t('sandbox.wizard_placeholder')}
                autoFocus
              />
              {wizardError && <InlineNotification kind="error" lowContrast title={t('action.error')} subtitle={wizardError} hideCloseButton style={{ marginTop: '0.5rem' }} />}
              <div className="form-actions">
                <Button kind="secondary" onClick={() => setWizardOpen(false)}>{t('action.cancel')}</Button>
                <Button onClick={() => void runWizard()} disabled={!wizardText.trim()}>{t('sandbox.wizard_generate')}</Button>
              </div>
            </>
          )}
        </AppModal>
      )}
    </div>
  )
}

