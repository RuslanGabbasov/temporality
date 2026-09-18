package procedure_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/temporality-project/temporality/frp/affordance"
	"github.com/temporality-project/temporality/frp/execution"
	"github.com/temporality-project/temporality/frp/frame"
	"github.com/temporality-project/temporality/frp/objective"
	"github.com/temporality-project/temporality/frp/procedure"
	"github.com/temporality-project/temporality/frp/protocol"
	"github.com/temporality-project/temporality/frp/substrate/memory"
)

// TestRebuildSeedsTriggerWithObjective locks the longitudinal procedure fix:
// a trigger carrying only affordance-id tokens ("read file") never lexically
// overlaps a natural-language goal, so the renderer matched zero procedures
// even when the world held plenty. Rebuild must seed the trigger with the
// episode's objective text, and the same goal wording in a later episode must
// match — through the plain MatchProcedures path, no embeddings involved.
func TestRebuildSeedsTriggerWithObjective(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	now := time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)

	goalText := "исследовать репозиторий ledger и найти два скрытых бага"
	goal := objective.Objective{Protocol: protocol.Name, Version: protocol.Version, ObjectiveID: "objective-1", EpisodeID: "episode-1", Text: goalText, SuccessConditions: []string{"тесты проходят"}}
	startEvent := protocol.Event{Protocol: protocol.Name, Version: protocol.Version, EventID: "episode-start", TransactionTime: now, ValidTime: now, EpisodeID: "episode-1", Type: "episode.started", Payload: map[string]any{"objective_id": goal.ObjectiveID}, Provenance: map[string]any{"source": "test"}}
	startEvent.ApplyDefaults(now)
	if err := store.CreateObjective(ctx, goal, startEvent); err != nil {
		t.Fatal(err)
	}

	def := affordance.Definition{Protocol: protocol.Name, Version: protocol.Version, ID: "read_file", ExecutionMode: affordance.ModeDeterministic, InputSchema: map[string]any{}, Capabilities: []string{"filesystem.read"}, Limits: affordance.Limits{TimeoutSec: 30, CPU: 1, MemoryMB: 64, DiskMB: 64}}
	req := affordance.Request{Protocol: protocol.Name, Version: protocol.Version, RequestID: "request-1", EpisodeID: "episode-1", AffordanceID: def.ID, Arguments: map[string]any{"path": "internal/series/series.go"}}
	requested := protocol.Event{Protocol: protocol.Name, Version: protocol.Version, EventID: "er-1", TransactionTime: now, ValidTime: now, EpisodeID: "episode-1", Type: affordance.EventRequested, Payload: map[string]any{"request_id": req.RequestID}, Provenance: map[string]any{"source": "test"}}
	requested.ApplyDefaults(now)
	createdEvent := protocol.Event{Protocol: protocol.Name, Version: protocol.Version, EventID: "ec-1", TransactionTime: now, ValidTime: now, EpisodeID: "episode-1", Type: execution.EventExecutionCreated, Payload: map[string]any{"execution_id": "x-1"}, Provenance: map[string]any{"source": "test"}}
	createdEvent.ApplyDefaults(now)
	value := execution.Execution{Protocol: protocol.Name, Version: protocol.Version, ExecutionID: "x-1", RequestID: req.RequestID, EpisodeID: "episode-1", AffordanceID: def.ID, Status: execution.StatusCreated, CreatedEventID: "ec-1", IntentPersistedAt: now}
	if err := store.CreateExecution(ctx, def, req, value, requested, createdEvent); err != nil {
		t.Fatal(err)
	}
	runningEvent := protocol.Event{Protocol: protocol.Name, Version: protocol.Version, EventID: "es-1", TransactionTime: now.Add(30 * time.Second), ValidTime: now.Add(30 * time.Second), EpisodeID: "episode-1", Type: execution.EventExecutionStarted, Payload: map[string]any{"execution_id": "x-1"}, Provenance: map[string]any{"source": "test"}}
	runningEvent.ApplyDefaults(now.Add(30 * time.Second))
	if _, err := store.TransitionExecution(ctx, "x-1", execution.StatusRunning, now.Add(30*time.Second), nil, runningEvent); err != nil {
		t.Fatal(err)
	}
	completedEvent := protocol.Event{Protocol: protocol.Name, Version: protocol.Version, EventID: "ex-1", TransactionTime: now.Add(time.Minute), ValidTime: now.Add(time.Minute), EpisodeID: "episode-1", Type: execution.EventExecutionCompleted, Payload: map[string]any{"execution_id": "x-1"}, Provenance: map[string]any{"source": "test"}}
	completedEvent.ApplyDefaults(now.Add(time.Minute))
	if _, err := store.TransitionExecution(ctx, "x-1", execution.StatusCompleted, now.Add(time.Minute), nil, completedEvent); err != nil {
		t.Fatal(err)
	}

	projector := procedure.Projector{Config: procedure.BuildConfig{MinimumEvidence: 1}}
	if err := projector.Rebuild(ctx, "episode-1", store, store); err != nil {
		t.Fatal(err)
	}
	procedures, err := store.ListProcedures(ctx, procedure.Filter{EpisodeID: "episode-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(procedures) != 1 {
		t.Fatalf("expected one procedure, got %d", len(procedures))
	}
	built := procedures[0]
	for _, term := range []string{"read", "file", "исследовать", "репозиторий", "ledger", "бага", "тесты"} {
		if !strings.Contains(built.SemanticTrigger, term) {
			t.Fatalf("trigger %q missing term %q", built.SemanticTrigger, term)
		}
	}

	// A later episode wording the goal the same way must retrieve the procedure.
	warmGoal := objective.Objective{Protocol: protocol.Name, Version: protocol.Version, ObjectiveID: "objective-2", EpisodeID: "episode-2", Text: goalText, SuccessConditions: []string{}}
	current := frame.Frame{Protocol: protocol.Name, Version: protocol.Version, FrameID: "frame-2", AgentID: "agent-2", EpisodeID: "episode-2", BranchID: "branch-2", ObjectiveID: warmGoal.ObjectiveID, AsOf: now, Focus: frame.Focus{Type: frame.RefQuery, Query: "скрытые баги"}, WorkingSet: []frame.Ref{}, Mode: frame.ModeExplore, Attention: frame.Attention{Policy: "balanced", Deliberate: true, Ambient: true, MaxCandidates: 32}, Filters: frame.Filters{AgentIDs: []string{}, RegionKinds: []string{}}, Budget: frame.Budget{Tokens: 16000}}
	matches := procedure.MatchProcedures(procedures, warmGoal, current, procedure.MatchConfig{Threshold: 0.1})
	if len(matches) == 0 {
		t.Fatal("warm episode with identical goal wording matched no procedures")
	}
	if matches[0].Procedure.ProcedureID != built.ProcedureID {
		t.Fatalf("unexpected match: %#v", matches[0])
	}
}

// TestBuildTriggerExcludedFromProcedureID locks id stability: the trigger now
// carries objective tokens, but procedureID must keep hashing only the
// episode/affordance/arguments triple so re-projecting old episodes (or a
// re-run with different objective wording) never mints duplicate procedures.
func TestBuildTriggerExcludedFromProcedureID(t *testing.T) {
	evidence := []procedure.Evidence{{
		Execution: execution.Execution{Protocol: protocol.Name, Version: protocol.Version, ExecutionID: "x", RequestID: "r", EpisodeID: "episode", AffordanceID: "read_file", Status: execution.StatusCompleted},
		Request:   affordance.Request{Protocol: protocol.Name, Version: protocol.Version, RequestID: "r", EpisodeID: "episode", AffordanceID: "read_file", Arguments: map[string]any{"path": "a.go"}},
	}}
	plain := procedure.Build("episode", evidence, procedure.BuildConfig{MinimumEvidence: 1})
	seeded := procedure.Build("episode", evidence, procedure.BuildConfig{MinimumEvidence: 1, ObjectiveText: "исследовать репозиторий"})
	if len(plain) != 1 || len(seeded) != 1 {
		t.Fatalf("expected one procedure each: %d %d", len(plain), len(seeded))
	}
	if plain[0].ProcedureID != seeded[0].ProcedureID {
		t.Fatalf("objective seeding must not change procedure ids: %s vs %s", plain[0].ProcedureID, seeded[0].ProcedureID)
	}
	if seeded[0].SemanticTrigger == plain[0].SemanticTrigger {
		t.Fatalf("objective seeding must extend the trigger: %q", seeded[0].SemanticTrigger)
	}
}
