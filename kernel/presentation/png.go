package presentation

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/math/fixed"
)

// PNG rasterizer (docs/presentation-engine.md §7.5): the same draw model the
// SVG emitter consumes, rasterized at a 2x scale so labels stay readable at
// Telegram and Matrix image sizes. Values, labels and legend are shared with
// SVG by construction (§7.3) — only the pixels differ.

// pngScale is the raster oversampling factor: every point becomes two device
// pixels, which keeps 10pt labels legible after client-side downscaling.
const pngScale = 2.0

// RenderPNG rasterizes the draw model into PNG bytes. A nil font (font parse
// failure) degrades to shapes-only output rather than failing the delivery —
// the fallback renderer still carries the text layer.
func RenderPNG(d Drawing) ([]byte, error) {
	scale := pngScale
	width := int(math.Ceil(d.Width * scale))
	height := int(math.Ceil(d.Height * scale))
	if width <= 0 || height <= 0 || width > 12000 || height > 16000 {
		return nil, fmt.Errorf("png frame %dx%d is out of bounds", width, height)
	}
	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	// Opaque background first (the layout's background rect covers it, but an
	// exact frame keeps the border rows clean).
	draw.Draw(canvas, canvas.Bounds(), &image.Uniform{C: color.RGBA{255, 255, 255, 255}}, image.Point{}, draw.Src)
	for _, op := range d.Ops {
		drawPNGOp(canvas, op, scale)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, canvas); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// renderColor parses a palette hex into a device color.
func renderColor(hex string) color.RGBA {
	r, g, b, ok := parseHexColor(hex)
	if !ok {
		r, g, b = 0x8d, 0x8d, 0x8d
	}
	return color.RGBA{R: r, G: g, B: b, A: 255}
}

func drawPNGOp(canvas *image.RGBA, op DrawOp, scale float64) {
	switch v := op.(type) {
	case Rect:
		x := int(v.X * scale)
		y := int(v.Y * scale)
		w := int(v.W * scale)
		h := int(v.H * scale)
		if v.Fill != "" {
			fillRect(canvas, image.Rect(x, y, x+w, y+h), renderColor(v.Fill))
		}
		if v.Stroke != "" && v.StrokeWidth > 0 {
			thickness := int(math.Max(1, v.StrokeWidth*scale))
			stroke := renderColor(v.Stroke)
			fillRect(canvas, image.Rect(x, y, x+w, y+thickness), stroke)
			fillRect(canvas, image.Rect(x, y+h-thickness, x+w, y+h), stroke)
			fillRect(canvas, image.Rect(x, y, x+thickness, y+h), stroke)
			fillRect(canvas, image.Rect(x+w-thickness, y, x+w, y+h), stroke)
		}
	case Segment:
		drawLine(canvas, v.X1*scale, v.Y1*scale, v.X2*scale, v.Y2*scale, renderColor(v.Color), int(math.Max(1, v.Width*scale)))
	case Glyph:
		drawPNGText(canvas, v, scale)
	case Wedge:
		fillWedge(canvas, v, scale)
	case Poly:
		if v.Fill != "" {
			fillPolygon(canvas, v.Points, scale, renderColor(v.Fill))
		}
		if v.Color != "" && len(v.Points) > 1 {
			for i := 0; i < len(v.Points)-1; i++ {
				drawLine(canvas, v.Points[i].X*scale, v.Points[i].Y*scale, v.Points[i+1].X*scale, v.Points[i+1].Y*scale, renderColor(v.Color), int(math.Max(1, v.Width*scale)))
			}
		}
	case Dot:
		fillCircle(canvas, v.CX*scale, v.CY*scale, v.R*scale, renderColor(v.Color))
	case Arrow:
		tipX := v.X
		tipY := v.Y
		baseX := tipX - v.Size*math.Cos(v.Angle)
		baseY := tipY - v.Size*math.Sin(v.Angle)
		perp := v.Size * 0.6
		left := Point{X: baseX + perp*math.Cos(v.Angle+math.Pi/2), Y: baseY + perp*math.Sin(v.Angle+math.Pi/2)}
		right := Point{X: baseX + perp*math.Cos(v.Angle-math.Pi/2), Y: baseY + perp*math.Sin(v.Angle-math.Pi/2)}
		fillPolygon(canvas, []Point{{X: tipX, Y: tipY}, left, right}, scale, renderColor(v.Color))
	}
}

// fillRect fills an axis-aligned rectangle clipped to the canvas.
func fillRect(canvas *image.RGBA, rect image.Rectangle, c color.RGBA) {
	draw.Draw(canvas, canvas.Bounds().Intersect(rect), &image.Uniform{C: c}, image.Point{}, draw.Src)
}

// drawLine rasterizes a thick line segment by sampling the path and stamping
// a round pen — simple, branch-free and visually equal to SVG strokes.
func drawLine(canvas *image.RGBA, x1, y1, x2, y2 float64, c color.RGBA, thickness int) {
	if thickness < 1 {
		thickness = 1
	}
	radius := float64(thickness) / 2
	length := math.Hypot(x2-x1, y2-y1)
	steps := int(length*2) + 1
	for i := 0; i <= steps; i++ {
		t := float64(i) / float64(steps)
		fillCircle(canvas, x1+(x2-x1)*t, y1+(y2-y1)*t, radius, c)
	}
}

// fillCircle paints one filled disc.
func fillCircle(canvas *image.RGBA, cx, cy, r float64, c color.RGBA) {
	if r <= 0 {
		return
	}
	minX := int(math.Floor(cx - r))
	maxX := int(math.Ceil(cx + r))
	minY := int(math.Floor(cy - r))
	maxY := int(math.Ceil(cy + r))
	bounds := canvas.Bounds()
	rr := r * r
	for y := minY; y <= maxY; y++ {
		if y < bounds.Min.Y || y >= bounds.Max.Y {
			continue
		}
		for x := minX; x <= maxX; x++ {
			if x < bounds.Min.X || x >= bounds.Max.X {
				continue
			}
			dx := float64(x) + 0.5 - cx
			dy := float64(y) + 0.5 - cy
			if dx*dx+dy*dy <= rr {
				canvas.SetRGBA(x, y, c)
			}
		}
	}
}

// fillWedge paints one (donut) sector by per-pixel angle tests.
func fillWedge(canvas *image.RGBA, w Wedge, scale float64) {
	cx := w.CX * scale
	cy := w.CY * scale
	r := w.R * scale
	inner := w.Inner * scale
	sweep := w.End - w.Start
	if sweep <= 0 || r <= 0 {
		return
	}
	minX := int(math.Floor(cx - r))
	maxX := int(math.Ceil(cx + r))
	minY := int(math.Floor(cy - r))
	maxY := int(math.Ceil(cy + r))
	bounds := canvas.Bounds()
	c := renderColor(w.Color)
	for y := minY; y <= maxY; y++ {
		if y < bounds.Min.Y || y >= bounds.Max.Y {
			continue
		}
		for x := minX; x <= maxX; x++ {
			if x < bounds.Min.X || x >= bounds.Max.X {
				continue
			}
			dx := float64(x) + 0.5 - cx
			dy := float64(y) + 0.5 - cy
			dist := math.Sqrt(dx*dx + dy*dy)
			if dist > r || dist < inner {
				continue
			}
			angle := math.Atan2(dy, dx)
			if !angleInArc(angle, w.Start, sweep) {
				continue
			}
			canvas.SetRGBA(x, y, c)
		}
	}
}

// angleInArc reports whether angle lies in [start, start+sweep) on the
// unwrapped circle (both values may be negative).
func angleInArc(angle, start, sweep float64) bool {
	// Normalize the offset into [0, 2pi).
	twoPi := 2 * math.Pi
	offset := math.Mod(angle-start, twoPi)
	if offset < 0 {
		offset += twoPi
	}
	if sweep >= twoPi {
		return true
	}
	return offset < sweep
}

// fillPolygon paints one filled polygon via a scanline even-odd test.
func fillPolygon(canvas *image.RGBA, points []Point, scale float64, c color.RGBA) {
	if len(points) < 3 {
		return
	}
	scaled := make([]Point, len(points))
	minY, maxY := math.Inf(1), math.Inf(-1)
	for i, p := range points {
		scaled[i] = Point{X: p.X * scale, Y: p.Y * scale}
		if scaled[i].Y < minY {
			minY = scaled[i].Y
		}
		if scaled[i].Y > maxY {
			maxY = scaled[i].Y
		}
	}
	bounds := canvas.Bounds()
	startY := int(math.Floor(minY))
	endY := int(math.Ceil(maxY))
	for y := startY; y <= endY; y++ {
		if y < bounds.Min.Y || y >= bounds.Max.Y {
			continue
		}
		scanY := float64(y) + 0.5
		xs := make([]float64, 0, 2)
		j := len(scaled) - 1
		for i := 0; i < len(scaled); i++ {
			p1, p2 := scaled[i], scaled[j]
			if (p1.Y < scanY && p2.Y >= scanY) || (p2.Y < scanY && p1.Y >= scanY) {
				xs = append(xs, p1.X+(scanY-p1.Y)/(p2.Y-p1.Y)*(p2.X-p1.X))
			}
			j = i
		}
		sortFloats(xs)
		for k := 0; k+1 < len(xs); k += 2 {
			from := int(math.Ceil(xs[k] - 0.5))
			to := int(math.Floor(xs[k+1] - 0.5))
			for x := from; x <= to; x++ {
				if x < bounds.Min.X || x >= bounds.Max.X {
					continue
				}
				canvas.SetRGBA(x, y, c)
			}
		}
	}
}

func sortFloats(values []float64) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

// drawPNGText paints one glyph run with the same face measurement used at
// layout time. Missing fonts skip the run (shapes survive).
var textDrawerMu = &sync.Mutex{}

func drawPNGText(canvas *image.RGBA, g Glyph, scale float64) {
	face := cachedFace(g.Size*scale, g.Bold, g.Mono)
	if face == nil {
		return
	}
	d := font.Drawer{
		Dst:  canvas,
		Src:  image.NewUniform(renderColor(g.Color)),
		Face: face,
	}
	x := g.X * scale
	y := g.Y * scale
	switch g.Anchor {
	case "middle":
		textMu.Lock()
		advance := font.MeasureString(face, g.Text)
		textMu.Unlock()
		x -= float64(advance) / 64 / 2
	case "end":
		textMu.Lock()
		advance := font.MeasureString(face, g.Text)
		textMu.Unlock()
		x -= float64(advance) / 64
	}
	textMu.Lock()
	defer textMu.Unlock()
	d.Dot = fixed.Point26_6{X: fixed.Int26_6(x * 64), Y: fixed.Int26_6(y * 64)}
	d.DrawString(g.Text)
}

// DebugWritePNG writes one rendered frame to a file — a developer aid for
// visual checks of the static renderers (never used in production paths).
func DebugWritePNG(path string, d Drawing) error {
	raw, err := RenderPNG(d)
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}
