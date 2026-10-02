package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseDraftCompletionPlainJSON(t *testing.T) {
	draft, err := parseDraftCompletion(`{
		"id": "code-review",
		"version": "1.0.0",
		"name": "Code Review",
		"description": "Reviews pull requests",
		"markdown": "## Purpose\nReview PRs.",
		"inputs": {"repository": {"type": "string", "required": true}},
		"capabilities": ["review-pr"],
		"tools": ["run_command"],
		"runtime": {"sandbox": "optional", "mcp": ["tracker"]},
		"preconditions": ["branch exists"],
		"evidence": {"required": ["review comment"]},
		"questions": [{"field": "runtime.mcp", "question": "Publish comments automatically?"}]
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if draft.Name != "Code Review" || draft.Description != "Reviews pull requests" {
		t.Fatalf("unexpected header fields: %+v", draft)
	}
	var manifest map[string]any
	if err := json.Unmarshal(draft.Manifest, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest["id"] != "code-review" {
		t.Fatalf("id not propagated: %v", manifest["id"])
	}
	inputs, ok := manifest["inputs"].(map[string]any)
	if !ok {
		t.Fatalf("inputs missing: %v", manifest["inputs"])
	}
	repo, ok := inputs["repository"].(map[string]any)
	if !ok || repo["required"] != true {
		t.Fatalf("input field lost required flag: %v", inputs["repository"])
	}
	runtime, ok := manifest["runtime"].(map[string]any)
	if !ok || runtime["sandbox"] != "optional" {
		t.Fatalf("runtime lost: %v", manifest["runtime"])
	}
	if _, hasEvidence := manifest["evidence"]; !hasEvidence {
		t.Fatal("evidence.required dropped")
	}
	if len(draft.Questions) != 1 || draft.Questions[0].Field != "runtime.mcp" {
		t.Fatalf("questions lost: %+v", draft.Questions)
	}
}

func TestParseDraftCompletionFencedJSON(t *testing.T) {
	draft, err := parseDraftCompletion("Some preamble\n```json\n{\"name\": \"Deploy\", \"markdown\": \"x\"}\n```\ntrailing")
	if err != nil {
		t.Fatal(err)
	}
	if draft.Name != "Deploy" {
		t.Fatalf("name = %q", draft.Name)
	}
}

func TestParseDraftCompletionDropsEmptySections(t *testing.T) {
	draft, err := parseDraftCompletion(`{"name": "Empty", "capabilities": [], "tools": [], "inputs": {}, "outputs": {}}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(draft.Manifest), "capabilities") {
		t.Fatalf("empty capabilities leaked into manifest: %s", draft.Manifest)
	}
	if len(draft.Questions) != 0 {
		t.Fatalf("questions should be empty: %+v", draft.Questions)
	}
}

func TestParseDraftCompletionInvalidJSON(t *testing.T) {
	if _, err := parseDraftCompletion("not json at all"); err == nil {
		t.Fatal("expected error for non-JSON content")
	}
}
