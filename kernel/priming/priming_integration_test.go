package priming

// Integration tests for Experience Priming.
//
// Unlike the unit tests next to them, these run the whole pipeline over
// HTTP: a fake journal serves a fixed 60-item knowledge corpus through the
// same contract as the real one (GET /v1/observations/knowledge?project=P
// answering {"knowledge": [...], "count": N}), and every assertion is made
// against the PrimingResult of a full Prime() call.
//
// Corpus layout (60 items, 6 scopes over the 5 required themes; the
// database/migrations theme deliberately forms two scopes so that a
// proposed-leaning pattern and an invalidated-majority pattern can be
// compared at identical relevance):
//
//	deploy     12 items: 8 confirmed, 2 proposed, 1 invalidated, 1 superseded
//	database   10 items: 3 confirmed, 5 proposed, 1 invalidated, 1 corrected
//	migrations  8 items: 1 confirmed, 1 proposed, 1 challenged, 5 invalidated
//	auth       10 items: 9 confirmed, 1 superseded
//	testing    10 items: 6 confirmed, 1 proposed, 1 challenged, 1 corrected, 1 invalidated
//	prompts    10 items: 6 confirmed, 2 proposed, 1 challenged, 1 invalidated
//
// ReuseCount spans 0..9 and several items carry topics of a neighbouring
// theme, so token overlap crosses scope boundaries the way real knowledge
// does.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/temporality-project/temporality/observation"
)

const (
	integrationProject = "temporality-qa"
	integrationToken   = "qa-priming-token"

	// deployTask mentions a deployment rollback; its tokens live in the
	// deploy items and (through "rollback" and cross topics) in the
	// database-theme items.
	deployTask = "deploy rollback blue green switch staging"

	// migrationTask is the controlled task of the invalidation test. Every
	// database-theme item matches exactly {migration, rollback} out of it
	// and never {downtime, window}, so both database-theme scopes receive
	// identical relevance (max overlap 1/2, weighted mean 1/2, scope
	// overlap 0) and only their validity mix can separate them.
	migrationTask = "migration rollback downtime window"

	// irrelevantTask tokens appear nowhere in the corpus.
	irrelevantTask = "quokka nebula xylophone kaleidoscope origami"
)

// areaTopics maps a theme to its topic vocabulary. "migrations" belongs to
// the database theme, which is why the database theme legitimately yields
// two scopes (items whose first topic is "database" vs "migrations").
var areaTopics = map[string][]string{
	"deploy":   {"deploy", "pipeline"},
	"database": {"database", "migrations"},
	"auth":     {"auth", "oauth"},
	"testing":  {"testing", "flaky"},
	"llm":      {"prompts", "llm"},
}

func topicArea(topic string) string {
	for area, topics := range areaTopics {
		for _, candidate := range topics {
			if candidate == topic {
				return area
			}
		}
	}
	return ""
}

// corpusRow is one fixture row of the integration corpus.
type corpusRow struct {
	id          string
	proposition string
	state       string
	topics      []string
	entities    []string
	reuse       int
	atRisk      bool
}

// integrationCorpus returns the deterministic 60-item fixture.
func integrationCorpus() []corpusRow {
	return []corpusRow{
		// --- deploy/pipeline ------------------------------------------------
		{id: "K-DEP-001", proposition: "blue green switch removes deploy downtime on staging", state: "confirmed", topics: []string{"deploy", "pipeline"}, entities: []string{"service-payments"}, reuse: 9},
		{id: "K-DEP-002", proposition: "rollback plan must tag the previous release before deploy", state: "confirmed", topics: []string{"deploy", "pipeline"}, reuse: 7},
		{id: "K-DEP-003", proposition: "canary rollout catches broken builds before full deploy", state: "confirmed", topics: []string{"deploy", "pipeline"}, reuse: 5},
		{id: "K-DEP-004", proposition: "deploy pipeline freezes during release window", state: "confirmed", topics: []string{"deploy", "pipeline", "database"}, reuse: 4},
		{id: "K-DEP-005", proposition: "rollback requires the release tag and a verified backup", state: "confirmed", topics: []string{"deploy", "pipeline"}, reuse: 3},
		{id: "K-DEP-006", proposition: "staging deploy must precede production rollout", state: "confirmed", topics: []string{"deploy", "pipeline"}, reuse: 2},
		{id: "K-DEP-007", proposition: "automated rollback triggers when canary error rate climbs", state: "confirmed", topics: []string{"deploy", "pipeline"}, reuse: 1},
		{id: "K-DEP-008", proposition: "deploy hooks drain the queue before switching traffic", state: "confirmed", topics: []string{"deploy", "pipeline"}, reuse: 1},
		{id: "K-DEP-009", proposition: "pipeline cache speeds up repeated deploy runs", state: "proposed", topics: []string{"deploy", "pipeline"}, reuse: 2},
		{id: "K-DEP-010", proposition: "deploy checklists prevent skipped smoke tests", state: "proposed", topics: []string{"deploy", "pipeline"}},
		{id: "K-DEP-011", proposition: "deploy by copying files manually", state: "invalidated", topics: []string{"deploy", "pipeline"}, reuse: 3},
		{id: "K-DEP-012", proposition: "single host deploy is enough for staging", state: "superseded", topics: []string{"deploy", "pipeline"}, reuse: 1},

		// --- database/migrations, scope "database" (proposed-leaning) ------
		// Every proposition contains exactly the tokens migration+rollback
		// out of migrationTask; see the control asserted in
		// requireCorpusShape.
		{id: "K-DB-001", proposition: "every migration needs a tested rollback script", state: "confirmed", topics: []string{"database", "migrations"}, reuse: 4},
		{id: "K-DB-002", proposition: "a long migration without rollback locks the accounts table", state: "confirmed", topics: []string{"database", "migrations"}, entities: []string{"table-accounts"}, reuse: 3},
		{id: "K-DB-003", proposition: "wrap schema changes in a migration with rollback", state: "confirmed", topics: []string{"database", "migrations"}, reuse: 2},
		{id: "K-DB-004", proposition: "migration rollback rehearsal keeps releases boring", state: "proposed", topics: []string{"database", "migrations", "deploy"}, reuse: 1},
		{id: "K-DB-005", proposition: "expand and contract makes migration rollback reversible", state: "proposed", topics: []string{"database", "migrations"}, reuse: 1},
		{id: "K-DB-006", proposition: "generate migration rollback from the schema diff", state: "proposed", topics: []string{"database", "migrations"}},
		{id: "K-DB-007", proposition: "run migration rollback drills before the release train", state: "proposed", topics: []string{"database", "migrations", "deploy"}, reuse: 1},
		{id: "K-DB-008", proposition: "pair every migration rollback with a forward path", state: "proposed", topics: []string{"database", "migrations"}},
		{id: "K-DB-009", proposition: "hotfix schema edits skip migration rollback entirely", state: "invalidated", topics: []string{"database", "migrations"}, reuse: 2},
		{id: "K-DB-010", proposition: "migration rollback means restoring yesterday dump", state: "corrected", topics: []string{"database", "migrations"}, reuse: 1, atRisk: true},

		// --- database/migrations, scope "migrations" (invalidated-majority)
		{id: "K-MIG-001", proposition: "migration rollback drills belong in the runbook", state: "confirmed", topics: []string{"migrations", "database"}, reuse: 2},
		{id: "K-MIG-002", proposition: "track migration rollback duration per environment", state: "proposed", topics: []string{"migrations", "database"}, reuse: 1, atRisk: true},
		{id: "K-MIG-003", proposition: "migration rollback is always safer than a forward fix", state: "challenged", topics: []string{"migrations", "database"}, reuse: 1},
		{id: "K-MIG-004", proposition: "migration rollback can run unattended at night", state: "invalidated", topics: []string{"migrations"}, reuse: 1},
		{id: "K-MIG-005", proposition: "copy migration rollback steps from the old wiki", state: "invalidated", topics: []string{"migrations"}},
		{id: "K-MIG-006", proposition: "skip migration rollback for trivial changes", state: "invalidated", topics: []string{"migrations"}},
		{id: "K-MIG-007", proposition: "manual migration rollback beats tooling", state: "invalidated", topics: []string{"migrations", "deploy"}, reuse: 1},
		{id: "K-MIG-008", proposition: "migration rollback tickets live in a team spreadsheet", state: "invalidated", topics: []string{"migrations"}},

		// --- auth/oauth -----------------------------------------------------
		{id: "K-AUT-001", proposition: "oauth device flow suits headless agents", state: "confirmed", topics: []string{"auth", "oauth"}, entities: []string{"provider-sso"}, reuse: 2},
		{id: "K-AUT-002", proposition: "rotate refresh tokens on every use", state: "confirmed", topics: []string{"auth", "oauth"}, reuse: 1},
		{id: "K-AUT-003", proposition: "scope oauth grants to the minimum needed", state: "confirmed", topics: []string{"auth", "oauth"}, reuse: 1},
		{id: "K-AUT-004", proposition: "short lived access tokens limit blast radius", state: "confirmed", topics: []string{"auth", "oauth"}},
		{id: "K-AUT-005", proposition: "oauth callbacks must validate the state parameter", state: "confirmed", topics: []string{"auth", "oauth"}},
		{id: "K-AUT-006", proposition: "store oauth secrets outside the repository", state: "confirmed", topics: []string{"auth", "oauth"}},
		{id: "K-AUT-007", proposition: "audit oauth grants quarterly", state: "confirmed", topics: []string{"auth", "oauth"}},
		{id: "K-AUT-008", proposition: "oauth errors deserve explicit retry handling", state: "confirmed", topics: []string{"auth", "oauth"}},
		{id: "K-AUT-009", proposition: "oauth clients migrate to the new identity provider", state: "confirmed", topics: []string{"auth", "oauth", "testing"}},
		{id: "K-AUT-010", proposition: "personal access tokens never expire", state: "superseded", topics: []string{"auth", "oauth"}},

		// --- testing/flaky --------------------------------------------------
		{id: "K-TST-001", proposition: "quarantine flaky tests before they poison the suite", state: "confirmed", topics: []string{"testing", "flaky"}, entities: []string{"suite-e2e"}, reuse: 2},
		{id: "K-TST-002", proposition: "retry only tests marked flaky by telemetry", state: "confirmed", topics: []string{"testing", "flaky"}, reuse: 1},
		{id: "K-TST-003", proposition: "seed randomness to keep flaky assertions stable", state: "confirmed", topics: []string{"testing", "flaky"}, reuse: 1},
		{id: "K-TST-004", proposition: "flaky test budgets belong in the definition of done", state: "confirmed", topics: []string{"testing", "flaky"}},
		{id: "K-TST-005", proposition: "run flaky tests in a dedicated lane", state: "confirmed", topics: []string{"testing", "flaky"}},
		{id: "K-TST-006", proposition: "measure flakiness per test not per suite", state: "confirmed", topics: []string{"testing", "flaky"}, reuse: 1},
		{id: "K-TST-007", proposition: "auto close issues for healed flaky tests", state: "proposed", topics: []string{"testing", "flaky", "pipeline"}},
		{id: "K-TST-008", proposition: "flaky tests indicate product bugs not test bugs", state: "challenged", topics: []string{"testing", "flaky"}},
		{id: "K-TST-009", proposition: "delete flaky tests on sight", state: "corrected", topics: []string{"testing", "flaky"}},
		{id: "K-TST-010", proposition: "rerun flaky tests until they pass", state: "invalidated", topics: []string{"testing", "flaky"}, reuse: 1},

		// --- llm/prompts ----------------------------------------------------
		{id: "K-PRM-001", proposition: "keep system prompts under two hundred lines", state: "confirmed", topics: []string{"prompts", "llm"}, entities: []string{"model-router"}, reuse: 2},
		{id: "K-PRM-002", proposition: "put output contracts in the prompt header", state: "confirmed", topics: []string{"prompts", "llm"}, reuse: 1},
		{id: "K-PRM-003", proposition: "few shot examples beat long instructions", state: "confirmed", topics: []string{"prompts", "llm"}, reuse: 1},
		{id: "K-PRM-004", proposition: "escape user data inside prompt templates", state: "confirmed", topics: []string{"prompts", "llm"}},
		{id: "K-PRM-005", proposition: "version prompts like code", state: "confirmed", topics: []string{"prompts", "llm"}},
		{id: "K-PRM-006", proposition: "pin model temperature per prompt template", state: "confirmed", topics: []string{"prompts", "llm"}, reuse: 1},
		{id: "K-PRM-007", proposition: "log full prompts for every failed call", state: "proposed", topics: []string{"prompts", "llm", "testing"}},
		{id: "K-PRM-008", proposition: "draft prompts next to the code that renders them", state: "proposed", topics: []string{"prompts", "llm"}},
		{id: "K-PRM-009", proposition: "prompt length does not affect latency", state: "challenged", topics: []string{"prompts", "llm"}},
		{id: "K-PRM-010", proposition: "chain prompts without validating intermediate output", state: "invalidated", topics: []string{"prompts", "llm"}, reuse: 1},
	}
}

func buildKnowledge(rows []corpusRow) []observation.Knowledge {
	items := make([]observation.Knowledge, 0, len(rows))
	for _, row := range rows {
		items = append(items, observation.Knowledge{
			ID:          row.id,
			Proposition: row.proposition,
			State:       row.state,
			Topics:      row.topics,
			Entities:    row.entities,
			ReuseCount:  row.reuse,
			AtRisk:      row.atRisk,
		})
	}
	return items
}

// rowTokens mirrors the item token set the scorer uses: proposition plus
// topics plus entities.
func rowTokens(row corpusRow) map[string]bool {
	return tokenize(row.proposition + " " + strings.Join(row.topics, " ") + " " + strings.Join(row.entities, " "))
}

// newIntegrationJournal serves the corpus exactly the way the real journal
// does: GET /v1/observations/knowledge requires a non-empty project (400
// otherwise) and answers {"knowledge": [...], "count": N}. When a token is
// configured the request must carry it as a bearer token (401 otherwise).
func newIntegrationJournal(t *testing.T, rows []corpusRow, token string) *httptest.Server {
	t.Helper()
	corpus := buildKnowledge(rows)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet || r.URL.Path != "/v1/observations/knowledge" {
			http.Error(w, `{"error":"unexpected endpoint"}`, http.StatusNotFound)
			return
		}
		if token != "" && r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, `{"error":"missing bearer token"}`, http.StatusUnauthorized)
			return
		}
		if r.URL.Query().Get("project") == "" {
			http.Error(w, `{"error":"project is required"}`, http.StatusBadRequest)
			return
		}
		if err := json.NewEncoder(w).Encode(map[string]any{"knowledge": corpus, "count": len(corpus)}); err != nil {
			t.Errorf("fake journal encode: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// primeCorpus validates the fixture and runs the full Prime pipeline over it.
func primeCorpus(t *testing.T, config Config, task string) PrimingResult {
	t.Helper()
	rows := integrationCorpus()
	requireCorpusShape(t, rows)
	srv := newIntegrationJournal(t, rows, integrationToken)
	result, err := NewPrimer(config, srv.URL, integrationToken).Prime(context.Background(), integrationProject, task)
	if err != nil {
		t.Fatalf("Prime(project=%q, task=%q) failed: %v", integrationProject, task, err)
	}
	return result
}

func patternIDs(patterns []ExperiencePattern) []string {
	ids := make([]string, 0, len(patterns))
	for _, pat := range patterns {
		ids = append(ids, pat.ID)
	}
	return ids
}

// requireCorpusShape fails the test when the fixture no longer matches the
// assumptions the integration expectations are derived from. Every check is
// a property of the corpus itself, never of the implementation.
func requireCorpusShape(t *testing.T, rows []corpusRow) {
	t.Helper()
	if len(rows) != 60 {
		t.Fatalf("integration corpus must hold 60 items, got %d", len(rows))
	}

	wantPerScope := map[string]int{"deploy": 12, "database": 10, "migrations": 8, "auth": 10, "testing": 10, "prompts": 10}
	perScope := map[string]int{}
	states := map[string]int{}
	seen := map[string]bool{}
	crossArea := 0
	minReuse, maxReuse := 1<<30, -1
	for _, row := range rows {
		if seen[row.id] {
			t.Fatalf("duplicate corpus id %s", row.id)
		}
		seen[row.id] = true
		if len(row.topics) == 0 {
			t.Fatalf("%s has no topics", row.id)
		}
		perScope[row.topics[0]]++
		states[row.state]++
		if row.reuse < minReuse {
			minReuse = row.reuse
		}
		if row.reuse > maxReuse {
			maxReuse = row.reuse
		}
		home := topicArea(row.topics[0])
		if home == "" {
			t.Fatalf("%s: primary topic %q belongs to no known theme", row.id, row.topics[0])
		}
		for _, topic := range row.topics[1:] {
			if area := topicArea(topic); area != "" && area != home {
				crossArea++
				break
			}
		}
	}
	if len(perScope) != len(wantPerScope) {
		t.Fatalf("corpus scopes %v, want exactly %v", perScope, wantPerScope)
	}
	for scope, want := range wantPerScope {
		if perScope[scope] != want {
			t.Fatalf("scope %q holds %d items, want %d", scope, perScope[scope], want)
		}
	}

	// Lifecycle mix: confirmed majority, a solid proposed share and every
	// weak state present, so validity weighting is exercised for real.
	for state, want := range map[string]int{
		"confirmed": 30, "proposed": 10, "invalidated": 5,
		"superseded": 2, "challenged": 2, "corrected": 2,
	} {
		if states[state] < want {
			t.Fatalf("corpus holds %d %s items, want at least %d", states[state], state, want)
		}
	}
	if minReuse != 0 || maxReuse != 9 {
		t.Fatalf("reuse span = %d..%d, want 0..9", minReuse, maxReuse)
	}
	if crossArea < 4 {
		t.Fatalf("only %d items carry cross-theme topics, want at least 4", crossArea)
	}

	// Control for TestIntegrationInvalidatedNotAboveProposed: every
	// database-theme item matches exactly {migration, rollback} out of
	// migrationTask, so both of its scopes score identical relevance.
	for _, row := range rows {
		if row.topics[0] != "database" && row.topics[0] != "migrations" {
			continue
		}
		tokens := rowTokens(row)
		if !tokens["migration"] || !tokens["rollback"] {
			t.Fatalf("%s must contain the tokens migration and rollback, got %v", row.id, tokens)
		}
		if tokens["downtime"] || tokens["window"] {
			t.Fatalf("%s must not contain %q tokens for the relevance control", row.id, migrationTask)
		}
	}

	// Control for TestIntegrationRelevantTaskRanksCorrectPattern: the
	// auth/testing/prompts scopes share no token with deployTask, and for
	// TestIntegrationIrrelevantTaskYieldsLittle no item shares a token with
	// irrelevantTask.
	deployTokens := tokenize(deployTask)
	irrelevantTokens := tokenize(irrelevantTask)
	if len(deployTokens) == 0 || len(irrelevantTokens) == 0 {
		t.Fatal("fixture tasks must tokenize to a non-empty token set")
	}
	for _, row := range rows {
		tokens := rowTokens(row)
		for token := range irrelevantTokens {
			if tokens[token] {
				t.Fatalf("fixture broken: %s contains irrelevant task token %q", row.id, token)
			}
		}
		switch row.topics[0] {
		case "auth", "testing", "prompts":
			for token := range deployTokens {
				if tokens[token] {
					t.Fatalf("fixture broken: %s (scope %s) contains deploy task token %q", row.id, row.topics[0], token)
				}
			}
		}
	}
}

// TestIntegrationRelevantTaskRanksCorrectPattern drives a deploy/rollback
// task through the full HTTP retrieval, grouping, scoring and budget
// pipeline: the deploy pattern must lead, its cues must be compact and
// reloadable (each starts with a knowledge id the pattern really holds) and
// the result must stay inside the pattern and token budgets.
func TestIntegrationRelevantTaskRanksCorrectPattern(t *testing.T) {
	config := DefaultConfig()
	result := primeCorpus(t, config, deployTask)

	if result.Project != integrationProject || result.Task != deployTask {
		t.Fatalf("result echoes project=%q task=%q", result.Project, result.Task)
	}
	if result.TotalKnowledge != 60 {
		t.Fatalf("TotalKnowledge = %d, want 60 (the whole corpus)", result.TotalKnowledge)
	}
	// Only deploy, database and migrations share tokens with the task; the
	// other scopes score 0 and must be cut by MinScore.
	if result.Primed != 3 || len(result.Patterns) != 3 {
		t.Fatalf("Primed = %d with patterns %v, want 3 (deploy, database, migrations)", result.Primed, patternIDs(result.Patterns))
	}
	if result.HiddenPatterns != 0 {
		t.Fatalf("HiddenPatterns = %d, want 0: the default budget fits three patterns", result.HiddenPatterns)
	}
	if result.Primed+result.HiddenPatterns > config.MaxPatterns {
		t.Fatalf("Primed %d + HiddenPatterns %d exceeds MaxPatterns %d", result.Primed, result.HiddenPatterns, config.MaxPatterns)
	}

	top := result.Patterns[0]
	if top.Scope != "deploy" || top.ID != "deploy" {
		t.Fatalf("top pattern = %q, want the deploy pattern (order: %v)", top.ID, patternIDs(result.Patterns))
	}
	if top.ActiveCount != 10 || top.InvalidCount != 2 {
		t.Fatalf("deploy pattern active/invalid = %d/%d, want 10/2", top.ActiveCount, top.InvalidCount)
	}
	if len(top.KnowledgeIDs) != 12 {
		t.Fatalf("deploy pattern exposes %d knowledge ids, want all 12", len(top.KnowledgeIDs))
	}
	if len(top.Cues) == 0 || len(top.Cues) > config.CuesPerPattern {
		t.Fatalf("deploy pattern renders %d cues, want 1..%d", len(top.Cues), config.CuesPerPattern)
	}

	// Every cue is a reload key: it starts with a real knowledge id of the
	// pattern ("<id>: ...").
	ids := map[string]bool{}
	for _, id := range top.KnowledgeIDs {
		ids[id] = true
	}
	for _, cue := range top.Cues {
		id, _, ok := strings.Cut(cue, ": ")
		if !ok || !ids[id] {
			t.Fatalf("cue %q does not start with a knowledge id of the pattern", cue)
		}
	}
	// Cues lead with the strongest evidence: the confirmed item with the
	// highest reuse count.
	if !strings.HasPrefix(top.Cues[0], "K-DEP-001: ") || !strings.Contains(top.Cues[0], "confirmed, reused 9") {
		t.Fatalf("first cue = %q, want K-DEP-001 confirmed with 9 reuses", top.Cues[0])
	}

	if result.TokensEstimate > config.MaxTokens {
		t.Fatalf("TokensEstimate = %d, want <= %d", result.TokensEstimate, config.MaxTokens)
	}
	for i, pat := range result.Patterns {
		if pat.Score < config.MinScore {
			t.Fatalf("pattern %q scored %.4f, below MinScore %.2f", pat.ID, pat.Score, config.MinScore)
		}
		if i > 0 && result.Patterns[i-1].Score < pat.Score {
			t.Fatalf("patterns are out of rank order at %d: %v", i, patternIDs(result.Patterns))
		}
		if pat.Scope == "auth" || pat.Scope == "testing" || pat.Scope == "prompts" {
			t.Fatalf("unrelated pattern %q surfaced for a deploy task", pat.ID)
		}
	}
}

// TestIntegrationIrrelevantTaskYieldsLittle feeds a task whose tokens appear
// nowhere in the corpus: with MinScore filtering there is nothing worth
// priming, so nothing must be rendered and no tokens spent.
func TestIntegrationIrrelevantTaskYieldsLittle(t *testing.T) {
	result := primeCorpus(t, DefaultConfig(), irrelevantTask)

	if result.TotalKnowledge != 60 {
		t.Fatalf("TotalKnowledge = %d, want 60: retrieval is not filtered by the task", result.TotalKnowledge)
	}
	if result.Primed != 0 || len(result.Patterns) != 0 {
		t.Fatalf("irrelevant task primed %d patterns (%v), want none", result.Primed, patternIDs(result.Patterns))
	}
	if result.HiddenPatterns != 0 {
		t.Fatalf("HiddenPatterns = %d, want 0: nothing was ranked", result.HiddenPatterns)
	}
	if result.TokensEstimate != 0 {
		t.Fatalf("TokensEstimate = %d, want 0: no cues may be rendered", result.TokensEstimate)
	}
}

// TestIntegrationInvalidatedNotAboveProposed compares the two scopes of the
// database theme at identical relevance (both match exactly half of the task
// tokens in every item, neither scope token is a task token): the
// proposed-leaning scope must outrank the invalidated-majority scope, and
// even the invalid-heavy pattern must lead its cues with live knowledge.
func TestIntegrationInvalidatedNotAboveProposed(t *testing.T) {
	result := primeCorpus(t, DefaultConfig(), migrationTask)

	proposedPos, invalidPos := -1, -1
	for i, pat := range result.Patterns {
		switch pat.Scope {
		case "database": // 3 confirmed / 5 proposed / 1 invalidated / 1 corrected
			proposedPos = i
		case "migrations": // 5 invalidated of 8 items
			invalidPos = i
		}
	}
	if proposedPos < 0 || invalidPos < 0 {
		t.Fatalf("both database-theme patterns must survive MinScore, got %v", patternIDs(result.Patterns))
	}
	if result.Patterns[proposedPos].Score <= result.Patterns[invalidPos].Score {
		t.Fatalf("invalidated-majority %q (score %.4f) must not outrank proposed-leaning %q (score %.4f)",
			result.Patterns[invalidPos].ID, result.Patterns[invalidPos].Score,
			result.Patterns[proposedPos].ID, result.Patterns[proposedPos].Score)
	}
	if proposedPos > invalidPos {
		t.Errorf("result order %v places the invalidated-majority pattern above the proposed one", patternIDs(result.Patterns))
	}
	if result.Patterns[0].Scope != "database" {
		t.Fatalf("top pattern for a migration task = %q, want database (order: %v)", result.Patterns[0].ID, patternIDs(result.Patterns))
	}

	invalid := result.Patterns[invalidPos]
	if invalid.InvalidCount != 5 || invalid.ActiveCount != 3 {
		t.Fatalf("migrations pattern active/invalid = %d/%d, want 3/5", invalid.ActiveCount, invalid.InvalidCount)
	}
	if len(invalid.Cues) == 0 || !strings.HasPrefix(invalid.Cues[0], "K-MIG-001: ") || !strings.Contains(invalid.Cues[0], "confirmed") {
		t.Fatalf("invalid-heavy pattern must still lead with its confirmed cue, got %v", invalid.Cues)
	}
	for _, cue := range invalid.Cues {
		if id, _, ok := strings.Cut(cue, ": "); !ok {
			t.Fatalf("cue %q must start with \"<knowledge_id>: \"", cue)
		} else if !containsID(invalid.KnowledgeIDs, id) {
			t.Fatalf("cue %q references id %q outside the pattern", cue, id)
		}
	}
}

// TestIntegrationEmptyTaskFallback drops the task entirely: the fallback
// ranking (validity plus reuse, no MinScore filter) must surface every scope
// and lead with the pattern of confirmed, heavily reused items.
func TestIntegrationEmptyTaskFallback(t *testing.T) {
	config := DefaultConfig()
	result := primeCorpus(t, config, "")

	if result.Task != "" {
		t.Fatalf("Task echoed as %q, want empty", result.Task)
	}
	if result.Primed != 6 || len(result.Patterns) != 6 {
		t.Fatalf("empty task primed %d patterns (%v), want all 6 scopes", result.Primed, patternIDs(result.Patterns))
	}
	if result.HiddenPatterns != 0 {
		t.Fatalf("HiddenPatterns = %d, want 0: the default budget fits six patterns", result.HiddenPatterns)
	}
	if result.TokensEstimate > config.MaxTokens {
		t.Fatalf("TokensEstimate = %d, want <= %d", result.TokensEstimate, config.MaxTokens)
	}

	top := result.Patterns[0]
	if top.Scope != "deploy" {
		t.Fatalf("fallback top pattern = %q, want deploy (order: %v)", top.ID, patternIDs(result.Patterns))
	}
	if len(top.Cues) != config.CuesPerPattern {
		t.Fatalf("fallback top pattern renders %d cues, want %d", len(top.Cues), config.CuesPerPattern)
	}
	if !strings.HasPrefix(top.Cues[0], "K-DEP-001: ") || !strings.Contains(top.Cues[0], "confirmed, reused 9") {
		t.Fatalf("first fallback cue = %q, want the confirmed item with 9 reuses", top.Cues[0])
	}
	for _, cue := range top.Cues {
		if !strings.Contains(cue, "confirmed") || !strings.Contains(cue, "reused") {
			t.Errorf("fallback cue %q must come from confirmed high-reuse items", cue)
		}
	}
	for i := 1; i < len(result.Patterns); i++ {
		if result.Patterns[i-1].Score < result.Patterns[i].Score {
			t.Fatalf("fallback patterns are out of rank order at %d: %v", i, patternIDs(result.Patterns))
		}
	}
}

// TestIntegrationBudgetHeld tightens the token budget to 120 tokens with two
// cues per pattern: the estimate must stay within the budget, the top
// pattern must still be fully rendered and the patterns that no longer fit
// must be reported as hidden rather than silently dropped.
func TestIntegrationBudgetHeld(t *testing.T) {
	config := Config{MaxTokens: 120, MaxPatterns: 7, CuesPerPattern: 2} // MinScore 0: no relevance filtering
	result := primeCorpus(t, config, deployTask)

	if result.TokensEstimate > config.MaxTokens {
		t.Fatalf("TokensEstimate = %d, must hold the budget of %d", result.TokensEstimate, config.MaxTokens)
	}
	if len(result.Patterns) < 1 {
		t.Fatal("the top-ranked pattern must always be rendered")
	}
	if result.Patterns[0].Scope != "deploy" {
		t.Fatalf("top pattern = %q, want deploy", result.Patterns[0].ID)
	}
	if len(result.Patterns[0].Cues) < 1 {
		t.Fatal("the top pattern must render at least one cue even under a tight budget")
	}
	if result.HiddenPatterns < 1 {
		t.Fatalf("HiddenPatterns = %d, want >= 1: the corpus cannot fit into %d tokens", result.HiddenPatterns, config.MaxTokens)
	}
	if result.Primed+result.HiddenPatterns > config.MaxPatterns {
		t.Fatalf("Primed %d + HiddenPatterns %d exceeds MaxPatterns %d", result.Primed, result.HiddenPatterns, config.MaxPatterns)
	}
	for i, pat := range result.Patterns {
		if len(pat.Cues) > config.CuesPerPattern {
			t.Fatalf("pattern %q renders %d cues, want <= %d", pat.ID, len(pat.Cues), config.CuesPerPattern)
		}
		if i > 0 && result.Patterns[i-1].Score < pat.Score {
			t.Fatalf("kept patterns are out of rank order at %d: %v", i, patternIDs(result.Patterns))
		}
	}
}

// TestIntegrationJournalContract exercises the failure paths of the journal
// contract: a missing project must be rejected with 400 and a request
// without the configured bearer token with 401, and Prime must surface both
// as errors instead of returning an empty result.
func TestIntegrationJournalContract(t *testing.T) {
	srv := newIntegrationJournal(t, integrationCorpus(), integrationToken)

	if _, err := NewPrimer(DefaultConfig(), srv.URL, integrationToken).Prime(context.Background(), "", deployTask); err == nil {
		t.Fatal("Prime without a project must fail, got nil error")
	} else if !strings.Contains(err.Error(), "400") {
		t.Fatalf("Prime without a project must surface the journal 400, got %q", err.Error())
	}

	if _, err := NewPrimer(DefaultConfig(), srv.URL, "").Prime(context.Background(), integrationProject, deployTask); err == nil {
		t.Fatal("Prime without the bearer token must fail, got nil error")
	} else if !strings.Contains(err.Error(), "401") {
		t.Fatalf("Prime without the bearer token must surface the journal 401, got %q", err.Error())
	}
}

func containsID(ids []string, id string) bool {
	for _, candidate := range ids {
		if candidate == id {
			return true
		}
	}
	return false
}
