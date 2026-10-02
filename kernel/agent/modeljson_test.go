package agent

import (
	"encoding/json"
	"testing"
)

func TestExtractJSONObject(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string // expected payload, "" means ok must be false
	}{
		{"plain", `{"name": "Deploy"}`, `{"name": "Deploy"}`},
		{"padded", "\n  {\"name\": \"Deploy\"}  \n", `{"name": "Deploy"}`},
		{"fenced", "```json\n{\"name\": \"Deploy\"}\n```", `{"name": "Deploy"}`},
		{"prose around", "Вот черновик:\n{\"name\": \"Deploy\"}\nНадеюсь, подойдёт.", `{"name": "Deploy"}`},
		{"reasoning block", "<think>\nТак, нужен навык деплоя…\n</think>\n\n{\"name\": \"Deploy\"}", `{"name": "Deploy"}`},
		{"two reasoning blocks", "<think>first</think> preamble <think>second</think>{\"name\": \"Deploy\"}", `{"name": "Deploy"}`},
		{"unclosed reasoning with object", `<think>drafting the manifest: {"name": "Deploy"}`, `{"name": "Deploy"}`},
		{"braces inside strings", `{"cmd": "awk '{print $1}'", "quote": "say \"hi\""}`, `{"cmd": "awk '{print $1}'", "quote": "say \"hi\""}`},
		{"braces inside strings in prose", `Result: {"cmd": "awk '{print $1}'", "quote": "say \"hi\""} done`, `{"cmd": "awk '{print $1}'", "quote": "say \"hi\""}`},
		{"skip broken first object", `{broken} {"name": "Deploy"}`, `{"name": "Deploy"}`},
		{"no object", "not json at all", ""},
		{"empty", "", ""},
		{"html garbage", "<html><body>502 Bad Gateway</body></html>", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload, ok := extractJSONObject(tc.content)
			if tc.want == "" {
				if ok {
					t.Fatalf("expected no object, got %q", payload)
				}
				return
			}
			if !ok {
				t.Fatalf("expected object, got none in %q", tc.content)
			}
			var got, want map[string]any
			if err := json.Unmarshal([]byte(payload), &got); err != nil {
				t.Fatalf("extracted payload is not valid JSON: %v (%q)", err, payload)
			}
			if err := json.Unmarshal([]byte(tc.want), &want); err != nil {
				t.Fatal(err)
			}
			gotJSON, _ := json.Marshal(got)
			wantJSON, _ := json.Marshal(want)
			if string(gotJSON) != string(wantJSON) {
				t.Fatalf("payload = %q, want %q", payload, tc.want)
			}
		})
	}
}
