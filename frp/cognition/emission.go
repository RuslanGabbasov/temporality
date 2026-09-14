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

// UnmarshalJSON accepts the canonical string ref and normalizes the common
// model mistake of copying a RenderPacket ref object {type,id}. Marshal output
// remains canonical because Ref is always stored as a string.
func (o *Observation) UnmarshalJSON(data []byte) error {
	var wire struct {
		Ref            json.RawMessage `json:"ref"`
		Interpretation string          `json:"interpretation"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	var ref string
	if err := json.Unmarshal(wire.Ref, &ref); err != nil {
		var object struct {
			Type frame.RefType `json:"type"`
			ID   string        `json:"id"`
		}
		if objectErr := json.Unmarshal(wire.Ref, &object); objectErr != nil || object.Type == "" || object.ID == "" {
			return errors.New("observation ref must be a canonical string or {type,id} object")
		}
		ref = string(object.Type) + ":" + object.ID
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
type FrameOperation struct {
	Op  string `json:"op"`
	Ref string `json:"ref"`
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
	Completion  *string              `json:"completion"`
}

func (e *CognitiveEmission) ApplyDefaults() {
	if e.Schema == "" {
		e.Schema = EmissionSchema
	}
	if e.Observation == nil {
		e.Observation = []Observation{}
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
	for _, o := range e.Observation {
		if _, err := ParseRef(o.Ref, false); err != nil {
			return fmt.Errorf("invalid observation ref: %w", err)
		}
		if strings.TrimSpace(o.Interpretation) == "" {
			return errors.New("observation interpretation is required")
		}
	}
	for _, r := range e.Reasoning {
		if strings.TrimSpace(r.Kind) == "" || strings.TrimSpace(r.Text) == "" {
			return errors.New("reasoning kind and text are required")
		}
	}
	for _, c := range e.Claims {
		if strings.TrimSpace(c.Proposition) == "" {
			return errors.New("claim proposition is required")
		}
		if c.Confidence < 0 || c.Confidence > 1 {
			return errors.New("claim confidence must be between 0 and 1")
		}
		if c.Status == "" {
			c.Status = ClaimCandidate
		}
		if c.Status != ClaimCandidate {
			return errors.New("emission claims must have candidate status")
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
		if _, err := ParseRef(op.Ref, true); err != nil {
			return err
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
	case frame.RefRegion, frame.RefClaim, frame.RefEvent, frame.RefExecution:
		return ref, nil
	default:
		return frame.Ref{}, fmt.Errorf("invalid ref type %q", kind)
	}
}
