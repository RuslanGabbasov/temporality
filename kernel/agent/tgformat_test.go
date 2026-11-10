package agent

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTelegramHTMLBasics(t *testing.T) {
	cases := []struct{ name, md, want string }{
		{"bold and italic", "**важно** и _акцент_", "<b>важно</b> и <i>акцент</i>"},
		{"heading", "## Итог", "<b>Итог</b>"},
		{"strikethrough", "~убрано~", "<s>убрано</s>"},
		{"inline code", "запусти `go test`", "запусти <code>go test</code>"},
		{"fenced code", "```go\nfmt.Println(1)\n```", "<pre><code class=\"language-go\">fmt.Println(1)\n</code></pre>"},
		{"link", "[инструкции](https://example.com)", "<a href=\"https://example.com\">инструкции</a>"},
		{"blockquote", "> одна строка", "<blockquote>\nодна строка\n\n</blockquote>"},
		{"ul list", "- первый\n- второй", "• первый\n• второй"},
		{"ol list", "1. первый\n2. второй", "1. первый\n2. второй"},
		{"task list", "- [x] сделано\n- [ ] ждёт", "• ☑  сделано\n• ☐  ждёт"},
		{"hr", "above\n\n---\n\nbelow", "above\n\n—\nbelow"},
		{"html escapes", "a < b & c", "a &lt; b &amp; c"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := telegramHTML(tc.md)
			if got != tc.want {
				t.Fatalf("telegramHTML(%q) = %q, want %q", tc.md, got, tc.want)
			}
		})
	}
}

func TestTelegramHTMLTableCollapses(t *testing.T) {
	md := "| a | b |\n|---|---|\n| 1 | 2 |\n| 3 | 4 |"
	got := telegramHTML(md)
	want := "<b>a | b</b>\n1 | 2\n3 | 4"
	if got != want {
		t.Fatalf("table collapse = %q, want %q", got, want)
	}
	// No stray table tags survive.
	for _, tag := range []string{"<table", "<td", "<th", "<tr"} {
		if strings.Contains(got, tag) {
			t.Fatalf("tag %s survived: %q", tag, got)
		}
	}
}

func TestTelegramHTMLScriptEscapes(t *testing.T) {
	got := telegramHTML("<script>alert(1)</script>")
	if strings.Contains(got, "<script>") {
		t.Fatalf("raw html survived: %q", got)
	}
}

func TestTelegramChunksSplitsLongBody(t *testing.T) {
	para := strings.Repeat("слово ", 150) // ~900 chars
	md := para + "\n\n" + para + "\n\n" + para + "\n\n" + para + "\n\n" + para + "\n\n" + para
	chunks := telegramChunks(md)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	for i, chunk := range chunks {
		if utf8.RuneCountInString(telegramHTML(chunk)) > telegramHTMLLimit {
			t.Fatalf("chunk %d over the limit: %d", i, utf8.RuneCountInString(telegramHTML(chunk)))
		}
	}
}

func TestTelegramChunksKeepsFenceWhole(t *testing.T) {
	body := "intro\n\n```go\n" + strings.Repeat("x := 1\n", 10) + "```\n\noutro"
	chunks := telegramChunks(body)
	for i, chunk := range chunks {
		if strings.Count(chunk, "```")%2 != 0 {
			t.Fatalf("chunk %d splits a fenced block: %q", i, chunk)
		}
	}
}

func TestTelegramChunksShortBodyStaysWhole(t *testing.T) {
	if chunks := telegramChunks("привет\n\nмир"); len(chunks) != 1 {
		t.Fatalf("expected one chunk, got %d: %v", len(chunks), chunks)
	}
}
