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
	// unmatchedPaths tallies the paths behind the "unmatched" row — requests
	// that reached the mux but matched no route (method mismatches, stale
	// clients polling removed endpoints). Capped: a scanner must not grow
	// this map forever.
	unmatchedPaths map[string]int64
}

// maxTrackedUnmatched bounds the path map; past it new paths land nowhere
// (the counter row still counts them) until the process restarts.
const maxTrackedUnmatched = 1024

func newEndpointCounters() *endpointCounters {
	return &endpointCounters{started: time.Now(), byRoute: map[string]*routeCounter{}, unmatchedPaths: map[string]int64{}}
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

// middleware accounts requests that reach the mux. It must sit INSIDE the
// auth gate: the gate wraps requests with r.WithContext, and ServeMux sets
// Pattern on that copy — an outer middleware reads the original, whose
// Pattern stays empty, and every authorized call looks unmatched (the
// v2026.2.5 counters misattributed the whole event traffic that way).
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
		if route == "unmatched" {
			// Query string dropped: it carries cursors and filters, not identity.
			path := r.URL.Path
			if len(path) > 120 {
				path = path[:120] + "…"
			}
			if _, tracked := c.unmatchedPaths[path]; tracked || len(c.unmatchedPaths) < maxTrackedUnmatched {
				c.unmatchedPaths[path]++
			}
		}
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
	type pathMatch struct {
		Path     string `json:"path"`
		Requests int64  `json:"requests"`
	}
	top := make([]pathMatch, 0, len(c.unmatchedPaths))
	for path, requests := range c.unmatchedPaths {
		top = append(top, pathMatch{Path: path, Requests: requests})
	}
	sort.Slice(top, func(i, j int) bool { return top[i].Requests > top[j].Requests })
	if len(top) > 10 {
		top = top[:10]
	}
	return map[string]any{"uptime_s": time.Since(c.started).Seconds(), "routes": rows, "unmatched_top": top}
}

// gateMiddleware sits OUTSIDE the auth gate and accounts only what the gate
// rejects (401/403) — everything else is already counted inside. A storm of
// rejected tokens must stay visible without polluting the route rows.
func (c *endpointCounters) gateMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		if rec.status != http.StatusUnauthorized && rec.status != http.StatusForbidden {
			return
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		counter := c.byRoute["gate-rejected"]
		if counter == nil {
			counter = &routeCounter{}
			c.byRoute["gate-rejected"] = counter
		}
		counter.Requests++
		counter.Errors4x++
	})
}

// writeCounters renders the snapshot for the debug listener.
func (c *endpointCounters) writeCounters(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(c.snapshot())
}
