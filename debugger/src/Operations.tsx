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
  const [ops, setOps] = useState<UncertainOperation[]>([])
  const [selected, setSelected] = useState<UncertainOperation | null>(null)
  const [identity, setIdentity] = useState<Whoami | null>(null)
  const [effect, setEffect] = useState<ReconcileEffect>('occurred')
  const [note, setNote] = useState('')
  const [actor, setActor] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [receipt, setReceipt] = useState<ReconcileReceipt | null>(null)

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

  const operator = canReconcile(identity?.role ?? '')

  return (
    <div style={{ padding: '1rem' }}>
      {error && (
        <InlineNotification kind="error" title="Error" subtitle={error} onClose={() => setError('')} lowContrast style={{ marginBottom: '1rem' }} />
      )}
      {receipt && (
        <InlineNotification
          kind="success"
          title="Verdict recorded"
          subtitle={`${receipt.effect} — ${receipt.operation_id}`}
          lowContrast
          style={{ marginBottom: '1rem' }}
        />
      )}

      <Grid>
        <Column sm={4} md={8} lg={16}>
          <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'center', marginBottom: '1rem' }}>
            <Button onClick={() => void load()} disabled={busy}>Refresh</Button>
            {identity && <Tag type="gray" size="sm">{identity.subject} ({identity.role})</Tag>}
            {ops.length > 0 && <Tag type="red" size="sm">{ops.length} unresolved</Tag>}
          </div>
        </Column>
      </Grid>

      <Grid>
        {/* Operation list */}
        <Column sm={4} md={4} lg={5}>
          <Section level={3}>
            <Heading>Pending Operations</Heading>
            <Stack gap={1}>
              {ops.length === 0 && (
                <Tile style={{ textAlign: 'center', padding: '2rem', color: '#7e8a9c' }}>
                  <p>No unresolved operations</p>
                  <p style={{ fontSize: '0.75rem', marginTop: '0.5rem' }}>All tool executions have been confirmed.</p>
                </Tile>
              )}
              {ops.map((op) => {
                const info = toolInfo(op.tool, op.server)
                const turn = turnOf(op.operation_id)
                return (
                  <Tile
                    key={`${op.run_id}:${op.operation_id}`}
                    onClick={() => setSelected(op)}
                    className={`workspace-tile ${selected?.operation_id === op.operation_id ? 'selected' : ''}`}
                    style={{ cursor: 'pointer', padding: '0.75rem' }}
                  >
                    <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
                      <span style={{ fontSize: '1.1rem' }}>{info.icon}</span>
                      <div style={{ flex: 1, minWidth: 0 }}>
                        <div style={{ fontWeight: 600, fontSize: '0.875rem' }}>{info.label}</div>
                        <div style={{ fontSize: '0.75rem', color: '#7e8a9c' }}>
                          {info.category}
                          {turn && ` · turn ${turn}`}
                        </div>
                      </div>
                    </div>
                    <div style={{ marginTop: '0.35rem', display: 'flex', gap: '0.35rem', flexWrap: 'wrap' }}>
                      <Tag type="warm-gray" size="sm">{REASON_SHORT[op.reason] ?? op.reason}</Tag>
                      <Tag type="gray" size="sm">{new Date(op.started_at).toLocaleString()}</Tag>
                    </div>
                  </Tile>
                )
              })}
            </Stack>
          </Section>
        </Column>

        {/* Detail panel */}
        <Column sm={4} md={4} lg={11}>
          <Section level={3}>
            {!selected ? (
              <div style={{ textAlign: 'center', color: '#7e8a9c', padding: '3rem 1rem' }}>
                <Heading>Select an operation</Heading>
                <p style={{ marginTop: '0.5rem' }}>Click an operation on the left to inspect it and record your verdict.</p>
              </div>
            ) : (
              <Stack gap={3}>
                {/* What happened */}
                <Tile>
                  <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', marginBottom: '0.75rem' }}>
                    <span style={{ fontSize: '1.5rem' }}>{toolInfo(selected.tool, selected.server).icon}</span>
                    <div>
                      <Heading style={{ fontSize: '1.1rem' }}>{toolInfo(selected.tool, selected.server).label}</Heading>
                      <div style={{ fontSize: '0.75rem', color: '#7e8a9c' }}>{toolInfo(selected.tool, selected.server).category}</div>
                    </div>
                  </div>

                  <div style={{
                    padding: '0.75rem 1rem',
                    background: '#1a1400',
                    border: '1px solid #3d3000',
                    borderRadius: '6px',
                    marginBottom: '0.75rem',
                  }}>
                    <div style={{ fontWeight: 600, color: '#e0af68', marginBottom: '0.25rem' }}>⚠ What happened</div>
                    <p style={{ fontSize: '0.875rem', color: '#c0caf5' }}>{reasonText(selected.reason)}</p>
                    <p style={{ fontSize: '0.75rem', color: '#7e8a9c', marginTop: '0.5rem' }}>
                      The agent executed this tool but the system couldn't confirm whether the side effect actually happened.
                      You need to check the external system and tell Temporality the outcome.
                    </p>
                  </div>
                </Tile>

                {/* Context */}
                <Tile>
                  <Heading style={{ fontSize: '0.875rem', marginBottom: '0.5rem' }}>Context</Heading>
                  <dl style={{ fontSize: '0.875rem' }}>
                    <div style={{ display: 'flex', gap: '0.5rem', marginBottom: '0.35rem' }}>
                      <dt style={{ color: '#7e8a9c', minWidth: '100px' }}>Run</dt>
                      <dd>
                        <a
                          href={`/agents?project=${encodeURIComponent(selected.project)}&run=${encodeURIComponent(selected.run_id)}`}
                          style={{ color: '#57d7e8' }}
                        >
                          {selected.run_id}
                        </a>
                      </dd>
                    </div>
                    {turnOf(selected.operation_id) && (
                      <div style={{ display: 'flex', gap: '0.5rem', marginBottom: '0.35rem' }}>
                        <dt style={{ color: '#7e8a9c', minWidth: '100px' }}>Turn</dt>
                        <dd>Turn {turnOf(selected.operation_id)}</dd>
                      </div>
                    )}
                    <div style={{ display: 'flex', gap: '0.5rem', marginBottom: '0.35rem' }}>
                      <dt style={{ color: '#7e8a9c', minWidth: '100px' }}>When</dt>
                      <dd>{new Date(selected.started_at).toLocaleString()}</dd>
                    </div>
                    <div style={{ display: 'flex', gap: '0.5rem', marginBottom: '0.35rem' }}>
                      <dt style={{ color: '#7e8a9c', minWidth: '100px' }}>Tool</dt>
                      <dd><code style={{ fontSize: '0.8rem' }}>{selected.tool}{selected.server ? ` @ ${selected.server}` : ''}</code></dd>
                    </div>
                    <div style={{ display: 'flex', gap: '0.5rem' }}>
                      <dt style={{ color: '#7e8a9c', minWidth: '100px' }}>Args hash</dt>
                      <dd><code style={{ fontSize: '0.7rem', color: '#565f89' }}>{selected.arguments_hash ?? '—'}</code></dd>
                    </div>
                  </dl>
                </Tile>

                {/* Verdict */}
                {!operator ? (
                  <Tile>
                    <p style={{ color: '#7e8a9c' }}>
                      Recording a verdict requires an operator token.
                      {identity?.role && <> Current role: <strong>{identity.role}</strong>.</>}
                    </p>
                  </Tile>
                ) : (
                  <Tile>
                    <Heading style={{ fontSize: '0.875rem', marginBottom: '0.75rem' }}>Record your verdict</Heading>
                    <p style={{ fontSize: '0.75rem', color: '#7e8a9c', marginBottom: '0.75rem' }}>
                      Check the external system (database, API, file system) and tell Temporality what actually happened.
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
                              border: `2px solid ${effect === option ? (option === 'occurred' ? '#9ece6a' : option === 'none' ? '#f7768e' : '#e0af68') : '#344258'}`,
                              background: effect === option ? (option === 'occurred' ? 'rgba(158,206,106,0.1)' : option === 'none' ? 'rgba(247,118,142,0.1)' : 'rgba(224,175,104,0.1)') : 'transparent',
                              color: '#e5e9f0',
                              cursor: 'pointer',
                              textAlign: 'center',
                              fontSize: '0.875rem',
                            }}
                          >
                            <div style={{ fontWeight: 600, marginBottom: '0.25rem', color: option === 'occurred' ? '#9ece6a' : option === 'none' ? '#f7768e' : '#e0af68' }}>
                              {option === 'occurred' ? '✓ Happened' : option === 'none' ? '✗ Did NOT happen' : '? Unknown'}
                            </div>
                            <div style={{ fontSize: '0.7rem', color: '#7e8a9c' }}>{EFFECT_HELP[option]}</div>
                          </button>
                        ))}
                      </div>
                      <TextArea
                        id="note"
                        labelText="What did you find?"
                        value={note}
                        onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => setNote(e.target.value)}
                        placeholder="e.g. Checked the database — the row was inserted twice. Or: File was not created, safe to retry."
                        rows={3}
                      />
                      <Stack orientation="horizontal" gap={2}>
                        <Button onClick={() => void submit()} disabled={busy}>
                          Submit verdict
                        </Button>
                        <Button kind="secondary" onClick={() => setSelected(null)}>Cancel</Button>
                      </Stack>
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
