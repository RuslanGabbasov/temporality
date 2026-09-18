package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/temporality-project/temporality/frp/cognition"
	"github.com/temporality-project/temporality/frp/frame"
	"github.com/temporality-project/temporality/frp/protocol"
)

// RefExists reports whether a frame ref resolves in the substrate. It backs
// the reducer's hallucinated-pin/attend guard (a visible rejection instead of
// a failed step); entity refs resolve against the M14 entity graph.
func (s *Store) RefExists(ctx context.Context, ref frame.Ref) (bool, error) {
	queries := map[frame.RefType]string{
		frame.RefEvent:     `SELECT 1 FROM events WHERE event_id=$1`,
		frame.RefClaim:     `SELECT 1 FROM claims WHERE claim_id=$1`,
		frame.RefExecution: `SELECT 1 FROM executions WHERE execution_id=$1`,
		frame.RefRegion:    `SELECT 1 FROM regions WHERE region_id=$1`,
		frame.RefEntity:    `SELECT 1 FROM entities WHERE entity_id=$1`,
	}
	query, ok := queries[ref.Type]
	if !ok {
		return false, fmt.Errorf("unsupported ref type %q", ref.Type)
	}
	var one int
	err := s.pool.QueryRow(ctx, query, ref.ID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

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
	_, err = tx.Exec(ctx, `INSERT INTO claims (claim_id,protocol,version,proposition,confidence,status,created_event,valid_from,valid_to,subject,predicate,object) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,NULLIF($10,''),NULLIF($11,''),NULLIF($12,''))`, c.ClaimID, c.Protocol, c.Version, c.Proposition, c.Confidence, c.Status, c.CreatedEvent, c.ValidFrom, c.ValidTo, c.Subject, c.Predicate, c.Object)
	if err != nil {
		return err
	}
	for _, r := range commit.Relations {
		_, err = tx.Exec(ctx, `INSERT INTO claim_relations (src_claim,dst_claim,type,weight,evidence_event) VALUES ($1,$2,$3,$4,$5)`, r.SourceClaim, r.DestinationClaim, r.Type, r.Weight, r.EvidenceEvent)
		if err != nil {
			return err
		}
	}
	if len(commit.Evidence) > 0 {
		var found int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM events WHERE event_id = ANY($1::uuid[])`, commit.Evidence).Scan(&found); err != nil {
			return err
		}
		if found != len(commit.Evidence) {
			return cognition.ErrEvidenceNotFound
		}
		for _, id := range commit.Evidence {
			if _, err = tx.Exec(ctx, `INSERT INTO claim_evidence (claim_id,evidence_event) VALUES ($1,$2) ON CONFLICT DO NOTHING`, c.ClaimID, id); err != nil {
				return err
			}
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

const claimColumns = `protocol,version,claim_id::text,proposition,confidence,status,created_event::text,valid_from,valid_to,COALESCE(subject,''),COALESCE(predicate,''),COALESCE(object,'')`

func scanClaim(row pgx.Row) (cognition.Claim, error) {
	var claim cognition.Claim
	err := row.Scan(&claim.Protocol, &claim.Version, &claim.ClaimID, &claim.Proposition, &claim.Confidence, &claim.Status, &claim.CreatedEvent, &claim.ValidFrom, &claim.ValidTo, &claim.Subject, &claim.Predicate, &claim.Object)
	return claim, err
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
	claim, err = scanClaim(tx.QueryRow(ctx, `SELECT `+claimColumns+` FROM claims WHERE claim_id=$1 FOR UPDATE`, transition.ClaimID))
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
	claim, err := scanClaim(s.pool.QueryRow(ctx, `SELECT `+claimColumns+` FROM claims WHERE claim_id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return claim, cognition.ErrClaimNotFound
	}
	return claim, err
}

func (s *Store) ListClaims(ctx context.Context) ([]cognition.Claim, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+claimColumns+` FROM claims ORDER BY claim_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]cognition.Claim, 0)
	for rows.Next() {
		var claim cognition.Claim
		if err = rows.Scan(&claim.Protocol, &claim.Version, &claim.ClaimID, &claim.Proposition, &claim.Confidence, &claim.Status, &claim.CreatedEvent, &claim.ValidFrom, &claim.ValidTo, &claim.Subject, &claim.Predicate, &claim.Object); err != nil {
			return nil, err
		}
		result = append(result, claim)
	}
	return result, rows.Err()
}

// ListClaimsByWorld implements the longitudinal-memory read path: a claim
// belongs to the world when its evidence events carry the world id
// (ingestion/observation claims) or when it was authored inside an episode
// that observed the world (model-emitted claims carry no evidence of their
// own, but their creating events share the episode with world observations).
// Claims citing a world.effect event are deliberately excluded: effects are
// episode EXPERIENCE (what an episode changed and observed right after), not
// durable world knowledge — the benchmark's warm arms were poisoned by
// outcome claims like "tests are green after the patch" riding into a
// recycled world as fact. The UNION dedupes claims reachable through both
// paths. WorldVersion is derived from the event log (pivot §21): the max
// world_version stamped on the claim's lifecycle events, so a confirm in a
// newer world state refreshes the claim while an untested old fact stays
// marked as old.
func (s *Store) ListClaimsByWorld(ctx context.Context, worldID string) ([]cognition.Claim, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+claimColumns+`, COALESCE((SELECT max((le.payload->>'world_version')::int) FROM events le WHERE le.payload->>'claim_id' = claims.claim_id::text AND le.payload ? 'world_version'), 0) FROM claims WHERE claim_id IN (
			SELECT ce.claim_id FROM claim_evidence ce JOIN events ev ON ev.event_id = ce.evidence_event WHERE ev.payload->>'world_id' = $1
			UNION
			SELECT c.claim_id FROM claims c JOIN events ev ON ev.event_id = c.created_event
				WHERE ev.episode_id IS NOT NULL AND ev.episode_id IN (
					SELECT DISTINCT e2.episode_id FROM events e2 WHERE e2.payload->>'world_id' = $1 AND e2.episode_id IS NOT NULL
				)
		) AND claim_id NOT IN (
			SELECT ce2.claim_id FROM claim_evidence ce2 JOIN events ev2 ON ev2.event_id = ce2.evidence_event WHERE ev2.type = 'world.effect'
		) ORDER BY claim_id`, worldID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]cognition.Claim, 0)
	for rows.Next() {
		var claim cognition.Claim
		if err = rows.Scan(&claim.Protocol, &claim.Version, &claim.ClaimID, &claim.Proposition, &claim.Confidence, &claim.Status, &claim.CreatedEvent, &claim.ValidFrom, &claim.ValidTo, &claim.Subject, &claim.Predicate, &claim.Object, &claim.WorldVersion); err != nil {
			return nil, err
		}
		result = append(result, claim)
	}
	return result, rows.Err()
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

func (s *Store) ListClaimEvidence(ctx context.Context, id string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT evidence_event::text FROM claim_evidence WHERE claim_id=$1 ORDER BY evidence_event`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]string, 0)
	for rows.Next() {
		var eventID string
		if err = rows.Scan(&eventID); err != nil {
			return nil, err
		}
		result = append(result, eventID)
	}
	if rows.Err() != nil {
		return nil, rows.Err()
	}
	if _, err = s.GetClaim(ctx, id); err != nil {
		return nil, err
	}
	return result, nil
}
