package main

import "testing"

// The intake audit's «what was started» lookup (the historical «never
// dispatched» flood): the dispatch link comes from intake.routed keyed by
// message id — matching run.started by project never worked, because
// classified events live in the system "intake" project while dispatched
// runs execute in the target project.
func TestIntakeDispatchSummary(t *testing.T) {
	started := []journalEvent{
		{Run: "intake-abc", Project: "intake", Data: map[string]any{"title": "An initiative message arrived"}},
		{Run: "inbound-matrix-abc-20261031-120934", Project: "omni", Data: map[string]any{"title": "Поправить тесты в PR SuperApp #25238"}},
	}
	routed := map[string]journalEvent{
		"$msg-1": {Data: map[string]any{
			"message_id": "$msg-1", "run_id": "inbound-matrix-abc-20261031-120934", "project_id": "omni",
		}},
		"$msg-2": {Data: map[string]any{
			"message_id": "$msg-2", "run_id": "inbound-matrix-zzz-20261031-150000", "project_id": "omni",
		}},
	}

	cases := []struct {
		subject, want string
	}{
		// Dispatched: the run's own title plus its TARGET project — never the
		// intake dialog run, never "(nothing started)".
		{"$msg-1", "Поправить тесты в PR SuperApp #25238 (project omni)"},
		// Dispatched but the title is outside the run.started listing.
		{"$msg-2", "(title unavailable) (project omni)"},
		// No dispatch link: nothing was started — the legitimate audit case.
		{"$msg-3", "(nothing started)"},
	}
	for _, c := range cases {
		if got := intakeDispatchSummary(routed, started, c.subject); got != c.want {
			t.Errorf("intakeDispatchSummary(%q) = %q, want %q", c.subject, got, c.want)
		}
	}

	// The intake dialog's own run.started must never masquerade as the
	// dispatched task (the old same-project heuristic picked exactly it).
	got := intakeDispatchSummary(routed, started, "$msg-1")
	if got == "An initiative message arrived (project intake)" {
		t.Error("summary must not resolve to the intake dialog run")
	}
}
