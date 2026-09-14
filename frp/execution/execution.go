package execution

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/temporality-project/temporality/frp/affordance"
	"github.com/temporality-project/temporality/frp/protocol"
)

type Status string

const (
	StatusCreated   Status = "created"
	StatusRunning   Status = "running"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

var ErrNotFound = errors.New("execution not found")

type Store interface {
	CreateExecution(context.Context, affordance.Definition, affordance.Request, Execution, protocol.Event, protocol.Event) error
	TransitionExecution(context.Context, string, Status, time.Time, *ExecutionError, protocol.Event) (Execution, error)
	GetExecution(context.Context, string) (Execution, error)
	ListActiveExecutions(context.Context, string) ([]Execution, error)
}

var transitions = map[Status]map[Status]struct{}{
	StatusCreated: {StatusRunning: {}, StatusCancelled: {}},
	StatusRunning: {StatusCompleted: {}, StatusFailed: {}, StatusCancelled: {}},
}

func (s Status) Valid() bool {
	return s == StatusCreated || s == StatusRunning || s == StatusCompleted || s == StatusFailed || s == StatusCancelled
}
func (s Status) Terminal() bool {
	return s == StatusCompleted || s == StatusFailed || s == StatusCancelled
}
func (s Status) CanTransitionTo(next Status) bool { _, ok := transitions[s][next]; return ok }

type ErrorClass string

const (
	ErrorPermissionDenied  ErrorClass = "permission_denied"
	ErrorResourceExhausted ErrorClass = "resource_exhausted"
	ErrorTimeout           ErrorClass = "timeout"
	ErrorProcessFailed     ErrorClass = "process_failed"
	ErrorUnavailable       ErrorClass = "unavailable"
	ErrorInvalidResult     ErrorClass = "invalid_result"
	ErrorInternal          ErrorClass = "internal"
)

func (c ErrorClass) Valid() bool {
	switch c {
	case ErrorPermissionDenied, ErrorResourceExhausted, ErrorTimeout, ErrorProcessFailed, ErrorUnavailable, ErrorInvalidResult, ErrorInternal:
		return true
	}
	return false
}

// ExecutionError is normalized at the executor boundary; Diagnostics must contain no secrets.
type ExecutionError struct {
	Class       ErrorClass     `json:"class"`
	Message     string         `json:"message"`
	Retryable   bool           `json:"retryable"`
	Resource    string         `json:"resource,omitempty"`
	Diagnostics map[string]any `json:"diagnostics,omitempty"`
}

func (e ExecutionError) Validate() error {
	if !e.Class.Valid() {
		return errors.New("invalid error class")
	}
	if strings.TrimSpace(e.Message) == "" {
		return errors.New("error message is required")
	}
	return nil
}

type Execution struct {
	Protocol          string          `json:"protocol"`
	Version           string          `json:"version"`
	ExecutionID       string          `json:"execution_id"`
	RequestID         string          `json:"request_id"`
	EpisodeID         string          `json:"episode_id"`
	AffordanceID      string          `json:"affordance_id"`
	Status            Status          `json:"status"`
	Phase             string          `json:"phase,omitempty"`
	CreatedEventID    string          `json:"created_event_id"`
	IntentPersistedAt time.Time       `json:"intent_persisted_at"`
	StartedAt         *time.Time      `json:"started_at,omitempty"`
	FinishedAt        *time.Time      `json:"finished_at,omitempty"`
	Error             *ExecutionError `json:"error,omitempty"`
}

func (e Execution) Validate() error {
	if e.Protocol != protocol.Name || e.Version != protocol.Version {
		return fmt.Errorf("unsupported protocol version %q/%q", e.Protocol, e.Version)
	}
	if e.ExecutionID == "" || e.RequestID == "" || e.EpisodeID == "" || e.AffordanceID == "" {
		return errors.New("execution_id, request_id, episode_id, and affordance_id are required")
	}
	if !e.Status.Valid() {
		return errors.New("invalid execution status")
	}
	if e.CreatedEventID == "" || e.IntentPersistedAt.IsZero() {
		return errors.New("persist-before-effect invariant violated: durable intent and creation event are required")
	}
	if e.Status == StatusCreated && (e.StartedAt != nil || e.FinishedAt != nil || e.Error != nil) {
		return errors.New("created execution cannot have runtime outcome")
	}
	if e.Status != StatusCreated && e.StartedAt == nil {
		return errors.New("started_at is required after execution starts")
	}
	if e.Status.Terminal() && e.FinishedAt == nil {
		return errors.New("finished_at is required for terminal execution")
	}
	if !e.Status.Terminal() && e.FinishedAt != nil {
		return errors.New("finished_at is only valid for terminal execution")
	}
	if e.Status == StatusFailed {
		if e.Error == nil {
			return errors.New("failed execution requires error")
		}
		if err := e.Error.Validate(); err != nil {
			return err
		}
	} else if e.Error != nil {
		return errors.New("error is only valid for failed execution")
	}
	return nil
}

func ValidateCreate(def affordance.Definition, request affordance.Request, value Execution, requested, created protocol.Event) error {
	if err := affordance.ValidateRequest(def, request); err != nil {
		return err
	}
	if err := value.Validate(); err != nil {
		return err
	}
	if value.Status != StatusCreated || value.RequestID != request.RequestID || value.EpisodeID != request.EpisodeID || value.AffordanceID != def.ID {
		return errors.New("execution does not match request or is not created")
	}
	if err := validateEvent(requested, affordance.EventRequested, request.EpisodeID, request.RequestID, "request_id"); err != nil {
		return fmt.Errorf("requested event: %w", err)
	}
	if err := validateEvent(created, EventExecutionCreated, value.EpisodeID, value.ExecutionID, "execution_id"); err != nil {
		return fmt.Errorf("created event: %w", err)
	}
	if value.CreatedEventID != created.EventID || !value.IntentPersistedAt.Equal(created.TransactionTime) {
		return errors.New("execution durable intent must reference creation event transaction time")
	}
	return nil
}

func ValidateTransitionEvent(value Execution, next Status, at time.Time, event protocol.Event) error {
	typeName, ok := EventTypeForStatus(next)
	if !ok || next == StatusCreated {
		return errors.New("transition has no canonical event type")
	}
	if err := validateEvent(event, typeName, value.EpisodeID, value.ExecutionID, "execution_id"); err != nil {
		return err
	}
	if !event.ValidTime.Equal(at) {
		return errors.New("event valid_time must equal transition time")
	}
	return nil
}

func validateEvent(event protocol.Event, eventType, episodeID, entityID, payloadKey string) error {
	if err := event.Validate(); err != nil {
		return err
	}
	if event.Type != eventType || event.EpisodeID != episodeID {
		return fmt.Errorf("expected canonical %s event for episode", eventType)
	}
	if id, ok := event.Payload[payloadKey].(string); !ok || id != entityID {
		return fmt.Errorf("event payload %s does not match", payloadKey)
	}
	return nil
}

func (e Execution) Transition(next Status, at time.Time, executionError *ExecutionError) (Execution, error) {
	if err := e.Validate(); err != nil {
		return Execution{}, err
	}
	if !e.Status.CanTransitionTo(next) {
		return Execution{}, fmt.Errorf("invalid execution transition %s -> %s", e.Status, next)
	}
	if at.IsZero() {
		return Execution{}, errors.New("transition time is required")
	}
	if at.Before(e.IntentPersistedAt) || (e.StartedAt != nil && at.Before(*e.StartedAt)) {
		return Execution{}, errors.New("transition time cannot precede execution lifecycle")
	}
	if next == StatusFailed {
		if executionError == nil {
			return Execution{}, errors.New("failed transition requires error")
		}
		if err := executionError.Validate(); err != nil {
			return Execution{}, err
		}
	} else if executionError != nil {
		return Execution{}, errors.New("error is only valid for failed transition")
	}
	e.Status = next
	if next == StatusRunning {
		t := at.UTC()
		e.StartedAt = &t
	} else {
		t := at.UTC()
		e.FinishedAt = &t
		e.Error = executionError
	}
	return e, e.Validate()
}

// ValidateEffectBoundary enforces that no adapter effect may start before its intent is durable.
func ValidateEffectBoundary(e Execution) error {
	if err := e.Validate(); err != nil {
		return err
	}
	if e.Status != StatusRunning {
		return errors.New("physical effect requires running execution")
	}
	if e.StartedAt.Before(e.IntentPersistedAt) {
		return errors.New("physical effect cannot precede persisted intent")
	}
	return nil
}

type Step struct {
	ID         string         `json:"id"`
	Capability string         `json:"capability"`
	Operation  string         `json:"operation"`
	Input      map[string]any `json:"input"`
}

type Plan struct {
	Steps []Step `json:"steps"`
}

func (p Plan) Validate(def affordance.Definition) error {
	allowed := make(map[string]struct{}, len(def.Capabilities))
	for _, c := range def.Capabilities {
		allowed[c] = struct{}{}
	}
	seen := map[string]struct{}{}
	for _, step := range p.Steps {
		if step.ID == "" || step.Operation == "" || step.Input == nil {
			return errors.New("workflow step id, operation, and input are required")
		}
		if _, ok := seen[step.ID]; ok {
			return fmt.Errorf("duplicate workflow step %q", step.ID)
		}
		seen[step.ID] = struct{}{}
		if _, ok := allowed[step.Capability]; !ok {
			return fmt.Errorf("workflow step uses undeclared capability %q", step.Capability)
		}
	}
	return nil
}

// DeterministicWorkflow only compiles semantic intent into declarative steps. Adapters execute them elsewhere.
type DeterministicWorkflow interface {
	Plan(affordance.Request) (Plan, error)
}
