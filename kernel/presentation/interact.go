package presentation

import (
	"encoding/json"
	"errors"
	"fmt"
)

// FindDeclaredAction resolves one component's declared action by component
// id (docs/presentation-engine.md §11.2): the interaction handler accepts
// only actions the validated IR actually declares — an undeclared or
// rewritten action can never execute. Form components carry their own
// submit action; children of containers are searched too.
func FindDeclaredAction(document json.RawMessage, componentID string) (Action, error) {
	if componentID == "" {
		return Action{}, errors.New("component_id is required")
	}
	doc, err := ParseDocument(document)
	if err != nil {
		return Action{}, fmt.Errorf("presentation is not readable: %w", err)
	}
	var found *Action
	var walk func(components []Component)
	walk = func(components []Component) {
		for i := range components {
			component := &components[i]
			if component.ID == componentID && found == nil {
				for j := range component.Actions {
					if component.Actions[j].Type == "submit" || component.Actions[j].Type == "select" {
						action := component.Actions[j]
						found = &action
						return
					}
				}
			}
			if found == nil {
				walk(component.Children)
			}
		}
	}
	walk(doc.Content)
	if found == nil {
		return Action{}, fmt.Errorf("component %q declares no executable action", componentID)
	}
	return *found, nil
}
