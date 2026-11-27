package workspace

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// RouterShadowRow is one shadow-mode comparison unit
// (docs/conversational-runtime.md §23): what the Conversational Runtime
// router WOULD do with an inbound channel message, and — when the message
// ended up on the legacy intake pipeline — what the intake verdict was.
// The runtime half is written at dispatch time; the verdict half is patched
// in when the intake workflow posts its classification. Nothing in this
// table drives behavior: it exists to measure agreement before migrating a
// channel away from legacy intake.
type RouterShadowRow struct {
	InboundMessageID string    `json:"inbound_message_id"`
	OrganizationID   string    `json:"organization_id,omitempty"`
	Channel          string    `json:"channel"`
	ChatID           string    `json:"chat_id"`
	ConversationID   string    `json:"conversation_id,omitempty"`
	TextExcerpt      string    `json:"text_excerpt"`
	HasAttachments   bool      `json:"has_attachments"`
	RuntimeIntent    string    `json:"runtime_intent"`
	RuntimeExecution string    `json:"runtime_execution"`
	RuntimeConfidence float64  `json:"runtime_confidence"`
	RuntimeLevel     string    `json:"runtime_level"`
	RuntimeServed    bool      `json:"runtime_served"`
	LegacyClassification string `json:"legacy_classification,omitempty"`
	Agreement        string    `json:"agreement,omitempty"` // agree|disagree|incomparable
	CreatedAt        time.Time `json:"created_at"`
	VerdictAt        *time.Time `json:"verdict_at,omitempty"`
}

// RecordRouterShadow writes (or refreshes) the runtime half of a shadow row.
// Idempotent per inbound message: a redelivered webhook recomputes the same
// deterministic decision and overwrites only runtime fields, never the
// legacy verdict.
func (s *Store) RecordRouterShadow(ctx context.Context, row *RouterShadowRow) error {
	if strings.TrimSpace(row.InboundMessageID) == "" {
		return errors.New("shadow row inbound_message_id is required")
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO conversation_router_shadow (
			inbound_message_id, organization_id, channel, chat_id, conversation_id,
			text_excerpt, has_attachments, runtime_intent, runtime_execution,
			runtime_confidence, runtime_level, runtime_served
		 ) VALUES ($1,$2,$3,$4,NULLIF($5,''),$6,$7,$8,$9,$10,$11,$12)
		 ON CONFLICT (inbound_message_id) DO UPDATE SET
			organization_id = EXCLUDED.organization_id,
			conversation_id = EXCLUDED.conversation_id,
			runtime_intent = EXCLUDED.runtime_intent,
			runtime_execution = EXCLUDED.runtime_execution,
			runtime_confidence = EXCLUDED.runtime_confidence,
			runtime_level = EXCLUDED.runtime_level,
			runtime_served = EXCLUDED.runtime_served`,
		row.InboundMessageID, row.OrganizationID, row.Channel, row.ChatID, row.ConversationID,
		row.TextExcerpt, row.HasAttachments, row.RuntimeIntent, row.RuntimeExecution,
		row.RuntimeConfidence, row.RuntimeLevel, row.RuntimeServed)
	return err
}

// RecordRouterShadowVerdict attaches the legacy intake verdict to a shadow
// row. Rows without a runtime half (channels whose loop never scored the
// message, e.g. a kernel running an older binary) get one with a
// not_evaluated level, so the verdict never rides a guessed comparison.
func (s *Store) RecordRouterShadowVerdict(ctx context.Context, inboundMessageID, classification, agreement string) error {
	if strings.TrimSpace(inboundMessageID) == "" || strings.TrimSpace(classification) == "" {
		return errors.New("shadow verdict message id and classification are required")
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO conversation_router_shadow (
			inbound_message_id, legacy_classification, agreement, verdict_at
		 ) VALUES ($1,$2,NULLIF($3,''),now())
		 ON CONFLICT (inbound_message_id) DO UPDATE SET
			legacy_classification = EXCLUDED.legacy_classification,
			agreement = EXCLUDED.agreement,
			verdict_at = now()`,
		inboundMessageID, classification, agreement)
	return err
}

// GetRouterShadow reads one shadow row; ErrNotFound when the message never
// reached a runtime-first loop.
func (s *Store) GetRouterShadow(ctx context.Context, inboundMessageID string) (RouterShadowRow, error) {
	row := RouterShadowRow{}
	var verdict *time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT inbound_message_id, organization_id, channel, chat_id, COALESCE(conversation_id,''),
			text_excerpt, has_attachments, runtime_intent, runtime_execution, runtime_confidence,
			runtime_level, runtime_served, COALESCE(legacy_classification,''), COALESCE(agreement,''),
			created_at, verdict_at
		 FROM conversation_router_shadow WHERE inbound_message_id=$1`, inboundMessageID).
		Scan(&row.InboundMessageID, &row.OrganizationID, &row.Channel, &row.ChatID, &row.ConversationID,
			&row.TextExcerpt, &row.HasAttachments, &row.RuntimeIntent, &row.RuntimeExecution, &row.RuntimeConfidence,
			&row.RuntimeLevel, &row.RuntimeServed, &row.LegacyClassification, &row.Agreement,
			&row.CreatedAt, &verdict)
	if errors.Is(err, pgx.ErrNoRows) {
		return RouterShadowRow{}, ErrNotFound
	}
	row.VerdictAt = verdict
	return row, nil
}
