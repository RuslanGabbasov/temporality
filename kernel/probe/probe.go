// Package probe provides read-after-write verification for tool operations.
// After a tool executes (or fails with uncertain effect), probes check for
// observable traces and record hints for the operator. Probes never
// automatically verdict — they only surface evidence.
package probe

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/temporality-project/temporality/observation"
)

// Result is the outcome of one probe.
type Result struct {
	OperationID string                 `json:"operation_id"`
	ProbeType   string                 `json:"probe_type"`
	Observable  bool                   `json:"observable"`
	Detail      string                 `json:"detail,omitempty"`
	Evidence    []observation.Evidence `json:"evidence,omitempty"`
}

// Probe checks whether an operation's effect is observable downstream.
type Probe interface {
	Check(ctx context.Context, op Operation) (Result, error)
}

// Operation describes one tool execution to probe.
type Operation struct {
	Project     string         `json:"project"`
	RunID       string         `json:"run_id"`
	OperationID string         `json:"operation_id"`
	Tool        string         `json:"tool"`
	Arguments   map[string]any `json:"arguments,omitempty"`
	Effect      string         `json:"effect,omitempty"`
}

// FileProbe checks whether a run_command that redirects output left an
// observable file reference. It is a hint-only probe — the operator verifies
// actual existence.
type FileProbe struct{}

func (p *FileProbe) Check(_ context.Context, op Operation) (Result, error) {
	if op.Tool != "run_command" {
		return Result{OperationID: op.OperationID, ProbeType: "file"}, nil
	}
	command, _ := op.Arguments["command"].([]any)
	for i, arg := range command {
		s, ok := arg.(string)
		if !ok {
			continue
		}
		if (s == ">" || s == ">>") && i+1 < len(command) {
			if target, ok := command[i+1].(string); ok {
				return Result{
					OperationID: op.OperationID,
					ProbeType:   "file",
					Observable:  true,
					Detail:      fmt.Sprintf("command references %q; verify in workspace", target),
					Evidence:    []observation.Evidence{{Ref: "workspace:" + target, Type: "probe"}},
				}, nil
			}
		}
	}
	return Result{OperationID: op.OperationID, ProbeType: "file"}, nil
}

// MCPProbe checks whether an MCP operation left an observable trace by
// looking for mcp.call.completed in the journal.
type MCPProbe struct {
	JournalURL string
	HTTPClient *http.Client
	APIToken   string
}

func (p *MCPProbe) Check(ctx context.Context, op Operation) (Result, error) {
	if !strings.HasPrefix(op.Tool, "mcp__") {
		return Result{OperationID: op.OperationID, ProbeType: "mcp"}, nil
	}
	url := fmt.Sprintf("%s/v1/observations/events?type=mcp.call.completed&project=%s&run=%s&limit=5",
		p.JournalURL, op.Project, op.RunID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Result{}, err
	}
	if p.APIToken != "" {
		req.Header.Set("Authorization", "Bearer "+p.APIToken)
	}
	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Result{OperationID: op.OperationID, ProbeType: "mcp"}, nil
	}
	var page struct {
		Events []observation.Event `json:"events"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		return Result{}, err
	}
	for _, event := range page.Events {
		if event.Data["operation_id"] == op.OperationID {
			resultRef, _ := event.Data["result_ref"].(string)
			return Result{
				OperationID: op.OperationID,
				ProbeType:   "mcp",
				Observable:  true,
				Detail:      fmt.Sprintf("mcp.call.completed recorded (result_ref=%s)", resultRef),
				Evidence:    []observation.Evidence{{Ref: event.EventID, Type: "mcp_call"}},
			}, nil
		}
	}
	return Result{OperationID: op.OperationID, ProbeType: "mcp", Detail: "no mcp.call.completed found"}, nil
}

// ProbeEvent records a probe result as a derived event.
func ProbeEvent(project, runID, operationID string, result Result) observation.Event {
	data := map[string]any{
		"operation_id": operationID,
		"probe_type":   result.ProbeType,
		"observable":   result.Observable,
	}
	if result.Detail != "" {
		data["detail"] = result.Detail
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%d", project, runID, operationID, time.Now().UnixNano())))
	return observation.Event{
		Schema:     "temporality.event/1",
		EventID:    fmt.Sprintf("probe/%s", hex.EncodeToString(digest[:12])),
		OccurredAt: time.Now().UTC(),
		Source:     observation.Source{ID: "temporality-probe", Integration: "temporality", Version: "1"},
		Context: observation.Context{
			Project: project,
			Run:     runID,
			Actor:   observation.Actor{ID: "system", Type: "probe"},
		},
		Type:     "operation.probed",
		Data:     data,
		Evidence: result.Evidence,
	}
}
