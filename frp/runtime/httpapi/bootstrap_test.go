package httpapi_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/temporality-project/temporality/frp/render"
	"github.com/temporality-project/temporality/frp/runtime/httpapi"
	"github.com/temporality-project/temporality/frp/substrate/memory"
	"github.com/temporality-project/temporality/frp/world"
)

func memoryStore() *memory.Store { return memory.New() }

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// TestBootstrapEndpoint drives the M15 first-contact flow: a world with a real
// (non-git) workspace, an empty substrate, and an objective. One bootstrap
// call must create the episode, run bounded discovery through the normal
// observation pipeline, populate claims/regions/entities, and hand back the
// first frame plus its render.
func TestBootstrapEndpoint(t *testing.T) {
	store := memoryStore()
	handler := httpapi.New(store, testLogger())
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/firstcontact\n\ngo 1.23\n\nrequire github.com/demo/dep v1.0.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# First Contact\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "cmd", "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "cmd", "app", "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	register := map[string]any{
		"world_id":      "bootstrap-world",
		"state_version": 1,
		"resources":     []map[string]any{{"id": "workspace", "type": "filesystem", "path": root}},
		"capabilities":  []string{"filesystem.read", "git.read"},
		"limits":        map[string]any{"max_read_bytes": 4096, "max_entries": 100, "timeout_sec": 10},
	}
	body, _ := json.Marshal(register)
	if created := serve(handler, http.MethodPost, "/v1/worlds", body); created.Code != http.StatusCreated {
		t.Fatalf("register world: %s", created.Body.String())
	}
	request, _ := json.Marshal(map[string]any{
		"world_id":       "bootstrap-world",
		"objective_text": "Find the cause of the failing test and propose a fix",
		"budget_tokens":  16000,
	})
	response := serve(handler, http.MethodPost, "/v1/bootstrap", request)
	if response.Code != http.StatusCreated {
		t.Fatalf("bootstrap status=%d body=%s", response.Code, response.Body.String())
	}
	var result struct {
		EpisodeID   string `json:"episode_id"`
		BranchID    string `json:"branch_id"`
		ObjectiveID string `json:"objective_id"`
		FrameID     string `json:"frame_id"`
		WorldID     string `json:"world_id"`
		Regions     int    `json:"regions"`
		Entities    int    `json:"entities"`
		Render      *render.Packet
		Ingestions  []struct {
			ResourceID string `json:"resource_id"`
			Error      string `json:"error"`
		} `json:"ingestions"`
		Summary map[string]any `json:"summary"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.EpisodeID == "" || result.ObjectiveID == "" || result.FrameID == "" || result.WorldID != "bootstrap-world" {
		t.Fatalf("bootstrap identity incomplete: %+v", result)
	}
	if len(result.Ingestions) != 1 || result.Ingestions[0].Error != "" || result.Ingestions[0].ResourceID != "workspace" {
		t.Fatalf("unexpected ingestions: %+v", result.Ingestions)
	}
	if result.Regions == 0 || result.Entities == 0 {
		t.Fatalf("substrate stayed empty: %+v", result.Summary)
	}
	if result.Summary["observations"].(float64) == 0 || result.Summary["claims"].(float64) == 0 {
		t.Fatalf("discovery produced nothing: %+v", result.Summary)
	}
	if result.Render == nil || result.Render.FrameID != result.FrameID {
		t.Fatalf("first render missing: %+v", result.Render)
	}
	// The episode is real: objective, frame, events, and observations resolve.
	if got := serve(handler, http.MethodGet, "/v1/objectives/"+result.ObjectiveID, nil); got.Code != http.StatusOK {
		t.Fatalf("objective missing: %s", got.Body.String())
	}
	if got := serve(handler, http.MethodGet, "/v1/frames/"+result.FrameID, nil); got.Code != http.StatusOK {
		t.Fatalf("frame missing: %s", got.Body.String())
	}
	events := serve(handler, http.MethodGet, "/v1/events?episode_id="+result.EpisodeID+"&limit=100", nil)
	var list struct {
		Events []struct {
			Type string `json:"type"`
		} `json:"events"`
	}
	if err := json.Unmarshal(events.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	for _, event := range list.Events {
		kinds[event.Type]++
	}
	if kinds[world.EventWorldObservation] == 0 {
		t.Fatalf("no observations in episode: %v", kinds)
	}
	if kinds["claim.candidate"] == 0 {
		t.Fatalf("no candidate claims in episode: %v", kinds)
	}
	if kinds["episode.started"] != 1 || kinds["frame.created"] != 1 {
		t.Fatalf("bootstrap episode events missing: %v", kinds)
	}
}

func TestBootstrapRejectsUnknownWorldAndMissingText(t *testing.T) {
	handler := testHandler()
	missing := serve(handler, http.MethodPost, "/v1/bootstrap", []byte(`{"world_id":"none","objective_text":"x"}`))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("unknown world returned %d", missing.Code)
	}
	noText := serve(handler, http.MethodPost, "/v1/bootstrap", []byte(`{"world_id":"x"}`))
	if noText.Code != http.StatusBadRequest {
		t.Fatalf("missing objective_text returned %d", noText.Code)
	}
}
