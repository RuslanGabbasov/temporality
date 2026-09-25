// Package llm provides a minimal OpenAI-compatible chat client with tool
// calling, used by the classic agent harness in the adaptive-memory vertical
// slice. It intentionally shares the TEMPORALITY_MODEL_* environment contract
// with the rest of the repository so benchmarks run against the same provider
// configuration.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// ToolDef describes one callable tool in the OpenAI function-calling format.
type ToolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"` // JSON schema object
}

// Message is one chat message. ToolCallID links assistant tool calls to the
// tool-role replies.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// ToolCall is a model-requested tool invocation. Args are already decoded
// from the provider's JSON string.
type ToolCall struct {
	ID      string         `json:"id"`
	Name    string         `json:"name"`
	Args    map[string]any `json:"args"`
	ArgsRaw string         `json:"args_raw"`
}

// Completion is one assistant turn.
type Completion struct {
	Content   string
	ToolCalls []ToolCall
	Usage     Usage
	Finish    string
}

// Usage carries provider token accounting.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Config is resolved from the environment.
type Config struct {
	BaseURL         string
	Model           string
	APIKey          string
	Temperature     float64
	Timeout         time.Duration
	MaxOutputTokens int
	Reasoning       string
}

// ConfigFromEnv reads the shared TEMPORALITY_MODEL_* variables.
func ConfigFromEnv() (Config, error) {
	cfg := Config{
		BaseURL:         strings.TrimSpace(os.Getenv("TEMPORALITY_MODEL_BASE_URL")),
		Model:           strings.TrimSpace(os.Getenv("TEMPORALITY_MODEL_ID")),
		APIKey:          strings.TrimSpace(os.Getenv("TEMPORALITY_MODEL_API_KEY")),
		Temperature:     0,
		Timeout:         180 * time.Second,
		MaxOutputTokens: 1024,
		Reasoning:       strings.TrimSpace(os.Getenv("TEMPORALITY_MODEL_REASONING")),
	}
	if cfg.BaseURL == "" || cfg.Model == "" {
		return cfg, errors.New("TEMPORALITY_MODEL_BASE_URL and TEMPORALITY_MODEL_ID are required")
	}
	if raw := strings.TrimSpace(os.Getenv("TEMPORALITY_MODEL_TEMPERATURE")); raw != "" {
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil || value < 0 || value > 2 {
			return cfg, fmt.Errorf("invalid TEMPORALITY_MODEL_TEMPERATURE: %s", raw)
		}
		cfg.Temperature = value
	}
	if raw := strings.TrimSpace(os.Getenv("TEMPORALITY_MODEL_TIMEOUT")); raw != "" {
		value, err := time.ParseDuration(raw)
		if err != nil || value <= 0 {
			return cfg, fmt.Errorf("invalid TEMPORALITY_MODEL_TIMEOUT: %s", raw)
		}
		cfg.Timeout = value
	}
	if raw := strings.TrimSpace(os.Getenv("TEMPORALITY_MODEL_MAX_OUTPUT_TOKENS")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 64 || value > 65536 {
			return cfg, fmt.Errorf("invalid TEMPORALITY_MODEL_MAX_OUTPUT_TOKENS: %s", raw)
		}
		cfg.MaxOutputTokens = value
	}
	return cfg, nil
}

// Client talks to one OpenAI-compatible endpoint.
type Client struct {
	cfg    Config
	http   *http.Client
	jitter *rand.Rand
}

// New builds a client.
func New(cfg Config) *Client {
	return &Client{cfg: cfg, http: &http.Client{Timeout: cfg.Timeout}, jitter: rand.New(rand.NewSource(1))}
}

// Model identifies the configured model in reports.
func (c *Client) Model() string { return c.cfg.Model }

type chatTool struct {
	Type     string  `json:"type"`
	Function ToolDef `json:"function"`
}

type toolCallWire struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type requestMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content"`
	ToolCalls  []toolCallWire `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type chatRequest struct {
	Model       string           `json:"model"`
	Messages    []requestMessage `json:"messages"`
	Tools       []chatTool       `json:"tools,omitempty"`
	ToolChoice  string           `json:"tool_choice,omitempty"`
	Temperature float64          `json:"temperature"`
	MaxTokens   int              `json:"max_tokens"`
	Reasoning   map[string]any   `json:"reasoning,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content   string         `json:"content"`
			ToolCalls []toolCallWire `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Code    int    `json:"code"`
	} `json:"error"`
}

// reasoningRequest maps the reasoning preset onto the provider field the same
// way the FRP adapter does, so both stacks share semantics.
func reasoningRequest(preset string) map[string]any {
	switch preset {
	case "", "off":
		return map[string]any{"enabled": false}
	case "exclude":
		return map[string]any{"exclude": true}
	case "low", "medium", "high":
		return map[string]any{"effort": preset}
	default:
		return map[string]any{"enabled": false}
	}
}

func toRequestMessages(messages []Message) []requestMessage {
	out := make([]requestMessage, 0, len(messages))
	for _, m := range messages {
		rm := requestMessage{Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID}
		for _, call := range m.ToolCalls {
			var tc toolCallWire
			tc.ID = call.ID
			tc.Type = "function"
			tc.Function.Name = call.Name
			tc.Function.Arguments = call.ArgsRaw
			rm.ToolCalls = append(rm.ToolCalls, tc)
		}
		out = append(out, rm)
	}
	return out
}

// Complete performs one chat completion. It retries transient provider
// failures (429/5xx/network) with bounded backoff.
func (c *Client) Complete(ctx context.Context, messages []Message, tools []ToolDef) (Completion, error) {
	req := chatRequest{
		Model:       c.cfg.Model,
		Messages:    toRequestMessages(messages),
		Temperature: c.cfg.Temperature,
		MaxTokens:   c.cfg.MaxOutputTokens,
		Reasoning:   reasoningRequest(c.cfg.Reasoning),
		ToolChoice:  "auto",
	}
	for _, tool := range tools {
		req.Tools = append(req.Tools, chatTool{Type: "function", Function: tool})
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return Completion{}, err
	}

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			delay := time.Duration(1<<uint(attempt))*time.Second + time.Duration(c.jitter.Int63n(1500))*time.Millisecond
			select {
			case <-ctx.Done():
				return Completion{}, ctx.Err()
			case <-time.After(delay):
			}
		}
		completion, err := c.once(ctx, payload)
		if err == nil {
			return completion, nil
		}
		lastErr = err
		if !transient(err) {
			return Completion{}, err
		}
	}
	return Completion{}, fmt.Errorf("giving up after retries: %w", lastErr)
}

func transient(err error) bool {
	var statusErr *statusError
	if errors.As(err, &statusErr) {
		return statusErr.code == http.StatusTooManyRequests || statusErr.code >= 500
	}
	return false
}

type statusError struct {
	code int
	body string
}

func (e *statusError) Error() string {
	return fmt.Sprintf("provider status %d: %s", e.code, e.body)
}

func (c *Client) once(ctx context.Context, payload []byte) (Completion, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.cfg.BaseURL, "/")+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return Completion{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	if c.cfg.APIKey != "" {
		request.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return Completion{}, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return Completion{}, err
	}
	if response.StatusCode != http.StatusOK {
		return Completion{}, &statusError{code: response.StatusCode, body: truncate(string(body), 400)}
	}
	var decoded chatResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return Completion{}, fmt.Errorf("decode response: %w", err)
	}
	if decoded.Error != nil {
		return Completion{}, &statusError{code: decoded.Error.Code, body: decoded.Error.Message}
	}
	if len(decoded.Choices) == 0 {
		return Completion{}, errors.New("provider returned no choices")
	}
	choice := decoded.Choices[0]
	completion := Completion{Content: choice.Message.Content, Finish: choice.FinishReason}
	for _, call := range choice.Message.ToolCalls {
		args := map[string]any{}
		if strings.TrimSpace(call.Function.Arguments) != "" {
			if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
				return Completion{}, fmt.Errorf("decode tool arguments for %s: %w", call.Function.Name, err)
			}
		}
		completion.ToolCalls = append(completion.ToolCalls, ToolCall{
			ID:      call.ID,
			Name:    call.Function.Name,
			Args:    args,
			ArgsRaw: call.Function.Arguments,
		})
	}
	if decoded.Usage != nil {
		completion.Usage = Usage{
			PromptTokens:     decoded.Usage.PromptTokens,
			CompletionTokens: decoded.Usage.CompletionTokens,
			TotalTokens:      decoded.Usage.TotalTokens,
		}
	}
	return completion, nil
}

func truncate(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "..."
}
