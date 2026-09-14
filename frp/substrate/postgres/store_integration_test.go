package postgres_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/temporality-project/temporality/frp/protocol"
	"github.com/temporality-project/temporality/frp/substrate"
	"github.com/temporality-project/temporality/frp/substrate/postgres"
)

func TestEventSurvivesReopenAndCannotBeMutated(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	store, err := postgres.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Migrate(ctx, "../../../migrations/000001_event_store.up.sql"); err != nil {
		store.Close()
		t.Fatal(err)
	}

	now := time.Now().UTC()
	event := protocol.Event{
		Protocol: protocol.Name, Version: protocol.Version, EventID: newTestUUID(),
		TransactionTime: now, ValidTime: now, Type: "episode.started",
		Payload: map[string]any{"test": true}, Provenance: map[string]any{"source": "integration-test"},
	}
	if err = store.Append(ctx, event); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err = store.Append(ctx, event); err == nil {
		store.Close()
		t.Fatal("duplicate event was accepted")
	}

	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, "UPDATE events SET type='episode.failed' WHERE event_id=$1", event.EventID); err == nil {
		pool.Close()
		store.Close()
		t.Fatal("append-only trigger allowed event mutation")
	}
	pool.Close()
	store.Close()

	reopened, err := postgres.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.Get(ctx, event.EventID)
	if err != nil {
		t.Fatal(err)
	}
	if got.EventID != event.EventID || got.Type != event.Type {
		t.Fatalf("unexpected event after reopen: %#v", got)
	}
	listed, err := reopened.List(ctx, substrate.EventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, candidate := range listed {
		if candidate.EventID == event.EventID {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("persisted event absent from replay stream")
	}
}

func TestPostgresCursorPagination(t *testing.T) {
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
	if err = store.Migrate(ctx, "../../../migrations/000001_event_store.up.sql"); err != nil {
		t.Fatal(err)
	}
	episodeID := newTestUUID()
	base := time.Now().UTC()
	for i := 0; i < 3; i++ {
		event := protocol.Event{Protocol: protocol.Name, Version: protocol.Version, EventID: newTestUUID(), EpisodeID: episodeID, TransactionTime: base.Add(time.Duration(i) * time.Nanosecond), ValidTime: base.Add(time.Duration(i) * time.Nanosecond), Type: "metric.recorded", Payload: map[string]any{"index": i}, Provenance: map[string]any{"source": "integration-test"}}
		if err = store.Append(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	first, err := store.ListPage(ctx, substrate.PageRequest{Filter: substrate.EventFilter{EpisodeID: episodeID}, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Events) != 2 || first.NextCursor == "" {
		t.Fatalf("unexpected first page: %#v", first)
	}
	second, err := store.ListPage(ctx, substrate.PageRequest{Filter: substrate.EventFilter{EpisodeID: episodeID}, Limit: 2, Cursor: first.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Events) != 1 || second.NextCursor != "" {
		t.Fatalf("unexpected second page: %#v", second)
	}
	if first.Events[1].EventID == second.Events[0].EventID {
		t.Fatal("cursor returned a duplicate event")
	}
}

func newTestUUID() string {
	n := time.Now().UnixNano()
	return fmt.Sprintf("%08x-0000-4000-8000-%012x", uint32(n>>32), uint64(n)&0xffffffffffff)
}
