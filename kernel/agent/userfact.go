package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// userFactOriginAgent marks facts saved by the dialog tool (vs the user
// settings screen); mirrors workspace.UserFactOriginAgent without the import.
const userFactOriginAgent = "agent"

// user_remember accumulates explicit durable facts and preferences about the
// CURRENT sender («предпочитает краткие ответы», «ведёт проект Omni»): the
// accumulated half of the per-user picture. The kernel resolves the sender
// from the run's actor — every entrypoint stamps it, so a channel dialog run
// always knows whose fact it is storing.

// handleUserRemember stores one fact about the sender via the workspace API.
// Duplicates are idempotent (the store dedups by exact text).
func (a *Activities) handleUserRemember(ctx context.Context, request ToolRequest) (ToolResult, error) {
	if request.ActorID == "" {
		return ToolResult{Content: "error: no sender resolved for this run — user facts are only stored for identified senders"}, nil
	}
	fact, _ := request.Arguments["fact"].(string)
	fact = strings.TrimSpace(fact)
	if fact == "" {
		return ToolResult{Content: "error: fact is required"}, nil
	}
	if len(fact) > 500 {
		fact = fact[:500]
	}
	body, status, err := a.workspaceDo(ctx, http.MethodPost,
		fmt.Sprintf("%s/v1/workspace/users/%s/facts", a.WorkspaceURL, url.PathEscape(request.ActorID)),
		map[string]any{"fact": fact, "origin": userFactOriginAgent})
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	if status >= 400 {
		return ToolResult{Content: fmt.Sprintf("error: user fact failed (HTTP %d): %s", status, truncate(body, 300))}, nil
	}
	return ToolResult{Content: "Saved. The fact now rides in the sender's profile for every future dialog."}, nil
}

// handleUserRemember is exercised through RunTool in tests; the stub server
// records the payload (see userfact_test.go).
