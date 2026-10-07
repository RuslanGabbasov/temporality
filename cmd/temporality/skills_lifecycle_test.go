package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// The evolution commands hit version lifecycle endpoints and render diffs.

func TestApplyAndRejectHitLifecycleEndpoints(t *testing.T) {
	var method, path string
	server := stubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		switch {
		case strings.HasSuffix(path, "/apply"):
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "deploy-service", "version": "1.3.0"})
		case strings.HasSuffix(path, "/reject"):
			_ = json.NewEncoder(w).Encode(map[string]any{"skill_id": "deploy-service", "version": "1.3.0", "status": "rejected"})
		}
	})
	code, stdout, stderr := runCLI(t, emptyEnv, "skill", "apply", "--url", server.URL, "deploy-service", "1.3.0")
	if code != 0 {
		t.Fatalf("apply exit %d: %s", code, stderr)
	}
	if method != http.MethodPost || path != "/v1/workspace/skills/deploy-service/versions/1.3.0/apply" {
		t.Fatalf("apply request = %s %s", method, path)
	}
	if !strings.Contains(stdout, "applied deploy-service 1.3.0") {
		t.Fatalf("stdout: %s", stdout)
	}

	code, stdout, stderr = runCLI(t, emptyEnv, "skill", "reject", "--url", server.URL, "deploy-service", "1.3.0")
	if code != 0 {
		t.Fatalf("reject exit %d: %s", code, stderr)
	}
	if method != http.MethodPost || path != "/v1/workspace/skills/deploy-service/versions/1.3.0/reject" {
		t.Fatalf("reject request = %s %s", method, path)
	}
	if !strings.Contains(stdout, "rejected deploy-service 1.3.0") {
		t.Fatalf("stdout: %s", stdout)
	}
}

func TestDiffCurrentVsDraft(t *testing.T) {
	server := stubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"versions": []map[string]any{
			{
				"version": "1.3.0", "status": "draft",
				"markdown": "## Purpose\nDeploy.\nWait for health.",
				"manifest": map[string]any{"id": "deploy-service", "capabilities": []string{"deploy", "verify"}, "tools": []string{"run_command"}},
			},
			{
				"version": "1.2.0", "status": "active",
				"markdown": "## Purpose\nDeploy.",
				"manifest": map[string]any{"id": "deploy-service", "capabilities": []string{"deploy"}, "tools": []string{"run_command", "curl"}},
			},
		}})
	})
	code, stdout, stderr := runCLI(t, emptyEnv, "skill", "diff", "--url", server.URL, "deploy-service")
	if code != 0 {
		t.Fatalf("diff exit %d: %s", code, stderr)
	}
	for _, want := range []string{"1.2.0 → 1.3.0", "capabilities: (none) → + verify", "tools: - curl → (none)", "SKILL.md: +1 -0 lines"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("diff missing %q in:\n%s", want, stdout)
		}
	}
}

func TestEvalsListAndEvalRun(t *testing.T) {
	server := stubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/evaluations") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": 7, "skill_version": "1.2.0", "passed": 1, "failed": 1,
				"cases": []map[string]any{
					{"name": "health", "passed": true},
					{"name": "strict", "passed": false, "missed": []string{"deployed"}},
				},
				"created_at": "2026-10-07T07:00:00Z",
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"runs": []map[string]any{
			{"id": 7, "skill_version": "1.2.0", "passed": 1, "failed": 1, "created_at": "2026-10-07T07:00:00Z"},
		}})
	})
	code, stdout, stderr := runCLI(t, emptyEnv, "skill", "evals", "--url", server.URL, "deploy-service")
	if code != 0 {
		t.Fatalf("evals exit %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "1.2.0") || !strings.Contains(stdout, "PASSED") {
		t.Fatalf("evals stdout: %s", stdout)
	}

	// A run with failures exits 1 and lists the failed case with its miss.
	code, stdout, stderr = runCLI(t, emptyEnv, "skill", "eval-run", "--url", server.URL, "deploy-service")
	if code != 1 {
		t.Fatalf("eval-run exit %d, want 1 (failed cases): %s", code, stderr)
	}
	if !strings.Contains(stdout, "✗ strict") || !strings.Contains(stdout, "missed: deployed") {
		t.Fatalf("eval-run stdout: %s", stdout)
	}
}
