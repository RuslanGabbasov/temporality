package frame

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

type OperationKind string

const (
	OpAttend  OperationKind = "attend"
	OpPin     OperationKind = "pin"
	OpUnpin   OperationKind = "unpin"
	OpSetMode OperationKind = "set_mode"
	OpSetZoom OperationKind = "set_zoom"
)

type Operation struct {
	Kind  OperationKind `json:"op"`
	Focus *Focus        `json:"focus,omitempty"`
	Ref   *Ref          `json:"ref,omitempty"`
	Mode  Mode          `json:"mode,omitempty"`
	Zoom  *int          `json:"zoom,omitempty"`
}
type Transition struct {
	AsOf       string      `json:"as_of"`
	Operations []Operation `json:"operations"`
}

func Reduce(current Frame, transition Transition) (Frame, error) {
	if err := current.Validate(); err != nil {
		return Frame{}, fmt.Errorf("invalid current frame: %w", err)
	}
	encoded, err := json.Marshal(struct {
		Version    string     `json:"reducer_version"`
		Parent     string     `json:"parent"`
		Transition Transition `json:"transition"`
	}{ReducerVersion, current.FrameID, transition})
	if err != nil {
		return Frame{}, err
	}
	next := clone(current)
	next.ParentFrameID = current.FrameID
	next.Revision = current.Revision + 1
	for _, operation := range transition.Operations {
		if err = apply(&next, operation); err != nil {
			return Frame{}, err
		}
	}
	if transition.AsOf != "" {
		if err = json.Unmarshal([]byte(`"`+transition.AsOf+`"`), &next.AsOf); err != nil {
			return Frame{}, errors.New("invalid transition as_of")
		}
	}
	sum := sha256.Sum256(encoded)
	raw := sum[:16]
	raw[6] = (raw[6] & 0x0f) | 0x50
	raw[8] = (raw[8] & 0x3f) | 0x80
	h := hex.EncodeToString(raw)
	next.FrameID = h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
	if err = next.Validate(); err != nil {
		return Frame{}, fmt.Errorf("invalid next frame: %w", err)
	}
	return next, nil
}

func apply(frame *Frame, op Operation) error {
	switch op.Kind {
	case OpAttend:
		if op.Focus == nil {
			return errors.New("attend requires focus")
		}
		if err := op.Focus.Validate(); err != nil {
			return err
		}
		frame.Focus = *op.Focus
	case OpPin:
		if op.Ref == nil {
			return errors.New("pin requires ref")
		}
		if err := op.Ref.Validate(); err != nil {
			return err
		}
		for _, existing := range frame.WorkingSet {
			if existing == *op.Ref {
				return nil
			}
		}
		frame.WorkingSet = append(frame.WorkingSet, *op.Ref)
	case OpUnpin:
		if op.Ref == nil {
			return errors.New("unpin requires ref")
		}
		filtered := make([]Ref, 0, len(frame.WorkingSet))
		for _, existing := range frame.WorkingSet {
			if existing != *op.Ref {
				filtered = append(filtered, existing)
			}
		}
		frame.WorkingSet = filtered
	case OpSetMode:
		frame.Mode = op.Mode
	case OpSetZoom:
		if op.Zoom == nil {
			return errors.New("set_zoom requires zoom")
		}
		frame.Zoom = *op.Zoom
	default:
		return fmt.Errorf("unsupported frame operation %q", op.Kind)
	}
	return nil
}
func clone(source Frame) Frame {
	result := source
	result.WorkingSet = append([]Ref(nil), source.WorkingSet...)
	result.Filters.AgentIDs = append([]string(nil), source.Filters.AgentIDs...)
	result.Filters.RegionKinds = append([]string(nil), source.Filters.RegionKinds...)
	return result
}
