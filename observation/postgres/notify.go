// PG LISTEN/NOTIFY wake-ups for journal consumers (SSE run streams).
//
// The channel name is a cross-process contract: every writer of
// observation_events (the journal service, the kernel, one-off ingest
// scripts) NOTIFYs observation_events_changed after an append, and every
// reader may LISTEN on its own dedicated connection. Notifications are pure
// hints: a lost or missed one must never lose data, only add latency, so
// every consumer keeps its periodic poll as the correctness fallback.
package postgres

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// observationEventsChanged is the NOTIFY channel the journal (and any other
// ingest process) fires on observation_events appends. The payload is
// optional: empty (or whitespace) means "something changed, wake everyone",
// any other value is the project id of the appended events.
const observationEventsChanged = "observation_events_changed"

const (
	// Reconnect backoff for the listener connection: exponential from the
	// base, capped at the max. A database restart must not spin the kernel,
	// but should also recover quickly.
	listenReconnectBase = 500 * time.Millisecond
	listenReconnectMax  = 30 * time.Second
	// Best-effort UNLISTEN before a connection goes back to the pool: a
	// pooled connection that still LISTENs would keep firing into a channel
	// nobody reads.
	unlistenTimeout = 2 * time.Second
)

// parseNotifyPayload resolves a notification payload to the project id it
// names. An empty result is the broadcast form: wake every project. Tolerant
// of writers that NOTIFY without a payload at all.
func parseNotifyPayload(payload string) string {
	return strings.TrimSpace(payload)
}

// coalesceSignal delivers one wake-up signal without blocking; when a signal
// is already pending, the new one is dropped — the consumer wakes once and
// reads everything that accumulated anyway.
func coalesceSignal(ch chan<- struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// SubscribeEvents LISTENs observation_events_changed on a dedicated pooled
// connection and returns a receive-only channel carrying one coalesced
// signal per notification (capacity 1: signals already queued are
// deduplicated). The returned func unsubscribes — the listener then UNLISTENs,
// closes the channel and releases the connection, so `for range ch`
// terminates; canceling ctx does the same. A broken connection reconnects
// with exponential backoff (500ms doubling up to 30s) and re-issues LISTEN;
// the channel stays the same throughout.
//
// If the initial connection or LISTEN fails, an error is returned and the
// caller simply keeps polling without wake-ups. Notifications that slip
// through a reconnect window are covered by that poll, never lost.
func (s *Store) SubscribeEvents(ctx context.Context) (<-chan struct{}, func(), error) {
	ch := make(chan struct{}, 1)
	stop, err := s.listenEvents(ctx,
		func(string) { coalesceSignal(ch) },
		func() { close(ch) })
	if err != nil {
		return nil, nil, err
	}
	return ch, stop, nil
}

// SubscribeEventProjects is the payload-aware form of SubscribeEvents: the
// channel carries the project id parsed from every notification payload
// (empty string = broadcast, wake all projects), so a multiplexer can wake
// only the streams of the project that actually changed. Coalescing follows
// the same buffer-1 rule as SubscribeEvents — under a notification burst some
// project ids may be dropped; consumers must treat deliveries as hints, not
// as an event log.
func (s *Store) SubscribeEventProjects(ctx context.Context) (<-chan string, func(), error) {
	ch := make(chan string, 1)
	stop, err := s.listenEvents(ctx,
		func(project string) {
			select {
			case ch <- project:
			default:
			}
		},
		func() { close(ch) })
	if err != nil {
		return nil, nil, err
	}
	return ch, stop, nil
}

// listenEvents runs the LISTEN loop: one dedicated connection, re-LISTENed
// after every reconnect, delivering parsed payload projects to deliver until
// ctx is canceled or stop is called; finish runs exactly once when the loop
// exits. Only the initial connection/LISTEN failure is reported as an error —
// later ones are retraced by the backoff loop until shutdown.
func (s *Store) listenEvents(ctx context.Context, deliver func(project string), finish func()) (func(), error) {
	lctx, cancel := context.WithCancel(ctx)
	conn, err := acquireListeningConn(lctx, s.pool)
	if err != nil {
		cancel()
		return nil, err
	}
	go func() {
		defer finish()
		defer cancel()
		defer func() {
			if conn != nil {
				releaseListeningConn(conn)
			}
		}()
		backoff := listenReconnectBase
		for {
			notification, err := conn.Conn().WaitForNotification(lctx)
			if err == nil {
				backoff = listenReconnectBase
				deliver(parseNotifyPayload(notification.Payload))
				continue
			}
			if lctx.Err() != nil {
				return
			}
			// The dedicated connection broke (database restart, network
			// drop): re-acquire and re-LISTEN with capped exponential
			// backoff. Anything missed until the reconnect lands is picked
			// up by consumers' polling fallback.
			releaseListeningConn(conn)
			conn = nil
			for {
				if lctx.Err() != nil {
					return
				}
				reconnected, err := acquireListeningConn(lctx, s.pool)
				if err == nil {
					conn = reconnected
					break
				}
				timer := time.NewTimer(backoff)
				select {
				case <-lctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
				backoff = min(backoff*2, listenReconnectMax)
			}
		}
	}()
	return cancel, nil
}

// acquireListeningConn takes a dedicated connection out of the pool and
// puts it into LISTEN state for the journal change channel.
func acquireListeningConn(ctx context.Context, pool *pgxpool.Pool) (*pgxpool.Conn, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("event listen: acquire connection: %w", err)
	}
	if _, err = conn.Exec(ctx, "LISTEN "+observationEventsChanged); err != nil {
		conn.Release()
		return nil, fmt.Errorf("event listen: LISTEN %s: %w", observationEventsChanged, err)
	}
	return conn, nil
}

// releaseListeningConn clears the LISTEN registration before the connection
// returns to the pool. Best-effort: on a broken connection the UNLISTEN
// fails silently and Release destroys the connection instead.
func releaseListeningConn(conn *pgxpool.Conn) {
	uctx, ucancel := context.WithTimeout(context.Background(), unlistenTimeout)
	_, _ = conn.Exec(uctx, "UNLISTEN *")
	ucancel()
	conn.Release()
}
