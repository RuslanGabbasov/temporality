package observation

import (
	"strings"
	"testing"
)

func TestRedactStringRewritesKnownTokens(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"github token", "token=ghp_16C7e42F292c6912E7710c838347Ae178B4a", "token=<redacted:github_token>"},
		{"aws key", "AKIAIOSFODNN7EXAMPLE", "<redacted:aws_access_key>"},
		{"slack", "xoxb-123456789012-abcdef", "<redacted:slack_token>"},
		{"openai", "sk-proj-abcdefghijklmnopqrstuvwx", "<redacted:openai_key>"},
		{"bearer", "Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9", "Authorization: <redacted:bearer_header>"},
		{"private key", "-----BEGIN RSA PRIVATE KEY-----", "<redacted:private_key_block>"},
		{"benign", "the quick brown fox", "the quick brown fox"},
	}
	for _, tc := range cases {
		if got := RedactString(tc.input); got != tc.want {
			t.Errorf("%s: RedactString(%q) = %q, want %q", tc.name, tc.input, got, tc.want)
		}
	}
}

func TestRedactStringIsDeterministic(t *testing.T) {
	input := "key=ghp_abcdefghijklmnopqrst and AKIA1234567890ABCDEF"
	first := RedactString(input)
	second := RedactString(input)
	if first != second {
		t.Fatal("redaction must be deterministic for idempotent ingest")
	}
	if strings.Contains(first, "ghp_") || strings.Contains(first, "AKIA") {
		t.Fatalf("secrets survived redaction: %q", first)
	}
}

func TestRedactEventWalksNestedData(t *testing.T) {
	event := &Event{Data: map[string]any{
		"output": "export TOKEN=ghp_abcdefghijklmnopqrstuvwx",
		"nested": map[string]any{
			"list": []any{"AKIA1234567890ABCDEF", "clean"},
		},
		"number": 42,
	}}
	RedactEvent(event)
	if got := event.Data["output"].(string); got != "export TOKEN=<redacted:github_token>" {
		t.Errorf("output not redacted: %q", got)
	}
	nested := event.Data["nested"].(map[string]any)
	list := nested["list"].([]any)
	if list[0].(string) != "<redacted:aws_access_key>" || list[1].(string) != "clean" {
		t.Errorf("nested list not redacted correctly: %v", list)
	}
	if event.Data["number"].(int) != 42 {
		t.Error("non-string values must pass through untouched")
	}
}
