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

// Reviewer notifications (docs/artifact-lifecycle.md §6 Phase 1). Submit fans
// the request out to every expanded reviewer over their channel — same
// delivery policy as ask_human (preferred channel, else the first enabled and
// configured one, else the web inbox which is a no-op here: the review queue
// lives in the UI). Plain text with a link, best-effort: failures are logged,
// never surfaced to the submit response.

// channelReviewNotifier delivers review notices through the channel registry.
type channelReviewNotifier struct {
	ws       artifactLifecycleStore                               // ListUsers for reviewer expansion
	settings func(context.Context) (agent.ChannelSettings, error) // database overlay, nil = env only
	client   *http.Client
	log      *slog.Logger
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
	settings := agent.ChannelSettingsFromEnv()
	if n.settings != nil {
		if stored, err := n.settings(ctx); err == nil {
			settings = stored.OverlayEnv()
		}
	}
	body := artifactReviewNoticeText(artifact, review, settings.UIURL)
	drivers := agent.ChannelDrivers()
	byType := make(map[string]agent.ChannelDriver, len(drivers))
	for _, driver := range drivers {
		byType[driver.Type()] = driver
	}
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
		err := driver.SendText(noticeCtx, n.client, settings.Channels[channel.Type], channel.Address,
			agent.OutboundText{Body: body, Question: body})
		cancel()
		if err != nil {
			n.log.Warn("artifact review notice", "channel", channel.Type, "user", target.ID, "artifact", artifact.ID, "error", err)
		}
	}
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
// the intake room notices, plain text for every transport.
func artifactReviewNoticeText(a workspace.Artifact, r workspace.ArtifactReview, uiURL string) string {
	var b strings.Builder
	b.WriteString("Temporality: artifact review requested.\n")
	fmt.Fprintf(&b, "%q (v%d) awaits your decision — mode %s.", a.Title, a.Version, r.Mode)
	if r.Note != "" {
		fmt.Fprintf(&b, "\nNote: %s", r.Note)
	}
	if uiURL != "" {
		fmt.Fprintf(&b, "\nApprove or reject in the UI: %s/artifacts", strings.TrimRight(uiURL, "/"))
	} else {
		b.WriteString("\nApprove or reject in the Temporality UI (Artifacts).")
	}
	return b.String()
}
