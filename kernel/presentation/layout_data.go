package presentation

import (
	"encoding/json"
	"strings"
)

// Structured-data blocks (docs/presentation-engine.md §6.2): table, card,
// card-grid, comparison, key-value, kpi, progress, accordion.

func (l *layout) drawTableBlock(c Component, x, y, width float64) float64 {
	var data TableData
	_ = decodeStrict(c.Data, &data)
	if len(data.Columns) == 0 || len(data.Rows) == 0 {
		return 0
	}
	const (
		cellPad    = 10.0
		cellSize   = 11.5
		headerSize = 11.5
	)
	lh := lineHeight(cellSize)

	// Column widths: natural width from content, clamped so no column eats
	// the frame; wide content wraps.
	cols := len(data.Columns)
	widths := make([]float64, cols)
	for i := range data.Columns {
		title := data.Columns[i].Title
		if title == "" {
			title = data.Columns[i].Key
		}
		widths[i] = measureText(headerSize, true, false, title) + 2*cellPad
	}
	for _, row := range data.Rows {
		for i, col := range data.Columns {
			if w := measureText(cellSize, false, false, row[col.Key].Display()) + 2*cellPad; w > widths[i] {
				widths[i] = w
			}
		}
	}
	maxCol := width * 0.45
	for i := range widths {
		if widths[i] > maxCol {
			widths[i] = maxCol
		}
	}
	total := 0.0
	for _, w := range widths {
		total += w
	}
	if total > width {
		scale := width / total
		for i := range widths {
			widths[i] *= scale
		}
	} else if total < width {
		// Distribute the slack to the first column-less table: tables read
		// better filling the frame.
		widths[0] += width - total
	}

	// Alignment per column.
	aligns := make([]string, cols)
	for i, col := range data.Columns {
		align := col.Align
		if align == "" && col.Kind == "number" {
			align = "right"
		}
		if align == "" {
			align = "left"
		}
		aligns[i] = align
	}

	totalHeight := 0.0
	if data.Title != "" {
		totalHeight += l.drawTextBlock(data.Title, 11.5, l.theme.TextSecondary, false, false, x, y+totalHeight, width) + 4
	}

	// Header row.
	headerLines := make([][]string, cols)
	headerHeight := 0.0
	for i, col := range data.Columns {
		title := col.Title
		if title == "" {
			title = col.Key
		}
		headerLines[i] = wrapText(title, headerSize, true, false, widths[i]-2*cellPad)
		if h := float64(len(headerLines[i])) * lineHeight(headerSize); h > headerHeight {
			headerHeight = h
		}
	}
	l.add(Rect{X: x, Y: y + totalHeight, W: width, H: headerHeight + 2*cellPad*0.7, Fill: l.theme.Surface})
	headerTop := y + totalHeight + cellPad*0.7
	for i := range data.Columns {
		for j, line := range headerLines[i] {
			l.pushCellText(line, x+columnX(widths, i), headerTop+float64(j)*lineHeight(headerSize), headerSize, true, aligns[i], widths[i], l.theme.TextPrimary)
		}
	}
	rowHeaderHeight := headerHeight + cellPad*1.4
	totalHeight += rowHeaderHeight

	// Body rows.
	for r, row := range data.Rows {
		cellLines := make([][]string, cols)
		rowHeight := 0.0
		for i, col := range data.Columns {
			cell := row[col.Key]
			text := cell.Display()
			if cell.Href != "" {
				text += " ↗"
			}
			cellLines[i] = wrapText(text, cellSize, false, false, widths[i]-2*cellPad)
			if h := float64(len(cellLines[i])) * lh; h > rowHeight {
				rowHeight = h
			}
		}
		rowTop := y + totalHeight
		if r%2 == 1 {
			l.add(Rect{X: x, Y: rowTop, W: width, H: rowHeight + cellPad*1.2, Fill: l.theme.Surface})
		}
		for i := range data.Columns {
			cell := row[data.Columns[i].Key]
			color := l.theme.TextPrimary
			if cell.Status != "" {
				color = l.theme.StatusColor(cell.Status)
			}
			for j, line := range cellLines[i] {
				l.pushCellText(line, x+columnX(widths, i), rowTop+cellPad*0.6+float64(j)*lh, cellSize, false, aligns[i], widths[i], color)
			}
		}
		totalHeight += rowHeight + cellPad*1.2
	}
	l.add(Segment{X1: x, Y1: y + totalHeight, X2: x + width, Y2: y + totalHeight, Color: l.theme.Border, Width: 1})
	return totalHeight
}

// columnX returns the left edge of column i.
func columnX(widths []float64, i int) float64 {
	x := 0.0
	for j := 0; j < i; j++ {
		x += widths[j]
	}
	return x
}

// pushCellText draws one table cell line honoring the column alignment.
func (l *layout) pushCellText(text string, colX, yTop, size float64, bold bool, align string, colWidth float64, color string) {
	const cellPad = 10.0
	switch align {
	case "center":
		l.pushText(text, colX+colWidth/2, yTop, size, bold, false, color, "middle")
	case "right":
		l.pushText(text, colX+colWidth-cellPad, yTop, size, bold, false, color, "end")
	default:
		l.pushText(text, colX+cellPad, yTop, size, bold, false, color, "start")
	}
}

func (l *layout) drawCardBlock(c Component, x, y, width float64) float64 {
	var props CardProps
	_ = decodeStrict(c.Props, &props)
	const innerPad = 14.0
	innerWidth := width - 2*innerPad

	// Render children first (into the live op list), then insert the card
	// background before them so the paint order is right.
	base := len(l.ops)
	bodyTop := y + innerPad
	if props.Title != "" {
		titleHeight := l.drawTextBlock(props.Title, 13, l.theme.TextPrimary, true, false, x+innerPad, bodyTop, innerWidth)
		bodyTop += titleHeight + 8
	}
	bodyHeight := l.renderComponents(c.Children, x+innerPad, bodyTop, innerWidth)
	bodyHeight = bodyTop + bodyHeight - y
	cardHeight := bodyHeight + innerPad
	l.insertOp(base, Rect{X: x, Y: y, W: width, H: cardHeight, Fill: l.theme.Surface, Radius: 8, Stroke: l.theme.Border, StrokeWidth: 1})
	return cardHeight
}

// insertOp splices one op into the stream at the given index.
func (l *layout) insertOp(index int, op DrawOp) {
	l.ops = append(l.ops, nil)
	copy(l.ops[index+1:], l.ops[index:])
	l.ops[index] = op
}

func (l *layout) drawCardGridBlock(c Component, x, y, width float64) float64 {
	var props CardGridProps
	_ = decodeStrict(c.Props, &props)
	cols := props.Columns
	if cols < 1 {
		cols = 2
	}
	if cols > 4 {
		cols = 4
	}
	gap := 12.0
	cellWidth := (width - float64(cols-1)*gap) / float64(cols)
	total := 0.0
	for start := 0; start < len(c.Children); start += cols {
		end := start + cols
		if end > len(c.Children) {
			end = len(c.Children)
		}
		// Children are cards: each draws its own surface, the row height is
		// decided by the tallest one.
		rowHeight := 0.0
		for i, child := range c.Children[start:end] {
			h := l.renderComponent(child, x+float64(i)*(cellWidth+gap), y+total, cellWidth)
			if h > rowHeight {
				rowHeight = h
			}
		}
		total += rowHeight
	}
	return total
}
func (l *layout) drawComparisonBlock(c Component, x, y, width float64) float64 {
	var items []ComparisonItem
	_ = jsonUnmarshalItems(c.Items, &items)
	if len(items) < 2 {
		return 0
	}
	// Rendered as a table: first column = option, then the union of criteria
	// in first-seen order.
	fields := []string{}
	seen := map[string]bool{}
	for _, item := range items {
		for _, field := range item.Fields {
			if !seen[field.Label] {
				seen[field.Label] = true
				fields = append(fields, field.Label)
			}
		}
	}
	data := TableData{Columns: []TableColumn{{Key: "option", Title: "Option"}}, Rows: make([]map[string]TableCell, 0, len(items))}
	for _, field := range fields {
		data.Columns = append(data.Columns, TableColumn{Key: fieldKey(field), Title: field})
	}
	for _, item := range items {
		cells := map[string]TableCell{"option": {Text: item.Label, Status: item.Status}}
		for _, field := range item.Fields {
			cells[fieldKey(field.Label)] = TableCell{Text: field.Value}
		}
		data.Rows = append(data.Rows, cells)
	}
	synthetic := Component{Type: "table", Data: mustMarshal(data)}
	return l.drawTableBlock(synthetic, x, y, width)
}

func fieldKey(label string) string { return "f_" + strings.ToLower(label) }

func (l *layout) drawKeyValueBlock(c Component, x, y, width float64) float64 {
	var items []KeyValueItem
	_ = jsonUnmarshalItems(c.Items, &items)
	if len(items) == 0 {
		return 0
	}
	const size = 12.5
	labelWidth := 0.0
	for _, item := range items {
		if w := measureText(size, false, false, item.Label); w > labelWidth {
			labelWidth = w
		}
	}
	if max := width * 0.4; labelWidth > max {
		labelWidth = max
	}
	valueX := x + labelWidth + 18
	valueWidth := width - (labelWidth + 18)
	total := 0.0
	for i, item := range items {
		if i > 0 {
			l.add(Segment{X1: x, Y1: y + total, X2: x + width, Y2: y + total, Color: l.theme.Border, Width: 0.5})
			total += 6
		}
		l.drawTextBlock(item.Label, size, l.theme.TextSecondary, false, false, x, y+total, labelWidth)
		color := l.theme.TextPrimary
		if item.Status != "" && item.Status != statusNeutral {
			color = l.theme.StatusColor(item.Status)
		}
		l.drawTextBlock(item.Value, size, color, false, false, valueX, y+total, valueWidth)
		total += lineHeight(size)
	}
	return total
}

func (l *layout) drawKPIBlock(c Component, x, y, width float64) float64 {
	var items []KPIItem
	_ = jsonUnmarshalItems(c.Items, &items)
	if len(items) == 0 {
		return 0
	}
	gap := 12.0
	cols := len(items)
	if cols > 4 {
		cols = 4
	}
	tileWidth := (width - float64(cols-1)*gap) / float64(cols)
	const tileHeight = 78.0
	total := 0.0
	for start := 0; start < len(items); start += cols {
		end := start + cols
		if end > len(items) {
			end = len(items)
		}
		for i, item := range items[start:end] {
			tx := x + float64(i)*(tileWidth+gap)
			color := l.theme.TextPrimary
			if item.Status != "" && item.Status != statusNeutral {
				color = l.theme.StatusColor(item.Status)
			}
			l.add(Rect{X: tx, Y: y + total, W: tileWidth, H: tileHeight, Fill: l.theme.Surface, Radius: 8, Stroke: l.theme.Border, StrokeWidth: 1})
			l.drawTextBlock(clipLine(item.Label, 11, tileWidth-20), 11, l.theme.TextSecondary, false, false, tx+10, y+total+10, tileWidth-20)
			valueLine := item.Value.Display() + unitSuffix(item.Unit)
			l.drawTextBlock(clipLine(valueLine, 21, tileWidth-20), 21, color, true, false, tx+10, y+total+28, tileWidth-20)
			noteY := y + total + 28 + lineHeight(21)
			note := item.Delta
			if item.Hint != "" {
				if note != "" {
					note += " · " + item.Hint
				} else {
					note = item.Hint
				}
			}
			if note != "" {
				l.drawTextBlock(clipLine(note, 10.5, tileWidth-20), 10.5, l.theme.TextSecondary, false, false, tx+10, noteY, tileWidth-20)
			}
		}
		total += tileHeight + 12
	}
	return total - 12
}

func (l *layout) drawProgressBlock(c Component, x, y, width float64) float64 {
	var items []ProgressItem
	_ = jsonUnmarshalItems(c.Items, &items)
	if len(items) == 0 {
		return 0
	}
	total := 0.0
	for _, item := range items {
		max := item.Max
		if max <= 0 {
			max = 100
		}
		share := item.Value / max
		if share < 0 {
			share = 0
		}
		if share > 1 {
			share = 1
		}
		color := l.theme.Accent
		if item.Status != "" && item.Status != statusNeutral {
			color = l.theme.StatusColor(item.Status)
		}
		valueText := formatNumber(item.Value) + " / " + formatNumber(max) + unitSuffix(item.Unit)
		valueWidth := measureText(11, false, false, valueText)
		labelWidth := width - valueWidth - 12
		if labelWidth < width*0.3 {
			labelWidth = width * 0.3
		}
		used := l.drawTextBlock(clipLine(item.Label, 11.5, labelWidth), 11.5, l.theme.TextSecondary, false, false, x, y+total, labelWidth)
		l.pushText(valueText, x+width, y+total, 11, false, false, l.theme.TextSecondary, "end")
		trackY := y + total + used + 4
		l.add(Rect{X: x, Y: trackY, W: width, H: 7, Fill: l.theme.Background, Radius: 3.5, Stroke: l.theme.Border, StrokeWidth: 1})
		if share > 0 {
			l.add(Rect{X: x, Y: trackY, W: width * share, H: 7, Fill: color, Radius: 3.5})
		}
		total += used + 4 + 7 + 12
	}
	return total - 12
}

func (l *layout) drawAccordionBlock(c Component, x, y, width float64) float64 {
	var items []AccordionItem
	_ = jsonUnmarshalItems(c.Items, &items)
	if len(items) == 0 {
		return 0
	}
	total := 0.0
	for i, item := range items {
		if i > 0 {
			l.add(Segment{X1: x, Y1: y + total, X2: x + width, Y2: y + total, Color: l.theme.Border, Width: 0.5})
			total += 8
		}
		l.pushText("▾", x, y+total, 12, false, false, l.theme.TextSecondary, "start")
		total += l.drawTextBlock(item.Label, 12.5, l.theme.TextPrimary, true, false, x+16, y+total, width-16) + 2
		total += l.drawTextBlock(item.Content, 11.5, l.theme.TextSecondary, false, false, x+16, y+total, width-16) + 4
	}
	if total > 0 {
		total -= 4
	}
	return total
}

// mustMarshal marshals a layout-internal payload; it cannot fail for plain
// structs and an error would mean a programming bug.
func mustMarshal(v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		return []byte("{}")
	}
	return raw
}
