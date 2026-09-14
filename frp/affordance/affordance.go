package affordance

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/temporality-project/temporality/frp/protocol"
)

type ExecutionMode string

const (
	ModeDeterministic ExecutionMode = "deterministic"
	ModeAdaptive      ExecutionMode = "adaptive"
)

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(?:\.[a-z][a-z0-9_]*)*$`)

type Limits struct {
	TimeoutSec int     `json:"timeout_sec"`
	CPU        float64 `json:"cpu"`
	MemoryMB   int     `json:"memory_mb"`
	DiskMB     int     `json:"disk_mb"`
}

type Planner struct {
	Enabled  bool   `json:"enabled"`
	Model    string `json:"model,omitempty"`
	MaxSteps int    `json:"max_steps,omitempty"`
}

type FailurePolicy struct {
	RetryTransient      bool `json:"retry_transient"`
	AllowStrategyChange bool `json:"allow_strategy_change"`
	MaxRetries          int  `json:"max_retries"`
}

// Definition describes a versioned semantic capability independently of its physical adapter.
type Definition struct {
	Protocol      string         `json:"protocol"`
	Version       string         `json:"version"`
	ID            string         `json:"id"`
	ExecutionMode ExecutionMode  `json:"execution_mode"`
	InputSchema   map[string]any `json:"input_schema"`
	Capabilities  []string       `json:"capabilities"`
	Limits        Limits         `json:"limits"`
	Planner       Planner        `json:"planner"`
	FailurePolicy FailurePolicy  `json:"failure_policy"`
}

func (d *Definition) ApplyDefaults() {
	if d.Protocol == "" {
		d.Protocol = protocol.Name
	}
	if d.Version == "" {
		d.Version = protocol.Version
	}
	if d.InputSchema == nil {
		d.InputSchema = map[string]any{}
	}
	if d.Capabilities == nil {
		d.Capabilities = []string{}
	}
}

func (d Definition) Validate() error {
	if d.Protocol != protocol.Name || d.Version != protocol.Version {
		return fmt.Errorf("unsupported protocol version %q/%q", d.Protocol, d.Version)
	}
	if !namePattern.MatchString(d.ID) {
		return errors.New("id must be a semantic snake_case name")
	}
	if d.ExecutionMode != ModeDeterministic && d.ExecutionMode != ModeAdaptive {
		return errors.New("execution_mode must be deterministic or adaptive")
	}
	if d.InputSchema == nil {
		return errors.New("input_schema is required")
	}
	seen := make(map[string]struct{}, len(d.Capabilities))
	for _, capability := range d.Capabilities {
		if !namePattern.MatchString(capability) {
			return fmt.Errorf("invalid capability %q", capability)
		}
		if _, ok := seen[capability]; ok {
			return fmt.Errorf("duplicate capability %q", capability)
		}
		seen[capability] = struct{}{}
	}
	if d.Limits.TimeoutSec <= 0 || d.Limits.CPU <= 0 || d.Limits.MemoryMB <= 0 || d.Limits.DiskMB <= 0 {
		return errors.New("all limits must be positive")
	}
	if d.FailurePolicy.MaxRetries < 0 {
		return errors.New("max_retries cannot be negative")
	}
	if !d.FailurePolicy.RetryTransient && d.FailurePolicy.MaxRetries != 0 {
		return errors.New("max_retries requires retry_transient")
	}
	if d.ExecutionMode == ModeDeterministic {
		if d.Planner.Enabled || d.Planner.Model != "" || d.Planner.MaxSteps != 0 {
			return errors.New("deterministic affordance cannot configure a planner")
		}
		if d.FailurePolicy.AllowStrategyChange {
			return errors.New("deterministic affordance cannot allow strategy change")
		}
	} else if !d.Planner.Enabled || strings.TrimSpace(d.Planner.Model) == "" || d.Planner.MaxSteps <= 0 {
		return errors.New("adaptive affordance requires an enabled planner with model and positive max_steps")
	}
	return nil
}

// Request is model intent expressed in semantic terms, never a runtime event or physical command.
type Request struct {
	Protocol     string         `json:"protocol"`
	Version      string         `json:"version"`
	RequestID    string         `json:"request_id"`
	EpisodeID    string         `json:"episode_id"`
	AffordanceID string         `json:"affordance_id"`
	Arguments    map[string]any `json:"arguments"`
}

func (r *Request) ApplyDefaults() {
	if r.Protocol == "" {
		r.Protocol = protocol.Name
	}
	if r.Version == "" {
		r.Version = protocol.Version
	}
	if r.Arguments == nil {
		r.Arguments = map[string]any{}
	}
}

func (r Request) Validate() error {
	if r.Protocol != protocol.Name || r.Version != protocol.Version {
		return fmt.Errorf("unsupported protocol version %q/%q", r.Protocol, r.Version)
	}
	if strings.TrimSpace(r.RequestID) == "" || strings.TrimSpace(r.EpisodeID) == "" {
		return errors.New("request_id and episode_id are required")
	}
	if !namePattern.MatchString(r.AffordanceID) {
		return errors.New("affordance_id must be a semantic snake_case name")
	}
	if r.Arguments == nil {
		return errors.New("arguments are required")
	}
	return nil
}

func ValidateRequest(def Definition, request Request) error {
	if err := def.Validate(); err != nil {
		return fmt.Errorf("definition: %w", err)
	}
	if err := request.Validate(); err != nil {
		return fmt.Errorf("request: %w", err)
	}
	if request.AffordanceID != def.ID {
		return errors.New("request affordance_id does not match definition")
	}
	if request.Protocol != def.Protocol || request.Version != def.Version {
		return errors.New("request and definition protocol versions do not match")
	}
	return nil
}
