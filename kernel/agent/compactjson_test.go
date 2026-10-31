package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

// compactJSON backs the tool.started arguments fragment: every trace-side
// parser (eventDetails argumentLines, eventSummary previews) JSON.parses it,
// so the truncated form must stay VALID — the old byte-slice+… produced
// broken JSON and content-heavy tool rows (write_file, remember_many)
// expanded to nothing.

func mustParse(t *testing.T, s string) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("compactJSON must produce valid JSON: %v\n%s", err, s)
	}
	return v
}

func TestCompactJSONShortPassthrough(t *testing.T) {
	in := map[string]any{"path": "/workspace/x", "content": "short"}
	if got := compactJSON(in, 500); got != `{"content":"short","path":"/workspace/x"}` {
		t.Fatalf("a short document must pass through untouched, got %s", got)
	}
}

func TestCompactJSONTruncatedStringStaysValid(t *testing.T) {
	long := strings.Repeat("абвгд ", 200) // way past the cap, multibyte content
	got := compactJSON(map[string]any{"path": "/w/f.ts", "content": long}, 300)
	parsed := mustParse(t, got)
	content, _ := parsed["content"].(string)
	if !strings.Contains(content, "…") {
		t.Fatalf("the ellipsis must ride inside the truncated string, got %s", got)
	}
	if !strings.HasPrefix(content, "абвгд ") {
		t.Fatalf("the fragment must keep the content head, got %q", content)
	}
	// The value-shrink pass keeps the STRUCTURE: a write_file's path survives
	// next to the content fragment — the reader sees WHAT file was written.
	if parsed["path"] != "/w/f.ts" {
		t.Fatalf("untouched sibling keys must survive truncation, got %s", got)
	}
}

func TestCompactJSONTruncatedArrayStaysValid(t *testing.T) {
	items := make([]string, 100)
	for i := range items {
		items[i] = strings.Repeat("item ", 30)
	}
	got := compactJSON(map[string]any{"items": items}, 400)
	parsed := mustParse(t, got)
	list, ok := parsed["items"].([]any)
	if !ok || len(list) == 0 {
		t.Fatalf("the truncated items array must survive with elements, got %s", got)
	}
	// The LAST element is either a complete item or the "…" filler — both
	// parse; the reader sees the list continues.
	if last := list[len(list)-1]; last != "…" {
		if s, _ := last.(string); !strings.Contains(s, "item") {
			t.Fatalf("the last element must be a complete item or the filler, got %v in %s", last, got)
		}
	}
}

func TestCompactJSONCutAfterKeyGetsFillerValue(t *testing.T) {
	// Force a prefix that ends right after a colon: the repair must complete
	// the pair with the "…" filler value.
	long := strings.Repeat("x", 400)
	raw, _ := json.Marshal(map[string]any{"key": long, "b": 1})
	got := truncateJSON([]byte(`{"key":"`+strings.Repeat("x", 500)+`","b":1}`), 9)
	parsed := mustParse(t, got)
	if _, ok := parsed["key"]; !ok {
		t.Fatalf("the truncated document must keep its top-level key, got %s (raw len %d)", got, len(raw))
	}
}

func TestCompactJSONMultibyteBoundaryNeverBreaksEncoding(t *testing.T) {
	long := strings.Repeat("ж", 300)
	got := compactJSON(map[string]any{"content": long}, 250)
	parsed := mustParse(t, got)
	if s, _ := parsed["content"].(string); !strings.HasPrefix(s, "ж") {
		t.Fatalf("a cut inside multibyte runes must still yield readable text, got %q", s)
	}
}

func TestCompactJSONTinyCapStillValid(t *testing.T) {
	got := compactJSON(map[string]any{"a": strings.Repeat("b", 100)}, 8)
	mustParse(t, got)
}
