package workspace

// A live-database test for the conversation outbox — the delivery pipeline's
// SQL shapes cannot be seen by the fake-store unit tests, and production
// proved it (2026-11-27): a bare `now()-$2` with a pgx-encoded time.Duration
// resolved the parameter to the datetime category's PREFERRED type
// (timestamptz), so every claim died with 42883 «timestamp with time zone <=
// interval» and run answers sat pending forever (matrix never heard back).
// Pins claim/retry/fail/stale-turn round-trips against a real migrated
// schema. Skipped unless TEMPORALITY_TEST_DATABASE_URL points at one.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestConversationOutboxLive(t *testing.T) {
	dsn := os.Getenv("TEMPORALITY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEMPORALITY_TEST_DATABASE_URL not set")
	}
	store, err := Open(context.Background(), dsn)
	require.NoError(t, err)
	require.NoError(t, store.MigrateAll(context.Background(), "../../migrations/workspace", func(string, ...any) {}))
	defer store.Close()
	ctx := context.Background()
	run := fmt.Sprintf("outbox-live-%d", time.Now().UnixNano())

	event := &ConversationOutboxEvent{
		ID: "outbox-live-" + run, EventType: OutboxRunAnswer,
		Payload: json.RawMessage(`{"run_id":"` + run + `","channel":"matrix","address":"!live:hs"}`),
	}
	require.NoError(t, store.EnqueueConversationOutboxEvent(ctx, event))
	t.Cleanup(func() { _, _ = store.pool.Exec(ctx, `DELETE FROM conversation_outbox WHERE id=$1`, event.ID) })

	// Due immediately: the claim must take it (the broken shape claimed NOTHING).
	claimed, err := store.ClaimDueConversationOutboxEvents(ctx, 10, time.Minute)
	require.NoError(t, err)
	require.Len(t, claimed, 1, "a due pending event must be claimable — an empty claim here is the 42883 regression")
	require.Equal(t, event.ID, claimed[0].ID)
	require.Equal(t, "processing", claimed[0].Status)

	// RetrySoon: back to pending, due in the future — not claimable right away.
	require.NoError(t, store.RetryConversationOutboxEvent(ctx, event.ID, time.Hour))
	again, err := store.ClaimDueConversationOutboxEvents(ctx, 10, time.Minute)
	require.NoError(t, err)
	require.Empty(t, again, "a retried event with a future next_attempt must stay parked")

	// A failed attempt charges the ladder, stays pending; past the ceiling parks failed.
	require.NoError(t, store.FailConversationOutboxEvent(ctx, event.ID, 1, errors.New("matrix send boom")))
	row, err := outboxRowByID(ctx, store, event.ID)
	require.NoError(t, err)
	require.Equal(t, 1, row.Attempts)
	require.Equal(t, "pending", row.Status)
	require.Contains(t, row.LastError, "matrix send boom")

	require.NoError(t, store.FailConversationOutboxEvent(ctx, event.ID, MaxOutboxAttempts, errors.New("give up")))
	row, err = outboxRowByID(ctx, store, event.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", row.Status)

	// The stale-turn scan shares the duration shape — must run clean.
	_, err = store.ListStaleConversationTurns(ctx, 5*time.Minute, 5)
	require.NoError(t, err)
}

func outboxRowByID(ctx context.Context, store *Store, id string) (ConversationOutboxEvent, error) {
	rows, err := store.pool.Query(ctx, `
		SELECT id, organization_id, conversation_id, event_type, payload, status, attempts,
				 last_error, created_at, delivered_at, next_attempt_at
		FROM conversation_outbox WHERE id=$1`, id)
	if err != nil {
		return ConversationOutboxEvent{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		return ConversationOutboxEvent{}, fmt.Errorf("outbox event %s not found", id)
	}
	return scanOutboxEvent(rows)
}
