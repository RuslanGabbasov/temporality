package main

import (
	"strings"
	"testing"
	"time"
)

func TestNormalizeFrictionText(t *testing.T) {
	a := normalizeFrictionText("Переслать отчёт 17 октября")
	b := normalizeFrictionText("  переслать отчёт  18 октября\n")
	if a != b {
		t.Fatalf("normalization must fold digits and whitespace: %q vs %q", a, b)
	}
	long := normalizeFrictionText(strings.Repeat("x", 300))
	if len(long) > 121 {
		t.Fatalf("normalization must truncate: %d", len(long))
	}
}

func TestCaseDueDedup(t *testing.T) {
	done := map[string]time.Time{}
	now := time.Now()
	done["knowledge_hygiene\x00k-1"] = now
	done["knowledge_hygiene\x00k-2"] = now.Add(-8 * 24 * time.Hour)
	if caseDue(done, "knowledge_hygiene", "k-1") {
		t.Fatal("a fresh verdict must suppress the case")
	}
	// The 7-day window is applied by the caller when building the index; the
	// index itself suppresses any known subject (older entries never land in
	// the map — the listing is bounded by the dedup horizon upstream).
	if !caseDue(done, "knowledge_hygiene", "k-3") {
		t.Fatal("unknown subject must be due")
	}
}

// The mining gate kills the noise the judge kept having to dress up as
// skills: retries/pagination (adjacent same-tool steps) and pure navigation
// (all-read-only sequences) are never procedures — for pairs and triples
// alike. A triple with an action tool (read → mutate → verify) is exactly
// the shape worth naming.
func TestSkillPatternWorthMining(t *testing.T) {
	for _, seq := range [][2]string{
		{"artifact_search", "artifact_search"}, // pagination/retry
		{"web_fetch", "web_fetch"},
		{"artifact_read", "saga_list"}, // navigation
		{"skill_inspect", "agents"},
		{"skill_search", "web_search"},
	} {
		if skillPatternWorthMining(seq[0], seq[1]) {
			t.Fatalf("pair %v must be filtered as noise", seq)
		}
	}
	for _, seq := range [][]string{
		{"artifact_search", "artifact_read", "saga_list"}, // all read-only
		{"web_fetch", "web_fetch", "run_command"},         // retry inside
		{"read_file", "run_command", "run_command"},       // retry inside
		{"skill_search", "skill_search", "skill_search"},  // pure pagination
		{"agents", "skill_inspect", "skill_history"},      // all read-only
	} {
		if skillPatternWorthMining(seq...) {
			t.Fatalf("sequence %v must be filtered as noise", seq)
		}
	}
	for _, seq := range [][]string{
		{"skill_search", "run_command"},
		{"read_file", "write_file"},
		{"artifact_read", "artifact_update"},
		{"delegate", "run_status"},
		{"read_file", "edit_file", "run_command"}, // read → mutate → verify
		{"artifact_read", "artifact_update", "run_status"},
		{"skill_search", "run_command", "web_fetch"},
	} {
		if !skillPatternWorthMining(seq...) {
			t.Fatalf("sequence %v carries an action tool and must stay in", seq)
		}
	}
}

// The readonly classifier must cover the real builtin lookups without
// catching the mutating tools.
func TestLooksLikeReadonlyTool(t *testing.T) {
	for _, name := range []string{
		"agents", "artifact_read", "artifact_search", "knowledge_explore",
		"knowledge_lookup", "list_triggers", "read_file", "run_status",
		"saga_list", "search_org_units", "search_projects", "secret_list",
		"skill_diff", "skill_executions", "skill_history", "skill_inspect",
		"skill_search", "web_fetch", "fetch_image", "view_image", "web_search",
	} {
		if !looksLikeReadonlyTool(name) {
			t.Errorf("%q must classify as read-only", name)
		}
	}
	for _, name := range []string{
		"run_command", "write_file", "edit_file", "artifact_create",
		"artifact_submit", "artifact_update", "saga_attach", "saga_register",
		"skill_propose", "skill_validate", "remember", "start_task",
		"delegate", "cancel_run", "create_trigger", "delete_trigger",
		"update_trigger", "request_approval", "submit_review", "intake_decide",
		"agent_propose", "ask_human", "send_file",
	} {
		if looksLikeReadonlyTool(name) {
			t.Errorf("%q must classify as mutating", name)
		}
	}
}

// A judge rejection is remembered for the dedup window so later passes skip
// the pattern instead of burning the cap on it again.
func TestRejectedSkillPatternMemory(t *testing.T) {
	const sig = "skill_search → run_command"
	if recentlyRejectedSkillPattern(sig) {
		t.Fatal("an unseen pattern cannot be rejected")
	}
	rememberRejectedSkillPattern(sig)
	if !recentlyRejectedSkillPattern(sig) {
		t.Fatal("a remembered rejection must suppress the pattern")
	}
	skillRejectedMu.Lock()
	skillRejectedPatterns[sig] = time.Now().Add(-supervisorCaseDedupWindow - time.Minute)
	skillRejectedMu.Unlock()
	if recentlyRejectedSkillPattern(sig) {
		t.Fatal("an expired rejection must let the pattern back in")
	}
}
