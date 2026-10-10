package agent

import (
	"os"
	"strings"
	"time"
)

// ModelCallActivityTimeout bounds one kernel.call_model activity execution,
// covering both the streaming attempt and the blocking fallback with its
// client-side retries. Long generations (large contexts, reasoning models,
// high max output tokens) legitimately exceed the previous hardcoded 10
// minutes, so the ceiling is configurable per deployment. Read once at
// process start; changing it between worker versions is a compatible change —
// activity timeouts are not part of the replay command identity.
var ModelCallActivityTimeout = durationFromEnv("TEMPORALITY_MODEL_ACTIVITY_TIMEOUT", 20*time.Minute)

// durationFromEnv parses a Go duration, falling back to the default when the
// variable is unset or malformed (a kernel that refuses to boot over a typo
// in an optional timeout would be worse than the default).
func durationFromEnv(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}
