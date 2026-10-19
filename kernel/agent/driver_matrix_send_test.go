package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestMatrixSendTextRendersMarkdown: agent answers are markdown — the matrix
// delivery carries formatted_body (org.matrix.custom.html) next to the plain
// fallback body, so Element renders headings, bold and tables instead of a
// wall of asterisks. Raw HTML in the model output stays escaped.
func TestMatrixSendTextRendersMarkdown(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/send/m.room.message/"):
			require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
			_ = json.NewEncoder(w).Encode(map[string]string{"event_id": "$out"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	driver := matrixDriver{}
	err := driver.SendText(context.Background(), server.Client(),
		map[string]string{"homeserver": server.URL, "access_token": "tok"},
		"!r:hs", OutboundText{Body: "## Итог\n\n**Важно** и _акцент_\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n<script>alert(1)</script>"})
	require.NoError(t, err)

	require.Equal(t, "org.matrix.custom.html", payload["format"])
	html, _ := payload["formatted_body"].(string)
	require.Contains(t, html, "<h2>Итог</h2>")
	require.Contains(t, html, "<strong>Важно</strong>")
	require.Contains(t, html, "<em>акцент</em>")
	require.Contains(t, html, "<table>")
	// Raw HTML is omitted by default (no Unsafe) — no markup injection.
	require.Contains(t, html, "raw HTML omitted")
	require.NotContains(t, html, "<script>")
	// The plain body stays the unformatted fallback, reply hint included.
	body, _ := payload["body"].(string)
	require.Contains(t, body, "## Итог")
	require.Contains(t, body, "Reply in this room to answer.")
}

// TestMatrixSendTextPlainStaysPlain: a message without markdown renders as a
// single escaped paragraph — no markdown constructs invent structure.
func TestMatrixSendTextPlainStaysPlain(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/send/m.room.message/") {
			require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
			_ = json.NewEncoder(w).Encode(map[string]string{"event_id": "$out"})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	driver := matrixDriver{}
	err := driver.SendText(context.Background(), server.Client(),
		map[string]string{"homeserver": server.URL, "access_token": "tok"},
		"!r:hs", OutboundText{Body: "Temporality: rate limit reached (10 messages per hour)."})
	require.NoError(t, err)

	require.Equal(t, "org.matrix.custom.html", payload["format"])
	html, _ := payload["formatted_body"].(string)
	require.Contains(t, html, "rate limit reached (10 messages per hour).")
	require.NotContains(t, html, "<strong>")
	require.NotContains(t, html, "<h")
}

// TestMatrixMarkRead: the ReadMarker capability posts an m.read receipt for
// the sender's event as the bot account.
func TestMatrixMarkRead(t *testing.T) {
	var receiptPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/receipt/m.read/") {
			receiptPath = r.URL.Path
			_ = json.NewEncoder(w).Encode(map[string]any{})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	var driver ReadMarker = matrixDriver{}
	require.NoError(t, driver.MarkRead(context.Background(), server.Client(),
		map[string]string{"homeserver": server.URL, "access_token": "tok"}, "!r:hs", "$evt1"))
	require.Contains(t, receiptPath, "/rooms/!r:hs/receipt/m.read/$evt1")
}
