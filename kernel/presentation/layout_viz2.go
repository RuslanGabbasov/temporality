package presentation

// Extended visualization layout (docs/presentation-engine-ext.md §3-§4):
// scatter/positioning map, heatmap, histogram, radar, sankey, swimlane and
// treemap draw into the SAME primitive ops the chart uses — one draw model,
// both static emitters for free (docs/presentation-engine.md §7.3).

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

func (l *layout) drawScatterBlock(c Component, x, y, width float64) float64 {
	var data ScatterData
	_ = decodeStrict(c.Data, &data)
	var props ScatterProps
	_ = decodeStrict(c.Props, &props)
	frag := layoutScatter(data, props, width, l.theme)
	for _, op := range frag.Ops {
		l.add(translateOp(op, x, y))
	}
	return frag.Height
}

func (l *layout) drawHeatmapBlock(c Component, x, y, width float64) float64 {
	var data HeatmapData
	_ = decodeStrict(c.Data, &data)
	var props HeatmapProps
	_ = decodeStrict(c.Props, &props)
	frag := layoutHeatmap(data, props, width, l.theme)
	for _, op := range frag.Ops {
		l.add(translateOp(op, x, y))
	}
	return frag.Height
}

func (l *layout) drawHistogramBlock(c Component, x, y, width float64) float64 {
	var data HistogramData
	_ = decodeStrict(c.Data, &data)
	var props HistogramProps
	_ = decodeStrict(c.Props, &props)
	frag := layoutHistogram(data, props, width, l.theme)
	for _, op := range frag.Ops {
		l.add(translateOp(op, x, y))
	}
	return frag.Height
}

func (l *layout) drawRadarBlock(c Component, x, y, width float64) float64 {
	var data RadarData
	_ = decodeStrict(c.Data, &data)
	var props RadarProps
	_ = decodeStrict(c.Props, &props)
	frag := layoutRadar(data, props, width, l.theme)
	for _, op := range frag.Ops {
		l.add(translateOp(op, x, y))
	}
	return frag.Height
}

func (l *layout) drawSankeyBlock(c Component, x, y, width float64) float64 {
	var data SankeyData
	_ = decodeStrict(c.Data, &data)
	var props SankeyProps
	_ = decodeStrict(c.Props, &props)
	frag := layoutSankey(data, props, width, l.theme)
	for _, op := range frag.Ops {
		l.add(translateOp(op, x, y))
	}
	return frag.Height
}

func (l *layout) drawSwimlaneBlock(c Component, x, y, width float64) float64 {
	var data SwimlaneData
	_ = decodeStrict(c.Data, &data)
	var props SwimlaneProps
	_ = decodeStrict(c.Props, &props)
	frag := layoutSwimlane(data, props, width, l.theme)
	for _, op := range frag.Ops {
		l.add(translateOp(op, x, y))
	}
	return frag.Height
}

func (l *layout) drawTreemapBlock(c Component, x, y, width float64) float64 {
	var data TreemapData
	_ = decodeStrict(c.Data, &data)
	var props TreemapProps
	_ = decodeStrict(c.Props, &props)
	frag := layoutTreemap(data, props, width, l.theme)
	for _, op := range frag.Ops {
		l.add(translateOp(op, x, y))
	}
	return frag.Height
}

// hexBlend mixes two #rrggbb colors; t = 0 → a, t = 1 → b. The draw model
// carries opaque colors only, so "translucent" effects (sankey ribbons,
// radar fills) pre-blend toward the theme background.
func hexBlend(a, b string, t float64) string {
	parse := func(hex string) (float64, float64, float64) {
		hex = strings.TrimPrefix(hex, "#")
		if len(hex) != 6 {
			return 128, 128, 128
		}
		r, _ := math.Modf(float64(hexPair(hex[0], hex[1])))
		g, _ := math.Modf(float64(hexPair(hex[2], hex[3])))
		b, _ := math.Modf(float64(hexPair(hex[4], hex[5])))
		return r, g, b
	}
	ar, ag, ab := parse(a)
	br, bg, bb := parse(b)
	mix := func(x, y float64) int {
		return int(math.Round(x + (y-x)*t))
	}
	return fmt.Sprintf("#%02x%02x%02x", mix(ar, br), mix(ag, bg), mix(ab, bb))
}

func hexPair(hi, lo byte) int {
	digit := func(c byte) int {
		switch {
		case c >= '0' && c <= '9':
			return int(c - '0')
		case c >= 'a' && c <= 'f':
			return int(c-'a') + 10
		case c >= 'A' && c <= 'f':
			return int(c-'A') + 10
		}
		return 0
	}
	return digit(hi)*16 + digit(lo)
}

// ===== Scatter / positioning map =====

func layoutScatter(data ScatterData, props ScatterProps, width float64, theme Theme) Drawing {
	frag := Drawing{Width: width}
	const (
		titleSize   = 12.5
		axisSize    = 10.5
		labelSize   = 10.0
		marginTop   = 6.0
		marginLeft  = 64.0
		marginRight = 16.0
		marginBot   = 34.0
		plotHeight  = 170.0
	)
	if width < 120 || len(data.Points) == 0 {
		return frag
	}
	plotW := width - marginLeft - marginRight
	if plotW < 40 {
		plotW = 40
	}
	height := 0.0
	if props.Title != "" {
		for _, line := range wrapText(props.Title, titleSize, true, false, width) {
			frag.Ops = append(frag.Ops, Glyph{X: 0, Y: height + titleSize, Size: titleSize, Bold: true, Color: theme.TextPrimary, Anchor: "start", Text: line})
			height += lineHeight(titleSize)
		}
		height += 6
	}
	plotTop := height

	// Domains: explicit ranges win; otherwise the data extent padded 8% so
	// edge points keep their labels inside the plot.
	xMin, xMax := axisDomain(data.Points, func(p ScatterPoint) float64 { return p.X }, data.XMin, data.XMax)
	yMin, yMax := axisDomain(data.Points, func(p ScatterPoint) float64 { return p.Y }, data.YMin, data.YMax)

	// Group palette: groups sort by first appearance for a stable legend.
	groupOrder := []string{}
	groupSeen := map[string]bool{}
	for _, p := range data.Points {
		if p.Group != "" && !groupSeen[p.Group] {
			groupSeen[p.Group] = true
			groupOrder = append(groupOrder, p.Group)
		}
	}
	colors := []string{theme.Accent, theme.Success, theme.Warning, theme.Danger, theme.Info, theme.Neutral}
	groupColor := map[string]string{}
	for i, g := range groupOrder {
		groupColor[g] = colors[i%len(colors)]
	}

	// Axes + grid.
	frag.Ops = append(frag.Ops, Segment{X1: marginLeft, Y1: plotTop, X2: marginLeft, Y2: plotTop + plotHeight, Color: theme.Border, Width: 1})
	frag.Ops = append(frag.Ops, Segment{X1: marginLeft, Y1: plotTop + plotHeight, X2: marginLeft + plotW, Y2: plotTop + plotHeight, Color: theme.Border, Width: 1})
	for i := 0; i <= 4; i++ {
		fraction := float64(i) / 4
		value := yMax - (yMax-yMin)*fraction
		gy := plotTop + plotHeight*fraction
		if i > 0 && i < 4 {
			frag.Ops = append(frag.Ops, Segment{X1: marginLeft, Y1: gy, X2: marginLeft + plotW, Y2: gy, Color: theme.Border, Width: 0.5, Dash: []float64{2, 3}})
		}
		frag.Ops = append(frag.Ops, Glyph{X: marginLeft - 6, Y: gy + axisSize*0.35, Size: axisSize, Color: theme.TextSecondary, Anchor: "end", Text: formatNumber(roundTick(value))})
		fx := marginLeft + plotW*fraction
		value = xMin + (xMax-xMin)*fraction
		if i > 0 && i < 4 {
			frag.Ops = append(frag.Ops, Segment{X1: fx, Y1: plotTop, X2: fx, Y2: plotTop + plotHeight, Color: theme.Border, Width: 0.5, Dash: []float64{2, 3}})
		}
		frag.Ops = append(frag.Ops, Glyph{X: fx, Y: plotTop + plotHeight + 14, Size: axisSize, Color: theme.TextSecondary, Anchor: "middle", Text: formatNumber(roundTick(value))})
	}
	if data.YTitle != "" {
		// Rotated into the left margin: laid out horizontally the caption
		// collided with the tick labels and marginLeft clipped it anyway. The
		// available run is the plot height, not the margin width.
		frag.Ops = append(frag.Ops, Glyph{X: 12, Y: plotTop + plotHeight/2, Size: axisSize, Color: theme.TextSecondary, Anchor: "middle", Rotate: -90, Text: clipLine(data.YTitle, axisSize, plotHeight-12)})
	}
	if data.XTitle != "" {
		frag.Ops = append(frag.Ops, Glyph{X: marginLeft + plotW/2, Y: plotTop + plotHeight + 30, Size: axisSize, Color: theme.TextSecondary, Anchor: "middle", Text: clipLine(data.XTitle, axisSize, plotW)})
	}

	px := func(x float64) float64 { return marginLeft + plotW*(x-xMin)/(xMax-xMin) }
	py := func(y float64) float64 { return plotTop + plotHeight*(yMax-y)/(yMax-yMin) }

	// Reference lines and the quadrant split (§4.1 spatial elements).
	refSegs := func() {
		for _, line := range props.RefLines {
			switch line.Axis {
			case "x":
				if line.At < xMin || line.At > xMax {
					continue
				}
				fx := px(line.At)
				frag.Ops = append(frag.Ops, Segment{X1: fx, Y1: plotTop, X2: fx, Y2: plotTop + plotHeight, Color: theme.Warning, Width: 1.2, Dash: []float64{4, 3}})
				if line.Label != "" {
					frag.Ops = append(frag.Ops, Glyph{X: fx + 3, Y: plotTop + 10, Size: labelSize - 1, Color: theme.TextSecondary, Anchor: "start", Text: clipLine(line.Label, labelSize-1, 120)})
				}
			case "y":
				if line.At < yMin || line.At > yMax {
					continue
				}
				fy := py(line.At)
				frag.Ops = append(frag.Ops, Segment{X1: marginLeft, Y1: fy, X2: marginLeft + plotW, Y2: fy, Color: theme.Warning, Width: 1.2, Dash: []float64{4, 3}})
				if line.Label != "" {
					frag.Ops = append(frag.Ops, Glyph{X: marginLeft + plotW - 3, Y: fy - 4, Size: labelSize - 1, Color: theme.TextSecondary, Anchor: "end", Text: clipLine(line.Label, labelSize-1, 120)})
				}
			}
		}
		if props.Quadrant {
			cx, cy := px(0), py(0)
			if 0 < xMin || 0 > xMax {
				cx = marginLeft + plotW/2
			}
			if 0 < yMin || 0 > yMax {
				cy = plotTop + plotHeight/2
			}
			frag.Ops = append(frag.Ops, Segment{X1: marginLeft, Y1: cy, X2: marginLeft + plotW, Y2: cy, Color: theme.Border, Width: 1.2})
			frag.Ops = append(frag.Ops, Segment{X1: cx, Y1: plotTop, X2: cx, Y2: plotTop + plotHeight, Color: theme.Border, Width: 1.2})
			if len(props.QuadrantLabels) > 0 {
				places := [][2]float64{{marginLeft + 6, plotTop + 14}, {marginLeft + plotW - 6, plotTop + 14}, {marginLeft + 6, plotTop + plotHeight - 6}, {marginLeft + plotW - 6, plotTop + plotHeight - 6}}
				anchors := []string{"start", "end", "start", "end"}
				for i, label := range props.QuadrantLabels {
					if i >= 4 || label == "" {
						continue
					}
					frag.Ops = append(frag.Ops, Glyph{X: places[i][0], Y: places[i][1], Size: labelSize, Bold: true, Color: theme.TextSecondary, Anchor: anchors[i], Text: clipLine(label, labelSize, plotW/2-12)})
				}
			}
		}
	}
	refSegs()

	// Points: highlight scales the marker, labels keep a simple offset layout
	// (§4.1 auto-placement = no overlap effort beyond the offset; the label
	// budget below keeps dense plots readable).
	showValues := true
	if props.ShowValues != nil {
		showValues = *props.ShowValues
	}
	for _, p := range data.Points {
		color := theme.Accent
		if p.Group != "" {
			color = groupColor[p.Group]
		}
		fx, fy := px(p.X), py(p.Y)
		r := 3.0
		if p.Size > 0 {
			r = 2.0 + p.Size*1.2
		}
		if p.Highlight {
			r += 1.5
			frag.Ops = append(frag.Ops, Dot{CX: fx, CY: fy, R: r + 2.5, Color: hexBlend(color, theme.Background, 0.55)})
		}
		frag.Ops = append(frag.Ops, Dot{CX: fx, CY: fy, R: r, Color: color})
		if showValues && p.Label != "" && len(data.Points) <= 40 {
			anchor := "start"
			lx := fx + r + 3
			if fx > marginLeft+plotW*0.72 {
				anchor = "end"
				lx = fx - r - 3
			}
			text := p.Label
			if p.Note != "" && len(data.Points) <= 20 {
				text = p.Label + " — " + p.Note
			}
			frag.Ops = append(frag.Ops, Glyph{X: lx, Y: fy - 4, Size: labelSize - 0.5, Bold: p.Highlight, Color: theme.TextPrimary, Anchor: anchor, Text: clipLine(text, labelSize, 90)})
		}
	}

	// Legend: one chip per group (§4.1 legend of groups).
	showLegend := len(groupOrder) > 1
	if props.ShowLegend != nil {
		showLegend = *props.ShowLegend
	}
	if showLegend {
		legendOffset := 30.0
		if data.XTitle != "" {
			legendOffset = 44
		}
		legendY := plotTop + plotHeight + legendOffset
		lx := 2.0
		for _, g := range groupOrder {
			entryW := 14 + measureText(labelSize, false, false, g) + 14
			if lx+entryW > width {
				break // single row: the text fallback carries the full list
			}
			frag.Ops = append(frag.Ops, Dot{CX: lx + 5, CY: legendY + 8, R: 4.5, Color: groupColor[g]})
			frag.Ops = append(frag.Ops, Glyph{X: lx + 14, Y: legendY + 11.5, Size: labelSize, Color: theme.TextPrimary, Anchor: "start", Text: clipLine(g, labelSize, width-18)})
			lx += entryW
		}
		height = legendY + 20
	} else {
		height = plotTop + plotHeight + marginBot
	}
	if height < plotTop+plotHeight+marginBot {
		height = plotTop + plotHeight + marginBot
	}
	frag.Height = height
	return frag
}

func axisDomain(points []ScatterPoint, get func(ScatterPoint) float64, minPtr, maxPtr *float64) (float64, float64) {
	if minPtr != nil && maxPtr != nil {
		return *minPtr, *maxPtr
	}
	min, max := math.Inf(1), math.Inf(-1)
	for _, p := range points {
		v := get(p)
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
	}
	if math.IsInf(min, 0) || math.IsInf(max, 0) {
		return 0, 1
	}
	if min == max {
		pad := math.Abs(min)*0.1 + 1
		min, max = min-pad, max+pad
	}
	pad := (max - min) * 0.08
	if minPtr == nil {
		min -= pad
	}
	if maxPtr == nil {
		max += pad
	}
	return min, max
}

// ===== Heatmap =====

func layoutHeatmap(data HeatmapData, props HeatmapProps, width float64, theme Theme) Drawing {
	frag := Drawing{Width: width}
	const (
		titleSize = 12.5
		labelSize = 9.5
		rowLabelW = 90.0
		cellH     = 24.0
		marginTop = 22.0
	)
	_ = marginTop
	if width < 120 || len(data.Rows) == 0 || len(data.Cols) == 0 {
		return frag
	}
	height := 0.0
	if props.Title != "" {
		for _, line := range wrapText(props.Title, titleSize, true, false, width) {
			frag.Ops = append(frag.Ops, Glyph{X: 0, Y: height + titleSize, Size: titleSize, Bold: true, Color: theme.TextPrimary, Anchor: "start", Text: line})
			height += lineHeight(titleSize)
		}
		height += 4
	}

	// Scale: explicit bounds win; otherwise the observed cell extent.
	min, max := math.Inf(1), math.Inf(-1)
	for _, row := range data.Values {
		for _, cell := range row {
			if cell == nil {
				continue
			}
			if *cell < min {
				min = *cell
			}
			if *cell > max {
				max = *cell
			}
		}
	}
	if data.Min != nil {
		min = *data.Min
	}
	if data.Max != nil {
		max = *data.Max
	}
	if math.IsInf(min, 0) || math.IsInf(max, 0) || min >= max {
		min, max = 0, 1
	}
	divergent := min < 0

	colW := (width - rowLabelW) / float64(len(data.Cols))
	if colW < 26 {
		colW = 26
	}
	// Column headers (no rotated text op — truncated horizontal labels).
	headerY := height + 14
	for i, col := range data.Cols {
		cx := rowLabelW + colW*(float64(i)+0.5)
		frag.Ops = append(frag.Ops, Glyph{X: cx, Y: headerY, Size: labelSize, Color: theme.TextSecondary, Anchor: "middle", Text: clipLine(col, labelSize, colW-4)})
	}
	height = marginTop
	cellW := colW
	for r, rowLabel := range data.Rows {
		frag.Ops = append(frag.Ops, Glyph{X: rowLabelW - 6, Y: height + cellH/2 + 3, Size: labelSize, Color: theme.TextSecondary, Anchor: "end", Text: clipLine(rowLabel, labelSize, rowLabelW-8)})
		for c := range data.Cols {
			cellRect := Rect{X: rowLabelW + colW*float64(c), Y: height, W: cellW - 2, H: cellH - 2, Radius: 2}
			cell := data.Values[r][c]
			if cell == nil {
				cellRect.Fill = theme.Surface
				frag.Ops = append(frag.Ops, cellRect)
				frag.Ops = append(frag.Ops, Glyph{X: cellRect.X + (cellRect.W)/2, Y: height + cellH/2 + 3, Size: labelSize, Color: theme.TextSecondary, Anchor: "middle", Text: "—"})
				continue
			}
			t := (*cell - min) / (max - min)
			var fill string
			if divergent && *cell < 0 {
				fill = hexBlend(theme.Neutral, theme.Info, clamp01(-t))
			} else {
				fill = hexBlend(theme.Neutral, theme.Accent, clamp01(t))
			}
			cellRect.Fill = fill
			frag.Ops = append(frag.Ops, cellRect)
			if props.ShowValues == nil || *props.ShowValues {
				textColor := theme.TextPrimary
				if clamp01(t) > 0.55 {
					textColor = pickContrastText(theme, fill)
				}
				frag.Ops = append(frag.Ops, Glyph{X: cellRect.X + cellRect.W/2, Y: height + cellH/2 + 3, Size: labelSize, Color: textColor, Anchor: "middle", Text: formatNumber(*cell)})
			}
		}
		height += cellH
	}
	// Scale caption: the numeric extent + unit (§3.2 axis labels/units).
	if data.Unit != "" {
		frag.Ops = append(frag.Ops, Glyph{X: 0, Y: height + 14, Size: labelSize, Color: theme.TextSecondary, Anchor: "start", Text: clipLine(fmt.Sprintf("%s: %s … %s", data.Unit, formatNumber(min), formatNumber(max)), labelSize, width)})
		height += 18
	}
	frag.Height = height
	return frag
}

func clamp01(t float64) float64 {
	if t < 0 {
		return 0
	}
	if t > 1 {
		return 1
	}
	return t
}

// pickContrastText picks dark or light text for a filled cell.
func pickContrastText(theme Theme, fill string) string {
	// Luma of the blended fill decides: dark fills get light text.
	_, g, b := 0.0, 0.0, 0.0
	hex := strings.TrimPrefix(fill, "#")
	if len(hex) == 6 {
		r := float64(hexPair(hex[0], hex[1]))
		g = float64(hexPair(hex[2], hex[3]))
		b = float64(hexPair(hex[4], hex[5]))
		if 0.299*r+0.587*g+0.114*b < 140 {
			return "#ffffff"
		}
	}
	return theme.TextPrimary
}

// ===== Histogram =====

func layoutHistogram(data HistogramData, props HistogramProps, width float64, theme Theme) Drawing {
	frag := Drawing{Width: width}
	const (
		titleSize   = 12.5
		axisSize    = 10.5
		labelSize   = 10.0
		marginTop   = 6.0
		marginLeft  = 46.0
		marginRight = 16.0
		plotHeight  = 170.0
	)
	_ = marginRight
	if width < 120 || len(data.Values) == 0 {
		return frag
	}
	height := 0.0
	if props.Title != "" {
		for _, line := range wrapText(props.Title, titleSize, true, false, width) {
			frag.Ops = append(frag.Ops, Glyph{X: 0, Y: height + titleSize, Size: titleSize, Bold: true, Color: theme.TextPrimary, Anchor: "start", Text: line})
			height += lineHeight(titleSize)
		}
		height += 6
	}
	plotW := width - marginLeft - marginRight
	if plotW < 40 {
		plotW = 40
	}
	plotTop := height

	min, max := math.Inf(1), math.Inf(-1)
	for _, v := range data.Values {
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
	}
	if min == max {
		pad := math.Abs(min)*0.1 + 1
		min, max = min-pad, max+pad
	}
	bins := data.Bins
	switch {
	case data.BinWidth > 0:
		span := max - min
		bins = int(math.Ceil(span / data.BinWidth))
		if bins < 1 {
			bins = 1
		}
		if bins > histogramBinLimit {
			bins = histogramBinLimit
		}
	case bins <= 0:
		// Sturges' rule with a small-sample floor.
		bins = int(math.Ceil(math.Log2(float64(len(data.Values))))) + 1
		if bins < 4 {
			bins = 4
		}
		if bins > histogramBinLimit {
			bins = histogramBinLimit
		}
	}
	binWidth := (max - min) / float64(bins)
	counts := make([]int, bins)
	for _, v := range data.Values {
		idx := int((v - min) / binWidth)
		if idx < 0 {
			idx = 0
		}
		if idx >= bins {
			idx = bins - 1
		}
		counts[idx]++
	}
	maxCount := 0
	for _, n := range counts {
		if n > maxCount {
			maxCount = n
		}
	}
	total := float64(len(data.Values))

	slotW := plotW / float64(bins)
	axisY := plotTop + plotHeight
	frag.Ops = append(frag.Ops, Segment{X1: marginLeft, Y1: plotTop, X2: marginLeft, Y2: axisY, Color: theme.Border, Width: 1})
	frag.Ops = append(frag.Ops, Segment{X1: marginLeft, Y1: axisY, X2: marginLeft + plotW, Y2: axisY, Color: theme.Border, Width: 1})
	peak := float64(maxCount)
	if props.Normalized {
		peak = total
	}
	for i := 0; i <= 4; i++ {
		fraction := float64(i) / 4
		value := peak * (1 - fraction)
		gy := plotTop + plotHeight*fraction
		if i > 0 && i < 4 {
			frag.Ops = append(frag.Ops, Segment{X1: marginLeft, Y1: gy, X2: marginLeft + plotW, Y2: gy, Color: theme.Border, Width: 0.5, Dash: []float64{2, 3}})
		}
		frag.Ops = append(frag.Ops, Glyph{X: marginLeft - 6, Y: gy + axisSize*0.35, Size: axisSize, Color: theme.TextSecondary, Anchor: "end", Text: formatNumber(roundTick(value))})
	}
	barColor := theme.Accent
	for i, count := range counts {
		h := plotHeight * float64(count) / peak
		bx := marginLeft + slotW*float64(i)
		if h > 0 {
			frag.Ops = append(frag.Ops, Rect{X: bx + 1, Y: axisY - h, W: slotW - 2, H: h, Fill: barColor})
		}
		showValues := props.ShowValues == nil || *props.ShowValues
		if showValues && slotW >= 26 && count > 0 {
			label := fmt.Sprintf("%d", count)
			if props.Normalized {
				label = formatPercent(float64(count) / total * 100)
			}
			frag.Ops = append(frag.Ops, Glyph{X: bx + slotW/2, Y: axisY - h - 4, Size: labelSize - 0.5, Color: theme.TextSecondary, Anchor: "middle", Text: label})
		}
		// Bin edges: first, last, and ~4 in between carry numeric labels.
		if i == 0 || i == bins-1 || (bins <= 7 && i < bins-1) || i%(bins/4+1) == 0 {
			frag.Ops = append(frag.Ops, Glyph{X: bx, Y: axisY + 14, Size: labelSize - 0.5, Color: theme.TextSecondary, Anchor: "middle", Text: formatNumber(roundTick(min + binWidth*float64(i)))})
		}
	}
	frag.Ops = append(frag.Ops, Glyph{X: marginLeft + plotW, Y: axisY + 14, Size: labelSize - 0.5, Color: theme.TextSecondary, Anchor: "middle", Text: formatNumber(roundTick(max))})
	if data.XTitle != "" {
		frag.Ops = append(frag.Ops, Glyph{X: marginLeft + plotW/2, Y: axisY + 30, Size: axisSize, Color: theme.TextSecondary, Anchor: "middle", Text: clipLine(data.XTitle, axisSize, plotW)})
	}
	if data.YTitle != "" {
		frag.Ops = append(frag.Ops, Glyph{X: 0, Y: plotTop + 10, Size: axisSize, Color: theme.TextSecondary, Anchor: "start", Text: clipLine(data.YTitle, axisSize, marginLeft)})
	}
	height = axisY + 44
	frag.Height = height
	return frag
}

// ===== Radar =====

func layoutRadar(data RadarData, props RadarProps, width float64, theme Theme) Drawing {
	frag := Drawing{Width: width}
	const (
		titleSize = 12.5
		labelSize = 10.0
		valueSize = 9.0
		radius    = 78.0
	)
	_ = titleSize
	if width < 140 || len(data.Axes) < 3 || len(data.Series) == 0 {
		return frag
	}
	height := 0.0
	if props.Title != "" {
		for _, line := range wrapText(props.Title, titleSize, true, false, width) {
			frag.Ops = append(frag.Ops, Glyph{X: 0, Y: height + titleSize, Size: titleSize, Bold: true, Color: theme.TextPrimary, Anchor: "start", Text: line})
			height += lineHeight(titleSize)
		}
		height += 4
	}
	cx := width / 2
	cy := height + radius + 18
	n := len(data.Axes)
	min := 0.0
	if data.Min != nil {
		min = *data.Min
	}
	// Web parity: default scale tops out at the largest value across series.
	max := 0.0
	for _, series := range data.Series {
		for _, v := range series.Values {
			if v > max {
				max = v
			}
		}
	}
	if data.Max != nil {
		max = *data.Max
	}
	if max <= min {
		max = min + 1
	}
	point := func(axis int, value float64) Point {
		angle := -math.Pi/2 + 2*math.Pi*float64(axis)/float64(n)
		t := clamp01((value - min) / (max - min))
		return Point{X: cx + math.Cos(angle)*radius*t, Y: cy + math.Sin(angle)*radius*t}
	}
	// Web: rings at 25/50/75/100% + spokes.
	for ring := 1; ring <= 4; ring++ {
		pts := make([]Point, 0, n)
		for a := 0; a < n; a++ {
			angle := -math.Pi/2 + 2*math.Pi*float64(a)/float64(n)
			pts = append(pts, Point{X: cx + math.Cos(angle)*radius*float64(ring)/4, Y: cy + math.Sin(angle)*radius*float64(ring)/4})
		}
		frag.Ops = append(frag.Ops, Poly{Points: pts, Color: theme.Border, Width: 0.6})
	}
	for a := 0; a < n; a++ {
		edge := point(a, max)
		frag.Ops = append(frag.Ops, Segment{X1: cx, Y1: cy, X2: edge.X, Y2: edge.Y, Color: theme.Border, Width: 0.6})
	}
	// Axis labels sit outside each spoke.
	for a, axis := range data.Axes {
		angle := -math.Pi/2 + 2*math.Pi*float64(a)/float64(n)
		lx := cx + math.Cos(angle)*(radius+12)
		ly := cy + math.Sin(angle)*(radius+12)
		anchor := "middle"
		if math.Cos(angle) > 0.3 {
			anchor = "start"
		} else if math.Cos(angle) < -0.3 {
			anchor = "end"
		}
		frag.Ops = append(frag.Ops, Glyph{X: lx, Y: ly + labelSize*0.35, Size: labelSize, Color: theme.TextSecondary, Anchor: anchor, Text: clipLine(axis, labelSize, 80)})
	}
	colors := []string{theme.Accent, theme.Success, theme.Warning, theme.Danger, theme.Info, theme.Neutral}
	showValues := props.ShowValues != nil && *props.ShowValues
	for si, series := range data.Series {
		color := colors[(si+1)%len(colors)]
		emphasis := props.Highlight == series.Name
		if emphasis {
			color = theme.Accent
		}
		pts := make([]Point, 0, len(series.Values))
		for a, value := range series.Values {
			pts = append(pts, point(a, value))
		}
		fillT := 0.18
		strokeW := 1.6
		dotR := 2.0
		if emphasis {
			fillT, strokeW, dotR = 0.38, 2.4, 2.6
		}
		frag.Ops = append(frag.Ops, Poly{Points: pts, Fill: hexBlend(color, theme.Background, fillT)})
		frag.Ops = append(frag.Ops, Poly{Points: pts, Color: color, Width: strokeW})
		for a, p := range pts {
			frag.Ops = append(frag.Ops, Dot{CX: p.X, CY: p.Y, R: dotR, Color: color})
			if showValues {
				frag.Ops = append(frag.Ops, Glyph{X: p.X, Y: p.Y - 5, Size: valueSize, Color: theme.TextSecondary, Anchor: "middle", Text: formatNumber(series.Values[a])})
			}
		}
	}
	height = cy + radius + 30
	// Legend row.
	showLegend := len(data.Series) > 1
	if props.ShowLegend != nil {
		showLegend = *props.ShowLegend
	}
	if showLegend {
		lx := 2.0
		legendY := height
		for si, series := range data.Series {
			color := colors[(si+1)%len(colors)]
			if props.Highlight == series.Name {
				color = theme.Accent
			}
			entryW := 14 + measureText(labelSize, false, false, series.Name) + 14
			if lx+entryW > width {
				break
			}
			frag.Ops = append(frag.Ops, Dot{CX: lx + 5, CY: legendY + 8, R: 4.5, Color: color})
			frag.Ops = append(frag.Ops, Glyph{X: lx + 14, Y: legendY + 11.5, Size: labelSize, Bold: props.Highlight == series.Name, Color: theme.TextPrimary, Anchor: "start", Text: clipLine(series.Name, labelSize, width-18)})
			lx += entryW
		}
		height = legendY + 20
	}
	frag.Height = height
	return frag
}

// ===== Sankey =====

type sankeyNodeLayout struct {
	node        SankeyNode
	column      int
	x, y        float64
	w, h        float64
	inSum       float64
	outSum      float64
	color       string
	orderWeight float64
}

func layoutSankey(data SankeyData, props SankeyProps, width float64, theme Theme) Drawing {
	frag := Drawing{Width: width}
	const (
		titleSize = 12.5
		labelSize = 10.0
		nodeW     = 12.0
		marginX   = 8.0
		rowGap    = 6.0
	)
	if width < 160 || len(data.Nodes) == 0 || len(data.Links) == 0 {
		return frag
	}
	height := 0.0
	if props.Title != "" {
		for _, line := range wrapText(props.Title, titleSize, true, false, width) {
			frag.Ops = append(frag.Ops, Glyph{X: 0, Y: height + titleSize, Size: titleSize, Bold: true, Color: theme.TextPrimary, Anchor: "start", Text: line})
			height += lineHeight(titleSize)
		}
		height += 6
	}
	plotTop := height

	ids := make(map[string]*sankeyNodeLayout, len(data.Nodes))
	colors := []string{theme.Accent, theme.Success, theme.Warning, theme.Danger, theme.Info, theme.Neutral}
	for i := range data.Nodes {
		ids[data.Nodes[i].ID] = &sankeyNodeLayout{node: data.Nodes[i], color: colors[i%len(colors)]}
	}
	inSum := map[string]float64{}
	outSum := map[string]float64{}
	for _, link := range data.Links {
		outSum[link.Source] += link.Value
		inSum[link.Target] += link.Value
	}
	// Column = longest path from a source (a node with no inbound flow).
	var columnOf func(id string, seen map[string]bool) int
	columnOf = func(id string, seen map[string]bool) int {
		if seen == nil {
			seen = map[string]bool{}
		}
		if seen[id] {
			return 0
		}
		seen[id] = true
		best := 0
		for _, link := range data.Links {
			if link.Target != id {
				continue
			}
			if col := columnOf(link.Source, seen) + 1; col > best {
				best = col
			}
		}
		return best
	}
	maxColumn := 0
	for _, node := range data.Nodes {
		nl := ids[node.ID]
		nl.column = columnOf(node.ID, nil)
		nl.inSum, nl.outSum = inSum[node.ID], outSum[node.ID]
		if nl.column > maxColumn {
			maxColumn = nl.column
		}
	}
	columns := make([][]*sankeyNodeLayout, maxColumn+1)
	for _, node := range data.Nodes {
		nl := ids[node.ID]
		columns[nl.column] = append(columns[nl.column], nl)
	}
	// Node height scale: the tallest volume column fills the plot.
	plotHeight := 240.0
	maxColVolume := 0.0
	for _, col := range columns {
		volume := 0.0
		for _, nl := range col {
			v := math.Max(nl.inSum, nl.outSum)
			if v == 0 {
				v = 0.001 // isolated node stays visible
			}
			volume += v
		}
		gaps := rowGap * float64(len(col)+1)
		if volume > 0 && (plotHeight-gaps)/volume < maxColVolume || maxColVolume == 0 {
			if volume > 0 {
				maxColVolume = (plotHeight - gaps) / volume
			}
		}
	}
	colGap := (width - marginX*2 - nodeW) / float64(maxColumn+1)
	if colGap > 150 {
		colGap = 150
	}
	// Deterministic order for nodes without inbound references: the original
	// declaration index.
	declIndex := map[string]float64{}
	for i, node := range data.Nodes {
		declIndex[node.ID] = float64(i)
	}
	totalHeight := 0.0
	for ci, col := range columns {
		// Order nodes within a column by their weighted affinity to reduce
		// crossings: sort by the mean position of their sources, falling back
		// to the declaration order.
		sort.Slice(col, func(i, j int) bool {
			mean := func(nl *sankeyNodeLayout) float64 {
				sum, weight := 0.0, 0.0
				for _, link := range data.Links {
					if link.Target == nl.node.ID {
						if src := ids[link.Source]; src != nil && src.column == nl.column-1 {
							sum += src.orderWeight
							weight += link.Value
						}
					}
				}
				if weight == 0 {
					return declIndex[nl.node.ID]
				}
				return sum / weight
			}
			return mean(col[i]) < mean(col[j])
		})
		y := plotTop + rowGap
		for _, nl := range col {
			v := math.Max(nl.inSum, nl.outSum)
			if v == 0 {
				v = 0.001
			}
			nl.x = marginX + float64(ci)*colGap
			nl.y = y
			nl.w = nodeW
			nl.h = math.Max(6, v*maxColVolume)
			y += nl.h + rowGap
			nl.orderWeight = float64(ci)
		}
		if y > totalHeight {
			totalHeight = y
		}
	}
	// Draw ribbons first (under the nodes), then nodes, then labels.
	ribbonOrder := append([]SankeyLink{}, data.Links...)
	sort.Slice(ribbonOrder, func(i, j int) bool { return ribbonOrder[i].Value > ribbonOrder[j].Value })
	for _, link := range ribbonOrder {
		src, dst := ids[link.Source], ids[link.Target]
		if src == nil || dst == nil {
			continue
		}
		srcOutTotal := math.Max(src.outSum, 0.001)
		dstInTotal := math.Max(dst.inSum, 0.001)
		// Offset = cumulative value of the links drawn into the same node
		// slot; deterministic by link order.
		srcOff, dstOff := 0.0, 0.0
		for _, other := range data.Links {
			if other.Source == link.Source && other.Value < link.Value {
				srcOff += other.Value
			}
			if other.Target == link.Target && other.Value < link.Value {
				dstOff += other.Value
			}
		}
		x0 := src.x + nodeW
		x1 := dst.x
		y0 := src.y + srcOff/srcOutTotal*src.h
		y1 := dst.y + dstOff/dstInTotal*dst.h
		t0 := link.Value / srcOutTotal * src.h
		t1 := link.Value / dstInTotal * dst.h
		fill := hexBlend(src.color, theme.Background, 0.45)
		// Ribbon as two mirrored S-curves, approximated with polygons.
		const steps = 10
		top := make([]Point, 0, steps+1)
		bottom := make([]Point, 0, steps+1)
		for s := 0; s <= steps; s++ {
			t := float64(s) / float64(steps)
			mx := x0 + (x1-x0)*t
			// cubic-ish ease for the S shape
			ease := t * t * (3 - 2*t)
			top = append(top, Point{X: mx, Y: y0 + (y1-y0)*ease})
			bottom = append(bottom, Point{X: mx, Y: y0 + t0 + (y1+t1-(y0+t0))*ease})
		}
		ribbon := append(append([]Point{}, top...), reversePoints(bottom)...)
		frag.Ops = append(frag.Ops, Poly{Points: ribbon, Fill: fill})
	}
	for _, col := range columns {
		for _, nl := range col {
			frag.Ops = append(frag.Ops, Rect{X: nl.x, Y: nl.y, W: nl.w, H: nl.h, Fill: nl.color, Radius: 2})
			label := labelOr(nl.node.Label, nl.node.ID)
			frag.Ops = append(frag.Ops, Glyph{X: nl.x - 4, Y: nl.y + nl.h/2 + 3, Size: labelSize, Color: theme.TextPrimary, Anchor: "end", Text: clipLine(label, labelSize, colGap-20)})
			showValues := props.ShowValues == nil || *props.ShowValues
			if showValues {
				volume := math.Max(math.Max(nl.inSum, nl.outSum), 0)
				text := formatNumber(roundTick(volume))
				if data.Unit != "" {
					text += unitSuffixText(data.Unit)
				}
				frag.Ops = append(frag.Ops, Glyph{X: nl.x + nl.w + 4, Y: nl.y + nl.h/2 + 3, Size: labelSize - 1, Color: theme.TextSecondary, Anchor: "start", Text: clipLine(text, labelSize, colGap-20)})
			}
		}
	}
	height = totalHeight + 10
	if height < plotTop+120 {
		height = plotTop + 120
	}
	frag.Height = height
	return frag
}

func reversePoints(points []Point) []Point {
	out := make([]Point, len(points))
	for i, p := range points {
		out[len(points)-1-i] = p
	}
	return out
}

func unitSuffixText(unit string) string {
	if unit == "" {
		return ""
	}
	return " " + unit
}

// ===== Swimlane =====

func layoutSwimlane(data SwimlaneData, props SwimlaneProps, width float64, theme Theme) Drawing {
	frag := Drawing{Width: width}
	const (
		titleSize  = 12.5
		labelSize  = 9.5
		laneLabelW = 86.0
		laneH      = 64.0
		marginX    = 8.0
	)
	if width < 200 || len(data.Lanes) == 0 || len(data.Nodes) == 0 {
		return frag
	}
	if width < 200 || len(data.Lanes) == 0 || len(data.Nodes) == 0 {
		return frag
	}
	height := 0.0
	if props.Title != "" {
		for _, line := range wrapText(props.Title, titleSize, true, false, width) {
			frag.Ops = append(frag.Ops, Glyph{X: 0, Y: height + titleSize, Size: titleSize, Bold: true, Color: theme.TextPrimary, Anchor: "start", Text: line})
			height += lineHeight(titleSize)
		}
		height += 6
	}
	// Column count: the max stage reached, or appearance order.
	nodeByID := map[string]*SwimlaneNode{}
	order := map[string]int{}
	for i := range data.Nodes {
		nodeByID[data.Nodes[i].ID] = &data.Nodes[i]
		order[data.Nodes[i].ID] = i
	}
	maxStage := 0
	for _, node := range data.Nodes {
		if node.Start > maxStage {
			maxStage = node.Start
		}
	}
	if maxStage == 0 {
		// Automatic placement: order by first appearance, packing greedy
		// per-lane rows so overlaps stay impossible.
		laneCursor := map[string]int{}
		for _, node := range data.Nodes {
			node.Start = laneCursor[node.Lane]
			laneCursor[node.Lane]++
		}
		for _, node := range data.Nodes {
			if node.Start > maxStage {
				maxStage = node.Start
			}
		}
	}
	cols := maxStage + 1
	// Node spans may extend past the computed stage count.
	for _, node := range data.Nodes {
		if node.Start+node.Span > cols {
			cols = node.Start + node.Span
		}
	}
	colW := (width - laneLabelW - marginX*2) / float64(cols)
	if colW < 64 {
		colW = 64
	}
	plotTop := height
	// Lane bands.
	for li, lane := range data.Lanes {
		by := plotTop + laneH*float64(li)
		fill := theme.Surface
		if li%2 == 1 {
			fill = hexBlend(theme.Surface, theme.Background, 0.5)
		}
		frag.Ops = append(frag.Ops, Rect{X: 0, Y: by, W: width, H: laneH, Fill: fill})
		frag.Ops = append(frag.Ops, Glyph{X: 8, Y: by + laneH/2 + 4, Size: labelSize + 0.5, Bold: true, Color: theme.TextSecondary, Anchor: "start", Text: clipLine(labelOr(lane.Label, lane.ID), labelSize, laneLabelW-12)})
	}
	// Nodes.
	statusColor := func(status string) string {
		switch status {
		case "success", "done", "completed":
			return theme.Success
		case "danger", "failed", "blocked":
			return theme.Danger
		case "warning", "pending", "waiting":
			return theme.Warning
		default:
			return theme.Accent
		}
	}
	rectByNode := map[string]Rect{}
	for _, node := range data.Nodes {
		li := 0
		for li < len(data.Lanes) && data.Lanes[li].ID != node.Lane {
			li++
		}
		span := node.Span
		if span < 1 {
			span = 1
		}
		nx := laneLabelW + marginX + colW*float64(node.Start) + 9
		ny := plotTop + laneH*float64(li) + 10
		nw := colW*float64(span) - 18
		nh := laneH - 20
		rect := Rect{X: nx, Y: ny, W: nw, H: nh, Radius: 5, Fill: theme.Surface, Stroke: statusColor(node.Status), StrokeWidth: 1.6}
		frag.Ops = append(frag.Ops, rect)
		rectByNode[node.ID] = rect
		// Label wrapped to two lines, note as the third.
		lines := wrapText(node.Label, labelSize, false, false, nw-10)
		if len(lines) > 2 {
			lines = append(lines[:2], "…")
		}
		for li2, line := range lines {
			frag.Ops = append(frag.Ops, Glyph{X: nx + 5, Y: ny + 12 + float64(li2)*12, Size: labelSize, Color: theme.TextPrimary, Anchor: "start", Text: line})
		}
		if node.Note != "" {
			frag.Ops = append(frag.Ops, Glyph{X: nx + 5, Y: ny + nh - 5, Size: labelSize - 1, Color: theme.TextSecondary, Anchor: "start", Text: clipLine(node.Note, labelSize-1, nw-10)})
		}
	}
	// Edges: orthogonal swimlane routing with obstacle checks (mirror of the
	// frontend router). The old fixed midpoint elbow cut straight through
	// boxes whenever the target sat in the same column or left of the source.
	type edgeMeta struct {
		rect         Rect
		lane, c0, c1 int
	}
	meta := make(map[string]edgeMeta, len(rectByNode))
	for id, r := range rectByNode {
		m := edgeMeta{rect: r, lane: 0, c0: 0, c1: 0}
		for _, node := range data.Nodes {
			if node.ID != id {
				continue
			}
			li := 0
			for li < len(data.Lanes) && data.Lanes[li].ID != node.Lane {
				li++
			}
			span := node.Span
			if span < 1 {
				span = 1
			}
			m.lane, m.c0, m.c1 = li, node.Start, node.Start+span-1
			break
		}
		meta[id] = m
	}
	boundary := func(k int) float64 { return laneLabelW + marginX + colW*float64(k) }
	pathClear := func(pts []Point, a, b string) bool {
		for i := 1; i < len(pts); i++ {
			minX, maxX := math.Min(pts[i-1].X, pts[i].X), math.Max(pts[i-1].X, pts[i].X)
			minY, maxY := math.Min(pts[i-1].Y, pts[i].Y), math.Max(pts[i-1].Y, pts[i].Y)
			for id, m := range meta {
				if id == a || id == b {
					continue
				}
				r := m.rect
				if maxX > r.X+1 && minX < r.X+r.W-1 && maxY > r.Y+1 && minY < r.Y+r.H-1 {
					return false
				}
			}
		}
		return true
	}
	for _, edge := range data.Edges {
		from, okF := rectByNode[edge.From]
		to, okT := rectByNode[edge.To]
		if !okF || !okT {
			continue
		}
		mf, mt := meta[edge.From], meta[edge.To]
		aCy := from.Y + from.H/2
		bCy := to.Y + to.H/2
		aCx := from.X + from.W/2
		bCx := to.X + to.W/2
		var cands [][]Point
		push := func(p []Point) {
			if len(p) > 1 {
				cands = append(cands, p)
			}
		}
		if mf.lane == mt.lane {
			if to.X >= from.X+from.W {
				push([]Point{{from.X + from.W, aCy}, {to.X, bCy}})
			} else if to.X+to.W <= from.X {
				push([]Point{{from.X, aCy}, {to.X + to.W, bCy}})
			}
			stripY := plotTop + laneH*float64(mf.lane) + 4
			push([]Point{{aCx, from.Y}, {aCx, stripY}, {bCx, stripY}, {bCx, to.Y}})
		} else {
			oL := math.Max(from.X, to.X)
			oR := math.Min(from.X+from.W, to.X+to.W)
			if oL < oR {
				cx := (oL + oR) / 2
				if to.Y > from.Y {
					push([]Point{{cx, from.Y + from.H}, {cx, to.Y}})
				} else {
					push([]Point{{cx, from.Y}, {cx, to.Y + to.H}})
				}
			}
			gapY := (math.Max(from.Y+from.H, to.Y) + math.Min(from.Y+from.H, to.Y)) / 2
			if to.Y > from.Y {
				gapY = (from.Y + from.H + to.Y) / 2
				push([]Point{{aCx, from.Y + from.H}, {aCx, gapY}, {bCx, gapY}, {bCx, to.Y}})
			} else {
				gapY = (to.Y + to.H + from.Y) / 2
				push([]Point{{aCx, from.Y}, {aCx, gapY}, {bCx, gapY}, {bCx, to.Y + to.H}})
			}
			if mf.c1+1 <= mt.c0 {
				for k := mf.c1 + 1; k <= mt.c0; k++ {
					ch := boundary(k)
					push([]Point{{from.X + from.W, aCy}, {ch, aCy}, {ch, bCy}, {to.X, bCy}})
				}
			}
			if mt.c1+1 <= mf.c0 {
				for k := mt.c1 + 1; k <= mf.c0; k++ {
					ch := boundary(k)
					push([]Point{{from.X, aCy}, {ch, aCy}, {ch, bCy}, {to.X + to.W, bCy}})
				}
			}
			// Corridor route: horizontal runs live in the band BETWEEN lanes
			// (always free of boxes), only the vertical crosses lanes — at a
			// column boundary channel (checked against spanning nodes below).
			bandBelow := func(l int) float64 { return plotTop + laneH*float64(l+1) }
			bandAbove := func(l int) float64 { return plotTop + laneH*float64(l) }
			if to.Y > from.Y {
				y1, y2 := bandBelow(mf.lane), bandAbove(mt.lane)
				for k := minInt(mf.c0, mt.c0); k <= maxInt(mf.c1, mt.c1)+1; k++ {
					ch := boundary(k)
					push([]Point{{aCx, from.Y + from.H}, {aCx, y1}, {ch, y1}, {ch, y2}, {bCx, y2}, {bCx, to.Y}})
				}
			} else {
				y1, y2 := bandAbove(mf.lane), bandBelow(mt.lane)
				for k := minInt(mf.c0, mt.c0); k <= maxInt(mf.c1, mt.c1)+1; k++ {
					ch := boundary(k)
					push([]Point{{aCx, from.Y}, {aCx, y1}, {ch, y1}, {ch, y2}, {bCx, y2}, {bCx, to.Y + to.H}})
				}
			}
		}
		var path []Point
		for _, c := range cands {
			if pathClear(c, edge.From, edge.To) {
				path = c
				break
			}
		}
		if path == nil && len(cands) > 0 {
			path = cands[0]
		}
		if len(path) < 2 {
			continue
		}
		color := theme.TextSecondary
		var dash []float64
		if edge.Dashed {
			dash = []float64{3, 3}
		}
		for i := 1; i < len(path); i++ {
			seg := Segment{X1: path[i-1].X, Y1: path[i-1].Y, X2: path[i].X, Y2: path[i].Y, Color: color, Width: 1.2}
			if dash != nil {
				seg.Dash = dash
			}
			frag.Ops = append(frag.Ops, seg)
		}
		// Arrow direction follows the LAST segment, not just "y changed".
		last, prev := path[len(path)-1], path[len(path)-2]
		dx, dy := last.X-prev.X, last.Y-prev.Y
		angle := 0.0
		switch {
		case math.Abs(dx) >= math.Abs(dy):
			if dx < 0 {
				angle = math.Pi
			}
		case dy < 0:
			angle = -math.Pi / 2
		default:
			angle = math.Pi / 2
		}
		frag.Ops = append(frag.Ops, Arrow{X: last.X, Y: last.Y, Size: 5, Angle: angle, Color: color})
		label := edge.Label
		if label == "" {
			label = edge.Condition
		}
		if label != "" {
			lx, ly := swimlaneLabelPos(path)
			frag.Ops = append(frag.Ops, Glyph{X: lx, Y: ly, Size: labelSize - 1, Color: theme.TextSecondary, Anchor: "start", Text: clipLine(label, labelSize-1, 90)})
		}
	}
	height = plotTop + laneH*float64(len(data.Lanes)) + 10
	frag.Height = height
	return frag
}

// swimlaneLabelPos places an edge label beside the longest path segment —
// usually the vertical channel, which runs between boxes — never across the
// whole route (the old midpoint placement landed on top of nodes).
func swimlaneLabelPos(path []Point) (float64, float64) {
	bi, bl := 1, -1.0
	for i := 1; i < len(path); i++ {
		l := math.Abs(path[i].X-path[i-1].X) + math.Abs(path[i].Y-path[i-1].Y)
		if l > bl {
			bl, bi = l, i
		}
	}
	x1, y1 := path[bi-1].X, path[bi-1].Y
	x2, y2 := path[bi].X, path[bi].Y
	if math.Abs(y2-y1) > math.Abs(x2-x1) {
		return (x1+x2)/2 + 4, (y1 + y2) / 2
	}
	return (x1 + x2) / 2, (y1+y2)/2 - 4
}

// ===== Treemap =====

func layoutTreemap(data TreemapData, props TreemapProps, width float64, theme Theme) Drawing {
	frag := Drawing{Width: width}
	const titleSize = 12.5
	if width < 140 {
		return frag
	}
	height := 0.0
	if props.Title != "" {
		for _, line := range wrapText(props.Title, titleSize, true, false, width) {
			frag.Ops = append(frag.Ops, Glyph{X: 0, Y: height + titleSize, Size: titleSize, Bold: true, Color: theme.TextPrimary, Anchor: "start", Text: line})
			height += lineHeight(titleSize)
		}
		height += 6
	}
	maxDepth := props.MaxDepth
	if maxDepth <= 0 {
		maxDepth = 2
	}
	plotH := width * 0.62
	if plotH > 340 {
		plotH = 340
	}
	// Normalize the top level against the children sum (a parent's explicit
	// value is display-only, §4.5).
	children := data.Root.Children
	total := 0.0
	for _, child := range children {
		total += subtreeValue(child)
	}
	if total <= 0 || len(children) == 0 {
		// A lone leaf still renders as one full rectangle.
		if data.Root.Value > 0 {
			rect := Rect{X: 0, Y: height, W: width, H: plotH, Radius: 4, Fill: hexBlend(theme.Accent, theme.Background, 0.25)}
			frag.Ops = append(frag.Ops, rect)
			drawTreemapLabels(&frag, rect, data.Root, data.Unit, props.ShowValues == nil || *props.ShowValues, theme)
			frag.Height = height + plotH
		}
		return frag
	}
	sort.Slice(children, func(i, j int) bool { return subtreeValue(children[i]) > subtreeValue(children[j]) })
	squarifyInto(&frag, children, total, Rect{X: 0, Y: height, W: width, H: plotH}, 1, maxDepth, data.Unit, props.ShowValues == nil || *props.ShowValues, theme)
	frag.Height = height + plotH
	return frag
}

func subtreeValue(node TreemapNode) float64 {
	if len(node.Children) == 0 {
		return node.Value
	}
	sum := 0.0
	for _, child := range node.Children {
		sum += subtreeValue(child)
	}
	return sum
}

// rowWorstRatio is the squarified criterion: the worst (largest) aspect
// ratio any node of the candidate row would get if the row took rowArea of
// the side `side` long.
func rowWorstRatio(row []TreemapNode, rowValue, rowArea, side float64) float64 {
	if rowValue <= 0 || rowArea <= 0 {
		return math.MaxFloat64
	}
	worst := 0.0
	rowLen := rowArea / side
	for _, node := range row {
		thickness := subtreeValue(node) / rowValue * rowLen
		if thickness <= 0 {
			return math.MaxFloat64
		}
		ratio := math.Max(rowLen/thickness, thickness/rowLen)
		if ratio > worst {
			worst = ratio
		}
	}
	return worst
}

// squarifyInto lays one node list into rect (classic squarified treemap,
// Bruls et al. — worst-ratio row packing), then recurses one level deeper.
func squarifyInto(frag *Drawing, nodes []TreemapNode, total float64, rect Rect, depth, maxDepth int, unit string, showValues bool, theme Theme) {
	if len(nodes) == 0 || total <= 0 || rect.W < 8 || rect.H < 8 {
		return
	}
	rowColor := func(value float64) string {
		t := clamp01(value / math.Max(total, 0.001))
		return hexBlend(theme.Neutral, theme.Accent, 0.15+0.6*t)
	}
	remaining := append([]TreemapNode{}, nodes...)
	areaLeft := rect.W * rect.H
	for len(remaining) > 0 {
		// Grow the row while the worst aspect ratio improves.
		row := []TreemapNode{remaining[0]}
		remaining = remaining[1:]
		rowValue := subtreeValue(row[0])
		rowArea := rowValue / total * areaLeft
		worst := rowWorstRatio(row, rowValue, rowArea, math.Min(rect.W, rect.H))
		for len(remaining) > 0 {
			candidate := remaining[0]
			newValue := rowValue + subtreeValue(candidate)
			newArea := newValue / total * areaLeft
			newWorst := rowWorstRatio(append(row, candidate), newValue, newArea, math.Min(rect.W, rect.H))
			if newWorst <= worst {
				row = append(row, candidate)
				remaining = remaining[1:]
				rowValue, rowArea, worst = newValue, newArea, newWorst
				continue
			}
			break
		}
		// Lay the row along the shorter side.
		var rowRect Rect
		if rect.W >= rect.H {
			rowW := rowArea / rect.H
			rowRect = Rect{X: rect.X, Y: rect.Y, W: rowW, H: rect.H}
			rect.X += rowW
			rect.W -= rowW
		} else {
			rowH := rowArea / rect.W
			rowRect = Rect{X: rect.X, Y: rect.Y, W: rect.W, H: rowH}
			rect.Y += rowH
			rect.H -= rowH
		}
		areaLeft = rect.W * rect.H
		// Split the row strip between its nodes.
		rowTotal := rowValue
		if rowRect.W >= rowRect.H {
			y := rowRect.Y
			for _, node := range row {
				nodeValue := subtreeValue(node)
				h := nodeValue / rowTotal * rowRect.H
				nodeRect := Rect{X: rowRect.X, Y: y, W: rowRect.W, H: h, Radius: 3}
				y += h
				if len(node.Children) > 0 && depth < maxDepth {
					inner := Rect{X: nodeRect.X + 3, Y: nodeRect.Y + 3, W: nodeRect.W - 6, H: nodeRect.H - 6}
					frag.Ops = append(frag.Ops, Rect{X: nodeRect.X, Y: nodeRect.Y, W: nodeRect.W, H: nodeRect.H, Radius: 3, Fill: rowColor(nodeValue), Stroke: theme.Background, StrokeWidth: 1})
					childTotal := subtreeValue(node)
					squarifyInto(frag, node.Children, childTotal, inner, depth+1, maxDepth, unit, showValues, theme)
					continue
				}
				nodeRect.Fill = rowColor(nodeValue)
				frag.Ops = append(frag.Ops, nodeRect)
				drawTreemapLabels(frag, nodeRect, node, unit, showValues, theme)
			}
			continue
		}
		x := rowRect.X
		for _, node := range row {
			nodeValue := subtreeValue(node)
			w := nodeValue / rowTotal * rowRect.W
			nodeRect := Rect{X: x, Y: rowRect.Y, W: w, H: rowRect.H, Radius: 3}
			x += w
			if len(node.Children) > 0 && depth < maxDepth {
				inner := Rect{X: nodeRect.X + 3, Y: nodeRect.Y + 3, W: nodeRect.W - 6, H: nodeRect.H - 6}
				frag.Ops = append(frag.Ops, Rect{X: nodeRect.X, Y: nodeRect.Y, W: nodeRect.W, H: nodeRect.H, Radius: 3, Fill: rowColor(nodeValue), Stroke: theme.Background, StrokeWidth: 1})
				squarifyInto(frag, node.Children, subtreeValue(node), inner, depth+1, maxDepth, unit, showValues, theme)
				continue
			}
			nodeRect.Fill = rowColor(nodeValue)
			frag.Ops = append(frag.Ops, nodeRect)
			drawTreemapLabels(frag, nodeRect, node, unit, showValues, theme)
		}
	}
}

func drawTreemapLabels(frag *Drawing, rect Rect, node TreemapNode, unit string, showValues bool, theme Theme) {
	if rect.W < 34 || rect.H < 16 {
		return
	}
	lines := wrapText(node.Label, 10, true, false, rect.W-8)
	if len(lines) > 2 {
		lines = lines[:2]
	}
	for i, line := range lines {
		if rect.H < 16+float64(i)*13 {
			return
		}
		frag.Ops = append(frag.Ops, Glyph{X: rect.X + 4, Y: rect.Y + 13 + float64(i)*13, Size: 10, Bold: true, Color: pickContrastText(theme, rect.Fill), Anchor: "start", Text: line})
	}
	if showValues && node.Value > 0 && rect.H >= 30 && len(lines) > 0 {
		value := formatNumber(node.Value)
		if unit != "" {
			value += unitSuffixText(unit)
		}
		frag.Ops = append(frag.Ops, Glyph{X: rect.X + 4, Y: rect.Y + 13 + float64(len(lines))*13, Size: 9, Color: pickContrastText(theme, rect.Fill), Anchor: "start", Text: clipLine(value, 9, rect.W-8)})
	}
}
