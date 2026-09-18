// Package ingest implements the M13 knowledge import pipeline. A bounded,
// read-only observation of a declared world resource becomes canonical
// world.observation events, and deterministic extractors turn those
// observations into candidate Claims that cite the observation events as
// evidence. Import never creates claims directly: what was found stays
// separable from what the substrate believes.
package ingest

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/temporality-project/temporality/frp/cognition"
	"github.com/temporality-project/temporality/frp/execution"
	"github.com/temporality-project/temporality/frp/protocol"
	"github.com/temporality-project/temporality/frp/world"
)

const (
	// DefaultDepth is the directory depth ingested when the request omits it.
	DefaultDepth = 2
	// MaxDepth bounds how deep a walk may go.
	MaxDepth = 5
	// DefaultMaxEvents bounds the observations produced by one run.
	DefaultMaxEvents = 100
	// MaxEvents is the hard upper bound on observations per run.
	MaxEvents = 500
	// DefaultGitLogLimit bounds the git history read when the request omits it.
	DefaultGitLogLimit = 10
)

// ErrInvalidRequest marks malformed ingestion requests.
var ErrInvalidRequest = errors.New("invalid ingestion request")

// Request describes one bounded ingestion run over a declared world resource.
type Request struct {
	WorldID     string `json:"world_id"`
	ResourceID  string `json:"resource_id"`
	EpisodeID   string `json:"episode_id,omitempty"`
	BranchID    string `json:"branch_id,omitempty"`
	Depth       int    `json:"depth,omitempty"`
	MaxEvents   int    `json:"max_events,omitempty"`
	GitLogLimit int    `json:"git_log_limit,omitempty"`
}

// Normalize applies defaults and clamps bounds.
func (r *Request) Normalize() {
	if r.Depth <= 0 {
		r.Depth = DefaultDepth
	}
	if r.Depth > MaxDepth {
		r.Depth = MaxDepth
	}
	if r.MaxEvents <= 0 {
		r.MaxEvents = DefaultMaxEvents
	}
	if r.MaxEvents > MaxEvents {
		r.MaxEvents = MaxEvents
	}
	if r.GitLogLimit <= 0 {
		r.GitLogLimit = DefaultGitLogLimit
	}
}

func (r Request) Validate() error {
	if r.WorldID == "" || r.ResourceID == "" {
		return fmt.Errorf("%w: world_id and resource_id are required", ErrInvalidRequest)
	}
	return nil
}

// ObservationRecord describes one committed world.observation event.
type ObservationRecord struct {
	EventID         string `json:"event_id"`
	Resource        string `json:"resource"`
	ObservationType string `json:"observation_type"`
	Truncated       bool   `json:"truncated,omitempty"`
}

// ClaimRecord describes one extracted candidate claim.
type ClaimRecord struct {
	ClaimID     string   `json:"claim_id"`
	EventID     string   `json:"event_id"`
	Proposition string   `json:"proposition"`
	Confidence  float32  `json:"confidence"`
	Extractor   string   `json:"extractor"`
	Evidence    []string `json:"evidence"`
}

// Result summarizes one ingestion run.
type Result struct {
	RunID        string              `json:"run_id"`
	WorldID      string              `json:"world_id"`
	WorldVersion int                 `json:"world_version"`
	ResourceID   string              `json:"resource_id"`
	ResourceType string              `json:"resource_type"`
	Observations []ObservationRecord `json:"observations"`
	Claims       []ClaimRecord       `json:"claims"`
	Truncated    bool                `json:"truncated"`
	Warnings     []string            `json:"warnings,omitempty"`
}

// EventAppender persists runtime facts.
type EventAppender interface {
	Append(context.Context, protocol.Event) error
}

// ClaimCommitter persists claims atomically with their creation event.
type ClaimCommitter interface {
	CommitClaim(context.Context, cognition.Commit) error
}

// Runner executes ingestion runs against one bound world snapshot.
type Runner struct {
	World  world.World
	Events EventAppender
	Claims ClaimCommitter
	NewID  func() string
	Now    func() time.Time
}

// observation couples a committed observation record with the payload the
// extractor needs. relPath is the observed path relative to the ingested
// resource root so propositions stay machine-independent.
type observation struct {
	record  ObservationRecord
	payload map[string]any
	relPath string
}

// Run performs the ingestion: observe the source, persist world.observation
// events, then extract and commit candidate claims citing those events.
// Observations are persisted as they are produced, so a failed run can leave
// committed facts behind; observations are runtime facts and stay valid even
// when extraction never runs.
func (r *Runner) Run(ctx context.Context, request Request) (Result, error) {
	if err := r.validateDeps(); err != nil {
		return Result{}, err
	}
	if err := request.Validate(); err != nil {
		return Result{}, err
	}
	request.Normalize()
	if request.WorldID != r.World.WorldID {
		return Result{}, fmt.Errorf("%w: request world %q does not match bound world %q", ErrInvalidRequest, request.WorldID, r.World.WorldID)
	}
	resource, ok := r.World.Resource(request.ResourceID)
	if !ok {
		return Result{}, fmt.Errorf("%w: unknown resource %q", ErrInvalidRequest, request.ResourceID)
	}
	result := Result{RunID: r.NewID(), WorldID: r.World.WorldID, WorldVersion: r.World.StateVersion, ResourceID: resource.ID, ResourceType: resource.Type, Observations: []ObservationRecord{}, Claims: []ClaimRecord{}}

	observations, walkErr := r.observeResource(ctx, &result, resource, request)
	extractions := Extract(resource, observations)
	claims, claimErr := r.commitClaims(ctx, request, result.RunID, extractions)
	result.Claims = claims
	if walkErr != nil {
		return result, walkErr
	}
	return result, claimErr
}

func (r *Runner) validateDeps() error {
	if r.Events == nil || r.Claims == nil || r.NewID == nil || r.Now == nil {
		return errors.New("ingest runner dependencies are required")
	}
	return nil
}

// observeStep runs one read-only adapter step and commits its observation
// event before returning it for extraction.
func (r *Runner) observeStep(ctx context.Context, adapter *world.Adapter, result *Result, request Request, step execution.Step, relPath string) (*observation, error) {
	observed, err := adapter.Observe(ctx, step)
	if err != nil {
		return nil, err
	}
	if observed.ObservationType == "" {
		return &observation{}, nil
	}
	event, err := world.IngestionObservationEvent(r.World, result.ResourceID, world.Observation{Resource: observed.Resource, ObservationType: observed.ObservationType, Payload: observed.Output, Truncated: observed.Truncated}, r.NewID(), r.Now().UTC(), request.EpisodeID, request.BranchID, result.RunID)
	if err != nil {
		return nil, err
	}
	if err = r.Events.Append(ctx, event); err != nil {
		return nil, fmt.Errorf("append observation: %w", err)
	}
	record := ObservationRecord{EventID: event.EventID, Resource: observed.Resource, ObservationType: observed.ObservationType, Truncated: observed.Truncated}
	result.Observations = append(result.Observations, record)
	return &observation{record: record, payload: observed.Output, relPath: relPath}, nil
}

func (r *Runner) commitClaims(ctx context.Context, request Request, runID string, extractions []Extraction) ([]ClaimRecord, error) {
	claims := make([]ClaimRecord, 0, len(extractions))
	for _, extraction := range extractions {
		claimID := r.NewID()
		eventID := r.NewID()
		at := r.Now().UTC()
		event := protocol.Event{
			EventID:         eventID,
			TransactionTime: at,
			ValidTime:       at,
			EpisodeID:       request.EpisodeID,
			BranchID:        request.BranchID,
			Type:            "claim." + string(cognition.ClaimCandidate),
			Payload: map[string]any{
				"claim_id":         claimID,
				"proposition":      extraction.Proposition,
				"confidence":       extraction.Confidence,
				"extractor":        extraction.Extractor,
				"evidence_events":  append([]string(nil), extraction.Evidence...),
				"ingestion_run_id": runID,
				"world_version":    r.World.StateVersion,
			},
			Provenance: map[string]any{"source": "ingestion", "extractor": extraction.Extractor, "run_id": runID},
		}
		if extraction.Subject != "" {
			event.Payload["subject"] = extraction.Subject
			event.Payload["predicate"] = extraction.Predicate
			event.Payload["object"] = extraction.Object
		}
		event.ApplyDefaults(at)
		claim := cognition.Claim{Protocol: protocol.Name, Version: protocol.Version, ClaimID: claimID, Proposition: extraction.Proposition, Confidence: extraction.Confidence, Status: cognition.ClaimCandidate, CreatedEvent: eventID, ValidFrom: at, Subject: extraction.Subject, Predicate: extraction.Predicate, Object: extraction.Object}
		commit := cognition.Commit{Event: event, Claim: claim, Evidence: extraction.Evidence}
		if err := r.Claims.CommitClaim(ctx, commit); err != nil {
			return claims, fmt.Errorf("commit extracted claim: %w", err)
		}
		claims = append(claims, ClaimRecord{ClaimID: claimID, EventID: eventID, Proposition: extraction.Proposition, Confidence: extraction.Confidence, Extractor: extraction.Extractor, Evidence: extraction.Evidence})
	}
	return claims, nil
}
