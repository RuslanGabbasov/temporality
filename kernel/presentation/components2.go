package presentation

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Extended diagram catalog and interactive forms (docs/presentation-engine.md
// §6.4–§6.7, P3 slice). Every type here reuses the existing machinery:
// diagrams convert to the flow layout for static formats and to Mermaid on
// the web (the IR stays engine-neutral); the form is the single declarative
// interactive component — input/select/checkbox/radio/date-picker/slider are
// field types of its JSON Schema (the same subset the HITL forms validate),
// not separate components.

// MindNode is one mindmap node: a label plus optional children.
type MindNode struct {
	Label    string     `json:"label"`
	Note     string     `json:"note,omitempty"`
	Children []MindNode `json:"children,omitempty"`
}

// MindmapData is the mindmap component's data payload (§6.4).
type MindmapData struct {
	Root MindNode `json:"root"`
}

// StateTransition is one state machine transition.
type StateTransition struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Label  string `json:"label,omitempty"`
	Dashed bool   `json:"dashed,omitempty"`
}

// StateData is the state-diagram component's data payload (§6.4).
type StateData struct {
	Initial     string            `json:"initial,omitempty"`
	States      []FlowNode        `json:"states"`
	Transitions []StateTransition `json:"transitions"`
}

// Entity is one ER entity: a name with optional attributes.
type Entity struct {
	Name       string   `json:"name"`
	Attributes []string `json:"attributes,omitempty"`
}

// Relation is one relationship between two entities/classes/nodes. The
// cardinality carries the plain "1:1"/"1:N"/"N:M"/"0:1" form; kind carries
// class-relation kinds (inheritance|composition|association).
type Relation struct {
	From        string `json:"from"`
	To          string `json:"to"`
	Label       string `json:"label,omitempty"`
	Cardinality string `json:"cardinality,omitempty"`
	Kind        string `json:"kind,omitempty"`
}

// ERData is the er-diagram component's data payload (§6.4).
type ERData struct {
	Entities  []Entity   `json:"entities"`
	Relations []Relation `json:"relations,omitempty"`
}

// Class is one class-diagram class.
type Class struct {
	Name       string   `json:"name"`
	Attributes []string `json:"attributes,omitempty"`
	Methods    []string `json:"methods,omitempty"`
}

// ClassData is the class-diagram component's data payload (§6.4).
type ClassData struct {
	Classes   []Class    `json:"classes"`
	Relations []Relation `json:"relations,omitempty"`
}

// checkMultilineList validates a bounded list of short one-line strings
// (attributes, methods, notes).
func checkMultilineList(v *validation, field string, values []string, max int, maxRunes int) {
	if len(values) > max {
		v.failf("%s holds %d entries, limit is %d", field, len(values), max)
		return
	}
	for i, value := range values {
		checkString(v, fmt.Sprintf("%s[%d]", field, i), value, maxRunes, false)
	}
}

func mindmapValidator(v *validation, c *Component) {
	decodeEmptyProps(v, c)
	var data MindmapData
	if !decodeData(v, c, &data) {
		v.failf("data with a root node is required")
		return
	}
	const (
		maxDepth = 6
		maxNodes = 100
	)
	count := 0
	var walk func(node MindNode, depth int, path string)
	walk = func(node MindNode, depth int, path string) {
		count++
		if count > maxNodes {
			v.failf("mindmap holds more than %d nodes", maxNodes)
			count = -1 // stop walking, one refusal is enough
			return
		}
		if depth > maxDepth {
			v.failf("%s: nesting deeper than %d levels", path, maxDepth)
			return
		}
		checkLabel(v, path+".label", node.Label)
		if node.Label == "" {
			v.failf("%s.label is required", path)
		}
		checkString(v, path+".note", node.Note, 200, false)
		for i, child := range node.Children {
			walk(child, depth+1, fmt.Sprintf("%s.children[%d]", path, i))
		}
	}
	walk(data.Root, 1, "data.root")
	if count < 0 {
		return
	}
}

func dependencyGraphValidator(v *validation, c *Component) {
	// The dependency graph shares the flowchart data shape (nodes + directed
	// edges); a dedicated type keeps the catalog semantics of §6.4 explicit.
	flowValidator(v, c)
}

func stateValidator(v *validation, c *Component) {
	var props struct {
		Title     string `json:"title,omitempty"`
		Direction string `json:"direction,omitempty"`
	}
	decodeProps(v, c, &props)
	checkLabel(v, "props.title", props.Title)
	checkEnum(v, "props.direction", props.Direction, map[string]bool{"TB": true, "LR": true})
	var data StateData
	if !decodeData(v, c, &data) {
		v.failf("data with states is required")
		return
	}
	const maxStates = 50
	if len(data.States) == 0 {
		v.failf("data.states must hold at least one state")
		return
	}
	if len(data.States) > maxStates {
		v.failf("data.states holds %d states, limit is %d", len(data.States), maxStates)
		return
	}
	if len(data.Transitions) > 4*maxStates {
		v.failf("data.transitions holds %d transitions, limit is %d", len(data.Transitions), 4*maxStates)
		return
	}
	ids := map[string]bool{}
	for i, state := range data.States {
		if state.ID == "" {
			v.failf("data.states[%d].id is required", i)
			continue
		}
		if ids[state.ID] {
			v.failf("data.states[%d]: duplicate state id %q", i, state.ID)
		}
		ids[state.ID] = true
		checkLabel(v, fmt.Sprintf("data.states[%d].label", i), state.Label)
		checkStatusField(v, fmt.Sprintf("data.states[%d].status", i), state.Status)
	}
	for i, transition := range data.Transitions {
		if !ids[transition.From] {
			v.failf("data.transitions[%d].from: unknown state %q", i, transition.From)
		}
		if !ids[transition.To] {
			v.failf("data.transitions[%d].to: unknown state %q", i, transition.To)
		}
		checkLabel(v, fmt.Sprintf("data.transitions[%d].label", i), transition.Label)
	}
	if data.Initial != "" && !ids[data.Initial] {
		v.failf("data.initial: unknown state %q", data.Initial)
	}
	if len(data.Transitions) == 0 {
		v.failf("data.transitions must hold at least one transition")
	}
}

var cardinalities = map[string]bool{"1:1": true, "1:N": true, "N:1": true, "N:M": true, "0:1": true, "0:N": true}

var classRelationKinds = map[string]bool{"inheritance": true, "composition": true, "association": true}

func erValidator(v *validation, c *Component) {
	var props struct {
		Title string `json:"title,omitempty"`
	}
	decodeProps(v, c, &props)
	checkLabel(v, "props.title", props.Title)
	var data ERData
	if !decodeData(v, c, &data) {
		v.failf("data with entities is required")
		return
	}
	const maxEntities = 40
	if len(data.Entities) == 0 {
		v.failf("data.entities must hold at least one entity")
		return
	}
	if len(data.Entities) > maxEntities {
		v.failf("data.entities holds %d entities, limit is %d", len(data.Entities), maxEntities)
		return
	}
	names := map[string]bool{}
	for i, entity := range data.Entities {
		if entity.Name == "" {
			v.failf("data.entities[%d].name is required", i)
			continue
		}
		if names[entity.Name] {
			v.failf("data.entities[%d]: duplicate entity %q", i, entity.Name)
		}
		names[entity.Name] = true
		checkMultilineList(v, fmt.Sprintf("data.entities[%d].attributes", i), entity.Attributes, 20, 80)
	}
	if len(data.Relations) > 3*maxEntities {
		v.failf("data.relations holds %d relations, limit is %d", len(data.Relations), 3*maxEntities)
		return
	}
	for i, relation := range data.Relations {
		if !names[relation.From] {
			v.failf("data.relations[%d].from: unknown entity %q", i, relation.From)
		}
		if !names[relation.To] {
			v.failf("data.relations[%d].to: unknown entity %q", i, relation.To)
		}
		checkLabel(v, fmt.Sprintf("data.relations[%d].label", i), relation.Label)
		checkEnum(v, fmt.Sprintf("data.relations[%d].cardinality", i), relation.Cardinality, cardinalities)
	}
}

func classValidator(v *validation, c *Component) {
	var props struct {
		Title string `json:"title,omitempty"`
	}
	decodeProps(v, c, &props)
	checkLabel(v, "props.title", props.Title)
	var data ClassData
	if !decodeData(v, c, &data) {
		v.failf("data with classes is required")
		return
	}
	const maxClasses = 40
	if len(data.Classes) == 0 {
		v.failf("data.classes must hold at least one class")
		return
	}
	if len(data.Classes) > maxClasses {
		v.failf("data.classes holds %d classes, limit is %d", len(data.Classes), maxClasses)
		return
	}
	names := map[string]bool{}
	for i, class := range data.Classes {
		if class.Name == "" {
			v.failf("data.classes[%d].name is required", i)
			continue
		}
		if names[class.Name] {
			v.failf("data.classes[%d]: duplicate class %q", i, class.Name)
		}
		names[class.Name] = true
		checkLabel(v, fmt.Sprintf("data.classes[%d].name", i), class.Name)
		checkMultilineList(v, fmt.Sprintf("data.classes[%d].attributes", i), class.Attributes, 15, 80)
		checkMultilineList(v, fmt.Sprintf("data.classes[%d].methods", i), class.Methods, 15, 80)
	}
	for i, relation := range data.Relations {
		if !names[relation.From] {
			v.failf("data.relations[%d].from: unknown class %q", i, relation.From)
		}
		if !names[relation.To] {
			v.failf("data.relations[%d].to: unknown class %q", i, relation.To)
		}
		checkLabel(v, fmt.Sprintf("data.relations[%d].label", i), relation.Label)
		checkEnum(v, fmt.Sprintf("data.relations[%d].kind", i), relation.Kind, classRelationKinds)
	}
}

// formSchemaMaxBytes bounds one form's embedded JSON Schema — the same
// ceiling the HITL ask path applies (approval_preview.go).
const formSchemaMaxBytes = 8 << 10

func formValidator(v *validation, c *Component) {
	checkLabel(v, "content", c.Content)
	var props struct {
		Schema json.RawMessage `json:"schema,omitempty"`
		Submit string          `json:"submit,omitempty"`
	}
	decodeProps(v, c, &props)
	if len(props.Schema) == 0 {
		v.failf("props.schema (JSON Schema subset) is required")
	} else {
		if len(props.Schema) > formSchemaMaxBytes {
			v.failf("props.schema exceeds %d bytes", formSchemaMaxBytes)
		} else {
			var schema struct {
				Type       string          `json:"type"`
				Properties json.RawMessage `json:"properties"`
				Required   []string        `json:"required"`
			}
			dec := json.NewDecoder(strings.NewReader(string(props.Schema)))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&schema); err != nil {
				v.failf("props.schema: %v", err)
			} else {
				if schema.Type != "" && schema.Type != "object" {
					v.failf("props.schema.type must be object")
				}
				if !rawObject(schema.Properties) {
					v.failf("props.schema.properties is required (an object of field schemas)")
				} else {
					var fields map[string]json.RawMessage
					_ = json.Unmarshal(schema.Properties, &fields)
					if len(fields) == 0 {
						v.failf("props.schema.properties must hold at least one field")
					}
					if len(fields) > 20 {
						v.failf("props.schema.properties holds %d fields, limit is 20", len(fields))
					}
					for name := range fields {
						checkShortString(v, "props.schema.properties."+name, name, 64)
					}
					for i, required := range schema.Required {
						checkShortString(v, fmt.Sprintf("props.schema.required[%d]", i), required, 64)
					}
				}
			}
		}
	}
	checkLabel(v, "props.submit", props.Submit)
	// The form must declare where its submission goes (§11.2: unambiguous
	// target).
	hasSubmit := false
	for _, action := range c.Actions {
		if action.Type == "submit" {
			hasSubmit = true
			if action.Target == "" {
				v.failf("actions: submit target is required")
			}
		}
	}
	if !hasSubmit {
		v.failf("actions must declare one submit action (target = operation id)")
	}
}
