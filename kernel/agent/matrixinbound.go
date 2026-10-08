package agent

import (
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// Inbound Matrix replies (docs/triggers-and-escalations.md §7-8): the matrix
// delivery transport is two-way. The kernel bot long-polls /sync and maps room
// replies back into the paused run through the same Approval signal the web
// inbox uses. The mapping is pure logic here; the poll loop lives in the
// kernel binary next to its Temporal client.

// MatrixInboundMessage is one observed room text message.
type MatrixInboundMessage struct {
	RoomID    string
	Sender    string
	Body      string
	Timestamp time.Time
}

// MatrixPendingAsk is the matching projection of one open human request that
// was delivered to a matrix room: a reply in that room answers it.
type MatrixPendingAsk struct {
	RequestID string // human request id (= ask operation id)
	RunID     string // full run path, e.g. "run/delegate/01" — identifies the waiting workflow
	ProjectID string
	UserID    string // resolved recipient: the room owner the question was delivered to
	CreatedAt time.Time
}

// MatrixUserRooms maps a workspace user to their enabled matrix room ids.
type MatrixUserRooms struct {
	UserID string
	Rooms  []string
}

// MatrixReply is one matched reply: the message answers the ask.
type MatrixReply struct {
	Ask       MatrixPendingAsk
	Message   MatrixInboundMessage
	Cancelled bool // explicit "cancel" reply declines the question
}

// matrixSyncResponse models the subset of /sync the poller needs.
type matrixSyncResponse struct {
	NextBatch string `json:"next_batch"`
	Rooms     struct {
		Join map[string]matrixJoinedRoom `json:"join"`
	} `json:"rooms"`
}

type matrixJoinedRoom struct {
	Timeline struct {
		Events []matrixRoomEvent `json:"events"`
	} `json:"timeline"`
}

type matrixRoomEvent struct {
	Type           string             `json:"type"`
	Sender         string             `json:"sender"`
	OriginServerTS int64              `json:"origin_server_ts"`
	Content        matrixEventContent `json:"content"`
}

type matrixEventContent struct {
	MsgType string `json:"msgtype"`
	Body    string `json:"body"`
}

// ParseMatrixSync extracts room text messages from a /sync response body.
// Non-message events, notices and empty bodies are ignored; messages are
// returned in chronological order across rooms.
func ParseMatrixSync(body []byte) (string, []MatrixInboundMessage, error) {
	var decoded matrixSyncResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return "", nil, err
	}
	messages := []MatrixInboundMessage{}
	for roomID, room := range decoded.Rooms.Join {
		for _, event := range room.Timeline.Events {
			if event.Type != "m.room.message" || event.Content.MsgType != "m.text" {
				continue
			}
			text := strings.TrimSpace(event.Content.Body)
			if text == "" {
				continue
			}
			messages = append(messages, MatrixInboundMessage{
				RoomID:    roomID,
				Sender:    event.Sender,
				Body:      text,
				Timestamp: time.UnixMilli(event.OriginServerTS),
			})
		}
	}
	sort.SliceStable(messages, func(i, j int) bool {
		if messages[i].Timestamp.Equal(messages[j].Timestamp) {
			return messages[i].RoomID < messages[j].RoomID
		}
		return messages[i].Timestamp.Before(messages[j].Timestamp)
	})
	return decoded.NextBatch, messages, nil
}

// matrixReplySkew tolerates clock drift between the homeserver's event
// timestamps and the workspace database clock when deciding whether a message
// can be a reply to a request created around the same time.
const matrixReplySkew = 2 * time.Minute

// matrixCancelBodies decline the question instead of answering it.
var matrixCancelBodies = map[string]bool{"cancel": true, "отмена": true, "cancelled": true}

// MatchMatrixReplies maps messages to the asks they answer. A message answers
// the oldest still-unmatched ask whose recipient owns an enabled matrix channel
// for that room. The bot's own messages never match — they are the questions
// themselves. Messages older than the ask (beyond clock skew) never match, so
// replayed room history cannot answer questions asked before it.
func MatchMatrixReplies(messages []MatrixInboundMessage, asks []MatrixPendingAsk, users []MatrixUserRooms, botUserID string) []MatrixReply {
	roomsByUser := make(map[string]map[string]bool, len(users))
	for _, user := range users {
		set := roomsByUser[user.UserID]
		if set == nil {
			set = make(map[string]bool)
			roomsByUser[user.UserID] = set
		}
		for _, room := range user.Rooms {
			set[room] = true
		}
	}
	// Eligible asks oldest-first: the first reply in a room answers the oldest
	// question the user saw there.
	open := make([]MatrixPendingAsk, 0, len(asks))
	for _, ask := range asks {
		if ask.UserID != "" && len(roomsByUser[ask.UserID]) > 0 {
			open = append(open, ask)
		}
	}
	sort.SliceStable(open, func(i, j int) bool { return open[i].CreatedAt.Before(open[j].CreatedAt) })
	answered := make(map[string]bool, len(open))
	replies := []MatrixReply{}
	for _, message := range messages {
		if botUserID != "" && message.Sender == botUserID {
			continue
		}
		for _, ask := range open {
			if answered[ask.RequestID] || !roomsByUser[ask.UserID][message.RoomID] {
				continue
			}
			if message.Timestamp.Before(ask.CreatedAt.Add(-matrixReplySkew)) {
				continue
			}
			answered[ask.RequestID] = true
			replies = append(replies, MatrixReply{
				Ask:       ask,
				Message:   message,
				Cancelled: matrixCancelBodies[strings.ToLower(strings.TrimSpace(message.Body))],
			})
			break
		}
	}
	return replies
}
