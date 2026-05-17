package assets

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log/slog"
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
	stateFontDPI    = 72.0
	stateCenterDiv  = 2
	stateAppIconKey = "icons/a2t-state-inactive.svg"

	// stateFontRatio is the font height as a fraction of the icon edge.
	// At 64px master the gobold size was 42, which fills the disc without
	// touching the rim — keep the same proportion at every resize so
	// installed 128/256 icons stay visually identical to the in-app one.
	stateFontRatio = 42.0 / 64.0
	// stateBaselineRatio is the baseline Y as a fraction of the icon
	// edge. At 64px master baseline was 47 (= 64*72/100 + 1) — the same
	// 0.734 ratio is reused for every output size.
	stateBaselineRatio = 47.0 / 64.0
)

// Fallback grey when an SVG has no <circle fill="..."> attribute.
const (
	stateFallbackR uint8 = 128
	stateFallbackG uint8 = 128
	stateFallbackB uint8 = 128
)

// StateIconPNG rasterises a state SVG at the default 64×64 master size.
// Equivalent to StateIconPNGSized(svgBytes, 64) — kept for callers that
// do not care about the size (the tray, the in-app Fyne icon).
func StateIconPNG(svgBytes []byte) []byte {
	return StateIconPNGSized(svgBytes, stateIconPx)
}

// StateIconPNGSized rasterises a state SVG at the requested edge size in
// pixels: takes only the <circle> fill colour, then redraws a fresh PNG
// with the "a2" label centred on top using the bundled gobold font.
// Returns a sizePx×sizePx NRGBA-encoded PNG.
//
// Used by cmd/genappicon to write 64/128/256 hicolor variants from a
// single source SVG so HiDPI docks (GNOME at scale ≥1.5) get a crisp
// icon instead of an upscaled blur. The full SVG markup (text,
// transforms, paths) is intentionally ignored — we drive rendering
// from Go because no pure-Go SVG rasteriser in our dep tree supports
// <text>, and the icons are always "letters on coloured disc".
func StateIconPNGSized(svgBytes []byte, sizePx int) []byte {
	if sizePx <= 0 {
		sizePx = stateIconPx
	}

	bg := parseHexColor(parseSVGCircleFill(svgBytes))

	img := image.NewNRGBA(image.Rect(0, 0, sizePx, sizePx))
	cx := float64(sizePx) * stateIconHalf

	drawDisc(img, cx, cx, cx, bg)
	drawLabel(img, sizePx, stateLabel,
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

// encodePNG serialises an in-memory NRGBA into a PNG byte slice. The
// error path is unreachable in practice (bytes.Buffer.Write never fails
// for a valid NRGBA), but logging + nil return is preferred over a
// panic so a stdlib regression or OOM does not abort the daemon.
func encodePNG(img *image.NRGBA) []byte {
	var buf bytes.Buffer

	if err := png.Encode(&buf, img); err != nil {
		slog.Error("assets: png encode failed; returning empty icon",
			slog.String("error", fmt.Sprintf("%v", err)),
		)

		return nil
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

// gobold is parsed once. opentype.Parse is not free; a single shared
// parsed font lets us spin up a face per requested size on demand
// without re-parsing the TTF every call.
//
//nolint:gochecknoglobals // immutable after init under sync.Once
var (
	gobboldOnce sync.Once
	gobboldFont *opentype.Font

	iconFaceMu    sync.Mutex
	iconFaceCache = map[int]font.Face{}
)

func parsedGobold() *opentype.Font {
	gobboldOnce.Do(func() {
		parsed, err := opentype.Parse(gobold.TTF)
		if err != nil {
			panic(fmt.Sprintf("assets: parse gobold: %v", err))
		}

		gobboldFont = parsed
	})

	return gobboldFont
}

// iconFaceFor returns a gobold face scaled for the requested icon edge
// size. Faces are cached per size — the tray hits 64px on every state
// transition, and the install path renders 64/128/256 once at startup,
// so a tiny map keeps both hot.
func iconFaceFor(sizePx int) font.Face {
	iconFaceMu.Lock()
	defer iconFaceMu.Unlock()

	if cached, ok := iconFaceCache[sizePx]; ok {
		return cached
	}

	face, err := opentype.NewFace(parsedGobold(), &opentype.FaceOptions{
		Size:    float64(sizePx) * stateFontRatio,
		DPI:     stateFontDPI,
		Hinting: font.HintingFull,
	})
	if err != nil {
		panic(fmt.Sprintf("assets: build font face: %v", err))
	}

	iconFaceCache[sizePx] = face

	return face
}

// drawLabel writes label centred horizontally on img at a baseline
// proportional to the icon edge, so a 256px install icon looks like a
// scaled-up version of the 64px in-app icon and not like a tiny glyph
// floating on a giant disc.
func drawLabel(img *image.NRGBA, sizePx int, label string, col color.NRGBA) {
	face := iconFaceFor(sizePx)
	advance := font.MeasureString(face, label)
	startX := (fixed.I(sizePx) - advance) / stateCenterDiv
	baselineY := int(float64(sizePx)*stateBaselineRatio) + 1

	drawer := &font.Drawer{
		Dst:  img,
		Src:  image.NewUniform(col),
		Face: face,
		Dot:  fixed.Point26_6{X: startX, Y: fixed.I(baselineY)},
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
