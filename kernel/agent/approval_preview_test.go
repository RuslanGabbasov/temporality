package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestApprovalPreviewRedactsSecretsAndBoundsArguments(t *testing.T) {
	args := map[string]any{"command": []any{"docker", "login", "--password", "sensitive", "--token=abc123", "curl", "https://alice:pass@example.test/?token=query-secret&mode=fast", "TOKEN=secret go test ./...", strings.Repeat("x", 300)}}
	preview, details := approvalOperation("operation-1", "run_command", args, false)
	encoded, err := json.Marshal(preview)
	if err != nil {
		t.Fatal(err)
	}
	value := string(encoded)
	for _, secret := range []string{"sensitive", "abc123", "alice:pass", "query-secret"} {
		if strings.Contains(value, secret) {
			t.Fatalf("preview leaked %q: %s", secret, value)
		}
	}
	if !strings.Contains(value, "[REDACTED]") || details["workspace"] != "/workspace" {
		t.Fatalf("missing safe preview fields: %s", value)
	}
	operation := preview["operation"].(map[string]any)
	if !strings.Contains(fmt.Sprint(operation["summary"]), "go test") {
		t.Fatalf("redaction hid the rest of the shell command: %s", value)
	}
}

func TestApprovalOperationHashesCanonicalArgsAndRedactsDisplay(t *testing.T) {
	args := map[string]any{
		"issue":  map[string]any{"title": "Fix parser", "api_token": "secret-value"},
		"labels": []any{"bug", "needs-review"},
	}
	first, details := approvalOperation("run/turn/01/tool-1", "mcp__issues__create_issue", args, false)
	second, _ := approvalOperation("run/turn/01/tool-1", "mcp__issues__create_issue", map[string]any{"labels": []any{"bug", "needs-review"}, "issue": args["issue"]}, false)
	description := first["operation"].(map[string]any)
	require.Equal(t, "run/turn/01/tool-1", description["id"])
	require.Equal(t, operationArgumentsHash(args), description["arguments_hash"])
	require.Equal(t, "mcp__issues__create_issue", details["tool"])
	encoded, err := json.Marshal(first)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "secret-value")
	require.Contains(t, string(encoded), "[REDACTED]")
	require.Equal(t, description["arguments_hash"], second["operation"].(map[string]any)["arguments_hash"])
	redaction := first["redaction"].(map[string]any)
	require.Equal(t, true, redaction["applied"])
}
