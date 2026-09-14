package cognition

import (
	"errors"

	"github.com/temporality-project/temporality/frp/frame"
)

type Rejection struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}
type Decision struct {
	Frame      frame.Frame      `json:"frame"`
	Transition frame.Transition `json:"transition"`
	Claims     []EmittedClaim   `json:"claims"`
	Actions    []ActionRequest  `json:"actions"`
	Rejections []Rejection      `json:"rejections"`
	Warnings   []string         `json:"warnings"`
}

func ReduceEmission(current frame.Frame, emission CognitiveEmission) (Decision, error) {
	emission.ApplyDefaults()
	if err := emission.Validate(); err != nil {
		return Decision{}, err
	}
	if emission.FrameID != current.FrameID {
		return Decision{}, errors.New("emission frame_id does not match current frame")
	}
	operations := make([]frame.Operation, 0, len(emission.Attention)+len(emission.FrameOps))
	for _, attention := range emission.Attention {
		focus, err := attention.Target.Focus()
		if err != nil {
			return Decision{}, err
		}
		operations = append(operations, frame.Operation{Kind: frame.OpAttend, Focus: &focus})
	}
	for _, candidate := range emission.FrameOps {
		ref, err := ParseRef(candidate.Ref, true)
		if err != nil {
			return Decision{}, err
		}
		kind := frame.OpPin
		if candidate.Op == "unpin" {
			kind = frame.OpUnpin
		}
		operations = append(operations, frame.Operation{Kind: kind, Ref: &ref})
	}
	transition := frame.Transition{Operations: operations}
	next, err := frame.Reduce(current, transition)
	if err != nil {
		return Decision{}, err
	}
	return Decision{Frame: next, Transition: transition, Claims: emission.Claims, Actions: emission.Actions, Rejections: []Rejection{}, Warnings: []string{}}, nil
}
