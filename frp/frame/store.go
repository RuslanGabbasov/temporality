package frame

import (
	"context"
	"errors"

	"github.com/temporality-project/temporality/frp/protocol"
)

var (
	ErrFrameNotFound = errors.New("frame not found")
	ErrFrameExists   = errors.New("frame already exists")
)

type TransitionResult struct {
	Frame Frame          `json:"frame"`
	Event protocol.Event `json:"event"`
}

type Store interface {
	CreateFrame(context.Context, Frame, protocol.Event) error
	TransitionFrame(context.Context, string, Transition, protocol.Event) (TransitionResult, error)
	GetFrame(context.Context, string) (Frame, error)
}

func ValidateCreate(value Frame, event protocol.Event) error {
	if err := value.Validate(); err != nil {
		return err
	}
	if err := event.Validate(); err != nil {
		return err
	}
	if value.ParentFrameID != "" || value.Revision != 0 {
		return errors.New("initial frame must have no parent and revision zero")
	}
	if event.Type != "frame.created" {
		return errors.New("initial frame event type must be frame.created")
	}
	return validateFrameEvent(value, event)
}

func ValidateTransition(parentID string, transition Transition, event protocol.Event) error {
	if parentID == "" {
		return errors.New("parent frame id is required")
	}
	if err := event.Validate(); err != nil {
		return err
	}
	if event.Type != "frame.transitioned" {
		return errors.New("transition event type must be frame.transitioned")
	}
	if id, ok := event.Payload["parent_frame_id"].(string); !ok || id != parentID {
		return errors.New("transition event must reference parent_frame_id")
	}
	return nil
}

func ValidateCreateEventForTransition(value Frame, event protocol.Event) error {
	return validateFrameEvent(value, event)
}

func validateFrameEvent(value Frame, event protocol.Event) error {
	if id, ok := event.Payload["frame_id"].(string); !ok || id != value.FrameID {
		return errors.New("event must reference frame_id")
	}
	if event.EpisodeID != "" && event.EpisodeID != value.EpisodeID {
		return errors.New("event episode_id does not match frame")
	}
	if event.BranchID != "" && event.BranchID != value.BranchID {
		return errors.New("event branch_id does not match frame")
	}
	return nil
}
