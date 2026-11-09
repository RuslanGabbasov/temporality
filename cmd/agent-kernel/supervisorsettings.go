package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/kernel/harness"
	"github.com/temporality-project/temporality/observation"
	"github.com/temporality-project/temporality/workspace"
)

// Supervisor settings surface (docs/supervisor-model.md): the installation-
// wide configuration of the model that works for the administrator — judge
// evaluations, vision fallback, embeddings (reserved), and the human-attention
// estimate coefficients. Reader-gated reads; operator-gated writes with
// provider-registry validation of every model name.

// RegisterSupervisorRoutes mounts GET/PUT /v1/workspace/supervisor and the
// verdict-acknowledgement endpoint.
func RegisterSupervisorRoutes(mux *http.ServeMux, gate *controlplane.Gate, ws *workspace.Store, activities *agent.Activities) {
	mux.HandleFunc("GET /v1/workspace/supervisor", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		writeSupervisorPayload(r.Context(), w, ws, activities)
	})

	mux.HandleFunc("PUT /v1/workspace/supervisor", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleOperator) {
			return
		}
		var req struct {
			JudgeModel  string `json:"judge_model"`
			VisionModel string `json:"vision_model"`
			// Harness role models use pointer semantics: an omitted field keeps
			// the stored value (older clients predate the roles), an empty
			// string clears the role so its cascade fallback applies.
			CompactionModel *string `json:"compaction_model"`
			TitleModel      *string `json:"title_model"`
			ExtractionModel *string `json:"extraction_model"`
			HAB             struct {
				ApprovalSeconds   *int `json:"approval_seconds,omitempty"`
				AnswerBaseSeconds *int `json:"answer_base_seconds,omitempty"`
				ReadingWPM        *int `json:"reading_wpm,omitempty"`
			} `json:"hab"`
			// Harness budget overrides (docs/harness-settings.md §6): a number
			// saves an override, null resets the knob to env/default, an omitted
			// key is untouched.
			Harness map[string]*int `json:"harness"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		current, err := ws.SupervisorSettings(r.Context())
		if err != nil {
			writeError(w, 500, err)
			return
		}
		next := workspace.SupervisorSettings{
			JudgeModel:  req.JudgeModel,
			VisionModel: req.VisionModel,
			HAB:         current.HAB,
		}
		pick := func(value *string, stored string) string {
			if value == nil {
				return stored
			}
			return strings.TrimSpace(*value)
		}
		next.CompactionModel = pick(req.CompactionModel, current.CompactionModel)
		next.TitleModel = pick(req.TitleModel, current.TitleModel)
		next.ExtractionModel = pick(req.ExtractionModel, current.ExtractionModel)
		// Every non-empty model must be served by a configured provider: a
		// typo'd model would otherwise fail silently at first use.
		for name, model := range map[string]string{
			"judge_model":      next.JudgeModel,
			"vision_model":     next.VisionModel,
			"compaction_model": next.CompactionModel,
			"title_model":      next.TitleModel,
			"extraction_model": next.ExtractionModel,
		} {
			if strings.TrimSpace(model) == "" {
				continue
			}
			if _, ok, err := ws.ProviderOfModel(r.Context(), strings.TrimSpace(model)); err != nil {
				writeError(w, 500, err)
				return
			} else if !ok {
				writeError(w, 422, fmt.Errorf("%s: model %q is not listed by any configured provider", name, model))
				return
			}
		}
		// Partial update semantics: omitted HAB fields keep their stored value.
		if req.HAB.ApprovalSeconds != nil {
			next.HAB.ApprovalSeconds = *req.HAB.ApprovalSeconds
		}
		if req.HAB.AnswerBaseSeconds != nil {
			next.HAB.AnswerBaseSeconds = *req.HAB.AnswerBaseSeconds
		}
		if req.HAB.ReadingWPM != nil {
			next.HAB.ReadingWPM = *req.HAB.ReadingWPM
		}
		// Harness budget edits validate BEFORE anything is written: a bad
		// payload must change nothing.
		sets, resets, err := harnessBudgetEdits(req.Harness)
		if err != nil {
			writeError(w, 422, err)
			return
		}
		merged := map[string]int{}
		storedOverrides, err := ws.HarnessBudgetOverrides(r.Context())
		if err != nil {
			writeError(w, 500, err)
			return
		}
		for key, value := range storedOverrides {
			merged[key] = value
		}
		for key, value := range sets {
			merged[key] = value
		}
		for _, key := range resets {
			delete(merged, key)
		}
		effective := harness.ApplyBudgetOverrides(harness.FromEnv(), merged)
		if err := checkBudgetConsistency(effective.Budgets); err != nil {
			writeError(w, 422, err)
			return
		}
		if err := ws.SaveSupervisorSettings(r.Context(), next); err != nil {
			writeError(w, 500, err)
			return
		}
		saved, err := ws.SupervisorSettings(r.Context())
		if err != nil {
			writeError(w, 500, err)
			return
		}
		if len(sets) > 0 || len(resets) > 0 {
			if err := ws.SetHarnessBudgetOverrides(r.Context(), sets, resets); err != nil {
				writeError(w, 500, err)
				return
			}
		}
		// New runs pin the composed snapshot at start (docs/harness-settings.md
		// §3): in-flight runs keep theirs, saved overrides reach the next run.
		harness.SetCurrent(effective)
		resetKeys := append([]string(nil), resets...)
		sort.Strings(resetKeys)
		savedKeys := make([]string, 0, len(sets))
		for key := range sets {
			savedKeys = append(savedKeys, key)
		}
		sort.Strings(savedKeys)
		_ = ws.RecordAccessAudit(r.Context(), requestUserID(r), workspace.AuditSettingChanged, "supervisor_settings", "installation", map[string]any{
			"judge_model": saved.JudgeModel, "vision_model": saved.VisionModel,
			"compaction_model": saved.CompactionModel, "title_model": saved.TitleModel, "extraction_model": saved.ExtractionModel,
			"harness_saved": savedKeys, "harness_reset": resetKeys,
		})
		writeSupervisorPayload(r.Context(), w, ws, activities)
	})

	// Acknowledging a verdict closes it in the problem queue without touching
	// anything the verdict proposes: the human decided — applied elsewhere,
	// dismissed, or noted. The acknowledgement is journaled, so the queue can
	// hide closed rows while keeping the audit trail.
	mux.HandleFunc("POST /v1/workspace/supervisor/verdicts/acknowledge", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleOperator) {
			return
		}
		var req struct {
			EventID string `json:"event_id"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		eventID := strings.TrimSpace(req.EventID)
		if eventID == "" {
			writeError(w, 422, fmt.Errorf("event_id is required"))
			return
		}
		item := observation.Event{
			Schema:     observation.Schema,
			EventID:    fmt.Sprintf("supervisor-ack/%s/%d", eventID, time.Now().UnixNano()),
			OccurredAt: time.Now().UTC(),
			Source:     observation.Source{ID: activities.SourceID, Integration: "temporality-agent-kernel", Version: "1"},
			Context:    observation.Context{Actor: observation.Actor{ID: requestUserID(r), Type: "human"}},
			Type:       "supervisor.verdict.acknowledged",
			Data:       map[string]any{"verdict_event_id": eventID},
		}
		if err := item.Validate(); err != nil {
			writeError(w, 400, err)
			return
		}
		if err := activities.Events.Enqueue(r.Context(), item); err != nil {
			writeError(w, 500, err)
			return
		}
		_ = ws.RecordAccessAudit(r.Context(), requestUserID(r), workspace.AuditSettingChanged, "supervisor_verdict", eventID, map[string]any{"acknowledged": true})
		writeJSON(w, 200, map[string]any{"acknowledged": eventID})
	})

	// Durable ack state (docs/supervisor-model.md §7): GET returns the whole
	// closed set, POST acknowledges a batch in one round-trip — the queue's
	// bulk actions were one sequential request per row and still lost rows to
	// the event-window replay. The journal keeps an audit event per batch
	// (the ids ride the data); the workspace table is the source of truth.
	mux.HandleFunc("GET /v1/workspace/supervisor/acks", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleOperator) {
			return
		}
		ids, err := ws.ListSupervisorAcks(r.Context())
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"ids": ids})
	})
	mux.HandleFunc("POST /v1/workspace/supervisor/acks", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleOperator) {
			return
		}
		var req struct {
			VerdictEventIDs []string `json:"verdict_event_ids"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		ids := make([]string, 0, len(req.VerdictEventIDs))
		for _, id := range req.VerdictEventIDs {
			if trimmed := strings.TrimSpace(id); trimmed != "" {
				ids = append(ids, trimmed)
			}
		}
		if len(ids) == 0 {
			writeError(w, 422, fmt.Errorf("verdict_event_ids is required"))
			return
		}
		if err := ws.AckSupervisorVerdicts(r.Context(), ids, requestUserID(r)); err != nil {
			writeError(w, 500, err)
			return
		}
		// One audit event per batch (ids in data), not per id — a bulk skip of
		// two hundred rows must not flood the journal it once came from.
		audit := observation.Event{
			Schema:     observation.Schema,
			EventID:    fmt.Sprintf("supervisor-ack-batch/%d", time.Now().UnixNano()),
			OccurredAt: time.Now().UTC(),
			Source:     observation.Source{ID: activities.SourceID, Integration: "temporality-agent-kernel", Version: "1"},
			Context:    observation.Context{Actor: observation.Actor{ID: requestUserID(r), Type: "human"}},
			Type:       "supervisor.verdict.acknowledged",
			Data:       map[string]any{"verdict_event_ids": ids, "bulk": len(ids) > 1},
		}
		if audit.Validate() == nil {
			_ = activities.Events.Enqueue(r.Context(), audit)
		}
		_ = ws.RecordAccessAudit(r.Context(), requestUserID(r), workspace.AuditSettingChanged, "supervisor_verdicts", fmt.Sprintf("%d", len(ids)), map[string]any{"acknowledged": true})
		writeJSON(w, 200, map[string]any{"acknowledged": ids})
	})
}

// writeSupervisorPayload answers both GET and PUT with the same shape: the
// settings themselves, semantic-layer health, the effective harness snapshot
// (what new runs pin), the compile-time defaults and the knobs with saved
// overrides — enough for the UI to render origin chips and reset targets
// (docs/harness-settings.md §6).
func writeSupervisorPayload(ctx context.Context, w http.ResponseWriter, ws *workspace.Store, activities *agent.Activities) {
	settings, err := ws.SupervisorSettings(ctx)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	overrides, err := ws.HarnessBudgetOverrides(ctx)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	saved := make([]string, 0, len(overrides))
	for key := range overrides {
		saved = append(saved, key)
	}
	sort.Strings(saved)
	writeJSON(w, 200, map[string]any{
		"judge_model":      settings.JudgeModel,
		"vision_model":     settings.VisionModel,
		"compaction_model": settings.CompactionModel,
		"title_model":      settings.TitleModel,
		"extraction_model": settings.ExtractionModel,
		"hab":              settings.HAB,
		// Semantic-layer health rides along (docs/supervisor-model.md §6.5):
		// the operator sees degradation without digging kernel logs.
		"embeddings": activities.EmbeddingsStatus(),
		// Effective harness knobs (docs/harness-settings.md): one look shows
		// what the kernel actually runs with.
		"harness":          harness.Current(),
		"harness_defaults": harness.Defaults().Budgets,
		"harness_saved":    saved,
	})
}

// budgetCeilings bounds the editable knobs. max_model_request_bytes is
// capped at the Temporal payload error limit (2 MiB): anything bigger would
// wedge the run in TMPRL1103 retries. continue_as_new_bytes is capped at the
// history error limit (512 MiB) — the hand-off must fire before the history
// service terminates the execution. The keep-groups knobs are turn counts,
// not bytes.
var budgetCeilings = map[string]int{
	"max_model_request_bytes":    2 << 20,
	"continue_as_new_bytes":      512 << 20,
	"compaction_keep_groups_min": 64,
	"compaction_keep_groups_max": 64,
}

const budgetCeilingDefault = 64 << 20

// harnessBudgetEdits splits the PUT "harness" map into per-knob overrides to
// save and keys to reset (null = back to env/default). Unknown keys and
// out-of-range values reject the whole payload: a partial write would leave
// the budgets in a state nobody chose.
func harnessBudgetEdits(raw map[string]*int) (sets map[string]int, resets []string, err error) {
	known := make(map[string]bool, len(budgetCeilings))
	for _, key := range harness.BudgetKeys() {
		known[key] = true
	}
	for key := range raw {
		if !known[key] {
			return nil, nil, fmt.Errorf("harness.%s: unknown budget knob", key)
		}
	}
	sets = map[string]int{}
	for _, key := range harness.BudgetKeys() {
		value, ok := raw[key]
		if !ok {
			continue
		}
		if value == nil {
			resets = append(resets, key)
			continue
		}
		ceiling := budgetCeilings[key]
		if ceiling == 0 {
			ceiling = budgetCeilingDefault
		}
		if *value < 1 || *value > ceiling {
			return nil, nil, fmt.Errorf("harness.%s: value %d out of range 1..%d", key, *value, ceiling)
		}
		sets[key] = *value
	}
	return sets, resets, nil
}

// checkBudgetConsistency rejects combinations no operator wants: an inverted
// keep-groups range, and a compaction trigger above the request ceiling —
// requests between the two would fail to schedule before compaction fires.
func checkBudgetConsistency(b harness.Budgets) error {
	if b.CompactionKeepGroupsMin > b.CompactionKeepGroupsMax {
		return fmt.Errorf("harness.compaction_keep_groups_min (%d) must not exceed compaction_keep_groups_max (%d)",
			b.CompactionKeepGroupsMin, b.CompactionKeepGroupsMax)
	}
	if b.CompactionTriggerBytes > b.MaxModelRequestBytes {
		return fmt.Errorf("harness.compaction_trigger_bytes (%d) must not exceed max_model_request_bytes (%d): oversized requests would fail before compaction fires",
			b.CompactionTriggerBytes, b.MaxModelRequestBytes)
	}
	return nil
}
