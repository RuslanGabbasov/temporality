package memory

import (
	"context"
	"testing"

	"github.com/temporality-project/temporality/aml/harness"
	"github.com/temporality-project/temporality/aml/llm"
	"github.com/temporality-project/temporality/aml/opsenv"
)

// §18.1: a reuse whose value matches in string form but arrives in the wrong
// JSON type (version: 2 instead of "2") must be UNRESOLVED, never a
// contradiction, and must not move confidence.
func TestTypedMismatchDoesNotContradict(t *testing.T) {
	store := NewMemStore()
	layer := NewLayer(store, ConfigForArmTest("D"), opsenv.AllServices, nil)
	layer.SetSession("s1", 1)
	layer.SetTaskContext("prod", "")

	seed := &Asset{DedupKey: "seed-version", Service: opsenv.SvcBilling, Problem: "request-params", Kind: KindParamRequired,
		Proposition: "billing-api invoices.list requires params.version=\"2\" (string)",
		Confidence:  0.8,
		Status:      StatusActive,
		Recommendation: Recommendation{Service: opsenv.SvcBilling, Resource: "invoices.list",
			Param: "params.version", Value: "2", ValueType: "string"},
		Environment:    "prod",
		SourceSessions: []string{"s0"},
	}
	seed.ID = NewUUID()
	if err := store.UpsertAsset(context.Background(), seed, true); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	layer.TaskStart(ctx, "s1", "G1", "Fetch the unpaid invoices from the billing-api invoices.list endpoint and report the first invoice id.")
	// The model sends the right value in the wrong type: number, not string.
	call := llm.ToolCall{ID: "c1", Name: "api_call", Args: map[string]any{
		"service": opsenv.SvcBilling, "resource": "invoices.list", "auth": "pat",
		"params": map[string]any{"version": 2.0},
	}}
	layer.BeforeToolCall(ctx, "s1", call)
	layer.AfterToolCall(ctx, "s1", call, harness.ToolResult{OK: false, Status: 400, Text: "HTTP 400", Cause: CauseParameter})

	stats := layer.Stats()
	if stats.Contradicted != 0 {
		t.Fatalf("typed mismatch must not contradict: %+v", stats)
	}
	if stats.Unresolved != 1 {
		t.Fatalf("typed mismatch must be UNRESOLVED: %+v", stats)
	}
	assets, _ := store.ListAssets(ctx)
	if assets[0].Confidence != 0.8 {
		t.Fatalf("confidence must not move on typed mismatch: %v", assets[0].Confidence)
	}
	events := store.Events()
	foundTyped := false
	for _, e := range events {
		if e.Type == EventUnresolved {
			if typed, _ := e.Payload["typed_mismatch"].(bool); typed {
				foundTyped = true
			}
		}
		if e.Type == EventWeakened {
			t.Fatal("typed mismatch must not weaken the asset")
		}
	}
	if !foundTyped {
		t.Fatal("expected an UNRESOLVED event carrying typed_mismatch=true")
	}
}

// §18.1 complement: the same failure with the correct type IS a contradiction
// path (value/parameter semantics) — typed fix must not mask real ones.
func TestCorrectTypeFailureStillAttributes(t *testing.T) {
	store := NewMemStore()
	layer := NewLayer(store, ConfigForArmTest("D"), opsenv.AllServices, nil)
	layer.SetSession("s1", 1)
	layer.SetTaskContext("prod", "")

	seed := &Asset{DedupKey: "seed-version2", Service: opsenv.SvcBilling, Problem: "request-params", Kind: KindParamRequired,
		Proposition: "billing-api invoices.list requires params.version=\"2\"",
		Confidence:  0.8, Status: StatusActive,
		Recommendation: Recommendation{Service: opsenv.SvcBilling, Resource: "invoices.list",
			Param: "params.version", Value: "2", ValueType: "string"},
		Environment: "prod", SourceSessions: []string{"s0"},
	}
	seed.ID = NewUUID()
	if err := store.UpsertAsset(context.Background(), seed, true); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	layer.TaskStart(ctx, "s1", "G1", "Fetch the unpaid invoices from the billing-api invoices.list endpoint and report the first invoice id.")
	call := llm.ToolCall{ID: "c1", Name: "api_call", Args: map[string]any{
		"service": opsenv.SvcBilling, "resource": "invoices.list", "auth": "pat",
		"params": map[string]any{"version": "2"},
	}}
	layer.BeforeToolCall(ctx, "s1", call)
	layer.AfterToolCall(ctx, "s1", call, harness.ToolResult{OK: false, Status: 400, Text: "HTTP 400", Cause: CauseParameter})

	if layer.Stats().Contradicted != 1 {
		t.Fatalf("correctly-typed failure must contradict: %+v", layer.Stats())
	}
}

// §18.2: a STALE asset with an independent-session confirmation returns
// ACTIVE directly with restored confidence, not through four cycles.
func TestResurrectionOnIndependentConfirmation(t *testing.T) {
	store := NewMemStore()
	layer := NewLayer(store, ConfigForArmTest("D"), opsenv.AllServices, nil)

	seed := &Asset{DedupKey: "seed-res", Service: opsenv.SvcBilling, Problem: "auth", Kind: KindStrategySwitch,
		Proposition: "billing-api invoices.list works with auth=oauth",
		Confidence:  0.30, Status: StatusStale, ConsecutiveContradictions: 2,
		Recommendation: Recommendation{Service: opsenv.SvcBilling, Resource: "invoices.list",
			Param: "auth", Value: "oauth", ValueType: "string"},
		Environment: "prod", SourceSessions: []string{"s0"},
	}
	seed.ID = NewUUID()
	ctx := context.Background()
	if err := store.UpsertAsset(ctx, seed, true); err != nil {
		t.Fatal(err)
	}

	layer.SetSession("s1", 2)
	layer.reinforce(ctx, seed.ID)

	asset, _ := store.ListAssets(ctx)
	if asset[0].Status != StatusActive {
		t.Fatalf("independent confirmation must resurrect STALE → ACTIVE, got %s", asset[0].Status)
	}
	if asset[0].Confidence < 0.55 {
		t.Fatalf("resurrection must restore confidence to ≥0.55, got %v", asset[0].Confidence)
	}
	if asset[0].ConsecutiveContradictions != 0 {
		t.Fatalf("resurrection must reset consecutive contradictions, got %d", asset[0].ConsecutiveContradictions)
	}
}

// §18.3: a first-attempt success creates a 0.40 asset; independent sessions
// mature it 0.40 → 0.55 → 0.67.
func TestWhatWorkedMaturation(t *testing.T) {
	store := NewMemStore()
	layer := NewLayer(store, ConfigForArmTest("D"), opsenv.AllServices, nil)
	ctx := context.Background()

	run := func(session string, index int) {
		layer.SetSession(session, index)
		layer.SetTaskContext("prod", "")
		layer.TaskStart(ctx, session, "G1", "Fetch the monthly invoice summary from the billing-api invoices.list endpoint.")
		call := llm.ToolCall{ID: "c1", Name: "api_call", Args: map[string]any{
			"service": opsenv.SvcBilling, "resource": "invoices.list", "auth": "oauth",
		}}
		layer.BeforeToolCall(ctx, session, call)
		layer.AfterToolCall(ctx, session, call, harness.ToolResult{OK: true, Status: 200, Text: "HTTP 200"})
		layer.TaskEnd(ctx, session, "G1", true, "summary id 42")
	}

	run("s1", 1)
	assets, _ := store.ListAssets(ctx)
	var ww *Asset
	for _, a := range assets {
		if a.Kind == KindWhatWorked {
			ww = a
		}
	}
	if ww == nil {
		t.Fatal("first-attempt success must create a what-worked asset")
	}
	if ww.Confidence != WhatWorkedBaseline {
		t.Fatalf("what-worked baseline must be %v, got %v", WhatWorkedBaseline, ww.Confidence)
	}

	run("s2", 2)
	assets, _ = store.ListAssets(ctx)
	for _, a := range assets {
		if a.Kind == KindWhatWorked {
			ww = a
		}
	}
	if ww.Confidence != WhatWorkedMature1 {
		t.Fatalf("after one independent session what-worked must mature to %v, got %v", WhatWorkedMature1, ww.Confidence)
	}

	run("s3", 3)
	assets, _ = store.ListAssets(ctx)
	for _, a := range assets {
		if a.Kind == KindWhatWorked {
			ww = a
		}
	}
	if ww.Confidence != WhatWorkedMature2 {
		t.Fatalf("after two independent sessions what-worked must mature to %v, got %v", WhatWorkedMature2, ww.Confidence)
	}
}

// Coding kinds: a remembered test command that fails with test-fail/build IS
// contradicted (repository evolved); a timeout is not.
func TestCommandContradictionSemantics(t *testing.T) {
	if !contradicts(KindTestCommand, CauseTestFail) {
		t.Fatal("test-command must be contradicted by test-fail")
	}
	if !contradicts(KindTestCommand, CauseBuild) {
		t.Fatal("test-command must be contradicted by build failure")
	}
	if contradicts(KindTestCommand, CauseTimeout) {
		t.Fatal("test-command must not be contradicted by timeout")
	}
	if contradicts(KindTestCommand, "") {
		t.Fatal("unknown cause must not contradict")
	}
	if !contradicts(KindNav, CauseNotFound) {
		t.Fatal("navigation must be contradicted when the file is gone")
	}
	if contradicts(KindNav, CauseTestFail) {
		t.Fatal("navigation must not be contradicted by test failures")
	}
}
