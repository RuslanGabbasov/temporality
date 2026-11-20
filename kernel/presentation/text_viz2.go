package presentation

// Text fallbacks for the extended visualization catalog
// (docs/presentation-engine-ext.md §9 Text): the fallback carries the
// SEMANTICS of the visual — axes, groups, positions, highlighted objects,
// flows with volumes — never just its name.

import (
	"fmt"
	"sort"
	"strings"
)

func scatterText(c Component, indent string, quadrant bool) []string {
	var data ScatterData
	_ = decodeStrict(c.Data, &data)
	var props ScatterProps
	_ = decodeStrict(c.Props, &props)
	var lines []string
	xAxis := labelOr(data.XTitle, "X")
	yAxis := labelOr(data.YTitle, "Y")
	if quadrant {
		lines = append(lines, indent+"Positioning map ("+xAxis+" × "+yAxis+"):")
	} else {
		lines = append(lines, indent+"Scatter ("+xAxis+" × "+yAxis+"):")
	}
	if quadrant && len(props.QuadrantLabels) == 4 {
		lines = append(lines, fmt.Sprintf("%s  quadrants: top-left %q · top-right %q · bottom-left %q · bottom-right %q", indent, props.QuadrantLabels[0], props.QuadrantLabels[1], props.QuadrantLabels[2], props.QuadrantLabels[3]))
	}
	for _, line := range props.RefLines {
		axis := "x"
		if line.Axis == "y" {
			axis = "y"
		}
		mark := line.Label
		if mark == "" {
			mark = fmt.Sprintf("%v", line.At)
		}
		lines = append(lines, fmt.Sprintf("%s  ref %s=%v (%s)", indent, axis, line.At, mark))
	}
	// Group objects under their group headers; ungrouped points in order.
	groups := map[string][]ScatterPoint{}
	order := []string{}
	for _, p := range data.Points {
		g := p.Group
		if g == "" {
			g = "—"
		}
		if _, seen := groups[g]; !seen {
			order = append(order, g)
		}
		groups[g] = append(groups[g], p)
	}
	for _, g := range order {
		if g != "—" {
			lines = append(lines, indent+"  "+g+":")
		}
		for _, p := range groups[g] {
			line := fmt.Sprintf("%s   • %s: %s = %v, %s = %v", indent, labelOr(p.Label, "point"), xAxis, p.X, yAxis, p.Y)
			if p.Highlight {
				line += " ★ highlighted"
			}
			if p.Note != "" {
				line += " — " + p.Note
			}
			lines = append(lines, line)
		}
	}
	return lines
}

func heatmapText(c Component, indent string) []string {
	var data HeatmapData
	_ = decodeStrict(c.Data, &data)
	var props HeatmapProps
	_ = decodeStrict(c.Props, &props)
	unit := unitSuffix(data.Unit)
	lines := []string{indent + "Heatmap" + labelSuffix(props.Title) + ":"}
	for r, row := range data.Rows {
		if r >= len(data.Values) {
			break
		}
		cells := make([]string, 0, len(data.Cols))
		for c, col := range data.Cols {
			if c >= len(data.Values[r]) || data.Values[r][c] == nil {
				cells = append(cells, col+"=—")
				continue
			}
			cells = append(cells, fmt.Sprintf("%s=%s%s", col, formatNumber(*data.Values[r][c]), unit))
		}
		lines = append(lines, indent+"  "+row+": "+strings.Join(cells, "; "))
	}
	return lines
}

func histogramText(c Component, indent string) []string {
	var data HistogramData
	_ = decodeStrict(c.Data, &data)
	var props HistogramProps
	_ = decodeStrict(c.Props, &props)
	unit := unitSuffix(data.Unit)
	if len(data.Values) == 0 {
		return []string{indent + "Histogram: no samples"}
	}
	min, max := data.Values[0], data.Values[0]
	for _, v := range data.Values {
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
	}
	if min == max {
		pad := float64(1)
		if min != 0 {
			pad = min * 0.1
		}
		min, max = min-pad, max+pad
	}
	bins := data.Bins
	switch {
	case data.BinWidth > 0:
		bins = int((max - min) / data.BinWidth)
		if bins < 1 {
			bins = 1
		}
		if bins > histogramBinLimit {
			bins = histogramBinLimit
		}
	case bins <= 0:
		bins = 8
		if bins > histogramBinLimit {
			bins = histogramBinLimit
		}
	}
	width := (max - min) / float64(bins)
	counts := make([]int, bins)
	for _, v := range data.Values {
		idx := int((v - min) / width)
		if idx < 0 {
			idx = 0
		}
		if idx >= bins {
			idx = bins - 1
		}
		counts[idx]++
	}
	title := labelOr(props.Title, "Histogram")
	axis := labelOr(data.XTitle, "value")
	lines := []string{fmt.Sprintf("%s%s (%s=%s…%s, %d samples):", indent+title, unit, axis, formatNumber(min), formatNumber(max), len(data.Values))}
	for i, count := range counts {
		share := ""
		if props.Normalized && len(data.Values) > 0 {
			share = fmt.Sprintf(" (%.1f%%)", float64(count)/float64(len(data.Values))*100)
		}
		lines = append(lines, fmt.Sprintf("%s  %s…%s: %d%s", indent, formatNumber(min+width*float64(i)), formatNumber(min+width*float64(i+1)), count, share))
	}
	return lines
}

func radarText(c Component, indent string) []string {
	var data RadarData
	_ = decodeStrict(c.Data, &data)
	var props RadarProps
	_ = decodeStrict(c.Props, &props)
	unit := unitSuffix(data.Unit)
	lines := []string{indent + "Radar" + labelSuffix(props.Title) + " (" + strings.Join(data.Axes, ", ") + "):"}
	for _, series := range data.Series {
		mark := ""
		if series.Name == props.Highlight {
			mark = " ★"
		}
		lines = append(lines, fmt.Sprintf("%s  %s%s:", indent, series.Name, mark))
		for a, value := range series.Values {
			axis := fmt.Sprintf("axis %d", a+1)
			if a < len(data.Axes) {
				axis = data.Axes[a]
			}
			lines = append(lines, fmt.Sprintf("%s    %s = %s%s", indent, axis, formatNumber(value), unit))
		}
	}
	return lines
}

func sankeyText(c Component, indent string) []string {
	var data SankeyData
	_ = decodeStrict(c.Data, &data)
	var props SankeyProps
	_ = decodeStrict(c.Props, &props)
	unit := unitSuffix(data.Unit)
	labels := map[string]string{}
	inSum := map[string]float64{}
	outSum := map[string]float64{}
	for _, node := range data.Nodes {
		labels[node.ID] = labelOr(node.Label, node.ID)
	}
	lines := []string{indent + "Flows" + labelSuffix(props.Title) + ":"}
	for _, link := range data.Links {
		line := fmt.Sprintf("%s  %s → %s: %s%s", indent, labelOr(labels[link.Source], link.Source), labelOr(labels[link.Target], link.Target), formatNumber(link.Value), unit)
		if link.Label != "" {
			line += " (" + link.Label + ")"
		}
		lines = append(lines, line)
		outSum[link.Source] += link.Value
		inSum[link.Target] += link.Value
	}
	// Node totals for branch/merge points (multiple flows in or out).
	for _, node := range data.Nodes {
		total := 0.0
		kind := ""
		switch {
		case outSum[node.ID] > 0 && inSum[node.ID] > 0:
			total, kind = inSum[node.ID], "passes"
		case outSum[node.ID] > 0:
			total, kind = outSum[node.ID], "distributes"
		case inSum[node.ID] > 0:
			total, kind = inSum[node.ID], "receives"
		default:
			continue
		}
		lines = append(lines, fmt.Sprintf("%s  %s %s %s%s", indent, labels[node.ID], kind, formatNumber(total), unit))
	}
	return lines
}

func swimlaneText(c Component, indent string) []string {
	var data SwimlaneData
	_ = decodeStrict(c.Data, &data)
	var props SwimlaneProps
	_ = decodeStrict(c.Props, &props)
	lines := []string{indent + "Process" + labelSuffix(props.Title) + ":"}
	for _, lane := range data.Lanes {
		lines = append(lines, indent+"  "+labelOr(lane.Label, lane.ID)+":")
		for _, node := range data.Nodes {
			if node.Lane != lane.ID {
				continue
			}
			line := fmt.Sprintf("%s    • %s", indent, node.Label)
			if node.Status != "" {
				line += " [" + node.Status + "]"
			}
			if node.Note != "" {
				line += " — " + node.Note
			}
			lines = append(lines, line)
		}
	}
	for _, edge := range data.Edges {
		label := edge.Label
		if label == "" {
			label = edge.Condition
		}
		if label != "" {
			lines = append(lines, fmt.Sprintf("%s  %s → %s (если %s)", indent, edge.From, edge.To, label))
		} else {
			lines = append(lines, fmt.Sprintf("%s  %s → %s", indent, edge.From, edge.To))
		}
	}
	return lines
}

func treemapText(c Component, indent string) []string {
	var data TreemapData
	_ = decodeStrict(c.Data, &data)
	var props TreemapProps
	_ = decodeStrict(c.Props, &props)
	unit := unitSuffix(data.Unit)
	total := subtreeValue(data.Root)
	lines := []string{fmt.Sprintf("%s%s (всего %s%s):", indent+"Treemap"+labelSuffix(props.Title), "", formatNumber(total), unit)}
	var walk func(node TreemapNode, depth int)
	walk = func(node TreemapNode, depth int) {
		pad := indent + strings.Repeat("  ", depth+1)
		share := ""
		if total > 0 && subtreeValue(node) > 0 {
			share = fmt.Sprintf(" (%.1f%%)", subtreeValue(node)/total*100)
		}
		line := fmt.Sprintf("%s%s: %s%s%s", pad, node.Label, formatNumber(subtreeValue(node)), unit, share)
		if node.Ref != nil {
			line += fmt.Sprintf(" [%s %s]", node.Ref.Type, node.Ref.ID)
		}
		lines = append(lines, line)
		children := append([]TreemapNode{}, node.Children...)
		sort.Slice(children, func(i, j int) bool { return subtreeValue(children[i]) > subtreeValue(children[j]) })
		for _, child := range children {
			walk(child, depth+1)
		}
	}
	for _, child := range data.Root.Children {
		walk(child, 1)
	}
	if len(data.Root.Children) == 0 {
		lines = append(lines, fmt.Sprintf("%s  %s: %s%s", indent, data.Root.Label, formatNumber(data.Root.Value), unit))
	}
	return lines
}

func labelSuffix(title string) string {
	if title == "" {
		return ""
	}
	return " — " + title
}
