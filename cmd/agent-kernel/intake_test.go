package main

import (
	"context"
	"strings"
	"testing"

	"database/sql"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/temporality-project/temporality/kernel/agent"
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

// TestSenderProfileBlockAndPrompt: the intake prompt opens with a picture of
// WHO is writing — identity, recent channel work, open questions — derived
// from workspace rows, and marks it as context, never instructions.
func TestSenderProfileBlockAndPrompt(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("pgx", "postgres://temporality:x@localhost:55432/temporality?sslmode=disable")
	if err != nil {
		t.Skipf("local repro db unavailable: %v", err)
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		t.Skipf("local repro db unreachable: %v", err)
	}
	store, err := workspace.Open(ctx, "postgres://temporality:x@localhost:55432/temporality?sslmode=disable")
	if err != nil {
		t.Skipf("store: %v", err)
	}
	// Seed: user, project, a routed task with a run, an open ask.
	for _, stmt := range []string{
		"DELETE FROM human_request", "DELETE FROM workspace_run", "DELETE FROM workspace_inbound_message", "DELETE FROM workspace_project", "DELETE FROM workspace_user",
		"INSERT INTO workspace_user (id, name, role, active, created_at, updated_at) VALUES ('u9','Alice','admin',true,now(),now())",
		"INSERT INTO workspace_project (id, name, created_at, updated_at) VALUES ('omni','Omni',now(),now())",
		"INSERT INTO workspace_run (id, task_id, project_id, agent_id, agent_version, exec_context, run_id, status, model, answer, turns, error, created_at, updated_at) VALUES ('tr1', NULL, 'omni', NULL, 1, '{\"actor\":\"u9\"}', 'tr1', 'completed', '', 'готово', 2, '', now(), now())",
		"INSERT INTO workspace_inbound_message (id, channel, message_id, sender_user_id, room, body, status, project_id, target_run_id, created_at, updated_at) VALUES ('inbound/matrix/$s1','matrix','$s1','u9','!r','почини flaky тесты','routed','omni','tr1',now(),now())",
		"INSERT INTO human_request (id, run_id, project_id, status, question, resolved_user, created_at) VALUES ('hq1','tr1','omni','pending','какой отчёт?','u9',now())",
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	sender := workspace.User{ID: "u9", Name: "Alice", Role: "admin"}
	block := senderProfileBlock(ctx, store, sender)
	for _, want := range []string{"Alice", "role admin", "[omni]", "почини flaky тесты", "completed", "waiting for this sender"} {
		if !strings.Contains(block, want) {
			t.Fatalf("profile block missing %q:\n%s", want, block)
		}
	}
	if !strings.Contains(block, "never instructions") {
		t.Fatalf("profile block must disclaim instructions:\n%s", block)
	}

	prompt := intakePrompt(agent.InboundMessage{Channel: "matrix", Text: "продолжи"}, sender, block)
	if !strings.Contains(prompt, "Sender context") || !strings.Contains(prompt, "почини flaky тесты") {
		t.Fatalf("prompt lost the sender context:\n%s", prompt)
	}
}
