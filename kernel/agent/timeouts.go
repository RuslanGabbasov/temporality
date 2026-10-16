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

// ToolActivityTimeout bounds one kernel.run_tool activity execution. It must
// exceed the sandbox command ceiling (kernel/sandbox maxTimeout = 15 min)
// with head room for container teardown, or every long command — e2e suites,
// builds — dies as a StartToClose timeout while the sandbox is still happily
// running (the mismatch happened: activity 4m < sandbox 10m). Configurable
// per deployment like the model timeout; read once at process start.
var ToolActivityTimeout = durationFromEnv("TEMPORALITY_TOOL_ACTIVITY_TIMEOUT", 16*time.Minute)

// modelHeartbeatInterval is how often kernel.call_model heartbeats while a
// provider call is in flight. Temporal delivers activity cancellation only
// through heartbeat responses, so this interval is the worst-case latency
// between a kernel.nudge signal and the actual abortion of the HTTP request;
// without heartbeats the cancelled call streams on as a zombie until its own
// watchdog or StartToClose timeout kills it. No HeartbeatTimeout is set on
// the activity options, so a missed beat cannot fail a healthy call.
var modelHeartbeatInterval = durationFromEnv("TEMPORALITY_MODEL_HEARTBEAT_INTERVAL", 10*time.Second)

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
