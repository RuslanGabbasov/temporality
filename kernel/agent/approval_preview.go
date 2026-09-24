package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	approvalDisplayMaxBytes = 4096
	approvalValueMaxBytes   = 256
	approvalCollectionLimit = 32
)

func approvalText(value string) string {
	var redacted bool
	clean := redactApprovalText(value, &redacted)
	if len(clean) > approvalValueMaxBytes {
		clean = clean[:approvalValueMaxBytes] + "…"
	}
	return clean
}

// approvalOperation keeps the canonical argument identity separate from the
// bounded display projection. The canonical JSON is hashed but never emitted.
func approvalOperation(operationID, tool string, args map[string]any, readOnly bool) (map[string]any, map[string]any) {
	canonical, _ := json.Marshal(args) // model tool arguments are JSON values.
	digest := sha256.Sum256(canonical)
	redacted, truncated := false, false
	display := boundedApprovalValue(args, 0, &redacted, &truncated)
	encoded, _ := json.Marshal(display)
	if len(encoded) > approvalDisplayMaxBytes {
		display = map[string]any{"notice": "arguments omitted: display limit exceeded"}
		truncated = true
	}

	operationType, risk := "tool.call", "medium"
	switch {
	case tool == "run_command":
		operationType, risk = "sandbox.exec", "high"
	case strings.HasPrefix(tool, "mcp__"):
		operationType, risk = "mcp.call", "high"
	}
	summary := "Call tool " + tool
	if tool == "run_command" {
		if displayedArgs, ok := display.(map[string]any); ok {
			if argv, ok := displayedArgs["command"].([]any); ok {
				parts := make([]string, 0, len(argv))
				for _, value := range argv {
					if text, ok := value.(string); ok {
						parts = append(parts, text)
					}
				}
				summary = "Run command: " + strings.Join(parts, " ")
			}
		}
	}
	if len(summary) > approvalValueMaxBytes {
		summary = summary[:approvalValueMaxBytes] + "…"
		truncated = true
	}
	operation := map[string]any{
		"id": operationID, "type": operationType, "summary": summary,
		"arguments": display, "arguments_hash": "sha256:" + hex.EncodeToString(digest[:]),
	}
	details := map[string]any{"tool": tool, "workspace_read_only": readOnly}
	if tool == "run_command" {
		details["workspace"] = "/workspace"
		if timeout, ok := args["timeout_sec"]; ok {
			details["timeout_sec"] = fmt.Sprint(timeout)
		}
	}
	return map[string]any{
		"operation": operation,
		"risk":      map[string]any{"level": risk},
		"redaction": map[string]any{"applied": redacted, "truncated": truncated, "display_limit_bytes": approvalDisplayMaxBytes},
	}, details
}

func operationArgumentsHash(args map[string]any) string {
	canonical, _ := json.Marshal(args)
	digest := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func boundedApprovalValue(value any, depth int, redacted, truncated *bool) any {
	if depth >= 6 {
		*truncated = true
		return "[TRUNCATED]"
	}
	switch value := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		// json.Marshal sorts map keys; sort here too so truncation is stable.
		sortStrings(keys)
		result := make(map[string]any, min(len(keys), approvalCollectionLimit))
		for index, key := range keys {
			if index >= approvalCollectionLimit {
				*truncated = true
				break
			}
			if sensitiveName(strings.ToLower(key)) {
				result[key] = "[REDACTED]"
				*redacted = true
				continue
			}
			result[key] = boundedApprovalValue(value[key], depth+1, redacted, truncated)
		}
		return result
	case []any:
		limit := min(len(value), approvalCollectionLimit)
		result := make([]any, 0, limit)
		redactNext := false
		for _, item := range value[:limit] {
			if text, ok := item.(string); ok {
				lower := strings.ToLower(text)
				if redactNext {
					result = append(result, "[REDACTED]")
					redactNext = false
					*redacted = true
					continue
				}
				if sensitiveFlag(lower) {
					if index := strings.IndexAny(text, "=:"); index >= 0 {
						result = append(result, text[:index+1]+"[REDACTED]")
						*redacted = true
					} else {
						result = append(result, text)
						redactNext = true
					}
					continue
				}
			}
			result = append(result, boundedApprovalValue(item, depth+1, redacted, truncated))
		}
		if len(value) > limit {
			*truncated = true
		}
		if redactNext {
			result = append(result, "[REDACTED]")
			*redacted = true
		}
		return result
	case string:
		clean := redactApprovalText(value, redacted)
		if len(clean) > approvalValueMaxBytes {
			*truncated = true
			clean = clean[:approvalValueMaxBytes] + "…"
		}
		return clean
	default:
		return value
	}
}

func redactApprovalText(value string, redacted *bool) string {
	lower := strings.ToLower(value)
	if strings.HasPrefix(lower, "bearer ") {
		*redacted = true
		return "Bearer [REDACTED]"
	}
	if scheme := strings.Index(lower, "://"); scheme >= 0 {
		start := scheme + 3
		if at := strings.Index(value[start:], "@"); at >= 0 {
			*redacted = true
			value = value[:start] + "[REDACTED]" + value[start+at:]
			lower = strings.ToLower(value)
		}
	}
	for _, key := range []string{"authorization:", "token=", "password=", "passwd=", "secret=", "api_key=", "api-key=", "credential="} {
		if at := strings.Index(lower, key); at >= 0 {
			*redacted = true
			start := at + len(key)
			end := start
			if start < len(value) && (value[start] == '\'' || value[start] == '"') {
				quote := value[start]
				end = start + 1
				for end < len(value) && value[end] != quote {
					end++
				}
				if end < len(value) {
					end++
				}
			} else {
				end = start
				for end < len(value) && !strings.ContainsRune(" \t;|&,", rune(value[end])) {
					end++
				}
			}
			return value[:start] + "[REDACTED]" + value[end:]
		}
	}
	if at := strings.IndexAny(value, "=:"); at > 0 && sensitiveName(strings.TrimLeft(strings.ToLower(value[:at]), "-")) {
		*redacted = true
		return value[:at+1] + "[REDACTED]"
	}
	return value
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

func sensitiveName(value string) bool {
	for _, marker := range []string{"token", "password", "passwd", "secret", "api_key", "api-key", "authorization", "credential", "private_key", "private-key"} {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
