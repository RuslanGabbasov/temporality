package postgres_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/temporality-project/temporality/frp/protocol"
	"github.com/temporality-project/temporality/frp/substrate/postgres"
	"github.com/temporality-project/temporality/frp/timetravel"
)

func TestTimeTravelEventOrderSnapshotFallbackAndNoFutureLeakage(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	store, err := postgres.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, migration := range []string{"../../../migrations/000001_event_store.up.sql", "../../../migrations/000010_time_travel.up.sql"} {
		if err = store.Migrate(ctx, migration); err != nil {
			t.Fatal(err)
		}
	}
	episodeID, branchID := newTestUUID(), newTestUUID()
	base := time.Now().UTC()
	for i := 0; i < 3; i++ {
		event := protocol.Event{Protocol: protocol.Name, Version: protocol.Version, EventID: newTestUUID(), EpisodeID: episodeID, BranchID: branchID, TransactionTime: base, ValidTime: base, Type: "test.recorded", Payload: map[string]any{"index": i}, Provenance: map[string]any{}}
		if err = store.Append(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	all, err := store.ListEventsThrough(ctx, episodeID, branchID, timetravel.EventCursor{EventSeq: 1<<62 - 1})
	if err != nil || len(all) != 3 {
		t.Fatalf("events: %d, %v", len(all), err)
	}
	for i := 1; i < len(all); i++ {
		if all[i].Cursor.EventSeq <= all[i-1].Cursor.EventSeq {
			t.Fatalf("event_seq is not strictly increasing: %#v", all)
		}
	}
	visible, err := store.ListEventsThrough(ctx, episodeID, branchID, all[1].Cursor)
	if err != nil || len(visible) != 2 {
		t.Fatalf("future event leaked: %d, %v", len(visible), err)
	}

	older, err := timetravel.NewSnapshot(timetravel.SnapshotMetadata{SnapshotID: newTestUUID(), EpisodeID: episodeID, CreatedAt: base.Add(time.Second), Through: all[0].Cursor}, json.RawMessage(`{"state":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if err = store.CreateSnapshot(ctx, older); err != nil {
		t.Fatal(err)
	}
	newer, err := timetravel.NewSnapshot(timetravel.SnapshotMetadata{SnapshotID: newTestUUID(), EpisodeID: episodeID, CreatedAt: base.Add(2 * time.Second), Through: all[1].Cursor}, json.RawMessage(`{"state":2}`))
	if err != nil {
		t.Fatal(err)
	}
	if err = store.CreateSnapshot(ctx, newer); err != nil {
		t.Fatal(err)
	}
	selected, ok, err := store.SelectSnapshot(ctx, episodeID, all[1].Cursor)
	if err != nil || !ok || selected.Metadata.SnapshotID != newer.Metadata.SnapshotID {
		t.Fatalf("latest snapshot not selected: %#v, %v, %v", selected, ok, err)
	}
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `UPDATE snapshots SET payload='{"corrupt":true}'::jsonb WHERE snapshot_id=$1`, newer.Metadata.SnapshotID)
	pool.Close()
	if err != nil {
		t.Fatal(err)
	}
	selected, ok, err = store.SelectSnapshot(ctx, episodeID, all[1].Cursor)
	if err != nil || !ok || selected.Metadata.SnapshotID != older.Metadata.SnapshotID {
		t.Fatalf("corrupt snapshot did not fall back: %#v, %v, %v", selected, ok, err)
	}
}
