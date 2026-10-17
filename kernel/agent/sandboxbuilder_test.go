package agent

import (
	"strings"
	"testing"
)

func TestParseSandboxDraftValid(t *testing.T) {
	known := []string{"node", "python", "browser-e2e"}
	reply := "```json" + `
{"id": "e2e-web", "name": "Web E2E", "description": "Playwright browser tests",
 "base": "browser-e2e", "packages": ["chromium"], "toolchains": ["playwright"],
 "memory": "4g", "cpus": "4", "scratch_size": "2g", "tmp_size": "1g",
 "rationale": "Браузерные тесты требуют памяти и большого scratch."}
` + "```" + `
Пояснение вне JSON.`
	draft, err := parseSandboxDraft(known)(reply)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if draft.ID != "e2e-web" || draft.Base != "browser-e2e" || draft.Memory != "4g" {
		t.Errorf("draft = %+v", draft)
	}
	if len(draft.Packages) != 1 {
		t.Errorf("packages = %v", draft.Packages)
	}
}

func TestParseSandboxDraftNormalizesID(t *testing.T) {
	draft, err := parseSandboxDraft([]string{"node"})(`{"id": "My Tool 1!", "name": "x", "base": "node"}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if draft.ID != "my-tool-1" {
		t.Errorf("id = %q, want normalized", draft.ID)
	}
}

func TestParseSandboxDraftRejectsUnknownBase(t *testing.T) {
	_, err := parseSandboxDraft([]string{"node"})(`{"id": "x1", "name": "x", "base": "windows-11"}`)
	if err == nil || !strings.Contains(err.Error(), "unknown base") {
		t.Errorf("err = %v, want unknown base", err)
	}
}

func TestParseSandboxDraftRejectsMalformedResources(t *testing.T) {
	_, err := parseSandboxDraft([]string{"node"})(`{"id": "x1", "name": "x", "base": "node", "memory": "a/b c"}`)
	if err == nil || !strings.Contains(err.Error(), "malformed memory") {
		t.Errorf("err = %v, want malformed memory", err)
	}
}

func TestParseSandboxDraftRequiresID(t *testing.T) {
	if _, err := parseSandboxDraft([]string{"node"})(`{"id": "x", "name": "", "base": "node"}`); err == nil {
		t.Fatal("empty/short id must fail")
	}
}
