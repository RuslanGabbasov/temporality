package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/temporality-project/temporality/observation"
)

// fakeKnowledgeJournal mimics the Journal's GET /v1/observations/knowledge
// contract: project is required, known ids answer with a one-item knowledge
// list, unknown ids answer 404, everything else is a 500.
func fakeKnowledgeJournal(t *testing.T, known map[string]bool) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/observations/knowledge" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		project := r.URL.Query().Get("project")
		if project == "" {
			http.Error(w, "project is required", http.StatusBadRequest)
			return
		}
		id := r.URL.Query().Get("knowledge_id")
		if !known[id] {
			http.Error(w, "knowledge not found", http.StatusNotFound)
			return
		}
		writeJSONForTest(w, map[string]any{
			"knowledge": []observation.Knowledge{{
				ID:          id,
				State:       "confirmed",
				Proposition: "fixture proposition for " + id,
				Project:     project,
			}},
			"count": 1,
		})
	}))
	t.Cleanup(server.Close)
	return server
}

func writeJSONForTest(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(value)
}

func TestFetchKnowledgeByIDs(t *testing.T) {
	server := fakeKnowledgeJournal(t, map[string]bool{"kn-1": true, "kn-2": true, "kn-9": false})

	found, missing, err := fetchKnowledgeByIDs(context.Background(), server.Client(), server.URL, "", "proj", []string{"kn-1", "kn-9", "kn-2"})
	if err != nil {
		t.Fatalf("fetchKnowledgeByIDs: %v", err)
	}
	if len(found) != 2 {
		t.Fatalf("found = %d items, want 2", len(found))
	}
	if found[0].ID != "kn-1" || found[1].ID != "kn-2" {
		t.Fatalf("found order = [%s %s], want [kn-1 kn-2]", found[0].ID, found[1].ID)
	}
	for _, item := range found {
		if item.State != "confirmed" {
			t.Errorf("knowledge %s state = %q, want confirmed", item.ID, item.State)
		}
		if item.Project != "proj" {
			t.Errorf("knowledge %s project = %q, want proj", item.ID, item.Project)
		}
		if !strings.HasPrefix(item.Proposition, "fixture proposition for ") {
			t.Errorf("knowledge %s proposition = %q, want fixture", item.ID, item.Proposition)
		}
	}
	if len(missing) != 1 || missing[0] != "kn-9" {
		t.Fatalf("missing = %v, want [kn-9]", missing)
	}
}

func TestFetchKnowledgeByIDsSendsAuthAndProject(t *testing.T) {
	var gotAuth, gotProject, gotID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotProject = r.URL.Query().Get("project")
		gotID = r.URL.Query().Get("knowledge_id")
		writeJSONForTest(w, map[string]any{"knowledge": []observation.Knowledge{{ID: gotID, State: "confirmed"}}, "count": 1})
	}))
	defer server.Close()

	if _, _, err := fetchKnowledgeByIDs(context.Background(), server.Client(), server.URL, "token-1", "proj x", []string{"kn/1&"}); err != nil {
		t.Fatalf("fetchKnowledgeByIDs: %v", err)
	}
	if gotAuth != "Bearer token-1" {
		t.Errorf("Authorization = %q, want Bearer token-1", gotAuth)
	}
	if gotProject != "proj x" {
		t.Errorf("project = %q, want %q", gotProject, "proj x")
	}
	if gotID != "kn/1&" {
		t.Errorf("knowledge_id = %q, want %q", gotID, "kn/1&")
	}
}

func TestFetchKnowledgeByIDsUpstreamError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()

	found, missing, err := fetchKnowledgeByIDs(context.Background(), server.Client(), server.URL, "", "proj", []string{"kn-1"})
	if err == nil {
		t.Fatal("fetchKnowledgeByIDs succeeded, want error on upstream 500")
	}
	if !strings.Contains(err.Error(), "kn-1") {
		t.Errorf("error %q does not mention the failing id", err.Error())
	}
	if found != nil || missing != nil {
		t.Errorf("found/missing = %v/%v, want nil on error", found, missing)
	}
}

func TestFetchKnowledgeBatchDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		http.Error(w, "slow journal", http.StatusInternalServerError)
	}))
	defer server.Close()

	// The outer 100ms deadline fires before any upstream response arrives, so
	// the batch must return an error instead of hanging for the full loop.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	var err error
	go func() {
		defer close(done)
		_, _, err = fetchKnowledgeByIDs(ctx, server.Client(), server.URL, "", "proj", []string{"kn-1"})
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("fetchKnowledgeByIDs did not return under the outer deadline")
	}
	if err == nil {
		t.Fatal("fetchKnowledgeByIDs succeeded, want deadline error")
	}
}

func TestFetchUpstreamErrorTruncated(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, strings.Repeat("x", 1000000), http.StatusInternalServerError)
	}))
	defer server.Close()

	_, _, err := fetchKnowledgeByIDs(context.Background(), server.Client(), server.URL, "", "proj", []string{"kn-1"})
	if err == nil {
		t.Fatal("fetchKnowledgeByIDs succeeded, want error on upstream 500")
	}
	message := err.Error()
	if !strings.HasPrefix(message, "knowledge") {
		t.Errorf("error %q does not start with %q", message, "knowledge")
	}
	if runeCount := len([]rune(message)); runeCount >= 1200 {
		t.Errorf("error length = %d runes, want < 1200", runeCount)
	}
}

func TestFetchFiltersForeignProject(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSONForTest(w, map[string]any{
			"knowledge": []observation.Knowledge{{
				ID:      "X",
				Project: "other",
				State:   "confirmed",
			}},
			"count": 1,
		})
	}))
	defer server.Close()

	found, missing, err := fetchKnowledgeByIDs(context.Background(), server.Client(), server.URL, "", "proj", []string{"X"})
	if err != nil {
		t.Fatalf("fetchKnowledgeByIDs: %v", err)
	}
	if len(found) != 0 {
		t.Errorf("found = %v, want empty: foreign-project items must be dropped", found)
	}
	if len(missing) != 0 {
		t.Errorf("missing = %v, want empty: foreign-project items are an anomaly, not a miss", missing)
	}
}

func TestParseKnowledgeIDs(t *testing.T) {
	ids, err := parseKnowledgeIDs("a, b ,a,,c")
	if err != nil {
		t.Fatalf("parseKnowledgeIDs: %v", err)
	}
	if len(ids) != 3 || ids[0] != "a" || ids[1] != "b" || ids[2] != "c" {
		t.Fatalf("ids = %v, want [a b c]", ids)
	}

	ids, err = parseKnowledgeIDs(" x ")
	if err != nil {
		t.Fatalf("parseKnowledgeIDs(%q): %v", " x ", err)
	}
	if len(ids) != 1 || ids[0] != "x" {
		t.Fatalf("ids = %v, want [x]", ids)
	}
}

func TestParseKnowledgeIDsRejectsEmpty(t *testing.T) {
	for _, raw := range []string{"", "   ", ",", " , , "} {
		ids, err := parseKnowledgeIDs(raw)
		if err == nil {
			t.Fatalf("parseKnowledgeIDs(%q) = %v, want error", raw, ids)
		}
		if !strings.Contains(err.Error(), "ids is required") {
			t.Errorf("parseKnowledgeIDs(%q) error = %q, want %q", raw, err.Error(), "ids is required")
		}
	}
}

func TestParseKnowledgeIDsRejectsOverLimit(t *testing.T) {
	raw := make([]string, 0, 21)
	for i := 0; i < 21; i++ {
		raw = append(raw, "kn-"+strings.Repeat("a", 0)+string(rune('a'+i%26))+string(rune('a'+i/26)))
	}
	ids, err := parseKnowledgeIDs(strings.Join(raw, ","))
	if err == nil {
		t.Fatalf("parseKnowledgeByIDs accepted 21 ids: %v", ids)
	}
	if !strings.Contains(err.Error(), "max 20") {
		t.Errorf("error = %q, want it to contain %q", err.Error(), "max 20")
	}

	// Exactly 20 unique ids must still pass.
	if ids, err := parseKnowledgeIDs(strings.Join(raw[:20], ",")); err != nil || len(ids) != 20 {
		t.Fatalf("parseKnowledgeIDs(20 ids) = %v, %v; want 20 ids, nil", ids, err)
	}
}
