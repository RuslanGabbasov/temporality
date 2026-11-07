package presentation

import (
	"fmt"
	"sort"
	"sync"
)

// ComponentKind groups registered components by their role in a document
// (docs/presentation-engine.md §6): the group drives which renderers are
// mandatory for a spec to count as complete (§5, §7).
type ComponentKind string

const (
	// KindText components carry prose and decorations.
	KindText ComponentKind = "text"
	// KindStructured components present tabular/structured data.
	KindStructured ComponentKind = "structured"
	// KindVisual components are charts, gauges and plots.
	KindVisual ComponentKind = "visual"
	// KindDiagram components are node-edge style drawings.
	KindDiagram ComponentKind = "diagram"
	// KindPlanning components carry timelines, plans and schedules.
	KindPlanning ComponentKind = "planning"
	// KindMedia components reference binary artifacts.
	KindMedia ComponentKind = "media"
	// KindInteractive components declare user actions (P2).
	KindInteractive ComponentKind = "interactive"
)

// Spec is the registry entry of one component type (docs/presentation-engine.md
// §5): identity, version, human description, kind, renderer availability,
// declared actions and the fallback behavior. New components plug in without
// touching the runtime — registration is the only extension point.
type Spec struct {
	// Type is the unique IR component type slug ("kpi", "chart", ...).
	Type string
	// Version of this component's contract; starts at "1.0".
	Version string
	// Description explains the component's purpose for docs and tooling.
	Description string
	// Kind groups the component by role.
	Kind ComponentKind
	// Aliases are alternative IR type slugs accepted on input and normalized
	// to Type at validation ("pie-chart" -> "chart"). Aliases keep the
	// catalog of §6.3 addressable without duplicating renderers.
	Aliases []string
	// Containers lists the child component types this component accepts;
	// empty means the component accepts no children.
	Containers []string
	// Actions lists the action types the component may declare.
	Actions []string
	// HasSVG / HasPNG report static renderer availability. The registry
	// completeness check (Complete) requires both for every registered type:
	// text-ish components render as layout cards, so all four formats exist
	// by construction (docs/presentation-engine.md §7.1).
	HasSVG bool
	HasPNG bool
	// NeedsMedia marks components whose rendering references binary
	// artifacts (images, files). Static formats draw a labeled placeholder
	// when the artifact is not inline-renderable.
	NeedsMedia bool
}

// acceptsChild reports whether the spec allows childType as a direct child.
func (s Spec) acceptsChild(childType string) bool {
	for _, allowed := range s.Containers {
		if allowed == "*" || allowed == childType {
			return true
		}
	}
	return false
}

// DefaultVersion is the version stamped on specs that do not set one.
const DefaultVersion = "1.0"

// Registry is the centralized component registry (docs/presentation-engine.md
// §5). It is safe for concurrent use; Register is intended at boot, Lookup at
// validation/render time.
type Registry struct {
	mu        sync.RWMutex
	specs     map[string]*Spec
	canonical map[string]string // alias slug -> canonical spec.Type
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{specs: map[string]*Spec{}, canonical: map[string]string{}}
}

// Register adds one component spec. Aliases are registered as hidden entries
// so validation can normalize them, but they never appear in Types.
// Registering the same type twice is a programming error (panic like the
// standard registry patterns: duplicates must surface in tests, §5).
func (r *Registry) Register(spec Spec) {
	if spec.Type == "" {
		panic("presentation: component spec without type")
	}
	if spec.Version == "" {
		spec.Version = DefaultVersion
	}
	if !spec.HasSVG || !spec.HasPNG {
		panic(fmt.Sprintf("presentation: component %q must implement SVG and PNG renderers (docs/presentation-engine.md §7.1)", spec.Type))
	}
	normalized := spec
	normalized.Aliases = nil
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.specs[spec.Type]; exists {
		panic(fmt.Sprintf("presentation: component %q registered twice", spec.Type))
	}
	r.specs[spec.Type] = &normalized
	for _, alias := range spec.Aliases {
		if alias == spec.Type {
			continue
		}
		aliasSpec := normalized
		aliasSpec.Type = alias
		r.specs[alias] = &aliasSpec
		r.canonical[alias] = spec.Type
	}
}

// Lookup resolves a component type (or alias); nil when unregistered.
func (r *Registry) Lookup(componentType string) *Spec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.specs[componentType]
}

// Types lists the canonical (non-alias) registered types in stable order.
func (r *Registry) Types() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	types := make([]string, 0, len(r.specs))
	for slug, spec := range r.specs {
		if _, isAlias := r.canonical[slug]; isAlias {
			continue
		}
		types = append(types, spec.Type)
	}
	sort.Strings(types)
	return types
}

// Complete verifies the registry invariant of docs/presentation-engine.md §5:
// every registered component must be renderable in all four formats. The
// layout engine gives every component a visual representation, so completeness
// reduces to "the spec exists and declares its kind"; the check keeps a
// registry drift from slipping in silently (e.g. a spec registered with an
// unknown kind).
func (r *Registry) Complete() error {
	for _, componentType := range r.Types() {
		spec := r.Lookup(componentType)
		if spec.Kind == "" {
			return fmt.Errorf("component %s has no kind", componentType)
		}
		if !spec.HasSVG || !spec.HasPNG {
			return fmt.Errorf("component %s lacks a static renderer", componentType)
		}
	}
	return nil
}

// builtinSpecs is the P0/P1 catalog (docs/presentation-engine.md §6): rich
// text, structured data, the core charts, flow/sequence diagrams, timeline
// and gantt, media and the declarative interactive seam. Every entry ships
// all four renderers: the layout engine gives each type a visual block, so
// the multi-format contract of §7.1 holds by construction.
func builtinSpecs() []Spec {
	all := func(spec Spec) Spec {
		spec.HasSVG = true
		spec.HasPNG = true
		return spec
	}
	return []Spec{
		all(Spec{Type: "text", Kind: KindText, Description: "Plain text line"}),
		all(Spec{Type: "markdown", Kind: KindText, Description: "Markdown block, the primary textual channel"}),
		all(Spec{Type: "heading", Kind: KindText, Description: "Section heading (props.level 1..6)"}),
		all(Spec{Type: "paragraph", Kind: KindText, Description: "Paragraph of prose"}),
		all(Spec{Type: "list", Kind: KindText, Description: "Ordered or bulleted list (items)"}),
		all(Spec{Type: "quote", Kind: KindText, Description: "Quotation with optional attribution"}),
		all(Spec{Type: "code", Kind: KindText, Description: "Code block (props.language)"}),
		all(Spec{Type: "link", Kind: KindText, Description: "Hyperlink (props.href)"}),
		all(Spec{Type: "badge", Kind: KindText, Description: "Status label (props.status)"}),
		all(Spec{Type: "divider", Kind: KindText, Description: "Horizontal separator"}),
		// Structured data (§6.2).
		all(Spec{Type: "table", Kind: KindStructured, Description: "Typed table (data.columns, data.rows)"}),
		all(Spec{Type: "card", Kind: KindStructured, Description: "Card container (props.title, children)", Containers: []string{"*"}}),
		all(Spec{Type: "card-grid", Kind: KindStructured, Description: "Responsive grid of cards (children)", Containers: []string{"card"}}),
		all(Spec{Type: "comparison", Kind: KindStructured, Description: "Side-by-side option comparison (items)"}),
		all(Spec{Type: "key-value", Kind: KindStructured, Description: "Key-value pairs (items)"}),
		all(Spec{Type: "kpi", Kind: KindStructured, Description: "Key metric tiles (items)"}),
		all(Spec{Type: "progress", Kind: KindStructured, Description: "Progress indicators (items)"}),
		all(Spec{Type: "status", Kind: KindStructured, Description: "Single status line (props.status)"}),
		all(Spec{Type: "alert", Kind: KindStructured, Description: "Callout with severity (props.severity)"}),
		all(Spec{Type: "accordion", Kind: KindStructured, Description: "Collapsible sections (items)"}),
		// Data visualization (§6.3). The explicit chart slugs are aliases of
		// the generic chart component with an implied chartType.
		all(Spec{Type: "chart", Kind: KindVisual, Description: "Chart (props.chartType: line|bar|pie|area|donut)",
			Aliases: []string{"line-chart", "bar-chart", "pie-chart", "area-chart", "donut-chart"}}),
		all(Spec{Type: "gauge", Kind: KindVisual, Description: "Single-value gauge (data.value)"}),
		// Diagrams (§6.4).
		all(Spec{Type: "flowchart", Kind: KindDiagram, Description: "Block diagram (data.nodes, data.edges)"}),
		all(Spec{Type: "sequence-diagram", Kind: KindDiagram, Description: "Sequence of messages (data.participants, data.messages)"}),
		all(Spec{Type: "mindmap", Kind: KindDiagram, Description: "Mind map tree (data.root)"}),
		all(Spec{Type: "dependency-graph", Kind: KindDiagram, Description: "Dependency graph (data.nodes, data.edges)"}),
		all(Spec{Type: "state-diagram", Kind: KindDiagram, Description: "State machine (data.states, data.transitions)"}),
		all(Spec{Type: "er-diagram", Kind: KindDiagram, Description: "Entity relationships (data.entities, data.relations)"}),
		all(Spec{Type: "class-diagram", Kind: KindDiagram, Description: "Classes and relations (data.classes, data.relations)"}),
		// Planning & timeline (§6.5). Roadmap is the gantt with a month
		// (quarter-like) scale implied.
		all(Spec{Type: "timeline", Kind: KindPlanning, Description: "Chronology of events (items)"}),
		all(Spec{Type: "gantt", Kind: KindPlanning, Description: "Schedule bars (data.tasks)", Aliases: []string{"roadmap"}}),
		// Media (§6.6).
		all(Spec{Type: "image", Kind: KindMedia, Description: "Image (props.src)", NeedsMedia: true}),
		all(Spec{Type: "image-gallery", Kind: KindMedia, Description: "Image grid (items)", NeedsMedia: true}),
		all(Spec{Type: "file", Kind: KindMedia, Description: "File attachment (props.name, props.href)", NeedsMedia: true}),
		// Interactive (§6.7) — declarative seam for P2; actions are data. The
		// form is the single interactive component: input/select/checkbox/
		// radio/date-picker/slider are field types of its embedded JSON
		// Schema (the same subset the HITL forms validate).
		all(Spec{Type: "button", Kind: KindInteractive, Description: "Action button (action)", Actions: []string{"submit", "select", "open"}}),
		all(Spec{Type: "form", Kind: KindInteractive, Description: "Declarative form (props.schema, submit action)", Actions: []string{"submit"}}),
	}
}

var (
	defaultRegistryOnce sync.Once
	defaultRegistry     *Registry
)

// Default returns the registry preloaded with the built-in catalog. Custom
// installations may extend a fresh NewRegistry()-built registry with their
// own specs — the runtime only ever talks to the Registry interface surface.
func Default() *Registry {
	defaultRegistryOnce.Do(func() {
		defaultRegistry = NewRegistry()
		for _, spec := range builtinSpecs() {
			defaultRegistry.Register(spec)
		}
	})
	return defaultRegistry
}

// NormalizeType resolves an alias to its canonical component type; unknown
// types are returned unchanged.
func (r *Registry) NormalizeType(componentType string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if canonical, ok := r.canonical[componentType]; ok {
		return canonical
	}
	return componentType
}

// NormalizeType resolves an alias against the default registry.
func NormalizeType(componentType string) string {
	return Default().NormalizeType(componentType)
}
