package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestApprovalPreviewRedactsSecretsAndBoundsArguments(t *testing.T) {
	args := map[string]any{"command": []any{"docker", "login", "--password", "sensitive", "--token=abc123", "curl", "https://alice:pass@example.test", "echo", strings.Repeat("x", 300)}}
	preview := approvalPreview("run_command", args)
	encoded, err := json.Marshal(preview)
	if err != nil {
		t.Fatal(err)
	}
	value := string(encoded)
	for _, secret := range []string{"sensitive", "abc123", "alice:pass"} {
		if strings.Contains(value, secret) {
			t.Fatalf("preview leaked %q: %s", secret, value)
		}
	}
	if !strings.Contains(value, "[REDACTED]") || !strings.Contains(value, "/workspace") {
		t.Fatalf("missing safe preview fields: %s", value)
	}
}
