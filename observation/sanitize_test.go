package observation

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

var testOccurredAt = time.Date(2026, 11, 10, 7, 11, 51, 0, time.UTC)

func TestScrubNULString(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"clean string untouched", "git status --porcelain", "git status --porcelain"},
		{"nul removed", "ls-files\x00-z", "ls-files-z"},
		{"all nuls removed", "\x00\x00\x00", ""},
		{"binary blob excerpt", "diff --git a/bin\x00 b/bin", "diff --git a/bin b/bin"},
	}
	for _, tc := range cases {
		if got := scrubNULString(tc.input); got != tc.want {
			t.Errorf("%s: scrubNULString(%q) = %q, want %q", tc.name, tc.input, got, tc.want)
		}
	}
}

func TestScrubNULValueWalksPayload(t *testing.T) {
	input := map[string]any{
		"output":      "listing\x00file",
		"nested":      map[string]any{"detail": "err\x00or", "count": 3},
		"list":        []any{"a\x00b", 1.5, true, nil, []any{"deep\x00"}},
		"\x00key\x00": "value",
		"number":      42,
	}
	scrubbed := ScrubNULValue(input).(map[string]any)

	if scrubbed["output"] != "listingfile" {
		t.Errorf("output = %v, want listingfile", scrubbed["output"])
	}
	nested := scrubbed["nested"].(map[string]any)
	if nested["detail"] != "error" || nested["count"] != 3 {
		t.Errorf("nested = %v, want detail=error count=3", nested)
	}
	list := scrubbed["list"].([]any)
	if list[0] != "ab" || list[1] != 1.5 || list[2] != true || list[3] != nil {
		t.Errorf("list = %v, want ab/1.5/true/nil preserved", list)
	}
	if deep := list[4].([]any); deep[0] != "deep" {
		t.Errorf("deep list = %v, want deep", deep)
	}
	if _, ok := scrubbed["key"]; !ok {
		t.Errorf("scrubbed key missing: %v", scrubbed)
	}
	if _, ok := scrubbed["\x00key\x00"]; ok {
		t.Errorf("raw NUL key must be scrubbed: %v", scrubbed)
	}
	if scrubbed["number"] != 42 {
		t.Errorf("number = %v, want 42", scrubbed["number"])
	}
}

// The exact production failure: a tool.completed event carrying shell output
// with NUL bytes. json.Marshal escapes them as \u0000, which PostgreSQL
// rejects on JSONB insert (SQLSTATE 22P05) — the emit failed and killed the
// agent run. Scrubbing before the marshal must produce insertable JSONB.
func TestScrubNULEventMarshalsWithoutEscapes(t *testing.T) {
	event := Event{
		Schema: Schema, EventID: "run/event/000001", OccurredAt: testOccurredAt,
		Source:  Source{ID: "kernel-test", Integration: "temporality-agent-kernel"},
		Context: Context{Project: "p", Run: "r"},
		Type:    "tool.completed",
		Data: map[string]any{
			"tool":   "run_command",
			"output": "git\x00-z listing",
		},
		Evidence: []Evidence{{Ref: "ref\x00with-nul", Type: "file"}},
	}

	poisoned, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal unscrubbed event: %v", err)
	}
	if !strings.Contains(string(poisoned), `\u0000`) {
		t.Fatalf("precondition: unscrubbed event must marshal with \\u0000 escapes, got: %s", poisoned)
	}

	clean := ScrubNULEvent(event)
	payload, err := json.Marshal(clean)
	if err != nil {
		t.Fatalf("marshal scrubbed event: %v", err)
	}
	if strings.Contains(string(payload), `\u0000`) {
		t.Fatalf("\\u0000 survived the scrub: %s", payload)
	}
	if !strings.Contains(string(payload), `"refwith-nul"`) {
		t.Fatalf("evidence ref must be scrubbed in place, got: %s", payload)
	}
	var decoded Event
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("scrubbed payload must stay valid JSON: %v\n%s", err, payload)
	}
	if decoded.Data["output"] != "git-z listing" {
		t.Errorf("output = %q, want git-z listing", decoded.Data["output"])
	}
}

func TestScrubNULEventHandlesEmptyCollections(t *testing.T) {
	event := Event{Schema: Schema, EventID: "e", OccurredAt: testOccurredAt,
		Source: Source{ID: "s", Integration: "i"}, Type: "run.started"}
	if got := ScrubNULEvent(event); got.Data != nil || got.Evidence != nil {
		t.Fatalf("nil data/evidence must pass through, got %v/%v", got.Data, got.Evidence)
	}
}

func TestScrubNULIsDeterministic(t *testing.T) {
	input := map[string]any{"output": "a\x00b"}
	first := string(mustMarshal(t, ScrubNULValue(input)))
	second := string(mustMarshal(t, ScrubNULValue(input)))
	if first != second {
		t.Fatalf("scrubbing must be deterministic: %q vs %q", first, second)
	}
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	payload, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return payload
}
