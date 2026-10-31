package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/workspace"
)

// Reviewer expansion mirrors ask_human group semantics: org: → unit members,
// role: → role holders, project_owner → admins, user:/bare → the named user;
// deduplicated, active only.
func TestExpandArtifactReviewers(t *testing.T) {
	users := []workspace.User{
		{ID: "a", Role: "writer", OrgUnitID: "core", Active: true},
		{ID: "b", Role: "writer", OrgUnitID: "core", Active: true},
		{ID: "c", Role: "writer", OrgUnitID: "core", Active: false}, // deactivated: never notified
		{ID: "d", Role: "admin", OrgUnitID: "ops", Active: true},
		{ID: "e", Role: "writer", OrgUnitID: "ops", Active: true},
	}
	got := expandArtifactReviewers([]string{"org:core", "role:admin", "user:e", "b", "project_owner"}, users, nil)
	require.Equal(t, []string{"a", "b", "d", "e"}, func() []string {
		ids := make([]string, 0, len(got))
		for _, u := range got {
			ids = append(ids, u.ID)
		}
		return ids
	}())
	require.Empty(t, expandArtifactReviewers([]string{"org:missing"}, users, nil))
}

// The delivery channel follows the ask_human policy: preferred channel when
// enabled+configured, else the first enabled+configured one, else the web
// inbox (nothing to send through).
func TestReviewerDeliveryChannel(t *testing.T) {
	user := workspace.User{
		ID: "u", Active: true, PreferredChannel: "matrix",
		Channels: []workspace.UserChannel{
			{Type: "webhook", Address: "http://hook.local/x", Enabled: true},
			{Type: "matrix", Address: "@u:server", Enabled: false}, // preferred but disabled
		},
	}
	settings := agent.ChannelSettings{Channels: map[string]map[string]string{
		"matrix": {"homeserver": "https://m", "access_token": "t"},
	}}
	// matrix is configured but disabled for the user → webhook fallback.
	channel, ok := reviewerDeliveryChannel(user, settings)
	require.True(t, ok)
	require.Equal(t, "webhook", channel.Type)

	user.Channels[1].Enabled = true
	channel, ok = reviewerDeliveryChannel(user, settings)
	require.True(t, ok)
	require.Equal(t, "matrix", channel.Type)

	// Nothing enabled → web inbox, nothing to send.
	user.Channels[0].Enabled, user.Channels[1].Enabled = false, false
	_, ok = reviewerDeliveryChannel(user, settings)
	require.False(t, ok)
}

// End to end: submit-side notification reaches every expanded reviewer over
// their channel (webhook here — self-contained, always configured) with the
// artifact title and the UI link; web-only reviewers and failures are skipped
// quietly.
func TestNotifyArtifactReviewRequestedDelivers(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(raw))
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer hook.Close()

	store := &fakeArtifactLifecycleStore{users: []workspace.User{
		{ID: "hooked", Active: true, Channels: []workspace.UserChannel{{Type: "webhook", Address: hook.URL, Enabled: true}}},
		{ID: "webonly", Active: true}, // web inbox: skipped quietly
		{ID: "broken", Active: true, Channels: []workspace.UserChannel{{Type: "webhook", Address: "http://127.0.0.1:1/x", Enabled: true}}},
	}}
	notifier := &channelReviewNotifier{
		ws: store, client: hook.Client(),
		settings: func(context.Context) (agent.ChannelSettings, error) {
			return agent.ChannelSettings{UIURL: "http://ui.local/"}, nil
		},
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	notifier.NotifyArtifactReviewRequested(context.Background(),
		workspace.Artifact{ID: "req-spec", Title: "Requirements spec", Version: 2},
		workspace.ArtifactReview{ID: "r1", Reviewers: []string{"user:hooked", "user:webonly", "user:broken"}, Mode: "all", Note: "ready"})

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, bodies, 1) // hooked delivered; broken failed and was skipped, webonly has no channel
	require.Contains(t, bodies[0], "Requirements spec")
	require.Contains(t, bodies[0], "http://ui.local/artifacts")
}
