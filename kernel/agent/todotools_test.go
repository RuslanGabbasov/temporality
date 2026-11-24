package agent

// Personal todo tool tests: the fake workspace server records every call so
// the tests pin down the SCOPE CONTRACT — the run's human starter rides every
// call, the paths/methods match the kernel's internal routes, and a run
// without a starter is refused without any HTTP traffic at all.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type recordedTodoCall struct {
	method  string
	path    string
	query   string
	payload map[string]any
}

func newTodoWorkspaceServer(t *testing.T, calls *[]recordedTodoCall) *httptest.Server {
	t.Helper()
	mu := &sync.Mutex{}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&payload)
		}
		mu.Lock()
		*calls = append(*calls, recordedTodoCall{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery, payload: payload})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"todos": []map[string]any{
				{"id": "t1", "group": "inbox", "text": "Позвонить в банк"},
			}})
		case http.MethodDelete:
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"todo": map[string]any{"id": "t1", "group": "done", "text": "x"}})
		}
	}))
}

func TestTodoToolsRideTheStarterScope(t *testing.T) {
	var calls []recordedTodoCall
	server := newTodoWorkspaceServer(t, &calls)
	defer server.Close()
	activities := &Activities{HTTP: server.Client(), WorkspaceURL: server.URL, WorkspaceToken: "internal"}
	ctx := context.Background()

	// list
	res, err := activities.RunTool(ctx, ToolRequest{ActorID: "ivanov", Name: "todo_list"})
	if err != nil || !strings.Contains(res.Content, "Позвонить в банк") {
		t.Fatalf("todo_list: %v %q", err, res.Content)
	}
	if calls[0].method != http.MethodGet || calls[0].path != "/v1/internal/todos" || calls[0].query != "actor=ivanov" {
		t.Fatalf("list call = %+v", calls[0])
	}

	// list with a group filter
	if _, err := activities.RunTool(ctx, ToolRequest{ActorID: "ivanov", Name: "todo_list", Arguments: map[string]any{"group": "next"}}); err != nil {
		t.Fatalf("todo_list group: %v", err)
	}
	if calls[1].query != "actor=ivanov&group=next" {
		t.Fatalf("filtered list query = %q", calls[1].query)
	}

	// add
	res, err = activities.RunTool(ctx, ToolRequest{ActorID: "ivanov", Name: "todo_add", Arguments: map[string]any{
		"text": "Подготовить отчёт", "group": "next", "note": "до пятницы",
	}})
	if err != nil || !strings.Contains(res.Content, `"todo"`) {
		t.Fatalf("todo_add: %v %q", err, res.Content)
	}
	if calls[2].method != http.MethodPost || calls[2].path != "/v1/internal/todos" {
		t.Fatalf("add call = %+v", calls[2])
	}
	if calls[2].payload["actor"] != "ivanov" || calls[2].payload["text"] != "Подготовить отчёт" || calls[2].payload["group"] != "next" {
		t.Fatalf("add payload = %#v", calls[2].payload)
	}

	// update (only the passed fields ride the call)
	if _, err := activities.RunTool(ctx, ToolRequest{ActorID: "ivanov", Name: "todo_update", Arguments: map[string]any{
		"todo_id": "t1", "text": "Новое имя",
	}}); err != nil {
		t.Fatalf("todo_update: %v", err)
	}
	if calls[3].method != http.MethodPatch || calls[3].path != "/v1/internal/todos/t1" {
		t.Fatalf("update call = %+v", calls[3])
	}
	if calls[3].payload["actor"] != "ivanov" || calls[3].payload["text"] != "Новое имя" {
		t.Fatalf("update payload = %#v", calls[3].payload)
	}
	if _, moved := calls[3].payload["group"]; moved {
		t.Fatalf("omitted group must not ride the call: %#v", calls[3].payload)
	}

	// done → group rides as "done"
	if _, err := activities.RunTool(ctx, ToolRequest{ActorID: "ivanov", Name: "todo_done", Arguments: map[string]any{"todo_id": "t1"}}); err != nil {
		t.Fatalf("todo_done: %v", err)
	}
	if calls[4].method != http.MethodPatch || calls[4].payload["group"] != "done" || calls[4].payload["actor"] != "ivanov" {
		t.Fatalf("done call = %+v payload %#v", calls[4], calls[4].payload)
	}

	// delete — actor in the query, id in the path
	if _, err := activities.RunTool(ctx, ToolRequest{ActorID: "ivanov", Name: "todo_delete", Arguments: map[string]any{"todo_id": "t1"}}); err != nil {
		t.Fatalf("todo_delete: %v", err)
	}
	if calls[5].method != http.MethodDelete || calls[5].path != "/v1/internal/todos/t1" || calls[5].query != "actor=ivanov" {
		t.Fatalf("delete call = %+v", calls[5])
	}
}

func TestTodoToolsRefuseWithoutHumanStarter(t *testing.T) {
	var calls []recordedTodoCall
	server := newTodoWorkspaceServer(t, &calls)
	defer server.Close()
	activities := &Activities{HTTP: server.Client(), WorkspaceURL: server.URL, WorkspaceToken: "internal"}
	ctx := context.Background()

	for _, name := range []string{"todo_list", "todo_add", "todo_update", "todo_done", "todo_delete"} {
		res, err := activities.RunTool(ctx, ToolRequest{Name: name, Arguments: map[string]any{"todo_id": "t1", "text": "x"}})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !strings.Contains(res.Content, "human starter") {
			t.Fatalf("%s must refuse without a starter, got %q", name, res.Content)
		}
	}
	if len(calls) != 0 {
		t.Fatalf("refused tools must make no HTTP calls, got %d", len(calls))
	}
}

func TestTodoToolsValidateGroupVocabulary(t *testing.T) {
	var calls []recordedTodoCall
	server := newTodoWorkspaceServer(t, &calls)
	defer server.Close()
	activities := &Activities{HTTP: server.Client(), WorkspaceURL: server.URL, WorkspaceToken: "internal"}

	res, err := activities.RunTool(context.Background(), ToolRequest{ActorID: "ivanov", Name: "todo_add", Arguments: map[string]any{
		"text": "x", "group": "archive",
	}})
	if err != nil {
		t.Fatalf("todo_add: %v", err)
	}
	if !strings.Contains(res.Content, "inbox, next, waiting, someday, done") {
		t.Fatalf("the error must teach the vocabulary, got %q", res.Content)
	}
	if len(calls) != 0 {
		t.Fatalf("a rejected group must not reach the kernel, got %d calls", len(calls))
	}
}

func TestTodoToolsRequireID(t *testing.T) {
	activities := &Activities{HTTP: http.DefaultClient, WorkspaceURL: "http://127.0.0.1:1", WorkspaceToken: "internal"}
	for _, name := range []string{"todo_update", "todo_done", "todo_delete"} {
		res, err := activities.RunTool(context.Background(), ToolRequest{ActorID: "ivanov", Name: name})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !strings.Contains(res.Content, "todo_id is required") {
			t.Fatalf("%s must demand todo_id, got %q", name, res.Content)
		}
	}
}

func TestTodoToolsAdvertisedAndPolicySafe(t *testing.T) {
	defs := map[string]bool{}
	for _, def := range KernelTools() {
		defs[def.Name] = true
	}
	for _, name := range []string{"todo_list", "todo_add", "todo_update", "todo_done", "todo_delete"} {
		if !defs[name] {
			t.Fatalf("%s is not advertised to models", name)
		}
		if !policySafeTools[name] {
			t.Fatalf("%s must be policy-safe (personal bookkeeping on the starter's own board)", name)
		}
	}
}
