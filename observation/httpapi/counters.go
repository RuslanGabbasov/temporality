package httpapi

// Per-route request accounting for load attribution (docs/resilience-and-
// scale.md §НТ): a campaign melts the journal without ever showing WHICH
// route did it — CPU profiles come later, these counters come first. They
// ride the main handler chain and are exposed as JSON on the debug listener
// (JOURNAL_DEBUG_ADDR, off by default) next to net/http/pprof.

import (
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"time"
)

// routeCounter is one route's running totals. Durations are wall-clock
// handler time; MaxNS is the slowest single request in the window.
type routeCounter struct {
	Requests int64 `json:"requests"`
	Errors4x int64 `json:"errors_4xx"`
	Errors5x int64 `json:"errors_5xx"`
	TotalNS  int64 `json:"total_ns"`
	MaxNS    int64 `json:"max_ns"`
}

// endpointCounters totals requests per route pattern. Keyed by the Go 1.22
// mux pattern ("GET /v1/observations/events"), so unmatched requests land on
// "unmatched" — a flood of those is itself a finding.
type endpointCounters struct {
	mu      sync.Mutex
	started time.Time
	byRoute map[string]*routeCounter
}

func newEndpointCounters() *endpointCounters {
	return &endpointCounters{started: time.Now(), byRoute: map[string]*routeCounter{}}
}

// statusRecorder captures the response code the handler wrote. Header-only
// replies (401 gates) and bodyless writes both land here.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (c *endpointCounters) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		elapsed := time.Since(start)
		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		counter := c.byRoute[route]
		if counter == nil {
			counter = &routeCounter{}
			c.byRoute[route] = counter
		}
		counter.Requests++
		switch {
		case rec.status >= 500:
			counter.Errors5x++
		case rec.status >= 400:
			counter.Errors4x++
		}
		counter.TotalNS += int64(elapsed)
		if int64(elapsed) > counter.MaxNS {
			counter.MaxNS = int64(elapsed)
		}
	})
}

// routeReport is one row of the snapshot, totals plus derived load numbers a
// human scans directly: mean_ms and the share of the window's total time.
type routeReport struct {
	Route    string  `json:"route"`
	Requests int64   `json:"requests"`
	Errors4x int64   `json:"errors_4xx"`
	Errors5x int64   `json:"errors_5xx"`
	MeanMS   float64 `json:"mean_ms"`
	MaxMS    float64 `json:"max_ms"`
	TotalS   float64 `json:"total_s"`
}

// snapshot returns the counters sorted by cumulative handler time — the top
// row is the current prime suspect, not the noisiest route.
func (c *endpointCounters) snapshot() map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	rows := make([]routeReport, 0, len(c.byRoute))
	for route, counter := range c.byRoute {
		mean := 0.0
		if counter.Requests > 0 {
			mean = float64(counter.TotalNS) / float64(counter.Requests) / 1e6
		}
		rows = append(rows, routeReport{
			Route:    route,
			Requests: counter.Requests,
			Errors4x: counter.Errors4x,
			Errors5x: counter.Errors5x,
			MeanMS:   mean,
			MaxMS:    float64(counter.MaxNS) / 1e6,
			TotalS:   float64(counter.TotalNS) / 1e9,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].TotalS > rows[j].TotalS })
	return map[string]any{"uptime_s": time.Since(c.started).Seconds(), "routes": rows}
}

// writeCounters renders the snapshot for the debug listener.
func (c *endpointCounters) writeCounters(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(c.snapshot())
}
