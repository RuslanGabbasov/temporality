package main

import (
	"context"
	"errors"
	"strings"

	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/workspace"
)

// Review decisions from the channel (docs/artifact-lifecycle.md §6 Phase 2).
// A review round delivered through a channel carries an orphan-run human
// request row (artifactnotify.go); when a reply matches that row, the shared
// inbound loop routes it here instead of signaling a workflow. The decision
// lands through the same store transition as the UI route — the review row
// stays the source of truth, the chat only gets confirmations.

// artifactReviewApproveWords / artifactReviewRejectWords are the decision
// keywords: the FIRST word of the reply decides, the rest is the comment.
// Conservative on purpose — bare "да"/"нет" or a stray "ok" mid-sentence
// must never close a review round.
var (
	artifactReviewApproveWords = map[string]bool{
		"approve": true, "approved": true, "lgtm": true, "accept": true,
		"ok": true, "ок": true, "окей": true,
		"одобрить": true, "одобряю": true, "одобрено": true, "согласен": true, "согласна": true,
		"согласовано": true,
	}
	artifactReviewRejectWords = map[string]bool{
		"reject": true, "rejected": true,
		"отклонить": true, "отклоняю": true, "отклонено": true,
	}
)

// parseArtifactReviewDecision recognizes an approve/reject reply: the first
// word (case- and punctuation-insensitive) picks the decision, the remaining
// words become the comment. ok=false leaves the round open — the reviewer is
// asked to answer with one of the keywords.
func parseArtifactReviewDecision(text string) (approve bool, comment string, ok bool) {
	words := strings.Fields(strings.TrimSpace(text))
	if len(words) == 0 {
		return false, "", false
	}
	word := strings.ToLower(strings.Trim(words[0], ".,;:!?«»\"'()"))
	switch {
	case artifactReviewApproveWords[word]:
		approve = true
	case artifactReviewRejectWords[word]:
		approve = false
	default:
		return false, "", false
	}
	return approve, strings.TrimSpace(strings.Join(words[1:], " ")), true
}

// routeArtifactReviewReply resolves one review round from a channel reply.
// It reports whether the message was consumed as a review answer: ONLY a
// recognized decision keyword (or an explicit cancel) is an answer. Any
// other text must stay unconsumed — it re-enters the pipeline as ordinary
// chat; swallowing it here starved whole chats (the 2026-11-27 intake
// outage: every message of a room with an open review ask matched it and
// died in a «review_how_to» notice, the agent never answered).
// Every consuming branch confirms in the reviewer's chat in the dialog
// language (detected from the reply text); the row and the round stay
// consistent: the row closes only when the round actually resolved (or the
// reviewer explicitly declined to answer).
func (l *channelInboundLoop) routeArtifactReviewReply(ctx context.Context, channelSettings map[string]string, reply agent.ChannelReply, reviewID string) bool {
	actor := reply.Ask.UserID // anyone in the chat answers as the chat owner (MVP)
	lang := agent.DetectLang(reply.Message.Text)

	// Explicit "cancel" declines answering: stop asking through the channel,
	// the round stays open for the artifacts page.
	if reply.Cancelled {
		l.cancelReviewRow(ctx, artifactReviewRequestID(reviewID), actor)
		l.setConversationInteractionStatus(ctx, reply.Message.ChatID, "artifact_review", reviewID, "cancelled")
		_ = l.store.RecordAccessAudit(ctx, actor, workspace.AuditHumanCancelled, "human_request", artifactReviewRequestID(reviewID), map[string]any{
			"review_id": reviewID, "channel": l.driver.Type(),
		})
		l.sendRoomNotice(ctx, channelSettings, reply.Message.ChatID, agent.ChannelText(lang, "review_left_open"))
		l.log.Info("artifact review reply declined", "review", reviewID, "sender", reply.Message.Sender)
		return true
	}

	approve, comment, ok := parseArtifactReviewDecision(reply.Message.Text)
	if !ok {
		// Not a decision keyword — not an answer. The message falls through
		// to intake / the conversational agent, the round stays open for a
		// real keyword (the delivery notice already named them).
		return false
	}

	review, err := l.store.GetArtifactReview(ctx, reviewID)
	if errors.Is(err, workspace.ErrNotFound) {
		// The round is gone (deleted artifact cascade) — close the stale row;
		// the message itself stays unconsumed, the dialog learns the review
		// no longer exists from the intake answer instead of dead air.
		l.cancelReviewRow(ctx, artifactReviewRequestID(reviewID), actor)
		l.setConversationInteractionStatus(ctx, reply.Message.ChatID, "artifact_review", reviewID, "cancelled")
		return false
	}
	if err != nil {
		l.log.Warn("artifact review reply load", "review", reviewID, "error", err)
		return true // consumed: the ask is still live, a retry may yet decide
	}
	if review.Status != "open" {
		// Decided elsewhere (UI) while the row was still matchable — close
		// the row so the inbox stops counting it, tell the reviewer.
		l.cancelReviewRow(ctx, artifactReviewRequestID(reviewID), actor)
		l.setConversationInteractionStatus(ctx, reply.Message.ChatID, "artifact_review", reviewID, "resolved")
		l.sendRoomNotice(ctx, channelSettings, reply.Message.ChatID, agent.ChannelText(lang, "review_already"))
		return true
	}

	users, err := l.store.ListUsers(ctx)
	if err != nil {
		l.log.Warn("artifact review reply users", "review", reviewID, "error", err)
		return true // consumed: the ask is still live, a retry may yet decide
	}
	units, _ := l.store.ListOrgUnits(ctx)
	entries := workspace.MatchArtifactReviewers(actor, users, units, review.Reviewers)
	if len(entries) == 0 {
		// The chat owner lost their reviewer entry (role change, unit move):
		// never resolve through this chat.
		l.log.Warn("artifact review reply not a reviewer", "review", reviewID, "actor", actor)
		l.sendRoomNotice(ctx, channelSettings, reply.Message.ChatID, agent.ChannelText(lang, "review_not_yours"))
		return true
	}

	resolved, artifact, err := l.store.ResolveArtifactReview(ctx, reviewID, entries, actor, approve, comment)
	if errors.Is(err, workspace.ErrConflict) {
		// Lost a race with a parallel decision — same as the already-closed
		// branch: close the row, keep the chat honest.
		l.cancelReviewRow(ctx, artifactReviewRequestID(reviewID), actor)
		l.setConversationInteractionStatus(ctx, reply.Message.ChatID, "artifact_review", reviewID, "resolved")
		l.sendRoomNotice(ctx, channelSettings, reply.Message.ChatID, agent.ChannelText(lang, "review_already"))
		return true
	}
	if err != nil {
		l.log.Warn("artifact review reply resolve", "review", reviewID, "actor", actor, "error", err)
		return true // row stays open — a retry (or the UI) can still decide
	}
	l.setConversationInteractionStatus(ctx, reply.Message.ChatID, "artifact_review", reviewID, "resolved")

	// Close the row as answered so the inbox badge drops immediately.
	if _, _, err := l.store.AnswerHumanRequest(ctx, artifactReviewRequestID(reviewID), decisionResponse(approve, comment), actor); err != nil && !errors.Is(err, workspace.ErrNotFound) {
		l.log.Warn("artifact review reply close row", "review", reviewID, "error", err)
	}

	decision := "reject"
	auditAction := workspace.AuditArtifactReviewRejected
	if approve {
		decision = "approve"
		auditAction = workspace.AuditArtifactReviewApproved
	}
	_ = l.store.RecordAccessAudit(ctx, actor, auditAction, "artifact", review.ArtifactID, map[string]any{
		"review_id": review.ID, "decision": decision, "version": review.ArtifactVersion,
		"comment": comment, "channel": l.driver.Type(),
	})
	emitArtifactLifecycleEvent(ctx, l.events, auditEventName(approve), review.ArtifactID, map[string]any{
		"artifact_id": review.ArtifactID, "title": artifact.Title, "version": review.ArtifactVersion,
		"review_id": review.ID, "decision": decision, "resolved_by": actor, "comment": comment,
		"via": "channel:" + l.driver.Type(),
	})

	key := "review_decided_re"
	if approve {
		key = "review_decided"
	}
	// The suffix renders in ONE pass: taking the pattern without the argument
	// and re-formatting the result turned "%s" into "%!s(MISSING)" and then
	// re-parsed that garbage as a format — the notice shipped strings like
	// «— %!!(string=user-…)s(MISSING)».
	suffix := ""
	if actor != "" {
		suffix = agent.ChannelText(lang, "review_by", actor)
	}
	l.sendRoomNotice(ctx, channelSettings, reply.Message.ChatID, agent.ChannelText(lang, key, artifact.Title, resolved.ArtifactVersion, suffix))
	l.log.Info("artifact review decided from channel", "review", reviewID, "artifact", review.ArtifactID, "decision", decision, "actor", actor, "channel", l.driver.Type())
	return true
}

// cancelReviewRow closes a review round's channel row best-effort. ErrNotFound
// is the normal case once the row is already closed (or never existed for
// web-only rounds) — only real failures are worth a log line.
func (l *channelInboundLoop) cancelReviewRow(ctx context.Context, requestID, actor string) {
	if err := l.store.CancelHumanRequest(ctx, requestID, actor); err != nil && !errors.Is(err, workspace.ErrNotFound) {
		l.log.Warn("artifact review cancel row", "request", requestID, "error", err)
	}
}

// decisionResponse is what the human_request row remembers as the response.
func decisionResponse(approve bool, comment string) string {
	word := "reject"
	if approve {
		word = "approve"
	}
	if comment == "" {
		return word
	}
	return word + ": " + comment
}

// auditEventName maps the decision to the lifecycle event type — the same
// events the HTTP route emits, so the UI trace reads both paths identically.
func auditEventName(approve bool) string {
	if approve {
		return "artifact.review.approved"
	}
	return "artifact.review.rejected"
}
