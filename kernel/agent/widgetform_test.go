package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

// The §7 example schema (docs/interactive-HITL.md) — the exact shape agents
// already attach to ask_human today.
const widgetTestSchema = `{"type":"object",
 "properties":{
   "channel":{"type":"string","enum":["canary","stable"],"title":"Канал"},
   "version":{"type":"string","minLength":5,"title":"Версия"},
   "replicas":{"type":"integer","minimum":1,"maximum":50},
   "confirm":{"type":"boolean","title":"Подтверждаю выкат"}},
 "required":["channel","version","confirm"]}`

func mustModel(t *testing.T, schema string) WidgetModel {
	t.Helper()
	model, err := BuildWidgetModel("Параметры релиза?", json.RawMessage(schema), WidgetState{Values: map[string]any{}})
	if err != nil {
		t.Fatalf("build model: %v", err)
	}
	return model
}

func TestBuildWidgetModelMapsSubset(t *testing.T) {
	model := mustModel(t, widgetTestSchema)
	if model.Question != "Параметры релиза?" || len(model.Fields) != 4 {
		t.Fatalf("model = %+v", model)
	}
	kinds := map[string]string{}
	for _, f := range model.Fields {
		kinds[f.Name] = f.Kind
		if f.Name == "channel" && !f.Required {
			t.Fatal("channel must be required")
		}
		if f.Name == "version" && f.MinLen != 5 {
			t.Fatal("version minLength lost")
		}
	}
	want := map[string]string{"channel": "choice", "version": "text", "replicas": "int", "confirm": "bool"}
	for name, kind := range want {
		if kinds[name] != kind {
			t.Fatalf("field %s kind = %q, want %q", name, kinds[name], kind)
		}
	}
}

func TestBuildWidgetModelRejectsBrokenSchema(t *testing.T) {
	for _, schema := range []string{"{oops", `{"type":"object"}`, `{"type":"object","properties":{"x":{"type":"alien"}}}`} {
		if _, err := BuildWidgetModel("q", json.RawMessage(schema), WidgetState{}); err == nil {
			t.Fatalf("schema %q must fail", schema)
		}
	}
}

func TestWidgetModelEncodeCap(t *testing.T) {
	model := mustModel(t, widgetTestSchema)
	encoded, err := model.Encode()
	if err != nil || len(encoded) > WidgetDataLimit {
		t.Fatalf("encode: %q err=%v", encoded, err)
	}
	// A schema with huge titles/hints overflows the cap and must refuse —
	// the delivery degrades to the hosted form instead of a broken widget.
	var big strings.Builder
	big.WriteString(`{"type":"object","properties":{`)
	for i := 0; i < 40; i++ {
		big.WriteString(`"f` + string(rune('a'+i%26)) + `":{"type":"string","title":"` + strings.Repeat("длинное-поле-", 20) + `"},`)
	}
	big.WriteString(`"z":{"type":"string"}}}`)
	if _, err := BuildWidgetModel("q", json.RawMessage(big.String()), WidgetState{}); err != nil {
		return // rejected at build — fine
	}
	model, err = BuildWidgetModel("q", json.RawMessage(big.String()), WidgetState{Values: map[string]any{}})
	if err == nil {
		if _, err = model.Encode(); err != ErrWidgetDataTooLarge {
			t.Fatalf("expected ErrWidgetDataTooLarge, got %v", err)
		}
	}
}

func TestWidgetSetAndSubmitFlow(t *testing.T) {
	model := mustModel(t, widgetTestSchema)

	set := func(t *testing.T, m WidgetModel, index int, value any) WidgetModel {
		t.Helper()
		op, err := DecodeWidgetOp(mustJSON(map[string]any{"op": "set", "i": index, "v": value}))
		if err != nil {
			t.Fatalf("decode op: %v", err)
		}
		next, ack, resolved, _, err := ApplyWidgetOp(m, op)
		if err != nil || resolved {
			t.Fatalf("set: err=%v resolved=%v", err, resolved)
		}
		if ack != "ok" {
			t.Fatalf("ack = %q", ack)
		}
		return next
	}
	// Alphabetical field order: channel=0, confirm=1, replicas=2, version=3.
	model = set(t, model, 0, "canary")
	model = set(t, model, 1, true)
	model = set(t, model, 2, float64(2))
	model = set(t, model, 3, "v1.2.3")

	op, _ := DecodeWidgetOp(`{"op":"submit"}`)
	next, ack, resolved, response, err := ApplyWidgetOp(model, op)
	if err != nil || !resolved {
		t.Fatalf("submit: err=%v resolved=%v", err, resolved)
	}
	if ack != "accepted" || !next.State.Done {
		t.Fatalf("ack=%q done=%v", ack, next.State.Done)
	}
	// Canonical JSON — exactly what a web-form submission produces (§7).
	if string(response) != `{"channel":"canary","confirm":true,"replicas":2,"version":"v1.2.3"}` {
		t.Fatalf("response = %s", response)
	}
	// A done widget ignores further ops.
	_, _, resolvedAgain, _, _ := ApplyWidgetOp(next, op)
	if resolvedAgain {
		t.Fatal("done widget resolved again")
	}
}

func TestWidgetSubmitValidationErrors(t *testing.T) {
	model := mustModel(t, widgetTestSchema)
	// Missing everything: submit fails, errors land on the required fields.
	op, _ := DecodeWidgetOp(`{"op":"submit"}`)
	next, ack, resolved, _, err := ApplyWidgetOp(model, op)
	if err != nil || resolved || ack != "invalid" {
		t.Fatalf("submit: err=%v resolved=%v ack=%q", err, resolved, ack)
	}
	errs := map[string]string{}
	for _, f := range next.Fields {
		if f.Error != "" {
			errs[f.Name] = f.Error
		}
	}
	if errs["channel"] == "" || errs["version"] == "" || errs["confirm"] == "" {
		t.Fatalf("required errors missing: %+v", errs)
	}
	if errs["replicas"] != "" {
		t.Fatalf("optional field flagged: %+v", errs)
	}
}

func TestWidgetConstraints(t *testing.T) {
	model := mustModel(t, widgetTestSchema)
	// replicas=99 violates maximum 50; the set itself accepts, the submit flags.
	op, _ := DecodeWidgetOp(`{"op":"set","i":2,"v":99}`)
	next, _, _, _, err := ApplyWidgetOp(model, op)
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	next, ack, resolved, _, _ := ApplyWidgetOp(next, mustOp(t, "submit"))
	if resolved || ack != "invalid" {
		t.Fatalf("constraint violated but resolved: ack=%q", ack)
	}
	for _, f := range next.Fields {
		if f.Name == "replicas" && f.Error == "" {
			t.Fatal("replicas error missing")
		}
	}
	// version shorter than minLength 5 flags at submit.
	next2, _, _, _, _ := ApplyWidgetOp(model, mustOp(t, "set", 3, "abc"))
	next2, _, _, _, _ = ApplyWidgetOp(next2, mustOp(t, "set", 0, "canary"))
	next2, _, _, _, _ = ApplyWidgetOp(next2, mustOp(t, "set", 1, true))
	next2, _, _, _, _ = ApplyWidgetOp(next2, mustOp(t, "set", 2, float64(2)))
	next2, _, resolved2, _, _ := ApplyWidgetOp(next2, mustOp(t, "submit"))
	if resolved2 {
		t.Fatal("short version accepted")
	}
	for _, f := range next2.Fields {
		if f.Name == "version" && f.Error == "" {
			t.Fatal("version error missing")
		}
	}
	_ = next
}

func TestWidgetResetAndBadOps(t *testing.T) {
	model := mustModel(t, widgetTestSchema)
	reset, _, resolved, _, err := ApplyWidgetOp(model, mustOp(t, "reset"))
	if err != nil || resolved || len(reset.State.Values) != 0 {
		t.Fatalf("reset: values=%v err=%v", reset.State.Values, err)
	}
	if _, err := DecodeWidgetOp(`{"op":"launch"}`); err == nil {
		t.Fatal("unknown op accepted")
	}
	if _, err := DecodeWidgetOp(strings.Repeat("x", 65)); err == nil {
		t.Fatal("oversized op accepted")
	}
	// Out-of-range index is rejected without touching the model.
	if _, _, _, _, err := ApplyWidgetOp(model, mustOp(t, "set", 99, "x")); err == nil {
		t.Fatal("bad index accepted")
	}
}

func TestWidgetDateRangeAndMulti(t *testing.T) {
	schema := `{"type":"object","properties":{
		"window":{"type":"array","minItems":2,"maxItems":2,"items":{"type":"string","format":"date"},"title":"Окно"},
		"tags":{"type":"array","items":{"enum":["a","b"]}}}}`
	model := mustModel(t, schema)
	// Alphabetical: tags=0 (multi), window=1 (daterange).
	if model.Fields[1].Kind != string(widgetDatePair) || model.Fields[0].Kind != string(widgetMulti) {
		t.Fatalf("kinds = %s / %s", model.Fields[0].Kind, model.Fields[1].Kind)
	}
	model, _, _, _, err := ApplyWidgetOp(model, mustOp(t, "set", 1, []any{"2026-01-05", "2026-01-02"}))
	if err != nil {
		t.Fatalf("set range: %v", err)
	}
	// Submit flags the reversed range, not a crash.
	next, _, resolved, _, _ := ApplyWidgetOp(model, mustOp(t, "submit"))
	if resolved {
		t.Fatal("reversed range accepted")
	}
	for _, f := range next.Fields {
		if f.Name == "window" && f.Error == "" {
			t.Fatal("range error missing")
		}
	}
	// Bad dates never enter the state.
	if _, _, _, _, err := ApplyWidgetOp(model, mustOp(t, "set", 1, []any{"not-a-date"})); err == nil {
		t.Fatal("bad date accepted")
	}
}

func mustOp(t *testing.T, op string, args ...any) WidgetOp {
	t.Helper()
	payload := map[string]any{"op": op}
	if len(args) > 0 {
		payload["i"] = args[0]
	}
	if len(args) > 1 {
		payload["v"] = args[1]
	}
	wop, err := DecodeWidgetOp(mustJSON(payload))
	if err != nil {
		t.Fatalf("decode op: %v", err)
	}
	return wop
}

func mustJSON(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(raw)
}
