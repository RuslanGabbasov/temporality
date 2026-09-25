package outbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/temporality-project/temporality/observation"
)

type Store struct {
	pool   *pgxpool.Pool
	url    string
	token  string
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
	// publisher authenticates with TEMPORALITY_API_TOKEN.
	return &Store{pool: pool, url: temporalityURL, token: strings.TrimSpace(os.Getenv("TEMPORALITY_API_TOKEN")), client: &http.Client{Timeout: 5 * time.Second}}, nil
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

func (s *Store) PublishOne(ctx context.Context) (bool, error) {
	var sourceID, eventID string
	var payload []byte
	err := s.pool.QueryRow(ctx, `WITH candidate AS (
		SELECT source_id,event_id FROM kernel_event_outbox
		WHERE delivered_at IS NULL AND next_attempt_at <= clock_timestamp()
		  AND (claimed_until IS NULL OR claimed_until < clock_timestamp())
		ORDER BY created_at,source_id,event_id LIMIT 1 FOR UPDATE SKIP LOCKED
	) UPDATE kernel_event_outbox AS o SET claimed_until=clock_timestamp()+interval '30 seconds'
	FROM candidate c WHERE o.source_id=c.source_id AND o.event_id=c.event_id
	RETURNING o.source_id,o.event_id,o.payload`).Scan(&sourceID, &eventID, &payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	body, err := json.Marshal(map[string]any{"events": []json.RawMessage{payload}})
	if err != nil {
		return false, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url+"/v1/observations/events", bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	request.Header.Set("Content-Type", "application/json")
	if s.token != "" {
		request.Header.Set("Authorization", "Bearer "+s.token)
	}
	response, err := s.client.Do(request)
	if err != nil {
		s.noteFailure(ctx, sourceID, eventID, err.Error())
		return false, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		err = fmt.Errorf("Temporality event ingest returned HTTP %d", response.StatusCode)
		s.noteFailure(ctx, sourceID, eventID, err.Error())
		return false, err
	}
	var result struct {
		Results []struct {
			Status string `json:"status"`
			Error  string `json:"error"`
		} `json:"results"`
	}
	if err = json.NewDecoder(response.Body).Decode(&result); err != nil {
		s.noteFailure(ctx, sourceID, eventID, "invalid Temporality ingest response: "+err.Error())
		return false, err
	}
	if len(result.Results) != 1 || (result.Results[0].Status != "accepted" && result.Results[0].Status != "repeated") {
		reason := "Temporality rejected event"
		if len(result.Results) == 1 && result.Results[0].Error != "" {
			reason += ": " + result.Results[0].Error
		}
		s.noteFailure(ctx, sourceID, eventID, reason)
		return false, errors.New(reason)
	}
	err = s.markDelivered(ctx, sourceID, eventID)
	return true, err
}

func (s *Store) noteFailure(ctx context.Context, sourceID, eventID, message string) {
	_, _ = s.pool.Exec(ctx, `UPDATE kernel_event_outbox SET attempts=attempts+1,last_error=$3,claimed_until=NULL,next_attempt_at=clock_timestamp()+LEAST(interval '5 minutes', interval '1 second' * power(2,LEAST(attempts,8))) WHERE source_id=$1 AND event_id=$2`, sourceID, eventID, message)
}

func (s *Store) markDelivered(ctx context.Context, sourceID, eventID string) error {
	_, err := s.pool.Exec(ctx, `UPDATE kernel_event_outbox SET delivered_at=clock_timestamp(),attempts=attempts+1,last_error='',claimed_until=NULL WHERE source_id=$1 AND event_id=$2`, sourceID, eventID)
	return err
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
		(SELECT last_error FROM kernel_event_outbox WHERE last_error <> '' ORDER BY attempts DESC, created_at DESC LIMIT 1)
		FROM kernel_event_outbox`).Scan(&stats.Pending, &oldest, &stats.Delivered, &stats.FailedAttemptsLast)
	if err != nil {
		return stats, err
	}
	if oldest != nil {
		stats.OldestPendingAge = time.Since(*oldest).Round(time.Second)
	}
	return stats, nil
}

func (s *Store) RunPublisher(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		published, err := s.PublishOne(ctx)
		if err == nil && published {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
