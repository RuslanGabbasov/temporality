import { useEffect, useState, useCallback } from 'react'
import {
  Button,
  TextInput,
  TextArea,
  InlineNotification,
  Select,
  SelectItem,
  Tag,
  Tile,
  Stack,
  Heading,
} from '@carbon/react'
import PageLoading from './PageLoading'
import { Add, TrashCan, Renew, Edit } from '@carbon/icons-react'
import { workspaceApi, type SandboxProfile } from './workspaceApi'
import { useT } from './i18n'
import ListFilter, { matchesFilter } from './ListFilter'
import AppModal from './Modal'
import { useConfirm } from './ConfirmDialog'

function message(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }

type ProfileForm = {
  id: string
  name: string
  description: string
  image: string
  base: string
  packages: string
  toolchains: string
  memory: string
  cpus: string
  scratch_size: string
  tmp_size: string
  cache_env: string
}

const emptyForm: ProfileForm = { id: '', name: '', description: '', image: '', base: '', packages: '', toolchains: '', memory: '', cpus: '', scratch_size: '', tmp_size: '', cache_env: '' }

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
  const [filter, setFilter] = useState('')
  const [building, setBuilding] = useState('')
  const [editing, setEditing] = useState<SandboxProfile | null>(null)
  const [wizardOpen, setWizardOpen] = useState(false)
  const [wizardText, setWizardText] = useState('')
  const [wizardBusy, setWizardBusy] = useState(false)
  const [wizardError, setWizardError] = useState('')
  const [draftRationale, setDraftRationale] = useState('')

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

  const build = async (p: SandboxProfile) => {
    setBuilding(p.id); setError(''); setNotice('')
    try {
      await workspaceApi.buildSandboxProfile(p.id)
      setNotice(t('sandbox.built_notice', { name: p.name }))
      await load()
    } catch (f) { setError(message(f)) }
    finally { setBuilding('') }
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
    setForm({
      id: p.id,
      name: p.name,
      description: p.description ?? '',
      image: p.image ?? '',
      base: p.base ?? '',
      packages: (p.packages ?? []).join(', '),
      toolchains: (p.toolchains ?? []).join(', '),
      memory: p.memory ?? '',
      cpus: p.cpus ?? '',
      scratch_size: p.scratch_size ?? '',
      tmp_size: p.tmp_size ?? '',
      cache_env: (p.cache_env ?? []).join('\n'),
    })
    setShowForm(true)
  }

  const saveCustom = async () => {
    setError('')
    const payload = {
      id: editing ? editing.id : form.id.trim(),
      name: form.name.trim(),
      description: form.description,
      image: form.base ? '' : form.image.trim(),
      base: form.base,
      packages: form.base ? form.packages.split(',').map((s) => s.trim()).filter(Boolean) : [],
      toolchains: form.toolchains.split(',').map((s) => s.trim()).filter(Boolean),
      memory: form.memory.trim(),
      cpus: form.cpus.trim(),
      scratch_size: form.scratch_size.trim(),
      tmp_size: form.tmp_size.trim(),
      cache_env: form.cache_env.split('\n').map((s) => s.trim()).filter(Boolean),
    }
    try {
      if (editing) {
        await workspaceApi.updateSandboxProfile(editing.id, payload)
        setEditing(null)
      } else {
        await workspaceApi.createSandboxProfile(payload)
      }
      setShowForm(false)
      setForm(emptyForm)
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
      setForm({
        id: draft.id,
        name: draft.name,
        description: draft.description ?? '',
        image: '',
        base: draft.base ?? '',
        packages: (draft.packages ?? []).join(', '),
        toolchains: (draft.toolchains ?? []).join(', '),
        memory: draft.memory ?? '',
        cpus: draft.cpus ?? '',
        scratch_size: draft.scratch_size ?? '',
        tmp_size: draft.tmp_size ?? '',
        cache_env: '',
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
        <Button size="sm" kind="secondary" onClick={() => { setWizardText(''); setWizardError(''); setWizardOpen(true) }}>{t('sandbox.wizard_button') ?? 'From description'}</Button>
        <Button size="sm" onClick={() => setShowForm(true)} renderIcon={Add}>{t('sandbox.add_custom')}</Button>
      </div>
      <div style={{ position: 'sticky', top: '3rem', background: 'var(--tm-bg)', zIndex: 1, paddingBottom: '0.5rem', marginBottom: '0.5rem', maxWidth: '420px' }}>
        <ListFilter value={filter} onChange={setFilter} placeholder={t('sandbox.filter')} />
      </div>

      {loading ? <PageLoading /> : (
        <Stack gap={4}>
          {visible.map((p) => (
            <Tile key={p.id}>
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
                  {(p.builtin || p.base) && (
                    <Button size="sm" kind="secondary" renderIcon={Renew} disabled={building === p.id} onClick={() => void build(p)}>
                      {building === p.id ? t('sandbox.building') : p.image ? t('sandbox.rebuild') : t('sandbox.build')}
                    </Button>
                  )}
                  {!p.builtin && !p.base && (
                    <Button size="sm" kind="danger--ghost" renderIcon={TrashCan} onClick={() => void remove(p)}>{t('action.delete')}</Button>
                  )}
                </div>
              </div>
              <div style={{ display: 'flex', gap: '0.25rem', marginTop: '0.5rem', flexWrap: 'wrap', alignItems: 'center', opacity: p.image ? 1 : 0.55 }}>
                {(p.toolchains ?? []).map((tc) => <Tag key={tc} type="blue" size="sm">{tc}</Tag>)}
                {p.base && <Tag type="purple" size="sm">{t('sandbox.base_tag', { base: p.base })}</Tag>}
                {(p.packages ?? []).map((pkg) => <Tag key={pkg} type="cool-gray" size="sm">{pkg}</Tag>)}
                {p.image
                  ? (p.available
                      ? <Tag type="green" size="sm">{t('sandbox.built')}</Tag>
                      : <Tag type="red" size="sm">{t('sandbox.missing_image')}</Tag>)
                  : <Tag type="gray" size="sm">{t('sandbox.not_built')}</Tag>}
                {p.memory && <Tag type="gray" size="sm">mem {p.memory}</Tag>}
                {p.scratch_size && <Tag type="gray" size="sm">scratch {p.scratch_size}</Tag>}
              </div>
              {p.image && <p style={{ color: 'var(--tm-text-3)', fontSize: '0.7rem', marginTop: '0.35rem', wordBreak: 'break-all' }}>{p.image}</p>}
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
            {!editing?.builtin && form.base && (
              <TextInput id="sp-packages" labelText={t('sandbox.field_packages')} value={form.packages} onChange={(e) => setForm({ ...form, packages: e.target.value })} placeholder="gcc, musl-dev" />
            )}
            {!editing?.builtin && !form.base && (
              <TextInput id="sp-image" labelText={t('sandbox.field_image')} value={form.image} onChange={(e) => setForm({ ...form, image: e.target.value })} placeholder="registry.local/toolbox@sha256:…" />
            )}
            <TextInput id="sp-toolchains" labelText={t('sandbox.field_toolchains')} value={form.toolchains} disabled={!!editing?.builtin} onChange={(e) => setForm({ ...form, toolchains: e.target.value })} placeholder="rust, wasm" />
            <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr 1fr 1fr', gap: '0.5rem' }}>
              <TextInput id="sp-memory" labelText={t('sandbox.field_memory')} value={form.memory} onChange={(e) => setForm({ ...form, memory: e.target.value })} placeholder="2g" />
              <TextInput id="sp-cpus" labelText={t('sandbox.field_cpus')} value={form.cpus} onChange={(e) => setForm({ ...form, cpus: e.target.value })} placeholder="2" />
              <TextInput id="sp-scratch" labelText={t('sandbox.field_scratch')} value={form.scratch_size} onChange={(e) => setForm({ ...form, scratch_size: e.target.value })} placeholder="1g" />
              <TextInput id="sp-tmp" labelText={t('sandbox.field_tmp')} value={form.tmp_size} onChange={(e) => setForm({ ...form, tmp_size: e.target.value })} placeholder="512m" />
            </div>
            <TextArea id="sp-cache" labelText={t('sandbox.field_cache')} rows={2} value={form.cache_env} onChange={(e) => setForm({ ...form, cache_env: e.target.value })} placeholder={'CARGO_HOME=/cache/cargo'} />
            <p style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem' }}>{editing?.builtin ? t('sandbox.edit_hint') : form.base ? t('sandbox.recipe_hint') : t('sandbox.custom_hint')}</p>
            <div style={{ display: 'flex', justifyContent: 'flex-end', gap: '0.5rem' }}>
              <Button kind="secondary" onClick={() => { setShowForm(false); setEditing(null); setDraftRationale('') }}>{t('action.cancel')}</Button>
              <Button onClick={() => void saveCustom()} disabled={!form.id.trim() || !form.name.trim() || (!editing && !form.base && !form.image.trim())}>{editing ? t('action.save') : t('action.create')}</Button>
            </div>
          </Stack>
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

