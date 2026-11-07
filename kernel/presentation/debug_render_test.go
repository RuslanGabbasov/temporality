package presentation

import (
	"testing"
)

// TestDebugRenderSample writes the sample document's static renders to
// /tmp for visual inspection during development. Skipped unless
// PRESENTATION_DEBUG=1.
func TestDebugRenderSample(t *testing.T) {
	if debugRenderOff() {
		t.Skip("set PRESENTATION_DEBUG=1 to write /tmp/presentation-debug.png")
	}
	doc := sampleDocument()
	drawing := LayoutDocument(doc, LayoutOptions{IncludeTitle: true})
	raw, err := RenderPNG(drawing)
	if err != nil {
		t.Fatalf("png: %v", err)
	}
	if err := osWriteFile("/tmp/presentation-debug.png", raw); err != nil {
		t.Fatalf("write: %v", err)
	}
	svg := RenderSVG(drawing)
	if err := osWriteFile("/tmp/presentation-debug.svg", svg); err != nil {
		t.Fatalf("write svg: %v", err)
	}
	t.Logf("png %d bytes, svg %d bytes", len(raw), len(svg))
}
