// Anthropic Messages wire (docs/subscription-providers-claude-codex.md §3.1):
// providers with auth=anthropic_oauth hold a Claude subscription OAuth token
// (claude setup-token, sk-ant-oat01-…). Those tokens are issued for the
// native /v1/messages surface — Claude Code itself never speaks the OpenAI
// compatibility layer — so the client translates its chat-completions request
// model onto the Messages wire instead of betting on the compat layer's
// undocumented OAuth behavior. Everything upstream of the payload (retry
// ladder, limiter, stream watchdogs, status-error diagnostics) stays shared
// with the OpenAI-compatible path; only the encode/decode swaps.
package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

const (
	// anthropicVersionHeader is the required Messages-API version header.
	anthropicVersionHeader = "2023-06-01"
	// anthropicOAuthBetaValue opts the subscription OAuth token into the
	// inference surface (the beta Claude Code itself rides).
	anthropicOAuthBetaValue = "oauth-2025-04-20"
	// defaultMessagesMaxTokens backstops the required max_tokens when neither
	// the config nor an option set one — the kernel default (§ConfigFromEnv).
	defaultMessagesMaxTokens = 16384
)

// messagesWire reports that this client talks the native Anthropic Messages
// API. The auth kind defines the wire: an anthropic_oauth provider carries a
// subscription token that must not be spent on the OpenAI-compat layer.
func (c *Client) messagesWire() bool { return c.cfg.AnthropicOAuth }

// completionsURL resolves the POST target for the configured wire. The
// provider base URL keeps the same shape as for chat-completions providers —
// it includes the /v1 segment ("https://api.anthropic.com/v1") — so the
// suffix follows it. A bare Anthropic host without /v1 is forgiven: that
// spelling is what every Anthropic doc example shows.
func (c *Client) completionsURL() string {
	base := strings.TrimRight(strings.TrimSpace(c.cfg.BaseURL), "/")
	if c.messagesWire() {
		if strings.HasSuffix(base, "/v1") {
			return base + "/messages"
		}
		if anthropicCompatHost(c.providerHost) {
			return base + "/v1/messages"
		}
		return base + "/messages"
	}
	return base + "/chat/completions"
}

// applyRequestHeaders writes the auth and protocol headers for the configured
// wire. The chat path stays byte-identical to the historical behavior
// (Content-Type, optional Bearer); the Messages wire adds the required
// anthropic-version and — for subscription tokens — the OAuth beta.
func (c *Client) applyRequestHeaders(request *http.Request, streaming bool) {
	request.Header.Set("Content-Type", "application/json")
	if streaming {
		request.Header.Set("Accept", "text/event-stream")
	}
	if c.cfg.APIKey != "" {
		request.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}
	if c.messagesWire() {
		request.Header.Set("Anthropic-Version", anthropicVersionHeader)
		if c.cfg.AnthropicOAuth {
			request.Header.Set("Anthropic-Beta", anthropicOAuthBetaValue)
		}
	}
}

// maskedRequestHeaders is the diagnostic view of what went out — credentials
// masked, everything else verbatim (the CallReport contract).
func (c *Client) maskedRequestHeaders() map[string]string {
	headers := map[string]string{
		"Content-Type":  "application/json",
		"Authorization": "Bearer " + maskKey(c.cfg.APIKey),
	}
	if c.messagesWire() {
		headers["Anthropic-Version"] = anthropicVersionHeader
		if c.cfg.AnthropicOAuth {
			headers["Anthropic-Beta"] = anthropicOAuthBetaValue
		}
	}
	return headers
}

// statusErrorFrom folds a rejected response into the diagnosable status error
// shared by both wires and both transports (blocking + streaming).
func (c *Client) statusErrorFrom(request *http.Request, response *http.Response, body, payload []byte) *statusError {
	return &statusError{
		code:       response.StatusCode,
		body:       truncate(string(body), 400),
		retryAfter: parseRetryAfterHeader(response.Header.Get("Retry-After"), time.Now()),
		url:        request.URL.String(),
		reqHeaders: c.maskedRequestHeaders(),
		reqBody:    truncate(string(payload), 2000),
		respHeaders: map[string]string{
			"Content-Type":     response.Header.Get("Content-Type"),
			"Www-Authenticate": response.Header.Get("Www-Authenticate"),
		},
	}
}

// encodePayload serializes the chat request model onto the configured wire:
// chat-completions JSON as-is, or the Messages translation for anthropic_oauth
// providers. The retry ladder in Complete re-runs it after every degradation.
func (c *Client) encodePayload(req *chatRequest) ([]byte, error) {
	if c.messagesWire() {
		return json.Marshal(messagesRequestFromChat(c.cfg, req))
	}
	return json.Marshal(req)
}

// --- wire types ---------------------------------------------------------

// messagesRequest is the Messages-API envelope. Only the fields the harness
// actually uses are modeled; optional surface (metadata, cache_control,
// thinking) stays off — an omitted field cannot 400.
type messagesRequest struct {
	Model       string            `json:"model"`
	MaxTokens   int               `json:"max_tokens"`
	Temperature float64           `json:"temperature,omitempty"`
	System      string            `json:"system,omitempty"`
	Messages    []messagesMessage `json:"messages"`
	Tools       []messagesTool    `json:"tools,omitempty"`
	// ToolChoice maps the harness's parallel-tool toggle onto the Messages
	// knob: {"type":"auto","disable_parallel_tool_use":true} replicates what
	// parallel_tool_calls=false does on the OpenAI wire.
	ToolChoice map[string]any `json:"tool_choice,omitempty"`
	Stream     bool           `json:"stream,omitempty"`
}

// messagesMessage is one turn. Content is either a plain string (rare — only
// when the whole turn is one text block) or []messagesBlock; the translation
// always emits blocks so tool results and images have a uniform home.
type messagesMessage struct {
	Role    string `json:"role"` // "user" | "assistant"
	Content any    `json:"content"`
}

type messagesBlock struct {
	Type string `json:"type"` // text | image | tool_use | tool_result
	Text string `json:"text,omitempty"`
	// tool_use
	ID    string         `json:"id,omitempty"`
	Name  string         `json:"name,omitempty"`
	Input map[string]any `json:"input,omitempty"`
	// tool_result
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   []messagesBlock `json:"content,omitempty"`
	// image
	Source *messagesImageSource `json:"source,omitempty"`
}

type messagesImageSource struct {
	Type      string `json:"type"` // base64
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type messagesTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

// messagesResponse is the blocking Messages reply: an ordered content-block
// array plus a stop reason and token accounting.
type messagesResponse struct {
	Content []struct {
		Type     string         `json:"type"` // text | thinking | redacted_thinking | tool_use
		Text     string         `json:"text"`
		Thinking string         `json:"thinking"`
		ID       string         `json:"id"`
		Name     string         `json:"name"`
		Input    map[string]any `json:"input"`
	} `json:"content"`
	StopReason string         `json:"stop_reason"`
	Usage      *messagesUsage `json:"usage"`
	Error      *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// messagesUsage is the Messages token accounting. Cache reads ride
// cache_read_input_tokens — the same number the OpenAI-style gateways expose
// as prompt_tokens_details.cached_tokens.
type messagesUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

// messagesEvent is one SSE frame of a streamed Messages reply. One loose
// struct covers every event type; the unused fields stay zero per event.
type messagesEvent struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message *struct {
		Usage *messagesUsage `json:"usage"`
	} `json:"message"`
	ContentBlock *struct {
		Type string `json:"type"`
		Text string `json:"text"`
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"content_block"`
	Delta *struct {
		Type        string `json:"type"` // text_delta | thinking_delta | input_json_delta
		Text        string `json:"text"`
		Thinking    string `json:"thinking"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Usage *messagesUsage `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// --- translation --------------------------------------------------------

// messagesRequestFromChat translates the chat-completions request model onto
// the Messages wire: leading system messages hoist into the top-level system
// string, tool replies become tool_result blocks in a following user turn
// (merged, because the Messages API wants tool results at the head of a user
// message), assistant tool calls become tool_use blocks, and the parallel-
// tool toggle maps onto tool_choice.disable_parallel_tool_use. Deliberately
// dropped: reasoning presets (extended thinking carries a block-preservation
// protocol of its own) and response_format (no Messages surface — the model
// still answers free-form JSON when asked).
func messagesRequestFromChat(cfg Config, req *chatRequest) *messagesRequest {
	out := &messagesRequest{
		Model:       req.Model,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		Stream:      req.Stream,
	}
	if out.MaxTokens <= 0 {
		out.MaxTokens = cfg.MaxOutputTokens
	}
	if out.MaxTokens <= 0 {
		out.MaxTokens = defaultMessagesMaxTokens
	}

	var systemParts []string
	rest := req.Messages
	for i, m := range req.Messages {
		if m.Role != "system" {
			rest = req.Messages[i:]
			break
		}
		systemParts = append(systemParts, m.Content)
		rest = nil
	}
	out.System = strings.Join(systemParts, "\n\n")

	appendTurn := func(role string, blocks []messagesBlock) {
		if n := len(out.Messages); n > 0 && out.Messages[n-1].Role == role {
			existing, _ := out.Messages[n-1].Content.([]messagesBlock)
			out.Messages[n-1].Content = append(existing, blocks...)
			return
		}
		out.Messages = append(out.Messages, messagesMessage{Role: role, Content: blocks})
	}
	for _, m := range rest {
		switch m.Role {
		case "tool":
			// One tool reply = one tool_result block. Results ride at the head
			// of a user turn; consecutive replies merge into that turn.
			result := messagesBlock{Type: "tool_result", ToolUseID: m.ToolCallID}
			if m.Content != "" {
				result.Content = append(result.Content, messagesBlock{Type: "text", Text: m.Content})
			}
			for _, image := range m.Images {
				result.Content = append(result.Content, imageBlock(image))
			}
			appendTurn("user", []messagesBlock{result})
		case "assistant":
			var blocks []messagesBlock
			if m.Content != "" {
				blocks = append(blocks, messagesBlock{Type: "text", Text: m.Content})
			}
			for _, call := range m.ToolCalls {
				blocks = append(blocks, messagesBlock{Type: "tool_use", ID: call.ID, Name: call.Function.Name, Input: toolUseInput(call.Function.Arguments)})
			}
			if len(blocks) == 0 {
				blocks = append(blocks, messagesBlock{Type: "text", Text: m.Content})
			}
			appendTurn("assistant", blocks)
		default: // user (normalizeSystemMessages already re-rolled late systems)
			var blocks []messagesBlock
			if m.Content != "" {
				blocks = append(blocks, messagesBlock{Type: "text", Text: m.Content})
			}
			for _, image := range m.Images {
				blocks = append(blocks, imageBlock(image))
			}
			if len(blocks) == 0 {
				blocks = append(blocks, messagesBlock{Type: "text", Text: m.Content})
			}
			appendTurn("user", blocks)
		}
	}

	if len(req.Tools) > 0 {
		for _, tool := range req.Tools {
			out.Tools = append(out.Tools, messagesTool{
				Name:        tool.Function.Name,
				Description: tool.Function.Description,
				InputSchema: tool.Function.Parameters,
			})
		}
		choice := map[string]any{"type": "auto"}
		if req.ParallelToolCalls != nil && !*req.ParallelToolCalls {
			choice["disable_parallel_tool_use"] = true
		}
		out.ToolChoice = choice
	}
	return out
}

// imageBlock renders one vision payload as a Messages image block (the
// OpenAI image_url data-URI equivalent).
func imageBlock(image ImageBlock) messagesBlock {
	return messagesBlock{
		Type: "image",
		Source: &messagesImageSource{
			Type:      "base64",
			MediaType: image.MIMEType,
			Data:      base64.StdEncoding.EncodeToString(image.Data),
		},
	}
}

// toolUseInput rebuilds a valid input object for a replayed assistant tool
// call: the parsed args when they decode, empty object otherwise — the
// Messages API rejects a missing input on tool_use blocks. ArgsError cases
// (unparsable raw JSON) degrade to an empty input instead of failing the
// whole history replay.
func toolUseInput(argsRaw string) map[string]any {
	if raw := strings.TrimSpace(argsRaw); raw != "" {
		var parsed map[string]any
		if err := json.Unmarshal([]byte(raw), &parsed); err == nil && len(parsed) > 0 {
			return parsed
		}
	}
	return map[string]any{}
}

// messagesFinish maps the Messages stop reason onto the OpenAI-shaped finish
// the harness and callers already understand.
func messagesFinish(stopReason string) string {
	switch stopReason {
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	case "":
		return ""
	default: // end_turn, stop_sequence, refusal, pause_turn
		return "stop"
	}
}

// decodeMessagesCompletion parses the blocking Messages reply into the shared
// Completion shape — same fields, same empty-completion guard as the
// chat-completions decoder in once().
func decodeMessagesCompletion(providerHost string, payload, body []byte) (Completion, error) {
	var decoded messagesResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return Completion{}, fmt.Errorf("decode response: %w", err)
	}
	if decoded.Error != nil {
		return Completion{}, &transportError{err: fmt.Errorf("anthropic error (%s): %s", decoded.Error.Type, decoded.Error.Message)}
	}
	var content, reasoning strings.Builder
	var calls []ToolCall
	for _, block := range decoded.Content {
		switch block.Type {
		case "text":
			content.WriteString(block.Text)
		case "thinking":
			reasoning.WriteString(block.Thinking)
		case "tool_use":
			calls = append(calls, ToolCall{ID: block.ID, Name: block.Name, Args: block.Input})
		}
	}
	completion := Completion{
		Content:   content.String(),
		Reasoning: reasoning.String(),
		Finish:    messagesFinish(decoded.StopReason),
		ToolCalls: calls,
		Provider:  providerHost,
	}
	// Same rule as once(): a 200 with no text, no tool calls and no reasoning
	// is a provider failure and rides the retry ladder, not the caller.
	if strings.TrimSpace(completion.Content) == "" && len(completion.ToolCalls) == 0 && strings.TrimSpace(completion.Reasoning) == "" {
		return Completion{}, &transportError{err: fmt.Errorf("provider returned an empty completion (no content, tool calls or reasoning); body: %s", truncate(string(body), 400))}
	}
	if decoded.Usage != nil {
		completion.Usage = Usage{
			PromptTokens:     decoded.Usage.InputTokens,
			CompletionTokens: decoded.Usage.OutputTokens,
			TotalTokens:      decoded.Usage.InputTokens + decoded.Usage.OutputTokens,
			CachedTokens:     decoded.Usage.CacheReadInputTokens,
		}
	}
	completion.RequestRef = contentRef(payload)
	completion.ResponseRef = contentRef(body)
	return completion, nil
}

// --- streaming ----------------------------------------------------------

// messagesStreamComplete is the Messages twin of StreamComplete: same
// watchdogs, same limiter, same Completion observability fields — only the
// SSE event grammar differs. Anthropic frames events as event/data pairs; the
// data lines carry a "type" field that identifies them, so the scanner keys
// off data alone (keepalive pings are data lines with type=ping and reset the
// idle timer like the chat wire's blank lines do — they are liveness, not
// progress).
func (c *Client) messagesStreamComplete(ctx context.Context, messages []Message, tools []ToolDef, onToken TokenCallback, options ...Option) (Completion, error) {
	req := chatRequest{
		Model:       c.cfg.Model,
		Messages:    toRequestMessages(messages),
		Temperature: c.cfg.Temperature,
		MaxTokens:   c.cfg.MaxOutputTokens,
		Stream:      true,
	}
	if len(tools) > 0 {
		parallel := false
		req.ToolChoice = "auto"
		req.ParallelToolCalls = &parallel
	}
	for _, tool := range tools {
		req.Tools = append(req.Tools, chatTool{Type: "function", Function: tool})
	}
	for _, option := range options {
		option(&req)
	}
	// No Messages surface for these; sending them would be unknown-field noise.
	req.ResponseFormat = nil
	req.Reasoning = nil
	req.PromptCacheKey = ""
	req.StreamOptions = nil
	payload, err := c.encodePayload(&req)
	if err != nil {
		return Completion{}, err
	}
	if err := c.limiter.acquire(ctx); err != nil {
		return Completion{}, err
	}
	defer c.limiter.release()

	started := time.Now()
	streamCtx, cancelStream := context.WithCancel(ctx)
	defer cancelStream()
	request, err := http.NewRequestWithContext(streamCtx, http.MethodPost, c.completionsURL(), bytes.NewReader(payload))
	if err != nil {
		return Completion{}, err
	}
	c.applyRequestHeaders(request, true)
	watchdogs := c.startStreamWatchdogs(cancelStream, payload)
	defer watchdogs.halt()

	response, err := c.streamHTTP.Do(request)
	if err != nil {
		return Completion{}, watchdogs.doFailure(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4<<20))
		return Completion{}, c.statusErrorFrom(request, response, body, payload)
	}

	scanner := bufio.NewScanner(response.Body)
	// Same ceiling as the chat wire: one huge input_json_delta must not kill
	// the stream with a scanner overflow.
	scanner.Buffer(make([]byte, 64*1024), 1<<20)

	var accumulated, reasoning strings.Builder
	type toolBlock struct {
		id   string
		name string
		raw  strings.Builder
	}
	toolBlocks := map[int]*toolBlock{}
	var finish string
	var usage Usage
	deltas := 0
	var firstTokenAt time.Time
	stop := false

	noteProgress := func() bool {
		watchdogs.progress()
		deltas++
		if firstTokenAt.IsZero() {
			firstTokenAt = time.Now()
		}
		return onToken != nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var event messagesEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			// Unparsable data lines are protocol noise, not traffic either.
			continue
		}
		watchdogs.traffic()
		switch event.Type {
		case "message_start":
			if event.Message != nil && event.Message.Usage != nil {
				usage.PromptTokens = event.Message.Usage.InputTokens
				usage.CachedTokens = event.Message.Usage.CacheReadInputTokens
				usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
			}
		case "content_block_start":
			if event.ContentBlock != nil && event.ContentBlock.Type == "tool_use" {
				toolBlocks[event.Index] = &toolBlock{id: event.ContentBlock.ID, name: event.ContentBlock.Name}
			}
		case "content_block_delta":
			if event.Delta == nil {
				continue
			}
			switch event.Delta.Type {
			case "text_delta":
				accumulated.WriteString(event.Delta.Text)
				if noteProgress() && onToken(StreamDelta{Text: event.Delta.Text}) {
					stop = true
				}
			case "thinking_delta":
				reasoning.WriteString(event.Delta.Thinking)
				if noteProgress() && onToken(StreamDelta{Reasoning: event.Delta.Thinking}) {
					stop = true
				}
			case "input_json_delta":
				block := toolBlocks[event.Index]
				if block != nil {
					block.raw.WriteString(event.Delta.PartialJSON)
				}
				if noteProgress() {
					delta := ToolCallDelta{Index: event.Index, ArgsDelta: event.Delta.PartialJSON}
					if block != nil {
						delta.ID = block.id
						delta.Name = block.name
					}
					onToken(StreamDelta{ToolCalls: []ToolCallDelta{delta}})
				}
			}
		case "message_delta":
			if event.Delta != nil && event.Delta.StopReason != "" {
				finish = messagesFinish(event.Delta.StopReason)
			}
			if event.Usage != nil {
				usage.CompletionTokens = event.Usage.OutputTokens
				usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
			}
		case "error":
			errType, errMsg := "unknown", "stream error"
			if event.Error != nil {
				errType, errMsg = event.Error.Type, event.Error.Message
			}
			return Completion{}, &transportError{err: fmt.Errorf("anthropic stream error (%s): %s", errType, errMsg)}
		}
		if stop {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return Completion{}, watchdogs.scannerFailure(err)
	}

	indexes := make([]int, 0, len(toolBlocks))
	for index := range toolBlocks {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	var calls []ToolCall
	for _, index := range indexes {
		block := toolBlocks[index]
		args := map[string]any{}
		argsError := ""
		if raw := strings.TrimSpace(block.raw.String()); raw != "" {
			if err := json.Unmarshal([]byte(raw), &args); err != nil {
				args = nil
				argsError = err.Error()
			}
		}
		calls = append(calls, ToolCall{ID: block.id, Name: block.name, Args: args, ArgsRaw: block.raw.String(), ArgsError: argsError})
	}

	completion := Completion{
		Content:      accumulated.String(),
		Reasoning:    reasoning.String(),
		Finish:       finish,
		Provider:     c.providerHost,
		LatencyMs:    time.Since(started).Milliseconds(),
		Usage:        usage,
		RequestRef:   contentRef(payload),
		Transport:    "stream",
		Deltas:       deltas,
		FirstTokenMs: firstTokenAt.Sub(started).Milliseconds(),
		ToolCalls:    calls,
	}
	completion.ResponseRef = contentRef([]byte(completion.Content))
	completion = rescueTextToolCalls(completion)
	// Same truncation contract as the chat wire: a stream that ends without
	// an answer, tool calls or finish reason is a provider-side truncation
	// and must fail, not complete the run with an empty answer.
	if !stop && completion.Finish == "" && completion.Content == "" && len(completion.ToolCalls) == 0 {
		return Completion{}, &transportError{err: errors.New("stream ended mid-generation: no answer, tool calls or finish reason")}
	}
	c.observeUsage(ctx, completion)
	return completion, nil
}

// --- shared stream watchdogs --------------------------------------------

// streamWatchdogs owns the three silence budgets (idle / no-progress /
// first-token) shared by both wires' streaming paths. lastTraffic counts any
// parseable data line (liveness); lastProgress counts only chunks carrying
// generated tokens (proof of generation); anyProgress remembers whether a
// generated token was ever seen, which gates the first-token budget.
type streamWatchdogs struct {
	idle         time.Duration
	noProgress   time.Duration
	firstToken   time.Duration
	lastTraffic  atomic.Int64
	lastProgress atomic.Int64
	anyProgress  atomic.Bool
	cancelReason atomic.Value // string: which budget fired, for the error
	stopWatchdog chan struct{}
	cancel       context.CancelFunc
}

// startStreamWatchdogs arms the budgets and launches the ticker goroutine.
// The caller must halt() it before the stream context is torn down for good.
func (c *Client) startStreamWatchdogs(cancelStream context.CancelFunc, payload []byte) *streamWatchdogs {
	watchdogs := &streamWatchdogs{
		idle:         c.cfg.StreamIdleTimeout,
		noProgress:   c.cfg.StreamNoProgressTimeout,
		firstToken:   c.firstTokenBudget(payload),
		stopWatchdog: make(chan struct{}),
		cancel:       cancelStream,
	}
	start := time.Now().UnixNano()
	watchdogs.lastTraffic.Store(start)
	watchdogs.lastProgress.Store(start)
	go func() {
		// Tick at the finest granularity of the three budgets — a budget may
		// fire up to one tick late, and the finest one is the one that guards
		// the activity deadline.
		tick := watchdogs.idle / 4
		if watchdogs.noProgress > 0 && watchdogs.noProgress/4 < tick {
			tick = watchdogs.noProgress / 4
		}
		if watchdogs.firstToken > 0 && watchdogs.firstToken/4 < tick {
			tick = watchdogs.firstToken / 4
		}
		if tick <= 0 {
			tick = time.Second
		}
		ticker := time.NewTicker(tick)
		defer ticker.Stop()
		for {
			select {
			case <-watchdogs.stopWatchdog:
				return
			case <-ticker.C:
				now := time.Now()
				if now.Sub(time.Unix(0, watchdogs.lastTraffic.Load())) >= watchdogs.idle {
					watchdogs.cancelReason.Store(fmt.Sprintf("idle timeout: no data lines for %s", watchdogs.idle))
					watchdogs.cancel()
					return
				}
				if watchdogs.noProgress > 0 && now.Sub(time.Unix(0, watchdogs.lastProgress.Load())) >= watchdogs.noProgress {
					watchdogs.cancelReason.Store(fmt.Sprintf("stalled stream: no generated tokens for %s", watchdogs.noProgress))
					watchdogs.cancel()
					return
				}
				if watchdogs.firstToken > 0 && !watchdogs.anyProgress.Load() && now.Sub(time.Unix(0, start)) >= watchdogs.firstToken {
					watchdogs.cancelReason.Store(fmt.Sprintf("first token timeout: no generated tokens for %s", watchdogs.firstToken))
					watchdogs.cancel()
					return
				}
			}
		}
	}()
	return watchdogs
}

// traffic records a parseable data line (any payload).
func (w *streamWatchdogs) traffic() { w.lastTraffic.Store(time.Now().UnixNano()) }

// progress records a chunk that carried generated tokens.
func (w *streamWatchdogs) progress() {
	w.lastTraffic.Store(time.Now().UnixNano())
	w.lastProgress.Store(time.Now().UnixNano())
	w.anyProgress.Store(true)
}

// halt stops the ticker goroutine.
func (w *streamWatchdogs) halt() { close(w.stopWatchdog) }

// doFailure maps a streamHTTP.Do error, preserving the watchdog identity: a
// budget-canceled request must not masquerade as a transport error and send
// callers into the blocking retry ladder (the queue lesson from the chat wire).
func (w *streamWatchdogs) doFailure(err error) error {
	if reason, ok := w.cancelReason.Load().(string); ok {
		return &watchdogError{reason: reason, cause: err}
	}
	return &transportError{err: err}
}

// scannerFailure maps a scanner error the same way: the watchdog may have
// canceled the context mid-read.
func (w *streamWatchdogs) scannerFailure(err error) error {
	if reason, ok := w.cancelReason.Load().(string); ok {
		return &watchdogError{reason: reason, cause: err}
	}
	return fmt.Errorf("stream read: %w", err)
}
