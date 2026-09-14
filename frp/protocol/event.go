package protocol

import (
	"errors"
	"fmt"
	"time"
)

const (
	Name    = "frp"
	Version = "0.3"
)

// Event records a durable runtime fact. Model intentions are not events.
type Event struct {
	Protocol         string         `json:"protocol"`
	Version          string         `json:"version"`
	EventID          string         `json:"event_id"`
	TransactionTime  time.Time      `json:"tx_time"`
	ValidTime        time.Time      `json:"valid_time"`
	AgentID          string         `json:"agent_id,omitempty"`
	EpisodeID        string         `json:"episode_id,omitempty"`
	BranchID         string         `json:"branch_id,omitempty"`
	ParentID         string         `json:"parent_id,omitempty"`
	Type             string         `json:"type"`
	Payload          map[string]any `json:"payload"`
	Provenance       map[string]any `json:"provenance"`
	SourceTrust      *float32       `json:"source_trust,omitempty"`
	EvidenceStrength *float32       `json:"evidence_strength,omitempty"`
	Freshness        *float32       `json:"freshness,omitempty"`
	AgentTrust       *float32       `json:"agent_trust,omitempty"`
	Consensus        *float32       `json:"consensus,omitempty"`
	Contradiction    *float32       `json:"contradiction,omitempty"`
}

func (e *Event) ApplyDefaults(now time.Time) {
	if e.Protocol == "" { e.Protocol = Name }
	if e.Version == "" { e.Version = Version }
	if e.TransactionTime.IsZero() { e.TransactionTime = now.UTC() }
	if e.ValidTime.IsZero() { e.ValidTime = e.TransactionTime }
	if e.Payload == nil { e.Payload = map[string]any{} }
	if e.Provenance == nil { e.Provenance = map[string]any{} }
}

func (e Event) Validate() error {
	if e.Protocol != Name || e.Version != Version {
		return fmt.Errorf("unsupported protocol version %q/%q", e.Protocol, e.Version)
	}
	if e.EventID == "" { return errors.New("event_id is required") }
	if e.Type == "" { return errors.New("type is required") }
	if e.TransactionTime.IsZero() || e.ValidTime.IsZero() { return errors.New("tx_time and valid_time are required") }
	return nil
}
