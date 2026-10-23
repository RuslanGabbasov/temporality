import { useState } from 'react'
import { Button, TextInput, Heading } from '@carbon/react'
import AppModal from './Modal'
import { changePassword } from './kernelApi'
import { useT } from './i18n'

/** Self-service password change. An account that already has a local
 *  password re-enters it (the server re-checks; a stolen session cannot lock
 *  the owner out). An SSO-provisioned account without one leaves the field
 *  empty — the authenticated principal sets the first password, the same
 *  8-character bar applies, and the bearer token survives. */
export default function PasswordChangeDialog({ onClose }: { onClose: () => void }) {
  const t = useT()
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [repeat, setRepeat] = useState('')
  const [error, setError] = useState('')
  const [done, setDone] = useState(false)
  const [busy, setBusy] = useState(false)

  const mismatch = repeat !== '' && next !== repeat
  const tooShort = next !== '' && next.length < 8
  const canSubmit = next.length >= 8 && next === repeat && !busy

  const submit = async () => {
    setBusy(true)
    setError('')
    try {
      await changePassword(current, next)
      setDone(true)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <AppModal onClose={onClose} panelStyle={{ width: '420px' }}>
      <Heading style={{ fontSize: '1.05rem', marginBottom: '0.75rem' }}>{t('password.change_title') ?? 'Change password'}</Heading>
      {done ? (
        <>
          <p style={{ color: 'var(--tm-text-2)', fontSize: '0.85rem', margin: '0.5rem 0 1rem' }}>
            {t('password.changed') ?? 'Password changed. Use it on your next sign-in — the current session stays alive.'}
          </p>
          <div style={{ display: 'flex', justifyContent: 'flex-end' }}>
            <Button onClick={onClose}>{t('action.close') ?? 'Close'}</Button>
          </div>
        </>
      ) : (
        <>
          <div style={{ display: 'flex', flexDirection: 'column', gap: '0.6rem', marginBottom: '0.75rem' }}>
            <TextInput
              id="password-current"
              type="password"
              labelText={t('password.current_optional') ?? 'Current password (leave empty if none set)'}
              value={current}
              onChange={(e: React.ChangeEvent<HTMLInputElement>) => setCurrent(e.target.value)}
              autoComplete="current-password"
            />
            <TextInput
              id="password-next"
              type="password"
              labelText={t('password.new') ?? 'New password'}
              value={next}
              onChange={(e: React.ChangeEvent<HTMLInputElement>) => setNext(e.target.value)}
              invalid={tooShort}
              invalidText={tooShort ? (t('password.too_short') ?? 'At least 8 characters') : undefined}
              autoComplete="new-password"
            />
            <TextInput
              id="password-repeat"
              type="password"
              labelText={t('password.repeat') ?? 'Repeat new password'}
              value={repeat}
              onChange={(e: React.ChangeEvent<HTMLInputElement>) => setRepeat(e.target.value)}
              invalid={mismatch}
              invalidText={mismatch ? (t('password.mismatch') ?? 'Passwords do not match') : undefined}
              autoComplete="new-password"
            />
            {error && <div style={{ color: 'var(--tm-danger, #f7768e)', fontSize: '0.8rem' }}>{error}</div>}
          </div>
          <div style={{ display: 'flex', justifyContent: 'flex-end', gap: '0.5rem' }}>
            <Button kind="secondary" onClick={onClose} disabled={busy}>{t('action.cancel') ?? 'Cancel'}</Button>
            <Button onClick={() => void submit()} disabled={!canSubmit}>{busy ? (t('password.saving') ?? 'Saving…') : (t('action.save') ?? 'Save')}</Button>
          </div>
        </>
      )}
    </AppModal>
  )
}
