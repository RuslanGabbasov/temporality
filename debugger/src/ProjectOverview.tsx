import { useCallback, useEffect, useMemo, useState } from 'react'
import { Grid, Column, Heading, InlineNotification, Tag, Tile } from '@carbon/react'
import { ChevronDown, ChevronRight } from '@carbon/icons-react'
import { projectBudget, type ProjectBudget } from './kernelApi'
import ProjectRequired from './ProjectRequired'
import { useI18n, pluralT } from './i18n'
import { formatDuration, approvalHint } from './eventSummary'

function message(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }

interface Props {
  project: string
}

/**
 * Project overview (docs/sagas.md): the analysis page for everything tied to
 * one project — totals (time, tokens, cost, approvals), explicit sagas with
 * their bound tasks, and the tasks that live outside any saga. Moved here from
 * the Runs tab: runs are execution, this is accounting.
 */
export default function ProjectOverview({ project }: Props) {
  const { t, locale } = useI18n()
  const [budget, setBudget] = useState<ProjectBudget | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [openSaga, setOpenSaga] = useState<string | null>(null)

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

  const unbound = useMemo(() => (budget?.tasks ?? []).filter((task) => !task.saga_id), [budget])
  const tasksOf = useCallback((sagaID: string) => (budget?.tasks ?? []).filter((task) => task.saga_id === sagaID), [budget])

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
          <Heading style={{ fontSize: '0.95rem', marginBottom: '0.4rem' }}>{t('project.sagas') ?? 'Sagas'}</Heading>
          {budget.sagas.length === 0 ? (
            <Tile style={{ color: 'var(--tm-text-3)', marginBottom: '1rem' }}>{t('project.no_sagas') ?? 'No sagas registered yet'}</Tile>
          ) : (
            <div style={{ border: '1px solid var(--tm-border)', borderRadius: '6px', overflow: 'hidden', marginBottom: '1rem' }}>
              <div style={{ display: 'grid', gridTemplateColumns: '1.2rem minmax(10rem, 2.2fr) minmax(4rem, 0.8fr) minmax(3rem, 0.7fr) minmax(3rem, 0.7fr) minmax(4rem, 1fr) minmax(4rem, 1fr) minmax(4.5rem, 1fr) minmax(3.5rem, 0.8fr) minmax(7rem, 1.4fr)', gap: '0.25rem 0.6rem', fontSize: '0.75rem', alignItems: 'baseline', padding: '0.4rem 0.6rem', background: 'var(--tm-raised)' }}>
                <span />
                <span style={{ color: 'var(--tm-text-3)' }}>{t('project.saga_col') ?? 'Saga'}</span>
                <span style={{ color: 'var(--tm-text-3)' }}>{t('project.tasks_col') ?? 'Tasks'}</span>
                <span style={{ color: 'var(--tm-text-3)' }}>{t('runs.task_col_runs') ?? 'Runs'}</span>
                <span style={{ color: 'var(--tm-text-3)' }}>{t('runs.task_agents_col') ?? 'Agents'}</span>
                <span style={{ color: 'var(--tm-text-3)' }}>⏱ {t('runs.task_elapsed') ?? 'elapsed'}</span>
                <span style={{ color: 'var(--tm-text-3)' }}>⚒ {t('runs.task_active') ?? 'active'}</span>
                <span style={{ color: 'var(--tm-text-3)' }}>{t('runs.tokens') ?? 'Tokens'}</span>
                <span style={{ color: 'var(--tm-text-3)' }}>{t('runs.cost') ?? 'Cost'}</span>
                <span style={{ color: 'var(--tm-text-3)' }}>👤 {t('runs.approvals_col') ?? 'Approvals'}</span>
              </div>
              {budget.sagas.map((saga) => (
                <div key={saga.saga_id} style={{ borderTop: '1px solid var(--tm-border)' }}>
                  <div style={{ display: 'grid', gridTemplateColumns: '1.2rem minmax(10rem, 2.2fr) minmax(4rem, 0.8fr) minmax(3rem, 0.7fr) minmax(3rem, 0.7fr) minmax(4rem, 1fr) minmax(4rem, 1fr) minmax(4.5rem, 1fr) minmax(3.5rem, 0.8fr) minmax(7rem, 1.4fr)', gap: '0.25rem 0.6rem', fontSize: '0.8rem', alignItems: 'baseline', padding: '0.35rem 0.6rem' }}>
                    <button
                      onClick={() => setOpenSaga(openSaga === saga.saga_id ? null : saga.saga_id)}
                      style={{ background: 'none', border: 'none', color: 'var(--tm-text-3)', cursor: tasksOf(saga.saga_id).length > 0 ? 'pointer' : 'default', padding: 0, lineHeight: 1 }}
                      title={tasksOf(saga.saga_id).length > 0 ? (t('project.saga_expand') ?? 'Bound tasks') : undefined}
                    >
                      {tasksOf(saga.saga_id).length > 0 && (openSaga === saga.saga_id ? <ChevronDown size={12} /> : <ChevronRight size={12} />)}
                    </button>
                    <span title={(saga.goal ? saga.goal + '\n' : '') + saga.saga_id} style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                      {saga.title || saga.saga_id}
                      {saga.running > 0 && <Tag type="cool-gray" size="sm" style={{ marginLeft: '0.35rem' }}>{pluralT(t, locale, 'runs.task_running_n', saga.running)}</Tag>}
                    </span>
                    <span>{saga.task_ids.length}</span>
                    <span>{saga.run_count}</span>
                    <span>{saga.agent_count}</span>
                    <span>{formatDuration(saga.elapsed_ms)}</span>
                    <span>{formatDuration(saga.active_ms)}</span>
                    <span>{saga.total_tokens.toLocaleString()}</span>
                    <span>{saga.total_cost_usd > 0 ? `$${saga.total_cost_usd.toFixed(2)}` : '—'}</span>
                    <span title={approvalHint(t, saga.approvals)} style={{ whiteSpace: 'nowrap' }}>
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
                        </div>
                      ))}
                    </div>
                  )}
                </div>
              ))}
            </div>
          )}

          {/* Tasks outside any saga: same accounting, no explicit effort behind them. */}
          <Heading style={{ fontSize: '0.95rem', marginBottom: '0.4rem' }}>
            {t('project.unbound') ?? 'Tasks without a saga'} ({unbound.length})
          </Heading>
          {unbound.length === 0 ? (
            <Tile style={{ color: 'var(--tm-text-3)' }}>{t('project.no_unbound') ?? 'Every task belongs to a saga'}</Tile>
          ) : (
            <div style={{ border: '1px solid var(--tm-border)', borderRadius: '6px', overflow: 'hidden' }}>
              {unbound.map((task) => (
                <div key={task.task_id} style={{ display: 'grid', gridTemplateColumns: 'minmax(10rem, 2.2fr) minmax(3rem, 0.7fr) minmax(3rem, 0.7fr) minmax(4rem, 1fr) minmax(4rem, 1fr) minmax(4.5rem, 1fr) minmax(3.5rem, 0.8fr) minmax(7rem, 1.4fr)', gap: '0.25rem 0.6rem', fontSize: '0.8rem', alignItems: 'baseline', padding: '0.35rem 0.6rem', borderTop: '1px solid var(--tm-border)' }}>
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
                  <span title={approvalHint(t, task.approvals)} style={{ whiteSpace: 'nowrap' }}>
                    {task.approvals.requested > 0
                      ? <>✓{task.approvals.granted} ✗{task.approvals.rejected}{task.approvals.timed_out > 0 ? ` ⏱${task.approvals.timed_out}` : ''}{(task.approvals.pending ?? 0) > 0 ? ` …${task.approvals.pending}` : ''} · {formatDuration(task.approvals.wait_ms)}</>
                      : '—'}
                  </span>
                </div>
              ))}
            </div>
          )}
        </>
      ) : !busy && !error && (
        <Tile style={{ color: 'var(--tm-text-3)' }}>{t('common.loading') ?? 'Loading…'}</Tile>
      )}
    </div>
  )
}
