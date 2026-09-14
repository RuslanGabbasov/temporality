package cognition

import (
	"errors"
	"fmt"
)

type RelationType string

const (
	RelationSupports    RelationType = "supports"
	RelationContradicts RelationType = "contradicts"
	RelationDerivedFrom RelationType = "derived_from"
	RelationSupersedes  RelationType = "supersedes"
)

type ClaimRelation struct {
	SourceClaim      string       `json:"src_claim"`
	DestinationClaim string       `json:"dst_claim"`
	Type             RelationType `json:"type"`
	Weight           float32      `json:"weight"`
	EvidenceEvent    string       `json:"evidence_event"`
}

func (r ClaimRelation) Validate() error {
	if r.SourceClaim == "" || r.DestinationClaim == "" {
		return errors.New("src_claim and dst_claim are required")
	}
	if r.SourceClaim == r.DestinationClaim {
		return errors.New("claim cannot relate to itself")
	}
	switch r.Type {
	case RelationSupports, RelationContradicts, RelationDerivedFrom, RelationSupersedes:
	default:
		return fmt.Errorf("invalid relation type %q", r.Type)
	}
	if r.Weight < 0 || r.Weight > 1 {
		return errors.New("weight must be between 0 and 1")
	}
	if r.EvidenceEvent == "" {
		return errors.New("evidence_event is required")
	}
	return nil
}
