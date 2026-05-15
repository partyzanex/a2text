package tray

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/partyzanex/a2text/assets"
	"github.com/partyzanex/a2text/internal/domain"
)

// SVG rendering constants.
const (
	svgIconPx         = 48                  // output PNG edge in pixels
	svgViewBox        = 256.0               // canonical SVG viewBox edge
	svgHexLen         = 6                   // expected RRGGBB hex length
	svgHexBase        = 16                  // hexadecimal numeral base
	svgHexBits        = 8                   // uint8 bit width for ParseUint
	svgFloat64        = 64                  // float64 bit width for ParseFloat
	svgMinPts         = 3                   // minimum polygon vertices for scanline fill
	svgHexREnd        = 2                   // slice end for R channel (0..2)
	svgHexGEnd        = 4                   // slice end for G channel (2..4)
	svgTranslateParts = 2                   // "translate(tx,ty)" yields exactly two parts
	colorFull         = uint8(iconMaxAlpha) // maximum 8-bit colour channel value
)

type iconEntry struct {
	file  string
	state domain.State
}

func iconEntries() []iconEntry {
	return []iconEntry{
		{"icons/a2t-state-inactive.svg", domain.StateIdle},
		{"icons/a2t-state-recording.svg", domain.StateRecording},
		{"icons/a2t-state-transcribing.svg", domain.StateTranscribing},
	}
}

// loadIcons populates tr.icons from the embedded SVG files on the first call.
func (tr *Tray) loadIcons() {
	tr.iconOnce.Do(func() {
		tr.icons = make(map[domain.State][]byte)

		entries := iconEntries()
		for i := range entries {
			data, err := assets.Icons.ReadFile(entries[i].file)
			if err != nil {
				continue
			}

			tr.icons[entries[i].state] = renderSVGIcon(data)
		}
	})
}

// iconFor returns the PNG icon for state. SVG icons are used when available;
// a programmatic colour circle is returned for states without a dedicated SVG.
func (tr *Tray) iconFor(state domain.State) []byte {
	tr.loadIcons()

	if data, ok := tr.icons[state]; ok {
		return data
	}

	rr, gg, bb := colorForState(state)

	return circleIcon(rr, gg, bb)
}

// renderSVGIcon decodes a state-icon SVG and returns a svgIconPx×svgIconPx PNG.
// Falls back to a grey circle when parsing fails.
func renderSVGIcon(svgBytes []byte) []byte {
	bgHex, transformStr, pathD := parseSVGParts(svgBytes)

	bg := parseHexColor(bgHex)
	tx, ty, sc := parseSVGTransform(transformStr)
	pts := applySVGTransform(parseSVGPath(pathD), tx, ty, sc)

	img := image.NewNRGBA(image.Rect(0, 0, svgIconPx, svgIconPx))
	cx := float64(svgIconPx) * iconHalfPixel

	drawCircleOnto(img, cx, cx, cx, bg)
	fillPolygon(img, pts, float64(svgIconPx)/svgViewBox,
		color.NRGBA{R: colorFull, G: colorFull, B: colorFull, A: colorFull})

	return encodePNG(img)
}

func encodePNG(img *image.NRGBA) []byte {
	var buf bytes.Buffer

	if err := png.Encode(&buf, img); err != nil {
		panic(fmt.Sprintf("tray: encodePNG: png.Encode: %v", err))
	}

	return buf.Bytes()
}

// drawCircleOnto renders a full-bleed anti-aliased circle onto img.
// Pixels at the rim are blended proportionally; pixels outside are untouched.
func drawCircleOnto(img *image.NRGBA, cx, cy, radius float64, col color.NRGBA) {
	b := img.Bounds()

	for iy := b.Min.Y; iy < b.Max.Y; iy++ {
		for ix := b.Min.X; ix < b.Max.X; ix++ {
			dx := float64(ix) + iconHalfPixel - cx
			dy := float64(iy) + iconHalfPixel - cy
			dist := math.Sqrt(dx*dx + dy*dy)

			if dist > radius {
				continue
			}

			frac := min(radius-dist, 1.0)
			img.SetNRGBA(ix, iy, color.NRGBA{
				R: col.R, G: col.G, B: col.B,
				A: uint8(iconMaxAlpha * frac),
			})
		}
	}
}

// parseSVGParts extracts the circle fill colour, path transform string, and
// path d attribute from a state-icon SVG file.
func parseSVGParts(svgBytes []byte) (bgHex, pathTransform, pathD string) {
	ss := string(svgBytes)

	if ci := strings.Index(ss, "<circle"); ci >= 0 {
		if end := strings.Index(ss[ci:], "/>"); end >= 0 {
			bgHex = extractAttrVal(ss[ci:ci+end], "fill")
		}
	}

	if pi := strings.Index(ss, "<path"); pi >= 0 {
		if end := strings.Index(ss[pi:], "/>"); end >= 0 {
			seg := ss[pi : pi+end]
			pathTransform = extractAttrVal(seg, "transform")
			pathD = extractAttrVal(seg, " d")
		}
	}

	return bgHex, pathTransform, pathD
}

// extractAttrVal returns the value of attribute name from an XML element
// fragment. Include a leading space in name to avoid partial matches
// (e.g. " d" avoids matching "id").
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

// parseHexColor parses a "#RRGGBB" colour string.
// Returns idle grey on parse failure.
func parseHexColor(hex string) color.NRGBA {
	hex = strings.TrimPrefix(hex, "#")

	if len(hex) != svgHexLen {
		return color.NRGBA{R: colorIdleR, G: colorIdleG, B: colorIdleB, A: colorFull}
	}

	rr, errR := strconv.ParseUint(hex[:svgHexREnd], svgHexBase, svgHexBits)
	gg, errG := strconv.ParseUint(hex[svgHexREnd:svgHexGEnd], svgHexBase, svgHexBits)
	bb, errB := strconv.ParseUint(hex[svgHexGEnd:], svgHexBase, svgHexBits)

	if errR != nil || errG != nil || errB != nil {
		return color.NRGBA{R: colorIdleR, G: colorIdleG, B: colorIdleB, A: colorFull}
	}

	return color.NRGBA{R: uint8(rr), G: uint8(gg), B: uint8(bb), A: colorFull}
}

// parseSVGTransform parses "translate(tx,ty) scale(s)".
// Scale defaults to 1.0 when absent.
func parseSVGTransform(trf string) (tx, ty, scale float64) {
	tx, ty = parseSVGTranslate(trf)
	scale = parseSVGScale(trf)

	return tx, ty, scale
}

// parseSVGTranslate extracts the tx,ty values from "translate(tx,ty)" in trf.
func parseSVGTranslate(trf string) (tx, ty float64) {
	_, inside, ok := strings.Cut(trf, "translate(")
	if !ok {
		return 0, 0
	}

	content, _, found := strings.Cut(inside, ")")
	if !found {
		return 0, 0
	}

	parts := strings.SplitN(content, ",", svgTranslateParts)
	if len(parts) != svgTranslateParts {
		return 0, 0
	}

	if vv, err := strconv.ParseFloat(strings.TrimSpace(parts[0]), svgFloat64); err == nil {
		tx = vv
	}

	if vv, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), svgFloat64); err == nil {
		ty = vv
	}

	return tx, ty
}

// parseSVGScale extracts the scale factor from "scale(s)" in trf.
// Returns 1.0 when absent or unparseable.
func parseSVGScale(trf string) float64 {
	_, inside, ok := strings.Cut(trf, "scale(")
	if !ok {
		return 1.0
	}

	content, _, found := strings.Cut(inside, ")")
	if !found {
		return 1.0
	}

	sc, err := strconv.ParseFloat(strings.TrimSpace(content), svgFloat64)
	if err != nil {
		return 1.0
	}

	return sc
}

// parseSVGPath parses a path d attribute containing only M, L, and Z commands
// and returns the polygon vertices.
func parseSVGPath(pathD string) [][2]float64 {
	tokens := strings.Fields(pathD)

	var pts [][2]float64

	for i := 0; i < len(tokens); {
		cmd := tokens[i]
		i++

		switch cmd {
		case "M", "L":
			if i+1 >= len(tokens) {
				break
			}

			xx, errX := strconv.ParseFloat(tokens[i], svgFloat64)
			yy, errY := strconv.ParseFloat(tokens[i+1], svgFloat64)
			i += 2

			if errX == nil && errY == nil {
				pts = append(pts, [2]float64{xx, yy})
			}

		case "Z", "z":
			// close path — no new vertex needed
		}
	}

	return pts
}

// applySVGTransform applies "translate(tx,ty) scale(sc)" to each point.
// SVG processes transforms left-to-right, so the effective formula is
// p' = (p * sc) + (tx, ty).
func applySVGTransform(pts [][2]float64, tx, ty, sc float64) [][2]float64 {
	out := make([][2]float64, len(pts))

	for i, pt := range pts {
		out[i] = [2]float64{pt[0]*sc + tx, pt[1]*sc + ty}
	}

	return out
}

// fillPolygon draws a filled polygon onto img using the scanline algorithm.
// pts are SVG viewBox coordinates; scale maps them to pixel space.
func fillPolygon(img *image.NRGBA, pts [][2]float64, scale float64, col color.NRGBA) {
	if len(pts) < svgMinPts {
		return
	}

	bounds := img.Bounds()
	scaled := scalePoints(pts, scale)
	yLo, yHi := polygonYRange(scaled, bounds)

	for yy := yLo; yy <= yHi; yy++ {
		xs := scanlineXs(scaled, float64(yy)+iconHalfPixel)
		slices.Sort(xs)

		for ki := 0; ki+1 < len(xs); ki += 2 {
			xLo := max(int(math.Round(xs[ki])), bounds.Min.X)
			xHi := min(int(math.Round(xs[ki+1])), bounds.Max.X)

			for xx := xLo; xx < xHi; xx++ {
				img.SetNRGBA(xx, yy, col)
			}
		}
	}
}

func scalePoints(pts [][2]float64, scale float64) [][2]float64 {
	out := make([][2]float64, len(pts))

	for i, pt := range pts {
		out[i] = [2]float64{pt[0] * scale, pt[1] * scale}
	}

	return out
}

func polygonYRange(pts [][2]float64, bounds image.Rectangle) (yLo, yHi int) {
	yMin, yMax := pts[0][1], pts[0][1]

	for _, pt := range pts[1:] {
		yMin = min(yMin, pt[1])
		yMax = max(yMax, pt[1])
	}

	return max(int(math.Floor(yMin)), bounds.Min.Y),
		min(int(math.Ceil(yMax)), bounds.Max.Y-1)
}

// scanlineXs returns the X intersections of the polygon edges with the
// horizontal scanline at y=yf.
func scanlineXs(pts [][2]float64, yf float64) []float64 {
	n := len(pts)

	var xs []float64

	for i := range n {
		j := (i + 1) % n
		y1, y2 := pts[i][1], pts[j][1]
		x1, x2 := pts[i][0], pts[j][0]

		if (y1 <= yf && yf < y2) || (y2 <= yf && yf < y1) {
			tt := (yf - y1) / (y2 - y1)
			xs = append(xs, x1+tt*(x2-x1))
		}
	}

	return xs
}
