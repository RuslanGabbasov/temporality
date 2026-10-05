import { useCallback, useEffect, useState } from 'react'
import {
  Button,
  TextInput,
  TextArea,
  Select,
  SelectItem,
  InlineNotification,
  Loading,
  Tag,
  Tile,
  Grid,
  Column,
  Stack,
  Section,
  Heading,
} from '@carbon/react'
import {
  canReconcile,
  isReconcileEffect,
  operations as fetchOperations,
  reasonText,
  reconcile,
  whoami,
  type ReconcileEffect,
  type ReconcileReceipt,
  type UncertainOperation,
  type Whoami,
} from './kernelApi'
import { useT } from './i18n'
import ListFilter, { matchesFilter } from './ListFilter'

function message(error: unknown) { return error instanceof Error ? error.message : 'Request failed' }

const EFFECT_HELP: Record<ReconcileEffect, string> = {
  none: 'Effect did NOT happen — safe to retry the operation',
  occurred: 'Effect DID happen — verified against the real system',
  unknown: 'Cannot determine — mark as unresolved',
}

const EFFECT_COLORS: Record<ReconcileEffect, 'green' | 'red' | 'warm-gray'> = {
  occurred: 'green',
  none: 'red',
  unknown: 'warm-gray',
}

const REASON_SHORT: Record<string, string> = {
  crash_window: 'Agent crashed before confirmation',
  stale_in_flight: 'Operation stuck — no response',
  failed_uncertain: 'Tool failed — may have side effects',
}

/** Extract human-readable info from tool name. */
function toolInfo(tool: string, server?: string): { icon: string; label: string; category: string } {
  if (tool.startsWith('mcp__')) {
    const name = tool.replace('mcp__', '')
    return { icon: '🔌', label: name, category: server ? `MCP · ${server}` : 'MCP tool' }
  }
  if (tool === 'run_command') return { icon: '⚡', label: 'Shell command', category: 'Sandbox' }
  if (tool === 'write_file') return { icon: '📝', label: 'Write file', category: 'Sandbox' }
  if (tool === 'read_file') return { icon: '📄', label: 'Read file', category: 'Sandbox' }
  if (tool === 'search') return { icon: '🔍', label: 'Search', category: 'Sandbox' }
  return { icon: '🔧', label: tool, category: 'Tool' }
}

/** Extract turn number from operation_id like ".../turn/05/call_..." */
function turnOf(opId: string): string | null {
  const m = opId.match(/turn\/(\d+)/)
  return m ? m[1] : null
}

export default function Operations({ project }: { project: string }) {
  const t = useT()
  const [ops, setOps] = useState<UncertainOperation[]>([])
  const [selected, setSelected] = useState<UncertainOperation | null>(null)
  const [selectedSet, setSelectedSet] = useState<Set<string>>(new Set())
  const [identity, setIdentity] = useState<Whoami | null>(null)
  const [effect, setEffect] = useState<ReconcileEffect>('occurred')
  const [note, setNote] = useState('')
  const [actor, setActor] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [receipt, setReceipt] = useState<ReconcileReceipt | null>(null)
  const [filter, setFilter] = useState('')
  const visibleOps = ops.filter((op) => matchesFilter(filter, op.tool, op.server, op.operation_id))

  const load = useCallback(async (target = project, silent = false) => {
    if (!target.trim()) return
    if (!silent) setBusy(true)
    setError('')
    try {
      const page = await fetchOperations(target.trim())
      setOps(page.operations ?? [])
      if (!silent) setSelected(null)
    } catch (failure) { setError(message(failure)) }
    finally { if (!silent) setBusy(false) }
  }, [project])

  useEffect(() => {
    void whoami().then((info) => {
      setIdentity(info)
      if (!actor) setActor(info.subject === 'anonymous' ? 'human-ui' : info.subject)
    }).catch(() => setIdentity(null))
    void load(project)
  }, [])

  async function submit() {
    if (!selected) return
    if (!isReconcileEffect(effect)) return
    setBusy(true); setError(''); setReceipt(null)
    try {
      const record = await reconcile({
        project: selected.project,
        run_id: selected.run_id,
        operation_id: selected.operation_id,
        effect,
        note: note.trim() || undefined,
        actor_id: actor.trim() || 'human-ui',
        started_event_id: selected.started_event_id,
      })
      setReceipt(record)
      setSelected(null)
      setNote('')
      await load(project, true)
    } catch (failure) { setError(message(failure)) }
    finally { setBusy(false) }
  }

  async function batchReconcile(batchEffect: ReconcileEffect) {
    const selected = ops.filter((op) => selectedSet.has(op.operation_id))
    if (selected.length === 0) return
    setBusy(true); setError(''); setReceipt(null)
    let count = 0
    for (const op of selected) {
      try {
        await reconcile({
          project: op.project, run_id: op.run_id, operation_id: op.operation_id,
          effect: batchEffect, note: note.trim() || undefined,
          actor_id: actor.trim() || 'human-ui', started_event_id: op.started_event_id,
        })
        count++
      } catch { /* continue */ }
    }
    setReceipt({ event_id: `batch-${count}`, operation_id: `${count} operations`, effect: batchEffect } as any)
    setSelectedSet(new Set()); setNote('')
    await load(project, true)
    setBusy(false)
  }

  const toggleSelect = (opId: string) => {
    setSelectedSet((prev) => {
      const next = new Set(prev)
      if (next.has(opId)) next.delete(opId)
      else next.add(opId)
      return next
    })
  }

  const operator = canReconcile(identity?.role ?? '')

  return (
    <div style={{ padding: '1rem' }}>
      {error && (
        <InlineNotification kind="error" title={t('action.error') ?? 'Error'} subtitle={error} onClose={() => setError('')} lowContrast style={{ marginBottom: '1rem' }} />
      )}
      {receipt && (
        <InlineNotification
          kind="success"
          title={t('operations.verdict_recorded') ?? 'Verdict recorded'}
          subtitle={`${receipt.effect} — ${receipt.operation_id}`}
          lowContrast
          style={{ marginBottom: '1rem' }}
        />
      )}

      <Grid>
        {/* Operation list */}
        <Column sm={4} md={4} lg={5}>
          <div style={{ position: 'sticky', top: '3rem', maxHeight: 'calc(100vh - 4rem)', overflowY: 'auto' }}>
            <div style={{ padding: '0.5rem 0', marginBottom: '0.5rem', borderBottom: '1px solid var(--tm-border)', position: 'sticky', top: 0, background: 'var(--tm-bg)', zIndex: 1 }}>
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '0.25rem' }}>
                <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
                  <Heading style={{ fontSize: '1rem' }}>{t('operations.title') ?? 'Operations'}</Heading>
                  {ops.length > 0 && <Tag type="red" size="sm">{visibleOps.length}</Tag>}
                </div>
                <button onClick={() => void load()} disabled={busy} style={{ background: 'none', border: 'none', color: 'var(--tm-text-2)', cursor: 'pointer', fontSize: '1rem', padding: '0.25rem', lineHeight: 1, borderRadius: '4px' }} title="Refresh">↻</button>
              </div>
              <ListFilter value={filter} onChange={setFilter} placeholder={t('common.filter_operations') ?? 'Filter operations…'} />
            </div>
            <Stack gap={1}>
              {ops.length === 0 && (
                <Tile style={{ textAlign: 'center', padding: '2rem', color: 'var(--tm-text-3)' }}>
                  <p>{t('operations.no_ops') ?? 'No unresolved operations'}</p>
                  <p style={{ fontSize: '0.75rem', marginTop: '0.5rem' }}>{t('operations.no_ops_hint') ?? 'All tool executions have been confirmed.'}</p>
                </Tile>
              )}
              {selectedSet.size > 0 && operator && (
                <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'center', padding: '0.5rem', marginBottom: '0.5rem', background: 'var(--tm-elevated)', borderRadius: '6px', border: '1px solid var(--tm-border)' }}>
                  <span style={{ fontSize: '0.8rem', color: 'var(--tm-text-3)' }}>{selectedSet.size} {t('operations.selected') ?? 'selected'}</span>
                  <Button size="sm" kind="ghost" onClick={() => void batchReconcile('occurred')} disabled={busy}>{t('operations.batch_occurred') ?? 'All occurred'}</Button>
                  <Button size="sm" kind="ghost" onClick={() => void batchReconcile('none')} disabled={busy}>{t('operations.batch_none') ?? 'All none'}</Button>
                  <Button size="sm" kind="ghost" onClick={() => void batchReconcile('unknown')} disabled={busy}>{t('operations.batch_unknown') ?? 'All unknown'}</Button>
                  <Button size="sm" kind="ghost" onClick={() => setSelectedSet(new Set())}>{t('operations.batch_clear') ?? 'Clear'}</Button>
                </div>
              )}
              {visibleOps.map((op) => {
                const info = toolInfo(op.tool, op.server)
                const turn = turnOf(op.operation_id)
                const isChecked = selectedSet.has(op.operation_id)
                return (
                  <Tile
                    key={`${op.run_id}:${op.operation_id}`}
                    onClick={() => setSelected(op)}
                    className={`workspace-tile ${selected?.operation_id === op.operation_id ? 'selected' : ''}`}
                    style={{ cursor: 'pointer', padding: '0.75rem' }}
                  >
                    <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
                      <input
                        type="checkbox"
                        checked={isChecked}
                        onClick={(e) => { e.stopPropagation(); toggleSelect(op.operation_id) }}
                        onChange={() => {}}
                        style={{ accentColor: 'var(--tm-teal)', flexShrink: 0 }}
                      />
                      <span style={{ fontSize: '1.1rem' }}>{info.icon}</span>
                      <div style={{ flex: 1, minWidth: 0 }}>
                        <div style={{ fontWeight: 600, fontSize: '0.875rem' }}>{info.label}</div>
                        <div style={{ fontSize: '0.75rem', color: 'var(--tm-text-3)' }}>
                          {info.category}
                          {turn && ` · ${t('operations.turn_n', { turn: String(turn) })}`}
                        </div>
                      </div>
                    </div>
                    <div style={{ marginTop: '0.35rem', display: 'flex', gap: '0.35rem', flexWrap: 'wrap' }}>
                      <Tag type="warm-gray" size="sm">{t(`operations.reason.${op.reason}`) ?? REASON_SHORT[op.reason] ?? op.reason}</Tag>
                      <Tag type="gray" size="sm">{new Date(op.started_at).toLocaleString()}</Tag>
                    </div>
                  </Tile>
                )
              })}
            </Stack>
          </div>
        </Column>

        {/* Detail panel */}
        <Column sm={4} md={4} lg={11}>
          <Section level={3}>
            {!selected ? (
              <div style={{ textAlign: 'center', color: 'var(--tm-text-3)', padding: '3rem 1rem' }}>
                <Heading>{t('operations.select') ?? 'Select an operation'}</Heading>
                <p style={{ marginTop: '0.5rem' }}>{t('operations.select_hint') ?? 'Click an operation on the left to inspect it and record your verdict.'}</p>
              </div>
            ) : (
              <Stack gap={3}>
                {/* What happened */}
                <Tile>
                  <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', marginBottom: '0.75rem' }}>
                    <span style={{ fontSize: '1.5rem' }}>{toolInfo(selected.tool, selected.server).icon}</span>
                    <div>
                      <Heading style={{ fontSize: '1.1rem' }}>{toolInfo(selected.tool, selected.server).label}</Heading>
                      <div style={{ fontSize: '0.75rem', color: 'var(--tm-text-3)' }}>{toolInfo(selected.tool, selected.server).category}</div>
                    </div>
                  </div>

                  <div style={{
                    padding: '0.75rem 1rem',
                    background: '#1a1400',
                    border: '1px solid #3d3000',
                    borderRadius: '6px',
                    marginBottom: '0.75rem',
                  }}>
                    <div style={{ fontWeight: 600, color: '#e0af68', marginBottom: '0.25rem' }}>⚠ {t('operations.what_happened') ?? 'What happened'}</div>
                    {selected.child_run_id ? (
                      <>
                        <p style={{ fontSize: '0.875rem', color: '#c0caf5' }}>
                          {t('operations.delegated_notice', { run: selected.child_run_id })}
                        </p>
                        <p style={{ fontSize: '0.75rem', color: 'var(--tm-text-3)', marginTop: '0.5rem' }}>
                          {t('operations.delegated_stats', { total: String(selected.child_ops_total ?? '?'), unresolved: String(selected.child_ops_unresolved ?? '?') })}{' '}
                          <a
                            href={`/agents?project=${encodeURIComponent(selected.project)}&run=${encodeURIComponent(selected.child_run_id)}`}
                            style={{ color: 'var(--tm-teal)' }}
                          >
                            {t('operations.open_child_run') ?? 'Open child run'}
                          </a>
                        </p>
                      </>
                    ) : (
                      <>
                        <p style={{ fontSize: '0.875rem', color: '#c0caf5' }}>{reasonText(selected.reason)}</p>
                        <p style={{ fontSize: '0.75rem', color: 'var(--tm-text-3)', marginTop: '0.5rem' }}>
                          {t('operations.agent_notice') ?? 'The agent executed this tool but the system couldn\'t confirm whether the side effect actually happened.'}
                          {' '}{t('operations.check_tell') ?? 'You need to check the external system and tell Temporality the outcome.'}
                        </p>
                      </>
                    )}
                  </div>
                </Tile>

                {/* Context */}
                <Tile>
                  <Heading style={{ fontSize: '0.875rem', marginBottom: '0.5rem' }}>{t('operations.context') ?? 'Context'}</Heading>
                  <dl style={{ fontSize: '0.875rem' }}>
                    <div style={{ display: 'flex', gap: '0.5rem', marginBottom: '0.35rem' }}>
                      <dt style={{ color: 'var(--tm-text-3)', minWidth: '100px' }}>{t('operations.run') ?? 'Run'}</dt>
                      <dd>
                        <a
                          href={`/agents?project=${encodeURIComponent(selected.project)}&run=${encodeURIComponent(selected.run_id)}`}
                          style={{ color: 'var(--tm-teal)' }}
                        >
                          {selected.run_id}
                        </a>
                      </dd>
                    </div>
                    {turnOf(selected.operation_id) && (
                      <div style={{ display: 'flex', gap: '0.5rem', marginBottom: '0.35rem' }}>
                        <dt style={{ color: 'var(--tm-text-3)', minWidth: '100px' }}>{t('operations.turn') ?? 'Turn'}</dt>
                        <dd>{t('runs.turn', { turn: String(turnOf(selected.operation_id)) })}</dd>
                      </div>
                    )}
                    <div style={{ display: 'flex', gap: '0.5rem', marginBottom: '0.35rem' }}>
                      <dt style={{ color: 'var(--tm-text-3)', minWidth: '100px' }}>{t('operations.when') ?? 'When'}</dt>
                      <dd>{new Date(selected.started_at).toLocaleString()}</dd>
                    </div>
                    <div style={{ display: 'flex', gap: '0.5rem', marginBottom: '0.35rem' }}>
                      <dt style={{ color: 'var(--tm-text-3)', minWidth: '100px' }}>{t('operations.tool') ?? 'Tool'}</dt>
                      <dd><code style={{ fontSize: '0.8rem' }}>{selected.tool}{selected.server ? ` @ ${selected.server}` : ''}</code></dd>
                    </div>
                    <div style={{ display: 'flex', gap: '0.5rem' }}>
                      <dt style={{ color: 'var(--tm-text-3)', minWidth: '100px' }}>{t('operations.args_hash') ?? 'Args hash'}</dt>
                      <dd><code style={{ fontSize: '0.7rem', color: 'var(--tm-muted)' }}>{selected.arguments_hash ?? '—'}</code></dd>
                    </div>
                  </dl>
                </Tile>

                {/* Verdict */}
                {!operator ? (
                  <Tile>
                    <p style={{ color: 'var(--tm-text-3)' }}>
                      {t('operations.operator_required') ?? 'Recording a verdict requires an operator token.'}
                      {identity?.role && <> {t('operations.current_role') ?? 'Current role:'} <strong>{identity.role}</strong>.</>}
                    </p>
                  </Tile>
                ) : (
                  <Tile>
                    <Heading style={{ fontSize: '0.875rem', marginBottom: '0.75rem' }}>{t('operations.record_verdict') ?? 'Record your verdict'}</Heading>
                    <p style={{ fontSize: '0.75rem', color: 'var(--tm-text-3)', marginBottom: '0.75rem' }}>
                      {t('operations.check_external') ?? 'Check the external system (database, API, file system) and tell Temporality what actually happened.'}
                    </p>
                    <Stack gap={3}>
                      <div style={{ display: 'flex', gap: '0.5rem' }}>
                        {(['occurred', 'none', 'unknown'] as ReconcileEffect[]).map((option) => (
                          <button
                            key={option}
                            onClick={() => setEffect(option)}
                            style={{
                              flex: 1,
                              padding: '0.75rem',
                              borderRadius: '6px',
                              border: `2px solid ${effect === option ? (option === 'occurred' ? '#9ece6a' : option === 'none' ? '#f7768e' : '#e0af68') : 'var(--tm-border)'}`,
                              background: effect === option ? (option === 'occurred' ? 'rgba(158,206,106,0.1)' : option === 'none' ? 'rgba(247,118,142,0.1)' : 'rgba(224,175,104,0.1)') : 'transparent',
                              color: 'var(--tm-text)',
                              cursor: 'pointer',
                              textAlign: 'center',
                              fontSize: '0.875rem',
                            }}
                          >
                            <div style={{ fontWeight: 600, marginBottom: '0.25rem', color: option === 'occurred' ? '#9ece6a' : option === 'none' ? '#f7768e' : '#e0af68' }}>
                              {option === 'occurred' ? `✓ ${t('operations.happened') ?? 'Happened'}` : option === 'none' ? `✗ ${t('operations.did_not_happen') ?? 'Did NOT happen'}` : `? ${t('operations.unknown_verdict') ?? 'Unknown'}`}
                            </div>
                            <div style={{ fontSize: '0.7rem', color: 'var(--tm-text-3)' }}>{t(`operations.effect_${option}`) ?? EFFECT_HELP[option]}</div>
                          </button>
                        ))}
                      </div>
                      <TextArea
                        id="note"
                        labelText={t('operations.what_did_you_find') ?? 'What did you find?'}
                        value={note}
                        onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => setNote(e.target.value)}
                        placeholder={t('operations.find_placeholder') ?? 'e.g. Checked the database — the row was inserted twice. Or: File was not created, safe to retry.'}
                        rows={3}
                      />
                      <div className="form-actions">
                        <Button kind="secondary" onClick={() => setSelected(null)}>{t('action.cancel') ?? 'Cancel'}</Button>
                        <Button onClick={() => void submit()} disabled={busy}>
                          {t('operations.submit_verdict') ?? 'Submit verdict'}
                        </Button>
                      </div>
                    </Stack>
                  </Tile>
                )}
              </Stack>
            )}
          </Section>
        </Column>
      </Grid>

      {busy && <Loading withOverlay={false} />}
    </div>
  )
}
