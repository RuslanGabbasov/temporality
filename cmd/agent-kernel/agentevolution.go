package main

// Agent evolution v2 (docs/agent-evolution.md): proposals from users and
// agents, one-click apply without gates, per-version agentSRE metrics under
// observation, one-click rollback and optional control examples. The eval
// gate of docs/agent-quality-loop.md is gone: a version proves itself on real
// work (the observation window), not on a pre-apply ritual. The diff between
// definition snapshots stays — transparency is the one thing the gate era got
// right.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/kernel/llm"
	"github.com/temporality-project/temporality/observation"
	"github.com/temporality-project/temporality/workspace"
)

// evolutionStore is the storage surface the evolution routes need;
// *workspace.Store satisfies it, tests provide fakes.
type evolutionStore interface {
	GetAgent(ctx context.Context, id string) (workspace.Agent, error)
	ListAgentVersions(ctx context.Context, agentID string) ([]workspace.AgentVersion, error)
	GetAgentVersion(ctx context.Context, agentID string, version int) (workspace.AgentVersion, error)
	AgentVersionsDiff(ctx context.Context, agentID string, from, to int) (workspace.VersionDiff, error)
	InsertAgentVersion(ctx context.Context, v workspace.AgentVersion) error
	RollbackAgentVersion(ctx context.Context, agentID string, toVersion int, author string) (workspace.AgentVersion, error)
	CreateAgentProposal(ctx context.Context, p *workspace.AgentProposal) error
	ListAgentProposals(ctx context.Context, agentID, status string, limit int) ([]workspace.AgentProposal, error)
	GetAgentProposal(ctx context.Context, id int64) (workspace.AgentProposal, error)
	ApplyAgentProposal(ctx context.Context, next workspace.Agent, proposalID int64, decidedBy string, snapshot *workspace.AgentVersion) (workspace.AgentProposal, error)
	DecideAgentProposal(ctx context.Context, id int64, status, decidedBy string) (workspace.AgentProposal, error)
	ListAgentVersionMetrics(ctx context.Context, agentID string, version int, since time.Time) ([]workspace.AgentVersionMetrics, error)
	AgentObservation(ctx context.Context, agentID string) (workspace.ObservationConfig, error)
	SetAgentObservation(ctx context.Context, agentID string, cfg workspace.ObservationConfig) error
	ListAgentCheckCases(ctx context.Context, agentID string) ([]workspace.AgentCheckCase, error)
	AddAgentCheckCase(ctx context.Context, c *workspace.AgentCheckCase) error
	DeleteAgentCheckCase(ctx context.Context, agentID string, id int64) error
}

// RegisterAgentEvolutionRoutes mounts the evolution v2 endpoints.
// modelFor resolves the drafting/smoke model per request (the wizard chain,
// writing its own error response when no model is available);
// observationURL feeds the journal events of version lifecycle.
func RegisterAgentEvolutionRoutes(mux *http.ServeMux, gate *controlplane.Gate, ws evolutionStore, modelFor func(http.ResponseWriter, *http.Request) *llm.Client, visibleUnits func(*http.Request) []string, observationURL string, journalToken func() string, log *slog.Logger) {
	// resolveAgent is the shared agent lookup + org visibility gate.
	resolveAgent := func(w http.ResponseWriter, r *http.Request) (workspace.Agent, bool) {
		a, err := ws.GetAgent(r.Context(), r.PathValue("agentID"))
		if err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, errors.New("agent not found"))
			} else {
				writeError(w, 500, err)
			}
			return workspace.Agent{}, false
		}
		if !workspace.OrgVisible(a.OrgUnitID, visibleUnits(r)) {
			writeError(w, 404, workspace.ErrNotFound)
			return workspace.Agent{}, false
		}
		return a, true
	}

	// GET /versions/diff: structural definition changes plus the compiled
	// prompt line diff of two snapshots (moved from the retired quality.go).
	mux.HandleFunc("GET /v1/workspace/agents/{agentID}/versions/diff", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		from, to, ok := qualityVersionRange(w, r)
		if !ok {
			return
		}
		if _, ok := resolveAgent(w, r); !ok {
			return
		}
		diff, err := ws.AgentVersionsDiff(r.Context(), r.PathValue("agentID"), from, to)
		if err != nil {
			if errors.Is(err, workspace.ErrVersionSnapshotMissing) {
				writeError(w, 422, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, diff)
	})

	// GET /proposals/{id}/diff: what applying this proposal changes — the
	// same shape as versions/diff plus the behavior summary, so the review
	// modal can answer «что в итоге поменяется» before the click.
	mux.HandleFunc("GET /v1/workspace/agents/{agentID}/proposals/{id}/diff", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		proposalID, ok := parseProposalID(w, r)
		if !ok {
			return
		}
		a, ok := resolveAgent(w, r)
		if !ok {
			return
		}
		proposal, err := ws.GetAgentProposal(r.Context(), proposalID)
		if err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		if proposal.AgentID != a.ID {
			writeError(w, 404, workspace.ErrNotFound)
			return
		}
		diff, err := proposalDiff(r.Context(), ws, a, proposal)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{
			"from":               diff.From,
			"to":                 diff.To,
			"definition_changes": diff.DefinitionChanges,
			"prompt_diff":        diff.PromptDiff,
			"change_summary":     proposal.ChangeSummary,
		})
	})

	// POST /proposals/from-text: the user describes a problem in free text,
	// the model drafts the definition change (constraints/completion — never
	// capabilities or purpose), the proposal lands as a pending draft with a
	// behavior summary for review. One click to apply follows.
	mux.HandleFunc("POST /v1/workspace/agents/{agentID}/proposals/from-text", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		var req struct {
			Problem string          `json:"problem"`
			Context json.RawMessage `json:"context"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		if strings.TrimSpace(req.Problem) == "" {
			writeError(w, 422, errors.New("problem is required"))
			return
		}
		a, ok := resolveAgent(w, r)
		if !ok {
			return
		}
		model := modelFor(w, r)
		if model == nil {
			return // modelFor already answered with the resolution error
		}
		draft, err := draftAgentProposal(r.Context(), model, a, req.Problem)
		if err != nil {
			writeError(w, 502, fmt.Errorf("draft proposal: %w", err))
			return
		}
		definition := workspace.AgentDefinition{}
		if a.Definition != nil {
			definition = *a.Definition
		}
		if draft.Constraints != nil {
			definition.Constraints = draft.Constraints
		}
		if draft.Completion != nil {
			definition.Completion = draft.Completion
		}
		problem := strings.TrimSpace(draft.Problem)
		if problem == "" {
			problem = strings.TrimSpace(req.Problem)
		}
		proposal := workspace.AgentProposal{
			AgentID: a.ID, BaseVersion: a.DefinitionVersion,
			Definition:     definition,
			Description:    a.Description,
			Problem:        problem,
			Change:         draft.Change,
			Effect:         draft.Effect,
			ChangeSummary:  draft.ChangeSummary,
			Source:         "user",
			Context:        req.Context,
			Author:         requestSubject(r),
			GeneratorModel: draft.Model,
		}
		if proposal.ChangeSummary == "" {
			proposal.ChangeSummary = summarizeChange(problem, draft.Change)
		}
		if err := ws.CreateAgentProposal(r.Context(), &proposal); err != nil {
			writeError(w, 500, err)
			return
		}
		emitAgentDefinitionEvent(r.Context(), observationURL, journalToken(), "agent.definition.proposed", a, proposal, actorFromRequest(r), map[string]any{"source": "user"})
		writeJSON(w, 201, map[string]any{
			"proposal_id":    proposal.ID,
			"change_summary": proposal.ChangeSummary,
			"definition":     proposal.Definition,
			"base_version":   proposal.BaseVersion,
		})
	})

	// POST /proposals/{id}/apply: one click, no gate (docs/agent-evolution.md
	// §1). The transactional apply still claims the proposal, bumps the
	// version and lands the immutable snapshot atomically — what changed is
	// only that nothing may block it. The snapshot records the lineage
	// (kind=proposal, baseline, change summary) the observation window then
	// measures against.
	mux.HandleFunc("POST /v1/workspace/agents/{agentID}/proposals/{id}/apply", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		proposalID, ok := parseProposalID(w, r)
		if !ok {
			return
		}
		a, ok := resolveAgent(w, r)
		if !ok {
			return
		}
		proposal, err := ws.GetAgentProposal(r.Context(), proposalID)
		if err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		if proposal.AgentID != a.ID {
			writeError(w, 404, workspace.ErrNotFound)
			return
		}
		next := a
		next.Description = proposal.Description
		definition := proposal.Definition
		next.Definition = &definition
		// Bindings never change through a proposal (mirror of the PUT freeze).
		next.OrgUnitID = a.OrgUnitID
		if agentSemanticsChanged(a, next) {
			next.DefinitionVersion++
		}
		var snapshot *workspace.AgentVersion
		if next.Definition != nil && next.DefinitionVersion != a.DefinitionVersion {
			version := agentVersionSnapshot(next, requestSubject(r), "proposal", proposal.GeneratorModel)
			version.ChangeSummary = proposal.ChangeSummary
			snapshot = &version
		}
		decided, err := ws.ApplyAgentProposal(r.Context(), next, proposal.ID, requestSubject(r), snapshot)
		if err != nil {
			switch {
			case errors.Is(err, workspace.ErrNotFound):
				writeError(w, 404, err)
			case errors.Is(err, workspace.ErrBaseVersionStale):
				// The agent moved past the proposal's base: honest refusal,
				// the caller re-bases or re-creates the proposal.
				writeJSON(w, 409, map[string]any{
					"error":           "base_version_stale",
					"base_version":    proposal.BaseVersion,
					"current_version": a.DefinitionVersion,
				})
			case errors.Is(err, workspace.ErrConflict):
				writeError(w, 409, err)
			default:
				writeError(w, 500, err)
			}
			return
		}
		emitAgentDefinitionEvent(r.Context(), observationURL, journalToken(), "agent.definition.applied", next, decided, actorFromRequest(r),
			map[string]any{"from_version": a.DefinitionVersion, "to_version": next.DefinitionVersion, "source": proposal.Source})
		writeJSON(w, 201, map[string]any{"version": next.DefinitionVersion, "agent": next, "proposal": decided})
	})

	// GET /versions/{v}/metrics: the agentSRE health card (docs/agent-evolution.md
	// §5) — the observation window of the version versus the same-length
	// window of its baseline, plus the daily series.
	mux.HandleFunc("GET /v1/workspace/agents/{agentID}/versions/{version}/metrics", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		version, ok := parseVersionParam(w, r)
		if !ok {
			return
		}
		a, ok := resolveAgent(w, r)
		if !ok {
			return
		}
		days := a.ObservationDays
		if raw := strings.TrimSpace(r.URL.Query().Get("days")); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 1 || parsed > 90 {
				writeError(w, 400, errors.New("days must be 1..90"))
				return
			}
			days = parsed
		}
		payload, err := versionMetricsPayload(r.Context(), ws, a, version, days)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, payload)
	})

	// GET/PUT /observation: the per-agent observation window (days, min runs).
	mux.HandleFunc("GET /v1/workspace/agents/{agentID}/observation", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		if _, ok := resolveAgent(w, r); !ok {
			return
		}
		cfg, err := ws.AgentObservation(r.Context(), r.PathValue("agentID"))
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"days": cfg.Days, "min_runs": cfg.MinRuns})
	})
	mux.HandleFunc("PUT /v1/workspace/agents/{agentID}/observation", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		var req struct {
			Days    int `json:"days"`
			MinRuns int `json:"min_runs"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		if _, ok := resolveAgent(w, r); !ok {
			return
		}
		cfg := workspace.ObservationConfig{Days: req.Days, MinRuns: req.MinRuns}
		if err := ws.SetAgentObservation(r.Context(), r.PathValue("agentID"), cfg); err != nil {
			if errors.Is(err, workspace.ErrConflict) {
				writeError(w, 422, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"days": cfg.Days, "min_runs": cfg.MinRuns})
	})

	// Control examples (docs/agent-evolution.md §2.4): save, list, delete and
	// the optional smoke run. The run is informational — it never blocks
	// anything; it answers «how does this definition react on these inputs».
	mux.HandleFunc("GET /v1/workspace/agents/{agentID}/check-cases", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		if _, ok := resolveAgent(w, r); !ok {
			return
		}
		cases, err := ws.ListAgentCheckCases(r.Context(), r.PathValue("agentID"))
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"cases": cases})
	})
	mux.HandleFunc("POST /v1/workspace/agents/{agentID}/check-cases", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		var req struct {
			Input          string   `json:"input"`
			MustContain    []string `json:"must_contain"`
			MustNotContain []string `json:"must_not_contain"`
			FromRunID      string   `json:"from_run_id"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		if strings.TrimSpace(req.Input) == "" {
			writeError(w, 422, errors.New("input is required"))
			return
		}
		if _, ok := resolveAgent(w, r); !ok {
			return
		}
		c := workspace.AgentCheckCase{
			AgentID: r.PathValue("agentID"), Input: req.Input,
			MustContain: req.MustContain, MustNotContain: req.MustNotContain,
			FromRunID: req.FromRunID, CreatedBy: requestSubject(r),
		}
		if err := ws.AddAgentCheckCase(r.Context(), &c); err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 201, c)
	})
	mux.HandleFunc("DELETE /v1/workspace/agents/{agentID}/check-cases/{cid}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		cid, err := strconv.ParseInt(r.PathValue("cid"), 10, 64)
		if err != nil || cid <= 0 {
			writeError(w, 400, errors.New("invalid case id"))
			return
		}
		if _, ok := resolveAgent(w, r); !ok {
			return
		}
		if err := ws.DeleteAgentCheckCase(r.Context(), r.PathValue("agentID"), cid); err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"deleted": cid})
	})
	mux.HandleFunc("POST /v1/workspace/agents/{agentID}/check-cases/run", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		a, ok := resolveAgent(w, r)
		if !ok {
			return
		}
		system, label, ok := checkCaseTarget(w, r, ws, a)
		if !ok {
			return
		}
		cases, err := ws.ListAgentCheckCases(r.Context(), a.ID)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		if len(cases) == 0 {
			writeError(w, 422, errors.New("no control examples — save some from practice first"))
			return
		}
		model := modelFor(w, r)
		if model == nil {
			return // modelFor already answered with the resolution error
		}
		if len(cases) > 16 {
			cases = cases[:16]
		}
		report := runCheckCases(r.Context(), model, system, cases)
		report["target"] = label
		writeJSON(w, 200, report)
	})
}

// agentVersionRow is a version snapshot plus the observation verdict the
// timeline shows next to it (docs/agent-evolution.md §6.4).
type agentVersionRow struct {
	workspace.AgentVersion
	Verdict any `json:"verdict"`
}

// candidateVisibleUnits is the org-chain projection handlers use outside the
// registration closures (nil means unfiltered: admins, service tokens and
// unassigned users).
func candidateVisibleUnits(r *http.Request) []string {
	principal, ok := controlplane.FromContext(r.Context())
	if !ok || principal.Role >= controlplane.RoleAdmin {
		return nil
	}
	return principal.VisibleUnits
}

// parseProposalID and parseVersionParam validate the numeric path parts.
func parseProposalID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, 400, errors.New("invalid proposal id"))
		return 0, false
	}
	return id, true
}

// qualityVersionRange parses ?from=&to= (moved from the retired quality.go):
// both parameters are required non-negative integers and must name different
// versions — invalid input is a 400 before any storage is touched.
func qualityVersionRange(w http.ResponseWriter, r *http.Request) (int, int, bool) {
	fromParam := strings.TrimSpace(r.URL.Query().Get("from"))
	toParam := strings.TrimSpace(r.URL.Query().Get("to"))
	if fromParam == "" || toParam == "" {
		writeError(w, 400, errors.New("from and to query parameters are required"))
		return 0, 0, false
	}
	parse := func(name, value string) (int, bool) {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 0 {
			writeError(w, 400, fmt.Errorf("invalid %s version %q", name, value))
			return 0, false
		}
		return parsed, true
	}
	from, ok := parse("from", fromParam)
	if !ok {
		return 0, 0, false
	}
	to, ok := parse("to", toParam)
	if !ok {
		return 0, 0, false
	}
	if from == to {
		writeError(w, 400, errors.New("from and to must be different versions"))
		return 0, 0, false
	}
	return from, to, true
}

func parseVersionParam(w http.ResponseWriter, r *http.Request) (int, bool) {
	version, err := strconv.Atoi(r.PathValue("version"))
	if err != nil || version <= 0 {
		writeError(w, 400, errors.New("invalid version"))
		return 0, false
	}
	return version, true
}

// proposalDiff diffs the proposal's base snapshot against the definition the
// proposal would apply. The target prompt is compiled by the same
// deterministic function runs use, so the diff answers exactly «что изменится
// в голове агента».
func proposalDiff(ctx context.Context, ws evolutionStore, a workspace.Agent, proposal workspace.AgentProposal) (workspace.VersionDiff, error) {
	from, err := ws.GetAgentVersion(ctx, a.ID, proposal.BaseVersion)
	if err != nil {
		return workspace.VersionDiff{}, err
	}
	next := a
	definition := proposal.Definition
	next.Definition = &definition
	next.Description = proposal.Description
	target := agentVersionSnapshot(next, "", "proposal", proposal.GeneratorModel)
	diff := workspace.VersionDiffOf(from, target)
	diff.From = proposal.BaseVersion
	diff.To = 0 // pre-apply: the target is a draft, not a stored version
	return diff, nil
}

// checkCaseTarget resolves the system prompt the smoke run grounds on:
// ?proposal=PID checks the proposal's draft definition, ?version=N checks a
// stored snapshot, neither checks the agent's current effective prompt.
func checkCaseTarget(w http.ResponseWriter, r *http.Request, ws evolutionStore, a workspace.Agent) (system, label string, ok bool) {
	if raw := strings.TrimSpace(r.URL.Query().Get("proposal")); raw != "" {
		pid, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || pid <= 0 {
			writeError(w, 400, errors.New("invalid proposal id"))
			return "", "", false
		}
		proposal, err := ws.GetAgentProposal(r.Context(), pid)
		if err != nil || proposal.AgentID != a.ID {
			writeError(w, 404, errors.New("proposal not found"))
			return "", "", false
		}
		next := a
		definition := proposal.Definition
		next.Definition = &definition
		_, prompt := agentPromptPreview(next)
		return prompt, fmt.Sprintf("proposal %d", pid), true
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("version")); raw != "" {
		version, err := strconv.Atoi(raw)
		if err != nil || version <= 0 {
			writeError(w, 400, errors.New("invalid version"))
			return "", "", false
		}
		snapshot, err := ws.GetAgentVersion(r.Context(), a.ID, version)
		if err != nil {
			writeError(w, 404, errors.New("version snapshot not found"))
			return "", "", false
		}
		return snapshot.CompiledPrompt, fmt.Sprintf("version %d", version), true
	}
	_, prompt := agentPromptPreview(a)
	return prompt, fmt.Sprintf("version %d (current)", a.DefinitionVersion), true
}

// observationFor computes the observation result of one version plus both
// metric arms — the shared core of the metrics endpoint and the version list.
func observationFor(ctx context.Context, ws evolutionStore, a workspace.Agent, version, days int, now time.Time) (workspace.ObservationResult, []workspace.AgentVersionMetrics, []workspace.AgentVersionMetrics, error) {
	snapshot, err := ws.GetAgentVersion(ctx, a.ID, version)
	if err != nil {
		return workspace.ObservationResult{}, nil, nil, err
	}
	since := now.Add(-time.Duration(days) * 24 * time.Hour)
	current, err := ws.ListAgentVersionMetrics(ctx, a.ID, version, since)
	if err != nil {
		return workspace.ObservationResult{}, nil, nil, err
	}
	// The baseline arm: the replaced version's window of the same length,
	// ending where this version started.
	baseline := []workspace.AgentVersionMetrics{}
	if snapshot.BaselineVersion > 0 && snapshot.BaselineVersion != version {
		baselineSince := snapshot.CreatedAt.Add(-time.Duration(days) * 24 * time.Hour)
		rows, err := ws.ListAgentVersionMetrics(ctx, a.ID, snapshot.BaselineVersion, baselineSince)
		if err != nil {
			return workspace.ObservationResult{}, nil, nil, err
		}
		// Like-for-like: only days before the switch belong to the arm.
		for _, row := range rows {
			if row.Day.Before(snapshot.CreatedAt) {
				baseline = append(baseline, row)
			}
		}
	}
	cfg, err := ws.AgentObservation(ctx, a.ID)
	if err != nil {
		return workspace.ObservationResult{}, nil, nil, err
	}
	result := workspace.EvaluateObservation(cfg, snapshot.CreatedAt, current, baseline, now)
	return result, current, baseline, nil
}

// versionMetricsPayload builds the docs/agent-evolution.md §5 response:
// the observation state of the version, both arms of the comparison, the
// rate deltas and the daily series.
func versionMetricsPayload(ctx context.Context, ws evolutionStore, a workspace.Agent, version, days int) (map[string]any, error) {
	now := time.Now().UTC()
	evaluation, current, baseline, err := observationFor(ctx, ws, a, version, days, now)
	if err != nil {
		return nil, err
	}
	snapshot, err := ws.GetAgentVersion(ctx, a.ID, version)
	if err != nil {
		return nil, err
	}
	cfg, err := ws.AgentObservation(ctx, a.ID)
	if err != nil {
		return nil, err
	}
	curSummary, baseSummary := workspace.SumMetrics(current), workspace.SumMetrics(baseline)
	deltas := workspace.MetricDeltas(curSummary, baseSummary, cfg.MinRuns)
	// The §5 wire shape: rates and per-run averages, verdicts nested.
	wire := func(sum workspace.WindowSummary) map[string]any {
		perRun := func(value float64) float64 {
			if sum.Runs == 0 {
				return 0
			}
			return value / float64(sum.Runs)
		}
		return map[string]any{
			"runs": sum.Runs, "succeeded": sum.Succeeded, "failed": sum.Failed,
			"cancelled": sum.Cancelled, "turn_limited": sum.TurnLimited,
			"verdicts": map[string]any{
				"accepted": sum.VerdictAccepted, "corrected": sum.VerdictCorrected, "rejected": sum.VerdictRejected,
			},
			"interventions":     perRun(float64(sum.Interventions)),
			"reworks":           sum.Reworks,
			"tokens_avg":        perRun(float64(sum.Tokens)),
			"cost_usd_avg":      perRun(sum.CostUSD),
			"wall_ms_avg":       perRun(float64(sum.WallMs)),
			"uncertain_ops_avg": perRun(sum.UncertainOps),
			"incidents":         sum.Incidents,
		}
	}
	wireDeltas := make([]map[string]any, 0, len(deltas))
	for _, d := range deltas {
		mark := "noise"
		if d.Signal {
			mark = "signal"
		}
		wireDeltas = append(wireDeltas, map[string]any{
			"metric": d.Metric, "current": d.Current, "baseline": d.Baseline,
			"delta": d.Delta, "signal": mark,
		})
	}
	daily := make([]map[string]any, 0, len(current))
	for _, row := range current {
		daily = append(daily, map[string]any{
			"day": row.Day.Format("2006-01-02"), "runs": row.Runs, "succeeded": row.Succeeded,
			"failed": row.Failed, "cancelled": row.Cancelled, "turn_limited": row.TurnLimited,
			"verdict_accepted": row.VerdictAccepted, "verdict_corrected": row.VerdictCorrected,
			"verdict_rejected": row.VerdictRejected, "interventions": row.Interventions,
			"reworks": row.Reworks, "tokens": row.Tokens, "cost_usd": row.CostUSD,
			"wall_ms": row.WallMs, "uncertain_ops": row.UncertainOps, "incidents": row.Incidents,
		})
	}
	var verdict any
	if evaluation.Verdict != "" {
		verdict = evaluation.Verdict
	}
	return map[string]any{
		"observation": map[string]any{
			"state": evaluation.State, "verdict": verdict,
			"days": cfg.Days, "min_runs": cfg.MinRuns,
			"started_at": snapshot.CreatedAt, "elapsed_days": evaluation.ElapsedDays,
			"runs": evaluation.Runs,
		},
		"current":  wire(curSummary),
		"baseline": wire(baseSummary),
		"delta":    wireDeltas,
		"daily":    daily,
	}, nil
}

// checkCaseMaxTokens caps one control-example generation. Patterns only
// inspect the answer head, and long generations on slow models turn into
// timeout tails — but a cap too low turns a legitimately verbose answer
// into a truncated one and the case fails on the missing tail. 8k is the
// compromise (the WithMaxTokens contract: checked-in-head answers).
const checkCaseMaxTokens = 8192

// agentProposalDraft is the model's answer to a free-text problem report.
type agentProposalDraft struct {
	Problem       string   `json:"problem"`
	Change        string   `json:"change"`
	Effect        string   `json:"effect"`
	ChangeSummary string   `json:"change_summary"`
	Constraints   []string `json:"constraints"`
	Completion    []string `json:"completion"`
	Model         string   `json:"-"`
}

// draftAgentProposal turns a user's free-text problem into a definition
// change draft. The contract mirrors agent_propose (kernel/agent): the
// proposal may only restate constraints and completion — full replacement
// lists, omitted fields keep the current value — and never touches
// capabilities or the agent's purpose.
func draftAgentProposal(ctx context.Context, model *llm.Client, a workspace.Agent, problem string) (agentProposalDraft, error) {
	constraints, completion := []string{}, []string{}
	if a.Definition != nil {
		constraints, completion = a.Definition.Constraints, a.Definition.Completion
	}
	encode := func(values []string) string {
		if len(values) == 0 {
			return "(none)"
		}
		payload, _ := json.Marshal(values)
		return string(payload)
	}
	prompt := fmt.Sprintf(`The user reports a problem with an AI agent and wants its definition changed. Draft the change.

Agent: %s
Purpose: %s
Current constraints (what the agent must never do): %s
Current completion checks (what to verify before declaring done): %s

User's problem report:
%s

Respond with one JSON object:
{"problem": "<the observed problem, restated in one or two sentences>", "change": "<the definition change you propose>", "effect": "<the expected effect once applied>", "change_summary": "<3-5 short lines describing what will change in the agent's BEHAVIOR — this is what the user reads before applying>", "constraints": %s, "completion": %s}

Rules: constraints and completion are FULL REPLACEMENT lists — copy the current items, add, reword or drop items as the problem requires; leave an array empty ([] or omitted) to keep the current list unchanged. Never invent capabilities. Ground every change in the reported problem; no generic advice. Keep "problem", "change" and "effect" to at most three sentences each — a truncated answer is a failed proposal.`,
		a.Name, a.Description, encode(constraints), encode(completion), strings.TrimSpace(problem),
		`["..."]`, `["..."]`)
	messages := []llm.Message{
		{Role: "system", Content: "You draft precise agent definition changes and answer with JSON only."},
		{Role: "user", Content: prompt},
	}
	callCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	// A one-shot JSON answer routinely comes back broken the first time —
	// truncated by the output cap, or with an unescaped quote that breaks
	// brace balance. One corrective round recovers it without the user
	// seeing a raw 502; the headroom above the usual ~1-2k of output keeps
	// the first attempt from truncating at all.
	draft, perr := parseAgentProposalDraft(callCtx, model, messages)
	if perr != nil {
		repair := append(slices.Clone(messages), llm.Message{Role: "user", Content: fmt.Sprintf("Your previous reply was not one complete JSON object (%s). Answer again with ONLY the JSON object — no prose, no code fences, close every bracket and quote, keep each text field to at most three sentences.", perr)})
		draft, perr = parseAgentProposalDraft(callCtx, model, repair)
		if perr != nil {
			return agentProposalDraft{}, fmt.Errorf("model answer was not one complete JSON object even after a corrective retry: %w", perr)
		}
	}
	return draft, nil
}

// parseAgentProposalDraft performs one model call over the given messages and
// parses the answer into a draft.
func parseAgentProposalDraft(ctx context.Context, model *llm.Client, messages []llm.Message) (agentProposalDraft, error) {
	completionResp, err := model.Complete(ctx, messages, nil, llm.WithMaxTokens(4096))
	if err != nil {
		return agentProposalDraft{}, err
	}
	payload, err := extractFirstJSONObject(completionResp.Content)
	if err != nil {
		return agentProposalDraft{}, err
	}
	var draft agentProposalDraft
	if err := json.Unmarshal([]byte(payload), &draft); err != nil {
		return agentProposalDraft{}, fmt.Errorf("model returned invalid JSON: %w", err)
	}
	draft.Problem = strings.TrimSpace(draft.Problem)
	draft.Change = strings.TrimSpace(draft.Change)
	draft.Effect = strings.TrimSpace(draft.Effect)
	draft.ChangeSummary = strings.TrimSpace(draft.ChangeSummary)
	if draft.Change == "" {
		return agentProposalDraft{}, errors.New("model returned an empty change")
	}
	return draft, nil
}

// extractFirstJSONObject finds the first balanced {...} payload in a model
// answer that may carry prose around the JSON.
func extractFirstJSONObject(content string) (string, error) {
	start := strings.Index(content, "{")
	if start < 0 {
		return "", errors.New("model returned no JSON object")
	}
	depth := 0
	inString, escaped := false, false
	for i := start; i < len(content); i++ {
		switch content[i] {
		case '\\':
			if inString {
				escaped = !escaped
				continue
			}
		case '"':
			if inString && !escaped {
				inString = false
			} else if !inString {
				inString = true
			}
		case '{':
			if !inString {
				depth++
			}
		case '}':
			if !inString {
				depth--
				if depth == 0 {
					return content[start : i+1], nil
				}
			}
		}
		escaped = false
	}
	return "", errors.New("unbalanced JSON object in model answer (the output was likely truncated — the closing brace never came)")
}

// summarizeChange is the change-summary fallback when the model omitted one:
// the proposed change itself is the most honest one-liner.
func summarizeChange(problem, change string) string {
	summary := strings.TrimSpace(change)
	if summary == "" {
		summary = strings.TrimSpace(problem)
	}
	if len(summary) > 300 {
		summary = summary[:300] + "…"
	}
	return summary
}

// runCheckCases executes the control examples against one system prompt:
// each case is a standalone model call plus pattern checks. Results are
// informational (docs/agent-evolution.md §2.4) — the smoke run never blocks
// anything, it only answers «how does this definition react».
func runCheckCases(ctx context.Context, model *llm.Client, system string, cases []workspace.AgentCheckCase) map[string]any {
	type result struct {
		Input  string `json:"input"`
		Pass   bool   `json:"pass"`
		Detail string `json:"detail,omitempty"`
	}
	results := make([]result, 0, len(cases))
	passed, failed := 0, 0
	for _, c := range cases {
		callCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		answer, err := model.Complete(callCtx, []llm.Message{
			{Role: "system", Content: system},
			{Role: "user", Content: c.Input},
		}, nil, llm.WithMaxTokens(checkCaseMaxTokens))
		cancel()
		if err != nil {
			failed++
			results = append(results, result{Input: c.Input, Pass: false, Detail: "model error: " + err.Error()})
			continue
		}
		missed, unexpected := []string{}, []string{}
		for _, want := range c.MustContain {
			if want != "" && !strings.Contains(answer.Content, want) {
				missed = append(missed, want)
			}
		}
		for _, banned := range c.MustNotContain {
			if banned != "" && strings.Contains(answer.Content, banned) {
				unexpected = append(unexpected, banned)
			}
		}
		if len(missed) == 0 && len(unexpected) == 0 {
			passed++
			results = append(results, result{Input: c.Input, Pass: true})
			continue
		}
		failed++
		detail := ""
		if len(missed) > 0 {
			detail = "missing: " + strings.Join(missed, ", ")
		}
		if len(unexpected) > 0 {
			if detail != "" {
				detail += "; "
			}
			detail += "unexpected: " + strings.Join(unexpected, ", ")
		}
		results = append(results, result{Input: c.Input, Pass: false, Detail: detail})
	}
	return map[string]any{"passed": passed, "failed": failed, "results": results}
}

// emitAgentVersionEvent journals a version lifecycle event
// (agent.version.observed / degraded / rollback) to the journal. Best-effort:
// the version rows are the source of truth, the event is the audit trail.
func emitAgentVersionEvent(ctx context.Context, observationURL, token, eventType string, a workspace.Agent, version int, data map[string]any) {
	if observationURL == "" {
		return
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d\x00%d", eventType, a.ID, version, time.Now().UnixNano())))
	payload := map[string]any{"agent_id": a.ID, "agent_name": a.Name, "version": version}
	for key, value := range data {
		payload[key] = value
	}
	event := observation.Event{
		Schema:     observation.Schema,
		EventID:    fmt.Sprintf("agent-version/%s/%s", eventType, hex.EncodeToString(digest[:10])),
		OccurredAt: time.Now().UTC(),
		Source:     observation.Source{ID: "workspace", Integration: "agent-kernel", Version: "1"},
		Context:    observation.Context{Project: a.ProjectID},
		Type:       eventType,
		Data:       payload,
	}
	if err := event.Validate(); err != nil {
		return
	}
	body, err := json.Marshal(map[string]any{"events": []observation.Event{event}})
	if err != nil {
		return
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, observationURL+"/v1/observations/events", bytes.NewReader(body))
	if err != nil {
		return
	}
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return
	}
	response.Body.Close()
}
