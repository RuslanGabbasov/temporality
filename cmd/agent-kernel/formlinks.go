package main

// Hosted form links (docs/interactive-HITL.md §6.2): a chat-delivered
// ask_human question carries a button with a scoped-secret link. Anyone with
// the link opens a minimalist standalone page — no harness account — answers
// the form, and closes the tab. The token IS the credential for exactly one
// question; the answer goes through the same validation and workflow signal
// as every other channel, and the audit trail records it as 'form-link'.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/workspace"
)

// formlinkSignaler signals the paused workflow — the same Approval payload
// every other channel answers with. *temporalClientAdapter in tests.
type formlinkSignaler interface {
	SignalFormAnswer(ctx context.Context, project, runID, operationID, canonicalResponse, actor string) error
}

// formlinkStore is the store face the routes need (*workspace.Store subset).
type formlinkStore interface {
	GetFormLink(ctx context.Context, token string) (workspace.FormLink, error)
	MarkFormLinkAnswered(ctx context.Context, token string) error
	GetHumanRequest(ctx context.Context, id string) (workspace.HumanRequest, error)
	AnswerHumanRequest(ctx context.Context, id, response, answeredBy string) (workspace.HumanRequest, error)
	RecordAccessAudit(ctx context.Context, actor, action, entityKind, entityID string, details map[string]any) error
}

// RegisterFormLinkRoutes mounts the public (token-authed) form page and the
// answer API, plus the internal mint endpoint the notification activity uses.
func RegisterFormLinkRoutes(mux *http.ServeMux, ws *workspace.Store, gate *controlplane.Gate, signaler formlinkSignaler, log interface{ Warn(string, ...any) }) {
	// Internal mint: the notification activity creates a link right before
	// delivering a schema ask into a channel. Internal token only.
	mux.HandleFunc("POST /v1/workspace/form-links", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleOperator) {
			return
		}
		var req struct {
			OperationID string `json:"operation_id"`
			RunID       string `json:"run_id"`
			ProjectID   string `json:"project_id"`
			AgentID     string `json:"agent_id"`
			LifetimeH   int    `json:"lifetime_hours"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		if req.OperationID == "" {
			writeError(w, 422, errors.New("operation_id is required"))
			return
		}
		lifetime := time.Duration(req.LifetimeH) * time.Hour
		if lifetime <= 0 || lifetime > 30*24*time.Hour {
			lifetime = 7 * 24 * time.Hour
		}
		link, err := ws.MintFormLink(r.Context(), req.OperationID, req.RunID, req.ProjectID, req.AgentID, "notify", lifetime)
		if err != nil {
			writeError(w, 409, err)
			return
		}
		writeJSON(w, 201, link)
	})

	// The page itself: token in the path is the credential. No gate.
	mux.HandleFunc("GET /forms/{token}", func(w http.ResponseWriter, r *http.Request) {
		token := r.PathValue("token")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		fmt.Fprint(w, formLinkPageHTML(token))
	})

	// Question data for the page. The token decides; a consumed/expired link
	// answers 409/410 with a machine code the page renders as a closed note.
	mux.HandleFunc("GET /forms/{token}/data", func(w http.ResponseWriter, r *http.Request) {
		link, question, err := liveFormLink(r, ws, r.PathValue("token"))
		if err != nil {
			writeFormLinkError(w, err)
			return
		}
		_ = link
		writeJSON(w, 200, map[string]any{
			"question":         question.Question,
			"context":          question.Context,
			"options":          question.Options,
			"schema":           rawOrEmpty(question.Schema),
			"expires_at":       link.ExpiresAt,
			"already_answered": question.Response != "",
		})
	})

	// The answer: same validation and signal path as every other channel.
	mux.HandleFunc("POST /forms/{token}/answer", func(w http.ResponseWriter, r *http.Request) {
		link, question, err := liveFormLink(r, ws, r.PathValue("token"))
		if err != nil {
			writeFormLinkError(w, err)
			return
		}
		if question.Response != "" {
			writeError(w, 409, errors.New("the question is already answered"))
			return
		}
		var body struct {
			Values map[string]any `json:"values"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<18)).Decode(&body); err != nil {
			writeError(w, 400, err)
			return
		}
		if err := workspace.ValidateFormValues(question.Schema, body.Values); err != nil {
			writeError(w, 422, err)
			return
		}
		canonical, err := workspace.CanonicalFormValues(body.Values)
		if err != nil {
			writeError(w, 422, err)
			return
		}
		const actor = "form-link"
		if _, err := ws.AnswerHumanRequest(r.Context(), link.OperationID, canonical, actor); err != nil {
			writeError(w, 409, err)
			return
		}
		if err := ws.MarkFormLinkAnswered(r.Context(), link.Token); err != nil {
			log.Warn("form link: mark answered", "error", err.Error())
		}
		if err := signaler.SignalFormAnswer(r.Context(), link.ProjectID, link.RunID, link.OperationID, canonical, actor); err != nil {
			// The row is closed and the audit written; a Temporal hiccup here
			// means the workflow resumes on its next liveness tick or the run
			// was already terminal — either way the answer is not lost: it is
			// on the row and in the audit trail.
			log.Warn("form link: signal", "operation", link.OperationID, "error", err.Error())
		}
		_ = ws.RecordAccessAudit(r.Context(), actor, workspace.AuditHumanAnswered, "human_request", link.OperationID, map[string]any{
			"via": "form-link", "run_id": link.RunID,
		})
		writeJSON(w, 200, map[string]any{"ok": true, "response": canonical})
	})
}

// liveFormLink resolves a token into a live link plus its open question.
func liveFormLink(r *http.Request, ws *workspace.Store, token string) (workspace.FormLink, workspace.HumanRequest, error) {
	link, err := ws.GetFormLink(r.Context(), token)
	if err != nil {
		return workspace.FormLink{}, workspace.HumanRequest{}, err
	}
	question, err := ws.GetHumanRequest(r.Context(), link.OperationID)
	if err != nil {
		return workspace.FormLink{}, workspace.HumanRequest{}, err
	}
	return link, question, nil
}

func writeFormLinkError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, workspace.ErrFormLinkConsumed):
		writeJSON(w, 409, map[string]any{"error": "form_link_consumed"})
	case errors.Is(err, workspace.ErrFormLinkExpired):
		writeJSON(w, 410, map[string]any{"error": "form_link_expired"})
	case errors.Is(err, workspace.ErrNotFound):
		writeError(w, 404, errors.New("unknown form link"))
	default:
		writeError(w, 500, err)
	}
}

func rawOrEmpty(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("{}")
	}
	return raw
}

// formLinkPageHTML serves the standalone form page: no harness shell, no
// external assets (works behind restricted egress), one inline script that
// fetches the question, renders the supported schema subset, validates
// lightly and posts the answer. The page speaks three states: the form, a
// success note, and a closed/expired note.
func formLinkPageHTML(token string) string {
	tokenJS, _ := json.Marshal(token)
	return `<!doctype html>
<html lang="en"><head><meta charset="utf-8"/>
<meta name="viewport" content="width=device-width, initial-scale=1"/>
<title>Temporality — form</title>
<style>
:root { color-scheme: light dark; }
body { font: 15px/1.5 -apple-system, 'Segoe UI', Roboto, sans-serif; margin: 0; background: #f5f6f7; color: #1c1f23; }
.dark body, body { }
main { max-width: 560px; margin: 2rem auto; padding: 0 1rem 3rem; }
h1 { font-size: 1.15rem; margin: 0 0 .4rem; }
.ctx { color: #5a6572; font-size: .85rem; white-space: pre-wrap; margin: 0 0 1rem; }
.field { margin: .8rem 0; }
.field label { display: block; font-size: .8rem; color: #39424d; margin-bottom: .25rem; }
.field .req { color: #c62828; }
.field input, .field select, .field textarea { width: 100%; box-sizing: border-box; font: inherit; padding: .45rem .55rem; border: 1px solid #c9cfd6; border-radius: 6px; background: #fff; color: inherit; }
.field .hint { font-size: .72rem; color: #7a848f; margin-top: .15rem; }
.field .err { font-size: .72rem; color: #c62828; margin-top: .15rem; }
.checks label { display: flex; gap: .4rem; align-items: center; font-size: .9rem; margin: .2rem 0; }
.checks input { width: auto; }
button { font: inherit; padding: .5rem 1.1rem; border: 0; border-radius: 6px; background: #0f766e; color: #fff; cursor: pointer; }
button:disabled { opacity: .5; cursor: default; }
.note { padding: .7rem .9rem; border-radius: 6px; margin: 1rem 0; font-size: .9rem; }
.note.ok { background: #e5f3ef; border: 1px solid #9fd4c6; }
.note.bad { background: #fdecec; border: 1px solid #f2b8b5; }
table { border-collapse: collapse; width: 100%; font-size: .8rem; }
td, th { border: 1px solid #d7dce1; padding: .3rem .5rem; text-align: left; }
</style></head><body><main id="app"><p>…</p></main>
<script>
const TOKEN = ` + string(tokenJS) + `;
const app = document.getElementById('app');
const esc = s => String(s ?? '').replace(/[&<>"]/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'}[c]));
const note = (cls, text) => { app.innerHTML = '<div class="note ' + cls + '">' + esc(text) + '</div><p style="font-size:.8rem;color:#7a848f">' + esc('You can close this tab.') + '</p>'; };
const errText = (status, body) => {
  try { const b = JSON.parse(body); if (b.error === 'form_link_consumed') return 'This form was already answered.'; if (b.error === 'form_link_expired') return 'This form link has expired.'; if (b.cause || b.error) return (b.cause ? b.cause + ': ' : '') + (b.error || ''); } catch (e) {}
  return 'HTTP ' + status;
};
function fieldHTML(name, f, required) {
  const label = esc(f.title || name) + (required ? ' <span class="req">*</span>' : '');
  const desc = f.description ? '<div class="hint">' + esc(f.description) + '</div>' : '';
  const ph = f.pattern ? '<div class="hint">format: ' + esc(f.pattern) + '</div>' : '';
  if (f.type === 'boolean') {
    return '<div class="field checks"><label><input type="checkbox" data-f="' + esc(name) + '" data-t="bool"' + (required ? ' data-req="1"' : '') + '/><span>' + label + '</span></label>' + desc + '</div>';
  }
  if (Array.isArray(f.enum) && f.enum.length && f.type !== 'array') {
    const opts = '<option value="" disabled selected>—</option>' + f.enum.map(o => '<option value="' + esc(o) + '">' + esc(o) + '</option>').join('');
    return '<div class="field"><label>' + label + '</label><select data-f="' + esc(name) + '" data-t="enum"' + (required ? ' data-req="1"' : '') + '>' + opts + '</select>' + desc + ph + '</div>';
  }
  if (f.type === 'array') {
    if (f.enum && f.enum.length) {
      const boxes = f.enum.map(o => '<label><input type="checkbox" data-f="' + esc(name) + '" data-t="multi" value="' + esc(o) + '"' + (required ? ' data-req="1"' : '') + '/><span>' + esc(o) + '</span></label>').join('');
      return '<div class="field checks">' + label + '<div class="checks">' + boxes + '</div>' + desc + '</div>';
    }
    if (f.items && f.items.format === 'date' && f.minItems === 2 && f.maxItems === 2) {
      return '<div class="field"><label>' + label + '</label><input type="date" data-f="' + esc(name) + '" data-t="date2a"' + (required ? ' data-req="1"' : '') + '/> → <input type="date" data-f2="' + esc(name) + '" data-t="date2b"/></div>' + desc;
    }
    return '<div class="field"><label>' + label + '</label><textarea rows="2" data-f="' + esc(name) + '" data-t="list" placeholder="one per line"></textarea>' + desc + '</div>';
  }
  const types = { 'date': 'date', 'date-time': 'datetime-local', 'time': 'time', 'email': 'email', 'url': 'url' };
  const itype = f.format && types[f.format] ? types[f.format] : f.format === 'password' ? 'password' : (f.type === 'integer' || f.type === 'number') ? 'number' : 'text';
  const extra = f.type === 'integer' ? ' step="1"' : f.type === 'number' ? ' step="any"' : '';
  return '<div class="field"><label>' + label + '</label><input type="' + itype + '"' + extra + ' data-f="' + esc(name) + '" data-t="' + (isNumber(f) ? 'num' : 'str') + '"' + (required ? ' data-req="1"' : '') + '/>' + desc + ph + '</div>';
}
function isNumber(f) { return f.type === 'number' || f.type === 'integer'; }
fetch('/forms/' + encodeURIComponent(TOKEN) + '/data').then(async r => {
  const body = await r.text();
  if (r.status !== 200) { note('bad', errText(r.status, body)); return; }
  const q = JSON.parse(body);
  const schema = q.schema && q.schema.properties ? q.schema : { properties: {}, required: [] };
  let html = '<h1>' + esc(q.question) + '</h1>';
  if (q.context) html += '<div class="ctx">' + esc(q.context) + '</div>';
  if (Object.keys(schema.properties || {}).length) {
    html += '<form id="f">';
    const required = new Set(schema.required || []);
    for (const [name, f] of Object.entries(schema.properties)) {
      html += fieldHTML(name, f, required.has(name));
    }
    html += '</form><button id="send">' + (window.SUBMIT_LABEL || 'Send') + '</button>';
  } else {
    html += '<form id="f"><div class="field"><label>Answer</label><textarea rows="3" data-f="__answer" data-t="str" data-req="1"></textarea></div></form><button id="send">Send</button>';
  }
  app.innerHTML = html;
  document.getElementById('send').onclick = async () => {
    const values = {};
    app.querySelectorAll('[data-f]').forEach(el => {
      const name = el.dataset.f, type = el.dataset.t;
      if (type === 'multi') {
        values[name] = values[name] || [];
        if (el.checked) values[name].push(el.value);
        return;
      }
      if (type === 'date2a') {
        const b = app.querySelector('[data-f2="' + name + '"]').value;
        if (el.value && b) values[name] = [el.value, b];
        return;
      }
      if (type === 'bool') { values[name] = el.checked; return; }
      let v = el.value;
      if (v === '' || v == null) return;
      if (type === 'num') v = Number(v);
      if (type === 'enum' || type === 'list' || type === 'str' || type === 'num') values[name] = v;
    });
    const btn = document.getElementById('send');
    btn.disabled = true;
    const resp = await fetch('/forms/' + encodeURIComponent(TOKEN) + '/answer', {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ values })
    });
    const body = await resp.text();
    if (resp.status === 200) { note('ok', 'Answer recorded — thank you.'); return; }
    btn.disabled = false;
    let msg = errText(resp.status, body);
    try { const b = JSON.parse(body); if (b.error && String(b.error).indexOf('form validation failed') >= 0) msg = b.error; } catch (e) {}
    note('bad', msg);
  };
}).catch(() => note('bad', 'The form service is unreachable — try again later.'));
</script></body></html>`
}

// kernelFormSignal adapts the Temporal client to the form-answer signal: the
// same Approval payload every channel answers with, routed through the
// kernel's default source id (form links are minted only for kernel-delivered
// asks). A Temporal hiccup surfaces to the caller, which logs it — the answer
// is already on the row and in the audit trail, and the workflow's own
// liveness path re-reads it.
type kernelFormSignal struct {
	client interface {
		SignalWorkflow(ctx context.Context, workflowID, runID, signalName string, arg any) error
	}
	sourceID string
}

func (k kernelFormSignal) SignalFormAnswer(ctx context.Context, project, runID, operationID, canonicalResponse, actor string) error {
	workflowID := agent.WorkflowID(k.sourceID, project, runID)
	return k.client.SignalWorkflow(ctx, workflowID, "", agent.ApprovalSignal, agent.Approval{
		OperationID: operationID,
		Approved:    true,
		Response:    canonicalResponse,
		ActorID:     actor,
	})
}
