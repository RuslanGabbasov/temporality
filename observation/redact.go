package observation

import (
	"regexp"
)

// Secret patterns recognized in event payloads on journal ingest — the second
// line of defense after boundary resolution (docs/secrets-management.md §4.3).
// Only well-known, high-confidence token shapes are rewritten: a broad
// "looks secretish" heuristic would corrupt legitimate payloads.
var secretPatterns = []struct {
	kind   string
	regexp *regexp.Regexp
}{
	{"github_token", regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{20,}`)},
	{"github_pat", regexp.MustCompile(`github_pat_[A-Za-z0-9_]{20,}`)},
	{"aws_access_key", regexp.MustCompile(`AKIA[0-9A-Z]{16}`)},
	{"slack_token", regexp.MustCompile(`xox[baprs]-[A-Za-z0-9-]{10,}`)},
	{"openai_key", regexp.MustCompile(`sk-[A-Za-z0-9_-]{20,}`)},
	{"anthropic_key", regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{20,}`)},
	{"google_api_key", regexp.MustCompile(`AIza[0-9A-Za-z_-]{30,}`)},
	{"private_key_block", regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)},
	{"bearer_header", regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._~+/-]{20,}={0,2}`)},
}

// RedactString rewrites every recognized secret-looking token in s with
// <redacted:kind>. It is deterministic: the same input always yields the same
// output, which keeps idempotent re-ingestion stable.
func RedactString(s string) string {
	for _, pattern := range secretPatterns {
		if pattern.regexp.MatchString(s) {
			s = pattern.regexp.ReplaceAllString(s, "<redacted:"+pattern.kind+">")
		}
	}
	return s
}

// RedactEvent rewrites secret-looking values inside an event's Data, in string
// values and stringified JSON nested inside them. Keys are left intact so the
// payload structure stays inspectable.
func RedactEvent(event *Event) {
	for key, value := range event.Data {
		event.Data[key] = redactValue(value)
	}
}

func redactValue(value any) any {
	switch typed := value.(type) {
	case string:
		return RedactString(typed)
	case map[string]any:
		for key, inner := range typed {
			typed[key] = redactValue(inner)
		}
		return typed
	case []any:
		for i, inner := range typed {
			typed[i] = redactValue(inner)
		}
		return typed
	default:
		return value
	}
}

// HasSecrets reports whether a string carries any recognized secret pattern —
// used by tests and by emit-side callers that want to know before sending.
func HasSecrets(s string) bool {
	for _, pattern := range secretPatterns {
		if pattern.regexp.MatchString(s) {
			return true
		}
	}
	return false
}
