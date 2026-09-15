package cognition

import (
	"errors"
	"time"

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

func ReduceEmission(current frame.Frame, emission CognitiveEmission, now time.Time) (Decision, error) {
	emission.ApplyDefaults()
	if err := emission.Validate(); err != nil {
		return Decision{}, err
	}
	if emission.FrameID != current.FrameID {
		return Decision{}, errors.New("emission frame_id does not match current frame")
	}
	if now.IsZero() {
		return Decision{}, errors.New("commit time is required")
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
	// The child frame advances to the commit time: a frame frozen at its
	// parent's as_of would forever exclude every later observation from
	// time-travel renders of it.
	transition := frame.Transition{Operations: operations, AsOf: now.UTC().Format(time.RFC3339Nano)}
	next, err := frame.Reduce(current, transition)
	if err != nil {
		return Decision{}, err
	}
	return Decision{Frame: next, Transition: transition, Claims: emission.Claims, Actions: emission.Actions, Rejections: []Rejection{}, Warnings: []string{}}, nil
}
