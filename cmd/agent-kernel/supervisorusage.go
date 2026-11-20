package main

// Supervisor model spend (docs/supervisor-model.md §7): the judge/supervisor
// background passes call the model OUTSIDE any run — no workspace_run row,
// no model.completed event — so their tokens were invisible until this
// aggregate. Every supervisor client is wrapped with a usage hook
// (llm.SetUsageHook) that journals supervisor.model.completed; this file
// folds those events into the dashboard's «Расход модели супервизора» card.

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/kernel/cost"
	"github.com/temporality-project/temporality/kernel/llm"
	"github.com/temporality-project/temporality/observation"
)

// supervisorUsageEventType is deliberately NOT model.completed: the health
// folds, the org usage sweep and the run trajectory all read model.completed
// with run-scoped semantics, and a runless event would pollute them.
const supervisorUsageEventType = "supervisor.model.completed"

// withSupervisorPurpose tags the call chain with what the model is doing
// (triage, insights, …). Values survive context.WithTimeout/WithValue down
// to llm.Client.Complete, where the usage hook reads it back.
func withSupervisorPurpose(ctx context.Context, purpose string) context.Context {
	return context.WithValue(ctx, supervisorPurposeKey{}, purpose)
}

type supervisorPurposeKey struct{}

func supervisorPurposeOf(ctx context.Context) string {
	if purpose, ok := ctx.Value(supervisorPurposeKey{}).(string); ok && purpose != "" {
		return purpose
	}
	return "other"
}

// journalSupervisorUsage writes one usage event through the durable outbox.
// Best-effort by design: a journal hiccup must not fail the model call that
// already succeeded — the caller logs and moves on.
func journalSupervisorUsage(ctx context.Context, outbox func(context.Context, observation.Event) error, event llm.UsageEvent, purpose string) error {
	if outbox == nil {
		return nil
	}
	return outbox(ctx, observation.Event{
		Schema:     observation.Schema,
		EventID:    fmt.Sprintf("supervisor-usage/%d", time.Now().UnixNano()),
		OccurredAt: time.Now().UTC(),
		Source:     observation.Source{ID: "workspace", Integration: "agent-kernel", Version: "1"},
		Context: observation.Context{
			Actor: observation.Actor{ID: "supervisor", Type: "system"},
		},
		Type: supervisorUsageEventType,
		Data: map[string]any{
			"model":             event.Model,
			"provider":          event.Provider,
			"purpose":           purpose,
			"prompt_tokens":     event.Usage.PromptTokens,
			"cached_tokens":     event.Usage.CachedTokens,
			"completion_tokens": event.Usage.CompletionTokens,
			"total_tokens":      event.Usage.TotalTokens,
			"latency_ms":        event.LatencyMs,
		},
	})
}

// SupervisorUsage is the aggregate the dashboard's supervisor tab renders.
type SupervisorUsage struct {
	Window           string                `json:"window"`
	From             *time.Time            `json:"from,omitempty"`
	Calls            int                   `json:"calls"`
	PromptTokens     int64                 `json:"prompt_tokens"`
	CompletionTokens int64                 `json:"completion_tokens"`
	CachedTokens     int64                 `json:"cached_tokens"`
	Tokens           int64                 `json:"tokens"`
	CostUSD          float64               `json:"cost_usd"`
	ByPurpose        []SupervisorUsageSlice `json:"by_purpose"`
	ByModel          []SupervisorUsageSlice `json:"by_model"`
}

// SupervisorUsageSlice is one row of a breakdown (purpose or model).
type SupervisorUsageSlice struct {
	Key     string  `json:"key"`
	Calls   int     `json:"calls"`
	Tokens  int64   `json:"tokens"`
	CostUSD float64 `json:"cost_usd"`
}

// foldSupervisorUsage prices the window's supervisor.model.completed events.
// Pure — the unit tests drive it directly.
func foldSupervisorUsage(events []observation.Event, prices cost.Config) SupervisorUsage {
	usage := SupervisorUsage{
		ByPurpose: []SupervisorUsageSlice{},
		ByModel:   []SupervisorUsageSlice{},
	}
	purposes := map[string]*SupervisorUsageSlice{}
	models := map[string]*SupervisorUsageSlice{}
	accumulate := func(index map[string]*SupervisorUsageSlice, key string, tokens int64, usd float64) {
		if key == "" {
			key = "unknown"
		}
		row, ok := index[key]
		if !ok {
			row = &SupervisorUsageSlice{Key: key}
			index[key] = row
		}
		row.Calls++
		row.Tokens += tokens
		row.CostUSD += usd
	}
	for i := range events {
		event := &events[i]
		if event.Type != supervisorUsageEventType {
			continue
		}
		promptTokens := usageInt(event.Data["prompt_tokens"])
		cached := usageInt(event.Data["cached_tokens"])
		completion := usageInt(event.Data["completion_tokens"])
		model, _ := event.Data["model"].(string)
		purpose, _ := event.Data["purpose"].(string)
		if purpose == "" {
			purpose = "other"
		}
		tokens := promptTokens + completion
		usd := prices.ModelCost(model, int(promptTokens), int(cached), int(completion))
		usage.Calls++
		usage.PromptTokens += promptTokens
		usage.CompletionTokens += completion
		usage.CachedTokens += cached
		usage.Tokens += tokens
		usage.CostUSD += usd
		accumulate(purposes, purpose, tokens, usd)
		accumulate(models, model, tokens, usd)
	}
	flatten := func(index map[string]*SupervisorUsageSlice) []SupervisorUsageSlice {
		rows := make([]SupervisorUsageSlice, 0, len(index))
		for _, row := range index {
			rows = append(rows, *row)
		}
		sort.Slice(rows, func(i, j int) bool {
			if rows[i].Tokens != rows[j].Tokens {
				return rows[i].Tokens > rows[j].Tokens
			}
			return rows[i].Key < rows[j].Key
		})
		return rows
	}
	usage.ByPurpose = flatten(purposes)
	usage.ByModel = flatten(models)
	return usage
}

// registerSupervisorUsageRoutes serves GET /v1/workspace/supervisor/usage:
// the token/cost aggregate of the supervisor model over one window, plus the
// per-purpose and per-model breakdowns that answer «куда уходят токены».
func registerSupervisorUsageRoutes(mux *http.ServeMux, gate *controlplane.Gate, observationURL, journalToken string, prices cost.Config) {
	mux.HandleFunc("GET /v1/workspace/supervisor/usage", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		windowKey, from, err := userStatsFrom(r.URL.Query().Get("window"))
		if err != nil {
			writeError(w, 400, err)
			return
		}
		// Bounded walk: supervisor passes are throttled by design (caps and
		// cadences), so 40 pages of 500 is far beyond any real window.
		events, err := userStatsJournalPages(r.Context(), observationURL, journalToken, supervisorUsageEventType, from, "", 40)
		if err != nil {
			writeError(w, 502, fmt.Errorf("journal query: %w", err))
			return
		}
		usage := foldSupervisorUsage(events, prices)
		usage.Window = windowKey
		if !from.IsZero() {
			usage.From = &from
		}
		writeJSON(w, 200, usage)
	})
}
