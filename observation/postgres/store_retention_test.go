package postgres

import (
	"strings"
	"testing"
)

// The sweep SQL is pinned verbatim: the batch-bounded delete is the one
// statement that runs with the append-only trigger lifted, so a refactor that
// quietly drops the ctid LIMIT, the occurred_at cutoff or the interval cast
// must fail here, not in production.
func TestRetentionSweepSQLPinned(t *testing.T) {
	const want = `DELETE FROM observation_events
	WHERE ctid IN (
		SELECT ctid FROM observation_events
		WHERE occurred_at <= now() - $1::interval
		LIMIT $2
	)`
	if retentionSweepSQL != want {
		t.Fatalf("retentionSweepSQL drifted:\n got: %s\nwant: %s", retentionSweepSQL, want)
	}
}

// SET/RESET must address exactly the GUC the trigger checks — a renamed GUC
// on one side only would either never lift the trigger or (worse) leave the
// escape hatch stuck open on pooled connections.
func TestRetentionGUCFraming(t *testing.T) {
	if retentionSweepGUC != "temporality.retention_sweep" {
		t.Fatalf("GUC = %q, want temporality.retention_sweep", retentionSweepGUC)
	}
	if retentionSweepSetSQL != `SET temporality.retention_sweep = 'on'` {
		t.Fatalf("set SQL = %q", retentionSweepSetSQL)
	}
	if retentionSweepResetSQL != `RESET temporality.retention_sweep` {
		t.Fatalf("reset SQL = %q", retentionSweepResetSQL)
	}
	if retentionSweepDefaultBatchSize != 5000 {
		t.Fatalf("default batch size = %d, want 5000", retentionSweepDefaultBatchSize)
	}
}

// The retention migration replaces the append-only trigger so DELETE passes
// only under the GUC while UPDATE stays banned in every case. These are
// string-level checks only: the runtime behavior (trigger actually fires, the
// GUC actually lifts DELETE, RESET actually clears the session) needs a live
// PostgreSQL — verify on a staging stand before relying on retention.
func TestObservationEventsRetentionMigration(t *testing.T) {
	sql := observationEventsRetention
	if strings.TrimSpace(sql) == "" {
		t.Fatal("retention migration must not be empty")
	}
	for _, fragment := range []string{
		"CREATE OR REPLACE FUNCTION reject_observation_event_mutation()",
		"current_setting('temporality.retention_sweep', true) = 'on'",
		"TG_OP = 'DELETE'",
		"RETURN OLD",
		"RAISE EXCEPTION 'Temporality observation event log is append-only'",
		"LANGUAGE plpgsql",
	} {
		if !strings.Contains(sql, fragment) {
			t.Fatalf("retention migration must contain %q", fragment)
		}
	}
	// The escape hatch opens exactly one way: a single DELETE-guarded RETURN
	// OLD, no RETURN NEW (that would allow UPDATE), a single exception raise.
	if n := strings.Count(sql, "RETURN OLD"); n != 1 {
		t.Fatalf("RETURN OLD count = %d, want exactly 1 (the DELETE guard)", n)
	}
	if strings.Contains(sql, "RETURN NEW") {
		t.Fatal("UPDATE must stay rejected: RETURN NEW must not appear")
	}
	if n := strings.Count(sql, "RAISE EXCEPTION"); n != 1 {
		t.Fatalf("RAISE EXCEPTION count = %d, want exactly 1", n)
	}
}

// The retention file must be registered in the boot migration list, after the
// baseline that creates the trigger function it replaces.
func TestJournalMigrationsRegistered(t *testing.T) {
	if len(journalMigrations) != 3 {
		t.Fatalf("journalMigrations = %d files, want 3", len(journalMigrations))
	}
	if journalMigrations[0] != observationEventsSchema {
		t.Fatal("baseline schema must run first")
	}
	if journalMigrations[1] != observationEventsIndexes {
		t.Fatal("index migration must run second")
	}
	if journalMigrations[2] != observationEventsRetention {
		t.Fatal("retention migration must run last")
	}
	if !strings.Contains(observationEventsSchema, "CREATE TRIGGER observation_events_append_only") {
		t.Fatal("baseline must create the trigger the retention migration replaces")
	}
}
