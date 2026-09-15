package httpapi_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/temporality-project/temporality/frp/affordance"
	"github.com/temporality-project/temporality/frp/world"
)

const worldBody = `{"world_id":"workspace-42","state_version":1,"resources":[{"id":"repo","type":"git_repository","path":"/workspace/repo"},{"id":"api","type":"http_endpoint","endpoint":"https://api.example.com"}],"capabilities":["filesystem.read","git.read","http.read"],"identities":[{"id":"agent","kind":"service"}],"credentials":[{"id":"api-key","identity_id":"agent","kind":"env","ref":"API_KEY"}],"limits":{"max_read_bytes":4096,"max_entries":100,"timeout_sec":15},"policies":[]}`

func TestWorldLifecycleAPI(t *testing.T) {
	handler := testHandler()
	created := serve(handler, http.MethodPost, "/v1/worlds", []byte(worldBody))
	if created.Code != http.StatusCreated {
		t.Fatalf("create world: %s", created.Body.String())
	}
	var createResponse struct {
		World world.World     `json:"world"`
		Event json.RawMessage `json:"event"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &createResponse); err != nil {
		t.Fatal(err)
	}
	if createResponse.World.WorldID != "workspace-42" || createResponse.World.StateVersion != 1 {
		t.Fatalf("unexpected world: %#v", createResponse.World)
	}
	if createResponse.World.Limits.TimeoutSec != 15 {
		t.Fatalf("limits not preserved: %#v", createResponse.World.Limits)
	}
	got := serve(handler, http.MethodGet, "/v1/worlds/workspace-42", nil)
	if got.Code != http.StatusOK {
		t.Fatalf("get world: %s", got.Body.String())
	}
	var stored world.World
	if err := json.Unmarshal(got.Body.Bytes(), &stored); err != nil {
		t.Fatal(err)
	}
	if stored.WorldID != "workspace-42" || len(stored.Resources) != 2 || len(stored.Credentials) != 1 {
		t.Fatalf("unexpected stored world: %#v", stored)
	}
	listed := serve(handler, http.MethodGet, "/v1/worlds", nil)
	if listed.Code != http.StatusOK {
		t.Fatalf("list worlds: %s", listed.Body.String())
	}
	var listResponse struct {
		Worlds []world.World `json:"worlds"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &listResponse); err != nil {
		t.Fatal(err)
	}
	if len(listResponse.Worlds) != 1 {
		t.Fatalf("unexpected world list: %#v", listResponse.Worlds)
	}
}

func TestCanonicalAffordancesEndpoint(t *testing.T) {
	response := serve(testHandler(), http.MethodGet, "/v1/affordances", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("list affordances: %s", response.Body.String())
	}
	var payload struct {
		Definitions []affordance.Definition `json:"definitions"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(payload.Definitions))
	for _, definition := range payload.Definitions {
		ids = append(ids, definition.ID)
		if err := definition.Validate(); err != nil {
			t.Fatalf("canonical definition %s invalid: %v", definition.ID, err)
		}
	}
	for _, want := range []string{"list_files", "read_file", "git_status", "git_log", "inspect_repository", "write_file", "run_tests"} {
		if !slices.Contains(ids, want) {
			t.Fatalf("canonical registry missing %s: %v", want, ids)
		}
	}
	if !slices.IsSorted(ids) {
		t.Fatalf("canonical registry not sorted: %v", ids)
	}
	// Path-scoped affordances declare argument hints for the model boundary.
	byID := make(map[string]affordance.Definition, len(payload.Definitions))
	for _, definition := range payload.Definitions {
		byID[definition.ID] = definition
	}
	if _, ok := byID["read_file"].InputSchema["properties"].(map[string]any)["path"]; !ok {
		t.Fatalf("read_file input schema lacks path hint: %#v", byID["read_file"].InputSchema)
	}
}

func TestWorldStateVersionMustIncrease(t *testing.T) {
	handler := testHandler()
	if created := serve(handler, http.MethodPost, "/v1/worlds", []byte(worldBody)); created.Code != http.StatusCreated {
		t.Fatalf("create world: %s", created.Body.String())
	}
	stale := serve(handler, http.MethodPost, "/v1/worlds", []byte(worldBody))
	if stale.Code != http.StatusUnprocessableEntity {
		t.Fatalf("stale state accepted: %s", stale.Body.String())
	}
	bumped := serve(handler, http.MethodPost, "/v1/worlds", []byte(`{"world_id":"workspace-42","state_version":2,"resources":[{"id":"repo","type":"git_repository","path":"/workspace/repo"}],"capabilities":["filesystem.read","git.read"]}`))
	if bumped.Code != http.StatusCreated {
		t.Fatalf("state bump rejected: %s", bumped.Body.String())
	}
	got := serve(handler, http.MethodGet, "/v1/worlds/workspace-42", nil)
	var stored world.World
	if err := json.Unmarshal(got.Body.Bytes(), &stored); err != nil {
		t.Fatal(err)
	}
	if stored.StateVersion != 2 || stored.Limits.MaxReadBytes != world.DefaultMaxReadBytes {
		t.Fatalf("unexpected bumped world: %#v", stored)
	}
}

func TestWorldValidationAPI(t *testing.T) {
	handler := testHandler()
	invalid := serve(handler, http.MethodPost, "/v1/worlds", []byte(`{"world_id":"Bad ID","state_version":1,"capabilities":[]}`))
	if invalid.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid world accepted: %s", invalid.Body.String())
	}
	unknownCapability := serve(handler, http.MethodPost, "/v1/worlds", []byte(`{"world_id":"w","state_version":1,"capabilities":["teleport.anywhere"]}`))
	if unknownCapability.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown capability accepted: %s", unknownCapability.Body.String())
	}
	missing := serve(handler, http.MethodGet, "/v1/worlds/none", nil)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing world returned %d", missing.Code)
	}
}

func TestExecutionBindsWorld(t *testing.T) {
	handler := testHandler()
	if created := serve(handler, http.MethodPost, "/v1/worlds", []byte(worldBody)); created.Code != http.StatusCreated {
		t.Fatalf("create world: %s", created.Body.String())
	}
	definition := `{"id":"list_files","execution_mode":"deterministic","input_schema":{},"capabilities":["filesystem.read"],"limits":{"timeout_sec":30,"cpu":1,"memory_mb":128,"disk_mb":64},"planner":{},"failure_policy":{"retry_transient":false,"allow_strategy_change":false,"max_retries":0}}`
	created := serve(handler, http.MethodPost, "/v1/executions", []byte(`{"definition":`+definition+`,"episode_id":"018f47a7-34b2-7d10-a932-4f3ff37a6c01","world_id":"workspace-42","arguments":{"path":"/workspace/repo"}}`))
	if created.Code != http.StatusCreated {
		t.Fatalf("create execution: %s", created.Body.String())
	}
	var response struct {
		Execution json.RawMessage `json:"execution"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	var value struct {
		WorldID      string `json:"world_id"`
		WorldVersion int    `json:"world_version"`
	}
	if err := json.Unmarshal(response.Execution, &value); err != nil {
		t.Fatal(err)
	}
	if value.WorldID != "workspace-42" || value.WorldVersion != 1 {
		t.Fatalf("execution not bound to world: %+v", value)
	}
}

func TestExecutionRejectsUngrantedWorldCapability(t *testing.T) {
	handler := testHandler()
	if created := serve(handler, http.MethodPost, "/v1/worlds", []byte(worldBody)); created.Code != http.StatusCreated {
		t.Fatalf("create world: %s", created.Body.String())
	}
	definition := `{"id":"run_command","execution_mode":"deterministic","input_schema":{},"capabilities":["process.execute"],"limits":{"timeout_sec":30,"cpu":1,"memory_mb":128,"disk_mb":64},"planner":{},"failure_policy":{"retry_transient":false,"allow_strategy_change":false,"max_retries":0}}`
	created := serve(handler, http.MethodPost, "/v1/executions", []byte(`{"definition":`+definition+`,"episode_id":"018f47a7-34b2-7d10-a932-4f3ff37a6c01","world_id":"workspace-42","arguments":{}}`))
	if created.Code != http.StatusUnprocessableEntity {
		t.Fatalf("ungranted capability accepted: %s", created.Body.String())
	}
	unknownWorld := serve(handler, http.MethodPost, "/v1/executions", []byte(`{"definition":`+definition+`,"episode_id":"018f47a7-34b2-7d10-a932-4f3ff37a6c01","world_id":"missing-world","arguments":{}}`))
	if unknownWorld.Code != http.StatusNotFound {
		t.Fatalf("unknown world returned %d", unknownWorld.Code)
	}
}
