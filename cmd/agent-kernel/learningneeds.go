package main

// Learning needs pass (docs/ai-guided-onboarding-and-learning.md, stage P3):
// a periodic, low-frequency supervisor pass that compares the FACTUAL
// configuration against the users' capability mastery and records
// suggestions into the learning center. Never a notification flood: one
// suggestion slot per (user, feature) with a status, refreshed only when the
// picture changes (§9.2 frequency capping, §5.4 no repeats without new
// circumstances).

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/temporality-project/temporality/workspace"
)

// learningNeedsPassCap bounds one pass — the suggestions persist, so a slow
// drip beats a burst.
const learningNeedsPassCap = 20

// startLearningNeedsLoop runs the learning needs pass on its own cadence.
// Deliberately separate from the supervisor triage loop (different rhythm,
// §9.2: learning suggestions are low-frequency, hours not minutes).
func startLearningNeedsLoop(ctx context.Context, log *slog.Logger, ws *workspace.Store) {
	interval := 45 * time.Minute
	if raw := strings.TrimSpace(os.Getenv("KERNEL_LEARNING_NEEDS_INTERVAL")); raw != "" {
		if d, perr := time.ParseDuration(raw); perr == nil && d > 0 {
			interval = d
		}
	}
	go func() {
		// First pass after boot: let the store settle.
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Minute):
		}
		for {
			runLearningNeedsPass(ctx, log, ws)
			select {
			case <-ctx.Done():
				return
			case <-time.After(interval):
			}
		}
	}()
}

func runLearningNeedsPass(ctx context.Context, log *slog.Logger, ws *workspace.Store) {
	appCtx := learningAppContext(ctx, ws, "", nil)

	// Which capabilities actually apply to this installation right now?
	var applicable []LearningCapability
	for _, cap := range learningCapabilities {
		if cap.AppliesTo == nil || cap.AppliesTo(appCtx) {
			applicable = append(applicable, cap)
		}
	}
	if len(applicable) == 0 {
		return
	}

	users, err := ws.ListUsers(ctx)
	if err != nil {
		log.Warn("learning needs: list users", "error", err.Error())
		return
	}
	recorded := 0
	for _, user := range users {
		if recorded >= learningNeedsPassCap {
			break
		}
		if !user.Active {
			continue
		}
		progress, err := ws.ListLearningProgress(ctx, user.ID)
		if err != nil {
			continue
		}
		mastery := map[string]string{}
		for _, p := range progress {
			mastery[p.FeatureID] = p.Mastery
		}
		for _, cap := range applicable {
			if m := mastery[cap.ID]; m == "practiced" || m == "verified" {
				continue // mastered — nothing to suggest (§18 scenario F)
			}
			status, detail := "unknown", ""
			if cap.Mastered != nil {
				status, detail = cap.Mastered(appCtx)
			}
			if status == "practiced" || status == "verified" {
				// The organization already uses it — record the evidence, do
				// not suggest a lesson for a fact the system can see (§18 F).
				_ = ws.UpsertLearningProgress(ctx, &workspace.LearningProgress{
					UserID: user.ID, FeatureID: cap.ID, Mastery: status, Source: "verification", Detail: detail,
				})
				continue
			}
			if err := ws.UpsertLearningSuggestion(ctx, workspace.LearningSuggestion{
				UserID: user.ID, FeatureID: cap.ID, Status: "suggested",
			}); err != nil {
				continue
			}
			recorded++
		}
	}
	if recorded > 0 {
		log.Info("learning needs: suggestions recorded", "count", recorded)
	}
}
