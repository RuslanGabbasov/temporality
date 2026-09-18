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
	EventFocusChanged       = "focus.changed"
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
	// Evidence holds bare event ids (no "event:" prefix) backing the claim —
	// the same shape cognition.Commit.Evidence uses. Stores persist them into
	// claim_evidence atomically with the claim, after verifying the events
	// exist (pivot knowledge lifecycle: model claims keep provenance).
	Evidence []string
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
	// ClaimTransitions (M16) carries refute/supersede operations resolved by
	// the guards below; stores apply them atomically with the step. Their
	// events also travel in Events (between attention and action events) so
	// the positional insert loops in stores pick them up unchanged.
	ClaimTransitions []cognition.Transition
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
	// ListClaims feeds M16 memory-tension detection: the step compares the
	// claims it is about to persist against the claim base it is entering.
	ListClaims(context.Context) ([]cognition.Claim, error)
}

func Run(ctx context.Context, store Store, input Input) (Result, error) {
	if store == nil || input.NewID == nil || input.Now == nil {
		return Result{}, errors.New("store, ID provider, and time provider are required")
	}
	now := input.Now().UTC()
	if now.IsZero() {
		return Result{}, errors.New("time provider returned zero time")
	}
	// Hallucinated pin/attend refs are visible rejections, not step failures:
	// when the store can answer ref existence, the reducer filters the ops and
	// records why. Stores without the check keep the old behavior — the store
	// guard still fails genuinely impossible refs at commit time.
	var resolver cognition.RefResolver
	if refStore, ok := store.(interface {
		RefExists(context.Context, frame.Ref) (bool, error)
	}); ok {
		resolver = func(ref frame.Ref) (bool, error) { return refStore.RefExists(ctx, ref) }
	}
	decision, err := cognition.ReduceEmission(input.Current, input.Emission, now, resolver)
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
		prepared.RenderPacketJSON, err = render.MarshalPacket(*input.RenderPacket)
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
		// Pivot knowledge lifecycle: a claim is born the status the model
		// earned — candidate for hypotheses, supported only for facts whose
		// evidence the emission cites (Validate enforces the pairing). The
		// creation event type mirrors the status so the log alone reads as a
		// lifecycle.
		status := emitted.Status
		if status == "" {
			status = cognition.ClaimCandidate
		}
		claimType := EventClaimCandidate
		if status == cognition.ClaimSupported {
			claimType = "claim.supported"
		}
		payload := map[string]any{"claim_id": claimID, "proposition": emitted.Proposition, "confidence": emitted.Confidence, "emission_id": emission.EmissionID}
		var evidenceIDs []string
		if len(emitted.Evidence) > 0 {
			evidenceIDs = make([]string, 0, len(emitted.Evidence))
			for _, ref := range emitted.Evidence {
				if parsed, parseErr := cognition.ParseRef(ref, false); parseErr == nil && parsed.Type == frame.RefEvent {
					evidenceIDs = append(evidenceIDs, parsed.ID)
				}
			}
			payload["evidence"] = append([]string(nil), emitted.Evidence...)
		}
		event, eventErr := newEvent(claimType, payload)
		if eventErr != nil {
			return Result{}, eventErr
		}
		value := cognition.Claim{ClaimID: claimID, Proposition: emitted.Proposition, Confidence: emitted.Confidence, Status: status, CreatedEvent: event.EventID, ValidFrom: now}
		value.ApplyDefaults(now)
		if err = value.Validate(); err != nil {
			return Result{}, err
		}
		prepared.Claims = append(prepared.Claims, Claim{Value: value, Event: event, Evidence: evidenceIDs})
		prepared.Events = append(prepared.Events, event)
	}
	// M16 memory reliability: what does this emission do to memory? The claim
	// base the step enters is loaded once — tension detection (what the new
	// claims introduce) and claim_ops guards (what the emission retires) both
	// read it; the step stays deterministic, so replay reproduces the same
	// outcome from the same state. Nothing is merged or dropped silently:
	// tensions travel with the transition event, refutations carry lineage.
	var memoryTensions []cognition.MemoryTension
	var claimBase []cognition.Claim
	needsClaimBase := len(prepared.Claims) > 0 || len(emission.ClaimOps) > 0
	for _, emitted := range emission.Claims {
		if emitted.Supersedes != "" {
			needsClaimBase = true
		}
	}
	if needsClaimBase {
		var listErr error
		claimBase, listErr = store.ListClaims(ctx)
		if listErr != nil {
			return Result{}, listErr
		}
	}
	if len(prepared.Claims) > 0 {
		incomingClaims := make([]cognition.Claim, 0, len(prepared.Claims))
		for _, claim := range prepared.Claims {
			incomingClaims = append(incomingClaims, claim.Value)
		}
		memoryTensions = cognition.NewTensions(claimBase, incomingClaims)
	}
	// Claim reconciliation guards: the model may refute a claim it saw, or
	// supersede one with a claim emitted in this same step. Schema-level
	// failures (unknown op, malformed ref) already failed validation; here a
	// wrong-but-well-formed target is a visible Rejection, not an error — the
	// same contract as the working-set guards.
	claimRejections := make([]cognition.Rejection, 0)
	claimTransitions := make([]cognition.Transition, 0)
	targeted := make(map[string]struct{})
	claimByID := make(map[string]cognition.Claim, len(claimBase))
	for _, claim := range claimBase {
		claimByID[claim.ClaimID] = claim
	}
	resolveTarget := func(ref, path string, to cognition.ClaimStatus) (cognition.Claim, bool) {
		parsed, parseErr := cognition.ParseRef(ref, false)
		if parseErr != nil {
			claimRejections = append(claimRejections, cognition.Rejection{Path: path, Reason: fmt.Sprintf("invalid claim ref: %v", parseErr)})
			return cognition.Claim{}, false
		}
		claim, exists := claimByID[parsed.ID]
		if !exists {
			claimRejections = append(claimRejections, cognition.Rejection{Path: path, Reason: "claim not found"})
			return cognition.Claim{}, false
		}
		if _, duplicate := targeted[claim.ClaimID]; duplicate {
			claimRejections = append(claimRejections, cognition.Rejection{Path: path, Reason: "claim already targeted by this emission"})
			return cognition.Claim{}, false
		}
		if !cognition.CanTransition(claim.Status, to) {
			reason := fmt.Sprintf("claim %q is not active", claim.Status)
			if claim.Status == cognition.ClaimSupported {
				reason = fmt.Sprintf("claim %q is already supported", claim.Status)
			}
			claimRejections = append(claimRejections, cognition.Rejection{Path: path, Reason: reason})
			return cognition.Claim{}, false
		}
		targeted[claim.ClaimID] = struct{}{}
		return claim, true
	}
	for i, op := range emission.ClaimOps {
		// Pivot knowledge lifecycle: refute retires a wrong claim, confirm
		// promotes a verified hypothesis to supported. Both ride the same
		// transition machinery and the same guards — only the target status
		// and event type differ.
		toStatus := cognition.ClaimRefuted
		eventType := "claim.refuted"
		if op.Op == "confirm" {
			toStatus = cognition.ClaimSupported
			eventType = "claim.supported"
		}
		claim, ok := resolveTarget(op.Claim, fmt.Sprintf("claim_ops[%d]", i), toStatus)
		if !ok {
			continue
		}
		event, eventErr := newEvent(eventType, map[string]any{"claim_id": claim.ClaimID, "emission_id": emission.EmissionID})
		if eventErr != nil {
			return Result{}, eventErr
		}
		transition := cognition.Transition{Event: event, ClaimID: claim.ClaimID, ToStatus: toStatus, ValidAt: now}
		if err = transition.Validate(); err != nil {
			return Result{}, err
		}
		claimTransitions = append(claimTransitions, transition)
	}
	for j, emitted := range emission.Claims {
		if emitted.Supersedes == "" {
			continue
		}
		path := fmt.Sprintf("claims[%d].supersedes", j)
		claim, ok := resolveTarget(emitted.Supersedes, path, cognition.ClaimSuperseded)
		if !ok {
			continue
		}
		event, eventErr := newEvent("claim.superseded", map[string]any{"claim_id": claim.ClaimID, "superseded_by": prepared.Claims[j].Value.ClaimID, "emission_id": emission.EmissionID})
		if eventErr != nil {
			return Result{}, eventErr
		}
		transition := cognition.Transition{Event: event, ClaimID: claim.ClaimID, ToStatus: cognition.ClaimSuperseded, ValidAt: now}
		if err = transition.Validate(); err != nil {
			return Result{}, err
		}
		claimTransitions = append(claimTransitions, transition)
	}
	prepared.ClaimTransitions = claimTransitions
	for _, transition := range claimTransitions {
		prepared.Events = append(prepared.Events, transition.Event)
	}
	// Pivot investigation history: a real focus change is a first-class event
	// with from/to, a coarse trigger, and the refs that back it — the claim it
	// retired/confirmed plus the events observed this step. It rides the same
	// attention-range slot as the transition events, so stores, replay and the
	// event log see it without any new machinery.
	if from, to, changed := focusChange(input.Current.Focus, decision.Frame.Focus); changed {
		trigger, evidence := focusTrigger(claimTransitions, emission)
		payload := map[string]any{"from": from, "to": to, "trigger": trigger, "frame_before": input.Current.FrameID, "frame_after": decision.Frame.FrameID, "emission_id": emission.EmissionID}
		if len(evidence) > 0 {
			payload["evidence"] = evidence
		}
		focusEvent, focusErr := newEvent(EventFocusChanged, payload)
		if focusErr != nil {
			return Result{}, focusErr
		}
		prepared.Events = append(prepared.Events, focusEvent)
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
	// M16: guard diagnostics travel with the transition event so the audit
	// trail answers not only what the frame became, but which parts of the
	// emission the runtime refused — and why.
	transitionPayload := map[string]any{"parent_frame_id": input.Current.FrameID, "frame_id": decision.Frame.FrameID, "emission_id": emission.EmissionID}
	rejections := decision.Rejections
	if len(claimRejections) > 0 {
		rejections = append(append([]cognition.Rejection{}, rejections...), claimRejections...)
	}
	if len(rejections) > 0 {
		transitionPayload["rejections"] = rejections
	}
	if len(decision.Warnings) > 0 {
		transitionPayload["warnings"] = decision.Warnings
	}
	if len(memoryTensions) > 0 {
		transitionPayload["memory_tensions"] = memoryTensions
	}
	transition, err := newEvent(EventFrameTransitioned, transitionPayload)
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

// focusStringRef renders a focus as its canonical ref string so focus.changed
// events stay comparable across frames ("query:text" / "entity:Sum").
func focusStringRef(f frame.Focus) string {
	if f.Type == frame.RefQuery {
		return string(frame.RefQuery) + ":" + f.Query
	}
	return string(f.Type) + ":" + f.ID
}

func focusChange(current, next frame.Focus) (from, to string, changed bool) {
	from, to = focusStringRef(current), focusStringRef(next)
	return from, to, from != to
}

// focusTrigger classifies why attention moved. Retiring a hypothesis
// (refute/supersede) outranks confirming one — a refutation is the stronger
// reason to look elsewhere; anything else is a deliberate pivot, traceable to
// the emission's own reasoning via emission_id. Evidence names the claims
// transitioned this step plus the observed events, capped to keep the log
// entry readable.
func focusTrigger(transitions []cognition.Transition, emission cognition.CognitiveEmission) (string, []string) {
	const evidenceCap = 8
	trigger := "deliberate"
	evidence := make([]string, 0, evidenceCap)
	for _, transition := range transitions {
		if len(evidence) < evidenceCap {
			evidence = append(evidence, "claim:"+transition.ClaimID)
		}
		switch {
		case transition.ToStatus == cognition.ClaimRefuted || transition.ToStatus == cognition.ClaimSuperseded:
			trigger = "hypothesis_retired"
		case transition.ToStatus == cognition.ClaimSupported && trigger == "deliberate":
			trigger = "hypothesis_confirmed"
		}
	}
	for _, observation := range emission.Observation {
		if len(evidence) >= evidenceCap {
			break
		}
		if ref, err := cognition.ParseRef(observation.Ref, false); err == nil && ref.Type == frame.RefEvent {
			evidence = append(evidence, observation.Ref)
		}
	}
	return trigger, evidence
}
