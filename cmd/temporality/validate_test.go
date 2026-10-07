package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validManifest = `
id: deploy-service
version: 1.2.0
name: Deploy service
description: Deploys a service through staging into production.
capabilities: [build, deploy]
tools: [run_command]
runtime:
  sandbox: required
  network: restricted
`

const skillMarkdown = `# Deploy service

## Purpose

Deploys a service through staging into production.

## Procedure

1. Build the image.
2. Deploy to staging.
`

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestValidateValidManifest(t *testing.T) {
	path := writeTemp(t, "skill.yaml", validManifest)
	code, stdout, stderr := runCLI(t, emptyEnv, "skill", "validate", path)
	if code != 0 {
		t.Fatalf("exit %d: %s%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "valid    deploy-service 1.2.0") {
		t.Fatalf("stdout: %s", stdout)
	}
}

func TestValidateDirectoryWithBothFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "skill.yaml"), []byte(validManifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(skillMarkdown), 0o644); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runCLI(t, emptyEnv, "skill", "validate", dir)
	if code != 0 {
		t.Fatalf("exit %d: %s%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "(skill.yaml + SKILL.md)") {
		t.Fatalf("stdout: %s", stdout)
	}
}

func TestValidateMarkdownInference(t *testing.T) {
	path := writeTemp(t, "SKILL.md", skillMarkdown)
	code, stdout, stderr := runCLI(t, emptyEnv, "skill", "validate", path)
	if code != 0 {
		t.Fatalf("exit %d: %s%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "valid    deploy-service 1.0.0") {
		t.Fatalf("stdout: %s", stdout)
	}
	if !strings.Contains(stdout, "inferred from SKILL.md: name, description, capabilities") {
		t.Fatalf("stdout: %s", stdout)
	}
}

func TestValidateInvalidManifest(t *testing.T) {
	path := writeTemp(t, "skill.yaml", "id: Deploy Service\nversion: 1.0\nname: X\n")
	code, stdout, stderr := runCLI(t, emptyEnv, "skill", "validate", path)
	if code != 1 {
		t.Fatalf("exit %d, want 1: %s%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "invalid") || !strings.Contains(stdout, "id:") || !strings.Contains(stdout, "capabilities:") {
		t.Fatalf("stdout: %s", stdout)
	}
}

func TestValidateBrokenYAML(t *testing.T) {
	path := writeTemp(t, "skill.yaml", "id: [unclosed\nversion: 1.0.0\nname: X\ncapabilities: [a]\ntools: [t]\n")
	code, stdout, _ := runCLI(t, emptyEnv, "skill", "validate", path)
	if code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if !strings.Contains(stdout, "manifest_yaml:") {
		t.Fatalf("stdout: %s", stdout)
	}
}

func TestValidateJSONOutput(t *testing.T) {
	path := writeTemp(t, "skill.yaml", validManifest)
	code, stdout, stderr := runCLI(t, emptyEnv, "skill", "validate", "--json", path)
	if code != 0 {
		t.Fatalf("exit %d: %s%s", code, stdout, stderr)
	}
	var result validationResult
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("stdout not JSON: %v\n%s", err, stdout)
	}
	if !result.Valid || result.ID != "deploy-service" || result.Version != "1.2.0" {
		t.Fatalf("result: %+v", result)
	}
}

func TestValidateMissingFile(t *testing.T) {
	code, _, stderr := runCLI(t, emptyEnv, "skill", "validate", "./does-not-exist/skill.yaml")
	if code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if !strings.Contains(stderr, "temporality skill validate:") {
		t.Fatalf("stderr: %s", stderr)
	}
}

func TestValidateEmptyDirectory(t *testing.T) {
	code, _, stderr := runCLI(t, emptyEnv, "skill", "validate", t.TempDir())
	if code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if !strings.Contains(stderr, "no skill.yaml or SKILL.md found") {
		t.Fatalf("stderr: %s", stderr)
	}
}

func TestValidateNotASkillFile(t *testing.T) {
	path := writeTemp(t, "notes.txt", "hello")
	code, _, stderr := runCLI(t, emptyEnv, "skill", "validate", path)
	if code != 1 || !strings.Contains(stderr, "is not a skill file") {
		t.Fatalf("exit %d stderr: %s", code, stderr)
	}
}

func TestValidateFlagsAfterPath(t *testing.T) {
	path := writeTemp(t, "skill.yaml", validManifest)
	var stdout, stderr bytes.Buffer
	code := run([]string{"skill", "validate", path, "--json"}, emptyEnv, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d: %s%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), `"valid": true`) {
		t.Fatalf("stdout: %s", stdout.String())
	}
}

func TestSlugifyMatchesServer(t *testing.T) {
	cases := map[string]string{
		"Deploy Service": "deploy-service",
		"  Q&A review ":  "qa-review",
		"ABC_123":        "abc-123",
		"!!!":            "untitled",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Fatalf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}
