import { useEffect, useState } from 'react'
import { Button, TextInput, Heading, Tag, Select, SelectItem } from '@carbon/react'
import { Add } from '@carbon/icons-react'
import { useT } from './i18n'
import { authHeaders, authToken, TOKEN_STORAGE_KEY } from './api'
import { setupStatus, buildSandbox, login as apiLogin, type SetupStatus } from './kernelApi'
import { workspaceApi } from './workspaceApi'

interface OnboardingProps {
  onComplete: () => void
}

const STEP_KEYS = [
  { id: 'welcome', titleKey: 'onboarding.welcome', descKey: 'onboarding.welcome_desc' },
  { id: 'project', titleKey: 'onboarding.project_title', descKey: 'onboarding.project_desc' },
  { id: 'admin', titleKey: 'onboarding.admin_title', descKey: 'onboarding.admin_desc' },
  { id: 'provider', titleKey: 'onboarding.provider_title', descKey: 'onboarding.provider_desc' },
  { id: 'agent', titleKey: 'onboarding.agent_title', descKey: 'onboarding.agent_desc' },
  { id: 'sandbox', titleKey: 'onboarding.sandbox_title', descKey: 'onboarding.sandbox_desc' },
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
  const [agentName, setAgentName] = useState('Coder')
  const [agentModel, setAgentModel] = useState('')
  const [status, setStatus] = useState<SetupStatus | null>(null)
  const [buildBusy, setBuildBusy] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const currentStep = STEP_KEYS.find((s) => s.id === step)!

  // The wizard needs the bootstrap status before the sandbox step too:
  // master_key_configured decides whether the provider key travels as an
  // encrypted secret reference or as a literal. Fetched once on mount and
  // refreshed on the sandbox step (the build button must reflect the actually
  // installed image even when the wizard is reopened).
  useEffect(() => {
    setupStatus().then(setStatus).catch(() => setStatus(null))
  }, [])
  useEffect(() => {
    if (step !== 'sandbox') return
    setupStatus().then(setStatus).catch(() => setStatus(null))
  }, [step])

  const createProject = async () => {
    if (!projectName.trim()) return
    setBusy(true); setError('')
    try {
      const resp = await fetch('/kernel-api/v1/workspace/projects', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', ...authHeaders() },
        body: JSON.stringify({ name: projectName.trim() }),
      })
      if (!resp.ok) throw new Error(await resp.text())
      setStep('admin')
    } catch (e) { setError(e instanceof Error ? e.message : 'Failed') }
    finally { setBusy(false) }
  }

  const createAdmin = async () => {
    if (!adminName.trim() || adminPassword.length < 8) return
    setBusy(true); setError('')
    try {
      const resp = await fetch('/kernel-api/v1/workspace/users', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', ...authHeaders() },
        body: JSON.stringify({
          name: adminName.trim(),
          email: adminEmail.trim(),
          role: 'admin',
          password: adminPassword,
          active: true,
        }),
      })
      if (!resp.ok) throw new Error(await resp.text())
      // Creating the first user enables auth immediately (Gate.Enabled counts
      // live workspace tokens): the anonymous wizard caller would get 401 on
      // the very next step. Log in as the new admin right away — the token
      // lands in localStorage, so authHeaders() picks it up without a reload
      // (a reload would restart the wizard and lose its state).
      if (!authToken()) {
        const result = await apiLogin(adminName.trim(), adminPassword)
        localStorage.setItem(TOKEN_STORAGE_KEY, result.token)
      }
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
      setProviderId(typeof created?.id === 'string' ? created.id : '')
      setAgentModel(providerModel || discoveredModels[0] || '')
      setStep('agent')
    } catch (e) { setError(e instanceof Error ? e.message : 'Failed') }
    finally { setBusy(false) }
  }

  const createAgent = async () => {
    if (!agentName.trim()) return
    setBusy(true); setError('')
    try {
      const resp = await fetch('/kernel-api/v1/workspace/agents', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', ...authHeaders() },
        body: JSON.stringify({
          name: agentName.trim(),
          model: agentModel.trim(),
          provider: providerId || providerName.trim(),
        }),
      })
      if (!resp.ok) throw new Error(await resp.text())
      setStep('sandbox')
    } catch (e) { setError(e instanceof Error ? e.message : 'Failed') }
    finally { setBusy(false) }
  }

  const buildSandboxImage = async () => {
    setBuildBusy(true); setError('')
    try {
      await buildSandbox()
      const fresh = await setupStatus().catch(() => null)
      setStatus(fresh)
    } catch (e) { setError(e instanceof Error ? e.message : 'Failed') }
    finally { setBuildBusy(false) }
  }

  const skipStep = () => {
    if (step === 'provider') setStep('agent')
    else if (step === 'agent') setStep('sandbox')
    else if (step === 'sandbox') setStep('done')
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
              {error && <p className="onboarding-error">{error}</p>}
              <div className="onboarding-actions">
                <Button kind="secondary" onClick={skipStep}>{t('action.skip')}</Button>
                <Button onClick={() => void createProvider()} disabled={busy || !providerName.trim() || !providerUrl.trim()}>{t('new.provider')}</Button>
              </div>
            </>
          )}

          {step === 'agent' && (
            <>
              <TextInput
                id="ob-agent-name"
                labelText={t('nav.agents') + ' ' + t('action.create').toLowerCase()}
                value={agentName}
                onChange={(e: React.ChangeEvent<HTMLInputElement>) => setAgentName(e.target.value)}
                placeholder="Coder"
              />
              <TextInput
                id="ob-agent-model"
                labelText="Model"
                value={agentModel}
                onChange={(e: React.ChangeEvent<HTMLInputElement>) => setAgentModel(e.target.value)}
                placeholder="gpt-4o"
              />
              {error && <p className="onboarding-error">{error}</p>}
              <div className="onboarding-actions">
                <Button kind="secondary" onClick={skipStep}>{t('action.skip')}</Button>
                <Button onClick={() => void createAgent()} disabled={busy || !agentName.trim()}>{t('action.create')} {t('nav.agents').toLowerCase()}</Button>
              </div>
            </>
          )}

          {step === 'sandbox' && (
            <>
              {!status && <p className="onboarding-hint">{t('onboarding.sandbox_checking')}</p>}
              {status?.sandbox.configured && status.sandbox.available && (
                <Tag type="green" size="sm">{t('onboarding.sandbox_ready')}</Tag>
              )}
              {status?.sandbox.configured && !status.sandbox.available && (
                <>
                  <Tag type="red" size="sm">{t('onboarding.sandbox_image_missing')}</Tag>
                  <p className="onboarding-hint">{status.sandbox.image}</p>
                </>
              )}
              {!status?.sandbox.configured && (
                <p className="onboarding-hint">{t('onboarding.sandbox_hint')}</p>
              )}
              {error && <p className="onboarding-error">{error}</p>}
              <div className="onboarding-actions">
                <Button kind="secondary" onClick={skipStep}>{t('action.skip')}</Button>
                <Button
                  onClick={() => void buildSandboxImage()}
                  disabled={buildBusy || (status?.sandbox.configured === true && status.sandbox.available)}
                >
                  {buildBusy ? t('onboarding.sandbox_building') : t('onboarding.sandbox_build')}
                </Button>
              </div>
              {buildBusy && <p className="onboarding-hint">{t('onboarding.sandbox_build_note')}</p>}
            </>
          )}

          {step === 'done' && (
            <>
              <ul className="onboarding-summary">
                <li>{t('onboarding.summary_project')}: {projectName}</li>
                {adminCreated && <li>{t('onboarding.summary_admin')}: {adminName}</li>}
                {providerId && <li>{t('onboarding.summary_provider')}: {providerName}</li>}
                {status?.sandbox.configured && status.sandbox.available && <li>{t('onboarding.summary_sandbox')}</li>}
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
