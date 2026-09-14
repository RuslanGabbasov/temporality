package attention

import (
	"errors"
	"fmt"
	"math"
	"sort"

	"github.com/temporality-project/temporality/frp/frame"
)

const Version = "attention-0.3.1"

type Features struct {
	SemanticRelevance float64 `json:"semantic_relevance"`
	GraphProximity    float64 `json:"graph_proximity"`
	Recency           float64 `json:"recency"`
	Activation        float64 `json:"activation"`
	Trust             float64 `json:"trust"`
	TaskRelevance     float64 `json:"task_relevance"`
	Surprise          float64 `json:"surprise"`
	AgentRelevance    float64 `json:"agent_relevance"`
	Pin               float64 `json:"pin"`
}
type Weights Features
type Policy struct {
	Version         string  `json:"version"`
	Weights         Weights `json:"weights"`
	HysteresisBonus float64 `json:"hysteresis_bonus"`
}

func DefaultPolicy() Policy {
	return Policy{Version: Version, Weights: Weights{SemanticRelevance: .2, GraphProximity: .1, Recency: .15, Activation: .1, Trust: .1, TaskRelevance: .15, Surprise: .08, AgentRelevance: .05, Pin: .07}, HysteresisBonus: .08}
}

type Candidate struct {
	Ref      frame.Ref `json:"ref"`
	Features Features  `json:"features"`
	Payload  any       `json:"payload,omitempty"`
}
type ScoredCandidate struct {
	Candidate         Candidate `json:"candidate"`
	Score             float64   `json:"score"`
	HysteresisApplied bool      `json:"hysteresis_applied"`
}
type Result struct {
	Version    string            `json:"version"`
	Considered int               `json:"considered"`
	Selected   []ScoredCandidate `json:"selected"`
}
type Engine struct{ policy Policy }

func New(policy Policy) (*Engine, error) {
	if policy.Version == "" {
		return nil, errors.New("attention policy version is required")
	}
	if policy.HysteresisBonus < 0 {
		return nil, errors.New("hysteresis bonus cannot be negative")
	}
	return &Engine{policy: policy}, nil
}
func (e *Engine) SelectAmbient(candidates []Candidate, previous map[string]struct{}, limit int) (Result, error) {
	if limit < 1 {
		return Result{}, errors.New("attention limit must be positive")
	}
	scored := make([]ScoredCandidate, 0, len(candidates))
	seen := map[string]struct{}{}
	for _, candidate := range candidates {
		if err := candidate.Ref.Validate(); err != nil {
			return Result{}, err
		}
		key := string(candidate.Ref.Type) + ":" + candidate.Ref.ID
		if _, ok := seen[key]; ok {
			return Result{}, fmt.Errorf("duplicate attention candidate %s", key)
		}
		seen[key] = struct{}{}
		if err := validateFeatures(candidate.Features); err != nil {
			return Result{}, err
		}
		score := dot(candidate.Features, e.policy.Weights)
		_, sticky := previous[key]
		if sticky {
			score += e.policy.HysteresisBonus
		}
		scored = append(scored, ScoredCandidate{Candidate: candidate, Score: round(score), HysteresisApplied: sticky})
	}
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].Score != scored[j].Score {
			return scored[i].Score > scored[j].Score
		}
		a, b := scored[i].Candidate.Ref, scored[j].Candidate.Ref
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		return a.ID < b.ID
	})
	if len(scored) > limit {
		scored = scored[:limit]
	}
	return Result{Version: e.policy.Version, Considered: len(candidates), Selected: scored}, nil
}
func (e *Engine) SelectDeliberate(target frame.Focus) (frame.Focus, error) {
	if err := target.Validate(); err != nil {
		return frame.Focus{}, err
	}
	return target, nil
}
func validateFeatures(f Features) error {
	values := []float64{f.SemanticRelevance, f.GraphProximity, f.Recency, f.Activation, f.Trust, f.TaskRelevance, f.Surprise, f.AgentRelevance, f.Pin}
	for _, value := range values {
		if value < 0 || value > 1 {
			return errors.New("attention features must be between 0 and 1")
		}
	}
	return nil
}
func dot(f Features, w Weights) float64 {
	return f.SemanticRelevance*w.SemanticRelevance + f.GraphProximity*w.GraphProximity + f.Recency*w.Recency + f.Activation*w.Activation + f.Trust*w.Trust + f.TaskRelevance*w.TaskRelevance + f.Surprise*w.Surprise + f.AgentRelevance*w.AgentRelevance + f.Pin*w.Pin
}
func round(value float64) float64 { return math.Round(value*1e9) / 1e9 }
