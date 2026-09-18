package cognition

import (
	"errors"
	"fmt"
	"time"

	"github.com/temporality-project/temporality/frp/frame"
)

// focusFlapHint is the threshold of consecutive focus changes after which the
// renderer's attention diagnostics warn the model about pathological focus
// switching.
const FocusFlapSteps = 3

type Rejection struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}
type Decision struct {
	Frame      frame.Frame      `json:"frame"`
	Transition frame.Transition `json:"transition"`
	Claims     []EmittedClaim   `json:"claims"`
	Actions    []ActionRequest  `json:"actions"`
	Rejections []Rejection     `json:"rejections"`
	Warnings   []string        `json:"warnings"`
}

// RefResolver reports whether a frame ref resolves in the substrate. The
// reducer uses it to reject hallucinated pins/attends visibly (a Rejection,
// the step survives) instead of failing the whole commit downstream — the
// same contract as claim_ops guards. A resolver error is a real store
// failure and fails the reduction.
type RefResolver func(frame.Ref) (bool, error)

func ReduceEmission(current frame.Frame, emission CognitiveEmission, now time.Time, resolve ...RefResolver) (Decision, error) {
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
	var resolver RefResolver
	if len(resolve) > 0 {
		resolver = resolve[0]
	}
	resolves := func(ref frame.Ref) (bool, error) {
		if resolver == nil || ref.Type == frame.RefQuery {
			return true, nil
		}
		return resolver(ref)
	}
	operations := make([]frame.Operation, 0, len(emission.Attention)+len(emission.FrameOps))
	rejections := make([]Rejection, 0)
	for i, attention := range emission.Attention {
		focus, err := attention.Target.Focus()
		if err != nil {
			return Decision{}, err
		}
		// An attend to a target the substrate has never heard of (a hallucinated
		// id) is a visible rejection: the focus stays where it was, the model
		// sees why, and the step still commits.
		if focus.Type != frame.RefQuery {
			exists, err := resolves(frame.Ref{Type: focus.Type, ID: focus.ID})
			if err != nil {
				return Decision{}, err
			}
			if !exists {
				rejections = append(rejections, Rejection{Path: fmt.Sprintf("attention[%d]", i), Reason: fmt.Sprintf("attend target %s:%s not found in substrate; focus unchanged", focus.Type, focus.ID)})
				continue
			}
		}
		operations = append(operations, frame.Operation{Kind: frame.OpAttend, Focus: &focus})
	}
	// M16 attention reliability: pins and unpins pass through deterministic
	// guards. The frame reducer stays permissive (historical transitions must
	// replay unchanged); the cognitive gate is here, where new emissions are
	// shaped — excess pins are rejected, no-op unpins become visible, and a
	// collapse to an empty working set is warned about, never silently applied.
	known := make(map[frame.Ref]struct{}, len(current.WorkingSet)+len(emission.FrameOps))
	for _, ref := range current.WorkingSet {
		known[ref] = struct{}{}
	}
	warnings := make([]string, 0)
	for i, candidate := range emission.FrameOps {
		ref, err := ParseRef(candidate.Ref, true)
		if err != nil {
			return Decision{}, err
		}
		// A pin of a ref that resolves nowhere (hallucinated or stale id) is
		// rejected visibly instead of failing the store commit — the model
		// slipped, the episode should not die for it. Re-pins are checked too:
		// a working set seeded via the API may carry a ref that never existed.
		if candidate.Op != "unpin" {
			exists, err := resolves(ref)
			if err != nil {
				return Decision{}, err
			}
			if !exists {
				rejections = append(rejections, Rejection{Path: fmt.Sprintf("frame_ops[%d]", i), Reason: fmt.Sprintf("pin target %s not found in substrate (hallucinated or stale ref); pin ignored", candidate.Ref)})
				continue
			}
		}
		switch candidate.Op {
		case "unpin":
			if _, present := known[ref]; !present {
				rejections = append(rejections, Rejection{Path: fmt.Sprintf("frame_ops[%d]", i), Reason: "unpin target is not in the working set"})
				continue
			}
			delete(known, ref)
		default: // pin
			if _, present := known[ref]; !present && len(known) >= frame.MaxWorkingSet {
				rejections = append(rejections, Rejection{Path: fmt.Sprintf("frame_ops[%d]", i), Reason: fmt.Sprintf("working set cap %d exceeded", frame.MaxWorkingSet)})
				continue
			}
			known[ref] = struct{}{}
		}
		kind := frame.OpPin
		if candidate.Op == "unpin" {
			kind = frame.OpUnpin
		}
		operations = append(operations, frame.Operation{Kind: kind, Ref: &ref})
	}
	if len(current.WorkingSet) > 0 && len(known) == 0 {
		warnings = append(warnings, "working set collapsed to empty: ambient attention is the only anchor left")
	}
	// The child frame advances to the commit time: a frame frozen at its
	// parent's as_of would forever exclude every later observation from
	// time-travel renders of it.
	transition := frame.Transition{Operations: operations, AsOf: now.UTC().Format(time.RFC3339Nano)}
	next, err := frame.Reduce(current, transition)
	if err != nil {
		return Decision{}, err
	}
	return Decision{Frame: next, Transition: transition, Claims: emission.Claims, Actions: emission.Actions, Rejections: rejections, Warnings: warnings}, nil
}
