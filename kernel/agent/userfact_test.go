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

// TestUserRememberTool: the tool posts the fact to the sender's user id —
// the kernel resolves whose fact it is from the run's actor.
func TestUserRememberTool(t *testing.T) {
	var path string
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/facts") {
			path = r.URL.Path
			_ = json.NewDecoder(r.Body).Decode(&payload)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 1, "fact": "предпочитает краткие ответы"})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	activities := &Activities{HTTP: server.Client(), WorkspaceURL: server.URL, WorkspaceToken: "internal"}
	result, err := activities.RunTool(context.Background(), ToolRequest{
		Name: "user_remember", ActorID: "u1", Project: "omni", RunID: "run-1",
		Arguments: map[string]any{"fact": "предпочитает краткие ответы"},
	})
	require.NoError(t, err)
	require.Contains(t, result.Content, "Saved")
	require.Contains(t, path, "/users/u1/facts")
	require.Equal(t, "предпочитает краткие ответы", payload["fact"])

	// No identified sender — nothing to store, no error.
	result, err = activities.RunTool(context.Background(), ToolRequest{
		Name: "user_remember", Project: "omni", RunID: "run-2",
		Arguments: map[string]any{"fact": "x"},
	})
	require.NoError(t, err)
	require.Contains(t, result.Content, "no sender resolved")

	// Empty fact — a 422-style guidance message, not an error.
	result, err = activities.RunTool(context.Background(), ToolRequest{
		Name: "user_remember", ActorID: "u1", Arguments: map[string]any{},
	})
	require.NoError(t, err)
	require.Contains(t, result.Content, "fact is required")
}
