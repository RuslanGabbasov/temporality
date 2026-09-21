package sidecar

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/temporality-project/temporality/aml/harness"
	"github.com/temporality-project/temporality/aml/llm"
	"github.com/temporality-project/temporality/aml/memory"
)

// Client implements harness.Hooks over the sidecar HTTP contract. The harness
// calls hooks sequentially per task, so the client tracks the current task
// from TaskStart; every hook failure degrades to "no memory", never blocking
// the loop.
type Client struct {
	base      string
	sessionID string
	env       string
	repo      string
	branch    string
	http      *http.Client

	mu           sync.Mutex
	currentTask  string
	lastStats    memory.Stats
	statsReady   bool
	lastInjected int
}

// NewClient builds the hook client for one session/environment pair.
func NewClient(base, sessionID, environment, repository, branch string) *Client {
	return &Client{
		base:      base,
		sessionID: sessionID,
		env:       environment,
		repo:      repository,
		branch:    branch,
		http:      &http.Client{Timeout: 30 * time.Second},
	}
}

// TaskStart starts the task on the sidecar and returns its hints.
func (c *Client) TaskStart(_ context.Context, _, taskID, taskText string) []harness.Hint {
	c.mu.Lock()
	c.currentTask = taskID
	c.mu.Unlock()
	req := TaskStartRequest{
		TaskID:      taskID,
		SessionID:   c.sessionID,
		Task:        taskText,
		Repository:  c.repo,
		Branch:      c.branch,
		Environment: c.env,
	}
	var out MemoriesResponse
	if err := c.post("/task/start", req, &out); err != nil {
		return nil
	}
	var hints []harness.Hint
	for _, m := range out.Memories {
		hints = append(hints, harness.Hint{Text: m.Hint})
	}
	return hints
}

// BeforeToolCall performs the pre-action probe (pre-action activation and the
// soft guardrail).
func (c *Client) BeforeToolCall(_ context.Context, _ string, call llm.ToolCall) []harness.Hint {
	c.mu.Lock()
	taskID := c.currentTask
	c.mu.Unlock()
	req := ToolBeforeRequest{TaskID: taskID, SessionID: c.sessionID, Tool: call.Name, Arguments: call.Args}
	var out MemoriesResponse
	if err := c.post("/tool/before", req, &out); err != nil {
		return nil
	}
	var hints []harness.Hint
	for _, m := range out.Memories {
		hints = append(hints, harness.Hint{Text: m.Hint})
	}
	return hints
}

// AfterToolCall records the observation for reuse/outcome attribution.
func (c *Client) AfterToolCall(_ context.Context, _ string, call llm.ToolCall, result harness.ToolResult) {
	c.mu.Lock()
	taskID := c.currentTask
	c.mu.Unlock()
	req := ToolAfterRequest{TaskID: taskID, SessionID: c.sessionID, Tool: call.Name, Arguments: call.Args}
	req.Result.OK = result.OK
	req.Result.Status = result.Status
	req.Result.Text = truncate(result.Text, 2000)
	req.Result.Cause = result.Cause
	_ = c.post("/tool/after", req, nil)
}

// TaskEnd closes the task and captures the final stats.
func (c *Client) TaskEnd(_ context.Context, _, taskID string, success bool, answer string) {
	req := TaskEndRequest{TaskID: taskID, SessionID: c.sessionID, Success: success, Answer: truncate(answer, 2000)}
	var out struct {
		OK    bool         `json:"ok"`
		Stats memory.Stats `json:"stats"`
	}
	if err := c.post("/task/end", req, &out); err != nil {
		return
	}
	c.mu.Lock()
	c.lastStats = out.Stats
	c.statsReady = true
	c.mu.Unlock()
}

// Stats returns the stats of the last finished task on this client.
func (c *Client) Stats() (memory.Stats, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastStats, c.statsReady
}

func (c *Client) post(path string, body, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	request, err := http.NewRequest(http.MethodPost, c.base+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("sidecar %s: status %d", path, response.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(response.Body).Decode(out)
}

func truncate(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "…"
}
