package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/temporality-project/temporality/kernel/llm"
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

func TestCompleteParsedRetriesOnProse(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		if requests == 1 {
			// The flaky small-model failure mode: conversational prose.
			fmt.Fprintf(w, `{"choices":[{"message":{"content":"Waiting for the human answer was the whole point. Done."},"finish_reason":"stop"}]}`)
			return
		}
		fmt.Fprintf(w, `{"choices":[{"message":{"content":"{\"name\": \"Deploy\"}"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	model := llm.New(llm.Config{BaseURL: server.URL, Model: "m", Timeout: 10 * time.Second})
	type payload struct {
		Name string `json:"name"`
	}
	parsed, err := completeParsed(context.Background(), model, []llm.Message{{Role: "user", Content: "build"}}, func(content string) (payload, error) {
		var p payload
		body, ok := extractJSONObject(content)
		if !ok {
			return payload{}, fmt.Errorf("model returned no JSON object")
		}
		if err := json.Unmarshal([]byte(body), &p); err != nil {
			return payload{}, fmt.Errorf("model returned invalid JSON: %w", err)
		}
		return p, nil
	})
	if err != nil {
		t.Fatalf("completeParsed: %v", err)
	}
	if parsed.Name != "Deploy" {
		t.Fatalf("name = %q, want Deploy", parsed.Name)
	}
	if requests != 2 {
		t.Errorf("model calls = %d, want 2 (prose first, JSON on retry)", requests)
	}
}

func TestCompleteParsedFailsAfterRetry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"choices":[{"message":{"content":"still not json"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	model := llm.New(llm.Config{BaseURL: server.URL, Model: "m", Timeout: 10 * time.Second})
	_, err := completeParsed(context.Background(), model, []llm.Message{{Role: "user", Content: "build"}}, func(content string) (struct{}, error) {
		return struct{}{}, fmt.Errorf("model returned invalid JSON")
	})
	if err == nil {
		t.Fatal("expected error when both attempts return prose")
	}
}
