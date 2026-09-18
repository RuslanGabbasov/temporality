package cognition

import (
	"context"
	"errors"

	"github.com/temporality-project/temporality/frp/protocol"
)

var (
	ErrClaimNotFound    = errors.New("claim not found")
	ErrEvidenceNotFound = errors.New("evidence event not found")
)

type Commit struct {
	Event     protocol.Event  `json:"event"`
	Claim     Claim           `json:"claim"`
	Relations []ClaimRelation `json:"relations,omitempty"`
	// Evidence references pre-existing events (typically world observations)
	// that back the claim. Unlike relations, evidence links a claim to runtime
	// facts rather than to other claims; stores must reject unknown event ids
	// so a claim can never cite evidence that was never observed (M13).
	Evidence []string `json:"evidence,omitempty"`
}

func (c Commit) Validate() error {
	if err := c.Event.Validate(); err != nil {
		return err
	}
	if err := c.Claim.Validate(); err != nil {
		return err
	}
	if c.Claim.CreatedEvent != c.Event.EventID {
		return errors.New("claim created_event must reference commit event")
	}
	for _, relation := range c.Relations {
		if err := relation.Validate(); err != nil {
			return err
		}
		if relation.SourceClaim != c.Claim.ClaimID {
			return errors.New("relation src_claim must reference commit claim")
		}
		if relation.EvidenceEvent != c.Event.EventID {
			return errors.New("relation evidence_event must reference commit event")
		}
	}
	seen := make(map[string]struct{}, len(c.Evidence))
	for _, id := range c.Evidence {
		if id == "" {
			return errors.New("evidence event id must not be empty")
		}
		if id == c.Event.EventID {
			return errors.New("evidence must not reference the commit event; created_event already does")
		}
		if _, duplicate := seen[id]; duplicate {
			return errors.New("evidence event ids must be unique")
		}
		seen[id] = struct{}{}
	}
	return nil
}

type Store interface {
	CommitClaim(context.Context, Commit) error
	TransitionClaim(context.Context, Transition) (Claim, error)
	GetClaim(context.Context, string) (Claim, error)
	ListClaims(context.Context) ([]Claim, error)
	// ListClaimsByWorld returns the durable, world-scoped claim base: claims
	// backed by evidence observed in the world (their evidence events carry the
	// world id) plus claims authored inside episodes that acted in that world.
	// This is the longitudinal-memory read path (M13/M15): a new episode in the
	// same world must see prior episodes' knowledge without transcript leakage.
	// Refuted/superseded filtering and cutoff projection stay the renderer's job.
	ListClaimsByWorld(context.Context, string) ([]Claim, error)
	ListRelations(context.Context, string) ([]ClaimRelation, error)
	// ListClaimEvidence returns the event ids backing a claim, excluding the
	// claim's own created_event (M13 provenance chain).
	ListClaimEvidence(context.Context, string) ([]string, error)
}
