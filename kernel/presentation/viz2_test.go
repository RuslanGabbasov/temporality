package presentation

// Extended visualization catalog tests (docs/presentation-engine-ext.md §12):
// validation accepts/accepts-rejects per component, the static layout emits
// deterministic non-empty ops, and the text fallback carries the semantics.

import (
	"strings"
	"testing"
)

func vizDoc(component string) string {
	return `{"version":"1.0","type":"presentation","id":"viz-test","title":"Ext catalog","content":[` + component + `]}`
}

func mustInvalidViz(t *testing.T, raw string, want string) {
	t.Helper()
	_, err := Validate([]byte(raw), Policy{})
	if err == nil {
		t.Fatalf("expected validation error containing %q, got none", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("expected error containing %q, got: %v", want, err)
	}
}

func TestVizScatterValidation(t *testing.T) {
	valid := vizDoc(`{"type":"scatter","data":{"points":[
		{"x":1,"y":2,"label":"a","group":"g1"},
		{"x":-3,"y":0.5,"label":"b","group":"g2","size":2,"highlight":true,"ref":{"type":"artifact","id":"ext/42"}}
	],"x_title":"Reach","y_title":"Impact","x_min":-5,"x_max":5}}`)
	if _, err := Validate([]byte(valid), Policy{}); err != nil {
		t.Fatalf("valid scatter rejected: %v", err)
	}
	mustInvalidViz(t, vizDoc(`{"type":"scatter","data":{"points":[]}}`), "at least one point")
	mustInvalidViz(t, vizDoc(`{"type":"scatter","data":{"points":[{"x":1,"y":2,"ref":{"type":"weapon","id":"x"}}]}}`), "artifact|task|project|run|agent")
	mustInvalidViz(t, vizDoc(`{"type":"scatter","data":{"points":[{"x":1,"y":2}],"x_min":5,"x_max":1}}`), "min (5) must be below max")
	// positioning-map alias normalizes onto quadrant with the flag set.
	doc, err := Validate([]byte(vizDoc(`{"type":"positioning-map","data":{"points":[{"x":1,"y":2,"label":"a"}]},"props":{"quadrantLabels":["tl","tr","bl","br"]}}`)), Policy{})
	if err != nil {
		t.Fatalf("positioning-map alias rejected: %v", err)
	}
	if doc.Content[0].Type != "quadrant" {
		t.Fatalf("alias must normalize to quadrant, got %q", doc.Content[0].Type)
	}
}

func TestVizHeatmapValidation(t *testing.T) {
	valid := vizDoc(`{"type":"heatmap","data":{
		"rows":["api","ui"],"cols":["mon","tue"],
		"values":[[1.5,null],[0,7]],"unit":"ms"}}`)
	if _, err := Validate([]byte(valid), Policy{}); err != nil {
		t.Fatalf("valid heatmap rejected: %v", err)
	}
	mustInvalidViz(t, vizDoc(`{"type":"heatmap","data":{"rows":["a"],"cols":["x"],"values":[[1,2]]}}`), "has 2 cells but data.cols has 1")
	// a nil-only matrix is legal (all missing values)
	if _, err := Validate([]byte(vizDoc(`{"type":"heatmap","data":{"rows":["a"],"cols":["x"],"values":[[null]]}}`)), Policy{}); err != nil {
		t.Fatalf("all-missing heatmap must validate: %v", err)
	}
}

func TestVizHistogramValidation(t *testing.T) {
	valid := vizDoc(`{"type":"histogram","data":{"values":[1,2,2,3,7],"bins":4,"unit":"s","x_title":"latency"},"props":{"normalized":false}}`)
	if _, err := Validate([]byte(valid), Policy{}); err != nil {
		t.Fatalf("valid histogram rejected: %v", err)
	}
	mustInvalidViz(t, vizDoc(`{"type":"histogram","data":{"values":[]}}`), "at least one sample")
	mustInvalidViz(t, vizDoc(`{"type":"histogram","data":{"values":[1],"bins":99}}`), "outside 0..60")
}

func TestVizRadarValidation(t *testing.T) {
	valid := vizDoc(`{"type":"radar","data":{"axes":["speed","cost","quality"],"series":[
		{"name":"agent-a","values":[3,4,5]},{"name":"agent-b","values":[4,2,4]}],
		"min":0,"max":5},"props":{"highlight":"agent-a"}}`)
	if _, err := Validate([]byte(valid), Policy{}); err != nil {
		t.Fatalf("valid radar rejected: %v", err)
	}
	mustInvalidViz(t, vizDoc(`{"type":"radar","data":{"axes":["a","b"],"series":[{"name":"x","values":[1,2]}]}}`), "at least 3 dimensions")
	mustInvalidViz(t, vizDoc(`{"type":"radar","data":{"axes":["a","b","c"],"series":[{"name":"x","values":[1,2]}]}}`), "has 2 points but data.axes has 3")
	mustInvalidViz(t, vizDoc(`{"type":"radar","data":{"axes":["a","b","c"],"series":[{"name":"x","values":[1,2,3]}]},"props":{"highlight":"nope"}}`), "does not match any series")
}

func TestVizSankeyValidation(t *testing.T) {
	valid := vizDoc(`{"type":"sankey","data":{"nodes":[
		{"id":"in","label":"Вход"},{"id":"mid","label":"Разбор"},{"id":"done","label":"Готов"},{"id":"dead","label":"Брак"}],
		"links":[
			{"source":"in","target":"mid","value":10},
			{"source":"mid","target":"done","value":7},
			{"source":"mid","target":"dead","value":3}],"unit":"задач"}}`)
	if _, err := Validate([]byte(valid), Policy{}); err != nil {
		t.Fatalf("valid sankey rejected: %v", err)
	}
	mustInvalidViz(t, vizDoc(`{"type":"sankey","data":{"nodes":[{"id":"a"}],"links":[{"source":"a","target":"ghost","value":1}]}}`), "not a declared node")
	mustInvalidViz(t, vizDoc(`{"type":"sankey","data":{"nodes":[{"id":"a"}],"links":[{"source":"a","target":"a","value":1}]}}`), "self-links")
	mustInvalidViz(t, vizDoc(`{"type":"sankey","data":{"nodes":[{"id":"a"},{"id":"b"}],"links":[{"source":"a","target":"b","value":-2}]}}`), "must be positive")
}

func TestVizSwimlaneValidation(t *testing.T) {
	valid := vizDoc(`{"type":"swimlane","data":{"lanes":[{"id":"agent","label":"Агент"},{"id":"human","label":"Человек"}],"nodes":[
		{"id":"draft","lane":"agent","label":"Черновик","start":0,"span":1},
		{"id":"review","lane":"human","label":"Согласование","status":"warning","start":1,"ref":{"type":"task","id":"task-9"}}],"edges":[
		{"from":"draft","to":"review","condition":"готово","dashed":false}]}}`)
	if _, err := Validate([]byte(valid), Policy{}); err != nil {
		t.Fatalf("valid swimlane rejected: %v", err)
	}
	mustInvalidViz(t, vizDoc(`{"type":"swimlane","data":{"lanes":[{"id":"a"}],"nodes":[{"id":"n","lane":"ghost","label":"x"}]}}`), "not a declared lane")
	mustInvalidViz(t, vizDoc(`{"type":"swimlane","data":{"lanes":[{"id":"a"}],"nodes":[{"id":"n","lane":"a","label":"x"}],"edges":[{"from":"n","to":"missing"}]}}`), "not a declared node")
	mustInvalidViz(t, vizDoc(`{"type":"swimlane","data":{"lanes":[{"id":"a"}],"nodes":[{"id":"n","lane":"a","label":"x"},{"id":"n","lane":"a","label":"y"}]}}`), "duplicated")
}

func TestVizTreemapValidation(t *testing.T) {
	valid := vizDoc(`{"type":"treemap","data":{"root":{"label":"Портфель","children":[
		{"label":"Продукт","children":[{"label":"Core","value":30},{"label":"API","value":20}]},
		{"label":"Инфра","value":25}]},"unit":"ч"}}`)
	if _, err := Validate([]byte(valid), Policy{}); err != nil {
		t.Fatalf("valid treemap rejected: %v", err)
	}
	mustInvalidViz(t, vizDoc(`{"type":"treemap","data":{"root":{"label":"Портфель","children":[{"label":"x","value":0}]}}}`), "must be positive on leaves")
	mustInvalidViz(t, vizDoc(`{"type":"treemap","data":{"root":{"label":"Портфель"}},"props":{"maxDepth":9}}`), "outside 0..3")
}

func TestVizLayoutAndText(t *testing.T) {
	doc := mustValid(t, vizDoc(`{"type":"scatter","data":{"points":[
		{"x":1,"y":2,"label":"a","group":"g1"},{"x":-2,"y":3,"label":"b","group":"g2","highlight":true}]},
		"props":{"quadrant":true,"quadrantLabels":["tl","tr","bl","br"]}}`))
	frag := LayoutDocument(doc, LayoutOptions{})
	if len(frag.Ops) < 10 {
		t.Fatalf("scatter layout too sparse: %d ops", len(frag.Ops))
	}
	again := LayoutDocument(doc, LayoutOptions{})
	if len(frag.Ops) != len(again.Ops) {
		t.Fatalf("layout must be deterministic")
	}
	joined := RenderText(doc)
	for _, want := range []string{"Positioning map", "a: X = 1", "★ highlighted"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("scatter text fallback must contain %q:\n%s", want, joined)
		}
	}
}

func TestVizSankeyTextCarriesFlows(t *testing.T) {
	doc := mustValid(t, vizDoc(`{"type":"sankey","data":{"nodes":[
		{"id":"in","label":"Вход"},{"id":"done","label":"Готово"}],"links":[
		{"source":"in","target":"done","value":12,"label":"взято в работу"}],"unit":"задач"}}`))
	joined := RenderText(doc)
	for _, want := range []string{"Вход → Готово: 12 задач", "взято в работу"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("sankey text fallback must contain %q:\n%s", want, joined)
		}
	}
}

func TestVizHeatmapTextCarriesMatrix(t *testing.T) {
	doc := mustValid(t, vizDoc(`{"type":"heatmap","data":{"rows":["api"],"cols":["mon","tue"],"values":[[3,null]],"unit":"ms"}}`))
	joined := RenderText(doc)
	if !strings.Contains(joined, "api: mon=3 ms; tue=—") {
		t.Fatalf("heatmap text must carry the matrix row:\n%s", joined)
	}
}
