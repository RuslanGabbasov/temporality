package cognition

import (
	"errors"
	"fmt"
	"strings"
	"time"

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
	return nil
}
