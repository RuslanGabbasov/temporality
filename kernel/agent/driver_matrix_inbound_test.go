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
