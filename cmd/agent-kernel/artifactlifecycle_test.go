package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/observation"
	"github.com/temporality-project/temporality/workspace"
)

// fakeArtifactLifecycleStore satisfies the lifecycle router's storage surface
// without a database; the artifact and review fixtures are preset per test.
type fakeArtifactLifecycleStore struct {
	mu sync.Mutex

	artifact workspace.Artifact
	review   workspace.ArtifactReview
	reviews  []workspace.ArtifactReview

	submitCount int
	resolveErr  error
	submitErr   error
	countErr    error
	audits      []storedAudit
}

func (f *fakeArtifactLifecycleStore) GetArtifactRange(_ context.Context, id string, _, _ int) (workspace.Artifact, error) {
	if id != f.artifact.ID {
		return workspace.Artifact{}, workspace.ErrNotFound
	}
	return f.artifact, nil
}

func (f *fakeArtifactLifecycleStore) SubmitArtifactReview(_ context.Context, artifactID string, reviewers []string, mode, note, requestedBy string) (workspace.ArtifactReview, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.submitCount++
	if f.submitErr != nil {
		return workspace.ArtifactReview{}, f.submitErr
	}
	f.review = workspace.ArtifactReview{
		ID: "round-1", ArtifactID: artifactID, ArtifactVersion: f.artifact.Version,
		Reviewers: reviewers, Mode: mode, Status: "open", Note: note, RequestedBy: requestedBy,
	}
	return f.review, nil
}

func (f *fakeArtifactLifecycleStore) ResolveArtifactReview(_ context.Context, reviewID, reviewer string, approve bool, comment string) (workspace.ArtifactReview, workspace.Artifact, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.resolveErr != nil {
		return workspace.ArtifactReview{}, workspace.Artifact{}, f.resolveErr
	}
	decision := "reject"
	status := workspace.ArtifactRejected
	if approve {
		decision, status = "approve", workspace.ArtifactApproved
	}
	f.review.Status = status
	f.review.Decisions = map[string]workspace.ReviewDecision{
		reviewer: {Decision: decision, Comment: comment, At: time.Now().UTC()},
	}
	f.artifact.Status = status
	return f.review, f.artifact, nil
}

func (f *fakeArtifactLifecycleStore) ListArtifactReviews(_ context.Context, _ string) ([]workspace.ArtifactReview, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reviews, nil
}

func (f *fakeArtifactLifecycleStore) CountArtifactSubmits(_ context.Context, _ string, _ time.Time) (int, error) {
	return f.submitCount, f.countErr
}

func (f *fakeArtifactLifecycleStore) RecordAccessAudit(_ context.Context, actor, action, entityKind, entityID string, details map[string]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.audits = append(f.audits, storedAudit{actor: actor, action: action, entityKind: entityKind, entityID: entityID, details: details})
	return nil
}

type captureArtifactOutbox struct {
	mu     sync.Mutex
	events []observation.Event
}

func (c *captureArtifactOutbox) Enqueue(_ context.Context, event observation.Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, event)
	return nil
}

func (c *captureArtifactOutbox) byType(eventType string) []observation.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	var matched []observation.Event
	for _, event := range c.events {
		if event.Type == eventType {
			matched = append(matched, event)
		}
	}
	return matched
}

// newArtifactLifecycleServer mounts the lifecycle router the way main wires
// it: real RBAC gate (writer, reader and the kernel service identity), fake
// store and outbox.
func newArtifactLifecycleServer(t *testing.T, store *fakeArtifactLifecycleStore, outbox *captureArtifactOutbox) *httptest.Server {
	t.Helper()
	gate, err := controlplane.NewGate("readertoken:reader:reader:*\nwritertoken:writer:writer:*\ninternaltoken:kernel-internal:operator:*")
	require.NoError(t, err)
	mux := http.NewServeMux()
	RegisterArtifactLifecycleRoutes(mux, gate, store, outbox)
	return httptest.NewServer(gate.Authenticate(mux))
}

func artifactPost(t *testing.T, server *httptest.Server, token, path, body string) (int, string) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, server.URL+path, strings.NewReader(body))
	require.NoError(t, err)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := server.Client().Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	return response.StatusCode, string(raw)
}

func defaultArtifactFixture() *fakeArtifactLifecycleStore {
	return &fakeArtifactLifecycleStore{
		artifact: workspace.Artifact{ID: "req-spec", Title: "Requirements spec", Status: workspace.ArtifactDraft, Version: 2, OriginRun: "run-42"},
	}
}

// A kernel submit opens a round: 202 with the round payload, an audit record
// and the artifact.review.requested event carrying the reviewers.
func TestArtifactSubmitAnswers202AndEmits(t *testing.T) {
	store := defaultArtifactFixture()
	outbox := &captureArtifactOutbox{}
	server := newArtifactLifecycleServer(t, store, outbox)
	defer server.Close()

	status, body := artifactPost(t, server, "internaltoken", "/v1/workspace/artifacts/req-spec/submit",
		`{"reviewers":["user:ivanov","project_owner"],"note":"ready"}`)
	require.Equal(t, 202, status)
	require.Contains(t, body, `"id":"round-1"`)
	requested := outbox.byType("artifact.review.requested")
	require.Len(t, requested, 1)
	require.Equal(t, "req-spec", requested[0].Data["artifact_id"])
	require.EqualValues(t, 2, requested[0].Data["version"])
	require.Equal(t, "kernel-internal", requested[0].Data["requested_by"])
	require.Len(t, store.audits, 1)
	require.Equal(t, workspace.AuditArtifactReviewRequested, store.audits[0].action)
}

// The submit quota is enforced against the run recorded in origin_run: past
// ARTIFACT_SUBMIT_PER_DAY the answer is 429, not a silent drop.
func TestArtifactSubmitQuotaIs429(t *testing.T) {
	store := defaultArtifactFixture()
	store.submitCount = 10
	outbox := &captureArtifactOutbox{}
	server := newArtifactLifecycleServer(t, store, outbox)
	defer server.Close()

	status, body := artifactPost(t, server, "internaltoken", "/v1/workspace/artifacts/req-spec/submit",
		`{"reviewers":["project_owner"]}`)
	require.Equal(t, 429, status)
	require.Contains(t, body, "run-42")
	require.Zero(t, len(outbox.events))
}

// Only a human resolves reviews: the kernel service identity gets 403 even
// though its role (operator) outranks writer.
func TestArtifactDecisionRefusesKernelInternal(t *testing.T) {
	store := defaultArtifactFixture()
	outbox := &captureArtifactOutbox{}
	server := newArtifactLifecycleServer(t, store, outbox)
	defer server.Close()

	status, _ := artifactPost(t, server, "internaltoken", "/v1/workspace/artifacts/req-spec/reviews/round-1/approve", `{}`)
	require.Equal(t, 403, status)
	require.Zero(t, len(outbox.events))
}

// A human approve closes the round, moves the artifact to approved, audits
// the decision and emits artifact.review.approved.
func TestArtifactApproveClosesRound(t *testing.T) {
	store := defaultArtifactFixture()
	store.review = workspace.ArtifactReview{ID: "round-1", ArtifactID: "req-spec", ArtifactVersion: 2, Reviewers: []string{"writer"}, Mode: "all", Status: "open"}
	outbox := &captureArtifactOutbox{}
	server := newArtifactLifecycleServer(t, store, outbox)
	defer server.Close()

	status, body := artifactPost(t, server, "writertoken", "/v1/workspace/artifacts/req-spec/reviews/round-1/approve",
		`{"comment":"lgtm"}`)
	require.Equal(t, 200, status)
	require.Contains(t, body, `"status":"approved"`)
	approved := outbox.byType("artifact.review.approved")
	require.Len(t, approved, 1)
	require.Equal(t, "writer", approved[0].Data["resolved_by"])
	require.Len(t, store.audits, 1)
	require.Equal(t, workspace.AuditArtifactReviewApproved, store.audits[0].action)
}

// A reader may watch the queue but never touch it: 403 on decisions.
func TestArtifactDecisionReaderForbidden(t *testing.T) {
	store := defaultArtifactFixture()
	outbox := &captureArtifactOutbox{}
	server := newArtifactLifecycleServer(t, store, outbox)
	defer server.Close()

	status, _ := artifactPost(t, server, "readertoken", "/v1/workspace/artifacts/req-spec/reviews/round-1/approve", `{}`)
	require.Equal(t, 403, status)
}

// Double resolution and decisions from non-reviewers are 409 conflicts.
func TestArtifactDoubleResolutionIs409(t *testing.T) {
	store := defaultArtifactFixture()
	store.resolveErr = fmt.Errorf("%w: review is already approved", workspace.ErrConflict)
	outbox := &captureArtifactOutbox{}
	server := newArtifactLifecycleServer(t, store, outbox)
	defer server.Close()

	status, body := artifactPost(t, server, "writertoken", "/v1/workspace/artifacts/req-spec/reviews/round-1/reject", `{}`)
	require.Equal(t, 409, status)
	require.Contains(t, body, "already approved")
}

// Validation errors (empty reviewers, bad mode) answer 422, a missing
// artifact 404 — the UI can tell a user mistake from a server fault.
func TestArtifactSubmitValidation(t *testing.T) {
	store := defaultArtifactFixture()
	outbox := &captureArtifactOutbox{}
	server := newArtifactLifecycleServer(t, store, outbox)
	defer server.Close()

	status, body := artifactPost(t, server, "writertoken", "/v1/workspace/artifacts/req-spec/submit", `{"reviewers":[]}`)
	require.Equal(t, 422, status)
	require.Contains(t, body, "reviewer")

	status, body = artifactPost(t, server, "writertoken", "/v1/workspace/artifacts/missing/submit", `{"reviewers":["project_owner"]}`)
	require.Equal(t, 404, status)

	status, body = artifactPost(t, server, "writertoken", "/v1/workspace/artifacts/req-spec/submit", `{"reviewers":["project_owner"],"mode":"majority"}`)
	require.Equal(t, 422, status)
	require.Contains(t, body, "mode")
}

// A locked (in_review) artifact refuses submit with 409 and the store's
// explanation of the lifecycle state.
func TestArtifactSubmitLockedIs409(t *testing.T) {
	store := defaultArtifactFixture()
	store.submitErr = fmt.Errorf("%w: artifact is in_review; only draft or rejected versions enter review", workspace.ErrArtifactLocked)
	outbox := &captureArtifactOutbox{}
	server := newArtifactLifecycleServer(t, store, outbox)
	defer server.Close()

	status, body := artifactPost(t, server, "writertoken", "/v1/workspace/artifacts/req-spec/submit", `{"reviewers":["project_owner"]}`)
	require.Equal(t, 409, status)
	require.Contains(t, body, "in_review")
	require.True(t, errors.Is(store.submitErr, workspace.ErrArtifactLocked))
}
