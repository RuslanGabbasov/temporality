package main

// Organizational Operations pure-logic tests: summary extraction caps,
// marker parsing, refs whitelisting, exec-context attribution, decision
// validation and the steer message framing
// (docs/temporality-organizational-operations-spec.md §4.3, §6, §7).

import (
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/workspace"
)

// nilTestLogger discards validation warnings — the assertions are on the
// returned decisions, not the log.
func nilTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func orgTestSettings() workspace.OrgActivitySettings {
	return workspace.OrgActivitySettings{
		ActivityEnabled: true, PublishRunStarts: true, PublishRunCompletions: true,
		PublishRunFailures: true, PublishSignificantProgress: true,
		PublishArtifactEvents: true, PublishQualityEvents: true,
		AllowTraceDiscovery: true, AwarenessScope: workspace.OrgAwarenessScopeUnit,
		BatchIntervalMinutes: 10, MaxEventsPerBatch: 200,
		MaxRecipientsPerEvent: 10, MaxDeliveriesPerRunPerWindow: 5,
	}
}

// §4.3: summaries are safe — capped, control characters collapsed, no raw
// tool payloads.
func TestOrgEventSummarySafeShape(t *testing.T) {
	raw := strings.Repeat("secret\tvalue\n", 100)
	summary := orgEventSummary("run.failed", map[string]any{"error": raw})
	require.LessOrEqual(t, len([]rune(summary)), orgSummaryMaxRunes+1) // cap + ellipsis
	require.NotContains(t, summary, "\t")
	require.NotContains(t, summary, "\n")

	require.Equal(t, "Deploy the gateway", orgEventSummary("run.started", map[string]any{"title": "Deploy the gateway"}))
	require.Equal(t, "Waiting for human input", orgEventSummary("human.requested", nil))
	require.Equal(t, "run cancelled", orgEventSummary("run.cancelled", nil))
}

// §4.2: unknown types never fold.
func TestOrgActivityCategoriesClosed(t *testing.T) {
	require.NotContains(t, orgActivityCategories, "tool.called")
	require.NotContains(t, orgActivityCategories, "model.completed")
	require.NotContains(t, orgActivityCategories, "turn.started")
	require.Contains(t, orgActivityCategories, "run.started")
	require.Contains(t, orgActivityCategories, "artifact.review.approved")
}

// §8: the consumption marker parse — one id, several, junk-safe.
func TestOrgAwarenessMarkerIDs(t *testing.T) {
	require.Nil(t, orgAwarenessMarkerIDs("no markers here"))
	require.Equal(t,
		[]string{"dlv-1"},
		orgAwarenessMarkerIDs("text [org-awareness:dlv-1] tail"))
	require.Equal(t,
		[]string{"dlv-1", "dlv-2"},
		orgAwarenessMarkerIDs("[org-awareness:dlv-1] and [org-awareness:dlv-2]"))
	require.Nil(t, orgAwarenessMarkerIDs("[org-awareness:unclosed"))
}

// §4.3: refs whitelist — only the known scalar ids survive.
func TestOrgEventRefsWhitelist(t *testing.T) {
	refs := orgEventRefs(map[string]any{
		"artifact_id": "a-1", "child_run_id": "run-2", "evil": map[string]any{"x": 1},
		"prompt": "do things", "request_id": "req-9",
	})
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(refs, &parsed))
	require.Equal(t, "a-1", parsed["artifact_id"])
	require.Equal(t, "run-2", parsed["child_run_id"])
	require.Equal(t, "req-9", parsed["request_id"])
	require.NotContains(t, parsed, "evil")
	require.NotContains(t, parsed, "prompt")
}

// §3: launch-time attribution reads exec_context.authorized_by.org_unit_id.
func TestOrgExecContextUnit(t *testing.T) {
	require.Empty(t, orgExecContextUnit(map[string]any{}))
	require.Empty(t, orgExecContextUnit(map[string]any{"authorized_by": "junk"}))
	require.Equal(t, "dept-1", orgExecContextUnit(map[string]any{
		"authorized_by": map[string]any{"org_unit_id": "dept-1", "user_id": "u-1"},
	}))
}

// §6: decision validation — the model suggests, validation decides.
func orgTestBatchFixtures() (events []workspace.OrgActivityEvent, runs []workspace.OrgActiveRun, recent []workspace.OrgAwarenessDelivery) {
	events = []workspace.OrgActivityEvent{
		{SourceID: "src", EventID: "ev-1", EventType: "run.completed", RunID: "source-run", Summary: "auth research finished", OccurredAt: time.Now()},
		{SourceID: "src", EventID: "ev-2", EventType: "run.failed", RunID: "other-run", Summary: "deploy failed", OccurredAt: time.Now()},
	}
	runs = []workspace.OrgActiveRun{
		{ProjectID: "p1", RunID: "active-run", Status: "running", OrgNodeID: "team", ActorID: "user-1"},
	}
	recent = []workspace.OrgAwarenessDelivery{
		{TargetRunID: "active-run", SourceEventID: "src/ev-old", DeduplicationKey: "src/ev-old|active-run"},
	}
	return events, runs, recent
}

func TestValidateOrgDecisionsHappyPath(t *testing.T) {
	events, runs, recent := orgTestBatchFixtures()
	verdict := agent.OrgBatchVerdict{Decisions: []agent.OrgDecision{{
		SourceEventID: "src/ev-1", TargetRunID: "active-run", Decision: "recommend_lookup",
		Relevance: "high", Reason: "related auth work", Message: "Auth research completed.",
	}}}
	decisions := validateOrgDecisions(nilTestLogger(), verdict, "batch-1", "team", "model-x", events, runs, recent, orgTestSettings(), time.Now())
	require.Len(t, decisions, 1)
	d := decisions[0]
	require.Equal(t, "recommend_lookup", d.Decision)
	require.Equal(t, "active-run", d.TargetRunID)
	require.Equal(t, "src/ev-1", d.SourceEventID) // normalized to composite id
	require.Equal(t, "source-run", d.SourceRunID)
	require.Equal(t, "src/ev-1|active-run", d.DeduplicationKey) // derived default
	require.Equal(t, "high", d.Relevance)
	require.Equal(t, "normal", d.Urgency)
}

func TestValidateOrgDecisionsDropsForeignTargetAndInventedSource(t *testing.T) {
	events, runs, recent := orgTestBatchFixtures()
	verdict := agent.OrgBatchVerdict{Decisions: []agent.OrgDecision{
		// completed/foreign run — must never be messaged (§6)
		{SourceEventID: "src/ev-1", TargetRunID: "completed-run", Decision: "notify"},
		// invented source event
		{SourceEventID: "src/ev-invented", TargetRunID: "active-run", Decision: "notify"},
		// unknown decision kind
		{TargetRunID: "active-run", Decision: "broadcast_all"},
		// valid ignore
		{Decision: "ignore"},
	}}
	decisions := validateOrgDecisions(nilTestLogger(), verdict, "batch-1", "team", "m", events, runs, recent, orgTestSettings(), time.Now())
	require.Len(t, decisions, 1)
	require.Equal(t, "ignore", decisions[0].Decision)
}

func TestValidateOrgDecisionsSuppressionVsUpdate(t *testing.T) {
	events, runs, _ := orgTestBatchFixtures()
	// A delivery with the same dedup key already exists (folded from a
	// previous batch) — §22 scenario 4.
	recent := []workspace.OrgAwarenessDelivery{
		{TargetRunID: "active-run", SourceEventID: "src/ev-1", DeduplicationKey: "src/ev-1|active-run"},
	}
	verdict := agent.OrgBatchVerdict{Decisions: []agent.OrgDecision{
		// exact key already delivered → suppressed
		{SourceEventID: "src/ev-1", TargetRunID: "active-run", Decision: "notify"},
		// update_awareness with a revision key → kept (§6.3)
		{SourceEventID: "src/ev-1", TargetRunID: "active-run", Decision: "update_awareness", DeduplicationKey: "src/ev-1|active-run|rev2"},
	}}
	decisions := validateOrgDecisions(nilTestLogger(), verdict, "batch-1", "team", "m", events, runs, recent, orgTestSettings(), time.Now())
	require.Len(t, decisions, 1)
	require.Equal(t, "update_awareness", decisions[0].Decision)
	require.Equal(t, "src/ev-1|active-run|rev2", decisions[0].DeduplicationKey)
}

func TestValidateOrgDecisionsNormalizesEnums(t *testing.T) {
	events, runs, _ := orgTestBatchFixtures()
	verdict := agent.OrgBatchVerdict{Decisions: []agent.OrgDecision{{
		SourceEventID: "ev-1", TargetRunID: "active-run", Decision: "notify",
		Relevance: "critical", Urgency: "ASAP", // both invalid → defaults
	}}}
	decisions := validateOrgDecisions(nilTestLogger(), verdict, "batch-1", "team", "m", events, runs, nil, orgTestSettings(), time.Now())
	require.Len(t, decisions, 1)
	require.Equal(t, "medium", decisions[0].Relevance)
	require.Equal(t, "normal", decisions[0].Urgency)
	// bare event id matched too (no source prefix)
	require.Equal(t, "src/ev-1", decisions[0].SourceEventID)
}

// §7: the steer message names itself as organizational awareness, carries
// the source run, the reason, the details option and the marker.
func TestOrgAwarenessSteerMessage(t *testing.T) {
	msg := orgAwarenessSteerMessage(workspace.OrgAwarenessDelivery{
		ID: "dlv-9", SourceRunID: "source-run", SourceSummary: "auth research finished",
		Reason: "same component", Message: "Auth research completed.", DetailLevel: "summary",
	})
	require.Contains(t, msg, "Organizational awareness")
	require.Contains(t, msg, "not a new task")
	require.Contains(t, msg, "source-run")
	require.Contains(t, msg, "same component")
	require.Contains(t, msg, "request_related_run_details")
	require.Contains(t, msg, "summary")
	require.Contains(t, msg, "[org-awareness:dlv-9]")
	// Round trip: the fold's marker parser finds the id again.
	require.Equal(t, []string{"dlv-9"}, orgAwarenessMarkerIDs(msg))
}

func TestOrgAwarenessSteerMessageWithoutSource(t *testing.T) {
	msg := orgAwarenessSteerMessage(workspace.OrgAwarenessDelivery{ID: "dlv-10"})
	require.NotContains(t, msg, "request_related_run_details")
	require.Contains(t, msg, "[org-awareness:dlv-10]")
}

// §5 scope resolution for reads.
func TestOrgUnitNodeScope(t *testing.T) {
	units := []workspace.OrgUnit{
		{ID: "root"},
		{ID: "dept", ParentID: "root", Path: "root"},
		{ID: "team", ParentID: "dept", Path: "root.dept"},
		{ID: "other", ParentID: "root", Path: "root"},
	}
	// nil visible = unfiltered
	scope, ok := orgUnitNodeScope(units, nil, "", "")
	require.True(t, ok)
	require.Nil(t, scope)
	// invisible node → not ok (404 at the handler)
	_, ok = orgUnitNodeScope(units, []string{"dept"}, "other", "")
	require.False(t, ok)
	// unit scope pins the node
	scope, ok = orgUnitNodeScope(units, []string{"dept"}, "dept", "unit")
	require.True(t, ok)
	require.Equal(t, []string{"dept"}, scope)
	// subtree scope adds descendants (self-path prefix on segment boundary)
	scope, ok = orgUnitNodeScope(units, nil, "dept", "subtree")
	require.True(t, ok)
	require.ElementsMatch(t, []string{"dept", "team"}, scope)
	// "other" must NOT leak into dept's subtree
	require.NotContains(t, scope, "other")
}
