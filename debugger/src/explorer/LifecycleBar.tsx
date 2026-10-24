import { useState } from 'react'
import { Button, InlineNotification, TextInput } from '@carbon/react'
import { observationApi } from '../observationApi'
import { useT } from '../i18n'
import { useOrgUnits, OrgUnitSelect } from '../orgUnits'

/** Lifecycle actions shared by the search card and the dossier (ТЗ §10.3):
 *  every state change goes through the existing challenge / invalidate /
 *  promote / correct mechanisms. The reason is a comment, not a toll: the
 *  human verdict itself is the decision, so an empty comment travels as a
 *  localized default ("актуальность снял человек") — a human who sees an
 *  outdated rule never has to write an essay before clicking. "Correct" is
 *  an invalidation that names the successor knowledge (replacement_id),
 *  which the server records as a supersession — the old statement stays as
 *  history and the replacement becomes the live rule. */
export default function LifecycleBar({ project, knowledgeID, onDone, onOpenDossier }: {
  project: string
  knowledgeID: string
  onDone: () => void
  onOpenDossier?: (id: string) => void
}) {
  const t = useT()
  const org = useOrgUnits()
  const [reason, setReason] = useState('')
  const [promoteScope, setPromoteScope] = useState('')
  const [replacement, setReplacement] = useState('')
  const [showCorrect, setShowCorrect] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  async function run(action: 'challenge' | 'invalidate' | 'promote' | 'correct') {
    if (busy) return
    if (action === 'correct' && !replacement.trim()) return
    setBusy(true); setError('')
    try {
      const actor = { id: 'human', type: 'human' as const }
      const comment = reason.trim() || defaultReason(action, t)
      if (action === 'challenge') {
        await observationApi.challenge({ knowledge_id: knowledgeID, project, actor, reason: comment })
      } else if (action === 'invalidate' || action === 'correct') {
        await observationApi.invalidate({
          knowledge_id: knowledgeID, project, actor,
          reason: comment, replacement_id: action === 'correct' ? replacement.trim() : undefined,
        })
      } else {
        await observationApi.promote({
          knowledge_id: knowledgeID, project, actor,
          scope_kind: promoteScope ? 'org_unit' : 'organization',
          scope_id: promoteScope || undefined,
          reason: comment,
        })
      }
      setReason(''); setReplacement(''); setShowCorrect(false)
      onDone()
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Request failed')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: '0.5rem' }}>
      {error && <InlineNotification kind="error" title="Error" subtitle={error} onClose={() => setError('')} lowContrast />}
      <div style={{ fontSize: '0.65rem', textTransform: 'uppercase', letterSpacing: '0.05em', color: 'var(--tm-muted)' }}>
        {t('explorer.lifecycle_heading') ?? 'Work with this knowledge'}
      </div>
      <TextInput
        id={`explorer-reason-${knowledgeID}`}
        size="sm" labelText="" hideLabel
        placeholder={t('explorer.lifecycle_reason_optional') ?? 'Comment (optional)'}
        value={reason} onChange={(e) => setReason(e.target.value)}
      />
      {/* Two action groups, visually separated: publish (with its scope) on
          the left, verdicts on the right. The scope select belongs to the
          promote action only, so it lives inside its group. Every button
          works without a comment — the click is the verdict. */}
      <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'center', flexWrap: 'wrap' }}>
        <Button size="sm" kind="primary" disabled={busy} onClick={() => void run('promote')}>{t('knowledge.promote') ?? 'Promote'}</Button>
        <OrgUnitSelect id={`explorer-promote-unit-${knowledgeID}`} org={org} value={promoteScope} onChange={setPromoteScope} allowUnassigned label={t('knowledge.promote_scope') ?? 'Visible to'} size="sm" hideLabel />
        <span aria-hidden style={{ width: '1px', height: '1.4rem', background: 'var(--tm-border)', flexShrink: 0 }} />
        <Button size="sm" kind="secondary" disabled={busy} onClick={() => void run('challenge')}>{t('runs.knowledge_challenge') ?? 'Challenge'}</Button>
        <Button size="sm" kind="secondary" disabled={busy} onClick={() => void run('invalidate')}>{t('runs.knowledge_invalidate') ?? 'Invalidate'}</Button>
        <Button size="sm" kind="secondary" disabled={busy || showCorrect} onClick={() => setShowCorrect(true)}>{t('explorer.lifecycle_correct') ?? 'Correct'}</Button>
      </div>
      {showCorrect && (
        <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'center', flexWrap: 'wrap' }}>
          <TextInput
            id={`explorer-replacement-${knowledgeID}`}
            size="sm" labelText="" hideLabel style={{ flex: 1, minWidth: '260px' }}
            placeholder={t('explorer.lifecycle_replacement') ?? 'Knowledge id of the replacement…'}
            value={replacement} onChange={(e) => setReplacement(e.target.value)}
          />
          {replacement.trim() && onOpenDossier && (
            <Button size="sm" kind="ghost" onClick={() => onOpenDossier(replacement.trim())}>{t('explorer.dossier_open') ?? 'Open dossier'}</Button>
          )}
          <Button size="sm" kind="secondary" disabled={busy || !replacement.trim()} onClick={() => void run('correct')}>
            {t('explorer.lifecycle_correct_apply') ?? 'Replace with successor'}
          </Button>
          <Button size="sm" kind="ghost" onClick={() => setShowCorrect(false)}>{t('action.cancel') ?? 'Cancel'}</Button>
        </div>
      )}
    </div>
  )
}

// defaultReason keeps the audit trail meaningful when the human just clicks:
// the journal invariant (every state change carries a reason) stays intact.
function defaultReason(action: 'challenge' | 'invalidate' | 'promote' | 'correct', t: (key: string) => string | undefined): string {
  const key = action === 'challenge' ? 'explorer.default_reason_challenge'
    : action === 'promote' ? 'explorer.default_reason_promote'
      : 'explorer.default_reason_invalidate'
  return t(key) ?? action
}
