package agent

import (
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// Inbound Matrix parsing (docs/triggers-and-escalations.md §7-8): the matrix
// delivery transport is two-way. The kernel bot long-polls /sync; the pure
// /sync → message extraction lives here, the poller adapter in
// driver_matrix_inbound.go, and the channel-neutral matching in
// channelinbound.go.

// MatrixInboundMessage is one observed room text message.
type MatrixInboundMessage struct {
	EventID   string
	RoomID    string
	Sender    string
	Body      string
	Timestamp time.Time
	// File attachments (m.file/m.image/m.audio/m.video): URL is the mxc://
	// content URI, Filename the original name. Text messages leave both empty.
	URL      string
	Filename string
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
	EventID        string             `json:"event_id"`
	Sender         string             `json:"sender"`
	OriginServerTS int64              `json:"origin_server_ts"`
	Content        matrixEventContent `json:"content"`
}

type matrixEventContent struct {
	MsgType  string `json:"msgtype"`
	Body     string `json:"body"`
	Filename string `json:"filename,omitempty"`
	URL      string `json:"url,omitempty"`
}

// ParseMatrixSync extracts room messages from a /sync response body. Text
// messages answer asks with words; file messages (m.file/m.image/m.audio/
// m.video carrying an mxc content URI) answer them with an attachment the
// loop downloads into the project workspace. Notices, empty bodies and files
// without a content URI are ignored; messages are returned in chronological
// order across rooms.
func ParseMatrixSync(body []byte) (string, []MatrixInboundMessage, error) {
	var decoded matrixSyncResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return "", nil, err
	}
	messages := []MatrixInboundMessage{}
	for roomID, room := range decoded.Rooms.Join {
		for _, event := range room.Timeline.Events {
			if event.Type != "m.room.message" {
				continue
			}
			text := strings.TrimSpace(event.Content.Body)
			if event.Content.MsgType == "m.text" {
				if text == "" {
					continue
				}
				messages = append(messages, MatrixInboundMessage{
					EventID:   event.EventID,
					RoomID:    roomID,
					Sender:    event.Sender,
					Body:      text,
					Timestamp: time.UnixMilli(event.OriginServerTS),
				})
				continue
			}
			if !matrixFileMsgTypes[event.Content.MsgType] || event.Content.URL == "" {
				continue
			}
			name := strings.TrimSpace(event.Content.Filename)
			if name == "" {
				name = text
			}
			if name == "" {
				name = "attachment"
			}
			messages = append(messages, MatrixInboundMessage{
				EventID:   event.EventID,
				RoomID:    roomID,
				Sender:    event.Sender,
				Body:      text,
				Timestamp: time.UnixMilli(event.OriginServerTS),
				URL:       event.Content.URL,
				Filename:  name,
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

// matrixFileMsgTypes are the m.room.message content types that carry an
// attachment (content.url = mxc://…).
var matrixFileMsgTypes = map[string]bool{
	"m.file": true, "m.image": true, "m.audio": true, "m.video": true,
}
