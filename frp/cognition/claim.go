package cognition

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/temporality-project/temporality/frp/entity"
	"github.com/temporality-project/temporality/frp/protocol"
)

type ClaimStatus string

const (
	ClaimCandidate  ClaimStatus = "candidate"
	ClaimSupported  ClaimStatus = "supported"
	ClaimRefuted    ClaimStatus = "refuted"
	ClaimSuperseded ClaimStatus = "superseded"
)

type Claim struct {
	Protocol     string      `json:"protocol"`
	Version      string      `json:"version"`
	ClaimID      string      `json:"claim_id"`
	Proposition  string      `json:"proposition"`
	Confidence   float32     `json:"confidence"`
	Status       ClaimStatus `json:"status"`
	CreatedEvent string      `json:"created_event"`
	ValidFrom    time.Time   `json:"valid_from"`
	ValidTo      *time.Time  `json:"valid_to,omitempty"`
	// Triple optionally binds the claim to the M14 knowledge graph: subject and
	// object are entity refs ("type:name"), predicate is a registered relation
	// predicate. All three fields are validated together (all-or-nothing).
	Subject string `json:"subject,omitempty"`
	Predicate string `json:"predicate,omitempty"`
	Object string `json:"object,omitempty"`
	// WorldVersion is the world state version this claim's knowledge was last
	// verified against: the max world_version stamped on the claim's lifecycle
	// events (creation, confirm/refute/supersede). It is derived from the event
	// log, not persisted in the claims table, so replay computes it the same
	// way. Pivot §21: knowledge delivered to a world at a newer state version
	// must be marked stale instead of posing as a current fact.
	WorldVersion int `json:"world_version,omitempty"`
}

func (c *Claim) ApplyDefaults(now time.Time) {
	if c.Protocol == "" {
		c.Protocol = protocol.Name
	}
	if c.Version == "" {
		c.Version = protocol.Version
	}
	if c.Status == "" {
		c.Status = ClaimCandidate
	}
	if c.ValidFrom.IsZero() {
		c.ValidFrom = now.UTC()
	}
}

func (c Claim) HasTriple() bool {
	return c.Subject != "" || c.Predicate != "" || c.Object != ""
}

// Triple returns the parsed entity refs and predicate when the claim carries
// a knowledge-graph triple.
func (c Claim) Triple() (subject entity.Ref, predicate string, object entity.Ref, err error) {
	subject, err = entity.ParseRef(c.Subject)
	if err != nil {
		return
	}
	predicate = c.Predicate
	if err = entity.ValidatePredicate(predicate); err != nil {
		return
	}
	object, err = entity.ParseRef(c.Object)
	if err != nil {
		return
	}
	return
}

func (c Claim) Validate() error {
	if c.Protocol != protocol.Name || c.Version != protocol.Version {
		return fmt.Errorf("unsupported protocol version %q/%q", c.Protocol, c.Version)
	}
	if c.ClaimID == "" {
		return errors.New("claim_id is required")
	}
	if strings.TrimSpace(c.Proposition) == "" {
		return errors.New("proposition is required")
	}
	if c.Confidence < 0 || c.Confidence > 1 {
		return errors.New("confidence must be between 0 and 1")
	}
	switch c.Status {
	case ClaimCandidate, ClaimSupported, ClaimRefuted, ClaimSuperseded:
	default:
		return fmt.Errorf("invalid claim status %q", c.Status)
	}
	if c.CreatedEvent == "" {
		return errors.New("created_event is required")
	}
	if c.ValidFrom.IsZero() {
		return errors.New("valid_from is required")
	}
	if c.ValidTo != nil && c.ValidTo.Before(c.ValidFrom) {
		return errors.New("valid_to cannot precede valid_from")
	}
	if (c.Subject == "") != (c.Predicate == "") || (c.Subject == "") != (c.Object == "") {
		return errors.New("claim triple fields subject/predicate/object must be set together")
	}
	if c.HasTriple() {
		if _, _, _, err := c.Triple(); err != nil {
			return err
		}
	}
	return nil
}
