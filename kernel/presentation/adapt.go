package presentation

import (
	"fmt"
	"strings"
)

// Channel adaptation (docs/presentation-engine.md §9): every channel declares
// its capabilities, and Adapt picks the richest representation the channel
// can carry — native is the web renderer's business (it always receives the
// IR), the adapters choose between markdown, static images and text.

// Capabilities is the feature matrix of one delivery channel (§9.2). The
// shape matches the spec's negotiation example field-for-field.
type Capabilities struct {
	Markdown         bool `json:"markdown"`
	Tables           bool `json:"tables"`
	Images           bool `json:"images"`
	Charts           bool `json:"charts"`
	Diagrams         bool `json:"diagrams"`
	InteractiveForms bool `json:"interactive_forms"`
	Buttons          bool `json:"buttons"`
	Files            bool `json:"files"`
}

// Delivery is one adapted representation of a document for a channel.
type Delivery struct {
	// Kind names the chosen representation: "markdown", "text" (attachments
	// may accompany both).
	Kind string
	// Body is the channel-facing text: markdown when the channel takes
	// markdown, plain text otherwise.
	Body string
	// Attachments carries the static images the channel should deliver as
	// files/photos (SVG for file-capable clients, PNG as the raster form).
	Attachments []Attachment
	// Notes explains what fell back and why — surfaced in
	// presentation.fallback events so operators can see adaptation quality
	// (§15).
	Notes []string
}

// Attachment is one static render of a visual component (§9.4 fallback
// order: native -> SVG -> PNG -> text).
type Attachment struct {
	// ComponentType is the IR type the image renders ("chart", "table", ...).
	ComponentType string
	// ComponentID is the component's declared id, "" when unnamed.
	ComponentID string
	// Format is "svg" or "png".
	Format string
	// Name is a delivery-facing file name.
	Name string
	// Data is the encoded image bytes.
	Data []byte
}

// Adapt renders the document for one channel's capabilities (§9.3 strategy
// table, §9.4 fallback order). Rendering failures degrade to text and are
// reported through Notes, never dropped silently (§9.4: fallback must not
// lose critical information).
func Adapt(doc Document, caps Capabilities) Delivery {
	markdown := RenderMarkdown(doc)
	text := RenderText(doc)
	delivery := Delivery{}
	visual := visualComponents(doc)

	switch {
	case caps.Markdown && caps.Tables && hasStructured(doc):
		delivery.Kind = "markdown"
		delivery.Body = markdown
	case caps.Markdown:
		delivery.Kind = "markdown"
		delivery.Body = markdown
		delivery.Notes = append(delivery.Notes, "channel lacks native tables; data arrives as markdown text")
	default:
		delivery.Kind = "text"
		delivery.Body = text
	}

	// Visual components need images when the channel cannot draw them
	// natively (charts/diagrams off) — SVG when the channel takes files,
	// otherwise PNG (Telegram-style image channels), and nothing at all when
	// neither: the text body already carries the data (§7.6).
	needsImages := len(visual) > 0 && (!caps.Charts || !caps.Diagrams)
	if !needsImages {
		return delivery
	}
	for _, component := range visual {
		frag := Document{Version: IRVersion, Type: DocumentType, ID: doc.ID, Title: componentTitle(component), Content: []Component{component}}
		drawing := LayoutDocument(frag, LayoutOptions{IncludeTitle: true, Width: 640})
		if caps.Files {
			if svg := RenderSVG(drawing); len(svg) > 0 {
				delivery.Attachments = append(delivery.Attachments, Attachment{
					ComponentType: component.Type, ComponentID: component.ID,
					Format: "svg", Name: attachmentName(doc, component, "svg"), Data: svg,
				})
				continue
			}
		}
		if caps.Images {
			png, err := RenderPNG(drawing)
			if err == nil && len(png) > 0 {
				delivery.Attachments = append(delivery.Attachments, Attachment{
					ComponentType: component.Type, ComponentID: component.ID,
					Format: "png", Name: attachmentName(doc, component, "png"), Data: png,
				})
				delivery.Notes = append(delivery.Notes, fmt.Sprintf("%s rendered as PNG (SVG unsupported)", component.Type))
				continue
			}
			if err != nil {
				delivery.Notes = append(delivery.Notes, fmt.Sprintf("%s PNG render failed: %v", component.Type, err))
			}
		}
		delivery.Notes = append(delivery.Notes, fmt.Sprintf("%s delivered as text: the channel cannot carry images", component.Type))
	}
	return delivery
}

// componentTitle derives the human-facing image title of one visual block.
func componentTitle(c Component) string {
	var props ChartProps
	if decodeStrict(c.Props, &props) == nil && props.Title != "" {
		return props.Title
	}
	return strings.ToUpper(c.Type[:1]) + c.Type[1:]
}

// attachmentName builds a stable, filesystem-friendly image name.
func attachmentName(doc Document, c Component, format string) string {
	base := doc.ID
	if base == "" {
		base = "presentation"
	}
	suffix := c.ID
	if suffix == "" {
		suffix = fmt.Sprintf("%d", indexOfType(doc, c))
	}
	return fmt.Sprintf("%s-%s-%s.%s", sanitizeFileBase(base), sanitizeFileBase(c.Type), sanitizeFileBase(suffix), format)
}

func sanitizeFileBase(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune('-')
		}
	}
	out := b.String()
	if out == "" {
		out = "part"
	}
	if len(out) > 60 {
		out = out[:60]
	}
	return out
}

func indexOfType(doc Document, target Component) int {
	for i, component := range doc.Content {
		if component.Type == target.Type && component.ID == target.ID {
			return i
		}
	}
	return 0
}

// hasStructured reports whether the document holds tabular data that only
// makes sense with table-capable rendering (drives the markdown/table note).
func hasStructured(doc Document) bool {
	for _, component := range doc.Content {
		if component.Type == "table" || component.Type == "comparison" {
			return true
		}
	}
	return false
}

// visualComponents lists the components that need a static image on channels
// without native chart/diagram rendering, in document order.
func visualComponents(doc Document) []Component {
	out := make([]Component, 0, 4)
	var walk func(components []Component)
	walk = func(components []Component) {
		for _, component := range components {
			switch component.Type {
			case "chart", "gauge", "flowchart", "sequence-diagram", "gantt":
				out = append(out, component)
			}
			walk(component.Children)
		}
	}
	walk(doc.Content)
	return out
}

// ChannelPresets are the capability matrices of the built-in channels
// (docs/presentation-engine.md §9.3). They are conservative baselines: actual
// client support varies (§9.2), and adapters may tighten them.
func ChannelPresets() map[string]Capabilities {
	return map[string]Capabilities{
		// Web always receives the full IR (native rendering); the preset is
		// what the kernel reports to get_capabilities for completeness.
		"web": {Markdown: true, Tables: true, Images: true, Charts: true, Diagrams: true, InteractiveForms: true, Buttons: true, Files: true},
		// Matrix: markdown bodies, file uploads (SVG rides as a file), no
		// native charts; buttons exist in some clients only.
		"matrix": {Markdown: true, Tables: false, Images: true, Charts: false, Diagrams: false, InteractiveForms: false, Buttons: true, Files: true},
		// Telegram: photo uploads only (PNG, §9.3) — file uploads are not the
		// delivery shape for visuals there, so the adapter skips SVG and the
		// fallback order lands on PNG.
		"telegram":   {Markdown: true, Tables: false, Images: true, Charts: false, Diagrams: false, InteractiveForms: false, Buttons: false, Files: false},
		"slack":      {Markdown: true, Tables: false, Images: true, Charts: false, Diagrams: false, InteractiveForms: false, Buttons: true, Files: true},
		"mattermost": {Markdown: true, Tables: false, Images: true, Charts: false, Diagrams: false, InteractiveForms: false, Buttons: true, Files: true},
		"webhook":    {Markdown: true, Tables: true, Images: true, Charts: true, Diagrams: true, InteractiveForms: false, Buttons: false, Files: true},
	}
}
