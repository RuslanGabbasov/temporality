package agent

import (
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
