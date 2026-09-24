package main

import (
	"strings"
	"testing"

	"github.com/temporality-project/temporality/kernel/agent"
)

func TestWorkflowIDForScopesByProjectAndRun(t *testing.T) {
	first := workflowIDFor("install-a", "project-a", "run-1")
	if first != workflowIDFor("install-a", "project-a", "run-1") {
		t.Fatal("workflow ID must be stable")
	}
	if first == workflowIDFor("install-a", "project-b", "run-1") {
		t.Fatal("same run ID in different projects must have separate workflows")
	}
	if first == workflowIDFor("install-a", "project-a", "run-2") {
		t.Fatal("different run IDs in the same project must have separate workflows")
	}
	if first == workflowIDFor("install-b", "project-a", "run-1") {
		t.Fatal("separate installations must have separate workflow IDs")
	}
}

func TestApprovalRequiresCanonicalArgumentsHash(t *testing.T) {
	base := agent.Approval{OperationID: "run/frame/call", ActorID: "operator", ArgumentsHash: "sha256:" + strings.Repeat("a", 64)}
	if err := validateApproval(base); err != nil {
		t.Fatalf("valid approval rejected: %v", err)
	}
	missing := base
	missing.ArgumentsHash = ""
	if err := validateApproval(missing); err == nil {
		t.Fatal("approval without arguments hash accepted")
	}
	malformed := base
	malformed.ArgumentsHash = "sha256:not-a-digest"
	if err := validateApproval(malformed); err == nil {
		t.Fatal("malformed arguments hash accepted")
	}
}
