package agent

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/temporality-project/temporality/kernel/llm"
)

// A clean install (no TEMPORALITY_MODEL_*): NewActivities must boot, the
// default client stays nil, and a call without a selected model fails with an
// actionable message instead of an empty-URL HTTP error.
func TestNewActivitiesBootsWithoutDefaultModel(t *testing.T) {
	t.Setenv("TEMPORALITY_MODEL_BASE_URL", "")
	t.Setenv("TEMPORALITY_MODEL_ID", "")
	t.Setenv("TEMPORALITY_MODEL_API_KEY", "")

	activities, err := NewActivities(nil)
	if err != nil {
		t.Fatalf("NewActivities: %v", err)
	}
	if activities.Model != nil {
		t.Fatalf("default model client = %v, want nil", activities.Model.Model())
	}

	_, err = activities.CallModel(context.Background(), ModelRequest{
		Messages: []llm.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("CallModel without any model succeeded")
	}
	for _, want := range []string{"no model configured", "TEMPORALITY_MODEL"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err.Error(), want)
		}
	}
}

// An explicit model no provider serves, with no env default either: the error
// must name the model and the two ways out (add it to a provider, or set the
// env default) — not the raw "TEMPORALITY_MODEL_* are required" boot message.
func TestCallModelUnservedModelWithoutDefaultFailsActionably(t *testing.T) {
	var lookups int32
	workspace := routingFakeWorkspace(t, "proj-1", "mimo-x", "http://unused.example", &lookups)
	defer workspace.Close()

	t.Setenv("TEMPORALITY_MODEL_BASE_URL", "")
	t.Setenv("TEMPORALITY_MODEL_ID", "")

	activities := &Activities{HTTP: &http.Client{}, WorkspaceURL: workspace.URL}
	_, err := activities.CallModel(context.Background(), ModelRequest{
		Model: "mimo-y", Project: "proj-1",
		Messages: []llm.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("CallModel with an unserved model succeeded")
	}
	for _, want := range []string{"mimo-y", "provider", "TEMPORALITY_MODEL"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err.Error(), want)
		}
	}
}
