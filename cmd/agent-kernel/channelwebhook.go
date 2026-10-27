package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"runtime/debug"
	"strconv"
	"sync"
	"time"

	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/workspace"
)

// channelWebhookReceiver feeds one push channel (docs/channels.md §3, §5)
// into the shared inbound pipeline: verify → parse → durable dedup row →
// ack → route (reply matching / intake) on a worker pool. It owns a
// channelInboundLoop value for the routing core (never its poll cycle —
// nothing polls here).
//
// The ack contract follows the Events API's ~3s budget: the synchronous part
// of handle ends at the durable dedup row (one INSERT, the unique
// (channel, message_id) key absorbing channel retries at the database), the
// response goes out, and the 15-20 sequential DB/HTTP/Temporal calls of the
// routing pipeline continue on a bounded worker pool. A crashed or overloaded
// kernel therefore never holds a delivery hostage: the row already in the
// database makes the channel's redelivery a dedup no-op (or, for a delivery
// that never got routed, a repair), and a full queue answers 503 instead of
// queueing behind the mutex the way the previous whole-pipeline lock did.
//
// Direct handle callers — unit tests, embedded wiring — keep the previous
// synchronous contract (route fully, then respond); see serverAcked.
type channelWebhookReceiver struct {
	mu     sync.Mutex
	loop   *channelInboundLoop
	hook   agent.ChannelWebhook
	log    *slog.Logger
	selfAt time.Time // when the cached self identity was last refreshed
	// seen maps recently routed message ids to their expiry — the webhook
	// twin of the poller's cursor. Channels retry deliveries of the same
	// event for about a minute; the map absorbs those retries within the
	// process lifetime. It doubles as the async-ack claim: an entry is set
	// when a delivery is accepted and rolled back when the queue refuses it,
	// so a refused message retries into the repair path instead of being
	// lost or routed twice. After a restart it is empty: a replayed event
	// routes again (at-least-once) and the workflow side already tolerates
	// the duplicate answer signal.
	seen map[string]time.Time

	// Async-ack worker pool, armed lazily by the first server-request handle
	// call. Sizes come from env (KERNEL_WEBHOOK_WORKERS / KERNEL_WEBHOOK_QUEUE);
	// pre-setting the fields before the first delivery pins them (tests).
	poolOnce sync.Once
	jobs     chan webhookJob
	workers  int
	queue    int
}

// webhookJob is one accepted delivery: a single message whose dedup row is
// already durable, plus the loop state snapshot the worker routes it with.
type webhookJob struct {
	ctx      context.Context // detached from the HTTP request (it ends at the ack)
	settings map[string]string
	message  agent.InboundMessage
	// adopt names the dedup row the routing pass must treat as its own
	// first insert (the ack phase wrote it): handleIntake gates on
	// InsertInboundMessage, and adopting turns that gate open for the row
	// instead of skipping the message as a duplicate.
	adopt map[string]struct{}
}

// webhookAckStore wraps the loop's store for one job: InsertInboundMessage
// reports first-insert for the rows the ack phase already wrote, everything
// else delegates untouched. The map belongs to a single job — one worker, and
// duplicate deliveries of the same message never get past the seen claim.
type webhookAckStore struct {
	channelInboundStore
	adopt map[string]struct{}
}

func (s webhookAckStore) InsertInboundMessage(ctx context.Context, m *workspace.InboundMessage) (bool, error) {
	if _, ok := s.adopt[m.ID]; ok {
		delete(s.adopt, m.ID)
		return true, nil
	}
	return s.channelInboundStore.InsertInboundMessage(ctx, m)
}

// inboundRowReader is the optional store surface the dedup repair path uses
// to tell an unprocessed fence row from one the pipeline already advanced.
// *workspace.Store satisfies it; test fakes may not.
type inboundRowReader interface {
	GetInboundMessage(ctx context.Context, id string) (workspace.InboundMessage, error)
}

// serverAcked reports whether the request arrived through a real http.Server
// (net/http tags every server request's context with ServerContextKey). Only
// then does the early ack make sense: the connection outlives this call and
// the worker pool owns the rest of the pipeline. Direct handle callers get
// the legacy synchronous pipeline, where the response still proves the batch
// was fully routed.
func serverAcked(req *http.Request) bool {
	return req.Context().Value(http.ServerContextKey) != nil
}

// webhookEnvInt reads one positive int from env, falling back to def.
func webhookEnvInt(name string, def int) int {
	if raw := os.Getenv(name); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// ensurePool arms the worker pool once: workers pull jobs off the bounded
// queue for the rest of the process lifetime.
func (r *channelWebhookReceiver) ensurePool() {
	r.poolOnce.Do(func() {
		if r.workers <= 0 {
			r.workers = webhookEnvInt("KERNEL_WEBHOOK_WORKERS", 8)
		}
		if r.queue <= 0 {
			r.queue = webhookEnvInt("KERNEL_WEBHOOK_QUEUE", 256)
		}
		r.jobs = make(chan webhookJob, r.queue)
		for i := 0; i < r.workers; i++ {
			go r.worker()
		}
	})
}

// handle is the mux entry of the channel's webhook route. Signature, settings
// and the durable dedup row stay synchronous; the routing pipeline (reply
// matching, intake runs — the slow tail) continues on a worker after the ack.
func (r *channelWebhookReceiver) handle(w http.ResponseWriter, req *http.Request) {
	body, err := io.ReadAll(io.LimitReader(req.Body, 1<<20))
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}
	// Same settings precedence as delivery and the poll cycle: database
	// overlay over env, reloaded per request so admin changes apply live.
	settings := agent.ChannelSettingsFromEnv()
	if r.loop.settings != nil {
		if stored, sErr := r.loop.settings(req.Context()); sErr == nil {
			settings = stored.OverlayEnv()
		}
	}
	channelSettings := settings.Channels[r.loop.driver.Type()]
	if !r.loop.driver.IsConfigured(channelSettings) {
		http.Error(w, "channel not configured", http.StatusServiceUnavailable)
		return
	}
	if vErr := r.hook.VerifyRequest(req, body, channelSettings); vErr != nil {
		r.log.Warn("channel webhook rejected", "channel", r.loop.driver.Type(), "error", vErr)
		http.Error(w, "verification failed", http.StatusForbidden)
		return
	}
	messages := r.hook.Parse(req, body)
	if !serverAcked(req) {
		// Direct caller: the legacy synchronous contract — process the whole
		// batch before answering.
		r.process(req.Context(), messages, channelSettings)
		r.hook.Respond(w, req, body)
		return
	}
	if len(messages) > 0 {
		// The durable rows land before the acknowledgement: a kernel that
		// dies right after the 200 leaves the messages recorded (the operator
		// sees them in the inbox; the pipeline's expiry sweep retires them),
		// and the channel's redelivery of the same event dedups at the
		// database instead of duplicating work.
		r.ensurePool()
		if !r.claimAck(req.Context(), messages, channelSettings) {
			// Queue full — refuse without blocking. The channel redelivers;
			// the rows already written route the retry through the repair
			// path, so the redelivery completes the work instead of
			// duplicating it.
			http.Error(w, "webhook busy", http.StatusServiceUnavailable)
			return
		}
	}
	r.hook.Respond(w, req, body)
}

// claimAck claims every unseen message for asynchronous routing and lands its
// durable dedup row (the claim and the insert share one critical section, so
// concurrent deliveries of the same event cannot both pass). The seen claim
// is rolled back when the queue refuses the job: the refused message's row
// stays behind as an unprocessed fence, and the channel's retry lands in the
// repair path instead of being lost. Reports whether every message was
// durably accepted — a full queue or a storage failure both answer 503, the
// channel redelivers, nothing is lost.
func (r *channelWebhookReceiver) claimAck(ctx context.Context, messages []agent.InboundMessage, channelSettings map[string]string) bool {
	r.mu.Lock()
	now := time.Now()
	r.sweepSeenLocked(now)
	var jobs []webhookJob
	ok := true
	for _, message := range messages {
		if _, dup := r.seen[message.ID]; dup {
			r.log.Info("channel webhook duplicate", "channel", r.loop.driver.Type(), "message", message.ID)
			continue
		}
		r.seen[message.ID] = now.Add(10 * time.Minute)
		// Same row shape the intake pipeline writes: the routing pass adopts
		// it as its own insert and the state machine continues on it.
		row := &workspace.InboundMessage{
			ID:        workspace.InboundMessageID(message.Channel, message.ID),
			Channel:   message.Channel,
			MessageID: message.ID,
			SenderRaw: message.Sender,
			Room:      message.ChatID,
			Body:      truncateForLog(message.Text, 8000),
			Status:    workspace.InboundStatusReceived,
		}
		inserted, err := r.loop.store.InsertInboundMessage(ctx, row)
		if err != nil {
			// Without the durable row there is no at-least-once: refuse the
			// delivery and let the channel's redelivery be the retry.
			r.log.Warn("channel webhook insert", "channel", r.loop.driver.Type(), "message", message.ID, "error", err)
			delete(r.seen, message.ID)
			ok = false
			continue
		}
		job := webhookJob{
			ctx:      context.WithoutCancel(ctx),
			settings: channelSettings,
			message:  message,
			// The row exists now (the ack phase wrote it): the routing pass's
			// own InsertInboundMessage gate must read it as its first insert,
			// or handleIntake would silently skip the message as a duplicate.
			adopt: map[string]struct{}{row.ID: {}},
		}
		if !inserted {
			// The row predates this delivery: a retry this process dropped on
			// a full queue, or a post-restart replay of a message the
			// previous process acknowledged. Route only rows that never
			// progressed; genuinely handled messages answer as duplicates.
			route, adopt, retryable := r.repairFenceLocked(ctx, row.ID)
			if !route {
				delete(r.seen, message.ID)
				if retryable {
					ok = false // fence state unknown: refuse, let the channel retry
				} else {
					r.log.Info("channel webhook duplicate", "channel", r.loop.driver.Type(), "message", message.ID)
				}
				continue
			}
			if !adopt {
				job.adopt = nil // row vanished: the pipeline inserts anew
			}
		}
		jobs = append(jobs, job)
	}
	r.mu.Unlock()

	for _, job := range jobs {
		select {
		case r.jobs <- job:
		default:
			ok = false
			r.mu.Lock()
			delete(r.seen, job.message.ID)
			r.mu.Unlock()
			r.log.Warn("channel webhook queue full", "channel", r.loop.driver.Type(), "message", job.message.ID)
		}
	}
	return ok
}

// repairFenceLocked classifies an already-present dedup row (called with the
// receiver mutex held): route it, and whether the routing pass must adopt the
// row as its own insert.
//
//   - received with no run pointers — an unprocessed fence (overflow retry,
//     crash after the ack): route and adopt;
//   - any progressed state (rejected, classified, routed, answered, expired)
//     or a run pointer — already handled: neither (the delivery answers as a
//     duplicate);
//   - row vanished — route, re-inserting through the pipeline;
//   - unreadable — retryable: the fence's state is unknown, the caller
//     refuses the delivery and the row and the channel's next delivery both
//     survive.
func (r *channelWebhookReceiver) repairFenceLocked(ctx context.Context, rowID string) (route, adopt, retryable bool) {
	reader, ok := r.loop.store.(inboundRowReader)
	if !ok {
		// Stores without the read surface (test fakes): every existing row
		// counts as unprocessed — the caller's scenario decides.
		return true, true, false
	}
	row, err := reader.GetInboundMessage(ctx, rowID)
	if err != nil {
		if errors.Is(err, workspace.ErrNotFound) {
			return true, false, false
		}
		r.log.Warn("channel webhook dedup read", "channel", r.loop.driver.Type(), "row", rowID, "error", err)
		return false, false, true
	}
	if row.Status != workspace.InboundStatusReceived || row.IntakeRunID != "" || row.TargetRunID != "" {
		return false, false, false
	}
	return true, true, false
}

// worker drains the job queue for the process lifetime. A panicking batch is
// logged with its stack and dropped: the HTTP ack is long gone (this
// goroutine is below no handler anymore), the dedup row stays unprocessed,
// and the channel's next delivery routes it again through the repair path.
func (r *channelWebhookReceiver) worker() {
	for job := range r.jobs {
		r.runJob(job)
	}
}

// runJob routes one accepted delivery. The identity refresh keeps the
// receiver mutex (token rotations invalidate the cached self identity; at
// most once an hour otherwise) — the slow routing tail runs on a private
// snapshot of the loop, so workers never share live routing state.
func (r *channelWebhookReceiver) runJob(job webhookJob) {
	defer func() {
		if recovered := recover(); recovered != nil {
			r.log.Error("channel webhook worker panic", "channel", r.loop.driver.Type(),
				"panic", recovered, "stack", string(debug.Stack()))
		}
	}()
	r.mu.Lock()
	loop := r.refreshIdentityLocked(job.ctx, job.settings)
	r.mu.Unlock()
	if len(job.adopt) > 0 {
		loop.store = webhookAckStore{channelInboundStore: loop.store, adopt: job.adopt}
	}
	loop.routeMessages(job.ctx, []agent.InboundMessage{job.message}, job.settings)
}

// process is the legacy synchronous pipeline for direct handle callers: seen
// dedup, identity refresh and the full route, then the response.
func (r *channelWebhookReceiver) process(ctx context.Context, messages []agent.InboundMessage, channelSettings map[string]string) {
	if len(messages) == 0 {
		return
	}
	r.mu.Lock()
	now := time.Now()
	r.sweepSeenLocked(now)
	fresh := make([]agent.InboundMessage, 0, len(messages))
	for _, message := range messages {
		if _, dup := r.seen[message.ID]; dup {
			r.log.Info("channel webhook duplicate", "channel", r.loop.driver.Type(), "message", message.ID)
			continue
		}
		r.seen[message.ID] = now.Add(10 * time.Minute)
		fresh = append(fresh, message)
	}
	if len(fresh) == 0 {
		r.mu.Unlock()
		return
	}
	loop := r.refreshIdentityLocked(ctx, channelSettings)
	r.mu.Unlock()
	loop.routeMessages(ctx, fresh, channelSettings)
}

// sweepSeenLocked drops expired seen entries inline (receiver mutex held).
func (r *channelWebhookReceiver) sweepSeenLocked(now time.Time) {
	if r.seen == nil {
		r.seen = make(map[string]time.Time)
		return
	}
	for id, expires := range r.seen {
		if now.After(expires) {
			delete(r.seen, id)
		}
	}
}

// refreshIdentityLocked refreshes the driver account identity under the
// receiver mutex and returns a private copy of the loop for the caller's
// routing pass: the fingerprint change the loop tracks and the hourly
// refresh, exactly as the synchronous pipeline always did.
func (r *channelWebhookReceiver) refreshIdentityLocked(ctx context.Context, channelSettings map[string]string) channelInboundLoop {
	if key := agent.ChannelFingerprint(channelSettings); key != r.loop.identityKey {
		r.loop.identityKey = key
		r.loop.selfID = ""
	}
	if resolver, ok := r.loop.driver.(agent.ChannelSelfResolver); ok && (r.loop.selfID == "" || time.Since(r.selfAt) > time.Hour) {
		// Token rotations may change the account identity; a failed resolve
		// retries on the next delivery (webhook hits are human-frequency).
		if selfID, err := resolver.Self(ctx, r.loop.httpClient, channelSettings); err != nil {
			r.log.Warn("channel webhook self", "channel", r.loop.driver.Type(), "error", err)
		} else {
			if selfID != r.loop.selfID {
				r.log.Info("channel webhook listening", "channel", r.loop.driver.Type(), "self", selfID)
			}
			r.loop.selfID = selfID
			r.selfAt = time.Now()
		}
	}
	return *r.loop
}
