package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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
		{"fence inside string value", "{\"id\": \"x\", \"markdown\": \"step 1:\n```bash\nmake test\n```\nstep 2\"}", "{\"id\": \"x\", \"markdown\": \"step 1:\\n```bash\\nmake test\\n```\\nstep 2\"}"},
		{"raw newlines in string", "{\"id\": \"x\", \"markdown\": \"## Purpose\nDeploy the service.\n\"}", "{\"id\": \"x\", \"markdown\": \"## Purpose\\nDeploy the service.\\n\"}"},
		{"trailing comma", "{\"id\": \"x\", \"capabilities\": [\"deploy\",],}", "{\"id\": \"x\", \"capabilities\": [\"deploy\"]}"},
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

// A gateway that accepts the streaming request and then goes silent must die
// on the stream watchdog with a NAMED error — and must NOT fall back to the
// blocking transport, which would hang on the very same parked connection
// until the activity's StartToClose masks the cause (the production incident:
// knowledge.extraction and skill_propose failing with bare StartToClose).
func TestStreamOrCompleteWatchdogSkipsBlockingFallback(t *testing.T) {
	var streamReqs, blockReqs int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, 4096)
		n, _ := r.Body.Read(body)
		if strings.Contains(string(body[:n]), `"stream":true`) {
			streamReqs++
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			// Park: no bytes ever — the first-token watchdog must cut it.
			<-r.Context().Done()
			return
		}
		blockReqs++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"{}"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	model := llm.New(llm.Config{
		BaseURL: server.URL, Model: "m", Timeout: 30 * time.Second,
		StreamFirstTokenTimeout: time.Second, StreamIdleTimeout: 500 * time.Millisecond,
		MaxOutputTokens: 1024,
	})
	_, err := streamOrComplete(context.Background(), model, []llm.Message{{Role: "user", Content: "build a skill"}})
	if err == nil {
		t.Fatal("expected a watchdog error from the parked stream")
	}
	if !llm.StreamTimedOut(err) {
		t.Fatalf("error must be the stream watchdog, got: %v", err)
	}
	if streamReqs < 1 {
		t.Fatalf("stream request never arrived: %d", streamReqs)
	}
	if blockReqs != 0 {
		t.Fatalf("a parked stream must not fall back to blocking (blocked calls: %d)", blockReqs)
	}
}

// A quota-exhausted provider (429 «Weekly/Monthly Limit Exhausted») must fail
// fast with the provider's own message: the blocking fallback cannot strip a
// quota and would only hammer the gateway for its whole 90s budget while the
// caller waits for a nameless timeout.
func TestStreamOrCompleteRateLimitFailsFast(t *testing.T) {
	var streamReqs, blockReqs int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, 4096)
		n, _ := r.Body.Read(body)
		if strings.Contains(string(body[:n]), `"stream":true`) {
			streamReqs++
		} else {
			blockReqs++
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"code":"1310","message":"Weekly/Monthly Limit Exhausted. Your limit will reset at 2026-11-16 21:04:32"}}`)
	}))
	defer server.Close()

	model := llm.New(llm.Config{BaseURL: server.URL, Model: "m", Timeout: 30 * time.Second, MaxOutputTokens: 1024})
	started := time.Now()
	_, err := streamOrComplete(context.Background(), model, []llm.Message{{Role: "user", Content: "build a skill"}})
	if err == nil {
		t.Fatal("expected the provider's 429 error")
	}
	if code, ok := llm.StatusOf(err); !ok || code != http.StatusTooManyRequests {
		t.Fatalf("error must carry the 429 status, got: %v", err)
	}
	if !strings.Contains(err.Error(), "Limit Exhausted") {
		t.Fatalf("error must keep the provider's message, got: %v", err)
	}
	if blockReqs != 0 {
		t.Fatalf("a 429 must not fall back to blocking (blocked calls: %d)", blockReqs)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("429 must fail fast, took %s", elapsed)
	}
	if streamReqs != 1 {
		t.Fatalf("stream requests = %d, want 1", streamReqs)
	}
}
