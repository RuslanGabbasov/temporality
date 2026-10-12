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

func TestParseMatrixSyncCapturesFileMessages(t *testing.T) {
	at := time.Now()
	body := syncPayload(t, "s4", map[string][]matrixRoomEvent{
		"!room:hs": {
			{Type: "m.room.message", Sender: "@u:hs", OriginServerTS: at.UnixMilli(), Content: matrixEventContent{MsgType: "m.file", Body: "here is the export", Filename: "skills-export.zip", URL: "mxc://hs/abc123"}},
			{Type: "m.room.message", Sender: "@u:hs", OriginServerTS: at.UnixMilli(), Content: matrixEventContent{MsgType: "m.image", Body: "screenshot.png", URL: "mxc://hs/img"}},
			{Type: "m.room.message", Sender: "@u:hs", OriginServerTS: at.UnixMilli(), Content: matrixEventContent{MsgType: "m.file", Filename: "broken.zip"}},
			{Type: "m.room.message", Sender: "@u:hs", OriginServerTS: at.UnixMilli(), Content: matrixEventContent{MsgType: "m.text", Body: "plain text still works"}},
		},
	})
	_, messages, err := ParseMatrixSync(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 3 {
		t.Fatalf("expected 3 messages (file, image, text), got %d", len(messages))
	}
	file := messages[0]
	if file.URL != "mxc://hs/abc123" || file.Filename != "skills-export.zip" || file.Body != "here is the export" {
		t.Fatalf("file message not captured: %+v", file)
	}
	image := messages[1]
	if image.Filename != "screenshot.png" {
		t.Fatalf("filename should fall back to body: %+v", image)
	}
	if messages[2].URL != "" {
		t.Fatalf("text message must not carry a file URL: %+v", messages[2])
	}
}

func TestParseMatrixSyncFileWithoutURLIsIgnored(t *testing.T) {
	at := time.Now()
	body := syncPayload(t, "s5", map[string][]matrixRoomEvent{
		"!room:hs": {
			{Type: "m.room.message", Sender: "@u:hs", OriginServerTS: at.UnixMilli(), Content: matrixEventContent{MsgType: "m.file", Body: "nameless", URL: ""}},
		},
	})
	_, messages, err := ParseMatrixSync(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 0 {
		t.Fatalf("file without content URI must be ignored, got %+v", messages)
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
