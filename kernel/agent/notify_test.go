package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPickChannelHonorsPreferredEnabledAndConfigured(t *testing.T) {
	profile := recipientChannels{
		Channels: []recipientChannel{
			{Type: "matrix", Address: "!room:matrix.org", Enabled: true},
			{Type: "telegram", Address: "12345", Enabled: true},
		},
		Preferred: "telegram",
	}
	channel, mode := pickChannel(profile, func(string) bool { return true })
	require.Equal(t, "telegram", mode)
	require.Equal(t, "12345", channel.Address)

	// Preferred transport not configured on the kernel: fall back to the
	// next enabled channel, not to silence.
	channel, mode = pickChannel(profile, func(channelType string) bool { return channelType != "telegram" })
	require.Equal(t, "matrix", mode)
	require.Equal(t, "!room:matrix.org", channel.Address)

	// Disabled entries never carry deliveries even when preferred.
	disabled := recipientChannels{
		Channels:  []recipientChannel{{Type: "matrix", Address: "!room:matrix.org", Enabled: false}},
		Preferred: "matrix",
	}
	_, mode = pickChannel(disabled, func(string) bool { return true })
	require.Equal(t, "web", mode)

	// No channels at all: the web inbox is always available.
	_, mode = pickChannel(recipientChannels{}, func(string) bool { return true })
	require.Equal(t, "web", mode)
}

func TestNormalizeRecipientAcceptsUserPrefix(t *testing.T) {
	require.Equal(t, "ruslan", normalizeRecipient("user:ruslan"))
	require.Equal(t, "ruslan", normalizeRecipient(" ruslan "))
	require.Equal(t, "", normalizeRecipient(""))
}

// TestNotifyChannelDeliversViaMatrix exercises the full resolution path: the
// recipient's profile comes from the workspace API, the preferred matrix
// channel carries the question, and the delivery is reported as delivered.
func TestNotifyChannelDeliversViaMatrix(t *testing.T) {
	var seenPath, seenAuth, seenBody string
	homeserver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		seenAuth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		seenBody = string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"event_id":"$1"}`))
	}))
	defer homeserver.Close()
	t.Setenv("KERNEL_MATRIX_HOMESERVER", homeserver.URL)
	t.Setenv("KERNEL_MATRIX_ACCESS_TOKEN", "bot-token")

	workspace := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/workspace/users/ruslan", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ruslan","channels":[{"type":"matrix","address":"!room:matrix.org","enabled":true}],"preferred_channel":"matrix"}`))
	}))
	defer workspace.Close()

	activities := &Activities{HTTP: homeserver.Client(), WorkspaceURL: workspace.URL, WorkspaceToken: "internal"}
	delivery, err := activities.NotifyChannel(context.Background(), NotifyChannelRequest{
		Recipient: "user:ruslan", Question: "Which database?",
		Options: []string{"postgres", "sqlite"}, Project: "repo-a", RunID: "run-1",
	})
	require.NoError(t, err)
	require.Equal(t, "matrix", delivery.Channel)
	require.Equal(t, "delivered", delivery.Status)
	require.Equal(t, "ruslan", delivery.Recipient)
	require.True(t, strings.HasPrefix(seenPath, "/_matrix/client/v3/rooms/"), "unexpected room path %q", seenPath)
	require.Contains(t, seenPath, "m.room.message")
	require.Equal(t, "Bearer bot-token", seenAuth)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(seenBody), &payload))
	require.Equal(t, "m.text", payload["msgtype"])
	require.Contains(t, payload["body"], "Which database?")
	require.Contains(t, payload["body"], "postgres / sqlite")
}

// TestNotifyChannelFallsBackToWebWhenTransportNotConfigured keeps the loop
// alive when the profile names a channel the kernel cannot send through.
func TestNotifyChannelFallsBackToWebWhenTransportNotConfigured(t *testing.T) {
	workspace := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ruslan","channels":[{"type":"telegram","address":"12345","enabled":true}],"preferred_channel":"telegram"}`))
	}))
	defer workspace.Close()

	activities := &Activities{HTTP: workspace.Client(), WorkspaceURL: workspace.URL, WorkspaceToken: "internal"}
	delivery, err := activities.NotifyChannel(context.Background(), NotifyChannelRequest{Recipient: "ruslan", Question: "Q?"})
	require.NoError(t, err)
	require.Equal(t, "web", delivery.Channel)
	require.Equal(t, "delivered", delivery.Status)
	require.Contains(t, delivery.Detail, "telegram unavailable")
}

// TestNotifyChannelSkipsUnknownRecipient reports a skip instead of failing
// when the run actor is not a workspace user (e.g. webhook-* actors).
func TestNotifyChannelSkipsUnknownRecipient(t *testing.T) {
	workspace := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/users") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"users":[{"id":"someone-else","name":"Someone Else"}]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer workspace.Close()

	activities := &Activities{HTTP: workspace.Client(), WorkspaceURL: workspace.URL, WorkspaceToken: "internal"}
	delivery, err := activities.NotifyChannel(context.Background(), NotifyChannelRequest{ActorID: "webhook-trigger-1", Question: "Q?"})
	require.NoError(t, err)
	require.Equal(t, "web", delivery.Channel)
	require.Equal(t, "skipped", delivery.Status)
}
