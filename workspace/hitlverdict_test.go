package workspace

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestVerdictProjection covers every human_request status of migration 000040
// (specs/quality-loop.md §3.3): answered splits by an exact options match,
// rejected is a verdict, expired/cancelled/pending/delivered carry none.
func TestVerdictProjection(t *testing.T) {
	cases := []struct {
		name     string
		status   string
		response string
		options  []string
		want     string
	}{
		{"pending has no verdict", HumanStatusPending, "", []string{"yes", "no"}, ""},
		{"delivered has no verdict", HumanStatusDelivered, "", []string{"yes", "no"}, ""},
		{"answered exact option is accepted", HumanStatusAnswered, "yes", []string{"yes", "no"}, VerdictAccepted},
		{"answered free text is corrected", HumanStatusAnswered, "deploy on friday", []string{"yes", "no"}, VerdictCorrected},
		{"answered case mismatch is corrected (exact match only)", HumanStatusAnswered, "Yes", []string{"yes"}, VerdictCorrected},
		{"answered punctuation mismatch is corrected", HumanStatusAnswered, "yes.", []string{"yes"}, VerdictCorrected},
		{"answered with no options is corrected", HumanStatusAnswered, "anything", nil, VerdictCorrected},
		{"expired has no verdict", HumanStatusExpired, "", []string{"yes"}, ""},
		{"cancelled has no verdict", HumanStatusCancelled, "", []string{"yes"}, ""},
		{"rejected is rejected", HumanStatusRejected, "", []string{"yes"}, VerdictRejected},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Verdict(HumanRequest{Status: tc.status, Response: tc.response, Options: tc.options})
			require.Equal(t, tc.want, got)
		})
	}
}

// sliceRow feeds scanHumanRequest with column values in the
// humanRequestColumns order; the length check guards column-order drift.
type sliceRow []any

func (r sliceRow) Scan(dest ...any) error {
	if len(dest) != len(r) {
		return fmt.Errorf("column mismatch: %d destinations, %d values", len(dest), len(r))
	}
	for i, target := range dest {
		switch t := target.(type) {
		case *string:
			*t, _ = r[i].(string)
		case *int:
			*t, _ = r[i].(int)
		case *[]byte:
			*t, _ = r[i].([]byte)
		case *time.Time:
			*t, _ = r[i].(time.Time)
		case **time.Time:
			if at, ok := r[i].(time.Time); ok {
				*t = &at
			} else {
				*t = nil
			}
		default:
			return fmt.Errorf("unsupported destination %T", target)
		}
	}
	return nil
}

// TestScanHumanRequestIncludesAgentVersion round-trips the pinned version
// through the shared column list and scan (§3.1).
func TestScanHumanRequestIncludesAgentVersion(t *testing.T) {
	now := time.Now()
	values := sliceRow{"op-1", "run-1", "proj", "agent-a", 7, "user:ruslan", "ruslan",
		"Deploy?", "", []byte(`["yes","no"]`), "answered", "web", "", "yes", "ruslan",
		300, "fallback", "", now, nil, nil, nil}
	request, err := scanHumanRequest(values)
	require.NoError(t, err)
	require.Equal(t, 7, request.AgentVersion)
	require.Equal(t, []string{"yes", "no"}, request.Options)
	require.Equal(t, "answered", request.Status)
}

// TestAgentVersionIsWrittenOnlyAtCreation guards the §3.1 immutability
// invariant at the source level (unit tests here have no database):
// agent_version appears exactly in the shared column list, the scan and the
// INSERT of CreateHumanRequest, and never inside the SET clause of any
// UPDATE human_request statement — the value is pinned at ask time.
func TestAgentVersionIsWrittenOnlyAtCreation(t *testing.T) {
	raw, err := os.ReadFile("humanrequest.go")
	require.NoError(t, err)
	source := string(raw)

	require.Contains(t, humanRequestColumns, "agent_id, agent_version, recipient")
	require.Contains(t, source, "&r.AgentID, &r.AgentVersion, &r.Recipient")
	require.Contains(t, source, "(id, run_id, project_id, agent_id, agent_version, recipient")

	parts := strings.Split(source, "UPDATE human_request")
	require.Greater(t, len(parts), 1, "expected the known UPDATE statements to stay in place")
	for _, part := range parts[1:] {
		if where := strings.Index(part, "WHERE"); where > 0 {
			part = part[:where]
		}
		require.NotContains(t, part, "agent_version",
			"agent_version must never be reassigned by an UPDATE")
	}
}

// TestClassifyRunOutcome covers the deterministic «правленый/подтверждённый»
// rule (§3.4) on every verdict shape.
func TestClassifyRunOutcome(t *testing.T) {
	accepted := HITLVerdict{Source: VerdictSourceHumanRequest, Type: VerdictAccepted}
	corrected := HITLVerdict{Source: VerdictSourceHumanRequest, Type: VerdictCorrected}
	rejected := HITLVerdict{Source: VerdictSourceHumanRequest, Type: VerdictRejected}
	approval := HITLVerdict{Source: VerdictSourceApproval, Type: VerdictAccepted}
	reconcile := HITLVerdict{Source: VerdictSourceReconcile, Type: VerdictCorrected}

	require.Equal(t, RunOutcomeConfirmed, ClassifyRunOutcome("succeeded", nil))
	require.Equal(t, RunOutcomeConfirmed, ClassifyRunOutcome("succeeded", []HITLVerdict{accepted, accepted}))
	require.Equal(t, RunOutcomeHumanCorrected, ClassifyRunOutcome("succeeded", []HITLVerdict{accepted, corrected}))
	require.Equal(t, RunOutcomeHumanCorrected, ClassifyRunOutcome("failed", []HITLVerdict{rejected}))
	require.Equal(t, RunOutcomeHumanCorrected, ClassifyRunOutcome("succeeded", []HITLVerdict{approval}))
	require.Equal(t, RunOutcomeHumanCorrected, ClassifyRunOutcome("succeeded", []HITLVerdict{reconcile}))
	require.Empty(t, ClassifyRunOutcome("started", nil))
	require.Empty(t, ClassifyRunOutcome("failed", nil))
	require.Empty(t, ClassifyRunOutcome("cancelled", []HITLVerdict{accepted}))
}

// TestRedactVerdictText checks the §10.3 behaviour: answers leave storage
// without credential-looking fragments and report the redaction.
func TestRedactVerdictText(t *testing.T) {
	clean, redacted := redactVerdictText("deploy to prod")
	require.False(t, redacted)
	require.Equal(t, "deploy to prod", clean)

	clean, redacted = redactVerdictText("use token=abc123 for deploy")
	require.True(t, redacted)
	require.Equal(t, "use token=[REDACTED] for deploy", clean)

	clean, redacted = redactVerdictText("Bearer eyJhbGciOiJIUzI1NiJ9.payload")
	require.True(t, redacted)
	require.Equal(t, "Bearer [REDACTED]", clean)

	clean, redacted = redactVerdictText("postgres://user:pass@db-internal:5432/app")
	require.True(t, redacted)
	require.Equal(t, "postgres://[REDACTED]@db-internal:5432/app", clean)
}
