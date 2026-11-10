package postgres

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/temporality-project/temporality/observation"
)

// One malformed row (jsonb allows scalars/arrays where the write path only
// produces objects) must degrade to a marker, never fail the whole listing —
// that failure mode kept /v1/agent/operations answering HTTP 500 forever.
func TestDecodeJSONObject(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want map[string]any
	}{
		{"object parses", `{"path":"pr-created"}`, map[string]any{"path": "pr-created"}},
		{"null is nil", `null`, nil},
		{"empty is nil", ``, nil},
		{"whitespace is nil", `  `, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := decodeJSONObject("src", "evt", []byte(tc.in))
			if tc.want == nil {
				if got != nil {
					t.Fatalf("decodeJSONObject(%q) = %v, want nil", tc.in, got)
				}
				return
			}
			encoded, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			expected, _ := json.Marshal(tc.want)
			if string(encoded) != string(expected) {
				t.Fatalf("decodeJSONObject(%q) = %s, want %s", tc.in, encoded, expected)
			}
		})
	}

	t.Run("array degrades to marker", func(t *testing.T) {
		got := decodeJSONObject("src", "evt", []byte(`[1,2,3]`))
		if got == nil || got["_unparsed_data"] != "[1,2,3]" {
			t.Fatalf("array must degrade to _unparsed_data marker, got %v", got)
		}
	})
	t.Run("scalar degrades to marker", func(t *testing.T) {
		got := decodeJSONObject("src", "evt", []byte(`"oops"`))
		if got == nil || got["_unparsed_data"] != `"oops"` {
			t.Fatalf("scalar must degrade to _unparsed_data marker, got %v", got)
		}
	})
	t.Run("long garbage is truncated", func(t *testing.T) {
		got := decodeJSONObject("src", "evt", []byte("["+strings.Repeat("1,", 1000)+"1]"))
		snippet, _ := got["_unparsed_data"].(string)
		if len(snippet) < 512 || !strings.HasSuffix(snippet, "…") {
			t.Fatalf("snippet must be truncated to ~512 chars with an ellipsis, got %d bytes", len(snippet))
		}
	})
}

func TestDecodeEvidence(t *testing.T) {
	if got := decodeEvidence("src", "evt", []byte(`[{"ref":"r"}]`)); len(got) != 1 || got[0].Ref != "r" {
		t.Fatalf("valid evidence must parse, got %v", got)
	}
	if got := decodeEvidence("src", "evt", []byte(`null`)); got != nil {
		t.Fatalf("null evidence must be nil, got %v", got)
	}
	if got := decodeEvidence("src", "evt", []byte(`{"not":"an array"}`)); got != nil {
		t.Fatalf("malformed evidence must drop to nil, got %v", got)
	}
}

// A tool output carrying NUL bytes must not poison the JSONB insert: the
// encode path scrubs \u0000 escapes before the bind, so one binary-laden
// tool.completed event can neither fail the append nor wedge the ingest batch
// (SQLSTATE 22P05 — PostgreSQL cannot represent \u0000 in jsonb).
func TestEncodeObservationEventScrubsNUL(t *testing.T) {
	event := observation.Event{
		Schema:     observation.Schema,
		EventID:    "run/event/000001",
		OccurredAt: time.Date(2026, 11, 10, 7, 11, 51, 0, time.UTC),
		Source:     observation.Source{ID: "kernel-test", Integration: "temporality-agent-kernel"},
		Context:    observation.Context{Project: "p", Run: "r"},
		Type:       "tool.completed",
		Data: map[string]any{
			"tool":   "run_command",
			"output": "git\x00-z listing\x00",
		},
		Evidence: []observation.Evidence{{Ref: "ref\x00"}},
	}
	data, evidence, _, err := encodeObservationEvent(event)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	for name, encoded := range map[string][]byte{"data": data, "evidence": evidence} {
		if strings.Contains(string(encoded), `\u0000`) {
			t.Fatalf("%s: \\u0000 survived the scrub: %s", name, encoded)
		}
		var decoded any
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatalf("%s: scrubbed payload must stay valid JSON: %v\n%s", name, err, encoded)
		}
	}
}
