package agent

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The interlocutor block is the difference between «кому на ревью: вам или
// Руслану Габбасову?» asked OF Ruslan Gabbasov and a model that knows who it
// talks to. The run context carries a user id; the name must ride the prompt.
func TestInterlocutorBlock(t *testing.T) {
	block := interlocutorBlock(RunInput{ActorID: "user-1", ActorName: "Руслан Габбасов", ActorRole: "admin"})
	if !strings.Contains(block, "Руслан Габбасов (admin)") {
		t.Fatalf("the block must name the interlocutor and role, got %q", block)
	}
	if !strings.Contains(block, "talk") || !strings.Contains(block, "started this run") {
		t.Fatalf("the block must say the conversation is with them, got %q", block)
	}
	// No name (a trigger or agent actor): no identity block at all — a wrong
	// one is worse than none.
	if interlocutorBlock(RunInput{ActorID: "trigger-1"}) != "" {
		t.Fatal("an unnamed actor must produce no block")
	}
	// A delegated child reports the REQUESTER, not a conversation partner.
	delegated := interlocutorBlock(RunInput{ActorID: "user-1", ActorName: "Руслан Габбасов", ParentRunID: "parent-1"})
	if !strings.Contains(delegated, "requested by") || strings.Contains(delegated, "talk") {
		t.Fatalf("a delegated run must name the requester, got %q", delegated)
	}
}

func TestResolveActorIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/workspace/users/user-1" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"id":"user-1","name":"Руслан Габбасов","role":"admin"}`))
	}))
	defer server.Close()
	activities := &Activities{HTTP: server.Client(), WorkspaceURL: server.URL, WorkspaceToken: "internal"}

	name, role, err := activities.resolveActorIdentity("user-1")
	if err != nil {
		t.Fatalf("resolveActorIdentity: %v", err)
	}
	if name != "Руслан Габбасов" || role != "admin" {
		t.Fatalf("identity = %q/%q", name, role)
	}
	// A non-user actor (404) resolves to nothing, not an error.
	name, role, err = activities.resolveActorIdentity("trigger-9")
	if err != nil || name != "" || role != "" {
		t.Fatalf("a non-user actor must resolve empty: %q/%q/%v", name, role, err)
	}
}
