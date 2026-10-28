package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseMxcURL(t *testing.T) {
	server, media, err := parseMxcURL("mxc://hs.example/AbCdEf123")
	if err != nil || server != "hs.example" || media != "AbCdEf123" {
		t.Fatalf("unexpected parse: %q %q %v", server, media, err)
	}
	for _, bad := range []string{"", "https://hs/x", "mxc://hs", "mxc:///onlymedia", "mxc://"} {
		if _, _, err := parseMxcURL(bad); err == nil {
			t.Fatalf("%q must be rejected", bad)
		}
	}
}

// TestMatrixPollerNormalizesMessages pins the matrix → channel-neutral
// conversion: the room becomes the chat id, the attachment becomes Files.
func TestMatrixPollerNormalizesMessages(t *testing.T) {
	now := time.Now()
	normalized := matrixNormalized([]MatrixInboundMessage{
		{EventID: "$1", RoomID: "!r:hs", Sender: "@u:hs", Body: "yes", Timestamp: now},
		{EventID: "$2", RoomID: "!r:hs", Sender: "@u:hs", Body: "export", Timestamp: now, URL: "mxc://hs/m1", Filename: "export.zip"},
	})
	if len(normalized) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(normalized))
	}
	text := normalized[0]
	if text.ID != "$1" || text.Channel != "matrix" || text.ChatID != "!r:hs" || text.Sender != "@u:hs" || text.Text != "yes" {
		t.Fatalf("unexpected text message: %+v", text)
	}
	if len(text.Files) != 0 {
		t.Fatalf("text message must carry no files: %+v", text)
	}
	file := normalized[1]
	if len(file.Files) != 1 || file.Files[0].Ref != "mxc://hs/m1" || file.Files[0].Name != "export.zip" {
		t.Fatalf("attachment not normalized: %+v", file)
	}
}

// TestMatrixPollerCoalescesElementMultiAttach: Element sends a multi-file
// message as one event per attachment plus a separate m.text. The batch must
// reach the pipeline as one message — one intake run, every file attached.
func TestMatrixPollerCoalescesElementMultiAttach(t *testing.T) {
	now := time.Now()
	normalized := matrixNormalized([]MatrixInboundMessage{
		{EventID: "$1", RoomID: "!r:hs", Sender: "@u:hs", Body: "otchet.zip", Timestamp: now, URL: "mxc://hs/m1", Filename: "otchet.zip"},
		{EventID: "$2", RoomID: "!r:hs", Sender: "@u:hs", Body: "photo.png", Timestamp: now, URL: "mxc://hs/m2", Filename: "photo.png"},
		{EventID: "$3", RoomID: "!r:hs", Sender: "@u:hs", Body: "вот материалы по задаче", Timestamp: now},
	})
	if len(normalized) != 1 {
		t.Fatalf("expected one coalesced message, got %d", len(normalized))
	}
	merged := normalized[0]
	if merged.ID != "$3" || merged.Text != "вот материалы по задаче" {
		t.Fatalf("the text event must carry the batch: %+v", merged)
	}
	if len(merged.Files) != 2 || merged.Files[0].Ref != "mxc://hs/m1" || merged.Files[1].Ref != "mxc://hs/m2" {
		t.Fatalf("attachments not merged in batch order: %+v", merged.Files)
	}
}

// TestMatrixPollerCoalescesBareFilesWithoutText: attachments sent with no
// caption at all collapse into one message whose body names every file —
// classification sees one task, not one run per file.
func TestMatrixPollerCoalescesBareFilesWithoutText(t *testing.T) {
	now := time.Now()
	normalized := matrixNormalized([]MatrixInboundMessage{
		{EventID: "$1", RoomID: "!r:hs", Sender: "@u:hs", Body: "a.pdf", Timestamp: now, URL: "mxc://hs/m1", Filename: "a.pdf"},
		{EventID: "$2", RoomID: "!r:hs", Sender: "@u:hs", Body: "b.png", Timestamp: now, URL: "mxc://hs/m2", Filename: "b.png"},
	})
	if len(normalized) != 1 {
		t.Fatalf("expected one coalesced message, got %d", len(normalized))
	}
	merged := normalized[0]
	if merged.ID != "$1" || merged.Text != "a.pdf\nb.png" {
		t.Fatalf("unexpected body: %+v", merged)
	}
	if len(merged.Files) != 2 {
		t.Fatalf("files not merged: %+v", merged.Files)
	}
}

// TestMatrixPollerCoalescingKeepsDistinctMessages: separate texts stay
// separate, a captioned file stays alone, another sender's files never merge.
func TestMatrixPollerCoalescingKeepsDistinctMessages(t *testing.T) {
	now := time.Now()
	normalized := matrixNormalized([]MatrixInboundMessage{
		{EventID: "$1", RoomID: "!r:hs", Sender: "@u:hs", Body: "первое", Timestamp: now},
		{EventID: "$2", RoomID: "!r:hs", Sender: "@u:hs", Body: "второе", Timestamp: now},
		{EventID: "$3", RoomID: "!r:hs", Sender: "@u:hs", Body: "вот экспорт", Timestamp: now, URL: "mxc://hs/m1", Filename: "export.zip"},
		{EventID: "$4", RoomID: "!r:hs", Sender: "@other:hs", Body: "x.pdf", Timestamp: now, URL: "mxc://hs/m2", Filename: "x.pdf"},
	})
	if len(normalized) != 4 {
		t.Fatalf("distinct messages must not merge, got %d", len(normalized))
	}
	for i, want := range []string{"$1", "$2", "$3", "$4"} {
		if normalized[i].ID != want {
			t.Fatalf("message %d = %s, want %s", i, normalized[i].ID, want)
		}
	}
}

// TestMatrixPollerBuffersAcrossSyncBatches: Element sends one room event per
// attachment as its upload finishes — each lands in its own sync batch, and
// every batch alone would start a file-only intake run. The poller holds the
// (chat, sender) group for the quiet window and releases it as one message;
// the exposed cursor stays behind until everything has been released.
func TestMatrixPollerBuffersAcrossSyncBatches(t *testing.T) {
	oldQuiet, oldMax, oldStep := matrixBatchQuiet, matrixBatchMaxWait, matrixBatchPollStep
	matrixBatchQuiet, matrixBatchMaxWait, matrixBatchPollStep = 40*time.Millisecond, 500*time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { matrixBatchQuiet, matrixBatchMaxWait, matrixBatchPollStep = oldQuiet, oldMax, oldStep })

	// Position-based sync: each since points at exactly one new event.
	event := func(id, body, url, filename string) map[string]any {
		content := map[string]any{"msgtype": "m.text", "body": body}
		if url != "" {
			content["msgtype"] = "m.file"
			content["url"] = url
			content["filename"] = filename
		}
		return map[string]any{
			"type": "m.room.message", "event_id": id, "sender": "@u:hs",
			"origin_server_ts": time.Now().UnixMilli(), "content": content,
		}
	}
	pages := map[string]struct {
		events []map[string]any
		next   string
	}{
		"":        {events: []map[string]any{event("$1", "otchet.zip", "mxc://hs/m1", "otchet.zip")}, next: "batch-1"},
		"batch-1": {events: []map[string]any{event("$2", "photo.png", "mxc://hs/m2", "photo.png")}, next: "batch-2"},
		"batch-2": {events: []map[string]any{event("$3", "вот материалы по задаче", "", "")}, next: "batch-3"},
		"batch-3": {next: "batch-3"},
	}
	var sincies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/account/whoami"):
			_ = json.NewEncoder(w).Encode(map[string]string{"user_id": "@bot:hs"})
		case strings.HasSuffix(r.URL.Path, "/sync"):
			since := r.URL.Query().Get("since")
			sincies = append(sincies, since)
			page := pages[since]
			var events []map[string]any
			if len(page.events) > 0 {
				events = page.events
			}
			room := map[string]any{"timeline": map[string]any{"events": events}}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"next_batch": page.next,
				"rooms": map[string]any{"join": map[string]any{"!r:hs": room}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	poller := newMatrixPoller()
	settings := map[string]string{"homeserver": server.URL, "access_token": "tok"}
	ctx := context.Background()

	// One Poll rides out the whole collection window: the events trickle in
	// batch by batch, then the quiet window closes and the group releases as
	// one message carrying every attachment.
	messages, next, err := poller.Poll(ctx, server.Client(), settings, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 {
		t.Fatalf("the group must release as one message, got %d: %+v", len(messages), messages)
	}
	merged := messages[0]
	if merged.ID != "$3" || merged.Text != "вот материалы по задаче" {
		t.Fatalf("unexpected merged message: %+v", merged)
	}
	if len(merged.Files) != 2 || merged.Files[0].Ref != "mxc://hs/m1" || merged.Files[1].Ref != "mxc://hs/m2" {
		t.Fatalf("attachments not merged: %+v", merged.Files)
	}
	if next != "batch-3" {
		t.Fatalf("cursor after the release = %q, want batch-3", next)
	}

	// The next sync continues from the released position.
	if _, next, err = poller.Poll(ctx, server.Client(), settings, next); err != nil {
		t.Fatal(err)
	}
	if next != "batch-3" {
		t.Fatalf("idle cursor = %q, want batch-3", next)
	}
	if sincies[len(sincies)-1] != "batch-3" {
		t.Fatalf("last sync since=%q, want batch-3", sincies[len(sincies)-1])
	}
}
