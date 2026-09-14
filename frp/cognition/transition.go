package cognition

import (
	"errors"
	"fmt"
	"time"

	"github.com/temporality-project/temporality/frp/protocol"
)

type Transition struct {
	Event      protocol.Event `json:"event"`
	ClaimID    string         `json:"claim_id"`
	ToStatus   ClaimStatus    `json:"to_status"`
	Confidence *float32       `json:"confidence,omitempty"`
	ValidAt    time.Time      `json:"valid_at"`
}

func (t Transition) Validate() error {
	if err := t.Event.Validate(); err != nil {
		return err
	}
	if t.ClaimID == "" {
		return errors.New("claim_id is required")
	}
	switch t.ToStatus {
	case ClaimSupported, ClaimRefuted, ClaimSuperseded:
	default:
		return fmt.Errorf("invalid transition target %q", t.ToStatus)
	}
	if t.Confidence != nil && (*t.Confidence < 0 || *t.Confidence > 1) {
		return errors.New("confidence must be between 0 and 1")
	}
	if t.ValidAt.IsZero() {
		return errors.New("valid_at is required")
	}
	if !t.Event.ValidTime.Equal(t.ValidAt) {
		return errors.New("event valid_time must equal transition valid_at")
	}
	if t.Event.Type != "claim."+string(t.ToStatus) {
		return errors.New("event type must match transition status")
	}
	return nil
}

func CanTransition(from, to ClaimStatus) bool {
	switch from {
	case ClaimCandidate:
		return to == ClaimSupported || to == ClaimRefuted || to == ClaimSuperseded
	case ClaimSupported:
		return to == ClaimRefuted || to == ClaimSuperseded
	default:
		return false
	}
}
