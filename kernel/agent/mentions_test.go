package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseMentions(t *testing.T) {
	tests := []struct {
		name string
		text string
		want []MentionRef
	}{
		{
			name: "explicit kind refs",
			text: "возьми @artifact:otchet-2026 и @skill:razbor-incidentov",
			want: []MentionRef{
				{Kind: "artifact", ID: "otchet-2026", Explicit: true},
				{Kind: "skill", ID: "razbor-incidentov", Explicit: true},
			},
		},
		{
			name: "bare at-mention and plain pasted id",
			text: "сверь @otchet-2026 с sozday-pr-20261021-142015",
			want: []MentionRef{
				{ID: "otchet-2026", Explicit: true},
				{ID: "sozday-pr-20261021-142015"},
			},
		},
		{
			name: "explicit span masks the weaker patterns",
			text: "@artifact:otchet-2026 и otchet-2026",
			want: []MentionRef{
				{Kind: "artifact", ID: "otchet-2026", Explicit: true},
				{ID: "otchet-2026"},
			},
		},
		{
			name: "kind case folds, scaffold and e-mail are not refs",
			text: "оставь @Artifact:Otchet-1 как есть, @artifact: допиши, напиши user@example-com",
			want: []MentionRef{{Kind: "artifact", ID: "Otchet-1", Explicit: true}},
		},
		{
			name: "plain prose has no candidates",
			text: "просто обнови отчёт и пришли результат",
			want: nil,
		},
		{
			name: "dashes in prose stay silent candidates",
			text: "long-running задача не резолвится",
			want: []MentionRef{{ID: "long-running"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseMentions(tt.text)
			if len(got) != len(tt.want) {
				t.Fatalf("ParseMentions(%q) = %v, want %v", tt.text, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("ParseMentions(%q)[%d] = %+v, want %+v", tt.text, i, got[i], tt.want[i])
				}
			}
		})
	}
}

// The mention registry fake serves the same metadata sources the activity
// reads: workspace lists (authed), the journal knowledge projection and the
// saga event stream that liveSagas projects.
func mentionTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/workspace/artifacts", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"artifacts": []any{
			map[string]any{"id": "otchet-2026", "title": "Отчёт по продажам", "status": "approved", "version": 3},
		}})
	})
	mux.HandleFunc("GET /v1/workspace/skills", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"skills": []any{
			map[string]any{"id": "razbor", "name": "Разбор инцидентов", "enabled": true},
		}})
	})
	mux.HandleFunc("GET /v1/workspace/teams", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"teams": []any{
			map[string]any{"id": "platforma", "name": "Платформа", "enabled": false},
		}})
	})
	mux.HandleFunc("GET /v1/workspace/agents", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"agents": []any{
			map[string]any{"id": "researcher", "name": "Ресёрчер", "enabled": true},
		}})
	})
	mux.HandleFunc("GET /v1/workspace/projects/demo/tasks", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"tasks": []any{
			map[string]any{"id": "sozday-pr-1", "title": "Создай PR"},
		}})
	})
	mux.HandleFunc("GET /v1/observations/knowledge", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"knowledge": []any{
			map[string]any{"id": "kn-42", "proposition": "Релиз выходит по вторникам", "state": "live"},
		}})
	})
	mux.HandleFunc("GET /v1/observations/events", func(w http.ResponseWriter, r *http.Request) {
		events := []any{}
		if r.URL.Query().Get("type") == "saga.registered" {
			events = append(events, map[string]any{
				"type": "saga.registered", "occurred_at": "2026-11-01T10:00:00Z",
				"context": map[string]any{"project": "demo", "run": "r1", "task": "t1"},
				"data":    map[string]any{"saga_id": "migraciya", "title": "Миграция", "goal": "перенести сервис", "task_id": "t1"},
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"events": events, "next_cursor": ""})
	})
	return httptest.NewServer(mux)
}

func mentionTestActivities(server *httptest.Server) *Activities {
	return &Activities{HTTP: server.Client(), WorkspaceURL: server.URL, TemporalityURL: server.URL, WorkspaceToken: "internal"}
}

func TestResolveMentionsRendersReferencedEntities(t *testing.T) {
	server := mentionTestServer(t)
	defer server.Close()
	activities := mentionTestActivities(server)

	block, err := activities.ResolveMentions(context.Background(), MentionRequest{
		Project: "demo",
		Prompt:  "Обнови @artifact:otchet-2026 по @skill:razbor, сверь kn-42 и @saga:migraciya; про @artifact:net-takogo спроси пользователя.",
	})
	if err != nil {
		t.Fatalf("ResolveMentions: %v", err)
	}
	for _, want := range []string{
		`artifact "Отчёт по продажам" (id: otchet-2026) — status: approved, version: 3`,
		`skill "Разбор инцидентов" (id: razbor)`,
		`knowledge "Релиз выходит по вторникам" (id: kn-42) — state: live`,
		`saga "Миграция" (id: migraciya) — goal: перенести сервис`,
		"artifact_read",
		"No workspace entity matches @artifact:net-takogo",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("block missing %q:\n%s", want, block)
		}
	}
	// The block is identity only — bodies stay behind the agent's chunked tools.
	if strings.Contains(block, "перенести сервис, а потом") {
		t.Errorf("block leaked body text:\n%s", block)
	}
}

func TestResolveMentionsDedupesAndResolvesBareIDs(t *testing.T) {
	server := mentionTestServer(t)
	defer server.Close()
	activities := mentionTestActivities(server)

	block, err := activities.ResolveMentions(context.Background(), MentionRequest{
		Project: "demo",
		Prompt:  "@artifact:otchet-2026 / otchet-2026 / @otchet-2026 — один и тот же артефакт",
	})
	if err != nil {
		t.Fatalf("ResolveMentions: %v", err)
	}
	if got := strings.Count(block, "id: otchet-2026"); got != 1 {
		t.Fatalf("want one resolved entry, got %d:\n%s", got, block)
	}
	// Bare ids resolve silently — only `@…` forms earn a "not found" note.
	if strings.Contains(block, "No workspace entity matches") {
		t.Fatalf("plain id was reported unresolved:\n%s", block)
	}
}

func TestResolveMentionsSkipsPlainProse(t *testing.T) {
	server := mentionTestServer(t)
	defer server.Close()
	activities := mentionTestActivities(server)

	block, err := activities.ResolveMentions(context.Background(), MentionRequest{
		Project: "demo",
		Prompt:  "просто обнови отчёт и пришли результат",
	})
	if err != nil {
		t.Fatalf("ResolveMentions: %v", err)
	}
	if block != "" {
		t.Fatalf("want empty block for prose, got:\n%s", block)
	}
}

func TestResolveMentionsMentionsOnlyEntitiesThatExist(t *testing.T) {
	server := mentionTestServer(t)
	defer server.Close()
	activities := mentionTestActivities(server)

	block, err := activities.ResolveMentions(context.Background(), MentionRequest{
		Project: "demo",
		Prompt:  "long-running работа с @team:platforma",
	})
	if err != nil {
		t.Fatalf("ResolveMentions: %v", err)
	}
	if !strings.Contains(block, `team "Платформа" (id: platforma) — disabled`) {
		t.Fatalf("team detail missing:\n%s", block)
	}
	// The prose dash-token matched nothing and must not be called out.
	if strings.Contains(block, "long-running") {
		t.Fatalf("prose token reported as a reference:\n%s", block)
	}
}
