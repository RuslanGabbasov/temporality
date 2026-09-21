package memory

import (
	"context"
	"strings"
	"testing"

	"github.com/temporality-project/temporality/aml/harness"
	"github.com/temporality-project/temporality/aml/llm"
	"github.com/temporality-project/temporality/aml/opsenv"
)

func apiCall(service, resource, auth string, params map[string]any) llm.ToolCall {
	if params == nil {
		params = map[string]any{}
	}
	return llm.ToolCall{ID: "c", Name: "api_call", Args: map[string]any{
		"service": service, "resource": resource, "auth": auth, "params": params,
	}}
}

func TestExtractAuthSwitch(t *testing.T) {
	log := []CallRecord{
		{Seq: 1, Tool: "api_call", Service: opsenv.SvcBilling, Resource: "invoices.list", Auth: "pat", Environment: "prod", Status: 401, Cause: CauseAuth},
		{Seq: 2, Tool: "api_call", Service: opsenv.SvcBilling, Resource: "invoices.list", Auth: "pat", Environment: "prod", Status: 401, Cause: CauseAuth},
		{Seq: 3, Tool: "api_call", Service: opsenv.SvcBilling, Resource: "invoices.list", Auth: "oauth", Environment: "prod", Status: 200, OK: true},
	}
	assets := ExtractExperiences(log, ExtractOptions{SessionID: "s1", TaskID: "T1", Environment: "prod", Version: "v3", Success: true})
	if len(assets) != 1 {
		t.Fatalf("assets = %d, want 1: %+v", len(assets), assets)
	}
	a := assets[0]
	if a.Kind != KindStrategySwitch || a.Recommendation.Param != "auth" || a.Recommendation.Value != "oauth" || a.Recommendation.Avoid != "pat" {
		t.Fatalf("bad recommendation: %+v", a.Recommendation)
	}
	if a.VersionContext != "v3" {
		t.Fatalf("version context = %q, want v3", a.VersionContext)
	}
	if a.Confidence != 0.7 {
		t.Fatalf("confidence = %v", a.Confidence)
	}
}

func TestExtractParamRequirement(t *testing.T) {
	log := []CallRecord{
		{Seq: 1, Tool: "api_call", Service: opsenv.SvcNotifications, Resource: "templates.list", Auth: "none", Environment: "prod", Status: 400, Cause: CauseParameter},
		{Seq: 2, Tool: "api_call", Service: opsenv.SvcNotifications, Resource: "templates.list", Auth: "none", Environment: "prod", Params: map[string]any{"channel": "email"}, Status: 200, OK: true},
	}
	assets := ExtractExperiences(log, ExtractOptions{SessionID: "s1", TaskID: "T3", Environment: "prod", Success: true})
	if len(assets) != 1 || assets[0].Kind != KindParamRequired {
		t.Fatalf("assets = %+v", assets)
	}
	if assets[0].Recommendation.Param != "params.channel" || assets[0].Recommendation.Value != "email" {
		t.Fatalf("bad recommendation: %+v", assets[0].Recommendation)
	}
}

func TestExtractPaginationChain(t *testing.T) {
	log := []CallRecord{
		{Seq: 1, Tool: "api_call", Service: opsenv.SvcSearch, Resource: "documents.search", Auth: "pat", Environment: "prod", Params: map[string]any{"query": "x"}, Status: 200, OK: true},
		{Seq: 2, Tool: "api_call", Service: opsenv.SvcSearch, Resource: "documents.search", Auth: "pat", Environment: "prod", Params: map[string]any{"query": "x", "cursor": "c2"}, Status: 200, OK: true},
		{Seq: 3, Tool: "api_call", Service: opsenv.SvcSearch, Resource: "documents.search", Auth: "pat", Environment: "prod", Params: map[string]any{"query": "x", "cursor": "c3"}, Status: 200, OK: true},
	}
	assets := ExtractExperiences(log, ExtractOptions{SessionID: "s1", TaskID: "T4", Environment: "prod", Success: true})
	found := false
	for _, a := range assets {
		if a.Kind == KindPagination {
			found = true
		}
	}
	if !found {
		t.Fatalf("no pagination asset: %+v", assets)
	}
}

func TestExtractionSkipsFailedTasks(t *testing.T) {
	log := []CallRecord{
		{Seq: 1, Tool: "api_call", Service: opsenv.SvcBilling, Resource: "invoices.list", Auth: "pat", Status: 401},
		{Seq: 2, Tool: "api_call", Service: opsenv.SvcBilling, Resource: "invoices.list", Auth: "oauth", Status: 200, OK: true},
	}
	if assets := ExtractExperiences(log, ExtractOptions{SessionID: "s", TaskID: "T", Success: false}); len(assets) != 0 {
		t.Fatalf("failed task produced assets: %+v", assets)
	}
}

// Flip separation: the pre-flip oauth asset and the post-flip pat asset must
// be distinct dedup keys, so the new strategy can mature while the old one
// goes stale.
func TestExtractSeparatesAssetsByValueAndEnvironment(t *testing.T) {
	preFlip := []CallRecord{
		{Seq: 1, Tool: "api_call", Service: opsenv.SvcBilling, Resource: "invoices.list", Auth: "pat", Status: 403, Cause: CauseAuth},
		{Seq: 2, Tool: "api_call", Service: opsenv.SvcBilling, Resource: "invoices.list", Auth: "oauth", Status: 200, OK: true},
	}
	postFlip := []CallRecord{
		{Seq: 1, Tool: "api_call", Service: opsenv.SvcBilling, Resource: "invoices.list", Auth: "oauth", Status: 403, Cause: CauseAuth},
		{Seq: 2, Tool: "api_call", Service: opsenv.SvcBilling, Resource: "invoices.list", Auth: "pat", Status: 200, OK: true},
	}
	staging := []CallRecord{
		{Seq: 1, Tool: "api_call", Service: opsenv.SvcBilling, Resource: "invoices.list", Auth: "pat", Environment: "staging", Status: 403, Cause: CauseAuth},
		{Seq: 2, Tool: "api_call", Service: opsenv.SvcBilling, Resource: "invoices.list", Auth: "oauth", Environment: "staging", Status: 200, OK: true},
	}
	a1 := ExtractExperiences(preFlip, ExtractOptions{SessionID: "s1", TaskID: "T1", Environment: "prod", Version: "v3", Success: true})
	a2 := ExtractExperiences(postFlip, ExtractOptions{SessionID: "s6", TaskID: "T1", Environment: "prod", Version: "v5", Success: true})
	a3 := ExtractExperiences(staging, ExtractOptions{SessionID: "s6", TaskID: "S1", Environment: "staging", Version: "v4", Success: true})
	if len(a1) != 1 || len(a2) != 1 || len(a3) != 1 {
		t.Fatalf("want 1 asset each: %+v %+v %+v", a1, a2, a3)
	}
	if a1[0].DedupKey == a2[0].DedupKey {
		t.Fatalf("flip must produce a separate asset, got same key")
	}
	if a1[0].DedupKey == a3[0].DedupKey {
		t.Fatalf("staging must produce a separate asset, got same key")
	}
}

func TestRankSkipsForeignEnvironmentInFullPipeline(t *testing.T) {
	prod := &Asset{DedupKey: "a", Service: opsenv.SvcBilling, Problem: "auth", Kind: KindStrategySwitch,
		Proposition: "billing-api invoices.list auth oauth works", Confidence: 0.7, Status: StatusActive,
		Recommendation: Recommendation{Service: opsenv.SvcBilling, Resource: "invoices.list", Param: "auth", Value: "oauth"},
		Environment:    "staging", LastConfirmedSession: 9}
	req := RankRequest{TaskText: "list invoices in billing-api", Service: opsenv.SvcBilling, Environment: "prod", SessionIndex: 10}
	// Full pipeline: a staging memory is not a candidate for a prod task.
	full := Rank([]*Asset{prod}, req, DefaultWeights, false)
	if len(full) != 0 {
		t.Fatalf("foreign-environment asset must be skipped in full ranking: %+v", full)
	}
	// Semantic-only (arm B): the same asset remains a candidate — the conflict
	// failure mode the conflict fixture exposes.
	sem := Rank([]*Asset{prod}, req, DefaultWeights, true)
	if len(sem) != 1 || sem[0].Score < Threshold {
		t.Fatalf("semantic-only must keep the foreign asset injectable: %+v", sem)
	}
}

func TestRankVersionMismatchHalvesScore(t *testing.T) {
	v3 := &Asset{DedupKey: "a", Service: opsenv.SvcBilling, Problem: "auth", Kind: KindStrategySwitch,
		Proposition: "billing-api invoices.list auth oauth works", Confidence: 0.9, Status: StatusActive,
		Recommendation: Recommendation{Service: opsenv.SvcBilling, Resource: "invoices.list", Param: "auth", Value: "oauth"},
		Environment:    "prod", VersionContext: "v3", LastConfirmedSession: 1}
	same := Rank([]*Asset{v3}, RankRequest{TaskText: "list invoices in billing-api", Service: opsenv.SvcBilling, Environment: "prod", Version: "v3", SessionIndex: 2}, DefaultWeights, false)[0]
	mismatch := Rank([]*Asset{v3}, RankRequest{TaskText: "list invoices in billing-api", Service: opsenv.SvcBilling, Environment: "prod", Version: "v5", SessionIndex: 2}, DefaultWeights, false)[0]
	if same.Score <= mismatch.Score || same.Score > 2*mismatch.Score+0.001 {
		t.Fatalf("version mismatch should roughly halve the score: same=%v mismatch=%v", same.Score, mismatch.Score)
	}
}

// Recency ≠ relevance: an older confirmed prod asset must outrank a fresh
// staging asset for a prod task (experiment 2 §7).
func TestRankRecencyIsNotRelevance(t *testing.T) {
	oldProd := &Asset{DedupKey: "old", Service: opsenv.SvcBilling, Problem: "auth", Kind: KindStrategySwitch,
		Proposition: "billing-api invoices.list auth oauth works", Confidence: 0.95, Status: StatusActive,
		Recommendation: Recommendation{Service: opsenv.SvcBilling, Resource: "invoices.list", Param: "auth", Value: "oauth"},
		Environment:    "prod", LastConfirmedSession: 1}
	freshStaging := &Asset{DedupKey: "fresh", Service: opsenv.SvcBilling, Problem: "auth", Kind: KindStrategySwitch,
		Proposition: "billing-api staging invoices.list auth oauth works", Confidence: 0.55, Status: StatusActive,
		Recommendation: Recommendation{Service: opsenv.SvcBilling, Resource: "invoices.list", Param: "auth", Value: "oauth"},
		Environment:    "staging", LastConfirmedSession: 9}
	scored := Rank([]*Asset{freshStaging, oldProd}, RankRequest{TaskText: "list invoices in billing-api prod", Service: opsenv.SvcBilling, Environment: "prod", SessionIndex: 10}, DefaultWeights, false)
	if scored[0].Asset.DedupKey != "old" {
		t.Fatalf("older prod asset must outrank fresh staging asset for a prod task: %+v", scored)
	}
}

func TestRankSemanticOnlyUsesScopeButIgnoresConfidence(t *testing.T) {
	strong := &Asset{DedupKey: "k1", Service: opsenv.SvcBilling, Proposition: "billing invoices oauth",
		Confidence: 0.9, Status: StatusActive, Recommendation: Recommendation{Service: opsenv.SvcBilling, Value: "oauth"}, Environment: "prod"}
	weak := &Asset{DedupKey: "k2", Service: opsenv.SvcBilling, Proposition: "billing invoices oauth",
		Confidence: 0.2, Status: StatusActive, Recommendation: Recommendation{Service: opsenv.SvcBilling, Value: "oauth"}, Environment: "prod"}
	req := RankRequest{TaskText: "billing invoices oauth", Service: opsenv.SvcBilling, Environment: "prod", SessionIndex: 1}
	scored := Rank([]*Asset{weak, strong}, req, DefaultWeights, true)
	// Identical scope and similarity must yield identical scores regardless of
	// confidence (arm B ignores confidence by design).
	if len(scored) != 2 || scored[0].Score != scored[1].Score {
		t.Fatalf("confidence leaked into semantic-only score: %+v", scored)
	}
	if scored[0].Score < 0.35 {
		t.Fatalf("scope match should clear the injection threshold: %v", scored[0].Score)
	}
	// Out-of-scope assets must rank far below in-scope ones.
	other := &Asset{DedupKey: "k3", Service: opsenv.SvcPayments, Proposition: "billing invoices oauth",
		Confidence: 0.9, Status: StatusActive, Recommendation: Recommendation{Service: opsenv.SvcPayments, Value: "pat"}, Environment: "prod"}
	scored = Rank([]*Asset{other, strong}, req, DefaultWeights, true)
	if scored[0].Asset.DedupKey != "k1" {
		t.Fatalf("in-scope asset must outrank out-of-scope: %+v", scored)
	}
}

// TestLayerFeedbackLoop walks RECALLED → INJECTED → REUSED → CONTRADICTED →
// WEAKENED: an oauth asset injected after the flip (prod v5, pat only) must be
// contradicted with cause attribution and weakened.
func TestLayerFeedbackLoop(t *testing.T) {
	store := NewMemStore()
	layer := NewLayer(store, ConfigForArmTest("D"), opsenv.AllServices, nil)
	layer.SetSession("s6", 6)
	// The layer still believes prod is v3 (stale version metadata): the v3
	// oauth memory stays injectable, gets reused and must be contradicted by
	// the gateway — the fallback path when version context is unavailable.
	layer.SetTaskContext("prod", "v3")

	seed := &Asset{DedupKey: "seed", Service: opsenv.SvcBilling, Problem: "auth", Kind: KindStrategySwitch,
		Proposition:          "on billing-api invoices.list (environment prod) auth=\"pat\" failed 1 times with HTTP 403; switching auth=\"oauth\" succeeded",
		Confidence:           0.9,
		Status:               StatusActive,
		Recommendation:       Recommendation{Service: opsenv.SvcBilling, Resource: "invoices.list", Param: "auth", Value: "oauth", Avoid: "pat"},
		Environment:          "prod",
		VersionContext:       "v3",
		SourceSessions:       []string{"s1"},
		LastConfirmedSession: 1,
	}
	seed.ID = NewUUID()
	if err := store.UpsertAsset(context.Background(), seed, true); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	hints := layer.TaskStart(ctx, "s6", "T1", "Using the internal API, list the invoices in the billing-api service and report the id and amount of the second invoice.")
	if len(hints) != 1 {
		t.Fatalf("hints = %d (%v), want 1", len(hints), hints)
	}

	// Agent follows the recommendation in the flipped world → oauth → 403 auth.
	call := apiCall(opsenv.SvcBilling, "invoices.list", "oauth", nil)
	layer.BeforeToolCall(ctx, "s6", call)
	layer.AfterToolCall(ctx, "s6", call, harness.ToolResult{OK: false, Status: 403, Text: "HTTP 403", Cause: CauseAuth})

	stats := layer.Stats()
	if stats.Reused != 1 || stats.Contradicted != 1 || stats.Unresolved != 0 {
		t.Fatalf("stats = %+v", stats)
	}
	assets, _ := store.ListAssets(ctx)
	if assets[0].Confidence >= 0.9 || assets[0].ContradictionCount != 1 {
		t.Fatalf("asset not weakened: %+v", assets[0])
	}
	if assets[0].Status != StatusStale {
		// 0.9*0.45 = 0.405 > 0.35 → not stale after one contradiction; require ≥2 rule
		if assets[0].ConsecutiveContradictions != 1 {
			t.Fatalf("contradiction tracking broken: %+v", assets[0])
		}
	}
}

// Experiment 2 §8: a 400 parameter failure while testing an auth memory is
// UNRESOLVED and must not weaken the memory.
func TestAttributionParameterFailureDoesNotWeakenAuthAsset(t *testing.T) {
	store := NewMemStore()
	layer := NewLayer(store, ConfigForArmTest("D"), opsenv.AllServices, nil)
	layer.SetSession("s1", 1)
	layer.SetTaskContext("prod", "v3")

	seed := &Asset{DedupKey: "seed", Service: opsenv.SvcBilling, Problem: "auth", Kind: KindStrategySwitch,
		Proposition:          "oauth works on billing-api invoices.get",
		Confidence:           0.9,
		Status:               StatusActive,
		Recommendation:       Recommendation{Service: opsenv.SvcBilling, Resource: "invoices.get", Param: "auth", Value: "oauth"},
		Environment:          "prod",
		SourceSessions:       []string{"s0"},
		LastConfirmedSession: 0,
	}
	seed.ID = NewUUID()
	if err := store.UpsertAsset(context.Background(), seed, true); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	layer.TaskStart(ctx, "s1", "T5", "In billing-api, get the details of invoice INV-2007 and report its vendor.")
	// oauth reused but version param wrong → 400 parameter.
	call := apiCall(opsenv.SvcBilling, "invoices.get", "oauth", map[string]any{"id": "INV-2007", "version": "1"})
	layer.BeforeToolCall(ctx, "s1", call)
	layer.AfterToolCall(ctx, "s1", call, harness.ToolResult{OK: false, Status: 400, Text: "HTTP 400", Cause: CauseParameter})

	stats := layer.Stats()
	if stats.Reused != 1 || stats.Unresolved != 1 || stats.Contradicted != 0 {
		t.Fatalf("stats = %+v", stats)
	}
	assets, _ := store.ListAssets(ctx)
	if assets[0].Confidence != 0.9 || assets[0].ContradictionCount != 0 {
		t.Fatalf("unrelated failure must not weaken auth memory: %+v", assets[0])
	}
}

// Unknown cause → UNRESOLVED, confidence unchanged.
func TestAttributionUnknownCauseIsUnresolved(t *testing.T) {
	store := NewMemStore()
	layer := NewLayer(store, ConfigForArmTest("D"), opsenv.AllServices, nil)
	layer.SetSession("s1", 1)
	layer.SetTaskContext("prod", "")

	seed := &Asset{DedupKey: "seed", Service: opsenv.SvcFlaky, Problem: "request-params", Kind: KindParamRequired,
		Proposition:          "flaky-gw reports.get requires params.mirror=true",
		Confidence:           0.8,
		Status:               StatusActive,
		Recommendation:       Recommendation{Service: opsenv.SvcFlaky, Resource: "reports.get", Param: "params.mirror", Value: "true"},
		Environment:          "prod",
		SourceSessions:       []string{"s0"},
		LastConfirmedSession: 0,
	}
	seed.ID = NewUUID()
	if err := store.UpsertAsset(context.Background(), seed, true); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	layer.TaskStart(ctx, "s1", "G1", "The flaky-gw service serves the monthly operations report. Fetch it and report the report id and the mode it was served in.")
	call := apiCall(opsenv.SvcFlaky, "reports.get", "pat", map[string]any{"mirror": true})
	layer.BeforeToolCall(ctx, "s1", call)
	layer.AfterToolCall(ctx, "s1", call, harness.ToolResult{OK: false, Status: 503, Text: "HTTP 503", Cause: "server"})

	stats := layer.Stats()
	if stats.Reused != 1 || stats.Unresolved != 1 {
		t.Fatalf("server failure must not contradict param memory: %+v", stats)
	}
	assets, _ := store.ListAssets(ctx)
	if assets[0].Confidence != 0.8 {
		t.Fatalf("confidence must not move on UNRESOLVED: %+v", assets[0])
	}
}

// Experiment 2 §4: same-session merges must not grow confidence; only a new
// independent session counts.
func TestSameSessionMergeDoesNotBumpConfidence(t *testing.T) {
	store := NewMemStore()
	layer := NewLayer(store, ConfigForArmTest("D"), opsenv.AllServices, nil)
	layer.SetSession("s1", 1)
	layer.SetTaskContext("prod", "v3")
	ctx := context.Background()

	run := func(taskID string) {
		layer.TaskStart(ctx, "s1", taskID, "list the invoices in the billing-api service")
		pat := apiCall(opsenv.SvcBilling, "invoices.list", "pat", nil)
		oauth := apiCall(opsenv.SvcBilling, "invoices.list", "oauth", nil)
		layer.BeforeToolCall(ctx, "s1", pat)
		layer.AfterToolCall(ctx, "s1", pat, harness.ToolResult{OK: false, Status: 403, Text: "HTTP 403", Cause: CauseAuth})
		layer.BeforeToolCall(ctx, "s1", oauth)
		layer.AfterToolCall(ctx, "s1", oauth, harness.ToolResult{OK: true, Status: 200, Text: `HTTP 200 {"invoices":[...]}`})
		layer.TaskEnd(ctx, "s1", taskID, true, "INV-1002 3400")
	}
	run("T1")
	assets, _ := store.ListAssets(ctx)
	if len(assets) != 1 || assets[0].Confidence != 0.7 || assets[0].ConfirmationCount != 1 {
		t.Fatalf("initial asset wrong: %+v", assets)
	}
	// Second merge within the SAME session: reuse may add a small weighted bump
	// (factor 0.2), but the merge itself must not count as an independent
	// confirmation — confidence growth stays marginal and count stays 1.
	run("T1b")
	assets, _ = store.ListAssets(ctx)
	if len(assets) != 1 {
		t.Fatalf("want dedup to 1, got %d", len(assets))
	}
	if assets[0].ConfirmationCount != 1 {
		t.Fatalf("same-session merge must not grow the session count: %+v", assets[0])
	}
	if assets[0].Confidence > 0.72 {
		t.Fatalf("same-session confidence growth must stay marginal (≤0.72): %+v", assets[0])
	}
	sameSessionConf := assets[0].Confidence
	// New session merge: full-weight reinforcement and a new confirmation.
	layer.SetSession("s2", 2)
	layer.SetTaskContext("prod", "v3")
	layer.TaskStart(ctx, "s2", "T1", "list the invoices in the billing-api service")
	pat := apiCall(opsenv.SvcBilling, "invoices.list", "pat", nil)
	oauth := apiCall(opsenv.SvcBilling, "invoices.list", "oauth", nil)
	layer.BeforeToolCall(ctx, "s2", pat)
	layer.AfterToolCall(ctx, "s2", pat, harness.ToolResult{OK: false, Status: 403, Text: "HTTP 403", Cause: CauseAuth})
	layer.BeforeToolCall(ctx, "s2", oauth)
	layer.AfterToolCall(ctx, "s2", oauth, harness.ToolResult{OK: true, Status: 200, Text: `HTTP 200 {"invoices":[...]}`})
	layer.TaskEnd(ctx, "s2", "T1", true, "INV-1002 3400")

	assets, _ = store.ListAssets(ctx)
	if assets[0].ConfirmationCount != 2 || len(assets[0].SourceSessions) != 2 {
		t.Fatalf("new-session merge must count: %+v", assets[0])
	}
	if assets[0].Confidence <= sameSessionConf+0.02 {
		t.Fatalf("new-session confirmation must outweigh same-session repeats: %+v", assets[0])
	}
}

// Staging memory must not be injected for a prod task (conflict scenario §6).
func TestLayerDoesNotInjectForeignEnvironmentForProdTask(t *testing.T) {
	store := NewMemStore()
	layer := NewLayer(store, ConfigForArmTest("D"), opsenv.AllServices, nil)
	layer.SetSession("s2", 2)
	layer.SetTaskContext("prod", "v5")

	staging := &Asset{DedupKey: "stg", Service: opsenv.SvcBilling, Problem: "auth", Kind: KindStrategySwitch,
		Proposition:          "on billing-api invoices.list (environment staging) auth=\"pat\" failed; auth=\"oauth\" succeeded",
		Confidence:           0.9,
		Status:               StatusActive,
		Recommendation:       Recommendation{Service: opsenv.SvcBilling, Resource: "invoices.list", Param: "auth", Value: "oauth"},
		Environment:          "staging",
		VersionContext:       "v4",
		SourceSessions:       []string{"s1"},
		LastConfirmedSession: 1,
	}
	staging.ID = NewUUID()
	if err := store.UpsertAsset(context.Background(), staging, true); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	hints := layer.TaskStart(ctx, "s2", "P1", "Using the internal API, list the invoices in the billing-api production environment and report the id and amount of the first invoice.")
	if len(hints) != 0 {
		t.Fatalf("staging hint must not be injected for a prod task: %v", hints)
	}
}

func TestLayerGuardrailFiresOnce(t *testing.T) {
	store := NewMemStore()
	layer := NewLayer(store, ConfigForArmTest("D"), opsenv.AllServices, nil)
	layer.SetSession("s1", 1)
	layer.SetTaskContext("prod", "")
	ctx := context.Background()
	layer.TaskStart(ctx, "s1", "G1", "The flaky-gw service serves the monthly operations report.")

	call := apiCall(opsenv.SvcFlaky, "reports.get", "pat", nil)
	var hints []harness.Hint
	for i := 0; i < 4; i++ {
		hints = append(hints, layer.BeforeToolCall(ctx, "s1", call)...)
		layer.AfterToolCall(ctx, "s1", call, harness.ToolResult{OK: false, Status: 503, Text: "HTTP 503", Cause: CauseServer})
	}
	guardrails := 0
	for _, h := range hints {
		if len(h.Text) > 0 && containsStr(h.Text, "already tried") {
			guardrails++
		}
	}
	if guardrails != 1 {
		t.Fatalf("guardrail fired %d times, want 1 (hints: %v)", guardrails, hints)
	}
	if layer.Stats().RepeatedFailedCall != 3 {
		t.Fatalf("repeated failed = %d, want 3", layer.Stats().RepeatedFailedCall)
	}
}

func TestLayerExtractionPersistsAcrossTasks(t *testing.T) {
	store := NewMemStore()
	layer := NewLayer(store, ConfigForArmTest("C"), opsenv.AllServices, nil)
	layer.SetSession("s1", 1)
	layer.SetTaskContext("prod", "v3")
	ctx := context.Background()
	layer.TaskStart(ctx, "s1", "T1", "list the invoices in the billing-api service")

	pat := apiCall(opsenv.SvcBilling, "invoices.list", "pat", nil)
	oauth := apiCall(opsenv.SvcBilling, "invoices.list", "oauth", nil)
	layer.BeforeToolCall(ctx, "s1", pat)
	layer.AfterToolCall(ctx, "s1", pat, harness.ToolResult{OK: false, Status: 403, Text: "HTTP 403", Cause: CauseAuth})
	layer.BeforeToolCall(ctx, "s1", oauth)
	layer.AfterToolCall(ctx, "s1", oauth, harness.ToolResult{OK: true, Status: 200, Text: `HTTP 200 {"invoices":[...]}`})
	layer.TaskEnd(ctx, "s1", "T1", true, "INV-1002 3400")

	assets, _ := store.ListAssets(ctx)
	if len(assets) != 1 {
		t.Fatalf("assets = %d, want 1", len(assets))
	}
	if assets[0].Recommendation.Value != "oauth" || assets[0].ConfirmationCount != 1 {
		t.Fatalf("bad asset: %+v", assets[0])
	}

	// Second session: the same transition merges instead of duplicating.
	layer.SetSession("s2", 2)
	layer.SetTaskContext("prod", "v3")
	layer.TaskStart(ctx, "s2", "T1", "list the invoices in the billing-api service")
	layer.BeforeToolCall(ctx, "s2", pat)
	layer.AfterToolCall(ctx, "s2", pat, harness.ToolResult{OK: false, Status: 403, Text: "HTTP 403", Cause: CauseAuth})
	layer.BeforeToolCall(ctx, "s2", oauth)
	layer.AfterToolCall(ctx, "s2", oauth, harness.ToolResult{OK: true, Status: 200, Text: `HTTP 200 {"invoices":[...]}`})
	layer.TaskEnd(ctx, "s2", "T1", true, "INV-1002 3400")

	assets, _ = store.ListAssets(ctx)
	if len(assets) != 1 {
		t.Fatalf("assets = %d, want dedup to 1", len(assets))
	}
	if assets[0].ConfirmationCount != 2 || len(assets[0].SourceSessions) != 2 {
		t.Fatalf("merge failed: %+v", assets[0])
	}
}

// Lifecycle events must be recorded in order for one reuse: RECALLED,
// INJECTED, REUSED, VALIDATED (+REINFORCED under feedback).
func TestLifecycleEventsRecorded(t *testing.T) {
	store := NewMemStore()
	layer := NewLayer(store, ConfigForArmTest("D"), opsenv.AllServices, nil)
	layer.SetSession("s1", 1)
	layer.SetTaskContext("prod", "v3")
	ctx := context.Background()

	layer.TaskStart(ctx, "s1", "T1", "list the invoices in the billing-api service")
	pat := apiCall(opsenv.SvcBilling, "invoices.list", "pat", nil)
	oauth := apiCall(opsenv.SvcBilling, "invoices.list", "oauth", nil)
	layer.BeforeToolCall(ctx, "s1", pat)
	layer.AfterToolCall(ctx, "s1", pat, harness.ToolResult{OK: false, Status: 403, Text: "HTTP 403", Cause: CauseAuth})
	layer.BeforeToolCall(ctx, "s1", oauth)
	layer.AfterToolCall(ctx, "s1", oauth, harness.ToolResult{OK: true, Status: 200, Text: `HTTP 200 {"invoices":[...]}`})
	layer.TaskEnd(ctx, "s1", "T1", true, "INV-1002 3400")

	// Next session: inject and reuse successfully.
	layer.SetSession("s2", 2)
	layer.SetTaskContext("prod", "v3")
	layer.TaskStart(ctx, "s2", "T1", "list the invoices in the billing-api service")
	layer.BeforeToolCall(ctx, "s2", oauth)
	layer.AfterToolCall(ctx, "s2", oauth, harness.ToolResult{OK: true, Status: 200, Text: `HTTP 200 {"invoices":[...]}`})

	types := map[string]int{}
	for _, e := range store.Events() {
		types[e.Type]++
	}
	for _, want := range []string{EventExtract, EventRecalled, EventInject, EventReuse, EventValidated, EventReinforced} {
		if types[want] == 0 {
			t.Fatalf("missing lifecycle event %s; got %v", want, types)
		}
	}
}

// ConfigForArmTest is ConfigForArm without the error for tests.
func ConfigForArmTest(arm string) Config {
	cfg, err := ConfigForArm(arm)
	if err != nil {
		panic(err)
	}
	return cfg
}

func containsStr(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}
