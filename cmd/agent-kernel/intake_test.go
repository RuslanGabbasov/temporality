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
