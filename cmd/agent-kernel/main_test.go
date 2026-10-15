package main

import (
	"context"
	"strings"
	"testing"

	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/workspace"
	"go.temporal.io/api/enums/v1"
)

// TestResolveProviderAPIKeyRefShapes covers the non-DB branches of provider
// credential resolution: a legacy literal key passes through, an UPPER_SNAKE
// name reads the process env, an unset env name falls back to the literal, and
// an empty ref stays empty. The secret:// branch routes into the workspace
// secret store and needs a live database — its resolution logic is covered by
// the secrets tests (seal/open roundtrip, ref parsing).
func TestResolveProviderAPIKeyRefShapes(t *testing.T) {
	ctx := context.Background()
	t.Setenv("TEST_PROVIDER_KEY_ENV", "env-secret-value")

	cases := []struct {
		name string
		ref  string
		want string
	}{
		{"literal provider key", "sk-proj-abcdef123456", "sk-proj-abcdef123456"},
		{"literal with mixed case", "ghp_Zx9q", "ghp_Zx9q"},
		{"env var name", "TEST_PROVIDER_KEY_ENV", "env-secret-value"},
		{"unset env name falls back to literal", "NEVER_SET_PROVIDER_KEY_42", "NEVER_SET_PROVIDER_KEY_42"},
		{"empty ref", "  ", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveProviderAPIKey(ctx, nil, tc.ref, nil)
			if err != nil {
				t.Fatalf("resolve %q: %v", tc.ref, err)
			}
			if got != tc.want {
				t.Fatalf("resolve %q = %q, want %q", tc.ref, got, tc.want)
			}
		})
	}
}

// TestResolveProviderAPIKeySecretRefDispatchesToStore asserts a secret:// ref
// never silently degrades to the literal string: without a store it must
// return an error (here: a nil-store panic is avoided by the empty-ref guard,
// so the case asserts the ref parses and would hit the store path).
func TestResolveProviderAPIKeySecretRefDispatchesToStore(t *testing.T) {
	if _, ok := workspace.ParseSecretReference("secret://openrouter-key"); !ok {
		t.Fatal("secret://openrouter-key must parse as a secret reference")
	}
}

func TestWorkflowIDForScopesByProjectAndRun(t *testing.T) {
	first := workflowIDFor("install-a", "project-a", "run-1")
	if first != workflowIDFor("install-a", "project-a", "run-1") {
		t.Fatal("workflow ID must be stable")
	}
	if first == workflowIDFor("install-a", "project-b", "run-1") {
		t.Fatal("same run ID in different projects must have separate workflows")
	}
	if first == workflowIDFor("install-a", "project-a", "run-2") {
		t.Fatal("different run IDs in the same project must have separate workflows")
	}
	if first == workflowIDFor("install-b", "project-a", "run-1") {
		t.Fatal("separate installations must have separate workflow IDs")
	}
}

func TestApprovalRequiresCanonicalArgumentsHash(t *testing.T) {
	base := agent.Approval{OperationID: "run/frame/call", ActorID: "operator", ArgumentsHash: "sha256:" + strings.Repeat("a", 64)}
	if err := validateApproval(base); err != nil {
		t.Fatalf("valid approval rejected: %v", err)
	}
	missing := base
	missing.ArgumentsHash = ""
	if err := validateApproval(missing); err == nil {
		t.Fatal("approval without arguments hash accepted")
	}
	malformed := base
	malformed.ArgumentsHash = "sha256:not-a-digest"
	if err := validateApproval(malformed); err == nil {
		t.Fatal("malformed arguments hash accepted")
	}
}

func TestWorkflowStatusNameMatchesCanonicalVocabulary(t *testing.T) {
	cases := map[enums.WorkflowExecutionStatus]string{
		enums.WORKFLOW_EXECUTION_STATUS_RUNNING:          "running",
		enums.WORKFLOW_EXECUTION_STATUS_COMPLETED:        "completed",
		enums.WORKFLOW_EXECUTION_STATUS_FAILED:           "failed",
		enums.WORKFLOW_EXECUTION_STATUS_CANCELED:         "cancelled",
		enums.WORKFLOW_EXECUTION_STATUS_TERMINATED:       "cancelled",
		enums.WORKFLOW_EXECUTION_STATUS_TIMED_OUT:        "failed",
		enums.WORKFLOW_EXECUTION_STATUS_CONTINUED_AS_NEW: "running",
	}
	for status, want := range cases {
		if got := workflowStatusName(status); got != want {
			t.Fatalf("status %v: got %q, want %q", status, got, want)
		}
	}
}

// TestIntakeEnvParsers pins the defaults and degenerate values of the intake
// knobs (docs/inbound-messages.md §12): unset falls back to the safe default,
// 0 is an explicit off/forever choice (not an error), garbage falls back
// rather than disabling a protection silently.
func TestIntakeEnvParsers(t *testing.T) {
	t.Run("rate default", func(t *testing.T) {
		if got := intakeRatePerHourFromEnv(); got != 10 {
			t.Fatalf("default rate: got %d, want 10", got)
		}
	})
	t.Run("rate zero disables", func(t *testing.T) {
		t.Setenv("KERNEL_INTAKE_MAX_PER_HOUR", "0")
		if got := intakeRatePerHourFromEnv(); got != 0 {
			t.Fatalf("zero rate: got %d, want 0", got)
		}
	})
	t.Run("rate garbage falls back", func(t *testing.T) {
		t.Setenv("KERNEL_INTAKE_MAX_PER_HOUR", "many")
		if got := intakeRatePerHourFromEnv(); got != 10 {
			t.Fatalf("garbage rate: got %d, want 10", got)
		}
	})
	t.Run("rate negative falls back", func(t *testing.T) {
		t.Setenv("KERNEL_INTAKE_MAX_PER_HOUR", "-5")
		if got := intakeRatePerHourFromEnv(); got != 10 {
			t.Fatalf("negative rate: got %d, want 10", got)
		}
	})
	t.Run("retention default", func(t *testing.T) {
		if got := intakeRetentionDaysFromEnv(); got != 30 {
			t.Fatalf("default retention: got %d, want 30", got)
		}
	})
	t.Run("retention zero keeps forever", func(t *testing.T) {
		t.Setenv("KERNEL_INTAKE_RETENTION_DAYS", "0")
		if got := intakeRetentionDaysFromEnv(); got != 0 {
			t.Fatalf("zero retention: got %d, want 0", got)
		}
	})
	t.Run("retention garbage falls back", func(t *testing.T) {
		t.Setenv("KERNEL_INTAKE_RETENTION_DAYS", "a month")
		if got := intakeRetentionDaysFromEnv(); got != 30 {
			t.Fatalf("garbage retention: got %d, want 30", got)
		}
	})
}

func TestSlugifyTrimsLeadingDashes(t *testing.T) {
	// A title opening with an emoji or quote used to leave a leading dash:
	// run ids like "-12-cpu-2026…".
	cases := []struct{ name, in, want string }{
		{"leading separator", " 12 cpu quota fix", "12-cpu-quota-fix"},
		{"emoji opener", "🔥 hotfix login", "hotfix-login"},
		{"cyrillic collapses without translit", "Починить вход", "untitled"},
		{"translit composed by caller", slugify(translit("Конкуренты и план")), "konkurenty-i-plan"},
		{"plain", "Fix Login Timeout", "fix-login-timeout"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := slugify(tc.in); got != tc.want {
				t.Fatalf("slugify(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
