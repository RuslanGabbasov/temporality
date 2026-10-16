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
	// ThreadRef is the conversation thread of the delivered question on
	// thread-capable transports (slack ts, docs/channels.md §9): a reply with
	// ReplyTo == ThreadRef matches this ask exactly, before the chat+oldest
	// fallback. Empty on non-threaded channels and asks whose thread is not
	// yet pinned — a first threaded reply backfills it.
	ThreadRef string
	CreatedAt time.Time
}

// UserChats maps a workspace user to their enabled chat addresses of one
// channel type.
type UserChats struct {
	UserID string
	Chats  []string
}

// SenderChannel is one enabled user channel of the loop's channel type with
// its registered sender identity — the closed-world check input
// (docs/inbound-messages.md §5).
type SenderChannel struct {
	Address  string
	SenderID string
}

// UserSenderChannels maps a workspace user to their enabled channels (with
// sender identities) of one channel type.
type UserSenderChannels struct {
	UserID   string
	Channels []SenderChannel
}

// IdentifyInboundSender resolves who may author initiative messages in a
// chat (docs/inbound-messages.md §5 — closed world): the chat must be an
// enabled channel address of some user, and the message sender must equal
// that channel's registered sender identity exactly. The second return says
// whether the chat is a registered channel address at all: a mismatch in a
// registered room deserves a polite rejection reply, an unregistered room
// must be recorded silently (anti-spam).
func IdentifyInboundSender(msg InboundMessage, users []UserSenderChannels) (userID string, roomRegistered bool) {
	for _, user := range users {
		for _, channel := range user.Channels {
			if channel.Address != msg.ChatID {
				continue
			}
			roomRegistered = true
			if channel.SenderID != "" && channel.SenderID == msg.Sender {
				return user.UserID, true
			}
		}
	}
	return "", roomRegistered
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

// MatchChannelReplies maps messages to the asks they answer. A threaded
// message (ReplyTo set) first tries the ask whose ThreadRef equals ReplyTo —
// an exact conversation hit; without one it falls back to the chat+oldest
// rule below, and a first threaded answer backfills the ask's ThreadRef so
// the rest of the conversation threads exactly. The fallback rule: a message
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
	match := func(message InboundMessage, index int) {
		ask := &open[index]
		answered[ask.RequestID] = true
		// A threaded answer to an ask without a pinned thread establishes the
		// conversation thread: later messages in it match exactly.
		if ask.ThreadRef == "" && message.ReplyTo != "" {
			ask.ThreadRef = message.ReplyTo
		}
		replies = append(replies, ChannelReply{
			Ask:       *ask,
			Message:   message,
			Cancelled: channelCancelBodies[strings.ToLower(strings.TrimSpace(message.Text))],
		})
	}
	eligible := func(message InboundMessage, ask PendingAsk) bool {
		return chatsByUser[ask.UserID][message.ChatID] &&
			!message.Timestamp.Before(ask.CreatedAt.Add(-channelReplySkew))
	}
	for _, message := range messages {
		if selfSenderID != "" && message.Sender == selfSenderID {
			continue
		}
		if message.ReplyTo != "" {
			// Exact thread hit first (docs/channels.md §9): the reply names the
			// conversation, the ask owns it — no age or chat arbitration needed
			// beyond the sanity checks above.
			matched := false
			for i := range open {
				if answered[open[i].RequestID] || open[i].ThreadRef != message.ReplyTo {
					continue
				}
				if !eligible(message, open[i]) {
					continue
				}
				match(message, i)
				matched = true
				break
			}
			if matched {
				continue
			}
		}
		for i := range open {
			if answered[open[i].RequestID] || !eligible(message, open[i]) {
				continue
			}
			match(message, i)
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

// ChannelWebhook is the push model of a two-way channel (docs/channels.md §3,
// §10): the channel calls the kernel over HTTP. Verification is not optional
// — every push driver must authenticate the request (signature or shared
// token) before its messages enter the inbound pipeline.
type ChannelWebhook interface {
	// Route is the mux pattern the kernel registers for this channel
	// ("POST /v1/channels/<type>/…").
	Route() string
	// VerifyRequest authenticates the request against the channel's secret
	// from the settings map; an error rejects the request with 403.
	VerifyRequest(r *http.Request, body []byte, settings map[string]string) error
	// Parse turns a verified request body into normalized messages; zero
	// messages means the payload carried nothing to route (handshakes,
	// acknowledgements, foreign event types).
	Parse(r *http.Request, body []byte) []InboundMessage
	// Respond writes the HTTP answer after the pipeline accepted the
	// payload — slack's url_verification echoes the challenge, everything
	// else acknowledges plainly.
	Respond(w http.ResponseWriter, r *http.Request, body []byte)
}

// ChannelSelfResolver resolves the channel account's own sender identity —
// pollers carry the method on themselves (ChannelPoller.Self); push drivers
// implement this separately so the inbound router can exclude the account's
// own outbound questions from reply matching.
type ChannelSelfResolver interface {
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
