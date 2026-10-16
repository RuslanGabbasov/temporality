package agent

import (
	"testing"
	"time"
)

func TestMatchChannelReplies(t *testing.T) {
	now := time.Now()
	users := []UserChats{{UserID: "u1", Chats: []string{"!r1:hs"}}}
	bot := "@bot:hs"
	messages := func(ms ...InboundMessage) []InboundMessage { return ms }
	msg := func(chat, sender string, at time.Time, body string) InboundMessage {
		return InboundMessage{ChatID: chat, Sender: sender, Text: body, Timestamp: at}
	}

	t.Run("routes reply to the asking run", func(t *testing.T) {
		asks := []PendingAsk{{RequestID: "run/turn/01/call_a", RunID: "run", ProjectID: "p", UserID: "u1", CreatedAt: now.Add(-time.Minute)}}
		replies := MatchChannelReplies(messages(msg("!r1:hs", "@human:hs", now, "deploy it")), asks, users, bot)
		if len(replies) != 1 || replies[0].Ask.RequestID != "run/turn/01/call_a" || replies[0].Message.Text != "deploy it" {
			t.Fatalf("unexpected replies: %+v", replies)
		}
	})

	t.Run("skips the channel account's own messages", func(t *testing.T) {
		asks := []PendingAsk{{RequestID: "op", RunID: "run", UserID: "u1", CreatedAt: now.Add(-time.Minute)}}
		replies := MatchChannelReplies(messages(msg("!r1:hs", bot, now, "Temporality: an agent is waiting")), asks, users, bot)
		if len(replies) != 0 {
			t.Fatalf("bot message matched: %+v", replies)
		}
	})

	t.Run("history before the ask never matches", func(t *testing.T) {
		asks := []PendingAsk{{RequestID: "op", RunID: "run", UserID: "u1", CreatedAt: now}}
		replies := MatchChannelReplies(messages(msg("!r1:hs", "@human:hs", now.Add(-10*time.Minute), "old chatter")), asks, users, bot)
		if len(replies) != 0 {
			t.Fatalf("stale history matched: %+v", replies)
		}
	})

	t.Run("clock skew within tolerance still matches", func(t *testing.T) {
		asks := []PendingAsk{{RequestID: "op", RunID: "run", UserID: "u1", CreatedAt: now}}
		replies := MatchChannelReplies(messages(msg("!r1:hs", "@human:hs", now.Add(channelReplySkew-time.Second), "quick reply")), asks, users, bot)
		if len(replies) != 1 {
			t.Fatalf("skewed reply not matched: %+v", replies)
		}
	})

	t.Run("oldest ask in the chat wins", func(t *testing.T) {
		asks := []PendingAsk{
			{RequestID: "newer", RunID: "run", UserID: "u1", CreatedAt: now.Add(-time.Minute)},
			{RequestID: "older", RunID: "run", UserID: "u1", CreatedAt: now.Add(-2 * time.Hour)},
		}
		replies := MatchChannelReplies(messages(msg("!r1:hs", "@human:hs", now, "yes")), asks, users, bot)
		if len(replies) != 1 || replies[0].Ask.RequestID != "older" {
			t.Fatalf("oldest ask should win: %+v", replies)
		}
	})

	t.Run("two asks take two replies in order", func(t *testing.T) {
		asks := []PendingAsk{
			{RequestID: "first", RunID: "run", UserID: "u1", CreatedAt: now.Add(-2 * time.Hour)},
			{RequestID: "second", RunID: "run", UserID: "u1", CreatedAt: now.Add(-time.Minute)},
		}
		replies := MatchChannelReplies(
			messages(msg("!r1:hs", "@human:hs", now.Add(-time.Minute), "one"), msg("!r1:hs", "@human:hs", now, "two")),
			asks, users, bot)
		if len(replies) != 2 || replies[0].Ask.RequestID != "first" || replies[1].Ask.RequestID != "second" {
			t.Fatalf("unexpected pairing: %+v", replies)
		}
	})

	t.Run("cancel body declines", func(t *testing.T) {
		asks := []PendingAsk{{RequestID: "op", RunID: "run", UserID: "u1", CreatedAt: now.Add(-time.Minute)}}
		replies := MatchChannelReplies(messages(msg("!r1:hs", "@human:hs", now, "CANCEL")), asks, users, bot)
		if len(replies) != 1 || !replies[0].Cancelled {
			t.Fatalf("cancel not detected: %+v", replies)
		}
	})

	t.Run("foreign chat and chatless asks never match", func(t *testing.T) {
		asks := []PendingAsk{
			{RequestID: "mine", RunID: "run", UserID: "u1", CreatedAt: now.Add(-time.Minute)},
			{RequestID: "chatless", RunID: "run", UserID: "u2", CreatedAt: now.Add(-time.Minute)},
		}
		replies := MatchChannelReplies(messages(msg("!other:hs", "@human:hs", now, "hello")), asks, users, bot)
		if len(replies) != 0 {
			t.Fatalf("foreign chat matched: %+v", replies)
		}
	})

	// Team fan-out (docs/inbound-messages.md §8): one request, one PendingAsk
	// per delivered member. Any member's chat answers it; the first answer
	// wins and later members' replies do not produce a second routing.
	t.Run("team ask first answer wins across members", func(t *testing.T) {
		team := []UserChats{{UserID: "head", Chats: []string{"!rhead:hs"}}, {UserID: "qa", Chats: []string{"!rqa:hs"}}}
		asks := []PendingAsk{
			{RequestID: "team-op", RunID: "run", UserID: "head", CreatedAt: now.Add(-time.Minute)},
			{RequestID: "team-op", RunID: "run", UserID: "qa", CreatedAt: now.Add(-time.Minute)},
		}
		replies := MatchChannelReplies(messages(
			msg("!rhead:hs", "@head:hs", now.Add(-time.Second), "head approves"),
			msg("!rqa:hs", "@qa:hs", now, "qa also approves"),
		), asks, team, bot)
		if len(replies) != 1 {
			t.Fatalf("team ask must route exactly one answer, got %d", len(replies))
		}
		if replies[0].Ask.UserID != "head" || replies[0].Message.Text != "head approves" {
			t.Fatalf("first member's answer must win: %+v", replies)
		}

		// QA answers first instead — same contract from the other side.
		replies = MatchChannelReplies(messages(
			msg("!rqa:hs", "@qa:hs", now.Add(-time.Second), "qa approves"),
			msg("!rhead:hs", "@head:hs", now, "head also approves"),
		), asks, team, bot)
		if len(replies) != 1 || replies[0].Ask.UserID != "qa" {
			t.Fatalf("qa's answer must win when it comes first: %+v", replies)
		}
	})
}

// TestMatchChannelRepliesThreads pins the thread contract (docs/channels.md
// §9): a threaded reply (ReplyTo) matches the ask whose ThreadRef equals it
// exactly — before and regardless of the chat+oldest fallback; without an
// exact hit the fallback applies and a first threaded answer backfills the
// ask's ThreadRef for the rest of the conversation.
func TestMatchChannelRepliesThreads(t *testing.T) {
	now := time.Now()
	users := []UserChats{{UserID: "u1", Chats: []string{"C1"}}}
	bot := "B0"
	messages := func(ms ...InboundMessage) []InboundMessage { return ms }
	msg := func(chat, sender string, at time.Time, body, replyTo string) InboundMessage {
		return InboundMessage{ChatID: chat, Sender: sender, Text: body, Timestamp: at, ReplyTo: replyTo}
	}

	t.Run("exact thread hit beats the oldest ask", func(t *testing.T) {
		asks := []PendingAsk{
			{RequestID: "older-threadless", RunID: "run", UserID: "u1", CreatedAt: now.Add(-2 * time.Hour)},
			{RequestID: "newer-threaded", RunID: "run", UserID: "u1", ThreadRef: "1697000000.000100", CreatedAt: now.Add(-time.Minute)},
		}
		replies := MatchChannelReplies(messages(msg("C1", "U1", now, "yes", "1697000000.000100")), asks, users, bot)
		if len(replies) != 1 || replies[0].Ask.RequestID != "newer-threaded" {
			t.Fatalf("threaded reply must hit its thread's ask: %+v", replies)
		}
	})

	t.Run("thread of a closed ask does not steal the reply", func(t *testing.T) {
		asks := []PendingAsk{
			{RequestID: "answered", RunID: "run", UserID: "u1", ThreadRef: "1697000000.000100", CreatedAt: now.Add(-2 * time.Hour)},
			{RequestID: "open", RunID: "run", UserID: "u1", CreatedAt: now.Add(-time.Minute)},
		}
		// The first message answers the ask that owns the thread; the second
		// message in the same thread finds that ask closed and falls through
		// to the open one instead of being dropped.
		replies := MatchChannelReplies(messages(
			msg("C1", "U1", now.Add(-time.Minute), "first", "1697000000.000100"),
			msg("C1", "U1", now, "second", "1697000000.000100"),
		), asks, users, bot)
		if len(replies) != 2 || replies[0].Ask.RequestID != "answered" || replies[1].Ask.RequestID != "open" {
			t.Fatalf("unexpected pairing: %+v", replies)
		}
	})

	t.Run("threaded reply with no thread hit falls back and backfills", func(t *testing.T) {
		asks := []PendingAsk{{RequestID: "op", RunID: "run", UserID: "u1", CreatedAt: now.Add(-time.Minute)}}
		replies := MatchChannelReplies(messages(msg("C1", "U1", now, "here", "1697000000.000900")), asks, users, bot)
		if len(replies) != 1 || replies[0].Ask.RequestID != "op" {
			t.Fatalf("fallback broken: %+v", replies)
		}
		if replies[0].Ask.ThreadRef != "1697000000.000900" {
			t.Fatalf("first threaded answer must backfill the thread ref: %+v", replies[0].Ask)
		}
	})

	t.Run("backfilled thread serves the next reply exactly", func(t *testing.T) {
		asks := []PendingAsk{
			{RequestID: "newer", RunID: "run", UserID: "u1", CreatedAt: now.Add(-time.Minute)},
			{RequestID: "older", RunID: "run", UserID: "u1", CreatedAt: now.Add(-2 * time.Hour)},
		}
		replies := MatchChannelReplies(messages(
			msg("C1", "U1", now.Add(-30*time.Second), "first", "1697000000.000900"),
			msg("C1", "U1", now, "second", "1697000000.000900"),
		), asks, users, bot)
		// The first threaded answer took the oldest ask (fallback) and pinned
		// its thread; the second lands in the same thread and must match that
		// same ask's conversation, not the newer one.
		if len(replies) != 2 {
			t.Fatalf("expected two routed replies, got %+v", replies)
		}
		if replies[0].Ask.RequestID != "older" || replies[1].Ask.RequestID != "newer" {
			t.Fatalf("unexpected pairing: %+v", replies)
		}
	})

	t.Run("thread hit from a foreign chat never matches", func(t *testing.T) {
		asks := []PendingAsk{{RequestID: "op", RunID: "run", UserID: "u1", ThreadRef: "1697000000.000100", CreatedAt: now.Add(-time.Minute)}}
		replies := MatchChannelReplies(messages(msg("C-other", "U1", now, "yes", "1697000000.000100")), asks, users, bot)
		if len(replies) != 0 {
			t.Fatalf("foreign chat matched through the thread: %+v", replies)
		}
	})
}
