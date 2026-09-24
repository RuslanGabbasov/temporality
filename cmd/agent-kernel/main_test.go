package main

import "testing"

func TestWorkflowIDForScopesByProjectAndRun(t *testing.T) {
	first := workflowIDFor("project-a", "run-1")
	if first != workflowIDFor("project-a", "run-1") {
		t.Fatal("workflow ID must be stable")
	}
	if first == workflowIDFor("project-b", "run-1") {
		t.Fatal("same run ID in different projects must have separate workflows")
	}
	if first == workflowIDFor("project-a", "run-2") {
		t.Fatal("different run IDs in the same project must have separate workflows")
	}
}
