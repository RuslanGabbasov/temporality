package presentation

import (
	"fmt"
	"sync"
)

// Third-party renderer extension point (docs/presentation-engine.md §17 P3.11:
// «Предусмотреть подключение сторонних renderer'ов»). A custom component
// registers once with its full stack — registry spec, structural validator,
// text renderer and static block renderer — and every layer of the engine
// then treats it like a builtin: validation accepts it, RenderText carries
// it, and LayoutDocument/RenderSVG/RenderPNG draw it. The web renderer
// receives the IR untouched and falls back to a placeholder for types it
// does not know (its own extension point).
//
// Registration is process-global and boot-time, mirroring Registry.Register's
// panic-on-duplicate contract: collisions must surface in tests, never in
// production traffic.

// BlockRendererFunc draws one custom component into an isolated fragment.
// The fragment's coordinates are local; the engine translates them into the
// document flow. Return ok=false to fall through to the builtin machinery.
type BlockRendererFunc func(c Component, width float64, theme Theme) (frag Drawing, ok bool)

// TextRendererFunc renders one custom component into text fallback lines.
type TextRendererFunc func(c Component, indent string, depth int) []string

// ComponentValidatorFunc validates one custom component's payload.
type ComponentValidatorFunc func(v *validation, c *Component)

type customComponent struct {
	spec      Spec
	validator ComponentValidatorFunc
	text      TextRendererFunc
	block     BlockRendererFunc
}

var (
	customMu       sync.RWMutex
	customRegistry = map[string]customComponent{}
)

// RegisterComponent installs a third-party component. The spec must declare
// SVG+PNG availability (the same §7.1 completeness contract as builtin
// specs); validator is mandatory, text and block renderers are mandatory —
// a component missing any of the four formats is refused at registration,
// not at first render (§5).
func RegisterComponent(spec Spec, validator ComponentValidatorFunc, text TextRendererFunc, block BlockRendererFunc) {
	if spec.Type == "" {
		panic("presentation: custom component without type")
	}
	if validator == nil || text == nil || block == nil {
		panic(fmt.Sprintf("presentation: custom component %q must ship validator, text and block renderers (§7.1)", spec.Type))
	}
	if !spec.HasSVG || !spec.HasPNG {
		panic(fmt.Sprintf("presentation: custom component %q must implement SVG and PNG renderers (§7.1)", spec.Type))
	}
	customMu.Lock()
	defer customMu.Unlock()
	if _, exists := customRegistry[spec.Type]; exists {
		panic(fmt.Sprintf("presentation: custom component %q registered twice", spec.Type))
	}
	if Default().Lookup(spec.Type) != nil {
		panic(fmt.Sprintf("presentation: custom component %q collides with a builtin type", spec.Type))
	}
	Default().Register(spec)
	customRegistry[spec.Type] = customComponent{spec: spec, validator: validator, text: text, block: block}
}

// customLookup resolves a custom component registration; ok=false for
// unknown or builtin types.
func customLookup(componentType string) (customComponent, bool) {
	customMu.RLock()
	defer customMu.RUnlock()
	entry, ok := customRegistry[componentType]
	return entry, ok
}
