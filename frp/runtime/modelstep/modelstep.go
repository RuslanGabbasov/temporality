// Package modelstep orchestrates deterministic rendering, model emission, and
// atomic cognitive-step persistence.
package modelstep

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/temporality-project/temporality/frp/affordance"
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

	// World binding (M11): forwarded to the committed step so its executions
	// snapshot the world the model's actions target.
	WorldID      string
	WorldVersion int
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
	ModelUsage      model.Usage                 `json:"model_usage"`
}

func (s Service) Run(ctx context.Context, input Input) (Result, error) {
	if s.Store == nil || s.Adapter == nil {
		return Result{}, errors.New("store and model adapter are required")
	}
	// Definitions travel with the render packet so the model sees the actions
	// it may request; map iteration order must not leak into the packet.
	definitions := make([]affordance.Definition, 0, len(input.Definitions))
	for _, definition := range input.Definitions {
		definitions = append(definitions, definition)
	}
	sort.Slice(definitions, func(i, j int) bool { return definitions[i].ID < definitions[j].ID })
	packet, err := render.New(s.Store).Render(ctx, render.Request{FrameID: input.FrameID, ObjectiveID: input.ObjectiveID, BudgetTokens: input.BudgetTokens, Affordances: definitions, WorldID: input.WorldID})
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
	emission, usage, err := s.Adapter.Emit(ctx, packet)
	if err != nil {
		return Result{}, fmt.Errorf("model emit: %w", err)
	}
	provenance, err := json.Marshal(s.ModelProvenance)
	if err != nil {
		return Result{}, fmt.Errorf("marshal model provenance: %w", err)
	}
	committed, err := step.Run(ctx, s.Store, step.Input{Current: current, Emission: emission, Definitions: input.Definitions, WorldID: input.WorldID, WorldVersion: input.WorldVersion, NewID: s.NewID, Now: s.Now, SuggestedAttention: suggestions, RenderPacket: &packet, ModelProvenance: provenance})
	if err != nil {
		return Result{}, err
	}
	return Result{RenderPacket: packet, Emission: emission, Step: committed, ModelProvenance: s.ModelProvenance, ModelUsage: usage}, nil
}

func ambientSuggestions(packet render.Packet) ([]frame.Ref, error) {
	result := []frame.Ref{}
	for _, section := range packet.Sections {
		if section.Kind != "map" {
			continue
		}
		for _, item := range section.Items {
			selected, ok := item.(render.MapItem)
			if !ok {
				return nil, errors.New("render map contains an invalid attention candidate")
			}
			ref, err := cognition.ParseRef(selected.Ref, false)
			if err != nil {
				return nil, fmt.Errorf("render map ref %q: %w", selected.Ref, err)
			}
			result = append(result, ref)
		}
		break
	}
	return result, nil
}
