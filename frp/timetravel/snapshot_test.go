package timetravel_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/temporality-project/temporality/frp/timetravel"
)

func TestCanonicalJSONHashIgnoresObjectOrderAndWhitespace(t *testing.T) {
	first, err := timetravel.CanonicalJSONHash(json.RawMessage(`{"b":[2,1],"a":{"x":true}}`))
	if err != nil {
		t.Fatal(err)
	}
	second, err := timetravel.CanonicalJSONHash(json.RawMessage(" { \"a\" : {\"x\":true}, \"b\" : [2,1] } "))
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("canonical hashes differ: %s != %s", first, second)
	}
	numeric, err := timetravel.CanonicalJSONHash(json.RawMessage(`{"b":[2.0,1e0],"a":{"x":true}}`))
	if err != nil {
		t.Fatal(err)
	}
	if first != numeric {
		t.Fatalf("equivalent number spellings differ: %s != %s", first, numeric)
	}
	if _, err := timetravel.CanonicalJSONHash(json.RawMessage(`{} {}`)); err == nil {
		t.Fatal("expected multiple JSON values to be rejected")
	}
}

func TestSnapshotValidationAndDeterministicSelection(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	makeSnapshot := func(id, eventID string, tx, created time.Time) timetravel.Snapshot {
		snapshot, err := timetravel.NewSnapshot(timetravel.SnapshotMetadata{
			SnapshotID: id, EpisodeID: "episode", CreatedAt: created,
			Through: timetravel.EventCursor{TransactionTime: tx, EventID: eventID},
		}, json.RawMessage(`{"state":1}`))
		if err != nil {
			t.Fatal(err)
		}
		return snapshot
	}

	snapshots := []timetravel.Snapshot{
		makeSnapshot("future-availability", "c", base, base.Add(10*time.Second)),
		makeSnapshot("b", "b", base, base.Add(time.Second)),
		makeSnapshot("a", "a", base, base.Add(time.Second)),
	}
	selected, ok, err := timetravel.SelectSnapshot(snapshots, timetravel.SnapshotSelection{
		AsOf: base, Cursor: timetravel.EventCursor{TransactionTime: base, EventID: "b"}, AvailableAt: base.Add(2 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ok || selected.Metadata.SnapshotID != "b" {
		t.Fatalf("unexpected selection: %#v, %v", selected, ok)
	}

	tampered := selected
	tampered.Content = json.RawMessage(`{"state":2}`)
	if err := tampered.Validate(); err == nil {
		t.Fatal("expected content hash mismatch")
	}
	tampered = selected
	tampered.Metadata.SnapshotVersion = "2"
	if err := tampered.Validate(); err == nil {
		t.Fatal("expected unsupported version")
	}
}
