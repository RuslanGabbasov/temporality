// Package observation defines the harness-independent event envelope accepted
// by Temporality. It deliberately does not depend on FRP Frames or UUID IDs.
package observation

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

const Schema = "temporality.event/1"

type Source struct {
	ID          string `json:"id"`
	Integration string `json:"integration"`
	Version     string `json:"version,omitempty"`
}

type Actor struct {
	ID   string `json:"id"`
	Type string `json:"type,omitempty"`
}

// Context contains opaque identifiers from the producing harness. Values are
// strings by design: producers are not required to use Temporality UUIDs.
type Context struct {
	Project       string `json:"project,omitempty"`
	Run           string `json:"run,omitempty"`
	Task          string `json:"task,omitempty"`
	Actor         Actor  `json:"actor,omitempty"`
	ParentEventID string `json:"parent_event_id,omitempty"`
}

type Evidence struct {
	Ref  string `json:"ref"`
	Type string `json:"type,omitempty"`
}

type Event struct {
	Schema     string         `json:"schema"`
	EventID    string         `json:"event_id"`
	OccurredAt time.Time      `json:"occurred_at"`
	ReceivedAt time.Time      `json:"received_at,omitempty"`
	Source     Source         `json:"source"`
	Context    Context        `json:"context,omitempty"`
	Type       string         `json:"type"`
	Data       map[string]any `json:"data,omitempty"`
	Evidence   []Evidence     `json:"evidence,omitempty"`
}

func (e Event) Validate() error {
	if e.Schema != Schema {
		return fmt.Errorf("schema must be %q", Schema)
	}
	if strings.TrimSpace(e.EventID) == "" {
		return errors.New("event_id is required")
	}
	if e.OccurredAt.IsZero() {
		return errors.New("occurred_at is required")
	}
	if strings.TrimSpace(e.Source.ID) == "" || strings.TrimSpace(e.Source.Integration) == "" {
		return errors.New("source.id and source.integration are required")
	}
	if strings.TrimSpace(e.Type) == "" {
		return errors.New("type is required")
	}
	for i, evidence := range e.Evidence {
		if strings.TrimSpace(evidence.Ref) == "" {
			return fmt.Errorf("evidence[%d].ref is required", i)
		}
	}
	switch e.Type {
	case "knowledge.proposed":
		if strings.TrimSpace(e.Context.Project) == "" {
			return errors.New("knowledge events require context.project")
		}
		if stringValue(e.Data, "knowledge_id") == "" || stringValue(e.Data, "proposition") == "" {
			return errors.New("knowledge.proposed requires data.knowledge_id and data.proposition")
		}
	case "knowledge.used", "knowledge.confirmed", "knowledge.challenged", "knowledge.corrected", "knowledge.invalidated", "knowledge.superseded", "knowledge.disproved":
		if strings.TrimSpace(e.Context.Project) == "" {
			return errors.New("knowledge events require context.project")
		}
		if stringValue(e.Data, "knowledge_id") == "" {
			return fmt.Errorf("%s requires data.knowledge_id", e.Type)
		}
		if (e.Type == "knowledge.invalidated" || e.Type == "knowledge.disproved") && stringValue(e.Data, "reason") == "" {
			return fmt.Errorf("%s requires data.reason", e.Type)
		}
		if e.Type == "knowledge.disproved" && len(e.Evidence) == 0 {
			return errors.New("knowledge.disproved requires evidence")
		}
	case "knowledge.linked":
		if strings.TrimSpace(e.Context.Project) == "" || stringValue(e.Data, "knowledge_id") == "" || stringValue(e.Data, "target_id") == "" {
			return errors.New("knowledge.linked requires context.project, data.knowledge_id, and data.target_id")
		}
		switch stringValue(e.Data, "relation") {
		case "supports", "contradicts", "derived_from", "depends_on", "supersedes", "related_to":
		default:
			return errors.New("knowledge.linked requires a supported data.relation")
		}
	case "hint.offered":
		if strings.TrimSpace(e.Context.Project) == "" {
			return errors.New("hint events require context.project")
		}
		if stringValue(e.Data, "hint_id") == "" || stringValue(e.Data, "knowledge_id") == "" {
			return errors.New("hint.offered requires data.hint_id and data.knowledge_id")
		}
	case "hint.used", "hint.ignored", "hint.outcome":
		if strings.TrimSpace(e.Context.Project) == "" {
			return errors.New("hint events require context.project")
		}
		if stringValue(e.Data, "hint_id") == "" {
			return fmt.Errorf("%s requires data.hint_id", e.Type)
		}
		if e.Type == "hint.outcome" && stringValue(e.Data, "outcome") == "" {
			return errors.New("hint.outcome requires data.outcome")
		}
	default:
		if strings.HasPrefix(e.Type, "knowledge.") {
			return fmt.Errorf("unsupported knowledge event type %q", e.Type)
		}
	}
	return nil
}

func stringValue(data map[string]any, key string) string {
	value, _ := data[key].(string)
	return strings.TrimSpace(value)
}
