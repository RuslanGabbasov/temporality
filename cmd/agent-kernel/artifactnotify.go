package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/workspace"
)

// Reviewer notifications (docs/artifact-lifecycle.md §6). Submit fans the
// request out to every expanded reviewer over their channel — same delivery
// policy as ask_human (preferred channel, else the first enabled and
// configured one, else the web inbox which is a no-op here: the review queue
// lives in the UI). Best-effort: failures are logged, never surfaced to the
// submit response.
//
// Phase 2 (docs/artifact-lifecycle.md §12): a channel-delivered notice also
// creates a human_request row — the orphan-run generalization. RunID
// `artifact-review/<round>` matches no workflow; the channel inbound loop's
// reply router recognizes it and resolves the review round instead of
// signaling a run (artifactreviewreply.go). The row is what lets a reply in
// the reviewer's chat match the round: same matching, thread pinning and
// answer bookkeeping as ask_human, with the review row staying the source of
// truth. Web-only reviewers get no row — the artifacts page is their queue.

// artifactReviewRequestPrefix marks the orphan-run human request rows that
// carry review rounds into the channel machinery.
const artifactReviewRequestPrefix = "artifact-review/"

// artifactReviewRequestID is the human_request id/run_id of a review round's
// channel row. Deterministic: the round's own id, so the HTTP decision route
// can close the row when the round resolves in the UI.
func artifactReviewRequestID(reviewID string) string {
	return artifactReviewRequestPrefix + reviewID
}

// artifactReviewFromRequestRun reports the review round behind an orphan-run
// human request row (ok=false for every ordinary run-scoped ask).
func artifactReviewFromRequestRun(runID string) (reviewID string, ok bool) {
	if !strings.HasPrefix(runID, artifactReviewRequestPrefix) {
		return "", false
	}
	reviewID = strings.TrimPrefix(runID, artifactReviewRequestPrefix)
	return reviewID, reviewID != ""
}

// channelReviewNotifier delivers review notices through the channel registry.
type channelReviewNotifier struct {
	ws       artifactLifecycleStore                               // ListUsers for reviewer expansion
	settings func(context.Context) (agent.ChannelSettings, error) // database overlay, nil = env only
	client   *http.Client
	log      *slog.Logger
	// drivers overrides the channel registry (tests); nil = agent.ChannelDrivers.
	drivers func() []agent.ChannelDriver
}

func (n *channelReviewNotifier) driverRegistry() []agent.ChannelDriver {
	if n.drivers != nil {
		return n.drivers()
	}
	return agent.ChannelDrivers()
}

// reviewNoticeDelivery is the per-reviewer outcome of one notice fan-out.
type reviewNoticeDelivery struct {
	userID   string
	channel  string
	status   string
	postedID string // thread root id on thread-capable transports
}

func (n *channelReviewNotifier) NotifyArtifactReviewRequested(ctx context.Context, artifact workspace.Artifact, review workspace.ArtifactReview) {
	users, err := n.ws.ListUsers(ctx)
	if err != nil {
		n.log.Warn("artifact review notice: load users", "artifact", artifact.ID, "error", err)
		return
	}
	targets := expandArtifactReviewers(review.Reviewers, users)
	if len(targets) == 0 {
		return
	}
	settings := n.currentSettings(ctx)
	body := artifactReviewNoticeText(artifact, review, settings.UIURL)

	// Send first, persist after: a reply cannot arrive before the question
	// was posted, so the row only becomes matchable once a send went through.
	// A crash in between costs an unanswered-looking notice — the reviewer's
	// reply falls through to intake and the round stays decidable in the UI.
	drivers := n.driverRegistry()
	byType := make(map[string]agent.ChannelDriver, len(drivers))
	for _, driver := range drivers {
		byType[driver.Type()] = driver
	}
	sent := make([]reviewNoticeDelivery, 0, len(targets))
	for _, target := range targets {
		channel, ok := reviewerDeliveryChannel(target, settings)
		if !ok {
			continue // web inbox: the artifacts page is the queue
		}
		driver, ok := byType[channel.Type]
		if !ok {
			continue
		}
		noticeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		// Body feeds text transports, Question the structured ones (webhook
		// envelope) — same shape as ask_human delivery.
		msg := agent.OutboundText{Body: body, Question: body}
		var postedID string
		var sendErr error
		if poster, ok := driver.(agent.ThreadedTextPoster); ok && driver.Spec().Capabilities.Threads {
			// Top-level post becomes the thread root: its id is the ref the
			// reviewer's threaded replies carry, so they match this exact
			// round (docs/channels.md §9) even with several rounds pending.
			postedID, sendErr = poster.SendTextThreaded(noticeCtx, n.client, settings.Channels[channel.Type], channel.Address, msg)
		} else {
			sendErr = driver.SendText(noticeCtx, n.client, settings.Channels[channel.Type], channel.Address, msg)
		}
		cancel()
		row := reviewNoticeDelivery{userID: target.ID, channel: channel.Type, postedID: postedID, status: workspace.HumanDeliveryDelivered}
		if sendErr != nil {
			n.log.Warn("artifact review notice", "channel", channel.Type, "user", target.ID, "artifact", artifact.ID, "error", sendErr)
			row.status = workspace.HumanDeliveryFailed
		}
		sent = append(sent, row)
	}

	delivered := make([]reviewNoticeDelivery, 0, len(sent))
	for _, d := range sent {
		if d.status == workspace.HumanDeliveryDelivered {
			delivered = append(delivered, d)
		}
	}
	if len(delivered) == 0 {
		return // web-only round or every transport failed: no row to match on
	}
	requestID := artifactReviewRequestID(review.ID)
	row := &workspace.HumanRequest{
		ID:        requestID,
		RunID:     requestID, // orphan: matches no workflow, routes to the review
		Recipient: strings.Join(review.Reviewers, ","),
		Question:  body,
		Options:   []string{"approve", "reject"},
		Status:    workspace.HumanStatusDelivered,
		Channel:   delivered[0].channel,
	}
	if err := n.ws.CreateHumanRequest(ctx, row); err != nil {
		n.log.Warn("artifact review row", "review", review.ID, "error", err)
		return // no row — no channel matching; the UI path still decides
	}
	if threadRef := firstPostedThread(delivered); threadRef != "" {
		if err := n.ws.SetHumanRequestThreadRef(ctx, requestID, threadRef); err != nil {
			n.log.Warn("artifact review thread pin", "review", review.ID, "error", err)
		}
	}
	deliveries := make([]workspace.HumanRequestDelivery, 0, len(sent))
	for _, d := range sent {
		deliveries = append(deliveries, workspace.HumanRequestDelivery{
			RequestID: requestID, UserID: d.userID, Channel: d.channel, Status: d.status,
		})
	}
	if err := n.ws.AddHumanRequestDeliveries(ctx, requestID, deliveries); err != nil {
		n.log.Warn("artifact review deliveries", "review", review.ID, "error", err)
	}
}

// deliverReviewText sends one text to every target's delivery channel
// (docs/artifact-lifecycle.md §6 Phase 2 — reminder/escalation sweep).
// Best-effort; returns how many sends went through so the caller stamps the
// sweep bookkeeping only when at least one reviewer actually saw it.
func (n *channelReviewNotifier) deliverReviewText(ctx context.Context, targets []workspace.User, body string) int {
	if len(targets) == 0 {
		return 0
	}
	settings := n.currentSettings(ctx)
	drivers := n.driverRegistry()
	byType := make(map[string]agent.ChannelDriver, len(drivers))
	for _, driver := range drivers {
		byType[driver.Type()] = driver
	}
	delivered := 0
	for _, target := range targets {
		channel, ok := reviewerDeliveryChannel(target, settings)
		if !ok {
			continue
		}
		driver, ok := byType[channel.Type]
		if !ok {
			continue
		}
		sendCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		err := driver.SendText(sendCtx, n.client, settings.Channels[channel.Type], channel.Address,
			agent.OutboundText{Body: body, Question: body})
		cancel()
		if err != nil {
			n.log.Warn("artifact review text", "channel", channel.Type, "user", target.ID, "error", err)
			continue
		}
		delivered++
	}
	return delivered
}

// currentSettings overlays the stored channel settings on the env defaults —
// the same resolution ask_human delivery uses.
func (n *channelReviewNotifier) currentSettings(ctx context.Context) agent.ChannelSettings {
	settings := agent.ChannelSettingsFromEnv()
	if n.settings != nil {
		if stored, err := n.settings(ctx); err == nil {
			settings = stored.OverlayEnv()
		}
	}
	return settings
}

// firstPostedThread returns the first successfully posted thread id: the
// review round's conversation anchor. All copies share one round, so the
// first root wins — the rest are leaves of the same conversation at most.
func firstPostedThread(delivered []reviewNoticeDelivery) string {
	for _, d := range delivered {
		if d.postedID != "" {
			return d.postedID
		}
	}
	return ""
}

// expandArtifactReviewers resolves reviewer entries to concrete active users
// (docs/artifact-lifecycle.md §6): org: → unit members, role: → role holders,
// project_owner → installation admins, user:/bare → the named user.
// Deduplicated by id, deterministic order.
func expandArtifactReviewers(entries []string, users []workspace.User) []workspace.User {
	matched := make([]workspace.User, 0, len(users))
	seen := map[string]bool{}
	add := func(u workspace.User) {
		if u.Active && !seen[u.ID] {
			seen[u.ID] = true
			matched = append(matched, u)
		}
	}
	for _, entry := range entries {
		switch {
		case strings.HasPrefix(entry, "org:"):
			unit := strings.TrimSpace(strings.TrimPrefix(entry, "org:"))
			for _, u := range users {
				if u.OrgUnitID == unit {
					add(u)
				}
			}
		case strings.HasPrefix(entry, "role:"):
			role := strings.TrimSpace(strings.TrimPrefix(entry, "role:"))
			for _, u := range users {
				if u.Role == role {
					add(u)
				}
			}
		case entry == "project_owner":
			for _, u := range users {
				if u.Role == "admin" {
					add(u)
				}
			}
		default:
			name := strings.TrimSpace(strings.TrimPrefix(entry, "user:"))
			if name == "" {
				continue
			}
			for _, u := range users {
				if u.ID == name || u.Name == name {
					add(u)
				}
			}
		}
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].ID < matched[j].ID })
	return matched
}

// reviewerDeliveryChannel applies the ask_human policy (pickChannel): the
// preferred channel when enabled and configured, else the first enabled and
// configured one, else nothing (web inbox).
func reviewerDeliveryChannel(u workspace.User, settings agent.ChannelSettings) (workspace.UserChannel, bool) {
	first := workspace.UserChannel{}
	for _, channel := range u.Channels {
		if !channel.Enabled || channel.Address == "" || !settings.Configured(channel.Type) {
			continue
		}
		if u.PreferredChannel == channel.Type {
			return channel, true
		}
		if first.Type == "" {
			first = channel
		}
	}
	if first.Type != "" {
		return first, true
	}
	return workspace.UserChannel{}, false
}

// artifactReviewNoticeText renders the channel-neutral notice; same tone as
// the intake room notices, plain text for every transport. Decisions are
// accepted right here (Phase 2) or on the artifacts page.
func artifactReviewNoticeText(a workspace.Artifact, r workspace.ArtifactReview, uiURL string) string {
	var b strings.Builder
	b.WriteString("Temporality: artifact review requested.\n")
	fmt.Fprintf(&b, "%q (v%d) awaits your decision — mode %s.", a.Title, a.Version, r.Mode)
	if r.Note != "" {
		fmt.Fprintf(&b, "\nNote: %s", r.Note)
	}
	b.WriteString("\nAnswer here: “approve [comment]” or “reject [comment]”.")
	if uiURL != "" {
		fmt.Fprintf(&b, "\nOr decide in the UI: %s/artifacts", strings.TrimRight(uiURL, "/"))
	} else {
		b.WriteString("\nOr decide in the Temporality UI (Artifacts).")
	}
	return b.String()
}
