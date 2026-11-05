import { useEffect, useState } from 'react'
import { Button, Heading, InlineNotification, Tag, TextArea, TextInput } from '@carbon/react'
import AppModal from './Modal'
import GeneratingState from './GeneratingState'
import { useConfirm } from './ConfirmDialog'
import {
  applyEvolutionProposal,
  evolutionProposalDiff,
  proposalFromText,
  rejectEvolutionProposal,
  addAgentCheckCase,
  type ProposalContext,
  type VersionDiff,
} from './kernelApi'
import { promptDiffTone } from './agentEvolution'
import { useT } from './i18n'

function message(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }

const commaList = (value: string): string[] =>
  value.split(',').map((s) => s.trim()).filter(Boolean)

/** Structural + compiled-prompt diff body (docs/agent-evolution.md §4): what
 * changes in the definition and «what changes in the agent's head». Shared by
 * the proposal review modal and the version-compare modal. */
export function DefinitionDiffView({ diff }: { diff: VersionDiff }) {
  const t = useT()
  const fieldLabel = (field: string) => {
    const key = `agents.diff_field_${field}`
    const label = t(key)
    return label === key ? field : label
  }
  const kindLabel = (kind: string) =>
    kind === 'added' ? (t('agents.diff_added') ?? 'added')
      : kind === 'removed' ? (t('agents.diff_removed') ?? 'removed')
        : (t('agents.diff_changed') ?? 'changed')
  return (
    <div>
      {diff.redacted && (
        <p style={{ fontSize: '0.72rem', color: 'var(--tm-amber)', margin: '0 0 0.5rem' }}>
          {t('agents.redacted_note') ?? 'Some values were masked.'}
        </p>
      )}
      <p className="cds--label" style={{ margin: '0 0 0.25rem' }}>{t('agents.diff_structure') ?? 'Definition changes'}</p>
      {diff.definition_changes.length === 0 ? (
        <p style={{ fontSize: '0.78rem', color: 'var(--tm-text-3)', margin: '0 0 0.75rem' }}>{t('agents.diff_empty') ?? 'No changes.'}</p>
      ) : (
        <div style={{ display: 'flex', flexDirection: 'column', gap: '0.35rem', marginBottom: '0.75rem' }}>
          {diff.definition_changes.map((change, i) => (
            <div key={i} style={{ border: '1px solid var(--tm-border)', borderRadius: '6px', padding: '0.35rem 0.6rem', fontSize: '0.78rem' }}>
              <div style={{ display: 'flex', gap: '0.4rem', alignItems: 'center', flexWrap: 'wrap' }}>
                <strong>{fieldLabel(change.field)}</strong>
                <Tag size="sm" type={change.kind === 'added' ? 'green' : change.kind === 'removed' ? 'red' : 'blue'}>{kindLabel(change.kind)}</Tag>
                {change.redacted && <Tag size="sm" type="warm-gray">{t('agents.redacted_tag') ?? 'masked'}</Tag>}
              </div>
              <div style={{ marginTop: '0.15rem', color: 'var(--tm-text-2)', overflowWrap: 'anywhere' }}>
                {change.kind === 'changed' && change.from !== undefined && (
                  <span style={{ textDecoration: 'line-through', color: 'var(--tm-text-3)' }}>{change.from} → </span>
                )}
                {change.value}
              </div>
            </div>
          ))}
        </div>
      )}
      <p className="cds--label" style={{ margin: '0 0 0.25rem' }}>{t('agents.diff_prompt') ?? 'Compiled prompt diff'}</p>
      {diff.prompt_diff.length === 0 ? (
        <p style={{ fontSize: '0.78rem', color: 'var(--tm-text-3)', margin: 0 }}>{t('agents.diff_empty') ?? 'No changes.'}</p>
      ) : (
        <pre style={{ background: 'var(--tm-elevated)', border: '1px solid var(--tm-border)', borderRadius: '6px', padding: '0.5rem 0.75rem', fontFamily: 'var(--tm-mono, monospace)', fontSize: '0.72rem', whiteSpace: 'pre-wrap', wordBreak: 'break-word', maxHeight: '18rem', overflow: 'auto', margin: 0 }}>
          {diff.prompt_diff.map((line, i) => {
            const tone = promptDiffTone(line)
            return (
              <div key={i} style={{ color: tone === 'add' ? 'var(--tm-success, #9ece6a)' : tone === 'del' ? 'var(--tm-danger, #f7768e)' : 'var(--tm-text-3)' }}>
                {line}
              </div>
            )
          })}
        </pre>
      )}
    </div>
  )
}

/** Proposal review modal (§6.2): the behavioral change_summary plus the
 * structural and prompt diffs, with the gate-free Apply/Reject decisions.
 * The diff is fetched on open — one modal serves the inbox, the from-text
 * draft review and the run-context flow. */
export function ProposalDiffModal({ agentId, proposalId, changeSummary, canDecide = true, onClose, onDecided }: {
  agentId: string
  proposalId: number
  changeSummary?: string
  canDecide?: boolean
  onClose: () => void
  onDecided?: (outcome: { kind: 'applied' | 'rejected'; version?: number }) => void
}) {
  const t = useT()
  const { confirmDialog, dialog: confirmDialogNode } = useConfirm()
  const [diff, setDiff] = useState<VersionDiff | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => {
    let cancelled = false
    evolutionProposalDiff(agentId, proposalId)
      .then((data) => { if (!cancelled) setDiff(data) })
      .catch((f) => { if (!cancelled) setError(message(f)) })
    return () => { cancelled = true }
  }, [agentId, proposalId])

  const apply = async () => {
    if (!(await confirmDialog({
      title: t('agents.proposal_apply_confirm_title') ?? 'Apply proposal?',
      body: t('agents.proposal_apply_confirm') ?? 'The definition version will increase and a snapshot will be stored.',
      confirmLabel: t('agents.proposal_apply') ?? 'Apply',
    }))) return
    setBusy(true); setError('')
    try {
      const receipt = await applyEvolutionProposal(agentId, proposalId)
      onDecided?.({ kind: 'applied', version: receipt.version })
      onClose()
    } catch (f) { setError(message(f)) }
    finally { setBusy(false) }
  }

  const reject = async () => {
    if (!(await confirmDialog({
      title: t('agents.proposal_reject_confirm_title') ?? 'Reject proposal?',
      body: t('agents.proposal_reject_confirm') ?? 'The proposal stays in history and can never be applied.',
      danger: true,
      confirmLabel: t('agents.proposal_reject') ?? 'Reject',
    }))) return
    setBusy(true); setError('')
    try {
      await rejectEvolutionProposal(agentId, proposalId)
      onDecided?.({ kind: 'rejected' })
      onClose()
    } catch (f) { setError(message(f)) }
    finally { setBusy(false) }
  }

  return (
    <AppModal onClose={onClose} panelStyle={{ width: '640px', maxHeight: '85vh', display: 'flex', flexDirection: 'column' }}>
      {confirmDialogNode}
      <Heading style={{ fontSize: '1.05rem' }}>{t('agents.proposal_diff_title', { id: String(proposalId) }) ?? `Proposal #${proposalId}`}</Heading>
      <div className="modal-scroll" style={{ marginTop: '0.75rem' }}>
        {error && <InlineNotification kind="error" title={t('action.error') ?? 'Error'} subtitle={error} onClose={() => setError('')} lowContrast style={{ marginBottom: '0.75rem' }} />}
        <div style={{ marginBottom: '0.75rem' }}>
          <p className="cds--label" style={{ margin: '0 0 0.25rem' }}>{t('agents.change_summary') ?? 'What changes'}</p>
          <p style={{ margin: 0, fontSize: '0.85rem', whiteSpace: 'pre-wrap' }}>
            {(diff?.change_summary ?? changeSummary) || <span style={{ color: 'var(--tm-text-3)' }}>{t('agents.none') ?? '(none)'}</span>}
          </p>
        </div>
        {diff ? (
          <DefinitionDiffView diff={diff} />
        ) : error ? null : (
          <p style={{ color: 'var(--tm-text-3)', fontSize: '0.8rem' }}>{t('action.loading') ?? 'Loading…'}</p>
        )}
      </div>
      {canDecide && (
        <div className="form-actions" style={{ marginTop: '0.75rem' }}>
          <Button kind="secondary" disabled={busy} onClick={() => void reject()}>{t('agents.proposal_reject') ?? 'Reject'}</Button>
          <Button disabled={busy} onClick={() => void apply()}>{t('agents.proposal_apply') ?? 'Apply'}</Button>
        </div>
      )}
    </AppModal>
  )
}

/** «Предложить улучшение» dialog (§6.3): free-text problem → from-text LLM
 * draft → diff review. With a run context attached (the run view) it also
 * offers «сохранить как пример» prefilled from the run prompt (§6.6). */
export function ImproveAgentDialog({ agentId, context, runPrompt, onClose, onChanged }: {
  agentId: string
  context?: ProposalContext
  runPrompt?: string
  onClose: () => void
  onChanged?: () => void
}) {
  const t = useT()
  const [problem, setProblem] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [draft, setDraft] = useState<{ proposalId: number; changeSummary: string } | null>(null)
  // "Save as example" (only with a run context): the input prefills from the
  // run prompt, the patterns stay empty for the human to fill.
  const [example, setExample] = useState({
    input: runPrompt ?? '',
    must: '',
    mustNot: '',
  })
  const [exampleBusy, setExampleBusy] = useState(false)
  const [exampleDone, setExampleDone] = useState(false)

  const submit = async () => {
    if (!problem.trim()) return
    setBusy(true); setError('')
    try {
      const created = await proposalFromText(agentId, { problem: problem.trim(), ...(context ? { context } : {}) })
      setDraft({ proposalId: created.proposal_id, changeSummary: created.change_summary })
      onChanged?.()
    } catch (f) { setError(message(f)) }
    finally { setBusy(false) }
  }

  const saveExample = async () => {
    if (!example.input.trim() || !context?.run_id) return
    setExampleBusy(true); setError('')
    try {
      await addAgentCheckCase(agentId, {
        input: example.input.trim(),
        must_contain: commaList(example.must),
        must_not_contain: commaList(example.mustNot),
        from_run_id: context.run_id,
      })
      setExampleDone(true)
      onChanged?.()
    } catch (f) { setError(message(f)) }
    finally { setExampleBusy(false) }
  }

  return (
    <>
      <AppModal onClose={onClose} panelStyle={{ width: '560px', maxHeight: '85vh', display: 'flex', flexDirection: 'column' }}>
        {busy ? (
          <GeneratingState
            title={t('agents.improve_drafting') ?? 'Drafting the proposal…'}
            hint={t('agents.improve_drafting_hint') ?? 'The agent analyses the problem and prepares a definition change with a behavioral summary. This usually takes less than a minute.'}
          />
        ) : (
          <>
            <Heading style={{ fontSize: '1.05rem' }}>{t('agents.improve_dialog_title') ?? 'Suggest an improvement to the agent'}</Heading>
            <p style={{ color: 'var(--tm-text-3)', fontSize: '0.8rem', margin: '0.25rem 0 0.75rem' }}>
              {t('agents.improve_dialog_hint') ?? 'Describe the problem in free words: «the agent does X, but it should do Y». A draft proposal with a diff will be prepared for review — it lands in the proposals inbox either way.'}
            </p>
            {error && <InlineNotification kind="error" title={t('action.error') ?? 'Error'} subtitle={error} onClose={() => setError('')} lowContrast style={{ marginBottom: '0.75rem' }} />}
            <div className="modal-scroll">
              <TextArea
                id="improve-problem"
                hideLabel
                labelText=""
                rows={5}
                autoFocus
                placeholder={t('agents.improve_problem_ph') ?? 'The agent does X, but it should do Y…'}
                value={problem}
                onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => setProblem(e.target.value)}
              />
              {context?.run_id && (
                <div style={{ marginTop: '1rem', borderTop: '1px solid var(--tm-border)', paddingTop: '0.75rem' }}>
                  <div className="skill-subheading" style={{ margin: '0 0 0.35rem' }}>{t('agents.save_example_title') ?? 'Save as a control example'}</div>
                  <p style={{ fontSize: '0.75rem', color: 'var(--tm-text-3)', margin: '0 0 0.5rem' }}>
                    {t('agents.save_example_hint') ?? 'A control example: the input plus phrases the answer must (or must not) contain. Saved from this run.'}
                  </p>
                  {exampleDone ? (
                    <p style={{ fontSize: '0.8rem', color: 'var(--tm-success, #9ece6a)', margin: '0 0 0.5rem' }}>
                      {t('agents.save_example_saved') ?? 'Example saved.'}
                    </p>
                  ) : (
                    <div style={{ display: 'flex', flexDirection: 'column', gap: '0.5rem' }}>
                      <TextArea
                        id="improve-example-input"
                        hideLabel
                        labelText=""
                        rows={3}
                        placeholder={t('agents.save_example_input_ph') ?? 'Task for the agent…'}
                        value={example.input}
                        onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => setExample({ ...example, input: e.target.value })}
                      />
                      <TextInput
                        id="improve-example-must"
                        hideLabel
                        labelText=""
                        placeholder={t('agents.save_example_must_ph') ?? 'Answer must contain (comma-separated)'}
                        value={example.must}
                        onChange={(e: React.ChangeEvent<HTMLInputElement>) => setExample({ ...example, must: e.target.value })}
                      />
                      <TextInput
                        id="improve-example-must-not"
                        hideLabel
                        labelText=""
                        placeholder={t('agents.save_example_must_not_ph') ?? 'Answer must NOT contain (comma-separated)'}
                        value={example.mustNot}
                        onChange={(e: React.ChangeEvent<HTMLInputElement>) => setExample({ ...example, mustNot: e.target.value })}
                      />
                      <div>
                        <Button size="sm" kind="secondary" disabled={exampleBusy || !example.input.trim()} onClick={() => void saveExample()}>
                          {exampleBusy ? '…' : (t('agents.save_example_action') ?? 'Save example')}
                        </Button>
                      </div>
                    </div>
                  )}
                </div>
              )}
            </div>
            <div className="form-actions">
              <Button kind="secondary" disabled={busy} onClick={onClose}>{t('action.cancel') ?? 'Cancel'}</Button>
              <Button disabled={busy || !problem.trim()} onClick={() => void submit()}>
                {t('agents.improve_submit') ?? 'Prepare a draft'}
              </Button>
            </div>
          </>
        )}
      </AppModal>
      {draft && (
        <ProposalDiffModal
          agentId={agentId}
          proposalId={draft.proposalId}
          changeSummary={draft.changeSummary}
          onClose={() => { setDraft(null); onClose() }}
          onDecided={() => { onChanged?.() }}
        />
      )}
    </>
  )
}
