package step

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/temporality-project/temporality/frp/affordance"
	"github.com/temporality-project/temporality/frp/cognition"
	"github.com/temporality-project/temporality/frp/execution"
	"github.com/temporality-project/temporality/frp/frame"
	"github.com/temporality-project/temporality/frp/protocol"
	"github.com/temporality-project/temporality/frp/render"
)

const (
	EventClaimCandidate     = "claim.candidate"
	EventAttentionSelected  = "attention.selected"
	EventAttentionSuggested = "attention.suggested"
	EventFrameTransitioned  = "frame.transitioned"
)

var ErrCurrentFrameChanged = errors.New("current frame changed")

type IDProvider func() string
type TimeProvider func() time.Time

type Input struct {
	Current     frame.Frame
	Emission    cognition.CognitiveEmission
	Definitions map[string]affordance.Definition
	NewID       IDProvider
	Now         TimeProvider

	// World binding (M11): when set, executions created by this step snapshot
	// the world state so replay knows which environment the intent targeted.
	WorldID      string
	WorldVersion int

	// SuggestedAttention and render metadata are optional for compatibility with
	// callers that commit an already-produced emission.
	SuggestedAttention []frame.Ref
	RenderPacket       *render.Packet
	ModelProvenance    json.RawMessage
}

type Claim struct {
	Value cognition.Claim
	Event protocol.Event
}

type Action struct {
	Definition affordance.Definition
	Request    affordance.Request
	Execution  execution.Execution
	Requested  protocol.Event
	Created    protocol.Event
}

type Prepared struct {
	Current            frame.Frame
	Emission           cognition.CognitiveEmission
	EmissionJSON       []byte
	EmissionHash       string
	Decision           cognition.Decision
	Claims             []Claim
	Actions            []Action
	Events             []protocol.Event
	Transition         protocol.Event
	SuggestedAttention []frame.Ref
	RenderPacketJSON   []byte
	RenderPacketHash   string
	ModelProvenance    json.RawMessage
	RendererVersion    string
	AttentionVersion   string
}

type Result struct {
	Decision   cognition.Decision    `json:"decision"`
	Frame      frame.Frame           `json:"frame"`
	Claims     []cognition.Claim     `json:"claims"`
	Executions []execution.Execution `json:"executions"`
	Events     []protocol.Event      `json:"events"`
}

type Store interface {
	CommitStep(context.Context, Prepared) (Result, error)
}

func Run(ctx context.Context, store Store, input Input) (Result, error) {
	if store == nil || input.NewID == nil || input.Now == nil {
		return Result{}, errors.New("store, ID provider, and time provider are required")
	}
	now := input.Now().UTC()
	if now.IsZero() {
		return Result{}, errors.New("time provider returned zero time")
	}
	decision, err := cognition.ReduceEmission(input.Current, input.Emission, now)
	if err != nil {
		return Result{}, err
	}
	// ReduceEmission applies defaults to a copy; retain that canonical form for durable storage.
	emission := input.Emission
	emission.ApplyDefaults()
	emissionJSON, err := json.Marshal(emission)
	if err != nil {
		return Result{}, err
	}
	sum := sha256.Sum256(emissionJSON)
	prepared := Prepared{Current: input.Current, Emission: emission, EmissionJSON: emissionJSON, EmissionHash: hex.EncodeToString(sum[:]), Decision: decision, Claims: []Claim{}, Actions: []Action{}, Events: []protocol.Event{}, SuggestedAttention: append([]frame.Ref(nil), input.SuggestedAttention...), ModelProvenance: append(json.RawMessage(nil), input.ModelProvenance...)}
	if input.RenderPacket != nil {
		prepared.RenderPacketJSON, err = json.Marshal(input.RenderPacket)
		if err != nil {
			return Result{}, fmt.Errorf("marshal render packet: %w", err)
		}
		renderSum := sha256.Sum256(prepared.RenderPacketJSON)
		prepared.RenderPacketHash = hex.EncodeToString(renderSum[:])
		prepared.RendererVersion = input.RenderPacket.RendererVersion
		prepared.AttentionVersion = input.RenderPacket.Provenance.AttentionVersion
	}

	newEvent := func(kind string, payload map[string]any) (protocol.Event, error) {
		id := input.NewID()
		if id == "" {
			return protocol.Event{}, errors.New("ID provider returned an empty ID")
		}
		e := protocol.Event{EventID: id, TransactionTime: now, ValidTime: now, AgentID: input.Current.AgentID, EpisodeID: input.Current.EpisodeID, BranchID: input.Current.BranchID, Type: kind, Payload: payload, Provenance: map[string]any{"source": "cognitive_step", "emission_id": emission.EmissionID}}
		e.ApplyDefaults(now)
		return e, e.Validate()
	}

	for _, emitted := range decision.Claims {
		claimID := input.NewID()
		if claimID == "" {
			return Result{}, errors.New("ID provider returned an empty ID")
		}
		event, eventErr := newEvent(EventClaimCandidate, map[string]any{"claim_id": claimID, "proposition": emitted.Proposition, "confidence": emitted.Confidence, "emission_id": emission.EmissionID})
		if eventErr != nil {
			return Result{}, eventErr
		}
		value := cognition.Claim{ClaimID: claimID, Proposition: emitted.Proposition, Confidence: emitted.Confidence, Status: cognition.ClaimCandidate, CreatedEvent: event.EventID, ValidFrom: now}
		value.ApplyDefaults(now)
		if err = value.Validate(); err != nil {
			return Result{}, err
		}
		prepared.Claims = append(prepared.Claims, Claim{Value: value, Event: event})
		prepared.Events = append(prepared.Events, event)
	}
	for _, suggested := range input.SuggestedAttention {
		if err = suggested.Validate(); err != nil {
			return Result{}, fmt.Errorf("suggested attention: %w", err)
		}
		event, eventErr := newEvent(EventAttentionSuggested, map[string]any{"emission_id": emission.EmissionID, "ref": string(suggested.Type) + ":" + suggested.ID})
		if eventErr != nil {
			return Result{}, eventErr
		}
		prepared.Events = append(prepared.Events, event)
	}
	if input.Current.Attention.Deliberate {
		for _, selected := range emission.Attention {
			event, eventErr := newEvent(EventAttentionSelected, map[string]any{"emission_id": emission.EmissionID, "target": selected.Target})
			if eventErr != nil {
				return Result{}, eventErr
			}
			prepared.Events = append(prepared.Events, event)
		}
	}
	for _, requestedAction := range decision.Actions {
		def, ok := input.Definitions[requestedAction.Affordance]
		if !ok {
			return Result{}, fmt.Errorf("action %q: %w", requestedAction.Affordance, affordance.ErrDefinitionNotFound)
		}
		requestID, executionID := input.NewID(), input.NewID()
		if requestID == "" || executionID == "" {
			return Result{}, errors.New("ID provider returned an empty ID")
		}
		request := affordance.Request{RequestID: requestID, EpisodeID: input.Current.EpisodeID, AffordanceID: requestedAction.Affordance, Arguments: requestedAction.Args}
		request.ApplyDefaults()
		requested, eventErr := newEvent(affordance.EventRequested, map[string]any{"request_id": requestID, "emission_id": emission.EmissionID})
		if eventErr != nil {
			return Result{}, eventErr
		}
		created, eventErr := newEvent(execution.EventExecutionCreated, map[string]any{"execution_id": executionID, "request_id": requestID, "emission_id": emission.EmissionID})
		if eventErr != nil {
			return Result{}, eventErr
		}
		value := execution.Execution{Protocol: protocol.Name, Version: protocol.Version, ExecutionID: executionID, RequestID: requestID, EpisodeID: input.Current.EpisodeID, BranchID: input.Current.BranchID, AffordanceID: def.ID, WorldID: input.WorldID, WorldVersion: input.WorldVersion, Status: execution.StatusCreated, CreatedEventID: created.EventID, IntentPersistedAt: now}
		if err = execution.ValidateCreate(def, request, value, requested, created); err != nil {
			return Result{}, fmt.Errorf("action %q: %w", requestedAction.Affordance, err)
		}
		prepared.Actions = append(prepared.Actions, Action{Definition: def, Request: request, Execution: value, Requested: requested, Created: created})
		prepared.Events = append(prepared.Events, requested, created)
	}
	transition, err := newEvent(EventFrameTransitioned, map[string]any{"parent_frame_id": input.Current.FrameID, "frame_id": decision.Frame.FrameID, "emission_id": emission.EmissionID})
	if err != nil {
		return Result{}, err
	}
	if err = frame.ValidateTransition(input.Current.FrameID, decision.Transition, transition); err != nil {
		return Result{}, err
	}
	if err = frame.ValidateCreateEventForTransition(decision.Frame, transition); err != nil {
		return Result{}, err
	}
	prepared.Transition = transition
	prepared.Events = append(prepared.Events, transition)
	return store.CommitStep(ctx, prepared)
}
