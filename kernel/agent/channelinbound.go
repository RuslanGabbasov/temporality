package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Inbound channel layer (docs/channels.md §4-5): channel-agnostic message
// model, reply matching and the pull-model driver contract. Everything
// channel-specific (protocols, cursors, attachment fetch) lives in the
// driver; the loop in the kernel binary owns routing, cursors and identity.

// InboundMessage is the normalized form of one user message observed in a
// channel. Everything channel-specific stays inside the fields — the core
// never parses a protocol again.
type InboundMessage struct {
	// ID is the channel's message identifier (matrix event_id, telegram
	// update_id) — the future dedup key for the inbound pipeline.
	ID string
	// Channel is the driver type that produced the message.
	Channel string
	// ChatID is the room/chat the message arrived in; it is matched against
	// the recipient's channel addresses.
	ChatID string
	// Sender is the channel identity of the author (mxid, member id…).
	Sender string
	Text   string
	// Files are the attachments the message carries; Ref is opaque to the
	// core (mxc://, file_id, URL) and only the driver can fetch it.
	Files     []InboundFile
	Timestamp time.Time
	// ReplyTo links the message to a previous one when the channel carries
	// threading (slack/teams/mattermost threads, matrix replies).
	ReplyTo string
}

// InboundFile is one attachment of an inbound message.
type InboundFile struct {
	Ref  string // driver-opaque reference (mxc://, file_id, URL)
	Name string // original file name
}

// PendingAsk is the matching projection of one open human request delivered
// through a channel: a reply in the recipient's chat answers it.
type PendingAsk struct {
	RequestID string // human request id (= ask operation id)
	RunID     string // full run path, e.g. "run/delegate/01" — identifies the waiting workflow
	ProjectID string
	UserID    string // resolved recipient: the chat owner the question was delivered to
	CreatedAt time.Time
}

// UserChats maps a workspace user to their enabled chat addresses of one
// channel type.
type UserChats struct {
	UserID string
	Chats  []string
}

// ChannelReply is one matched reply: the message answers the ask.
type ChannelReply struct {
	Ask       PendingAsk
	Message   InboundMessage
	Cancelled bool // explicit "cancel" reply declines the question
}

// channelReplySkew tolerates clock drift between the channel server's event
// timestamps and the workspace database clock when deciding whether a message
// can be a reply to a request created around the same time.
const channelReplySkew = 2 * time.Minute

// channelCancelBodies decline the question instead of answering it.
var channelCancelBodies = map[string]bool{"cancel": true, "отмена": true, "cancelled": true}

// MatchChannelReplies maps messages to the asks they answer. A message
// answers the oldest still-unmatched ask whose recipient owns an enabled
// chat of that channel for the message's chat id. The channel account's own
// messages never match — they are the questions themselves. Messages older
// than the ask (beyond clock skew) never match, so replayed history cannot
// answer questions asked before it.
func MatchChannelReplies(messages []InboundMessage, asks []PendingAsk, users []UserChats, selfSenderID string) []ChannelReply {
	chatsByUser := make(map[string]map[string]bool, len(users))
	for _, user := range users {
		set := chatsByUser[user.UserID]
		if set == nil {
			set = make(map[string]bool)
			chatsByUser[user.UserID] = set
		}
		for _, chat := range user.Chats {
			set[chat] = true
		}
	}
	// Eligible asks oldest-first: the first reply in a chat answers the oldest
	// question the user saw there.
	open := make([]PendingAsk, 0, len(asks))
	for _, ask := range asks {
		if ask.UserID != "" && len(chatsByUser[ask.UserID]) > 0 {
			open = append(open, ask)
		}
	}
	sort.SliceStable(open, func(i, j int) bool { return open[i].CreatedAt.Before(open[j].CreatedAt) })
	answered := make(map[string]bool, len(open))
	replies := []ChannelReply{}
	for _, message := range messages {
		if selfSenderID != "" && message.Sender == selfSenderID {
			continue
		}
		for _, ask := range open {
			if answered[ask.RequestID] || !chatsByUser[ask.UserID][message.ChatID] {
				continue
			}
			if message.Timestamp.Before(ask.CreatedAt.Add(-channelReplySkew)) {
				continue
			}
			answered[ask.RequestID] = true
			replies = append(replies, ChannelReply{
				Ask:       ask,
				Message:   message,
				Cancelled: channelCancelBodies[strings.ToLower(strings.TrimSpace(message.Text))],
			})
			break
		}
	}
	return replies
}

// ChannelPoller is the pull model of a two-way channel (docs/channels.md §3):
// the kernel loop long-polls with an opaque cursor it persists between
// cycles. Push channels (webhook drivers) do not implement it.
type ChannelPoller interface {
	// Poll blocks until messages arrive or the long-poll times out. cursor
	// is the previous next value ("" = start now, bounded history replay);
	// returns normalized messages plus the next cursor.
	Poll(ctx context.Context, client *http.Client, settings map[string]string, cursor string) ([]InboundMessage, string, error)
	// Self resolves the account's own sender identity in the channel
	// (matrix whoami → mxid) so the loop can exclude its own outbound
	// questions from reply matching.
	Self(ctx context.Context, client *http.Client, settings map[string]string) (string, error)
}

// ChannelFileFetcher downloads one inbound attachment; pollers whose
// channels carry files (capabilities.FilesIn) implement it. The bytes move
// through the driver because only it can resolve the opaque Ref.
type ChannelFileFetcher interface {
	FetchFile(ctx context.Context, client *http.Client, settings map[string]string, file InboundFile) ([]byte, error)
}

// ChannelFingerprint derives a stable identity of one channel's effective
// settings. Any credential change means a different account stream: the
// persisted cursor no longer applies and the cached self identity is stale.
func ChannelFingerprint(settings map[string]string) string {
	keys := make([]string, 0, len(settings))
	for key := range settings {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	digest := sha256.New()
	for _, key := range keys {
		digest.Write([]byte(key))
		digest.Write([]byte{0})
		digest.Write([]byte(settings[key]))
		digest.Write([]byte{0})
	}
	return hex.EncodeToString(digest.Sum(nil))[:16]
}
