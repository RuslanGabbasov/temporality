package skills

import (
	"strings"
	"testing"
)

const sampleMarkdown = `# Deploy service

## Purpose

Deploys a service through staging into production.

## When to use

Use this skill when a service must be deployed through
the standard deployment pipeline.

## Procedure

1. Check repository state.
2. Build the image.
3. Run tests.
4. Deploy to staging.
5. Wait until the service is healthy.
6. Promote to production.

## Constraints

- Do not deploy with uncommitted changes.
- Production promotion requires successful staging verification.
`

const sampleManifest = `
id: deploy-service
version: 1.2.0
name: Deploy service
description: Deploys a service through staging into production.
capabilities:
  - build
  - deploy
  - verify
tools:
  - run_command
  - mcp__tracker
runtime:
  sandbox: required
  network: restricted
preconditions:
  - repository_available
postconditions:
  - deployment_created
evidence:
  required:
    - test_result
`

func TestParseManifestYAML(t *testing.T) {
	m, err := ParseManifest(sampleManifest)
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != "deploy-service" || m.Version != "1.2.0" {
		t.Fatalf("unexpected identity: %+v", m)
	}
	if len(m.Capabilities) != 3 || m.Capabilities[0] != "build" {
		t.Fatalf("capabilities: %v", m.Capabilities)
	}
	if m.Runtime.Sandbox != "required" || m.Runtime.Network != "restricted" {
		t.Fatalf("runtime: %+v", m.Runtime)
	}
	if len(m.Evidence.Required) != 1 || m.Evidence.Required[0] != "test_result" {
		t.Fatalf("evidence: %+v", m.Evidence)
	}
}

func TestParseManifestJSON(t *testing.T) {
	raw := `{"id":"code-review","version":"1.0.0","name":"Code review","capabilities":["inspect_code"],"tools":["run_command"]}`
	m, err := ParseManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != "code-review" || len(m.Capabilities) != 1 {
		t.Fatalf("unexpected: %+v", m)
	}
}

func TestValidate(t *testing.T) {
	m, _ := ParseManifest(sampleManifest)
	if issues := m.Validate(); len(issues) != 0 {
		t.Fatalf("expected valid manifest, got %v", issues)
	}
	broken := Manifest{ID: "Bad ID", Version: "1.0", Name: "x"}
	issues := broken.Validate()
	if len(issues) < 4 {
		t.Fatalf("expected multiple issues, got %v", issues)
	}
}

func TestInferFromMarkdown(t *testing.T) {
	m := InferFromMarkdown(sampleMarkdown)
	if m.Name != "Deploy service" {
		t.Fatalf("name: %q", m.Name)
	}
	if m.ID != "deploy-service" {
		t.Fatalf("id: %q", m.ID)
	}
	if m.Version != "1.0.0" {
		t.Fatalf("version: %q", m.Version)
	}
	if len(m.Inferred) == 0 {
		t.Fatal("inferred fields must be recorded")
	}
	if !strings.Contains(m.Description, "staging") {
		t.Fatalf("description: %q", m.Description)
	}
}

func TestDigestBounded(t *testing.T) {
	m, _ := ParseManifest(sampleManifest)
	d := Digest(m.Name, m.Version, sampleMarkdown, m, 600)
	if len(d) > 700 { // budget + ellipsis suffix
		t.Fatalf("digest too long: %d", len(d))
	}
	if !strings.Contains(d, "Deploy service") || !strings.Contains(d, "1.2.0") {
		t.Fatalf("digest missing identity: %q", d)
	}
	if !strings.Contains(d, "Procedure:") {
		t.Fatalf("digest missing procedure: %q", d)
	}
}
