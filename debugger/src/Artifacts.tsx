import { useEffect, useState, useCallback } from 'react'
import {
  Button,
  TextInput,
  TextArea,
  InlineNotification,
  Tag,
  Tile,
  Grid,
  Column,
  Stack,
  Heading,
  Select,
  SelectItem,
} from '@carbon/react'
import PageLoading from './PageLoading'
import { Add, Edit, TrashCan } from '@carbon/icons-react'
import { workspaceApi, type Artifact } from './workspaceApi'
import { useT } from './i18n'
import ListFilter, { matchesFilter } from './ListFilter'
import AppModal from './Modal'
import { useConfirm } from './ConfirmDialog'
import { useOrgUnits, OrgUnitSelect, OrgBadge } from './orgUnits'

function message(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }

export function humanArtifactSize(bytes: number): string {
  if (!bytes) return '—'
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KiB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MiB`
}

type ArtifactForm = Partial<Artifact>

export default function Artifacts() {
  const t = useT()
  const { confirmDialog, dialog: confirmDialogNode } = useConfirm()
  const org = useOrgUnits()
  const [artifacts, setArtifacts] = useState<Artifact[]>([])
  const [references, setReferences] = useState<Record<string, string[]>>({})
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [showForm, setShowForm] = useState(false)
  const [editing, setEditing] = useState<Artifact | null>(null)
  const [form, setForm] = useState<ArtifactForm>({})
  const [filter, setFilter] = useState('')
  const [providerFilter, setProviderFilter] = useState('')
  const [orgFilter, setOrgFilter] = useState('')

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const [list, refs] = await Promise.all([
        workspaceApi.listArtifacts(),
        workspaceApi.artifactReferences().catch(() => ({ references: {} as Record<string, string[]> })),
      ])
      setArtifacts(list.artifacts ?? [])
      setReferences(refs.references ?? {})
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }, [])

  useEffect(() => { void load() }, [load])

  const visibleArtifacts = artifacts.filter((a) => {
    if (providerFilter && a.provider !== providerFilter) return false
    if (orgFilter && (a.org_unit_id ?? '') !== orgFilter) return false
    return matchesFilter(filter, a.title, a.id, a.description)
  })

  const startCreate = () => {
    setEditing(null)
    setForm({ id: '', title: '', description: '', provider: 'builtin', content: '', mime_type: 'text/markdown' })
    setShowForm(true)
  }

  const startEdit = async (a: Artifact) => {
    setEditing(a)
    setForm({ ...a, content: '' })
    setShowForm(true)
    // List rows ship no bodies: pull the document itself for the editor.
    try {
      const page = await workspaceApi.getArtifact(a.id)
      setForm((prev) => ({ ...prev, content: page.artifact.content ?? '' }))
    } catch (f) { setError(message(f)) }
  }

  const save = async () => {
    if (!form.title?.trim()) return
    setLoading(true); setError('')
    try {
      if (editing) {
        await workspaceApi.updateArtifact(editing.id, {
          title: form.title, description: form.description, provider: form.provider,
          external_ref: form.external_ref, content: form.content, mime_type: form.mime_type,
        })
        // PUT payloads keep the stored org binding: re-scoping goes through
        // the admin binding endpoint, same contract as secrets and skills.
        if (org.isAdmin && (form.org_unit_id ?? '') !== (editing.org_unit_id ?? '')) {
          await workspaceApi.setResourceBinding('artifact', editing.id, form.org_unit_id ?? '')
        }
      } else {
        await workspaceApi.createArtifact({
          id: form.id ?? '', title: form.title, description: form.description,
          provider: form.provider ?? 'builtin', external_ref: form.external_ref,
          content: form.content, mime_type: form.mime_type, org_unit_id: form.org_unit_id,
        })
      }
      setShowForm(false); setEditing(null)
      void load()
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }

  const remove = async (a: Artifact) => {
    if (!(await confirmDialog({ title: t('artifacts.delete_confirm', { name: a.title }) ?? `Delete artifact "${a.title}"?`, body: t('action.delete_warning') ?? 'This cannot be undone.', danger: true, confirmLabel: t('action.delete') ?? 'Delete' }))) return
    setLoading(true); setError('')
    try {
      await workspaceApi.deleteArtifact(a.id)
      void load()
    } catch (f) { setError(message(f)) }
    finally { setLoading(false) }
  }

  return (
    <div style={{ padding: '1rem' }}>
      {confirmDialogNode}
      {error && <InlineNotification kind="error" title={t('action.error') ?? 'Error'} subtitle={error} onClose={() => setError('')} lowContrast style={{ marginBottom: '1rem' }} />}

      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '0.5rem' }}>
        <Heading>{t('artifacts.title') ?? 'Reference Artifacts'}</Heading>
        <Button renderIcon={Add} onClick={startCreate}>{t('artifacts.new') ?? 'New Artifact'}</Button>
      </div>
      <div style={{ display: 'flex', gap: '0.5rem', flexWrap: 'wrap', alignItems: 'flex-end', position: 'sticky', top: '3rem', background: 'var(--tm-bg)', zIndex: 1, paddingBottom: '0.5rem', marginBottom: '0.5rem' }}>
        <div style={{ maxWidth: '420px', flex: '1 1 260px' }}>
          <ListFilter value={filter} onChange={setFilter} placeholder={t('artifacts.filter_placeholder') ?? 'Filter artifacts…'} />
        </div>
        <Select id="artifact-provider-filter" hideLabel labelText="provider" value={providerFilter} onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setProviderFilter(e.target.value)} style={{ maxWidth: '180px' }}>
          <SelectItem value="" text={t('artifacts.filter_all_providers') ?? 'All providers'} />
          <SelectItem value="builtin" text="builtin" />
          <SelectItem value="url" text="url" />
        </Select>
        {org.units.length > 0 && (
          <Select id="artifact-org-filter" hideLabel labelText="org" value={orgFilter} onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setOrgFilter(e.target.value)} style={{ maxWidth: '220px' }}>
            <SelectItem value="" text={t('artifacts.filter_all_orgs') ?? 'All units'} />
            {org.units.map((u) => <SelectItem key={u.id} value={u.id} text={u.name} />)}
          </Select>
        )}
      </div>

      {artifacts.length === 0 && !loading && (
        <Tile style={{ textAlign: 'center', padding: '3rem', color: 'var(--tm-text-3)' }}>
          <p>{t('artifacts.empty') ?? 'No reference artifacts yet.'}</p>
          <p style={{ fontSize: '0.8rem', marginTop: '0.5rem' }}>
            {t('artifacts.empty_hint') ?? 'Regulations, approved instructions and checklists live here. Skills reference them from the manifest; agents load the body on demand instead of keeping it in the prompt.'}
          </p>
        </Tile>
      )}

      <Grid>
        {visibleArtifacts.map((a) => {
          const usedBy = references[a.id] ?? []
          return (
            <Column key={a.id} sm={4} md={4} lg={4}>
              <Tile style={{ marginBottom: '0.75rem' }}>
                <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start' }}>
                  <div>
                    <div style={{ display: 'flex', alignItems: 'center', gap: '0.3rem', flexWrap: 'wrap' }}>
                      <strong>{a.title}</strong>
                      <OrgBadge orgUnitID={a.org_unit_id} org={org} />
                    </div>
                    <p style={{ fontFamily: 'var(--tm-font-mono, monospace)', fontSize: '0.72rem', color: 'var(--tm-text-3)', marginTop: '0.15rem' }}>{a.id}</p>
                    {a.description && (
                      <p style={{ color: 'var(--tm-text-3)', fontSize: '0.75rem', marginTop: '0.25rem', wordBreak: 'break-word' }}>{a.description}</p>
                    )}
                    <div style={{ display: 'flex', gap: '0.25rem', flexWrap: 'wrap', marginTop: '0.5rem', alignItems: 'center' }}>
                      <Tag type={a.provider === 'builtin' ? 'blue' : 'gray'} size="sm">{a.provider}</Tag>
                      <Tag type="cool-gray" size="sm">{humanArtifactSize(a.size_bytes)}</Tag>
                      {usedBy.length > 0
                        ? <Tag type="green" size="sm" title={usedBy.join(', ')}>{t('artifacts.used_by', { n: String(usedBy.length) }) ?? `used by ${usedBy.length} skill(s)`}</Tag>
                        : <Tag size="sm">{t('artifacts.not_used') ?? 'not referenced'}</Tag>}
                    </div>
                    {a.provider === 'url' && a.external_ref && (
                      <p style={{ fontSize: '0.7rem', marginTop: '0.5rem', wordBreak: 'break-all', color: 'var(--tm-text-3)' }}>{a.external_ref}</p>
                    )}
                  </div>
                  <Stack orientation="horizontal" gap={1}>
                    <Button size="sm" kind="ghost" hasIconOnly renderIcon={Edit} iconDescription="Edit" onClick={() => void startEdit(a)} />
                    <Button size="sm" kind="danger--ghost" hasIconOnly renderIcon={TrashCan} iconDescription="Delete" onClick={() => void remove(a)} />
                  </Stack>
                </div>
              </Tile>
            </Column>
          )
        })}
      </Grid>

      {showForm && (
        <AppModal onClose={() => { setShowForm(false); setEditing(null) }} panelStyle={{ width: '640px' }}>
          <Heading>{editing ? (t('artifacts.edit') ?? 'Edit Artifact') : (t('artifacts.new') ?? 'New Artifact')}</Heading>
          <Stack gap={3}>
            <TextInput
              id="artifact-id"
              labelText={t('artifacts.id_label') ?? 'ID'}
              value={form.id ?? ''}
              disabled={!!editing}
              onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, id: e.target.value })}
              placeholder="coding-standard"
              helperText={t('artifacts.id_helper') ?? 'Lowercase letters, digits and dashes; skills reference the artifact by this id'}
              autoFocus
            />
            <TextInput
              id="artifact-title"
              labelText={t('artifacts.title_label') ?? 'Title'}
              value={form.title ?? ''}
              onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, title: e.target.value })}
              placeholder={t('artifacts.title_placeholder') ?? 'Code quality standard'}
            />
            <TextArea
              id="artifact-description"
              labelText={t('artifacts.description_label') ?? 'Applicability (when to read this)'}
              value={form.description ?? ''}
              onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => setForm({ ...form, description: e.target.value })}
              rows={2}
              placeholder={t('artifacts.description_placeholder') ?? 'Read before changing any Go code in this repository'}
              helperText={t('artifacts.description_helper') ?? 'The agent matches the task against this condition — it decides whether artifact_read is worth the tokens'}
            />
            <Select
              id="artifact-provider"
              labelText={t('artifacts.provider_label') ?? 'Provider'}
              value={form.provider ?? 'builtin'}
              onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setForm({ ...form, provider: e.target.value as Artifact['provider'] })}
            >
              <SelectItem value="builtin" text={t('artifacts.provider_builtin') ?? 'builtin — body stored in the registry (≤ 2 MiB)'} />
              <SelectItem value="url" text={t('artifacts.provider_url') ?? 'url — body lives in an external system; the kernel fetches and caches it'} />
            </Select>
            {(form.provider ?? 'builtin') === 'url' ? (
              <TextInput
                id="artifact-external-ref"
                labelText={t('artifacts.external_ref_label') ?? 'External reference'}
                value={form.external_ref ?? ''}
                onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, external_ref: e.target.value })}
                placeholder="https://wiki.internal.example.com/deploy-runbook"
                helperText={t('artifacts.external_ref_helper') ?? 'Absolute http(s) URL; the sandbox never dials out — the kernel proxies the fetch'}
              />
            ) : (
              <TextArea
                id="artifact-content"
                labelText={t('artifacts.content_label') ?? 'Document body'}
                value={form.content ?? ''}
                onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => setForm({ ...form, content: e.target.value })}
                rows={12}
                placeholder="# Standard…"
                helperText={t('artifacts.content_helper') ?? 'Markdown; served to agents in 32 KiB chunks, never inlined into the prompt'}
                style={{ fontFamily: 'var(--tm-font-mono, monospace)', fontSize: '0.8rem' }}
              />
            )}
            <TextInput
              id="artifact-mime"
              labelText={t('artifacts.mime_label') ?? 'MIME type'}
              value={form.mime_type ?? ''}
              onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, mime_type: e.target.value })}
              placeholder="text/markdown"
            />
            {org.units.length > 0 && (
              <OrgUnitSelect
                id="artifact-org"
                label={t('artifacts.org_label') ?? 'Availability'}
                org={org}
                value={form.org_unit_id ?? ''}
                onChange={(v: string) => setForm({ ...form, org_unit_id: v })}
              />
            )}
            <Stack orientation="horizontal" gap={2}>
              <Button onClick={() => void save()} disabled={!form.title?.trim() || (!editing && !form.id?.trim()) || ((form.provider ?? 'builtin') === 'url' ? !form.external_ref?.trim() : !form.content?.trim())}>{t('action.save') ?? 'Save'}</Button>
              <Button kind="secondary" onClick={() => { setShowForm(false); setEditing(null) }}>{t('action.cancel') ?? 'Cancel'}</Button>
            </Stack>
          </Stack>
        </AppModal>
      )}

      {loading && <PageLoading />}
    </div>
  )
}
