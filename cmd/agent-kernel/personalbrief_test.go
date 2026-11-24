package main

// Personal brief tests: the pure halves — summary sanitization (the model's
// answer is untrusted input), the GTD group vocabulary, the fallback digest
// and the full-dashboard gate. Store paths need a database and live behind
// the routes, so they stay uncovered here (like the rest of the kernel's
// route files).

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/workspace"
)

func TestNormalizeTodoGroup(t *testing.T) {
	for _, g := range []string{"inbox", "next", "waiting", "someday", "done"} {
		if got := normalizeTodoGroup(g); got != g {
			t.Fatalf("normalizeTodoGroup(%q) = %q", g, got)
		}
	}
	// Case and whitespace fold into the vocabulary…
	if got := normalizeTodoGroup("  NEXT "); got != "next" {
		t.Fatalf("normalizeTodoGroup case fold = %q", got)
	}
	// …and everything unknown degrades to the inbox instead of failing.
	if got := normalizeTodoGroup("archive"); got != "inbox" {
		t.Fatalf("unknown group = %q, want inbox", got)
	}
	if got := normalizeTodoGroup(""); got != "inbox" {
		t.Fatalf("empty group = %q, want inbox", got)
	}
}

func TestSanitizeUserBriefSummary(t *testing.T) {
	summary, err := sanitizeUserBriefSummary(`{"summary": ["Первый пункт.", "  ", "Второй пункт."], "extra": 1}`)
	if err != nil {
		t.Fatalf("valid summary rejected: %v", err)
	}
	if len(summary) != 2 || summary[0] != "Первый пункт." || summary[1] != "Второй пункт." {
		t.Fatalf("summary = %#v", summary)
	}

	// A fenced or chatty answer still parses: the JSON object is extracted.
	summary, err = sanitizeUserBriefSummary("Вот резюме:\n```json\n{\"summary\": [\"Один\"]}\n```")
	if err != nil || len(summary) != 1 || summary[0] != "Один" {
		t.Fatalf("fenced summary = %#v, err %v", summary, err)
	}

	// Junk is an error, not a crash.
	if _, err := sanitizeUserBriefSummary("модели здесь нечего ответить"); err == nil {
		t.Fatal("junk accepted")
	}
	// An empty (or non-string-only) summary is an error.
	if _, err := sanitizeUserBriefSummary(`{"summary": []}`); err == nil {
		t.Fatal("empty summary accepted")
	}
	if _, err := sanitizeUserBriefSummary(`{"summary": [1, 2, 3]}`); err == nil {
		t.Fatal("non-string summary accepted")
	}

	// Caps: at most 8 bullets, each at most 400 runes.
	long := strings.Repeat("д", 500)
	many := `{"summary": [`
	for i := 0; i < 12; i++ {
		if i > 0 {
			many += ", "
		}
		many += `"` + long + `"`
	}
	many += `]}`
	summary, err = sanitizeUserBriefSummary(many)
	if err != nil {
		t.Fatalf("capped summary rejected: %v", err)
	}
	if len(summary) != 8 {
		t.Fatalf("summary length = %d, want 8", len(summary))
	}
	if got := len([]rune(summary[0])); got != 400 {
		t.Fatalf("bullet length = %d, want 400", got)
	}
}

func TestBriefDigestAndFactsDigest(t *testing.T) {
	now := time.Date(2026, 11, 24, 8, 0, 0, 0, time.UTC)
	payload := UserBriefPayload{
		Day: briefDay(now), Source: "fallback",
		Failed:    []UserBriefRun{{RunID: "r1", Title: "Импорт", Status: "failed"}},
		Approvals: []UserBriefApproval{{ArtifactID: "a1", ArtifactTitle: "Отчёт"}},
		Learning:  UserBriefLearning{Active: true, Done: 2, Total: 5, NextID: "runs-page"},
		Metrics:   UserBriefMetrics{Month: briefMonth(now), Runs: 12, Tokens: 3400, CostUSD: 1.5},
	}
	digest := briefDigest(payload)
	if len(digest) == 0 {
		t.Fatal("empty digest")
	}
	joined := strings.Join(digest, "\n")
	for _, want := range []string{"неудачных", "согласования", "Обучение", "За месяц"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("digest missing %q:\n%s", want, joined)
		}
	}

	facts := briefFactsDigest(payload)
	for _, want := range []string{"неудачных за 3 дня: 1", "Импорт", "ждёт согласования: 1", "пройдено 2 из 5"} {
		if !strings.Contains(facts, want) {
			t.Fatalf("facts digest missing %q:\n%s", want, facts)
		}
	}

	// The no-facts digest still renders sensible lines (no model needed).
	empty := briefDigest(UserBriefPayload{Metrics: UserBriefMetrics{Month: briefMonth(now)}})
	if !strings.Contains(strings.Join(empty, "\n"), "активных запусков нет") {
		t.Fatalf("empty digest = %#v", empty)
	}
}

func TestBriefDayMonth(t *testing.T) {
	now := time.Date(2026, 11, 24, 8, 30, 0, 0, time.FixedZone("test", 3*3600))
	if got := briefDay(now); got != "2026-11-24" {
		t.Fatalf("briefDay = %q (must be UTC-keyed)", got)
	}
	if got := briefMonth(now); got != "2026-11" {
		t.Fatalf("briefMonth = %q", got)
	}
}

func TestCapBriefRuns(t *testing.T) {
	runs := make([]UserBriefRun, 12)
	for i := range runs {
		// Newest-first, like ListRunsByActorSince returns them.
		runs[i] = UserBriefRun{RunID: "r", At: time.Unix(int64(111 - i), 0)}
	}
	capped := capBriefRuns(runs, 8)
	if len(capped) != 8 {
		t.Fatalf("capped length = %d", len(capped))
	}
	// Newest-first input: the cap keeps the newest tail of the list.
	if capped[0].At.Unix() != 111 || capped[7].At.Unix() != 104 {
		t.Fatalf("cap kept the wrong slice: first %v last %v", capped[0].At, capped[7].At)
	}
	if capBriefRuns(runs[:3], 8) == nil || len(capBriefRuns(runs[:3], 8)) != 3 {
		t.Fatal("short list must pass through")
	}
}

func TestIsOrgHeadOf(t *testing.T) {
	units := []workspace.OrgUnit{
		{ID: "u1", HeadUserID: "alice"},
		{ID: "u2", HeadUserID: ""},
		{ID: "u3", HeadUserID: "Bob"},
	}
	if !isOrgHeadOf(units, "alice") {
		t.Fatal("alice must head u1")
	}
	if !isOrgHeadOf(units, "bob") {
		t.Fatal("head match must fold case")
	}
	if isOrgHeadOf(units, "carol") {
		t.Fatal("carol heads nothing")
	}
	if isOrgHeadOf(nil, "alice") {
		t.Fatal("no units, no head")
	}
}

func TestCanSeeFullDashboard(t *testing.T) {
	// The pure gates (role / org grants) need no store at all; the nil store
	// degrades the org-head path closed (unit tests carry no database).
	check := func(p controlplane.Principal) bool { return canSeeFullDashboard(context.Background(), nil, p) }
	if !check(controlplane.Principal{Role: controlplane.RoleAdmin}) {
		t.Fatal("admin sees the full dashboard")
	}
	if !check(controlplane.Principal{Role: controlplane.RoleReader, OrgRoles: []controlplane.OrgRoleGrant{{UnitID: "u1"}}}) {
		t.Fatal("org role grant sees the full dashboard")
	}
	if check(controlplane.Principal{Role: controlplane.RoleReader}) {
		t.Fatal("plain reader without units must NOT see the full dashboard")
	}
	// The org-head path needs the unit tree, which the nil store cannot read:
	// it must degrade closed, not panic and not leak the dashboard.
	if check(controlplane.Principal{Role: controlplane.RoleReader, UserID: "alice"}) {
		t.Fatal("nil store must degrade the org-head path closed")
	}
}
