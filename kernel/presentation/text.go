package presentation

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Text rendering (docs/presentation-engine.md §7.6): every component must
// carry its core meaning into plain text, because some channels cannot show
// visuals at all and the text fallback must not lose the data. RenderText
// produces the channel-neutral plain form; RenderMarkdown produces the
// markdown-flavored form used by markdown-capable channels (tables stay
// tables, images stay references).

// RenderText renders the whole document as plain text — the last link of the
// fallback chain (§9.4) and the server-side text preview stored with every
// presentation.
func RenderText(doc Document) string {
	var b strings.Builder
	if doc.Title != "" {
		b.WriteString(doc.Title)
		b.WriteString("\n")
	}
	for i, component := range doc.Content {
		lines := RenderComponentText(component, 0)
		if len(lines) == 0 {
			continue
		}
		if i > 0 && b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(strings.Join(lines, "\n"))
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// RenderMarkdown renders the document as markdown for markdown-capable
// channels (§9.2 capability negotiation).
func RenderMarkdown(doc Document) string {
	var b strings.Builder
	if doc.Title != "" {
		b.WriteString("# ")
		b.WriteString(doc.Title)
		b.WriteString("\n\n")
	}
	for i, component := range doc.Content {
		lines := RenderComponentText(component, 0)
		if len(lines) == 0 {
			continue
		}
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(strings.Join(lines, "\n"))
		b.WriteString("\n\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// RenderComponentText renders one component into text lines; depth drives the
// indentation of nested children.
func RenderComponentText(c Component, depth int) []string {
	indent := strings.Repeat("  ", depth)
	if entry, isCustom := customLookup(c.Type); isCustom {
		if lines := entry.text(c, indent, depth); lines != nil {
			return lines
		}
	}
	data := decodeRenderData(c)
	switch c.Type {
	case "text", "paragraph":
		if c.Content == "" {
			return nil
		}
		return prefixLines(indent, c.Content)
	case "markdown":
		if c.Content == "" {
			return nil
		}
		return prefixLines(indent, c.Content)
	case "heading":
		level := 2
		var props HeadingProps
		if decodeStrict(c.Props, &props) == nil && props.Level >= 1 && props.Level <= 6 {
			level = props.Level
		}
		return []string{indent + strings.Repeat("#", level) + " " + c.Content}
	case "list":
		var items []ListItem
		_ = jsonUnmarshalItems(c.Items, &items)
		ordered := false
		var props ListProps
		if decodeStrict(c.Props, &props) == nil {
			ordered = props.Ordered
		}
		lines := make([]string, 0, len(items))
		for i, item := range items {
			marker := "-"
			if ordered {
				marker = fmt.Sprintf("%d.", i+1)
			}
			entry := indent + marker + " " + item.Label
			if item.Description != "" {
				entry += " — " + item.Description
			}
			lines = append(lines, entry)
		}
		return lines
	case "quote":
		attribution := ""
		var props QuoteProps
		if decodeStrict(c.Props, &props) == nil && props.Attribution != "" {
			attribution = " — " + props.Attribution
		}
		return prefixLines(indent+"> ", c.Content+attribution)
	case "code":
		language := ""
		var props CodeProps
		if decodeStrict(c.Props, &props) == nil && props.Language != "" {
			language = props.Language
		}
		return []string{indent + "```" + language, indent + c.Content, indent + "```"}
	case "link":
		var props LinkProps
		_ = decodeStrict(c.Props, &props)
		label := c.Content
		if label == "" {
			label = props.Href
		}
		return []string{indent + label + " (" + props.Href + ")"}
	case "badge":
		var props BadgeProps
		_ = decodeStrict(c.Props, &props)
		return []string{indent + "[" + strings.ToUpper(statusWord(props.Status)) + "] " + c.Content}
	case "divider":
		return []string{indent + "────────"}
	case "table":
		return tableText(c, data.table, indent)
	case "card":
		return cardText(c, indent, depth)
	case "card-grid":
		return childrenText(c, indent, depth)
	case "comparison":
		return comparisonText(c, indent)
	case "key-value":
		return keyValueText(c, indent)
	case "kpi":
		return kpiText(c, indent)
	case "progress":
		return progressText(c, indent)
	case "status":
		var props StatusProps
		_ = decodeStrict(c.Props, &props)
		line := indent + "[" + strings.ToUpper(statusWord(props.Status)) + "] " + c.Content
		if props.Detail != "" {
			line += " — " + props.Detail
		}
		return []string{line}
	case "alert":
		var props AlertProps
		_ = decodeStrict(c.Props, &props)
		title := props.Title
		if title == "" {
			title = strings.ToUpper(statusWord(props.Severity))
		}
		return prefixLines(indent+"⚠ "+title+": ", c.Content)
	case "accordion":
		return accordionText(c, indent)
	case "chart":
		return chartText(c, data.chart, indent)
	case "gauge":
		var data GaugeData
		_ = decodeStrict(c.Data, &data)
		max := data.Max
		if max == 0 {
			max = 100
		}
		line := fmt.Sprintf("%s: %s%s (%s–%s)", labelOr(data.Label, "Value"), formatNumber(data.Value), unitSuffix(data.Unit), formatNumber(data.Min), formatNumber(max))
		return []string{indent + line}
	case "flowchart":
		return flowText(c, indent)
	case "dependency-graph":
		return flowText(c, indent)
	case "mindmap":
		return mindmapText(c, indent)
	case "state-diagram":
		return stateText(c, indent)
	case "er-diagram":
		return erText(c, indent)
	case "class-diagram":
		return classText(c, indent)
	case "form":
		return formText(c, indent)
	case "sequence-diagram":
		return sequenceText(c, indent)
	case "timeline":
		return timelineText(c, indent)
	case "gantt":
		return ganttText(c, indent)
	case "image":
		var props ImageProps
		_ = decodeStrict(c.Props, &props)
		caption := props.Caption
		if caption == "" {
			caption = c.Content
		}
		if caption == "" {
			caption = "image"
		}
		return []string{indent + "[image] " + caption + " (" + props.Src + ")"}
	case "image-gallery":
		var items []GalleryItem
		_ = jsonUnmarshalItems(c.Items, &items)
		lines := make([]string, 0, len(items)+1)
		lines = append(lines, indent+"[gallery]")
		for _, item := range items {
			label := item.Caption
			if label == "" {
				label = item.Alt
			}
			lines = append(lines, indent+"  • "+label+" ("+item.Src+")")
		}
		return lines
	case "file":
		var props FileProps
		_ = decodeStrict(c.Props, &props)
		line := indent + "[file] " + props.Name
		if props.Size != "" {
			line += " (" + props.Size + ")"
		}
		if c.Content != "" {
			line += " — " + c.Content
		}
		return []string{line}
	case "button":
		actions := describeActions(c.Actions)
		if len(actions) == 0 {
			return []string{indent + "[" + c.Content + "]"}
		}
		return []string{indent + "[" + c.Content + "] → " + strings.Join(actions, ", ")}
	default:
		return nil
	}
}

// decodeRenderData eagerly decodes the typed data payloads the text renderer
// may need (table and chart), ignoring errors: text rendering must never fail
// on data the validator already accepted.
type renderData struct {
	table TableData
	chart ChartData
}

func decodeRenderData(c Component) renderData {
	var data renderData
	_ = decodeStrict(c.Data, &data.table)
	_ = decodeStrict(c.Data, &data.chart)
	return data
}

func prefixLines(indent, text string) []string {
	lines := strings.Split(text, "\n")
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = indent + line
	}
	return out
}

func indentLines(lines []string, indent string) []string {
	out := make([]string, len(lines))
	for i, line := range lines {
		if line == "" {
			out[i] = line
			continue
		}
		out[i] = indent + line
	}
	return out
}

func childrenText(c Component, indent string, depth int) []string {
	var lines []string
	for _, child := range c.Children {
		lines = append(lines, RenderComponentText(child, depth+1)...)
	}
	return lines
}

func cardText(c Component, indent string, depth int) []string {
	var props CardProps
	_ = decodeStrict(c.Props, &props)
	var lines []string
	if props.Title != "" {
		lines = append(lines, indent+"▌ "+props.Title)
	}
	return append(lines, indentLines(childrenTextLines(c, depth), indent)...)
}

func childrenTextLines(c Component, depth int) []string {
	var lines []string
	for _, child := range c.Children {
		lines = append(lines, RenderComponentText(child, depth+1)...)
	}
	return lines
}

func tableText(c Component, data TableData, indent string) []string {
	if len(data.Columns) == 0 {
		return nil
	}
	headers := make([]string, 0, len(data.Columns))
	for _, col := range data.Columns {
		title := col.Title
		if title == "" {
			title = col.Key
		}
		headers = append(headers, title)
	}
	lines := []string{indent + "| " + strings.Join(headers, " | ") + " |"}
	separator := make([]string, 0, len(data.Columns))
	for range data.Columns {
		separator = append(separator, "---")
	}
	lines = append(lines, indent+"| "+strings.Join(separator, " | ")+" |")
	for _, row := range data.Rows {
		cells := make([]string, 0, len(data.Columns))
		for _, col := range data.Columns {
			cells = append(cells, row[col.Key].Display())
		}
		lines = append(lines, indent+"| "+strings.Join(cells, " | ")+" |")
	}
	return lines
}

func comparisonText(c Component, indent string) []string {
	var items []ComparisonItem
	_ = jsonUnmarshalItems(c.Items, &items)
	lines := make([]string, 0, len(items))
	for _, item := range items {
		parts := make([]string, 0, len(item.Fields))
		for _, field := range item.Fields {
			parts = append(parts, field.Label+": "+field.Value)
		}
		status := ""
		if item.Status != "" && item.Status != statusNeutral {
			status = " [" + strings.ToUpper(statusWord(item.Status)) + "]"
		}
		lines = append(lines, indent+"• "+item.Label+status+": "+strings.Join(parts, "; "))
	}
	return lines
}

func keyValueText(c Component, indent string) []string {
	var items []KeyValueItem
	_ = jsonUnmarshalItems(c.Items, &items)
	lines := make([]string, 0, len(items))
	for _, item := range items {
		lines = append(lines, indent+item.Label+": "+item.Value)
	}
	return lines
}

func kpiText(c Component, indent string) []string {
	var items []KPIItem
	_ = jsonUnmarshalItems(c.Items, &items)
	lines := make([]string, 0, len(items))
	for _, item := range items {
		line := item.Label + ": " + item.Value.Display() + unitSuffix(item.Unit)
		if item.Delta != "" {
			line += " (" + item.Delta + ")"
		}
		if item.Hint != "" {
			line += " — " + item.Hint
		}
		lines = append(lines, indent+line)
	}
	return lines
}

func progressText(c Component, indent string) []string {
	var items []ProgressItem
	_ = jsonUnmarshalItems(c.Items, &items)
	lines := make([]string, 0, len(items))
	for _, item := range items {
		max := item.Max
		if max <= 0 {
			max = 100
		}
		share := 0.0
		if max > 0 {
			share = item.Value / max
		}
		filled := int(math.Round(share * 10))
		if filled > 10 {
			filled = 10
		}
		if filled < 0 {
			filled = 0
		}
		bar := strings.Repeat("█", filled) + strings.Repeat("░", 10-filled)
		lines = append(lines, fmt.Sprintf("%s%s %s %s/%s", indent, bar, item.Label, formatNumber(item.Value), formatNumber(max)))
	}
	return lines
}

func accordionText(c Component, indent string) []string {
	var items []AccordionItem
	_ = jsonUnmarshalItems(c.Items, &items)
	lines := make([]string, 0, len(items))
	for _, item := range items {
		lines = append(lines, indent+"▸ "+item.Label+":")
		lines = append(lines, indentLines(prefixLines("", item.Content), indent+"  ")...)
	}
	return lines
}

func chartText(c Component, data ChartData, indent string) []string {
	var props ChartProps
	_ = decodeStrict(c.Props, &props)
	labels := data.Labels
	values := data.Values
	name := props.Title
	if len(values) == 0 && len(data.Series) == 1 {
		values = data.Series[0].Values
		name = labelOr(name, data.Series[0].Name)
		if len(labels) == 0 {
			labels = syntheticLabels(len(values))
		}
	}
	lines := make([]string, 0, len(values)+2)
	if name != "" {
		lines = append(lines, indent+name+" ("+props.ChartType+"):")
	}
	if len(values) > 0 && len(labels) == len(values) {
		total := 0.0
		for _, value := range values {
			total += value
		}
		for i, value := range values {
			share := ""
			if props.ChartType == "pie" || props.ChartType == "donut" {
				if total > 0 {
					share = fmt.Sprintf(", %s%%", formatNumber(math.Round(value/total*1000)/10))
				}
			}
			lines = append(lines, fmt.Sprintf("%s  %s: %s%s%s", indent, labels[i], formatNumber(value), unitSuffix(data.Unit), share))
		}
	} else if len(data.Series) > 1 {
		for _, series := range data.Series {
			lines = append(lines, fmt.Sprintf("%s  %s: %s", indent, series.Name, formatNumbers(series.Values)))
		}
	}
	if len(lines) == 0 {
		lines = append(lines, indent+"[chart]")
	}
	return lines
}

func flowText(c Component, indent string) []string {
	var data FlowData
	_ = decodeStrict(c.Data, &data)
	labels := map[string]string{}
	for _, node := range data.Nodes {
		labels[node.ID] = node.Label
	}
	lines := make([]string, 0, len(data.Edges)+len(data.Nodes))
	for _, edge := range data.Edges {
		line := indent + labelOr(labels[edge.From], edge.From) + " → " + labelOr(labels[edge.To], edge.To)
		if edge.Label != "" {
			line += " (" + edge.Label + ")"
		}
		lines = append(lines, line)
	}
	if len(data.Edges) == 0 {
		for _, node := range data.Nodes {
			lines = append(lines, indent+"• "+node.Label)
		}
	}
	return lines
}

func sequenceText(c Component, indent string) []string {
	var data SequenceData
	_ = decodeStrict(c.Data, &data)
	labels := map[string]string{}
	for _, p := range data.Participants {
		labels[p.ID] = p.Label
	}
	lines := make([]string, 0, len(data.Messages))
	for _, msg := range data.Messages {
		arrow := "→"
		if msg.Kind == "return" {
			arrow = "⇠"
		} else if msg.Kind == "async" {
			arrow = "⇢"
		}
		line := indent + labelOr(labels[msg.From], msg.From) + " " + arrow + " " + labelOr(labels[msg.To], msg.To)
		if msg.Label != "" {
			line += ": " + msg.Label
		}
		lines = append(lines, line)
	}
	return lines
}

func timelineText(c Component, indent string) []string {
	var items []TimelineItem
	_ = jsonUnmarshalItems(c.Items, &items)
	lines := make([]string, 0, len(items))
	for _, item := range items {
		line := indent + item.At + " — " + item.Label
		if item.Description != "" {
			line += ": " + item.Description
		}
		lines = append(lines, line)
	}
	return lines
}

func ganttText(c Component, indent string) []string {
	var data GanttData
	_ = decodeStrict(c.Data, &data)
	lines := make([]string, 0, len(data.Tasks))
	for _, task := range data.Tasks {
		line := indent + task.Start + " – " + task.End + "  " + task.Label
		if task.Assignee != "" {
			line += " (" + task.Assignee + ")"
		}
		if task.Status != "" && task.Status != statusNeutral {
			line += " [" + strings.ToUpper(statusWord(task.Status)) + "]"
		}
		lines = append(lines, line)
	}
	return lines
}

// describeActions renders the declared actions of one component for the text
// fallback — the human must still learn what the interactive form would do.
func describeActions(actions []Action) []string {
	out := make([]string, 0, len(actions))
	for _, action := range actions {
		label := action.Label
		if label == "" {
			label = action.Type
		}
		out = append(out, label)
	}
	return out
}

func mindmapText(c Component, indent string) []string {
	var data MindmapData
	_ = decodeStrict(c.Data, &data)
	lines := make([]string, 0, 8)
	var walk func(node MindNode, depth int)
	walk = func(node MindNode, depth int) {
		prefix := strings.Repeat("  ", depth) + "- "
		if depth == 0 {
			prefix = ""
		}
		line := indent + prefix + node.Label
		if node.Note != "" {
			line += " (" + node.Note + ")"
		}
		lines = append(lines, line)
		for _, child := range node.Children {
			walk(child, depth+1)
		}
	}
	walk(data.Root, 0)
	if len(lines) == 0 {
		lines = append(lines, indent+"[mindmap]")
	}
	return lines
}

func stateText(c Component, indent string) []string {
	var data StateData
	_ = decodeStrict(c.Data, &data)
	lines := make([]string, 0, len(data.Transitions)+2)
	if data.Initial != "" {
		lines = append(lines, indent+"start → "+data.Initial)
	}
	for _, transition := range data.Transitions {
		line := indent + transition.From + " → " + transition.To
		if transition.Label != "" {
			line += ": " + transition.Label
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		lines = append(lines, indent+"[state diagram]")
	}
	return lines
}

func erText(c Component, indent string) []string {
	var data ERData
	_ = decodeStrict(c.Data, &data)
	lines := make([]string, 0, len(data.Entities)+len(data.Relations))
	for _, entity := range data.Entities {
		line := indent + "• " + entity.Name
		if len(entity.Attributes) > 0 {
			line += ": " + strings.Join(entity.Attributes, ", ")
		}
		lines = append(lines, line)
	}
	for _, relation := range data.Relations {
		line := indent + relation.From + " — " + relation.To
		parts := []string{}
		if relation.Cardinality != "" {
			parts = append(parts, relation.Cardinality)
		}
		if relation.Label != "" {
			parts = append(parts, relation.Label)
		}
		if len(parts) > 0 {
			line += " (" + strings.Join(parts, ", ") + ")"
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		lines = append(lines, indent+"[er diagram]")
	}
	return lines
}

func classText(c Component, indent string) []string {
	var data ClassData
	_ = decodeStrict(c.Data, &data)
	lines := make([]string, 0, len(data.Classes)*2+len(data.Relations))
	for _, class := range data.Classes {
		lines = append(lines, indent+"• "+class.Name)
		for _, attribute := range class.Attributes {
			lines = append(lines, indent+"    "+attribute)
		}
		for _, method := range class.Methods {
			lines = append(lines, indent+"    "+method)
		}
	}
	for _, relation := range data.Relations {
		line := indent + relation.From + " — " + relation.To
		parts := []string{}
		if relation.Kind != "" {
			parts = append(parts, relation.Kind)
		}
		if relation.Label != "" {
			parts = append(parts, relation.Label)
		}
		if len(parts) > 0 {
			line += " (" + strings.Join(parts, ", ") + ")"
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		lines = append(lines, indent+"[class diagram]")
	}
	return lines
}

func formText(c Component, indent string) []string {
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
	lines := make([]string, 0, len(schema.Properties)+2)
	if c.Content != "" {
		lines = append(lines, indent+c.Content)
	}
	required := map[string]bool{}
	for _, name := range schema.Required {
		required[name] = true
	}
	for name := range schema.Properties {
		fieldType := "text"
		var field struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(schema.Properties[name], &field)
		if field.Type != "" {
			fieldType = field.Type
		}
		marker := ""
		if required[name] {
			marker = " *"
		}
		lines = append(lines, indent+"  • "+name+marker+" ("+fieldType+")")
	}
	submit := props.Submit
	if submit == "" {
		submit = "Submit"
	}
	target := ""
	for _, action := range c.Actions {
		if action.Type == "submit" && action.Target != "" {
			target = action.Target
		}
	}
	lines = append(lines, indent+"["+submit+"] — answer in the Temporality UI"+mapString(" (", target, ")"))
	return lines
}

// mapString joins only when the middle value is non-empty.
func mapString(left, middle, right string) string {
	if middle == "" {
		return ""
	}
	return left + middle + right
}

func statusWord(status string) string {
	if status == "" {
		return statusNeutral
	}
	return status
}

func unitSuffix(unit string) string {
	if unit == "" {
		return ""
	}
	return " " + unit
}

func labelOr(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func syntheticLabels(count int) []string {
	labels := make([]string, count)
	for i := range labels {
		labels[i] = strconv.Itoa(i + 1)
	}
	return labels
}

func formatNumber(value float64) string {
	if value == math.Trunc(value) && math.Abs(value) < 1e15 {
		return strconv.FormatInt(int64(value), 10)
	}
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func formatNumbers(values []float64) string {
	parts := make([]string, len(values))
	for i, value := range values {
		parts[i] = formatNumber(value)
	}
	return strings.Join(parts, ", ")
}
