import { useCallback, useEffect, useState } from 'react'
import { Button, Select, SelectItem, TextInput, InlineNotification, Heading } from '@carbon/react'
import { Save, Renew } from '@carbon/icons-react'
import PageLoading from './PageLoading'
import { workspaceApi, type Provider, type SupervisorSettings } from './workspaceApi'
import { useT } from './i18n'

function message(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }

/** «Модель-супервайзер» (docs/supervisor-model.md, docs/harness-settings.md
 *  «Модельные роли»): the judge model — the one that supervises (evaluations,
 *  knowledge conflicts, area summaries) — plus the human-attention estimate
 *  coefficients (HAB). The vision model and the harness model roles live on
 *  the «Харнес» page; both pages write the same supervisor API. */
export default function SupervisorModel() {
  const t = useT()
  const [supervisor, setSupervisor] = useState<SupervisorSettings | null>(null)
  const [providers, setProviders] = useState<Provider[]>([])
  const [judgeDraft, setJudgeDraft] = useState('')
  const [habDraft, setHabDraft] = useState({ approval: 45, answerBase: 20, readingWpm: 180 })
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
      setJudgeDraft(sup.judge_model ?? '')
      setHabDraft({
        approval: sup.hab.approval_seconds,
        answerBase: sup.hab.answer_base_seconds,
        readingWpm: sup.hab.reading_wpm,
      })
      setProviders(providersData.providers ?? [])
    } catch (e) { setError(message(e)) }
    finally { setLoading(false) }
  }, [])
  useEffect(() => { void load() }, [load])

  // One Save for the page: the judge model and the HAB coefficients change
  // together in practice. The dialog's save/validate flow — HAB falls back to
  // the defaults on empty input.
  const save = async () => {
    setBusy(true)
    setError('')
    setSaved(false)
    try {
      const sup = await workspaceApi.updateSupervisorSettings({
        judge_model: judgeDraft.trim(),
        // vision_model has plain-string semantics on PUT (an omitted field is
        // stored as empty): send the stored value back unchanged — the vision
        // role is edited on the «Харнес» page.
        vision_model: supervisor?.vision_model ?? '',
        hab: {
          approval_seconds: Number(habDraft.approval) || 45,
          answer_base_seconds: Number(habDraft.answerBase) || 20,
          reading_wpm: Number(habDraft.readingWpm) || 180,
        },
      })
      setSupervisor(sup)
      setJudgeDraft(sup.judge_model ?? '')
      setSaved(true)
    } catch (e) { setError(message(e)) }
    finally { setBusy(false) }
  }

  if (loading) return <PageLoading />

  return (
    <div style={{ padding: '1rem' }}>
      {error && <InlineNotification kind="error" title={t('action.error') ?? 'Error'} subtitle={error} onClose={() => setError('')} lowContrast style={{ marginBottom: '1rem' }} />}
      {saved && <InlineNotification kind="success" title={t('quality.saved') ?? 'Saved'} hideCloseButton lowContrast style={{ marginBottom: '1rem' }} />}

      <Heading>{t('quality.settings_title') ?? 'Supervisor model'}</Heading>
      <p style={{ color: 'var(--tm-text-3)', fontSize: '0.8rem', margin: '0.25rem 0 1rem', maxWidth: '46rem' }}>{t('quality.settings_hint') ?? ''}</p>

      {/* Two-column settings layout: the left column is the model ROLE, the
          right — the HAB estimate coefficients. A 40rem single column left
          the right half of the screen dead and the form read as a stub. */}
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(320px, 1fr))', gap: '1.5rem', alignItems: 'start', maxWidth: '72rem' }}>
        {/* The judge role (docs/harness-settings.md «Модельные роли»): the
            model that supervises — judging, knowledge conflicts, area
            summaries. Empty = judge off. */}
        <section style={{ display: 'flex', flexDirection: 'column', gap: '0.25rem' }}>
          <Select id="supervisor-judge-model" labelText={t('quality.judge_model') ?? 'Judge model'} value={judgeDraft} onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setJudgeDraft(e.target.value)}>
            <SelectItem value="" text={t('quality.model_off_judge') ?? '— off —'} />
            {providers.flatMap((p) => (p.models ?? []).map((m) => (
              <SelectItem key={`j/${p.id}/${m}`} value={m} text={`${m} · ${p.name}`} />
            )))}
          </Select>
          <p style={{ fontSize: '0.75rem', color: 'var(--tm-text-3)', margin: 0 }}>{t('quality.judge_model_hint') ?? ''}</p>
        </section>

        <section>
          <div style={{ fontSize: '0.8rem', fontWeight: 600, marginBottom: '0.35rem' }}>{t('quality.hab_title') ?? 'Human Attention Budget (estimates)'}</div>
          <p style={{ fontSize: '0.72rem', color: 'var(--tm-text-3)', margin: '0 0 0.5rem' }}>{t('quality.hab_hint') ?? ''}</p>
          <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(9rem, 1fr))', gap: '0.6rem' }}>
            <TextInput id="hab-approval" labelText={t('quality.hab_approval') ?? 'Approval, sec'} value={String(habDraft.approval)} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setHabDraft({ ...habDraft, approval: Number(e.target.value) })} />
            <TextInput id="hab-answer" labelText={t('quality.hab_answer') ?? 'Answer base, sec'} value={String(habDraft.answerBase)} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setHabDraft({ ...habDraft, answerBase: Number(e.target.value) })} />
            <TextInput id="hab-wpm" labelText={t('quality.hab_wpm') ?? 'Reading, wpm'} value={String(habDraft.readingWpm)} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setHabDraft({ ...habDraft, readingWpm: Number(e.target.value) })} />
          </div>
        </section>
      </div>

      <div className="form-actions" style={{ maxWidth: '72rem' }}>
        <Button renderIcon={Save} disabled={busy || !supervisor} onClick={() => void save()}>
          {busy ? (t('common.saving') ?? 'Saving…') : (t('action.save') ?? 'Save')}
        </Button>
        <Button kind="secondary" renderIcon={Renew} disabled={busy} onClick={() => void load()}>{t('action.refresh') ?? 'Refresh'}</Button>
      </div>
    </div>
  )
}
