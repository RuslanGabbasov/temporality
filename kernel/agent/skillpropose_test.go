package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/temporality-project/temporality/kernel/llm"
	"github.com/temporality-project/temporality/observation"
)

type captureOutbox struct {
	mu     sync.Mutex
	events []observation.Event
}

func (c *captureOutbox) Enqueue(_ context.Context, event observation.Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, event)
	return nil
}

func (c *captureOutbox) byType(eventType string) []observation.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	var matched []observation.Event
	for _, event := range c.events {
		if event.Type == eventType {
			matched = append(matched, event)
		}
	}
	return matched
}

// skillModelStub serves one fixed skill-draft completion.
func skillModelStub(t *testing.T, draftJSON string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message": map[string]any{"content": draftJSON},
			}},
		})
	}))
	t.Cleanup(server.Close)
	return server
}

// skillWorkspaceStub fakes the kernel workspace API for skill_propose.
// exists() decides whether GET /skills/code-review finds an existing skill;
// the recorder exposes the captured create/propose payloads and any PUT call.
func skillWorkspaceStub(t *testing.T, exists func() bool) (*httptest.Server, *skillProposeRecorder) {
	t.Helper()
	recorder := &skillProposeRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.mu.Lock()
		defer recorder.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/workspace/skills/code-review":
			if !exists() {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "code-review", "version": "1.2.0"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/workspace/skills":
			_ = json.NewDecoder(r.Body).Decode(&recorder.created)
			w.WriteHeader(201)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "code-review", "version": "0.1.0"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/workspace/skills/code-review/versions":
			_ = json.NewDecoder(r.Body).Decode(&recorder.proposed)
			w.WriteHeader(201)
			_ = json.NewEncoder(w).Encode(map[string]any{"skill_id": "code-review", "version": "1.2.1", "status": "draft"})
		case r.Method == http.MethodPut:
			recorder.putPaths = append(recorder.putPaths, r.URL.Path)
			w.WriteHeader(200)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server, recorder
}

type skillProposeRecorder struct {
	mu       sync.Mutex
	created  map[string]any
	proposed map[string]any
	putPaths []string
}

func (r *skillProposeRecorder) snapshots() (created, proposed map[string]any, putPaths []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.created, r.proposed, r.putPaths
}

func decodeToolJSON(t *testing.T, content string) map[string]any {
	t.Helper()
	var parsed map[string]any
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		t.Fatalf("tool result is not JSON: %v (%q)", err, content)
	}
	return parsed
}

const proposeDraftJSON = `{"id":"code-review","name":"Code Review","description":"Reviews pull requests","markdown":"## Purpose\nReview PRs.","capabilities":["review-pr"],"tools":["run_command"]}`

// A proposal for a brand-new skill must be stored as a draft (never live),
// carry provenance, and record the skill.proposed event.
func TestSkillProposeNewSkillLandsAsDraft(t *testing.T) {
	model := skillModelStub(t, proposeDraftJSON)
	server, recorder := skillWorkspaceStub(t, func() bool { return false })
	outbox := &captureOutbox{}
	activities := &Activities{
		HTTP: server.Client(), Model: llm.New(llm.Config{BaseURL: model.URL, Model: "m"}),
		WorkspaceURL: server.URL, WorkspaceToken: "internal",
		Events: outbox, SourceID: "kernel",
	}

	result, err := activities.RunTool(context.Background(), ToolRequest{
		RunID: "run-42", OperationID: "run-42/turn/02/call-1", Project: "demo",
		Name: "skill_propose", Arguments: map[string]any{"description": "Review pull requests"},
	})
	if err != nil {
		t.Fatalf("skill_propose: %v", err)
	}
	parsed := decodeToolJSON(t, result.Content)
	if parsed["state"] != "draft" {
		t.Fatalf("state = %v, want draft", parsed["state"])
	}
	if parsed["version"] != "0.1.0" {
		t.Fatalf("version = %v, want 0.1.0", parsed["version"])
	}
	created, proposed, putPaths := recorder.snapshots()
	if len(putPaths) != 0 {
		t.Fatalf("new-skill proposal must not PUT, got %v", putPaths)
	}
	if created == nil {
		t.Fatal("create payload missing")
	}
	if created["draft"] != true {
		t.Fatalf("create payload draft flag = %v, want true (%v)", created["draft"], created)
	}
	if created["origin"] != "agent-proposal" {
		t.Fatalf("origin = %v, want agent-proposal", created["origin"])
	}
	if runs, _ := created["source_runs"].([]any); len(runs) != 1 || runs[0] != "run-42" {
		t.Fatalf("source_runs = %v, want [run-42]", created["source_runs"])
	}
	if proposed != nil {
		t.Fatalf("unexpected version proposal for a new skill: %v", proposed)
	}
	events := outbox.byType("skill.proposed")
	if len(events) != 1 {
		t.Fatalf("skill.proposed events = %d, want 1", len(events))
	}
	if err := events[0].Validate(); err != nil {
		t.Fatalf("skill.proposed invalid: %v", err)
	}
	if events[0].Data["skill_id"] != "code-review" || events[0].Context.Run != "run-42" {
		t.Fatalf("unexpected event: %+v", events[0])
	}
	if len(events[0].Evidence) != 1 || events[0].Evidence[0].Ref != "run-42/turn/02/call-1" {
		t.Fatalf("evidence = %v, want the proposing operation", events[0].Evidence)
	}
}

// A proposal for an existing skill must land as a draft version via the
// versions endpoint — the current revision is never rewritten by a PUT.
func TestSkillProposeExistingSkillProposesDraftVersion(t *testing.T) {
	model := skillModelStub(t, proposeDraftJSON)
	server, recorder := skillWorkspaceStub(t, func() bool { return true })
	outbox := &captureOutbox{}
	activities := &Activities{
		HTTP: server.Client(), Model: llm.New(llm.Config{BaseURL: model.URL, Model: "m"}),
		WorkspaceURL: server.URL, WorkspaceToken: "internal",
		Events: outbox, SourceID: "kernel",
	}

	result, err := activities.RunTool(context.Background(), ToolRequest{
		RunID: "run-43", OperationID: "run-43/turn/01/call-2", Project: "demo",
		Name: "skill_propose", Arguments: map[string]any{"description": "Review pull requests"},
	})
	if err != nil {
		t.Fatalf("skill_propose: %v", err)
	}
	parsed := decodeToolJSON(t, result.Content)
	if parsed["state"] != "draft" {
		t.Fatalf("state = %v, want draft", parsed["state"])
	}
	if parsed["version"] != "1.2.1" {
		t.Fatalf("version = %v, want 1.2.1 from the versions endpoint", parsed["version"])
	}
	created, proposed, putPaths := recorder.snapshots()
	if created != nil {
		t.Fatalf("existing-skill proposal must not POST /skills, got %v", created)
	}
	if len(putPaths) != 0 {
		t.Fatalf("proposal must never PUT the live skill, got %v", putPaths)
	}
	if proposed == nil {
		t.Fatal("version proposal payload missing")
	}
	if _, hasVersion := proposed["version"]; hasVersion {
		t.Fatalf("client must not pick the version, got %v", proposed["version"])
	}
	if proposed["origin"] != "agent-proposal" {
		t.Fatalf("origin = %v, want agent-proposal", proposed["origin"])
	}
	if manifest, _ := proposed["manifest_yaml"].(string); manifest == "" || !json.Valid([]byte(manifest)) {
		t.Fatalf("manifest_yaml must be valid JSON, got %q", manifest)
	}
	if len(outbox.byType("skill.proposed")) != 1 {
		t.Fatalf("skill.proposed events = %d, want 1", len(outbox.byType("skill.proposed")))
	}
}
