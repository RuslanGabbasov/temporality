package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/observation"
	"github.com/temporality-project/temporality/workspace"
)

// Eval-gated proposal apply (specs/quality-loop.md §5, §8.3, §10.1, M3):
// applying an agent proposal is only possible over a fresh green evaluation
// run of the proposal's base version. When no usable run exists the server
// starts one itself (trigger='gate') and answers 202 with that exact
// evaluation_run_id; a repeat apply waits on the same run and never starts a
// second one (§8.3 idempotency). A red run answers 409 with the failed
// cases, a stale base answers 409 base_version_stale. The green path hands
// over to the transactional apply callback (Store.ApplyAgentProposal:
// claim + version++ + immutable snapshot in one transaction, §10.1a).
//
// The request body may carry {"gate": false} to skip the gate check and
// {"override": true, "justification": "..."} to promote a red gate (§5.4,
// §11.2): writer is a full service administrator, below-writer roles are
// refused by the RBAC gate before any of this is read. An override writes
// an audit record and the agent.evaluation.gate.overridden event
// (override-rate < 5 % is a loop-health metric, §12). The silent-looking
// gate:false path is not silent either: it writes an audit record and the
// agent.evaluation.gate.skipped event, so every apply without a green gate
// stays attributable (CWE-778, §12 override-rate would otherwise miss it).

// evalGateRunTimeout bounds the background gate run; it matches
// workspace.evalGateRunningWindow, so a run that dies with the deadline is
// failed by the next apply instead of answering 202 forever (§10.4).
const evalGateRunTimeout = 30 * time.Minute

// evalGateStore is the storage surface the gated apply needs;
// *workspace.Store satisfies it and tests provide fakes.
type evalGateStore interface {
	GetAgent(ctx context.Context, id string) (workspace.Agent, error)
	GetAgentProposal(ctx context.Context, agentID string, id int64) (workspace.AgentProposal, error)
	GetAgentEvaluationSuite(ctx context.Context, agentID string) (workspace.AgentEvaluationSuite, error)
	EvalGateStatus(ctx context.Context, agentID string, proposalID int64, baseVersion int, now time.Time) (workspace.EvalGateDecision, error)
	CreateGateRun(ctx context.Context, agentID string, version int, proposalID int64) (workspace.AgentEvaluationRun, bool, error)
	AbandonGateRun(ctx context.Context, runID int64, cause string) error
	RecordAccessAudit(ctx context.Context, actor, action, entityKind, entityID string, details map[string]any) error
}

// evalGateRunner executes the gate evaluation the apply endpoint started;
// *kernelGateRunner (below) adapts the kernel activities, tests provide fakes.
type evalGateRunner interface {
	RunGateEvaluation(ctx context.Context, agentID string, version int, meta agent.EvalRunMeta) (string, error)
}

// evalGateOutbox is the durable-outbox surface the gate events need (§9);
// *outbox.Outbox satisfies it and tests provide fakes.
type evalGateOutbox interface {
	Enqueue(context.Context, observation.Event) error
}

// evalApplyFunc continues a cleared apply on the regular path: build the
// target agent, claim the proposal, bump the version, store the snapshot and
// answer 200. The gate handler never touches agent state itself.
type evalApplyFunc func(w http.ResponseWriter, r *http.Request, a workspace.Agent, proposal workspace.AgentProposal)

// kernelGateRunner adapts the kernel activities to the gate runner contract.
type kernelGateRunner struct {
	activities *agent.Activities
}

// RunGateEvaluation finalises the pre-created gate run row: held-out cases
// are excluded because the run decides the gate (§4.5), and a record failure
// surfaces as the error that fails the row (§10.4).
func (k kernelGateRunner) RunGateEvaluation(ctx context.Context, agentID string, version int, meta agent.EvalRunMeta) (string, error) {
	return k.activities.RunAgentEvaluationMeta(ctx, agentID, version, meta)
}

// evalGateRequest is the optional apply body (§8.3): gate defaults to true
// (nil pointer), override promotes a red gate and requires a justification
// so audit and the override-rate metric stay meaningful (§5.4).
type evalGateRequest struct {
	Gate          *bool  `json:"gate"`
	Override      bool   `json:"override"`
	Justification string `json:"justification"`
}

// parseEvalGateRequest reads the optional body; an empty body is the
// default apply (gate on, no override), a malformed body is a 400.
func parseEvalGateRequest(w http.ResponseWriter, r *http.Request) (evalGateRequest, bool) {
	var request evalGateRequest
	if r.ContentLength == 0 {
		return request, true
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&request); err != nil {
		writeError(w, 400, err)
		return request, false
	}
	return request, true
}

// RegisterM3Routes mounts the gated apply endpoint over the interfaces above;
// main wires the real store, kernel runner and apply callback, tests mount
// this handler alone.
func RegisterM3Routes(mux *http.ServeMux, gate *controlplane.Gate, store evalGateStore, runner evalGateRunner, events evalGateOutbox, apply evalApplyFunc, visibleUnits func(*http.Request) []string, now func() time.Time, log *slog.Logger) {
	if now == nil {
		now = time.Now
	}
	mux.HandleFunc("POST /v1/workspace/agents/{agentID}/proposals/{id}/apply", func(w http.ResponseWriter, r *http.Request) {
		// §5.4, §11.2: writer is a full service administrator; below-writer
		// roles may not apply, override or skip the gate at all.
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		agentID := r.PathValue("agentID")
		proposalID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			writeError(w, 400, errors.New("invalid proposal id"))
			return
		}
		request, ok := parseEvalGateRequest(w, r)
		if !ok {
			return
		}
		a, err := store.GetAgent(r.Context(), agentID)
		if err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, errors.New("agent not found"))
				return
			}
			writeError(w, 500, err)
			return
		}
		if !workspace.OrgVisible(a.OrgUnitID, visibleUnits(r)) {
			writeError(w, 404, workspace.ErrNotFound)
			return
		}
		proposal, err := store.GetAgentProposal(r.Context(), agentID, proposalID)
		if err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, errors.New("proposal not found"))
				return
			}
			writeError(w, 500, err)
			return
		}
		if proposal.Status != "pending" {
			writeJSON(w, 409, map[string]any{
				"error":  fmt.Sprintf("proposal %d is already %s", proposal.ID, proposal.Status),
				"status": proposal.Status,
			})
			return
		}
		// §5.5, §10.1б: a base that moved past the proposal must be rebased —
		// never applied over a stale base, silently or otherwise.
		if proposal.BaseVersion != a.DefinitionVersion {
			writeJSON(w, 409, map[string]any{
				"error":           "base_version_stale",
				"base_version":    proposal.BaseVersion,
				"current_version": a.DefinitionVersion,
			})
			return
		}
		// gate:false (writer right, §8.3): the gate is skipped entirely —
		// no decision lookup, no run. The skip itself is never silent: an
		// audit record and the agent.evaluation.gate.skipped event mark who
		// bypassed the gate and on which base version (CWE-778).
		if request.Gate != nil && !*request.Gate {
			recordEvalGateSkip(r.Context(), store, log, a, proposal, requestSubject(r))
			emitEvalGateEvent(r.Context(), events, "agent.evaluation.gate.skipped", a, proposal, nil,
				map[string]any{"cause": "gate_skipped"}, actorFromRequest(r))
			apply(w, r, a, proposal)
			return
		}
		// Golden drift (§13 option B): a case past its stale_after refuses
		// the apply with suite_stale before the gate can silently gate on an
		// outdated reference — the owner either refreshes the cases or
		// overrides with a justification.
		suite, err := store.GetAgentEvaluationSuite(r.Context(), agentID)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		if stale := workspace.EvalSuiteStaleCases(suite.Cases, now()); len(stale) > 0 {
			if request.Override {
				justification := strings.TrimSpace(request.Justification)
				if justification == "" {
					writeJSON(w, 400, map[string]any{"error": evalGateOverrideRequired()})
					return
				}
				decision := workspace.EvalGateDecision{Cause: workspace.CauseSuiteStale}
				recordEvalGateOverride(r.Context(), store, log, a, proposal, decision, justification, requestSubject(r))
				emitEvalGateEvent(r.Context(), events, "agent.evaluation.gate.overridden", a, proposal, nil,
					map[string]any{"cause": workspace.CauseSuiteStale, "override_justification": justification}, actorFromRequest(r))
				apply(w, r, a, proposal)
				return
			}
			emitEvalGateEvent(r.Context(), events, "agent.evaluation.gate.blocked", a, proposal, nil,
				map[string]any{"cause": workspace.CauseSuiteStale}, actorFromRequest(r))
			writeJSON(w, 409, map[string]any{
				"error":       "suite_stale",
				"cause":       workspace.CauseSuiteStale,
				"stale_cases": stale,
			})
			return
		}
		decision, err := store.EvalGateStatus(r.Context(), agentID, proposalID, proposal.BaseVersion, now())
		if err != nil {
			writeError(w, 500, err)
			return
		}
		switch decision.State {
		case workspace.EvalGateGreen:
			// Fresh green on the base version: apply proceeds (§5.3).
			emitEvalGateEvent(r.Context(), events, "agent.evaluation.gate.passed", a, proposal, decision.Run, nil, actorFromRequest(r))
			apply(w, r, a, proposal)
		case workspace.EvalGateRed:
			// Red run (or judge-degraded green): 409 with the body of reasons
			// (§5.4, §8.3) unless a writer promotes it with a justification —
			// the override that keeps audit and override-rate honest (§12).
			if request.Override {
				justification := strings.TrimSpace(request.Justification)
				if justification == "" {
					writeJSON(w, 400, map[string]any{"error": evalGateOverrideRequired()})
					return
				}
				recordEvalGateOverride(r.Context(), store, log, a, proposal, decision, justification, requestSubject(r))
				emitEvalGateEvent(r.Context(), events, "agent.evaluation.gate.overridden", a, proposal, decision.Run,
					map[string]any{"cause": decision.Cause, "override_justification": justification}, actorFromRequest(r))
				apply(w, r, a, proposal)
				return
			}
			emitEvalGateEvent(r.Context(), events, "agent.evaluation.gate.blocked", a, proposal, decision.Run,
				map[string]any{"cause": decision.Cause}, actorFromRequest(r))
			writeJSON(w, 409, map[string]any{
				"error":             "gate_blocked",
				"cause":             decision.Cause,
				"evaluation_run_id": evalRunID(decision.Run),
				"failed_cases":      evalFailedCaseNames(decision.Run),
			})
		case workspace.EvalGateRunning:
			// §8.3 idempotency: the repeat apply waits on the exact run this
			// proposal already started — never a second one.
			writeJSON(w, 202, map[string]any{
				"evaluation_run_id": decision.Run.ID,
				"status":            "running",
			})
		case workspace.EvalGateNone:
			// Nothing usable: pre-create the gate run and answer 202 (§5.3).
			// Only the apply that actually inserted the row starts the
			// runner — a concurrent apply that also resolved the gate to
			// 'none' gets the very same run with created=false and waits on
			// it instead of launching a second evaluation over one row
			// (§8.3 idempotency, CWE-362).
			run, created, createErr := store.CreateGateRun(r.Context(), agentID, proposal.BaseVersion, proposalID)
			if createErr != nil {
				writeError(w, 500, createErr)
				return
			}
			if created {
				startGateRun(r.Context(), store, runner, log, agentID, proposal, run)
			}
			writeJSON(w, 202, map[string]any{
				"evaluation_run_id": run.ID,
				"status":            "running",
			})
		default:
			writeError(w, 500, errors.New("unknown eval gate state"))
		}
	})
}

// recordEvalGateOverride writes the audit entry of a promoted red gate
// (§5.4): the who (applying writer), the what (proposal, base version, the
// run that was overridden) and the why (justification). Best-effort — the
// override itself already happened; an audit failure is logged, never
// silently dropped.
func recordEvalGateOverride(ctx context.Context, store evalGateStore, log *slog.Logger, a workspace.Agent, proposal workspace.AgentProposal, decision workspace.EvalGateDecision, justification, subject string) {
	err := store.RecordAccessAudit(ctx, subject, workspace.AuditEvalGateOverridden, "agent_proposal",
		strconv.FormatInt(proposal.ID, 10), map[string]any{
			"agent_id":               a.ID,
			"base_version":           proposal.BaseVersion,
			"evaluation_run_id":      evalRunID(decision.Run),
			"gate_cause":             decision.Cause,
			"override_justification": justification,
		})
	if err != nil && log != nil {
		log.Warn("record eval gate override audit", "proposal", proposal.ID, "error", err)
	}
}

// recordEvalGateSkip writes the audit entry of a gate-less apply (§8.3,
// gate:false): the who (applying writer), the what (proposal, base version)
// and the fact that no gate decision was consulted. Best-effort, exactly
// like the override record — the apply already happened; an audit failure
// is logged, never silently dropped.
func recordEvalGateSkip(ctx context.Context, store evalGateStore, log *slog.Logger, a workspace.Agent, proposal workspace.AgentProposal, subject string) {
	err := store.RecordAccessAudit(ctx, subject, workspace.AuditEvalGateSkipped, "agent_proposal",
		strconv.FormatInt(proposal.ID, 10), map[string]any{
			"agent_id":     a.ID,
			"base_version": proposal.BaseVersion,
			"gate":         false,
		})
	if err != nil && log != nil {
		log.Warn("record eval gate skip audit", "proposal", proposal.ID, "error", err)
	}
}

// startGateRun launches the gate evaluation in the background: the HTTP
// response is the 202 above, the run finalises the pre-created row. A failed
// run fails that row (verdict=error, §10.4) so the next apply starts anew
// instead of waiting on a row that will never decide.
func startGateRun(ctx context.Context, store evalGateStore, runner evalGateRunner, log *slog.Logger, agentID string, proposal workspace.AgentProposal, run workspace.AgentEvaluationRun) {
	go func() {
		runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), evalGateRunTimeout)
		defer cancel()
		meta := agent.EvalRunMeta{RunID: run.ID, ProposalID: proposal.ID, Trigger: agent.EvalTriggerGate}
		if _, err := runner.RunGateEvaluation(runCtx, agentID, proposal.BaseVersion, meta); err != nil {
			log.Warn("gate evaluation run failed", "agent", agentID, "run", run.ID, "error", err)
			if abandonErr := store.AbandonGateRun(context.WithoutCancel(ctx), run.ID, workspace.CauseGateRunAborted); abandonErr != nil {
				log.Warn("abandon gate evaluation run", "agent", agentID, "run", run.ID, "error", abandonErr)
			}
		}
	}()
}

// evalRunID safely reads the deciding run's id (nil run → 0).
func evalRunID(run *workspace.AgentEvaluationRun) int64 {
	if run == nil {
		return 0
	}
	return run.ID
}

// evalFailedCaseNames lists the case names that blocked the gate: failed
// cases plus errored ones (an errored case is never a pass, §5.2). Names
// only — gate responses stay free of production answer text (§10.3).
func evalFailedCaseNames(run *workspace.AgentEvaluationRun) []string {
	names := []string{}
	if run == nil {
		return names
	}
	for _, c := range run.Cases {
		if !c.Passed {
			names = append(names, c.Name)
		}
	}
	return names
}

// emitEvalGateEvent records agent.evaluation.gate.passed | .blocked |
// .overridden | .skipped (§9) through the durable outbox. Best-effort: the run row and
// the apply answer stay the source of truth. Event ids include UnixNano
// (§8), so repeated emissions never collide in the journal.
func emitEvalGateEvent(ctx context.Context, events evalGateOutbox, eventType string, a workspace.Agent, proposal workspace.AgentProposal, run *workspace.AgentEvaluationRun, extra map[string]any, actor observation.Actor) {
	if events == nil {
		return
	}
	data := map[string]any{
		"agent_id":          a.ID,
		"proposal_id":       proposal.ID,
		"base_version":      proposal.BaseVersion,
		"evaluation_run_id": evalRunID(run),
	}
	for key, value := range extra {
		data[key] = value
	}
	item := observation.Event{
		Schema:     observation.Schema,
		EventID:    fmt.Sprintf("eval-gate/%s/%d/%d", proposal.AgentID, proposal.ID, time.Now().UnixNano()),
		OccurredAt: time.Now().UTC(),
		Source:     observation.Source{ID: "workspace", Integration: "agent-kernel", Version: "1"},
		Context:    observation.Context{Project: a.ProjectID, Actor: actor},
		Type:       eventType,
		Data:       data,
	}
	if err := item.Validate(); err != nil {
		return
	}
	if err := events.Enqueue(ctx, item); err != nil {
		slog.Warn("emit eval gate event", "type", eventType, "proposal", proposal.ID, "error", err)
	}
}

// evalGateOverrideRequired is the refusal of an override attempt without a
// justification (§5.4): promoting a red gate must stay attributable in audit
// and in the override-rate metric.
func evalGateOverrideRequired() string {
	return "justification is required to override the eval gate"
}
