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

func TestMatrixQuestionHTMLCard(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"channel": {"type": "string", "enum": ["canary", "stable"], "title": "Release channel"},
			"ticket": {"type": "string", "pattern": "^[A-Z]{2,4}-[0-9]{3,6}$", "title": "Ticket"},
			"confirm": {"type": "boolean", "title": "Confirm"}
		},
		"required": ["channel", "ticket"]
	}`)
	msg := OutboundText{
		Question:  "Deploy <billing> now?",
		Context:   "Versions & <window> matter",
		AnswerURL: "https://ui.example/agents?project=p&x=1",
		Schema:    schema,
	}
	card := matrixQuestionHTML(msg)
	require.NotEmpty(t, card)

	// The question and context are escaped — raw angle brackets must not leak
	// into the room as markup.
	require.Contains(t, card, "Deploy &lt;billing&gt; now?")
	require.Contains(t, card, "Versions &amp; &lt;window&gt; matter")

	// Field table: labels (title, falling back to the name), shapes, marks.
	// Fields render sorted by name for a stable, diff-able card.
	require.Contains(t, card, "<table>")
	require.Contains(t, card, "Release channel")
	require.Contains(t, card, "one of: canary / stable")
	require.Contains(t, card, "Confirm</td><td>boolean")
	require.Contains(t, card, "Ticket <b>*</b>")
	require.Contains(t, card, "matches ^[A-Z]{2,4}-[0-9]{3,6}$")

	// The UI link is a real anchor with an escaped href.
	require.Contains(t, card, `<a href="https://ui.example/agents?project=p&amp;x=1">open the form in the web UI</a>`)

	// The answer hint tells the room how to reply in text.
	require.Contains(t, card, "<code>field: value</code>")
}

func TestMatrixQuestionHTMLDegradatesOnBrokenSchema(t *testing.T) {
	require.Empty(t, matrixQuestionHTML(OutboundText{Schema: json.RawMessage(`{oops`)}))
	require.Empty(t, matrixQuestionHTML(OutboundText{Schema: json.RawMessage(`{"type":"object"}`)}))
}

// A schema ask rides the threaded poster: the card ships as formatted_body and
// the returned event_id becomes the thread anchor (reactions/replies match
// their ask through it).
func TestMatrixSendTextThreadedReturnsEventIDForSchemaCard(t *testing.T) {
	var payload map[string]any
	var eventID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/send/m.room.message/") {
			require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
			eventID = "$abc123"
			_ = json.NewEncoder(w).Encode(map[string]string{"event_id": eventID})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	schema := json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"]}`)
	driver := matrixDriver{}
	postedID, err := driver.SendTextThreaded(context.Background(), server.Client(),
		map[string]string{"homeserver": server.URL, "access_token": "tok"},
		"!r:hs", OutboundText{Body: "Question body", Question: "Question body", Schema: schema})
	require.NoError(t, err)
	require.Equal(t, "$abc123", postedID)
	require.Equal(t, "org.matrix.custom.html", payload["format"])
	formatted, _ := payload["formatted_body"].(string)
	require.Contains(t, formatted, "<table>")
}
