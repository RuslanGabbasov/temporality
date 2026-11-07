// Package presentation implements the Presentation Engine (docs/presentation-engine.md):
// a declarative intermediate representation (Presentation IR) of structured agent
// messages, its validation, the component registry, the lifecycle runtime and the
// multi-format renderers (Text, SVG, PNG) required from every visualizer.
//
// The IR is renderer-agnostic plain JSON: no React, no Mermaid, no channel and no
// LLM assumptions leak into this package (docs/presentation-engine.md §2.2). All
// renderers derive from the same component data — the layout layer builds one draw
// model that both the SVG emitter and the PNG rasterizer consume, so values cannot
// diverge between formats (§7.3).
package presentation

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// IR semantic version of the document format. A validator refuses documents
// whose version it does not know, so a future incompatible change bumps this
// constant instead of silently misreading old payloads.
const IRVersion = "1.0"

// DocumentType is the fixed document-level type of a Presentation IR payload.
const DocumentType = "presentation"

// Document is the root of one presentation: a title plus an ordered list of
// components (docs/presentation-engine.md §4.2).
type Document struct {
	Version string      `json:"version"`
	Type    string      `json:"type"`
	ID      string      `json:"id,omitempty"`
	Title   string      `json:"title,omitempty"`
	Content []Component `json:"content"`
}

// Component is one declarative building block. The generic property set is
// fixed by §4.3 (type, id, content, data, props, actions, items); per-type
// validators define which of them are required and which shapes the raw
// data/props/items payloads may take. Children carries nested composition
// (§4.4) — only container types (card, card-grid) accept it.
// ChartType is the §4.2 example's component-level chart kind; validation
// folds it into props, so renderers only ever read props.chartType.
type Component struct {
	Type      string          `json:"type"`
	ID        string          `json:"id,omitempty"`
	Content   string          `json:"content,omitempty"`
	ChartType string          `json:"chartType,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
	Props     json.RawMessage `json:"props,omitempty"`
	Actions   []Action        `json:"actions,omitempty"`
	Items     json.RawMessage `json:"items,omitempty"`
	Children  []Component     `json:"children,omitempty"`
}

// Action is one declared interactive action (docs/presentation-engine.md §11.1).
// Actions are data, never code: the runtime resolves target through the
// interaction handler, and no executable instruction may ride in Payload.
type Action struct {
	Type    string          `json:"type"`
	Label   string          `json:"label,omitempty"`
	Target  string          `json:"target,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// ParseDocument decodes and shallowly checks one IR payload: envelope fields
// (version/type) must match the supported IR, and the JSON must be an object.
// Full structural validation happens in Validate.
func ParseDocument(raw []byte) (Document, error) {
	var doc Document
	if err := json.Unmarshal(raw, &doc); err != nil {
		return Document{}, fmt.Errorf("presentation IR is not valid JSON: %w", err)
	}
	if doc.Version != IRVersion {
		return Document{}, fmt.Errorf("unsupported IR version %q (want %q)", doc.Version, IRVersion)
	}
	if doc.Type != DocumentType {
		return Document{}, fmt.Errorf("unsupported document type %q (want %q)", doc.Type, DocumentType)
	}
	return doc, nil
}

// decodeStrict decodes raw into out refusing unknown fields: IR authors cannot
// smuggle unvalidated payload past the typed shapes (docs/presentation-engine.md
// §14.3, strict JSON Schema validation).
func decodeStrict(raw json.RawMessage, out any) error {
	if len(raw) == 0 {
		return nil
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return err
	}
	// Trailing garbage (a second JSON value) is refused the same way.
	if dec.More() {
		return errors.New("unexpected trailing JSON content")
	}
	return nil
}

// rawObject reports whether raw is a JSON object (the expected shape of every
// data/props/items payload).
func rawObject(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	trimmed := strings.TrimLeft(string(raw), " \t\r\n")
	return strings.HasPrefix(trimmed, "{")
}

// rawArray reports whether raw is a JSON array.
func rawArray(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	trimmed := strings.TrimLeft(string(raw), " \t\r\n")
	return strings.HasPrefix(trimmed, "[")
}
