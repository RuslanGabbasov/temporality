package presentation

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

const compileChartIR = `{"version":"1.0","type":"presentation","title":"Latency","content":[
	{"type":"kpi","items":[{"label":"p95","value":120,"unit":"ms","status":"success"}]}
]}`

const compileDoc = `# Отчёт

Текст до визуализации.

` + "```presentation\n" + compileChartIR + "\n```\n" + `
Текст между визуализациями.

` + "```presentation\n{\"ref\": \"presentation-42\"}\n```\n" + `
Финальный абзац.`

func TestCompileMarkdownRoundTrip(t *testing.T) {
	resolver := func(id string) (json.RawMessage, error) {
		if id != "presentation-42" {
			return nil, fmt.Errorf("not found")
		}
		return json.RawMessage(`{"version":"1.0","type":"presentation","title":"Traffic","content":[{"type":"chart","props":{"chartType":"bar"},"data":{"labels":["a","b"],"values":[1,2]}}]}`), nil
	}
	compiled, images, err := CompileMarkdownCapped([]byte(compileDoc), Policy{}, resolver)
	if err != nil {
		t.Fatalf("compile failed: %v", err)
	}
	if images != 2 {
		t.Fatalf("expected 2 embedded images, got %d", images)
	}
	text := string(compiled)

	// Fences are gone, two data-URI images are in, prose survives in order.
	if strings.Contains(text, "```presentation") {
		t.Fatal("fence survived compilation")
	}
	if strings.Contains(text, "\"ref\"") || strings.Contains(text, "presentation-42") {
		t.Fatal("ref payload leaked into the compiled document")
	}
	if got := strings.Count(text, "data:image/png;base64,"); got != 2 {
		t.Fatalf("expected 2 data URIs, got %d", got)
	}
	for _, needle := range []string{"# Отчёт", "Текст до визуализации.", "Текст между визуализациями.", "Финальный абзац."} {
		if !strings.Contains(text, needle) {
			t.Fatalf("prose lost: %q\n%s", needle, text)
		}
	}
	// Order: the first image carries the fence's document title as alt text.
	first := strings.Index(text, "![Latency](data:image/png;base64,")
	second := strings.Index(text, "![Traffic](data:image/png;base64,")
	if first < 0 || second < 0 || first > second {
		t.Fatalf("images out of order or untitled: first=%d second=%d", first, second)
	}
	// The embedded payload is a real PNG.
	payload := text[first+len("![Latency](data:image/png;base64,"):]
	payload = payload[:strings.Index(payload, ")")]
	if raw, err := base64.StdEncoding.DecodeString(payload); err != nil || !strings.HasPrefix(string(raw[:8]), "\x89PNG") {
		t.Fatalf("embedded image is not a PNG (err=%v)", err)
	}
}

func TestCompileMarkdownNoFencesPassthrough(t *testing.T) {
	source := []byte("# Просто текст\n\nБез визуализаций.")
	compiled, images, err := CompileMarkdownCapped(source, Policy{}, nil)
	if err != nil || images != 0 || string(compiled) != string(source) {
		t.Fatalf("no-fence document must pass through unchanged (images=%d, err=%v)", images, err)
	}
}

func TestCompileMarkdownErrors(t *testing.T) {
	// Unterminated fence.
	_, _, err := CompileMarkdownCapped([]byte("```presentation\nno close"), Policy{}, nil)
	if err == nil || !strings.Contains(err.Error(), "not closed") {
		t.Fatalf("unterminated fence not reported: %v", err)
	}

	// Invalid IR: all problems reported in one pass.
	broken := "# t\n\n```presentation\n{\"version\":\"1.0\",\"type\":\"presentation\",\"content\":[{\"type\":\"text\",\"props\":{\"evil\":1}}]}\n```\n\n```presentation\n{}\n```\n"
	_, _, err = CompileMarkdownCapped([]byte(broken), Policy{}, nil)
	if err == nil || !strings.Contains(err.Error(), "fence #1") || !strings.Contains(err.Error(), "fence #2") {
		t.Fatalf("both fence problems must be reported together: %v", err)
	}

	// Ref without a resolver.
	_, _, err = CompileMarkdownCapped([]byte("```presentation\n{\"ref\":\"x\"}\n```"), Policy{}, nil)
	if err == nil || !strings.Contains(err.Error(), "no resolver") {
		t.Fatalf("ref without resolver not reported: %v", err)
	}

	// Ref resolution failure carries the id.
	_, _, err = CompileMarkdownCapped([]byte("```presentation\n{\"ref\":\"missing-1\"}\n```"), Policy{}, func(id string) (json.RawMessage, error) {
		return nil, fmt.Errorf("not found")
	})
	if err == nil || !strings.Contains(err.Error(), "missing-1") || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("ref failure not detailed: %v", err)
	}
}

func TestValidateMarkdownFences(t *testing.T) {
	// Valid document passes.
	if err := ValidateMarkdownFences([]byte(compileDoc), Policy{}); err != nil {
		t.Fatalf("valid fences rejected: %v", err)
	}
	// Ref fences pass structurally (resolution is compile-time).
	refOnly := "intro\n```presentation\n{\"ref\":\"whatever\"}\n```\n"
	if err := ValidateMarkdownFences([]byte(refOnly), Policy{}); err != nil {
		t.Fatalf("ref fence rejected at validation: %v", err)
	}
	// Broken IR fails with the fence line number.
	broken := "intro\n```presentation\nno json at all\n```\n"
	err := ValidateMarkdownFences([]byte(broken), Policy{})
	if err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("broken fence error lacks the line: %v", err)
	}
}

func TestEscapeMarkdownAlt(t *testing.T) {
	if got := escapeMarkdownAlt("Chart [1] (final)"); got != "Chart 1 final" {
		t.Fatalf("alt escaping wrong: %q", got)
	}
}
