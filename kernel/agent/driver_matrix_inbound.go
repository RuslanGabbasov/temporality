package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// matrixPoller adapts the Matrix client-server API to the ChannelPoller
// contract: /sync long-poll with a since cursor, whoami for the bot's own
// identity, and authenticated media downloads for file replies. The loop
// owns the cursor; this adapter is stateless.
type matrixPoller struct{}

// matrixSyncTimeout is the server-side long-poll window: shorter than the
// loop's cycle budget so one poll returns within it.
const matrixSyncTimeout = 25 * time.Second

// inboundFileCap bounds one inbound attachment (same spirit as the outbound
// send_file cap; uploads and downloads share generous-but-bounded limits).
const inboundFileCap = 100 << 20

func (matrixPoller) Poll(ctx context.Context, client *http.Client, settings map[string]string, cursor string) ([]InboundMessage, string, error) {
	homeserver, token, err := matrixCredentials(settings)
	if err != nil {
		return nil, "", err
	}
	messages, next, err := matrixSync(ctx, client, homeserver, token, cursor)
	if err != nil && cursor != "" && matrixUnknownSince(err) {
		// The homeserver forgot the cursor (restart, retention): fall back to
		// a bounded initial sync instead of erroring forever.
		messages, next, err = matrixSync(ctx, client, homeserver, token, "")
	}
	if err != nil {
		return nil, "", err
	}
	return matrixNormalized(messages), next, nil
}

func (matrixPoller) Self(ctx context.Context, client *http.Client, settings map[string]string) (string, error) {
	homeserver, token, err := matrixCredentials(settings)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, homeserver+"/_matrix/client/v3/account/whoami", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("matrix whoami returned %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	var decoded struct {
		UserID string `json:"user_id"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return "", err
	}
	if decoded.UserID == "" {
		return "", errors.New("no user_id in whoami response")
	}
	return decoded.UserID, nil
}

// FetchFile downloads an mxc:// content URI through the authenticated media
// endpoints: the modern client/v1 route first, the legacy media/v3 as the
// fallback for homeservers that have not moved yet.
func (matrixPoller) FetchFile(ctx context.Context, client *http.Client, settings map[string]string, file InboundFile) ([]byte, error) {
	homeserver, token, err := matrixCredentials(settings)
	if err != nil {
		return nil, err
	}
	server, mediaID, err := parseMxcURL(file.Ref)
	if err != nil {
		return nil, err
	}
	routes := []string{
		fmt.Sprintf("%s/_matrix/client/v1/media/download/%s/%s?timeout_ms=30000", homeserver, url.PathEscape(server), url.PathEscape(mediaID)),
		fmt.Sprintf("%s/_matrix/media/v3/download/%s/%s", homeserver, url.PathEscape(server), url.PathEscape(mediaID)),
	}
	var lastErr error
	for _, endpoint := range routes {
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if reqErr != nil {
			return nil, reqErr
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, doErr := client.Do(req)
		if doErr != nil {
			lastErr = doErr
			continue
		}
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
			resp.Body.Close()
			lastErr = fmt.Errorf("returned %d: %s", resp.StatusCode, truncate(string(body), 200))
			continue
		}
		payload, readErr := io.ReadAll(io.LimitReader(resp.Body, inboundFileCap+1))
		resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}
		if len(payload) > inboundFileCap {
			return nil, fmt.Errorf("attachment exceeds the %d-byte inbound cap", inboundFileCap)
		}
		return payload, nil
	}
	return nil, lastErr
}

// matrixCredentials extracts and normalizes the homeserver/token pair.
func matrixCredentials(settings map[string]string) (homeserver, token string, err error) {
	homeserver = strings.TrimRight(setting(settings, "homeserver"), "/")
	token = TrimBearerPrefix(setting(settings, "access_token"))
	if homeserver == "" || token == "" {
		return "", "", errors.New("matrix transport is not configured")
	}
	return homeserver, token, nil
}

// matrixSync long-polls /sync. The initial sync (no cursor) bounds the room
// history it asks for; replayed events are still constrained by the
// ask-created-at rule, so replies that arrived while the kernel was down are
// picked up without older chatter matching.
func matrixSync(ctx context.Context, client *http.Client, homeserver, token, cursor string) ([]MatrixInboundMessage, string, error) {
	endpoint := homeserver + "/_matrix/client/v3/sync?timeout=" + strconv.Itoa(int(matrixSyncTimeout.Seconds()))
	if cursor == "" {
		endpoint += "&filter=" + url.QueryEscape(`{"room":{"timeline":{"limit":50}}}`)
	} else {
		endpoint += "&since=" + url.QueryEscape(cursor)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("matrix sync returned %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	next, messages, err := ParseMatrixSync(body)
	if err != nil {
		return nil, "", err
	}
	return messages, next, nil
}

// matrixUnknownSince reports whether a sync error means the homeserver no
// longer knows the cursor.
func matrixUnknownSince(err error) bool {
	if err == nil {
		return false
	}
	detail := err.Error()
	return strings.Contains(detail, "M_UNKNOWN_SINCE") || strings.Contains(detail, "unknown since")
}

// matrixNormalized converts parsed matrix messages into the channel-neutral
// model: the room becomes the chat, the single attachment becomes Files. The
// batch then passes through attachment coalescing — Element sends a multi-file
// message as several protocol-independent events.
func matrixNormalized(messages []MatrixInboundMessage) []InboundMessage {
	normalized := make([]InboundMessage, 0, len(messages))
	for _, message := range messages {
		entry := InboundMessage{
			ID:        message.EventID,
			Channel:   "matrix",
			ChatID:    message.RoomID,
			Sender:    message.Sender,
			Text:      message.Body,
			Timestamp: message.Timestamp,
		}
		if message.URL != "" {
			entry.Files = []InboundFile{{Ref: message.URL, Name: message.Filename}}
		}
		normalized = append(normalized, entry)
	}
	return coalesceMatrixBatch(normalized)
}

// matrixMessageKind classifies one normalized message for batch coalescing.
type matrixMessageKind int

const (
	matrixPlainMessage matrixMessageKind = iota // text without attachment
	matrixCaptionedFile                         // attachment with its own caption
	matrixBareFile                              // attachment whose body is just the file name
)

// matrixKind applies the caption convention of the spec: a file event carries
// a caption only when the client stored an explicit filename distinct from
// the body — otherwise the body is the file name itself.
func matrixKind(message InboundMessage) matrixMessageKind {
	if len(message.Files) == 0 {
		return matrixPlainMessage
	}
	body := strings.TrimSpace(message.Text)
	if name := message.Files[0].Name; name != "" && body != "" && body != name {
		return matrixCaptionedFile
	}
	return matrixBareFile
}

// coalesceMatrixBatch merges bare-attachment events into a neighbouring
// message of the same sender in the same room: without it a "three files plus
// a caption" message would start three file-only intake runs plus a text run.
// Deterministic: every bare file joins the nearest anchor by batch position
// (ties to the earlier one); a group with no anchor at all collapses into its
// first file event, with the file names as the body so classification still
// sees what arrived. Captioned files and plain texts never merge.
func coalesceMatrixBatch(messages []InboundMessage) []InboundMessage {
	type slot struct{ anchors, bare []int }
	groups := map[[2]string]*slot{}
	for i, message := range messages {
		key := [2]string{message.ChatID, message.Sender}
		s := groups[key]
		if s == nil {
			s = &slot{}
			groups[key] = s
		}
		switch matrixKind(message) {
		case matrixBareFile:
			s.bare = append(s.bare, i)
		default:
			s.anchors = append(s.anchors, i)
		}
	}
	absorb := map[int][]int{}
	var orphanBare []int
	for _, s := range groups {
		for _, bi := range s.bare {
			target, best := -1, len(messages)+1
			for _, ai := range s.anchors {
				distance := ai - bi
				if distance < 0 {
					distance = -distance
				}
				if distance < best {
					best, target = distance, ai
				}
			}
			if target >= 0 {
				absorb[target] = append(absorb[target], bi)
			} else {
				orphanBare = append(orphanBare, bi)
			}
		}
	}
	sort.Ints(orphanBare)
	if len(orphanBare) > 1 {
		absorb[orphanBare[0]] = append(absorb[orphanBare[0]], orphanBare[1:]...)
	}
	if len(absorb) == 0 {
		return messages
	}
	merged := make([]bool, len(messages))
	for _, sources := range absorb {
		for _, si := range sources {
			merged[si] = true
		}
	}
	result := make([]InboundMessage, 0, len(messages))
	for i := range messages {
		if merged[i] {
			continue
		}
		message := messages[i]
		if sources, ok := absorb[i]; ok {
			sort.Ints(sources)
			for _, si := range sources {
				message.Files = append(message.Files, messages[si].Files...)
			}
			if matrixKind(messages[i]) == matrixBareFile {
				names := make([]string, 0, len(message.Files))
				for _, file := range message.Files {
					if file.Name != "" {
						names = append(names, file.Name)
					}
				}
				if len(names) > 0 {
					message.Text = strings.Join(names, "\n")
				}
			}
		}
		result = append(result, message)
	}
	return result
}

// parseMxcURL splits mxc://<server>/<mediaID> into its parts.
func parseMxcURL(raw string) (string, string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "mxc" || parsed.Host == "" || strings.Trim(parsed.Path, "/") == "" {
		return "", "", fmt.Errorf("invalid matrix content URI %q", raw)
	}
	return parsed.Host, strings.Trim(parsed.Path, "/"), nil
}
