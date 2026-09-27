import { useCallback, useEffect, useState } from 'react'
import { API_BASE, authHeaders } from './api'
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
function json(value: unknown) { return JSON.stringify(value, null, 2) }

function initialProject(): string {
  try {
    return new URLSearchParams(window.location.search).get('project') ?? ''
  } catch {
    return ''
  }
}

const EFFECT_HELP: Record<ReconcileEffect, string> = {
  none: 'the effect did not land — the operation is safe to re-issue',
  occurred: 'the effect landed downstream — verified against the external system',
  unknown: 'the outcome could not be established — documented as unresolved',
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
          title="Reconciliation recorded"
          subtitle={`${receipt.event_id} for ${receipt.operation_id} · effect ${receipt.effect}`}
          lowContrast
          style={{ marginBottom: '1rem' }}
        />
      )}

      <Grid>
        <Column sm={4} md={8} lg={16}>
          <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'flex-end', marginBottom: '1rem' }}>
            <Button onClick={() => void load()} disabled={busy}>Load operations</Button>
            {identity && <Tag type="gray">{identity.subject} ({identity.role})</Tag>}
          </div>
        </Column>
      </Grid>

      <Grid>
        <Column sm={4} md={3} lg={4}>
          <Section level={3}>
            <Heading>Unresolved ({ops.length})</Heading>
            <Stack gap={2}>
              {ops.length === 0 && <Tile><p>No uncertain operations</p></Tile>}
              {ops.map((op) => (
                <Tile
                  key={`${op.run_id}:${op.operation_id}`}
                  onClick={() => setSelected(op)}
                  className={`workspace-tile ${selected?.operation_id === op.operation_id ? 'selected' : ''}`}
                >
                  <strong>{op.tool}{op.server ? ` @ ${op.server}` : ''}</strong>
                  <br />
                  <Tag type="warm-gray" size="sm">{op.reason}</Tag>
                  <Tag type="gray" size="sm">{op.run_id}</Tag>
                  <br />
                  <small style={{ color: '#7e8a9c' }}>{new Date(op.started_at).toLocaleString()}</small>
                </Tile>
              ))}
            </Stack>
          </Section>
        </Column>

        <Column sm={4} md={5} lg={12}>
          <Section level={3}>
            <Heading>{selected ? selected.state : (ops.length ? 'Select an operation' : 'Nothing to reconcile')}</Heading>
            {!selected ? (
              <Tile><p>Select an operation to view details and record a verdict</p></Tile>
            ) : (
              <Stack gap={3}>
                <Tile>
                  <Heading>{selected.tool}{selected.server ? ` @ ${selected.server}` : ''}</Heading>
                  <p>{reasonText(selected.reason)}</p>
                  <dl>
                    <div><dt>run</dt><dd><a href={`/agents?project=${encodeURIComponent(selected.project)}&run=${encodeURIComponent(selected.run_id)}`}>{selected.run_id}</a></dd></div>
                    <div><dt>operation</dt><dd><code>{selected.operation_id}</code></dd></div>
                    <div><dt>arguments hash</dt><dd><code>{selected.arguments_hash ?? 'unavailable'}</code></dd></div>
                    <div><dt>started</dt><dd>{new Date(selected.started_at).toLocaleString()}</dd></div>
                    <div><dt>event</dt><dd><code>{selected.started_event_id}</code></dd></div>
                  </dl>
                </Tile>

                {!operator ? (
                  <Tile><p style={{ color: '#7e8a9c' }}>Requires operator token (current: {identity?.role ?? 'unknown'})</p></Tile>
                ) : (
                  <Tile>
                    <Heading>Record Verdict</Heading>
                    <Stack gap={3}>
                      <Select id="effect" labelText="Effect" value={effect} onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setEffect(e.target.value as ReconcileEffect)}>
                        {(['none', 'occurred', 'unknown'] as ReconcileEffect[]).map((option) => (
                          <SelectItem key={option} value={option} text={`${option} — ${EFFECT_HELP[option]}`} />
                        ))}
                      </Select>
                      <TextArea id="note" labelText="Note" value={note} onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => setNote(e.target.value)} placeholder="verified the downstream system…" rows={3} />
                      <TextInput id="actor" labelText="Actor ID" value={actor} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setActor(e.target.value)} placeholder="human-ui" />
                      <Stack orientation="horizontal" gap={2}>
                        <Button onClick={() => void submit()} disabled={busy}>Record verdict</Button>
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