import { useCallback, useEffect, useState } from 'react'
import { API_BASE, authHeaders } from './api'
import Token from './Token'
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

// window-free URL state so the component also renders in plain vitest.
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

// Operations is the reconciliation console: operations whose effect state the
// event stream cannot settle on its own, with the operator verdict form.
export default function Operations() {
  const [project, setProject] = useState(initialProject())
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
    // Identity and the first listing load once; later refreshes are explicit.
    // eslint-disable-next-line react-hooks/exhaustive-deps
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
  return <div className="observability-shell agent-runs-shell">
    <header className="topbar obs-topbar"><div><span className="eyebrow">TEMPORALITY / AGENT KERNEL</span><h1>Operations</h1></div><div className="header-actions"><span className="connection">Kernel <code>:8090</code> · Events <code>{API_BASE}</code>{identity && <span> · {identity.subject} ({identity.role})</span>}</span><a className="obs-link" href="/experience">Experience</a><a className="obs-link" href="/agents">Agent runs</a><a className="obs-link" href="/observability">Knowledge</a><Token /></div></header>
    <form className="obs-controls agent-controls" onSubmit={(event) => { event.preventDefault(); void load() }}><label>Project ID<input value={project} onChange={(event) => setProject(event.target.value)} placeholder="repo-a" required /></label><button className="primary" disabled={busy}>{busy ? 'Loading…' : 'Load operations'}</button></form>
    {error && <div className="obs-error" role="alert">{error}</div>}
    {receipt && <div className="obs-controls" role="status">Recorded <strong>{receipt.event_id}</strong> for {receipt.operation_id} · effect {receipt.effect}. The operation is settled and left the list.</div>}
    <main className="agent-grid">
      <aside className="obs-panel agent-list"><header><span className="eyebrow">UNRESOLVED OPERATIONS</span><strong>{ops.length} open</strong></header>
        {!ops.length ? <p className="obs-empty">Нет операций с неопределённым эффектом. Если run упал между выполнением инструмента и фиксацией результата, он появится здесь.</p> : ops.map((op) => <button key={`${op.run_id}:${op.operation_id}`} className={`agent-run ${selected?.operation_id === op.operation_id ? 'selected' : ''}`} onClick={() => setSelected(op)}><time>{new Date(op.started_at).toLocaleString()}</time><strong>{op.tool}{op.server ? ` @ ${op.server}` : ''}</strong><span>{op.reason} · {op.run_id}</span></button>)}
      </aside>
      <section className="obs-panel agent-detail"><header><span className="eyebrow">OPERATION & VERDICT</span><strong>{selected ? selected.state : (ops.length ? 'select an operation' : 'nothing to reconcile')}</strong></header>
        {!selected ? <p className="obs-empty">Выберите операцию слева, чтобы увидеть детали и записать вердикт оператора. Вердикт пишет производное событие operation.reconciled и сеттлит операцию.</p> : <>
          <article className="agent-approval">
            <h2>{selected.tool}{selected.server ? ` @ ${selected.server}` : ''}</h2>
            <p>{reasonText(selected.reason)}</p>
            <dl>
              <div><dt>run</dt><dd><a href={`/agents?project=${encodeURIComponent(selected.project)}&run=${encodeURIComponent(selected.run_id)}`}>{selected.run_id}</a></dd></div>
              <div><dt>operation</dt><dd><code>{selected.operation_id}</code></dd></div>
              <div><dt>arguments hash</dt><dd><code>{selected.arguments_hash ?? 'unavailable'}</code></dd></div>
              <div><dt>started</dt><dd>{new Date(selected.started_at).toLocaleString()}</dd></div>
              <div><dt>event</dt><dd><code>{selected.started_event_id}</code></dd></div>
            </dl>
            <details><summary>raw operation</summary><pre>{json(selected)}</pre></details>
            {!operator && <p className="obs-empty">Запись вердикта требует токен с ролью operator. Текущий токен ({identity?.role ?? 'unknown'}) может только просматривать.</p>}
            {operator && <>
              <label>Effect<select value={effect} onChange={(event) => setEffect(event.target.value as ReconcileEffect)}>{(['none', 'occurred', 'unknown'] as ReconcileEffect[]).map((option) => <option key={option} value={option}>{option} — {EFFECT_HELP[option]}</option>)}</select></label>
              <label>Note<input value={note} onChange={(event) => setNote(event.target.value)} placeholder="verified the downstream system: …" /></label>
              <label>Actor ID<input value={actor} onChange={(event) => setActor(event.target.value)} placeholder="human-ui" /></label>
              <div className="agent-actions"><button className="primary" disabled={busy} onClick={() => void submit()}>{busy ? 'Recording…' : 'Record verdict'}</button><button disabled={busy} onClick={() => setSelected(null)}>Cancel</button></div>
            </>}
          </article>
        </>}
      </section>
    </main>
  </div>
}
