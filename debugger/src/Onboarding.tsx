import { useEffect, useState } from 'react'
import { Button, TextInput, Heading, Tag, Select, SelectItem } from '@carbon/react'
import { Add, Checkmark } from '@carbon/icons-react'
import { useT } from './i18n'
import { authHeaders, authToken, TOKEN_STORAGE_KEY } from './api'
import { setupStatus, login as apiLogin, type SetupStatus } from './kernelApi'
import { workspaceApi, type SandboxProfile } from './workspaceApi'
import type { BuiltinAgentSpec } from './workspaceApi'

interface OnboardingProps {
  onComplete: () => void
}

const STEP_KEYS = [
  { id: 'welcome', titleKey: 'onboarding.welcome', descKey: 'onboarding.welcome_desc' },
  { id: 'project', titleKey: 'onboarding.project_title', descKey: 'onboarding.project_desc' },
  { id: 'admin', titleKey: 'onboarding.admin_title', descKey: 'onboarding.admin_desc' },
  { id: 'provider', titleKey: 'onboarding.provider_title', descKey: 'onboarding.provider_desc' },
  { id: 'sandbox', titleKey: 'onboarding.sandbox_title', descKey: 'onboarding.sandbox_desc' },
  { id: 'agent', titleKey: 'onboarding.agent_title', descKey: 'onboarding.agent_desc' },
  { id: 'supervisor', titleKey: 'onboarding.supervisor_title', descKey: 'onboarding.supervisor_desc' },
  { id: 'done', titleKey: 'onboarding.done_title', descKey: 'onboarding.done_desc' },
] as const

type StepId = typeof STEP_KEYS[number]['id']

export default function Onboarding({ onComplete }: OnboardingProps) {
  const t = useT()
  const [step, setStep] = useState<StepId>('welcome')
  const [projectName, setProjectName] = useState('')
  const [adminName, setAdminName] = useState('')
  const [adminEmail, setAdminEmail] = useState('')
  const [adminPassword, setAdminPassword] = useState('')
  const [adminCreated, setAdminCreated] = useState(false)
  const [providerName, setProviderName] = useState('')
  const [providerUrl, setProviderUrl] = useState('')
  const [providerKey, setProviderKey] = useState('')
  const [discoveredModels, setDiscoveredModels] = useState<string[]>([])
  const [providerModel, setProviderModel] = useState('')
  const [checking, setChecking] = useState(false)
  const [checkNote, setCheckNote] = useState('')
  const [providerId, setProviderId] = useState('')
  // Sandbox: pick one of the builtin profiles (node/python/browser-e2e) and
  // build its image; the agent then binds to it at creation.
  const [profiles, setProfiles] = useState<SandboxProfile[]>([])
  const [sandboxProfile, setSandboxProfile] = useState('')
  const [buildBusy, setBuildBusy] = useState('')
  // Agent: a curated template + a name; the definition comes from the template.
  const [templates, setTemplates] = useState<BuiltinAgentSpec[]>([])
  const [templateSlug, setTemplateSlug] = useState('')
  const [agentName, setAgentName] = useState('')
  const [agentCreated, setAgentCreated] = useState(false)
  // Supervisor: the model that judges and supervises; defaults to the main one.
  const [supervisorModel, setSupervisorModel] = useState('')
  const [status, setStatus] = useState<SetupStatus | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const currentStep = STEP_KEYS.find((s) => s.id === step)!
  const modelOptions = discoveredModels.length > 0 ? discoveredModels : (providerModel ? [providerModel] : [])

  // The wizard needs the bootstrap status before the sandbox step too:
  // master_key_configured decides whether the provider key travels as an
  // encrypted secret reference or as a literal. Fetched once on mount and
  // refreshed on the sandbox step (the build button must reflect the actually
  // installed image even when the wizard is reopened).
  useEffect(() => {
    setupStatus().then(setStatus).catch(() => setStatus(null))
  }, [])
  // A token left in localStorage dies with the database it belonged to
  // (operator wiped the DB to re-run onboarding), and its 401s would break
  // the first gated call — the provider probe — while the anonymous steps
  // would 401 every later step; the login replaces it with a valid one.
  useEffect(() => {
    if (step !== 'admin') return
    if (authToken()) { try { localStorage.removeItem(TOKEN_STORAGE_KEY) } catch { /* private mode */ } }
  }, [step])

  // Sandbox profiles and agent templates load once (the wizard runs before
  // there is anything to filter by).
  useEffect(() => {
    if (step !== 'sandbox') return
    void workspaceApi.listSandboxProfiles()
      .then((data) => {
        const list = (data.profiles ?? []).filter((p) => p.builtin)
        setProfiles(list)
        // Default choice: the first profile whose image is already built,
        // else the generic node one.
        const built = list.find((p) => p.image)
        setSandboxProfile((current) => current || (built?.id ?? list[0]?.id ?? ''))
      })
      .catch(() => setProfiles([]))
  }, [step])

  useEffect(() => {
    if (step !== 'agent') return
    void workspaceApi.listBuiltinAgents()
      .then((data) => {
        const list = data.builtins ?? []
        setTemplates(list)
        setTemplateSlug((current) => current || list[0]?.slug || '')
      })
      .catch(() => setTemplates([]))
  }, [step])

  const selectedTemplate = templates.find((tpl) => tpl.slug === templateSlug)
  const selectedProfile = profiles.find((p) => p.id === sandboxProfile)

  const createProject = async () => {
    if (!projectName.trim()) return
    setBusy(true); setError('')
    try {
      await workspaceApi.createProject({ id: projectName.trim().toLowerCase().replace(/[^a-z0-9-]+/g, '-').replace(/^-+|-+$/g, ''), name: projectName.trim() } as never)
      setStep('admin')
    } catch (e) { setError(e instanceof Error ? e.message : 'Failed') }
    finally { setBusy(false) }
  }

  const createAdmin = async () => {
    if (!adminName.trim() || adminPassword.length < 8) return
    setBusy(true); setError('')
    try {
      await workspaceApi.createUser({ name: adminName.trim(), email: adminEmail.trim(), role: 'admin', password: adminPassword, active: true } as never)
      // Creating the first user enables auth immediately (the gate counts live
      // workspace users): the wizard must hold a token the kernel knows.
      // Always log in as the fresh admin — a stale token from a previous
      // install would 401 every later step; the login replaces it.
      const session = await apiLogin(adminName.trim(), adminPassword)
      localStorage.setItem(TOKEN_STORAGE_KEY, session.token)
      setAdminCreated(true)
      setStep('provider')
    } catch (e) { setError(e instanceof Error ? e.message : 'Failed') }
    finally { setBusy(false) }
  }

  // Check the typed key against the provider before creating anything: a
  // probe call both validates the credentials and discovers the model catalog
  // (the same auto-fetch the Providers page uses) — no manual model typing.
  const checkProvider = async () => {
    if (!providerUrl.trim()) return
    setChecking(true); setCheckNote('')
    try {
      const data = await workspaceApi.probeProviderModels({ base_url: providerUrl.trim(), api_key_ref: providerKey.trim() })
      const models = data.models ?? []
      setDiscoveredModels(models)
      setProviderModel((current) => (current && models.includes(current) ? current : models[0] ?? ''))
      setCheckNote(t('providers.fetched_n', { count: String(data.count ?? models.length) }) ?? `${data.count ?? models.length}`)
    } catch (e) {
      setDiscoveredModels([])
      setProviderModel('')
      setCheckNote(`${t('providers.fetch_failed') ?? 'Failed'}: ${e instanceof Error ? e.message : 'Request failed'}`)
    } finally { setChecking(false) }
  }

  const createProvider = async () => {
    if (!providerName.trim() || !providerUrl.trim()) return
    setBusy(true); setError('')
    try {
      // With a master key configured the key travels as a secret reference
      // (encrypted at rest); without one the provider stores the literal ref
      // as before — both resolve at the execution boundary.
      let apiKeyRef = providerKey.trim()
      if (providerKey.trim() && status?.master_key_configured) {
        const secretName = `provider-${providerName.trim().toLowerCase().replace(/[^a-z0-9-]+/g, '-')}-key`
        const created = await fetch('/kernel-api/v1/workspace/secrets', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json', ...authHeaders() },
          body: JSON.stringify({ name: secretName, provider: 'builtin', value: providerKey.trim() }),
        })
        if (!created.ok) throw new Error(await created.text())
        apiKeyRef = `secret://${secretName}`
      }
      const resp = await fetch('/kernel-api/v1/workspace/providers', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', ...authHeaders() },
        body: JSON.stringify({
          name: providerName.trim(),
          base_url: providerUrl.trim(),
          api_key_ref: apiKeyRef,
          models: discoveredModels,
        }),
      })
      if (!resp.ok) throw new Error(await resp.text())
      const created = await resp.json().catch(() => null)
      setProviderId(typeof created?.id === 'string' ? created.id : providerName.trim())
      setStep('sandbox')
    } catch (e) { setError(e instanceof Error ? e.message : 'Failed') }
    finally { setBusy(false) }
  }

  const buildProfile = async () => {
    if (!sandboxProfile) return
    setBuildBusy(sandboxProfile); setError('')
    try {
      await workspaceApi.buildSandboxProfile(sandboxProfile)
      const fresh = await setupStatus().catch(() => null)
      setStatus(fresh)
      const list = await workspaceApi.listSandboxProfiles().catch(() => ({ profiles: [] as SandboxProfile[] }))
      setProfiles(list.profiles ?? [])
    } catch (e) {
      // A client timeout does not stop the build: the server finishes it in
      // the background and the profile flips to «image ready» shortly.
      const timeout = e instanceof Error && e.message.includes('timed out')
      setError(timeout
        ? (t('onboarding.sandbox_timeout') ?? 'The build keeps running on the server — re-check the status in a minute.')
        : e instanceof Error ? e.message : 'Failed')
      if (timeout) {
        window.setTimeout(() => {
          void workspaceApi.listSandboxProfiles().then((data) => setProfiles(data.profiles ?? [])).catch(() => {})
        }, 60_000)
      }
    }
    finally { setBuildBusy('') }
  }

  const createAgent = async () => {
    if (!agentName.trim() || !selectedTemplate) return
    setBusy(true); setError('')
    try {
      await workspaceApi.createAgent({
        name: agentName.trim(),
        model: providerModel || modelOptions[0] || '',
        provider: providerId || providerName.trim(),
        description: selectedTemplate.description,
        definition: { ...selectedTemplate.definition },
        sandbox_profile: sandboxProfile || undefined,
        network_access: selectedTemplate.network_access,
        read_only: selectedTemplate.read_only,
      } as never)
      setAgentCreated(true)
      if (providerModel) setSupervisorModel(providerModel)
      setStep('supervisor')
    } catch (e) { setError(e instanceof Error ? e.message : 'Failed') }
    finally { setBusy(false) }
  }

  const saveSupervisor = async () => {
    if (!supervisorModel.trim()) { setStep('done'); return }
    setBusy(true); setError('')
    try {
      await workspaceApi.updateJudgeConfig(supervisorModel.trim())
      setStep('done')
    } catch (e) { setError(e instanceof Error ? e.message : 'Failed') }
    finally { setBusy(false) }
  }

  const skipStep = () => {
    if (step === 'provider') setStep('sandbox')
    else if (step === 'sandbox') setStep('agent')
    else if (step === 'agent') setStep('supervisor')
    else if (step === 'supervisor') setStep('done')
  }

  return (
    <div className="onboarding-overlay">
      <div className="onboarding-card">
        {/* Logo */}
        <div className="onboarding-logo">
          <img src="/temporality.svg" alt="Temporality" width={64} height={70} />
          <h1>Temporality</h1>
        </div>

        {/* Progress */}
        {step !== 'welcome' && step !== 'done' && (
          <div className="onboarding-progress">
            {STEP_KEYS.filter((s) => s.id !== 'welcome' && s.id !== 'done').map((s) => {
              const active = s.id === step
              const done = STEP_KEYS.findIndex((x) => x.id === step) > STEP_KEYS.findIndex((x) => x.id === s.id)
              return <div key={s.id} className={`progress-dot ${active ? 'active' : ''} ${done ? 'done' : ''}`} />
            })}
          </div>
        )}

        {/* Step content */}
        <div className="onboarding-step">
          <Heading>{t(currentStep.titleKey)}</Heading>
          <p className="onboarding-desc">{t(currentStep.descKey)}</p>

          {step === 'welcome' && (
            <div className="onboarding-actions">
              <Button onClick={() => setStep('project')} renderIcon={Add}>{t('onboarding.get_started')}</Button>
            </div>
          )}

          {step === 'project' && (
            <>
              <TextInput
                id="ob-project"
                labelText={t('onboarding.project_name')}
                value={projectName}
                onChange={(e: React.ChangeEvent<HTMLInputElement>) => setProjectName(e.target.value)}
                placeholder="e.g. my-team, backend-agents"
                onKeyDown={(e: React.KeyboardEvent) => e.key === 'Enter' && void createProject()}
              />
              {error && <p className="onboarding-error">{error}</p>}
              <div className="onboarding-actions">
                <Button onClick={() => void createProject()} disabled={busy || !projectName.trim()}>{t('onboarding.create_project')}</Button>
              </div>
            </>
          )}

          {step === 'admin' && (
            <>
              {adminCreated && (
                <Tag type="green" size="sm">{t('onboarding.admin_created')}</Tag>
              )}
              <TextInput
                id="ob-admin-name"
                labelText={t('onboarding.admin_name')}
                value={adminName}
                onChange={(e: React.ChangeEvent<HTMLInputElement>) => setAdminName(e.target.value)}
                placeholder="Ivan Petrov"
              />
              <TextInput
                id="ob-admin-email"
                labelText={t('onboarding.admin_email')}
                value={adminEmail}
                onChange={(e: React.ChangeEvent<HTMLInputElement>) => setAdminEmail(e.target.value)}
                placeholder="ivan@company.com"
              />
              <TextInput
                id="ob-admin-password"
                labelText={t('onboarding.admin_password')}
                type="password"
                value={adminPassword}
                onChange={(e: React.ChangeEvent<HTMLInputElement>) => setAdminPassword(e.target.value)}
                placeholder="≥ 8 chars"
              />
              <p className="onboarding-hint">{t('onboarding.admin_hint')}</p>
              {error && <p className="onboarding-error">{error}</p>}
              <div className="onboarding-actions">
                <Button kind="secondary" onClick={() => setStep('provider')}>{t('action.skip')}</Button>
                <Button onClick={() => void createAdmin()} disabled={busy || !adminName.trim() || adminPassword.length < 8}>{t('onboarding.admin_create')}</Button>
              </div>
            </>
          )}

          {step === 'provider' && (
            <>
              <TextInput
                id="ob-prov-name"
                labelText={t('providers.name')}
                value={providerName}
                onChange={(e: React.ChangeEvent<HTMLInputElement>) => setProviderName(e.target.value)}
                placeholder="e.g. openai, xiaomi, z-ai"
              />
              <TextInput
                id="ob-prov-url"
                labelText={t('providers.url')}
                value={providerUrl}
                onChange={(e: React.ChangeEvent<HTMLInputElement>) => setProviderUrl(e.target.value)}
                placeholder="https://api.openai.com/v1"
              />
              <TextInput
                id="ob-prov-key"
                labelText={t('providers.key')}
                type="password"
                value={providerKey}
                onChange={(e: React.ChangeEvent<HTMLInputElement>) => setProviderKey(e.target.value)}
                placeholder="sk-..."
              />
              <div>
                <Button size="sm" kind="secondary" renderIcon={Add} disabled={checking || !providerUrl.trim()} onClick={() => void checkProvider()}>
                  {checking ? (t('providers.fetching_models') ?? 'Discovering…') : (t('providers.check_and_fetch') ?? 'Check key & fetch models')}
                </Button>
                {checkNote && <p className="onboarding-hint" style={{ marginBottom: 0 }}>{checkNote}</p>}
              </div>
              {discoveredModels.length > 0 && (
                <Select
                  id="ob-prov-model"
                  labelText={t('onboarding.provider_default_model') ?? 'Default model'}
                  value={providerModel}
                  onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setProviderModel(e.target.value)}
                >
                  {discoveredModels.map((m) => <SelectItem key={m} value={m} text={m} />)}
                </Select>
              )}
              {status && !status.master_key_configured && (
                <p className="onboarding-hint">
                  {t('onboarding.master_key_hint') ?? 'Secrets are encrypted with an auto-generated key stored in the database. For production set KERNEL_SECRET_MASTER_KEY in .env so secrets survive a DB restore elsewhere.'}
                </p>
              )}
              {error && <p className="onboarding-error">{error}</p>}
              <div className="onboarding-actions">
                <Button kind="secondary" onClick={skipStep}>{t('action.skip')}</Button>
                <Button onClick={() => void createProvider()} disabled={busy || !providerName.trim() || !providerUrl.trim()}>{t('new.provider')}</Button>
              </div>
            </>
          )}

          {step === 'sandbox' && (
            <>
              {profiles.length === 0 && <p className="onboarding-hint">{t('onboarding.sandbox_checking') ?? 'Checking sandbox profiles…'}</p>}
              <div style={{ display: 'flex', flexDirection: 'column', gap: '0.4rem' }}>
                {profiles.map((p) => {
                  const selected = p.id === sandboxProfile
                  const built = Boolean(p.image)
                  return (
                    <div
                      key={p.id}
                      onClick={() => setSandboxProfile(p.id)}
                      style={{
                        display: 'flex', gap: '0.5rem', alignItems: 'flex-start', padding: '0.55rem 0.7rem',
                        border: `1px solid ${selected ? 'var(--tm-amber, #e6b85c)' : 'var(--tm-border)'}`,
                        borderRadius: '8px', cursor: 'pointer',
                        background: selected ? 'rgba(230,184,92,.07)' : 'transparent',
                      }}
                    >
                      <span style={{ paddingTop: '0.15rem' }}>{selected ? <Checkmark size={14} /> : <span style={{ display: 'inline-block', width: 14 }} />}</span>
                      <span style={{ display: 'flex', flexDirection: 'column', minWidth: 0, flex: 1 }}>
                        <span style={{ display: 'flex', gap: '0.4rem', alignItems: 'center', flexWrap: 'wrap' }}>
                          <strong style={{ fontSize: '0.85rem' }}>{p.name}</strong>
                          <Tag size="sm" type={built ? 'green' : 'gray'}>{built ? (t('onboarding.sandbox_built') ?? 'image ready') : (t('onboarding.sandbox_not_built') ?? 'needs a build')}</Tag>
                        </span>
                        <span style={{ fontSize: '0.72rem', color: 'var(--tm-text-3)' }}>{p.description}</span>
                      </span>
                    </div>
                  )
                })}
              </div>
              {selectedProfile && !selectedProfile.image && (
                <div className="onboarding-actions" style={{ marginTop: 0 }}>
                  <Button size="sm" kind="secondary" disabled={buildBusy !== ''} onClick={() => void buildProfile()}>
                    {buildBusy === sandboxProfile ? (t('onboarding.sandbox_building') ?? 'Building…') : (t('onboarding.sandbox_build') ?? 'Build image')}
                  </Button>
                  <span style={{ fontSize: '0.72rem', color: 'var(--tm-text-3)' }}>{t('onboarding.sandbox_build_note')}</span>
                </div>
              )}
              {status?.sandbox.pinned_by_env && (
                <p className="onboarding-hint">{t('onboarding.sandbox_pinned_hint')}</p>
              )}
              {error && <p className="onboarding-error">{error}</p>}
              <div className="onboarding-actions">
                <Button kind="secondary" onClick={skipStep}>{t('action.skip')}</Button>
                <Button onClick={() => setStep('agent')} disabled={!sandboxProfile}>{t('action.next') ?? 'Next'}</Button>
              </div>
            </>
          )}

          {step === 'agent' && (
            <>
              <div style={{ display: 'grid', gap: '0.4rem' }}>
                {templates.map((tpl) => {
                  const selected = tpl.slug === templateSlug
                  return (
                    <div
                      key={tpl.slug}
                      onClick={() => { setTemplateSlug(tpl.slug); setAgentName(tpl.name) }}
                      style={{
                        display: 'flex', gap: '0.5rem', alignItems: 'flex-start', padding: '0.55rem 0.7rem',
                        border: `1px solid ${selected ? 'var(--tm-amber, #e6b85c)' : 'var(--tm-border)'}`,
                        borderRadius: '8px', cursor: 'pointer',
                        background: selected ? 'rgba(230,184,92,.07)' : 'transparent',
                      }}
                    >
                      <span style={{ paddingTop: '0.15rem' }}>{selected ? <Checkmark size={14} /> : <span style={{ display: 'inline-block', width: 14 }} />}</span>
                      <span style={{ display: 'flex', flexDirection: 'column', minWidth: 0 }}>
                        <strong style={{ fontSize: '0.85rem' }}>{tpl.name}</strong>
                        <span style={{ fontSize: '0.72rem', color: 'var(--tm-text-3)' }}>{tpl.description}</span>
                      </span>
                    </div>
                  )
                })}
              </div>
              <TextInput
                id="ob-agent-name"
                labelText={t('onboarding.agent_name_label') ?? 'Agent name'}
                value={agentName}
                onChange={(e: React.ChangeEvent<HTMLInputElement>) => setAgentName(e.target.value)}
                placeholder={selectedTemplate?.name ?? 'Coder'}
              />
              {modelOptions.length > 0 && (
                <Select
                  id="ob-agent-model"
                  labelText={t('onboarding.provider_default_model') ?? 'Model'}
                  value={providerModel}
                  onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setProviderModel(e.target.value)}
                >
                  {modelOptions.map((m) => <SelectItem key={m} value={m} text={m} />)}
                </Select>
              )}
              {agentCreated && <Tag type="green" size="sm">{t('onboarding.agent_created') ?? 'Agent created'}</Tag>}
              {error && <p className="onboarding-error">{error}</p>}
              <div className="onboarding-actions">
                <Button kind="secondary" onClick={skipStep}>{t('action.skip')}</Button>
                <Button onClick={() => void createAgent()} disabled={busy || !agentName.trim() || !selectedTemplate}>{t('action.create')} {t('nav.agents').toLowerCase()}</Button>
              </div>
            </>
          )}

          {step === 'supervisor' && (
            <>
              <p className="onboarding-hint" style={{ marginTop: 0 }}>
                {t('onboarding.supervisor_hint') ?? 'The supervisor reviews runs, judges agent evolution and predicts knowledge conflicts. You can change it later in Settings.'}
              </p>
              {modelOptions.length > 0 ? (
                <Select
                  id="ob-supervisor-model"
                  labelText={t('onboarding.supervisor_model') ?? 'Supervisor model'}
                  value={supervisorModel}
                  onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setSupervisorModel(e.target.value)}
                >
                  {modelOptions.map((m) => <SelectItem key={m} value={m} text={m === providerModel ? `${m} (${t('onboarding.supervisor_same') ?? 'same as the main model'})` : m} />)}
                </Select>
              ) : (
                <p className="onboarding-hint">{t('onboarding.supervisor_skip_hint') ?? 'No model catalog discovered — the supervisor will use the runs’ model. You can pick one later.'}</p>
              )}
              {error && <p className="onboarding-error">{error}</p>}
              <div className="onboarding-actions">
                <Button kind="secondary" onClick={() => setStep('done')}>{t('action.skip')}</Button>
                <Button onClick={() => void saveSupervisor()} disabled={busy || !supervisorModel.trim()}>{t('action.save') ?? 'Save'}</Button>
              </div>
            </>
          )}

          {step === 'done' && (
            <>
              <ul className="onboarding-summary">
                <li>{t('onboarding.summary_project')}: {projectName}</li>
                {adminCreated && <li>{t('onboarding.summary_admin')}: {adminName}</li>}
                {providerId && <li>{t('onboarding.summary_provider')}: {providerName}</li>}
                {selectedProfile && <li>{t('onboarding.summary_sandbox')}: {selectedProfile.name}{selectedProfile.image ? '' : ` (${t('onboarding.sandbox_not_built') ?? 'not built yet'})`}</li>}
                {agentCreated && <li>{t('onboarding.summary_agent', { name: agentName }) ?? `Agent: ${agentName}`}</li>}
                {supervisorModel && <li>{t('onboarding.summary_supervisor') ?? 'Supervisor model'}: {supervisorModel}</li>}
              </ul>
              <div className="onboarding-actions">
                <Button onClick={onComplete}>{t('onboarding.open_workspace')}</Button>
              </div>
            </>
          )}
        </div>
      </div>
    </div>
  )
}
