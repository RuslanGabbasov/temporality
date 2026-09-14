package httpapi

import (
	"fmt"
	"net/http"
	"sync/atomic"
)

type metrics struct {
	httpRequests        atomic.Uint64
	eventsAppended      atomic.Uint64
	appendErrors        atomic.Uint64
	replays             atomic.Uint64
	replayErrors        atomic.Uint64
	replayDurationNanos atomic.Uint64
}

func (m *metrics) handler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("content-type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = fmt.Fprintf(w, `# TYPE temporality_http_requests_total counter
temporality_http_requests_total %d
# TYPE temporality_events_appended_total counter
temporality_events_appended_total %d
# TYPE temporality_event_append_errors_total counter
temporality_event_append_errors_total %d
# TYPE temporality_replays_total counter
temporality_replays_total %d
# TYPE temporality_replay_errors_total counter
temporality_replay_errors_total %d
# TYPE temporality_replay_duration_seconds_total counter
temporality_replay_duration_seconds_total %.9f
`, m.httpRequests.Load(), m.eventsAppended.Load(), m.appendErrors.Load(), m.replays.Load(), m.replayErrors.Load(), float64(m.replayDurationNanos.Load())/1e9)
}

func (m *metrics) count(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { m.httpRequests.Add(1); next.ServeHTTP(w, r) })
}
