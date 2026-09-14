package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/temporality-project/temporality/frp/cognition"
	"github.com/temporality-project/temporality/frp/protocol"
)

func (s *Store) CommitClaim(ctx context.Context, commit cognition.Commit) error {
	if err := commit.Validate(); err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = insertEvent(ctx, tx, commit.Event); err != nil {
		return err
	}
	c := commit.Claim
	_, err = tx.Exec(ctx, `INSERT INTO claims (claim_id,protocol,version,proposition,confidence,status,created_event,valid_from,valid_to) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, c.ClaimID, c.Protocol, c.Version, c.Proposition, c.Confidence, c.Status, c.CreatedEvent, c.ValidFrom, c.ValidTo)
	if err != nil {
		return err
	}
	for _, r := range commit.Relations {
		_, err = tx.Exec(ctx, `INSERT INTO claim_relations (src_claim,dst_claim,type,weight,evidence_event) VALUES ($1,$2,$3,$4,$5)`, r.SourceClaim, r.DestinationClaim, r.Type, r.Weight, r.EvidenceEvent)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func insertEvent(ctx context.Context, tx pgx.Tx, e protocol.Event) error {
	payload, err := json.Marshal(e.Payload)
	if err != nil {
		return err
	}
	provenance, err := json.Marshal(e.Provenance)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO events (event_id,protocol,version,tx_time,valid_time,agent_id,episode_id,branch_id,parent_id,type,payload,provenance,source_trust,evidence_strength,freshness,agent_trust,consensus,contradiction) VALUES ($1,$2,$3,$4,$5,NULLIF($6,'')::uuid,NULLIF($7,'')::uuid,NULLIF($8,'')::uuid,NULLIF($9,'')::uuid,$10,$11,$12,$13,$14,$15,$16,$17,$18)`, e.EventID, e.Protocol, e.Version, e.TransactionTime, e.ValidTime, e.AgentID, e.EpisodeID, e.BranchID, e.ParentID, e.Type, payload, provenance, e.SourceTrust, e.EvidenceStrength, e.Freshness, e.AgentTrust, e.Consensus, e.Contradiction)
	return err
}

func (s *Store) TransitionClaim(ctx context.Context, transition cognition.Transition) (cognition.Claim, error) {
	if err := transition.Validate(); err != nil {
		return cognition.Claim{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return cognition.Claim{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var claim cognition.Claim
	err = tx.QueryRow(ctx, `SELECT protocol,version,claim_id::text,proposition,confidence,status,created_event::text,valid_from,valid_to FROM claims WHERE claim_id=$1 FOR UPDATE`, transition.ClaimID).Scan(&claim.Protocol, &claim.Version, &claim.ClaimID, &claim.Proposition, &claim.Confidence, &claim.Status, &claim.CreatedEvent, &claim.ValidFrom, &claim.ValidTo)
	if errors.Is(err, pgx.ErrNoRows) {
		return cognition.Claim{}, cognition.ErrClaimNotFound
	}
	if err != nil {
		return cognition.Claim{}, err
	}
	if !cognition.CanTransition(claim.Status, transition.ToStatus) {
		return cognition.Claim{}, fmt.Errorf("invalid claim transition %s -> %s", claim.Status, transition.ToStatus)
	}
	if transition.ValidAt.Before(claim.ValidFrom) {
		return cognition.Claim{}, errors.New("transition valid_at cannot precede claim valid_from")
	}
	if err = insertEvent(ctx, tx, transition.Event); err != nil {
		return cognition.Claim{}, err
	}
	if _, err = tx.Exec(ctx, `SELECT set_config('temporality.claim_transition','1',true)`); err != nil {
		return cognition.Claim{}, err
	}
	var validTo *time.Time
	if transition.ToStatus == cognition.ClaimRefuted || transition.ToStatus == cognition.ClaimSuperseded {
		value := transition.ValidAt
		validTo = &value
	}
	_, err = tx.Exec(ctx, `UPDATE claims SET status=$2,confidence=COALESCE($3,confidence),valid_to=$4 WHERE claim_id=$1`, transition.ClaimID, transition.ToStatus, transition.Confidence, validTo)
	if err != nil {
		return cognition.Claim{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return cognition.Claim{}, err
	}
	claim.Status = transition.ToStatus
	claim.ValidTo = validTo
	if transition.Confidence != nil {
		claim.Confidence = *transition.Confidence
	}
	return claim, nil
}

func (s *Store) GetClaim(ctx context.Context, id string) (cognition.Claim, error) {
	var claim cognition.Claim
	err := s.pool.QueryRow(ctx, `SELECT protocol,version,claim_id::text,proposition,confidence,status,created_event::text,valid_from,valid_to FROM claims WHERE claim_id=$1`, id).Scan(&claim.Protocol, &claim.Version, &claim.ClaimID, &claim.Proposition, &claim.Confidence, &claim.Status, &claim.CreatedEvent, &claim.ValidFrom, &claim.ValidTo)
	if errors.Is(err, pgx.ErrNoRows) {
		return claim, cognition.ErrClaimNotFound
	}
	return claim, err
}

func (s *Store) ListRelations(ctx context.Context, id string) ([]cognition.ClaimRelation, error) {
	rows, err := s.pool.Query(ctx, `SELECT src_claim::text,dst_claim::text,type,weight,evidence_event::text FROM claim_relations WHERE src_claim=$1 OR dst_claim=$1 ORDER BY src_claim,dst_claim,type,evidence_event`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]cognition.ClaimRelation, 0)
	for rows.Next() {
		var relation cognition.ClaimRelation
		if err = rows.Scan(&relation.SourceClaim, &relation.DestinationClaim, &relation.Type, &relation.Weight, &relation.EvidenceEvent); err != nil {
			return nil, err
		}
		result = append(result, relation)
	}
	return result, rows.Err()
}
