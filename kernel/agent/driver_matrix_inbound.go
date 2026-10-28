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
	"sync"
	"time"
)

// matrixPoller adapts the Matrix client-server API to the ChannelPoller
// contract: /sync long-poll with a since cursor, whoami for the bot's own
// identity, and authenticated media downloads for file replies. The loop
// owns the exposed cursor; this adapter keeps its own sync position ahead of
// it while a message group is buffering.
type matrixPoller struct {
	buffer *matrixBatchBuffer
	cursor string // internal /sync position, ahead of the exposed one while buffering
}

func newMatrixPoller() *matrixPoller {
	return &matrixPoller{buffer: newMatrixBatchBuffer()}
}

// matrixSyncTimeout is the server-side long-poll window: shorter than the
// loop's cycle budget so one poll returns within it.
const matrixSyncTimeout = 25 * time.Second

// inboundFileCap bounds one inbound attachment (same spirit as the outbound
// send_file cap; uploads and downloads share generous-but-bounded limits).
const inboundFileCap = 100 << 20

// Timing of the collection window. Element uploads attachments to the media
// repo first and sends one room event per file as its upload finishes — the
// events of one "two files plus a caption" message land in different sync
// batches seconds apart, and each alone would start a file-only intake run.
// The buffer holds a (chat, sender) group until the sender goes quiet (or
// the wait cap trips), then releases it through batch coalescing. Vars so
// tests can shrink them.
var (
	matrixBatchQuiet    = 2500 * time.Millisecond
	matrixBatchMaxWait  = 8 * time.Second
	matrixBatchPollStep = 200 * time.Millisecond
)

// SetMatrixBatchTimingForTests overrides the collection window; the
// cmd-level test suite calls it from its TestMain to keep cycle-based tests
// fast.
func SetMatrixBatchTimingForTests(quiet, maxWait, step time.Duration) {
	matrixBatchQuiet, matrixBatchMaxWait, matrixBatchPollStep = quiet, maxWait, step
}

func (p *matrixPoller) Poll(ctx context.Context, client *http.Client, settings map[string]string, cursor string) ([]InboundMessage, string, error) {
	homeserver, token, err := matrixCredentials(settings)
	if err != nil {
		return nil, "", err
	}
	if p.buffer == nil {
		p.buffer = newMatrixBatchBuffer()
	}
		if p.cursor == "" {
			p.cursor = cursor
		}
		exposed := cursor
		for {
			// While a group is buffering the long poll is replaced by short polls:
			// the release must happen within the quiet window, not after the next
			// 25-second quiet sync.
			timeout := int(matrixSyncTimeout.Seconds())
			if p.buffer.pending() {
				timeout = 0
			}
			messages, next, err := matrixSync(ctx, client, homeserver, token, p.cursor, timeout)
			if err != nil && p.cursor != "" && matrixUnknownSince(err) {
				// The homeserver forgot the cursor (restart, retention): fall back
				// to a bounded initial sync instead of erroring forever.
				messages, next, err = matrixSync(ctx, client, homeserver, token, "", timeout)
			}
			if err != nil {
				return nil, "", err
			}
			p.cursor = next
			now := time.Now()
			for _, message := range messages {
				if p.buffer.contains(message.EventID) {
					continue // replayed while buffering — already parked
				}
				p.buffer.add(message, now)
			}
			released := p.buffer.flushDue(now)
			if !p.buffer.pending() {
				// Everything seen so far has been released — the position is safe
				// to expose. While any group still buffers, the exposed cursor
				// stays behind: a crash redelivers those events instead of
				// silently dropping a half-collected message.
				exposed = p.cursor
			}
			if len(released) > 0 {
				return released, exposed, nil
			}
			if p.buffer.pending() {
				select {
				case <-ctx.Done():
					return nil, "", ctx.Err()
				case <-time.After(matrixBatchPollStep):
				}
				continue
			}
			return nil, exposed, nil
		}
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
func matrixSync(ctx context.Context, client *http.Client, homeserver, token, cursor string, timeoutSeconds int) ([]MatrixInboundMessage, string, error) {
	endpoint := homeserver + "/_matrix/client/v3/sync?timeout=" + strconv.Itoa(timeoutSeconds)
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

// matrixBatchBuffer parks (chat, sender) groups between syncs: Element's
// multi-file message is several independent room events, often split across
// sync batches by upload times, and each alone would start its own intake
// run. Groups release through batch coalescing once the sender goes quiet.
type matrixBatchBuffer struct {
	mu     sync.Mutex
	groups map[string]*matrixBatch
}

type matrixBatch struct {
	messages []MatrixInboundMessage
	first    time.Time
	last     time.Time
}

func newMatrixBatchBuffer() *matrixBatchBuffer {
	return &matrixBatchBuffer{groups: map[string]*matrixBatch{}}
}

func (b *matrixBatchBuffer) add(message MatrixInboundMessage, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	key := message.RoomID + "\x00" + message.Sender
	batch := b.groups[key]
	if batch == nil {
		batch = &matrixBatch{first: now}
		b.groups[key] = batch
	}
	batch.messages = append(batch.messages, message)
	batch.last = now
}

func (b *matrixBatchBuffer) contains(eventID string) bool {
	if eventID == "" {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, batch := range b.groups {
		for _, message := range batch.messages {
			if message.EventID == eventID {
				return true
			}
		}
	}
	return false
}

func (b *matrixBatchBuffer) pending() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.groups) > 0
}

// flushDue releases groups whose sender went quiet (or whose wait cap
// tripped) and runs each through batch coalescing — plain texts stay separate
// messages, bare attachments join their nearest anchor.
func (b *matrixBatchBuffer) flushDue(now time.Time) []InboundMessage {
	b.mu.Lock()
	defer b.mu.Unlock()
	var released []InboundMessage
	for key, batch := range b.groups {
		if now.Sub(batch.last) < matrixBatchQuiet && now.Sub(batch.first) < matrixBatchMaxWait {
			continue
		}
		delete(b.groups, key)
		released = append(released, matrixNormalized(batch.messages)...)
	}
	return released
}
