package presentation

import (
	"strings"
	"testing"
)

func mustValid(t *testing.T, raw string) Document {
	t.Helper()
	doc, err := Validate([]byte(raw), Policy{})
	if err != nil {
		t.Fatalf("valid document rejected: %v\n%s", err, raw)
	}
	return doc
}

func wantInvalid(t *testing.T, raw string, needle string) {
	t.Helper()
	wantInvalidPolicy(t, raw, needle, Policy{})
}

func wantInvalidPolicy(t *testing.T, raw string, needle string, policy Policy) {
	t.Helper()
	_, err := Validate([]byte(raw), policy)
	if err == nil {
		t.Fatalf("invalid document accepted:\n%s", raw)
	}
	if needle != "" && !strings.Contains(err.Error(), needle) {
		t.Fatalf("error %q does not mention %q", err.Error(), needle)
	}
}

func TestValidateAcceptsSpecExample(t *testing.T) {
	mustValid(t, `{
		"version": "1.0",
		"type": "presentation",
		"id": "presentation-123",
		"title": "Test execution report",
		"content": [
			{"type": "text", "content": "Тестирование завершено."},
			{"type": "kpi", "items": [
				{"label": "Passed", "value": 942, "status": "success"},
				{"label": "Failed", "value": 31, "status": "danger"}
			]},
			{"type": "chart", "chartType": "pie", "data": {
				"labels": ["Passed", "Failed", "Skipped"],
				"values": [942, 31, 27]
			}}
		]
	}`)
}

// The §4.2 example uses "chartType" inline on the component — wait, it
// doesn't: the chart type rides in props per this implementation. Both the
// canonical props form and the catalog alias slugs must validate.
func TestValidateChartAliasSlugs(t *testing.T) {
	doc := mustValid(t, `{
		"version": "1.0", "type": "presentation",
		"content": [{"type": "pie-chart", "data": {"labels": ["a", "b"], "values": [2, 3]}}]
	}`)
	if doc.Content[0].Type != "chart" {
		t.Fatalf("alias not normalized: %s", doc.Content[0].Type)
	}
	var props ChartProps
	if err := decodeStrict(doc.Content[0].Props, &props); err != nil {
		t.Fatalf("props not populated: %v", err)
	}
	if props.ChartType != "pie" {
		t.Fatalf("alias did not imply chartType: %+v", props)
	}
}

func TestValidateRejectsEnvelopeProblems(t *testing.T) {
	wantInvalid(t, `{"version": "2.0", "type": "presentation", "content": [{"type": "text", "content": "x"}]}`, "version")
	wantInvalid(t, `{"version": "1.0", "type": "note", "content": [{"type": "text", "content": "x"}]}`, "type")
	wantInvalid(t, `{"version": "1.0", "type": "presentation", "content": []}`, "no components")
	wantInvalid(t, ``, "empty")
}

func TestValidateRejectsUnknownTypesAndFields(t *testing.T) {
	wantInvalid(t, `{"version": "1.0", "type": "presentation", "content": [{"type": "holocard", "content": "x"}]}`, "unknown component type")
	wantInvalid(t, `{"version": "1.0", "type": "presentation", "content": [{"type": "kpi", "items": [{"label": "a", "value": 1, "evil": true}]}]}`, "items")
	wantInvalid(t, `{"version": "1.0", "type": "presentation", "content": [{"type": "text", "content": "x", "props": {"color": "#ff0000"}}]}`, "props")
}

func TestValidateLimits(t *testing.T) {
	// Depth: nesting beyond MaxDepth is refused.
	var deep strings.Builder
	deep.WriteString(`{"version": "1.0", "type": "presentation", "content": [{"type": "card", "children": [`)
	nesting := MaxDepthNestingProbe
	for i := 0; i < nesting; i++ {
		deep.WriteString(`{"type": "card", "children": [`)
	}
	deep.WriteString(`{"type": "text", "content": "leaf"}`)
	for i := 0; i < nesting; i++ {
		deep.WriteString(`]}`)
	}
	deep.WriteString(`]}`)
	deep.WriteString(`]}`)
	wantInvalid(t, deep.String(), "nesting")

	// Component count.
	var many strings.Builder
	many.WriteString(`{"version": "1.0", "type": "presentation", "content": [`)
	for i := 0; i < 250; i++ {
		if i > 0 {
			many.WriteString(",")
		}
		many.WriteString(`{"type": "text", "content": "x"}`)
	}
	many.WriteString(`]}`)
	wantInvalid(t, many.String(), "components")

	// Document size.
	huge := `{"version": "1.0", "type": "presentation", "content": [{"type": "text", "content": "` + strings.Repeat("x", 256<<10) + `"}]}`
	wantInvalid(t, huge, "bytes")
}

// MaxDepthNestingProbe is two levels past the limit: the outer document
// components already sit at depth 1.
const MaxDepthNestingProbe = 8

func TestValidateSecurity(t *testing.T) {
	// javascript: URLs.
	wantInvalid(t, `{"version": "1.0", "type": "presentation", "content": [
		{"type": "link", "content": "click", "props": {"href": "javascript:alert(1)"}}
	]}`, "not an allowed URL")

	// data: URLs.
	wantInvalid(t, `{"version": "1.0", "type": "presentation", "content": [
		{"type": "image", "props": {"src": "data:text/html;base64,PHNjcmlwdD4="}}
	]}`, "not an allowed URL")

	// External images off by default (§14.8).
	wantInvalid(t, `{"version": "1.0", "type": "presentation", "content": [
		{"type": "image", "props": {"src": "https://evil.example/pixel.png"}}
	]}`, "external images are disabled")

	// With policy consent, https images pass.
	doc, err := Validate([]byte(`{"version": "1.0", "type": "presentation", "content": [
		{"type": "image", "props": {"src": "https://cdn.example/pixel.png"}}
	]}`), Policy{AllowExternalImages: true})
	if err != nil {
		t.Fatalf("allowed external image rejected: %v", err)
	}
	_ = doc

	// Host allowlist (with policy consent, only listed hosts pass).
	wantInvalidPolicy(t, `{"version": "1.0", "type": "presentation", "content": [
		{"type": "image", "props": {"src": "https://other.example/pixel.png"}}
	]}`, "allowed image hosts", Policy{AllowExternalImages: true, AllowedImageHosts: []string{"cdn.example"}})

	// Control characters.
	wantInvalid(t, `{"version": "1.0", "type": "presentation", "content": [
		{"type": "text", "content": "bad\u0000char"}
	]}`, "control")

	// Table cell href smuggling.
	wantInvalid(t, `{"version": "1.0", "type": "presentation", "content": [
		{"type": "table", "data": {"columns": [{"key": "a"}], "rows": [{"a": {"text": "x", "href": "javascript:1"}}]}}
	]}`, "not an allowed URL")

	// Action payload must be an object, bounded.
	wantInvalid(t, `{"version": "1.0", "type": "presentation", "content": [
		{"type": "button", "content": "go", "actions": [{"type": "submit", "target": "t", "payload": "rm -rf /"}]}
	]}`, "payload must be a JSON object")
}

func TestValidatePolicyDisabledType(t *testing.T) {
	policy := Policy{DisabledTypes: map[string]bool{"chart": true}}
	_, err := Validate([]byte(`{"version": "1.0", "type": "presentation", "content": [
		{"type": "chart", "props": {"chartType": "line"}, "data": {"labels": ["a"], "values": [1]}}
	]}`), policy)
	if err == nil || !strings.Contains(err.Error(), "disabled by policy") {
		t.Fatalf("policy disabled type not enforced: %v", err)
	}
}

func TestValidateTables(t *testing.T) {
	mustValid(t, `{"version": "1.0", "type": "presentation", "content": [
		{"type": "table", "data": {
			"columns": [
				{"key": "name", "title": "Тест", "align": "left"},
				{"key": "duration", "kind": "number", "align": "right"},
				{"key": "result", "kind": "status"}
			],
			"rows": [
				{"name": "login", "duration": 12, "result": {"text": "pass", "status": "success"}},
				{"name": "checkout", "duration": 88, "result": {"text": "fail", "status": "danger"}}
			]
		}}
	]}`)
	wantInvalid(t, `{"version": "1.0", "type": "presentation", "content": [
		{"type": "table", "data": {"columns": [{"key": "a"}], "rows": [{"b": 1}]}}
	]}`, "unknown column key")
	wantInvalid(t, `{"version": "1.0", "type": "presentation", "content": [
		{"type": "table", "data": {"columns": [{"key": "a"}, {"key": "a"}], "rows": [{"a": 1}]}}
	]}`, "duplicate column key")
}

func TestValidateCharts(t *testing.T) {
	wantInvalid(t, `{"version": "1.0", "type": "presentation", "content": [
		{"type": "chart", "props": {"chartType": "hologram"}, "data": {"labels": ["a"], "values": [1]}}
	]}`, "chartType")
	wantInvalid(t, `{"version": "1.0", "type": "presentation", "content": [
		{"type": "chart", "props": {"chartType": "pie"}, "data": {"labels": ["a", "b"], "values": [1, -2]}}
	]}`, "negative")
	wantInvalid(t, `{"version": "1.0", "type": "presentation", "content": [
		{"type": "chart", "props": {"chartType": "line"}, "data": {"labels": ["a", "b"], "values": [1]}}
	]}`, "points but data.labels")
	wantInvalid(t, `{"version": "1.0", "type": "presentation", "content": [
		{"type": "chart", "props": {"chartType": "bar"}, "data": {"series": [{"name": "s", "values": [1]}, {"name": "t", "values": [2]}, {"name": "u", "values": [3]}, {"name": "v", "values": [4]}, {"name": "w", "values": [5]}, {"name": "x", "values": [6]}, {"name": "y", "values": [7]}]}}
	]}`, "series holds 7")
}

func TestValidateFlowAndGantt(t *testing.T) {
	mustValid(t, `{"version": "1.0", "type": "presentation", "content": [
		{"type": "flowchart", "data": {
			"nodes": [
				{"id": "a", "label": "Build", "status": "success"},
				{"id": "b", "label": "Тесты"},
				{"id": "c", "label": "Deploy", "shape": "diamond"}
			],
			"edges": [{"from": "a", "to": "b"}, {"from": "b", "to": "c", "label": "green"}]
		}}
	]}`)
	wantInvalid(t, `{"version": "1.0", "type": "presentation", "content": [
		{"type": "flowchart", "data": {
			"nodes": [{"id": "a", "label": "Build"}, {"id": "b", "label": "Test"}],
			"edges": [{"from": "a", "to": "b"}, {"from": "b", "to": "a"}]
		}}
	]}`, "cycle")
	wantInvalid(t, `{"version": "1.0", "type": "presentation", "content": [
		{"type": "flowchart", "data": {
			"nodes": [{"id": "a", "label": "Build"}],
			"edges": [{"from": "a", "to": "ghost"}]
		}}
	]}`, "unknown node")
	mustValid(t, `{"version": "1.0", "type": "presentation", "content": [
		{"type": "gantt", "data": {"tasks": [
			{"id": "t1", "label": "Design", "start": "2026-03-01", "end": "2026-03-05", "status": "success", "assignee": "Аня"},
			{"id": "t2", "label": "Implement", "start": "2026-03-05", "end": "2026-03-12", "dependsOn": ["t1"]}
		]}}
	]}`)
	wantInvalid(t, `{"version": "1.0", "type": "presentation", "content": [
		{"type": "gantt", "data": {"tasks": [{"label": "Backwards", "start": "2026-03-05", "end": "2026-03-01"}]}}
	]}`, "end")
}

func TestValidateActions(t *testing.T) {
	mustValid(t, `{"version": "1.0", "type": "presentation", "content": [
		{"type": "button", "content": "Approve", "actions": [
			{"type": "submit", "target": "approval-123", "payload": {"decision": "approve"}}
		]}
	]}`)
	wantInvalid(t, `{"version": "1.0", "type": "presentation", "content": [
		{"type": "text", "content": "x", "actions": [{"type": "submit", "target": "t"}]}
	]}`, "not supported by component")
	wantInvalid(t, `{"version": "1.0", "type": "presentation", "content": [
		{"type": "button", "content": "go", "actions": [{"type": "detonate", "target": "t"}]}
	]}`, "unknown action type")
	wantInvalid(t, `{"version": "1.0", "type": "presentation", "content": [
		{"type": "button", "content": "go", "actions": [{"type": "submit"}]}
	]}`, "target is required")
}

func TestRegistryCompleteness(t *testing.T) {
	// §5: a missing mandatory renderer must surface at registration or in
	// tests. Register enforces SVG+PNG at boot; Complete re-checks kinds.
	registry := Default()
	if err := registry.Complete(); err != nil {
		t.Fatalf("default registry incomplete: %v", err)
	}
	for _, componentType := range registry.Types() {
		spec := registry.Lookup(componentType)
		if spec == nil {
			t.Fatalf("lookup failed for %s", componentType)
		}
		if spec.Version == "" {
			t.Fatalf("component %s has no version", componentType)
		}
	}
}

func TestRegistryPanicsOnIncompleteSpec(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("registering a spec without static renderers must panic")
		}
	}()
	NewRegistry().Register(Spec{Type: "broken", Kind: KindText, HasSVG: false, HasPNG: true})
}

func TestRegistryPanicsOnDuplicate(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("duplicate registration must panic")
		}
	}()
	r := NewRegistry()
	r.Register(Spec{Type: "text", Kind: KindText, HasSVG: true, HasPNG: true})
	r.Register(Spec{Type: "text", Kind: KindText, HasSVG: true, HasPNG: true})
}

func TestSchemaFileMatchesPackage(t *testing.T) {
	// The schemas/ copy must never drift from the embedded original.
	embedded := string(IRSchema())
	onDisk, err := schemaOnDisk()
	if err != nil {
		t.Skipf("schemas/ copy not reachable: %v", err)
	}
	if onDisk != embedded {
		t.Fatal("schemas/temporality.presentation.v1.schema.json drifted from kernel/presentation/presentation-ir-v1.schema.json")
	}
}
