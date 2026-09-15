package world

import (
	"time"

	"github.com/temporality-project/temporality/frp/protocol"
)

// Canonical world event types (M11). world.observation records what the agent
// observed in the external world; registration events record the environment
// state a replay must attribute effects to.
const (
	EventWorldRegistered   = "world.registered"
	EventWorldStateUpdated = "world.state_updated"
	EventWorldObservation  = "world.observation"
)

func CanonicalEventTypes() []string {
	return []string{EventWorldRegistered, EventWorldStateUpdated, EventWorldObservation}
}

func IsCanonicalEventType(eventType string) bool {
	for _, candidate := range CanonicalEventTypes() {
		if eventType == candidate {
			return true
		}
	}
	return false
}

func EventTypeForStateVersion(version int) (string, bool) {
	if version < 1 {
		return "", false
	}
	if version == 1 {
		return EventWorldRegistered, true
	}
	return EventWorldStateUpdated, true
}

// StateEvent builds the canonical event persisted atomically with a world
// create or state update. The full world snapshot is embedded in the payload
// so replay can reconstruct the exact environment without the worlds table.
func StateEvent(value World, eventID string, at time.Time) (protocol.Event, error) {
	eventType, ok := EventTypeForStateVersion(value.StateVersion)
	if !ok {
		return protocol.Event{}, ErrWorldStateStale
	}
	if err := value.Validate(); err != nil {
		return protocol.Event{}, err
	}
	event := protocol.Event{EventID: eventID, TransactionTime: at.UTC(), ValidTime: at.UTC(), Type: eventType, Payload: map[string]any{"world_id": value.WorldID, "state_version": value.StateVersion, "world": value}, Provenance: map[string]any{"source": "runtime"}}
	event.ApplyDefaults(at.UTC())
	return event, event.Validate()
}

// Observation is a single observed fact about the external world, produced by
// a world adapter step and always committed through the execution pipeline.
type Observation struct {
	Resource        string         `json:"resource"`
	ObservationType string         `json:"observation_type"`
	Payload         map[string]any `json:"payload"`
	Truncated       bool           `json:"truncated,omitempty"`
}

func (o Observation) Validate() error {
	if o.Resource == "" || o.ObservationType == "" {
		return ErrObservationInvalid
	}
	if o.Payload == nil {
		return ErrObservationInvalid
	}
	return nil
}

// ObservationEvent builds a canonical world.observation event. Memory is not
// only what the agent thinks; memory also contains the agent's observations
// of the world (M11).
func ObservationEvent(value World, executionID, affordanceID string, observation Observation, eventID string, at time.Time, episodeID, branchID string, intentWorldVersion int) (protocol.Event, error) {
	if err := observation.Validate(); err != nil {
		return protocol.Event{}, err
	}
	payload := map[string]any{
		"world_id":         value.WorldID,
		"world_version":    value.StateVersion,
		"execution_id":     executionID,
		"affordance_id":    affordanceID,
		"resource":         observation.Resource,
		"observation_type": observation.ObservationType,
		"payload":          observation.Payload,
	}
	if observation.Truncated {
		payload["truncated"] = true
	}
	if intentWorldVersion > 0 && intentWorldVersion != value.StateVersion {
		payload["intent_world_version"] = intentWorldVersion
	}
	event := protocol.Event{EventID: eventID, TransactionTime: at.UTC(), ValidTime: at.UTC(), EpisodeID: episodeID, BranchID: branchID, Type: EventWorldObservation, Payload: payload, Provenance: map[string]any{"source": "temporality-executor", "execution_id": executionID, "affordance_id": affordanceID}}
	trust := float32(0.9)
	event.EvidenceStrength = &trust
	event.ApplyDefaults(at.UTC())
	return event, event.Validate()
}

// IngestionObservationEvent builds a canonical world.observation event recorded
// by the ingestion pipeline (M13) rather than an execution step. The provenance
// chain keeps ingestion distinguishable from executor observations while the
// event stays in the same canonical class, so attention, regions, and replay
// treat both identically.
func IngestionObservationEvent(value World, resourceID string, observation Observation, eventID string, at time.Time, episodeID, branchID, runID string) (protocol.Event, error) {
	if err := observation.Validate(); err != nil {
		return protocol.Event{}, err
	}
	payload := map[string]any{
		"world_id":           value.WorldID,
		"world_version":      value.StateVersion,
		"resource":           observation.Resource,
		"observation_type":   observation.ObservationType,
		"payload":            observation.Payload,
		"source_resource_id": resourceID,
	}
	if runID != "" {
		payload["ingestion_run_id"] = runID
	}
	if observation.Truncated {
		payload["truncated"] = true
	}
	event := protocol.Event{EventID: eventID, TransactionTime: at.UTC(), ValidTime: at.UTC(), EpisodeID: episodeID, BranchID: branchID, Type: EventWorldObservation, Payload: payload, Provenance: map[string]any{"source": "ingestion", "run_id": runID, "resource_id": resourceID}}
	trust := float32(0.9)
	event.EvidenceStrength = &trust
	event.ApplyDefaults(at.UTC())
	return event, event.Validate()
}

// ValidateStateEvent checks the canonical shape of a persisted world event.
func ValidateStateEvent(event protocol.Event, value World) error {
	if err := event.Validate(); err != nil {
		return err
	}
	expected, ok := EventTypeForStateVersion(value.StateVersion)
	if !ok || event.Type != expected {
		return ErrObservationInvalid
	}
	if id, _ := event.Payload["world_id"].(string); id != value.WorldID {
		return ErrObservationInvalid
	}
	if version, ok := event.Payload["state_version"].(int); !ok || version != value.StateVersion {
		return ErrObservationInvalid
	}
	return nil
}
