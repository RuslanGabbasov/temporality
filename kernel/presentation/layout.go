package presentation

import (
	"encoding/json"
	"fmt"
	"strings"
)

// The layout engine: validated IR -> draw model. This file carries the
// document flow (padding, block stack) and the text-shaped components;
// layout_data.go the structured ones, layout_viz.go the charts and plans,
// layout_flow.go the diagrams.

// LayoutOptions tunes one static render.
type LayoutOptions struct {
	// Width is the frame width in points; 0 selects the default.
	Width float64
	// Theme selects the palette; zero value selects the light theme.
	Theme Theme
	// MaxHeight caps the frame height; taller documents are cut (the
	// validator's limits make this a pathological-input guard, not a normal
	// path).
	MaxHeight float64
	// IncludeTitle renders the document title as a heading block.
	IncludeTitle bool
	// Scale multiplies every coordinate at raster time (PNG only; SVG uses
	// 1:1 units).
	Scale float64
}

// Layout geometry constants (points).
const (
	defaultLayoutWidth = 760
	layoutOuterPad     = 28.0
	layoutBlockGap     = 16.0
	layoutMaxHeight    = 8000
	lineHeightFactor   = 1.45
)

// Defaults applies the built-in defaults to zero-valued options.
func (o LayoutOptions) withDefaults() LayoutOptions {
	if o.Width <= 0 {
		o.Width = defaultLayoutWidth
	}
	if o.MaxHeight <= 0 {
		o.MaxHeight = layoutMaxHeight
	}
	if o.Scale <= 0 {
		o.Scale = 1
	}
	if o.Theme.Background == "" {
		o.Theme = LightTheme()
	}
	return o
}

// LayoutDocument renders a validated document into the draw model consumed
// by the SVG and PNG emitters. Only validated documents may be laid out —
// the engine indexes typed payloads without re-checking them.
func LayoutDocument(doc Document, opts LayoutOptions) Drawing {
	opts = opts.withDefaults()
	l := &layout{
		opts:  opts,
		theme: opts.Theme,
		width: opts.Width,
		ops:   make([]DrawOp, 0, 256),
	}
	if opts.IncludeTitle && doc.Title != "" {
		l.pushHeading(doc.Title, 1)
	}
	yTop := l.cursorY() + layoutOuterPad
	total := l.renderComponents(doc.Content, layoutOuterPad, yTop, opts.Width-2*layoutOuterPad)
	l.y = yTop + total
	height := l.cursorY() + layoutOuterPad
	if height > opts.MaxHeight {
		height = opts.MaxHeight
	}
	// The opaque frame background is spliced in first: the real height is
	// only known after layout, and a MaxHeight-sized canvas would leak an
	// empty tail into every image.
	l.insertOp(0, Rect{X: 0, Y: 0, W: opts.Width, H: height, Fill: l.theme.Background})
	return Drawing{Width: opts.Width, Height: height, Ops: l.ops}
}

// layout is the mutable render state of one frame.
type layout struct {
	opts  LayoutOptions
	theme Theme
	width float64 // frame width
	y     float64 // cursor (bottom of the last block)
	ops   []DrawOp
}

func (l *layout) add(op DrawOp) { l.ops = append(l.ops, op) }

func (l *layout) cursorY() float64 { return l.y }

// renderComponents lays a component list into (x, yTop, width) and advances
// the outer cursor when the region is the document flow (depth 0).
func (l *layout) renderComponents(components []Component, x, yTop, width float64) float64 {
	y := yTop
	for i, component := range components {
		if i > 0 {
			y += layoutBlockGap
		}
		if y >= l.opts.MaxHeight {
			break
		}
		y += l.renderComponent(component, x, y, width)
	}
	return y - yTop
}

// renderComponent draws one component with its top-left at (x, y) and returns
// its height.
func (l *layout) renderComponent(c Component, x, y, width float64) float64 {
	// Third-party components render through their registered block renderer
	// first (docs/presentation-engine.md §17 P3.11).
	if entry, isCustom := customLookup(c.Type); isCustom {
		if frag, ok := entry.block(c, width, l.theme); ok {
			for _, op := range frag.Ops {
				l.add(translateOp(op, x, y))
			}
			return frag.Height
		}
	}
	// The renderer treats IR content as untrusted even after validation:
	// every string is drawn via escaped text glyphs, never via markup.
	switch c.Type {
	case "heading":
		var props HeadingProps
		_ = decodeStrict(c.Props, &props)
		return l.drawHeadingBlock(c.Content, props.Level, x, y, width)
	case "text", "paragraph":
		return l.drawParagraphBlock(c.Content, 13, l.theme.TextPrimary, false, x, y, width)
	case "markdown":
		return l.drawMarkdownBlock(c, x, y, width)
	case "list":
		return l.drawListBlock(c, x, y, width)
	case "quote":
		return l.drawQuoteBlock(c, x, y, width)
	case "code":
		return l.drawCodeBlock(c, x, y, width)
	case "link":
		return l.drawLinkBlock(c, x, y, width)
	case "badge":
		return l.drawBadgeBlock(c, x, y)
	case "divider":
		l.add(Rect{X: x, Y: y + 8, W: width, H: 1, Fill: l.theme.Border})
		return 17
	case "table":
		return l.drawTableBlock(c, x, y, width)
	case "card":
		return l.drawCardBlock(c, x, y, width)
	case "card-grid":
		return l.drawCardGridBlock(c, x, y, width)
	case "comparison":
		return l.drawComparisonBlock(c, x, y, width)
	case "key-value":
		return l.drawKeyValueBlock(c, x, y, width)
	case "kpi":
		return l.drawKPIBlock(c, x, y, width)
	case "progress":
		return l.drawProgressBlock(c, x, y, width)
	case "status":
		return l.drawStatusBlock(c, x, y, width)
	case "alert":
		return l.drawAlertBlock(c, x, y, width)
	case "accordion":
		return l.drawAccordionBlock(c, x, y, width)
	case "chart":
		return l.drawChartBlock(c, x, y, width)
	case "gauge":
		return l.drawGaugeBlock(c, x, y, width)
	case "flowchart":
		return l.drawFlowBlock(c, x, y, width)
	case "dependency-graph":
		return l.drawDependencyGraphBlock(c, x, y, width)
	case "mindmap":
		return l.drawMindmapBlock(c, x, y, width)
	case "state-diagram":
		return l.drawStateBlock(c, x, y, width)
	case "er-diagram":
		return l.drawERBlock(c, x, y, width)
	case "class-diagram":
		return l.drawClassBlock(c, x, y, width)
	case "form":
		return l.drawFormBlock(c, x, y, width)
	case "sequence-diagram":
		return l.drawSequenceBlock(c, x, y, width)
	case "timeline":
		return l.drawTimelineBlock(c, x, y, width)
	case "gantt":
		return l.drawGanttBlock(c, x, y, width)
	case "image":
		return l.drawImageBlock(c, x, y, width)
	case "image-gallery":
		return l.drawGalleryBlock(c, x, y, width)
	case "file":
		return l.drawFileBlock(c, x, y, width)
	case "button":
		return l.drawButtonBlock(c, x, y, width)
	default:
		return 0
	}
}

// --- text primitives ---------------------------------------------------------

// wrapText wraps text into lines no wider than maxWidth using the same font
// metrics the rasterizer uses. Overlong words (URLs, hashes) hard-break.
func wrapText(text string, size float64, bold, mono bool, maxWidth float64) []string {
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, wrapLine(line, size, bold, mono, maxWidth)...)
	}
	if len(out) == 0 {
		out = []string{""}
	}
	return out
}

func wrapLine(line string, size float64, bold, mono bool, maxWidth float64) []string {
	if maxWidth <= 0 {
		return []string{line}
	}
	if measureText(size, bold, mono, line) <= maxWidth {
		return []string{line}
	}
	words := strings.Fields(line)
	if len(words) == 0 {
		return []string{line}
	}
	var out []string
	current := ""
	for _, word := range words {
		candidate := word
		if current != "" {
			candidate = current + " " + word
		}
		if measureText(size, bold, mono, candidate) <= maxWidth {
			current = candidate
			continue
		}
		if current != "" {
			out = append(out, current)
			current = ""
		}
		// The word alone exceeds the line: hard-break it.
		for measureText(size, bold, mono, word) > maxWidth {
			cut := len(word)
			for cut > 1 && measureText(size, bold, mono, word[:cut-1]) > maxWidth {
				cut--
			}
			if cut <= 1 {
				break
			}
			out = append(out, word[:cut-1])
			word = word[cut-1:]
		}
		current = word
	}
	if current != "" {
		out = append(out, current)
	}
	if len(out) == 0 {
		out = []string{line}
	}
	return out
}

func lineHeight(size float64) float64 { return size * lineHeightFactor }

// pushText adds one glyph op with the baseline computed from a top y.
func (l *layout) pushText(text string, x, yTop, size float64, bold, mono bool, color, anchor string) {
	l.add(Glyph{X: x, Y: yTop + size*0.93, Size: size, Bold: bold, Mono: mono, Color: color, Anchor: anchor, Text: text})
}

// measureTextBlock returns the wrapped line count of a text block — the
// height math without drawing anything.
func measureTextBlock(text string, size float64, width float64) int {
	return len(wrapText(text, size, false, false, width))
}

// drawTextBlock draws wrapped text and returns its height.
func (l *layout) drawTextBlock(text string, size float64, color string, bold, mono bool, x, y, width float64) float64 {
	lines := wrapText(text, size, bold, mono, width)
	lh := lineHeight(size)
	for i, line := range lines {
		l.pushText(line, x, y+float64(i)*lh, size, bold, mono, color, "start")
	}
	return float64(len(lines)) * lh
}

func (l *layout) drawParagraphBlock(text string, size float64, color string, bold bool, x, y, width float64) float64 {
	return l.drawTextBlock(text, size, color, bold, false, x, y, width)
}

var headingSizes = map[int]float64{1: 24, 2: 19.5, 3: 16.5, 4: 14.5, 5: 13, 6: 12}

func (l *layout) pushHeading(text string, level int) {
	size := headingSizes[level]
	if size == 0 {
		size = 12
	}
	l.drawTextBlock(text, size, l.theme.TextPrimary, true, false, layoutOuterPad, l.y, l.width-2*layoutOuterPad)
	l.y += lineHeight(size)
}

func (l *layout) drawHeadingBlock(text string, level int, x, y, width float64) float64 {
	if level < 1 || level > 6 {
		level = 2
	}
	size := headingSizes[level]
	return l.drawTextBlock(text, size, l.theme.TextPrimary, true, false, x, y, width)
}

func (l *layout) drawMarkdownBlock(c Component, x, y, width float64) float64 {
	// Static formats carry markdown verbatim: the source is honest data, and
	// inline styling belongs to the web renderer (§7.3 allows typographic
	// adaptation, not content rewriting).
	return l.drawParagraphBlock(c.Content, 13, l.theme.TextPrimary, false, x, y, width)
}

func (l *layout) drawListBlock(c Component, x, y, width float64) float64 {
	var props ListProps
	_ = decodeStrict(c.Props, &props)
	var items []ListItem
	_ = jsonUnmarshalItems(c.Items, &items)
	markerWidth := 18.0
	total := 0.0
	for i, item := range items {
		marker := "•"
		if props.Ordered {
			marker = fmt.Sprintf("%d.", i+1)
		}
		l.pushText(marker, x, y+total+1.5, 12.5, false, false, l.theme.TextSecondary, "start")
		used := l.drawTextBlock(item.Label, 12.5, l.theme.TextPrimary, false, false, x+markerWidth, y+total, width-markerWidth)
		if item.Description != "" {
			used += l.drawTextBlock(item.Description, 11.5, l.theme.TextSecondary, false, false, x+markerWidth, y+total+used, width-markerWidth)
		}
		total += used + 3
	}
	if total > 0 {
		total -= 3
	}
	return total
}

func (l *layout) drawQuoteBlock(c Component, x, y, width float64) float64 {
	var props QuoteProps
	_ = decodeStrict(c.Props, &props)
	text := c.Content
	if props.Attribution != "" {
		text += "\n— " + props.Attribution
	}
	l.add(Rect{X: x, Y: y + 2, W: 3, H: lineHeight(12.5) + 6, Fill: l.theme.Accent})
	return l.drawTextBlock(text, 12.5, l.theme.TextSecondary, false, false, x+14, y, width-14)
}

func (l *layout) drawCodeBlock(c Component, x, y, width float64) float64 {
	var props CodeProps
	_ = decodeStrict(c.Props, &props)
	size := 11.5
	lh := lineHeight(size)
	lines := wrapText(c.Content, size, false, true, width-20)
	height := float64(len(lines))*lh + 16
	l.add(Rect{X: x, Y: y, W: width, H: height, Fill: l.theme.Surface, Radius: 6})
	for i, line := range lines {
		l.pushText(line, x+10, y+8+float64(i)*lh, size, false, true, l.theme.TextPrimary, "start")
	}
	return height
}

func (l *layout) drawLinkBlock(c Component, x, y, width float64) float64 {
	var props LinkProps
	_ = decodeStrict(c.Props, &props)
	label := c.Content
	if label == "" {
		label = props.Href
	}
	height := l.drawTextBlock(label, 12.5, l.theme.Accent, false, false, x, y, width)
	underlineWidth := measureText(12.5, false, false, label)
	if underlineWidth > width {
		underlineWidth = width
	}
	l.add(Segment{X1: x, Y1: y + height - 2, X2: x + underlineWidth, Y2: y + height - 2, Color: l.theme.Accent, Width: 1})
	if props.Description != "" {
		height += l.drawTextBlock(props.Description, 11, l.theme.TextSecondary, false, false, x, y+height+2, width) + 2
	}
	return height
}

func (l *layout) drawBadgeBlock(c Component, x, y float64) float64 {
	var props BadgeProps
	_ = decodeStrict(c.Props, &props)
	color := l.theme.StatusColor(props.Status)
	label := c.Content
	textWidth := measureText(11, true, false, label)
	height := 22.0
	l.add(Rect{X: x, Y: y, W: textWidth + 20, H: height, Fill: color, Radius: height / 2})
	l.pushText(label, x+(textWidth+20)/2, y+(height-lineHeight(11))/2, 11, true, false, l.pickTextColor(color), "middle")
	return height
}

// pickTextColor chooses black or white glyph color for a filled chip: the
// static palettes use mid-saturation colors where white loses contrast on the
// warning tone.
func (l *layout) pickTextColor(bg string) string {
	if bg == l.theme.Warning {
		return "#161616"
	}
	return "#ffffff"
}

func (l *layout) drawStatusBlock(c Component, x, y, width float64) float64 {
	var props StatusProps
	_ = decodeStrict(c.Props, &props)
	color := l.theme.StatusColor(props.Status)
	l.add(Dot{CX: x + 7, CY: y + 9, R: 5, Color: color})
	height := l.drawTextBlock(c.Content, 12.5, l.theme.TextPrimary, true, false, x+20, y, width-20)
	if props.Detail != "" {
		height += l.drawTextBlock(props.Detail, 11, l.theme.TextSecondary, false, false, x+20, y+height, width-20)
	}
	return height
}

func (l *layout) drawAlertBlock(c Component, x, y, width float64) float64 {
	var props AlertProps
	_ = decodeStrict(c.Props, &props)
	if props.Severity == "" {
		props.Severity = statusInfo
	}
	color := l.theme.StatusColor(props.Severity)
	inner := width - 24
	contentLines := measureTextBlock(c.Content, 12.5, inner)
	height := float64(contentLines)*lineHeight(12.5) + 20
	if props.Title != "" {
		height += lineHeight(12.5)
	}
	l.add(Rect{X: x, Y: y, W: width, H: height, Fill: l.theme.Surface, Radius: 6})
	l.add(Rect{X: x, Y: y, W: 4, H: height, Fill: color, Radius: 2})
	textY := y + 10
	if props.Title != "" {
		l.pushText(props.Title, x+18, textY, 12.5, true, false, color, "start")
		textY += lineHeight(12.5)
	}
	l.drawTextBlock(c.Content, 12.5, l.theme.TextPrimary, false, false, x+18, textY, inner)
	return height
}

func (l *layout) drawImageBlock(c Component, x, y, width float64) float64 {
	var props ImageProps
	_ = decodeStrict(c.Props, &props)
	boxHeight := 96.0
	l.add(Rect{X: x, Y: y, W: width, H: boxHeight, Fill: l.theme.Surface, Radius: 6})
	l.add(Rect{X: x, Y: y, W: width, H: boxHeight, Stroke: l.theme.Border, StrokeWidth: 1, Radius: 6})
	l.pushText("IMAGE", x+width/2, y+boxHeight/2-lineHeight(11)/2, 11, true, false, l.theme.TextSecondary, "middle")
	caption := props.Caption
	if caption == "" {
		caption = c.Content
	}
	height := boxHeight
	if caption != "" {
		height += 4 + l.drawTextBlock(caption, 11.5, l.theme.TextSecondary, false, false, x, y+height+4, width)
	}
	return height
}

func (l *layout) drawGalleryBlock(c Component, x, y, width float64) float64 {
	var props GalleryProps
	_ = decodeStrict(c.Props, &props)
	var items []GalleryItem
	_ = jsonUnmarshalItems(c.Items, &items)
	if len(items) == 0 {
		return 0
	}
	cols := props.Columns
	if cols < 1 || cols > 4 {
		cols = 3
		if len(items) < cols {
			cols = len(items)
		}
	}
	gap := 12.0
	cellWidth := (width - float64(cols-1)*gap) / float64(cols)
	cellHeight := 76.0
	total := 0.0
	for i, item := range items {
		col := i % cols
		row := i / cols
		cx := x + float64(col)*(cellWidth+gap)
		cy := y + float64(row)*(cellHeight+gap+16)
		l.add(Rect{X: cx, Y: cy, W: cellWidth, H: cellHeight, Fill: l.theme.Surface, Radius: 6})
		l.pushText("IMAGE", cx+cellWidth/2, cy+cellHeight/2-lineHeight(10.5)/2, 10.5, true, false, l.theme.TextSecondary, "middle")
		caption := item.Caption
		if caption == "" {
			caption = item.Alt
		}
		if caption != "" {
			l.drawTextBlock(clipLine(caption, 10.5, cellWidth), 10.5, l.theme.TextSecondary, false, false, cx, cy+cellHeight+4, cellWidth)
		}
		rows := row + 1
		total = float64(rows)*(cellHeight+gap+16) - gap - 16
	}
	return total
}

func (l *layout) drawFileBlock(c Component, x, y, width float64) float64 {
	var props FileProps
	_ = decodeStrict(c.Props, &props)
	label := props.Name
	if props.Size != "" {
		label += "  ·  " + props.Size
	}
	textWidth := measureText(12, false, false, label)
	chipWidth := textWidth + 44
	if chipWidth > width {
		chipWidth = width
	}
	height := 30.0
	l.add(Rect{X: x, Y: y, W: chipWidth, H: height, Fill: l.theme.Surface, Radius: 6})
	l.add(Rect{X: x, Y: y, W: chipWidth, H: height, Stroke: l.theme.Border, StrokeWidth: 1, Radius: 6})
	l.pushText("≡", x+12, y+(height-lineHeight(12))/2, 12, false, false, l.theme.TextSecondary, "start")
	l.pushText(clipLine(label, 12, chipWidth-44), x+30, y+(height-lineHeight(12))/2, 12, false, false, l.theme.TextPrimary, "start")
	total := height
	if c.Content != "" {
		total += 4 + l.drawTextBlock(c.Content, 11, l.theme.TextSecondary, false, false, x, y+total+4, width)
	}
	return total
}

func (l *layout) drawButtonBlock(c Component, x, y, width float64) float64 {
	label := c.Content
	textWidth := measureText(12, true, false, label)
	buttonWidth := textWidth + 32
	height := 32.0
	l.add(Rect{X: x, Y: y, W: buttonWidth, H: height, Fill: l.theme.Accent, Radius: 6})
	l.pushText(label, x+buttonWidth/2, y+(height-lineHeight(12))/2, 12, true, false, "#ffffff", "middle")
	total := height
	if actions := describeActions(c.Actions); len(actions) > 0 {
		note := "→ " + strings.Join(actions, ", ")
		total += 4
		l.drawTextBlock(clipLine(note, 11, width), 11, l.theme.TextSecondary, false, false, x, y+total, width)
		total += lineHeight(11)
	}
	return total
}

// drawFormBlock renders the declarative form for static formats: field boxes
// with names, types and required markers plus the submit strip. Static
// channels cannot carry input — the strip says where the form lives (the
// Temporality UI answers it), so the fallback keeps the meaning (§7.6).
func (l *layout) drawFormBlock(c Component, x, y, width float64) float64 {
	var props struct {
		Schema json.RawMessage `json:"schema,omitempty"`
		Submit string          `json:"submit,omitempty"`
	}
	_ = decodeStrict(c.Props, &props)
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	_ = json.Unmarshal(props.Schema, &schema)
	required := map[string]bool{}
	for _, name := range schema.Required {
		required[name] = true
	}
	total := 0.0
	if c.Content != "" {
		total += l.drawTextBlock(c.Content, 12.5, l.theme.TextPrimary, true, false, x, y+total, width) + 4
	}
	const fieldHeight = 30.0
	for name, raw := range schema.Properties {
		var field struct {
			Type   string   `json:"type"`
			Enum   []string `json:"enum"`
			Title  string   `json:"title"`
			Format string   `json:"format"`
		}
		_ = json.Unmarshal(raw, &field)
		fieldKind := field.Type
		if fieldKind == "" {
			fieldKind = "string"
		}
		if len(field.Enum) > 0 {
			fieldKind = "choice"
		}
		l.add(Rect{X: x, Y: y + total, W: width, H: fieldHeight, Fill: l.theme.Background, Stroke: l.theme.Border, StrokeWidth: 1, Radius: 4})
		label := name
		if field.Title != "" {
			label = field.Title + " (" + name + ")"
		}
		if required[name] {
			label += " *"
		}
		l.pushText(clipLine(label, 10.5, width-90), x+10, y+total+(fieldHeight-lineHeight(10.5))/2, 10.5, false, false, l.theme.TextPrimary, "start")
		l.pushText(clipLine(fieldKind, 10.5, 80), x+width-10, y+total+(fieldHeight-lineHeight(10.5))/2, 10.5, false, false, l.theme.TextSecondary, "end")
		total += fieldHeight + 8
	}
	submit := props.Submit
	if submit == "" {
		submit = "Submit"
	}
	buttonWidth := measureText(12, true, false, submit) + 32
	l.add(Rect{X: x, Y: y + total, W: buttonWidth, H: 30, Fill: l.theme.Accent, Radius: 6})
	l.pushText(submit, x+buttonWidth/2, y+total+(30-lineHeight(12))/2, 12, true, false, "#ffffff", "middle")
	total += 30
	for _, action := range c.Actions {
		if action.Type == "submit" && action.Target != "" {
			total += 4
			l.drawTextBlock(clipLine("answers land on operation "+action.Target, 10, width), 10, l.theme.TextSecondary, false, false, x, y+total, width)
			total += lineHeight(10)
		}
	}
	return total
}

// clipLine truncates one line of text to fit maxWidth, appending an ellipsis
// when cutting was needed.
func clipLine(text string, size float64, maxWidth float64) string {
	if measureText(size, false, false, text) <= maxWidth {
		return text
	}
	runes := []rune(text)
	for len(runes) > 1 && measureText(size, false, false, string(runes)+"…") > maxWidth {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + "…"
}
