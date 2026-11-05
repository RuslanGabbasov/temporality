package main

// Installation judge model configuration. The eval judge's calibration and
// gate coupling were removed with the quality loop (docs/agent-evolution.md
// §8); what stays is the model setting itself — the knowledge contours
// (conflict judging, area summaries) resolve their judge model through it,
// and the onboarding writes it here.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/observation"
	"github.com/temporality-project/temporality/workspace"
)

// judgeOutbox is the durable-outbox surface the config event needs;
// *outbox.Outbox satisfies it and tests provide fakes.
type judgeOutbox interface {
	Enqueue(context.Context, observation.Event) error
}

// judgeStore is the storage surface the judge endpoints need;
// *workspace.Store satisfies it and tests provide fakes.
type judgeStore interface {
	JudgeConfig(ctx context.Context) (workspace.JudgeConfig, error)
}

// judgeSettingsStore is the writable part the PUT needs; only the real
// store implements it (judge tests simply do not register the PUT route).
type judgeSettingsStore interface {
	ProviderOfModel(ctx context.Context, model string) (string, bool, error)
	SetSetting(ctx context.Context, key, value string) error
}

// RegisterJudgeRoutes mounts the judge model configuration endpoints.
func RegisterJudgeRoutes(mux *http.ServeMux, gate *controlplane.Gate, ws judgeStore, events judgeOutbox, log *slog.Logger) {
	// The judge model resolution every judge-using contour reads before
	// scoring; configured=false turns judge mode off.
	mux.HandleFunc("GET /v1/workspace/evaluation-judge", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		config, err := ws.JudgeConfig(r.Context())
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, config)
	})

	// The judge model as an installation setting (operator-only): an empty
	// model turns the judge off and must override a non-empty env default —
	// an operator who disabled the judge in the UI owns that choice. A
	// non-empty model must resolve to a configured provider's catalog.
	if settings, ok := ws.(judgeSettingsStore); ok {
		mux.HandleFunc("PUT /v1/workspace/evaluation-judge", func(w http.ResponseWriter, r *http.Request) {
			if !gate.Allow(w, r, controlplane.RoleOperator) {
				return
			}
			var req struct {
				Model string `json:"model"`
			}
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
				writeError(w, 400, err)
				return
			}
			model := strings.TrimSpace(req.Model)
			if model != "" {
				if _, ok, err := settings.ProviderOfModel(r.Context(), model); err != nil {
					writeError(w, 500, err)
					return
				} else if !ok {
					writeError(w, 422, fmt.Errorf("model %q is not listed by any configured provider", model))
					return
				}
			}
			if err := settings.SetSetting(r.Context(), workspace.SettingJudgeModel, model); err != nil {
				writeError(w, 500, err)
				return
			}
			config, err := ws.JudgeConfig(r.Context())
			if err != nil {
				writeError(w, 500, err)
				return
			}
			emitJudgeConfigured(r.Context(), events, config, requestSubject(r))
			writeJSON(w, 200, config)
		})
	}
}

// emitJudgeConfigured records agent.evaluation.judge.configured — kept for
// the audit trail of installation-level model choices.
func emitJudgeConfigured(ctx context.Context, events judgeOutbox, config workspace.JudgeConfig, subject string) {
	if events == nil {
		return
	}
	item := observation.Event{
		Schema:     observation.Schema,
		EventID:    fmt.Sprintf("judge-config/%d", time.Now().UnixNano()),
		OccurredAt: time.Now().UTC(),
		Source:     observation.Source{ID: "workspace", Integration: "agent-kernel", Version: "1"},
		Context:    observation.Context{Actor: observation.Actor{ID: subject, Type: "human"}},
		Type:       "agent.evaluation.judge.configured",
		Data:       map[string]any{"model": config.Model, "provider_id": config.ProviderID, "configured": config.Configured, "source": config.Source},
	}
	if err := events.Enqueue(ctx, item); err != nil {
		slog.Warn("emit judge configured", "error", err)
	}
}
