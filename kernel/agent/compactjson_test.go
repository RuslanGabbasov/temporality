package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
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

// A tool's STRING answer is content, not structure: re-marshaling it wrapped
// the JSON answers of saga_*/MCP tools into quoted, escaped noise
// ("{\"saga_id\":…}") in every trace row.

func TestCompactJSONStringContentNotQuoted(t *testing.T) {
	got := compactJSON(`{"saga_id":"saga-x","status":"registered"}`, 1000)
	if got != `{"saga_id":"saga-x","status":"registered"}` {
		t.Fatalf("string content must ride through unquoted and unescaped, got %s", got)
	}
}

func TestCompactJSONStringEnvelopeUnwraps(t *testing.T) {
	// Some MCP servers answer with a JSON string wrapping the real payload.
	got := compactJSON(`"{\"connected\":true,\"mapId\":\"2\"}"`, 1000)
	if got != `{"connected":true,"mapId":"2"}` {
		t.Fatalf("a JSON-string envelope must unwrap to its payload, got %s", got)
	}
}

func TestCompactJSONStringLongJSONStaysParseable(t *testing.T) {
	doc := `{"content":"` + strings.Repeat("абвгд ", 200) + `","path":"/w/f.ts"}`
	got := compactJSON(doc, 300)
	if strings.HasPrefix(got, `"`) {
		t.Fatalf("the content must not be re-quoted, got %s", got)
	}
	parsed := mustParse(t, got)
	if parsed["path"] != "/w/f.ts" {
		t.Fatalf("untouched sibling keys must survive truncation, got %s", got)
	}
	content, _ := parsed["content"].(string)
	if !strings.Contains(content, "…") {
		t.Fatalf("the ellipsis must ride inside the truncated string, got %s", got)
	}
}

func TestCompactJSONStringPlainTextCutsOnRuneBoundary(t *testing.T) {
	long := strings.Repeat("щ", 100) // multibyte, not JSON
	got := compactJSON(long, 50)
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("the cut must end with a visible ellipsis, got %q", got)
	}
	if !strings.HasPrefix(got, strings.Repeat("щ", 10)) {
		t.Fatalf("the fragment must keep the content head, got %q", got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("the cut must not break multibyte runes, got %q", got)
	}
}

// rememberManyArgsPreview backs the remember_many tool row: the trace must
// let the reader see WHAT the agent decided to keep — the generic 500-char
// compact turned a big batch into two shrunken items and an ellipsis.

func TestRememberManyArgsPreviewKeepsEveryProposition(t *testing.T) {
	items := make([]any, 0, 30)
	for i := 0; i < 30; i++ {
		items = append(items, map[string]any{
			"proposition": fmt.Sprintf("Fact %02d: %s", i, strings.Repeat("абвгд ", 40)), // ~240 bytes of multibyte text each
			"evidence":    []any{"artifact://e1"},
		})
	}
	got := rememberManyArgsPreview(map[string]any{"items": items})
	if len(got) > 12000 {
		t.Fatalf("the preview must fit its budget, got %d chars", len(got))
	}
	parsed := mustParse(t, got)
	list, ok := parsed["items"].([]any)
	if !ok || len(list) != 30 {
		t.Fatalf("every proposition must survive the preview, got %d items in %d chars", len(list), len(got))
	}
	for i, raw := range list {
		item, _ := raw.(map[string]any)
		proposition, _ := item["proposition"].(string)
		if !strings.HasPrefix(proposition, fmt.Sprintf("Fact %02d:", i)) {
			t.Fatalf("item %d lost its proposition head: %q", i, proposition)
		}
	}
	// Evidence degrades out of the preview: the full refs ride each item's
	// own knowledge.proposed event.
	if strings.Contains(got, "evidence") {
		t.Fatalf("evidence refs must not bloat the preview, got %s", got[:200])
	}
}

func TestRememberManyArgsPreviewDropsEmptyAndClipsLong(t *testing.T) {
	got := rememberManyArgsPreview(map[string]any{"items": []any{
		map[string]any{"proposition": strings.Repeat("длинный факт ", 100)},
		map[string]any{"proposition": "   "},
		"not-an-object",
	}})
	parsed := mustParse(t, got)
	list := parsed["items"].([]any)
	if len(list) != 1 {
		t.Fatalf("empty propositions and non-objects must be dropped, got %v", parsed)
	}
	first := list[0].(map[string]any)["proposition"].(string)
	if len(first) > 201 || !strings.HasSuffix(first, "…") {
		t.Fatalf("a long proposition must clip to one line, got %d bytes: %q", len(first), first)
	}
}

func TestRememberManyArgsPreviewEmptyBatch(t *testing.T) {
	got := rememberManyArgsPreview(map[string]any{"items": []any{}})
	parsed := mustParse(t, got)
	if list := parsed["items"].([]any); len(list) != 0 {
		t.Fatalf("an empty batch must preview as an empty list, got %s", got)
	}
}
