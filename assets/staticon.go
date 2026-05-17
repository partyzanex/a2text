package assets

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"strconv"
	"strings"
	"sync"

	"fyne.io/fyne/v2"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// Icon rendering constants. The output is a single small PNG used by:
//
//   - the tray, where StatusNotifierItem renders it at ~22px on most
//     Linux desktops (Plasma scales down our 64px master cleanly);
//   - the Fyne settings window title-bar and taskbar entry.
//
// Master size 64 is large enough that the "a2" glyphs stay legible
// after the DE downscales it for the system tray; the previous 48
// looked noticeably blurred on HiDPI panels.
const (
	stateIconPx     = 64
	stateIconHalf   = 0.5
	stateIconAlpha  = 255.0
	stateColorFull  = uint8(stateIconAlpha)
	stateHexLen     = 6
	stateHexBase    = 16
	stateHexBits    = 8
	stateHexREnd    = 2
	stateHexGEnd    = 4
	stateLabel      = "a2"
	stateFontSize   = 42.0 // gobold size that fills the 64px circle
	stateFontDPI    = 72.0
	stateBaselineY  = stateIconPx*72/100 + 1
	stateCenterDiv  = 2
	stateAppIconKey = "icons/a2t-state-inactive.svg"
)

// Fallback grey when an SVG has no <circle fill="..."> attribute.
const (
	stateFallbackR uint8 = 128
	stateFallbackG uint8 = 128
	stateFallbackB uint8 = 128
)

// StateIconPNG rasterises a state SVG: takes only the <circle> fill
// colour, then redraws a fresh PNG with the "a2" label centred on top
// using the bundled gobold font. Returns a 64×64 NRGBA-encoded PNG.
//
// The full SVG markup (text, transforms, paths) is intentionally
// ignored — we drive the rendering from Go because no pure-Go SVG
// rasteriser in our dep tree supports <text>, and the icons are
// always "letters on coloured disc" by design.
func StateIconPNG(svgBytes []byte) []byte {
	bg := parseHexColor(parseSVGCircleFill(svgBytes))

	img := image.NewNRGBA(image.Rect(0, 0, stateIconPx, stateIconPx))
	cx := float64(stateIconPx) * stateIconHalf

	drawDisc(img, cx, cx, cx, bg)
	drawLabel(img, stateLabel,
		color.NRGBA{R: stateColorFull, G: stateColorFull, B: stateColorFull, A: stateColorFull})

	return encodePNG(img)
}

// AppIcon returns the "a2" application icon as a Fyne resource. Used
// by the settings window for its title-bar and DE taskbar entry so the
// window matches the tray indicator. Built from the inactive (grey)
// state SVG so the resting colour represents the app at large.
//
//nolint:ireturn // fyne.Resource is the consumer's only stable contract
func AppIcon() fyne.Resource {
	body, err := Icons.ReadFile(stateAppIconKey)
	if err != nil {
		panic(fmt.Sprintf("assets: AppIcon read %s: %v", stateAppIconKey, err))
	}

	return fyne.NewStaticResource("a2text.png", StateIconPNG(body))
}

func encodePNG(img *image.NRGBA) []byte {
	var buf bytes.Buffer

	if err := png.Encode(&buf, img); err != nil {
		panic(fmt.Sprintf("assets: png encode: %v", err))
	}

	return buf.Bytes()
}

// drawDisc renders a full-bleed anti-aliased filled circle onto img.
// Pixels at the rim are blended proportionally to their distance from
// the geometric boundary; pixels outside are untouched.
func drawDisc(img *image.NRGBA, cx, cy, radius float64, col color.NRGBA) {
	b := img.Bounds()

	for iy := b.Min.Y; iy < b.Max.Y; iy++ {
		for ix := b.Min.X; ix < b.Max.X; ix++ {
			dx := float64(ix) + stateIconHalf - cx
			dy := float64(iy) + stateIconHalf - cy
			dist := math.Sqrt(dx*dx + dy*dy)

			if dist > radius {
				continue
			}

			frac := min(radius-dist, 1.0)
			img.SetNRGBA(ix, iy, color.NRGBA{
				R: col.R, G: col.G, B: col.B,
				A: uint8(stateIconAlpha * frac),
			})
		}
	}
}

// iconFace caches the parsed gobold font face. opentype.Parse +
// NewFace are not free, and the renderer is hit on every tray-state
// transition and on every settings-window open; one shared face keeps
// that path zero-alloc after the first call.
//
//nolint:gochecknoglobals // immutable after init under sync.Once
var (
	iconFaceOnce sync.Once
	iconFace     font.Face
)

func getIconFace() font.Face {
	iconFaceOnce.Do(func() {
		parsed, err := opentype.Parse(gobold.TTF)
		if err != nil {
			panic(fmt.Sprintf("assets: parse gobold: %v", err))
		}

		face, err := opentype.NewFace(parsed, &opentype.FaceOptions{
			Size:    stateFontSize,
			DPI:     stateFontDPI,
			Hinting: font.HintingFull,
		})
		if err != nil {
			panic(fmt.Sprintf("assets: build font face: %v", err))
		}

		iconFace = face
	})

	return iconFace
}

// drawLabel writes label centred horizontally on img using the cached
// gobold face at stateBaselineY. Vertical placement is fixed (the
// icons are always stateIconPx square); horizontal centring is
// computed from font metrics so the label stays put across any future
// relabelling.
func drawLabel(img *image.NRGBA, label string, col color.NRGBA) {
	face := getIconFace()
	advance := font.MeasureString(face, label)
	startX := (fixed.I(stateIconPx) - advance) / stateCenterDiv

	drawer := &font.Drawer{
		Dst:  img,
		Src:  image.NewUniform(col),
		Face: face,
		Dot:  fixed.Point26_6{X: startX, Y: fixed.I(stateBaselineY)},
	}
	drawer.DrawString(label)
}

// parseSVGCircleFill extracts the fill colour from the first <circle>
// element of an SVG. Empty string when none — parseHexColor then falls
// back to grey.
func parseSVGCircleFill(svgBytes []byte) string {
	ss := string(svgBytes)

	ci := strings.Index(ss, "<circle")
	if ci < 0 {
		return ""
	}

	end := strings.Index(ss[ci:], "/>")
	if end < 0 {
		return ""
	}

	return extractAttrVal(ss[ci:ci+end], "fill")
}

// extractAttrVal returns the value of attribute name from an XML
// element fragment. Include a leading space in name to avoid partial
// matches (e.g. " d" would otherwise also match "id").
func extractAttrVal(elem, name string) string {
	key := name + `="`

	_, rest, ok := strings.Cut(elem, key)
	if !ok {
		return ""
	}

	val, _, found := strings.Cut(rest, `"`)
	if !found {
		return ""
	}

	return val
}

// parseHexColor parses a "#RRGGBB" colour string. Returns the fallback
// grey on parse failure so a typo in an SVG file at least produces a
// visible icon instead of transparency.
func parseHexColor(hex string) color.NRGBA {
	hex = strings.TrimPrefix(hex, "#")

	if len(hex) != stateHexLen {
		return color.NRGBA{R: stateFallbackR, G: stateFallbackG, B: stateFallbackB, A: stateColorFull}
	}

	rr, errR := strconv.ParseUint(hex[:stateHexREnd], stateHexBase, stateHexBits)
	gg, errG := strconv.ParseUint(hex[stateHexREnd:stateHexGEnd], stateHexBase, stateHexBits)
	bb, errB := strconv.ParseUint(hex[stateHexGEnd:], stateHexBase, stateHexBits)

	if errR != nil || errG != nil || errB != nil {
		return color.NRGBA{R: stateFallbackR, G: stateFallbackG, B: stateFallbackB, A: stateColorFull}
	}

	return color.NRGBA{R: uint8(rr), G: uint8(gg), B: uint8(bb), A: stateColorFull}
}
