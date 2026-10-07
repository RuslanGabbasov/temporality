package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func emptyEnv(string) string { return "" }

func runCLI(t *testing.T, env func(string) string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(args, env, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestRunUsage(t *testing.T) {
	for _, args := range [][]string{{}, {"-h"}, {"--help"}, {"help"}, {"skill", "-h"}} {
		code, stdout, _ := runCLI(t, emptyEnv, args...)
		if code != 0 {
			t.Fatalf("args %v: exit %d, want 0", args, code)
		}
		if !strings.Contains(stdout, "Usage:") || !strings.Contains(stdout, "temporality skill validate") {
			t.Fatalf("args %v: missing usage text:\n%s", args, stdout)
		}
	}
}

func TestRunUnknownCommand(t *testing.T) {
	code, _, stderr := runCLI(t, emptyEnv, "frobnicate")
	if code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if !strings.Contains(stderr, `unknown command "frobnicate"`) {
		t.Fatalf("stderr: %s", stderr)
	}
	code, _, stderr = runCLI(t, emptyEnv, "skill", "explode")
	if code != 2 || !strings.Contains(stderr, `unknown command "explode"`) {
		t.Fatalf("exit %d stderr: %s", code, stderr)
	}
}

func TestRunSkillWithoutSubcommand(t *testing.T) {
	code, _, stderr := runCLI(t, emptyEnv, "skill")
	if code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if !strings.Contains(stderr, "Usage:") {
		t.Fatalf("stderr: %s", stderr)
	}
}

func TestSubcommandRequiresID(t *testing.T) {
	for _, sub := range []string{"inspect", "history", "executions", "memory", "validate"} {
		code, _, stderr := runCLI(t, emptyEnv, "skill", sub)
		if code != 2 {
			t.Fatalf("%s: exit %d, want 2", sub, code)
		}
		if !strings.Contains(stderr, "usage:") {
			t.Fatalf("%s: stderr: %s", sub, stderr)
		}
	}
}

func TestUnknownFlag(t *testing.T) {
	code, _, stderr := runCLI(t, emptyEnv, "skill", "list", "--nope")
	if code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if !strings.Contains(stderr, "flag provided but not defined") {
		t.Fatalf("stderr: %s", stderr)
	}
}

func TestReorderFlags(t *testing.T) {
	got := reorderFlags([]string{"deploy", "--json", "--url", "http://x", "extra"})
	want := []string{"--json", "--url", "http://x", "deploy", "extra"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("got %v, want %v", got, want)
	}
	// Flags after positionals are moved in front.
	got = reorderFlags([]string{"deploy", "--json"})
	if strings.Join(got, "\x00") != strings.Join([]string{"--json", "deploy"}, "\x00") {
		t.Fatalf("got %v", got)
	}
	// = form does not swallow the next positional.
	got = reorderFlags([]string{"--url=http://x", "deploy"})
	if strings.Join(got, "\x00") != strings.Join([]string{"--url=http://x", "deploy"}, "\x00") {
		t.Fatalf("got %v", got)
	}
}

// stubAPI fakes the kernel workspace skill API and records the Authorization
// header and paths of the last request.
func stubAPI(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

func TestListHuman(t *testing.T) {
	server := stubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("Authorization = %q", got)
		}
		if r.URL.Path != "/v1/workspace/skills" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"count": 1,
			"skills": []map[string]any{{
				"id": "deploy-service", "name": "Deploy service", "version": "1.2.0",
				"version_status": "active", "updated_at": "2026-10-06T16:39:51Z",
			}},
		})
	})
	code, stdout, stderr := runCLI(t, emptyEnv, "skill", "list", "--url", server.URL, "--token", "tok")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "deploy-service") || !strings.Contains(stdout, "1.2.0") || !strings.Contains(stdout, "Deploy service") {
		t.Fatalf("stdout: %s", stdout)
	}
	if !strings.Contains(stdout, "ID") || !strings.Contains(stdout, "UPDATED") {
		t.Fatalf("missing table header: %s", stdout)
	}
}

func TestListJSON(t *testing.T) {
	server := stubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"count":  1,
			"skills": []map[string]any{{"id": "deploy-service"}},
		})
	})
	env := func(key string) string {
		if key == "TEMPORALITY_URL" {
			return server.URL
		}
		return ""
	}
	// The URL comes from TEMPORALITY_URL, no flags needed.
	code, stdout, stderr := runCLI(t, env, "skill", "list", "--json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	var payload struct {
		Count  int `json:"count"`
		Skills []struct {
			ID string `json:"id"`
		} `json:"skills"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("stdout not JSON: %v\n%s", err, stdout)
	}
	if payload.Count != 1 || len(payload.Skills) != 1 || payload.Skills[0].ID != "deploy-service" {
		t.Fatalf("payload: %+v", payload)
	}
}

func TestEnvTokenUsed(t *testing.T) {
	server := stubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Errorf("Authorization = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"skills": []any{}, "count": 0})
	})
	env := func(key string) string {
		switch key {
		case "TEMPORALITY_URL":
			return server.URL
		case "TEMPORALITY_API_TOKEN":
			return "secret"
		}
		return ""
	}
	code, _, stderr := runCLI(t, env, "skill", "list")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
}

func TestHTTPErrorDecoded(t *testing.T) {
	server := stubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"skill not found"}`))
	})
	code, _, stderr := runCLI(t, emptyEnv, "skill", "inspect", "nope", "--url", server.URL)
	if code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if !strings.Contains(stderr, "HTTP 404: skill not found") {
		t.Fatalf("stderr: %s", stderr)
	}
}

func TestExecutionsProjectFilterAndLimit(t *testing.T) {
	server := stubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/workspace/skills/deploy-service/executions" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.URL.Query().Get("limit"); got != "10" {
			t.Errorf("limit = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"executions": []map[string]any{
			{"id": 2, "project_id": "lighthouse", "run_id": "run/2", "started_at": "2026-10-07T05:50:38Z"},
			{"id": 1, "project_id": "other", "run_id": "run/1", "started_at": "2026-10-07T05:50:00Z"},
		}})
	})
	code, stdout, stderr := runCLI(t, emptyEnv, "skill", "executions", "deploy-service",
		"--url", server.URL, "--project", "lighthouse", "--limit", "10")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if strings.Contains(stdout, "run/1") || !strings.Contains(stdout, "run/2") {
		t.Fatalf("project filter failed: %s", stdout)
	}
}

func TestEmptyResults(t *testing.T) {
	server := stubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"executions": []any{}})
	})
	code, stdout, _ := runCLI(t, emptyEnv, "skill", "executions", "x", "--url", server.URL)
	if code != 0 || !strings.Contains(stdout, "no executions") {
		t.Fatalf("exit %d stdout: %s", code, stdout)
	}
}

func TestMemoryHuman(t *testing.T) {
	server := stubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/workspace/skills/graphmap-qa-workflow/memory" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"memory": []map[string]any{
			{
				"knowledge_id": "run/delegate/01/knowledge/51",
				"proposition":  "GraphMap service was unavailable during this QA run.",
				"capability":   "resolve target map and read structure",
				"project":      "lighthouse",
				"occurred_at":  "2026-10-03T10:13:18Z",
				"state":        "invalidated",
			},
		}})
	})
	code, stdout, stderr := runCLI(t, emptyEnv, "skill", "memory", "graphmap-qa-workflow", "--url", server.URL)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	for _, want := range []string{"invalidated", "lighthouse", "51", "GraphMap service was unavailable"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout missing %q:\n%s", want, stdout)
		}
	}
}
