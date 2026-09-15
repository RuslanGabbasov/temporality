package httpapi_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/temporality-project/temporality/frp/ingest"
)

// TestIngestEndpoint drives the full M13 flow over HTTP: register a world
// pointing at a real workspace, ingest it, and verify observations and
// evidence-backed claims entered the substrate.
func TestIngestEndpoint(t *testing.T) {
	handler := testHandler()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/httpdemo\n\ngo 1.23\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# HTTP Demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	register := map[string]any{
		"world_id":      "ingest-world",
		"state_version": 1,
		"resources":     []map[string]any{{"id": "workspace", "type": "filesystem", "path": root}},
		"capabilities":  []string{"filesystem.read"},
	}
	body, _ := json.Marshal(register)
	if created := serve(handler, http.MethodPost, "/v1/worlds", body); created.Code != http.StatusCreated {
		t.Fatalf("register world: %s", created.Body.String())
	}
	request, _ := json.Marshal(map[string]any{"world_id": "ingest-world", "resource_id": "workspace", "episode_id": "ep-ingest", "depth": 1})
	response := serve(handler, http.MethodPost, "/v1/ingest", request)
	if response.Code != http.StatusCreated {
		t.Fatalf("ingest status=%d body=%s", response.Code, response.Body.String())
	}
	var result ingest.Result
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.ResourceID != "workspace" || len(result.Observations) < 3 {
		t.Fatalf("unexpected result: %+v", result)
	}
	propositions := map[string]ingest.ClaimRecord{}
	for _, claim := range result.Claims {
		propositions[claim.Proposition] = claim
	}
	module, ok := propositions[`Go module "example.com/httpdemo" is declared at go.mod`]
	if !ok {
		t.Fatalf("missing module claim in %v", propositions)
	}
	if len(module.Evidence) != 1 {
		t.Fatalf("module claim evidence = %v", module.Evidence)
	}
	// The claim is readable and its evidence resolves to the observation event.
	if got := serve(handler, http.MethodGet, "/v1/claims/"+module.ClaimID, nil); got.Code != http.StatusOK {
		t.Fatalf("get claim: %s", got.Body.String())
	}
	evidenceResponse := serve(handler, http.MethodGet, "/v1/claims/"+module.ClaimID+"/evidence", nil)
	if evidenceResponse.Code != http.StatusOK {
		t.Fatalf("claim evidence: %s", evidenceResponse.Body.String())
	}
	var evidence struct {
		ClaimID string `json:"claim_id"`
		Events  []struct {
			Type    string         `json:"type"`
			Payload map[string]any `json:"payload"`
		} `json:"events"`
	}
	if err := json.Unmarshal(evidenceResponse.Body.Bytes(), &evidence); err != nil {
		t.Fatal(err)
	}
	if len(evidence.Events) != 1 || evidence.Events[0].Type != "world.observation" {
		t.Fatalf("unexpected evidence events: %+v", evidence.Events)
	}
	if observationType, _ := evidence.Events[0].Payload["observation_type"].(string); observationType != "file_content" {
		t.Fatalf("evidence observation type = %q", observationType)
	}
}

// TestIngestUnknownWorldAndResource verifies failures stay honest: nothing is
// committed when the world or resource does not exist.
func TestIngestUnknownWorldAndResource(t *testing.T) {
	handler := testHandler()
	request, _ := json.Marshal(map[string]any{"world_id": "missing", "resource_id": "workspace"})
	if response := serve(handler, http.MethodPost, "/v1/ingest", request); response.Code != http.StatusNotFound {
		t.Fatalf("missing world status=%d body=%s", response.Code, response.Body.String())
	}
	register := map[string]any{"world_id": "w2", "state_version": 1, "resources": []map[string]any{{"id": "repo", "type": "filesystem", "path": t.TempDir()}}, "capabilities": []string{"filesystem.read"}}
	body, _ := json.Marshal(register)
	if created := serve(handler, http.MethodPost, "/v1/worlds", body); created.Code != http.StatusCreated {
		t.Fatalf("register world: %s", created.Body.String())
	}
	badResource, _ := json.Marshal(map[string]any{"world_id": "w2", "resource_id": "ghost"})
	if response := serve(handler, http.MethodPost, "/v1/ingest", badResource); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown resource status=%d body=%s", response.Code, response.Body.String())
	}
	listed := serve(handler, http.MethodGet, "/v1/worlds", nil)
	if !strings.Contains(listed.Body.String(), "w2") {
		t.Fatalf("world list missing w2: %s", listed.Body.String())
	}
}
