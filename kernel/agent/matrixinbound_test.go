package agent

import (
	"encoding/json"
	"testing"
	"time"
)

func syncPayload(t *testing.T, nextBatch string, rooms map[string][]matrixRoomEvent) []byte {
	t.Helper()
	joined := make(map[string]matrixJoinedRoom, len(rooms))
	for roomID, events := range rooms {
		room := matrixJoinedRoom{}
		room.Timeline.Events = events
		joined[roomID] = room
	}
	response := matrixSyncResponse{NextBatch: nextBatch}
	response.Rooms.Join = joined
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestParseMatrixSyncKeepsTextMessagesOnly(t *testing.T) {
	askedAt := time.Now()
	body := syncPayload(t, "s2", map[string][]matrixRoomEvent{
		"!room:hs": {
			{Type: "m.room.member", Sender: "@u:hs", OriginServerTS: askedAt.Add(-time.Minute).UnixMilli(), Content: matrixEventContent{MsgType: "m.text", Body: "join"}},
			{Type: "m.room.message", Sender: "@u:hs", OriginServerTS: askedAt.UnixMilli(), Content: matrixEventContent{MsgType: "m.notice", Body: "notice"}},
			{Type: "m.room.message", Sender: "@u:hs", OriginServerTS: askedAt.UnixMilli(), Content: matrixEventContent{MsgType: "m.text", Body: "  "}},
			{Type: "m.room.message", Sender: "@u:hs", OriginServerTS: askedAt.UnixMilli(), Content: matrixEventContent{MsgType: "m.text", Body: " yes "}},
		},
	})
	since, messages, err := ParseMatrixSync(body)
	if err != nil {
		t.Fatal(err)
	}
	if since != "s2" {
		t.Fatalf("next batch: %q", since)
	}
	if len(messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(messages))
	}
	if messages[0].Body != "yes" || messages[0].RoomID != "!room:hs" || messages[0].Sender != "@u:hs" {
		t.Fatalf("unexpected message: %+v", messages[0])
	}
	if !messages[0].Timestamp.Equal(time.UnixMilli(askedAt.UnixMilli())) {
		t.Fatalf("timestamp not converted: %+v", messages[0])
	}
}

func TestParseMatrixSyncOrdersChronologically(t *testing.T) {
	base := time.Now()
	body := syncPayload(t, "s3", map[string][]matrixRoomEvent{
		"!b:hs": {{Type: "m.room.message", Sender: "@u:hs", OriginServerTS: base.Add(2 * time.Second).UnixMilli(), Content: matrixEventContent{MsgType: "m.text", Body: "second"}}},
		"!a:hs": {
			{Type: "m.room.message", Sender: "@u:hs", OriginServerTS: base.Add(time.Second).UnixMilli(), Content: matrixEventContent{MsgType: "m.text", Body: "first"}},
			{Type: "m.room.message", Sender: "@u:hs", OriginServerTS: base.UnixMilli(), Content: matrixEventContent{MsgType: "m.text", Body: "zero"}},
		},
	})
	_, messages, err := ParseMatrixSync(body)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{messages[0].Body, messages[1].Body, messages[2].Body}
	want := []string{"zero", "first", "second"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order: got %v want %v", got, want)
		}
	}
}

func TestMatchMatrixReplies(t *testing.T) {
	now := time.Now()
	users := []MatrixUserRooms{{UserID: "u1", Rooms: []string{"!r1:hs"}}}
	bot := "@bot:hs"
	messages := func(ms ...MatrixInboundMessage) []MatrixInboundMessage { return ms }
	msg := func(room, sender string, at time.Time, body string) MatrixInboundMessage {
		return MatrixInboundMessage{RoomID: room, Sender: sender, Body: body, Timestamp: at}
	}

	t.Run("routes reply to the asking run", func(t *testing.T) {
		asks := []MatrixPendingAsk{{RequestID: "run/turn/01/call_a", RunID: "run", ProjectID: "p", UserID: "u1", CreatedAt: now.Add(-time.Minute)}}
		replies := MatchMatrixReplies(messages(msg("!r1:hs", "@human:hs", now, "deploy it")), asks, users, bot)
		if len(replies) != 1 || replies[0].Ask.RequestID != "run/turn/01/call_a" || replies[0].Message.Body != "deploy it" {
			t.Fatalf("unexpected replies: %+v", replies)
		}
	})

	t.Run("skips bot's own messages", func(t *testing.T) {
		asks := []MatrixPendingAsk{{RequestID: "op", RunID: "run", UserID: "u1", CreatedAt: now.Add(-time.Minute)}}
		replies := MatchMatrixReplies(messages(msg("!r1:hs", bot, now, "Temporality: an agent is waiting")), asks, users, bot)
		if len(replies) != 0 {
			t.Fatalf("bot message matched: %+v", replies)
		}
	})

	t.Run("history before the ask never matches", func(t *testing.T) {
		asks := []MatrixPendingAsk{{RequestID: "op", RunID: "run", UserID: "u1", CreatedAt: now}}
		replies := MatchMatrixReplies(messages(msg("!r1:hs", "@human:hs", now.Add(-10*time.Minute), "old chatter")), asks, users, bot)
		if len(replies) != 0 {
			t.Fatalf("stale history matched: %+v", replies)
		}
	})

	t.Run("clock skew within tolerance still matches", func(t *testing.T) {
		asks := []MatrixPendingAsk{{RequestID: "op", RunID: "run", UserID: "u1", CreatedAt: now}}
		replies := MatchMatrixReplies(messages(msg("!r1:hs", "@human:hs", now.Add(matrixReplySkew-time.Second), "quick reply")), asks, users, bot)
		if len(replies) != 1 {
			t.Fatalf("skewed reply not matched: %+v", replies)
		}
	})

	t.Run("oldest ask in the room wins", func(t *testing.T) {
		asks := []MatrixPendingAsk{
			{RequestID: "newer", RunID: "run", UserID: "u1", CreatedAt: now.Add(-time.Minute)},
			{RequestID: "older", RunID: "run", UserID: "u1", CreatedAt: now.Add(-2 * time.Hour)},
		}
		replies := MatchMatrixReplies(messages(msg("!r1:hs", "@human:hs", now, "yes")), asks, users, bot)
		if len(replies) != 1 || replies[0].Ask.RequestID != "older" {
			t.Fatalf("oldest ask should win: %+v", replies)
		}
	})

	t.Run("two asks take two replies in order", func(t *testing.T) {
		asks := []MatrixPendingAsk{
			{RequestID: "first", RunID: "run", UserID: "u1", CreatedAt: now.Add(-2 * time.Hour)},
			{RequestID: "second", RunID: "run", UserID: "u1", CreatedAt: now.Add(-time.Minute)},
		}
		replies := MatchMatrixReplies(
			messages(msg("!r1:hs", "@human:hs", now.Add(-time.Minute), "one"), msg("!r1:hs", "@human:hs", now, "two")),
			asks, users, bot)
		if len(replies) != 2 || replies[0].Ask.RequestID != "first" || replies[1].Ask.RequestID != "second" {
			t.Fatalf("unexpected pairing: %+v", replies)
		}
	})

	t.Run("cancel body declines", func(t *testing.T) {
		asks := []MatrixPendingAsk{{RequestID: "op", RunID: "run", UserID: "u1", CreatedAt: now.Add(-time.Minute)}}
		replies := MatchMatrixReplies(messages(msg("!r1:hs", "@human:hs", now, "CANCEL")), asks, users, bot)
		if len(replies) != 1 || !replies[0].Cancelled {
			t.Fatalf("cancel not detected: %+v", replies)
		}
	})

	t.Run("foreign room and roomless asks never match", func(t *testing.T) {
		asks := []MatrixPendingAsk{
			{RequestID: "mine", RunID: "run", UserID: "u1", CreatedAt: now.Add(-time.Minute)},
			{RequestID: "roomless", RunID: "run", UserID: "u2", CreatedAt: now.Add(-time.Minute)},
		}
		replies := MatchMatrixReplies(messages(msg("!other:hs", "@human:hs", now, "hello")), asks, users, bot)
		if len(replies) != 0 {
			t.Fatalf("foreign room matched: %+v", replies)
		}
	})
}
