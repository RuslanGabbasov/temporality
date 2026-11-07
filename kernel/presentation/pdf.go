package presentation

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"
	"strconv"
)

// PDF export (docs/presentation-engine.md §17 P3.10): the static frame
// rasterized into a single-page PDF. The document embeds the pixels as a
// FlateDecode RGB image — pure stdlib, no external dependencies, and the
// same draw model as SVG/PNG feeds it, so §7.3 parity holds for every
// format. The export never blocks the agent loop: callers decide where it
// runs (§19).
//
// The writer emits PDF 1.4 with one page sized after the image (the frame's
// logical point size), one XObject image, and xref offsets computed while
// writing. Text lives in the raster, so no font embedding is needed —
// Cyrillic and every glyph the fonts carry survive by construction.

// RenderPDF converts the draw model into single-page PDF bytes.
func RenderPDF(d Drawing) ([]byte, error) {
	widthPx := int(math.Ceil(d.Width * pngScale))
	heightPx := int(math.Ceil(d.Height * pngScale))
	if widthPx <= 0 || heightPx <= 0 || widthPx > 12000 || heightPx > 16000 {
		return nil, fmt.Errorf("pdf frame %dx%d is out of bounds", widthPx, heightPx)
	}
	canvas := image.NewRGBA(image.Rect(0, 0, widthPx, heightPx))
	draw.Draw(canvas, canvas.Bounds(), &image.Uniform{C: color.RGBA{R: 255, G: 255, B: 255, A: 255}}, image.Point{}, draw.Src)
	for _, op := range d.Ops {
		drawPNGOp(canvas, op, pngScale)
	}

	// Flatten to RGB rows: PDF image data carries no alpha channel.
	bounds := canvas.Bounds()
	raw := make([]byte, widthPx*heightPx*3)
	i := 0
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, _ := canvas.At(x, y).RGBA()
			raw[i] = byte(r >> 8)
			raw[i+1] = byte(g >> 8)
			raw[i+2] = byte(b >> 8)
			i += 3
		}
	}

	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	if _, err := writer.Write(raw); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}

	pageW := number(d.Width)
	pageH := number(d.Height)

	imageData := compressed.Bytes()
	content := fmt.Sprintf("q %s 0 0 %s 0 0 cm /Im0 Do Q", pageW, pageH)

	// Object graph, 1-based numbering: 1 catalog, 2 pages, 3 page, 4 image,
	// 5 content stream.
	catalog := fmt.Sprintf("<< /Type /Catalog /Pages 2 0 R >>")
	pages := fmt.Sprintf("<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	page := fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %s %s] /Resources << /XObject << /Im0 4 0 R >> >> /Contents 5 0 R >>", pageW, pageH)
	imageObj := append([]byte(fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceRGB /BitsPerComponent 8 /Filter /FlateDecode /Length %d >>\nstream\n", widthPx, heightPx, len(imageData))), imageData...)
	imageObj = append(imageObj, []byte("\nendstream")...)
	contentObj := fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content)

	bodies := [][]byte{[]byte(catalog), []byte(pages), []byte(page), imageObj, []byte(contentObj)}

	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
	offsets := make([]int, len(bodies))
	for idx, body := range bodies {
		offsets[idx] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", idx+1, body)
	}
	xrefStart := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n", len(bodies)+1)
	buf.WriteString("0000000000 65535 f \n")
	for _, offset := range offsets {
		padded := strconv.FormatInt(int64(offset), 10)
		for len(padded) < 10 {
			padded = "0" + padded
		}
		fmt.Fprintf(&buf, "%s 00000 n \n", padded)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(bodies)+1, xrefStart)
	return buf.Bytes(), nil
}
