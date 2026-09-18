package cognition

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/temporality-project/temporality/frp/frame"
)

const EmissionSchema = "frp.cognitive-emission.v1"

type Observation struct {
	Ref            string `json:"ref"`
	Interpretation string `json:"interpretation"`
}

// refFromWire accepts the canonical string ref ("event:UUID") or the common
// model mistake of copying a RenderPacket ref object {type,id,text} and
// returns the canonical string form. Empty input yields "" so callers can
// decide how unanchored items are handled.
func refFromWire(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var ref string
	if err := json.Unmarshal(raw, &ref); err == nil {
		return ref, nil
	}
	var object struct {
		Type frame.RefType `json:"type"`
		ID   string        `json:"id"`
		Text string        `json:"text"`
	}
	if objectErr := json.Unmarshal(raw, &object); objectErr != nil || object.Type == "" {
		return "", errors.New("ref must be a canonical string or typed object")
	}
	value := object.ID
	if object.Type == frame.RefQuery {
		value = object.Text
	}
	if value == "" {
		return "", errors.New("ref object requires id, or text for query")
	}
	return string(object.Type) + ":" + value, nil
}

// UnmarshalJSON accepts the canonical string ref and normalizes the common
// model mistake of copying a RenderPacket ref object {type,id}. Marshal output
// remains canonical because Ref is always stored as a string. An absent or
// empty ref decodes to ""; Validate tolerates it and ApplyDefaults drops the
// unanchored item — its interpretation belongs in reasoning.
func (o *Observation) UnmarshalJSON(data []byte) error {
	var wire struct {
		Ref            json.RawMessage `json:"ref"`
		Interpretation string          `json:"interpretation"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	ref, err := refFromWire(wire.Ref)
	if err != nil {
		return fmt.Errorf("observation %s", err)
	}
	o.Ref = ref
	o.Interpretation = wire.Interpretation
	return nil
}

type Reasoning struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}
type EmittedClaim struct {
	Proposition string      `json:"proposition"`
	Confidence  float32     `json:"confidence"`
	Status      ClaimStatus `json:"status"`
	// Supersedes optionally names an existing claim ("claim:UUID") this claim
	// replaces. The old claim transitions to superseded with lineage to this
	// one — reconciliation of a seen tension without inventing new machinery.
	Supersedes string `json:"supersedes,omitempty"`
	// Evidence optionally cites pre-existing events ("event:UUID") that
	// directly back the proposition. A claim born "supported" must cite
	// evidence — an unproven fact is a hypothesis, and warm memory must be
	// able to distinguish facts from speculation (pivot knowledge lifecycle).
	Evidence []string `json:"evidence,omitempty"`
}

// UnmarshalJSON keeps the canonical claim shape and normalizes the common
// model mistake of a typed ref object in supersedes, mirroring FrameOperation.
// Evidence items accept the same shapes: canonical strings or ref objects.
func (e *EmittedClaim) UnmarshalJSON(data []byte) error {
	var wire struct {
		Proposition string          `json:"proposition"`
		Confidence  float32         `json:"confidence"`
		Status      ClaimStatus     `json:"status"`
		Supersedes  json.RawMessage `json:"supersedes"`
		Evidence    json.RawMessage `json:"evidence"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	supersedes, err := refFromWire(wire.Supersedes)
	if err != nil {
		return fmt.Errorf("claim supersedes %s", err)
	}
	evidence, err := refListFromWire(wire.Evidence)
	if err != nil {
		return fmt.Errorf("claim %s", err)
	}
	e.Proposition, e.Confidence, e.Status, e.Supersedes, e.Evidence = wire.Proposition, wire.Confidence, wire.Status, supersedes, evidence
	return nil
}

// refListFromWire accepts an array of canonical string refs ("event:UUID")
// with the common model mistake of ref objects mixed in, mirroring refFromWire.
func refListFromWire(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, errors.New("evidence must be an array of event refs")
	}
	refs := make([]string, 0, len(items))
	for _, item := range items {
		ref, err := refFromWire(item)
		if err != nil {
			return nil, fmt.Errorf("evidence %s", err)
		}
		refs = append(refs, ref)
	}
	return refs, nil
}
type AttentionTarget struct {
	Type frame.RefType `json:"type"`
	ID   string        `json:"id,omitempty"`
	Text string        `json:"text,omitempty"`
}
type AttentionOperation struct {
	Op     string          `json:"op"`
	Target AttentionTarget `json:"target"`
}
type ActionRequest struct {
	Affordance string         `json:"affordance"`
	Args       map[string]any `json:"args"`
}

// UnmarshalJSON accepts the canonical {"affordance","args"} action shape and
// normalizes the common model mistake of tool-call style {"id","arguments"}
// or {"name","params"} items. Marshal output stays canonical, and validation
// still rejects affordance ids outside the step's definitions.
func (a *ActionRequest) UnmarshalJSON(data []byte) error {
	var wire struct {
		Affordance string         `json:"affordance"`
		Args       map[string]any `json:"args"`
		ID         string         `json:"id"`
		Name       string         `json:"name"`
		Arguments  map[string]any `json:"arguments"`
		Params     map[string]any `json:"params"`
		Parameters map[string]any `json:"parameters"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	a.Affordance = firstNonEmpty(wire.Affordance, wire.ID, wire.Name)
	a.Args = wire.Args
	if a.Args == nil {
		a.Args = wire.Arguments
	}
	if a.Args == nil {
		a.Args = wire.Params
	}
	if a.Args == nil {
		a.Args = wire.Parameters
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

type FrameOperation struct {
	Op  string `json:"op"`
	Ref string `json:"ref"`
}

// UnmarshalJSON accepts the canonical string ref and normalizes the common
// model mistake of a typed ref object {type,id}, mirroring Observation.
func (f *FrameOperation) UnmarshalJSON(data []byte) error {
	var wire struct {
		Op  string          `json:"op"`
		Ref json.RawMessage `json:"ref"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	ref, err := refFromWire(wire.Ref)
	if err != nil {
		return fmt.Errorf("frame op %s", err)
	}
	f.Op = wire.Op
	f.Ref = ref
	return nil
}

// ClaimOperation reconciles memory (M16, pivot lifecycle): refute names a
// claim the model saw — typically through memory_health — as wrong, retiring
// it from the active base; confirm promotes a live hypothesis (candidate) to
// supported once the agent has verified it (e.g. an execution result proved
// it). The runtime never invents these; ids are copied from the packet.
type ClaimOperation struct {
	Op    string `json:"op"`
	Claim string `json:"claim"`
}

// UnmarshalJSON accepts the canonical string ref and normalizes the common
// model mistake of a typed ref object {type,id}, mirroring FrameOperation.
func (c *ClaimOperation) UnmarshalJSON(data []byte) error {
	var wire struct {
		Op    string          `json:"op"`
		Claim json.RawMessage `json:"claim"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	claim, err := refFromWire(wire.Claim)
	if err != nil {
		return fmt.Errorf("claim op %s", err)
	}
	c.Op = wire.Op
	c.Claim = claim
	return nil
}

type CognitiveEmission struct {
	Schema      string               `json:"schema"`
	EmissionID  string               `json:"emission_id"`
	FrameID     string               `json:"frame_id"`
	Observation []Observation        `json:"observation"`
	Reasoning   []Reasoning          `json:"reasoning"`
	Claims      []EmittedClaim       `json:"claims"`
	Attention   []AttentionOperation `json:"attention"`
	Actions     []ActionRequest      `json:"actions"`
	FrameOps    []FrameOperation     `json:"frame_ops"`
	ClaimOps    []ClaimOperation     `json:"claim_ops"`
	Completion  *string              `json:"completion"`
}

// UnmarshalJSON keeps field-level normalization (observation/action/frame op
// shapes) and additionally accepts completion as {text|answer|summary|content}
// — a common model shortcut for the final answer. Canonical output remains a
// plain string or null.
func (e *CognitiveEmission) UnmarshalJSON(data []byte) error {
	type emissionWire CognitiveEmission
	var wire struct {
		emissionWire
		// Shadows the embedded *string so an object completion reaches
		// completionFromWire instead of failing the whole decode.
		Completion json.RawMessage `json:"completion"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	completion, err := completionFromWire(wire.Completion)
	if err != nil {
		return err
	}
	*e = CognitiveEmission(wire.emissionWire)
	e.Completion = completion
	return nil
}

func completionFromWire(raw json.RawMessage) (*string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		if strings.TrimSpace(text) == "" {
			return nil, nil
		}
		return &text, nil
	}
	var object struct {
		Text    string `json:"text"`
		Answer  string `json:"answer"`
		Summary string `json:"summary"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, errors.New("completion must be a string, an object with text/answer/summary/content, or null")
	}
	for _, value := range []string{object.Text, object.Answer, object.Summary, object.Content} {
		if strings.TrimSpace(value) != "" {
			return &value, nil
		}
	}
	return nil, nil
}

func (e *CognitiveEmission) ApplyDefaults() {
	if e.Schema == "" {
		e.Schema = EmissionSchema
	}
	if e.Observation == nil {
		e.Observation = []Observation{}
	} else {
		// Unanchored observations (empty ref) are model commentary, not
		// substrate interpretations; their interpretation belongs to reasoning.
		kept := make([]Observation, 0, len(e.Observation))
		for _, observation := range e.Observation {
			if strings.TrimSpace(observation.Ref) == "" {
				continue
			}
			kept = append(kept, observation)
		}
		e.Observation = kept
	}
	if e.Reasoning == nil {
		e.Reasoning = []Reasoning{}
	}
	if e.Claims == nil {
		e.Claims = []EmittedClaim{}
	}
	for i := range e.Claims {
		if e.Claims[i].Status == "" {
			e.Claims[i].Status = ClaimCandidate
		}
	}
	if e.Attention == nil {
		e.Attention = []AttentionOperation{}
	}
	if e.Actions == nil {
		e.Actions = []ActionRequest{}
	}
	if e.FrameOps == nil {
		e.FrameOps = []FrameOperation{}
	} else {
		kept := make([]FrameOperation, 0, len(e.FrameOps))
		for _, op := range e.FrameOps {
			if strings.TrimSpace(op.Ref) == "" {
				continue
			}
			kept = append(kept, op)
		}
		e.FrameOps = kept
	}
	if e.ClaimOps == nil {
		e.ClaimOps = []ClaimOperation{}
	} else {
		// Unanchored claim ops (empty ref) are dropped, not fatal — mirroring
		// frame_ops tolerance for recoverable model slips.
		kept := make([]ClaimOperation, 0, len(e.ClaimOps))
		for _, op := range e.ClaimOps {
			if strings.TrimSpace(op.Claim) == "" {
				continue
			}
			kept = append(kept, op)
		}
		e.ClaimOps = kept
	}
	for i := range e.Claims {
		e.Claims[i].Supersedes = strings.TrimSpace(e.Claims[i].Supersedes)
		// Evidence hygiene mirrors the rest of the schema: empty entries are
		// dropped and duplicates collapse so a sloppy model cannot smuggle
		// noise into the provenance chain.
		if len(e.Claims[i].Evidence) > 0 {
			kept := make([]string, 0, len(e.Claims[i].Evidence))
			seen := make(map[string]struct{}, len(e.Claims[i].Evidence))
			for _, ref := range e.Claims[i].Evidence {
				ref = strings.TrimSpace(ref)
				if ref == "" {
					continue
				}
				if _, duplicate := seen[ref]; duplicate {
					continue
				}
				seen[ref] = struct{}{}
				kept = append(kept, ref)
			}
			if len(kept) == 0 {
				kept = nil
			}
			e.Claims[i].Evidence = kept
		}
	}
}

func (e CognitiveEmission) Validate() error {
	if e.Schema != EmissionSchema {
		return fmt.Errorf("unsupported emission schema %q", e.Schema)
	}
	if e.EmissionID == "" {
		return errors.New("emission_id is required")
	}
	if e.FrameID == "" {
		return errors.New("frame_id is required")
	}
	for i, o := range e.Observation {
		// Empty refs are tolerated here and dropped by ApplyDefaults before
		// anything is persisted: an unanchored observation carries no
		// substrate meaning, and failing the whole emission for it would make
		// the cognition loop brittle against sloppy models.
		if strings.TrimSpace(o.Ref) == "" {
			continue
		}
		if _, err := ParseRef(o.Ref, false); err != nil {
			return fmt.Errorf("observation[%d]: invalid ref: %w", i, err)
		}
		if strings.TrimSpace(o.Interpretation) == "" {
			return fmt.Errorf("observation[%d]: interpretation is required", i)
		}
	}
	for _, r := range e.Reasoning {
		if strings.TrimSpace(r.Kind) == "" || strings.TrimSpace(r.Text) == "" {
			return errors.New("reasoning kind and text are required")
		}
	}
	for i, c := range e.Claims {
		if strings.TrimSpace(c.Proposition) == "" {
			return errors.New("claim proposition is required")
		}
		if c.Confidence < 0 || c.Confidence > 1 {
			return errors.New("claim confidence must be between 0 and 1")
		}
		if c.Status == "" {
			c.Status = ClaimCandidate
		}
		switch c.Status {
		case ClaimCandidate:
			// A hypothesis may cite the events that motivated it, but only a
			// cited fact may be born confirmed.
		case ClaimSupported:
			if len(c.Evidence) == 0 {
				return fmt.Errorf("claims[%d]: supported status requires evidence refs", i)
			}
		default:
			return errors.New("emission claims must have candidate or supported status")
		}
		for j, evidence := range c.Evidence {
			ref, err := ParseRef(evidence, false)
			if err != nil {
				return fmt.Errorf("claims[%d].evidence[%d]: %w", i, j, err)
			}
			if ref.Type != frame.RefEvent {
				return fmt.Errorf("claims[%d].evidence[%d]: must reference an event, got %q", i, j, evidence)
			}
		}
	}
	for _, a := range e.Attention {
		if a.Op != "attend" {
			return fmt.Errorf("unsupported attention operation %q", a.Op)
		}
		if _, err := a.Target.Focus(); err != nil {
			return err
		}
	}
	for _, a := range e.Actions {
		if strings.TrimSpace(a.Affordance) == "" {
			return errors.New("action affordance is required")
		}
		if a.Args == nil {
			return errors.New("action args are required")
		}
	}
	for _, op := range e.FrameOps {
		if op.Op != "pin" && op.Op != "unpin" {
			return fmt.Errorf("unsupported frame operation %q", op.Op)
		}
		// Empty refs are tolerated here and dropped by ApplyDefaults: pinning
		// nothing is a no-op, and failing the emission for it would stall the
		// cognition loop on a recoverable model slip.
		if strings.TrimSpace(op.Ref) == "" {
			continue
		}
		if _, err := ParseRef(op.Ref, true); err != nil {
			return err
		}
	}
	for i, op := range e.ClaimOps {
		if op.Op != "refute" && op.Op != "confirm" {
			return fmt.Errorf("unsupported claim operation %q", op.Op)
		}
		if strings.TrimSpace(op.Claim) == "" {
			continue
		}
		ref, err := ParseRef(op.Claim, false)
		if err != nil {
			return fmt.Errorf("claim_ops[%d]: %w", i, err)
		}
		if ref.Type != frame.RefClaim {
			return fmt.Errorf("claim_ops[%d]: must target a claim ref, got %q", i, op.Claim)
		}
	}
	for i, c := range e.Claims {
		if strings.TrimSpace(c.Supersedes) == "" {
			continue
		}
		ref, err := ParseRef(c.Supersedes, false)
		if err != nil {
			return fmt.Errorf("claims[%d].supersedes: %w", i, err)
		}
		if ref.Type != frame.RefClaim {
			return fmt.Errorf("claims[%d].supersedes: must target a claim ref, got %q", i, c.Supersedes)
		}
	}
	return nil
}

func (t AttentionTarget) Focus() (frame.Focus, error) {
	if t.Type == frame.RefQuery {
		if strings.TrimSpace(t.Text) == "" || t.ID != "" {
			return frame.Focus{}, errors.New("query attention requires text and no id")
		}
		return frame.Focus{Type: frame.RefQuery, Query: t.Text}, nil
	}
	candidate := frame.Focus{Type: t.Type, ID: t.ID}
	return candidate, candidate.Validate()
}

func ParseRef(value string, workingSet bool) (frame.Ref, error) {
	kind, id, ok := strings.Cut(value, ":")
	if !ok || id == "" {
		return frame.Ref{}, fmt.Errorf("invalid ref %q", value)
	}
	ref := frame.Ref{Type: frame.RefType(kind), ID: id}
	if workingSet {
		return ref, ref.Validate()
	}
	switch ref.Type {
	case frame.RefRegion, frame.RefClaim, frame.RefEvent, frame.RefExecution, frame.RefEntity, frame.RefQuery:
		return ref, nil
	default:
		return frame.Ref{}, fmt.Errorf("invalid ref type %q", kind)
	}
}
