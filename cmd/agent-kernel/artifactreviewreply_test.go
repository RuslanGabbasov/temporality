package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/workspace"
)

// Channel reply → review resolution (docs/artifact-lifecycle.md §6 Phase 2).

func TestParseArtifactReviewDecision(t *testing.T) {
	cases := []struct {
		text    string
		approve bool
		comment string
		ok      bool
	}{
		{"approve", true, "", true},
		{"Approve", true, "", true},
		{"LGTM", true, "", true},
		{"ок", true, "", true},
		{"Одобряю, но проверь метрики", true, "но проверь метрики", true},
		{"approve: fix the diagram first", true, "fix the diagram first", true},
		{"reject.", false, "", true},
		{"Отклоняю — слишком сыро", false, "— слишком сыро", true},
		{"согласен", true, "", true},
		{"maybe approve later", false, "", false}, // keyword not first
		{"да", false, "", false},                  // ambiguous on purpose
		{"нет", false, "", false},
		{"", false, "", false},
		{"could you resend the link?", false, "", false},
	}
	for _, tc := range cases {
		approve, comment, ok := parseArtifactReviewDecision(tc.text)
		require.Equal(t, tc.ok, ok, "ok mismatch for %q", tc.text)
		if tc.ok {
			require.Equal(t, tc.approve, approve, "approve mismatch for %q", tc.text)
			require.Equal(t, tc.comment, comment, "comment mismatch for %q", tc.text)
		}
	}
}

func TestArtifactReviewFromRequestRun(t *testing.T) {
	id, ok := artifactReviewFromRequestRun("artifact-review/art-1-v2-123")
	require.True(t, ok)
	require.Equal(t, "art-1-v2-123", id)

	_, ok = artifactReviewFromRequestRun("artifact-review/")
	require.False(t, ok) // prefix alone is not a round

	_, ok = artifactReviewFromRequestRun("run/delegate/03")
	require.False(t, ok) // ordinary asks stay ordinary
}

// reviewEchoDriver is a thread-capable transport that records every text and
// hands out deterministic thread roots.
type reviewEchoDriver struct {
	mu     sync.Mutex
	sent   *[]string
	nextID int
}

func (d *reviewEchoDriver) Type() string { return "matrix" }
func (d *reviewEchoDriver) Spec() agent.ChannelSpec {
	return agent.ChannelSpec{Capabilities: agent.ChannelCapabilities{Threads: true}}
}
func (d *reviewEchoDriver) IsConfigured(map[string]string) bool { return true }
func (d *reviewEchoDriver) SendText(_ context.Context, _ *http.Client, _ map[string]string, _ string, msg agent.OutboundText) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.sent != nil {
		*d.sent = append(*d.sent, msg.Body)
	}
	return nil
}
func (d *reviewEchoDriver) SendFile(context.Context, *http.Client, map[string]string, string, agent.OutboundFile) error {
	return agent.ErrChannelFilesUnsupported
}
func (d *reviewEchoDriver) Poller() agent.ChannelPoller   { return nil }
func (d *reviewEchoDriver) Webhook() agent.ChannelWebhook { return nil }
func (d *reviewEchoDriver) SendTextThreaded(_ context.Context, _ *http.Client, _ map[string]string, _ string, msg agent.OutboundText) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.nextID++
	if d.sent != nil {
		*d.sent = append(*d.sent, msg.Body)
	}
	return "$review-root-" + strconv.Itoa(d.nextID), nil
}

func reviewMatrixSettings() agent.ChannelSettings {
	return agent.ChannelSettings{Channels: map[string]map[string]string{
		"matrix": {"homeserver": "https://hs", "access_token": "t"},
	}}
}

// reviewReplyLoop wires a channel loop against a fake store with one open
// round, the way main does (signal and journal are never used on this path).
func reviewReplyLoop(store *fakeInboundStore, notices *[]string) *channelInboundLoop {
	driver := &reviewEchoDriver{sent: notices}
	return &channelInboundLoop{
		driver: driver, httpClient: http.DefaultClient, store: store,
		sourceID: "src", log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func reviewFixture() (*fakeInboundStore, agent.ChannelReply) {
	store := &fakeInboundStore{
		users: []workspace.User{
			{ID: "u-rev", Name: "Reviewer", Role: "writer", Active: true},
			{ID: "u-other", Name: "Other", Role: "writer", Active: true},
		},
		artifacts: map[string]workspace.Artifact{
			"art-1": {ID: "art-1", Title: "Требования", Status: workspace.ArtifactInReview},
		},
		reviews: []workspace.ArtifactReview{{
			ID: "art-1-v1-1", ArtifactID: "art-1", ArtifactVersion: 1,
			Reviewers: []string{"user:u-rev"}, Mode: "any", Status: "open",
		}},
	}
	reply := agent.ChannelReply{
		Ask: agent.PendingAsk{
			RequestID: artifactReviewRequestID("art-1-v1-1"),
			RunID:     artifactReviewRequestID("art-1-v1-1"),
			UserID:    "u-rev",
		},
		Message: agent.InboundMessage{ID: "m1", ChatID: "@u-rev:hs", Sender: "u-sender", Text: "approve"},
	}
	return store, reply
}

func TestRouteArtifactReviewReplyApproves(t *testing.T) {
	store, reply := reviewFixture()
	var notices []string
	loop := reviewReplyLoop(store, &notices)

	loop.routeArtifactReviewReply(context.Background(), map[string]string{}, reply, "art-1-v1-1")

	// The round closed as approved, the row closed as answered, the chat got
	// a confirmation, the audit trail carries the artifact action.
	require.Equal(t, []string{"art-1-v1-1"}, store.resolvedIDs)
	require.Equal(t, workspace.ArtifactApproved, store.reviews[0].Status)
	require.Equal(t, "u-rev", store.reviews[0].Decisions["user:u-rev"].DecidedBy)
	require.Contains(t, store.answered[artifactReviewRequestID("art-1-v1-1")], "approve")
	require.Len(t, notices, 1)
	require.Contains(t, notices[0], "Требования")
	require.Contains(t, store.audits, workspace.AuditArtifactReviewApproved)
}

func TestRouteArtifactReviewReplyRejects(t *testing.T) {
	store, reply := reviewFixture()
	reply.Message.Text = "Отклоняю — слишком сыро"
	var notices []string
	loop := reviewReplyLoop(store, &notices)

	loop.routeArtifactReviewReply(context.Background(), map[string]string{}, reply, "art-1-v1-1")

	require.Equal(t, workspace.ArtifactRejected, store.reviews[0].Status)
	require.Contains(t, store.answered[artifactReviewRequestID("art-1-v1-1")], "слишком сыро")
	require.Contains(t, store.audits, workspace.AuditArtifactReviewRejected)
}

func TestRouteArtifactReviewReplyUnknownKeywordAsksHow(t *testing.T) {
	store, reply := reviewFixture()
	reply.Message.Text = "а что тут проверять?"
	var notices []string
	loop := reviewReplyLoop(store, &notices)

	loop.routeArtifactReviewReply(context.Background(), map[string]string{}, reply, "art-1-v1-1")

	require.Empty(t, store.resolvedIDs) // round stays open
	require.Empty(t, store.answered)
	require.Len(t, notices, 1) // usage hint
}

func TestRouteArtifactReviewReplyNonReviewerRefused(t *testing.T) {
	store, reply := reviewFixture()
	reply.Ask.UserID = "u-other" // the chat owner is not on the round
	var notices []string
	loop := reviewReplyLoop(store, &notices)

	loop.routeArtifactReviewReply(context.Background(), map[string]string{}, reply, "art-1-v1-1")

	require.Empty(t, store.resolvedIDs)
	require.Len(t, notices, 1)
	require.Contains(t, notices[0], "not a reviewer")
}

func TestRouteArtifactReviewReplyAlreadyResolvedClosesRow(t *testing.T) {
	store, reply := reviewFixture()
	store.reviews[0].Status = workspace.ArtifactApproved
	var notices []string
	loop := reviewReplyLoop(store, &notices)

	loop.routeArtifactReviewReply(context.Background(), map[string]string{}, reply, "art-1-v1-1")

	require.Contains(t, store.cancelled, artifactReviewRequestID("art-1-v1-1"))
	require.Contains(t, notices[0], "already resolved")
}

func TestRouteArtifactReviewReplyCancelKeepsRoundOpen(t *testing.T) {
	store, reply := reviewFixture()
	reply.Cancelled = true
	var notices []string
	loop := reviewReplyLoop(store, &notices)

	loop.routeArtifactReviewReply(context.Background(), map[string]string{}, reply, "art-1-v1-1")

	require.Empty(t, store.resolvedIDs) // the round itself is untouched
	require.Contains(t, store.cancelled, artifactReviewRequestID("art-1-v1-1"))
	require.Contains(t, notices[0], "stays open")
}

// routeReply branches to the review path before any run-signal machinery:
// a reply to an orphan-run row must never build or signal a workflow id.
func TestRouteReplyBranchesToReviewRound(t *testing.T) {
	store, reply := reviewFixture()
	signaled := 0
	loop := reviewReplyLoop(store, nil)
	loop.signal = func(context.Context, string, agent.Approval) error { signaled++; return nil }

	loop.routeReply(context.Background(), map[string]string{}, reply)

	require.Equal(t, 0, signaled)
	require.Equal(t, []string{"art-1-v1-1"}, store.resolvedIDs)
}

// Notifier: a channel-delivered notice persists the orphan-run row with one
// delivery per reviewer, so later replies match the round.
func TestNotifyArtifactReviewCreatesChannelRow(t *testing.T) {
	ws := &fakeArtifactLifecycleStore{
		users: []workspace.User{
			{ID: "u1", Active: true, PreferredChannel: "matrix",
				Channels: []workspace.UserChannel{{Type: "matrix", Address: "@u1:hs", Enabled: true}}},
			{ID: "u2", Active: true}, // web-only: artifacts page is the queue
		},
	}
	var notices []string
	echo := &reviewEchoDriver{sent: &notices}
	notifier := &channelReviewNotifier{
		ws: ws, client: http.DefaultClient,
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		settings: func(context.Context) (agent.ChannelSettings, error) {
			return reviewMatrixSettings(), nil
		},
		drivers: func() []agent.ChannelDriver { return []agent.ChannelDriver{echo} },
	}

	artifact := workspace.Artifact{ID: "art-1", Title: "Требования", Version: 2, Status: workspace.ArtifactInReview}
	review := workspace.ArtifactReview{ID: "art-1-v2-9", ArtifactID: "art-1", ArtifactVersion: 2,
		Reviewers: []string{"user:u1", "user:u2"}, Mode: "all", Status: "open"}

	notifier.NotifyArtifactReviewRequested(context.Background(), artifact, review)

	require.Len(t, notices, 1) // u2 is web-only
	require.Contains(t, notices[0], "approve [comment]")
	require.Len(t, ws.createdRows, 1)
	row := ws.createdRows[0]
	require.Equal(t, artifactReviewRequestID("art-1-v2-9"), row.ID)
	require.Equal(t, row.ID, row.RunID) // orphan: no workflow behind it
	require.Equal(t, "", row.ProjectID)
	require.Equal(t, workspace.HumanStatusDelivered, row.Status)
	require.Equal(t, []string{"approve", "reject"}, row.Options)
	dels := ws.deliveries[row.ID]
	require.Len(t, dels, 1)
	require.Equal(t, "u1", dels[0].UserID)
	require.Equal(t, workspace.HumanDeliveryDelivered, dels[0].Status)
	// The threaded poster's id became the conversation anchor.
	require.Equal(t, "$review-root-1", ws.threadRefs[row.ID])
}

// No channel target at all → no row, no deliveries (web-only round).
func TestNotifyArtifactReviewWebOnlySkipsRow(t *testing.T) {
	ws := &fakeArtifactLifecycleStore{
		users: []workspace.User{{ID: "u2", Active: true}},
	}
	notifier := &channelReviewNotifier{
		ws: ws, client: http.DefaultClient,
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	artifact := workspace.Artifact{ID: "art-1", Title: "T", Version: 1, Status: workspace.ArtifactInReview}
	review := workspace.ArtifactReview{ID: "art-1-v1-2", ArtifactID: "art-1", ArtifactVersion: 1,
		Reviewers: []string{"user:u2"}, Mode: "all", Status: "open"}

	notifier.NotifyArtifactReviewRequested(context.Background(), artifact, review)

	require.Empty(t, ws.createdRows)
}

// Sweep: reminder at one timeout, escalation to the unit head at two.
func TestSweepArtifactReviewsRemindsAndEscalates(t *testing.T) {
	now := time.Now()
	store := &fakeSweepStore{
		reviews: []workspace.StaleArtifactReview{
			{Review: reviewRound("r-fresh", now.Add(-time.Hour)), ArtifactTitle: "Fresh"},
			{Review: reviewRound("r-old", now.Add(-80*time.Hour)), ArtifactTitle: "Old"},
			{Review: reviewRound("r-stale", now.Add(-200*time.Hour)), ArtifactTitle: "Stale", OrgUnitID: "unit-core"},
		},
		orgHeads:    map[string]string{"unit-core": "u-head", "unit-root": "u-root"},
		unitParents: map[string]string{"unit-core": "unit-root"},
		users: []workspace.User{
			{ID: "u-rev", Active: true, PreferredChannel: "matrix",
				Channels: []workspace.UserChannel{{Type: "matrix", Address: "@u-rev:hs", Enabled: true}}},
			{ID: "u-head", Active: false}, // inactive head: escalation walks up
			{ID: "u-root", Active: true, PreferredChannel: "matrix",
				Channels: []workspace.UserChannel{{Type: "matrix", Address: "@u-root:hs", Enabled: true}}},
		},
	}
	var notices []string
	echo := &reviewEchoDriver{sent: &notices}
	notify := &channelReviewNotifier{
		client: http.DefaultClient,
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		settings: func(context.Context) (agent.ChannelSettings, error) {
			return reviewMatrixSettings(), nil
		},
		drivers: func() []agent.ChannelDriver { return []agent.ChannelDriver{echo} },
	}

	timeout := 72 * time.Hour
	sweepArtifactReviews(context.Background(), store, notify, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), now, timeout)

	// Fresh round: untouched. Old round: reminded once. Stale round: straight
	// to the effective head (u-head inactive → u-root), no reminder.
	require.Empty(t, store.reminded["r-fresh"])
	require.NotZero(t, store.reminded["r-old"])
	require.Zero(t, store.escalated["r-old"])
	require.NotZero(t, store.escalated["r-stale"])
	require.Zero(t, store.reminded["r-stale"])
	require.Len(t, notices, 2)
	require.Contains(t, notices[0], "reminder")
	require.Contains(t, notices[1], "escalation")
}

// Already-stamped rounds are skipped on the next pass (the stamp is the
// dedupe, so a twice-daily sweep cannot spam).
func TestSweepArtifactReviewsStampsDedupe(t *testing.T) {
	now := time.Now()
	store := &fakeSweepStore{
		reviews: []workspace.StaleArtifactReview{
			{Review: reviewRound("r-old", now.Add(-80*time.Hour)), ArtifactTitle: "Old"},
		},
		users: []workspace.User{
			{ID: "u-rev", Active: true, PreferredChannel: "matrix",
				Channels: []workspace.UserChannel{{Type: "matrix", Address: "@u-rev:hs", Enabled: true}}},
		},
	}
	var notices []string
	echo := &reviewEchoDriver{sent: &notices}
	notify := &channelReviewNotifier{
		client: http.DefaultClient,
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		settings: func(context.Context) (agent.ChannelSettings, error) {
			return reviewMatrixSettings(), nil
		},
		drivers: func() []agent.ChannelDriver { return []agent.ChannelDriver{echo} },
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	timeout := 72 * time.Hour

	sweepArtifactReviews(context.Background(), store, notify, nil, log, now, timeout)
	sweepArtifactReviews(context.Background(), store, notify, nil, log, now.Add(5*time.Minute), timeout)

	require.Len(t, notices, 1) // one reminder, no repeats
}

func reviewRound(id string, requestedAt time.Time) workspace.ArtifactReview {
	return workspace.ArtifactReview{
		ID: id, ArtifactID: "art-1", ArtifactVersion: 1,
		Reviewers: []string{"user:u-rev"}, Mode: "all", Status: "open",
		RequestedAt: requestedAt, RequestedBy: "kernel-internal",
	}
}

// Escalation routing: bottom-up to the nearest active head, admins as the
// last resort.
func TestArtifactReviewEscalationTargets(t *testing.T) {
	users := []workspace.User{
		{ID: "u-head", Active: false},
		{ID: "u-root", Active: true},
		{ID: "u-admin", Role: "admin", Active: true},
	}
	store := &fakeSweepStore{
		orgHeads:    map[string]string{"unit-core": "u-head", "unit-root": "u-root"},
		unitParents: map[string]string{"unit-core": "unit-root"},
		users:       users,
	}
	ctx := context.Background()
	targets := artifactReviewEscalationTargets(ctx, store, users, "unit-core")
	require.Len(t, targets, 1)
	require.Equal(t, "u-root", targets[0].ID) // nearest active head, bottom-up

	// No unit at all → admins (project_owner semantics).
	targets = artifactReviewEscalationTargets(ctx, store, users, "")
	require.Len(t, targets, 1)
	require.Equal(t, "u-admin", targets[0].ID)
}

func TestArtifactReviewTimeoutFromEnv(t *testing.T) {
	t.Setenv("ARTIFACT_REVIEW_TIMEOUT", "48h")
	require.Equal(t, 48*time.Hour, artifactReviewTimeout())
	t.Setenv("ARTIFACT_REVIEW_TIMEOUT", "0")
	require.Equal(t, time.Duration(0), artifactReviewTimeout())
	t.Setenv("ARTIFACT_REVIEW_TIMEOUT", "garbage")
	require.Equal(t, 72*time.Hour, artifactReviewTimeout())
	t.Setenv("ARTIFACT_REVIEW_TIMEOUT", "")
	require.Equal(t, 72*time.Hour, artifactReviewTimeout())
}

func TestDecisionResponseAndEventName(t *testing.T) {
	require.Equal(t, "approve", decisionResponse(true, ""))
	require.Equal(t, "approve: fix x", decisionResponse(true, "fix x"))
	require.Equal(t, "reject", decisionResponse(false, ""))
	require.Equal(t, "artifact.review.approved", auditEventName(true))
	require.Equal(t, "artifact.review.rejected", auditEventName(false))
}

// fakeSweepStore is the sweep's storage fake: preset rounds, recorded stamps.
type fakeSweepStore struct {
	mu          sync.Mutex
	reviews     []workspace.StaleArtifactReview
	orgHeads    map[string]string
	unitParents map[string]string
	users       []workspace.User

	reminded  map[string]time.Time
	escalated map[string]time.Time
}

func (s *fakeSweepStore) ListOpenArtifactReviews(context.Context) ([]workspace.StaleArtifactReview, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]workspace.StaleArtifactReview, len(s.reviews))
	copy(out, s.reviews)
	for i := range out {
		if t, ok := s.reminded[out[i].Review.ID]; ok {
			stamped := t
			out[i].Review.RemindedAt = &stamped
		}
		if t, ok := s.escalated[out[i].Review.ID]; ok {
			stamped := t
			out[i].Review.EscalatedAt = &stamped
		}
	}
	return out, nil
}

func (s *fakeSweepStore) MarkArtifactReviewReminded(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reminded == nil {
		s.reminded = map[string]time.Time{}
	}
	s.reminded[id] = time.Now()
	return nil
}

func (s *fakeSweepStore) MarkArtifactReviewEscalated(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.escalated == nil {
		s.escalated = map[string]time.Time{}
	}
	s.escalated[id] = time.Now()
	return nil
}

// GetOrgUnit mirrors the production materialized path: Path carries the
// ancestor chain dot-joined, so Ancestors() yields [ancestors..., self].
func (s *fakeSweepStore) GetOrgUnit(_ context.Context, id string) (workspace.OrgUnit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	head, ok := s.orgHeads[id]
	if !ok {
		return workspace.OrgUnit{}, workspace.ErrNotFound
	}
	return workspace.OrgUnit{ID: id, HeadUserID: head, Path: s.unitParents[id]}, nil
}

func (s *fakeSweepStore) ListUsers(context.Context) ([]workspace.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.users, nil
}
