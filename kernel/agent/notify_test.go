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

// workspaceStub serves the user list plus the human-request upsert endpoint
// the notification activity records through.
func workspaceStub(t *testing.T, usersJSON string, seenUpsert *string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/workspace/users" && r.Method == http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(usersJSON))
		case r.URL.Path == "/v1/workspace/human-requests" && r.Method == http.MethodPost:
			raw, _ := io.ReadAll(r.Body)
			if seenUpsert != nil {
				*seenUpsert = string(raw)
			}
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// TestNotifyChannelDeliversViaMatrix exercises the full resolution path: the
// recipient resolves from the workspace user list, the preferred matrix
// channel carries the question, the delivery is reported as delivered and the
// human_request row is upserted with the resolved user.
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

	var upsert string
	workspace := workspaceStub(t, `{"users":[{"id":"ruslan","name":"Ruslan","active":true,"channels":[{"type":"matrix","address":"!room:matrix.org","enabled":true}],"preferred_channel":"matrix"}]}`, &upsert)

	activities := &Activities{HTTP: homeserver.Client(), WorkspaceURL: workspace.URL, WorkspaceToken: "internal"}
	delivery, err := activities.NotifyChannel(context.Background(), NotifyChannelRequest{
		Recipient: "user:ruslan", Question: "Which database?", OperationID: "run-1/turn/01/ask-1",
		Options: []string{"postgres", "sqlite"}, Project: "repo-a", RunID: "run-1", TimeoutSeconds: 300,
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

	var row map[string]any
	require.NoError(t, json.Unmarshal([]byte(upsert), &row))
	require.Equal(t, "run-1/turn/01/ask-1", row["id"])
	require.Equal(t, "ruslan", row["resolved_user"])
	require.Equal(t, "delivered", row["status"])
	require.Equal(t, "matrix", row["channel"])
}

// TestNotifyChannelFallsBackToWebWhenTransportNotConfigured keeps the loop
// alive when the profile names a channel the kernel cannot send through.
func TestNotifyChannelFallsBackToWebWhenTransportNotConfigured(t *testing.T) {
	workspace := workspaceStub(t, `{"users":[{"id":"ruslan","name":"Ruslan","active":true,"channels":[{"type":"telegram","address":"12345","enabled":true}],"preferred_channel":"telegram"}]}`, nil)

	activities := &Activities{HTTP: workspace.Client(), WorkspaceURL: workspace.URL, WorkspaceToken: "internal"}
	delivery, err := activities.NotifyChannel(context.Background(), NotifyChannelRequest{Recipient: "ruslan", Question: "Q?", OperationID: "op-1", RunID: "run-1", Project: "p"})
	require.NoError(t, err)
	require.Equal(t, "web", delivery.Channel)
	require.Equal(t, "delivered", delivery.Status)
	require.Contains(t, delivery.Detail, "telegram unavailable")
}

// TestNotifyChannelRejectsUnknownRecipient reports a rejection (§30 spirit:
// no user, no delivery) when neither the recipient nor the actor resolves to
// a workspace user — e.g. webhook-* actors without a named recipient.
func TestNotifyChannelRejectsUnknownRecipient(t *testing.T) {
	workspace := workspaceStub(t, `{"users":[{"id":"someone-else","name":"Someone Else","active":true}]}`, nil)

	activities := &Activities{HTTP: workspace.Client(), WorkspaceURL: workspace.URL, WorkspaceToken: "internal"}
	delivery, err := activities.NotifyChannel(context.Background(), NotifyChannelRequest{ActorID: "webhook-trigger-1", Question: "Q?", OperationID: "op-1", RunID: "run-1", Project: "p"})
	require.NoError(t, err)
	require.Equal(t, "rejected", delivery.Status)
	require.Contains(t, delivery.Detail, "could not be resolved")
}

// TestNotifyChannelRejectsUserOutsideIdentityTargets verifies the §30 gate:
// an execution identity with human_targets only permits listed users.
func TestNotifyChannelRejectsUserOutsideIdentityTargets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/workspace/users":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"users":[{"id":"ruslan","active":true},{"id":"eldar","active":true}]}`))
		case r.URL.Path == "/v1/workspace/execution-identities":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"identities":[{"id":"ci-bot","human_targets":["eldar"]}]}`))
		case r.URL.Path == "/v1/workspace/human-requests":
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	activities := &Activities{HTTP: server.Client(), WorkspaceURL: server.URL, WorkspaceToken: "internal"}
	// ruslan is a valid user but not in the identity's human_targets.
	delivery, err := activities.NotifyChannel(context.Background(), NotifyChannelRequest{
		Recipient: "ruslan", Question: "Q?", OperationID: "op-1", RunID: "run-1", Project: "p",
		ExecutionIdentityID: "ci-bot",
	})
	require.NoError(t, err)
	require.Equal(t, "rejected", delivery.Status)
	require.Contains(t, delivery.Detail, `may not address user "ruslan"`)

	// eldar is listed: the delivery proceeds.
	delivery, err = activities.NotifyChannel(context.Background(), NotifyChannelRequest{
		Recipient: "eldar", Question: "Q?", OperationID: "op-2", RunID: "run-1", Project: "p",
		ExecutionIdentityID: "ci-bot",
	})
	require.NoError(t, err)
	require.Equal(t, "delivered", delivery.Status)
}
