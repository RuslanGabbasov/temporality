import { useState } from 'react'
import { Button, TextInput, Heading } from '@carbon/react'
import { Add } from '@carbon/icons-react'
import { useT } from './i18n'

interface OnboardingProps {
  onComplete: () => void
}

const STEP_KEYS = [
  { id: 'welcome', titleKey: 'onboarding.welcome', descKey: 'onboarding.welcome_desc' },
  { id: 'project', titleKey: 'onboarding.project_title', descKey: 'onboarding.project_desc' },
  { id: 'provider', titleKey: 'onboarding.provider_title', descKey: 'onboarding.provider_desc' },
  { id: 'agent', titleKey: 'onboarding.agent_title', descKey: 'onboarding.agent_desc' },
  { id: 'done', titleKey: 'onboarding.done_title', descKey: 'onboarding.done_desc' },
] as const

type StepId = typeof STEP_KEYS[number]['id']

export default function Onboarding({ onComplete }: OnboardingProps) {
  const t = useT()
  const [step, setStep] = useState<StepId>('welcome')
  const [projectName, setProjectName] = useState('')
  const [providerName, setProviderName] = useState('')
  const [providerUrl, setProviderUrl] = useState('')
  const [providerKey, setProviderKey] = useState('')
  const [providerModels, setProviderModels] = useState('')
  const [agentName, setAgentName] = useState('Coder')
  const [agentModel, setAgentModel] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const currentStep = STEP_KEYS.find((s) => s.id === step)!

  const createProject = async () => {
    if (!projectName.trim()) return
    setBusy(true); setError('')
    try {
      const resp = await fetch('/kernel-api/v1/workspace/projects', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name: projectName.trim() }),
      })
      if (!resp.ok) throw new Error(await resp.text())
      setStep('provider')
    } catch (e) { setError(e instanceof Error ? e.message : 'Failed') }
    finally { setBusy(false) }
  }

  const createProvider = async () => {
    if (!providerName.trim() || !providerUrl.trim()) return
    setBusy(true); setError('')
    try {
      const resp = await fetch('/kernel-api/v1/workspace/providers', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          name: providerName.trim(),
          base_url: providerUrl.trim(),
          api_key_ref: providerKey.trim(),
          models: providerModels.split(',').map((m) => m.trim()).filter(Boolean),
        }),
      })
      if (!resp.ok) throw new Error(await resp.text())
      setAgentModel(providerModels.split(',')[0]?.trim() ?? '')
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
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          name: agentName.trim(),
          model: agentModel.trim(),
          provider: providerName.trim(),
        }),
      })
      if (!resp.ok) throw new Error(await resp.text())
      setStep('done')
    } catch (e) { setError(e instanceof Error ? e.message : 'Failed') }
    finally { setBusy(false) }
  }

  const skipStep = () => {
    if (step === 'provider') setStep('agent')
    else if (step === 'agent') setStep('done')
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
              <TextInput
                id="ob-prov-models"
                labelText={t('providers.models')}
                value={providerModels}
                onChange={(e: React.ChangeEvent<HTMLInputElement>) => setProviderModels(e.target.value)}
                placeholder="gpt-4o, gpt-4o-mini"
              />
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

          {step === 'done' && (
            <div className="onboarding-actions">
              <Button onClick={onComplete}>{t('onboarding.open_workspace')}</Button>
            </div>
          )}
        </div>
      </div>
    </div>
  )
}