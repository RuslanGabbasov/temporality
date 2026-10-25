package main

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/kernel/llm"
)

// The verification-gap audit must not flood the queue: judged gaps only, one
// case per run, judged-honest runs marked sticky and never re-judged.

type capturedCase struct {
	caseID, subject, project, summary, recommendation string
	extra                                             map[string]any
}

type verificationActivitiesFake struct {
	mu       sync.Mutex
	cases    []capturedCase
	reviewed []string
}

func (f *verificationActivitiesFake) PublishSupervisorCase(_ context.Context, model, caseID, subject, project, summary, recommendation string, extra map[string]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cases = append(f.cases, capturedCase{caseID: caseID, subject: subject, project: project, summary: summary, recommendation: recommendation, extra: extra})
	return nil
}

func (f *verificationActivitiesFake) EmitVerificationReviewed(_ context.Context, run, explanation string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reviewed = append(f.reviewed, run)
	return nil
}

type verificationJournal struct {
	completions []journalEvent
	tools       []journalEvent
	delegations []journalEvent
	reviewed    []journalEvent
	finals      map[string]string
}

func (j *verificationJournal) fetch(_ context.Context, _ string, _ string, eventType string, _ int) ([]journalEvent, error) {
	switch eventType {
	case "run.completed":
		return j.completions, nil
	case "tool.started":
		return j.tools, nil
	case "delegation.started":
		return j.delegations, nil
	case "verification.reviewed":
		return j.reviewed, nil
	case "model.text_delta":
		events := []journalEvent{}
		for run, text := range j.finals {
			events = append(events, journalEvent{Run: run, Type: eventType, Data: map[string]any{"text": text}})
		}
		return events, nil
	}
	return nil, nil
}

func runCompleted(run, project string) journalEvent {
	return journalEvent{Run: run, Project: project, Type: "run.completed", Data: map[string]any{}}
}

func toolCommand(run, command string) journalEvent {
	return journalEvent{Run: run, Type: "tool.started", Data: map[string]any{"command": command}}
}

// withVerificationFixture swaps the fetcher and judge indirections for the
// duration of one test.
func withVerificationFixture(journal *verificationJournal, judge func(ctx context.Context, client *llm.Client, final, commands string) (agent.VerificationGapVerdict, error), fn func()) {
	oldFetch, oldJudge := verificationFetch, verificationJudge
	verificationFetch = func(ctx context.Context, url, token, eventType string, limit int) ([]journalEvent, error) {
		return journal.fetch(ctx, url, token, eventType, limit)
	}
	verificationJudge = judge
	defer func() { verificationFetch, verificationJudge = oldFetch, oldJudge }()
	fn()
}

func TestVerifyCompletionClaimsJudgeGate(t *testing.T) {
	journal := &verificationJournal{
		completions: []journalEvent{
			runCompleted("run-gap", "proj"),
			runCompleted("run-honest", "proj"),
		},
		tools: []journalEvent{
			toolCommand("run-gap", "curl https://api.example.com"),
			toolCommand("run-honest", "curl https://api.example.com"),
		},
		finals: map[string]string{
			"run-gap":    "Все тесты проходят, задача выполнена. run-gap",
			"run-honest": "Все тесты проходят. run-honest",
		},
	}
	fake := &verificationActivitiesFake{}
	// The judge confirms the gap for run-gap (its commands digest contains
	// no tests) and clears run-honest.
	withVerificationFixture(journal, func(ctx context.Context, client *llm.Client, final, commands string) (agent.VerificationGapVerdict, error) {
		if contains(final, "run-gap") {
			return agent.VerificationGapVerdict{Gap: true, Explanation: "заявлены тесты, команд тестов в журнале нет"}, nil
		}
		return agent.VerificationGapVerdict{Gap: false, Explanation: "отчёт честный: проверка была интерактивной"}, nil
	}, func() {
		verifyCompletionClaims(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), fake, "", "", &llm.Client{}, map[string]time.Time{})
	})

	require.Len(t, fake.cases, 1)
	require.Equal(t, agent.CaseVerificationGap, fake.cases[0].caseID)
	require.Contains(t, fake.cases[0].subject, "run-gap")
	require.Len(t, fake.reviewed, 1)
	require.Contains(t, fake.reviewed[0], "run-honest")
	// The case carries the judge verdict, the actual commands and a proper
	// cause label.
	require.Contains(t, fake.cases[0].summary, "команд тестов в журнале нет")
	require.Contains(t, fake.cases[0].summary, "curl")
	require.Equal(t, "verification_gap", fake.cases[0].extra["cause_class"])
}

// A run whose journal shows verification commands is never nominated.
func TestVerifyCompletionClaimsVerifiedRunSkipped(t *testing.T) {
	journal := &verificationJournal{
		completions: []journalEvent{runCompleted("run-verified", "proj")},
		tools:       []journalEvent{toolCommand("run-verified", "go test ./...")},
		finals:      map[string]string{"run-verified": "Все тесты проходят."},
	}
	judge := func(ctx context.Context, client *llm.Client, final, commands string) (agent.VerificationGapVerdict, error) {
		t.Fatal("verified run reached the judge")
		return agent.VerificationGapVerdict{}, nil
	}
	fake := &verificationActivitiesFake{}
	withVerificationFixture(journal, judge, func() {
		verifyCompletionClaims(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), fake, "", "", &llm.Client{}, map[string]time.Time{})
	})
	require.Empty(t, fake.cases)
	require.Empty(t, fake.reviewed)
}

// Already-cased (done index) and already-reviewed runs skip without judging.
func TestVerifyCompletionClaimsStickySkips(t *testing.T) {
	journal := &verificationJournal{
		completions: []journalEvent{runCompleted("run-cased", "proj"), runCompleted("run-reviewed", "proj")},
		finals: map[string]string{
			"run-cased":    "Все тесты проходят. run-cased",
			"run-reviewed": "Все тесты проходят. run-reviewed",
		},
		reviewed: []journalEvent{{Run: "run-reviewed", Type: "verification.reviewed", Data: map[string]any{"subject": "run-reviewed:verification_claim"}}},
	}
	judge := func(ctx context.Context, client *llm.Client, final, commands string) (agent.VerificationGapVerdict, error) {
		t.Fatal("sticky run reached the judge")
		return agent.VerificationGapVerdict{}, nil
	}
	fake := &verificationActivitiesFake{}
	withVerificationFixture(journal, judge, func() {
		done := map[string]time.Time{}
		done[agent.CaseVerificationGap+"\x00run-cased:verification_claim"] = time.Now()
		verifyCompletionClaims(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), fake, "", "", &llm.Client{}, done)
	})
	require.Empty(t, fake.cases)
	require.Empty(t, fake.reviewed)
}

// Without a judge client the sweep stays silent: nominations never become
// unjudged cases (that was the 208-card flood).
func TestVerifyCompletionClaimsNoJudgeNoCases(t *testing.T) {
	journal := &verificationJournal{
		completions: []journalEvent{runCompleted("run-1", "proj"), runCompleted("run-2", "proj")},
		finals:      map[string]string{"run-1": "Все тесты проходят.", "run-2": "Все тесты проходят."},
	}
	fake := &verificationActivitiesFake{}
	withVerificationFixture(journal, nil, func() {
		verifyCompletionClaims(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), fake, "", "", nil, map[string]time.Time{})
	})
	require.Empty(t, fake.cases)
	require.Empty(t, fake.reviewed)
}

func contains(haystack, needle string) bool {
	return len(needle) == 0 || (len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
