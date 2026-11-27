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

// The C1 repeat pattern counts DISTINCT tasks per (actor, normalized title):
// runs of one task — chat follow-up turns, «Давай продолжим» re-runs,
// amendment runs around an auto-restarted long run — share the task's
// first-message title and must not read as the user repeating the request.
func TestFrictionSubmissionsCountDistinctTasks(t *testing.T) {
	started := func(run, taskID, actor, title string) journalEvent {
		return journalEvent{Run: run, ActorID: actor, Data: map[string]any{"task_id": taskID, "title": title}}
	}
	const title = "Нужно написать техпроект"
	events := []journalEvent{
		// One conversation: four runs of the same task (the first message plus
		// follow-ups after the run hit its turn limit and auto-restarted).
		started("t-1-20261126-100000", "t-1", "user-x", title),
		started("t-1-20261126-110000", "t-1", "user-x", title),
		started("t-1-20261126-120000", "t-1", "user-x", title),
		started("t-1-20261126-130000", "t-1", "user-x", title),
	}
	subs := frictionSubmissions(events)
	key := frictionAsk{actor: "user-x", title: normalizeFrictionText(title)}
	if n := len(subs[key]); n != 1 {
		t.Fatalf("four runs of one task must collapse into one submission, got %d", n)
	}

	// A run without a task row stands in with its run id: distinct runs stay
	// distinct submissions.
	events = append(events, started("adhoc-run-1", "", "user-x", title), started("adhoc-run-2", "", "user-x", title))
	subs = frictionSubmissions(events)
	if n := len(subs[key]); n != 3 {
		t.Fatalf("two taskless runs must add two submissions, got %d", n)
	}

	// Distinct tasks with the same title by one actor are genuine repeats.
	events = append(events, started("t-2-a", "t-2", "user-x", title))
	subs = frictionSubmissions(events)
	if n := len(subs[key]); n != 4 {
		t.Fatalf("a distinct task with the same title must count separately, got %d", n)
	}

	// The same title from another actor stays their own subject.
	other := frictionAsk{actor: "user-y", title: normalizeFrictionText(title)}
	subs = frictionSubmissions([]journalEvent{started("t-3-a", "t-3", "user-y", title)})
	if n := len(subs[other]); n != 1 {
		t.Fatalf("another actor's submission must not merge, got %d", n)
	}

	// Untitled events (child runs, continuations) never count.
	subs = frictionSubmissions([]journalEvent{{Run: "child-r1", ActorID: "user-x", Data: map[string]any{"task_id": "t-1", "restart_attempt": float64(1)}}})
	if len(subs) != 0 {
		t.Fatalf("untitled run.started must be skipped, got %d subjects", len(subs))
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
