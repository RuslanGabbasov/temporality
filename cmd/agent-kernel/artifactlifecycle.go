package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/observation"
	"github.com/temporality-project/temporality/workspace"
)

// Artifact lifecycle HTTP surface (docs/artifact-lifecycle.md §6–§7).
//
// Origin split at create time: a human create is an explicit act of approval
// (status=approved, origin_type=human, origin_user set); the kernel-internal
// service identity — the one agent tools authenticate as — lands drafts
// (status=draft, origin_type=agent, origin_run set). Drafts enter review via
// submit (202, idempotent per version), and only humans resolve reviews:
// approve/reject refuse kernel-internal so no run can approve its own work
// product (§4 review model).
//
// Events ride the durable outbox so the review queue survives restarts:
// artifact.proposed, artifact.review.requested, artifact.review.approved,
// artifact.review.rejected.

// artifactLifecycleStore is the storage surface this router needs;
// *workspace.Store satisfies it and tests provide fakes.
type artifactLifecycleStore interface {
	GetArtifactRange(ctx context.Context, id string, offset, limit int) (workspace.Artifact, error)
	SubmitArtifactReview(ctx context.Context, artifactID string, reviewers []string, mode, note, requestedBy string) (workspace.ArtifactReview, error)
	ResolveArtifactReview(ctx context.Context, reviewID, reviewer string, approve bool, comment string) (workspace.ArtifactReview, workspace.Artifact, error)
	ListArtifactReviews(ctx context.Context, artifactID string) ([]workspace.ArtifactReview, error)
	CountArtifactSubmits(ctx context.Context, runID string, since time.Time) (int, error)
	RecordAccessAudit(ctx context.Context, actor, action, entityKind, entityID string, details map[string]any) error
}

// artifactLifecycleOutbox is the durable-outbox surface for lifecycle events.
type artifactLifecycleOutbox interface {
	Enqueue(context.Context, observation.Event) error
}

// isKernelInternal reports whether the caller is the kernel service identity
// (the one agent tools use). Reviews must never resolve under it.
func isKernelInternal(r *http.Request) bool {
	principal, ok := controlplane.FromContext(r.Context())
	return ok && principal.Subject == "kernel-internal"
}

// artifactSubmitPerDay bounds how often one run may open review rounds
// (docs/artifact-lifecycle.md §9 quota; env ARTIFACT_SUBMIT_PER_DAY).
func artifactSubmitPerDay() int {
	if v := os.Getenv("ARTIFACT_SUBMIT_PER_DAY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	return 10
}

type artifactSubmitRequest struct {
	Reviewers []string `json:"reviewers"`
	Mode      string   `json:"mode"` // all (default) | any
	Note      string   `json:"note"`
}

type artifactReviewDecisionRequest struct {
	Comment string `json:"comment"`
}

// emitArtifactLifecycleEvent journals one lifecycle event, best-effort: the
// artifact and review rows stay the source of truth.
func emitArtifactLifecycleEvent(ctx context.Context, events artifactLifecycleOutbox, eventType, artifactID string, data map[string]any) {
	if events == nil {
		return
	}
	item := observation.Event{
		Schema:     observation.Schema,
		EventID:    fmt.Sprintf("artifact-lifecycle/%s/%s/%d", artifactID, eventType, time.Now().UnixNano()),
		OccurredAt: time.Now().UTC(),
		Source:     observation.Source{ID: "workspace", Integration: "agent-kernel", Version: "1"},
		Context:    observation.Context{Actor: observation.Actor{ID: "workspace", Type: "system"}},
		Type:       eventType,
		Data:       data,
	}
	if err := events.Enqueue(ctx, item); err != nil {
		slog.Warn("emit artifact lifecycle event", "artifact", artifactID, "type", eventType, "error", err)
	}
}

// RegisterArtifactLifecycleRoutes mounts submit/review endpoints. Create and
// edit stay in main.go; this router owns everything past the draft state.
func RegisterArtifactLifecycleRoutes(mux *http.ServeMux, gate *controlplane.Gate, ws artifactLifecycleStore, events artifactLifecycleOutbox) {
	// Open a review round for the artifact's current version. 202 + the round:
	// the decision arrives later, through approve/reject below. Idempotent per
	// open round — a retried submit returns the same row without a second one.
	mux.HandleFunc("POST /v1/workspace/artifacts/{artifactID}/submit", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		var req artifactSubmitRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		// Same contract as the store (ValidateArtifactReviewers), checked here so
		// a 422 never depends on parsing store error strings.
		if err := workspace.ValidateArtifactReviewers(req.Reviewers); err != nil {
			writeError(w, 422, err)
			return
		}
		if req.Mode == "" {
			req.Mode = "all"
		}
		if req.Mode != "all" && req.Mode != "any" {
			writeError(w, 422, fmt.Errorf("mode must be all or any (got %q)", req.Mode))
			return
		}
		artifactID := r.PathValue("artifactID")
		artifact, err := ws.GetArtifactRange(r.Context(), artifactID, 0, 0)
		if err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				writeError(w, 404, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		requestedBy := requestUserID(r)
		// Agent submits carry the run in origin_run — that is the quota owner.
		// Humans are not rate-limited by the agent budget.
		if isKernelInternal(r) && artifact.OriginRun != "" {
			if n, err := ws.CountArtifactSubmits(r.Context(), artifact.OriginRun, time.Now().UTC().Add(-24*time.Hour)); err == nil && n >= artifactSubmitPerDay() {
				writeError(w, 429, fmt.Errorf("run %s already submitted %d artifacts in the last 24h (limit %d)", artifact.OriginRun, n, artifactSubmitPerDay()))
				return
			}
		}
		review, err := ws.SubmitArtifactReview(r.Context(), artifactID, req.Reviewers, req.Mode, req.Note, requestedBy)
		if err != nil {
			writeError(w, artifactSubmitErrorStatus(err), err)
			return
		}
		_ = ws.RecordAccessAudit(r.Context(), requestedBy, workspace.AuditArtifactReviewRequested, "artifact", artifactID, map[string]any{
			"review_id": review.ID, "reviewers": req.Reviewers, "mode": review.Mode, "version": artifact.Version,
		})
		emitArtifactLifecycleEvent(r.Context(), events, "artifact.review.requested", artifactID, map[string]any{
			"artifact_id": artifactID, "title": artifact.Title, "version": artifact.Version,
			"review_id": review.ID, "reviewers": review.Reviewers, "mode": review.Mode, "note": review.Note, "requested_by": requestedBy,
		})
		writeJSON(w, 202, review)
	})

	// Review rounds of one artifact, newest first: the UI panel reads this.
	mux.HandleFunc("GET /v1/workspace/artifacts/{artifactID}/reviews", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		reviews, err := ws.ListArtifactReviews(r.Context(), r.PathValue("artifactID"))
		if err != nil {
			writeError(w, 500, err)
			return
		}
		if reviews == nil {
			reviews = []workspace.ArtifactReview{}
		}
		writeJSON(w, 200, map[string]any{"reviews": reviews})
	})

	decisionHandler := func(approve bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !gate.Allow(w, r, controlplane.RoleWriter) {
				return
			}
			// §4: only a human resolves reviews. The kernel-internal identity
			// (agent tools) must never approve what a run produced.
			if isKernelInternal(r) {
				writeError(w, 403, errors.New("artifact reviews are resolved by humans only"))
				return
			}
			var req artifactReviewDecisionRequest
			if r.ContentLength != 0 {
				if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
					writeError(w, 400, err)
					return
				}
			}
			review, artifact, err := ws.ResolveArtifactReview(r.Context(), r.PathValue("reviewID"), requestUserID(r), approve, strings.TrimSpace(req.Comment))
			if err != nil {
				writeError(w, artifactReviewErrorStatus(err), err)
				return
			}
			decision := "reject"
			eventType := "artifact.review.rejected"
			auditAction := workspace.AuditArtifactReviewRejected
			if approve {
				decision, eventType, auditAction = "approve", "artifact.review.approved", workspace.AuditArtifactReviewApproved
			}
			_ = ws.RecordAccessAudit(r.Context(), requestUserID(r), auditAction, "artifact", review.ArtifactID, map[string]any{
				"review_id": review.ID, "decision": decision, "version": review.ArtifactVersion, "comment": req.Comment,
			})
			emitArtifactLifecycleEvent(r.Context(), events, eventType, review.ArtifactID, map[string]any{
				"artifact_id": review.ArtifactID, "title": artifact.Title, "version": review.ArtifactVersion,
				"review_id": review.ID, "decision": decision, "resolved_by": requestUserID(r), "comment": req.Comment,
			})
			writeJSON(w, 200, map[string]any{"review": review, "artifact": artifact})
		}
	}
	mux.HandleFunc("POST /v1/workspace/artifacts/{artifactID}/reviews/{reviewID}/approve", decisionHandler(true))
	mux.HandleFunc("POST /v1/workspace/artifacts/{artifactID}/reviews/{reviewID}/reject", decisionHandler(false))
}

// artifactSubmitErrorStatus maps store errors to submit responses: validation
// → 422, missing artifact → 404, lifecycle conflicts → 409.
func artifactSubmitErrorStatus(err error) int {
	switch {
	case errors.Is(err, workspace.ErrNotFound):
		return 404
	case errors.Is(err, workspace.ErrArtifactLocked):
		return 409
	case strings.Contains(err.Error(), "reviewers") || strings.Contains(err.Error(), "mode must be"):
		return 422
	default:
		return 500
	}
}

// artifactReviewErrorStatus maps store errors to decision responses: double
// resolution and non-reviewer decisions are 409 conflicts.
func artifactReviewErrorStatus(err error) int {
	switch {
	case errors.Is(err, workspace.ErrNotFound):
		return 404
	case errors.Is(err, workspace.ErrConflict), errors.Is(err, workspace.ErrArtifactLocked):
		return 409
	default:
		return 500
	}
}
