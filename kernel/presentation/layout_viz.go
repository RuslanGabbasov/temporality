package presentation

import (
	"fmt"
	"math"
	"time"
)

// Visualization and planning blocks (docs/presentation-engine.md §6.3, §6.5).

func (l *layout) drawChartBlock(c Component, x, y, width float64) float64 {
	var data ChartData
	_ = decodeStrict(c.Data, &data)
	var props ChartProps
	_ = decodeStrict(c.Props, &props)
	if props.ChartType == "" {
		props.ChartType = "line"
	}
	frag := layoutChart(data, props, width, l.theme)
	for _, op := range frag.Ops {
		l.add(translateOp(op, x, y))
	}
	return frag.Height
}

func (l *layout) drawGaugeBlock(c Component, x, y, width float64) float64 {
	var data GaugeData
	_ = decodeStrict(c.Data, &data)
	max := data.Max
	if max == 0 {
		max = 100
	}
	radius := width * 0.22
	if radius > 90 {
		radius = 90
	}
	if radius < 40 {
		radius = 40
	}
	cx := x + width/2
	cy := y + radius + 14
	share := (data.Value - data.Min) / (max - data.Min)
	if share < 0 {
		share = 0
	}
	if share > 1 {
		share = 1
	}
	// Half-donut gauge: background arc over the top, the value portion on it.
	start := math.Pi
	end := 2 * math.Pi
	valueAngle := start + (end-start)*share
	l.add(Wedge{CX: cx, CY: cy, R: radius, Inner: radius * 0.68, Start: start, End: end, Color: l.theme.Surface})
	if share > 0 {
		l.add(Wedge{CX: cx, CY: cy, R: radius, Inner: radius * 0.68, Start: start, End: valueAngle, Color: l.theme.Accent})
	}
	valueText := formatNumber(data.Value) + unitSuffix(data.Unit)
	l.pushText(valueText, cx, cy-14, 20, true, false, l.theme.TextPrimary, "middle")
	if data.Label != "" {
		l.pushText(clipLine(data.Label, 11, width), cx, cy+8, 11, false, false, l.theme.TextSecondary, "middle")
	}
	return radius + 40
}

func (l *layout) drawTimelineBlock(c Component, x, y, width float64) float64 {
	var items []TimelineItem
	_ = jsonUnmarshalItems(c.Items, &items)
	if len(items) == 0 {
		return 0
	}
	const (
		dotX  = 8.0
		dateX = 26.0
		dateW = 86.0
		bodyX = 120.0
	)
	total := 0.0
	for i, item := range items {
		color := l.theme.StatusColor(item.Status)
		// The spine connects consecutive dots; first and last entries carry a
		// one-sided stub so the line never dangles past an endpoint.
		switch {
		case i == 0 && len(items) > 1:
			l.add(Segment{X1: x + dotX, Y1: y + total + 8, X2: x + dotX, Y2: y + total + 52, Color: l.theme.Border, Width: 2})
		case i == len(items)-1 && i > 0:
			l.add(Segment{X1: x + dotX, Y1: y + total - 44, X2: x + dotX, Y2: y + total + 8, Color: l.theme.Border, Width: 2})
		case i > 0:
			l.add(Segment{X1: x + dotX, Y1: y + total - 44, X2: x + dotX, Y2: y + total + 52, Color: l.theme.Border, Width: 2})
		}
		l.add(Dot{CX: x + dotX, CY: y + total + 8, R: 5, Color: color})
		l.pushText(clipLine(item.At, 11, dateW), x+dateX, y+total+2, 11, true, false, l.theme.TextSecondary, "start")
		used := l.drawTextBlock(item.Label, 12.5, l.theme.TextPrimary, true, false, x+bodyX, y+total, width-bodyX)
		if item.Description != "" {
			used += l.drawTextBlock(item.Description, 11, l.theme.TextSecondary, false, false, x+bodyX, y+total+used, width-bodyX)
		}
		total += maxFloat(used, 22) + 22
	}
	return total - 22
}

func (l *layout) drawGanttBlock(c Component, x, y, width float64) float64 {
	var data GanttData
	_ = decodeStrict(c.Data, &data)
	var props GanttProps
	_ = decodeStrict(c.Props, &props)
	if len(data.Tasks) == 0 {
		return 0
	}
	type span struct {
		start, end time.Time
		task       GanttTask
	}
	spans := make([]span, 0, len(data.Tasks))
	var minStart, maxEnd time.Time
	for _, task := range data.Tasks {
		start, startOK := parseIRDate(task.Start)
		end, endOK := parseIRDate(task.End)
		if !startOK || !endOK {
			return 0
		}
		if end.Before(start) {
			end = start
		}
		spans = append(spans, span{start: start, end: end, task: task})
		if minStart.IsZero() || start.Before(minStart) {
			minStart = start
		}
		if maxEnd.IsZero() || end.After(maxEnd) {
			maxEnd = end
		}
	}

	const (
		labelWidth = 170.0
		axisHeight = 22.0
		rowHeight  = 26.0
		barHeight  = 12.0
	)
	chartX := x + labelWidth
	chartW := width - labelWidth
	totalDays := maxEnd.Sub(minStart).Hours() / 24
	if totalDays < 1 {
		totalDays = 1
	}
	dayWidth := chartW / totalDays
	if dayWidth > 26 {
		dayWidth = 26 // giant scales read worse than a wider axis
	}
	usedW := totalDays * dayWidth
	if usedW > chartW {
		usedW = chartW
	}

	total := 0.0
	if props.Title != "" {
		total += l.drawTextBlock(props.Title, 13, l.theme.TextPrimary, true, false, x, y+total, width) + 4
	}
	axisY := y + total
	total += axisHeight

	// Day grid: ticks per the requested scale.
	tickStep, tickFormat := ganttTick(props.Scale, totalDays)
	t := truncateDay(minStart)
	gridBottom := y + total + float64(len(spans))*rowHeight
	for !t.After(maxEnd) {
		offsetDays := t.Sub(minStart).Hours() / 24
		tickX := chartX + offsetDays*dayWidth
		l.add(Segment{X1: tickX, Y1: axisY, X2: tickX, Y2: gridBottom, Color: l.theme.Border, Width: 0.5})
		l.pushText(t.Format(tickFormat), tickX+2, axisY, 9.5, false, false, l.theme.TextSecondary, "start")
		t = t.AddDate(0, 0, tickStep)
	}
	l.pushText("Task", x, axisY, 10.5, true, false, l.theme.TextSecondary, "start")

	for i, s := range spans {
		rowY := y + total + float64(i)*rowHeight
		l.pushText(clipLine(s.task.Label, 10.5, labelWidth-8), x, rowY+(rowHeight-barHeight)/2-1, 10.5, false, false, l.theme.TextPrimary, "start")
		startOffset := s.start.Sub(minStart).Hours() / 24
		barX := chartX + startOffset*dayWidth
		barW := (s.end.Sub(s.start).Hours() / 24) * dayWidth
		if barW < 3 {
			barW = 3
		}
		color := l.theme.Accent
		if s.task.Status != "" && s.task.Status != statusNeutral {
			color = l.theme.StatusColor(s.task.Status)
		}
		l.add(Rect{X: barX, Y: rowY + (rowHeight-barHeight)/2, W: barW, H: barHeight, Fill: color, Radius: 3})
		if s.task.Assignee != "" {
			labelX := barX + barW + 6
			if labelX+measureText(9.5, false, false, s.task.Assignee) < x+width {
				l.pushText(s.task.Assignee, labelX, rowY+(rowHeight-barHeight)/2-1, 9.5, false, false, l.theme.TextSecondary, "start")
			}
		}
	}
	total += float64(len(spans)) * rowHeight
	return total
}

// ganttTick picks the grid step and label format for the requested scale.
func ganttTick(scale string, totalDays float64) (stepDays int, format string) {
	switch scale {
	case "day":
		if totalDays <= 45 {
			return 1, "02.01"
		}
		return 7, "02.01"
	case "week":
		return 7, "02.01"
	case "month":
		if totalDays > 240 {
			return 91, "01.06"
		}
		return 30, "01.06"
	default:
		if totalDays <= 30 {
			return 1, "02.01"
		}
		if totalDays <= 180 {
			return 7, "02.01"
		}
		return 30, "01.06"
	}
}

func truncateDay(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

// chartSeries is one normalized plotted series.
type chartSeries struct {
	name   string
	values []float64
	color  string
}

// layoutChart renders one chart into an isolated fragment: the shared core of
// the chart block and the document-level static render. Same data in, same
// glyphs out — for every static format (docs/presentation-engine.md §7.3).
func layoutChart(data ChartData, props ChartProps, width float64, theme Theme) Drawing {
	frag := Drawing{Width: width}
	const (
		titleSize   = 12.5
		axisSize    = 10.5
		labelSize   = 10.0
		marginTop   = 6.0
		marginLeft  = 46.0
		marginRight = 16.0
		legendH     = 20.0
		plotHeight  = 170.0
	)
	if width < 120 {
		return frag
	}
	plotW := width - marginLeft - marginRight
	if plotW < 40 {
		plotW = 40
	}
	height := marginTop

	if props.Title != "" {
		lines := wrapText(props.Title, titleSize, true, false, width)
		for _, line := range lines {
			frag.Ops = append(frag.Ops, Glyph{X: 0, Y: height + titleSize, Size: titleSize, Bold: true, Color: theme.TextPrimary, Anchor: "start", Text: line})
			height += lineHeight(titleSize)
		}
		height += 6
	}

	seriesColors := []string{theme.Accent, theme.Success, theme.Warning, theme.Danger, theme.Info, theme.Neutral}
	seriesList := make([]chartSeries, 0, len(data.Series)+1)
	if len(data.Values) > 0 {
		seriesList = append(seriesList, chartSeries{name: labelOr(props.Title, "Series 1"), values: data.Values, color: seriesColors[0]})
	}
	for i, s := range data.Series {
		seriesList = append(seriesList, chartSeries{name: s.Name, values: s.Values, color: seriesColors[(i+1)%len(seriesColors)]})
	}
	if len(seriesList) == 0 {
		return frag
	}

	min, max := math.Inf(1), math.Inf(-1)
	count := 0
	for _, s := range seriesList {
		for _, v := range s.values {
			if v < min {
				min = v
			}
			if v > max {
				max = v
			}
		}
		if len(s.values) > count {
			count = len(s.values)
		}
	}
	if math.IsInf(min, 0) || math.IsInf(max, 0) || count == 0 {
		return frag
	}
	if min == max {
		// A flat line still needs a visible band around the value.
		pad := math.Abs(min)*0.1 + 1
		min -= pad
		max += pad
	}

	labels := data.Labels
	if len(labels) < count {
		labels = append(append([]string{}, labels...), syntheticLabels(count-len(labels))...)
	}

	isShare := props.ChartType == "pie" || props.ChartType == "donut"

	if isShare {
		// Pie/donut render the first series as shares of a whole; the legend
		// carries every label so no slice caption can get lost (§7.5).
		values := seriesList[0].values
		total := 0.0
		for _, v := range values {
			total += v
		}
		cx := width / 2
		cy := height + plotHeight/2
		r := plotHeight/2 - 14
		if r < 44 {
			r = 44
		}
		angle := -math.Pi / 2 // start at 12 o'clock, clockwise
		for i, v := range values {
			if total <= 0 || v == 0 {
				continue
			}
			share := v / total
			sweep := share * 2 * math.Pi
			frag.Ops = append(frag.Ops, Wedge{CX: cx, CY: cy, R: r, Inner: donutInner(props.ChartType, r), Start: angle, End: angle + sweep, Color: seriesColors[i%len(seriesColors)]})
			if share >= 0.04 {
				mid := angle + sweep/2
				lx := cx + math.Cos(mid)*(r*0.72)
				ly := cy + math.Sin(mid)*(r*0.72)
				if props.ShowValues {
					frag.Ops = append(frag.Ops, Glyph{X: lx, Y: ly + labelSize*0.35, Size: labelSize, Color: pickPieTextColor(theme), Anchor: "middle", Text: formatPercent(share * 100)})
				}
			}
			angle += sweep
		}
		height += plotHeight
	} else {
		chartTop := height
		frag.Ops = append(frag.Ops, Segment{X1: marginLeft, Y1: chartTop, X2: marginLeft, Y2: chartTop + plotHeight, Color: theme.Border, Width: 1})
		frag.Ops = append(frag.Ops, Segment{X1: marginLeft, Y1: chartTop + plotHeight, X2: marginLeft + plotW, Y2: chartTop + plotHeight, Color: theme.Border, Width: 1})

		// Gridlines + y-axis labels: four steps between min and max.
		steps := 4
		for i := 0; i <= steps; i++ {
			fraction := float64(i) / float64(steps)
			value := max - (max-min)*fraction
			gy := chartTop + plotHeight*fraction
			if i > 0 && i < steps {
				frag.Ops = append(frag.Ops, Segment{X1: marginLeft, Y1: gy, X2: marginLeft + plotW, Y2: gy, Color: theme.Border, Width: 0.5, Dash: []float64{2, 3}})
			}
			frag.Ops = append(frag.Ops, Glyph{X: marginLeft - 6, Y: gy + axisSize*0.35, Size: axisSize, Color: theme.TextSecondary, Anchor: "end", Text: formatNumber(roundTick(value))})
		}

		slotW := plotW / float64(count)
		for si, s := range seriesList {
			pts := make([]Point, 0, len(s.values))
			for i, v := range s.values {
				fx := marginLeft + slotW*(float64(i)+0.5)
				fy := chartTop + plotHeight*(max-v)/(max-min)
				pts = append(pts, Point{X: fx, Y: fy})
			}
			switch props.ChartType {
			case "bar":
				barW := slotW * 0.62
				if len(seriesList) > 1 {
					barW = slotW * 0.62 / float64(len(seriesList))
				}
				if barW < 2 {
					barW = 2
				}
				for i, p := range pts {
					bx := p.X - barW/2 + float64(si)*barW
					by := p.Y
					frag.Ops = append(frag.Ops, Rect{X: bx, Y: by, W: barW, H: chartTop + plotHeight - by, Fill: s.color})
					if props.ShowValues && barW >= 22 {
						frag.Ops = append(frag.Ops, Glyph{X: bx + barW/2, Y: by - 3, Size: labelSize, Color: theme.TextSecondary, Anchor: "middle", Text: formatNumber(s.values[i])})
					}
				}
			case "area":
				areaPts := append(append([]Point{}, pts...), Point{X: pts[len(pts)-1].X, Y: chartTop + plotHeight}, Point{X: pts[0].X, Y: chartTop + plotHeight})
				frag.Ops = append(frag.Ops, Poly{Points: areaPts, Fill: blendOnBackground(s.color, theme, 0.16)})
				frag.Ops = append(frag.Ops, Poly{Points: pts, Color: s.color, Width: 2})
			default: // line
				frag.Ops = append(frag.Ops, Poly{Points: pts, Color: s.color, Width: 2})
				for _, p := range pts {
					frag.Ops = append(frag.Ops, Dot{CX: p.X, CY: p.Y, R: 2.5, Color: s.color})
				}
				if props.ShowValues {
					for i, p := range pts {
						frag.Ops = append(frag.Ops, Glyph{X: p.X, Y: p.Y - 6, Size: labelSize, Color: theme.TextSecondary, Anchor: "middle", Text: formatNumber(s.values[i])})
					}
				}
			}
		}

		// X labels: show at most ~10 to stay readable.
		labelStep := 1
		if count > 10 {
			labelStep = (count + 9) / 10
		}
		for i := 0; i < count; i += labelStep {
			fx := marginLeft + slotW*(float64(i)+0.5)
			text := clipLine(labels[i], labelSize, maxFloat(slotW*float64(labelStep)*0.9, 30))
			frag.Ops = append(frag.Ops, Glyph{X: fx, Y: chartTop + plotHeight + 14, Size: labelSize, Color: theme.TextSecondary, Anchor: "middle", Text: text})
		}
		if data.YTitle != "" {
			frag.Ops = append(frag.Ops, Glyph{X: 0, Y: chartTop + 10, Size: axisSize, Color: theme.TextSecondary, Anchor: "start", Text: clipLine(data.YTitle, axisSize, marginLeft)})
		}
		if data.XTitle != "" {
			frag.Ops = append(frag.Ops, Glyph{X: marginLeft + plotW/2, Y: chartTop + plotHeight + 30, Size: axisSize, Color: theme.TextSecondary, Anchor: "middle", Text: clipLine(data.XTitle, axisSize, plotW)})
			height += 16
		}
		height += plotHeight + 22
	}

	// Legend: series names for plots, every pie label for shares (§7.5:
	// legends must not be lost in export).
	showLegend := true
	if props.ShowLegend != nil {
		showLegend = *props.ShowLegend
	}
	var entries []legendEntry
	if isShare {
		values := seriesList[0].values
		for i := 0; i < len(values) && i < len(labels); i++ {
			entries = append(entries, legendEntry{color: seriesColors[i%len(seriesColors)], text: labels[i]})
		}
	} else if len(seriesList) > 1 || hasSeriesNames(seriesList) {
		for _, s := range seriesList {
			entries = append(entries, legendEntry{color: s.color, text: s.name})
		}
	}
	if showLegend && len(entries) > 0 {
		legendY := height + 2
		lx := 2.0
		for _, entry := range entries {
			entryW := 14 + measureText(labelSize, false, false, entry.text) + 14
			if lx+entryW > width {
				lx = 2
				legendY += legendH
			}
			frag.Ops = append(frag.Ops, Dot{CX: lx + 5, CY: legendY + 8, R: 4.5, Color: entry.color})
			frag.Ops = append(frag.Ops, Glyph{X: lx + 14, Y: legendY + 11.5, Size: labelSize, Color: theme.TextPrimary, Anchor: "start", Text: clipLine(entry.text, labelSize, width-18)})
			lx += entryW
		}
		height = legendY + legendH
	}

	frag.Height = height
	return frag
}

func hasSeriesNames(seriesList []chartSeries) bool {
	for _, s := range seriesList {
		if s.name != "" {
			return true
		}
	}
	return false
}

func donutInner(chartType string, r float64) float64 {
	if chartType == "donut" {
		return r * 0.6
	}
	return 0
}

func pickPieTextColor(theme Theme) string {
	// Percent captions sit on colored slices; dark themes need light text.
	if theme.Background == "#161616" {
		return "#f4f4f4"
	}
	return "#ffffff"
}

type legendEntry struct {
	text  string
	color string
}

func formatPercent(v float64) string {
	return formatNumber(math.Round(v*10)/10) + "%"
}

func roundTick(v float64) float64 {
	if v == 0 {
		return 0
	}
	abs := math.Abs(v)
	if abs >= 10 {
		return math.Round(v)
	}
	if abs >= 1 {
		return math.Round(v*10) / 10
	}
	return math.Round(v*100) / 100
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// parseHexColor decodes #rrggbb into components.
func parseHexColor(hex string) (r, g, b uint8, ok bool) {
	if len(hex) != 7 || hex[0] != '#' {
		return 0, 0, 0, false
	}
	values, err := fmt.Sscanf(hex, "#%02x%02x%02x", &r, &g, &b)
	if err != nil || values != 3 {
		return 0, 0, 0, false
	}
	return r, g, b, true
}

// blendOnBackground approximates a translucent fill by blending the color
// toward the theme background: the PNG rasterizer draws solid pixels, and the
// SVG emitter receives the same pre-blended color, so area fills look the
// same in both formats (§7.3).
func blendOnBackground(hex string, theme Theme, alpha float64) string {
	r, g, b, ok := parseHexColor(hex)
	if !ok {
		return hex
	}
	br, bg, bb, ok := parseHexColor(theme.Background)
	if !ok {
		br, bg, bb = 255, 255, 255
	}
	mix := func(c, bg uint8) uint8 {
		return uint8(math.Round(float64(c)*(1-alpha) + float64(bg)*alpha))
	}
	return fmt.Sprintf("#%02x%02x%02x", mix(r, br), mix(g, bg), mix(b, bb))
}
