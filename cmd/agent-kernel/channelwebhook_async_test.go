package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/workspace"
)

// Async-ack webhook tests: the sender's 200 lands after the durable dedup
// row, while the routing pipeline (intake chain, reply signals) continues on
// the receiver's worker pool. Unlike channelwebhook_test.go these scenarios
// go through a real http.Server — the async pipeline arms only for server
// requests (serverAcked), direct handle calls keep the legacy contract.

// intakeRecorder stands in for the intake run start: it counts calls per
// message and optionally holds each call until the test releases it (a slow
// downstream).
type intakeRecorder struct {
	mu     sync.Mutex
	counts map[string]int
	hold   <-chan struct{}
}

func (rec *intakeRecorder) start(_ context.Context, message agent.InboundMessage, _ string) (string, error) {
	if rec.hold != nil {
		<-rec.hold
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.counts == nil {
		rec.counts = map[string]int{}
	}
	rec.counts[message.ID]++
	return "intake-run/" + message.ID, nil
}

func (rec *intakeRecorder) total() int {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	n := 0
	for _, c := range rec.counts {
		n += c
	}
	return n
}

func (rec *intakeRecorder) count(messageID string) int {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return rec.counts[messageID]
}

// waitFor blocks until the recorder saw at least total intake calls.
func (rec *intakeRecorder) waitFor(t *testing.T, total int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for rec.total() < total {
		if time.Now().After(deadline) {
			t.Fatalf("intake recorded %d calls, want %d", rec.total(), total)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// asyncAckServer wraps a receiver in a real http.Server. The self identity is
// seeded (with the matching settings fingerprint) so the worker's hourly
// refresh short-circuits — no network in these tests.
func asyncAckServer(t *testing.T, store channelInboundStore, sig *recordedSignal, intake *intakeRecorder) (*channelWebhookReceiver, *httptest.Server) {
	t.Helper()
	driver := slackBotTestDriver(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	receiver := &channelWebhookReceiver{
		loop: &channelInboundLoop{
			driver:     driver,
			httpClient: http.DefaultClient,
			settings: func(context.Context) (agent.ChannelSettings, error) {
				return agent.ChannelSettings{Channels: map[string]map[string]string{
					"slackbot": {"bot_token": "xoxb-test", "signing_secret": "sekrit"},
				}}, nil
			},
			store:         store,
			sourceID:      "src",
			log:           logger,
			signal:        sig.record,
			intake:        intake.start,
			intakeEnabled: true,
		},
		hook: driver.Webhook(),
		log:  logger,
	}
	receiver.loop.selfID = "UBOT"
	receiver.loop.identityKey = agent.ChannelFingerprint(map[string]string{
		"bot_token": "xoxb-test", "signing_secret": "sekrit",
	})
	receiver.selfAt = time.Now()
	if testing.Verbose() {
		receiver.log = slog.New(slog.NewTextHandler(os.Stderr, nil))
		receiver.loop.log = receiver.log
	}
	mux := http.NewServeMux()
	mux.HandleFunc(receiver.hook.Route(), receiver.handle)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return receiver, server
}

// registeredSenderStore provides the closed-world user the intake pipeline
// identifies senders against (sender U1 writes into channel C1).
func registeredSenderStore() *fakeInboundStore {
	return &fakeInboundStore{
		users: []workspace.User{{
			ID: "u1", Name: "Human",
			Channels: []workspace.UserChannel{{Type: "slackbot", Address: "C1", SenderID: "U1", Enabled: true}},
		}},
	}
}

// postSignedEvent delivers one signed Events API envelope through the server
// and reports the response status.
func postSignedEvent(t *testing.T, serverURL, eventID, user, channel, text string) (int, error) {
	t.Helper()
	ts := strconv.FormatFloat(float64(time.Now().Unix())+0.0001, 'f', -1, 64)
	payload := `{"type":"event_callback","event_id":"` + eventID + `","event":{"type":"message","text":"` + text + `","user":"` + user + `","channel":"` + channel + `","ts":"` + ts + `"}}`
	req, err := http.NewRequest(http.MethodPost, serverURL+"/v1/channels/slackbot/events", strings.NewReader(payload))
	if err != nil {
		return 0, err
	}
	headerTS := strconv.FormatInt(time.Now().Unix(), 10)
	req.Header.Set("X-Slack-Request-Timestamp", headerTS)
	req.Header.Set("X-Slack-Signature", slackSignForTest("sekrit", headerTS, payload))
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

func mustPost(t *testing.T, serverURL, eventID string, wantStatus int) {
	t.Helper()
	status, err := postSignedEvent(t, serverURL, eventID, "U1", "C1", "подготовь смету")
	if err != nil {
		t.Fatalf("delivery %s: %v", eventID, err)
	}
	if status != wantStatus {
		t.Fatalf("delivery %s: expected %d, got %d", eventID, wantStatus, status)
	}
}

// TestChannelWebhookAsyncAckAnswersFast: the ack no longer waits for the
// intake chain — even with the downstream blocked, the sender gets its 200
// right after the durable dedup row, well inside the Events API's ~3s budget
// (the test asserts 100ms). The pipeline finishes on the worker.
func TestChannelWebhookAsyncAckAnswersFast(t *testing.T) {
	release := make(chan struct{})
	intake := &intakeRecorder{hold: release}
	_, server := asyncAckServer(t, registeredSenderStore(), &recordedSignal{}, intake)

	start := time.Now()
	mustPost(t, server.URL, "EvFast", http.StatusOK)
	elapsed := time.Since(start)
	if elapsed > 100*time.Millisecond {
		t.Fatalf("ack took %s with a blocked downstream — the pipeline leaked into the request", elapsed)
	}

	close(release)
	intake.waitFor(t, 1)
}

// TestChannelWebhookAsyncAckDeduplicatesRetries: the second delivery of the
// same event claims nothing — both deliveries ack 200, the pipeline routes
// exactly once.
func TestChannelWebhookAsyncAckDeduplicatesRetries(t *testing.T) {
	intake := &intakeRecorder{}
	store := registeredSenderStore()
	_, server := asyncAckServer(t, store, &recordedSignal{}, intake)

	mustPost(t, server.URL, "EvDup", http.StatusOK)
	mustPost(t, server.URL, "EvDup", http.StatusOK)

	intake.waitFor(t, 1)
	if calls := intake.total(); calls != 1 {
		t.Fatalf("retry must not re-route, got %d intake runs", calls)
	}
	if _, ok := store.inboundRow("EvDup"); !ok {
		t.Fatal("message not recorded")
	}
}

// TestChannelWebhookAsyncAckOverloadAnswers503: a full queue refuses the
// delivery with 503 without blocking — already-claimed messages keep
// processing, and the refused delivery still leaves its durable row. The
// channel's retry then routes through the unprocessed-row repair path:
// exactly once, no duplicates.
func TestChannelWebhookAsyncAckOverloadAnswers503(t *testing.T) {
	release := make(chan struct{})
	intake := &intakeRecorder{hold: release}
	store := registeredSenderStore()
	receiver, server := asyncAckServer(t, store, &recordedSignal{}, intake)
	receiver.workers, receiver.queue = 1, 1 // pin the pool before the first delivery arms it

	mustPost(t, server.URL, "Ev1", http.StatusOK) // occupies the single worker
	mustPost(t, server.URL, "Ev2", http.StatusOK) // fills the queue
	mustPost(t, server.URL, "Ev3", http.StatusServiceUnavailable)
	if _, ok := store.inboundRow("Ev3"); !ok {
		t.Fatal("refused delivery left no durable row")
	}

	close(release)
	intake.waitFor(t, 2)
	if intake.count("Ev3") != 0 {
		t.Fatal("refused event was processed anyway")
	}

	// The channel redelivers the refused event: the row exists, nothing
	// processed it — the repair path routes it exactly once.
	mustPost(t, server.URL, "Ev3", http.StatusOK)
	intake.waitFor(t, 3)
	for _, eventID := range []string{"Ev1", "Ev2", "Ev3"} {
		if n := intake.count(eventID); n != 1 {
			t.Fatalf("event %s routed %d times, want 1", eventID, n)
		}
	}
}

// outageStore simulates a storage outage on the dedup insert: the ack phase
// cannot land the durable row, so the receiver must refuse the delivery.
type outageStore struct {
	*fakeInboundStore
	outage atomic.Bool
}

func (s *outageStore) InsertInboundMessage(ctx context.Context, m *workspace.InboundMessage) (bool, error) {
	if s.outage.Load() {
		return false, errors.New("storage outage")
	}
	return s.fakeInboundStore.InsertInboundMessage(ctx, m)
}

// TestChannelWebhookAsyncAckStorageOutageAnswers503: without the durable row
// there is no at-least-once — a storage outage refuses the delivery (503)
// instead of acking a message that would be lost behind a premature 200. The
// channel's retry after recovery routes normally, exactly once.
func TestChannelWebhookAsyncAckStorageOutageAnswers503(t *testing.T) {
	intake := &intakeRecorder{}
	store := &outageStore{fakeInboundStore: registeredSenderStore()}
	store.outage.Store(true)
	_, server := asyncAckServer(t, store, &recordedSignal{}, intake)

	mustPost(t, server.URL, "EvOut", http.StatusServiceUnavailable)
	if intake.total() != 0 {
		t.Fatal("unaccepted delivery was processed anyway")
	}

	store.outage.Store(false) // the database recovers; the channel redelivers
	mustPost(t, server.URL, "EvOut", http.StatusOK)
	intake.waitFor(t, 1)
	if n := intake.count("EvOut"); n != 1 {
		t.Fatalf("event EvOut routed %d times after recovery, want 1", n)
	}
}

// TestChannelWebhookAsyncAckConcurrentPosts: parallel deliveries under -race.
// Duplicates racing the same claim (the seen map is the claim, guarded by the
// receiver mutex) must still route exactly once.
func TestChannelWebhookAsyncAckConcurrentPosts(t *testing.T) {
	intake := &intakeRecorder{}
	_, server := asyncAckServer(t, registeredSenderStore(), &recordedSignal{}, intake)

	const unique, copies = 12, 3
	var wg sync.WaitGroup
	for i := 0; i < unique*copies; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			mustPost(t, server.URL, fmt.Sprintf("EvRace%d", i%unique), http.StatusOK)
		}(i)
	}
	wg.Wait()

	intake.waitFor(t, unique)
	for id := 0; id < unique; id++ {
		if n := intake.count(fmt.Sprintf("EvRace%d", id)); n != 1 {
			t.Fatalf("event EvRace%d routed %d times, want 1", id, n)
		}
	}
}
