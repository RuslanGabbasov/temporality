package cognition

import (
	"context"
	"errors"

	"github.com/temporality-project/temporality/frp/protocol"
)

var ErrClaimNotFound = errors.New("claim not found")

type Commit struct {
	Event     protocol.Event  `json:"event"`
	Claim     Claim           `json:"claim"`
	Relations []ClaimRelation `json:"relations,omitempty"`
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
	return nil
}

type Store interface {
	CommitClaim(context.Context, Commit) error
	GetClaim(context.Context, string) (Claim, error)
	ListRelations(context.Context, string) ([]ClaimRelation, error)
}
