package presentation

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Temporary visual check: renders the competitive map — rotated Y caption in
// the left margin, X caption, legend row below it — to a PNG for inspection.
// Gated behind PRESENTATION_DEBUG=1 like the other debug renders.
func TestScatterVisualDebug(t *testing.T) {
	if debugRenderOff() {
		t.Skip("set PRESENTATION_DEBUG=1 to write /tmp/scatter-debug.png")
	}
	data := ScatterData{
		Points: []ScatterPoint{
			{X: 2.1, Y: 4.3, Label: "Matrix ingest", Group: "agent"},
			{X: 4.4, Y: 2.2, Label: "Run sandbox", Group: "agent"},
			{X: 1.2, Y: 1.1, Label: "Knowledge", Group: "knowledge"},
			{X: 3.5, Y: 3.9, Label: "Approval", Group: "human"},
		},
		XTitle: "Reach of the change",
		YTitle: "Impact of the partnership",
	}
	props := ScatterProps{
		Title:          "Конкурентная карта (positioning map)",
		Quadrant:       true,
		QuadrantLabels: []string{"Сильные", "Проверить", "Развивать", "Отложить"},
	}
	dataJSON, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	propsJSON, err := json.Marshal(props)
	if err != nil {
		t.Fatalf("marshal props: %v", err)
	}
	doc := Document{
		Version: "1.0", Type: "presentation", ID: "vis", Title: "Scatter visual",
		Content: []Component{{Type: "scatter", ID: "sc", Data: dataJSON, Props: propsJSON}},
	}
	drawing := LayoutDocument(doc, LayoutOptions{Width: 560, IncludeTitle: true})
	raw, err := RenderPNG(drawing)
	if err != nil {
		t.Fatalf("png: %v", err)
	}
	out := filepath.Join(os.TempDir(), "scatter-debug.png")
	if err := os.WriteFile(out, raw, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Logf("wrote %s (%d bytes)", out, len(raw))
}
