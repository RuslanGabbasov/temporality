package mcpclient

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// One MCP call's budget is the server's ceiling — never a timeout_ms from the
// call arguments. The kernel used to honor the model's ask and killed Penpot
// export_shape at exactly its 2-minute guess (timeout_ms: 120000) while the
// operator's ceiling allowed 10 minutes. Call now derives the budget from
// serverCallBudget() alone; what is pinned here is the attribution: the
// kernel's own deadline must surface as an actionable error, and everything
// else must pass through untouched.
func TestCallTimeoutErrorAttribution(t *testing.T) {
	const tool = "mcp__penpot__export_shape"

	wrapped := fmt.Errorf("call tool: %w", context.DeadlineExceeded)
	err := callTimeoutError(tool, 10*time.Minute, wrapped)
	if err == nil || !strings.Contains(err.Error(), tool) || !strings.Contains(err.Error(), "10m0s") {
		t.Fatalf("the kernel's own deadline must name the tool and the expired budget, got %v", err)
	}

	if direct := callTimeoutError(tool, 10*time.Minute, context.DeadlineExceeded); !strings.Contains(direct.Error(), "timed out after") {
		t.Fatalf("a bare sentinel deadline must be attributed too, got %v", direct)
	}

	serverSide := errors.New("jsonrpc2: code -32000: context deadline exceeded")
	if got := callTimeoutError(tool, 10*time.Minute, serverSide); got != serverSide {
		t.Fatalf("a server-side timeout quoting the deadline text must pass through unchanged, got %v", got)
	}

	other := errors.New("connection reset")
	if got := callTimeoutError(tool, 10*time.Minute, other); got != other {
		t.Fatalf("non-deadline errors must pass through unchanged, got %v", got)
	}

	if canceled := callTimeoutError(tool, 10*time.Minute, context.Canceled); canceled != context.Canceled {
		t.Fatalf("caller cancellation is not this budget's expiry and must pass through, got %v", canceled)
	}
}

func TestServerCallBudgetDefault(t *testing.T) {
	if got := (&Client{}).serverCallBudget(); got != 10*time.Minute {
		t.Fatalf("a server without configured timeout_ms must keep the 10-minute default, got %v", got)
	}
	if got := (&Client{cfg: ServerConfig{TimeoutMs: 60000}}).serverCallBudget(); got != time.Minute {
		t.Fatalf("a configured timeout_ms must become the per-call budget verbatim, got %v", got)
	}
}
