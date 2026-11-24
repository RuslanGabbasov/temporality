package main

// The conflict sweep tests: mechanical resolutions apply themselves (with the
// journal and decide calls to prove it), genuine conflicts stay untouched,
// the legacy backlog (payloads without a resolution) is judged once.

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/kernel/llm"
	"github.com/temporality-project/temporality/observation"
	"github.com/temporality-project/temporality/workspace"
)

type conflictSweepStoreFake struct {
	cases     []workspace.SupervisorCase
	decided   map[string]string
	listCalls int
}

func (f *conflictSweepStoreFake) ListSupervisorQueue(context.Context, workspace.SupervisorQueueFilter) (workspace.SupervisorQueue, error) {
	f.listCalls++
	return workspace.SupervisorQueue{Cases: f.cases}, nil
}

func (f *conflictSweepStoreFake) DecideSupervisorCase(_ context.Context, caseKey, decision, _ string) (workspace.SupervisorCase, error) {
	if f.decided == nil {
		f.decided = map[string]string{}
	}
	if _, ok := f.decided[caseKey]; ok {
		return workspace.SupervisorCase{}, workspace.ErrSupervisorCaseDecided
	}
	f.decided[caseKey] = decision
	return workspace.SupervisorCase{CaseKey: caseKey, Decision: decision}, nil
}

func (f *conflictSweepStoreFake) ListAllAgents(context.Context) ([]workspace.Agent, error) {
	return nil, nil
}
func (f *conflictSweepStoreFake) ListSkillsVisible(context.Context, []string) ([]workspace.Skill, error) {
	return nil, nil
}
func (f *conflictSweepStoreFake) CreateAgentProposal(context.Context, *workspace.AgentProposal) error {
	return nil
}
func (f *conflictSweepStoreFake) ListAgentVersions(context.Context, string) ([]workspace.AgentVersion, error) {
	return nil, nil
}
func (f *conflictSweepStoreFake) ListAgentVersionMetrics(context.Context, string, int, time.Time) ([]workspace.AgentVersionMetrics, error) {
	return nil, nil
}
func (f *conflictSweepStoreFake) ListRunsByAgent(context.Context, string, int) ([]workspace.Run, error) {
	return nil, nil
}

type conflictSweepOutboxFake struct{ events []observation.Event }

func (f *conflictSweepOutboxFake) Enqueue(_ context.Context, e observation.Event) error {
	f.events = append(f.events, e)
	return nil
}

type conflictSweepInvalidate struct{ body map[string]any }

func conflictSweepJournal(t *testing.T) (*httptest.Server, *[]conflictSweepInvalidate) {
	t.Helper()
	calls := &[]conflictSweepInvalidate{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/observations/knowledge/invalidate" {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			*calls = append(*calls, conflictSweepInvalidate{body})
			_, _ = w.Write([]byte(`{}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	return server, calls
}

func conflictSweepCase(payload map[string]any) workspace.SupervisorCase {
	raw, _ := json.Marshal(payload)
	return workspace.SupervisorCase{
		CaseKey:        "supervisor-case/knowledge_conflict/k-new~k-old",
		Family:         agent.CaseKnowledgeConflict,
		Subject:        "k-new~k-old",
		Project:        "proj",
		Payload:        raw,
		Status:         workspace.SupervisorCaseOpen,
		Summary:        "s",
		Recommendation: "r",
	}
}

func conflictSweepJudgeServer(t *testing.T, reply string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": reply}}},
		})
	}))
	t.Cleanup(server.Close)
	return server
}

func TestSweepKnowledgeConflictsAppliesGeneralize(t *testing.T) {
	t.Setenv("KERNEL_CONFLICT_AUTORESOLVE", "on")
	journal, invalidates := conflictSweepJournal(t)
	outbox := &conflictSweepOutboxFake{}
	store := &conflictSweepStoreFake{cases: []workspace.SupervisorCase{conflictSweepCase(map[string]any{
		"knowledge_id": "k-new", "neighbor_id": "k-old",
		"new_project": "proj", "old_project": "proj",
		"new_proposition": "деплой канареечный", "old_proposition": "деплой синий-зелёный",
		"resolution": "generalize", "generalized_proposition": "деплой идёт поэтапно с проверками",
	})}}
	sweepKnowledgeConflicts(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)),
		&agent.Activities{Events: outbox, SourceID: "test-kernel"}, store, journal.URL, "token", nil)

	require.Len(t, outbox.events, 1, "the generalized statement is proposed once")
	proposed := outbox.events[0]
	require.Equal(t, "knowledge.proposed", proposed.Type)
	genID, _ := proposed.Data["knowledge_id"].(string)
	require.Regexp(t, `^gen-[0-9a-f]{12}$`, genID)
	require.Equal(t, "деплой идёт поэтапно с проверками", proposed.Data["proposition"])
	require.Equal(t, "proj", proposed.Context.Project)

	require.Len(t, *invalidates, 2, "both sides retire with the replacement")
	byID := map[string]map[string]any{}
	for _, call := range *invalidates {
		id, _ := call.body["knowledge_id"].(string)
		byID[id] = call.body
	}
	require.Contains(t, byID, "k-new")
	require.Contains(t, byID, "k-old")
	for _, body := range byID {
		require.Equal(t, genID, body["replacement_id"])
		require.Equal(t, "proj", body["project"])
	}
	require.Equal(t, "auto_generalized", store.decided[store.cases[0].CaseKey])
}

func TestSweepKnowledgeConflictsSkipsGenuineConflicts(t *testing.T) {
	t.Setenv("KERNEL_CONFLICT_AUTORESOLVE", "on")
	journal, invalidates := conflictSweepJournal(t)
	outbox := &conflictSweepOutboxFake{}
	// A judge IS configured — a payload already carrying resolution=conflict
	// must not cost another call and must not be auto-decided.
	model := conflictSweepJudgeServer(t, `{"resolution": "conflict", "explanation": "x"}`)
	store := &conflictSweepStoreFake{cases: []workspace.SupervisorCase{conflictSweepCase(map[string]any{
		"knowledge_id": "k-new", "neighbor_id": "k-old",
		"resolution": "conflict",
	})}}
	sweepKnowledgeConflicts(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)),
		&agent.Activities{Events: outbox, SourceID: "test-kernel"}, store, journal.URL, "token", llm.New(llm.Config{BaseURL: model.URL, Model: "m"}))

	require.Empty(t, *invalidates)
	require.Empty(t, store.decided)
	require.Empty(t, outbox.events)
}

func TestSweepKnowledgeConflictsJudgesLegacyBacklog(t *testing.T) {
	t.Setenv("KERNEL_CONFLICT_AUTORESOLVE", "on")
	outbox := &conflictSweepOutboxFake{}
	legacy := conflictSweepCase(map[string]any{
		"knowledge_id": "k-new", "neighbor_id": "k-old", "old_project": "proj",
		"new_proposition": "a", "old_proposition": "b",
	})
	store := &conflictSweepStoreFake{cases: []workspace.SupervisorCase{legacy}}

	// Judged a genuine conflict → a marker verdict enriches the case payload
	// (FULL original payload preserved) so later passes never re-judge; the
	// human keeps an untouched, open conflict card.
	model := conflictSweepJudgeServer(t, `{"resolution": "conflict", "explanation": "одно отрицает другое"}`)
	journal, invalidates := conflictSweepJournal(t)
	sweepKnowledgeConflicts(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)),
		&agent.Activities{Events: outbox, SourceID: "test-kernel"}, store, journal.URL, "token", llm.New(llm.Config{BaseURL: model.URL, Model: "m"}))
	require.Len(t, outbox.events, 1)
	marker := outbox.events[0]
	require.Equal(t, "k-new~k-old", marker.Data["subject"])
	require.Equal(t, "conflict", marker.Data["resolution"])
	require.Equal(t, "conflict_sweep", marker.Data["kind"])
	require.Equal(t, "k-new", marker.Data["knowledge_id"], "the marker must carry the full original payload")
	require.Equal(t, "proj", marker.Data["old_project"])
	require.Empty(t, store.decided)
	require.Empty(t, *invalidates)

	// Judged mechanical (keep_both) → the case closes without journal writes.
	outbox2 := &conflictSweepOutboxFake{}
	model2 := conflictSweepJudgeServer(t, `{"resolution": "keep_both", "explanation": "разные scope"}`)
	store2 := &conflictSweepStoreFake{cases: []workspace.SupervisorCase{conflictSweepCase(legacyPayload())}}
	journal2, invalidates2 := conflictSweepJournal(t)
	sweepKnowledgeConflicts(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)),
		&agent.Activities{Events: outbox2}, store2, journal2.URL, "token", llm.New(llm.Config{BaseURL: model2.URL, Model: "m"}))
	require.Equal(t, "auto_keep_both", store2.decided[store2.cases[0].CaseKey])
	require.Empty(t, *invalidates2)
	require.Empty(t, outbox2.events)
}

func legacyPayload() map[string]any {
	return map[string]any{
		"knowledge_id": "k-new", "neighbor_id": "k-old", "old_project": "proj",
		"new_proposition": "a", "old_proposition": "b",
	}
}

func TestSweepKnowledgeConflictsAppliesOutdated(t *testing.T) {
	t.Setenv("KERNEL_CONFLICT_AUTORESOLVE", "on")
	journal, invalidates := conflictSweepJournal(t)
	outbox := &conflictSweepOutboxFake{}
	store := &conflictSweepStoreFake{cases: []workspace.SupervisorCase{conflictSweepCase(map[string]any{
		"knowledge_id": "k-new", "neighbor_id": "k-old",
		"old_project": "proj", "resolution": "outdated",
	})}}
	sweepKnowledgeConflicts(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)),
		&agent.Activities{Events: outbox, SourceID: "test-kernel"}, store, journal.URL, "token", nil)

	require.Len(t, *invalidates, 1)
	require.Equal(t, "k-old", (*invalidates)[0].body["knowledge_id"])
	require.Equal(t, "k-new", (*invalidates)[0].body["replacement_id"], "outdated is a supersession by the new item")
	require.Equal(t, "auto_invalidated_old", store.decided[store.cases[0].CaseKey])
}

func TestSweepKnowledgeConflictsOffSwitch(t *testing.T) {
	t.Setenv("KERNEL_CONFLICT_AUTORESOLVE", "off")
	store := &conflictSweepStoreFake{cases: []workspace.SupervisorCase{conflictSweepCase(map[string]any{"resolution": "keep_both"})}}
	sweepKnowledgeConflicts(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)),
		&agent.Activities{Events: &conflictSweepOutboxFake{}, SourceID: "test-kernel"}, store, "http://unused", "token", nil)
	require.Zero(t, store.listCalls, "the off-switch must skip the pass entirely")
}
