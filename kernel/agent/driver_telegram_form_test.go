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

// A schema ask with a hosted form link carries an inline keyboard with one
// URL button — the form opens in the browser, no bot callbacks involved.
func TestTelegramSendTextFormButton(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer server.Close()

	// Swap the hardwired api.telegram.org host via the settings map is not
	// possible — the endpoint is const. Route through the test server by
	// pointing BaseURL... the driver has no BaseURL knob, so assert through
	// the request the server actually received (the driver calls
	// api.telegram.org; use a transport override instead).
	client := &http.Client{Transport: roundTripperTo(strings.TrimPrefix(server.URL, "http://"))}

	driver := telegramDriver{}
	err := driver.SendText(context.Background(), client,
		map[string]string{"bot_token": "tok"},
		"123", OutboundText{
			Body:    "Question body",
			FormURL: "https://ui.example/kernel-api/forms/abc123",
			Lang:    "en",
		})
	require.NoError(t, err)

	markup, _ := payload["reply_markup"].(map[string]any)
	require.NotNil(t, markup, "reply_markup missing")
	rows, _ := markup["inline_keyboard"].([]any)
	require.Len(t, rows, 1)
	buttons, _ := rows[0].([]any)
	require.Len(t, buttons, 1)
	button, _ := buttons[0].(map[string]any)
	require.Equal(t, "Open form", button["text"])
	require.Equal(t, "https://ui.example/kernel-api/forms/abc123", button["url"])
}

func TestTelegramSendTextWithoutFormHasNoMarkup(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer server.Close()

	client := &http.Client{Transport: roundTripperTo(strings.TrimPrefix(server.URL, "http://"))}
	driver := telegramDriver{}
	require.NoError(t, driver.SendText(context.Background(), client,
		map[string]string{"bot_token": "tok"}, "123", OutboundText{Body: "plain"}))
	require.NotContains(t, payload, "reply_markup")
}

// roundTripperTo redirects every request to the given base host (host:port
// only, no scheme — always plain http to the test server), preserving path
// and body — the telegram driver hardwires api.telegram.org.
type roundTripperTo string

func (rt roundTripperTo) RoundTrip(req *http.Request) (*http.Response, error) {
	out := req.Clone(req.Context())
	out.URL.Scheme = "http"
	out.URL.Host = string(rt)
	return http.DefaultTransport.RoundTrip(out)
}
