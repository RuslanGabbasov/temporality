import { useCallback, useEffect, useMemo, useState } from 'react'
import { Heading, InlineNotification, Tag, Tile, Button, Stack, TextInput, TextArea, Select, SelectItem } from '@carbon/react'
import { ChevronDown, ChevronRight } from '@carbon/icons-react'
import { projectBudget, sagaCreate, sagaRename, sagaMerge, sagaDelete, taskSagaBind, type ProjectBudget, type SagaRollup, type TaskRollup } from './kernelApi'
import ProjectRequired from './ProjectRequired'
import AppModal from './Modal'
import { useConfirm } from './ConfirmDialog'
import { useI18n, pluralT } from './i18n'
import { formatDuration, approvalHint } from './eventSummary'

function message(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }

interface Props {
  project: string
}

// Which management dialog is open (docs/sagas.md §8): rename/merge operate on
// sagas, attach/create bind an unbound task, detach needs no dialog.
type Dialog =
  | { kind: 'rename'; saga: SagaRollup }
  | { kind: 'merge'; sources: string[] }
  | { kind: 'attach'; task: TaskRollup }
  | { kind: 'create'; task: TaskRollup }

/**
 * Project overview (docs/sagas.md): the analysis page for everything tied to
 * one project — totals (time, tokens, cost, approvals), explicit sagas with
 * their bound tasks, and the tasks that live outside any saga. Moved here from
 * the Runs tab: runs are execution, this is accounting. Sagas are manageable
 * by hand here too: rename, merge duplicates, bind unbound tasks — the manual
 * half of docs/sagas.md §8, event-sourced like the agent tools.
 */
export default function ProjectOverview({ project }: Props) {
  const { t, locale } = useI18n()
  const [budget, setBudget] = useState<ProjectBudget | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [openSaga, setOpenSaga] = useState<string | null>(null)
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [dialog, setDialog] = useState<Dialog | null>(null)
  // Form state shared by the dialogs; reset on open.
  const [formTitle, setFormTitle] = useState('')
  const [formGoal, setFormGoal] = useState('')
  const [formSaga, setFormSaga] = useState('')
  const [actionError, setActionError] = useState('')
  const [actionBusy, setActionBusy] = useState(false)
  const { confirmDialog, dialog: confirmDialogNode } = useConfirm()

  // Operator hygiene (docs/sagas.md §8): a mistyped registration or a wrongly
  // merged saga is deleted — the bound tasks fall back to the unbound pool;
  // runs, tasks and journal events are untouched. Confirm-guarded: the
  // gesture is irreversible in the UI.
  const deleteSaga = async (saga: SagaRollup) => {
    const taskCount = (budget?.tasks ?? []).filter((task) => task.saga_id === saga.saga_id).length
    const ok = await confirmDialog({
      title: t('project.saga_delete_confirm', { name: saga.title || saga.saga_id }) ?? `Delete saga «${saga.title || saga.saga_id}»?`,
      body: taskCount > 0
        ? (t('project.saga_delete_body_tasks', { count: String(taskCount) }) ?? `${taskCount} bound task(s) will return to the unbound pool. Runs and journal history stay untouched.`)
        : (t('project.saga_delete_body') ?? 'The saga grouping is removed. Runs, tasks and journal history stay untouched.'),
      danger: true,
      confirmLabel: t('project.saga_delete') ?? 'Delete',
    })
    if (!ok) return
    await runAction(() => sagaDelete(project.trim(), saga.saga_id))
  }

  const load = useCallback(async () => {
    if (!project.trim()) return
    setBusy(true)
    setError('')
    try {
      const data = await projectBudget(project.trim())
      // Older kernels predate sagas: normalize so the page never sees nulls.
      setBudget({ ...data, sagas: data.sagas ?? [], tasks: data.tasks ?? [], approvals: data.approvals })
    } catch (failure) {
      setBudget(null)
      setError(message(failure))
    } finally {
      setBusy(false)
    }
  }, [project])

  useEffect(() => { void load() }, [load])

  const unbound = useMemo(() => (budget?.tasks ?? [])
    .filter((task) => !task.saga_id)
    .sort((a, b) => (b.first_started_at ?? '').localeCompare(a.first_started_at ?? '')), [budget])
  const [showAllUnbound, setShowAllUnbound] = useState(false)
  const unboundShown = 8
  const tasksOf = useCallback((sagaID: string) => (budget?.tasks ?? []).filter((task) => task.saga_id === sagaID), [budget])
  const liveSagas = useMemo(() => (budget?.sagas ?? []).filter((saga) => !saga.merged_into), [budget])
  const sagaTitle = useCallback((sagaID: string) => {
    const saga = (budget?.sagas ?? []).find((s) => s.saga_id === sagaID)
    return saga ? (saga.title || saga.saga_id) : sagaID
  }, [budget])

  const toggleSelected = (sagaID: string, checked: boolean) => {
    setSelected((prev) => {
      const next = new Set(prev)
      if (checked) next.add(sagaID)
      else next.delete(sagaID)
      return next
    })
  }

  const openDialog = (next: Dialog, defaults?: { title?: string; goal?: string; sagaID?: string }) => {
    setActionError('')
    setFormTitle(defaults?.title ?? '')
    setFormGoal(defaults?.goal ?? '')
    setFormSaga(defaults?.sagaID ?? '')
    setDialog(next)
  }

  // Every successful action closes its dialog, clears the merge selection and
  // reloads: the projection changed server-side, the rows must follow.
  const runAction = async (action: () => Promise<unknown>) => {
    setActionBusy(true)
    setActionError('')
    try {
      await action()
      setDialog(null)
      setSelected(new Set())
      await load()
    } catch (failure) {
      setActionError(message(failure))
    } finally {
      setActionBusy(false)
    }
  }

  if (!project.trim()) return <ProjectRequired what={t('project.title') ?? 'project overview'} />
  return (
    <div style={{ padding: '1rem' }}>
      {error && (
        <InlineNotification kind="error" title={t('common.error') ?? 'Error'} subtitle={error} onClose={() => setError('')} lowContrast style={{ marginBottom: '1rem' }} />
      )}

      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '0.75rem' }}>
        <Heading style={{ fontSize: '1.1rem' }}>{t('project.title') ?? 'Project overview'}</Heading>
        <button onClick={() => void load()} disabled={busy} style={{ background: 'none', border: 'none', color: 'var(--tm-text-2)', cursor: 'pointer', fontSize: '1rem', padding: '0.25rem', lineHeight: 1, borderRadius: '4px' }} title="Refresh">↻</button>
      </div>

      {budget ? (
        <>
          {/* Totals across every task, bound or not. */}
          <div style={{ display: 'flex', gap: '1rem', flexWrap: 'wrap', alignItems: 'baseline', color: 'var(--tm-text-2)', fontSize: '0.85rem', marginBottom: '1rem', border: '1px solid var(--tm-border)', borderRadius: '6px', padding: '0.5rem 0.75rem' }}>
            <span><strong style={{ color: 'var(--tm-text)' }}>{budget.task_count}</strong> {pluralT(t, locale, 'project.tasks_n', budget.task_count)}</span>
            <span><strong style={{ color: 'var(--tm-text)' }}>{budget.saga_count}</strong> {pluralT(t, locale, 'project.sagas_n', budget.saga_count)}</span>
            <span><strong style={{ color: 'var(--tm-text)' }}>{budget.run_count}</strong> {pluralT(t, locale, 'runs.task_runs_n', budget.run_count)}</span>
            <span>⏱ <strong style={{ color: 'var(--tm-text)' }}>{formatDuration(budget.elapsed_ms)}</strong> {t('runs.task_elapsed') ?? 'elapsed'}</span>
            <span>⚒ <strong style={{ color: 'var(--tm-text)' }}>{formatDuration(budget.active_ms)}</strong> {t('runs.task_active') ?? 'active'}</span>
            <span><strong style={{ color: 'var(--tm-text)' }}>{budget.total_tokens.toLocaleString()}</strong> {t('runs.tokens') ?? 'tokens'}</span>
            {budget.total_cost_usd > 0 && <span><strong style={{ color: 'var(--tm-text)' }}>${budget.total_cost_usd.toFixed(2)}</strong></span>}
            {budget.approvals.requested > 0 && (
              <span title={approvalHint(t, budget.approvals)}>👤 {budget.approvals.granted + budget.approvals.rejected}/{budget.approvals.requested}{(budget.approvals.pending ?? 0) > 0 ? ` · ⏳${budget.approvals.pending}` : ''}</span>
            )}
          </div>

          {/* Explicit sagas: one row per registered effort, expandable to its tasks. */}
          <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'baseline', marginBottom: '0.4rem' }}>
            <Heading style={{ fontSize: '0.95rem' }}>{t('project.sagas') ?? 'Sagas'}</Heading>
            {liveSagas.length >= 2 && (
              <Button
                size="sm"
                kind="ghost"
                disabled={selected.size < 1}
                title={selected.size < 2 ? (t('project.saga_merge_select') ?? 'Select two or more sagas') : undefined}
                onClick={() => openDialog({ kind: 'merge', sources: [...selected] })}
              >
                {t('project.saga_merge') ?? 'Merge'}{selected.size > 0 ? ` (${selected.size})` : ''}
              </Button>
            )}
          </div>
          {budget.sagas.length === 0 ? (
            <Tile style={{ color: 'var(--tm-text-3)', marginBottom: '1rem' }}>{t('project.no_sagas') ?? 'No sagas registered yet'}</Tile>
          ) : (
            <div style={{ border: '1px solid var(--tm-border)', borderRadius: '6px', overflow: 'hidden', marginBottom: '1rem' }}>
              <div style={{ display: 'grid', gridTemplateColumns: '1.6rem 1.2rem minmax(9rem, 2.2fr) minmax(4rem, 0.8fr) minmax(3rem, 0.7fr) minmax(3rem, 0.7fr) minmax(4rem, 1fr) minmax(4rem, 1fr) minmax(4.5rem, 1fr) minmax(3.5rem, 0.8fr) minmax(6rem, 1.2fr) minmax(7rem, 1.4fr)', gap: '0.25rem 0.6rem', fontSize: '0.75rem', alignItems: 'baseline', padding: '0.4rem 0.6rem', background: 'var(--tm-raised)' }}>
                <span />
                <span />
                <span style={{ color: 'var(--tm-text-3)' }}>{t('project.saga_col') ?? 'Saga'}</span>
                <span style={{ color: 'var(--tm-text-3)' }}>{t('project.tasks_col') ?? 'Tasks'}</span>
                <span style={{ color: 'var(--tm-text-3)' }}>{t('runs.task_col_runs') ?? 'Runs'}</span>
                <span style={{ color: 'var(--tm-text-3)' }}>{t('runs.task_agents_col') ?? 'Agents'}</span>
                <span style={{ color: 'var(--tm-text-3)' }}>⏱ {t('runs.task_elapsed') ?? 'elapsed'}</span>
                <span style={{ color: 'var(--tm-text-3)' }}>⚒ {t('runs.task_active') ?? 'active'}</span>
                <span style={{ color: 'var(--tm-text-3)' }}>{t('runs.tokens') ?? 'Tokens'}</span>
                <span style={{ color: 'var(--tm-text-3)' }}>{t('runs.cost') ?? 'Cost'}</span>
                <span style={{ color: 'var(--tm-text-3)' }} title={t('dashboard.human_col_hint') ?? undefined}>🧠/⏳</span>
                <span style={{ color: 'var(--tm-text-3)' }}>👤 {t('runs.approvals_col') ?? 'Approvals'}</span>
              </div>
              {budget.sagas.map((saga) => {
                const merged = !!saga.merged_into
                return (
                  <div key={saga.saga_id} style={{ borderTop: '1px solid var(--tm-border)', opacity: merged ? 0.55 : 1 }}>
                    <div style={{ display: 'grid', gridTemplateColumns: '1.6rem 1.2rem minmax(9rem, 2.2fr) minmax(4rem, 0.8fr) minmax(3rem, 0.7fr) minmax(3rem, 0.7fr) minmax(4rem, 1fr) minmax(4rem, 1fr) minmax(4.5rem, 1fr) minmax(3.5rem, 0.8fr) minmax(6rem, 1.2fr) minmax(7rem, 1.4fr)', gap: '0.25rem 0.6rem', fontSize: '0.8rem', alignItems: 'baseline', padding: '0.35rem 0.6rem' }}>
                      <span style={{ display: 'flex', alignItems: 'center' }}>
                        {!merged && (
                          <input
                            type="checkbox"
                            checked={selected.has(saga.saga_id)}
                            onChange={(e) => toggleSelected(saga.saga_id, e.target.checked)}
                            title={t('project.saga_merge') ?? 'Merge'}
                            style={{ accentColor: 'var(--tm-accent, #f97316)' }}
                          />
                        )}
                      </span>
                      <button
                        onClick={() => setOpenSaga(openSaga === saga.saga_id ? null : saga.saga_id)}
                        style={{ background: 'none', border: 'none', color: 'var(--tm-text-3)', cursor: tasksOf(saga.saga_id).length > 0 ? 'pointer' : 'default', padding: 0, lineHeight: 1 }}
                        title={tasksOf(saga.saga_id).length > 0 ? (t('project.saga_expand') ?? 'Bound tasks') : undefined}
                      >
                        {tasksOf(saga.saga_id).length > 0 && (openSaga === saga.saga_id ? <ChevronDown size={12} /> : <ChevronRight size={12} />)}
                      </button>
                      <span style={{ display: 'flex', alignItems: 'baseline', gap: '0.35rem', minWidth: 0 }} title={(saga.goal ? saga.goal + '\n' : '') + saga.saga_id}>
                        <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', textDecoration: merged ? 'line-through' : 'none' }}>
                          {saga.title || saga.saga_id}
                        </span>
                        {merged && <Tag size="sm" type="cool-gray" title={saga.merged_into}>{t('project.saga_merged_into') ?? 'merged into'} → {sagaTitle(saga.merged_into!)}</Tag>}
                        {!merged && saga.running > 0 && <Tag type="cool-gray" size="sm">{pluralT(t, locale, 'runs.task_running_n', saga.running)}</Tag>}
                        {!merged && (
                          <>
                          <button
                            onClick={() => openDialog({ kind: 'rename', saga }, { title: saga.title, goal: saga.goal })}
                            title={t('project.saga_rename') ?? 'Rename'}
                            style={{ background: 'none', border: 'none', color: 'var(--tm-text-3)', cursor: 'pointer', padding: 0, fontSize: '0.75rem', lineHeight: 1, flexShrink: 0 }}
                          >✎</button>
                          <button
                            onClick={() => void deleteSaga(saga)}
                            title={t('project.saga_delete') ?? 'Delete saga'}
                            style={{ background: 'none', border: 'none', color: 'var(--tm-text-3)', cursor: 'pointer', padding: 0, fontSize: '0.75rem', lineHeight: 1, flexShrink: 0 }}
                          >✕</button>
                          </>
                        )}
                      </span>
                      <span>{saga.task_ids.length}</span>
                      <span>{saga.run_count}</span>
                      <span>{saga.agent_count}</span>
                      <span>{formatDuration(saga.elapsed_ms)}</span>
                      <span>{formatDuration(saga.active_ms)}</span>
                      <span>{saga.total_tokens.toLocaleString()}</span>
                      <span>{saga.total_cost_usd > 0 ? `$${saga.total_cost_usd.toFixed(2)}` : '—'}</span>
                      <span title={t('dashboard.human_col_hint') ?? undefined} style={{ whiteSpace: 'nowrap' }}>
                        {(saga.human?.hab_seconds ?? 0) > 0 || (saga.human?.hwt_seconds ?? 0) > 0
                          ? <>🧠 {formatDuration((saga.human?.hab_seconds ?? 0) * 1000)} · ⏳ {formatDuration((saga.human?.hwt_seconds ?? 0) * 1000)}</>
                          : '—'}
                      </span>
                      <span title={approvalHint(t, saga.approvals, saga.human)} style={{ whiteSpace: 'nowrap' }}>
                        {saga.approvals.requested > 0
                          ? <>✓{saga.approvals.granted} ✗{saga.approvals.rejected}{saga.approvals.timed_out > 0 ? ` ⏱${saga.approvals.timed_out}` : ''}{(saga.approvals.pending ?? 0) > 0 ? ` …${saga.approvals.pending}` : ''} · {formatDuration(saga.approvals.wait_ms)}</>
                          : '—'}
                      </span>
                    </div>
                    {openSaga === saga.saga_id && tasksOf(saga.saga_id).length > 0 && (
                      <div style={{ padding: '0.25rem 0.6rem 0.5rem 2rem', display: 'grid', gap: '0.2rem', fontSize: '0.78rem', color: 'var(--tm-text-2)' }}>
                        {tasksOf(saga.saga_id).map((task) => (
                          <div key={task.task_id} style={{ display: 'flex', gap: '0.75rem', flexWrap: 'wrap', alignItems: 'baseline' }}>
                            <span style={{ minWidth: '10rem', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', color: 'var(--tm-text)' }} title={task.root_run_id ? `${task.title || task.task_id}\n${task.root_run_id}` : task.title || task.task_id}>
                              {task.title || task.task_id}
                              {task.running > 0 && <Tag type="cool-gray" size="sm" style={{ marginLeft: '0.35rem' }}>{pluralT(t, locale, 'runs.task_running_n', task.running)}</Tag>}
                            </span>
                            <span>{pluralT(t, locale, 'runs.task_runs_n', task.run_count)}</span>
                            <span>{pluralT(t, locale, 'runs.task_agents_n', task.agent_count)}</span>
                            <span>⏱ {formatDuration(task.elapsed_ms)}</span>
                            <span>{task.total_tokens.toLocaleString()} {t('runs.tokens') ?? 'tokens'}</span>
                            {task.total_cost_usd > 0 && <span>${task.total_cost_usd.toFixed(2)}</span>}
                            <button
                              onClick={() => void runAction(() => taskSagaBind(project.trim(), task.task_id, ''))}
                              title={t('project.saga_detach') ?? 'Detach from saga'}
                              style={{ background: 'none', border: 'none', color: 'var(--tm-text-3)', cursor: 'pointer', padding: 0, fontSize: '0.75rem', lineHeight: 1 }}
                            >✕</button>
                          </div>
                        ))}
                      </div>
                    )}
                  </div>
                )
              })}
            </div>
          )}

          {/* Tasks outside any saga: same accounting, no explicit effort behind them.
              Newest first, capped: a long-lived project accretes hundreds of
              unbound tasks — the block should show the LIVE tail, not the landfill. */}
          <Heading style={{ fontSize: '0.95rem', marginBottom: '0.4rem' }}>
            {t('project.unbound') ?? 'Tasks without a saga'} ({unbound.length})
          </Heading>
          {unbound.length === 0 ? (
            <Tile style={{ color: 'var(--tm-text-3)' }}>{t('project.no_unbound') ?? 'Every task belongs to a saga'}</Tile>
          ) : (
            <div style={{ border: '1px solid var(--tm-border)', borderRadius: '6px', overflow: 'hidden' }}>
              {(showAllUnbound ? unbound : unbound.slice(0, unboundShown)).map((task) => (
                <div key={task.task_id} style={{ display: 'grid', gridTemplateColumns: 'minmax(10rem, 2.2fr) minmax(3rem, 0.7fr) minmax(3rem, 0.7fr) minmax(4rem, 1fr) minmax(4rem, 1fr) minmax(4.5rem, 1fr) minmax(3.5rem, 0.8fr) minmax(6rem, 1.2fr) minmax(7rem, 1.4fr) minmax(11rem, auto)', gap: '0.25rem 0.6rem', fontSize: '0.8rem', alignItems: 'baseline', padding: '0.35rem 0.6rem', borderTop: '1px solid var(--tm-border)' }}>
                  <span title={task.root_run_id ? `${task.title || task.task_id}\n${task.root_run_id}` : task.title || task.task_id} style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                    {task.title || task.task_id}
                    {task.running > 0 && <Tag type="cool-gray" size="sm" style={{ marginLeft: '0.35rem' }}>{pluralT(t, locale, 'runs.task_running_n', task.running)}</Tag>}
                  </span>
                  <span>{task.run_count}</span>
                  <span>{task.agent_count}</span>
                  <span>{formatDuration(task.elapsed_ms)}</span>
                  <span>{formatDuration(task.active_ms)}</span>
                  <span>{task.total_tokens.toLocaleString()}</span>
                  <span>{task.total_cost_usd > 0 ? `$${task.total_cost_usd.toFixed(2)}` : '—'}</span>
                  <span title={t('dashboard.human_col_hint') ?? undefined} style={{ whiteSpace: 'nowrap' }}>
                    {(task.human?.hab_seconds ?? 0) > 0 || (task.human?.hwt_seconds ?? 0) > 0
                      ? <>🧠 {formatDuration((task.human?.hab_seconds ?? 0) * 1000)} · ⏳ {formatDuration((task.human?.hwt_seconds ?? 0) * 1000)}</>
                      : '—'}
                  </span>
                  <span title={approvalHint(t, task.approvals, task.human)} style={{ whiteSpace: 'nowrap' }}>
                    {task.approvals.requested > 0
                      ? <>✓{task.approvals.granted} ✗{task.approvals.rejected}{task.approvals.timed_out > 0 ? ` ⏱${task.approvals.timed_out}` : ''}{(task.approvals.pending ?? 0) > 0 ? ` …${task.approvals.pending}` : ''} · {formatDuration(task.approvals.wait_ms)}</>
                      : '—'}
                  </span>
                  <span style={{ display: 'flex', gap: '0.25rem', whiteSpace: 'nowrap' }}>
                    <Button size="sm" kind="ghost" onClick={() => openDialog({ kind: 'attach', task }, { sagaID: liveSagas[0]?.saga_id })} disabled={liveSagas.length === 0} title={liveSagas.length === 0 ? (t('project.saga_none') ?? 'No sagas in the project yet — create one') : undefined}>
                      {t('project.saga_attach') ?? 'Attach to saga'}
                    </Button>
                    <Button size="sm" kind="ghost" onClick={() => openDialog({ kind: 'create', task }, { title: task.title })}>
                      {t('project.saga_create') ?? 'Create saga'}
                    </Button>
                  </span>
                </div>
              ))}
              {unbound.length > unboundShown && !showAllUnbound && (
                <div style={{ padding: '0.35rem 0.6rem', borderTop: '1px solid var(--tm-border)' }}>
                  <Button size="sm" kind="ghost" onClick={() => setShowAllUnbound(true)}>
                    {t('project.unbound_show_all') ?? 'Show all'} ({unbound.length - unboundShown} {t('project.unbound_more') ?? 'more'})
                  </Button>
                </div>
              )}
            </div>
          )}
        </>
      ) : !busy && !error && (
        <Tile style={{ color: 'var(--tm-text-3)' }}>{t('common.loading') ?? 'Loading…'}</Tile>
      )}

      {dialog?.kind === 'rename' && (
        <AppModal onClose={() => setDialog(null)} panelStyle={{ width: '480px' }}>
          <Heading style={{ fontSize: '1rem', marginBottom: '0.75rem' }}>{t('project.saga_rename_title') ?? 'Rename saga'}</Heading>
          <Stack gap={4}>
            <TextInput
              id="saga-rename-title"
              labelText={t('project.saga_title_label') ?? 'Title'}
              value={formTitle}
              onChange={(e: React.ChangeEvent<HTMLInputElement>) => setFormTitle(e.target.value)}
              autoFocus
            />
            <TextArea
              id="saga-rename-goal"
              labelText={t('project.saga_goal_label') ?? 'Goal (optional)'}
              value={formGoal}
              onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => setFormGoal(e.target.value)}
              rows={2}
            />
            {actionError && <InlineNotification kind="error" title={t('common.error') ?? 'Error'} subtitle={actionError} lowContrast hideCloseButton />}
            <div className="form-actions" style={{ marginTop: 0 }}>
              <Button size="sm" kind="secondary" onClick={() => setDialog(null)}>{t('action.cancel') ?? 'Cancel'}</Button>
              <Button size="sm" disabled={actionBusy || (!formTitle.trim() && !formGoal.trim())} onClick={() => void runAction(() => sagaRename(project.trim(), dialog.saga.saga_id, { title: formTitle.trim() || undefined, goal: formGoal.trim() || undefined }))}>
                {t('action.save') ?? 'Save'}
              </Button>
            </div>
          </Stack>
        </AppModal>
      )}

      {dialog?.kind === 'merge' && (
        <AppModal onClose={() => setDialog(null)} panelStyle={{ width: '520px' }}>
          <Heading style={{ fontSize: '1rem', marginBottom: '0.75rem' }}>{t('project.saga_merge_title') ?? 'Merge sagas'}</Heading>
          <Stack gap={4}>
            <div style={{ fontSize: '0.85rem', color: 'var(--tm-text-2)' }}>{t('project.saga_merge_hint') ?? 'The selected sagas fold into the target: their tasks and spend move to it.'}</div>
            <div style={{ display: 'grid', gap: '0.3rem', fontSize: '0.85rem', color: 'var(--tm-text-2)', padding: '0.5rem 0.75rem', border: '1px solid var(--tm-border)', borderRadius: '6px' }}>
              {dialog.sources.map((sourceID) => (
                <span key={sourceID} style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                  ← {sagaTitle(sourceID)}
                </span>
              ))}
            </div>
            <Select
              id="saga-merge-target"
              labelText={t('project.saga_merge_target') ?? 'Merge into'}
              value={formSaga}
              onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setFormSaga(e.target.value)}
            >
              <SelectItem value="" text="—" />
              {liveSagas.filter((s) => !dialog.sources.includes(s.saga_id)).map((s) => (
                <SelectItem key={s.saga_id} value={s.saga_id} text={s.title || s.saga_id} />
              ))}
            </Select>
            {actionError && <InlineNotification kind="error" title={t('common.error') ?? 'Error'} subtitle={actionError} lowContrast hideCloseButton />}
            <div className="form-actions" style={{ marginTop: 0 }}>
              <Button size="sm" kind="secondary" onClick={() => setDialog(null)}>{t('action.cancel') ?? 'Cancel'}</Button>
              <Button size="sm" disabled={actionBusy || !formSaga} onClick={() => void runAction(() => sagaMerge(project.trim(), formSaga, dialog.sources))}>
                {t('project.saga_merge') ?? 'Merge'}
              </Button>
            </div>
          </Stack>
        </AppModal>
      )}

      {dialog?.kind === 'attach' && (
        <AppModal onClose={() => setDialog(null)} panelStyle={{ width: '480px' }}>
          <Heading style={{ fontSize: '1rem', marginBottom: '0.75rem' }}>{t('project.saga_attach_title') ?? 'Attach task to saga'}</Heading>
          <Stack gap={4}>
            <div style={{ fontSize: '0.85rem', color: 'var(--tm-text-2)' }}>
              {t('project.saga_attach_hint') ?? 'The task moves to the selected saga; further accounting follows it.'}
            </div>
            <div style={{ fontSize: '0.85rem', color: 'var(--tm-text)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
              {dialog.task.title || dialog.task.task_id}
            </div>
            <Select
              id="task-saga-select"
              labelText={t('project.saga_col') ?? 'Saga'}
              value={formSaga}
              onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setFormSaga(e.target.value)}
            >
              <SelectItem value="" text="—" />
              {liveSagas.map((s) => (
                <SelectItem key={s.saga_id} value={s.saga_id} text={`${s.title || s.saga_id} (${s.task_ids.length})`} />
              ))}
            </Select>
            {actionError && <InlineNotification kind="error" title={t('common.error') ?? 'Error'} subtitle={actionError} lowContrast hideCloseButton />}
            <div className="form-actions" style={{ marginTop: 0 }}>
              <Button size="sm" kind="secondary" onClick={() => setDialog(null)}>{t('action.cancel') ?? 'Cancel'}</Button>
              <Button size="sm" disabled={actionBusy || !formSaga} onClick={() => void runAction(() => taskSagaBind(project.trim(), dialog.task.task_id, formSaga))}>
                {t('project.saga_attach') ?? 'Attach'}
              </Button>
            </div>
          </Stack>
        </AppModal>
      )}

      {dialog?.kind === 'create' && (
        <AppModal onClose={() => setDialog(null)} panelStyle={{ width: '480px' }}>
          <Heading style={{ fontSize: '1rem', marginBottom: '0.75rem' }}>{t('project.saga_create_title') ?? 'New saga'}</Heading>
          <Stack gap={4}>
            <div style={{ fontSize: '0.85rem', color: 'var(--tm-text-2)' }}>
              {t('project.saga_create_hint') ?? 'The saga is created and bound to this task right away.'}
            </div>
            <div style={{ fontSize: '0.85rem', color: 'var(--tm-text)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
              {dialog.task.title || dialog.task.task_id}
            </div>
            <TextInput
              id="saga-create-title"
              labelText={t('project.saga_title_label') ?? 'Title'}
              value={formTitle}
              onChange={(e: React.ChangeEvent<HTMLInputElement>) => setFormTitle(e.target.value)}
              autoFocus
            />
            <TextArea
              id="saga-create-goal"
              labelText={t('project.saga_goal_label') ?? 'Goal (optional)'}
              value={formGoal}
              onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => setFormGoal(e.target.value)}
              rows={2}
            />
            {actionError && <InlineNotification kind="error" title={t('common.error') ?? 'Error'} subtitle={actionError} lowContrast hideCloseButton />}
            <div className="form-actions" style={{ marginTop: 0 }}>
              <Button size="sm" kind="secondary" onClick={() => setDialog(null)}>{t('action.cancel') ?? 'Cancel'}</Button>
              <Button size="sm" disabled={actionBusy || !formTitle.trim()} onClick={() => void runAction(() => sagaCreate(project.trim(), { title: formTitle.trim(), goal: formGoal.trim() || undefined, task_id: dialog.task.task_id }))}>
                {t('action.save') ?? 'Create'}
              </Button>
            </div>
          </Stack>
        </AppModal>
      )}
      {confirmDialogNode}
    </div>
  )
}
