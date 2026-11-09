import { useCallback, useEffect, useState } from 'react'
import { Button, Select, SelectItem, InlineNotification, Heading, NumberInput, Tag } from '@carbon/react'
import { Save, Renew } from '@carbon/icons-react'
import PageLoading from './PageLoading'
import { workspaceApi, type Provider, type SupervisorSettings } from './workspaceApi'
import { HARNESS_KNOBS, formatBytes, validateBudgetDrafts, budgetEdits } from './harnessBudgets'
import { useI18n, useT } from './i18n'

function message(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }

/** «Харнес» (docs/harness-settings.md): the vision model and the harness model
 *  roles (compaction card, run titles, knowledge extraction), plus the
 *  editable budget knobs — env is the base layer, saved overrides reach new
 *  runs, reset returns a knob to env/default. The judge model and the HAB
 *  coefficients live on the «Модель-супервайзер» page; both pages write the
 *  same supervisor API. */
export default function Harness() {
  const t = useT()
  const { locale } = useI18n()
  const [supervisor, setSupervisor] = useState<SupervisorSettings | null>(null)
  const [providers, setProviders] = useState<Provider[]>([])
  const [visionDraft, setVisionDraft] = useState('')
  const [compactionDraft, setCompactionDraft] = useState('')
  const [titleDraft, setTitleDraft] = useState('')
  const [extractionDraft, setExtractionDraft] = useState('')
  // Budget drafts: raw input strings plus the set of knobs with a staged
  // null-reset (the PUT sends a number to save an override, null to reset).
  const [budgetDrafts, setBudgetDrafts] = useState<Record<string, string>>({})
  const [stagedResets, setStagedResets] = useState<ReadonlySet<string>>(new Set())
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [saved, setSaved] = useState(false)

  const syncBudgetDrafts = useCallback((sup: SupervisorSettings) => {
    const budgets = sup.harness?.budgets ?? {}
    const drafts: Record<string, string> = {}
    for (const knob of HARNESS_KNOBS) drafts[knob.key] = String(budgets[knob.key] ?? '')
    setBudgetDrafts(drafts)
    setStagedResets(new Set())
  }, [])

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
      syncBudgetDrafts(sup)
      setProviders(providersData.providers ?? [])
    } catch (e) { setError(message(e)) }
    finally { setLoading(false) }
  }, [syncBudgetDrafts])
  useEffect(() => { void load() }, [load])

  const effectiveBudgets = supervisor?.harness?.budgets ?? {}
  const budgetErrors = validateBudgetDrafts(budgetDrafts, effectiveBudgets)
  const hasBudgetErrors = Object.keys(budgetErrors).length > 0

  // Origin chip: a saved override wins; without one the effective value
  // differing from the compile-time default means env is the base layer.
  const originOf = (key: string): 'saved' | 'env' | 'default' => {
    if (supervisor?.harness_saved?.includes(key)) return 'saved'
    const effective = supervisor?.harness?.budgets?.[key]
    const fallback = supervisor?.harness_defaults?.[key]
    return effective !== undefined && fallback !== undefined && effective !== fallback ? 'env' : 'default'
  }

  const toggleReset = (key: string) => setStagedResets((prev) => {
    const next = new Set(prev)
    if (next.has(key)) next.delete(key)
    else next.add(key)
    return next
  })

  // One Save for the page: the vision model, the harness roles and the
  // budget knobs change together in practice.
  const save = async () => {
    setBusy(true)
    setError('')
    setSaved(false)
    try {
      const harness = budgetEdits(budgetDrafts, effectiveBudgets, stagedResets)
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
        // Budget overrides: changed keys as numbers, staged resets as nulls,
        // untouched keys omitted entirely.
        ...(Object.keys(harness).length > 0 ? { harness } : {}),
      })
      setSupervisor(sup)
      setVisionDraft(sup.vision_model ?? '')
      setCompactionDraft(sup.compaction_model ?? '')
      setTitleDraft(sup.title_model ?? '')
      setExtractionDraft(sup.extraction_model ?? '')
      syncBudgetDrafts(sup)
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

        {/* Harness budgets (docs/harness-settings.md §6): env is the base
            layer, a saved override applies to new runs only, and a reset
            returns the knob to env/default. */}
        {supervisor?.harness && (
          <div style={{ marginTop: '1.25rem' }}>
            <div style={{ fontSize: '0.8rem', fontWeight: 600, marginBottom: '0.35rem' }}>{t('quality.harness_title') ?? 'Effective harness settings'}</div>
            <p style={{ fontSize: '0.72rem', color: 'var(--tm-text-3)', margin: '0 0 0.25rem' }}>{t('quality.harness_hint') ?? ''}</p>
            <p style={{ fontSize: '0.72rem', color: 'var(--tm-warning, var(--tm-text-3))', margin: '0 0 0.5rem' }}>⚠ {t('quality.harness_new_runs_hint') ?? ''}</p>
            <div>
              {HARNESS_KNOBS.map((knob) => {
                const error_ = budgetErrors[knob.key]
                const staged = stagedResets.has(knob.key)
                const origin = originOf(knob.key)
                const raw = (budgetDrafts[knob.key] ?? '').trim()
                const readout = knob.kind === 'bytes' && /^\d+$/.test(raw) ? `≈ ${formatBytes(Number(raw), locale)}` : ''
                return (
                  <div key={knob.key} style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 1fr) auto', gap: '0.35rem 1rem', alignItems: 'start', borderTop: '1px solid var(--tm-border)', padding: '0.55rem 0' }}>
                    <div>
                      <div style={{ fontSize: '0.78rem', fontWeight: 600 }}>{t(`quality.harness_knob_${knob.key}`)}</div>
                      <div style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)', marginTop: '0.1rem' }}>{t(`quality.harness_knob_${knob.key}_hint`)}</div>
                      {error_ && <div style={{ fontSize: '0.68rem', color: 'var(--tm-danger)', marginTop: '0.15rem' }}>{t(`quality.harness_err_${error_.code}`, error_.vars)}</div>}
                    </div>
                    <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', flexWrap: 'wrap', justifyContent: 'flex-end' }}>
                      <NumberInput
                        id={`harness-knob-${knob.key}`}
                        size="sm"
                        hideSteppers
                        hideLabel
                        label={t(`quality.harness_knob_${knob.key}`)}
                        value={budgetDrafts[knob.key] ?? ''}
                        min={1}
                        max={knob.ceiling}
                        invalid={!!error_}
                        disabled={staged}
                        style={{ width: '9rem' }}
                        onChange={(_event, { value }) => setBudgetDrafts((prev) => ({ ...prev, [knob.key]: String(value) }))}
                      />
                      {readout && <span style={{ fontSize: '0.72rem', color: 'var(--tm-text-3)', whiteSpace: 'nowrap' }}>{readout}</span>}
                      <Tag type={origin === 'saved' ? 'purple' : origin === 'env' ? 'warm-gray' : 'gray'} size="sm">{t(`quality.harness_origin_${origin}`)}</Tag>
                      {staged && <Tag type="red" size="sm">{t('quality.harness_reset_pending')}</Tag>}
                      {origin === 'saved' && (
                        <Button kind="danger--ghost" size="sm" disabled={busy} onClick={() => toggleReset(knob.key)}>
                          {staged ? t('quality.harness_reset_undo') : t('quality.harness_reset')}
                        </Button>
                      )}
                    </div>
                  </div>
                )
              })}
            </div>
          </div>
        )}

        <div className="form-actions">
          <Button renderIcon={Save} disabled={busy || !supervisor || hasBudgetErrors} onClick={() => void save()}>
            {busy ? (t('common.saving') ?? 'Saving…') : (t('action.save') ?? 'Save')}
          </Button>
          <Button kind="secondary" renderIcon={Renew} disabled={busy} onClick={() => void load()}>{t('action.refresh') ?? 'Refresh'}</Button>
        </div>
      </div>
    </div>
  )
}
