package workspace

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"
)

// Hosted form links (docs/interactive-HITL.md §6.2): a chat-delivered
// ask_human question can carry a link with a scoped secret. Whoever holds the
// link can open and answer exactly that question in a browser — no harness
// account. The link dies with the question: answered, cancelled or expired
// rows refuse everything.

// FormLink is one minted question link.
type FormLink struct {
	Token       string     `json:"token"`
	OperationID string     `json:"operation_id"`
	RunID       string     `json:"run_id,omitempty"`
	ProjectID   string     `json:"project_id,omitempty"`
	AgentID     string     `json:"agent_id,omitempty"`
	CreatedBy   string     `json:"created_by,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   time.Time  `json:"expires_at"`
	AnsweredAt  *time.Time `json:"answered_at,omitempty"`
}

// ErrFormLinkConsumed reports a link whose question already answered.
var ErrFormLinkConsumed = errors.New("form link already used")

// MintFormLink creates a link for one ask: a 32-byte random token (URL-safe
// hex), expiring after the given lifetime. The ask must still be open — a
// mint against an answered/cancelled/expired question is refused, so a stale
// delivery cannot mint a second life for a dead question.
func (s *Store) MintFormLink(ctx context.Context, operationID, runID, projectID, agentID, createdBy string, lifetime time.Duration) (FormLink, error) {
	if operationID == "" {
		return FormLink{}, errors.New("operation_id is required")
	}
	row := s.pool.QueryRow(ctx,
		`SELECT status FROM human_request WHERE id = $1`, operationID)
	var status string
	if err := row.Scan(&status); err != nil {
		return FormLink{}, err
	}
	switch status {
	case HumanStatusPending, HumanStatusDelivered:
	default:
		return FormLink{}, ErrFormLinkConsumed
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return FormLink{}, err
	}
	link := FormLink{
		Token:       hex.EncodeToString(raw),
		OperationID: operationID,
		RunID:       runID,
		ProjectID:   projectID,
		AgentID:     agentID,
		CreatedBy:   createdBy,
		CreatedAt:   time.Now().UTC(),
		ExpiresAt:   time.Now().UTC().Add(lifetime),
	}
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO workspace_form_link (token, operation_id, run_id, project_id, agent_id, created_by, created_at, expires_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		link.Token, link.OperationID, link.RunID, link.ProjectID, link.AgentID, link.CreatedBy, link.CreatedAt, link.ExpiresAt); err != nil {
		return FormLink{}, err
	}
	return link, nil
}

// GetFormLink returns a live (unexpired, unconsumed) link by token. A consumed
// link answers with ErrFormLinkConsumed — the UI must not re-offer a dead
// question, and an expired one with a distinct error.
func (s *Store) GetFormLink(ctx context.Context, token string) (FormLink, error) {
	var link FormLink
	var answeredAt *time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT token, operation_id, run_id, project_id, agent_id, created_by, created_at, expires_at, answered_at
		 FROM workspace_form_link WHERE token = $1`, token).
		Scan(&link.Token, &link.OperationID, &link.RunID, &link.ProjectID, &link.AgentID,
			&link.CreatedBy, &link.CreatedAt, &link.ExpiresAt, &answeredAt)
	if err != nil {
		return FormLink{}, err
	}
	link.AnsweredAt = answeredAt
	if link.AnsweredAt != nil {
		return link, ErrFormLinkConsumed
	}
	if time.Now().UTC().After(link.ExpiresAt) {
		return link, ErrFormLinkExpired
	}
	return link, nil
}

// ErrFormLinkExpired reports a link past its lifetime.
var ErrFormLinkExpired = errors.New("form link expired")

// MarkFormLinkAnswered stamps the consume time — idempotent, so the answer
// path and the workflow's own row close racing each other stay honest.
func (s *Store) MarkFormLinkAnswered(ctx context.Context, token string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE workspace_form_link SET answered_at = now() WHERE token = $1 AND answered_at IS NULL`, token)
	return err
}
