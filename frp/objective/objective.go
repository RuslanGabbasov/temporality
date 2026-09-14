package objective

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/temporality-project/temporality/frp/protocol"
)

var ErrNotFound = errors.New("objective not found")

type Constraints struct {
	MaxCost  float64    `json:"max_cost,omitempty"`
	Deadline *time.Time `json:"deadline,omitempty"`
}
type Objective struct {
	Protocol          string      `json:"protocol"`
	Version           string      `json:"version"`
	ObjectiveID       string      `json:"objective_id"`
	EpisodeID         string      `json:"episode_id"`
	Text              string      `json:"text"`
	SuccessConditions []string    `json:"success_conditions"`
	Constraints       Constraints `json:"constraints"`
}

func (o *Objective) ApplyDefaults() {
	if o.Protocol == "" {
		o.Protocol = protocol.Name
	}
	if o.Version == "" {
		o.Version = protocol.Version
	}
	if o.SuccessConditions == nil {
		o.SuccessConditions = []string{}
	}
}
func (o Objective) Validate() error {
	if o.Protocol != protocol.Name || o.Version != protocol.Version {
		return fmt.Errorf("unsupported protocol version %q/%q", o.Protocol, o.Version)
	}
	if o.ObjectiveID == "" || o.EpisodeID == "" {
		return errors.New("objective_id and episode_id are required")
	}
	if strings.TrimSpace(o.Text) == "" {
		return errors.New("objective text is required")
	}
	for _, condition := range o.SuccessConditions {
		if strings.TrimSpace(condition) == "" {
			return errors.New("success conditions cannot be empty")
		}
	}
	if o.Constraints.MaxCost < 0 {
		return errors.New("max_cost cannot be negative")
	}
	return nil
}

type Store interface {
	CreateObjective(context.Context, Objective, protocol.Event) error
	GetObjective(context.Context, string) (Objective, error)
}

func ValidateCreate(o Objective, e protocol.Event) error {
	if err := o.Validate(); err != nil {
		return err
	}
	if err := e.Validate(); err != nil {
		return err
	}
	if e.Type != "episode.started" {
		return errors.New("objective creation event must be episode.started")
	}
	if e.EpisodeID != o.EpisodeID {
		return errors.New("event episode_id does not match objective")
	}
	if id, ok := e.Payload["objective_id"].(string); !ok || id != o.ObjectiveID {
		return errors.New("event must reference objective_id")
	}
	return nil
}
