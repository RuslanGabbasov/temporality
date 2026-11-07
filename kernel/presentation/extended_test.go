package presentation

import (
	"strings"
	"testing"
)

// P2/P3 kernel-side tests: the extended diagram catalog, the declarative
// form, the strip-fences answer path, the PDF export and the third-party
// renderer extension point.

func TestValidateExtendedDiagrams(t *testing.T) {
	// Mindmap: tree with bounded depth.
	mustValid(t, `{"version":"1.0","type":"presentation","content":[
		{"type":"mindmap","data":{"root":{"label":"Система","children":[
			{"label":"Backend","children":[{"label":"API"}]},
			{"label":"Frontend","note":"React"}
		]}}}
	]}`)
	wantInvalid(t, `{"version":"1.0","type":"presentation","content":[
		{"type":"mindmap","data":{"root":{"label":""}}}
	]}`, "label is required")

	// Dependency graph: flowchart shape.
	mustValid(t, `{"version":"1.0","type":"presentation","content":[
		{"type":"dependency-graph","data":{"nodes":[{"id":"a","label":"Auth"},{"id":"b","label":"Billing"}],"edges":[{"from":"a","to":"b"}]}}
	]}`)

	// State diagram: transitions must reference known states.
	mustValid(t, `{"version":"1.0","type":"presentation","content":[
		{"type":"state-diagram","data":{"initial":"draft","states":[{"id":"draft","label":"Draft"},{"id":"done","label":"Done"}],"transitions":[{"from":"draft","to":"done","label":"finish"}]}}
	]}`)
	wantInvalid(t, `{"version":"1.0","type":"presentation","content":[
		{"type":"state-diagram","data":{"states":[{"id":"draft","label":"Draft"}],"transitions":[{"from":"draft","to":"ghost"}]}}
	]}`, "unknown state")

	// ER diagram: cardinality enum; relations must reference known entities.
	mustValid(t, `{"version":"1.0","type":"presentation","content":[
		{"type":"er-diagram","data":{"entities":[{"name":"users","attributes":["string id","string email"]},{"name":"orders"}],"relations":[{"from":"users","to":"orders","cardinality":"1:N","label":"places"}]}}
	]}`)
	wantInvalid(t, `{"version":"1.0","type":"presentation","content":[
		{"type":"er-diagram","data":{"entities":[{"name":"users"}],"relations":[{"from":"users","to":"orders","cardinality":"many"}]}}
	]}`, "cardinality")

	// Class diagram: relation kinds; relations must reference known classes.
	mustValid(t, `{"version":"1.0","type":"presentation","content":[
		{"type":"class-diagram","data":{"classes":[{"name":"Animal","attributes":["String name"],"methods":["walk()"]},{"name":"Dog"}],"relations":[{"from":"Dog","to":"Animal","kind":"inheritance"}]}}
	]}`)
	wantInvalid(t, `{"version":"1.0","type":"presentation","content":[
		{"type":"class-diagram","data":{"classes":[{"name":"A"}],"relations":[{"from":"A","to":"B","kind":"metaphysical"}]}}
	]}`, "kind")

	// Roadmap alias implies the gantt month scale.
	doc := mustValid(t, `{"version":"1.0","type":"presentation","content":[
		{"type":"roadmap","data":{"tasks":[{"label":"Q1 work","start":"2026-01-01","end":"2026-03-31"}]}}
	]}`)
	if doc.Content[0].Type != "gantt" {
		t.Fatalf("roadmap not normalized: %s", doc.Content[0].Type)
	}
	var props GanttProps
	if err := decodeStrict(doc.Content[0].Props, &props); err != nil || props.Scale != "month" {
		t.Fatalf("roadmap did not imply month scale: %+v (%v)", props, err)
	}
}

func TestValidateForm(t *testing.T) {
	mustValid(t, `{"version":"1.0","type":"presentation","content":[
		{"type":"form","content":"Review","props":{
			"schema":{"type":"object","properties":{"name":{"type":"string","minLength":3},"flags":{"type":"array","items":{"type":"string","enum":["a","b"]}}},"required":["name"]},
			"submit":"Send"
		},"actions":[{"type":"submit","target":"ask-42"}]}
	]}`)
	// No schema → refused.
	wantInvalid(t, `{"version":"1.0","type":"presentation","content":[
		{"type":"form","actions":[{"type":"submit","target":"t"}]}
	]}`, "props.schema")
	// No submit action → refused.
	wantInvalid(t, `{"version":"1.0","type":"presentation","content":[
		{"type":"form","props":{"schema":{"type":"object","properties":{"a":{"type":"string"}}}}}
	]}`, "submit")
	// Schema without properties → refused.
	wantInvalid(t, `{"version":"1.0","type":"presentation","content":[
		{"type":"form","props":{"schema":{"type":"object"}},"actions":[{"type":"submit","target":"t"}]}
	]}`, "properties")
}

func TestTextRendersExtendedDiagrams(t *testing.T) {
	doc := mustValid(t, `{"version":"1.0","type":"presentation","content":[
		{"type":"mindmap","data":{"root":{"label":"root","children":[{"label":"leaf","note":"n"}]}}},
		{"type":"state-diagram","data":{"initial":"a","states":[{"id":"a","label":"A"},{"id":"b","label":"B"}],"transitions":[{"from":"a","to":"b","label":"go"}]}},
		{"type":"er-diagram","data":{"entities":[{"name":"users","attributes":["string id"]},{"name":"orders"}],"relations":[{"from":"users","to":"orders","cardinality":"1:N"}]}},
		{"type":"class-diagram","data":{"classes":[{"name":"Animal","attributes":["String name"],"methods":["walk()"]},{"name":"Dog"}],"relations":[{"from":"Dog","to":"Animal","kind":"inheritance"}]}},
		{"type":"form","props":{"schema":{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]},"submit":"Send"},"actions":[{"type":"submit","target":"ask-1"}]}
	]}`)
	text := RenderText(doc)
	for _, needle := range []string{"root", "leaf (n)", "start → a", "a → b: go", "users", "1:N", "Animal", "walk()", "name *", "Send"} {
		if !strings.Contains(text, needle) {
			t.Fatalf("text fallback lost %q from:\n%s", needle, text)
		}
	}
	// Every new type lays out and renders statically (four-format DoD §7.7).
	drawing := LayoutDocument(doc, LayoutOptions{IncludeTitle: true})
	if len(RenderSVG(drawing)) == 0 {
		t.Fatal("empty svg")
	}
	if _, err := RenderPNG(drawing); err != nil {
		t.Fatalf("png failed: %v", err)
	}
	pdf, err := RenderPDF(drawing)
	if err != nil {
		t.Fatalf("pdf failed: %v", err)
	}
	if !strings.HasPrefix(string(pdf[:5]), "%PDF-") {
		t.Fatal("pdf signature missing")
	}
}

func TestStripFences(t *testing.T) {
	answer := "Краткое резюме.\n\n```presentation\n{\"version\":\"1.0\",\"type\":\"presentation\",\"content\":[]}\n```\n\nХвост."
	stripped := StripFences(answer)
	if strings.Contains(stripped, "```") || strings.Contains(stripped, "\"version\"") {
		t.Fatalf("fence content leaked:\n%s", stripped)
	}
	if !strings.Contains(stripped, "Краткое резюме.") || !strings.Contains(stripped, "Хвост.") || !strings.Contains(stripped, "[structured answer") {
		t.Fatalf("prose damaged:\n%s", stripped)
	}
	// Non-presentation fences stay untouched.
	kept := StripFences("```go\nfmt.Println()\n```")
	if !strings.Contains(kept, "fmt.Println()") {
		t.Fatalf("non-presentation fence stripped:\n%s", kept)
	}
}

func TestFindDeclaredAction(t *testing.T) {
	doc := []byte(`{"version":"1.0","type":"presentation","content":[
		{"type":"button","id":"btn","content":"Approve","actions":[{"type":"submit","target":"ask-1"}]},
		{"type":"card","id":"card","children":[
			{"type":"button","id":"nested-btn","content":"Deny","actions":[{"type":"submit","target":"ask-2"}]}
		]}
	]}`)
	action, err := FindDeclaredAction(doc, "btn")
	if err != nil || action.Target != "ask-1" || action.Type != "submit" {
		t.Fatalf("declared action not found: %+v (%v)", action, err)
	}
	// Nested children are searched.
	nested, err := FindDeclaredAction(doc, "nested-btn")
	if err != nil || nested.Target != "ask-2" {
		t.Fatalf("nested action not found: %+v (%v)", nested, err)
	}
	if _, err := FindDeclaredAction(doc, "no-such"); err == nil {
		t.Fatal("undeclared component accepted")
	}
	// A component without executable actions (text) is refused.
	textDoc := []byte(`{"version":"1.0","type":"presentation","content":[{"type":"text","id":"t","content":"x"}]}`)
	if _, err := FindDeclaredAction(textDoc, "t"); err == nil {
		t.Fatal("component without actions accepted")
	}
}

func TestRenderPDFValidDocument(t *testing.T) {
	doc := sampleDocument()
	drawing := LayoutDocument(doc, LayoutOptions{IncludeTitle: true})
	pdf, err := RenderPDF(drawing)
	if err != nil {
		t.Fatalf("pdf render failed: %v", err)
	}
	if !strings.HasPrefix(string(pdf), "%PDF-1.4") {
		t.Fatal("pdf header missing")
	}
	if !strings.Contains(string(pdf), "/Type /Catalog") || !strings.Contains(string(pdf), "/Type /Page") {
		t.Fatal("pdf object graph incomplete")
	}
	if !strings.Contains(string(pdf), "/Filter /FlateDecode") {
		t.Fatal("pdf image stream missing")
	}
	if !strings.Contains(string(pdf), "startxref") {
		t.Fatal("pdf xref missing")
	}
}

func TestRegisterCustomComponent(t *testing.T) {
	// The third-party seam (§17 P3.11): a custom component registers its
	// full stack and all four formats render it.
	RegisterComponent(
		Spec{Type: "custom-note", Kind: KindText, Description: "Example third-party component", HasSVG: true, HasPNG: true},
		func(v *validation, c *Component) { checkContent(v, "content", c.Content) },
		func(c Component, indent string, depth int) []string {
			return []string{indent + "[custom] " + c.Content}
		},
		func(c Component, width float64, theme Theme) (Drawing, bool) {
			frag := Drawing{Width: width, Height: 30}
			frag.Ops = append(frag.Ops, Rect{X: 0, Y: 0, W: width, H: 30, Fill: theme.Accent, Radius: 4})
			frag.Ops = append(frag.Ops, Glyph{X: 8, Y: 21, Size: 12, Color: "#ffffff", Text: "custom: " + c.Content})
			return frag, true
		},
	)
	doc := mustValid(t, `{"version":"1.0","type":"presentation","content":[
		{"type":"custom-note","content":"hello from a third party"}
	]}`)
	text := RenderText(doc)
	if !strings.Contains(text, "[custom] hello from a third party") {
		t.Fatalf("custom text renderer not used:\n%s", text)
	}
	drawing := LayoutDocument(doc, LayoutOptions{})
	found := false
	for _, op := range drawing.Ops {
		if glyph, ok := op.(Glyph); ok && strings.Contains(glyph.Text, "custom: hello") {
			found = true
		}
	}
	if !found {
		t.Fatal("custom block renderer not used")
	}
	// Duplicates panic.
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("duplicate custom registration must panic")
			}
		}()
		RegisterComponent(Spec{Type: "custom-note", Kind: KindText, HasSVG: true, HasPNG: true},
			func(v *validation, c *Component) {}, func(c Component, i string, d int) []string { return nil }, func(c Component, w float64, t Theme) (Drawing, bool) { return Drawing{}, false })
	}()
}
