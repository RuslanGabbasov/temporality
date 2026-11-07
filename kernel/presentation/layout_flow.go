package presentation

import (
	"fmt"
	"math"
	"strings"
)

// Diagram blocks (docs/presentation-engine.md §6.4): flowchart and sequence.
// The IR is engine-neutral; the static renderers draw the graph directly, the
// web renderer may map the same nodes/edges onto Mermaid.

func (l *layout) drawFlowBlock(c Component, x, y, width float64) float64 {
	var data FlowData
	_ = decodeStrict(c.Data, &data)
	var props FlowProps
	_ = decodeStrict(c.Props, &props)
	if props.Direction == "" {
		props.Direction = "TB"
	}
	if len(data.Nodes) == 0 {
		return 0
	}
	frag := layoutFlow(data, props.Direction, width, l.theme)
	return l.drawFragmentWithOptionalTitle(props.Title, frag, x, y)
}

func (l *layout) drawSequenceBlock(c Component, x, y, width float64) float64 {
	var data SequenceData
	_ = decodeStrict(c.Data, &data)
	var props struct {
		Title string `json:"title,omitempty"`
	}
	_ = decodeStrict(c.Props, &props)
	if len(data.Participants) == 0 || len(data.Messages) == 0 {
		return 0
	}
	frag := layoutSequence(data, width, l.theme)
	return l.drawFragmentWithOptionalTitle(props.Title, frag, x, y)
}

// drawFragmentWithOptionalTitle draws a title line, then a fragment shifted
// into place below it. Fragment coordinates are local: every op is translated
// by the block origin here.
func (l *layout) drawFragmentWithOptionalTitle(title string, frag Drawing, x, y float64) float64 {
	offset := 0.0
	if title != "" {
		offset = l.drawTextBlock(title, 13, l.theme.TextPrimary, true, false, x, y, frag.Width) + 6
	}
	for _, op := range frag.Ops {
		l.add(translateOp(op, x, y+offset))
	}
	return offset + frag.Height
}

// translateOp shifts one draw op by (dx, dy).
func translateOp(op DrawOp, dx, dy float64) DrawOp {
	switch v := op.(type) {
	case Rect:
		v.X += dx
		v.Y += dy
		return v
	case Segment:
		v.X1 += dx
		v.Y1 += dy
		v.X2 += dx
		v.Y2 += dy
		return v
	case Glyph:
		v.X += dx
		v.Y += dy
		return v
	case Wedge:
		v.CX += dx
		v.CY += dy
		return v
	case Poly:
		for i := range v.Points {
			v.Points[i].X += dx
			v.Points[i].Y += dy
		}
		return v
	case Dot:
		v.CX += dx
		v.CY += dy
		return v
	case Arrow:
		v.X += dx
		v.Y += dy
		return v
	default:
		return op
	}
}

// flowLayoutBox is one positioned node.
type flowLayoutBox struct {
	node       FlowNode
	x, y, w, h float64
}

// layoutFlow renders a flowchart: layered (longest-path) layout with straight
// connectors and arrowheads. Dependency cycles were refused at validation, so
// the level assignment always terminates.
func layoutFlow(data FlowData, direction string, width float64, theme Theme) Drawing {
	frag := Drawing{Width: width}
	const (
		nodePadX   = 12.0
		nodePadY   = 8.0
		labelSize  = 11.0
		levelGap   = 44.0
		siblingGap = 16.0
		maxNodeW   = 170.0
	)

	// Level assignment: longest path from any source.
	level := map[string]int{}
	incoming := map[string]int{}
	adj := map[string][]string{}
	for _, edge := range data.Edges {
		incoming[edge.To]++
		adj[edge.From] = append(adj[edge.From], edge.To)
	}
	remaining := map[string]int{}
	for id := range incoming {
		remaining[id] = incoming[id]
	}
	// Topological pass (Kahn), relaxing the longest-path levels.
	queue := make([]string, 0, len(data.Nodes))
	for _, node := range data.Nodes {
		if incoming[node.ID] == 0 {
			level[node.ID] = 0
			queue = append(queue, node.ID)
		}
	}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		for _, next := range adj[id] {
			if level[id]+1 > level[next] {
				level[next] = level[id] + 1
			}
			remaining[next]--
			if remaining[next] == 0 {
				queue = append(queue, next)
			}
		}
	}
	// Nodes unreachable via the topological pass (cycle-guarded validation
	// makes this impossible, but layout must not loop on hostile input).
	for _, node := range data.Nodes {
		if _, ok := level[node.ID]; !ok {
			level[node.ID] = 0
		}
	}

	// Node sizes.
	boxes := make([]flowLayoutBox, len(data.Nodes))
	nodeByID := map[string]int{}
	for i, node := range data.Nodes {
		lines := wrapText(node.Label, labelSize, false, false, maxNodeW-2*nodePadX)
		w := 0.0
		for _, line := range lines {
			if lw := measureText(labelSize, false, false, line); lw > w {
				w = lw
			}
		}
		boxes[i] = flowLayoutBox{node: node, w: minFloat(w+2*nodePadX, maxNodeW), h: float64(len(lines))*lineHeight(labelSize) + 2*nodePadY}
		nodeByID[node.ID] = i
	}

	// Positioning.
	maxLevel := 0
	for _, lv := range level {
		if lv > maxLevel {
			maxLevel = lv
		}
	}
	byLevel := make([][]int, maxLevel+1)
	for i, node := range data.Nodes {
		lv := level[node.ID]
		byLevel[lv] = append(byLevel[lv], i)
	}
	if direction == "LR" {
		// Levels spread horizontally, members vertically.
		levelX := 0.0
		for lv := 0; lv <= maxLevel; lv++ {
			members := byLevel[lv]
			levelW := 0.0
			for _, idx := range members {
				if boxes[idx].w > levelW {
					levelW = boxes[idx].w
				}
			}
			yCursor := 0.0
			for _, idx := range members {
				box := &boxes[idx]
				box.x = levelX
				box.y = yCursor
				yCursor += box.h + siblingGap
			}
			levelX += levelW + levelGap
		}
		frag.Width = levelX - levelGap
		if frag.Width < width*0.5 {
			frag.Width = width * 0.5
		}
	} else {
		// TB: levels stack vertically, members spread over the full width.
		levelHeight := make([]float64, maxLevel+1)
		for lv := 0; lv <= maxLevel; lv++ {
			for _, idx := range byLevel[lv] {
				if boxes[idx].h > levelHeight[lv] {
					levelHeight[lv] = boxes[idx].h
				}
			}
		}
		widest := 0.0
		yCursor := 0.0
		for lv := 0; lv <= maxLevel; lv++ {
			members := byLevel[lv]
			rowWidth := 0.0
			for _, idx := range members {
				rowWidth += boxes[idx].w
			}
			rowWidth += float64(len(members)-1) * siblingGap
			if rowWidth > widest {
				widest = rowWidth
			}
			startX := (width - rowWidth) / 2
			if startX < 0 {
				startX = 0
			}
			for _, idx := range members {
				box := &boxes[idx]
				box.x = startX
				box.y = yCursor + (levelHeight[lv]-box.h)/2
				startX += box.w + siblingGap
			}
			yCursor += levelHeight[lv] + levelGap
		}
		_ = widest
		frag.Width = width
	}

	// Draw boxes.
	nodeColor := func(node FlowNode) string {
		if node.Status != "" && node.Status != statusNeutral {
			return theme.StatusColor(node.Status)
		}
		return theme.Border
	}
	for _, box := range boxes {
		color := nodeColor(box.node)
		switch box.node.Shape {
		case "diamond":
			cx, cy := box.x+box.w/2, box.y+box.h/2
			frag.Ops = append(frag.Ops, Poly{Points: []Point{{cx, box.y}, {box.x + box.w, cy}, {cx, box.y + box.h}, {box.x, cy}}, Fill: theme.Surface})
			frag.Ops = append(frag.Ops, Poly{Points: []Point{{cx, box.y}, {box.x + box.w, cy}, {cx, box.y + box.h}, {box.x, cy}}, Color: color, Width: 1.2})
		case "round":
			frag.Ops = append(frag.Ops, Rect{X: box.x, Y: box.y, W: box.w, H: box.h, Fill: theme.Surface, Stroke: color, StrokeWidth: 1.2, Radius: box.h / 2})
		default:
			frag.Ops = append(frag.Ops, Rect{X: box.x, Y: box.y, W: box.w, H: box.h, Fill: theme.Surface, Stroke: color, StrokeWidth: 1.2, Radius: 6})
		}
		lines := wrapText(box.node.Label, labelSize, false, false, box.w-2*nodePadX)
		textTop := box.y + (box.h-float64(len(lines))*lineHeight(labelSize))/2
		for i, line := range lines {
			frag.Ops = append(frag.Ops, Glyph{X: box.x + box.w/2, Y: textTop + float64(i)*lineHeight(labelSize) + labelSize*0.93, Size: labelSize, Color: theme.TextPrimary, Anchor: "middle", Text: line})
		}
	}

	// Draw edges with arrowheads and midpoint labels.
	edgeLabelBG := make([]Rect, 0)
	for _, edge := range data.Edges {
		from, okFrom := nodeByID[edge.From]
		to, okTo := nodeByID[edge.To]
		if !okFrom || !okTo {
			continue
		}
		a, b := boxes[from], boxes[to]
		var x1, y1, x2, y2 float64
		if direction == "LR" {
			x1, y1 = a.x+a.w, a.y+a.h/2
			x2, y2 = b.x, b.y+b.h/2
		} else {
			x1, y1 = a.x+a.w/2, a.y+a.h
			x2, y2 = b.x+b.w/2, b.y
		}
		style := Segment{X1: x1, Y1: y1, X2: x2, Y2: y2, Color: theme.TextSecondary, Width: 1.2}
		if edge.Dashed {
			style.Dash = []float64{4, 3}
		}
		frag.Ops = append(frag.Ops, style)
		angle := math.Atan2(y2-y1, x2-x1)
		frag.Ops = append(frag.Ops, Arrow{X: x2, Y: y2, Angle: angle, Size: 7, Color: theme.TextSecondary})
		if edge.Label != "" {
			mx, my := (x1+x2)/2, (y1+y2)/2
			tw := measureText(10, false, false, edge.Label)
			edgeLabelBG = append(edgeLabelBG, Rect{X: mx - tw/2 - 4, Y: my - 8, W: tw + 8, H: 15, Fill: theme.Background})
			frag.Ops = append(frag.Ops, Glyph{X: mx, Y: my + 3.5, Size: 10, Color: theme.TextSecondary, Anchor: "middle", Text: edge.Label})
		}
	}
	// Label backgrounds go before their glyphs: append order is bg, glyph per
	// label already, but backgrounds must not cover other lines — insert all
	// backgrounds after the edges, before the labels. v1 keeps append order.
	for _, bg := range edgeLabelBG {
		frag.Ops = append(frag.Ops, bg)
	}

	frag.Height = flowExtent(boxes) + 8
	return frag
}

// flowExtent computes the bottom-right extent of the laid boxes.
func flowExtent(boxes []flowLayoutBox) float64 {
	maxY := 0.0
	for _, box := range boxes {
		if box.y+box.h > maxY {
			maxY = box.y + box.h
		}
	}
	return maxY
}

// layoutSequence renders a sequence diagram: lifelines with messages in
// order, dashed return arrows.
func layoutSequence(data SequenceData, width float64, theme Theme) Drawing {
	frag := Drawing{Width: width}
	const (
		headH     = 26.0
		msgStep   = 32.0
		labelSize = 10.5
		margin    = 8.0
	)
	count := len(data.Participants)
	slotW := (width - 2*margin) / float64(count)
	laneX := func(id string) float64 {
		for i, p := range data.Participants {
			if p.ID == id {
				return margin + slotW*(float64(i)+0.5)
			}
		}
		return margin + slotW/2
	}

	// Participant heads.
	for i, p := range data.Participants {
		cx := margin + slotW*(float64(i)+0.5)
		tw := measureText(labelSize, true, false, p.Label)
		headW := minFloat(tw+16, slotW-8)
		frag.Ops = append(frag.Ops, Rect{X: cx - headW/2, Y: 4, W: headW, H: headH, Fill: theme.Surface, Stroke: theme.Border, StrokeWidth: 1, Radius: 6})
		frag.Ops = append(frag.Ops, Glyph{X: cx, Y: 4 + headH/2 + labelSize*0.35, Size: labelSize, Bold: true, Color: theme.TextPrimary, Anchor: "middle", Text: clipLine(p.Label, labelSize, headW-12)})
	}

	lifelineTop := 4 + headH
	msgTop := lifelineTop + 14
	y := msgTop
	for _, msg := range data.Messages {
		x1 := laneX(msg.From)
		x2 := laneX(msg.To)
		label := msg.Label
		if label != "" {
			tw := measureText(labelSize, false, false, label)
			mx := (x1 + x2) / 2
			start := mx - tw/2 - 4
			if start < margin {
				start = margin
			}
			frag.Ops = append(frag.Ops, Rect{X: start, Y: y - 13, W: minFloat(tw+8, absF(x2-x1)), H: 14, Fill: theme.Background})
			frag.Ops = append(frag.Ops, Glyph{X: mx, Y: y - 2.5, Size: labelSize, Color: theme.TextPrimary, Anchor: "middle", Text: clipLine(label, labelSize, absF(x2-x1)-8)})
		}
		style := Segment{X1: x1, Y1: y, X2: x2, Y2: y, Color: theme.TextSecondary, Width: 1.2}
		if msg.Kind == "return" {
			style.Dash = []float64{4, 3}
		}
		frag.Ops = append(frag.Ops, style)
		angle := 0.0
		if x2 < x1 {
			angle = math.Pi
		}
		frag.Ops = append(frag.Ops, Arrow{X: x2, Y: y, Angle: angle, Size: 7, Color: theme.TextSecondary})
		y += msgStep
	}
	// Lifelines: drawn after the labels so they sit under message arrows in
	// SVG paint order but stay visible through the whole exchange.
	for i := range data.Participants {
		cx := margin + slotW*(float64(i)+0.5)
		frag.Ops = append(frag.Ops, Segment{X1: cx, Y1: lifelineTop, X2: cx, Y2: y - msgStep + 8, Color: theme.Border, Width: 1, Dash: []float64{3, 4}})
	}
	frag.Height = y - msgStep + 16
	return frag
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func absF(v float64) float64 {
	return math.Abs(v)
}

// flowMermaid produces the Mermaid source of a flowchart — the web renderer's
// diagram engine consumes it (the IR itself stays Mermaid-free, §2.2: the
// conversion is a renderer detail, not a representation change).
func flowMermaid(data FlowData, direction string) string {
	if direction == "" {
		direction = "TB"
	}
	var b strings.Builder
	b.WriteString("flowchart " + direction + "\n")
	nodeShape := func(node FlowNode) (string, string) {
		switch node.Shape {
		case "round":
			return "(", ")"
		case "diamond":
			return "{", "}"
		default:
			return "[", "]"
		}
	}
	for _, node := range data.Nodes {
		open, close := nodeShape(node)
		fmt.Fprintf(&b, "    %s%s\"%s\"%s\n", node.ID, open, sanitizeMermaid(node.Label), close)
	}
	for _, edge := range data.Edges {
		if edge.Label != "" {
			if edge.Dashed {
				fmt.Fprintf(&b, "    %s -.\"%s\".-> %s\n", edge.From, sanitizeMermaid(edge.Label), edge.To)
			} else {
				fmt.Fprintf(&b, "    %s -->|\"%s\"| %s\n", edge.From, sanitizeMermaid(edge.Label), edge.To)
			}
			continue
		}
		if edge.Dashed {
			fmt.Fprintf(&b, "    %s -.-> %s\n", edge.From, edge.To)
			continue
		}
		fmt.Fprintf(&b, "    %s --> %s\n", edge.From, edge.To)
	}
	return b.String()
}

// sequenceMermaid produces the Mermaid source of a sequence diagram.
func sequenceMermaid(data SequenceData) string {
	var b strings.Builder
	b.WriteString("sequenceDiagram\n")
	for _, p := range data.Participants {
		fmt.Fprintf(&b, "    participant %s as \"%s\"\n", p.ID, sanitizeMermaid(p.Label))
	}
	for _, msg := range data.Messages {
		arrow := "->>"
		if msg.Kind == "async" {
			arrow = "->>"
		} else if msg.Kind == "return" {
			arrow = "-->>"
		}
		if msg.Label != "" {
			fmt.Fprintf(&b, "    %s %s %s : %s\n", msg.From, arrow, msg.To, sanitizeMermaid(msg.Label))
			continue
		}
		fmt.Fprintf(&b, "    %s %s %s\n", msg.From, arrow, msg.To)
	}
	return b.String()
}

// sanitizeMermaid strips quotes and newlines from labels so a crafted label
// cannot break out of the Mermaid node syntax (the web renderer treats the
// result as data; this keeps the diagram parseable).
func sanitizeMermaid(text string) string {
	replacer := strings.NewReplacer("\"", "'", "\n", " ", "\r", "")
	return replacer.Replace(text)
}
