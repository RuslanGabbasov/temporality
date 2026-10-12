package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/temporality-project/temporality/kernel/agent"
)

// channelWebhookReceiver feeds one push channel (docs/channels.md §3, §5)
// into the shared inbound pipeline: verify → parse → dedup → reply matching
// → intake. It owns a channelInboundLoop value for the routing core (never
// its poll cycle — nothing polls here) and serializes requests under a mutex:
// channels retry on non-2xx answers, so concurrent deliveries of the same
// event must queue behind the dedup row instead of racing it.
type channelWebhookReceiver struct {
	mu     sync.Mutex
	loop   *channelInboundLoop
	hook   agent.ChannelWebhook
	log    *slog.Logger
	selfAt time.Time // when the cached self identity was last refreshed
	// seen maps recently routed message ids to their expiry — the webhook
	// twin of the poller's cursor. Channels retry deliveries of the same
	// event for about a minute; the map absorbs those retries within the
	// process lifetime. After a restart it is empty: a replayed event routes
	// again (at-least-once) and the workflow side already tolerates the
	// duplicate answer signal.
	seen map[string]time.Time
}

// handle is the mux entry of the channel's webhook route.
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
	// The pipeline runs before the acknowledgement: a mid-processing crash
	// makes the channel retry (the dedup row absorbs the duplicate) instead
	// of losing the message behind a premature 200.
	r.process(req.Context(), messages, channelSettings)
	r.hook.Respond(w, req, body)
}

// process routes one parsed batch under the receiver lock. The self identity
// (when the driver resolves one) refreshes on the fingerprint change the
// loop tracks and at most once an hour otherwise — settings reloads above
// keep the map current, this caches only the identity lookup.
func (r *channelWebhookReceiver) process(ctx context.Context, messages []agent.InboundMessage, channelSettings map[string]string) {
	if len(messages) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	// Reply routing has no durable dedup row (the row lands with the intake
	// pipeline only), so retries of the same event would re-signal the run.
	// The seen map is the in-process fence; expired entries sweep inline.
	fresh := make([]agent.InboundMessage, 0, len(messages))
	if r.seen == nil {
		r.seen = make(map[string]time.Time, len(messages))
	} else {
		for id, expires := range r.seen {
			if now.After(expires) {
				delete(r.seen, id)
			}
		}
	}
	for _, message := range messages {
		if _, dup := r.seen[message.ID]; dup {
			r.log.Info("channel webhook duplicate", "channel", r.loop.driver.Type(), "message", message.ID)
			continue
		}
		r.seen[message.ID] = now.Add(10 * time.Minute)
		fresh = append(fresh, message)
	}
	if len(fresh) == 0 {
		return
	}
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
	r.loop.routeMessages(ctx, fresh, channelSettings)
}
