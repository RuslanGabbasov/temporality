package agent

import (
	"fmt"
	"strings"
)

// approvalPreview exposes only bounded, display-safe command metadata to the
// human approval UI. Tool arguments remain private to the workflow/activity.
func approvalPreview(tool string, args map[string]any) map[string]any {
	if tool != "run_command" {
		return nil
	}
	command, _ := args["command"].([]any)
	argv := make([]string, 0, len(command))
	redactNext := false
	for _, raw := range command {
		value, ok := raw.(string)
		if !ok {
			continue
		}
		if len(value) > 240 {
			value = value[:240] + "…"
		}
		lower := strings.ToLower(value)
		if redactNext {
			value = "[REDACTED]"
			redactNext = false
		} else if sensitiveFlag(lower) {
			if at := strings.IndexAny(value, "=:"); at >= 0 {
				value = value[:at+1] + "[REDACTED]"
			} else {
				redactNext = true
			}
		} else if strings.HasPrefix(lower, "bearer ") {
			value = "Bearer [REDACTED]"
		} else if strings.Contains(lower, "://") && strings.Contains(lower[strings.Index(lower, "://")+3:], "@") {
			start := strings.Index(value, "://") + 3
			end := strings.Index(value[start:], "@") + start
			value = value[:start] + "[REDACTED]" + value[end:]
		} else if sensitiveAssignment(lower) {
			if at := strings.IndexAny(value, "=:"); at >= 0 {
				value = value[:at+1] + "[REDACTED]"
			}
		}
		if len(argv) < 32 {
			argv = append(argv, value)
		}
	}
	if redactNext {
		argv = append(argv, "[REDACTED]")
	}
	preview := map[string]any{"tool": tool, "argv": argv, "workspace": "/workspace", "workspace_read_only": false}
	if timeout, ok := args["timeout_sec"]; ok {
		preview["timeout_sec"] = fmt.Sprint(timeout)
	}
	return preview
}

func sensitiveFlag(value string) bool {
	if !strings.HasPrefix(value, "-") {
		return false
	}
	name := strings.TrimLeft(value, "-")
	if at := strings.IndexAny(name, "=:"); at >= 0 {
		name = name[:at]
	}
	return sensitiveName(name)
}

func sensitiveAssignment(value string) bool {
	at := strings.IndexAny(value, "=:")
	return at > 0 && sensitiveName(strings.TrimLeft(value[:at], "-"))
}

func sensitiveName(value string) bool {
	for _, marker := range []string{"token", "password", "passwd", "secret", "api_key", "api-key", "authorization", "credential", "private_key"} {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}
