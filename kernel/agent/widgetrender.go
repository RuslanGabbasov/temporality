package agent

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Widget HTML rendering (Bot API 10.4 sendWidget): the neutral WidgetModel
// becomes a self-contained HTML widget — a tiny state-driven form that
// renders from the widget's data channel and posts compact ops back.
//
// The widget is deliberately "dumb": every value change, validation and the
// submit decision live in the kernel (widgetform.go). The transport's op cap
// (64 bytes) and data cap (1024 bytes) shape the protocol — see the op JSON
// in widgetPost below.
//
// Client bridge note: the Bot API reference documents only the server side
// of widgets; the widget's client-side query bridge is the single
// integration point of this file (widgetPost). Adjust it there when the
// official widget client API surface is published — nothing else in the
// kernel touches the bridge.

// telegramWidgetEsc escapes text for HTML text nodes inside the widget
// (values arrive from user input and schema titles).
func telegramWidgetEsc(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;")
	return r.Replace(s)
}

// telegramWidgetHTML renders the model into the widget document. The initial
// model embeds inline; the widget re-renders from chat_data updates the
// kernel pushes after every accepted op.
func telegramWidgetHTML(model WidgetModel, formURL, lang string) (string, error) {
	modelJSON, err := json.Marshal(model)
	if err != nil {
		return "", fmt.Errorf("widget html: %w", err)
	}
	labels := widgetLabels(lang)
	// The template stays small (32KB raw / 4096B zlib caps): one function per
	// concern, no dependencies.
	html := `<!DOCTYPE html><html><head><meta charset="utf-8"><style>
*{box-sizing:border-box;font-family:-apple-system,'Segoe UI',Roboto,sans-serif}
body{margin:8px;font-size:13px;color:var(--tg-theme-text-color,#111)}
.f{margin-bottom:10px}.f label{display:block;font-weight:600;margin-bottom:3px}
.f .hint{font-size:11px;opacity:.7;margin-bottom:3px}
.f .err{font-size:11px;color:#d54a4a;margin-top:2px}
input[type=text],input[type=number],input[type=date]{width:100%;padding:6px 8px;border:1px solid var(--tg-theme-hint-color,#bbb);border-radius:6px;background:transparent;color:inherit}
.cho{display:flex;flex-direction:column;gap:3px}.cho label{font-weight:400;display:flex;gap:6px;align-items:center}
button{width:100%;padding:8px;border:0;border-radius:8px;background:var(--tg-theme-button-color,#2ea6ff);color:var(--tg-theme-button-text-color,#fff);font-weight:600;cursor:pointer}
button.sec{background:transparent;color:inherit;text-decoration:underline;font-weight:400}
.err-global{color:#d54a4a;font-size:12px;margin:6px 0}
.done{padding:10px;text-align:center;font-weight:600}
</style></head><body><div id="app"></div><script>
const M=__MODEL__;
const L=__LABELS__;
let model=M;
function esc(s){return String(s==null?'':s).replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]))}
function post(op){try{TelegramWidget.send(JSON.stringify(op))}catch(e){}}
function render(){
 const el=document.getElementById('app');
 if(model.s.done){el.innerHTML='<div class="done">✅ '+esc(L.done)+'</div>';return}
 let h='';
 model.f.forEach((f,i)=>{
  h+='<div class="f">';
  h+='<label>'+esc(f.t)+(f.rq?' *':'')+'</label>';
  if(f.h)h+='<div class="hint">'+esc(f.h)+'</div>';
  const val=model.s.v?model.s.v[f.n]:undefined;
  if(f.k==='bool'){h+='<label style="font-weight:400"><input type="checkbox" data-i="'+i+'" '+(val?'checked':'')+'> '+esc(L.yes)+'</label>'}
  else if(f.k==='choice'){h+='<div class="cho">'+(f.o||[]).map(o=>'<label><input type="radio" name="f'+i+'" data-i="'+i+'" value="'+esc(o)+'" '+(val===o?'checked':'')+'>'+esc(o)+'</label>').join('')+'</div>'}
  else if(f.k==='multi'){h+='<div class="cho">'+(f.o||[]).map(o=>'<label><input type="checkbox" data-mi="'+i+'" value="'+esc(o)+'" '+((val||[]).indexOf(o)>=0?'checked':'')+'>'+esc(o)+'</label>').join('')+'</div>'}
  else if(f.k==='daterange'){const a=(val||[])[0]||'',b=(val||[])[1]||'';h+='<input type="date" data-di="'+i+'" data-ds="0" value="'+esc(a)+'"> <input type="date" data-di="'+i+'" data-ds="1" value="'+esc(b)+'">'}
  else{const t=f.k==='int'||f.k==='num'?'number':'text';h+='<input type="'+t+'" data-i="'+i+'" value="'+esc(val==null?'':val)+'">'}
  if(f.err)h+='<div class="err">'+esc(f.err)+'</div>';
  h+='</div>';
 });
 h+='<div id="gerr" class="err-global"></div>';
 h+='<button id="sub">'+esc(L.submit)+'</button>';
 if(L.reset)h+='<button class="sec" id="res">'+esc(L.reset)+'</button>';
 el.innerHTML=h;
 el.querySelectorAll('[data-i]').forEach(inp=>inp.addEventListener('change',e=>{
  const i=+e.target.dataset.i;
  let v=e.target.value;
  if(e.target.type==='checkbox')v=e.target.checked;
  if(model.f[i].k==='int'||model.f[i].k==='num')v=parseFloat(v);
  post({op:'set',i:i,v:v});
 }));
 el.querySelectorAll('[data-mi]').forEach(inp=>inp.addEventListener('change',e=>{
  const i=+e.target.dataset.mi;
  const boxes=[...el.querySelectorAll('[data-mi="'+i+'"]')].filter(b=>b.checked).map(b=>b.value);
  post({op:'set',i:i,v:boxes});
 }));
 el.querySelectorAll('[data-di]').forEach(inp=>inp.addEventListener('change',e=>{
  const i=+e.target.dataset.di,slot=+e.target.dataset.ds;
  const cur=(model.s.v&&model.s.v[model.f[i].n])||[];
  const next=[slot===0?e.target.value:(cur[0]||''),slot===1?e.target.value:(cur[1]||'')];
  post({op:'set',i:i,v:next});
 }));
 const sub=document.getElementById('sub');
 if(sub)sub.addEventListener('click',()=>post({op:'submit'}));
 const res=document.getElementById('res');
 if(res)res.addEventListener('click',()=>post({op:'reset'}));
}
// Single integration point with the Telegram widget client bridge: the
// official client API surface is not part of the Bot API reference — adapt
// here if it differs. After an accepted op the kernel acknowledges via
// answerWidgetQuery and pushes the new model through editMessageWidgetData;
// the widget re-renders from whatever surface delivers it.
function applyModel(next){if(next){try{model=typeof next==='string'?JSON.parse(next):next;render()}catch(e){}}}
document.addEventListener('widgetdata',e=>applyModel(e.detail));
document.addEventListener('DOMContentLoaded',render);
</script></body></html>`
	html = strings.Replace(html, "__MODEL__", string(modelJSON), 1)
	html = strings.Replace(html, "__LABELS__", string(mustJSONWidget(labels)), 1)
	return html, nil
}

// widgetLabels carries the canned widget strings in the dialog language.
func widgetLabels(lang string) map[string]string {
	en := map[string]string{
		"done":   "Accepted — thank you!",
		"yes":    "Yes",
		"submit": "Submit",
		"reset":  "Reset",
	}
	if strings.HasPrefix(strings.ToLower(lang), "ru") {
		en["done"] = "Принято — спасибо!"
		en["yes"] = "Да"
		en["submit"] = "Отправить"
		en["reset"] = "Сбросить"
	}
	return en
}

func mustJSONWidget(v any) []byte {
	raw, _ := json.Marshal(v)
	return raw
}
