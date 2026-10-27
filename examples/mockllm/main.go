// mockllm — a deterministic OpenAI-compatible provider for load testing
// (docs/resilience-and-scale.md §Нагрузочное тестирование). It answers
// /v1/chat/completions exactly like a real provider — streaming or blocking —
// with a scripted coding-loop: two tool-call turns, then a final answer.
// First-token delay, token count, failure rate and payload size are flags,
// so a load profile can exercise the watchdogs, the retry ladder and the
// token-stream bus without touching a real provider (no rate limits, no
// cost, fully reproducible).
//
//	agent-kernel env: TEMPORALITY_MODEL_BASE_URL=http://mockllm:8081/v1
//	                  TEMPORALITY_MODEL_ID=loadgen-model
//
// Metrics (GET /metrics, JSON): request counters, first-token latency
// histogram, active-request gauge — the load report quotes them.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

var (
	addr       = flag.String("addr", ":8081", "listen address")
	firstToken = flag.Duration("first-token", 200*time.Millisecond, "delay before the first streamed token (env MOCKLLM_FIRST_TOKEN)")
	tokenGap   = flag.Duration("token-gap", 15*time.Millisecond, "delay between streamed tokens (env MOCKLLM_TOKEN_GAP)")
	tokenCount = flag.Int("tokens", 120, "streamed content tokens per answer (env MOCKLLM_TOKENS)")
	charsToken = flag.Int("chars-per-token", 4, "characters per token delta")
	failEvery  = flag.Int("fail-every", 0, "fail every Nth request with 429 (0 = never)")
	// HITL profile (docs/resilience-and-scale.md §НТ): after N tool results
	// the mock answers ask_human instead of a tool call — the run parks on
	// the approval signal until the driver answers it.
	askAfterTools = flag.Int("ask-human-after-tools", 0, "answer ask_human once the request carries this many tool results (env MOCKLLM_ASK_AFTER_TOOLS, 0 = off)")
	toolCommand   = flag.String("tool-command", "echo loadgen step", "command the scripted tool calls execute (env MOCKLLM_TOOL_COMMAND; heavier profiles: 'sleep 10 && echo heavy')")
	histBucket    = flag.Duration("hist-bucket", 100*time.Millisecond, "first-token histogram bucket width")
)

// Counters exposed at /metrics.
var (
	totalRequests  atomic.Int64
	streamRequests atomic.Int64
	fail429        atomic.Int64
	toolCalls      atomic.Int64
	finalAnswers   atomic.Int64
	active         atomic.Int64
)

// firstTokenHist: fixed-size histogram of measured first-token latency,
// bucketed by -hist-bucket; the last bucket is overflow.
var firstTokenHist [32]atomic.Int64

func main() {
	flag.Parse()
	// Env overrides win over flags: the compose loadtest profile passes the
	// heavy-profile knobs this way (MOCKLLM_* in docker-compose.dev.yml).
	if v := strings.TrimSpace(os.Getenv("MOCKLLM_FIRST_TOKEN")); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			*firstToken = d
		}
	}
	if v := strings.TrimSpace(os.Getenv("MOCKLLM_TOKEN_GAP")); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			*tokenGap = d
		}
	}
	if v := strings.TrimSpace(os.Getenv("MOCKLLM_TOKENS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			*tokenCount = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("MOCKLLM_TOOL_COMMAND")); v != "" {
		*toolCommand = v
	}
	if v := strings.TrimSpace(os.Getenv("MOCKLLM_ASK_AFTER_TOOLS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			*askAfterTools = n
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", handleChat)
	mux.HandleFunc("GET /metrics", handleMetrics)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true})
	})
	log.Printf("mockllm on %s: first-token=%s token-gap=%s tokens=%d fail-every=%d",
		*addr, *firstToken, *tokenGap, *tokenCount, *failEvery)
	log.Fatal(http.ListenAndServe(*addr, mux))
}

type chatRequest struct {
	Stream bool `json:"stream"`
	// Messages are parsed only for the tool-role count that drives the
	// scripted answer; the content itself is irrelevant to the mock.
	Messages []struct {
		Role string `json:"role"`
	} `json:"messages"`
}

func handleChat(w http.ResponseWriter, r *http.Request) {
	active.Add(1)
	defer active.Add(-1)
	totalRequests.Add(1)

	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]any{"error": map[string]any{"message": "bad json"}})
		return
	}

	// Deterministic failure injection: every Nth request answers 429, so a
	// load profile exercises the retry ladder and the supervisor's friction
	// view reproducibly instead of with random flake.
	if *failEvery > 0 && totalRequests.Load()%int64(*failEvery) == 0 {
		fail429.Add(1)
		w.Header().Set("Retry-After", "1")
		writeJSON(w, 429, map[string]any{"error": map[string]any{"message": "mock rate limit", "code": 429}})
		return
	}

	tools := 0
	for _, m := range req.Messages {
		if m.Role == "tool" {
			tools++
		}
	}

	start := time.Now()
	if req.Stream {
		streamRequests.Add(1)
		streamResponse(w, tools, start)
		return
	}
	blockResponse(w, tools, start)
}

// scripted: two tool-call turns (the loadgen scenario "3 model calls,
// 2 tools"), then the final answer. With the HITL profile on, the first
// turn after the tool results becomes an ask_human call — the run parks on
// the approval signal until the driver answers.
func scripted(tools, askAfter int) (toolName, args, content string) {
	if askAfter > 0 && tools == askAfter {
		toolCalls.Add(1)
		return "ask_human", fmt.Sprintf(`{"question":"Load-test decision %d: approve the next step?","options":["yes","no"]}`, tools), ""
	}
	switch {
	case tools == 0:
		toolCalls.Add(1)
		return "run_command", fmt.Sprintf(`{"command":["sh","-c","%s 1"]}`, *toolCommand), ""
	case tools == 1:
		toolCalls.Add(1)
		return "run_command", fmt.Sprintf(`{"command":["sh","-c","%s 2"]}`, *toolCommand), ""
	default:
		finalAnswers.Add(1)
		return "", "", "Load run complete: the scripted coding loop finished successfully."
	}
}

func streamResponse(w http.ResponseWriter, tools int, start time.Time) {
	name, args, content := scripted(tools, *askAfterTools)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, 500, map[string]any{"error": "no flusher"})
		return
	}

	send := func(payload map[string]any) {
		data, _ := json.Marshal(payload)
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}
	id := fmt.Sprintf("chatcmpl-mock-%d", time.Now().UnixNano())
	base := map[string]any{
		"id": id, "object": "chat.completion.chunk",
		"created": time.Now().Unix(), "model": "loadgen-model",
	}

	// The first-token delay is the scripted provider "thinking" time; the
	// histogram records the measured value, not the configured one.
	time.Sleep(*firstToken)
	bucketFirstToken(time.Since(start))

	choice := func(delta map[string]any, finish string) map[string]any {
		payload := map[string]any{}
		for k, v := range base {
			payload[k] = v
		}
		payload["choices"] = []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}
		return payload
	}

	if name != "" {
		// Tool-call turn: role chunk, one full-args tool delta, done + usage.
		send(choice(map[string]any{"role": "assistant"}, ""))
		send(choice(map[string]any{"tool_calls": []any{map[string]any{
			"index": 0, "id": fmt.Sprintf("call-%d", time.Now().UnixNano()),
			"type": "function", "function": map[string]any{"name": name, "arguments": args},
		}}}, "tool_calls"))
		sendUsage(send, choice)
		return
	}

	// Content turn: role chunk, N token deltas, finish.
	send(choice(map[string]any{"role": "assistant"}, ""))
	tok := strings.Repeat("load ", *tokenCount)
	size := *charsToken
	for i := 0; i < len(tok); i += size {
		end := i + size
		if end > len(tok) {
			end = len(tok)
		}
		send(choice(map[string]any{"content": tok[i:end]}, ""))
		time.Sleep(*tokenGap)
	}
	send(choice(map[string]any{}, "stop"))
	sendUsage(send, choice)
	_ = content
}

func blockResponse(w http.ResponseWriter, tools int, start time.Time) {
	name, args, content := scripted(tools, *askAfterTools)
	time.Sleep(*firstToken)
	bucketFirstToken(time.Since(start))
	msg := map[string]any{"role": "assistant"}
	finish := "stop"
	if name != "" {
		msg["tool_calls"] = []any{map[string]any{
			"id": fmt.Sprintf("call-%d", time.Now().UnixNano()), "type": "function",
			"function": map[string]any{"name": name, "arguments": args},
		}}
		finish = "tool_calls"
	} else {
		msg["content"] = content
	}
	completion := 0
	if name == "" {
		completion = *tokenCount
	}
	writeJSON(w, 200, map[string]any{
		"id": fmt.Sprintf("chatcmpl-mock-%d", time.Now().UnixNano()), "object": "chat.completion",
		"created": time.Now().Unix(), "model": "loadgen-model",
		"choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": finish}},
		"usage":   map[string]any{"prompt_tokens": 500, "completion_tokens": completion, "total_tokens": 500 + completion},
	})
}

func sendUsage(send func(map[string]any), choice func(map[string]any, string) map[string]any) {
	payload := choice(map[string]any{}, "stop")
	payload["usage"] = map[string]any{"prompt_tokens": 500, "completion_tokens": *tokenCount, "total_tokens": 500 + *tokenCount}
	send(payload)
}

func bucketFirstToken(d time.Duration) {
	idx := int(d / *histBucket)
	if idx >= len(firstTokenHist) {
		idx = len(firstTokenHist) - 1
	}
	firstTokenHist[idx].Add(1)
}

func handleMetrics(w http.ResponseWriter, _ *http.Request) {
	hist := make([]int64, 0, len(firstTokenHist))
	for i := range firstTokenHist {
		hist = append(hist, firstTokenHist[i].Load())
	}
	writeJSON(w, 200, map[string]any{
		"total_requests":                totalRequests.Load(),
		"stream_requests":               streamRequests.Load(),
		"failures_429":                  fail429.Load(),
		"tool_calls_issued":             toolCalls.Load(),
		"final_answers":                 finalAnswers.Load(),
		"active_requests":               active.Load(),
		"first_token_ms_hist_bucket_ms": histBucket.Milliseconds(),
		"first_token_ms_hist":           hist,
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
