package main

import (
	"strings"
	"testing"

	"github.com/temporality-project/temporality/workspace"
)

// TestAmendmentPromptComposition: the follow-up prompt carries the three
// context layers — original task, prior outcome, amendment — and stays bounded
// when the previous answer is huge.
func TestAmendmentPromptComposition(t *testing.T) {
	task := workspace.Task{ID: "t1", Title: "Смета", Prompt: "Подготовь смету на разработку."}
	prev := workspace.Run{ID: "r1", TaskID: "t1", Status: "completed", Answer: strings.Repeat("итог ", 100000)}
	prompt := amendmentPrompt(task, prev, "добавь раздел про риски")
	for _, want := range []string{"Смета", "Подготовь смету", "r1", "completed", "добавь раздел про риски", "Amendment"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt[:400])
		}
	}
	// The runaway answer is truncated, not passed whole.
	if len(prompt) > 8000 {
		t.Fatalf("prompt must stay bounded, got %d chars", len(prompt))
	}
}

// TestHasUserProjects: the system intake project is always present
// (bootstrapIntake), so onboarding must key off user projects only.
func TestHasUserProjects(t *testing.T) {
	if hasUserProjects(nil) {
		t.Fatal("empty list must not count as set up")
	}
	if hasUserProjects([]workspace.Project{{ID: IntakeProjectID, Name: "Intake (system)"}}) {
		t.Fatal("intake-only install must still need setup")
	}
	if !hasUserProjects([]workspace.Project{{ID: IntakeProjectID}, {ID: "p1", Name: "Первый проект"}}) {
		t.Fatal("a user project means setup is done")
	}
}

// TestAmendmentPromptFailedRun: a failed prior run surfaces its error as the
// outcome instead of an empty section.
func TestAmendmentPromptFailedRun(t *testing.T) {
	task := workspace.Task{ID: "t1", Title: "T", Prompt: "P"}
	prev := workspace.Run{ID: "r2", TaskID: "t1", Status: "failed", Error: "budget exhausted"}
	prompt := amendmentPrompt(task, prev, "повтори с меньшим объёмом")
	if !strings.Contains(prompt, "run failed: budget exhausted") {
		t.Fatalf("prompt = %q", prompt)
	}
}
