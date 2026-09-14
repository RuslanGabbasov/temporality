// Package modelstep orchestrates deterministic rendering, model emission, and
// atomic cognitive-step persistence.
package modelstep

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/temporality-project/temporality/frp/affordance"
	"github.com/temporality-project/temporality/frp/attention"
	"github.com/temporality-project/temporality/frp/cognition"
	"github.com/temporality-project/temporality/frp/frame"
	"github.com/temporality-project/temporality/frp/model"
	"github.com/temporality-project/temporality/frp/render"
	"github.com/temporality-project/temporality/frp/runtime/step"
)

type Input struct {
	FrameID      string
	ObjectiveID  string
	BudgetTokens int
	Definitions  map[string]affordance.Definition
}

type Store interface {
	render.Stores
	step.Store
}

type Service struct {
	Store           Store
	Adapter         model.Adapter
	ModelProvenance model.Provenance
	NewID           step.IDProvider
	Now             step.TimeProvider
}

type Result struct {
	RenderPacket    render.Packet               `json:"render_packet"`
	Emission        cognition.CognitiveEmission `json:"emission"`
	Step            step.Result                 `json:"step"`
	ModelProvenance model.Provenance            `json:"model_provenance"`
}

func (s Service) Run(ctx context.Context, input Input) (Result, error) {
	if s.Store == nil || s.Adapter == nil {
		return Result{}, errors.New("store and model adapter are required")
	}
	packet, err := render.New(s.Store).Render(ctx, render.Request{FrameID: input.FrameID, ObjectiveID: input.ObjectiveID, BudgetTokens: input.BudgetTokens})
	if err != nil {
		return Result{}, fmt.Errorf("render model input: %w", err)
	}
	current, err := s.Store.GetFrame(ctx, input.FrameID)
	if err != nil {
		return Result{}, err
	}
	suggestions, err := ambientSuggestions(packet)
	if err != nil {
		return Result{}, err
	}
	emission, err := s.Adapter.Emit(ctx, packet)
	if err != nil {
		return Result{}, fmt.Errorf("model emit: %w", err)
	}
	provenance, err := json.Marshal(s.ModelProvenance)
	if err != nil {
		return Result{}, fmt.Errorf("marshal model provenance: %w", err)
	}
	committed, err := step.Run(ctx, s.Store, step.Input{Current: current, Emission: emission, Definitions: input.Definitions, NewID: s.NewID, Now: s.Now, SuggestedAttention: suggestions, RenderPacket: &packet, ModelProvenance: provenance})
	if err != nil {
		return Result{}, err
	}
	return Result{RenderPacket: packet, Emission: emission, Step: committed, ModelProvenance: s.ModelProvenance}, nil
}

func ambientSuggestions(packet render.Packet) ([]frame.Ref, error) {
	result := []frame.Ref{}
	for _, section := range packet.Sections {
		if section.Kind != "map" {
			continue
		}
		for _, item := range section.Items {
			selected, ok := item.(attention.ScoredCandidate)
			if !ok {
				return nil, errors.New("render map contains an invalid attention candidate")
			}
			result = append(result, selected.Candidate.Ref)
		}
		break
	}
	return result, nil
}
