package main

import (
	"strings"
	"testing"

	"github.com/temporality-project/temporality/kernel/agent"
	"go.temporal.io/api/enums/v1"
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

func TestWorkflowStatusNameMatchesCanonicalVocabulary(t *testing.T) {
	cases := map[enums.WorkflowExecutionStatus]string{
		enums.WORKFLOW_EXECUTION_STATUS_RUNNING:          "running",
		enums.WORKFLOW_EXECUTION_STATUS_COMPLETED:        "completed",
		enums.WORKFLOW_EXECUTION_STATUS_FAILED:           "failed",
		enums.WORKFLOW_EXECUTION_STATUS_CANCELED:         "cancelled",
		enums.WORKFLOW_EXECUTION_STATUS_TERMINATED:       "cancelled",
		enums.WORKFLOW_EXECUTION_STATUS_TIMED_OUT:        "failed",
		enums.WORKFLOW_EXECUTION_STATUS_CONTINUED_AS_NEW: "running",
	}
	for status, want := range cases {
		if got := workflowStatusName(status); got != want {
			t.Fatalf("status %v: got %q, want %q", status, got, want)
		}
	}
}
