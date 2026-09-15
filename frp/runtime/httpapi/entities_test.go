package httpapi_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/temporality-project/temporality/frp/entity"
	"github.com/temporality-project/temporality/frp/ingest"
)

// TestEntityEndpoints drives the M14 flow end to end: ingest a workspace with
// a go.mod, rebuild the entity projection from the triple-bearing claims, and
// inspect entities and relations over HTTP.
func TestEntityEndpoints(t *testing.T) {
	handler := testHandler()
	root := t.TempDir()
	goMod := "module example.com/graph\n\ngo 1.24\n\nrequire (\n\tgithub.com/dep/one v1.0.0\n)\n"
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatal(err)
	}
	register := map[string]any{
		"world_id":      "entity-world",
		"state_version": 1,
		"resources":     []map[string]any{{"id": "workspace", "type": "filesystem", "path": root}},
		"capabilities":  []string{"filesystem.read"},
	}
	body, _ := json.Marshal(register)
	if created := serve(handler, http.MethodPost, "/v1/worlds", body); created.Code != http.StatusCreated {
		t.Fatalf("register world: %s", created.Body.String())
	}
	request, _ := json.Marshal(map[string]any{"world_id": "entity-world", "resource_id": "workspace", "episode_id": "ep-entity", "depth": 1})
	response := serve(handler, http.MethodPost, "/v1/ingest", request)
	if response.Code != http.StatusCreated {
		t.Fatalf("ingest status=%d body=%s", response.Code, response.Body.String())
	}
	var result ingest.Result
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	triples := 0
	for _, claim := range result.Claims {
		fetched := serve(handler, http.MethodGet, "/v1/claims/"+claim.ClaimID, nil)
		if fetched.Code != http.StatusOK {
			t.Fatalf("get claim: %s", fetched.Body.String())
		}
		var detail struct {
			Claim struct {
				Subject   string `json:"subject"`
				Predicate string `json:"predicate"`
				Object    string `json:"object"`
			} `json:"claim"`
		}
		if err := json.Unmarshal(fetched.Body.Bytes(), &detail); err != nil {
			t.Fatal(err)
		}
		if detail.Claim.Subject != "" {
			triples++
		}
	}
	if triples < 4 {
		t.Fatalf("expected at least 4 triple-bearing claims, got %d in %+v", triples, result.Claims)
	}

	// Rebuild the entity projection.
	rebuilt := serve(handler, http.MethodPost, "/v1/projections/entities/rebuild", nil)
	if rebuilt.Code != http.StatusOK {
		t.Fatalf("rebuild status=%d body=%s", rebuilt.Code, rebuilt.Body.String())
	}
	var rebuildResult struct {
		ProjectionVersion string                  `json:"projection_version"`
		Entities          []entity.Entity         `json:"entities"`
		Relations         []entity.EntityRelation `json:"relations"`
	}
	if err := json.Unmarshal(rebuilt.Body.Bytes(), &rebuildResult); err != nil {
		t.Fatal(err)
	}
	if len(rebuildResult.Entities) < 5 {
		t.Fatalf("expected module+file+toolchain+library+workspace entities, got %+v", rebuildResult.Entities)
	}
	if len(rebuildResult.Relations) < 4 {
		t.Fatalf("expected declared_in+targets+depends_on+contains relations, got %+v", rebuildResult.Relations)
	}

	// List with a type filter.
	listed := serve(handler, http.MethodGet, "/v1/entities?type=module", nil)
	if listed.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", listed.Code, listed.Body.String())
	}
	var listResult struct {
		Entities []entity.Entity `json:"entities"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &listResult); err != nil {
		t.Fatal(err)
	}
	if len(listResult.Entities) != 1 || listResult.Entities[0].Name != "example.com/graph" {
		t.Fatalf("module entities = %+v", listResult.Entities)
	}
	moduleID := listResult.Entities[0].EntityID

	// Bad type filter is rejected with the registry error.
	if got := serve(handler, http.MethodGet, "/v1/entities?type=bogus", nil); got.Code != http.StatusBadRequest {
		t.Fatalf("bogus type status=%d", got.Code)
	}

	// Get one entity with its relations.
	got := serve(handler, http.MethodGet, "/v1/entities/"+moduleID, nil)
	if got.Code != http.StatusOK {
		t.Fatalf("get entity status=%d body=%s", got.Code, got.Body.String())
	}
	var detail struct {
		Entity    entity.Entity           `json:"entity"`
		Relations []entity.EntityRelation `json:"relations"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Entity.MentionCount < 3 {
		t.Fatalf("module mention count = %d", detail.Entity.MentionCount)
	}
	predicates := map[string]int{}
	for _, relation := range detail.Relations {
		predicates[relation.Predicate]++
	}
	if predicates["declared_in"] != 1 || predicates["targets"] != 1 || predicates["depends_on"] != 1 {
		t.Fatalf("module relations = %+v", detail.Relations)
	}

	// Unknown entity id is a clean 404.
	missing := serve(handler, http.MethodGet, "/v1/entities/"+moduleID+"00", nil)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing entity status=%d", missing.Code)
	}
}
