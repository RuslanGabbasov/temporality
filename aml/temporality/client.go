// Package temporality adapts the generic observation API to the optional hook
// surface of the AML harness. The harness remains in control of its agent loop.
package temporality

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/temporality-project/temporality/aml/harness"
	"github.com/temporality-project/temporality/aml/llm"
)

// Config identifies an external harness source and its project namespace.
// Set SourceID uniquely per producer so event IDs can remain locally generated.
type Config struct {
	BaseURL   string
	Project   string
	SourceID  string
	ActorID   string
	ActorType string
	Timeout   time.Duration
}

type Client struct {
	base   string
	config Config
	http   *http.Client

	mu          sync.Mutex
	taskText    map[string]string
	sessionTask map[string]string
	hintIDs     map[string][]string
}

type event struct {
	Schema     string         `json:"schema"`
	EventID    string         `json:"event_id"`
	OccurredAt time.Time      `json:"occurred_at"`
	Source     source         `json:"source"`
	Context    eventContext   `json:"context"`
	Type       string         `json:"type"`
	Data       map[string]any `json:"data,omitempty"`
}

type source struct {
	ID          string `json:"id"`
	Integration string `json:"integration"`
	Version     string `json:"version,omitempty"`
}

type eventContext struct {
	Project string `json:"project"`
	Run     string `json:"run,omitempty"`
	Task    string `json:"task,omitempty"`
	Actor   actor  `json:"actor,omitempty"`
}

type actor struct {
	ID   string `json:"id"`
	Type string `json:"type,omitempty"`
}

type hintRequest struct {
	Project    string `json:"project"`
	Run        string `json:"run,omitempty"`
	Task       string `json:"task,omitempty"`
	Actor      actor  `json:"actor,omitempty"`
	Query      string `json:"query,omitempty"`
	Tool       string `json:"tool,omitempty"`
	ToolResult string `json:"tool_result,omitempty"`
	Limit      int    `json:"limit"`
}

type hintReply struct {
	Hints []struct {
		ID          string `json:"knowledge_id"`
		HintID      string `json:"hint_id"`
		Proposition string `json:"proposition"`
		State       string `json:"state"`
		Caution     string `json:"caution,omitempty"`
	} `json:"hints"`
}

// New constructs a best-effort adapter. Hook failures degrade to no memory and
// never fail the task. A short default timeout bounds added loop latency.
func New(config Config) *Client {
	config.BaseURL = strings.TrimRight(config.BaseURL, "/")
	if config.SourceID == "" {
		config.SourceID = "aml-harness"
	}
	if config.ActorType == "" {
		config.ActorType = "agent"
	}
	if config.Timeout <= 0 {
		config.Timeout = 1200 * time.Millisecond
	}
	return &Client{
		base: config.BaseURL, config: config,
		http:     &http.Client{Timeout: config.Timeout},
		taskText: map[string]string{}, sessionTask: map[string]string{}, hintIDs: map[string][]string{},
	}
}

func (c *Client) TaskStart(_ context.Context, sessionID, taskID, taskText string) []harness.Hint {
	c.mu.Lock()
	c.taskText[taskID] = taskText
	c.sessionTask[sessionID] = taskID
	c.mu.Unlock()
	c.record(sessionID, taskID, "task.started", nil)
	return nil
}

func (c *Client) BeforeToolCall(_ context.Context, sessionID string, call llm.ToolCall) []harness.Hint {
	c.record(sessionID, "", "tool.started", map[string]any{"tool": call.Name, "tool_call_id": call.ID})
	return nil
}

func (c *Client) AfterToolCall(_ context.Context, sessionID string, call llm.ToolCall, result harness.ToolResult) {
	c.record(sessionID, "", "tool.completed", map[string]any{
		"tool": call.Name, "tool_call_id": call.ID, "ok": result.OK, "status": result.Status, "cause": result.Cause,
	})
}

// HintsAfterToolCall performs supplemental activation after the result is
// available. The result is sent only to the activation request and is never
// copied into the observation event stream.
func (c *Client) HintsAfterToolCall(_ context.Context, sessionID string, call llm.ToolCall, result harness.ToolResult) []harness.Hint {
	if c.base == "" || c.config.Project == "" {
		return nil
	}
	c.mu.Lock()
	taskID := c.sessionTask[sessionID]
	taskText := c.taskText[taskID]
	c.mu.Unlock()
	request := hintRequest{
		Project: c.config.Project, Run: sessionID, Task: taskID,
		Actor: actor{ID: c.config.ActorID, Type: c.config.ActorType},
		Query: clip(taskText, 4096), Tool: call.Name, ToolResult: clip(result.Text, 8192), Limit: 8,
	}
	body, err := json.Marshal(request)
	if err != nil {
		return nil
	}
	response, err := c.http.Post(c.base+"/v1/observations/hints", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil
	}
	var payload hintReply
	if json.NewDecoder(response.Body).Decode(&payload) != nil {
		return nil
	}
	hints := make([]harness.Hint, 0, len(payload.Hints))
	used := make([]string, 0, len(payload.Hints))
	for _, item := range payload.Hints {
		text := "[Temporality " + item.State + "] " + item.Proposition
		if item.Caution != "" {
			text += " (" + item.Caution + ")"
		}
		hints = append(hints, harness.Hint{Text: text})
		if item.HintID != "" {
			used = append(used, item.HintID)
			c.record(sessionID, "", "hint.used", map[string]any{"hint_id": item.HintID, "knowledge_id": item.ID})
		}
	}
	c.mu.Lock()
	c.hintIDs[sessionID] = append(c.hintIDs[sessionID], used...)
	c.mu.Unlock()
	return hints
}

func (c *Client) TaskEnd(_ context.Context, sessionID, taskID string, success bool, _ string) {
	c.record(sessionID, taskID, "task.completed", map[string]any{"success": success})
	c.mu.Lock()
	ids := append([]string(nil), c.hintIDs[sessionID]...)
	delete(c.hintIDs, sessionID)
	delete(c.taskText, taskID)
	delete(c.sessionTask, sessionID)
	c.mu.Unlock()
	outcome := "task_failed"
	if success {
		outcome = "task_succeeded"
	}
	for _, id := range ids {
		c.record(sessionID, taskID, "hint.outcome", map[string]any{"hint_id": id, "outcome": outcome, "attribution": "correlated_task_outcome"})
	}
}

func (c *Client) record(sessionID, taskID, kind string, data map[string]any) {
	if c.base == "" || c.config.Project == "" {
		return
	}
	if taskID == "" {
		c.mu.Lock()
		taskID = c.sessionTask[sessionID]
		c.mu.Unlock()
	}
	identifier := c.config.ActorID
	if identifier == "" {
		identifier = "agent"
	}
	item := event{
		Schema: "temporality.event/1", EventID: eventID(), OccurredAt: time.Now().UTC(),
		Source:  source{ID: c.config.SourceID, Integration: "aml-harness", Version: "1"},
		Context: eventContext{Project: c.config.Project, Run: sessionID, Task: taskID, Actor: actor{ID: identifier, Type: c.config.ActorType}},
		Type:    kind, Data: data,
	}
	body, err := json.Marshal(map[string]any{"events": []event{item}})
	if err != nil {
		return
	}
	request, err := http.NewRequest(http.MethodPost, c.base+"/v1/observations/events", bytes.NewReader(body))
	if err != nil {
		return
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err == nil {
		_ = response.Body.Close()
	}
}

func eventID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return time.Now().UTC().Format("20060102T150405.000000000")
	}
	return hex.EncodeToString(raw[:])
}

func clip(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
