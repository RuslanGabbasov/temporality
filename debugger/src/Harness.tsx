import { useCallback, useEffect, useState } from 'react'
import { Button, Select, SelectItem, InlineNotification, Heading } from '@carbon/react'
import { Save, Renew } from '@carbon/icons-react'
import PageLoading from './PageLoading'
import { workspaceApi, type Provider, type SupervisorSettings } from './workspaceApi'
import { useT } from './i18n'

function message(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }

/** «Харнес» (docs/harness-settings.md): the vision model and the harness model
 *  roles (compaction card, run titles, knowledge extraction), plus the
 *  effective (env-resolved, read-only) budget snapshot — one place to see what
 *  the kernel actually runs with. The judge model and the HAB coefficients
 *  live on the «Модель-супервайзер» page; both pages write the same supervisor
 *  API. */
export default function Harness() {
  const t = useT()
  const [supervisor, setSupervisor] = useState<SupervisorSettings | null>(null)
  const [providers, setProviders] = useState<Provider[]>([])
  const [visionDraft, setVisionDraft] = useState('')
  const [compactionDraft, setCompactionDraft] = useState('')
  const [titleDraft, setTitleDraft] = useState('')
  const [extractionDraft, setExtractionDraft] = useState('')
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [saved, setSaved] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    setError('')
    setSaved(false)
    try {
      const [sup, providersData] = await Promise.all([
        workspaceApi.getSupervisorSettings(),
        workspaceApi.listProviders().catch(() => ({ providers: [] as Provider[] })),
      ])
      setSupervisor(sup)
      setVisionDraft(sup.vision_model ?? '')
      setCompactionDraft(sup.compaction_model ?? '')
      setTitleDraft(sup.title_model ?? '')
      setExtractionDraft(sup.extraction_model ?? '')
      setProviders(providersData.providers ?? [])
    } catch (e) { setError(message(e)) }
    finally { setLoading(false) }
  }, [])
  useEffect(() => { void load() }, [load])

  // One Save for the page: the vision model and the harness roles change
  // together in practice.
  const save = async () => {
    setBusy(true)
    setError('')
    setSaved(false)
    try {
      const sup = await workspaceApi.updateSupervisorSettings({
        // judge_model has plain-string semantics on PUT (an omitted field is
        // stored as empty): send the stored value back unchanged — the judge
        // is edited on the «Модель-супервайзер» page. The harness roles use
        // pointer semantics (an omitted field keeps its stored value) and the
        // HAB coefficients are omitted entirely — both stay untouched here.
        judge_model: supervisor?.judge_model ?? '',
        vision_model: visionDraft.trim(),
        compaction_model: compactionDraft.trim(),
        title_model: titleDraft.trim(),
        extraction_model: extractionDraft.trim(),
      })
      setSupervisor(sup)
      setVisionDraft(sup.vision_model ?? '')
      setCompactionDraft(sup.compaction_model ?? '')
      setTitleDraft(sup.title_model ?? '')
      setExtractionDraft(sup.extraction_model ?? '')
      setSaved(true)
    } catch (e) { setError(message(e)) }
    finally { setBusy(false) }
  }

  if (loading) return <PageLoading />

  return (
    <div style={{ padding: '1rem' }}>
      {error && <InlineNotification kind="error" title={t('action.error') ?? 'Error'} subtitle={error} onClose={() => setError('')} lowContrast style={{ marginBottom: '1rem' }} />}
      {saved && <InlineNotification kind="success" title={t('quality.saved') ?? 'Saved'} hideCloseButton lowContrast style={{ marginBottom: '1rem' }} />}

      <Heading>{t('harness.title') ?? 'Harness'}</Heading>
      <p style={{ color: 'var(--tm-text-3)', fontSize: '0.8rem', margin: '0.25rem 0 1rem', maxWidth: '46rem' }}>{t('harness.subtitle') ?? ''}</p>

      <div style={{ maxWidth: '40rem' }}>
        <Select id="supervisor-vision-model" labelText={t('quality.vision_model') ?? 'Vision model (image fallback)'} value={visionDraft} onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setVisionDraft(e.target.value)}>
          <SelectItem value="" text={t('quality.model_off') ?? '— main model —'} />
          {providers.flatMap((p) => (p.models ?? []).map((m) => (
            <SelectItem key={`v/${p.id}/${m}`} value={m} text={`${m} · ${p.name}`} />
          )))}
        </Select>
        <p style={{ fontSize: '0.75rem', color: 'var(--tm-text-3)', margin: '0.25rem 0 0' }}>{t('quality.vision_model_hint') ?? ''}</p>

        {/* Harness model roles (docs/harness-settings.md): the models that
            serve the harness's own background work. Empty = the role's
            cascade fallback (judge model, then the default model). */}
        <div style={{ marginTop: '1.25rem' }}>
          <div style={{ fontSize: '0.8rem', fontWeight: 600, marginBottom: '0.35rem' }}>{t('quality.roles_title') ?? 'Harness models'}</div>
          <p style={{ fontSize: '0.72rem', color: 'var(--tm-text-3)', margin: '0 0 0.5rem' }}>{t('quality.roles_hint') ?? ''}</p>
          <Select id="harness-compaction-model" labelText={t('quality.compaction_model') ?? 'Compaction model (state card)'} value={compactionDraft} onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setCompactionDraft(e.target.value)}>
            <SelectItem value="" text={t('quality.model_cascade') ?? '— cascade fallback —'} />
            {providers.flatMap((p) => (p.models ?? []).map((m) => (
              <SelectItem key={`c/${p.id}/${m}`} value={m} text={`${m} · ${p.name}`} />
            )))}
          </Select>
          <Select id="harness-title-model" labelText={t('quality.title_model') ?? 'Run title model'} value={titleDraft} onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setTitleDraft(e.target.value)} style={{ marginTop: '0.6rem' }}>
            <SelectItem value="" text={t('quality.model_cascade') ?? '— cascade fallback —'} />
            {providers.flatMap((p) => (p.models ?? []).map((m) => (
              <SelectItem key={`t/${p.id}/${m}`} value={m} text={`${m} · ${p.name}`} />
            )))}
          </Select>
          <Select id="harness-extraction-model" labelText={t('quality.extraction_model') ?? 'Knowledge extraction model'} value={extractionDraft} onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setExtractionDraft(e.target.value)} style={{ marginTop: '0.6rem' }}>
            <SelectItem value="" text={t('quality.model_cascade') ?? '— cascade fallback —'} />
            {providers.flatMap((p) => (p.models ?? []).map((m) => (
              <SelectItem key={`e/${p.id}/${m}`} value={m} text={`${m} · ${p.name}`} />
            )))}
          </Select>
        </div>

        {/* Effective harness knobs (env-resolved, read-only): one place to
            see what the kernel actually runs with. */}
        {supervisor?.harness && (
          <div style={{ marginTop: '1.25rem' }}>
            <div style={{ fontSize: '0.8rem', fontWeight: 600, marginBottom: '0.35rem' }}>{t('quality.harness_title') ?? 'Effective harness settings'}</div>
            <p style={{ fontSize: '0.72rem', color: 'var(--tm-text-3)', margin: '0 0 0.5rem' }}>{t('quality.harness_hint') ?? ''}</p>
            <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(180px, 1fr))', gap: '0.25rem 0.8rem', fontSize: '0.72rem', color: 'var(--tm-text-2)' }}>
              {Object.entries(supervisor.harness.budgets ?? {}).map(([key, value]) => (
                <div key={key} style={{ display: 'flex', justifyContent: 'space-between', gap: '0.5rem' }}>
                  <code style={{ fontFamily: 'var(--tm-font-mono)', fontSize: '0.68rem' }}>{key}</code>
                  <span>{value}</span>
                </div>
              ))}
            </div>
          </div>
        )}

        <div className="form-actions">
          <Button renderIcon={Save} disabled={busy || !supervisor} onClick={() => void save()}>
            {busy ? (t('common.saving') ?? 'Saving…') : (t('action.save') ?? 'Save')}
          </Button>
          <Button kind="secondary" renderIcon={Renew} disabled={busy} onClick={() => void load()}>{t('action.refresh') ?? 'Refresh'}</Button>
        </div>
      </div>
    </div>
  )
}
