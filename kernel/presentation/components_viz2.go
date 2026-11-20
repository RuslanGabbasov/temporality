package presentation

// Extended visualization components (docs/presentation-engine-ext.md §3-§4,
// §6): scatter/positioning map, heatmap, histogram, radar, sankey, swimlane,
// treemap. Same contract as the base catalog — typed data, typed props,
// strict validation, renderer-agnostic semantics. Geometry stays with the
// renderers (§8.2): props carry semantic toggles only.

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// Per-collection caps (§11): generous room for honest visuals, hard stops
// against layout bombs. The draw model bounds render cost by construction,
// but the validator refuses early with a specific message.
const (
	scatterPointLimit   = 300
	refLineLimit        = 6
	heatmapLineLimit    = 40
	heatmapCellLimit    = 400
	histogramValueLimit = 2000
	histogramBinLimit   = 60
	radarAxisLimit      = 12
	radarSeriesLimit    = 6
	sankeyNodeLimit     = 24
	sankeyLinkLimit     = 60
	swimlaneLaneLimit   = 12
	swimlaneNodeLimit   = 40
	treemapNodeLimit    = 64
	treemapDepthLimit   = 3
)

// EntityRef links one visual element to an existing Temporality entity
// (§6.4): the IR carries the pointer only — access checks and resolution
// happen at runtime, never inside the presentation engine.
type EntityRef struct {
	Type string `json:"type"` // artifact | task | project | run | agent
	ID   string `json:"id"`
}

var entityRefTypes = map[string]bool{"artifact": true, "task": true, "project": true, "run": true, "agent": true}

func checkEntityRef(v *validation, field string, ref *EntityRef) {
	if ref == nil {
		return
	}
	if !entityRefTypes[ref.Type] {
		v.failf("%s.type: %q is not one of artifact|task|project|run|agent", field, ref.Type)
	}
	if strings.TrimSpace(ref.ID) == "" {
		v.failf("%s.id is required", field)
		return
	}
	checkLabel(v, field+".id", ref.ID)
}

// ===== Scatter / Positioning map (§3.1, §4.1) =====

// ScatterPoint is one plotted object with independent coordinates — not a
// slot in a parallel values array.
type ScatterPoint struct {
	X         float64    `json:"x"`
	Y         float64    `json:"y"`
	Label     string     `json:"label,omitempty"`
	Group     string     `json:"group,omitempty"`
	Size      float64    `json:"size,omitempty"` // relative marker scale, 1 = default
	Highlight bool       `json:"highlight,omitempty"`
	Note      string     `json:"note,omitempty"`
	Ref       *EntityRef `json:"ref,omitempty"`
}

// RefLine is one vertical or horizontal reference line (§4.1 spatial
// elements).
type RefLine struct {
	Axis  string  `json:"axis"` // x = vertical line at an x value | y = horizontal
	At    float64 `json:"at"`
	Label string  `json:"label,omitempty"`
}

// ScatterData carries the coordinate objects.
type ScatterData struct {
	Points []ScatterPoint `json:"points"`
	Unit   string         `json:"unit,omitempty"`
	XTitle string         `json:"x_title,omitempty"`
	YTitle string         `json:"y_title,omitempty"`
	XMin   *float64       `json:"x_min,omitempty"`
	XMax   *float64       `json:"x_max,omitempty"`
	YMin   *float64       `json:"y_min,omitempty"`
	YMax   *float64       `json:"y_max,omitempty"`
}

// ScatterProps is the scatter display contract; quadrant mode splits the plot
// into four labeled regions (§4.1 — the positioning map is a profile of the
// shared coordinate renderer).
type ScatterProps struct {
	Title          string    `json:"title,omitempty"`
	ShowValues     *bool     `json:"showValues,omitempty"`
	ShowLegend     *bool     `json:"showLegend,omitempty"`
	Quadrant       bool      `json:"quadrant,omitempty"`
	QuadrantLabels []string  `json:"quadrantLabels,omitempty"` // top-left, top-right, bottom-left, bottom-right
	RefLines       []RefLine `json:"refLines,omitempty"`
}

// ===== Heatmap (§3.2) =====

// HeatmapData is the value matrix: rows × cols, nil cell = missing value
// (rendered as an explicit empty cell, never as zero).
type HeatmapData struct {
	Rows   []string     `json:"rows"`
	Cols   []string     `json:"cols"`
	Values [][]*float64 `json:"values"`
	Unit   string       `json:"unit,omitempty"`
	Min    *float64     `json:"min,omitempty"`
	Max    *float64     `json:"max,omitempty"`
}

// HeatmapProps is the heatmap display contract.
type HeatmapProps struct {
	Title      string `json:"title,omitempty"`
	ShowValues *bool  `json:"showValues,omitempty"`
}

// ===== Histogram (§3.3) =====

// HistogramData carries the raw sample; binning happens at render time from
// bins or binWidth (§3.3 — automatic when neither is set).
type HistogramData struct {
	Values   []float64 `json:"values"`
	Bins     int       `json:"bins,omitempty"`
	BinWidth float64   `json:"bin_width,omitempty"`
	Unit     string    `json:"unit,omitempty"`
	XTitle   string    `json:"x_title,omitempty"`
	YTitle   string    `json:"y_title,omitempty"`
}

// HistogramProps is the histogram display contract; normalized switches the
// y axis from counts to the share of the sample.
type HistogramProps struct {
	Title      string `json:"title,omitempty"`
	Normalized bool   `json:"normalized,omitempty"`
	ShowValues *bool  `json:"showValues,omitempty"`
}

// ===== Radar (§4.2) =====

// RadarData is one axis per dimension, one series per compared object.
type RadarData struct {
	Axes   []string      `json:"axes"`
	Series []ChartSeries `json:"series"`
	Min    *float64      `json:"min,omitempty"`
	Max    *float64      `json:"max,omitempty"`
	Unit   string        `json:"unit,omitempty"`
}

// RadarProps is the radar display contract; highlight names one series to
// draw emphasized.
type RadarProps struct {
	Title      string `json:"title,omitempty"`
	ShowValues *bool  `json:"showValues,omitempty"`
	ShowLegend *bool  `json:"showLegend,omitempty"`
	Highlight  string `json:"highlight,omitempty"`
}

// ===== Sankey (§4.3) =====

// SankeyNode is one flow state/category/participant.
type SankeyNode struct {
	ID    string `json:"id"`
	Label string `json:"label,omitempty"`
	Group string `json:"group,omitempty"`
}

// SankeyLink is one directed flow with its volume.
type SankeyLink struct {
	Source string  `json:"source"`
	Target string  `json:"target"`
	Value  float64 `json:"value"`
	Label  string  `json:"label,omitempty"`
}

// SankeyData carries the flow graph.
type SankeyData struct {
	Nodes []SankeyNode `json:"nodes"`
	Links []SankeyLink `json:"links"`
	Unit  string       `json:"unit,omitempty"`
}

// SankeyProps is the sankey display contract.
type SankeyProps struct {
	Title      string `json:"title,omitempty"`
	ShowValues *bool  `json:"showValues,omitempty"`
}

// ===== Swimlane (§4.4) =====

// SwimlaneLane is one participant's track — a semantic element in its own
// right (§4.4: never collapse lanes into flowchart nodes).
type SwimlaneLane struct {
	ID    string `json:"id"`
	Label string `json:"label,omitempty"`
}

// SwimlaneNode is one action/event on a lane; Start is the stage column
// (0-based, default = first appearance order), Span the column width.
type SwimlaneNode struct {
	ID     string     `json:"id"`
	Lane   string     `json:"lane"`
	Label  string     `json:"label"`
	Status string     `json:"status,omitempty"`
	Note   string     `json:"note,omitempty"`
	Start  int        `json:"start,omitempty"`
	Span   int        `json:"span,omitempty"`
	Ref    *EntityRef `json:"ref,omitempty"`
}

// SwimlaneEdge is one transition; Condition carries the guard text.
type SwimlaneEdge struct {
	From      string `json:"from"`
	To        string `json:"to"`
	Label     string `json:"label,omitempty"`
	Condition string `json:"condition,omitempty"`
	Dashed    bool   `json:"dashed,omitempty"`
}

// SwimlaneData carries lanes, nodes and transitions.
type SwimlaneData struct {
	Lanes []SwimlaneLane `json:"lanes"`
	Nodes []SwimlaneNode `json:"nodes"`
	Edges []SwimlaneEdge `json:"edges,omitempty"`
}

// SwimlaneProps is the swimlane display contract.
type SwimlaneProps struct {
	Title string `json:"title,omitempty"`
}

// ===== Treemap (§4.5) =====

// TreemapNode is one hierarchy element: a leaf carries Value, a branch
// carries Children (its own Value, when set, is display-only — layout sums
// the children).
type TreemapNode struct {
	Label    string        `json:"label"`
	Value    float64       `json:"value,omitempty"`
	Children []TreemapNode `json:"children,omitempty"`
	Ref      *EntityRef    `json:"ref,omitempty"`
}

// TreemapData carries the hierarchy root.
type TreemapData struct {
	Root TreemapNode `json:"root"`
	Unit string      `json:"unit,omitempty"`
}

// TreemapProps is the treemap display contract; MaxDepth bounds the drawn
// nesting (1..3, default 2 — deeper levels stay in the text fallback).
type TreemapProps struct {
	Title      string `json:"title,omitempty"`
	ShowValues *bool  `json:"showValues,omitempty"`
	MaxDepth   int    `json:"maxDepth,omitempty"`
}

// ===== shared validation helpers =====

func checkFinite(v *validation, field string, value float64) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		v.failf("%s must be a finite number", field)
	}
}

func checkRangePtrs(v *validation, field string, min, max *float64) {
	if min != nil {
		checkFinite(v, field+".min", *min)
	}
	if max != nil {
		checkFinite(v, field+".max", *max)
	}
	if min != nil && max != nil && *min >= *max {
		v.failf("%s: min (%v) must be below max (%v)", field, *min, *max)
	}
}

// ===== validators =====

func scatterValidator(v *validation, c *Component) {
	var props ScatterProps
	decodeProps(v, c, &props)
	checkLabel(v, "props.title", props.Title)
	if props.ShowValues != nil && *props.ShowValues && len(props.QuadrantLabels) == 0 {
		// value labels stay allowed without quadrant mode; nothing to check
	}
	if len(props.QuadrantLabels) > 4 {
		v.failf("props.quadrantLabels holds %d entries, limit is 4 (top-left, top-right, bottom-left, bottom-right)", len(props.QuadrantLabels))
	}
	for i, label := range props.QuadrantLabels {
		checkLabel(v, fmt.Sprintf("props.quadrantLabels[%d]", i), label)
	}
	if len(props.RefLines) > refLineLimit {
		v.failf("props.refLines holds %d entries, limit is %d", len(props.RefLines), refLineLimit)
	}
	for i, line := range props.RefLines {
		field := fmt.Sprintf("props.refLines[%d]", i)
		checkEnum(v, field+".axis", line.Axis, map[string]bool{"x": true, "y": true})
		if line.Axis == "" {
			v.failf("%s.axis is required (x|y)", field)
		}
		checkFinite(v, field+".at", line.At)
		checkLabel(v, field+".label", line.Label)
	}

	var data ScatterData
	if !decodeData(v, c, &data) {
		v.failf("data with points is required")
		return
	}
	checkLabel(v, "data.unit", data.Unit)
	checkLabel(v, "data.x_title", data.XTitle)
	checkLabel(v, "data.y_title", data.YTitle)
	checkRangePtrs(v, "data.x", data.XMin, data.XMax)
	checkRangePtrs(v, "data.y", data.YMin, data.YMax)
	if len(data.Points) == 0 {
		v.failf("data.points must carry at least one point")
	}
	if len(data.Points) > scatterPointLimit {
		v.failf("data.points holds %d points, limit is %d", len(data.Points), scatterPointLimit)
	}
	for i, point := range data.Points {
		field := fmt.Sprintf("data.points[%d]", i)
		checkFinite(v, field+".x", point.X)
		checkFinite(v, field+".y", point.Y)
		checkLabel(v, field+".label", point.Label)
		checkLabel(v, field+".group", point.Group)
		checkLabel(v, field+".note", point.Note)
		if point.Size != 0 && (point.Size < 0.2 || point.Size > 6) {
			v.failf("%s.size: %v outside the 0.2..6 scale", field, point.Size)
		}
		checkEntityRef(v, field+".ref", point.Ref)
	}
}

// quadrantValidator is scatter in positioning-map mode (§4.1): the alias
// slug flips the quadrant flag via aliasDefaults before this runs.
func quadrantValidator(v *validation, c *Component) {
	var props ScatterProps
	decodeProps(v, c, &props)
	if !props.Quadrant {
		props.Quadrant = true
		if encoded, err := json.Marshal(props); err == nil {
			c.Props = encoded
		}
	}
	scatterValidator(v, c)
}

func heatmapValidator(v *validation, c *Component) {
	var props HeatmapProps
	decodeProps(v, c, &props)
	checkLabel(v, "props.title", props.Title)

	var data HeatmapData
	if !decodeData(v, c, &data) {
		v.failf("data with rows, cols and values is required")
		return
	}
	checkLabel(v, "data.unit", data.Unit)
	checkRangePtrs(v, "data", data.Min, data.Max)
	if len(data.Rows) == 0 || len(data.Cols) == 0 {
		v.failf("data.rows and data.cols must each carry at least one label")
	}
	checkCount(v, "data.rows", len(data.Rows), heatmapLineLimit)
	checkCount(v, "data.cols", len(data.Cols), heatmapLineLimit)
	for i, label := range data.Rows {
		checkLabel(v, fmt.Sprintf("data.rows[%d]", i), label)
	}
	for i, label := range data.Cols {
		checkLabel(v, fmt.Sprintf("data.cols[%d]", i), label)
	}
	if len(data.Values) != len(data.Rows) {
		v.failf("data.values has %d rows but data.rows has %d", len(data.Values), len(data.Rows))
	}
	cells := 0
	for r, row := range data.Values {
		if len(row) != len(data.Cols) {
			v.failf("data.values[%d] has %d cells but data.cols has %d", r, len(row), len(data.Cols))
			continue
		}
		cells += len(row)
		for col, cell := range row {
			if cell == nil {
				continue
			}
			checkFinite(v, fmt.Sprintf("data.values[%d][%d]", r, col), *cell)
		}
	}
	if cells > heatmapCellLimit {
		v.failf("heatmap holds %d cells, limit is %d", cells, heatmapCellLimit)
	}
}

func histogramValidator(v *validation, c *Component) {
	var props HistogramProps
	decodeProps(v, c, &props)
	checkLabel(v, "props.title", props.Title)

	var data HistogramData
	if !decodeData(v, c, &data) {
		v.failf("data with values is required")
		return
	}
	checkLabel(v, "data.unit", data.Unit)
	checkLabel(v, "data.x_title", data.XTitle)
	checkLabel(v, "data.y_title", data.YTitle)
	if data.Bins < 0 || data.Bins > histogramBinLimit {
		v.failf("data.bins: %d outside 0..%d", data.Bins, histogramBinLimit)
	}
	if data.BinWidth < 0 {
		v.failf("data.bin_width must be >= 0")
	}
	if len(data.Values) == 0 {
		v.failf("data.values must carry at least one sample")
	}
	if len(data.Values) > histogramValueLimit {
		v.failf("data.values holds %d samples, limit is %d", len(data.Values), histogramValueLimit)
	}
	checkNumbers(v, "data.values", data.Values)
}

func radarValidator(v *validation, c *Component) {
	var props RadarProps
	decodeProps(v, c, &props)
	checkLabel(v, "props.title", props.Title)
	checkLabel(v, "props.highlight", props.Highlight)

	var data RadarData
	if !decodeData(v, c, &data) {
		v.failf("data with axes and series is required")
		return
	}
	checkLabel(v, "data.unit", data.Unit)
	checkRangePtrs(v, "data", data.Min, data.Max)
	if len(data.Axes) < 3 {
		v.failf("data.axes must carry at least 3 dimensions")
	}
	checkCount(v, "data.axes", len(data.Axes), radarAxisLimit)
	for i, axis := range data.Axes {
		checkLabel(v, fmt.Sprintf("data.axes[%d]", i), axis)
	}
	if len(data.Series) == 0 {
		v.failf("data.series must carry at least one object")
	}
	checkCount(v, "data.series", len(data.Series), radarSeriesLimit)
	highlightKnown := props.Highlight == ""
	for i, series := range data.Series {
		field := fmt.Sprintf("data.series[%d]", i)
		checkLabel(v, field+".name", series.Name)
		if series.Name == props.Highlight {
			highlightKnown = true
		}
		if len(series.Values) != len(data.Axes) {
			v.failf("%s.values has %d points but data.axes has %d", field, len(series.Values), len(data.Axes))
			continue
		}
		checkNumbers(v, field+".values", series.Values)
	}
	if !highlightKnown {
		v.failf("props.highlight: %q does not match any series name", props.Highlight)
	}
}

func sankeyValidator(v *validation, c *Component) {
	var props SankeyProps
	decodeProps(v, c, &props)
	checkLabel(v, "props.title", props.Title)

	var data SankeyData
	if !decodeData(v, c, &data) {
		v.failf("data with nodes and links is required")
		return
	}
	checkLabel(v, "data.unit", data.Unit)
	if len(data.Nodes) == 0 {
		v.failf("data.nodes must carry at least one node")
	}
	checkCount(v, "data.nodes", len(data.Nodes), sankeyNodeLimit)
	ids := make(map[string]bool, len(data.Nodes))
	for i, node := range data.Nodes {
		field := fmt.Sprintf("data.nodes[%d]", i)
		if strings.TrimSpace(node.ID) == "" {
			v.failf("%s.id is required", field)
			continue
		}
		checkLabel(v, field+".id", node.ID)
		if ids[node.ID] {
			v.failf("%s.id %q is duplicated", field, node.ID)
		}
		ids[node.ID] = true
		checkLabel(v, field+".label", node.Label)
		checkLabel(v, field+".group", node.Group)
	}
	if len(data.Links) == 0 {
		v.failf("data.links must carry at least one flow")
	}
	checkCount(v, "data.links", len(data.Links), sankeyLinkLimit)
	for i, link := range data.Links {
		field := fmt.Sprintf("data.links[%d]", i)
		if !ids[link.Source] {
			v.failf("%s.source %q is not a declared node", field, link.Source)
		}
		if !ids[link.Target] {
			v.failf("%s.target %q is not a declared node", field, link.Target)
		}
		if link.Source == link.Target {
			v.failf("%s: self-links are not supported", field)
		}
		checkFinite(v, field+".value", link.Value)
		if link.Value <= 0 {
			v.failf("%s.value must be positive", field)
		}
		checkLabel(v, field+".label", link.Label)
	}
}

func swimlaneValidator(v *validation, c *Component) {
	var props SwimlaneProps
	decodeProps(v, c, &props)
	checkLabel(v, "props.title", props.Title)

	var data SwimlaneData
	if !decodeData(v, c, &data) {
		v.failf("data with lanes and nodes is required")
		return
	}
	if len(data.Lanes) == 0 {
		v.failf("data.lanes must carry at least one lane")
	}
	checkCount(v, "data.lanes", len(data.Lanes), swimlaneLaneLimit)
	laneIDs := make(map[string]bool, len(data.Lanes))
	for i, lane := range data.Lanes {
		field := fmt.Sprintf("data.lanes[%d]", i)
		if strings.TrimSpace(lane.ID) == "" {
			v.failf("%s.id is required", field)
			continue
		}
		checkLabel(v, field+".id", lane.ID)
		if laneIDs[lane.ID] {
			v.failf("%s.id %q is duplicated", field, lane.ID)
		}
		laneIDs[lane.ID] = true
		checkLabel(v, field+".label", lane.Label)
	}
	if len(data.Nodes) == 0 {
		v.failf("data.nodes must carry at least one action")
	}
	checkCount(v, "data.nodes", len(data.Nodes), swimlaneNodeLimit)
	nodeIDs := make(map[string]bool, len(data.Nodes))
	for i, node := range data.Nodes {
		field := fmt.Sprintf("data.nodes[%d]", i)
		if strings.TrimSpace(node.ID) == "" {
			v.failf("%s.id is required", field)
			continue
		}
		checkLabel(v, field+".id", node.ID)
		if nodeIDs[node.ID] {
			v.failf("%s.id %q is duplicated", field, node.ID)
		}
		nodeIDs[node.ID] = true
		if !laneIDs[node.Lane] {
			v.failf("%s.lane %q is not a declared lane", field, node.Lane)
		}
		checkLabel(v, field+".label", node.Label)
		if node.Label == "" {
			v.failf("%s.label is required", field)
		}
		checkStatusField(v, field+".status", node.Status)
		checkLabel(v, field+".note", node.Note)
		if node.Start < 0 {
			v.failf("%s.start must be >= 0", field)
		}
		if node.Span < 0 {
			v.failf("%s.span must be >= 0 (0/1 mean one column)", field)
		}
		checkEntityRef(v, field+".ref", node.Ref)
	}
	for i, edge := range data.Edges {
		field := fmt.Sprintf("data.edges[%d]", i)
		if !nodeIDs[edge.From] {
			v.failf("%s.from %q is not a declared node", field, edge.From)
		}
		if !nodeIDs[edge.To] {
			v.failf("%s.to %q is not a declared node", field, edge.To)
		}
		if edge.From == edge.To {
			v.failf("%s: self-transitions are not supported", field)
		}
		checkLabel(v, field+".label", edge.Label)
		checkLabel(v, field+".condition", edge.Condition)
	}
}

func treemapValidator(v *validation, c *Component) {
	var props TreemapProps
	decodeProps(v, c, &props)
	checkLabel(v, "props.title", props.Title)
	if props.MaxDepth < 0 || props.MaxDepth > treemapDepthLimit {
		v.failf("props.maxDepth: %d outside 0..%d", props.MaxDepth, treemapDepthLimit)
	}

	var data TreemapData
	if !decodeData(v, c, &data) {
		v.failf("data.root is required")
		return
	}
	checkLabel(v, "data.unit", data.Unit)
	if strings.TrimSpace(data.Root.Label) == "" {
		v.failf("data.root.label is required")
	}
	total := 0
	var walk func(node TreemapNode, depth int, field string)
	walk = func(node TreemapNode, depth int, field string) {
		total++
		if total > treemapNodeLimit {
			v.failf("treemap holds more than %d nodes", treemapNodeLimit)
			return
		}
		checkLabel(v, field+".label", node.Label)
		checkEntityRef(v, field+".ref", node.Ref)
		if len(node.Children) > 0 {
			checkCount(v, field+".children", len(node.Children), 20)
			for i, child := range node.Children {
				walk(child, depth+1, fmt.Sprintf("%s.children[%d]", field, i))
			}
			if depth+1 > treemapDepthLimit+1 {
				v.failf("%s: nesting deeper than %d levels", field, treemapDepthLimit)
			}
		} else if node.Value <= 0 {
			v.failf("%s.value must be positive on leaves", field)
		}
	}
	walk(data.Root, 1, "data.root")
}
