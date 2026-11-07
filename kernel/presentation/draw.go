package presentation

import (
	"strconv"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
)

// The draw model (docs/presentation-engine.md §7.3, single source of truth):
// layout turns a validated document into a list of primitive draw operations,
// and both the SVG emitter and the PNG rasterizer consume exactly that list.
// Two consequences follow by construction: data cannot diverge between
// formats, and a new static format is one emitter away.

// Drawing is one rendered static frame in layout points (1pt = 1px at scale 1).
type Drawing struct {
	Width  float64
	Height float64
	Ops    []DrawOp
}

// DrawOp is one primitive operation.
type DrawOp interface{ drawOp() }

// Rect is a filled (optionally stroked) rectangle with optional corner radius.
type Rect struct {
	X, Y, W, H  float64
	Fill        string
	Stroke      string
	StrokeWidth float64
	Radius      float64
}

func (Rect) drawOp() {}

// Segment is a straight line.
type Segment struct {
	X1, Y1, X2, Y2 float64
	Color          string
	Width          float64
	Dash           []float64
}

func (Segment) drawOp() {}

// Glyph is one positioned single-style text run. Baseline is the y of the
// text baseline (the layout engine positions it, not the emitters).
type Glyph struct {
	X, Y   float64
	Size   float64
	Bold   bool
	Mono   bool
	Color  string
	Anchor string // "start" | "middle" | "end"
	Text   string
}

func (Glyph) drawOp() {}

// Wedge is one filled circular sector (pie slice); Inner>0 renders a donut.
type Wedge struct {
	CX, CY float64
	R      float64
	Inner  float64
	Start  float64 // radians, 0 = 12 o'clock, clockwise
	End    float64
	Color  string
}

func (Wedge) drawOp() {}

// Poly is a polyline; when Fill is set, the given points are closed and
// filled first (area charts close against the baseline by appending the
// baseline corners to Points).
type Poly struct {
	Points []Point
	Color  string
	Width  float64
	Fill   string
}

func (Poly) drawOp() {}

// Point is a plain coordinate pair.
type Point struct{ X, Y float64 }

// Dot is one filled circle (scatter points, timeline nodes, legend chips).
type Dot struct {
	CX, CY, R float64
	Color     string
}

func (Dot) drawOp() {}

// Arrow is one filled triangle head at (X,Y) pointing along Angle.
type Arrow struct {
	X, Y, Size float64
	Angle      float64 // radians
	Color      string
}

func (Arrow) drawOp() {}

// Theme is the static-render palette (docs/presentation-engine.md §7.7: light
// and dark variants). Channels usually render light images; the theme is a
// layout option, never agent-controlled (§8.2).
type Theme struct {
	Background    string
	Surface       string
	Border        string
	TextPrimary   string
	TextSecondary string
	Accent        string
	Success       string
	Info          string
	Warning       string
	Danger        string
	Neutral       string
}

// LightTheme is the default static palette.
func LightTheme() Theme {
	return Theme{
		Background:    "#ffffff",
		Surface:       "#f4f4f4",
		Border:        "#e0e0e0",
		TextPrimary:   "#161616",
		TextSecondary: "#525252",
		Accent:        "#0f62fe",
		Success:       "#198038",
		Info:          "#0f62fe",
		Warning:       "#d09900",
		Danger:        "#da1e28",
		Neutral:       "#8d8d8d",
	}
}

// DarkTheme mirrors the light palette for dark-themed channels.
func DarkTheme() Theme {
	return Theme{
		Background:    "#161616",
		Surface:       "#262626",
		Border:        "#393939",
		TextPrimary:   "#f4f4f4",
		TextSecondary: "#a8a8a8",
		Accent:        "#4589ff",
		Success:       "#24a148",
		Info:          "#4589ff",
		Warning:       "#f1c21b",
		Danger:        "#fa4d56",
		Neutral:       "#8d8d8d",
	}
}

// StatusColor maps an IR status/severity onto the theme palette.
func (t Theme) StatusColor(status string) string {
	switch status {
	case statusSuccess:
		return t.Success
	case statusInfo:
		return t.Info
	case statusWarning:
		return t.Warning
	case statusDanger:
		return t.Danger
	default:
		return t.Neutral
	}
}

// fonts lazily parses the embedded Go fonts (Latin + Cyrillic coverage is
// why the Go fonts are the static-render choice: §7.7 requires correct
// Cyrillic in SVG and PNG).
var fonts = struct {
	once     sync.Once
	regular  *opentype.Font
	bold     *opentype.Font
	mono     *opentype.Font
	parseErr error
}{
	regular: nil, bold: nil, mono: nil,
}

func loadFonts() (*opentype.Font, *opentype.Font, *opentype.Font, error) {
	fonts.once.Do(func() {
		regular, err := opentype.Parse(goregular.TTF)
		if err != nil {
			fonts.parseErr = err
			return
		}
		bold, err := opentype.Parse(gobold.TTF)
		if err != nil {
			fonts.parseErr = err
			return
		}
		mono, err := opentype.Parse(gomono.TTF)
		if err != nil {
			fonts.parseErr = err
			return
		}
		fonts.regular, fonts.bold, fonts.mono = regular, bold, mono
	})
	return fonts.regular, fonts.bold, fonts.mono, fonts.parseErr
}

// measureText returns the rendered width of text at the given point size,
// using the same font the PNG rasterizer draws with — the SVG layout and the
// PNG layout must agree on wrap points (§7.7 consistent sizes).
//
// font.Face is not safe for concurrent use, so measurement and rasterized
// text drawing share one package-level lock: text work is a small fraction of
// a render, and correctness beats parallelism here.
var textMu sync.Mutex

func measureText(size float64, bold, mono bool, text string) float64 {
	face := cachedFace(size, bold, mono)
	if face == nil {
		return float64(len(text)) * size * 0.55
	}
	textMu.Lock()
	defer textMu.Unlock()
	width := font.MeasureString(face, text)
	return float64(width) / 64
}

// textFace builds a font.Face at the requested size. DPI is fixed at 72 so
// point size equals pixel size and layout coordinates stay in one unit.
func textFace(size float64, bold, mono bool) font.Face {
	regular, boldFont, monoFont, err := loadFonts()
	if err != nil {
		return nil
	}
	source := regular
	if mono {
		source = monoFont
	} else if bold {
		source = boldFont
	}
	face, err := opentype.NewFace(source, &opentype.FaceOptions{
		Size: size,
		DPI:  72,
	})
	if err != nil {
		return nil
	}
	return face
}

// faceCache is a process-wide face cache: building a truetype face per
// measurement is expensive and layout measures thousands of runs.
var faceCache sync.Map // faceKey -> font.Face

func cachedFace(size float64, bold, mono bool) font.Face {
	key := faceKey(size, bold, mono)
	if face, ok := faceCache.Load(key); ok {
		return face.(font.Face)
	}
	face := textFace(size, bold, mono)
	if face == nil {
		return nil
	}
	actual, _ := faceCache.LoadOrStore(key, face)
	return actual.(font.Face)
}

func faceKey(size float64, bold, mono bool) string {
	key := strconv.FormatFloat(size, 'f', -1, 64)
	if bold {
		key += "b"
	}
	if mono {
		key += "m"
	}
	return key
}
