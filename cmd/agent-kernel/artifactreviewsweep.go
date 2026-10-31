package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/temporality-project/temporality/workspace"
)

// Artifact review reminder sweep (docs/artifact-lifecycle.md §6 Phase 2).
// Open rounds that sit past the deadline (env ARTIFACT_REVIEW_TIMEOUT,
// default 72h) get one reminder to their reviewers' channels; a round that
// stays silent for another full timeout escalates to the artifact org unit's
// effective head — bottom-up, the nearest ancestor with a head — or to the
// installation admins when no unit/head applies (project_owner semantics:
// projects carry no owner yet). Each step fires once per round — the
// reminded_at/escalated_at stamps double as the dedupe — and 0 disables the
// sweep entirely.

// artifactReviewSweepStore is the storage surface the sweep needs;
// *workspace.Store satisfies it, tests provide fakes.
type artifactReviewSweepStore interface {
	ListOpenArtifactReviews(ctx context.Context) ([]workspace.StaleArtifactReview, error)
	MarkArtifactReviewReminded(ctx context.Context, id string) error
	MarkArtifactReviewEscalated(ctx context.Context, id string) error
	GetOrgUnit(ctx context.Context, id string) (workspace.OrgUnit, error)
	ListUsers(ctx context.Context) ([]workspace.User, error)
}

// artifactReviewTimeout parses ARTIFACT_REVIEW_TIMEOUT as a Go duration
// ("72h", "30m"); unparsable values fall back to the 72h default, "0"
// disables the sweep.
func artifactReviewTimeout() time.Duration {
	if v := strings.TrimSpace(os.Getenv("ARTIFACT_REVIEW_TIMEOUT")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= 0 {
			return d
		}
	}
	return 72 * time.Hour
}

// runArtifactReviewSweep is the sweep loop: a short tick, real work only when
// there is something past its deadline. Same shape as the quality schedule.
func runArtifactReviewSweep(ctx context.Context, store artifactReviewSweepStore, notify *channelReviewNotifier, events artifactLifecycleOutbox, log *slog.Logger) {
	timeout := artifactReviewTimeout()
	if timeout <= 0 {
		return
	}
	const tick = 5 * time.Minute
	timer := time.NewTimer(tick)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		sweepArtifactReviews(ctx, store, notify, events, log, time.Now(), timeout)
	}
}

// sweepArtifactReviews is one pass over the open rounds. Escalation is
// checked before the reminder: a round already twice past its deadline
// (kernel down, fresh deploy) goes straight to the head instead of first
// pretending a reminder is enough.
func sweepArtifactReviews(ctx context.Context, store artifactReviewSweepStore, notify *channelReviewNotifier, events artifactLifecycleOutbox, log *slog.Logger, now time.Time, timeout time.Duration) {
	stale, err := store.ListOpenArtifactReviews(ctx)
	if err != nil {
		log.Warn("artifact review sweep list", "error", err)
		return
	}
	if len(stale) == 0 {
		return
	}
	users, err := store.ListUsers(ctx)
	if err != nil {
		log.Warn("artifact review sweep users", "error", err)
		return
	}
	for _, round := range stale {
		if ctx.Err() != nil {
			return
		}
		age := now.Sub(round.Review.RequestedAt)
		switch {
		case age >= 2*timeout && round.Review.EscalatedAt == nil:
			targets := artifactReviewEscalationTargets(ctx, store, users, round.OrgUnitID)
			body := artifactReviewEscalationText(round, timeout, notify.currentSettings(ctx).UIURL)
			if notify.deliverReviewText(ctx, targets, body) == 0 {
				continue // nobody reachable — retry next tick, no stamp
			}
			if err := store.MarkArtifactReviewEscalated(ctx, round.Review.ID); err != nil {
				log.Warn("artifact review escalation stamp", "review", round.Review.ID, "error", err)
				continue
			}
			emitArtifactLifecycleEvent(ctx, events, "artifact.review.escalated", round.Review.ArtifactID, map[string]any{
				"artifact_id": round.Review.ArtifactID, "title": round.ArtifactTitle,
				"version": round.Review.ArtifactVersion, "review_id": round.Review.ID,
				"age_hours": int(age.Hours()),
			})
			log.Info("artifact review escalated", "review", round.Review.ID, "artifact", round.Review.ArtifactID, "age", age.Round(time.Hour).String())
		case age >= timeout && round.Review.RemindedAt == nil:
			targets := expandArtifactReviewers(round.Review.Reviewers, users, nil)
			body := artifactReviewReminderText(round, timeout, notify.currentSettings(ctx).UIURL)
			if notify.deliverReviewText(ctx, targets, body) == 0 {
				continue
			}
			if err := store.MarkArtifactReviewReminded(ctx, round.Review.ID); err != nil {
				log.Warn("artifact review reminder stamp", "review", round.Review.ID, "error", err)
				continue
			}
			emitArtifactLifecycleEvent(ctx, events, "artifact.review.reminded", round.Review.ArtifactID, map[string]any{
				"artifact_id": round.Review.ArtifactID, "title": round.ArtifactTitle,
				"version": round.Review.ArtifactVersion, "review_id": round.Review.ID,
				"age_hours": int(age.Hours()),
			})
			log.Info("artifact review reminded", "review", round.Review.ID, "artifact", round.Review.ArtifactID, "age", age.Round(time.Hour).String())
		}
	}
}

// artifactReviewEscalationTargets resolves the escalation addressees: the
// artifact org unit's effective head — bottom-up, the nearest ancestor with
// a head assigned and active — else the installation admins. An unreachable
// head (inactive account) falls through to the next ancestor, then admins.
func artifactReviewEscalationTargets(ctx context.Context, store artifactReviewSweepStore, users []workspace.User, orgUnitID string) []workspace.User {
	if orgUnitID != "" {
		chain, err := store.GetOrgUnit(ctx, orgUnitID)
		if err == nil {
			ids := chain.Ancestors() // oldest first
			for i := len(ids) - 1; i >= 0; i-- {
				unit, err := store.GetOrgUnit(ctx, ids[i])
				if err != nil || unit.HeadUserID == "" {
					continue
				}
				for _, u := range users {
					if u.ID == unit.HeadUserID && u.Active {
						return []workspace.User{u}
					}
				}
			}
		}
	}
	return expandArtifactReviewers([]string{"project_owner"}, users, nil)
}

// artifactReviewReminderText is the reviewer-facing nudge: same facts as the
// original notice, one extra line saying how long the round has waited.
func artifactReviewReminderText(round workspace.StaleArtifactReview, timeout time.Duration, uiURL string) string {
	var b strings.Builder
	b.WriteString("Temporality: artifact review reminder.\n")
	waited := time.Since(round.Review.RequestedAt).Round(time.Hour)
	fmt.Fprintf(&b, "%q (v%d) still awaits your decision — open for %s (deadline %s).",
		round.ArtifactTitle, round.Review.ArtifactVersion, waited, timeout)
	if uiURL != "" {
		fmt.Fprintf(&b, "\nDecide in the UI or reply in this chat: %s/artifacts", strings.TrimRight(uiURL, "/"))
	} else {
		b.WriteString("\nDecide in the Temporality UI (Artifacts) or reply in this chat.")
	}
	return b.String()
}

// artifactReviewEscalationText is the head/admin-facing escalation: what has
// been waiting, for how long, and who was asked first.
func artifactReviewEscalationText(round workspace.StaleArtifactReview, timeout time.Duration, uiURL string) string {
	var b strings.Builder
	b.WriteString("Temporality: artifact review escalation.\n")
	waited := time.Since(round.Review.RequestedAt).Round(time.Hour)
	fmt.Fprintf(&b, "%q (v%d) has been in review for %s with no decision — reviewers (%s) were reminded after %s and stayed silent.",
		round.ArtifactTitle, round.Review.ArtifactVersion, waited,
		strings.Join(round.Review.Reviewers, ", "), timeout)
	if round.Review.RequestedBy != "" {
		fmt.Fprintf(&b, "\nRequested by %s.", round.Review.RequestedBy)
	}
	if uiURL != "" {
		fmt.Fprintf(&b, "\nReview: %s/artifacts", strings.TrimRight(uiURL, "/"))
	} else {
		b.WriteString("\nOpen the Temporality UI (Artifacts) to decide.")
	}
	return b.String()
}
