package presentation

import (
	"bytes"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// SVG emitter (docs/presentation-engine.md §7.4): one standalone frame per
// Drawing — no external CSS, no scripts (the draw model cannot carry any),
// explicit viewBox and size, text as native glyphs so Cyrillic renders in
// every SVG-capable client.

// RenderSVG converts the draw model into a standalone SVG document.
func RenderSVG(d Drawing) []byte {
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%s" height="%s" viewBox="0 0 %s %s">`,
		number(d.Width), number(d.Height), number(d.Width), number(d.Height))
	b.WriteString("\n")
	// The frame background: static images must stay readable on any channel
	// surface, so they carry their own opaque canvas.
	b.WriteString(`<rect x="0" y="0" width="100%" height="100%" fill="#ffffff"/>` + "\n")
	for _, op := range d.Ops {
		writeSVGOp(&b, op)
	}
	b.WriteString("</svg>\n")
	return b.Bytes()
}

// number formats a float for SVG attributes: no trailing zeros, no exponent.
func number(v float64) string {
	s := strconv.FormatFloat(v, 'f', 2, 64)
	s = strings.TrimRight(s, "0")
	s = strings.TrimSuffix(s, ".")
	if s == "" || s == "-" {
		return "0"
	}
	return s
}

func writeSVGOp(b *bytes.Buffer, op DrawOp) {
	switch v := op.(type) {
	case Rect:
		attrs := fmt.Sprintf(` x="%s" y="%s" width="%s" height="%s"`, number(v.X), number(v.Y), number(v.W), number(v.H))
		if v.Radius > 0 {
			attrs += fmt.Sprintf(` rx="%s"`, number(v.Radius))
		}
		if v.Fill != "" {
			attrs += fmt.Sprintf(` fill="%s"`, svgColor(v.Fill))
		} else {
			attrs += ` fill="none"`
		}
		if v.Stroke != "" {
			attrs += fmt.Sprintf(` stroke="%s" stroke-width="%s"`, svgColor(v.Stroke), number(v.StrokeWidth))
		}
		fmt.Fprintf(b, "<rect%s/>\n", attrs)
	case Segment:
		attrs := fmt.Sprintf(` x1="%s" y1="%s" x2="%s" y2="%s" stroke="%s" stroke-width="%s"`,
			number(v.X1), number(v.Y1), number(v.X2), number(v.Y2), svgColor(v.Color), number(v.Width))
		if len(v.Dash) > 0 {
			attrs += fmt.Sprintf(` stroke-dasharray="%s"`, number(v.Dash[0])+" "+number(v.Dash[len(v.Dash)-1]))
		}
		fmt.Fprintf(b, "<line%s/>\n", attrs)
	case Glyph:
		anchor := v.Anchor
		if anchor == "" {
			anchor = "start"
		}
		family := "sans-serif"
		if v.Mono {
			family = "monospace"
		}
		weight := "normal"
		if v.Bold {
			weight = "bold"
		}
		fmt.Fprintf(b, "<text x=%q y=%q font-size=%q font-family=%q font-weight=%q fill=%q text-anchor=%q>%s</text>\n",
			number(v.X), number(v.Y), number(v.Size), family, weight, svgColor(v.Color), anchor, escapeXML(v.Text))
	case Wedge:
		path := wedgePath(v)
		fmt.Fprintf(b, "<path d=%q fill=%q/>\n", path, svgColor(v.Color))
	case Poly:
		if v.Fill != "" {
			points := polygonPoints(v.Points)
			fmt.Fprintf(b, "<polygon points=%q fill=%q stroke=\"none\"/>\n", points, svgColor(v.Fill))
		}
		if v.Color != "" && len(v.Points) > 1 {
			points := polylinePoints(v.Points)
			attrs := fmt.Sprintf(` points=%q fill="none" stroke=%q stroke-width=%q`, points, svgColor(v.Color), number(v.Width))
			fmt.Fprintf(b, "<polyline%s/>\n", attrs)
		}
	case Dot:
		fmt.Fprintf(b, "<circle cx=%q cy=%q r=%q fill=%q/>\n", number(v.CX), number(v.CY), number(v.R), svgColor(v.Color))
	case Arrow:
		path := arrowPath(v)
		fmt.Fprintf(b, "<path d=%q fill=%q/>\n", path, svgColor(v.Color))
	}
}

// wedgePath builds the SVG path of one (donut) sector. A full circle sector
// is written as two arcs; SVG forbids a single arc sweep of 360°.
func wedgePath(w Wedge) string {
	sweep := w.End - w.Start
	if sweep <= 0 {
		return ""
	}
	cos := func(angle float64) float64 { return w.CX + w.R*math.Cos(angle) }
	sin := func(angle float64) float64 { return w.CY + w.R*math.Sin(angle) }
	large := 0
	if sweep > math.Pi {
		large = 1
	}
	if w.Inner > 0 {
		// Donut: outer arc, line to inner arc, inner arc back, close.
		ix := func(angle float64) float64 { return w.CX + w.Inner*math.Cos(angle) }
		iy := func(angle float64) float64 { return w.CY + w.Inner*math.Sin(angle) }
		return fmt.Sprintf("M %s %s A %s %s 0 %d 1 %s %s L %s %s A %s %s 0 %d 0 %s %s Z",
			number(cos(w.Start)), number(sin(w.Start)),
			number(w.R), number(w.R), large,
			number(cos(w.End)), number(sin(w.End)),
			number(ix(w.End)), number(iy(w.End)),
			number(w.Inner), number(w.Inner), large,
			number(ix(w.Start)), number(iy(w.Start)))
	}
	if sweep >= 2*math.Pi-0.0001 {
		return fmt.Sprintf("M %s %s A %s %s 0 1 1 %s %s A %s %s 0 1 1 %s %s Z",
			number(w.CX), number(w.CY-w.R),
			number(w.R), number(w.R),
			number(w.CX), number(w.CY+w.R),
			number(w.R), number(w.R),
			number(w.CX), number(w.CY-w.R))
	}
	return fmt.Sprintf("M %s %s L %s %s A %s %s 0 %d 1 %s %s Z",
		number(w.CX), number(w.CY),
		number(cos(w.Start)), number(sin(w.Start)),
		number(w.R), number(w.R), large,
		number(cos(w.End)), number(sin(w.End)))
}

// arrowPath builds one filled triangle head at the op position pointing
// along the op angle.
func arrowPath(a Arrow) string {
	tipX := a.X
	tipY := a.Y
	baseX := tipX - a.Size*math.Cos(a.Angle)
	baseY := tipY - a.Size*math.Sin(a.Angle)
	perp := a.Size * 0.6
	leftX := baseX + perp*math.Cos(a.Angle+math.Pi/2)
	leftY := baseY + perp*math.Sin(a.Angle+math.Pi/2)
	rightX := baseX + perp*math.Cos(a.Angle-math.Pi/2)
	rightY := baseY + perp*math.Sin(a.Angle-math.Pi/2)
	return fmt.Sprintf("M %s %s L %s %s L %s %s Z",
		number(tipX), number(tipY),
		number(leftX), number(leftY),
		number(rightX), number(rightY))
}

func polygonPoints(points []Point) string {
	parts := make([]string, 0, len(points))
	for _, p := range points {
		parts = append(parts, number(p.X)+","+number(p.Y))
	}
	return strings.Join(parts, " ")
}

func polylinePoints(points []Point) string {
	return polygonPoints(points)
}

// svgColor validates a color token: the draw model only ever carries palette
// hex values, and anything else falls back to a neutral — a malformed or
// hostile token cannot become XML content.
func svgColor(color string) string {
	if strings.HasPrefix(color, "#") && len(color) == 7 {
		if _, _, _, ok := parseHexColor(color); ok {
			return color
		}
	}
	return "#8d8d8d"
}

// escapeXML escapes the five XML entities for text content.
func escapeXML(text string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	)
	return r.Replace(text)
}
