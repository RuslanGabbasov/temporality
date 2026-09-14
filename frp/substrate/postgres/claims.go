package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/temporality-project/temporality/frp/cognition"
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
	payload, err := json.Marshal(commit.Event.Payload)
	if err != nil {
		return err
	}
	provenance, err := json.Marshal(commit.Event.Provenance)
	if err != nil {
		return err
	}
	e := commit.Event
	_, err = tx.Exec(ctx, `INSERT INTO events (event_id,protocol,version,tx_time,valid_time,agent_id,episode_id,branch_id,parent_id,type,payload,provenance,source_trust,evidence_strength,freshness,agent_trust,consensus,contradiction) VALUES ($1,$2,$3,$4,$5,NULLIF($6,'')::uuid,NULLIF($7,'')::uuid,NULLIF($8,'')::uuid,NULLIF($9,'')::uuid,$10,$11,$12,$13,$14,$15,$16,$17,$18)`, e.EventID, e.Protocol, e.Version, e.TransactionTime, e.ValidTime, e.AgentID, e.EpisodeID, e.BranchID, e.ParentID, e.Type, payload, provenance, e.SourceTrust, e.EvidenceStrength, e.Freshness, e.AgentTrust, e.Consensus, e.Contradiction)
	if err != nil {
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
