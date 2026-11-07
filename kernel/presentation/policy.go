package presentation

// Policy is the organization-level control over what a presentation may
// contain (docs/presentation-engine.md §14): individual component types can
// be disabled, external media controlled, and resource limits tightened.
// The zero value is the safe default (everything standard enabled, defaults
// from Limits()).
type Policy struct {
	// DisabledTypes refuses whole component kinds regardless of document
	// correctness — an organization may switch off interactive or media
	// components without a code change.
	DisabledTypes map[string]bool
	// AllowExternalImages permits http(s) image sources. When false, image
	// components may only reference in-app paths (artifact and workspace
	// URLs), so agent documents cannot make clients fetch third-party
	// resources (§14.8).
	AllowExternalImages bool
	// AllowedImageHosts restricts external images further when
	// AllowExternalImages is set; empty means every http(s) host is fine.
	AllowedImageHosts []string
	// Limits overrides the default resource ceilings; nil fields fall back
	// to the built-in defaults from Limits().
	Limits *Limits
}

// Disabled reports whether the component type is switched off by policy.
func (p Policy) Disabled(componentType string) bool {
	return p.DisabledTypes != nil && p.DisabledTypes[componentType]
}

// Limits bounds one document's resource footprint (docs/presentation-engine.md
// §14.4–§14.6, §4.4): depth, component count, payload sizes and per-collection
// caps. They exist so one agent document can never exhaust renderer memory or
// flood a channel.
type Limits struct {
	// MaxDocumentBytes caps the whole serialized IR.
	MaxDocumentBytes int
	// MaxContentBytes caps one component's text content (text, markdown,
	// code, quote...).
	MaxContentBytes int
	// MaxDepth caps child nesting (document -> component -> children...).
	MaxDepth int
	// MaxComponents caps the total number of components across nesting.
	MaxComponents int
	// MaxChildren caps the immediate children of one container component.
	MaxChildren int
	// MaxListItems caps list and key-value entry counts.
	MaxListItems int
	// MaxTableRows / MaxTableCols cap table shape.
	MaxTableRows int
	// MaxTableCols int
	MaxTableCols int
	// MaxChartPoints caps the total number of plotted values in one chart
	// (sum over series).
	MaxChartPoints int
	// MaxKPIItems caps kpi, progress and comparison entry counts.
	MaxKPIItems int
	// MaxGalleryItems caps image-gallery entry counts.
	MaxGalleryItems int
	// MaxLabelRunes caps one human-facing label/value string.
	MaxLabelRunes int
}

// Defaults returns the built-in limits. The numbers leave generous room for
// honest reports (a 500-row table, a 200-point chart) while keeping the
// serialized document far below the API and journal payload ceilings.
func Defaults() Limits {
	return Limits{
		MaxDocumentBytes: 256 << 10,
		MaxContentBytes:  64 << 10,
		MaxDepth:         8,
		MaxComponents:    200,
		MaxChildren:      40,
		MaxListItems:     100,
		MaxTableRows:     500,
		MaxTableCols:     20,
		MaxChartPoints:   200,
		MaxKPIItems:      12,
		MaxGalleryItems:  24,
		MaxLabelRunes:    300,
	}
}

// effective merges the policy override over the defaults.
func (p Policy) effective() Limits {
	limits := Defaults()
	if p.Limits == nil {
		return limits
	}
	if p.Limits.MaxDocumentBytes > 0 {
		limits.MaxDocumentBytes = p.Limits.MaxDocumentBytes
	}
	if p.Limits.MaxContentBytes > 0 {
		limits.MaxContentBytes = p.Limits.MaxContentBytes
	}
	if p.Limits.MaxDepth > 0 {
		limits.MaxDepth = p.Limits.MaxDepth
	}
	if p.Limits.MaxComponents > 0 {
		limits.MaxComponents = p.Limits.MaxComponents
	}
	if p.Limits.MaxChildren > 0 {
		limits.MaxChildren = p.Limits.MaxChildren
	}
	if p.Limits.MaxListItems > 0 {
		limits.MaxListItems = p.Limits.MaxListItems
	}
	if p.Limits.MaxTableRows > 0 {
		limits.MaxTableRows = p.Limits.MaxTableRows
	}
	if p.Limits.MaxTableCols > 0 {
		limits.MaxTableCols = p.Limits.MaxTableCols
	}
	if p.Limits.MaxChartPoints > 0 {
		limits.MaxChartPoints = p.Limits.MaxChartPoints
	}
	if p.Limits.MaxKPIItems > 0 {
		limits.MaxKPIItems = p.Limits.MaxKPIItems
	}
	if p.Limits.MaxGalleryItems > 0 {
		limits.MaxGalleryItems = p.Limits.MaxGalleryItems
	}
	if p.Limits.MaxLabelRunes > 0 {
		limits.MaxLabelRunes = p.Limits.MaxLabelRunes
	}
	return limits
}
