package main

import (
	"context"
	"regexp"
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
	defer store.Close()
	// A clean repro database carries no schema — migrate to the same one the
	// kernel applies at boot, or the seeds below reference absent tables.
	if err := store.MigrateAll(ctx, "../../migrations/workspace", func(string, ...any) {}); err != nil {
		t.Skipf("migrate: %v", err)
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

	prompt := intakePrompt(agent.InboundMessage{Channel: "matrix", Text: "продолжи"}, sender, block, "")
	if !strings.Contains(prompt, "Sender context") || !strings.Contains(prompt, "почини flaky тесты") {
		t.Fatalf("prompt lost the sender context:\n%s", prompt)
	}
}

// The continuation block: a messenger reply sent long after the run finished
// must reach the intake agent WITH the room's recent dialogue — the user's
// messages and the agent's delivered answers, dated — so «продолжи» has
// something to refer to. Without it the intake classified context-free texts
// as brand-new tasks (docs/inbound-messages.md, the resumed-dialog blindspot).
func TestRoomDialogBlockBuildsDatedHistory(t *testing.T) {
	ctx := context.Background()
	dsn := "postgres://temporality:x@localhost:55432/temporality?sslmode=disable"
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Skipf("local repro db unavailable: %v", err)
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		t.Skipf("local repro db unreachable: %v", err)
	}
	store, err := workspace.Open(ctx, dsn)
	if err != nil {
		t.Skipf("store: %v", err)
	}
	defer store.Close()
	// The real migrated schema — the dialog SQL is exactly what the live
	// kernel runs, and a scan-shape drift must fail here, not in production.
	// The path is repo-relative: tests run from the package dir.
	if err := store.MigrateAll(ctx, "../../migrations/workspace", func(string, ...any) {}); err != nil {
		t.Skipf("migrate: %v", err)
	}
	for _, stmt := range []string{
		// Parent project for the target run's foreign key (shared with the other
		// intake tests' seeds — upsert keeps the tests order-independent).
		"INSERT INTO workspace_project (id, name, created_at, updated_at) VALUES ('omni','Omni',now(),now()) ON CONFLICT (id) DO NOTHING",
		"DELETE FROM workspace_inbound_message WHERE room = '!rr'",
		"DELETE FROM workspace_run WHERE id = 'trd1'",
		// Yesterday's dialog: a routed message with its answered target run…
		"INSERT INTO workspace_run (id, project_id, exec_context, run_id, status, model, answer, turns, error, created_at, updated_at) VALUES ('trd1','omni','{}','trd1','completed','','ключ сохранён в секретах проекта',1,'', now() - interval '1 day', now())",
		"INSERT INTO workspace_inbound_message (id, channel, message_id, sender_user_id, room, body, status, project_id, target_run_id, created_at, updated_at) VALUES ('inbound/matrix/$old','matrix','$old','u9','!rr','сгенерируй ключ для MCP','routed','omni','trd1', now() - interval '1 day', now())",
		// …and an old rejected row that never shaped the dialog.
		"INSERT INTO workspace_inbound_message (id, channel, message_id, sender_user_id, room, body, status, created_at, updated_at) VALUES ('inbound/matrix/$rej','matrix','$rej','u9','!rr','спам','rejected', now() - interval '2 days', now())",
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	runner := &intakeRunner{ws: store}
	block := runner.roomDialogBlock(ctx, agent.InboundMessage{Channel: "matrix", ChatID: "!rr", ID: "$new"})
	for _, want := range []string{
		"Recent dialogue in this room",
		"User: сгенерируй ключ для MCP",
		"Agent: ключ сохранён в секретах проекта",
		"may continue one of these conversations",
	} {
		if !strings.Contains(block, want) {
			t.Fatalf("dialog block missing %q:\n%s", want, block)
		}
	}
	if strings.Contains(block, "спам") {
		t.Fatalf("rejected rows must stay out of the dialog:\n%s", block)
	}
	// The stamp is a date, not a bare clock time — «today or yesterday» must be
	// answerable from the block itself.
	if !regexp.MustCompile(`\[\d{4}-\d{2}-\d{2} \d{2}:\d{2}\] User:`).MatchString(block) {
		t.Fatalf("dialog lines must be date-stamped:\n%s", block)
	}

	// A fresh room (no history) yields no block — first messages keep the
	// lean prompt.
	if empty := runner.roomDialogBlock(ctx, agent.InboundMessage{Channel: "matrix", ChatID: "!fresh-room", ID: "$x"}); empty != "" {
		t.Fatalf("fresh room must yield no dialog block, got:\n%s", empty)
	}
}
