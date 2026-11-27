package main

// A LIVE reproduction of the production channel loop on the real
// *workspace.Store (migrated schema) behind a fake matrix homeserver. The
// unit tests run on fake stores; the 2026-11-27 intake outage (zero inbound
// rows, zero conversations created after deploy of waves 1-3) was invisible
// to them, so this test pins the whole real-SQL path: openAsks →
// persistChannelMessage → reply matching → intake, twice, to prove the poll
// cursor survives. Skipped unless the scratch database is reachable.

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/workspace"
)

const liveDiagDSN = "postgres://temporality:temporality@localhost:54329/temporality?sslmode=disable"

func liveDiagStore(t *testing.T) (*workspace.Store, *sql.DB) {
	t.Helper()
	db, err := sql.Open("pgx", liveDiagDSN)
	if err != nil {
		t.Skipf("scratch db unavailable: %v", err)
	}
	if err := db.PingContext(context.Background()); err != nil {
		t.Skipf("scratch db unreachable: %v", err)
	}
	store, err := workspace.Open(context.Background(), liveDiagDSN)
	require.NoError(t, err)
	require.NoError(t, store.MigrateAll(context.Background(), "../../migrations/workspace", func(string, ...any) {}))
	t.Cleanup(func() { store.Close(); db.Close() })
	return store, db
}

func liveDiagLoop(t *testing.T, store *workspace.Store, server *httptest.Server) *channelInboundLoop {
	t.Helper()
	driver := matrixTestDriver(t)
	return &channelInboundLoop{
		driver:     driver,
		poller:     driver.Poller(),
		httpClient: server.Client(),
		settings: func(context.Context) (agent.ChannelSettings, error) {
			return agent.ChannelSettings{Channels: map[string]map[string]string{
				"matrix": {"homeserver": server.URL, "access_token": "tok"},
			}}, nil
		},
		store:    store,
		sourceID: "live-diag",
		log:      slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
	}
}

func TestLiveChannelLoopFullCycle(t *testing.T) {
	store, db := liveDiagStore(t)
	ctx := context.Background()
	room := "!diag-live:hs"
	mxid := "@human-diag:hs"

	// Fresh state for this run's identity only (do not nuke shared scratch data).
	for _, stmt := range []string{
		"DELETE FROM conversation_transport WHERE source='matrix' AND source_conversation_id='" + room + "'",
		"DELETE FROM conversation_participant WHERE conversation_id LIKE 'conv-" + stableConversationID("matrix", room)[4:] + "%'",
		"DELETE FROM conversation_message WHERE conversation_id LIKE 'conv-" + stableConversationID("matrix", room)[4:] + "%'",
		"DELETE FROM conversation_interaction WHERE conversation_id LIKE 'conv-" + stableConversationID("matrix", room)[4:] + "%'",
		"DELETE FROM conversation WHERE id LIKE 'conv-" + stableConversationID("matrix", room)[4:] + "%'",
		"DELETE FROM human_request WHERE id LIKE 'hq-diag%'",
		"DELETE FROM workspace_inbound_message WHERE room='" + room + "'",
		"DELETE FROM workspace_user WHERE id='u-diag-live'",
		"INSERT INTO workspace_user (id, name, role, active, created_at, updated_at) VALUES ('u-diag-live','Diag Human','member',true,now(),now())",
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed: %v: %v", stmt, err)
		}
	}
	// The user's matrix channel: address = room id, sender = mxid.
	_, err := db.ExecContext(ctx,
		`UPDATE workspace_user SET channels=$1 WHERE id='u-diag-live'`,
		`[{"type":"matrix","address":"`+room+`","sender_id":"`+mxid+`","enabled":true}]`)
	require.NoError(t, err)

	var sent testSink
	server := matrixIntakeHomeServer(t, room, []any{
		matrixTextEvent("$diag-m1", room, mxid, "подготовь отчёт по инциденту"),
	}, &sent)
	loop := liveDiagLoop(t, store, server)

	intakeCalls := 0
	loop.intake = func(context.Context, agent.InboundMessage, string) (string, error) {
		intakeCalls++
		return "run-diag-1", nil
	}
	loop.intakeEnabled = true

	// Cycle 1: fresh cursor → whoami + sync + route.
	delay, err := loop.cycle(ctx)
	require.NoError(t, err)
	t.Logf("cycle1 delay=%v intakeCalls=%d sent=%v cursor=%q", delay, intakeCalls, sent.items, loop.cursor)

	// The conversation for the room must exist now, with the message appended.
	convID := stableConversationID("matrix", room)
	_, err = store.GetConversation(ctx, convID)
	require.NoError(t, err, "the room conversation must be created by persistChannelMessage")
	messages, err := store.ListConversationMessages(ctx, convID, 0, 10)
	require.NoError(t, err)
	require.NotEmpty(t, messages, "the human message must be durably appended")

	// Cycle 2 (new batch): proves the cursor is not wedged by the real store.
	server2 := matrixIntakeHomeServer(t, room, []any{
		matrixTextEvent("$diag-m2", room, mxid, "второе сообщение"),
	}, &sent)
	loop2 := liveDiagLoop(t, store, server2)
	loop2.intake = loop.intake
	loop2.intakeEnabled = true
	loop2.cursor = loop.cursor
	loop2.selfID = "@bot:hs"
	_, err = loop2.cycle(ctx)
	require.NoError(t, err, "second cycle must not wedge")
	require.Equal(t, 2, intakeCalls, "both messages must reach intake")
}

// The outage reproduction (2026-11-27): an open review ask in the room plus
// the wave-1 openAsks additions (deliveries, interaction projector). Before
// the fix the ask swallowed EVERY message of the room (routeArtifactReviewReply
// consumed even non-decision texts), so the agent went silent — zero intake
// rows for ordinary chat. This test pins the fixed pipeline on the real
// *workspace.Store: a non-decision message falls through to intake and the
// ask stays open; a decision keyword closes the stale row (the round itself
// is unknown to the store here) instead of answering into dead air.
func TestLiveChannelLoopWithOpenReviewAsk(t *testing.T) {
	store, db := liveDiagStore(t)
	ctx := context.Background()
	room := "!diag-ask:hs"
	mxid := "@human-diag:hs"
	convSuffix := stableConversationID("matrix", room)[4:]

	for _, stmt := range []string{
		"DELETE FROM conversation_transport WHERE source='matrix' AND source_conversation_id='" + room + "'",
		"DELETE FROM conversation_participant WHERE conversation_id LIKE 'conv-" + convSuffix + "%'",
		"DELETE FROM conversation_message WHERE conversation_id LIKE 'conv-" + convSuffix + "%'",
		"DELETE FROM conversation_interaction WHERE conversation_id LIKE 'conv-" + convSuffix + "%'",
		"DELETE FROM conversation WHERE id LIKE 'conv-" + convSuffix + "%'",
		"DELETE FROM human_request_delivery WHERE request_id LIKE 'artifact-review/rev-diag' OR request_id LIKE 'hq-diag-ask%'",
		"DELETE FROM human_request WHERE id LIKE 'artifact-review/rev-diag' OR id LIKE 'hq-diag-ask%'",
		"DELETE FROM workspace_inbound_message WHERE room='" + room + "'",
		"DELETE FROM workspace_user WHERE id='u-diag-live'",
		"INSERT INTO workspace_user (id, name, role, active, created_at, updated_at) VALUES ('u-diag-live','Diag Human','member',true,now(),now())",
		// The ask the room is clogged with: a workflow review round. The row
		// id/run_id mirror the notifier's deterministic id (artifactnotify.go):
		// human_request.id = artifact-review/<reviewID> — cancelReviewRow
		// closes it by exactly that id.
		"INSERT INTO human_request (id, run_id, project_id, status, channel, question, resolved_user, created_at) VALUES ('artifact-review/rev-diag','artifact-review/rev-diag','omni','pending','matrix','Согласовать артефакт?','u-diag-live',now())",
		"INSERT INTO human_request_delivery (request_id, user_id, channel, status, thread_ref, source_message_ref, created_at) VALUES ('artifact-review/rev-diag','u-diag-live','matrix','delivered','','',now())",
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed: %v: %v", stmt, err)
		}
	}
	_, err := db.ExecContext(ctx,
		`UPDATE workspace_user SET channels=$1 WHERE id='u-diag-live'`,
		`[{"type":"matrix","address":"`+room+`","sender_id":"`+mxid+`","enabled":true}]`)
	require.NoError(t, err)

	var sent testSink
	server := matrixIntakeHomeServer(t, room, []any{
		matrixTextEvent("$diag-a1", room, mxid, "ну где результаты?"),
	}, &sent)
	loop := liveDiagLoop(t, store, server)
	intakeCalls := 0
	loop.intake = func(context.Context, agent.InboundMessage, string) (string, error) {
		intakeCalls++
		return "run-diag-ask-" + strconv.Itoa(intakeCalls), nil
	}
	loop.intakeEnabled = true

	start := time.Now()
	delay, err := loop.cycle(ctx)
	require.NoError(t, err, "openAsks with deliveries + projector must not fail")
	t.Logf("ask cycle delay=%v elapsed=%v cursor=%q", delay, time.Since(start), loop.cursor)
	_, err = store.GetConversation(ctx, stableConversationID("matrix", room))
	require.NoError(t, err, "the projector must create the room conversation for the ask")
	// The fix: ordinary chat is not eaten by the review ask — it reaches
	// intake, and the ask stays open for a real decision keyword.
	require.Equal(t, 1, intakeCalls, "a non-decision message must fall through to intake")
	var status string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT status FROM human_request WHERE id='artifact-review/rev-diag'`).Scan(&status))
	require.Equal(t, "pending", status, "the open ask must survive ordinary chat")

	// Cycle 2: the decision keyword. The round is unknown to the store, so
	// the stale channel row closes (no more matching) and the message itself
	// still reaches intake — the dialog gets an explanation, not dead air.
	server2 := matrixIntakeHomeServer(t, room, []any{
		matrixTextEvent("$diag-a2", room, mxid, "согласовано"),
	}, &sent)
	loop2 := liveDiagLoop(t, store, server2)
	loop2.intake = loop.intake
	loop2.intakeEnabled = true
	loop2.cursor = loop.cursor
	loop2.selfID = "@bot:hs"
	_, err = loop2.cycle(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, intakeCalls, "the decision word must not be swallowed either (round gone)")
	require.NoError(t, db.QueryRowContext(ctx, `SELECT status FROM human_request WHERE id='artifact-review/rev-diag'`).Scan(&status))
	require.Equal(t, "cancelled", status, "the stale ask row must close on a decision word")
}
