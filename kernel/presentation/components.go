package presentation

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Typed component shapes. Every data/props/items payload of every component
// type has a Go struct here; validation decodes strictly (unknown fields are
// refused), so what reaches the renderers is exactly what these types can
// express. The structs are the machine-checked equivalent of the JSON Schema.

// FlexValue is an IR scalar that may arrive as a JSON number or string
// (KPI values are "942" or "99.9%" — both legitimate).
type FlexValue struct {
	Text  string
	Num   float64
	IsNum bool
}

// UnmarshalJSON accepts a JSON number or string.
func (f *FlexValue) UnmarshalJSON(raw []byte) error {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "null" {
		return nil
	}
	if num, err := strconv.ParseFloat(trimmed, 64); err == nil {
		f.Num = num
		f.IsNum = true
		f.Text = trimmed
		return nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return fmt.Errorf("value must be a number or string")
	}
	f.Text = text
	return nil
}

// MarshalJSON keeps the original scalar kind.
func (f FlexValue) MarshalJSON() ([]byte, error) {
	if f.IsNum {
		return []byte(f.Text), nil
	}
	return json.Marshal(f.Text)
}

// Display renders the value for text renderers.
func (f FlexValue) Display() string {
	return f.Text
}

// ListItem is one list entry: a string in items decodes into Label.
type ListItem struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// UnmarshalJSON accepts both plain strings and {label, description} objects.
// The object path decodes strictly: unknown item fields are refused (§14.3).
func (l *ListItem) UnmarshalJSON(raw []byte) error {
	trimmed := strings.TrimSpace(string(raw))
	if strings.HasPrefix(trimmed, "\"") {
		return json.Unmarshal(raw, &l.Label)
	}
	type alias ListItem
	var plain alias
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&plain); err != nil {
		return err
	}
	*l = ListItem(plain)
	return nil
}

// KeyValueItem is one key-value entry (docs/presentation-engine.md §6.2).
type KeyValueItem struct {
	Label  string `json:"label"`
	Value  string `json:"value"`
	Status string `json:"status,omitempty"`
}

// KPIItem is one key metric (§4.2 example).
type KPIItem struct {
	Label  string    `json:"label"`
	Value  FlexValue `json:"value"`
	Status string    `json:"status,omitempty"`
	Delta  string    `json:"delta,omitempty"`
	Unit   string    `json:"unit,omitempty"`
	Hint   string    `json:"hint,omitempty"`
}

// ProgressItem is one progress indicator.
type ProgressItem struct {
	Label  string  `json:"label"`
	Value  float64 `json:"value"`
	Max    float64 `json:"max,omitempty"`
	Status string  `json:"status,omitempty"`
	Unit   string  `json:"unit,omitempty"`
}

// CompareField is one criterion/value pair inside a comparison option.
type CompareField struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// ComparisonItem is one compared option.
type ComparisonItem struct {
	Label  string         `json:"label"`
	Status string         `json:"status,omitempty"`
	Fields []CompareField `json:"fields"`
}

// TimelineItem is one chronology entry (§6.5).
type TimelineItem struct {
	Label       string `json:"label"`
	At          string `json:"at"`
	Status      string `json:"status,omitempty"`
	Description string `json:"description,omitempty"`
}

// AccordionItem is one collapsible section.
type AccordionItem struct {
	Label   string `json:"label"`
	Content string `json:"content"`
}

// GalleryItem is one gallery image.
type GalleryItem struct {
	Src     string `json:"src"`
	Alt     string `json:"alt,omitempty"`
	Caption string `json:"caption,omitempty"`
}

// TableColumn describes one table column (§6.2: typed values, alignment).
type TableColumn struct {
	Key   string `json:"key"`
	Title string `json:"title,omitempty"`
	Align string `json:"align,omitempty"` // left | center | right
	Kind  string `json:"kind,omitempty"`  // text | number | status
}

// TableCell is one table cell: a scalar or a rich object.
type TableCell struct {
	Text   string  `json:"-"`
	Number float64 `json:"-"`
	IsNum  bool    `json:"-"`
	Bool   bool    `json:"-"`
	IsNull bool    `json:"-"`
	Status string  `json:"status,omitempty"`
	Href   string  `json:"href,omitempty"`
}

// UnmarshalJSON accepts scalars and {text, status, href} objects.
func (c *TableCell) UnmarshalJSON(raw []byte) error {
	type richCell struct {
		Text   *string `json:"text"`
		Status string  `json:"status,omitempty"`
		Href   string  `json:"href,omitempty"`
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "null" {
		c.IsNull = true
		return nil
	}
	if strings.HasPrefix(trimmed, "{") {
		var obj richCell
		if err := json.Unmarshal(raw, &obj); err != nil {
			return err
		}
		if obj.Text != nil {
			c.Text = *obj.Text
		}
		c.Status = obj.Status
		c.Href = obj.Href
		return nil
	}
	if strings.HasPrefix(trimmed, "\"") {
		if err := json.Unmarshal(raw, &c.Text); err != nil {
			return err
		}
		return nil
	}
	if trimmed == "true" || trimmed == "false" {
		if err := json.Unmarshal(raw, &c.Bool); err != nil {
			return err
		}
		if c.Bool {
			c.Text = "true"
		} else {
			c.Text = "false"
		}
		return nil
	}
	num, err := strconv.ParseFloat(trimmed, 64)
	if err != nil {
		return fmt.Errorf("cell must be a scalar or {text,status,href}")
	}
	c.Number = num
	c.IsNum = true
	c.Text = trimmed
	return nil
}

// Display renders the cell for text renderers.
func (c TableCell) Display() string {
	return c.Text
}

// TableData is the table component's data payload.
type TableData struct {
	Columns []TableColumn          `json:"columns"`
	Rows    []map[string]TableCell `json:"rows"`
	// Title is the optional table caption.
	Title string `json:"title,omitempty"`
}

// ChartSeries is one named data series.
type ChartSeries struct {
	Name   string    `json:"name"`
	Values []float64 `json:"values"`
}

// ChartData is the chart component's data payload (§4.2 example shape:
// labels + values; Series carries multi-series plots).
type ChartData struct {
	Labels []string      `json:"labels,omitempty"`
	Values []float64     `json:"values,omitempty"`
	Series []ChartSeries `json:"series,omitempty"`
	Unit   string        `json:"unit,omitempty"`
	XTitle string        `json:"x_title,omitempty"`
	YTitle string        `json:"y_title,omitempty"`
}

// ChartProps is the chart component's display contract: the chart kind and
// semantic toggles only — geometry stays with the renderer (§8.2).
type ChartProps struct {
	ChartType  string `json:"chartType,omitempty"` // line | bar | pie | donut | area
	Title      string `json:"title,omitempty"`
	ShowValues bool   `json:"showValues,omitempty"`
	ShowLegend *bool  `json:"showLegend,omitempty"`
}

// GaugeData is the gauge component's data payload.
type GaugeData struct {
	Value float64 `json:"value"`
	Min   float64 `json:"min,omitempty"`
	Max   float64 `json:"max,omitempty"`
	Label string  `json:"label,omitempty"`
	Unit  string  `json:"unit,omitempty"`
}

// GanttTask is one schedule bar (§6.5: intervals, statuses, assignees,
// dependencies).
type GanttTask struct {
	ID        string   `json:"id,omitempty"`
	Label     string   `json:"label"`
	Start     string   `json:"start"`
	End       string   `json:"end"`
	Status    string   `json:"status,omitempty"`
	Assignee  string   `json:"assignee,omitempty"`
	DependsOn []string `json:"dependsOn,omitempty"`
}

// GanttData is the gantt component's data payload.
type GanttData struct {
	Tasks []GanttTask `json:"tasks"`
}

// GanttProps is the gantt display contract.
type GanttProps struct {
	Scale string `json:"scale,omitempty"` // day | week | month
	Title string `json:"title,omitempty"`
}

// FlowNode is one flowchart node.
type FlowNode struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Shape  string `json:"shape,omitempty"` // rect | round | diamond
	Status string `json:"status,omitempty"`
}

// FlowEdge is one directed connection.
type FlowEdge struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Label  string `json:"label,omitempty"`
	Dashed bool   `json:"dashed,omitempty"`
}

// FlowData is the flowchart component's data payload (§6.4: nodes, directed
// links, labels, node types).
type FlowData struct {
	Nodes []FlowNode `json:"nodes"`
	Edges []FlowEdge `json:"edges,omitempty"`
}

// FlowProps is the flowchart display contract.
type FlowProps struct {
	Direction string `json:"direction,omitempty"` // TB | LR
	Title     string `json:"title,omitempty"`
}

// SequenceParticipant is one lifeline.
type SequenceParticipant struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// SequenceMessage is one exchange between lifelines.
type SequenceMessage struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Label string `json:"label,omitempty"`
	Kind  string `json:"kind,omitempty"` // sync | async | return
}

// SequenceData is the sequence-diagram component's data payload.
type SequenceData struct {
	Participants []SequenceParticipant `json:"participants"`
	Messages     []SequenceMessage     `json:"messages"`
}

// PlainProps carries the shared display props of text components.
type HeadingProps struct {
	Level int `json:"level,omitempty"`
}

type ListProps struct {
	Ordered bool `json:"ordered,omitempty"`
}

type QuoteProps struct {
	Attribution string `json:"attribution,omitempty"`
}

type CodeProps struct {
	Language string `json:"language,omitempty"`
}

type LinkProps struct {
	Href        string `json:"href"`
	Description string `json:"description,omitempty"`
}

type BadgeProps struct {
	Status string `json:"status,omitempty"`
}

type CardProps struct {
	Title string `json:"title,omitempty"`
}

type CardGridProps struct {
	Columns int `json:"columns,omitempty"`
}

type StatusProps struct {
	Status string `json:"status,omitempty"`
	Detail string `json:"detail,omitempty"`
}

type AlertProps struct {
	Severity string `json:"severity,omitempty"`
	Title    string `json:"title,omitempty"`
}

type ImageProps struct {
	Src     string `json:"src"`
	Caption string `json:"caption,omitempty"`
}

type GalleryProps struct {
	Columns int `json:"columns,omitempty"`
}

type FileProps struct {
	Name       string `json:"name"`
	Href       string `json:"href,omitempty"`
	ArtifactID string `json:"artifact_id,omitempty"`
	Size       string `json:"size,omitempty"`
}

// Enum vocabularies shared across types.
const (
	statusNeutral = "neutral"
	statusSuccess = "success"
	statusInfo    = "info"
	statusWarning = "warning"
	statusDanger  = "danger"
)

var statusValues = map[string]bool{
	statusNeutral: true, statusSuccess: true, statusInfo: true, statusWarning: true, statusDanger: true,
}

var severityValues = map[string]bool{
	statusInfo: true, statusSuccess: true, statusWarning: true, statusDanger: true,
}

var chartTypes = map[string]bool{"line": true, "bar": true, "pie": true, "donut": true, "area": true}

var chartAliasTypes = map[string]string{
	"line-chart": "line", "bar-chart": "bar", "pie-chart": "pie", "area-chart": "area", "donut-chart": "donut",
}

// planningAliasDefaults applies the implied props of planning aliases:
// a roadmap IS a gantt with a month scale (§6.5).
func planningAliasDefaults(c *Component, canonical string) {
	if canonical != "gantt" {
		return
	}
	if c.Type != "roadmap" {
		return
	}
	var props GanttProps
	if len(c.Props) > 0 {
		_ = json.Unmarshal(c.Props, &props)
	}
	if props.Scale == "" {
		props.Scale = "month"
		encoded, _ := json.Marshal(props)
		c.Props = encoded
	}
}

// chartDefaultPoints bounds chart rendering effort per chart (matching the
// default MaxChartPoints).
const chartDefaultPoints = 200

// collectionLimits are the per-collection caps the validator uses; they track
// the default Limits and are checked against policy.effective() where a
// dedicated field exists.
func checkCount(v *validation, field string, got, max int) {
	if got > max {
		v.failf("%s holds %d entries, limit is %d", field, got, max)
	}
}

// checkEnum validates one enum-typed string field.
func checkEnum(v *validation, field, raw string, allowed map[string]bool) string {
	if raw == "" {
		return ""
	}
	if !allowed[raw] {
		v.failf("%s: %q is not one of %s", field, raw, enumNames(allowed))
		return ""
	}
	return raw
}

func enumNames(allowed map[string]bool) string {
	names := make([]string, 0, len(allowed))
	for name := range allowed {
		names = append(names, name)
	}
	// Deterministic order for stable validation messages.
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j] < names[j-1]; j-- {
			names[j], names[j-1] = names[j-1], names[j]
		}
	}
	return strings.Join(names, "|")
}

// checkStatusField validates an optional status/severity value.
func checkStatusField(v *validation, field, raw string) string {
	return checkEnum(v, field, raw, statusValues)
}

// checkImageURL applies the URL safety rules plus the media policy
// (docs/presentation-engine.md §14.8): external images need policy consent
// and, when configured, a host allowlist match.
func checkImageURL(v *validation, field, raw string, required bool) {
	if raw == "" {
		if required {
			v.failf("%s is required", field)
		}
		return
	}
	if !safeURL(raw) {
		v.failf("%s: %q is not an allowed URL (http(s) or in-app path)", field, truncateLabel(raw))
		return
	}
	external := strings.Contains(raw, "://")
	if !external {
		return
	}
	if !v.policy.AllowExternalImages {
		v.failf("%s: external images are disabled by policy", field)
		return
	}
	if len(v.policy.AllowedImageHosts) == 0 {
		return
	}
	host := hostOf(raw)
	for _, allowed := range v.policy.AllowedImageHosts {
		if host == allowed {
			return
		}
	}
	v.failf("%s: host %q is not in the allowed image hosts", field, host)
}

func hostOf(raw string) string {
	parsed := raw
	if idx := strings.Index(parsed, "://"); idx >= 0 {
		parsed = parsed[idx+3:]
	}
	if idx := strings.IndexAny(parsed, "/?#"); idx >= 0 {
		parsed = parsed[:idx]
	}
	return strings.ToLower(parsed)
}

// parseIRDate accepts the two honest date forms an agent may send: a calendar
// day or a full timestamp. Layout-precision-only forms ("2026-03") are
// refused — ambiguous dates render wrong bars.
func parseIRDate(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	for _, layout := range []string{time.RFC3339, "2006-01-02", "2006-01-02T15:04"} {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

// The per-component validators. Each one decodes its typed shapes strictly,
// checks the safety rules and leaves the normalized payload in place.
var componentValidators map[string]func(v *validation, c *Component)

func init() {
	componentValidators = map[string]func(v *validation, c *Component){
		"text":             textValidator,
		"markdown":         contentValidator("markdown"),
		"paragraph":        contentValidator("paragraph"),
		"heading":          headingValidator,
		"list":             listValidator,
		"quote":            quoteValidator,
		"code":             codeValidator,
		"link":             linkValidator,
		"badge":            badgeValidator,
		"divider":          dividerValidator,
		"table":            tableValidator,
		"card":             cardValidator,
		"card-grid":        cardGridValidator,
		"comparison":       comparisonValidator,
		"key-value":        keyValueValidator,
		"kpi":              kpiValidator,
		"progress":         progressValidator,
		"status":           statusValidator,
		"alert":            alertValidator,
		"accordion":        accordionValidator,
		"chart":            chartValidator,
		"gauge":            gaugeValidator,
		"flowchart":        flowValidator,
		"sequence-diagram": sequenceValidator,
		"mindmap":          mindmapValidator,
		"dependency-graph": dependencyGraphValidator,
		"state-diagram":    stateValidator,
		"er-diagram":       erValidator,
		"class-diagram":    classValidator,
		"form":             formValidator,
		"timeline":         timelineValidator,
		"gantt":            ganttValidator,
		"image":            imageValidator,
		"image-gallery":    galleryValidator,
		"file":             fileValidator,
		"button":           buttonValidator,
	}
}

// aliasDefaults applies the implied display props of alias slugs: a
// "pie-chart" component IS a chart with chartType "pie" (docs/presentation-
// engine.md §6.3 catalog uses explicit slugs; the IR canonical form is chart).
func aliasDefaults(c *Component, canonical string) bool {
	if canonical != "chart" {
		return false
	}
	if implied, ok := chartAliasTypes[c.Type]; ok {
		var props ChartProps
		if len(c.Props) > 0 {
			_ = json.Unmarshal(c.Props, &props)
		}
		if props.ChartType == "" {
			props.ChartType = implied
			encoded, _ := json.Marshal(props)
			c.Props = encoded
		}
		return true
	}
	return false
}

// decodeEmptyProps enforces the "no props" contract of components that do
// not define any display parameters — an agent cannot smuggle styling keys
// onto a component that has none (§8.2: agents never set CSS).
func decodeEmptyProps(v *validation, c *Component) {
	var empty struct{}
	decodeProps(v, c, &empty)
}

func textValidator(v *validation, c *Component) {
	checkContent(v, "content", c.Content)
	decodeEmptyProps(v, c)
}

func contentValidator(name string) func(v *validation, c *Component) {
	return func(v *validation, c *Component) {
		checkContent(v, "content", c.Content)
		decodeEmptyProps(v, c)
	}
}

func headingValidator(v *validation, c *Component) {
	checkContent(v, "content", c.Content)
	var props HeadingProps
	if decodeProps(v, c, &props) {
		if props.Level < 0 || props.Level > 6 {
			v.failf("props.level must be 1..6")
		}
	}
}

func listValidator(v *validation, c *Component) {
	var props ListProps
	decodeProps(v, c, &props)
	var items []ListItem
	count := decodeItems(v, c, "items", &items, v.limits.MaxListItems)
	checkCount(v, "items", count, v.limits.MaxListItems)
	for i, item := range items {
		checkLabel(v, fmt.Sprintf("items[%d].label", i), item.Label)
		checkLabel(v, fmt.Sprintf("items[%d].description", i), item.Description)
	}
	if count == 0 {
		v.failf("items must hold at least one entry")
	}
}

func quoteValidator(v *validation, c *Component) {
	checkContent(v, "content", c.Content)
	var props QuoteProps
	if decodeProps(v, c, &props) {
		checkLabel(v, "props.attribution", props.Attribution)
	}
}

func codeValidator(v *validation, c *Component) {
	checkContent(v, "content", c.Content)
	var props CodeProps
	if decodeProps(v, c, &props) {
		checkShortString(v, "props.language", props.Language, 40)
	}
}

func linkValidator(v *validation, c *Component) {
	checkLabel(v, "content", c.Content)
	var props LinkProps
	if decodeProps(v, c, &props) {
		checkURL(v, "props.href", props.Href, true)
		checkLabel(v, "props.description", props.Description)
	} else {
		v.failf("props.href is required")
	}
}

func badgeValidator(v *validation, c *Component) {
	checkLabel(v, "content", c.Content)
	var props BadgeProps
	if decodeProps(v, c, &props) {
		checkStatusField(v, "props.status", props.Status)
	}
}

func dividerValidator(v *validation, c *Component) {
	decodeEmptyProps(v, c)
}

func cardValidator(v *validation, c *Component) {
	var props CardProps
	if decodeProps(v, c, &props) {
		checkLabel(v, "props.title", props.Title)
	}
}

func cardGridValidator(v *validation, c *Component) {
	var props CardGridProps
	if decodeProps(v, c, &props) {
		if props.Columns < 0 || props.Columns > 4 {
			v.failf("props.columns must be 1..4")
		}
	}
}

func comparisonValidator(v *validation, c *Component) {
	decodeEmptyProps(v, c)
	var items []ComparisonItem
	count := decodeItems(v, c, "items", &items, v.limits.MaxKPIItems)
	checkCount(v, "items", count, v.limits.MaxKPIItems)
	for i, item := range items {
		checkLabel(v, fmt.Sprintf("items[%d].label", i), item.Label)
		checkStatusField(v, fmt.Sprintf("items[%d].status", i), item.Status)
		for j, field := range item.Fields {
			checkLabel(v, fmt.Sprintf("items[%d].fields[%d].label", i, j), field.Label)
			checkLabel(v, fmt.Sprintf("items[%d].fields[%d].value", i, j), field.Value)
		}
	}
	if count < 2 {
		v.failf("items must hold at least two compared options")
	}
}

func keyValueValidator(v *validation, c *Component) {
	decodeEmptyProps(v, c)
	var items []KeyValueItem
	count := decodeItems(v, c, "items", &items, v.limits.MaxListItems)
	checkCount(v, "items", count, v.limits.MaxListItems)
	for i, item := range items {
		checkLabel(v, fmt.Sprintf("items[%d].label", i), item.Label)
		checkLabel(v, fmt.Sprintf("items[%d].value", i), item.Value)
		checkStatusField(v, fmt.Sprintf("items[%d].status", i), item.Status)
	}
	if count == 0 {
		v.failf("items must hold at least one entry")
	}
}

func kpiValidator(v *validation, c *Component) {
	decodeEmptyProps(v, c)
	var items []KPIItem
	count := decodeItems(v, c, "items", &items, v.limits.MaxKPIItems)
	checkCount(v, "items", count, v.limits.MaxKPIItems)
	for i, item := range items {
		checkLabel(v, fmt.Sprintf("items[%d].label", i), item.Label)
		checkLabel(v, fmt.Sprintf("items[%d].delta", i), item.Delta)
		checkLabel(v, fmt.Sprintf("items[%d].unit", i), item.Unit)
		checkLabel(v, fmt.Sprintf("items[%d].hint", i), item.Hint)
		checkStatusField(v, fmt.Sprintf("items[%d].status", i), item.Status)
	}
	if count == 0 {
		v.failf("items must hold at least one entry")
	}
}

func progressValidator(v *validation, c *Component) {
	decodeEmptyProps(v, c)
	var items []ProgressItem
	count := decodeItems(v, c, "items", &items, v.limits.MaxKPIItems)
	checkCount(v, "items", count, v.limits.MaxKPIItems)
	for i, item := range items {
		checkLabel(v, fmt.Sprintf("items[%d].label", i), item.Label)
		if item.Value < 0 || math.IsNaN(item.Value) || math.IsInf(item.Value, 0) {
			v.failf("items[%d].value must be a finite number >= 0", i)
		}
		max := item.Max
		if max == 0 {
			max = 100
		}
		if max <= 0 {
			v.failf("items[%d].max must be positive", i)
		} else if item.Value > max {
			v.failf("items[%d].value exceeds max (%g > %g)", i, item.Value, max)
		}
		checkLabel(v, fmt.Sprintf("items[%d].unit", i), item.Unit)
		checkStatusField(v, fmt.Sprintf("items[%d].status", i), item.Status)
	}
	if count == 0 {
		v.failf("items must hold at least one entry")
	}
}

func statusValidator(v *validation, c *Component) {
	checkLabel(v, "content", c.Content)
	var props StatusProps
	if decodeProps(v, c, &props) {
		checkStatusField(v, "props.status", props.Status)
		checkLabel(v, "props.detail", props.Detail)
	}
}

func alertValidator(v *validation, c *Component) {
	checkContent(v, "content", c.Content)
	var props AlertProps
	if decodeProps(v, c, &props) {
		if props.Severity == "" {
			props.Severity = statusInfo
		}
		if !severityValues[props.Severity] {
			v.failf("props.severity: %q is not one of danger|info|success|warning", props.Severity)
		}
		checkLabel(v, "props.title", props.Title)
	}
}

func accordionValidator(v *validation, c *Component) {
	decodeEmptyProps(v, c)
	var items []AccordionItem
	count := decodeItems(v, c, "items", &items, v.limits.MaxListItems)
	checkCount(v, "items", count, v.limits.MaxListItems)
	for i, item := range items {
		checkLabel(v, fmt.Sprintf("items[%d].label", i), item.Label)
		checkContent(v, fmt.Sprintf("items[%d].content", i), item.Content)
	}
	if count == 0 {
		v.failf("items must hold at least one entry")
	}
}

func tableValidator(v *validation, c *Component) {
	decodeEmptyProps(v, c)
	var data TableData
	if !decodeData(v, c, &data) {
		v.failf("data with columns and rows is required")
		return
	}
	checkLabel(v, "data.title", data.Title)
	if len(data.Columns) == 0 {
		v.failf("data.columns must hold at least one column")
		return
	}
	if len(data.Columns) > v.limits.MaxTableCols {
		v.failf("data.columns holds %d columns, limit is %d", len(data.Columns), v.limits.MaxTableCols)
		return
	}
	keys := map[string]bool{}
	for i, col := range data.Columns {
		if col.Key == "" {
			v.failf("data.columns[%d].key is required", i)
			continue
		}
		if keys[col.Key] {
			v.failf("data.columns[%d]: duplicate column key %q", i, col.Key)
		}
		keys[col.Key] = true
		checkLabel(v, fmt.Sprintf("data.columns[%d].title", i), col.Title)
		checkEnum(v, fmt.Sprintf("data.columns[%d].align", i), col.Align, map[string]bool{"left": true, "center": true, "right": true})
		checkEnum(v, fmt.Sprintf("data.columns[%d].kind", i), col.Kind, map[string]bool{"text": true, "number": true, "status": true})
	}
	if len(data.Rows) == 0 {
		v.failf("data.rows must hold at least one row")
	}
	if len(data.Rows) > v.limits.MaxTableRows {
		v.failf("data.rows holds %d rows, limit is %d", len(data.Rows), v.limits.MaxTableRows)
		return
	}
	for i, row := range data.Rows {
		for key := range row {
			if !keys[key] {
				v.failf("data.rows[%d]: unknown column key %q", i, key)
			}
			cell := row[key]
			if cell.Href != "" && !safeURL(cell.Href) {
				v.failf("data.rows[%d].%s: href %q is not an allowed URL", i, key, truncateLabel(cell.Href))
			}
			checkStatusField(v, fmt.Sprintf("data.rows[%d].%s.status", i, key), cell.Status)
		}
	}
}

func chartValidator(v *validation, c *Component) {
	var props ChartProps
	decodeProps(v, c, &props)
	// The §4.2 example carries chartType on the component itself; validation
	// folds it into props so renderers read one place.
	if props.ChartType == "" && c.ChartType != "" {
		props.ChartType = c.ChartType
		if encoded, err := json.Marshal(props); err == nil {
			c.Props = encoded
		}
	}
	chartType := checkEnum(v, "props.chartType", props.ChartType, chartTypes)
	if chartType == "" {
		v.failf("props.chartType is required (line|bar|pie|donut|area)")
	}
	checkLabel(v, "props.title", props.Title)
	var data ChartData
	if !decodeData(v, c, &data) {
		v.failf("data with labels and values is required")
		return
	}
	checkLabel(v, "data.unit", data.Unit)
	checkLabel(v, "data.x_title", data.XTitle)
	checkLabel(v, "data.y_title", data.YTitle)
	for i, label := range data.Labels {
		checkLabel(v, fmt.Sprintf("data.labels[%d]", i), label)
	}
	points := 0
	if len(data.Values) > 0 {
		points += len(data.Values)
		checkNumbers(v, "data.values", data.Values)
	}
	if len(data.Series) > 0 {
		if len(data.Series) > 6 {
			v.failf("data.series holds %d series, limit is 6", len(data.Series))
		}
		for i, series := range data.Series {
			checkLabel(v, fmt.Sprintf("data.series[%d].name", i), series.Name)
			checkNumbers(v, fmt.Sprintf("data.series[%d].values", i), series.Values)
			points += len(series.Values)
			if len(data.Labels) > 0 && len(series.Values) != len(data.Labels) {
				v.failf("data.series[%d].values has %d points but data.labels has %d", i, len(series.Values), len(data.Labels))
			}
		}
	}
	if points == 0 {
		v.failf("data.values or data.series must carry at least one point")
	}
	if points > v.limits.MaxChartPoints {
		v.failf("chart holds %d points, limit is %d", points, v.limits.MaxChartPoints)
	}
	if len(data.Values) > 0 && len(data.Labels) > 0 && len(data.Values) != len(data.Labels) {
		v.failf("data.values has %d points but data.labels has %d", len(data.Values), len(data.Labels))
	}
	// Pie and donut slices are shares of a whole: negative and all-zero data
	// would silently misrepresent the result (§6.3 — visualization must not
	// distort the data).
	if chartType == "pie" || chartType == "donut" {
		values := data.Values
		if len(values) == 0 && len(data.Series) == 1 {
			values = data.Series[0].Values
		}
		sum := 0.0
		negative := false
		for _, value := range values {
			if value < 0 {
				negative = true
			}
			sum += value
		}
		if negative {
			v.failf("pie data must not hold negative values")
		}
		if sum <= 0 {
			v.failf("pie data must hold at least one positive value")
		}
	}
}

func checkNumbers(v *validation, field string, values []float64) {
	for i, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			v.failf("%s[%d] is not a finite number", field, i)
		}
	}
}

func gaugeValidator(v *validation, c *Component) {
	decodeEmptyProps(v, c)
	var data GaugeData
	if !decodeData(v, c, &data) {
		v.failf("data.value is required")
		return
	}
	max := data.Max
	if max == 0 {
		max = 100
	}
	if data.Min >= max {
		v.failf("data.max must be greater than data.min")
	}
	if data.Value < data.Min || data.Value > max {
		v.failf("data.value %g is outside [%g, %g]", data.Value, data.Min, max)
	}
	checkLabel(v, "data.label", data.Label)
	checkLabel(v, "data.unit", data.Unit)
}

func flowValidator(v *validation, c *Component) {
	var props FlowProps
	decodeProps(v, c, &props)
	if props.Direction == "" {
		props.Direction = "TB"
	}
	checkEnum(v, "props.direction", props.Direction, map[string]bool{"TB": true, "LR": true})
	checkLabel(v, "props.title", props.Title)
	var data FlowData
	if !decodeData(v, c, &data) {
		v.failf("data with nodes is required")
		return
	}
	const maxNodes = 100
	if len(data.Nodes) == 0 {
		v.failf("data.nodes must hold at least one node")
		return
	}
	if len(data.Nodes) > maxNodes {
		v.failf("data.nodes holds %d nodes, limit is %d", len(data.Nodes), maxNodes)
		return
	}
	if len(data.Edges) > 4*maxNodes {
		v.failf("data.edges holds %d edges, limit is %d", len(data.Edges), 4*maxNodes)
		return
	}
	ids := map[string]bool{}
	for i, node := range data.Nodes {
		if node.ID == "" {
			v.failf("data.nodes[%d].id is required", i)
			continue
		}
		if ids[node.ID] {
			v.failf("data.nodes[%d]: duplicate node id %q", i, node.ID)
		}
		ids[node.ID] = true
		checkLabel(v, fmt.Sprintf("data.nodes[%d].label", i), node.Label)
		checkEnum(v, fmt.Sprintf("data.nodes[%d].shape", i), node.Shape, map[string]bool{"rect": true, "round": true, "diamond": true})
		checkStatusField(v, fmt.Sprintf("data.nodes[%d].status", i), node.Status)
	}
	for i, edge := range data.Edges {
		if edge.From == "" || edge.To == "" {
			v.failf("data.edges[%d]: from and to are required", i)
			continue
		}
		if !ids[edge.From] {
			v.failf("data.edges[%d].from: unknown node %q", i, edge.From)
		}
		if !ids[edge.To] {
			v.failf("data.edges[%d].to: unknown node %q", i, edge.To)
		}
		checkLabel(v, fmt.Sprintf("data.edges[%d].label", i), edge.Label)
	}
	// Cycles would loop the layered layout forever; a flowchart with a cycle
	// is almost always an authoring bug, so refuse it here.
	if err := checkAcyclic(data.Nodes, data.Edges); err != nil {
		v.failf("data.edges: %v", err)
	}
}

// checkAcyclic rejects dependency cycles in flowchart and gantt data.
func checkAcyclic(nodes []FlowNode, edges []FlowEdge) error {
	adj := map[string][]string{}
	for _, edge := range edges {
		adj[edge.From] = append(adj[edge.From], edge.To)
	}
	state := map[string]int{} // 0 = unvisited, 1 = in progress, 2 = done
	var visit func(node string) error
	visit = func(node string) error {
		switch state[node] {
		case 1:
			return fmt.Errorf("cycle through node %q", node)
		case 2:
			return nil
		}
		state[node] = 1
		for _, next := range adj[node] {
			if err := visit(next); err != nil {
				return err
			}
		}
		state[node] = 2
		return nil
	}
	for _, node := range nodes {
		if err := visit(node.ID); err != nil {
			return err
		}
	}
	return nil
}

func sequenceValidator(v *validation, c *Component) {
	var props struct {
		Title string `json:"title,omitempty"`
	}
	decodeProps(v, c, &props)
	checkLabel(v, "props.title", props.Title)
	var data SequenceData
	if !decodeData(v, c, &data) {
		v.failf("data with participants and messages is required")
		return
	}
	const maxParticipants = 12
	const maxMessages = 200
	if len(data.Participants) < 2 {
		v.failf("data.participants must hold at least two participants")
	}
	if len(data.Participants) > maxParticipants {
		v.failf("data.participants holds %d participants, limit is %d", len(data.Participants), maxParticipants)
		return
	}
	if len(data.Messages) > maxMessages {
		v.failf("data.messages holds %d messages, limit is %d", len(data.Messages), maxMessages)
		return
	}
	ids := map[string]bool{}
	for i, p := range data.Participants {
		if p.ID == "" {
			v.failf("data.participants[%d].id is required", i)
			continue
		}
		if ids[p.ID] {
			v.failf("data.participants[%d]: duplicate id %q", i, p.ID)
		}
		ids[p.ID] = true
		checkLabel(v, fmt.Sprintf("data.participants[%d].label", i), p.Label)
	}
	for i, msg := range data.Messages {
		if !ids[msg.From] {
			v.failf("data.messages[%d].from: unknown participant %q", i, msg.From)
		}
		if !ids[msg.To] {
			v.failf("data.messages[%d].to: unknown participant %q", i, msg.To)
		}
		checkLabel(v, fmt.Sprintf("data.messages[%d].label", i), msg.Label)
		checkEnum(v, fmt.Sprintf("data.messages[%d].kind", i), msg.Kind, map[string]bool{"sync": true, "async": true, "return": true})
	}
	if len(data.Messages) == 0 {
		v.failf("data.messages must hold at least one message")
	}
}

func timelineValidator(v *validation, c *Component) {
	decodeEmptyProps(v, c)
	var items []TimelineItem
	count := decodeItems(v, c, "items", &items, v.limits.MaxListItems)
	checkCount(v, "items", count, v.limits.MaxListItems)
	for i, item := range items {
		checkLabel(v, fmt.Sprintf("items[%d].label", i), item.Label)
		checkString(v, fmt.Sprintf("items[%d].at", i), item.At, 60, false)
		if item.At == "" {
			v.failf("items[%d].at is required", i)
		}
		checkLabel(v, fmt.Sprintf("items[%d].description", i), item.Description)
		checkStatusField(v, fmt.Sprintf("items[%d].status", i), item.Status)
	}
	if count == 0 {
		v.failf("items must hold at least one entry")
	}
}

func ganttValidator(v *validation, c *Component) {
	var props GanttProps
	decodeProps(v, c, &props)
	checkEnum(v, "props.scale", props.Scale, map[string]bool{"day": true, "week": true, "month": true})
	checkLabel(v, "props.title", props.Title)
	var data GanttData
	if !decodeData(v, c, &data) {
		v.failf("data.tasks is required")
		return
	}
	const maxTasks = 100
	if len(data.Tasks) == 0 {
		v.failf("data.tasks must hold at least one task")
		return
	}
	if len(data.Tasks) > maxTasks {
		v.failf("data.tasks holds %d tasks, limit is %d", len(data.Tasks), maxTasks)
		return
	}
	ids := map[string]bool{}
	for i, task := range data.Tasks {
		checkLabel(v, fmt.Sprintf("data.tasks[%d].label", i), task.Label)
		checkLabel(v, fmt.Sprintf("data.tasks[%d].assignee", i), task.Assignee)
		checkStatusField(v, fmt.Sprintf("data.tasks[%d].status", i), task.Status)
		start, startErr := parseIRDate(task.Start)
		if !startErr {
			v.failf("data.tasks[%d].start: %q is not a date (YYYY-MM-DD or RFC3339)", i, task.Start)
		}
		end, endOK := parseIRDate(task.End)
		if !endOK {
			v.failf("data.tasks[%d].end: %q is not a date (YYYY-MM-DD or RFC3339)", i, task.End)
		} else if startErr && end.Before(start) {
			v.failf("data.tasks[%d]: end %s is before start %s", i, task.End, task.Start)
		}
		if task.ID != "" {
			if ids[task.ID] {
				v.failf("data.tasks[%d]: duplicate task id %q", i, task.ID)
			}
			ids[task.ID] = true
		}
	}
	for i, task := range data.Tasks {
		for j, dep := range task.DependsOn {
			if !ids[dep] {
				v.failf("data.tasks[%d].dependsOn[%d]: unknown task id %q", i, j, dep)
			}
		}
	}
}

func imageValidator(v *validation, c *Component) {
	checkLabel(v, "content", c.Content)
	var props ImageProps
	if decodeProps(v, c, &props) {
		checkImageURL(v, "props.src", props.Src, true)
		checkLabel(v, "props.caption", props.Caption)
	} else {
		v.failf("props.src is required")
	}
}

func galleryValidator(v *validation, c *Component) {
	var props GalleryProps
	decodeProps(v, c, &props)
	if props.Columns < 0 || props.Columns > 4 {
		v.failf("props.columns must be 1..4")
	}
	var items []GalleryItem
	count := decodeItems(v, c, "items", &items, v.limits.MaxGalleryItems)
	checkCount(v, "items", count, v.limits.MaxGalleryItems)
	for i, item := range items {
		checkImageURL(v, fmt.Sprintf("items[%d].src", i), item.Src, true)
		checkLabel(v, fmt.Sprintf("items[%d].alt", i), item.Alt)
		checkLabel(v, fmt.Sprintf("items[%d].caption", i), item.Caption)
	}
	if count == 0 {
		v.failf("items must hold at least one entry")
	}
}

func fileValidator(v *validation, c *Component) {
	checkLabel(v, "content", c.Content)
	var props FileProps
	if decodeProps(v, c, &props) {
		if props.Name == "" {
			v.failf("props.name is required")
		} else {
			checkLabel(v, "props.name", props.Name)
		}
		checkURL(v, "props.href", props.Href, false)
		checkShortString(v, "props.artifact_id", props.ArtifactID, 128)
		checkLabel(v, "props.size", props.Size)
	} else {
		v.failf("props.name is required")
	}
}

func buttonValidator(v *validation, c *Component) {
	checkLabel(v, "content", c.Content)
	decodeEmptyProps(v, c)
	if len(c.Actions) == 0 {
		v.failf("actions with one declared action is required")
	}
}
