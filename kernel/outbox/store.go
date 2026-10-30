package outbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/temporality-project/temporality/observation"
)

type Store struct {
	pool   *pgxpool.Pool
	url    string
	client *http.Client
}

func Open(ctx context.Context, databaseURL, temporalityURL string) (*Store, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	if err = pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	// The journal may require bearer auth (JOURNAL_AUTH_TOKENS); the kernel
	// publisher authenticates with TEMPORALITY_API_TOKEN. The token is read
	// per delivery, not captured: the background re-resolver may fill it long
	// after boot (journal boot-order race recovery).
	return &Store{pool: pool, url: temporalityURL, client: &http.Client{Timeout: 5 * time.Second}}, nil
}

func (s *Store) Close() { s.pool.Close() }

func (s *Store) Migrate(ctx context.Context, path string) error {
	// The migration is deliberately applied by the kernel binary, not by the
	// Temporality runtime. This keeps producer-owned recovery state separate.
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, string(data))
	return err
}

func (s *Store) Enqueue(ctx context.Context, event observation.Event) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	result, err := s.pool.Exec(ctx, `INSERT INTO kernel_event_outbox(source_id,event_id,payload) VALUES($1,$2,$3) ON CONFLICT(source_id,event_id) DO UPDATE SET last_error=kernel_event_outbox.last_error WHERE kernel_event_outbox.payload=EXCLUDED.payload`, event.Source.ID, event.EventID, payload)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("outbox key %s/%s already exists with different content", event.Source.ID, event.EventID)
	}
	return nil
}

const (
	// maxClaimBatch bounds a single claim CTE: enough to amortize POSTs over
	// the journal's ingest batch limit without widening the SKIP LOCKED window
	// too much.
	maxClaimBatch = 50
	// maxEventsPerPost matches the journal ingest contract: a request carries
	// between 1 and 100 events (observation/httpapi appendObservations).
	maxEventsPerPost = 100
	// cleanupBatchSize bounds a single DELETE of delivered rows.
	cleanupBatchSize = 1000
)

const claimEventsQuery = `WITH candidate AS (
	SELECT source_id,event_id FROM kernel_event_outbox
	WHERE delivered_at IS NULL AND next_attempt_at <= clock_timestamp()
	  AND (claimed_until IS NULL OR claimed_until < clock_timestamp())
	ORDER BY created_at,source_id,event_id LIMIT $1 FOR UPDATE SKIP LOCKED
) UPDATE kernel_event_outbox AS o SET claimed_until=clock_timestamp()+interval '30 seconds'
FROM candidate c WHERE o.source_id=c.source_id AND o.event_id=c.event_id
RETURNING o.source_id,o.event_id,o.payload`

// claimedEvent is one outbox row leased for delivery; Payload is the stored
// event JSON, forwarded to the journal verbatim.
type claimedEvent struct {
	SourceID string
	EventID  string
	Payload  json.RawMessage
}

func (s *Store) claimEvents(ctx context.Context, limit int) ([]claimedEvent, error) {
	rows, err := s.pool.Query(ctx, claimEventsQuery, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var claimed []claimedEvent
	for rows.Next() {
		var event claimedEvent
		if err := rows.Scan(&event.SourceID, &event.EventID, &event.Payload); err != nil {
			return nil, err
		}
		claimed = append(claimed, event)
	}
	return claimed, rows.Err()
}

// PublishBatch claims up to maxClaimBatch due events and delivers them to the
// journal, at most maxEventsPerPost events per POST. It reports how many
// events were marked delivered. A non-nil error means the batch was not fully
// delivered: successfully delivered events are already marked, failed ones
// keep the single-event failure semantics (backoff + last_error, lease
// release), and events never attempted keep their 30-second claim lease.
func (s *Store) PublishBatch(ctx context.Context) (int, error) {
	return s.publishBatch(ctx, maxClaimBatch)
}

// PublishOne delivers exactly one event; kept as a thin wrapper over the
// batching path for compatibility with the previous API.
func (s *Store) PublishOne(ctx context.Context) (bool, error) {
	published, err := s.publishBatch(ctx, 1)
	return published > 0, err
}

// errEventRejected marks per-event ingest rejections: the journal answered,
// so the remaining chunks may still succeed and publishing continues.
var errEventRejected = errors.New("event ingest rejected")

// quarantineAfterAttempts: an event rejected this many times by the journal
// itself (not transport!) is quarantined instead of blocking the queue.
const quarantineAfterAttempts = 10

func (s *Store) publishBatch(ctx context.Context, limit int) (int, error) {
	claimed, err := s.claimEvents(ctx, limit)
	if err != nil || len(claimed) == 0 {
		return 0, err
	}
	delivered := 0
	var firstErr error
	for _, chunk := range chunkEvents(claimed, maxEventsPerPost) {
		count, err := s.deliverChunk(ctx, chunk)
		delivered += count
		if err == nil {
			continue
		}
		if firstErr == nil {
			firstErr = err
		}
		// A transport/HTTP failure predicts failures of the next POST too:
		// stop and leave unprocessed events on their claim lease; they retry
		// after it expires (30 seconds).
		if !errors.Is(err, errEventRejected) {
			break
		}
	}
	return delivered, firstErr
}

func (s *Store) deliverChunk(ctx context.Context, chunk []claimedEvent) (int, error) {
	body, err := frameEventsPost(chunk)
	if err != nil {
		return 0, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url+"/v1/observations/events", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	request.Header.Set("Content-Type", "application/json")
	if token := strings.TrimSpace(os.Getenv("TEMPORALITY_API_TOKEN")); token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := s.client.Do(request)
	if err != nil {
		s.failChunk(ctx, chunk, err.Error())
		return 0, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		err = fmt.Errorf("Temporality event ingest returned HTTP %d", response.StatusCode)
		s.failChunk(ctx, chunk, err.Error())
		return 0, err
	}
	responseBody, readErr := io.ReadAll(response.Body)
	if readErr != nil {
		s.failChunk(ctx, chunk, invalidIngestResponseReason(readErr))
		return 0, readErr
	}
	results, err := parseIngestResponse(responseBody)
	if err != nil {
		s.failChunk(ctx, chunk, invalidIngestResponseReason(err))
		return 0, err
	}
	deliveredEvents, failedEvents := resolveIngestOutcome(chunk, results)
	for _, failed := range failedEvents {
		s.noteFailure(ctx, failed.Event.SourceID, failed.Event.EventID, failed.Reason)
	}
	// Poison isolation: a permanently rejected event (validation, duplicate id
	// shape, …) must NOT hold the queue hostage. The journal answered, so the
	// transport is fine — mark the rejects delivered-side dead (quarantine
	// after N attempts) and keep delivering the rest. Without this, one bad
	// event shape froze the whole outbox for two days: 19k events queued
	// behind a single poisoned row while runs, traces and the chat starved.
	for _, failed := range failedEvents {
		s.quarantineIfHopeless(ctx, failed.Event.SourceID, failed.Event.EventID)
	}
	if len(failedEvents) > 0 && len(deliveredEvents) == 0 {
		return 0, fmt.Errorf("%w: %s", errEventRejected, failedEvents[0].Reason)
	}
	if err := s.markDeliveredBatch(ctx, deliveredEvents); err != nil {
		return 0, err
	}
	return len(deliveredEvents), nil
}

// quarantineIfHopeless buries an event that failed delivery enough times:
// it is marked delivered with a quarantine flag instead of blocking the
// queue forever. The row keeps its last_error for the post-mortem; the
// event itself is lost by design — a poison event must cost itself, not
// the whole pipeline.
func (s *Store) quarantineIfHopeless(ctx context.Context, sourceID, eventID string) {
	_, _ = s.pool.Exec(ctx, `UPDATE kernel_event_outbox
		SET delivered_at = clock_timestamp(), last_error = last_error || ' [quarantined: permanently rejected]'
		WHERE source_id = $1 AND event_id = $2 AND attempts >= $3`,
		sourceID, eventID, quarantineAfterAttempts)
}

// failChunk applies the single-event failure semantics to every event of a
// chunk that did not reach the journal.
func (s *Store) failChunk(ctx context.Context, chunk []claimedEvent, message string) {
	for _, event := range chunk {
		s.noteFailure(ctx, event.SourceID, event.EventID, message)
	}
}

func (s *Store) noteFailure(ctx context.Context, sourceID, eventID, message string) {
	_, _ = s.pool.Exec(ctx, `UPDATE kernel_event_outbox SET attempts=attempts+1,last_error=$3,claimed_until=NULL,next_attempt_at=clock_timestamp()+LEAST(interval '5 minutes', interval '1 second' * power(2,LEAST(attempts,8))) WHERE source_id=$1 AND event_id=$2`, sourceID, eventID, message)
}

func (s *Store) markDeliveredBatch(ctx context.Context, events []claimedEvent) error {
	if len(events) == 0 {
		return nil
	}
	sourceIDs := make([]string, len(events))
	eventIDs := make([]string, len(events))
	for i, event := range events {
		sourceIDs[i] = event.SourceID
		eventIDs[i] = event.EventID
	}
	_, err := s.pool.Exec(ctx, `UPDATE kernel_event_outbox SET delivered_at=clock_timestamp(),attempts=attempts+1,last_error='',claimed_until=NULL
		WHERE (source_id,event_id) IN (SELECT * FROM unnest($1::text[],$2::text[]))`, sourceIDs, eventIDs)
	return err
}

// chunkEvents splits claimed events into contiguous chunks of at most limit
// events, preserving order.
func chunkEvents(events []claimedEvent, limit int) [][]claimedEvent {
	if limit <= 0 {
		limit = 1
	}
	var chunks [][]claimedEvent
	for start := 0; start < len(events); start += limit {
		end := min(start+limit, len(events))
		chunks = append(chunks, events[start:end])
	}
	return chunks
}

type eventsPostBody struct {
	Events []json.RawMessage `json:"events"`
}

// frameEventsPost builds the journal ingest request body: the raw stored
// payloads embedded verbatim as {"events":[raw1,raw2,...]}.
func frameEventsPost(chunk []claimedEvent) ([]byte, error) {
	events := make([]json.RawMessage, len(chunk))
	for i, event := range chunk {
		events[i] = event.Payload
	}
	return json.Marshal(eventsPostBody{Events: events})
}

// ingestEventResult mirrors one per-event entry of the journal ingest
// response: status is accepted, repeated or rejected, error is set for
// rejections.
type ingestEventResult struct {
	Status string `json:"status"`
	Error  string `json:"error"`
}

// parseIngestResponse decodes the ingest response envelope {"results":[...]}
// — the format PublishOne has always consumed.
func parseIngestResponse(body []byte) ([]ingestEventResult, error) {
	var envelope struct {
		Results []ingestEventResult `json:"results"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}
	return envelope.Results, nil
}

// isDeliveredStatus reports whether the journal durably holds the event:
// accepted on first write, repeated on an idempotent re-delivery.
func isDeliveredStatus(status string) bool {
	return status == "accepted" || status == "repeated"
}

const invalidIngestResponsePrefix = "invalid Temporality ingest response"

func invalidIngestResponseReason(err error) string {
	return invalidIngestResponsePrefix + ": " + err.Error()
}

// rejectedEventReason keeps the PublishOne rejection message, including the
// server-provided detail when present.
func rejectedEventReason(result ingestEventResult) string {
	reason := "Temporality rejected event"
	if result.Error != "" {
		reason += ": " + result.Error
	}
	return reason
}

type failedEvent struct {
	Event  claimedEvent
	Reason string
}

// resolveIngestOutcome pairs claimed events with per-event ingest results.
// The results are positional: results[i] describes chunk[i]. When the counts
// disagree the contract is broken and nothing is marked delivered — a later
// re-delivery is safe (the journal dedupes by source/event id), marking an
// event delivered that never landed is not.
func resolveIngestOutcome(chunk []claimedEvent, results []ingestEventResult) (delivered []claimedEvent, failed []failedEvent) {
	if len(results) != len(chunk) {
		for _, event := range chunk {
			failed = append(failed, failedEvent{Event: event, Reason: rejectedEventReason(ingestEventResult{})})
		}
		return nil, failed
	}
	for i, event := range chunk {
		if isDeliveredStatus(results[i].Status) {
			delivered = append(delivered, event)
			continue
		}
		failed = append(failed, failedEvent{Event: event, Reason: rejectedEventReason(results[i])})
	}
	return delivered, failed
}

// keepDraining is the publisher loop policy carried over from the single-event
// publisher: pull the next batch immediately while deliveries keep succeeding,
// wait on the ticker after an empty claim or any failure.
func keepDraining(published int, err error) bool {
	return err == nil && published > 0
}

// Stats reports delivery reconciliation state for operators: how many events
// are pending and how long the oldest has been waiting.
type Stats struct {
	Pending            int64
	OldestPendingAge   time.Duration // zero when nothing is pending
	Delivered          int64
	FailedAttemptsLast string // last_error of the most recently failed attempt, if any
}

func (s *Store) Stats(ctx context.Context) (Stats, error) {
	var stats Stats
	var oldest *time.Time
	err := s.pool.QueryRow(ctx, `SELECT
		count(*) FILTER (WHERE delivered_at IS NULL),
		min(created_at) FILTER (WHERE delivered_at IS NULL),
		count(*) FILTER (WHERE delivered_at IS NOT NULL),
		COALESCE((SELECT last_error FROM kernel_event_outbox WHERE last_error <> '' ORDER BY attempts DESC, created_at DESC LIMIT 1), '')
		FROM kernel_event_outbox`).Scan(&stats.Pending, &oldest, &stats.Delivered, &stats.FailedAttemptsLast)
	if err != nil {
		return stats, err
	}
	if oldest != nil {
		stats.OldestPendingAge = time.Since(*oldest).Round(time.Second)
	}
	return stats, nil
}

// CleanupDelivered removes delivered outbox rows older than the given age so
// the producer's recovery table does not grow without bound. The journal
// keeps the full event history; the outbox only needs recent recovery state.
// It deletes in bounded batches until nothing qualifies and reports the total
// number of rows removed.
func (s *Store) CleanupDelivered(ctx context.Context, olderThan time.Duration) (int64, error) {
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		tag, err := s.pool.Exec(ctx, `DELETE FROM kernel_event_outbox
			WHERE (source_id,event_id) IN (
				SELECT source_id,event_id FROM kernel_event_outbox
				WHERE delivered_at IS NOT NULL
				  AND delivered_at <= clock_timestamp() - ($1::float8 * interval '1 second')
				ORDER BY delivered_at,source_id,event_id
				LIMIT $2
			)`, olderThan.Seconds(), cleanupBatchSize)
		if err != nil {
			return total, err
		}
		deleted := tag.RowsAffected()
		total += deleted
		if deleted < cleanupBatchSize {
			return total, nil
		}
	}
}

func (s *Store) RunPublisher(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		published, err := s.PublishBatch(ctx)
		if keepDraining(published, err) {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
